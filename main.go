package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"go4.org/strutil"
)

var transportClient = &http.Transport{
	Proxy:              http.ProxyFromEnvironment,
	DisableCompression: true,
	Dial: (&net.Dialer{
		Timeout: 5 * time.Second,
	}).Dial,
	TLSHandshakeTimeout: 5 * time.Second,
}

var client = &http.Client{
	Timeout:   time.Second * 5,
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
	shed            uint64
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
		atomic.AddUint64(&t.shed, 1)
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
	resp, err := client.Do(req)
	if err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
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

func shouldGC(arrived, finished, usedBytes, st uint64) bool {
	// Amount of heap that has already been used plus an estimation of the amount
	// of heap to used to process the queue. Here we are using simple mean as
	// an estimation.
	avgHeapUsage := uint64(0)
	if finished > 0 {
		avgHeapUsage = usedBytes / finished
	}
	estimation := avgHeapUsage * (arrived - finished)
	return (usedBytes + estimation) > st
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
	atomic.StoreUint64(&t.shed, 0)

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
		fmt.Printf("%d,%s,%v\n", start.Unix(), gen.string(), end.Sub(start).Nanoseconds()/1e6)
	}
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

////////// SHEDDING THRESHOLD
const (
	// We currently have room for increase/decrease the entropy three times in a row.
	// This is an important constraint, as the candidate will be bound by min and max fraction.
	maxFraction     = 0.9
	startFraction   = 0.7
	entropyFraction = 0.1
	minFraction     = 0.5
)

type sheddingThreshold struct {
	r                      *rand.Rand
	max, min, val, entropy uint64
}

func newSheddingThreshold(seed int64, genSize uint64) sheddingThreshold {
	return sheddingThreshold{
		r:       rand.New(rand.NewSource(seed)),
		max:     uint64(maxFraction * float64(genSize)),
		min:     uint64(minFraction * float64(genSize)),
		val:     uint64(startFraction * float64(genSize)),
		entropy: uint64(entropyFraction * float64(genSize)),
	}
}

func (st *sheddingThreshold) value() uint64 {
	return getThreshold(st.r, st.val, st.max, st.min, st.entropy)
}

func getThreshold(r *rand.Rand, val, max, min, entropy uint64) uint64 {
	e := uint64(r.Float64() * float64(entropy))
	c := int64(val) + (randomSign(r) * int64(e))
	var candidate uint64
	if c > 0 {
		candidate = uint64(c)
	} else {
		candidate = min + e
	}
	if candidate > max {
		return max - e
	}
	if candidate < min {
		return min + e
	}
	return candidate
}

func randomSign(r *rand.Rand) int64 {
	n := r.Float64()
	if n < 0.5 {
		return -1
	}
	return 1
}

////////// SAMPLE WINDOW
const (
	// Default sample size should be fairly small, so big requests get checked up quickly.
	defaultSampleSize = uint64(32)
	// Max sample size can not be very big because of peaks.
	// The algorithm is fairly conservative, but we never know.
	maxSampleSize = uint64(512)
	// As load changes a lot, the history size does not need to be big.
	sampleHistorySize = 5
)

func newSampleWindow() sampleWindow {
	var h [sampleHistorySize]uint64
	for i := range h {
		h[i] = math.MaxUint64
	}
	return sampleWindow{numReq: defaultSampleSize, history: h}
}

type sampleWindow struct {
	history      [sampleHistorySize]uint64
	historyIndex int
	numReq       uint64
}

func (s *sampleWindow) size() uint64 {
	return atomic.LoadUint64(&s.numReq)
}

func (s *sampleWindow) update(finished uint64) {
	s.history[s.historyIndex] = finished
	s.historyIndex = (s.historyIndex + 1) % len(s.history)
	min := uint64(math.MaxUint64)
	for _, s := range s.history {
		if s < min {
			min = s
		}
	}
	sw := uint64(min)
	if min > 0 {
		if sw > maxSampleSize {
			sw = maxSampleSize
		}
	}
	atomic.StoreUint64(&s.numReq, sw)
}

////////// PENDING WAITER
type pendingWaiter struct {
	arrived, finished uint64
	wg                sync.WaitGroup
}

func (w *pendingWaiter) requestArrived() {
	w.wg.Add(1)
	atomic.AddUint64(&w.arrived, 1)
}

func (w *pendingWaiter) requestFinished() uint64 {
	w.wg.Done()
	return atomic.AddUint64(&w.finished, 1)
}

func (w *pendingWaiter) waitPending() (uint64, uint64) {
	a, f := atomic.LoadUint64(&w.arrived), atomic.LoadUint64(&w.finished)
	w.wg.Wait()
	atomic.StoreUint64(&w.arrived, 0)
	atomic.StoreUint64(&w.finished, 0)
	return a, f
}

func (w *pendingWaiter) requestInfo() (uint64, uint64) {
	return atomic.LoadUint64(&w.arrived), atomic.LoadUint64(&w.finished)
}
