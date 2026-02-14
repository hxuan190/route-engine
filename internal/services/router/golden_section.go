package router

import (
	"math/big"
	"math/bits"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
)

const (
	GoldenRatio     = 1.6180339887498948482
	GoldenTolerance = 0.01 // 1% precision
	GoldenMaxIter   = 15   // Reduced for latency
	MinSplitRatio   = 0.10 // Minimum 10% allocation
	MaxSplitRatio   = 0.90 // Maximum 90% allocation

	// Fixed-point precision for ratio calculations (1e6 = 6 decimal places)
	ratioPrecision = 1000000
)

// GoldenSectionResult holds the result of golden-section optimization
type GoldenSectionResult struct {
	Path1Percent uint8
	Path2Percent uint8
	Path1Hops    []domain.HopQuote
	Path2Hops    []domain.HopQuote
	TotalOut     *big.Int
	TotalIn      *big.Int
	TotalFee     *big.Int
	AvgImpactBps uint16
}

// GoldenSectionSplit finds optimal split ratio between two paths using golden-section search
func (s *Splitter) GoldenSectionSplit(
	path1, path2 []solana.PublicKey,
	amount *big.Int,
	exactIn bool,
) *GoldenSectionResult {
	a, b := MinSplitRatio, MaxSplitRatio
	c := b - (b-a)/GoldenRatio
	d := a + (b-a)/GoldenRatio

	// Evaluation function returns (totalOutput, path1Quote, path2Quote)
	type evalResult struct {
		total  *big.Int
		quote1 *domain.MultiHopQuoteResult
		quote2 *domain.MultiHopQuoteResult
	}

	evalSplit := func(ratio float64) evalResult {
		amt1 := mulRatio(amount, ratio)
		amt2 := new(big.Int).Sub(amount, amt1)

		// Ensure minimum amounts
		if amt1.Sign() <= 0 || amt2.Sign() <= 0 {
			return evalResult{total: big.NewInt(0)}
		}

		quote1, err1 := s.router.evaluateSinglePath(path1, amt1, exactIn)
		quote2, err2 := s.router.evaluateSinglePath(path2, amt2, exactIn)

		if err1 != nil || err2 != nil || quote1 == nil || quote2 == nil {
			return evalResult{total: big.NewInt(0)}
		}

		var total *big.Int
		if exactIn {
			total = new(big.Int).Add(quote1.AmountOut, quote2.AmountOut)
		} else {
			total = new(big.Int).Add(quote1.AmountIn, quote2.AmountIn)
		}

		return evalResult{
			total:  total,
			quote1: quote1,
			quote2: quote2,
		}
	}

	// Compare function for optimization direction
	isBetter := func(new, old *big.Int) bool {
		if exactIn {
			return new.Cmp(old) > 0 // Higher output is better
		}
		return new.Cmp(old) < 0 // Lower input is better
	}

	fc := evalSplit(c)
	fd := evalSplit(d)

	// Track best result
	var bestResult evalResult
	var bestRatio float64

	if isBetter(fc.total, fd.total) {
		bestResult = fc
		bestRatio = c
	} else {
		bestResult = fd
		bestRatio = d
	}

	// Golden-section search
	for i := 0; i < GoldenMaxIter && (b-a) > GoldenTolerance; i++ {
		if isBetter(fc.total, fd.total) {
			b = d
			d = c
			fd = fc
			c = b - (b-a)/GoldenRatio
			fc = evalSplit(c)

			if fc.total.Sign() > 0 && isBetter(fc.total, bestResult.total) {
				bestResult = fc
				bestRatio = c
			}
		} else {
			a = c
			c = d
			fc = fd
			d = a + (b-a)/GoldenRatio
			fd = evalSplit(d)

			if fd.total.Sign() > 0 && isBetter(fd.total, bestResult.total) {
				bestResult = fd
				bestRatio = d
			}
		}
	}

	// Return nil if no valid result found
	if bestResult.quote1 == nil || bestResult.quote2 == nil {
		return nil
	}

	pct1 := uint8(bestRatio * 100)
	pct2 := 100 - pct1

	// Calculate totals
	totalFee := new(big.Int).Add(bestResult.quote1.TotalFee, bestResult.quote2.TotalFee)

	// Weighted average impact
	impact1 := uint32(bestResult.quote1.PriceImpactBps) * uint32(pct1)
	impact2 := uint32(bestResult.quote2.PriceImpactBps) * uint32(pct2)
	avgImpact := uint16((impact1 + impact2) / 100)

	var totalIn, totalOut *big.Int
	if exactIn {
		totalIn = amount
		totalOut = bestResult.total
	} else {
		totalIn = bestResult.total
		totalOut = amount
	}

	return &GoldenSectionResult{
		Path1Percent: pct1,
		Path2Percent: pct2,
		Path1Hops:    bestResult.quote1.Hops,
		Path2Hops:    bestResult.quote2.Hops,
		TotalOut:     totalOut,
		TotalIn:      totalIn,
		TotalFee:     totalFee,
		AvgImpactBps: avgImpact,
	}
}

// mulRatio multiplies a big.Int by a float64 ratio using integer math (zero big.Float allocation)
// Uses fixed-point arithmetic: result = amount * (ratio * precision) / precision
func mulRatio(amount *big.Int, ratio float64) *big.Int {
	// Fast path for uint64 amounts (most common case)
	if amount.IsUint64() {
		amt := amount.Uint64()
		// Use fixed-point: multiply by ratio scaled to integer, then divide
		ratioFixed := uint64(ratio * ratioPrecision)
		result := (amt * ratioFixed) / ratioPrecision
		return new(big.Int).SetUint64(result)
	}

	// Slow path for large amounts: use pooled big.Int
	ratioFixed := GetBigInt()
	temp := GetBigInt()
	defer func() {
		PutBigInt(ratioFixed)
		PutBigInt(temp)
	}()

	ratioFixed.SetInt64(int64(ratio * ratioPrecision))
	temp.Mul(amount, ratioFixed)
	temp.Div(temp, big.NewInt(ratioPrecision))

	// Must allocate result since we return it
	return new(big.Int).Set(temp)
}

// mulRatioU64 multiplies a uint64 amount by a float64 ratio (zero allocation)
// Uses math/bits.Mul64 to prevent overflow and precision loss for large amounts
// This is critical for HFT: even 1 lamport error can cause validator rejection
func mulRatioU64(amount uint64, ratio float64) uint64 {
	// Fast path: ratio boundary cases
	if ratio >= 1.0 {
		return amount
	}
	if ratio <= 0.0 {
		return 0
	}

	// Convert ratio to fixed-point integer (scale by 1e6)
	ratioFixed := uint64(ratio * ratioPrecision)

	// Use 128-bit multiplication to avoid overflow
	// hi, lo = amount * ratioFixed (128-bit result)
	hi, lo := bits.Mul64(amount, ratioFixed)

	// Divide 128-bit result by ratioPrecision
	// result = (hi << 64 + lo) / ratioPrecision
	result, _ := bits.Div64(hi, lo, ratioPrecision)

	return result
}

// mulRatioInto multiplies amount by ratio and stores result in dst (zero allocation)
// This is the hot-path version when you have a pre-allocated destination
func mulRatioInto(dst, amount *big.Int, ratio float64) {
	if amount.IsUint64() {
		amt := amount.Uint64()
		ratioFixed := uint64(ratio * ratioPrecision)
		result := (amt * ratioFixed) / ratioPrecision
		dst.SetUint64(result)
		return
	}

	// Large amount path
	ratioFixed := GetBigInt()
	defer PutBigInt(ratioFixed)

	ratioFixed.SetInt64(int64(ratio * ratioPrecision))
	dst.Mul(amount, ratioFixed)
	dst.Div(dst, big.NewInt(ratioPrecision))
}

// GoldenSectionSplitFast finds optimal split ratio between two paths using uint64 (zero-allocation)
func (s *Splitter) GoldenSectionSplitFast(
	path1, path2 []solana.PublicKey,
	amount uint64,
	exactIn bool,
) *FastGoldenSectionResult {
	a, b := MinSplitRatio, MaxSplitRatio
	c := b - (b-a)/GoldenRatio
	d := a + (b-a)/GoldenRatio

	type evalResult struct {
		total  uint64
		quote1 *FastMultiHopQuoteResult
		quote2 *FastMultiHopQuoteResult
	}

	evalSplit := func(ratio float64) evalResult {
		amt1 := mulRatioU64(amount, ratio)
		amt2 := amount - amt1

		if amt1 == 0 || amt2 == 0 {
			return evalResult{total: 0}
		}

		quote1, err1 := s.router.evaluateSinglePathFast(path1, amt1, exactIn)
		quote2, err2 := s.router.evaluateSinglePathFast(path2, amt2, exactIn)

		if err1 != nil || err2 != nil || quote1 == nil || quote2 == nil {
			return evalResult{total: 0}
		}

		var total uint64
		if exactIn {
			total = quote1.AmountOut + quote2.AmountOut
		} else {
			total = quote1.AmountIn + quote2.AmountIn
		}

		return evalResult{
			total:  total,
			quote1: quote1,
			quote2: quote2,
		}
	}

	isBetter := func(newVal, oldVal uint64) bool {
		if exactIn {
			return newVal > oldVal
		}
		return newVal < oldVal && newVal > 0
	}

	fc := evalSplit(c)
	fd := evalSplit(d)

	var bestResult evalResult
	var bestRatio float64

	if isBetter(fc.total, fd.total) {
		bestResult = fc
		bestRatio = c
	} else {
		bestResult = fd
		bestRatio = d
	}

	for i := 0; i < GoldenMaxIter && (b-a) > GoldenTolerance; i++ {
		if isBetter(fc.total, fd.total) {
			b = d
			d = c
			fd = fc
			c = b - (b-a)/GoldenRatio
			fc = evalSplit(c)

			if fc.total > 0 && isBetter(fc.total, bestResult.total) {
				bestResult = fc
				bestRatio = c
			}
		} else {
			a = c
			c = d
			fc = fd
			d = a + (b-a)/GoldenRatio
			fd = evalSplit(d)

			if fd.total > 0 && isBetter(fd.total, bestResult.total) {
				bestResult = fd
				bestRatio = d
			}
		}
	}

	if bestResult.quote1 == nil || bestResult.quote2 == nil {
		return nil
	}

	pct1 := uint8(bestRatio * 100)
	pct2 := 100 - pct1

	totalFee := bestResult.quote1.TotalFee + bestResult.quote2.TotalFee

	impact1 := uint32(bestResult.quote1.PriceImpactBps) * uint32(pct1)
	impact2 := uint32(bestResult.quote2.PriceImpactBps) * uint32(pct2)
	avgImpact := uint16((impact1 + impact2) / 100)

	var totalIn, totalOut uint64
	if exactIn {
		totalIn = amount
		totalOut = bestResult.total
	} else {
		totalIn = bestResult.total
		totalOut = amount
	}

	return &FastGoldenSectionResult{
		Path1Percent: pct1,
		Path2Percent: pct2,
		Path1Hops:    bestResult.quote1.Hops,
		Path2Hops:    bestResult.quote2.Hops,
		TotalOut:     totalOut,
		TotalIn:      totalIn,
		TotalFee:     totalFee,
		AvgImpactBps: avgImpact,
	}
}

// ToSplitResult converts GoldenSectionResult to SplitResult
func (gsr *GoldenSectionResult) ToSplitResult(path1, path2 []solana.PublicKey) *SplitResult {
	if gsr == nil {
		return nil
	}

	// Calculate individual amounts from percentages
	amt1In := mulRatio(gsr.TotalIn, float64(gsr.Path1Percent)/100.0)
	amt2In := new(big.Int).Sub(gsr.TotalIn, amt1In)

	amt1Out := mulRatio(gsr.TotalOut, float64(gsr.Path1Percent)/100.0)
	amt2Out := new(big.Int).Sub(gsr.TotalOut, amt1Out)

	fee1 := mulRatio(gsr.TotalFee, float64(gsr.Path1Percent)/100.0)
	fee2 := new(big.Int).Sub(gsr.TotalFee, fee1)

	return &SplitResult{
		Splits: []SplitPath{
			{
				Path:      path1,
				Hops:      gsr.Path1Hops,
				Percent:   gsr.Path1Percent,
				AmountIn:  amt1In,
				AmountOut: amt1Out,
				FeeAmount: fee1,
				ImpactBps: 0, // Will be recalculated
			},
			{
				Path:      path2,
				Hops:      gsr.Path2Hops,
				Percent:   gsr.Path2Percent,
				AmountIn:  amt2In,
				AmountOut: amt2Out,
				FeeAmount: fee2,
				ImpactBps: 0, // Will be recalculated
			},
		},
		TotalAmountIn:  gsr.TotalIn,
		TotalAmountOut: gsr.TotalOut,
		TotalFee:       gsr.TotalFee,
		AvgImpactBps:   gsr.AvgImpactBps,
	}
}
