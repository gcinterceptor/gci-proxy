package main

import (
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
)

////////// SHEDDING THRESHOLD
const (
	maxFraction     = 0.7 // Percentage of the genSize which defines the upper bound of the the shedding threshold.
	startFraction   = 0.5 // Percentage of the genSize which defines the start of the the shedding threshold.
	entropyFraction = 0.1 // Percentage of the genSize that might increase/decrease after each GC.
	minFraction     = 0.3 // Percentage of the genSize which defines the lower bound of the the shedding threshold.
)

type sheddingThreshold struct {
	r                      *rand.Rand
	max, min, val, entropy int64
}

func newSheddingThreshold(seed int64, genSize int64) sheddingThreshold {
	return sheddingThreshold{
		r:       rand.New(rand.NewSource(seed)),
		max:     int64(maxFraction * float64(genSize)),
		min:     int64(minFraction * float64(genSize)),
		val:     int64(startFraction * float64(genSize)),
		entropy: int64(entropyFraction * float64(genSize)),
	}
}

func randomSign(r *rand.Rand) int64 {
	n := r.Float64()
	if n < 0.5 {
		return -1
	}
	return 1
}

func (st *sheddingThreshold) nextEntropy() int64 {
	return int64(st.r.Float64() * float64(st.entropy))
}

func (st *sheddingThreshold) NextValue() int64 {
	candidate := atomic.LoadInt64(&st.val) + (randomSign(st.r) * st.nextEntropy())
	if candidate > st.max {
		candidate = st.max - st.nextEntropy()
	} else if candidate < st.min {
		candidate = st.min + st.nextEntropy()
	}
	atomic.StoreInt64(&st.val, candidate)
	return candidate
}

func (st *sheddingThreshold) GC() {
	atomic.AddInt64(&st.val, -st.nextEntropy())
}

////////// SAMPLE WINDOW
const (
	// Default sample size should be fairly small, so big requests get checked up quickly.
	defaultSampleSize = int64(128)
	// Max sample size can not be very big because of peaks. But can not be
	// small because of high throughput systems.
	maxSampleSize = int64(1024)
	// As load changes a lot, the history size does not need to be big.
	sampleHistorySize = 5
)

func newSampleWindow(seed int64) sampleWindow {
	var h [sampleHistorySize]int64
	for i := range h {
		h[i] = math.MaxInt64
	}
	sw := sampleWindow{
		history: h,
		r:       rand.New(rand.NewSource(seed)),
	}
	sw.update(defaultSampleSize) // let entropy do its magic.
	return sw
}

type sampleWindow struct {
	history      [sampleHistorySize]int64
	historyIndex int
	numReq       int64
	r            *rand.Rand
}

func (s *sampleWindow) size() int64 {
	return atomic.LoadInt64(&s.numReq)
}

func (s *sampleWindow) update(finished int64) {
	s.historyIndex = (s.historyIndex + 1) % len(s.history)
	s.history[s.historyIndex] = finished
	candidate := int64(math.MaxInt64)
	for _, val := range s.history {
		if val < candidate {
			candidate = val
		}
	}
	if candidate > maxSampleSize {
		candidate = maxSampleSize
	} else if candidate < defaultSampleSize {
		candidate = candidate + defaultSampleSize
	}
	atomic.StoreInt64(&s.numReq, candidate)
}

////////// PENDING WAITER
type pendingWaiter struct {
	finished int64
	wg       sync.WaitGroup
}

func (w *pendingWaiter) requestArrived() {
	w.wg.Add(1)
}

func (w *pendingWaiter) requestFinished() int64 {
	w.wg.Done()
	return atomic.AddInt64(&w.finished, 1)
}

func (w *pendingWaiter) finishedCount() int64 {
	return atomic.LoadInt64(&w.finished)
}

func (w *pendingWaiter) waitPending() int64 {
	w.wg.Wait()
	return atomic.LoadInt64(&w.finished)
}

func (w *pendingWaiter) reset() {
	w.wg = sync.WaitGroup{}
	atomic.StoreInt64(&w.finished, 0)
}
