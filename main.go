// +build !fast

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"go4.org/strutil"
)

var numCPU = runtime.NumCPU()

var transportClient = &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	MaxIdleConns:        numCPU,
	MaxIdleConnsPerHost: numCPU,
	IdleConnTimeout:     5 * time.Minute,
}

var client = &http.Client{
	Timeout:   5 * time.Minute,
	Transport: transportClient,
}

func main() {
	const (
		defaultPort        = "3000"
		defaultPortUsage   = "default server port, '3000'"
		defaultTarget      = "http://127.0.0.1:8080"
		defaultTargetUsage = "default redirect url, 'http://127.0.0.1:8080'"
	)
	fmt.Println("BOO")

	// flags
	port := flag.String("port", defaultPort, defaultPortUsage)
	redirectURL := flag.String("url", defaultTarget, defaultTargetUsage)
	yGen := flag.Uint64("ygen", 0, "Young generation size, in bytes.")
	tGen := flag.Uint64("tgen", 0, "Tenured generation size, in bytes.")
	printGC := flag.Bool("print_gc", true, "Whether to print gc information.")
	flag.Parse()

	if *yGen == 0 || *tGen == 0 {
		log.Fatalf("Neither ygen nor tgen can be 0. ygen:%d tgen:%d", *yGen, *tGen)
	}
	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		Handler:      newProxy(*redirectURL, *yGen, *tGen, *printGC),
		Addr:         ":" + *port,
	}
	log.Fatal(srv.ListenAndServe())
}

const (
	gciHeader       = "gci"
	heapCheckHeader = "ch"
)

var genSeparator = []byte{124} // character "|"

type generation string

func (g generation) string() string {
	return string(g)
}

const (
	gen1 generation = "gen1"
	gen2 generation = "gen2"
)

type transport struct {
	isAvailable     int32
	target          string
	waiter          pendingWaiter
	window          sampleWindow
	stGen1          sheddingThreshold
	stGen2          sheddingThreshold
	printGC         bool
	heapCheckBuffer *bytes.Buffer
}

var shedResponse = &http.Response{
	Status:        http.StatusText(http.StatusServiceUnavailable),
	StatusCode:    http.StatusServiceUnavailable,
	Body:          http.NoBody,
	ContentLength: 0,
	Header:        make(http.Header, 0),
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if atomic.LoadInt32(&t.isAvailable) == 1 {
		return shedResponse, nil
	}
	t.waiter.requestArrived()
	resp, err := transportClient.RoundTrip(request)
	finished := t.waiter.requestFinished()
	if finished%t.window.size() == 0 { // Is it time to check the heap?
		go t.checkHeap()
	}
	return resp, err
}

func (t *transport) checkHeap() {
	req, err := http.NewRequest("GET", t.target, http.NoBody)
	if err != nil {
		panic(fmt.Sprintf("Err trying to build heap check request: %q\n", err))
	}
	req.Header.Set(gciHeader, heapCheckHeader)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
	end := time.Now()
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("Heap check returned status code which is no OK:%v\n", resp.StatusCode))
	}
	t.heapCheckBuffer.Reset()
	_, err = t.heapCheckBuffer.ReadFrom(resp.Body)
	if err != nil {
		panic(fmt.Sprintf("Could not read check heap response: %q", err))
	}
	resp.Body.Close()
	hs := bytes.Split(t.heapCheckBuffer.Bytes(), genSeparator)
	arrived, finished := t.waiter.requestInfo()
	if len(hs) > 1 { // If there is more than one generation, lets check the tenured and run the full gc if needed.
		usedGen2, err := strutil.ParseUintBytes(hs[1], 10, 64)
		if err != nil {
			panic(fmt.Sprintf("Could not convert usedGen2 size to number: %q", err))
		}
		if shouldGC(arrived, finished, usedGen2, t.stGen2.value()) {
			if t.printGC {
				fmt.Printf("ch,%d,%v,%v\n", start.Unix(), byteToStringSlice(hs), end.Sub(start).Nanoseconds()/1e6)
			}
			t.gc(gen2)
			return
		}
	}
	usedGen1, err := strutil.ParseUintBytes(hs[0], 10, 64)
	if err != nil {
		panic(fmt.Sprintf("Could not convert usedGen1 size to number: %q", err))
	}
	if shouldGC(arrived, finished, usedGen1, t.stGen1.value()) {
		if t.printGC {
			fmt.Printf("ch,%d,%v,%v\n", start.Unix(), byteToStringSlice(hs), end.Sub(start).Nanoseconds()/1e6)
		}
		t.gc(gen1)
		return
	}
	if t.printGC {
		fmt.Printf("ch,%d,%v,%v\n", start.Unix(), byteToStringSlice(hs), end.Sub(start).Nanoseconds()/1e6)
	}
}

func (t *transport) gc(gen generation) {
	// This wait pending could occur only at GC time. It is here because
	// we don't the heap checking to interfere with the request processing.
	if !atomic.CompareAndSwapInt32(&t.isAvailable, 0, 1) {
		return
	}
	defer func() {
		atomic.StoreInt32(&t.isAvailable, 0)
	}()
	_, finished := t.waiter.waitPending()
	t.window.update(finished)

	req, err := http.NewRequest("GET", t.target, nil)
	if err != nil {
		panic(fmt.Sprintf("Err trying to build gc request: %q\n", err))
	}
	req.Header.Set(gciHeader, gen.string())

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		panic(fmt.Sprintf("Err trying to trigger gc:%q\n", err))
	}
	end := time.Now()
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("GC trigger returned status code which is no OK:%v\n", resp.StatusCode))
	}
	io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()
	if t.printGC {
		fmt.Printf("gc,%d,%s,%v\n", start.Unix(), gen.string(), end.Sub(start).Nanoseconds()/1e6)
	}
}

func byteToStringSlice(iSlice [][]byte) string {
	sSlice := make([]string, len(iSlice))
	for i, v := range iSlice {
		sSlice[i] = string(v)
	}
	return strings.Join(sSlice, ",")
}

func newTransport(target string, yGen, tGen uint64, printGC bool) *transport {
	return &transport{
		target:          target,
		window:          newSampleWindow(),
		stGen1:          newSheddingThreshold(time.Now().UnixNano(), yGen),
		stGen2:          newSheddingThreshold(time.Now().UnixNano(), tGen),
		printGC:         printGC,
		heapCheckBuffer: bytes.NewBuffer(make([]byte, 8)), // enough to store a uint64
	}
}

////////// PROXY

func newProxy(redirURL string, yGen, tGen uint64, printGC bool) http.HandlerFunc {
	target, err := url.Parse(redirURL)
	if err != nil {
		log.Fatalf("couldn't parse url:%q", err)
	}
	t := newTransport(target.String(), yGen, tGen, printGC)
	return func(w http.ResponseWriter, req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
		resp, err := t.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		copyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
