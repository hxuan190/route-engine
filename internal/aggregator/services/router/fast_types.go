package router

import (
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
)

// FastSwapQuote is a zero-allocation quote type using uint64 for internal routing
// Convert to domain.SwapQuote only at API boundary
type FastSwapQuote struct {
	AmountIn       uint64
	AmountOut      uint64
	Fee            uint64
	PriceImpactBps uint16
	Pool           *domain.Pool
	AToB           bool
}

// ToSwapQuote converts FastSwapQuote to domain.SwapQuote for API response
// This is the only place where big.Int allocation occurs
func (f *FastSwapQuote) ToSwapQuote() *domain.SwapQuote {
	return &domain.SwapQuote{
		AmountIn:       new(big.Int).SetUint64(f.AmountIn),
		AmountOut:      new(big.Int).SetUint64(f.AmountOut),
		Fee:            new(big.Int).SetUint64(f.Fee),
		PriceImpactBps: f.PriceImpactBps,
		Pool:           f.Pool,
		AToB:           f.AToB,
	}
}

// FastHopQuote represents a single hop in a multi-hop route (zero-allocation)
type FastHopQuote struct {
	Pool           *domain.Pool
	AmountIn       uint64
	AmountOut      uint64
	FeeAmount      uint64
	AToB           bool
	PriceImpactBps uint16
}

// ToHopQuote converts FastHopQuote to domain.HopQuote for API response
func (f *FastHopQuote) ToHopQuote() domain.HopQuote {
	return domain.HopQuote{
		Pool:           f.Pool,
		AmountIn:       new(big.Int).SetUint64(f.AmountIn),
		AmountOut:      new(big.Int).SetUint64(f.AmountOut),
		FeeAmount:      new(big.Int).SetUint64(f.FeeAmount),
		AToB:           f.AToB,
		PriceImpactBps: f.PriceImpactBps,
	}
}

// FastMultiHopQuoteResult holds the complete multi-hop quote result (zero-allocation)
type FastMultiHopQuoteResult struct {
	Route          []solana.PublicKey
	Hops           []FastHopQuote
	AmountIn       uint64
	AmountOut      uint64
	TotalFee       uint64
	PriceImpactBps uint16
}

// ToMultiHopQuoteResult converts to domain.MultiHopQuoteResult for API response
func (f *FastMultiHopQuoteResult) ToMultiHopQuoteResult() *domain.MultiHopQuoteResult {
	hops := make([]domain.HopQuote, len(f.Hops))
	for i, h := range f.Hops {
		hops[i] = h.ToHopQuote()
	}

	return &domain.MultiHopQuoteResult{
		Route:          f.Route,
		Hops:           hops,
		AmountIn:       new(big.Int).SetUint64(f.AmountIn),
		AmountOut:      new(big.Int).SetUint64(f.AmountOut),
		TotalFee:       new(big.Int).SetUint64(f.TotalFee),
		PriceImpactBps: f.PriceImpactBps,
	}
}

// FastSuperEdgeQuote represents an optimized quote across a super edge (zero-allocation)
type FastSuperEdgeQuote struct {
	AmountIn       uint64
	AmountOut      uint64
	Fee            uint64
	PriceImpactBps uint16
	IsSplit        bool
	PoolSplits     []FastPoolSplit
}

// FastPoolSplit represents one pool's contribution in a split quote
type FastPoolSplit struct {
	Pool      *domain.Pool
	Percent   uint8
	AmountIn  uint64
	AmountOut uint64
	Fee       uint64
	AToB      bool
}

// FastQuoterFunc is the function signature for fast quoters (uint64 in/out)
type FastQuoterFunc func(pool *domain.Pool, amount uint64, aToB, exactIn bool) (*FastSwapQuote, error)

// FastSplitPath represents a path with its allocation (zero-allocation version of SplitPath)
type FastSplitPath struct {
	Path      []solana.PublicKey
	Hops      []FastHopQuote
	Percent   uint8
	AmountIn  uint64
	AmountOut uint64
	FeeAmount uint64
	ImpactBps uint16
}

// FastSplitResult holds the optimized split across multiple paths (zero-allocation)
type FastSplitResult struct {
	Splits         []FastSplitPath
	TotalAmountIn  uint64
	TotalAmountOut uint64
	TotalFee       uint64
	AvgImpactBps   uint16
}

// ToSplitResult converts FastSplitResult to SplitResult for API compatibility
func (f *FastSplitResult) ToSplitResult() *SplitResult {
	if f == nil || len(f.Splits) == 0 {
		return nil
	}

	splits := make([]SplitPath, len(f.Splits))
	for i, s := range f.Splits {
		hops := make([]domain.HopQuote, len(s.Hops))
		for j, h := range s.Hops {
			hops[j] = h.ToHopQuote()
		}
		splits[i] = SplitPath{
			Path:      s.Path,
			Hops:      hops,
			Percent:   s.Percent,
			AmountIn:  new(big.Int).SetUint64(s.AmountIn),
			AmountOut: new(big.Int).SetUint64(s.AmountOut),
			FeeAmount: new(big.Int).SetUint64(s.FeeAmount),
			ImpactBps: s.ImpactBps,
		}
	}

	return &SplitResult{
		Splits:         splits,
		TotalAmountIn:  new(big.Int).SetUint64(f.TotalAmountIn),
		TotalAmountOut: new(big.Int).SetUint64(f.TotalAmountOut),
		TotalFee:       new(big.Int).SetUint64(f.TotalFee),
		AvgImpactBps:   f.AvgImpactBps,
	}
}

// ToMultiHopQuote converts FastSplitResult to domain.MultiHopQuoteResult
func (f *FastSplitResult) ToMultiHopQuote() *domain.MultiHopQuoteResult {
	if f == nil || len(f.Splits) == 0 {
		return nil
	}

	if f.TotalAmountOut == 0 {
		return nil
	}

	// For single path: use its hops directly
	if len(f.Splits) == 1 {
		hops := make([]domain.HopQuote, len(f.Splits[0].Hops))
		for i, h := range f.Splits[0].Hops {
			hops[i] = h.ToHopQuote()
		}
		return &domain.MultiHopQuoteResult{
			Route:          f.Splits[0].Path,
			Hops:           hops,
			AmountIn:       new(big.Int).SetUint64(f.TotalAmountIn),
			AmountOut:      new(big.Int).SetUint64(f.TotalAmountOut),
			TotalFee:       new(big.Int).SetUint64(f.TotalFee),
			PriceImpactBps: f.AvgImpactBps,
			IsSplitRoute:   false,
		}
	}

	// For multi-path: use dominant path's hops
	dominantPath := f.Splits[0]
	hops := make([]domain.HopQuote, len(dominantPath.Hops))
	for i, h := range dominantPath.Hops {
		hops[i] = h.ToHopQuote()
	}

	return &domain.MultiHopQuoteResult{
		Route:          dominantPath.Path,
		Hops:           hops,
		AmountIn:       new(big.Int).SetUint64(f.TotalAmountIn),
		AmountOut:      new(big.Int).SetUint64(f.TotalAmountOut),
		TotalFee:       new(big.Int).SetUint64(f.TotalFee),
		PriceImpactBps: f.AvgImpactBps,
		IsSplitRoute:   false,
	}
}

// FastSplitAllocation represents a single path's allocation after optimization (zero-allocation)
type FastSplitAllocation struct {
	Path      []solana.PublicKey
	Hops      []FastHopQuote
	Ratio     float64
	AmountIn  uint64
	AmountOut uint64
	TotalFee  uint64
	ImpactBps uint16
}

// FastGoldenSectionResult holds the result of golden-section optimization (zero-allocation)
type FastGoldenSectionResult struct {
	Path1Percent uint8
	Path2Percent uint8
	Path1Hops    []FastHopQuote
	Path2Hops    []FastHopQuote
	TotalOut     uint64
	TotalIn      uint64
	TotalFee     uint64
	AvgImpactBps uint16
}

// ToFastSplitResult converts FastGoldenSectionResult to FastSplitResult
func (gsr *FastGoldenSectionResult) ToFastSplitResult(path1, path2 []solana.PublicKey) *FastSplitResult {
	if gsr == nil {
		return nil
	}

	amt1In := mulRatioU64(gsr.TotalIn, float64(gsr.Path1Percent)/100.0)
	amt2In := gsr.TotalIn - amt1In

	amt1Out := mulRatioU64(gsr.TotalOut, float64(gsr.Path1Percent)/100.0)
	amt2Out := gsr.TotalOut - amt1Out

	fee1 := mulRatioU64(gsr.TotalFee, float64(gsr.Path1Percent)/100.0)
	fee2 := gsr.TotalFee - fee1

	return &FastSplitResult{
		Splits: []FastSplitPath{
			{
				Path:      path1,
				Hops:      gsr.Path1Hops,
				Percent:   gsr.Path1Percent,
				AmountIn:  amt1In,
				AmountOut: amt1Out,
				FeeAmount: fee1,
				ImpactBps: 0,
			},
			{
				Path:      path2,
				Hops:      gsr.Path2Hops,
				Percent:   gsr.Path2Percent,
				AmountIn:  amt2In,
				AmountOut: amt2Out,
				FeeAmount: fee2,
				ImpactBps: 0,
			},
		},
		TotalAmountIn:  gsr.TotalIn,
		TotalAmountOut: gsr.TotalOut,
		TotalFee:       gsr.TotalFee,
		AvgImpactBps:   gsr.AvgImpactBps,
	}
}

// FastSplitQuoteResult holds direct pool split quote (zero-allocation)
type FastSplitQuoteResult struct {
	InputMint      solana.PublicKey
	OutputMint     solana.PublicKey
	Splits         []FastSplitRoute
	TotalAmountIn  uint64
	TotalAmountOut uint64
	TotalFee       uint64
	PriceImpactBps uint16
}

// FastSplitRoute represents one pool's contribution in direct split (zero-allocation)
type FastSplitRoute struct {
	Pool           *domain.Pool
	Percent        uint8
	AmountIn       uint64
	AmountOut      uint64
	FeeAmount      uint64
	AToB           bool
	PriceImpactBps uint16
}

// ToMultiHopQuoteResult converts FastSplitQuoteResult to domain.MultiHopQuoteResult
func (f *FastSplitQuoteResult) ToMultiHopQuoteResult() *domain.MultiHopQuoteResult {
	if f == nil || len(f.Splits) == 0 {
		return nil
	}

	// Use the first (dominant) split's pool for the hop
	split := f.Splits[0]
	hops := []domain.HopQuote{{
		Pool:           split.Pool,
		AmountIn:       new(big.Int).SetUint64(f.TotalAmountIn),
		AmountOut:      new(big.Int).SetUint64(f.TotalAmountOut),
		FeeAmount:      new(big.Int).SetUint64(f.TotalFee),
		AToB:           split.AToB,
		PriceImpactBps: f.PriceImpactBps,
	}}

	splitPercents := make([]uint8, len(f.Splits))
	for i, s := range f.Splits {
		splitPercents[i] = s.Percent
	}

	return &domain.MultiHopQuoteResult{
		Route:          []solana.PublicKey{f.InputMint, f.OutputMint},
		Hops:           hops,
		AmountIn:       new(big.Int).SetUint64(f.TotalAmountIn),
		AmountOut:      new(big.Int).SetUint64(f.TotalAmountOut),
		TotalFee:       new(big.Int).SetUint64(f.TotalFee),
		PriceImpactBps: f.PriceImpactBps,
		IsSplitRoute:   len(f.Splits) > 1,
		SplitPercents:  splitPercents,
	}
}

// Pre-allocated empty slices to avoid allocation
var (
	emptyFastHops       = []FastHopQuote{}
	emptyFastPoolSplits = []FastPoolSplit{}
)
