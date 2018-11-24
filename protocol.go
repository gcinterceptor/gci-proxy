package main

import (
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
)

func shouldGC(arrived, finished, usedBytes, st uint64) bool {
	// Amount of heap that has already been used plus an estimation of the amount
	// of heap to used to process the queue. Here we are using simple mean as
	// an estimation.
	avgHeapUsage := uint64(0)
	if finished > 0 {
		avgHeapUsage = usedBytes / finished
	}
	estimation := avgHeapUsage * (arrived - finished)
	return (usedBytes + estimation) > st
}

////////// SHEDDING THRESHOLD
const (
	maxFraction     = 0.7 // Percentage of the genSize which defines the upper bound of the the shedding threshold.
	startFraction   = 0.6 // Percentage of the genSize which defines the start of the the shedding threshold.
	entropyFraction = 0.1 // Percentage of the genSize that might increase/decrease after each GC.
	minFraction     = 0.5 // Percentage of the genSize which defines the lower bound of the the shedding threshold.
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

func (st *sheddingThreshold) shouldGC(arrived, finished, usedBytes int64) bool {
	// Amount of heap that has already been used plus an estimation of the amount
	// of heap to used to process the queue. Here we are using simple mean as
	// an estimation.
	avgHeapUsage := int64(0)
	if finished > 0 {
		avgHeapUsage = usedBytes / finished
	}
	estimation := avgHeapUsage * (arrived - finished)
	val := atomic.LoadInt64(&st.val)
	shouldGC := (usedBytes + estimation) > val
	entropy := int64(st.r.Float64() * float64(st.entropy))
	var candidate int64
	if shouldGC {
		candidate = val - entropy
	} else {
		candidate = val + entropy
	}
	switch {
	case candidate > st.max:
		atomic.StoreInt64(&st.val, st.max-entropy)
	case candidate < st.min:
		atomic.StoreInt64(&st.val, st.min+entropy)
	default:
		atomic.StoreInt64(&st.val, candidate)
	}
	return shouldGC
}

////////// SAMPLE WINDOW
const (
	// Default sample size should be fairly small, so big requests get checked up quickly.
	defaultSampleSize = int64(64)
	// Max sample size can not be very big because of peaks. But can not be
	// small because of high throughput systems.
	maxSampleSize = int64(256)
	// As load changes a lot, the history size does not need to be big.
	sampleHistorySize = 5
)

func newSampleWindow() sampleWindow {
	var h [sampleHistorySize]int64
	for i := range h {
		h[i] = math.MaxInt64
	}
	return sampleWindow{numReq: defaultSampleSize, history: h}
}

type sampleWindow struct {
	history      [sampleHistorySize]int64
	historyIndex int
	numReq       int64
}

func (s *sampleWindow) size() int64 {
	return atomic.LoadInt64(&s.numReq)
}

func (s *sampleWindow) update(finished int64) {
	s.history[s.historyIndex] = finished
	s.historyIndex = (s.historyIndex + 1) % len(s.history)
	min := int64(math.MaxInt64)
	for _, s := range s.history {
		if s < min {
			min = s
		}
	}
	sw := int64(min)
	if min > 0 {
		if sw > maxSampleSize {
			sw = maxSampleSize
		}
		atomic.StoreInt64(&s.numReq, sw)
	}
}

////////// PENDING WAITER
type pendingWaiter struct {
	arrived, finished int64
	wg                sync.WaitGroup
}

func (w *pendingWaiter) requestArrived() {
	w.wg.Add(1)
	atomic.AddInt64(&w.arrived, 1)
}

func (w *pendingWaiter) requestFinished() int64 {
	w.wg.Done()
	return atomic.AddInt64(&w.finished, 1)
}

func (w *pendingWaiter) waitPending() (int64, int64) {
	w.wg.Wait()
	a, f := atomic.LoadInt64(&w.arrived), atomic.LoadInt64(&w.finished)
	atomic.StoreInt64(&w.arrived, 0)
	atomic.StoreInt64(&w.finished, 0)
	return a, f
}

func (w *pendingWaiter) requestInfo() (int64, int64) {
	return atomic.LoadInt64(&w.arrived), atomic.LoadInt64(&w.finished)
}
