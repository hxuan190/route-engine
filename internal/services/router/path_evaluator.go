package router

import (
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
	"github.com/hxuan190/route-engine/internal/metrics"
)

// PathEvaluationTimeout is the maximum time to spend evaluating a single path
const PathEvaluationTimeout = 50 * time.Millisecond

// pathEvaluationResult holds the result of evaluating a single path
type pathEvaluationResult struct {
	route *domain.MultiHopQuoteResult
	err   error
}

// pathEvalCounter for sampling metrics (1/64 calls)
var pathEvalCounter atomic.Uint64

// evaluatePaths simulates all candidate paths in parallel and returns the best one
// Optimized: samples metrics 1/64, uses pre-allocated result slice for small path counts
func (r *Router) evaluatePaths(paths [][]solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	if len(paths) == 0 {
		return nil, ErrNoRoute
	}

	// Sample metrics 1/64 to reduce hot-path overhead
	sample := pathEvalCounter.Add(1)&0x3F == 0
	var start time.Time
	if sample {
		start = time.Now()
	}

	// Limit number of paths to evaluate
	if len(paths) > MaxPathsToEvaluate {
		paths = paths[:MaxPathsToEvaluate]
	}

	// For small path counts, sequential evaluation avoids goroutine overhead
	if len(paths) <= 2 {
		bestRoute := r.evaluatePathsSequential(paths, amount, exactIn)
		if sample {
			metrics.MultiHopDuration.Observe(time.Since(start).Seconds())
		}
		if bestRoute == nil {
			return nil, ErrNoRoute
		}
		return bestRoute, nil
	}

	// Evaluate paths in parallel for larger counts
	results := make([]pathEvaluationResult, len(paths))
	var wg sync.WaitGroup

	for i, path := range paths {
		wg.Add(1)
		go func(idx int, p []solana.PublicKey) {
			defer wg.Done()
			route, err := r.evaluateSinglePath(p, amount, exactIn)
			results[idx] = pathEvaluationResult{route: route, err: err}
		}(i, path)
	}
	wg.Wait()

	// Find best result from pre-allocated slice (no channel overhead)
	var bestRoute *domain.MultiHopQuoteResult
	for _, res := range results {
		if res.err != nil || res.route == nil {
			continue
		}

		if bestRoute == nil {
			bestRoute = res.route
			continue
		}

		// Compare routes
		if exactIn {
			if res.route.AmountOut.Cmp(bestRoute.AmountOut) > 0 {
				bestRoute = res.route
			}
		} else {
			if res.route.AmountIn.Cmp(bestRoute.AmountIn) < 0 {
				bestRoute = res.route
			}
		}
	}

	if sample {
		metrics.MultiHopDuration.Observe(time.Since(start).Seconds())
	}

	if bestRoute == nil {
		return nil, ErrNoRoute
	}

	return bestRoute, nil
}

// evaluatePathsSequential evaluates paths without goroutine overhead (for small counts)
func (r *Router) evaluatePathsSequential(paths [][]solana.PublicKey, amount *big.Int, exactIn bool) *domain.MultiHopQuoteResult {
	var bestRoute *domain.MultiHopQuoteResult

	for _, path := range paths {
		route, err := r.evaluateSinglePath(path, amount, exactIn)
		if err != nil || route == nil {
			continue
		}

		if bestRoute == nil {
			bestRoute = route
			continue
		}

		if exactIn {
			if route.AmountOut.Cmp(bestRoute.AmountOut) > 0 {
				bestRoute = route
			}
		} else {
			if route.AmountIn.Cmp(bestRoute.AmountIn) < 0 {
				bestRoute = route
			}
		}
	}

	return bestRoute
}

// evaluateSinglePath chains quotes through each hop in the path
func (r *Router) evaluateSinglePath(path []solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	if len(path) < 2 {
		return nil, ErrNoRoute
	}

	// For ExactIn: forward simulation (input -> output)
	// For ExactOut: backward simulation (output -> input)
	if exactIn {
		return r.evaluatePathExactIn(path, amount)
	}
	return r.evaluatePathExactOut(path, amount)
}

// evaluatePathExactIn simulates a path forward for ExactIn swaps
// Optimized: uses pre-computed SuperEdges (zero allocation), reuses SuperEdgeQuoter
func (r *Router) evaluatePathExactIn(path []solana.PublicKey, amountIn *big.Int) (*domain.MultiHopQuoteResult, error) {
	hops := make([]domain.HopQuote, 0, len(path)-1)
	currentAmount := amountIn // Don't copy initially, only copy when needed
	totalFee := GetBigInt()
	defer PutBigInt(totalFee)

	totalImpact := uint32(0)

	// Use value-type quoter (stack allocated, no heap allocation)
	seQuoter := SuperEdgeQuoter{quoter: r.Quoter}

	for i := 0; i < len(path)-1; i++ {
		inputMint := path[i]
		outputMint := path[i+1]

		// Get pre-computed SuperEdge for this hop (zero allocation)
		se := r.Graph.GetPrecomputedSuperEdge(inputMint, outputMint)
		if se == nil || se.IsEmpty() {
			return nil, ErrNoPoolFound
		}

		// Get optimized quote across all pools in the SuperEdge
		seQuote, err := seQuoter.GetQuote(se, currentAmount, true)
		if err != nil {
			return nil, err
		}

		// Use the pool from the quote (first split has the primary pool)
		if len(seQuote.PoolSplits) == 0 {
			return nil, ErrNoPoolFound
		}
		primaryPool := seQuote.PoolSplits[0].Pool
		aToB := seQuote.PoolSplits[0].AToB

		// Add hop to result
		hops = append(hops, domain.HopQuote{
			Pool:           primaryPool,
			AmountIn:       seQuote.AmountIn,
			AmountOut:      seQuote.AmountOut,
			FeeAmount:      seQuote.Fee,
			AToB:           aToB,
			PriceImpactBps: seQuote.PriceImpactBps,
		})

		totalFee.Add(totalFee, seQuote.Fee)
		totalImpact += uint32(seQuote.PriceImpactBps)

		// Output of this hop becomes input of next hop
		currentAmount = seQuote.AmountOut
	}

	finalFee := new(big.Int).Set(totalFee)
	combinedImpact := uint16(min(int(totalImpact), 10000))

	return &domain.MultiHopQuoteResult{
		Route:          path,
		Hops:           hops,
		AmountIn:       amountIn,
		AmountOut:      currentAmount,
		TotalFee:       finalFee,
		PriceImpactBps: combinedImpact,
	}, nil
}

// evaluatePathExactOut simulates a path backward for ExactOut swaps
// Optimized: uses pre-computed SuperEdges (zero allocation), reuses SuperEdgeQuoter
func (r *Router) evaluatePathExactOut(path []solana.PublicKey, amountOut *big.Int) (*domain.MultiHopQuoteResult, error) {
	hops := make([]domain.HopQuote, len(path)-1)
	currentAmount := amountOut // Don't copy initially
	totalFee := GetBigInt()
	defer PutBigInt(totalFee)

	totalImpact := uint32(0)

	// Use value-type quoter (stack allocated, no heap allocation)
	seQuoter := SuperEdgeQuoter{quoter: r.Quoter}

	for i := len(path) - 1; i > 0; i-- {
		inputMint := path[i-1]
		outputMint := path[i]

		// Get pre-computed SuperEdge for this hop (zero allocation)
		se := r.Graph.GetPrecomputedSuperEdge(inputMint, outputMint)
		if se == nil || se.IsEmpty() {
			return nil, ErrNoPoolFound
		}

		// Get optimized quote (exactOut)
		seQuote, err := seQuoter.GetQuote(se, currentAmount, false)
		if err != nil {
			return nil, err
		}

		// Use the pool from the quote (first split has the primary pool)
		if len(seQuote.PoolSplits) == 0 {
			return nil, ErrNoPoolFound
		}
		primaryPool := seQuote.PoolSplits[0].Pool
		aToB := seQuote.PoolSplits[0].AToB

		// Add hop to result (in reverse order)
		hops[i-1] = domain.HopQuote{
			Pool:           primaryPool,
			AmountIn:       seQuote.AmountIn,
			AmountOut:      seQuote.AmountOut,
			FeeAmount:      seQuote.Fee,
			AToB:           aToB,
			PriceImpactBps: seQuote.PriceImpactBps,
		}

		totalFee.Add(totalFee, seQuote.Fee)
		totalImpact += uint32(seQuote.PriceImpactBps)

		// Input of this hop becomes output of previous hop (going backward)
		currentAmount = seQuote.AmountIn
	}

	finalFee := new(big.Int).Set(totalFee)
	combinedImpact := uint16(min(int(totalImpact), 10000))

	return &domain.MultiHopQuoteResult{
		Route:          path,
		Hops:           hops,
		AmountIn:       currentAmount,
		AmountOut:      amountOut,
		TotalFee:       finalFee,
		PriceImpactBps: combinedImpact,
	}, nil
}

// fastPathEvaluationResult holds the result of fast path evaluation
type fastPathEvaluationResult struct {
	route *FastMultiHopQuoteResult
	err   error
}

// evaluatePathsFast evaluates paths using uint64 (zero big.Int allocation in hot path)
func (r *Router) evaluatePathsFast(paths [][]solana.PublicKey, amount uint64, exactIn bool) (*FastMultiHopQuoteResult, error) {
	if len(paths) == 0 {
		return nil, ErrNoRoute
	}

	if len(paths) > MaxPathsToEvaluate {
		paths = paths[:MaxPathsToEvaluate]
	}

	// For small path counts, sequential evaluation avoids goroutine overhead
	if len(paths) <= 2 {
		return r.evaluatePathsFastSequential(paths, amount, exactIn)
	}

	// Evaluate paths in parallel
	results := make([]fastPathEvaluationResult, len(paths))
	var wg sync.WaitGroup

	for i, path := range paths {
		wg.Add(1)
		go func(idx int, p []solana.PublicKey) {
			defer wg.Done()
			route, err := r.evaluateSinglePathFast(p, amount, exactIn)
			results[idx] = fastPathEvaluationResult{route: route, err: err}
		}(i, path)
	}
	wg.Wait()

	var bestRoute *FastMultiHopQuoteResult
	for _, res := range results {
		if res.err != nil || res.route == nil {
			continue
		}

		if bestRoute == nil {
			bestRoute = res.route
			continue
		}

		if exactIn {
			if res.route.AmountOut > bestRoute.AmountOut {
				bestRoute = res.route
			}
		} else {
			if res.route.AmountIn < bestRoute.AmountIn {
				bestRoute = res.route
			}
		}
	}

	if bestRoute == nil {
		return nil, ErrNoRoute
	}
	return bestRoute, nil
}

// evaluatePathsFastSequential evaluates paths sequentially (zero allocation)
func (r *Router) evaluatePathsFastSequential(paths [][]solana.PublicKey, amount uint64, exactIn bool) (*FastMultiHopQuoteResult, error) {
	var bestRoute *FastMultiHopQuoteResult

	for _, path := range paths {
		route, err := r.evaluateSinglePathFast(path, amount, exactIn)
		if err != nil || route == nil {
			continue
		}

		if bestRoute == nil {
			bestRoute = route
			continue
		}

		if exactIn {
			if route.AmountOut > bestRoute.AmountOut {
				bestRoute = route
			}
		} else {
			if route.AmountIn < bestRoute.AmountIn {
				bestRoute = route
			}
		}
	}

	if bestRoute == nil {
		return nil, ErrNoRoute
	}
	return bestRoute, nil
}

// evaluateSinglePathFast evaluates a single path using uint64 (zero big.Int allocation)
func (r *Router) evaluateSinglePathFast(path []solana.PublicKey, amount uint64, exactIn bool) (*FastMultiHopQuoteResult, error) {
	if len(path) < 2 {
		return nil, ErrNoRoute
	}

	if exactIn {
		return r.evaluatePathFastExactIn(path, amount)
	}
	return r.evaluatePathFastExactOut(path, amount)
}

// evaluatePathFastExactIn simulates a path forward using uint64 (zero big.Int allocation)
func (r *Router) evaluatePathFastExactIn(path []solana.PublicKey, amountIn uint64) (*FastMultiHopQuoteResult, error) {
	hops := make([]FastHopQuote, 0, len(path)-1)
	currentAmount := amountIn
	var totalFee uint64
	totalImpact := uint32(0)

	// Use fast quoter
	seQuoter := FastSuperEdgeQuoter{quoter: r.FastQuoter}

	for i := 0; i < len(path)-1; i++ {
		inputMint := path[i]
		outputMint := path[i+1]

		se := r.Graph.GetPrecomputedSuperEdge(inputMint, outputMint)
		if se == nil || se.IsEmpty() {
			return nil, ErrNoPoolFound
		}

		seQuote, err := seQuoter.GetFastQuote(se, currentAmount, true)
		if err != nil {
			return nil, err
		}

		if len(seQuote.PoolSplits) == 0 {
			return nil, ErrNoPoolFound
		}
		primaryPool := seQuote.PoolSplits[0].Pool
		aToB := seQuote.PoolSplits[0].AToB

		hops = append(hops, FastHopQuote{
			Pool:           primaryPool,
			AmountIn:       seQuote.AmountIn,
			AmountOut:      seQuote.AmountOut,
			FeeAmount:      seQuote.Fee,
			AToB:           aToB,
			PriceImpactBps: seQuote.PriceImpactBps,
		})

		totalFee += seQuote.Fee
		totalImpact += uint32(seQuote.PriceImpactBps)
		currentAmount = seQuote.AmountOut
	}

	combinedImpact := uint16(min(int(totalImpact), 10000))

	return &FastMultiHopQuoteResult{
		Route:          path,
		Hops:           hops,
		AmountIn:       amountIn,
		AmountOut:      currentAmount,
		TotalFee:       totalFee,
		PriceImpactBps: combinedImpact,
	}, nil
}

// evaluatePathFastExactOut simulates a path backward using uint64 (zero big.Int allocation)
func (r *Router) evaluatePathFastExactOut(path []solana.PublicKey, amountOut uint64) (*FastMultiHopQuoteResult, error) {
	hops := make([]FastHopQuote, len(path)-1)
	currentAmount := amountOut
	var totalFee uint64
	totalImpact := uint32(0)

	seQuoter := FastSuperEdgeQuoter{quoter: r.FastQuoter}

	for i := len(path) - 1; i > 0; i-- {
		inputMint := path[i-1]
		outputMint := path[i]

		se := r.Graph.GetPrecomputedSuperEdge(inputMint, outputMint)
		if se == nil || se.IsEmpty() {
			return nil, ErrNoPoolFound
		}

		seQuote, err := seQuoter.GetFastQuote(se, currentAmount, false)
		if err != nil {
			return nil, err
		}

		if len(seQuote.PoolSplits) == 0 {
			return nil, ErrNoPoolFound
		}
		primaryPool := seQuote.PoolSplits[0].Pool
		aToB := seQuote.PoolSplits[0].AToB

		hops[i-1] = FastHopQuote{
			Pool:           primaryPool,
			AmountIn:       seQuote.AmountIn,
			AmountOut:      seQuote.AmountOut,
			FeeAmount:      seQuote.Fee,
			AToB:           aToB,
			PriceImpactBps: seQuote.PriceImpactBps,
		}

		totalFee += seQuote.Fee
		totalImpact += uint32(seQuote.PriceImpactBps)
		currentAmount = seQuote.AmountIn
	}

	combinedImpact := uint16(min(int(totalImpact), 10000))

	return &FastMultiHopQuoteResult{
		Route:          path,
		Hops:           hops,
		AmountIn:       currentAmount,
		AmountOut:      amountOut,
		TotalFee:       totalFee,
		PriceImpactBps: combinedImpact,
	}, nil
}
