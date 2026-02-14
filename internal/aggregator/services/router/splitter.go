package router

import (
	"math/big"
	"sort"
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
)

// MaxSplitPaths is the maximum number of paths to split across
const MaxSplitPaths = 3

// SplitPath represents a path with its allocation
type SplitPath struct {
	Path      []solana.PublicKey
	Hops      []domain.HopQuote // Full hop chain with Pool refs from evaluation
	Percent   uint8             // Allocation percentage (0-100)
	AmountIn  *big.Int
	AmountOut *big.Int
	FeeAmount *big.Int
	ImpactBps uint16
}

// SplitResult holds the optimized split across multiple paths
type SplitResult struct {
	Splits         []SplitPath
	TotalAmountIn  *big.Int
	TotalAmountOut *big.Int
	TotalFee       *big.Int
	AvgImpactBps   uint16
}

// Splitter optimizes trade volume distribution across multiple paths
type Splitter struct {
	router            *Router
	admmWorkspacePool *sync.Pool // Pool of ADMMWorkspace for zero-alloc solving
}

// NewSplitter creates a new splitter instance
func NewSplitter(router *Router) *Splitter {
	return &Splitter{
		router: router,
		admmWorkspacePool: &sync.Pool{
			New: func() interface{} {
				return NewADMMWorkspace(MaxSplitPaths)
			},
		},
	}
}

// FindOptimalSplit finds the optimal split across multiple paths
// Uses Golden-Section search for 2 paths, Iterative Greedy for 3+ paths
func (s *Splitter) FindOptimalSplit(inputMint, outputMint solana.PublicKey, amount *big.Int, paths [][]solana.PublicKey, exactIn bool) (*SplitResult, error) {
	if len(paths) == 0 {
		return nil, ErrNoRoute
	}

	// Limit paths to evaluate
	if len(paths) > MaxSplitPaths {
		paths = paths[:MaxSplitPaths]
	}

	// If only one path, no split needed
	if len(paths) == 1 {
		return s.evaluateSinglePath(paths[0], amount, exactIn)
	}

	// For exactly 2 paths: use Golden-Section search (mathematically optimal)
	if len(paths) == 2 {
		return s.findOptimalTwoPathSplit(paths[0], paths[1], amount, exactIn)
	}

	// For 3+ paths: use Iterative Greedy Heuristic
	return s.findOptimalMultiPathSplit(paths, amount, exactIn)
}

// findOptimalTwoPathSplit uses Golden-Section search for optimal 2-path split
func (s *Splitter) findOptimalTwoPathSplit(path1, path2 []solana.PublicKey, amount *big.Int, exactIn bool) (*SplitResult, error) {
	// Try Golden-Section optimization
	gsResult := s.GoldenSectionSplit(path1, path2, amount, exactIn)
	if gsResult != nil {
		return gsResult.ToSplitResult(path1, path2), nil
	}

	// Fallback: evaluate both paths individually and pick the best
	quote1, err1 := s.router.evaluateSinglePath(path1, amount, exactIn)
	quote2, err2 := s.router.evaluateSinglePath(path2, amount, exactIn)

	if err1 != nil && err2 != nil {
		return nil, ErrNoRoute
	}

	// Pick the better single path
	var bestPath []solana.PublicKey
	var bestQuote *domain.MultiHopQuoteResult

	if err1 == nil && err2 == nil {
		if exactIn {
			if quote1.AmountOut.Cmp(quote2.AmountOut) >= 0 {
				bestPath, bestQuote = path1, quote1
			} else {
				bestPath, bestQuote = path2, quote2
			}
		} else {
			if quote1.AmountIn.Cmp(quote2.AmountIn) <= 0 {
				bestPath, bestQuote = path1, quote1
			} else {
				bestPath, bestQuote = path2, quote2
			}
		}
	} else if err1 == nil {
		bestPath, bestQuote = path1, quote1
	} else {
		bestPath, bestQuote = path2, quote2
	}

	return &SplitResult{
		Splits: []SplitPath{{
			Path:      bestPath,
			Hops:      bestQuote.Hops,
			Percent:   100,
			AmountIn:  bestQuote.AmountIn,
			AmountOut: bestQuote.AmountOut,
			FeeAmount: bestQuote.TotalFee,
			ImpactBps: bestQuote.PriceImpactBps,
		}},
		TotalAmountIn:  bestQuote.AmountIn,
		TotalAmountOut: bestQuote.AmountOut,
		TotalFee:       bestQuote.TotalFee,
		AvgImpactBps:   bestQuote.PriceImpactBps,
	}, nil
}

// findOptimalMultiPathSplit uses ADMM solver for 3+ paths
func (s *Splitter) findOptimalMultiPathSplit(paths [][]solana.PublicKey, amount *big.Int, exactIn bool) (*SplitResult, error) {
	solver := NewADMMSolver(s.router)

	// Get workspace from pool
	ws := s.admmWorkspacePool.Get().(*ADMMWorkspace)
	defer s.admmWorkspacePool.Put(ws)

	allocs := solver.Solve(paths, amount, exactIn, ws)

	if len(allocs) > 0 {
		result := AllocationsToSplitResult(allocs)
		if result != nil && result.TotalAmountOut != nil && result.TotalAmountOut.Sign() > 0 {
			return result, nil
		}
	}

	// Fallback: evaluate all paths individually and pick the best
	return s.fallbackBestSinglePath(paths, amount, exactIn)
}

// fallbackBestSinglePath evaluates all paths and returns the best single path
func (s *Splitter) fallbackBestSinglePath(paths [][]solana.PublicKey, amount *big.Int, exactIn bool) (*SplitResult, error) {
	var bestResult *SplitResult

	for _, path := range paths {
		result, err := s.evaluateSinglePath(path, amount, exactIn)
		if err != nil {
			continue
		}

		if bestResult == nil {
			bestResult = result
			continue
		}

		if exactIn && result.TotalAmountOut.Cmp(bestResult.TotalAmountOut) > 0 {
			bestResult = result
		} else if !exactIn && result.TotalAmountIn.Cmp(bestResult.TotalAmountIn) < 0 {
			bestResult = result
		}
	}

	if bestResult == nil {
		return nil, ErrNoRoute
	}
	return bestResult, nil
}

// evaluateSinglePath evaluates a single path as a split result
func (s *Splitter) evaluateSinglePath(path []solana.PublicKey, amount *big.Int, exactIn bool) (*SplitResult, error) {
	quote, err := s.router.evaluateSinglePath(path, amount, exactIn)
	if err != nil {
		return nil, err
	}

	return &SplitResult{
		Splits: []SplitPath{{
			Path:      path,
			Hops:      quote.Hops, // CRITICAL: Preserve hop chain with Pool refs
			Percent:   100,
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			FeeAmount: quote.TotalFee,
			ImpactBps: quote.PriceImpactBps,
		}},
		TotalAmountIn:  quote.AmountIn,
		TotalAmountOut: quote.AmountOut,
		TotalFee:       quote.TotalFee,
		AvgImpactBps:   quote.PriceImpactBps,
	}, nil
}

// buildResult builds the final SplitResult from evaluated splits
func (s *Splitter) buildResult(splits []SplitPath, totalAmount *big.Int) (*SplitResult, error) {
	// Filter out zero-allocation splits and failed evaluations
	activeSplits := make([]SplitPath, 0, len(splits))
	for _, split := range splits {
		if split.Percent > 0 && split.AmountOut != nil && split.AmountOut.Sign() > 0 {
			activeSplits = append(activeSplits, split)
		}
	}

	if len(activeSplits) == 0 {
		return nil, ErrNoRoute
	}

	// Sort by percentage (highest first)
	sort.Slice(activeSplits, func(i, j int) bool {
		return activeSplits[i].Percent > activeSplits[j].Percent
	})

	// Calculate totals
	totalIn := big.NewInt(0)
	totalOut := big.NewInt(0)
	totalFee := big.NewInt(0)
	weightedImpact := uint32(0)

	for _, split := range activeSplits {
		totalIn.Add(totalIn, split.AmountIn)
		totalOut.Add(totalOut, split.AmountOut)
		if split.FeeAmount != nil {
			totalFee.Add(totalFee, split.FeeAmount)
		}
		weightedImpact += uint32(split.ImpactBps) * uint32(split.Percent)
	}

	avgImpact := uint16(weightedImpact / 100)

	return &SplitResult{
		Splits:         activeSplits,
		TotalAmountIn:  totalIn,
		TotalAmountOut: totalOut,
		TotalFee:       totalFee,
		AvgImpactBps:   avgImpact,
	}, nil
}

// FindOptimalSplitFast finds the optimal split across multiple paths using uint64 (zero-allocation)
func (s *Splitter) FindOptimalSplitFast(inputMint, outputMint solana.PublicKey, amount uint64, paths [][]solana.PublicKey, exactIn bool) (*FastSplitResult, error) {
	if len(paths) == 0 {
		return nil, ErrNoRoute
	}

	if len(paths) > MaxSplitPaths {
		paths = paths[:MaxSplitPaths]
	}

	if len(paths) == 1 {
		return s.evaluateSinglePathFast(paths[0], amount, exactIn)
	}

	if len(paths) == 2 {
		return s.findOptimalTwoPathSplitFast(paths[0], paths[1], amount, exactIn)
	}

	return s.findOptimalMultiPathSplitFast(paths, amount, exactIn)
}

// findOptimalTwoPathSplitFast uses Golden-Section search for optimal 2-path split with uint64
func (s *Splitter) findOptimalTwoPathSplitFast(path1, path2 []solana.PublicKey, amount uint64, exactIn bool) (*FastSplitResult, error) {
	gsResult := s.GoldenSectionSplitFast(path1, path2, amount, exactIn)
	if gsResult != nil {
		return gsResult.ToFastSplitResult(path1, path2), nil
	}

	quote1, err1 := s.router.evaluateSinglePathFast(path1, amount, exactIn)
	quote2, err2 := s.router.evaluateSinglePathFast(path2, amount, exactIn)

	if err1 != nil && err2 != nil {
		return nil, ErrNoRoute
	}

	var bestPath []solana.PublicKey
	var bestQuote *FastMultiHopQuoteResult

	if err1 == nil && err2 == nil {
		if exactIn {
			if quote1.AmountOut >= quote2.AmountOut {
				bestPath, bestQuote = path1, quote1
			} else {
				bestPath, bestQuote = path2, quote2
			}
		} else {
			if quote1.AmountIn <= quote2.AmountIn {
				bestPath, bestQuote = path1, quote1
			} else {
				bestPath, bestQuote = path2, quote2
			}
		}
	} else if err1 == nil {
		bestPath, bestQuote = path1, quote1
	} else {
		bestPath, bestQuote = path2, quote2
	}

	return &FastSplitResult{
		Splits: []FastSplitPath{{
			Path:      bestPath,
			Hops:      bestQuote.Hops,
			Percent:   100,
			AmountIn:  bestQuote.AmountIn,
			AmountOut: bestQuote.AmountOut,
			FeeAmount: bestQuote.TotalFee,
			ImpactBps: bestQuote.PriceImpactBps,
		}},
		TotalAmountIn:  bestQuote.AmountIn,
		TotalAmountOut: bestQuote.AmountOut,
		TotalFee:       bestQuote.TotalFee,
		AvgImpactBps:   bestQuote.PriceImpactBps,
	}, nil
}

// findOptimalMultiPathSplitFast uses ADMM solver for 3+ paths with uint64
func (s *Splitter) findOptimalMultiPathSplitFast(paths [][]solana.PublicKey, amount uint64, exactIn bool) (*FastSplitResult, error) {
	solver := NewADMMSolver(s.router)

	// Get workspace from pool
	ws := s.admmWorkspacePool.Get().(*ADMMWorkspace)
	defer s.admmWorkspacePool.Put(ws)

	allocs := solver.SolveFast(paths, amount, exactIn, ws)

	if len(allocs) > 0 {
		result := FastAllocationsToSplitResult(allocs)
		if result != nil && result.TotalAmountOut > 0 {
			return result, nil
		}
	}

	return s.fallbackBestSinglePathFast(paths, amount, exactIn)
}

// fallbackBestSinglePathFast evaluates all paths and returns the best single path using uint64
func (s *Splitter) fallbackBestSinglePathFast(paths [][]solana.PublicKey, amount uint64, exactIn bool) (*FastSplitResult, error) {
	var bestResult *FastSplitResult

	for _, path := range paths {
		result, err := s.evaluateSinglePathFast(path, amount, exactIn)
		if err != nil {
			continue
		}

		if bestResult == nil {
			bestResult = result
			continue
		}

		if exactIn && result.TotalAmountOut > bestResult.TotalAmountOut {
			bestResult = result
		} else if !exactIn && result.TotalAmountIn < bestResult.TotalAmountIn {
			bestResult = result
		}
	}

	if bestResult == nil {
		return nil, ErrNoRoute
	}
	return bestResult, nil
}

// evaluateSinglePathFast evaluates a single path as a fast split result
func (s *Splitter) evaluateSinglePathFast(path []solana.PublicKey, amount uint64, exactIn bool) (*FastSplitResult, error) {
	quote, err := s.router.evaluateSinglePathFast(path, amount, exactIn)
	if err != nil {
		return nil, err
	}

	return &FastSplitResult{
		Splits: []FastSplitPath{{
			Path:      path,
			Hops:      quote.Hops,
			Percent:   100,
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			FeeAmount: quote.TotalFee,
			ImpactBps: quote.PriceImpactBps,
		}},
		TotalAmountIn:  quote.AmountIn,
		TotalAmountOut: quote.AmountOut,
		TotalFee:       quote.TotalFee,
		AvgImpactBps:   quote.PriceImpactBps,
	}, nil
}

// ToMultiHopQuote converts SplitResult to MultiHopQuoteResult for API compatibility
func (sr *SplitResult) ToMultiHopQuote() *domain.MultiHopQuoteResult {
	if len(sr.Splits) == 0 {
		return nil
	}

	// Validate result has valid amounts
	if sr.TotalAmountOut == nil || sr.TotalAmountOut.Sign() <= 0 {
		return nil
	}

	// For single path: use its hops directly (has Pool refs for transaction builder)
	if len(sr.Splits) == 1 {
		return &domain.MultiHopQuoteResult{
			Route:          sr.Splits[0].Path,
			Hops:           sr.Splits[0].Hops, // Use stored hops with Pool refs
			AmountIn:       sr.TotalAmountIn,
			AmountOut:      sr.TotalAmountOut,
			TotalFee:       sr.TotalFee,
			PriceImpactBps: sr.AvgImpactBps,
			IsSplitRoute:   false,
		}
	}

	// For multi-path: use dominant path's hops only
	// The builder cannot handle true multi-path routing yet (it misinterprets
	// IsSplitRoute for multi-hop paths by flattening inputIdx/outputIdx to 0/1).
	// Until the builder supports multi-path, we return only the dominant path
	// with the combined totals to avoid corrupting the route plan.
	dominantPath := sr.Splits[0] // Already sorted by percent

	return &domain.MultiHopQuoteResult{
		Route:          dominantPath.Path,
		Hops:           dominantPath.Hops,
		AmountIn:       sr.TotalAmountIn,
		AmountOut:      sr.TotalAmountOut,
		TotalFee:       sr.TotalFee,
		PriceImpactBps: sr.AvgImpactBps,
		IsSplitRoute:   false,
	}
}
