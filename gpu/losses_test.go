package gpu

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLossesRecoverUpToTheLimitWithinTheWindow(t *testing.T) {
	var l Losses
	start := time.Unix(1000, 0).UTC()
	errs := make([]error, LossLimit+1)
	for i := range errs {
		errs[i] = fmt.Errorf("loss %d: %w", i, ErrDeviceLost)
		ok := l.Record(start.Add(time.Duration(i)*time.Second), errs[i])
		if want := i < LossLimit; ok != want {
			t.Fatalf("loss %d: recover %v, want %v", i, ok, want)
		}
	}
	if l.Count() != LossLimit+1 {
		t.Errorf("count %d, want %d", l.Count(), LossLimit+1)
	}
	for _, err := range errs {
		if !errors.Is(l.Err(), err) {
			t.Errorf("joined %v lacks %v", l.Err(), err)
		}
	}
}

func TestLossesOlderThanTheWindowAreForgotten(t *testing.T) {
	var l Losses
	start := time.Unix(1000, 0).UTC()
	for i := range LossLimit {
		if !l.Record(start.Add(time.Duration(i)*time.Second), ErrDeviceLost) {
			t.Fatalf("loss %d refused", i)
		}
	}
	if !l.Record(start.Add(LossWindow), ErrDeviceLost) {
		t.Fatal("a loss a window after the first counted the first against the limit")
	}
	if l.Count() != LossLimit {
		t.Errorf("count %d, want %d", l.Count(), LossLimit)
	}
	if !l.Record(start.Add(10*LossWindow), ErrDeviceLost) || l.Count() != 1 {
		t.Errorf("after a quiet spell count %d, want 1", l.Count())
	}
}
