package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSheddingThreshold(t *testing.T) {
	seed := int64(25)
	yGen := int64(1024)
	tGen := int64(2048)
	st := newSheddingThreshold(seed, yGen, tGen)

	// With this seed, the first sign is negative and the random is 0.8961363003854933.
	entropy := int64(entropyFraction * float64(yGen))
	want := int64((startFraction * float64(yGen)) - (float64(entropy) * 0.8961363003854933))
	got := st.young()
	if got < want-1 || got > want+1 { // accept rounding errors.
		t.Errorf("young ST - got:%d want:%d [entropy:%d st.yEntropy:%d st.yGen:%d]", got, want, entropy, st.yEntropy, st.yGen)
	}

	// With this seed, the next sign is positive and the random is 0.23406078193324906.
	entropy = int64(entropyFraction * float64(tGen))
	want = int64((startFraction * float64(tGen)) + (float64(entropy) * 0.23406078193324906))
	got = st.tenured()
	if got < want-1 || got > want+1 { // accept rounding errors.
		t.Errorf("tenured ST - got:%d want:%d [entropy:%d st.tEntropy:%d st.tGen:%d]", got, want, entropy, st.tEntropy, st.tGen)
	}
}

func TestSampleWindow_Update(t *testing.T) {
	sw := newSampleWindow()
	if sw.size() != defaultSampleSize {
		t.Errorf("sw.size() want:%d got:%d", defaultSampleSize, sw.size())
	}
	for i := 0; i < sampleHistorySize; i++ {
		sw.update(uint64(i))
		if sw.size() != 0 {
			t.Errorf("sw.size() want:0 got:%d [index:%d history:%v]", sw.size(), i, sw.history)
		}
	}
	// At this point the 0 is the oldest in the history, the next min is 1.
	sw.update(uint64(3))
	if sw.size() != 1 {
		t.Errorf("sw.size() want:1 got:%d [history:%v]", sw.size(), sw.history)
	}
	// At this point the 1 is the oldest in the history, the next min is 2.
	sw.update(uint64(3))
	if sw.size() != 2 {
		t.Errorf("sw.size() want:2 got:%d [history:%v]", sw.size(), sw.history)
	}
}

func TestProxyHandle(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello, client")
	}))
	defer target.Close()

	p := newProxy(target.URL, 1024, 1024)

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

	p := newProxy(target.URL, 1024, 1024)

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

	p := newProxy(target.URL, 1024, 1024)

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
