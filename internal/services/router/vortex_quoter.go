package router

import (
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/hxuan190/route-engine/internal/domain"
	"github.com/hxuan190/route-engine/internal/metrics"
	vortex_go "github.com/thehyperflames/valiant_go/generated/valiant"
)

var (
	ErrInvalidPool           = errors.New("invalid pool")
	ErrInsufficientLiquidity = errors.New("insufficient liquidity")
	ErrUnsupportedPoolType   = errors.New("unsupported pool type for quote")
	ErrNoPoolFound           = errors.New("no pool found")
	ErrNoRoute               = errors.New("no route found")
)

// vortexQuoteCounter for sampling metrics (1/128 calls)
var vortexQuoteCounter atomic.Uint64

type VortexQuoter struct{}

func NewVortexQuoter() *VortexQuoter {
	return &VortexQuoter{}
}

// GetQuoteExactIn calculates output amount for CLMM swap (optimized hot path)
// Optimizations:
// - Metrics sampled 1/128 calls (saves ~80ns/call)
// - Uses uint256 price impact calculation (saves 8 pooled big.Int ops)
// - Minimizes big.Int allocations (only 2 for return struct)
func (q *VortexQuoter) GetQuoteExactIn(pool *domain.Pool, amountIn *big.Int, aToB bool) (*domain.SwapQuote, error) {
	// Sample metrics 1/128 to reduce hot-path overhead
	sample := vortexQuoteCounter.Add(1)&0x7F == 0
	var start time.Time
	if sample {
		start = time.Now()
	}

	if pool == nil {
		return nil, ErrInvalidPool
	}
	if pool.Type != domain.PoolTypeVortex {
		return nil, ErrUnsupportedPoolType
	}

	vortexData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || vortexData == nil || vortexData.ParsedVortex == nil {
		return nil, errors.New("missing Vortex specific data")
	}

	vortex := vortexData.ParsedVortex
	tickArrays := vortexData.ParsedTickArrays
	if tickArrays == nil {
		tickArrays = emptyTickArrays
	}

	// Fast path: check IsUint64 before conversion
	if !amountIn.IsUint64() {
		return nil, errors.New("amount too large for u64")
	}
	amountInUint64 := amountIn.Uint64()

	result, err := ComputeSwapExactIn(
		amountInUint64,
		aToB,
		vortex,
		tickArrays,
	)
	if err != nil {
		return nil, fmt.Errorf("CLMM calculation error: %w", err)
	}

	// Use uint256 price impact calculation (zero-alloc via pool)
	var priceImpact uint16
	if vortexData.SqrtPriceX64 != nil {
		sqrtPriceU256 := GetU256()
		U256FromBigInt(vortexData.SqrtPriceX64, sqrtPriceU256)
		priceImpact = CalculatePriceImpactU256(
			amountInUint64,
			result.AmountOut,
			aToB,
			sqrtPriceU256,
			uint32(vortex.FeeRate),
		)
		PutU256(sqrtPriceU256)
	}

	// Only allocate big.Int for return struct (required by interface)
	amountOutBig := new(big.Int).SetUint64(result.AmountOut)
	feeBig := new(big.Int).SetUint64(result.FeeAmount)

	if sample {
		metrics.VortexQuoteDuration.Observe(time.Since(start).Seconds())
	}

	return &domain.SwapQuote{
		AmountIn:       amountIn,
		AmountOut:      amountOutBig,
		PriceImpactBps: priceImpact,
		Fee:            feeBig,
		Pool:           pool,
		AToB:           aToB,
	}, nil
}

// GetQuoteExactOut calculates input amount for CLMM swap (optimized hot path)
func (q *VortexQuoter) GetQuoteExactOut(pool *domain.Pool, amountOut *big.Int, aToB bool) (*domain.SwapQuote, error) {
	// Sample metrics 1/128 to reduce hot-path overhead
	sample := vortexQuoteCounter.Add(1)&0x7F == 0
	var start time.Time
	if sample {
		start = time.Now()
	}

	if pool == nil {
		return nil, ErrInvalidPool
	}
	if pool.Type != domain.PoolTypeVortex {
		return nil, ErrUnsupportedPoolType
	}

	vortexData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || vortexData == nil || vortexData.ParsedVortex == nil {
		return nil, errors.New("missing Vortex specific data")
	}

	vortex := vortexData.ParsedVortex
	tickArrays := vortexData.ParsedTickArrays
	if tickArrays == nil {
		tickArrays = emptyTickArrays
	}

	// Fast path: check IsUint64 before conversion
	if !amountOut.IsUint64() {
		return nil, errors.New("amount too large for u64")
	}
	amountOutUint64 := amountOut.Uint64()

	result, err := ComputeSwapExactOut(
		amountOutUint64,
		aToB,
		vortex,
		tickArrays,
	)
	if err != nil {
		return nil, fmt.Errorf("CLMM calculation error: %w", err)
	}

	// Use uint256 price impact calculation (zero-alloc via pool)
	var priceImpact uint16
	if vortexData.SqrtPriceX64 != nil {
		sqrtPriceU256 := GetU256()
		U256FromBigInt(vortexData.SqrtPriceX64, sqrtPriceU256)
		priceImpact = CalculatePriceImpactU256(
			result.AmountIn,
			amountOutUint64,
			aToB,
			sqrtPriceU256,
			uint32(vortex.FeeRate),
		)
		PutU256(sqrtPriceU256)
	}

	// Only allocate big.Int for return struct (required by interface)
	amountInBig := new(big.Int).SetUint64(result.AmountIn)
	feeBig := new(big.Int).SetUint64(result.FeeAmount)

	if sample {
		metrics.VortexQuoteDuration.Observe(time.Since(start).Seconds())
	}

	return &domain.SwapQuote{
		AmountIn:       amountInBig,
		AmountOut:      amountOut,
		PriceImpactBps: priceImpact,
		Fee:            feeBig,
		Pool:           pool,
		AToB:           aToB,
	}, nil
}

func (q *VortexQuoter) SupportsPoolType(poolType domain.PoolType) bool {
	return poolType == domain.PoolTypeVortex
}

// GetFastQuoteExactIn returns a FastSwapQuote (zero big.Int allocation)
// Use this in the internal routing hot path
func (q *VortexQuoter) GetFastQuoteExactIn(pool *domain.Pool, amountIn uint64, aToB bool) (*FastSwapQuote, error) {
	if pool == nil {
		return nil, ErrInvalidPool
	}
	if pool.Type != domain.PoolTypeVortex {
		return nil, ErrUnsupportedPoolType
	}

	vortexData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || vortexData == nil || vortexData.ParsedVortex == nil {
		return nil, errors.New("missing Vortex specific data")
	}

	vortex := vortexData.ParsedVortex
	tickArrays := vortexData.ParsedTickArrays
	if tickArrays == nil {
		tickArrays = emptyTickArrays
	}

	result, err := ComputeSwapExactIn(amountIn, aToB, vortex, tickArrays)
	if err != nil {
		return nil, err
	}

	// Calculate price impact using uint256 (zero-alloc via pool)
	var priceImpact uint16
	if vortexData.SqrtPriceX64 != nil {
		sqrtPriceU256 := GetU256()
		U256FromBigInt(vortexData.SqrtPriceX64, sqrtPriceU256)
		priceImpact = CalculatePriceImpactU256(
			amountIn,
			result.AmountOut,
			aToB,
			sqrtPriceU256,
			uint32(vortex.FeeRate),
		)
		PutU256(sqrtPriceU256)
	}

	return &FastSwapQuote{
		AmountIn:       amountIn,
		AmountOut:      result.AmountOut,
		Fee:            result.FeeAmount,
		PriceImpactBps: priceImpact,
		Pool:           pool,
		AToB:           aToB,
	}, nil
}

// GetFastQuoteExactOut returns a FastSwapQuote for exact output (zero big.Int allocation)
func (q *VortexQuoter) GetFastQuoteExactOut(pool *domain.Pool, amountOut uint64, aToB bool) (*FastSwapQuote, error) {
	if pool == nil {
		return nil, ErrInvalidPool
	}
	if pool.Type != domain.PoolTypeVortex {
		return nil, ErrUnsupportedPoolType
	}

	vortexData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || vortexData == nil || vortexData.ParsedVortex == nil {
		return nil, errors.New("missing Vortex specific data")
	}

	vortex := vortexData.ParsedVortex
	tickArrays := vortexData.ParsedTickArrays
	if tickArrays == nil {
		tickArrays = emptyTickArrays
	}

	result, err := ComputeSwapExactOut(amountOut, aToB, vortex, tickArrays)
	if err != nil {
		return nil, err
	}

	// Calculate price impact using uint256 (zero-alloc via pool)
	var priceImpact uint16
	if vortexData.SqrtPriceX64 != nil {
		sqrtPriceU256 := GetU256()
		U256FromBigInt(vortexData.SqrtPriceX64, sqrtPriceU256)
		priceImpact = CalculatePriceImpactU256(
			result.AmountIn,
			amountOut,
			aToB,
			sqrtPriceU256,
			uint32(vortex.FeeRate),
		)
		PutU256(sqrtPriceU256)
	}

	return &FastSwapQuote{
		AmountIn:       result.AmountIn,
		AmountOut:      amountOut,
		Fee:            result.FeeAmount,
		PriceImpactBps: priceImpact,
		Pool:           pool,
		AToB:           aToB,
	}, nil
}

// Pre-allocated empty slice to avoid allocation when tickArrays is nil
var emptyTickArrays = []*vortex_go.TickArrayAccount{}

// CalculatePriceImpactU256Fast is an inlined version for maximum performance
// when sqrtPriceX64 is already in uint256 format
func CalculatePriceImpactU256Fast(
	amountIn uint64,
	amountOut uint64,
	aToB bool,
	sqrtPriceX64 *uint256.Int,
	feeRate uint32,
) uint16 {
	if amountIn == 0 || amountOut == 0 || sqrtPriceX64.IsZero() {
		return 0
	}

	// Get temporary values from pool
	sqrtPrice := GetU256()
	effectivePrice := GetU256()
	currentPrice := GetU256()
	temp := GetU256()
	feeAmount := GetU256()
	amountInU := GetU256()
	amountOutU := GetU256()
	amountInEffective := GetU256()

	defer func() {
		PutU256(sqrtPrice)
		PutU256(effectivePrice)
		PutU256(currentPrice)
		PutU256(temp)
		PutU256(feeAmount)
		PutU256(amountInU)
		PutU256(amountOutU)
		PutU256(amountInEffective)
	}()

	amountInU.SetUint64(amountIn)
	amountOutU.SetUint64(amountOut)

	// sqrtPrice = sqrtPriceX64^2 / Q64
	sqrtPrice.Mul(sqrtPriceX64, sqrtPriceX64)
	sqrtPrice.Div(sqrtPrice, u256Q64)

	// Calculate fee amount: feeAmount = amountIn * feeRate / 1000000
	feeAmount.SetUint64(uint64(feeRate))
	feeAmount.Mul(amountInU, feeAmount)
	feeAmount.Div(feeAmount, u256FeeBase)

	// amountInEffective = amountIn - feeAmount
	if amountInU.Cmp(feeAmount) <= 0 {
		return 0
	}
	amountInEffective.Sub(amountInU, feeAmount)

	if aToB {
		currentPrice.Set(sqrtPrice)
		effectivePrice.Mul(amountOutU, u256Q64)
		effectivePrice.Div(effectivePrice, amountInEffective)
	} else {
		currentPrice.Mul(u256Q64, u256Q64)
		currentPrice.Div(currentPrice, sqrtPrice)
		effectivePrice.Mul(amountOutU, u256Q64)
		effectivePrice.Div(effectivePrice, amountInEffective)
	}

	if currentPrice.IsZero() || effectivePrice.Cmp(currentPrice) >= 0 {
		return 0
	}

	// impact = (currentPrice - effectivePrice) * 10000 / currentPrice
	temp.Sub(currentPrice, effectivePrice)
	temp.Mul(temp, u256BpsDenom)
	temp.Div(temp, currentPrice)

	if temp.IsUint64() {
		val := temp.Uint64()
		if val > 65535 {
			return 65535
		}
		return uint16(val)
	}
	return 65535
}
