package router

import (
	"sync"

	"github.com/gagliardetto/solana-go"
)

// MaxPathsToEvaluate limits the number of paths to evaluate for performance
const MaxPathsToEvaluate = 10

// maxBFSCapacity is the maximum number of tokens we support in BFS arena
// This should be larger than the expected number of unique tokens
const maxBFSCapacity = 8192

// bfsGeneration tracks the current BFS generation for O(1) visited checks
// Instead of clearing arrays, we increment generation and compare
type bfsGeneration uint32

// bfsArena is a pooled arena for zero-allocation BFS operations
// Uses generation-based visited tracking to avoid clearing arrays
type bfsArena struct {
	// Generation-based visited tracking (no clearing needed)
	forwardGen  []bfsGeneration // forwardGen[tokenID] == currentGen means visited
	backwardGen []bfsGeneration
	currentGen  bfsGeneration

	// Parent tracking for path reconstruction (indexed by TokenID)
	// -1 means no parent (root), -2 means unvisited
	forwardParent  []int32
	backwardParent []int32

	// Double-buffered frontiers (swap instead of allocate)
	// frontiers[0] is typically forward, frontiers[1] is backward
	frontiers [2][]TokenID
	frontIdx  int // Current frontier index (0 or 1) - used for internal tracking if needed

	// Pre-allocated buffers
	neighborBuf     []TokenID                       // Buffer for new frontier expansion
	neighborIterBuf []TokenID                       // Buffer for getNeighborsInto (separate from pathBuf!)
	pathBuf         []TokenID                       // Buffer for temporary path reconstruction
	resultPaths     [][]TokenID                     // Pre-allocated result paths (slice of slices into pathStorage)
	pathStorage     [MaxPathsToEvaluate][16]TokenID // Fixed storage for paths (max 16 hops)

	capacity int
}

// bfsArenaPool pools BFS arenas to avoid allocation per search
var bfsArenaPool = sync.Pool{
	New: func() interface{} {
		return newBFSArena(maxBFSCapacity)
	},
}

// getBFSArena gets a BFS arena from the pool
func getBFSArena() *bfsArena {
	return bfsArenaPool.Get().(*bfsArena)
}

// putBFSArena returns a BFS arena to the pool
func putBFSArena(a *bfsArena) {
	a.reset()
	bfsArenaPool.Put(a)
}

// newBFSArena creates a new BFS arena with given capacity
func newBFSArena(capacity int) *bfsArena {
	a := &bfsArena{
		forwardGen:     make([]bfsGeneration, capacity),
		backwardGen:    make([]bfsGeneration, capacity),
		forwardParent:  make([]int32, capacity),
		backwardParent: make([]int32, capacity),
		// Frontier sufficient to hold worst-case of one layer
		frontiers:       [2][]TokenID{make([]TokenID, 0, 1024), make([]TokenID, 0, 1024)},
		neighborBuf:     make([]TokenID, 0, 1024), // Buffer for next layer
		neighborIterBuf: make([]TokenID, 0, 128),  // Buffer for neighbors
		pathBuf:         make([]TokenID, 0, 16),
		resultPaths:     make([][]TokenID, 0, MaxPathsToEvaluate),
		pathStorage:     [MaxPathsToEvaluate][16]TokenID{},
		capacity:        capacity,
		currentGen:      1, // Start at 1 because 0 is the default value of arrays (unvisited)
	}
	return a
}

// reset prepares arena for reuse (O(1) operation via generation increment)
func (a *bfsArena) reset() {
	a.currentGen++
	// Handle generation overflow (very rare, ~4 billion searches)
	if a.currentGen == 0 {
		// Clear arrays on overflow
		for i := range a.forwardGen {
			a.forwardGen[i] = 0
			a.backwardGen[i] = 0
		}
		a.currentGen = 1
	}
	a.frontiers[0] = a.frontiers[0][:0]
	a.frontiers[1] = a.frontiers[1][:0]
	a.frontIdx = 0
	a.resultPaths = a.resultPaths[:0]
	a.neighborIterBuf = a.neighborIterBuf[:0]
}

// isForwardVisited checks if token was visited from forward direction (O(1))
func (a *bfsArena) isForwardVisited(id TokenID) bool {
	if int(id) >= a.capacity {
		return false
	}
	return a.forwardGen[id] == a.currentGen
}

// isBackwardVisited checks if token was visited from backward direction (O(1))
func (a *bfsArena) isBackwardVisited(id TokenID) bool {
	if int(id) >= a.capacity {
		return false
	}
	return a.backwardGen[id] == a.currentGen
}

// markForwardVisited marks token as visited from forward direction with parent
func (a *bfsArena) markForwardVisited(id TokenID, parent int32) {
	if int(id) >= a.capacity {
		return
	}
	a.forwardGen[id] = a.currentGen
	a.forwardParent[id] = parent
}

// markBackwardVisited marks token as visited from backward direction with parent
func (a *bfsArena) markBackwardVisited(id TokenID, parent int32) {
	if int(id) >= a.capacity {
		return
	}
	a.backwardGen[id] = a.currentGen
	a.backwardParent[id] = parent
}

// getForwardParent returns the parent token ID for forward path (-1 if root)
func (a *bfsArena) getForwardParent(id TokenID) int32 {
	if int(id) >= a.capacity {
		return -1
	}
	return a.forwardParent[id]
}

// getBackwardParent returns the parent token ID for backward path (-1 if root)
func (a *bfsArena) getBackwardParent(id TokenID) int32 {
	if int(id) >= a.capacity {
		return -1
	}
	return a.backwardParent[id]
}

// reconstructPath builds path from meeting point using parent arrays
// Returns path stored in arena's pathStorage (zero allocation)
func (a *bfsArena) reconstructPath(meetingPoint TokenID, pathIdx int) []TokenID {
	if pathIdx >= MaxPathsToEvaluate {
		return nil
	}

	// Use pre-allocated storage
	storage := &a.pathStorage[pathIdx]
	path := storage[:0]

	// Build forward path (from input to meeting point)
	a.pathBuf = a.pathBuf[:0]
	for id := meetingPoint; ; {
		a.pathBuf = append(a.pathBuf, id)
		parent := a.getForwardParent(id)
		if parent < 0 {
			break
		}
		id = TokenID(parent)
	}

	// Reverse and copy forward path
	for i := len(a.pathBuf) - 1; i >= 0; i-- {
		path = append(path, a.pathBuf[i])
	}

	// Build backward path (from meeting point to output, skip meeting point)
	parent := a.getBackwardParent(meetingPoint)
	for parent >= 0 {
		path = append(path, TokenID(parent))
		parent = a.getBackwardParent(TokenID(parent))
	}

	return path
}

// FindPaths discovers all routes between two tokens up to maxHops using Bidirectional BFS.
// Returns a list of token paths (as PublicKeys) sorted by hop count (shortest first).
// Optimized: Uses pooled BFS arena for zero-allocation hot path.
func (g *Graph) FindPaths(inputMint, outputMint solana.PublicKey, maxHops int) [][]solana.PublicKey {
	snap := g.getSnapshot()
	if snap.registry == nil || snap.adjFast == nil {
		return nil
	}

	// Convert mints to token IDs
	inputID, okInput := snap.registry.GetID(inputMint)
	outputID, okOutput := snap.registry.GetID(outputMint)

	if !okInput || !okOutput {
		return nil
	}

	// Same token - no path needed
	if inputID == outputID {
		return nil
	}

	// Get arena from pool - Caller manages arena lifecycle for Zero-Copy result
	arena := getBFSArena()
	defer putBFSArena(arena)

	// Run bidirectional BFS with pooled arena
	// Arena is passed in so we can control its lifecycle
	tokenPaths := g.bidirectionalBFSArena(arena, inputID, outputID, maxHops, snap)

	if len(tokenPaths) == 0 {
		return nil
	}

	// Convert token ID paths back to PublicKey paths
	// This step involves allocation and copy, as we are returning to the user
	result := make([][]solana.PublicKey, len(tokenPaths))
	for i, tokenPath := range tokenPaths {
		pubkeyPath := make([]solana.PublicKey, len(tokenPath))
		for j, tid := range tokenPath {
			pubkeyPath[j] = snap.registry.GetMint(tid)
		}
		result[i] = pubkeyPath
	}

	return result
}

// bidirectionalBFSArena performs bidirectional BFS using pooled arena (zero allocation in hot path)
// Pre-req: Arena MUST be provided by caller. Result slices point into Arena storage.
func (g *Graph) bidirectionalBFSArena(arena *bfsArena, inputID, outputID TokenID, maxHops int, snap *graphSnapshot) [][]TokenID {
	if maxHops < 1 {
		return nil
	}

	// Check direct 1-hop (Fastest) - bypass arena logic if possible
	if snap.adjFast.get(inputID, outputID) != nil {
		// Return slice of slice pointing to a temporary slice
		// It's safe to return a small heap-allocated slice here for the 1-hop optimization
		// or use the arena if we want to be strictly consistent.
		// For simplicity and speed of 1-hop, we can just return:
		return [][]TokenID{{inputID, outputID}}
	}

	// Init Logic
	arena.markForwardVisited(inputID, -1)
	arena.markBackwardVisited(outputID, -1)

	// Setup Frontiers directly (No append)
	arena.frontiers[0] = arena.frontiers[0][:0]
	arena.frontiers[0] = append(arena.frontiers[0], inputID)

	arena.frontiers[1] = arena.frontiers[1][:0]
	arena.frontiers[1] = append(arena.frontiers[1], outputID)

	pathCount := 0
	maxDepthPerSide := (maxHops + 1) / 2

	for depth := 1; depth <= maxDepthPerSide; depth++ {
		// --- FORWARD EXPANSION ---
		// Mở rộng Forward
		if len(arena.frontiers[0]) > 0 {
			// Buffer tạm cho next layer của Forward
			// We use neighborBuf as the shared buffer for next frontier construction
			nextFwd := arena.neighborBuf[:0]

			for _, tokenID := range arena.frontiers[0] {
				arena.neighborIterBuf = arena.neighborIterBuf[:0]
				neighbors := snap.adjFast.getNeighborsInto(tokenID, arena.neighborIterBuf)

				for _, neighborID := range neighbors {
					if arena.isForwardVisited(neighborID) {
						continue
					}

					arena.markForwardVisited(neighborID, int32(tokenID))
					nextFwd = append(nextFwd, neighborID)

					// [FIX 1] ONLY Check Intersection in Forward Direction
					// This prevents duplicate paths from being recorded in both directions
					if arena.isBackwardVisited(neighborID) {
						path := arena.reconstructPath(neighborID, pathCount)
						if path != nil {
							arena.resultPaths = append(arena.resultPaths, path)
							pathCount++
							if pathCount >= MaxPathsToEvaluate {
								return arena.resultPaths
							}
						}
					}
				}
			}
			// [FIX 2] Capture buffer growth to prevent hidden heap allocations
			// If nextFwd grew beyond neighborBuf's capacity, update the reference
			if cap(nextFwd) > cap(arena.neighborBuf) {
				arena.neighborBuf = nextFwd[:0]
			}

			// Update Frontier 0: Replace with nextFwd
			arena.frontiers[0] = append(arena.frontiers[0][:0], nextFwd...)
		}

		// --- BACKWARD EXPANSION ---
		if len(arena.frontiers[1]) > 0 {
			nextBwd := arena.neighborBuf[:0] // Reuse buffer
			for _, tokenID := range arena.frontiers[1] {
				arena.neighborIterBuf = arena.neighborIterBuf[:0]
				neighbors := snap.adjFast.getNeighborsInto(tokenID, arena.neighborIterBuf)
				for _, neighborID := range neighbors {
					if arena.isBackwardVisited(neighborID) {
						continue
					}

					arena.markBackwardVisited(neighborID, int32(tokenID))
					nextBwd = append(nextBwd, neighborID)

					// RESTORE: Check intersection in backward direction too
					// This is REQUIRED to catch paths that meet at the same depth (odd-length paths)
					if arena.isForwardVisited(neighborID) {
						path := arena.reconstructPath(neighborID, pathCount)
						if path != nil {
							// Zero-alloc deduplication: Check if path already exists
							if !isPathDuplicate(arena.resultPaths, path) {
								arena.resultPaths = append(arena.resultPaths, path)
								pathCount++
								if pathCount >= MaxPathsToEvaluate {
									return arena.resultPaths
								}
							}
						}
					}
				}
			}
			// [FIX 2] Capture buffer growth for backward direction too
			if cap(nextBwd) > cap(arena.neighborBuf) {
				arena.neighborBuf = nextBwd[:0]
			}

			arena.frontiers[1] = append(arena.frontiers[1][:0], nextBwd...)
		}

		// Early exit if no more nodes to explore
		if len(arena.frontiers[0]) == 0 && len(arena.frontiers[1]) == 0 {
			break
		}
	}
	return arena.resultPaths
}

// isPathDuplicate checks if newPath already exists in existingPaths (zero-alloc)
// Scans backwards since recent paths are more likely to be duplicates
func isPathDuplicate(existingPaths [][]TokenID, newPath []TokenID) bool {
	// Scan backwards - recent paths more likely to be duplicates
	for i := len(existingPaths) - 1; i >= 0; i-- {
		p := existingPaths[i]
		if len(p) != len(newPath) {
			continue
		}

		// Compare path contents
		match := true
		for j := 0; j < len(p); j++ {
			if p[j] != newPath[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
