package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/julienschmidt/httprouter"
)

type transport struct {
	isAvailable int32
	shed        uint64
	target      string
	inflight    sync.WaitGroup
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
	t.inflight.Add(1)
	defer func() {
		t.inflight.Done()
	}()
	response, err := transportClient.RoundTrip(request)
	if err != nil {
		fmt.Printf("Err: %q\n", err)
		return nil, err
	}

	if atomic.LoadInt32(&t.isAvailable) == 0 && response.StatusCode == http.StatusServiceUnavailable {
		if atomic.CompareAndSwapInt32(&t.isAvailable, 0, 1) {
			go func() {
				defer func() {
					atomic.StoreInt32(&t.isAvailable, 0)
				}()
				t.inflight.Wait()

				req, err := http.NewRequest("GET", fmt.Sprintf("%s/", t.target), nil)
				if err != nil {
					panic(fmt.Sprintf("Err trying to build gc request: %q\n", err))
				}
				req.Header.Set("GCI", fmt.Sprintf("/%d", atomic.LoadUint64(&t.shed)))
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
			}()
		}
	}
	// Requests shed by the JVM.
	if response.StatusCode == http.StatusServiceUnavailable {
		atomic.AddUint64(&t.shed, 1)
	}
	return response, nil
}

type proxy struct {
	target *url.URL
	proxy  *httputil.ReverseProxy
}

func (p *proxy) handle(w http.ResponseWriter, r *http.Request) {
	p.proxy.ServeHTTP(w, r)
}

func newProxy(target string) *proxy {
	url, _ := url.Parse(target)
	p := httputil.NewSingleHostReverseProxy(url)
	p.Transport = &transport{target: target, isAvailable: 0, shed: 0}
	return &proxy{target: url, proxy: p}
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
	flag.Parse()

	proxy := newProxy(*redirectURL)

	router := httprouter.New()
	router.HandlerFunc("GET", "/", proxy.handle)
	log.Fatal(http.ListenAndServe(":"+*port, router))
}
