package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

const (
	gciHeader = "gci"
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
	finished       int64
}

func timeMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func (t *transport) RoundTrip(ctx *fasthttp.RequestCtx) {
	if !*disableGCI {
		t.mu.Lock()
		if !t.isAvailable {
			ctx.Error("", fasthttp.StatusServiceUnavailable)
			t.mu.Unlock()
			return
		}
		finished := t.waiter.finishedCount()
		if finished > 0 && finished%t.window.size() == 0 {
			ctx.Error("", fasthttp.StatusServiceUnavailable)
			t.isAvailable = false
			t.mu.Unlock()
			go t.checkHeap()
			return
		}
		t.mu.Unlock()
		t.waiter.requestArrived()
	}
	ctx.Request.Header.Del("Connection")
	if err := t.client.Do(&ctx.Request, &ctx.Response); err != nil {
		panic(fmt.Sprintf("Problem calling proxy:%q", err))
	}
	ctx.Response.Header.Del("Connection")
	if !*disableGCI {
		t.mu.Lock()
		t.waiter.requestFinished()
		t.mu.Unlock()
	}
}

func (t *transport) checkHeap() {
	start := time.Now()
	// Only start when all requests have finished.
	finished := t.waiter.waitPending()
	timeWaitPending := time.Since(start).Nanoseconds() / 1e6

	// Request the agent to check the heap and run the GC if needed.
	req := fasthttp.AcquireRequest()
	req.SetRequestURI(t.protocolTarget)
	req.Header.Set(gciHeader, fmt.Sprintf("%d", t.st.NextValue()))
	resp := fasthttp.AcquireResponse()
	if err := t.protocolClient.Do(req, resp); err != nil {
		panic(fmt.Sprintf("Err trying to check heap:%q\n", err))
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		panic(fmt.Sprintf("Heap check returned status code which is not 200:%v\n", resp.StatusCode()))
	}
	hasGCed := int(resp.Body()[0])
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	// Update the internal structures if the GC has happened and make server available again.
	t.mu.Lock()
	t.finished += finished
	if hasGCed == 1 {
		t.st.GC()
		t.window.update(t.finished)
		t.finished = 0
	}
	t.waiter.reset()
	t.isAvailable = true
	t.mu.Unlock()
	if t.printGC {
		fmt.Printf("%d,%d,%d,%d,%d\n", timeMillis(), hasGCed, finished, timeWaitPending, time.Since(start).Nanoseconds()/1e6)
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
