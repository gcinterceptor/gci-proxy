package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

type transport struct {
	isAvailable int32
	shed        uint64
	target      string
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

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if atomic.LoadInt32(&t.isAvailable) == 1 {
		atomic.AddUint64(&t.shed, 1)
		return shedResponse(request), nil
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		fmt.Printf("Err: %q\n", err)
		return nil, err
	}
	if atomic.LoadInt32(&t.isAvailable) == 0 && response.StatusCode == http.StatusServiceUnavailable {
		atomic.StoreInt32(&t.isAvailable, 1)
		// This call will block until the service is fully available.
		go func() {
			defer func() {
				atomic.StoreInt32(&t.isAvailable, 0)
			}()
			req, err := http.NewRequest("GET", fmt.Sprintf("%s/", t.target), nil)
			if err != nil {
				// TODO: This should kill the server. It is a situation that GCI can not cope right now.
				fmt.Println("Err trying to build gc request: %q", err)
				return
			}
			// Use previous number of shed requests.
			req.Header.Set("GCI", fmt.Sprintf("/%d", atomic.LoadUint64(&t.shed)))
			// Setup for collecting the number of shed requests.
			atomic.StoreUint64(&t.shed, 0)

			// TODO: Stop using http.DefaultClient
			// https://medium.com/@nate510/don-t-use-go-s-default-http-client-4804cb19f779
			// TODO: Also check status codes.
			if _, err := http.DefaultClient.Do(req); err != nil {
				// TODO: This should kill the server. It is a situation that GCI can not cope right now.
				fmt.Println("Err trying to trigger gc: %q", err)
				return
			}
		}()
		return response, nil
	}
	return response, nil
}

type proxy struct {
	target      *url.URL
	proxy       *httputil.ReverseProxy
	latencyFile *os.File
	writer      *bufio.Writer
}

func (p *proxy) handle(w http.ResponseWriter, r *http.Request) {
	s := time.Now()
	p.proxy.ServeHTTP(w, r)
	p.writer.WriteString(fmt.Sprintf("%d\n", time.Since(s).Nanoseconds()/1e6))
}

func newProxy(target string) *proxy {
	url, _ := url.Parse(target)
	p := httputil.NewSingleHostReverseProxy(url)
	p.Transport = &transport{target: target, isAvailable: 0, shed: 0}
	f, err := os.Create("proxy_latency.csv")
	if err != nil {
		panic(err)
	}
	return &proxy{target: url, proxy: p, latencyFile: f, writer: bufio.NewWriter(f)}
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

	// proxy
	proxy := newProxy(*redirectURL)

	signal_chan := make(chan os.Signal, 1)
	signal.Notify(signal_chan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	// server redirection
	go func() {
		http.HandleFunc("/", proxy.handle)
		log.Fatal(http.ListenAndServe(":"+*port, nil))
	}()

	<-signal_chan

	proxy.writer.Flush()
	if err := proxy.latencyFile.Close(); err != nil {
		panic(err)
	}
}
