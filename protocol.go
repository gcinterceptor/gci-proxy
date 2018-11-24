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
	maxFraction     = 0.8  // Percentage of the genSize which defines the upper bound of the the shedding threshold.
	startFraction   = 0.65 // Percentage of the genSize which defines the start of the the shedding threshold.
	entropyFraction = 0.25 // Percentage of the genSize that might increase/decrease after each GC.
	minFraction     = 0.5  // Percentage of the genSize which defines the lower bound of the the shedding threshold.
)

type sheddingThreshold struct {
	r                      *rand.Rand
	max, min, val, entropy uint64
}

func newSheddingThreshold(seed int64, genSize uint64) sheddingThreshold {
	return sheddingThreshold{
		r:       rand.New(rand.NewSource(seed)),
		max:     uint64(maxFraction * float64(genSize)),
		min:     uint64(minFraction * float64(genSize)),
		val:     uint64(startFraction * float64(genSize)),
		entropy: uint64(entropyFraction * float64(genSize)),
	}
}

func (st *sheddingThreshold) value() uint64 {
	return getThreshold(st.r, st.val, st.max, st.min, st.entropy)
}

func getThreshold(r *rand.Rand, val, max, min, entropy uint64) uint64 {
	e := uint64(r.Float64() * float64(entropy))
	c := int64(val) + (randomSign(r) * int64(e))
	var candidate uint64
	if c > 0 {
		candidate = uint64(c)
	} else {
		candidate = min + e
	}
	if candidate > max {
		return max - e
	}
	if candidate < min {
		return min + e
	}
	return candidate
}

func randomSign(r *rand.Rand) int64 {
	n := r.Float64()
	if n < 0.5 {
		return -1
	}
	return 1
}

////////// SAMPLE WINDOW
const (
	// Default sample size should be fairly small, so big requests get checked up quickly.
	defaultSampleSize = uint64(64)
	// Max sample size can not be very big because of peaks. But can not be
	// small because of high throughput systems.
	maxSampleSize = uint64(256)
	// As load changes a lot, the history size does not need to be big.
	sampleHistorySize = 5
)

func newSampleWindow() sampleWindow {
	var h [sampleHistorySize]uint64
	for i := range h {
		h[i] = math.MaxUint64
	}
	return sampleWindow{numReq: defaultSampleSize, history: h}
}

type sampleWindow struct {
	history      [sampleHistorySize]uint64
	historyIndex int
	numReq       uint64
}

func (s *sampleWindow) size() uint64 {
	return atomic.LoadUint64(&s.numReq)
}

func (s *sampleWindow) update(finished uint64) {
	s.history[s.historyIndex] = finished
	s.historyIndex = (s.historyIndex + 1) % len(s.history)
	min := uint64(math.MaxUint64)
	for _, s := range s.history {
		if s < min {
			min = s
		}
	}
	sw := uint64(min)
	if min > 0 {
		if sw > maxSampleSize {
			sw = maxSampleSize
		}
		atomic.StoreUint64(&s.numReq, sw)
	}
}

////////// PENDING WAITER
type pendingWaiter struct {
	arrived, finished uint64
	wg                sync.WaitGroup
}

func (w *pendingWaiter) requestArrived() {
	w.wg.Add(1)
	atomic.AddUint64(&w.arrived, 1)
}

func (w *pendingWaiter) requestFinished() uint64 {
	w.wg.Done()
	return atomic.AddUint64(&w.finished, 1)
}

func (w *pendingWaiter) waitPending() (uint64, uint64) {
	a, f := atomic.LoadUint64(&w.arrived), atomic.LoadUint64(&w.finished)
	w.wg.Wait()
	atomic.StoreUint64(&w.arrived, 0)
	atomic.StoreUint64(&w.finished, 0)
	return a, f
}

func (w *pendingWaiter) requestInfo() (uint64, uint64) {
	return atomic.LoadUint64(&w.arrived), atomic.LoadUint64(&w.finished)
}
