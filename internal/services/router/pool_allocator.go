package router

import (
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
)

// Pool allocators for reducing GC pressure during snapshot rebuilds

const (
	defaultPoolSliceCap    = 16
	defaultNeighborMapCap  = 64
	defaultAdjMapCap       = 256
	defaultPoolsMapCap     = 1024
)

// poolSlicePool reuses []*domain.Pool slices
var poolSlicePool = sync.Pool{
	New: func() interface{} {
		s := make([]*domain.Pool, 0, defaultPoolSliceCap)
		return &s
	},
}

// neighborMapPool reuses map[solana.PublicKey][]*domain.Pool
var neighborMapPool = sync.Pool{
	New: func() interface{} {
		return make(map[solana.PublicKey][]*domain.Pool, defaultNeighborMapCap)
	},
}

// adjMapPool reuses adjMap
var adjMapPool = sync.Pool{
	New: func() interface{} {
		return make(adjMap, defaultAdjMapCap)
	},
}

// poolsMapPool reuses poolsMap
var poolsMapPool = sync.Pool{
	New: func() interface{} {
		return make(poolsMap, defaultPoolsMapCap)
	},
}

// GetPoolSlice gets a pool slice from the pool
func GetPoolSlice() []*domain.Pool {
	s := poolSlicePool.Get().(*[]*domain.Pool)
	return (*s)[:0]
}

// PutPoolSlice returns a pool slice to the pool
func PutPoolSlice(s []*domain.Pool) {
	if cap(s) > 0 {
		// Clear references to allow GC of pool objects
		for i := range s {
			s[i] = nil
		}
		s = s[:0]
		poolSlicePool.Put(&s)
	}
}

// GetNeighborMap gets a neighbor map from the pool
func GetNeighborMap() map[solana.PublicKey][]*domain.Pool {
	m := neighborMapPool.Get().(map[solana.PublicKey][]*domain.Pool)
	// Clear the map before returning
	for k := range m {
		delete(m, k)
	}
	return m
}

// PutNeighborMap returns a neighbor map to the pool
func PutNeighborMap(m map[solana.PublicKey][]*domain.Pool) {
	if m == nil {
		return
	}
	// Clear references
	for k := range m {
		delete(m, k)
	}
	neighborMapPool.Put(m)
}

// GetAdjMap gets an adjacency map from the pool
func GetAdjMap() adjMap {
	m := adjMapPool.Get().(adjMap)
	// Clear the map before returning
	for k := range m {
		delete(m, k)
	}
	return m
}

// PutAdjMap returns an adjacency map to the pool
func PutAdjMap(m adjMap) {
	if m == nil {
		return
	}
	// Clear references
	for k := range m {
		delete(m, k)
	}
	adjMapPool.Put(m)
}

// GetPoolsMap gets a pools map from the pool
func GetPoolsMap() poolsMap {
	m := poolsMapPool.Get().(poolsMap)
	// Clear the map before returning
	for k := range m {
		delete(m, k)
	}
	return m
}

// PutPoolsMap returns a pools map to the pool
func PutPoolsMap(m poolsMap) {
	if m == nil {
		return
	}
	// Clear references
	for k := range m {
		delete(m, k)
	}
	poolsMapPool.Put(m)
}

// snapshotPool reuses graphSnapshot structs
var snapshotPool = sync.Pool{
	New: func() interface{} {
		return &graphSnapshot{}
	},
}

// GetSnapshot gets a snapshot from the pool
func GetSnapshot() *graphSnapshot {
	return snapshotPool.Get().(*graphSnapshot)
}

// PutSnapshot returns a snapshot to the pool after clearing
func PutSnapshot(s *graphSnapshot) {
	if s == nil {
		return
	}
	// Return nested structures to their pools
	if s.adj != nil {
		for _, neighbors := range s.adj {
			PutNeighborMap(neighbors)
		}
		PutAdjMap(s.adj)
	}
	if s.pools != nil {
		PutPoolsMap(s.pools)
	}
	s.adj = nil
	s.pools = nil
	snapshotPool.Put(s)
}
