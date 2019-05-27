package main

import (
	"testing"

	"github.com/matryer/is"
)

func TestSheddingThreshold_NextValue(t *testing.T) {
	t.Run("Sanity", func(t *testing.T) {
		is := is.New(t)
		s := newSheddingThreshold(1, 1000)

		curr := s.val
		is.True(s.val > s.min) // CurrVal should be bigger than minimum.
		is.True(s.val < s.max) // CurrVal should be bigger than minimum.

		next := s.NextValue()
		is.True(next > s.min) // NextValue should be bigger than minimum.
		is.True(next < s.max) // NextValue should be bigger than minimum.

		is.True(next > curr) // Next should increase until it reaches the maximum.
	})

	t.Run("MustDecreaseIfGreaterThanMaximum", func(t *testing.T) {
		is := is.New(t)
		s := newSheddingThreshold(1, 1000)
		s.max = s.val + 1
		v := s.val
		next := s.NextValue()
		is.True(next < v)
		is.True(next < s.max)
	})

	t.Run("MustIncreaseIfLessThanMinimum", func(t *testing.T) {
		is := is.New(t)
		s := newSheddingThreshold(1, 1000)
		s.min = s.val - 1
		v := s.val
		next := s.NextValue()
		is.True(next > v)
		is.True(next > s.min)
	})
}

func TestSheddingThreshold_GC(t *testing.T) {
	is := is.New(t)
	s := newSheddingThreshold(1, 1000)
	next := s.NextValue()
	s.GC()
	v := s.val
	is.True(v < next) // Val should decrease when GC is called.
}
