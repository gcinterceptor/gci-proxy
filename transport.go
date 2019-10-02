package main

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

const (
	gciHeader       = "gci"
	checkHeapHeader = "ch"
	gcHeader        = "gc"
)

type transport struct {
	isAvailable    bool
	client         *fasthttp.HostClient
	protocolClient *fasthttp.HostClient
	protocolTarget string
	waiter         pendingWaiter
	window         sampleWindow
	st             sheddingThreshold
	printGC        bool
	mu             sync.Mutex
	checking       bool
}

func timeMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func (t *transport) RoundTrip(ctx *fasthttp.RequestCtx) {
	if !*disableGCI {
		t.mu.Lock()
		if !t.isAvailable {
			t.mu.Unlock()
			ctx.Error("", fasthttp.StatusServiceUnavailable)
			return
		}
		t.waiter.requestArrived()
		t.mu.Unlock()
	}
	ctx.Request.Header.Del("Connection")
	if err := t.client.Do(&ctx.Request, &ctx.Response); err != nil {
		panic(fmt.Sprintf("Problem calling proxy:%q", err))
	}
	ctx.Response.Header.Del("Connection")
	if !*disableGCI {
		if t.waiter.requestFinished()%t.window.size() == 0 {
			// Yes, it is possible to call checkHeapAndGC twice
			// before the service becomes unavailable.
			t.mu.Lock()
			if !t.checking {
				t.checking = true
				go t.checkHeapAndGC()
			}
			t.mu.Unlock()
		}
	}
}

// Request the agent to check the heap.
func (t *transport) callAgentCH() int64 {
	req := fasthttp.AcquireRequest()
	req.SetRequestURI(t.protocolTarget)
	req.Header.Set(gciHeader, checkHeapHeader)
	resp := fasthttp.AcquireResponse()
	if err := t.protocolClient.Do(req, resp); err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		panic(fmt.Sprintf("Heap check returned status code which is not 200:%v\n", resp.StatusCode()))
	}
	usage, err := strconv.ParseInt(string(resp.Body()), 10, 64)
	if err != nil {
		panic(fmt.Sprintf("Heap check returned buffer which could not be converted to int:%q\n", err))
	}
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)
	return usage
}

func (t *transport) callAgentGC() {
	req := fasthttp.AcquireRequest()
	req.SetRequestURI(t.protocolTarget)
	req.Header.Set(gciHeader, "gc")
	resp := fasthttp.AcquireResponse()
	if err := t.protocolClient.Do(req, resp); err != nil {
		panic(fmt.Sprintf("Err trying to GC:%q\n", err))
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		panic(fmt.Sprintf("GC returned status code which is not 200:%v\n", resp.StatusCode()))
	}
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)
}

func (t *transport) checkHeapAndGC() {
	defer func() {
		t.mu.Lock()
		t.checking = false
		t.mu.Unlock()
	}()
	pendingTime, gcTime := int64(0), int64(0)
	finished := t.waiter.finishedCount()
	used := t.callAgentCH()
	st := t.st.NextValue()
	needGC := used > st
	if needGC {
		// Make the microsservice unavailable.
		t.mu.Lock()
		t.isAvailable = false
		t.mu.Unlock()

		// Wait for pending requests.
		s := time.Now()
		finished = t.waiter.waitPending()
		pendingTime = time.Since(s).Nanoseconds() / 1e6

		// Trigger GC.
		s = time.Now()
		t.callAgentGC()
		gcTime = time.Since(s).Nanoseconds() / 1e6

		// Update internal structures.
		t.st.GC()
		t.window.update(finished)
		t.waiter.reset()

		// Make the microsservice available again.
		t.mu.Lock()
		t.isAvailable = true
		t.mu.Unlock()
	}
	if t.printGC {
		fmt.Printf("%d,%t,%d,%d,%d,%d,%d\n", timeMillis(), needGC, finished, used, st, pendingTime, gcTime)
	}
}

func newTransport(target string, yGen int64, printGC bool, gciTarget, gciCmdPath string) *transport {
	if gciTarget == "" {
		gciTarget = target
	}
	return &transport{
		isAvailable: true,
		client: &fasthttp.HostClient{
			Addr:         target,
			Dial:         fasthttp.Dial,
			ReadTimeout:  120 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		protocolClient: &fasthttp.HostClient{
			Addr:         gciTarget,
			Dial:         fasthttp.Dial,
			ReadTimeout:  120 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		protocolTarget: fmt.Sprintf("http://%s/%s", gciTarget, gciCmdPath),
		window:         newSampleWindow(time.Now().UnixNano()),
		st:             newSheddingThreshold(time.Now().UnixNano(), yGen),
		printGC:        printGC,
	}
}