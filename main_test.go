package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
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
