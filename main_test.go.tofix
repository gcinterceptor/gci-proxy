package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
)

func TestSheddingThreshold(t *testing.T) {
	seed := int64(25)
	genSize := uint64(1024)
	st := newSheddingThreshold(seed, genSize)

	// With this seed, the first sign is negative and the random is 0.8961363003854933.
	start := startFraction * float64(genSize)
	entropy := uint64(entropyFraction * float64(genSize))
	want := uint64(start - float64(entropy)*0.8961363003854933)
	got := st.value()
	if got < want-1 || got > want+1 { // accept rounding errors.
		t.Errorf("young ST - got:%d want:%d [entropy:%d st.yEntropy:%d st.yGen:%d]", got, want, entropy, st.entropy, st.val)
	}

	// With this seed, the next sign is positive and the random is 0.23406078193324906.
	want = uint64(start + float64(entropy)*0.23406078193324906)
	got = st.value()
	if got < want-1 || got > want+1 { // accept rounding errors.
		t.Errorf("tenured ST - got:%d want:%d [entropy:%d st.tEntropy:%d st.tGen:%d]", got, want, entropy, st.entropy, st.val)
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
		if r.URL.Path == "/hello" {
			fmt.Fprint(w, "Hello, client")
		}
	}))
	defer target.Close()

	server := httptest.NewServer(newProxy(target.URL+"/gci", 1024, 1024, false))
	defer server.Close()

	res, err := http.Get(server.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	str, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if string(str) != "Hello, client" {
		t.Errorf("want:\"Hello, client\" got:\"%s\"", string(str))
	}
}

func TestTransport_CheckHeapSize(t *testing.T) {
	data := []struct {
		msg      string
		response string
	}{
		{"onlyGen", "1"},
		{"bothGens", fmt.Sprintf("1%s1", genSeparator)},
	}
	for _, d := range data {
		t.Run(d.msg, func(t *testing.T) {
			var wg sync.WaitGroup
			gotGCIHeapCheck := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(gciHeader) == heapCheckHeader && r.URL.Path == "/" {
					fmt.Fprintf(w, d.response)
					gotGCIHeapCheck = true
				}
				wg.Done()
			}))
			defer target.Close()

			server := httptest.NewServer(newProxy(target.URL, 1024, 1024, false))
			defer server.Close()

			fireReqs(t, &wg, server.URL)

			if !gotGCIHeapCheck {
				t.Errorf("check cheader not set")
			}
		})
	}
}

func TestTransport_GC(t *testing.T) {
	data := []struct {
		msg      string
		response string
		gen      string
	}{
		{"gen1", "1024", gen1.string()},
		{"bothGens_gcGen1", fmt.Sprintf("1024%s1", string(genSeparator)), gen1.string()},
		{"bothGens_gcGen2", fmt.Sprintf("1%s1024", string(genSeparator)), gen2.string()},
	}
	for _, d := range data {
		t.Run(d.msg, func(t *testing.T) {
			var wg sync.WaitGroup
			gcRan := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(gciHeader) == heapCheckHeader && r.URL.Path == "/gci" {
					fmt.Fprintf(w, d.response)
				}
				if r.Header.Get(gciHeader) == d.gen && r.URL.Path == "/gci" {
					gcRan = true
				}
				wg.Done()
			}))
			defer target.Close()

			server := httptest.NewServer(newProxy(target.URL+"/gci", 1024, 1024, false))
			defer server.Close()

			wg.Add(1) // the GC call.
			fireReqs(t, &wg, server.URL)

			if !gcRan {
				t.Errorf("check cheader not set")
			}
		})
	}
}

func fireReqs(t *testing.T, wg *sync.WaitGroup, url string) {
	wg.Add(1) // heap check.
	for i := uint64(0); i < defaultSampleSize; i++ {
		wg.Add(1)
		_, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func BenchmarkProxyHandle_Stateless(b *testing.B) {
	for n := 0; n < b.N; n++ {
		func() {
			var wg sync.WaitGroup
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer wg.Done()
				switch r.Header.Get(gciHeader) {
				case "":
					// Allocating some memory and a consuming a little processing.
					m := make([]byte, 1024)
					for i := range m {
						m[i] = byte(i)
					}
					w.WriteHeader(200)
				case heapCheckHeader:
					var mem runtime.MemStats
					runtime.ReadMemStats(&mem)
					fmt.Fprintf(w, "%d", mem.HeapAlloc)
				default:
					runtime.GC()
				}
			}))
			defer target.Close()
			server := httptest.NewServer(newProxy(target.URL, 10240, 10240, false))
			defer server.Close()

			wg.Add(1) // GC call.
			wg.Add(1) // heap check.
			for i := uint64(0); i < defaultSampleSize; i++ {
				wg.Add(1)
				_, err := http.Get(server.URL)
				if err != nil {
					b.Fatal(err)
				}
			}
			wg.Wait()
		}()
	}
}
