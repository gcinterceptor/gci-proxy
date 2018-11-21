// +build fast

package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/valyala/tcplisten"
	"go4.org/strutil"
)

var numCPUs = runtime.GOMAXPROCS(0)

func main() {
	const (
		defaultPort        = "3000"
		defaultPortUsage   = "default server port, '3000'"
		defaultTarget      = "http://127.0.0.1:8080"
		defaultTargetUsage = "default redirect url, 'http://127.0.0.1:8080'"
	)

	// flags
	port := flag.String("port", defaultPort, defaultPortUsage)
	redirectURL := flag.String("url", defaultTarget, defaultTargetUsage)
	yGen := flag.Uint64("ygen", 0, "Young generation size, in bytes.")
	tGen := flag.Uint64("tgen", 0, "Tenured generation size, in bytes.")
	printGC := flag.Bool("print_gc", true, "Whether to print gc information.")
	gciCmdPath := flag.String("gci_cmd_path", "", "URl path to be appended to the target to send GCI commands.")
	flag.Parse()

	if *yGen == 0 || *tGen == 0 {
		log.Fatalf("Neither ygen nor tgen can be 0. ygen:%d tgen:%d", *yGen, *tGen)
	}
	cfg := tcplisten.Config{
		ReusePort: true,
	}
	listenAddr := ":" + *port
	ln, err := cfg.NewListener("tcp4", listenAddr)
	if err != nil {
		log.Fatalf("cannot listen to %q: %s", listenAddr, err)
	}
	server := &fasthttp.Server{
		Handler:           newProxy(*redirectURL, *yGen, *tGen, *printGC, *gciCmdPath),
		LogAllErrors:      false,
		ReduceMemoryUsage: true,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		ReadBufferSize:    10 * 1024,
	}
	if err := server.Serve(ln); err != nil {
		log.Fatalf("error in fasthttp server: %s", err)
	}
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
	client          *fasthttp.HostClient
	protocolTarget  string
	waiter          pendingWaiter
	window          sampleWindow
	stGen1          sheddingThreshold
	stGen2          sheddingThreshold
	printGC         bool
	heapCheckBuffer *bytes.Buffer
}

func (t *transport) RoundTrip(ctx *fasthttp.RequestCtx) {
	req := &ctx.Request
	resp := &ctx.Response
	if atomic.LoadInt32(&t.isAvailable) == 1 {
		resp.Header.SetContentLength(0)
		resp.ResetBody()
		ctx.SetStatusCode(http.StatusServiceUnavailable)
		return
	}
	t.waiter.requestArrived()

	err := t.client.Do(req, resp)
	finished := t.waiter.requestFinished()
	if err != nil {
		return
	}
	if finished%t.window.size() == 0 { // Is it time to check the heap?
		go t.checkHeap()
	}
}

func (t *transport) checkHeap() {
	req := fasthttp.AcquireRequest()
	req.SetRequestURI(t.protocolTarget)
	req.Header.Add(gciHeader, heapCheckHeader)

	resp := fasthttp.AcquireResponse()
	err := t.client.Do(req, resp)
	if err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
	if resp.StatusCode() != http.StatusOK {
		panic(fmt.Sprintf("Heap check returned status code which is no OK:%v\n", resp.StatusCode))
	}
	hs := bytes.Split(resp.Body(), genSeparator)
	arrived, finished := t.waiter.requestInfo()
	if len(hs) > 1 { // If there is more than one generation, lets check the tenured and run the full gc if needed.
		usedGen2, err := strutil.ParseUintBytes(hs[1], 10, 64)
		if err != nil {
			panic(fmt.Sprintf("Could not convert usedGen2 size to number: %q", err))
		}
		if shouldGC(arrived, finished, usedGen2, t.stGen2.value()) {
			t.gc(gen2)
			return
		}
	}
	usedGen1, err := strutil.ParseUintBytes(hs[0], 10, 64)
	if err != nil {
		panic(fmt.Sprintf("Could not convert usedGen1 size to number: %q", err))
	}
	if shouldGC(arrived, finished, usedGen1, t.stGen1.value()) {
		t.gc(gen1)
		return
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

	req := fasthttp.AcquireRequest()
	req.SetRequestURI(t.protocolTarget)
	req.Header.Add(gciHeader, gen.string())

	resp := fasthttp.AcquireResponse()
	start := time.Now()
	err := t.client.Do(req, resp)
	if err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
	end := time.Now()
	if resp.StatusCode() != http.StatusOK {
		panic(fmt.Sprintf("GC trigger returned status code which is no OK:%v\n", resp.StatusCode))
	}
	if t.printGC {
		fmt.Printf("%d,%s,%v\n", start.Unix(), gen.string(), end.Sub(start).Nanoseconds()/1e6)
	}
}

func newTransport(target string, yGen, tGen uint64, printGC bool, gciCmdPath string) *transport {
	client := &fasthttp.HostClient{
		Addr:         target,
		Dial:         fasthttp.Dial,
		MaxConns:     numCPUs,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
	}

	return &transport{
		client:          client,
		protocolTarget:  fmt.Sprintf("http://%s/%s", target, gciCmdPath),
		window:          newSampleWindow(),
		stGen1:          newSheddingThreshold(time.Now().UnixNano(), yGen),
		stGen2:          newSheddingThreshold(time.Now().UnixNano(), tGen),
		printGC:         printGC,
		heapCheckBuffer: bytes.NewBuffer(make([]byte, 8)), // enough to store a uint64
	}
}

////////// PROXY

func newProxy(redirURL string, yGen, tGen uint64, printGC bool, gciCmd string) func(*fasthttp.RequestCtx) {
	target, err := url.Parse(redirURL)
	if err != nil {
		log.Fatalf("couldn't parse url:%q", err)
	}
	t := newTransport(target.String(), yGen, tGen, printGC, gciCmd)
	return t.RoundTrip
}
