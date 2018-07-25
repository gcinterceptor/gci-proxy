package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/julienschmidt/httprouter"
)

const (
	gciHeader       = "gci"
	heapCheckHeader = "ch"
	genSeparator    = "|"
)

type generation string

func (g generation) string() string {
	return string(g)
}

const (
	youngGen    generation = "gen1"
	tenduredGen generation = "gen2"
)

type transport struct {
	isAvailable    int32
	shed           uint64
	target         string
	inflightWaiter sync.WaitGroup
	numReqArrived  uint64
	numReqFinished uint64
	window         sampleWindow
	st             sheddingThreshold
}

func shedResponse(req *http.Request) *http.Response {
	return &http.Response{
		Status:        http.StatusText(http.StatusServiceUnavailable),
		StatusCode:    http.StatusServiceUnavailable,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Body:          ioutil.NopCloser(bytes.NewReader([]byte{})),
		ContentLength: 0,
		Request:       req,
		Header:        make(http.Header, 0),
	}
}

var transportClient = &http.Transport{
	Dial: (&net.Dialer{
		Timeout: 5 * time.Second,
	}).Dial,
	TLSHandshakeTimeout: 5 * time.Second,
}
var client = &http.Client{
	Timeout:   time.Second * 5,
	Transport: transportClient,
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		buf, err := ioutil.ReadAll(request.Body)
		if err != nil {
			panic(err)
		}
		request.Body = ioutil.NopCloser(bytes.NewBuffer(buf))
	}
	if atomic.LoadInt32(&t.isAvailable) == 1 {
		if request != nil && request.Body != nil {
			request.Body.Close()
		}
		atomic.AddUint64(&t.shed, 1)
		return shedResponse(request), nil
	}
	nArrived := atomic.AddUint64(&t.numReqArrived, 1)
	t.inflightWaiter.Add(1)
	defer func() {
		atomic.AddUint64(&t.numReqFinished, 1)
		t.inflightWaiter.Done()
	}()
	response, err := transportClient.RoundTrip(request)
	if err != nil {
		fmt.Printf("Err: %q\n", err)
		return nil, err
	}

	// Is it time to check the heap?
	if nArrived%t.window.size() == 0 {
		go func() {
			req, err := http.NewRequest("GET", fmt.Sprintf("%s/", t.target), nil)
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
			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				panic(fmt.Sprintf("Could not read check heap response: %q", err))
			}
			resp.Body.Close()
			hs := strings.Split(string(b), genSeparator)
			usedGen1, err := strconv.ParseInt(hs[0], 10, 64)
			if err != nil {
				panic(fmt.Sprintf("Could not convert usedGen1 size to number: %q", err))
			}
			if usedGen1 > t.st.young() {
				go t.gc(youngGen)
				return
			}
			if len(hs) > 1 {
				usedGen2, err := strconv.ParseInt(hs[1], 10, 64)
				if err != nil {
					panic(fmt.Sprintf("Could not convert usedGen1 size to number: %q", err))
				}
				if usedGen2 > t.st.tenured() {
					go t.gc(youngGen)
					return
				}
			}
		}()
	}

	// Requests shed by the JVM.
	if response.StatusCode == http.StatusServiceUnavailable {
		atomic.AddUint64(&t.shed, 1)
	}
	return response, nil
}

func (t *transport) gc(gen generation) {
	defer func() {
		atomic.StoreInt32(&t.isAvailable, 0)
	}()
	t.inflightWaiter.Wait()
	t.window.update(atomic.LoadUint64(&t.numReqFinished))

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/", t.target), nil)
	if err != nil {
		panic(fmt.Sprintf("Err trying to build gc request: %q\n", err))
	}
	req.Header.Set(gciHeader, gen.string())
	atomic.StoreUint64(&t.shed, 0)
	resp, err := client.Do(req)
	if err != nil {
		panic(fmt.Sprintf("Err trying to trigger gc:%q\n", err))
	}
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("GC trigger returned status code which is no OK:%v\n", resp.StatusCode))
	}
	if resp != nil {
		ioutil.ReadAll(resp.Body)
		resp.Body.Close()
	}
}

////////// PROXY
type proxy struct {
	target *url.URL
	proxy  *httputil.ReverseProxy
}

func (p *proxy) handle(w http.ResponseWriter, r *http.Request) {
	p.proxy.ServeHTTP(w, r)
}

func newProxy(target string, yGen, tGen int64) *proxy {
	url, _ := url.Parse(target)
	p := httputil.NewSingleHostReverseProxy(url)
	p.Transport = newTransport(target, yGen, tGen)
	return &proxy{target: url, proxy: p}
}

func newTransport(target string, yGen, tGen int64) *transport {
	return &transport{
		target: target,
		window: newSampleWindow(),
		st:     newSheddingThreshold(time.Now().UnixNano(), yGen, tGen),
	}
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
	yGen := flag.Int64("ygen", 0, "Invalid young generation size.")
	tGen := flag.Int64("tgen", 0, "Invalid tenured generation size.")
	flag.Parse()

	proxy := newProxy(*redirectURL, *yGen, *tGen)

	router := httprouter.New()
	router.HandlerFunc("GET", "/", proxy.handle)
	log.Fatal(http.ListenAndServe(":"+*port, router))
}

////////// SHEDDING THRESHOLD
const (
	// We currently have room for increase/decrease the entropy three times in a row.
	maxFraction     = 0.85
	startFraction   = 0.7
	entropyFraction = 0.05
	minFraction     = 0.55
)

type sheddingThreshold struct {
	r                                *rand.Rand
	maxYGen, minYGen, yGen, yEntropy int64
	maxTGen, minTGen, tGen, tEntropy int64
}

func newSheddingThreshold(seed int64, yGen, tGen int64) sheddingThreshold {
	return sheddingThreshold{
		r:        rand.New(rand.NewSource(seed)),
		maxYGen:  int64(maxFraction * float64(yGen)),
		minYGen:  int64(minFraction * float64(yGen)),
		yGen:     int64(startFraction * float64(yGen)),
		yEntropy: int64(entropyFraction * float64(yGen)),
		maxTGen:  int64(maxFraction * float64(tGen)),
		minTGen:  int64(minFraction * float64(tGen)),
		tGen:     int64(startFraction * float64(tGen)),
		tEntropy: int64(entropyFraction * float64(tGen)),
	}
}

func (st *sheddingThreshold) tenured() int64 {
	return getThreshold(st.r, st.tGen, st.maxTGen, st.minTGen, st.tEntropy)
}

func (st *sheddingThreshold) young() int64 {
	return getThreshold(st.r, st.yGen, st.maxYGen, st.minYGen, st.yEntropy)
}

func getThreshold(r *rand.Rand, val, max, min, entropy int64) int64 {
	e := int64(r.Float64() * float64(entropy))
	candidate := val + (randomSign(r) * e)
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
	defaultSampleSize = uint64(64)
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
