package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matryer/is"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

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
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+gciHandler {
			fmt.Fprintf(w, "%c", byte(1))
			called = true
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
	for !called {
	}
}

func callAndDiscard(client http.Client, is *is.I) {
	res, err := client.Get("http://test")
	is.NoErr(err)
	defer res.Body.Close()
	io.Copy(ioutil.Discard, res.Body)
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
