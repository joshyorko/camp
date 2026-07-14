package host

import (
	"testing"
	"time"
)

func TestRealClockReportsTimeAndTicks(t *testing.T) {
	t.Parallel()
	clock := NewClock()
	before := time.Now()
	now := clock.Now()
	after := time.Now()
	if now.Before(before) || now.After(after) {
		t.Fatalf("Now() = %v, outside [%v,%v]", now, before, after)
	}

	ticker := clock.NewTicker(time.Millisecond)
	defer ticker.Stop()
	select {
	case <-ticker.C():
	case <-time.After(time.Second):
		t.Fatal("ticker did not tick")
	}
}
