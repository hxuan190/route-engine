package router

import (
	"math/big"
	"sort"
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
)

// SuperEdgeSplitThreshold is the minimum amount (in smallest units) to consider splitting
const SuperEdgeSplitThreshold = 1000000 // 1M units

// MaxPoolsPerSuperEdge limits pools to evaluate in a super edge
const MaxPoolsPerSuperEdge = 5

// SuperEdge aggregates multiple liquidity pools between the same token pair
// It presents a unified interface for quoting, automatically optimizing across internal pools
type SuperEdge struct {
	From     TokenID
	To       TokenID
	FromMint solana.PublicKey
	ToMint   solana.PublicKey

	mu       sync.RWMutex
	pools    []*domain.Pool // Sorted by output liquidity (descending)
	bestPool *domain.Pool   // Cached pointer to best single pool

	// immutable indicates this SuperEdge is part of a snapshot and should not be modified
	immutable bool
}

// superEdgePool provides object pooling for SuperEdge to reduce allocations
var superEdgePool = sync.Pool{
	New: func() interface{} {
		return &SuperEdge{
			pools: make([]*domain.Pool, 0, MaxPoolsPerSuperEdge),
		}
	},
}

// AcquireSuperEdge gets a SuperEdge from the pool
func AcquireSuperEdge(from, to TokenID, fromMint, toMint solana.PublicKey) *SuperEdge {
	se := superEdgePool.Get().(*SuperEdge)
	se.From = from
	se.To = to
	se.FromMint = fromMint
	se.ToMint = toMint
	se.pools = se.pools[:0]
	se.bestPool = nil
	se.immutable = false
	return se
}

// ReleaseSuperEdge returns a SuperEdge to the pool
// SAFETY: Only call on SuperEdges obtained from AcquireSuperEdge, never on snapshot SuperEdges
func ReleaseSuperEdge(se *SuperEdge) {
	if se == nil || se.immutable {
		return
	}
	se.pools = se.pools[:0]
	se.bestPool = nil
	superEdgePool.Put(se)
}

// superEdgeMatrix provides O(1) lookup for pre-computed SuperEdges
// Uses sparse storage: only stores edges that exist
type superEdgeMatrix struct {
	// edges[fromID] maps toID -> *SuperEdge
	// Using map of maps for sparse storage (most token pairs don't have pools)
	edges map[TokenID]map[TokenID]*SuperEdge

	// Flat slice for iteration (optional, for debugging/metrics)
	count int
}

// newSuperEdgeMatrix creates a new matrix with estimated capacity
func newSuperEdgeMatrix(estimatedTokens int) *superEdgeMatrix {
	return &superEdgeMatrix{
		edges: make(map[TokenID]map[TokenID]*SuperEdge, estimatedTokens),
	}
}

// set stores a SuperEdge in the matrix
func (m *superEdgeMatrix) set(fromID, toID TokenID, se *SuperEdge) {
	if m.edges[fromID] == nil {
		m.edges[fromID] = make(map[TokenID]*SuperEdge, 4)
	}
	if m.edges[fromID][toID] == nil {
		m.count++
	}
	m.edges[fromID][toID] = se
}

// get retrieves a SuperEdge from the matrix (O(1) lookup)
func (m *superEdgeMatrix) get(fromID, toID TokenID) *SuperEdge {
	if row := m.edges[fromID]; row != nil {
		return row[toID]
	}
	return nil
}

// NewSuperEdge creates a new super edge between two tokens
func NewSuperEdge(from, to TokenID, fromMint, toMint solana.PublicKey) *SuperEdge {
	return &SuperEdge{
		From:     from,
		To:       to,
		FromMint: fromMint,
		ToMint:   toMint,
		pools:    make([]*domain.Pool, 0, 4),
	}
}

// NewImmutableSuperEdge creates a pre-computed SuperEdge for snapshot storage
// The pools slice is set directly without copying (caller must not modify after)
func NewImmutableSuperEdge(from, to TokenID, fromMint, toMint solana.PublicKey, pools []*domain.Pool) *SuperEdge {
	se := &SuperEdge{
		From:      from,
		To:        to,
		FromMint:  fromMint,
		ToMint:    toMint,
		pools:     pools,
		immutable: true,
	}
	if len(pools) > 0 {
		se.bestPool = pools[0]
	}
	return se
}

// AddPool adds a pool to the super edge and maintains sorting
func (se *SuperEdge) AddPool(pool *domain.Pool) {
	se.mu.Lock()
	defer se.mu.Unlock()

	// Check if pool already exists
	for i, p := range se.pools {
		if p.Address.Equals(pool.Address) {
			se.pools[i] = pool // Update existing
			se.sortAndCacheBest()
			return
		}
	}

	se.pools = append(se.pools, pool)
	se.sortAndCacheBest()
}

// RemovePool removes a pool from the super edge
func (se *SuperEdge) RemovePool(poolAddr solana.PublicKey) {
	se.mu.Lock()
	defer se.mu.Unlock()

	for i, p := range se.pools {
		if p.Address.Equals(poolAddr) {
			se.pools = append(se.pools[:i], se.pools[i+1:]...)
			se.sortAndCacheBest()
			return
		}
	}
}

// sortAndCacheBest sorts pools by output liquidity and caches the best pool
// Must be called with mu held
// Optimized: uses uint64 shadow fields to avoid big.Int comparison overhead
func (se *SuperEdge) sortAndCacheBest() {
	if len(se.pools) == 0 {
		se.bestPool = nil
		return
	}

	// Sort by output reserve (descending) using uint64 for zero-alloc comparison
	sort.Slice(se.pools, func(i, j int) bool {
		liqI := se.getOutputReserveU64(se.pools[i])
		liqJ := se.getOutputReserveU64(se.pools[j])
		return liqI > liqJ
	})

	// Limit pools
	if len(se.pools) > MaxPoolsPerSuperEdge {
		se.pools = se.pools[:MaxPoolsPerSuperEdge]
	}

	se.bestPool = se.pools[0]
}

// getOutputReserveU64 returns the output reserve as uint64 for zero-allocation sorting
func (se *SuperEdge) getOutputReserveU64(pool *domain.Pool) uint64 {
	if pool.TokenMintA.Equals(se.FromMint) {
		return pool.ReserveBU64
	}
	return pool.ReserveAU64
}

// getOutputReserve returns the output reserve for a pool in this edge direction (big.Int version)
func (se *SuperEdge) getOutputReserve(pool *domain.Pool) *big.Int {
	if pool.TokenMintA.Equals(se.FromMint) {
		return pool.ReserveB
	}
	return pool.ReserveA
}

// GetPools returns a copy of the pools slice (thread-safe)
// Note: For hot paths, use GetPoolsDirect instead to avoid allocation
func (se *SuperEdge) GetPools() []*domain.Pool {
	se.mu.RLock()
	defer se.mu.RUnlock()

	result := make([]*domain.Pool, len(se.pools))
	copy(result, se.pools)
	return result
}

// GetPoolsDirect returns the pools slice directly without copying (zero allocation)
// SAFETY: Caller must not modify the returned slice. The slice is valid only while
// the SuperEdge is not being modified. For immutable graph snapshots, this is safe.
func (se *SuperEdge) GetPoolsDirect() []*domain.Pool {
	// Fast path for immutable SuperEdges (no lock needed)
	if se.immutable {
		return se.pools
	}
	se.mu.RLock()
	defer se.mu.RUnlock()
	return se.pools
}

// GetBestPool returns the best single pool for this edge
func (se *SuperEdge) GetBestPool() *domain.Pool {
	if se.immutable {
		return se.bestPool
	}
	se.mu.RLock()
	defer se.mu.RUnlock()
	return se.bestPool
}

// PoolCount returns the number of pools in this super edge
func (se *SuperEdge) PoolCount() int {
	if se.immutable {
		return len(se.pools)
	}
	se.mu.RLock()
	defer se.mu.RUnlock()
	return len(se.pools)
}

// IsEmpty returns true if no pools exist in this edge
func (se *SuperEdge) IsEmpty() bool {
	if se.immutable {
		return len(se.pools) == 0
	}
	se.mu.RLock()
	defer se.mu.RUnlock()
	return len(se.pools) == 0
}

// SuperEdgeQuote represents an optimized quote across a super edge
type SuperEdgeQuote struct {
	AmountIn       *big.Int
	AmountOut      *big.Int
	Fee            *big.Int
	PriceImpactBps uint16
	IsSplit        bool
	PoolSplits     []PoolSplit
}

// PoolSplit represents one pool's contribution in a split quote
type PoolSplit struct {
	Pool      *domain.Pool
	Percent   uint8
	AmountIn  *big.Int
	AmountOut *big.Int
	Fee       *big.Int
	AToB      bool
}

// SuperEdgeQuoter provides quoting functionality for super edges
// Optimized: stateless struct that can be reused or stack-allocated
type SuperEdgeQuoter struct {
	quoter QuoterFunc
}

// NewSuperEdgeQuoter creates a new super edge quoter
func NewSuperEdgeQuoter(quoter QuoterFunc) *SuperEdgeQuoter {
	return &SuperEdgeQuoter{quoter: quoter}
}

// Pre-allocated threshold for fast comparison (avoid big.NewInt per call)
var superEdgeSplitThresholdBig = big.NewInt(SuperEdgeSplitThreshold)

// GetQuote gets the best quote for a super edge, potentially splitting across pools
// Optimized: uses GetPoolsDirect to avoid slice copy, pre-allocated threshold
func (seq *SuperEdgeQuoter) GetQuote(se *SuperEdge, amount *big.Int, exactIn bool) (*SuperEdgeQuote, error) {
	pools := se.GetPoolsDirect() // Zero-alloc: returns slice directly
	if len(pools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Determine swap direction
	aToB := pools[0].TokenMintA.Equals(se.FromMint)

	// For small amounts or single pool, use best pool directly
	// Use pre-allocated threshold to avoid big.NewInt allocation
	if len(pools) == 1 || amount.Cmp(superEdgeSplitThresholdBig) < 0 {
		return seq.quoteSinglePool(pools[0], amount, aToB, exactIn)
	}

	// Try splitting across top pools
	splitQuote, err := seq.quoteSplitPools(pools, amount, aToB, exactIn)
	if err != nil {
		// Fallback to single pool
		return seq.quoteSinglePool(pools[0], amount, aToB, exactIn)
	}

	// Compare with single pool quote
	singleQuote, singleErr := seq.quoteSinglePool(pools[0], amount, aToB, exactIn)
	if singleErr != nil {
		return splitQuote, nil
	}

	// Return the better quote
	if exactIn {
		if splitQuote.AmountOut.Cmp(singleQuote.AmountOut) > 0 {
			return splitQuote, nil
		}
		return singleQuote, nil
	}
	// ExactOut: prefer lower input
	if splitQuote.AmountIn.Cmp(singleQuote.AmountIn) < 0 {
		return splitQuote, nil
	}
	return singleQuote, nil
}

// quoteSinglePool quotes using a single pool
func (seq *SuperEdgeQuoter) quoteSinglePool(pool *domain.Pool, amount *big.Int, aToB, exactIn bool) (*SuperEdgeQuote, error) {
	quote, err := seq.quoter(pool, amount, aToB, exactIn)
	if err != nil {
		return nil, err
	}

	return &SuperEdgeQuote{
		AmountIn:       quote.AmountIn,
		AmountOut:      quote.AmountOut,
		Fee:            quote.Fee,
		PriceImpactBps: quote.PriceImpactBps,
		IsSplit:        false,
		PoolSplits: []PoolSplit{{
			Pool:      pool,
			Percent:   100,
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			Fee:       quote.Fee,
			AToB:      aToB,
		}},
	}, nil
}

// quoteSplitPools optimizes a quote across multiple pools using equal split
// Optimized: uses pooled big.Int for temporaries, pre-allocated constants
func (seq *SuperEdgeQuoter) quoteSplitPools(pools []*domain.Pool, totalAmount *big.Int, aToB, exactIn bool) (*SuperEdgeQuote, error) {
	// Limit to top 3 pools
	numPools := len(pools)
	if numPools > 3 {
		numPools = 3
		pools = pools[:numPools]
	}

	// Start with equal split
	percent := uint8(100 / numPools)
	remainder := uint8(100 - (percent * uint8(numPools)))

	splits := make([]PoolSplit, numPools)

	// Use pooled big.Int for temporaries
	totalIn := GetBigInt()
	totalOut := GetBigInt()
	totalFee := GetBigInt()
	splitAmount := GetBigInt()
	pctBig := GetBigInt()

	weightedImpact := uint32(0)
	validSplits := 0

	for i, pool := range pools {
		pct := percent
		if i == 0 {
			pct += remainder
		}

		// Calculate split amount using pooled big.Int
		pctBig.SetInt64(int64(pct))
		splitAmount.Mul(totalAmount, pctBig)
		splitAmount.Div(splitAmount, HUNDRED)

		// Create a copy for the quoter (it may store the reference)
		splitAmountCopy := new(big.Int).Set(splitAmount)

		quote, err := seq.quoter(pool, splitAmountCopy, aToB, exactIn)
		if err != nil {
			continue
		}

		splits[i] = PoolSplit{
			Pool:      pool,
			Percent:   pct,
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			Fee:       quote.Fee,
			AToB:      aToB,
		}
		validSplits++

		totalIn.Add(totalIn, quote.AmountIn)
		totalOut.Add(totalOut, quote.AmountOut)
		totalFee.Add(totalFee, quote.Fee)
		weightedImpact += uint32(quote.PriceImpactBps) * uint32(pct)
	}

	// Return pooled temporaries
	PutBigInt(splitAmount)
	PutBigInt(pctBig)

	if totalOut.Sign() == 0 || validSplits == 0 {
		PutBigInt(totalIn)
		PutBigInt(totalOut)
		PutBigInt(totalFee)
		return nil, ErrNoPoolFound
	}

	// Filter empty splits (only if some failed)
	var activeSplits []PoolSplit
	if validSplits == numPools {
		// All succeeded, use splits directly
		activeSplits = splits
	} else {
		// Some failed, filter
		activeSplits = make([]PoolSplit, 0, validSplits)
		for _, s := range splits {
			if s.Pool != nil {
				activeSplits = append(activeSplits, s)
			}
		}
	}

	avgImpact := uint16(weightedImpact / 100)

	// Create final copies for result (these escape to heap)
	finalIn := new(big.Int).Set(totalIn)
	finalOut := new(big.Int).Set(totalOut)
	finalFee := new(big.Int).Set(totalFee)

	// Return pooled temporaries
	PutBigInt(totalIn)
	PutBigInt(totalOut)
	PutBigInt(totalFee)

	return &SuperEdgeQuote{
		AmountIn:       finalIn,
		AmountOut:      finalOut,
		Fee:            finalFee,
		PriceImpactBps: avgImpact,
		IsSplit:        len(activeSplits) > 1,
		PoolSplits:     activeSplits,
	}, nil
}

// ToSwapQuote converts SuperEdgeQuote to domain.SwapQuote for compatibility
func (seq *SuperEdgeQuote) ToSwapQuote() *domain.SwapQuote {
	return &domain.SwapQuote{
		AmountIn:       seq.AmountIn,
		AmountOut:      seq.AmountOut,
		Fee:            seq.Fee,
		PriceImpactBps: seq.PriceImpactBps,
	}
}

// FastSuperEdgeQuoter provides zero-allocation quoting for super edges
type FastSuperEdgeQuoter struct {
	quoter FastQuoterFunc
}

// NewFastSuperEdgeQuoter creates a new fast super edge quoter
func NewFastSuperEdgeQuoter(quoter FastQuoterFunc) *FastSuperEdgeQuoter {
	return &FastSuperEdgeQuoter{quoter: quoter}
}

// GetFastQuote gets the best quote using uint64 (zero big.Int allocation)
func (seq *FastSuperEdgeQuoter) GetFastQuote(se *SuperEdge, amount uint64, exactIn bool) (*FastSuperEdgeQuote, error) {
	pools := se.GetPoolsDirect()
	if len(pools) == 0 {
		return nil, ErrNoPoolFound
	}

	aToB := pools[0].TokenMintA.Equals(se.FromMint)

	// For small amounts or single pool, use best pool directly
	if len(pools) == 1 || amount < SuperEdgeSplitThreshold {
		return seq.fastQuoteSinglePool(pools[0], amount, aToB, exactIn)
	}

	// Try splitting across top pools
	splitQuote, err := seq.fastQuoteSplitPools(pools, amount, aToB, exactIn)
	if err != nil {
		return seq.fastQuoteSinglePool(pools[0], amount, aToB, exactIn)
	}

	// Compare with single pool quote
	singleQuote, singleErr := seq.fastQuoteSinglePool(pools[0], amount, aToB, exactIn)
	if singleErr != nil {
		return splitQuote, nil
	}

	// Return the better quote
	if exactIn {
		if splitQuote.AmountOut > singleQuote.AmountOut {
			return splitQuote, nil
		}
		return singleQuote, nil
	}
	if splitQuote.AmountIn < singleQuote.AmountIn {
		return splitQuote, nil
	}
	return singleQuote, nil
}

// fastQuoteSinglePool quotes using a single pool (zero allocation)
func (seq *FastSuperEdgeQuoter) fastQuoteSinglePool(pool *domain.Pool, amount uint64, aToB, exactIn bool) (*FastSuperEdgeQuote, error) {
	quote, err := seq.quoter(pool, amount, aToB, exactIn)
	if err != nil {
		return nil, err
	}

	return &FastSuperEdgeQuote{
		AmountIn:       quote.AmountIn,
		AmountOut:      quote.AmountOut,
		Fee:            quote.Fee,
		PriceImpactBps: quote.PriceImpactBps,
		IsSplit:        false,
		PoolSplits: []FastPoolSplit{{
			Pool:      pool,
			Percent:   100,
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			Fee:       quote.Fee,
			AToB:      aToB,
		}},
	}, nil
}

// fastQuoteSplitPools optimizes a quote across multiple pools (zero big.Int allocation)
func (seq *FastSuperEdgeQuoter) fastQuoteSplitPools(pools []*domain.Pool, totalAmount uint64, aToB, exactIn bool) (*FastSuperEdgeQuote, error) {
	numPools := len(pools)
	if numPools > 3 {
		numPools = 3
		pools = pools[:numPools]
	}

	percent := uint8(100 / numPools)
	remainder := uint8(100 - (percent * uint8(numPools)))

	splits := make([]FastPoolSplit, numPools)
	var totalIn, totalOut, totalFee uint64
	weightedImpact := uint32(0)
	validSplits := 0

	for i, pool := range pools {
		pct := percent
		if i == 0 {
			pct += remainder
		}

		splitAmount := (totalAmount * uint64(pct)) / 100

		quote, err := seq.quoter(pool, splitAmount, aToB, exactIn)
		if err != nil {
			continue
		}

		splits[i] = FastPoolSplit{
			Pool:      pool,
			Percent:   pct,
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			Fee:       quote.Fee,
			AToB:      aToB,
		}
		validSplits++

		totalIn += quote.AmountIn
		totalOut += quote.AmountOut
		totalFee += quote.Fee
		weightedImpact += uint32(quote.PriceImpactBps) * uint32(pct)
	}

	if totalOut == 0 || validSplits == 0 {
		return nil, ErrNoPoolFound
	}

	// Filter empty splits if some failed
	var activeSplits []FastPoolSplit
	if validSplits == numPools {
		activeSplits = splits
	} else {
		activeSplits = make([]FastPoolSplit, 0, validSplits)
		for _, s := range splits {
			if s.Pool != nil {
				activeSplits = append(activeSplits, s)
			}
		}
	}

	return &FastSuperEdgeQuote{
		AmountIn:       totalIn,
		AmountOut:      totalOut,
		Fee:            totalFee,
		PriceImpactBps: uint16(weightedImpact / 100),
		IsSplit:        len(activeSplits) > 1,
		PoolSplits:     activeSplits,
	}, nil
}
