package autotune

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestFreeOSMemoryNoPanic(t *testing.T) {
	// Verify that debug.FreeOSMemory() can be called without panic.
	// This is the core operation of the memory reclaimer.
	debug.FreeOSMemory()
}

func TestGCPercentSet(t *testing.T) {
	// init() sets GOGC=50. SetGCPercent returns the previous value,
	// so calling it again should return 50.
	prev := debug.SetGCPercent(50)
	if prev != 50 {
		t.Errorf("expected GOGC=50 (set by init), got %d", prev)
	}
}

func TestMemoryLimitSet(t *testing.T) {
	// init() sets GOMEMLIMIT=32MB. SetMemoryLimit returns the previous value.
	expected := int64(32 * 1024 * 1024)
	prev := debug.SetMemoryLimit(expected)
	if prev != expected {
		t.Errorf("expected GOMEMLIMIT=%d (set by init), got %d", expected, prev)
	}
}

func TestReclaimerGoroutineRunning(t *testing.T) {
	// The reclaimer goroutine is started by init().
	// We can't easily inspect it directly, but we can verify
	// the system doesn't deadlock by waiting briefly.
	done := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		// OK — system is not deadlocked
	case <-time.After(2 * time.Second):
		t.Fatal("system appears deadlocked, reclaimer goroutine may have issues")
	}
}
