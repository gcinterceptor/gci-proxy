package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestProxyHandle(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello, client")
	}))
	defer target.Close()

	p := newProxy(target.URL)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.handle(w, r)
	}))
	defer server.Close()

	res, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	str, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(str) != "Hello, client" {
		t.Errorf("want:\"Hello, client\" got:\"%s\"", string(str))
	}
}

func TestCheckHeapSize(t *testing.T) {
	var wg sync.WaitGroup
	gotGCIHeapCheck := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(gciHeader) == heapCheckHeader {
			fmt.Fprintf(w, "%d", 1)
			gotGCIHeapCheck = true
		}
		wg.Done()
	}))
	defer target.Close()

	p := newProxy(target.URL)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.handle(w, r)
	}))
	defer server.Close()

	wg.Add(1) // heap check.
	for i := uint64(0); i < defaultSampleSize; i++ {
		wg.Add(1)
		_, err := http.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if !gotGCIHeapCheck {
		t.Errorf("check cheader not set")
	}
}

func BenchmarkProxyHandle(b *testing.B) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello, client")
	}))
	defer target.Close()

	p := newProxy(target.URL)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.handle(w, r)
	}))
	defer server.Close()

	b.SetParallelism(10)
	for n := 0; n < b.N; n++ {
		res, err := http.Get(server.URL)
		if err != nil {
			b.Fatal(err)
		}
		res.Body.Close()
		if err != nil {
			b.Fatal(err)
		}
	}
}
