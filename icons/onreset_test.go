package icons

import (
	"slices"
	"testing"
)

func TestOnResetUnsubscribesOnlyItsOwnRegistration(t *testing.T) {
	c := New("hicolor")
	var ran []string
	listen := func(name string) func() { return func() { ran = append(ran, name) } }
	shared := listen("twice")
	stopFirst := c.OnReset(listen("first"))
	stopTwiceA := c.OnReset(shared)
	stopTwiceB := c.OnReset(shared)
	stopLast := c.OnReset(listen("last"))
	stopNil := c.OnReset(nil)

	reset := func(want ...string) {
		t.Helper()
		ran = nil
		c.InvalidateTheme()
		if !slices.Equal(ran, want) {
			t.Errorf("a reset ran %q, want %q", ran, want)
		}
	}
	reset("first", "twice", "twice", "last")
	stopTwiceA()
	reset("first", "twice", "last")
	stopTwiceA()
	stopNil()
	reset("first", "twice", "last")
	stopFirst()
	stopLast()
	reset("twice")
	stopTwiceB()
	reset()
	if n := len(c.resetListeners); n != 0 {
		t.Errorf("the cache still holds %d listeners with every one unsubscribed", n)
	}
}

func TestAListenerMayUnsubscribeItselfDuringAReset(t *testing.T) {
	c := New("hicolor")
	runs := 0
	var stop func()
	stop = c.OnReset(func() {
		runs++
		stop()
	})
	others := 0
	c.OnReset(func() { others++ })
	c.InvalidateTheme()
	c.InvalidateTheme()
	if runs != 1 || others != 2 {
		t.Errorf("the self-unsubscribing listener ran %d times and the other %d, want 1 and 2", runs, others)
	}
}
