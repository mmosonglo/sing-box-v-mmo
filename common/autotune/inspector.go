package autotune

import (
	"log"
	"runtime/debug"
	"time"
)

func init() {
	// Tune Go runtime for low-memory embedded devices (OpenWrt routers).
	// GOGC=50: trigger GC at 50% heap growth instead of default 100%.
	debug.SetGCPercent(50)
	// GOMEMLIMIT=32MB: soft memory limit to keep total Go heap under control.
	debug.SetMemoryLimit(32 * 1024 * 1024)

	go runMemoryReclaimer()
}

// runMemoryReclaimer periodically forces the Go runtime to return unused
// memory back to the operating system. This is critical on OpenWrt routers
// where RAM is scarce (64-256MB).
//
// Unlike the previous implementation, this does NOT call runtime.ReadMemStats()
// which causes a full stop-the-world pause. Instead it simply calls
// debug.FreeOSMemory() which internally runs a GC cycle and then releases
// idle heap spans back to the OS via madvise(MADV_DONTNEED).
func runMemoryReclaimer() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		debug.FreeOSMemory()
	}

	log.Println("[autotune] memory reclaimer stopped")
}
