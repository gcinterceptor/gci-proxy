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
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
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

	// flags
	port := flag.String("port", defaultPort, defaultPortUsage)
	redirectURL := flag.String("target", defaultTarget, defaultTargetUsage)
	yGen := flag.Int64("ygen", 0, "Young generation size, in bytes.")
	tGen := flag.Int64("tgen", 0, "Tenured generation size, in bytes.")
	printGC := flag.Bool("print_gc", true, "Whether to print gc information.")
	gciTarget := flag.String("gci_target", defaultTarget, defaultTargetUsage)
	gciCmdPath := flag.String("gci_path", "", "URl path to be appended to the target to send GCI commands.")
	flag.Parse()

	if *yGen == 0 || *tGen == 0 {
		log.Fatalf("Neither ygen nor tgen can be 0. ygen:%d tgen:%d", *yGen, *tGen)
	}
	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		Handler:      newProxy(*redirectURL, *yGen, *tGen, *printGC, *gciTarget, *gciCmdPath),
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
	protocolTarget  string
	checkHeapChan   chan int64
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
	if t.waiter.requestFinished()%t.window.size() == 0 { // Is it time to check the heap?
		atomic.StoreInt32(&t.isAvailable, 1)
	}
	return resp, err
}

func (t *transport) heapChecker() {
	for finished := range t.checkHeapChan {
		t.checkHeap(finished)
	}
}

func (t *transport) checkHeap(finished int64) {
	defer func() {
		atomic.StoreInt32(&t.isAvailable, 0)
	}()

	req, err := http.NewRequest("GET", t.protocolTarget, http.NoBody)
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
		panic(fmt.Sprintf("Heap check returned status code which is not 200:%v\n", resp.StatusCode))
	}
	t.heapCheckBuffer.Reset()
	_, err = t.heapCheckBuffer.ReadFrom(resp.Body)
	if err != nil {
		panic(fmt.Sprintf("Could not read check heap response: %q", err))
	}
	resp.Body.Close()
	hs := bytes.Split(t.heapCheckBuffer.Bytes(), genSeparator)
	if t.printGC {
		fmt.Printf("%d,ch,%d,%s,%d\n", time.Now().Unix(), end.Sub(start).Nanoseconds()/1e6, byteToStringSlice(hs), finished)
	}
	usedGen1, err := strconv.ParseInt(string(hs[0]), 10, 64)
	if err != nil {
		panic(fmt.Sprintf("Could not convert usedGen1 size to number: bytes:%v str:%v", usedGen1, string(usedGen1)))
	}
	if t.stGen1.shouldGC(finished, usedGen1) {
		t.gc(gen1, finished)
		return
	}
	if len(hs) > 1 { // If there is more than one generation, lets check the tenured and run the full gc if needed.
		usedGen2, err := strconv.ParseInt(string(hs[1]), 10, 64)
		if err != nil {
			panic(fmt.Sprintf("Could not convert usedGen2 size to number: bytes:%v str:%v", usedGen2, string(usedGen2)))
		}
		if t.stGen2.shouldGC(finished, usedGen2) {
			t.gc(gen2, finished)
			return
		}
	}
}

func byteToStringSlice(iSlice [][]byte) string {
	sSlice := make([]string, len(iSlice))
	for i, v := range iSlice {
		sSlice[i] = string(v)
	}
	return strings.Join(sSlice, ",")
}

func (t *transport) gc(gen generation, finished int64) {
	t.window.update(finished)
	t.waiter.reset()

	req, err := http.NewRequest("GET", t.protocolTarget, http.NoBody)
	if err != nil {
		panic(fmt.Sprintf("Err trying to build gc request: %q\n", err))
	}
	req.Header.Set(gciHeader, gen.string())

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
	io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()

	end := time.Now()
	if resp.StatusCode != http.StatusNoContent {
		panic(fmt.Sprintf("GC trigger returned status code which is not 204:%v\n", resp.StatusCode))
	}
	if t.printGC {
		fmt.Printf("%d,gc,%s,%v\n", start.Unix(), gen.string(), end.Sub(start).Nanoseconds()/1e6)
	}
}

func newTransport(yGen, tGen int64, printGC bool, gciTarget, gciCmdPath string) *transport {
	return &transport{
		protocolTarget:  fmt.Sprintf("http://%s/%s", gciTarget, gciCmdPath),
		window:          newSampleWindow(),
		stGen1:          newSheddingThreshold(time.Now().UnixNano(), yGen),
		stGen2:          newSheddingThreshold(time.Now().UnixNano(), tGen),
		printGC:         printGC,
		heapCheckBuffer: bytes.NewBuffer(make([]byte, 8)), // enough to store a uint64
		checkHeapChan:   make(chan int64),
	}
}

////////// PROXY

func newProxy(redirURL string, yGen, tGen int64, printGC bool, gciTarget, gciCmd string) http.HandlerFunc {
	t := newTransport(yGen, tGen, printGC, gciTarget, gciCmd)
	go t.heapChecker()
	return func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Scheme == "" {
			req.URL.Scheme = "http"
		}
		req.URL.Host = redirURL
		req.Header.Del("Connection")
		resp, err := t.RoundTrip(req)
		if err != nil {
			panic(fmt.Sprintf("Problem calling proxy:%q", err))
		}
		resp.Header.Del("Connection")
		copyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()
		if atomic.LoadInt32(&t.isAvailable) == 1 {
			// Only check the heap when there is no pending request.
			if a, f := t.waiter.requestInfo(); a == f {
				go func() { t.checkHeapChan <- f }()
			}
		}
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
