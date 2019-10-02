package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matryer/is"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestTransport_CallAgentCH(t *testing.T) {
	is := is.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal(r.Header.Get(gciHeader), checkHeapHeader)
		w.Write([]byte("10"))
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	is.Equal(int64(10), gci.callAgentCH())
}

func TestTransport_CallAgentGC(t *testing.T) {
	is := is.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal(r.Header.Get(gciHeader), gcHeader)
		w.WriteHeader(fasthttp.StatusOK)
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	gci.callAgentGC()
}

func TestTransport_RoundTrip(t *testing.T) {
	t.Run("ServiceUnavailable", func(t *testing.T) {
		is := is.New(t)
		gci := newTransport("", 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
		gci.isAvailable = false
		ctx := fasthttp.RequestCtx{}
		gci.RoundTrip(&ctx)
		is.Equal(fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
	})

	t.Run("Proxy", func(t *testing.T) {
		is := is.New(t)
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/hello" {
				fmt.Fprint(w, "Hello")
			}
		}))
		defer target.Close()

		ctx := fasthttp.RequestCtx{
			Request:  fasthttp.Request{},
			Response: fasthttp.Response{},
		}
		ctx.Request.SetRequestURI("http://test/hello")

		gci := newTransport(target.URL[7:], 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
		gci.RoundTrip(&ctx)
		is.Equal([]byte("Hello"), ctx.Response.Body())
	})
}

func TestTransport_RoundTrip_GCI(t *testing.T) {
	is := is.New(t)
	gciHandler := "gci"
	chCalled := int32(0)
	gcCalled := int32(0)
	helloCalled := int64(0)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hello":
			fmt.Fprint(w, "Hello")
			atomic.AddInt64(&helloCalled, 1)
			fmt.Println("Hello", helloCalled)
		case "/" + gciHandler:
			switch r.Header.Get(gciHeader) {
			case checkHeapHeader:
				w.Write([]byte("10"))
				atomic.AddInt32(&chCalled, 1)
			case gcHeader:
				atomic.AddInt32(&gcCalled, 1)
			}
		}
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 5, true, "", gciHandler) // Need to remove the http:// from the beginning of the URL.
	for i := int64(0); i < defaultSampleSize; i++ {              // Need one more call after defaultSampleSize to trigger gci checks.
		ctx := fasthttp.RequestCtx{
			Request:  fasthttp.Request{},
			Response: fasthttp.Response{},
		}
		ctx.Request.SetRequestURI("http://test/hello")
		gci.RoundTrip(&ctx)
	}
	for atomic.LoadInt64(&helloCalled) != defaultSampleSize {
		time.Sleep(10 * time.Millisecond)
	}
	for atomic.LoadInt32(&chCalled) != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	for atomic.LoadInt32(&gcCalled) != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	// Double checking if nothing has been corruped and the request reaches the target.
	helloCalled = 0
	ctx := fasthttp.RequestCtx{
		Request:  fasthttp.Request{},
		Response: fasthttp.Response{},
	}
	ctx.Request.SetRequestURI("http://test/hello")
	gci.RoundTrip(&ctx)
	for atomic.LoadInt64(&helloCalled) != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	is.Equal([]byte("Hello"), ctx.Response.Body())
}

// serve serves http request using provided fasthttp handler
func newFasthttpServer(handler fasthttp.RequestHandler) (*fasthttp.Server, http.Client) {
	ln := fasthttputil.NewInmemoryListener()
	s := &fasthttp.Server{
		Handler:          handler,
		DisableKeepalive: true,
	}
	go func() {
		if err := s.Serve(ln); err != nil {
			panic(fmt.Errorf("failed to serve: %v", err))
		}
	}()
	return s, http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return ln.Dial()
			},
		},
	}
}