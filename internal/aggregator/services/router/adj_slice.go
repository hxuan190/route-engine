package router

import (
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
)

// poolEdge holds pools for a token pair edge
type poolEdge struct {
	pools []*domain.Pool
}

// adjSlice is a 2D slice for O(1) adjacency lookup using token IDs
// Usage: adjSlice[fromTokenID][toTokenID] -> poolEdge
type adjSlice struct {
	edges    [][]poolEdge // 2D array: [fromID][toID]
	capacity int          // Current capacity (number of tokens)
}

// newAdjSlice creates a new adjacency slice with initial capacity
func newAdjSlice(initialCapacity int) *adjSlice {
	if initialCapacity < 64 {
		initialCapacity = 64
	}
	edges := make([][]poolEdge, initialCapacity)
	for i := range edges {
		edges[i] = make([]poolEdge, initialCapacity)
	}
	return &adjSlice{
		edges:    edges,
		capacity: initialCapacity,
	}
}

// ensureCapacity grows the slice if needed
func (a *adjSlice) ensureCapacity(tokenID TokenID) {
	needed := int(tokenID) + 1
	if needed <= a.capacity {
		return
	}

	// Grow by 2x or to needed size, whichever is larger
	newCap := a.capacity * 2
	if newCap < needed {
		newCap = needed
	}

	// Create new 2D slice
	newEdges := make([][]poolEdge, newCap)
	for i := range newEdges {
		newEdges[i] = make([]poolEdge, newCap)
	}

	// Copy existing data
	for i := 0; i < a.capacity; i++ {
		for j := 0; j < a.capacity; j++ {
			newEdges[i][j] = a.edges[i][j]
		}
	}

	a.edges = newEdges
	a.capacity = newCap
}

// set sets pools for a token pair
func (a *adjSlice) set(from, to TokenID, pools []*domain.Pool) {
	a.ensureCapacity(from)
	a.ensureCapacity(to)
	a.edges[from][to] = poolEdge{pools: pools}
}

// get returns pools for a token pair
func (a *adjSlice) get(from, to TokenID) []*domain.Pool {
	if int(from) >= a.capacity || int(to) >= a.capacity {
		return nil
	}
	return a.edges[from][to].pools
}

// append adds a pool to a token pair edge
func (a *adjSlice) append(from, to TokenID, pool *domain.Pool) {
	a.ensureCapacity(from)
	a.ensureCapacity(to)
	a.edges[from][to].pools = append(a.edges[from][to].pools, pool)
}

// remove removes a pool from a token pair edge by address
func (a *adjSlice) remove(from, to TokenID, poolAddr [32]byte) {
	if int(from) >= a.capacity || int(to) >= a.capacity {
		return
	}
	pools := a.edges[from][to].pools
	for i, p := range pools {
		if p.Address == poolAddr {
			// Remove by swapping with last element
			pools[i] = pools[len(pools)-1]
			a.edges[from][to].pools = pools[:len(pools)-1]
			return
		}
	}
}

// clear removes all pools for a token pair
func (a *adjSlice) clear(from, to TokenID) {
	if int(from) >= a.capacity || int(to) >= a.capacity {
		return
	}
	a.edges[from][to].pools = nil
}

// clearAll clears all edges
func (a *adjSlice) clearAll() {
	for i := range a.edges {
		for j := range a.edges[i] {
			a.edges[i][j].pools = nil
		}
	}
}

// iterate calls fn for each non-empty edge
func (a *adjSlice) iterate(fn func(from, to TokenID, pools []*domain.Pool)) {
	for i := 0; i < a.capacity; i++ {
		for j := 0; j < a.capacity; j++ {
			if len(a.edges[i][j].pools) > 0 {
				fn(TokenID(i), TokenID(j), a.edges[i][j].pools)
			}
		}
	}
}

// getNeighbors returns all neighbors (token IDs with pools) for a token
// Note: This allocates a new slice. For hot paths, use getNeighborsInto instead.
func (a *adjSlice) getNeighbors(from TokenID) []TokenID {
	if int(from) >= a.capacity {
		return nil
	}
	var neighbors []TokenID
	for j := 0; j < a.capacity; j++ {
		if len(a.edges[from][j].pools) > 0 {
			neighbors = append(neighbors, TokenID(j))
		}
	}
	return neighbors
}

// getNeighborsInto appends neighbors to the provided buffer (zero allocation)
// Returns the buffer with neighbors appended. The buffer should be pre-cleared by caller.
// This is the hot-path version that avoids allocation.
func (a *adjSlice) getNeighborsInto(from TokenID, buf []TokenID) []TokenID {
	if int(from) >= a.capacity {
		return buf
	}
	for j := 0; j < a.capacity; j++ {
		if len(a.edges[from][j].pools) > 0 {
			buf = append(buf, TokenID(j))
		}
	}
	return buf
}
