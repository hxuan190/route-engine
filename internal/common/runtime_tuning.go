package common

import (
	"os"
	"runtime"
	"runtime/debug"

	"github.com/rs/zerolog/log"
)

// Runtime profiles for different server configurations
const (
	// Small server: 2 vCPU, 4GB RAM (test/dev environment)
	SmallServerGOGC     = 500
	SmallServerMemLimit = 2.5 * 1024 * 1024 * 1024 // 2.5GB
	SmallServerMaxProcs = 1                        // Leave 1 core for OS

	// Medium server: 4-8 vCPU, 8-16GB RAM
	MediumServerGOGC     = 800
	MediumServerMemLimit = 8 * 1024 * 1024 * 1024 // 8GB
	MediumServerMaxProcs = 0                      // Auto-detect (NumCPU/2)

	// Large server: 16+ vCPU, 32GB+ RAM (production)
	LargeServerGOGC     = 1000
	LargeServerMemLimit = 16 * 1024 * 1024 * 1024 // 16GB
	LargeServerMaxProcs = 0                       // Auto-detect
)

// detectServerProfile returns appropriate settings based on available resources
func detectServerProfile() (gogc int, memLimit int64, maxProcs int) {
	totalCPU := runtime.NumCPU()
	
	// Detect based on CPU count (RAM detection requires cgo or /proc parsing)
	switch {
	case totalCPU <= 2:
		// Small server (2 vCPU, ~4GB RAM)
		// Reserve 1 core for OS, use conservative memory
		return SmallServerGOGC, int64(SmallServerMemLimit), SmallServerMaxProcs
	case totalCPU <= 8:
		// Medium server (4-8 vCPU, 8-16GB RAM)
		return MediumServerGOGC, int64(MediumServerMemLimit), totalCPU / 2
	default:
		// Large server (16+ vCPU, 32GB+ RAM)
		return LargeServerGOGC, int64(LargeServerMemLimit), totalCPU / 2
	}
}

// InitRuntimeForHFT configures Go runtime for low-latency HFT workloads
// Automatically detects server profile and applies optimal settings
// Override with environment variables: GOGC, GOMAXPROCS, GOMEMLIMIT
func InitRuntimeForHFT() {
	// Auto-detect server profile
	defaultGOGC, defaultMemLimit, defaultMaxProcs := detectServerProfile()

	// GOGC Strategy for Object Pooling:
	// - We use sync.Pool extensively for adjMap, poolsMap, []*domain.Pool slices
	// - Low GOGC (50) = frequent GC = pooled objects get collected before reuse
	// - High GOGC (500-1000) = infrequent GC = pools stay "warm", objects get reused
	// - GOMEMLIMIT acts as safety net to prevent unbounded memory growth
	if gcPercent := os.Getenv("GOGC"); gcPercent == "" {
		debug.SetGCPercent(defaultGOGC)
		log.Info().
			Int("GOGC", defaultGOGC).
			Msg("[runtime] Set GOGC for Object Pooling (keeps sync.Pool warm)")
	}

	// GOMAXPROCS Strategy:
	// - Small servers (2 vCPU): Use 1 core, leave 1 for OS/IRQs
	// - Larger servers: Use physical cores (NumCPU/2) to avoid hyperthread contention
	if maxProcs := os.Getenv("GOMAXPROCS"); maxProcs == "" {
		if defaultMaxProcs == 0 {
			defaultMaxProcs = runtime.NumCPU() / 2
		}
		if defaultMaxProcs < 1 {
			defaultMaxProcs = 1
		}
		runtime.GOMAXPROCS(defaultMaxProcs)
		log.Info().
			Int("GOMAXPROCS", defaultMaxProcs).
			Int("total_cpu", runtime.NumCPU()).
			Msg("[runtime] Set GOMAXPROCS")
	}

	// Memory limit (Go 1.19+)
	// Critical for high GOGC strategy - acts as safety net
	// Small servers: 2.5GB (leave room for OS on 4GB server)
	// Larger servers: 8-16GB based on profile
	if memLimit := os.Getenv("GOMEMLIMIT"); memLimit == "" {
		debug.SetMemoryLimit(defaultMemLimit)
		log.Info().
			Int64("GOMEMLIMIT_bytes", defaultMemLimit).
			Float64("GOMEMLIMIT_GB", float64(defaultMemLimit)/1024/1024/1024).
			Msg("[runtime] Set memory limit (safety net for high GOGC)")
	}

	// Log current runtime settings
	logRuntimeSettings()
}

// logRuntimeSettings logs current Go runtime configuration
func logRuntimeSettings() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	log.Info().
		Int("num_cpu", runtime.NumCPU()).
		Int("gomaxprocs", runtime.GOMAXPROCS(0)).
		Uint64("heap_alloc_mb", memStats.HeapAlloc/1024/1024).
		Uint64("heap_sys_mb", memStats.HeapSys/1024/1024).
		Str("go_version", runtime.Version()).
		Msg("[runtime] Current runtime settings")
}

// LockOSThreadForCriticalPath locks the current goroutine to its OS thread
// Use this for latency-critical goroutines to avoid context switching
func LockOSThreadForCriticalPath() {
	runtime.LockOSThread()
}

// UnlockOSThread unlocks the current goroutine from its OS thread
func UnlockOSThread() {
	runtime.UnlockOSThread()
}

// ForceGC triggers a manual garbage collection
// Use sparingly, typically during initialization or idle periods
func ForceGC() {
	runtime.GC()
}

// DisableGC disables garbage collection entirely
// WARNING: Only use for very short critical sections, memory will grow unbounded
func DisableGC() int {
	return debug.SetGCPercent(-1)
}

// RestoreGC restores GC to a previous percentage
func RestoreGC(percent int) {
	debug.SetGCPercent(percent)
}
