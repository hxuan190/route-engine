package router

import (
	"math/big"
)

// Price impact thresholds in basis points (bps)
const (
	PriceImpactLow      uint16 = 100  // 1% - Low impact
	PriceImpactModerate uint16 = 300  // 3% - Moderate impact
	PriceImpactHigh     uint16 = 500  // 5% - High impact
	PriceImpactExtreme  uint16 = 1000 // 10% - Extreme impact
)

// PriceImpactSeverity represents the severity level of price impact
type PriceImpactSeverity string

const (
	SeverityNone     PriceImpactSeverity = "none"     // < 1%
	SeverityLow      PriceImpactSeverity = "low"      // 1-3%
	SeverityModerate PriceImpactSeverity = "moderate" // 3-5%
	SeverityHigh     PriceImpactSeverity = "high"     // 5-10%
	SeverityExtreme  PriceImpactSeverity = "extreme"  // > 10%
)

// GetPriceImpactSeverity returns the severity level based on price impact bps
func GetPriceImpactSeverity(priceImpactBps uint16) PriceImpactSeverity {
	switch {
	case priceImpactBps < PriceImpactLow:
		return SeverityNone
	case priceImpactBps < PriceImpactModerate:
		return SeverityLow
	case priceImpactBps < PriceImpactHigh:
		return SeverityModerate
	case priceImpactBps < PriceImpactExtreme:
		return SeverityHigh
	default:
		return SeverityExtreme
	}
}

// CalculatePriceImpactCLMM calculates price impact for CLMM pools
// Price impact = (1 - effective_price / current_price) * 10000
// For A->B swap: current_price = sqrtPriceX64^2, effective_price = amountOut / amountIn
// For B->A swap: current_price = 1 / sqrtPriceX64^2, effective_price = amountIn / amountOut
func CalculatePriceImpactCLMM(
	amountIn *big.Int,
	amountOut *big.Int,
	aToB bool,
	sqrtPriceX64 *big.Int,
	feeRate uint32,
) uint16 {
	if amountIn == nil || amountOut == nil || sqrtPriceX64 == nil {
		return 0
	}
	if amountIn.Sign() <= 0 || amountOut.Sign() <= 0 || sqrtPriceX64.Sign() <= 0 {
		return 0
	}

	// Use pooled big.Int for temporary calculations
	sqrtPrice := GetBigInt()
	feeAmount := GetBigInt()
	amountInEffective := GetBigInt()
	currentPrice := GetBigInt()
	effectivePrice := GetBigInt()
	priceDiff := GetBigInt()
	impact := GetBigInt()
	feeRateBig := GetBigInt()

	defer func() {
		PutBigInt(sqrtPrice)
		PutBigInt(feeAmount)
		PutBigInt(amountInEffective)
		PutBigInt(currentPrice)
		PutBigInt(effectivePrice)
		PutBigInt(priceDiff)
		PutBigInt(impact)
		PutBigInt(feeRateBig)
	}()

	// Calculate current price based on sqrt price
	// price = (sqrtPriceX64 / 2^64)^2
	sqrtPrice.Mul(sqrtPriceX64, sqrtPriceX64)
	sqrtPrice.Div(sqrtPrice, Q64)

	// Calculate effective price from the actual swap
	// We need to exclude the fee for price impact calculation to show pure slippage
	// amountInAvailable = amountIn - fee
	feeRateBig.SetInt64(int64(feeRate))
	feeAmount.Mul(amountIn, feeRateBig)
	feeAmount.Div(feeAmount, FEE_BASE) // feeRate is in millionths

	amountInEffective.Sub(amountIn, feeAmount)
	if amountInEffective.Sign() <= 0 {
		return 0
	}

	// For A->B: effective_price = amountOut / amountInEffective (how much B per A)
	// For B->A: effective_price = amountInEffective / amountOut (how much A per B)
	if aToB {
		// A->B swap: current price is sqrtPrice (B per A)
		currentPrice.Set(sqrtPrice)

		// Effective price: amountOut / amountInEffective (scaled by Q64 for precision)
		effectivePrice.Mul(amountOut, Q64)
		effectivePrice.Div(effectivePrice, amountInEffective)
	} else {
		// B->A swap: current price is inverted (1 / sqrtPrice)
		// currentPrice = 2^64 / sqrtPrice (A per B)
		currentPrice.Mul(Q64, Q64)
		currentPrice.Div(currentPrice, sqrtPrice)

		// Effective price: amountOut / amountInEffective (A per B, scaled)
		effectivePrice.Mul(amountOut, Q64)
		effectivePrice.Div(effectivePrice, amountInEffective)
	}

	// Avoid division by zero
	if currentPrice.Sign() <= 0 {
		return 0
	}

	// Price impact = (1 - effectivePrice / currentPrice) * 10000
	// = (currentPrice - effectivePrice) / currentPrice * 10000
	// If effectivePrice > currentPrice (positive slippage), impact is 0
	if effectivePrice.Cmp(currentPrice) >= 0 {
		return 0
	}

	priceDiff.Sub(currentPrice, effectivePrice)
	impact.Mul(priceDiff, BPS_DENOM)
	impact.Div(impact, currentPrice)

	// Cap at max uint16
	if !impact.IsUint64() || impact.Uint64() > 65535 {
		return 65535
	}

	return uint16(impact.Uint64())
}

// GetPriceImpactWarning returns a user-friendly warning message based on impact
func GetPriceImpactWarning(priceImpactBps uint16) string {
	severity := GetPriceImpactSeverity(priceImpactBps)

	switch severity {
	case SeverityNone:
		return ""
	case SeverityLow:
		return "Low price impact"
	case SeverityModerate:
		return "Moderate price impact - consider reducing trade size"
	case SeverityHigh:
		return "High price impact - you may receive significantly less tokens"
	case SeverityExtreme:
		return "EXTREME price impact - this trade will severely impact the market price"
	default:
		return ""
	}
}
