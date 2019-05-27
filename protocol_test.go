package main

import (
	"testing"

	"github.com/matryer/is"
)

func TestSheddingThreshold_NextValue(t *testing.T) {
	is := is.New(t)
	s := newSheddingThreshold(1, 1000)

	curr := s.val
	is.True(s.val > s.min) // CurrVal should be bigger than minimum.
	is.True(s.val < s.max) // CurrVal should be bigger than minimum.

	next := s.NextValue()

	is.True(next > s.min) // NextValue should be bigger than minimum.
	is.True(next < s.max) // NextValue should be bigger than minimum.
	is.True(next > curr)  // Next should increase until it reaches the maximum.

	next1 := s.NextValue()
	next2 := s.NextValue()

	is.True(next2 < next1) // Next should decrease when reached the maximum.
}

func TestSheddingThreshold_GC(t *testing.T) {
	is := is.New(t)
	s := newSheddingThreshold(1, 1000)
	next := s.NextValue()
	s.GC()
	v := s.val
	is.True(v < next) // Val should decrease when GC is called.
}
