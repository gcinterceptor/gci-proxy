package main

import (
	"testing"

	"github.com/matryer/is"
	"github.com/valyala/fasthttp"
)

func TestTransportCheckHeap_BackendUnavailable(t *testing.T) {
	is := is.New(t)
	gci := newTransport("", 1000, true, "", "") // Need to remove the http:// from the beginning of the URL.
	gci.isAvailable = false
	ctx := fasthttp.RequestCtx{}
	gci.RoundTrip(&ctx)
	is.Equal(fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}
