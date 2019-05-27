package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/matryer/is"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestTransportCheckHeap_BackendUnavailable(t *testing.T) {
	is := is.New(t)
	gci := newTransport("", 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	gci.isAvailable = false
	ctx := fasthttp.RequestCtx{}
	gci.RoundTrip(&ctx)
	is.Equal(fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}

func TestCallAgentCH(t *testing.T) {
	is := is.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal(r.Header.Get(gciHeader), checkHeapHeader)
		w.Write([]byte("10"))
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	proxy, _ := newFasthttpServer(func(ctx *fasthttp.RequestCtx) {
		gci.RoundTrip(ctx)
	})
	defer proxy.Shutdown()

	is.Equal(int64(10), gci.callAgentCH())
}

func TestCallAgentGC(t *testing.T) {
	is := is.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal(r.Header.Get(gciHeader), gcHeader)
		w.WriteHeader(fasthttp.StatusOK)
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	proxy, _ := newFasthttpServer(func(ctx *fasthttp.RequestCtx) {
		gci.RoundTrip(ctx)
	})
	defer proxy.Shutdown()

	gci.callAgentGC()
}

func TestProxy(t *testing.T) {
	is := is.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hello" {
			fmt.Fprint(w, "Hello")
		}
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	proxy, client := newFasthttpServer(func(ctx *fasthttp.RequestCtx) {
		gci.RoundTrip(ctx)
	})
	defer proxy.Shutdown()

	res, err := client.Get("http://test/hello")
	is.NoErr(err)
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	is.NoErr(err)
	is.Equal(body, []byte("Hello"))
}

func TestProxy_GCI(t *testing.T) {
	is := is.New(t)
	gciHandler := "gci"
	called := int32(0)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+gciHandler {
			w.Write([]byte("10"))
			atomic.AddInt32(&called, 1)
		}
	}))
	defer target.Close()

	gci := newTransport(target.URL[7:], 1000, true, "", gciHandler) // Need to remove the http:// from the beginning of the URL.
	proxy, client := newFasthttpServer(func(ctx *fasthttp.RequestCtx) {
		gci.RoundTrip(ctx)
	})
	defer proxy.Shutdown()

	for i := int64(0); i < defaultSampleSize+1; i++ { // Need one more call after defaultSampleSize to trigger gci checks.
		callAndDiscard(client, is)
	}

	for atomic.LoadInt32(&called) != 1 {
	}
}

func callAndDiscard(client http.Client, is *is.I) {
	res, err := client.Get("http://test")
	is.NoErr(err)
	defer res.Body.Close()
	io.Copy(ioutil.Discard, res.Body)
}

func callAndReturnBody(client http.Client) ([]byte, error) {
	res, err := client.Get("http://test")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
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
