package router

import (
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gagliardetto/solana-go"
	container "github.com/thehyperflames/dicontainer-go"
	"github.com/hxuan190/route-engine/internal/domain"
	"github.com/hxuan190/route-engine/internal/metrics"
)

// MaxPoolsPerPair limits pools per token pair for faster routing
const MaxPoolsPerPair = 5

type adjMap = map[solana.PublicKey]map[solana.PublicKey][]*domain.Pool
type poolsMap = map[solana.PublicKey]*domain.Pool

// PoolReadyChecker checks if a pool is ready for trading
type PoolReadyChecker interface {
	IsPoolReady(pool *domain.Pool) bool
}

// graphSnapshot holds immutable snapshot of graph data for lock-free reads
type graphSnapshot struct {
	adj   adjMap
	pools poolsMap

	// Optimized O(1) lookup structures
	adjFast    *adjSlice         // Integer ID based adjacency for O(1) lookup
	registry   *TokenRegistry    // Token ID registry (shared, not owned)
	superEdges *superEdgeMatrix  // Pre-computed SuperEdges for zero-alloc lookup
}

// graphDiff tracks incremental changes for efficient snapshot updates
type graphDiff struct {
	added   []*domain.Pool
	removed []solana.PublicKey
	updated []*domain.Pool
}

const (
	ROUTER_SERVICE = "router.Graph"
)

// Graph represents the token routing graph with lock-free reads
type Graph struct {
	container *container.DIContainer

	mu sync.Mutex // Only for writes

	// Atomic snapshots for lock-free reads
	snapshot atomic.Value // *graphSnapshot

	// Mutable state (protected by mu)
	adj   adjMap
	pools poolsMap

	// Token registry for O(1) integer ID lookups
	tokenRegistry *TokenRegistry

	// Pending changes for incremental updates
	pendingDiff graphDiff

	// Atomic counters
	poolCount      atomic.Int64
	readyPoolCount atomic.Int64

	// Lazy snapshot rebuild
	snapshotDirty atomic.Bool
	stopRefresher chan struct{}

	// Pool readiness checker (optional, falls back to pool.IsReady())
	readyChecker PoolReadyChecker

	// Threshold for incremental vs full rebuild (pool count)
	incrementalThreshold int
}

func (g *Graph) ID() string {
	return ROUTER_SERVICE
}

func (g *Graph) Configure(c container.IContainer) error {
	g.adj = make(adjMap)
	g.pools = make(poolsMap)
	g.tokenRegistry = NewTokenRegistry()
	g.stopRefresher = make(chan struct{})
	g.incrementalThreshold = 50 // Use incremental updates when fewer changes than this

	g.rebuildSnapshot()
	go g.snapshotRefresher()
	return nil
}

func (g *Graph) Start() error {
	return nil
}

func (g *Graph) Stop() error {
	return nil
}

func (g *Graph) Lock() {
	g.mu.Lock()
}

func (g *Graph) Unlock() {
	g.mu.Unlock()
}

func (g *Graph) SetReadyChecker(checker PoolReadyChecker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.readyChecker = checker
	g.rebuildSnapshot()
}

func (g *Graph) isPoolReady(pool *domain.Pool) bool {
	if g.readyChecker != nil {
		return g.readyChecker.IsPoolReady(pool)
	}
	return pool.IsReady()
}

// snapshotRefresher periodically rebuilds the snapshot if dirty
func (g *Graph) snapshotRefresher() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopRefresher:
			return
		case <-ticker.C:
			if g.snapshotDirty.CompareAndSwap(true, false) {
				g.mu.Lock()
				g.applyPendingChanges()
				g.mu.Unlock()
			}
		}
	}
}

// applyPendingChanges applies incremental changes or triggers full rebuild
// Must be called with mu held
func (g *Graph) applyPendingChanges() {
	totalChanges := len(g.pendingDiff.added) + len(g.pendingDiff.removed) + len(g.pendingDiff.updated)

	// Use incremental update for small changes, full rebuild otherwise
	if totalChanges > 0 && totalChanges < g.incrementalThreshold {
		g.applyIncrementalUpdate()
	} else {
		g.rebuildSnapshot()
	}

	// Clear pending diff
	g.pendingDiff = graphDiff{}
}

// applyIncrementalUpdate applies changes to snapshot without full rebuild
// Must be called with mu held
func (g *Graph) applyIncrementalUpdate() {
	metrics.GraphIncrementalUpdates.Inc()

	oldSnap := g.getSnapshot()
	if oldSnap == nil {
		g.rebuildSnapshot()
		return
	}

	// Create new maps by copying old ones (shallow copy for unchanged entries)
	newPools := make(poolsMap, len(oldSnap.pools)+len(g.pendingDiff.added))
	for addr, pool := range oldSnap.pools {
		newPools[addr] = pool
	}

	newAdj := make(adjMap, len(oldSnap.adj))
	for mint, neighbors := range oldSnap.adj {
		newNeighbors := make(map[solana.PublicKey][]*domain.Pool, len(neighbors))
		for mint2, pools := range neighbors {
			// Copy the slice reference (will be replaced if modified)
			newNeighbors[mint2] = pools
		}
		newAdj[mint] = newNeighbors
	}

	readyCount := g.readyPoolCount.Load()

	// Apply removals
	for _, addr := range g.pendingDiff.removed {
		pool, exists := newPools[addr]
		if !exists {
			continue
		}
		if g.isPoolReady(pool) {
			readyCount--
		}
		delete(newPools, addr)
		g.removePoolFromAdj(newAdj, pool)
	}

	// Apply updates (remove old, add updated)
	for _, pool := range g.pendingDiff.updated {
		oldPool, exists := newPools[pool.Address]
		if exists {
			wasReady := g.isPoolReady(oldPool)
			isReady := g.isPoolReady(pool)
			if wasReady && !isReady {
				readyCount--
				g.removePoolFromAdj(newAdj, oldPool)
			} else if !wasReady && isReady {
				readyCount++
				g.addPoolToAdj(newAdj, pool)
			} else if isReady {
				// Update pool reference in adjacency
				g.updatePoolInAdj(newAdj, pool)
			}
		}
		newPools[pool.Address] = pool
	}

	// Apply additions
	for _, pool := range g.pendingDiff.added {
		newPools[pool.Address] = pool
		if g.isPoolReady(pool) {
			readyCount++
			g.addPoolToAdj(newAdj, pool)
		}
	}

	g.snapshot.Store(&graphSnapshot{adj: newAdj, pools: newPools})
	g.poolCount.Store(int64(len(newPools)))
	g.readyPoolCount.Store(readyCount)
	metrics.PoolCount.Set(float64(len(newPools)))
	metrics.ReadyPoolCount.Set(float64(readyCount))
}

// addPoolToAdj adds a pool to adjacency map
func (g *Graph) addPoolToAdj(adj adjMap, pool *domain.Pool) {
	// Add A -> B
	if adj[pool.TokenMintA] == nil {
		adj[pool.TokenMintA] = make(map[solana.PublicKey][]*domain.Pool)
	}
	adj[pool.TokenMintA][pool.TokenMintB] = append(adj[pool.TokenMintA][pool.TokenMintB], pool)

	// Add B -> A
	if adj[pool.TokenMintB] == nil {
		adj[pool.TokenMintB] = make(map[solana.PublicKey][]*domain.Pool)
	}
	adj[pool.TokenMintB][pool.TokenMintA] = append(adj[pool.TokenMintB][pool.TokenMintA], pool)
}

// removePoolFromAdj removes a pool from adjacency map
func (g *Graph) removePoolFromAdj(adj adjMap, pool *domain.Pool) {
	g.removePoolEdge(adj, pool.TokenMintA, pool.TokenMintB, pool.Address)
	g.removePoolEdge(adj, pool.TokenMintB, pool.TokenMintA, pool.Address)
}

// removePoolEdge removes a specific pool edge
func (g *Graph) removePoolEdge(adj adjMap, from, to, poolAddr solana.PublicKey) {
	if neighbors, ok := adj[from]; ok {
		if pools, ok := neighbors[to]; ok {
			newPools := make([]*domain.Pool, 0, len(pools)-1)
			for _, p := range pools {
				if !p.Address.Equals(poolAddr) {
					newPools = append(newPools, p)
				}
			}
			if len(newPools) == 0 {
				delete(neighbors, to)
			} else {
				neighbors[to] = newPools
			}
			if len(neighbors) == 0 {
				delete(adj, from)
			}
		}
	}
}

// updatePoolInAdj updates pool reference in adjacency map
func (g *Graph) updatePoolInAdj(adj adjMap, pool *domain.Pool) {
	updateEdge := func(from, to solana.PublicKey) {
		if neighbors, ok := adj[from]; ok {
			if pools, ok := neighbors[to]; ok {
				for i, p := range pools {
					if p.Address.Equals(pool.Address) {
						pools[i] = pool
						break
					}
				}
			}
		}
	}
	updateEdge(pool.TokenMintA, pool.TokenMintB)
	updateEdge(pool.TokenMintB, pool.TokenMintA)
}

// StopRefresher stops the background snapshot refresher
func (g *Graph) StopRefresher() {
	close(g.stopRefresher)
}

// rebuildSnapshot creates a new immutable snapshot with pre-filtered ready pools
// Must be called with mu held
func (g *Graph) rebuildSnapshot() {
	metrics.GraphSnapshotRebuilds.Inc()

	// Use pooled allocations to reduce GC pressure
	newAdj := GetAdjMap()
	newPools := GetPoolsMap()
	readyCount := int64(0)

	for addr, pool := range g.pools {
		newPools[addr] = pool
		if g.isPoolReady(pool) {
			readyCount++
		}
	}

	// Build optimized O(1) adjacency slice
	// Estimate capacity based on unique tokens
	tokenCount := g.tokenRegistry.Size()
	if tokenCount < 64 {
		tokenCount = 64
	}
	newAdjFast := newAdjSlice(tokenCount)

	// Pre-compute SuperEdges matrix for zero-alloc lookup during quoting
	newSuperEdges := newSuperEdgeMatrix(tokenCount)

	// Build adjacency with only ready pools pre-filtered and sorted by liquidity
	for mintA, neighbors := range g.adj {
		newNeighbors := GetNeighborMap()
		mintAID := g.tokenRegistry.GetOrCreate(mintA)

		for mintB, pools := range neighbors {
			readyPools := GetPoolSlice()
			for _, p := range pools {
				if g.isPoolReady(p) {
					readyPools = append(readyPools, p)
				}
			}
			if len(readyPools) > 0 {
				// Sort by output liquidity (descending) and keep top K
				sortPoolsByOutputLiquidity(readyPools, mintA)
				if len(readyPools) > MaxPoolsPerPair {
					readyPools = readyPools[:MaxPoolsPerPair]
				}
				newNeighbors[mintB] = readyPools

				// Also add to optimized adjSlice for O(1) lookup
				mintBID := g.tokenRegistry.GetOrCreate(mintB)
				newAdjFast.set(mintAID, mintBID, readyPools)

				// Pre-compute immutable SuperEdge for this token pair
				// The pools slice is already sorted and limited, create SuperEdge directly
				se := NewImmutableSuperEdge(mintAID, mintBID, mintA, mintB, readyPools)
				newSuperEdges.set(mintAID, mintBID, se)
			} else {
				PutPoolSlice(readyPools)
			}
		}
		if len(newNeighbors) > 0 {
			newAdj[mintA] = newNeighbors
		} else {
			PutNeighborMap(newNeighbors)
		}
	}

	// Store old snapshot for recycling after new one is active
	oldSnap := g.snapshot.Load()

	g.snapshot.Store(&graphSnapshot{
		adj:        newAdj,
		pools:      newPools,
		adjFast:    newAdjFast,
		registry:   g.tokenRegistry,
		superEdges: newSuperEdges,
	})
	g.poolCount.Store(int64(len(g.pools)))
	g.readyPoolCount.Store(readyCount)
	metrics.PoolCount.Set(float64(len(g.pools)))
	metrics.ReadyPoolCount.Set(float64(readyCount))

	// Recycle old snapshot (safe because new snapshot is now active)
	if oldSnap != nil {
		if old, ok := oldSnap.(*graphSnapshot); ok && old != nil {
			recycleSnapshot(old)
		}
	}
}

// recycleSnapshot returns snapshot allocations to pools
func recycleSnapshot(snap *graphSnapshot) {
	if snap == nil {
		return
	}
	// Return nested structures to their pools
	if snap.adj != nil {
		for _, neighbors := range snap.adj {
			for _, pools := range neighbors {
				PutPoolSlice(pools)
			}
			PutNeighborMap(neighbors)
		}
		PutAdjMap(snap.adj)
	}
	if snap.pools != nil {
		PutPoolsMap(snap.pools)
	}
}

// sortPoolsByOutputLiquidity sorts pools by output reserve (descending)
// inputMint determines which reserve is the output
// Optimized: uses uint64 shadow fields to avoid big.Int comparison overhead
func sortPoolsByOutputLiquidity(pools []*domain.Pool, inputMint solana.PublicKey) {
	if len(pools) <= 1 {
		return
	}
	sort.Slice(pools, func(i, j int) bool {
		liqI := getOutputReserveU64(pools[i], inputMint)
		liqJ := getOutputReserveU64(pools[j], inputMint)
		return liqI > liqJ
	})
}

// getOutputReserveU64 returns the output reserve as uint64 based on input mint
// Uses uint64 shadow fields for zero-allocation comparison
func getOutputReserveU64(pool *domain.Pool, inputMint solana.PublicKey) uint64 {
	if pool.TokenMintA == inputMint {
		return pool.ReserveBU64
	}
	return pool.ReserveAU64
}

// getOutputReserve returns the output reserve based on input mint (big.Int version)
// Kept for backward compatibility
func getOutputReserve(pool *domain.Pool, inputMint solana.PublicKey) *big.Int {
	if pool.TokenMintA == inputMint {
		return pool.ReserveB
	}
	return pool.ReserveA
}

func (g *Graph) getSnapshot() *graphSnapshot {
	return g.snapshot.Load().(*graphSnapshot)
}

// AddPool adds a pool to the graph with lazy snapshot rebuild
func (g *Graph) AddPool(pool *domain.Pool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	isUpdate := g.addPoolLocked(pool)
	if isUpdate {
		g.pendingDiff.updated = append(g.pendingDiff.updated, pool)
	} else {
		g.pendingDiff.added = append(g.pendingDiff.added, pool)
	}
	g.snapshotDirty.Store(true) // Mark dirty for lazy rebuild
}

// addPoolLocked adds pool to mutable state, returns true if it was an update
func (g *Graph) addPoolLocked(pool *domain.Pool) bool {
	if _, exists := g.pools[pool.Address]; exists {
		g.pools[pool.Address] = pool
		return true
	}

	g.pools[pool.Address] = pool

	// Add A -> B edge
	if _, ok := g.adj[pool.TokenMintA]; !ok {
		g.adj[pool.TokenMintA] = make(map[solana.PublicKey][]*domain.Pool)
	}
	g.adj[pool.TokenMintA][pool.TokenMintB] = append(g.adj[pool.TokenMintA][pool.TokenMintB], pool)

	// Add B -> A edge
	if _, ok := g.adj[pool.TokenMintB]; !ok {
		g.adj[pool.TokenMintB] = make(map[solana.PublicKey][]*domain.Pool)
	}
	g.adj[pool.TokenMintB][pool.TokenMintA] = append(g.adj[pool.TokenMintB][pool.TokenMintA], pool)

	return false
}

// AddPoolsBatch adds multiple pools with a single snapshot rebuild
func (g *Graph) AddPoolsBatch(pools []*domain.Pool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, pool := range pools {
		g.addPoolLocked(pool)
	}
	// Clear any pending diff and rebuild immediately for batch operations
	g.pendingDiff = graphDiff{}
	g.snapshotDirty.Store(false)
	g.rebuildSnapshot()
}

// RemovePool removes a pool from the graph with lazy snapshot rebuild
func (g *Graph) RemovePool(poolAddress solana.PublicKey) {
	g.mu.Lock()
	defer g.mu.Unlock()

	pool, exists := g.pools[poolAddress]
	if !exists {
		return
	}

	delete(g.pools, poolAddress)
	g.removeEdge(pool.TokenMintA, pool.TokenMintB, poolAddress)
	g.removeEdge(pool.TokenMintB, pool.TokenMintA, poolAddress)
	g.pendingDiff.removed = append(g.pendingDiff.removed, poolAddress)
	g.snapshotDirty.Store(true) // Mark dirty for lazy rebuild
}

func (g *Graph) removeEdge(from, to, poolAddress solana.PublicKey) {
	if neighbors, ok := g.adj[from]; ok {
		if pools, ok := neighbors[to]; ok {
			newPools := make([]*domain.Pool, 0, len(pools))
			for _, p := range pools {
				if !p.Address.Equals(poolAddress) {
					newPools = append(newPools, p)
				}
			}
			if len(newPools) == 0 {
				delete(neighbors, to)
			} else {
				neighbors[to] = newPools
			}
		}
		if len(neighbors) == 0 {
			delete(g.adj, from)
		}
	}
}

// RefreshSnapshot rebuilds the snapshot - call after modifying pool state
func (g *Graph) RefreshSnapshot() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pendingDiff = graphDiff{}
	g.snapshotDirty.Store(false)
	g.rebuildSnapshot()
}

// MarkDirty marks the snapshot as needing rebuild
func (g *Graph) MarkDirty() {
	g.snapshotDirty.Store(true)
}

// GetPool returns a pool by address (lock-free read from snapshot)
func (g *Graph) GetPool(address solana.PublicKey) *domain.Pool {
	snap := g.getSnapshot()
	return snap.pools[address]
}

// GetPoolMutable returns a pool by address from mutable state (with lock)
// Use this when you need the latest pool data that may not be in the snapshot yet
func (g *Graph) GetPoolMutable(address solana.PublicKey) *domain.Pool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pools[address]
}

// GetPoolByAddress returns a pool by address string (lock-free read)
func (g *Graph) GetPoolByAddress(addressStr string) *domain.Pool {
	pubkey, err := solana.PublicKeyFromBase58(addressStr)
	if err != nil {
		return nil
	}
	snap := g.getSnapshot()
	return snap.pools[pubkey]
}

// GetPoolCount returns total pool count (lock-free)
func (g *Graph) GetPoolCount() int {
	return int(g.poolCount.Load())
}

// GetReadyPoolCount returns ready pool count (lock-free)
func (g *Graph) GetReadyPoolCount() int {
	return int(g.readyPoolCount.Load())
}

// GetAllPools returns all pools (lock-free read)
func (g *Graph) GetAllPools() []*domain.Pool {
	snap := g.getSnapshot()
	pools := make([]*domain.Pool, 0, len(snap.pools))
	for _, p := range snap.pools {
		pools = append(pools, p)
	}
	return pools
}

// GetDirectRoutesForPair returns all ready pools connecting two tokens directly (lock-free read)
// Pools are pre-filtered during snapshot creation
func (g *Graph) GetDirectRoutesForPair(mintA, mintB solana.PublicKey) []*domain.Pool {
	snap := g.getSnapshot()

	// Try O(1) lookup first using integer IDs
	if snap.adjFast != nil && snap.registry != nil {
		idA, okA := snap.registry.GetID(mintA)
		idB, okB := snap.registry.GetID(mintB)
		if okA && okB {
			if pools := snap.adjFast.get(idA, idB); pools != nil {
				return pools
			}
			return nil
		}
	}

	// Fallback to map lookup
	if neighbors, ok := snap.adj[mintA]; ok {
		if pools, ok := neighbors[mintB]; ok {
			return pools // Already filtered for ready pools
		}
	}
	return nil
}

// GetDirectRoutesForPairByID returns pools using token IDs for O(1) lookup
func (g *Graph) GetDirectRoutesForPairByID(idA, idB TokenID) []*domain.Pool {
	snap := g.getSnapshot()
	if snap.adjFast == nil {
		return nil
	}
	return snap.adjFast.get(idA, idB)
}

// GetTokenID returns the token ID for a mint address
func (g *Graph) GetTokenID(mint solana.PublicKey) (TokenID, bool) {
	return g.tokenRegistry.GetID(mint)
}

// GetTokenMint returns the mint address for a token ID
func (g *Graph) GetTokenMint(id TokenID) solana.PublicKey {
	return g.tokenRegistry.GetMint(id)
}

// GetSuperEdge returns a SuperEdge for the token pair, aggregating all pools
// DEPRECATED: Use GetPrecomputedSuperEdge for zero-allocation hot path
func (g *Graph) GetSuperEdge(fromMint, toMint solana.PublicKey) *SuperEdge {
	// Try pre-computed lookup first (zero allocation)
	snap := g.getSnapshot()
	if snap.superEdges != nil && snap.registry != nil {
		fromID, okA := snap.registry.GetID(fromMint)
		toID, okB := snap.registry.GetID(toMint)
		if okA && okB {
			if se := snap.superEdges.get(fromID, toID); se != nil {
				return se
			}
		}
	}

	// Fallback to dynamic construction (allocates)
	pools := g.GetDirectRoutesForPair(fromMint, toMint)
	if len(pools) == 0 {
		return nil
	}

	fromID, _ := g.tokenRegistry.GetID(fromMint)
	toID, _ := g.tokenRegistry.GetID(toMint)

	se := NewSuperEdge(fromID, toID, fromMint, toMint)
	for _, pool := range pools {
		se.AddPool(pool)
	}

	return se
}

// GetSuperEdgeByID returns a SuperEdge using token IDs for O(1) lookup
// DEPRECATED: Use GetPrecomputedSuperEdgeByID for zero-allocation hot path
func (g *Graph) GetSuperEdgeByID(fromID, toID TokenID) *SuperEdge {
	// Try pre-computed lookup first (zero allocation)
	snap := g.getSnapshot()
	if snap.superEdges != nil {
		if se := snap.superEdges.get(fromID, toID); se != nil {
			return se
		}
	}

	// Fallback to dynamic construction (allocates)
	pools := g.GetDirectRoutesForPairByID(fromID, toID)
	if len(pools) == 0 {
		return nil
	}

	fromMint := g.tokenRegistry.GetMint(fromID)
	toMint := g.tokenRegistry.GetMint(toID)

	se := NewSuperEdge(fromID, toID, fromMint, toMint)
	for _, pool := range pools {
		se.AddPool(pool)
	}

	return se
}

// GetPrecomputedSuperEdge returns a pre-computed SuperEdge from the snapshot (zero allocation)
// Returns nil if no SuperEdge exists for the token pair
func (g *Graph) GetPrecomputedSuperEdge(fromMint, toMint solana.PublicKey) *SuperEdge {
	snap := g.getSnapshot()
	if snap.superEdges == nil || snap.registry == nil {
		return nil
	}
	fromID, okA := snap.registry.GetID(fromMint)
	toID, okB := snap.registry.GetID(toMint)
	if !okA || !okB {
		return nil
	}
	return snap.superEdges.get(fromID, toID)
}

// GetPrecomputedSuperEdgeByID returns a pre-computed SuperEdge using token IDs (zero allocation)
func (g *Graph) GetPrecomputedSuperEdgeByID(fromID, toID TokenID) *SuperEdge {
	snap := g.getSnapshot()
	if snap.superEdges == nil {
		return nil
	}
	return snap.superEdges.get(fromID, toID)
}
