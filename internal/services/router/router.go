package router

import (
	"math/big"
	"sort"
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
)

// MaxSplits is the maximum number of pools to split across
const MaxSplits = 3

// MinSplitPercent is the minimum percent allocation per pool
const MinSplitPercent = 10

// QuoterFunc is a function that can quote a pool
type QuoterFunc func(pool *domain.Pool, amount *big.Int, aToB bool, exactIn bool) (*domain.SwapQuote, error)

type Router struct {
	Graph      *Graph
	Quoter     QuoterFunc
	FastQuoter FastQuoterFunc // Zero-allocation quoter using uint64
}

func NewRouter(graph *Graph, quoter QuoterFunc) *Router {
	return &Router{
		Graph:  graph,
		Quoter: quoter,
	}
}

// SetFastQuoter sets the fast quoter for zero-allocation path evaluation
func (r *Router) SetFastQuoter(fastQuoter FastQuoterFunc) {
	r.FastQuoter = fastQuoter
}

// Common intermediate tokens for multi-hop routing
var IntermediateTokens = []solana.PublicKey{
	solana.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),
	solana.MustPublicKeyFromBase58("uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"),
}

func (r *Router) GetQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.QuoteResult, error) {
	directPools := r.Graph.GetDirectRoutesForPair(inputMint, outputMint)
	if len(directPools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Use value type to avoid heap allocation during iteration
	var currentBest domain.QuoteResult
	var bestOutput *big.Int
	hasBest := false

	for _, pool := range directPools {
		aToB := pool.TokenMintA == inputMint && pool.TokenMintB == outputMint

		quote, err := r.Quoter(pool, amount, aToB, exactIn)

		if err != nil {
			continue
		}

		// EARLY EXIT: if we already have a zero price impact quote, skip inferior quotes
		if hasBest && currentBest.PriceImpactBps == 0 && quote.PriceImpactBps > 0 {
			continue
		}

		if !hasBest || (exactIn && quote.AmountOut.Cmp(bestOutput) > 0) || (!exactIn && quote.AmountIn.Cmp(bestOutput) < 0) {
			// Value copy - no heap allocation
			currentBest = domain.QuoteResult{
				Pool:           pool,
				AmountIn:       quote.AmountIn,
				AmountOut:      quote.AmountOut,
				FeeAmount:      quote.Fee,
				AToB:           aToB,
				PriceImpactBps: quote.PriceImpactBps,
			}
			hasBest = true
			if exactIn {
				bestOutput = quote.AmountOut
			} else {
				bestOutput = quote.AmountIn
			}

			// EARLY EXIT: found perfect quote (zero impact), no need to check more pools
			if quote.PriceImpactBps == 0 {
				break
			}
		}
	}

	if !hasBest {
		return nil, ErrNoPoolFound
	}

	// Single heap allocation at return
	return &currentBest, nil
}

// GetSplitQuote finds the optimal split across multiple pools for a token pair
func (r *Router) GetSplitQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.SplitQuoteResult, error) {
	pools := r.Graph.GetDirectRoutesForPair(inputMint, outputMint)
	if len(pools) == 0 {
		return nil, ErrNoPoolFound
	}

	// If only one pool, no split needed
	if len(pools) == 1 {
		return r.getSinglePoolQuote(pools[0], inputMint, outputMint, amount, exactIn)
	}

	rankedPools := r.rankPoolsByLiquidity(pools, inputMint, amount)

	if len(rankedPools) > MaxSplits {
		rankedPools = rankedPools[:MaxSplits]
	}
	return r.findOptimalSplit(rankedPools, inputMint, outputMint, amount, exactIn)
}

// getSinglePoolQuote gets quote for a single pool (no split)
func (r *Router) getSinglePoolQuote(pool *domain.Pool, inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.SplitQuoteResult, error) {
	aToB := pool.TokenMintA == inputMint

	// Use quoter function (supports all pool types)
	quote, err := r.Quoter(pool, amount, aToB, exactIn)
	if err != nil {
		return nil, err
	}

	return &domain.SplitQuoteResult{
		InputMint:  inputMint,
		OutputMint: outputMint,
		Splits: []domain.SplitRoute{
			{
				Pool:           pool,
				Percent:        100,
				AmountIn:       quote.AmountIn,
				AmountOut:      quote.AmountOut,
				FeeAmount:      quote.Fee,
				AToB:           aToB,
				PriceImpactBps: quote.PriceImpactBps,
			},
		},
		TotalAmountIn:  quote.AmountIn,
		TotalAmountOut: quote.AmountOut,
		TotalFee:       quote.Fee,
		PriceImpactBps: quote.PriceImpactBps,
	}, nil
}

type poolScore struct {
	Pool      *domain.Pool
	AToB      bool
	Liquidity *big.Int
	Score     float64
}

func (r *Router) rankPoolsByLiquidity(pools []*domain.Pool, inputMint solana.PublicKey, amount *big.Int) []*domain.Pool {
	scores := make([]poolScore, 0, len(pools))

	// Use pooled big.Float for calculations
	amountFloat := GetBigFloat()
	liqFloat := GetBigFloat()
	scoreFloat := GetBigFloat()
	defer func() {
		PutBigFloat(amountFloat)
		PutBigFloat(liqFloat)
		PutBigFloat(scoreFloat)
	}()

	amountFloat.SetInt(amount)

	for _, pool := range pools {
		if pool == nil || !pool.IsReady() {
			continue
		}

		aToB := pool.TokenMintA == inputMint

		var liquidity *big.Int
		if aToB {
			liquidity = pool.ReserveB
		} else {
			liquidity = pool.ReserveA
		}

		if liquidity == nil || liquidity.Sign() <= 0 {
			continue
		}

		// Score = liquidity relative to swap amount
		// Higher score = pool can handle more of the swap with less impact
		liqFloat.SetInt(liquidity)
		scoreFloat.Quo(liqFloat, amountFloat)
		score, _ := scoreFloat.Float64()

		scores = append(scores, poolScore{
			Pool:      pool,
			AToB:      aToB,
			Liquidity: liquidity,
			Score:     score,
		})
	}

	// Use partial sort for top-K selection (O(n) instead of O(n log n))
	// Only sort top MaxSplits pools since that's all we need
	k := MaxSplits
	if k > len(scores) {
		k = len(scores)
	}

	if k > 0 && len(scores) > k {
		// Partial sort: find top K elements using selection
		partialSortTopK(scores, k)
		scores = scores[:k]
	} else if len(scores) > 1 {
		// Small array, just sort it
		sort.Slice(scores, func(i, j int) bool {
			return scores[i].Score > scores[j].Score
		})
	}

	// Extract sorted pools
	result := make([]*domain.Pool, len(scores))
	for i, s := range scores {
		result[i] = s.Pool
	}

	return result
}

// partialSortTopK partially sorts scores to put top K elements at the beginning (descending by Score)
// Uses a simple selection approach which is O(n*k) but faster than O(n log n) sort for small k
func partialSortTopK(scores []poolScore, k int) {
	n := len(scores)
	if k >= n {
		sort.Slice(scores, func(i, j int) bool {
			return scores[i].Score > scores[j].Score
		})
		return
	}

	// For each position 0..k-1, find the maximum in remaining elements
	for i := 0; i < k; i++ {
		maxIdx := i
		for j := i + 1; j < n; j++ {
			if scores[j].Score > scores[maxIdx].Score {
				maxIdx = j
			}
		}
		if maxIdx != i {
			scores[i], scores[maxIdx] = scores[maxIdx], scores[i]
		}
	}
}

// findOptimalSplit uses binary search to find optimal split percentages
func (r *Router) findOptimalSplit(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount *big.Int, exactIn bool) (*domain.SplitQuoteResult, error) {
	if len(pools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Configuration 1: Single best pool (baseline)
	bestResult, _ := r.getSinglePoolQuote(pools[0], inputMint, outputMint, totalAmount, exactIn)

	// Configuration 2: 2-way split with binary search (if we have 2+ pools)
	if len(pools) >= 2 {
		optimalSplit := r.binarySearchSplit(pools[:2], inputMint, outputMint, totalAmount, exactIn)
		if optimalSplit != nil && r.isBetterSplit(optimalSplit, bestResult, exactIn) {
			bestResult = optimalSplit
		}
	}

	// Configuration 3: 3-way split with gradient search (if we have 3+ pools)
	if len(pools) >= 3 {
		threeSplit := r.optimizeThreeWaySplit(pools[:3], inputMint, outputMint, totalAmount, exactIn)
		if threeSplit != nil && r.isBetterSplit(threeSplit, bestResult, exactIn) {
			bestResult = threeSplit
		}
	}

	if bestResult == nil {
		return nil, ErrNoPoolFound
	}

	return bestResult, nil
}

// binarySearchSplit finds optimal 2-pool split using binary search
func (r *Router) binarySearchSplit(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount *big.Int, exactIn bool) *domain.SplitQuoteResult {
	if len(pools) != 2 {
		return nil
	}

	// Binary search for optimal split between 10% and 90%
	lo, hi := uint8(MinSplitPercent), uint8(100-MinSplitPercent)
	var bestResult *domain.SplitQuoteResult

	// First, evaluate at boundaries and middle
	for _, p1 := range []uint8{lo, 50, hi} {
		p2 := 100 - p1
		split, err := r.calculateSplit(pools, inputMint, outputMint, totalAmount, []uint8{p1, p2}, exactIn)
		if err == nil && r.isBetterSplit(split, bestResult, exactIn) {
			bestResult = split
		}
	}

	// Binary search with ~4 iterations to converge (reduced for latency)
	for iteration := 0; iteration < 4 && hi-lo > 5; iteration++ {
		mid := (lo + hi) / 2
		midLeft := mid - 2
		midRight := mid + 2

		if midLeft < MinSplitPercent {
			midLeft = MinSplitPercent
		}
		if midRight > 100-MinSplitPercent {
			midRight = 100 - MinSplitPercent
		}

		leftSplit, errL := r.calculateSplit(pools, inputMint, outputMint, totalAmount, []uint8{midLeft, 100 - midLeft}, exactIn)
		rightSplit, errR := r.calculateSplit(pools, inputMint, outputMint, totalAmount, []uint8{midRight, 100 - midRight}, exactIn)

		// Update best result
		if errL == nil && r.isBetterSplit(leftSplit, bestResult, exactIn) {
			bestResult = leftSplit
		}
		if errR == nil && r.isBetterSplit(rightSplit, bestResult, exactIn) {
			bestResult = rightSplit
		}

		// Determine search direction based on which side is better
		leftBetter := false
		if errL == nil && errR == nil {
			if exactIn {
				leftBetter = leftSplit.TotalAmountOut.Cmp(rightSplit.TotalAmountOut) > 0
			} else {
				leftBetter = leftSplit.TotalAmountIn.Cmp(rightSplit.TotalAmountIn) < 0
			}
		} else if errL == nil {
			leftBetter = true
		}

		if leftBetter {
			hi = mid
		} else {
			lo = mid
		}
	}

	return bestResult
}

// optimizeThreeWaySplit uses gradient descent to optimize 3-pool splits
func (r *Router) optimizeThreeWaySplit(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount *big.Int, exactIn bool) *domain.SplitQuoteResult {
	if len(pools) != 3 {
		return nil
	}

	// Start with even split
	p1, p2 := uint8(34), uint8(33)
	p3 := 100 - p1 - p2

	var bestResult *domain.SplitQuoteResult
	bestResult, _ = r.calculateSplit(pools, inputMint, outputMint, totalAmount, []uint8{p1, p2, p3}, exactIn)

	if bestResult == nil {
		return nil
	}

	// Track previous best for early exit on minimal improvement
	prevBestOutput := new(big.Int)
	if exactIn {
		prevBestOutput.Set(bestResult.TotalAmountOut)
	} else {
		prevBestOutput.Set(bestResult.TotalAmountIn)
	}

	// Gradient descent: try small adjustments
	step := uint8(5)
	improved := true
	iterations := 0
	maxIterations := 3 // Limit iterations for latency

	for improved && step >= 2 && iterations < maxIterations {
		improved = false
		iterations++

		// Try adjusting each pair
		adjustments := []struct{ dp1, dp2 int8 }{
			{int8(step), -int8(step)}, {-int8(step), int8(step)},
			{int8(step), 0}, {-int8(step), 0},
			{0, int8(step)}, {0, -int8(step)},
		}

		for _, adj := range adjustments {
			newP1 := int(p1) + int(adj.dp1)
			newP2 := int(p2) + int(adj.dp2)
			newP3 := 100 - newP1 - newP2

			// Check bounds
			if newP1 < MinSplitPercent || newP1 > 100-2*MinSplitPercent ||
				newP2 < MinSplitPercent || newP2 > 100-2*MinSplitPercent ||
				newP3 < MinSplitPercent || newP3 > 100-2*MinSplitPercent {
				continue
			}

			split, err := r.calculateSplit(pools, inputMint, outputMint, totalAmount, []uint8{uint8(newP1), uint8(newP2), uint8(newP3)}, exactIn)
			if err == nil && r.isBetterSplit(split, bestResult, exactIn) {
				// Early exit: check if improvement is < 0.1% (10 basis points)
				var currentOutput, newOutput *big.Int
				if exactIn {
					currentOutput = bestResult.TotalAmountOut
					newOutput = split.TotalAmountOut
				} else {
					currentOutput = bestResult.TotalAmountIn
					newOutput = split.TotalAmountIn
				}

				// Calculate improvement ratio: |new - current| / current
				diff := new(big.Int).Sub(newOutput, currentOutput)
				diff.Abs(diff)
				diff.Mul(diff, big.NewInt(10000)) // Scale for basis points
				threshold := new(big.Int).Div(diff, currentOutput)

				// If improvement < 10 bps (0.1%), stop optimizing
				if threshold.Cmp(big.NewInt(10)) < 0 {
					bestResult = split
					return bestResult
				}

				bestResult = split
				p1, p2, p3 = uint8(newP1), uint8(newP2), uint8(newP3)
				improved = true
				break
			}
		}

		if !improved {
			step /= 2
			improved = step >= 2
		}
	}

	return bestResult
}

// calculateSplit calculates the output for a specific split configuration
func (r *Router) calculateSplit(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount *big.Int, percents []uint8, exactIn bool) (*domain.SplitQuoteResult, error) {
	if len(pools) != len(percents) {
		return nil, ErrNoPoolFound
	}

	// Validate percents sum to 100
	var sum uint8
	for _, p := range percents {
		sum += p
	}
	if sum != 100 {
		return nil, ErrNoPoolFound
	}

	splits := make([]domain.SplitRoute, 0, len(pools))
	totalOut := GetBigInt()
	totalFee := GetBigInt()
	splitAmount := GetBigInt()
	percentBig := GetBigInt()

	// We need to copy final values since totalOut/totalFee are returned
	defer func() {
		PutBigInt(splitAmount)
		PutBigInt(percentBig)
	}()

	totalImpact := uint32(0)

	for i, pool := range pools {
		if percents[i] < MinSplitPercent {
			continue
		}

		// Calculate amount for this split using pooled big.Int
		percentBig.SetInt64(int64(percents[i]))
		splitAmount.Mul(totalAmount, percentBig)
		splitAmount.Div(splitAmount, HUNDRED)

		if splitAmount.Sign() <= 0 {
			continue
		}

		aToB := pool.TokenMintA == inputMint

		var quote *domain.SwapQuote
		var err error

		// Create a copy of splitAmount since it's reused
		splitAmountCopy := new(big.Int).Set(splitAmount)

		quote, err = r.Quoter(pool, splitAmountCopy, aToB, exactIn)

		if err != nil {
			continue
		}

		splits = append(splits, domain.SplitRoute{
			Pool:           pool,
			Percent:        percents[i],
			AmountIn:       quote.AmountIn,
			AmountOut:      quote.AmountOut,
			FeeAmount:      quote.Fee,
			AToB:           aToB,
			PriceImpactBps: quote.PriceImpactBps,
		})

		totalOut.Add(totalOut, quote.AmountOut)
		totalFee.Add(totalFee, quote.Fee)
		totalImpact += uint32(quote.PriceImpactBps) * uint32(percents[i])
	}

	if len(splits) == 0 {
		PutBigInt(totalOut)
		PutBigInt(totalFee)
		return nil, ErrNoPoolFound
	}

	// Weighted average price impact
	avgImpact := uint16(totalImpact / 100)

	// Create final copies for the result (these escape to heap but only once per successful call)
	finalOut := new(big.Int).Set(totalOut)
	finalFee := new(big.Int).Set(totalFee)
	PutBigInt(totalOut)
	PutBigInt(totalFee)

	return &domain.SplitQuoteResult{
		InputMint:      inputMint,
		OutputMint:     outputMint,
		Splits:         splits,
		TotalAmountIn:  totalAmount,
		TotalAmountOut: finalOut,
		TotalFee:       finalFee,
		PriceImpactBps: avgImpact,
	}, nil
}

// isBetterSplit checks if newSplit is better than current best
func (r *Router) isBetterSplit(newSplit, bestSplit *domain.SplitQuoteResult, exactIn bool) bool {
	if bestSplit == nil {
		return true
	}
	if newSplit == nil {
		return false
	}

	if exactIn {
		// For ExactIn, higher output is better
		return newSplit.TotalAmountOut.Cmp(bestSplit.TotalAmountOut) > 0
	}
	// For ExactOut, lower input is better
	return newSplit.TotalAmountIn.Cmp(bestSplit.TotalAmountIn) < 0
}

// ConvertSplitToMultiHop converts SplitQuoteResult to MultiHopQuoteResult for compatibility
func (r *Router) ConvertSplitToMultiHop(split *domain.SplitQuoteResult) *domain.MultiHopQuoteResult {
	hops := make([]domain.HopQuote, len(split.Splits))
	for i, s := range split.Splits {
		hops[i] = domain.HopQuote{
			Pool:           s.Pool,
			AmountIn:       s.AmountIn,
			AmountOut:      s.AmountOut,
			FeeAmount:      s.FeeAmount,
			AToB:           s.AToB,
			PriceImpactBps: s.PriceImpactBps,
		}
	}

	return &domain.MultiHopQuoteResult{
		Route:          []solana.PublicKey{split.InputMint, split.OutputMint},
		Hops:           hops,
		AmountIn:       split.TotalAmountIn,
		AmountOut:      split.TotalAmountOut,
		TotalFee:       split.TotalFee,
		PriceImpactBps: split.PriceImpactBps,
		IsSplitRoute:   len(split.Splits) > 1,
		SplitPercents:  extractPercents(split.Splits),
	}
}

func extractPercents(splits []domain.SplitRoute) []uint8 {
	percents := make([]uint8, len(splits))
	for i, s := range splits {
		percents[i] = s.Percent
	}
	return percents
}

// GetSplitQuoteFast finds the best split across multiple direct pools using uint64 (zero-allocation)
func (r *Router) GetSplitQuoteFast(inputMint, outputMint solana.PublicKey, amount uint64, exactIn bool) (*FastSplitQuoteResult, error) {
	se := r.Graph.GetPrecomputedSuperEdge(inputMint, outputMint)
	if se == nil || se.IsEmpty() {
		return nil, ErrNoPoolFound
	}

	pools := se.GetPools()
	if len(pools) == 0 {
		return nil, ErrNoPoolFound
	}

	return r.findOptimalSplitFast(pools, inputMint, outputMint, amount, exactIn)
}

// findOptimalSplitFast uses binary search to find optimal split percentages with uint64
func (r *Router) findOptimalSplitFast(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount uint64, exactIn bool) (*FastSplitQuoteResult, error) {
	if len(pools) == 0 {
		return nil, ErrNoPoolFound
	}

	bestResult, _ := r.getSinglePoolQuoteFast(pools[0], inputMint, outputMint, totalAmount, exactIn)

	if len(pools) >= 2 {
		optimalSplit := r.binarySearchSplitFast(pools[:2], inputMint, outputMint, totalAmount, exactIn)
		if optimalSplit != nil && r.isBetterSplitFast(optimalSplit, bestResult, exactIn) {
			bestResult = optimalSplit
		}
	}

	if len(pools) >= 3 {
		threeSplit := r.optimizeThreeWaySplitFast(pools[:3], inputMint, outputMint, totalAmount, exactIn)
		if threeSplit != nil && r.isBetterSplitFast(threeSplit, bestResult, exactIn) {
			bestResult = threeSplit
		}
	}

	if bestResult == nil {
		return nil, ErrNoPoolFound
	}

	return bestResult, nil
}

// binarySearchSplitFast finds optimal 2-pool split using binary search with uint64
func (r *Router) binarySearchSplitFast(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount uint64, exactIn bool) *FastSplitQuoteResult {
	if len(pools) != 2 {
		return nil
	}

	lo, hi := uint8(MinSplitPercent), uint8(100-MinSplitPercent)
	var bestResult *FastSplitQuoteResult

	for _, p1 := range []uint8{lo, 50, hi} {
		p2 := 100 - p1
		split, err := r.calculateSplitFast(pools, inputMint, outputMint, totalAmount, []uint8{p1, p2}, exactIn)
		if err == nil && r.isBetterSplitFast(split, bestResult, exactIn) {
			bestResult = split
		}
	}

	for iteration := 0; iteration < 4 && hi-lo > 5; iteration++ {
		mid := (lo + hi) / 2
		midLeft := mid - 2
		midRight := mid + 2

		if midLeft < MinSplitPercent {
			midLeft = MinSplitPercent
		}
		if midRight > 100-MinSplitPercent {
			midRight = 100 - MinSplitPercent
		}

		leftSplit, errL := r.calculateSplitFast(pools, inputMint, outputMint, totalAmount, []uint8{midLeft, 100 - midLeft}, exactIn)
		rightSplit, errR := r.calculateSplitFast(pools, inputMint, outputMint, totalAmount, []uint8{midRight, 100 - midRight}, exactIn)

		if errL == nil && r.isBetterSplitFast(leftSplit, bestResult, exactIn) {
			bestResult = leftSplit
		}
		if errR == nil && r.isBetterSplitFast(rightSplit, bestResult, exactIn) {
			bestResult = rightSplit
		}

		leftBetter := false
		if errL == nil && errR == nil {
			if exactIn {
				leftBetter = leftSplit.TotalAmountOut > rightSplit.TotalAmountOut
			} else {
				leftBetter = leftSplit.TotalAmountIn < rightSplit.TotalAmountIn
			}
		} else if errL == nil {
			leftBetter = true
		}

		if leftBetter {
			hi = mid
		} else {
			lo = mid
		}
	}

	return bestResult
}

// optimizeThreeWaySplitFast uses gradient descent to optimize 3-pool splits with uint64
func (r *Router) optimizeThreeWaySplitFast(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount uint64, exactIn bool) *FastSplitQuoteResult {
	if len(pools) != 3 {
		return nil
	}

	p1, p2 := uint8(34), uint8(33)
	p3 := 100 - p1 - p2

	var bestResult *FastSplitQuoteResult
	bestResult, _ = r.calculateSplitFast(pools, inputMint, outputMint, totalAmount, []uint8{p1, p2, p3}, exactIn)

	if bestResult == nil {
		return nil
	}

	var prevBestOutput uint64
	if exactIn {
		prevBestOutput = bestResult.TotalAmountOut
	} else {
		prevBestOutput = bestResult.TotalAmountIn
	}

	step := uint8(5)
	improved := true
	iterations := 0
	maxIterations := 3

	for improved && step >= 2 && iterations < maxIterations {
		improved = false
		iterations++

		adjustments := []struct{ dp1, dp2 int8 }{
			{int8(step), -int8(step)}, {-int8(step), int8(step)},
			{int8(step), 0}, {-int8(step), 0},
			{0, int8(step)}, {0, -int8(step)},
		}

		for _, adj := range adjustments {
			newP1 := int(p1) + int(adj.dp1)
			newP2 := int(p2) + int(adj.dp2)
			newP3 := 100 - newP1 - newP2

			if newP1 < MinSplitPercent || newP1 > 100-2*MinSplitPercent ||
				newP2 < MinSplitPercent || newP2 > 100-2*MinSplitPercent ||
				newP3 < MinSplitPercent || newP3 > 100-2*MinSplitPercent {
				continue
			}

			split, err := r.calculateSplitFast(pools, inputMint, outputMint, totalAmount, []uint8{uint8(newP1), uint8(newP2), uint8(newP3)}, exactIn)
			if err == nil && r.isBetterSplitFast(split, bestResult, exactIn) {
				var currentOutput, newOutput uint64
				if exactIn {
					currentOutput = bestResult.TotalAmountOut
					newOutput = split.TotalAmountOut
				} else {
					currentOutput = bestResult.TotalAmountIn
					newOutput = split.TotalAmountIn
				}

				var diff uint64
				if newOutput > currentOutput {
					diff = newOutput - currentOutput
				} else {
					diff = currentOutput - newOutput
				}

				if currentOutput > 0 && (diff*10000)/currentOutput < 10 {
					bestResult = split
					return bestResult
				}

				bestResult = split
				p1, p2, p3 = uint8(newP1), uint8(newP2), uint8(newP3)
				improved = true
				break
			}
		}

		if !improved {
			step /= 2
			improved = step >= 2
		}
	}

	_ = prevBestOutput
	return bestResult
}

// calculateSplitFast calculates the output for a specific split configuration with uint64
func (r *Router) calculateSplitFast(pools []*domain.Pool, inputMint, outputMint solana.PublicKey, totalAmount uint64, percents []uint8, exactIn bool) (*FastSplitQuoteResult, error) {
	if len(pools) != len(percents) {
		return nil, ErrNoPoolFound
	}

	var sum uint8
	for _, p := range percents {
		sum += p
	}
	if sum != 100 {
		return nil, ErrNoPoolFound
	}

	splits := make([]FastSplitRoute, 0, len(pools))
	var totalOut, totalFee uint64
	totalImpact := uint32(0)

	for i, pool := range pools {
		if percents[i] < MinSplitPercent {
			continue
		}

		splitAmount := (totalAmount * uint64(percents[i])) / 100
		if splitAmount == 0 {
			continue
		}

		aToB := pool.TokenMintA == inputMint

		quote, err := r.FastQuoter(pool, splitAmount, aToB, exactIn)
		if err != nil {
			continue
		}

		splits = append(splits, FastSplitRoute{
			Pool:           pool,
			Percent:        percents[i],
			AmountIn:       quote.AmountIn,
			AmountOut:      quote.AmountOut,
			FeeAmount:      quote.Fee,
			AToB:           aToB,
			PriceImpactBps: quote.PriceImpactBps,
		})

		totalOut += quote.AmountOut
		totalFee += quote.Fee
		totalImpact += uint32(quote.PriceImpactBps) * uint32(percents[i])
	}

	if len(splits) == 0 {
		return nil, ErrNoPoolFound
	}

	avgImpact := uint16(totalImpact / 100)

	return &FastSplitQuoteResult{
		InputMint:      inputMint,
		OutputMint:     outputMint,
		Splits:         splits,
		TotalAmountIn:  totalAmount,
		TotalAmountOut: totalOut,
		TotalFee:       totalFee,
		PriceImpactBps: avgImpact,
	}, nil
}

// getSinglePoolQuoteFast gets a quote from a single pool using uint64
func (r *Router) getSinglePoolQuoteFast(pool *domain.Pool, inputMint, outputMint solana.PublicKey, amount uint64, exactIn bool) (*FastSplitQuoteResult, error) {
	aToB := pool.TokenMintA == inputMint

	quote, err := r.FastQuoter(pool, amount, aToB, exactIn)
	if err != nil {
		return nil, err
	}

	return &FastSplitQuoteResult{
		InputMint:  inputMint,
		OutputMint: outputMint,
		Splits: []FastSplitRoute{{
			Pool:           pool,
			Percent:        100,
			AmountIn:       quote.AmountIn,
			AmountOut:      quote.AmountOut,
			FeeAmount:      quote.Fee,
			AToB:           aToB,
			PriceImpactBps: quote.PriceImpactBps,
		}},
		TotalAmountIn:  quote.AmountIn,
		TotalAmountOut: quote.AmountOut,
		TotalFee:       quote.Fee,
		PriceImpactBps: quote.PriceImpactBps,
	}, nil
}

// isBetterSplitFast checks if newSplit is better than current best using uint64
func (r *Router) isBetterSplitFast(newSplit, bestSplit *FastSplitQuoteResult, exactIn bool) bool {
	if bestSplit == nil {
		return true
	}
	if newSplit == nil {
		return false
	}

	if exactIn {
		return newSplit.TotalAmountOut > bestSplit.TotalAmountOut
	}
	return newSplit.TotalAmountIn < bestSplit.TotalAmountIn
}

// hopResult holds the result of a parallel 2-hop quote calculation
type hopResult struct {
	quote *domain.MultiHopQuoteResult
	err   error
}

// poolQuoteKey is the key for caching pool quotes within a request
// Uses uint64 hash instead of string for zero-allocation lookups
type poolQuoteKey uint64

// makePoolQuoteKey creates a cache key using FNV-1a hash (zero allocation)
func makePoolQuoteKey(poolAddr solana.PublicKey, amount *big.Int, aToB, exactIn bool) poolQuoteKey {
	h := uint64(fnvOffset64)

	// Hash pool address (32 bytes)
	for _, b := range poolAddr {
		h ^= uint64(b)
		h *= fnvPrime64
	}

	// Hash amount as uint64 if it fits (most common case)
	if amount != nil && amount.IsUint64() {
		amountU64 := amount.Uint64()
		for i := 0; i < 8; i++ {
			h ^= (amountU64 >> (i * 8)) & 0xFF
			h *= fnvPrime64
		}
	} else if amount != nil {
		// Fallback for large amounts (rare)
		bytes := amount.Bytes()
		for _, b := range bytes {
			h ^= uint64(b)
			h *= fnvPrime64
		}
	}

	// Hash flags
	var flags uint64
	if aToB {
		flags |= 1
	}
	if exactIn {
		flags |= 2
	}
	h ^= flags
	h *= fnvPrime64

	return poolQuoteKey(h)
}

// requestScopedCache caches pool quotes within a single GetMultiHopQuote call
// Optimized: uses uint64 hash keys instead of string keys (zero allocation)
type requestScopedCache struct {
	mu     sync.RWMutex
	quotes map[poolQuoteKey]*domain.SwapQuote
}

func newRequestScopedCache() *requestScopedCache {
	return &requestScopedCache{
		quotes: make(map[poolQuoteKey]*domain.SwapQuote, 32),
	}
}

func (c *requestScopedCache) Get(pool *domain.Pool, amount *big.Int, aToB, exactIn bool) (*domain.SwapQuote, bool) {
	key := makePoolQuoteKey(pool.Address, amount, aToB, exactIn)
	c.mu.RLock()
	quote, ok := c.quotes[key]
	c.mu.RUnlock()
	return quote, ok
}

func (c *requestScopedCache) Set(pool *domain.Pool, amount *big.Int, aToB, exactIn bool, quote *domain.SwapQuote) {
	key := makePoolQuoteKey(pool.Address, amount, aToB, exactIn)
	c.mu.Lock()
	c.quotes[key] = quote
	c.mu.Unlock()
}

func (r *Router) GetMultiHopQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	// Try direct route first - fastest path
	directQuote, directErr := r.getDirectQuote(inputMint, outputMint, amount, exactIn)

	// If direct route exists with good liquidity, return immediately (skip multi-hop search)
	if directQuote != nil && directQuote.PriceImpactBps < 100 {
		return directQuote, nil
	}

	// For large trades with high price impact, try split routing on direct pools
	if directQuote != nil && directQuote.PriceImpactBps >= 100 {
		splitQuote, err := r.GetSplitQuote(inputMint, outputMint, amount, exactIn)

		if err == nil && splitQuote != nil && len(splitQuote.Splits) > 1 {
			splitResult := r.ConvertSplitToMultiHop(splitQuote)
			// Use split if it gives better output
			if exactIn && splitResult.AmountOut.Cmp(directQuote.AmountOut) > 0 {
				return splitResult, nil
			} else if !exactIn && splitResult.AmountIn.Cmp(directQuote.AmountIn) < 0 {
				return splitResult, nil
			}
		}
	}

	// Use Bidirectional BFS to find multi-hop paths (up to 4 hops).
	// V0 transactions with Address Lookup Tables support 3+ hop routes.
	paths := r.Graph.FindPaths(inputMint, outputMint, 4)

	if len(paths) > 0 {
		var bestQuote *domain.MultiHopQuoteResult

		// Use Splitter for multi-path optimization when multiple paths available
		if len(paths) > 1 {
			splitter := NewSplitter(r)
			splitResult, err := splitter.FindOptimalSplit(inputMint, outputMint, amount, paths, exactIn)
			if err == nil && splitResult != nil {
				bestQuote = splitResult.ToMultiHopQuote()
			}
		}

		// Fallback: evaluate single best path if splitter didn't produce result
		if bestQuote == nil {
			multiHopQuote, err := r.evaluatePaths(paths, amount, exactIn)
			if err == nil && multiHopQuote != nil {
				bestQuote = multiHopQuote
			}
		}

		if bestQuote != nil {
			// Compare with direct quote if it exists
			if directQuote != nil {
				if exactIn && bestQuote.AmountOut.Cmp(directQuote.AmountOut) > 0 {
					return bestQuote, nil
				} else if !exactIn && bestQuote.AmountIn.Cmp(directQuote.AmountIn) < 0 {
					return bestQuote, nil
				}
				return directQuote, nil
			}
			return bestQuote, nil
		}
	}

	// Return direct quote if available
	if directQuote != nil {
		return directQuote, nil
	}

	if directErr != nil {
		return nil, directErr
	}
	return nil, ErrNoRoute
}

// GetMultiHopQuoteFast is the zero-allocation version using uint64 internally
// Converts to domain.MultiHopQuoteResult only at return (API boundary)
// Full feature parity with GetMultiHopQuote: 4-hop support, split routing, multi-path splitter
func (r *Router) GetMultiHopQuoteFast(inputMint, outputMint solana.PublicKey, amount uint64, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	if r.FastQuoter == nil {
		return r.GetMultiHopQuote(inputMint, outputMint, new(big.Int).SetUint64(amount), exactIn)
	}

	// Try direct route first - fastest path
	directQuote, directErr := r.getDirectQuoteFast(inputMint, outputMint, amount, exactIn)

	// If direct route exists with good liquidity, return immediately (skip multi-hop search)
	if directQuote != nil && directQuote.PriceImpactBps < 100 {
		return directQuote.ToMultiHopQuoteResult(), nil
	}

	// For large trades with high price impact, try split routing on direct pools
	if directQuote != nil && directQuote.PriceImpactBps >= 100 {
		splitQuote, err := r.GetSplitQuoteFast(inputMint, outputMint, amount, exactIn)
		if err == nil && splitQuote != nil && len(splitQuote.Splits) > 1 {
			splitResult := splitQuote.ToMultiHopQuoteResult()
			// Use split if it gives better output
			if exactIn && splitResult.AmountOut.Cmp(directQuote.ToMultiHopQuoteResult().AmountOut) > 0 {
				return splitResult, nil
			} else if !exactIn && splitResult.AmountIn.Cmp(directQuote.ToMultiHopQuoteResult().AmountIn) < 0 {
				return splitResult, nil
			}
		}
	}

	// Use Bidirectional BFS to find multi-hop paths (up to 4 hops)
	// V0 transactions with Address Lookup Tables support 3+ hop routes
	paths := r.Graph.FindPaths(inputMint, outputMint, 4)

	if len(paths) > 0 {
		var bestQuote *FastMultiHopQuoteResult

		// Use Splitter for multi-path optimization when multiple paths available
		if len(paths) > 1 {
			splitter := NewSplitter(r)
			splitResult, err := splitter.FindOptimalSplitFast(inputMint, outputMint, amount, paths, exactIn)
			if err == nil && splitResult != nil {
				// Convert FastSplitResult to FastMultiHopQuoteResult
				if len(splitResult.Splits) == 1 {
					bestQuote = &FastMultiHopQuoteResult{
						Route:          splitResult.Splits[0].Path,
						Hops:           splitResult.Splits[0].Hops,
						AmountIn:       splitResult.TotalAmountIn,
						AmountOut:      splitResult.TotalAmountOut,
						TotalFee:       splitResult.TotalFee,
						PriceImpactBps: splitResult.AvgImpactBps,
					}
				} else if len(splitResult.Splits) > 1 {
					// Use dominant path's hops for multi-path
					dominantPath := splitResult.Splits[0]
					bestQuote = &FastMultiHopQuoteResult{
						Route:          dominantPath.Path,
						Hops:           dominantPath.Hops,
						AmountIn:       splitResult.TotalAmountIn,
						AmountOut:      splitResult.TotalAmountOut,
						TotalFee:       splitResult.TotalFee,
						PriceImpactBps: splitResult.AvgImpactBps,
					}
				}
			}
		}

		// Fallback: evaluate single best path if splitter didn't produce result
		if bestQuote == nil {
			multiHopQuote, err := r.evaluatePathsFast(paths, amount, exactIn)
			if err == nil && multiHopQuote != nil {
				bestQuote = multiHopQuote
			}
		}

		if bestQuote != nil {
			// Compare with direct quote if it exists
			if directQuote != nil {
				if exactIn && bestQuote.AmountOut > directQuote.AmountOut {
					return bestQuote.ToMultiHopQuoteResult(), nil
				} else if !exactIn && bestQuote.AmountIn < directQuote.AmountIn {
					return bestQuote.ToMultiHopQuoteResult(), nil
				}
				return directQuote.ToMultiHopQuoteResult(), nil
			}
			return bestQuote.ToMultiHopQuoteResult(), nil
		}
	}

	// Return direct quote if available
	if directQuote != nil {
		return directQuote.ToMultiHopQuoteResult(), nil
	}

	if directErr != nil {
		return nil, directErr
	}
	return nil, ErrNoRoute
}

// getDirectQuoteFast gets direct quote using uint64 (zero big.Int allocation)
func (r *Router) getDirectQuoteFast(inputMint, outputMint solana.PublicKey, amount uint64, exactIn bool) (*FastMultiHopQuoteResult, error) {
	directPools := r.Graph.GetDirectRoutesForPair(inputMint, outputMint)
	if len(directPools) == 0 {
		return nil, ErrNoPoolFound
	}

	var bestHop *FastHopQuote
	var bestOutput uint64

	for _, pool := range directPools {
		aToB := pool.TokenMintA == inputMint && pool.TokenMintB == outputMint

		quote, err := r.FastQuoter(pool, amount, aToB, exactIn)
		if err != nil {
			continue
		}

		// Compare and keep best
		isBetter := false
		if bestHop == nil {
			isBetter = true
		} else if exactIn && quote.AmountOut > bestOutput {
			isBetter = true
		} else if !exactIn && quote.AmountIn < bestOutput {
			isBetter = true
		}

		if isBetter {
			bestHop = &FastHopQuote{
				Pool:           pool,
				AmountIn:       quote.AmountIn,
				AmountOut:      quote.AmountOut,
				FeeAmount:      quote.Fee,
				AToB:           aToB,
				PriceImpactBps: quote.PriceImpactBps,
			}
			if exactIn {
				bestOutput = quote.AmountOut
			} else {
				bestOutput = quote.AmountIn
			}
		}
	}

	if bestHop == nil {
		return nil, ErrNoPoolFound
	}

	return &FastMultiHopQuoteResult{
		Route:          []solana.PublicKey{inputMint, outputMint},
		Hops:           []FastHopQuote{*bestHop},
		AmountIn:       bestHop.AmountIn,
		AmountOut:      bestHop.AmountOut,
		TotalFee:       bestHop.FeeAmount,
		PriceImpactBps: bestHop.PriceImpactBps,
	}, nil
}

// directQuoteResult holds the result of a parallel direct quote calculation
type directQuoteResult struct {
	hop   domain.HopQuote
	valid bool
}

func (r *Router) getDirectQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	directPools := r.Graph.GetDirectRoutesForPair(inputMint, outputMint)
	if len(directPools) == 0 {
		return nil, ErrNoPoolFound
	}

	// For small pool counts, sequential is faster (goroutine overhead)
	if len(directPools) <= 3 {
		return r.getDirectQuoteSequential(directPools, inputMint, outputMint, amount, exactIn)
	}

	// Parallel evaluation for larger pool counts
	return r.getDirectQuoteParallel(directPools, inputMint, outputMint, amount, exactIn)
}

func (r *Router) getDirectQuoteSequential(directPools []*domain.Pool, inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	var bestHop domain.HopQuote
	hasBest := false

	for _, pool := range directPools {
		aToB := pool.TokenMintA == inputMint && pool.TokenMintB == outputMint

		var quote *domain.SwapQuote
		var err error

		quote, err = r.Quoter(pool, amount, aToB, exactIn)

		if err != nil {
			continue
		}

		if hasBest && bestHop.PriceImpactBps == 0 && quote.PriceImpactBps > 0 {
			continue
		}

		hop := domain.HopQuote{
			Pool:           pool,
			AmountIn:       quote.AmountIn,
			AmountOut:      quote.AmountOut,
			FeeAmount:      quote.Fee,
			AToB:           aToB,
			PriceImpactBps: quote.PriceImpactBps,
		}

		if !hasBest {
			bestHop = hop
			hasBest = true
			if quote.PriceImpactBps == 0 {
				break
			}
			continue
		}

		if exactIn && quote.AmountOut.Cmp(bestHop.AmountOut) > 0 {
			bestHop = hop
			if quote.PriceImpactBps == 0 {
				break
			}
		} else if !exactIn && quote.AmountIn.Cmp(bestHop.AmountIn) < 0 {
			bestHop = hop
			if quote.PriceImpactBps == 0 {
				break
			}
		}
	}

	if !hasBest {
		return nil, ErrNoPoolFound
	}

	return &domain.MultiHopQuoteResult{
		Route:          []solana.PublicKey{inputMint, outputMint},
		Hops:           []domain.HopQuote{bestHop},
		AmountIn:       bestHop.AmountIn,
		AmountOut:      bestHop.AmountOut,
		TotalFee:       bestHop.FeeAmount,
		PriceImpactBps: bestHop.PriceImpactBps,
	}, nil
}

func (r *Router) getDirectQuoteParallel(directPools []*domain.Pool, inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	results := make([]directQuoteResult, len(directPools))
	var wg sync.WaitGroup

	for i, pool := range directPools {
		wg.Add(1)
		go func(idx int, p *domain.Pool) {
			defer wg.Done()

			aToB := p.TokenMintA == inputMint && p.TokenMintB == outputMint

			quote, err := r.Quoter(p, amount, aToB, exactIn)

			if err != nil {
				return
			}

			results[idx] = directQuoteResult{
				hop: domain.HopQuote{
					Pool:           p,
					AmountIn:       quote.AmountIn,
					AmountOut:      quote.AmountOut,
					FeeAmount:      quote.Fee,
					AToB:           aToB,
					PriceImpactBps: quote.PriceImpactBps,
				},
				valid: true,
			}
		}(i, pool)
	}

	wg.Wait()

	// Find best result from parallel computations
	var bestHop domain.HopQuote
	hasBest := false

	for _, res := range results {
		if !res.valid {
			continue
		}

		if !hasBest {
			bestHop = res.hop
			hasBest = true
			continue
		}

		// Skip inferior quotes if we have zero impact
		if bestHop.PriceImpactBps == 0 && res.hop.PriceImpactBps > 0 {
			continue
		}

		if exactIn && res.hop.AmountOut.Cmp(bestHop.AmountOut) > 0 {
			bestHop = res.hop
		} else if !exactIn && res.hop.AmountIn.Cmp(bestHop.AmountIn) < 0 {
			bestHop = res.hop
		}
	}

	if !hasBest {
		return nil, ErrNoPoolFound
	}

	return &domain.MultiHopQuoteResult{
		Route:          []solana.PublicKey{inputMint, outputMint},
		Hops:           []domain.HopQuote{bestHop},
		AmountIn:       bestHop.AmountIn,
		AmountOut:      bestHop.AmountOut,
		TotalFee:       bestHop.FeeAmount,
		PriceImpactBps: bestHop.PriceImpactBps,
	}, nil
}

// cachedFirstHopQuote holds pre-computed first hop quote
type cachedFirstHopQuote struct {
	pool  *domain.Pool
	quote *domain.SwapQuote
	aToB  bool
}

// twoHopResult holds result of a two-hop quote combination
type twoHopResult struct {
	firstHop    cachedFirstHopQuote
	secondPool  *domain.Pool
	secondQuote *domain.SwapQuote
	secondAToB  bool
	amountOut   *big.Int
	valid       bool
}

func (r *Router) getTwoHopQuoteExactIn(inputMint, intermediate, outputMint solana.PublicKey, amountIn *big.Int) (*domain.MultiHopQuoteResult, error) {

	firstHopPools := r.Graph.GetDirectRoutesForPair(inputMint, intermediate)
	secondHopPools := r.Graph.GetDirectRoutesForPair(intermediate, outputMint)

	if len(firstHopPools) == 0 || len(secondHopPools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Parallel first hop quote calculations
	firstQuotes := make([]cachedFirstHopQuote, len(firstHopPools))
	var wgFirst sync.WaitGroup
	for i, pool := range firstHopPools {
		wgFirst.Add(1)
		go func(idx int, p *domain.Pool) {
			defer wgFirst.Done()
			aToB := p.TokenMintA == inputMint && p.TokenMintB == intermediate
			quote, err := r.Quoter(p, amountIn, aToB, true)
			if err == nil {
				firstQuotes[idx] = cachedFirstHopQuote{pool: p, quote: quote, aToB: aToB}
			}
		}(i, pool)
	}
	wgFirst.Wait()

	// Filter valid first hop quotes
	validFirstQuotes := make([]cachedFirstHopQuote, 0, len(firstHopPools))
	for _, fq := range firstQuotes {
		if fq.quote != nil {
			validFirstQuotes = append(validFirstQuotes, fq)
		}
	}

	if len(validFirstQuotes) == 0 {
		return nil, ErrNoPoolFound
	}

	// Parallel second hop combinations
	totalCombinations := len(validFirstQuotes) * len(secondHopPools)
	results := make([]twoHopResult, totalCombinations)
	var wgSecond sync.WaitGroup

	idx := 0
	for _, first := range validFirstQuotes {
		for _, secondPool := range secondHopPools {
			wgSecond.Add(1)
			go func(resIdx int, fh cachedFirstHopQuote, sp *domain.Pool) {
				defer wgSecond.Done()
				aToB2 := sp.TokenMintA == intermediate && sp.TokenMintB == outputMint
				secondQuote, err := r.Quoter(sp, fh.quote.AmountOut, aToB2, true)
				if err == nil {
					results[resIdx] = twoHopResult{
						firstHop:    fh,
						secondPool:  sp,
						secondQuote: secondQuote,
						secondAToB:  aToB2,
						amountOut:   secondQuote.AmountOut,
						valid:       true,
					}
				}
			}(idx, first, secondPool)
			idx++
		}
	}
	wgSecond.Wait()

	// Find best result
	var bestResult twoHopResult
	hasBest := false
	for _, res := range results {
		if !res.valid {
			continue
		}
		if !hasBest || res.amountOut.Cmp(bestResult.amountOut) > 0 {
			bestResult = res
			hasBest = true
		}
	}

	if !hasBest {
		return nil, ErrNoPoolFound
	}

	totalFee := GetBigInt()
	totalFee.Add(bestResult.firstHop.quote.Fee, bestResult.secondQuote.Fee)
	finalFee := new(big.Int).Set(totalFee)
	PutBigInt(totalFee)

	combinedImpact := uint16(min(int(bestResult.firstHop.quote.PriceImpactBps)+int(bestResult.secondQuote.PriceImpactBps), 10000))

	return &domain.MultiHopQuoteResult{
		Route: []solana.PublicKey{inputMint, intermediate, outputMint},
		Hops: []domain.HopQuote{
			{Pool: bestResult.firstHop.pool, AmountIn: bestResult.firstHop.quote.AmountIn, AmountOut: bestResult.firstHop.quote.AmountOut, FeeAmount: bestResult.firstHop.quote.Fee, AToB: bestResult.firstHop.aToB, PriceImpactBps: bestResult.firstHop.quote.PriceImpactBps},
			{Pool: bestResult.secondPool, AmountIn: bestResult.secondQuote.AmountIn, AmountOut: bestResult.secondQuote.AmountOut, FeeAmount: bestResult.secondQuote.Fee, AToB: bestResult.secondAToB, PriceImpactBps: bestResult.secondQuote.PriceImpactBps},
		},
		AmountIn:       amountIn,
		AmountOut:      bestResult.amountOut,
		TotalFee:       finalFee,
		PriceImpactBps: combinedImpact,
	}, nil
}

// cachedSecondHopQuote holds pre-computed second hop quote for ExactOut
type cachedSecondHopQuote struct {
	pool  *domain.Pool
	quote *domain.SwapQuote
	aToB  bool
}

// twoHopResultOut holds result of a two-hop quote combination for ExactOut
type twoHopResultOut struct {
	secondHop  cachedSecondHopQuote
	firstPool  *domain.Pool
	firstQuote *domain.SwapQuote
	firstAToB  bool
	amountIn   *big.Int
	valid      bool
}

func (r *Router) getTwoHopQuoteExactOut(inputMint, intermediate, outputMint solana.PublicKey, amountOut *big.Int) (*domain.MultiHopQuoteResult, error) {
	firstHopPools := r.Graph.GetDirectRoutesForPair(inputMint, intermediate)
	secondHopPools := r.Graph.GetDirectRoutesForPair(intermediate, outputMint)

	if len(firstHopPools) == 0 || len(secondHopPools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Parallel second hop quote calculations
	secondQuotes := make([]cachedSecondHopQuote, len(secondHopPools))
	var wgSecond sync.WaitGroup
	for i, pool := range secondHopPools {
		wgSecond.Add(1)
		go func(idx int, p *domain.Pool) {
			defer wgSecond.Done()
			aToB := p.TokenMintA == intermediate && p.TokenMintB == outputMint
			quote, err := r.Quoter(p, amountOut, aToB, false)
			if err == nil {
				secondQuotes[idx] = cachedSecondHopQuote{pool: p, quote: quote, aToB: aToB}
			}
		}(i, pool)
	}
	wgSecond.Wait()

	// Filter valid second hop quotes
	validSecondQuotes := make([]cachedSecondHopQuote, 0, len(secondHopPools))
	for _, sq := range secondQuotes {
		if sq.quote != nil {
			validSecondQuotes = append(validSecondQuotes, sq)
		}
	}

	if len(validSecondQuotes) == 0 {
		return nil, ErrNoPoolFound
	}

	// Parallel first hop combinations
	totalCombinations := len(validSecondQuotes) * len(firstHopPools)
	results := make([]twoHopResultOut, totalCombinations)
	var wgFirst sync.WaitGroup

	idx := 0
	for _, second := range validSecondQuotes {
		for _, firstPool := range firstHopPools {
			wgFirst.Add(1)
			go func(resIdx int, sh cachedSecondHopQuote, fp *domain.Pool) {
				defer wgFirst.Done()
				aToB1 := fp.TokenMintA == inputMint && fp.TokenMintB == intermediate
				firstQuote, err := r.Quoter(fp, sh.quote.AmountIn, aToB1, false)
				if err == nil {
					results[resIdx] = twoHopResultOut{
						secondHop:  sh,
						firstPool:  fp,
						firstQuote: firstQuote,
						firstAToB:  aToB1,
						amountIn:   firstQuote.AmountIn,
						valid:      true,
					}
				}
			}(idx, second, firstPool)
			idx++
		}
	}
	wgFirst.Wait()

	// Find best result (lowest amountIn for ExactOut)
	var bestResult twoHopResultOut
	hasBest := false
	for _, res := range results {
		if !res.valid {
			continue
		}
		if !hasBest || res.amountIn.Cmp(bestResult.amountIn) < 0 {
			bestResult = res
			hasBest = true
		}
	}

	if !hasBest {
		return nil, ErrNoPoolFound
	}

	totalFee := GetBigInt()
	totalFee.Add(bestResult.firstQuote.Fee, bestResult.secondHop.quote.Fee)
	finalFee := new(big.Int).Set(totalFee)
	PutBigInt(totalFee)

	combinedImpact := uint16(min(int(bestResult.firstQuote.PriceImpactBps)+int(bestResult.secondHop.quote.PriceImpactBps), 10000))

	return &domain.MultiHopQuoteResult{
		Route: []solana.PublicKey{inputMint, intermediate, outputMint},
		Hops: []domain.HopQuote{
			{Pool: bestResult.firstPool, AmountIn: bestResult.firstQuote.AmountIn, AmountOut: bestResult.firstQuote.AmountOut, FeeAmount: bestResult.firstQuote.Fee, AToB: bestResult.firstAToB, PriceImpactBps: bestResult.firstQuote.PriceImpactBps},
			{Pool: bestResult.secondHop.pool, AmountIn: bestResult.secondHop.quote.AmountIn, AmountOut: bestResult.secondHop.quote.AmountOut, FeeAmount: bestResult.secondHop.quote.Fee, AToB: bestResult.secondHop.aToB, PriceImpactBps: bestResult.secondHop.quote.PriceImpactBps},
		},
		AmountIn:       bestResult.amountIn,
		AmountOut:      amountOut,
		TotalFee:       finalFee,
		PriceImpactBps: combinedImpact,
	}, nil
}

// getTwoHopQuoteExactInWithCache uses request-scoped cache to avoid redundant calculations
func (r *Router) getTwoHopQuoteExactInWithCache(inputMint, intermediate, outputMint solana.PublicKey, amountIn *big.Int, cache *requestScopedCache) (*domain.MultiHopQuoteResult, error) {
	firstHopPools := r.Graph.GetDirectRoutesForPair(inputMint, intermediate)
	secondHopPools := r.Graph.GetDirectRoutesForPair(intermediate, outputMint)

	if len(firstHopPools) == 0 || len(secondHopPools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Get first hop quotes with caching
	firstQuotes := make([]cachedFirstHopQuote, 0, len(firstHopPools))
	for _, pool := range firstHopPools {
		aToB := pool.TokenMintA == inputMint && pool.TokenMintB == intermediate

		// Try cache first
		if cachedQ, ok := cache.Get(pool, amountIn, aToB, true); ok {
			firstQuotes = append(firstQuotes, cachedFirstHopQuote{pool: pool, quote: cachedQ, aToB: aToB})
			continue
		}

		quote, err := r.Quoter(pool, amountIn, aToB, true)
		if err == nil {
			cache.Set(pool, amountIn, aToB, true, quote)
			firstQuotes = append(firstQuotes, cachedFirstHopQuote{pool: pool, quote: quote, aToB: aToB})
		}
	}

	if len(firstQuotes) == 0 {
		return nil, ErrNoPoolFound
	}

	var bestResult *domain.MultiHopQuoteResult
	var bestAmountOut *big.Int

	route := make([]solana.PublicKey, 3)
	route[0] = inputMint
	route[1] = intermediate
	route[2] = outputMint

	for _, first := range firstQuotes {
		for _, secondPool := range secondHopPools {
			aToB2 := secondPool.TokenMintA == intermediate && secondPool.TokenMintB == outputMint

			// Try cache first for second hop
			var secondQuote *domain.SwapQuote
			if cachedQ, ok := cache.Get(secondPool, first.quote.AmountOut, aToB2, true); ok {
				secondQuote = cachedQ
			} else {
				q, err := r.Quoter(secondPool, first.quote.AmountOut, aToB2, true)
				if err != nil {
					continue
				}
				cache.Set(secondPool, first.quote.AmountOut, aToB2, true, q)
				secondQuote = q
			}

			if bestResult == nil || secondQuote.AmountOut.Cmp(bestAmountOut) > 0 {
				bestAmountOut = secondQuote.AmountOut
				totalFee := new(big.Int).Add(first.quote.Fee, secondQuote.Fee)
				combinedImpact := uint16(min(int(first.quote.PriceImpactBps)+int(secondQuote.PriceImpactBps), 10000))

				bestResult = &domain.MultiHopQuoteResult{
					Route: route,
					Hops: []domain.HopQuote{
						{Pool: first.pool, AmountIn: first.quote.AmountIn, AmountOut: first.quote.AmountOut, FeeAmount: first.quote.Fee, AToB: first.aToB, PriceImpactBps: first.quote.PriceImpactBps},
						{Pool: secondPool, AmountIn: secondQuote.AmountIn, AmountOut: secondQuote.AmountOut, FeeAmount: secondQuote.Fee, AToB: aToB2, PriceImpactBps: secondQuote.PriceImpactBps},
					},
					AmountIn:       amountIn,
					AmountOut:      secondQuote.AmountOut,
					TotalFee:       totalFee,
					PriceImpactBps: combinedImpact,
				}
			}
		}
	}

	if bestResult == nil {
		return nil, ErrNoPoolFound
	}

	return bestResult, nil
}

// getTwoHopQuoteExactOutWithCache uses request-scoped cache to avoid redundant calculations
func (r *Router) getTwoHopQuoteExactOutWithCache(inputMint, intermediate, outputMint solana.PublicKey, amountOut *big.Int, cache *requestScopedCache) (*domain.MultiHopQuoteResult, error) {
	firstHopPools := r.Graph.GetDirectRoutesForPair(inputMint, intermediate)
	secondHopPools := r.Graph.GetDirectRoutesForPair(intermediate, outputMint)

	if len(firstHopPools) == 0 || len(secondHopPools) == 0 {
		return nil, ErrNoPoolFound
	}

	// Get second hop quotes with caching
	secondQuotes := make([]cachedSecondHopQuote, 0, len(secondHopPools))
	for _, pool := range secondHopPools {
		aToB := pool.TokenMintA == intermediate && pool.TokenMintB == outputMint

		// Try cache first
		if cachedQ, ok := cache.Get(pool, amountOut, aToB, false); ok {
			secondQuotes = append(secondQuotes, cachedSecondHopQuote{pool: pool, quote: cachedQ, aToB: aToB})
			continue
		}

		quote, err := r.Quoter(pool, amountOut, aToB, false)
		if err == nil {
			cache.Set(pool, amountOut, aToB, false, quote)
			secondQuotes = append(secondQuotes, cachedSecondHopQuote{pool: pool, quote: quote, aToB: aToB})
		}
	}

	if len(secondQuotes) == 0 {
		return nil, ErrNoPoolFound
	}

	var bestResult *domain.MultiHopQuoteResult
	var bestAmountIn *big.Int

	route := make([]solana.PublicKey, 3)
	route[0] = inputMint
	route[1] = intermediate
	route[2] = outputMint

	for _, second := range secondQuotes {
		for _, firstPool := range firstHopPools {
			aToB1 := firstPool.TokenMintA == inputMint && firstPool.TokenMintB == intermediate

			// Try cache first for first hop
			var firstQuote *domain.SwapQuote
			if cachedQ, ok := cache.Get(firstPool, second.quote.AmountIn, aToB1, false); ok {
				firstQuote = cachedQ
			} else {
				q, err := r.Quoter(firstPool, second.quote.AmountIn, aToB1, false)
				if err != nil {
					continue
				}
				cache.Set(firstPool, second.quote.AmountIn, aToB1, false, q)
				firstQuote = q
			}

			if bestResult == nil || firstQuote.AmountIn.Cmp(bestAmountIn) < 0 {
				bestAmountIn = firstQuote.AmountIn
				totalFee := new(big.Int).Add(firstQuote.Fee, second.quote.Fee)
				combinedImpact := uint16(min(int(firstQuote.PriceImpactBps)+int(second.quote.PriceImpactBps), 10000))

				bestResult = &domain.MultiHopQuoteResult{
					Route: route,
					Hops: []domain.HopQuote{
						{Pool: firstPool, AmountIn: firstQuote.AmountIn, AmountOut: firstQuote.AmountOut, FeeAmount: firstQuote.Fee, AToB: aToB1, PriceImpactBps: firstQuote.PriceImpactBps},
						{Pool: second.pool, AmountIn: second.quote.AmountIn, AmountOut: second.quote.AmountOut, FeeAmount: second.quote.Fee, AToB: second.aToB, PriceImpactBps: second.quote.PriceImpactBps},
					},
					AmountIn:       firstQuote.AmountIn,
					AmountOut:      amountOut,
					TotalFee:       totalFee,
					PriceImpactBps: combinedImpact,
				}
			}
		}
	}

	if bestResult == nil {
		return nil, ErrNoPoolFound
	}

	return bestResult, nil
}
