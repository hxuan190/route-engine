package router

import (
	"math/big"
	"sync"

	"github.com/holiman/uint256"
)

// Pre-computed constants (avoid allocation on every call)
var (
	// Q64 = 2^64 for fixed-point math
	Q64 = new(big.Int).Lsh(big.NewInt(1), 64)
	// Q128 = 2^128
	Q128 = new(big.Int).Lsh(big.NewInt(1), 128)
	// BPS_DENOM = 10000 for basis points
	BPS_DENOM = big.NewInt(10000)
	// FEE_BASE = 1000000 for fee rate calculations
	FEE_BASE = big.NewInt(1000000)
	// ZERO for comparisons
	ZERO = big.NewInt(0)
	// ONE for calculations
	ONE = big.NewInt(1)
	// HUNDRED for percentage calculations
	HUNDRED = big.NewInt(100)

	// uint256 versions
	u256Q64      = uint256.NewInt(0).Lsh(uint256.NewInt(1), 64)
	u256BpsDenom = uint256.NewInt(10000)
	u256FeeBase  = uint256.NewInt(1000000)
	u256Zero     = uint256.NewInt(0)
	u256Hundred  = uint256.NewInt(100)
)

// Object pools for zero-allocation hot path

var uint256Pool = sync.Pool{
	New: func() interface{} {
		return new(uint256.Int)
	},
}

var bigIntPool = sync.Pool{
	New: func() interface{} {
		return new(big.Int)
	},
}

var bigFloatPool = sync.Pool{
	New: func() interface{} {
		return new(big.Float)
	},
}

// GetU256 gets a uint256.Int from the pool
func GetU256() *uint256.Int {
	return uint256Pool.Get().(*uint256.Int)
}

// PutU256 returns a uint256.Int to the pool
func PutU256(v *uint256.Int) {
	v.Clear()
	uint256Pool.Put(v)
}

// GetBigInt gets a big.Int from the pool
func GetBigInt() *big.Int {
	return bigIntPool.Get().(*big.Int)
}

// PutBigInt returns a big.Int to the pool
func PutBigInt(v *big.Int) {
	v.SetInt64(0)
	bigIntPool.Put(v)
}

// GetBigFloat gets a big.Float from the pool
func GetBigFloat() *big.Float {
	return bigFloatPool.Get().(*big.Float)
}

// PutBigFloat returns a big.Float to the pool
func PutBigFloat(v *big.Float) {
	v.SetFloat64(0)
	bigFloatPool.Put(v)
}

// U256FromBigInt converts big.Int to uint256.Int (zero allocation if using pool)
func U256FromBigInt(b *big.Int, out *uint256.Int) *uint256.Int {
	if b == nil {
		out.Clear()
		return out
	}
	out.SetFromBig(b)
	return out
}

// U256ToBigInt converts uint256.Int to big.Int (uses pool)
func U256ToBigInt(u *uint256.Int) *big.Int {
	return u.ToBig()
}

// U256Mul multiplies two uint256 values: out = a * b
func U256Mul(a, b, out *uint256.Int) *uint256.Int {
	out.Mul(a, b)
	return out
}

// U256Div divides two uint256 values: out = a / b
func U256Div(a, b, out *uint256.Int) *uint256.Int {
	if b.IsZero() {
		out.Clear()
		return out
	}
	out.Div(a, b)
	return out
}

// U256Add adds two uint256 values: out = a + b
func U256Add(a, b, out *uint256.Int) *uint256.Int {
	out.Add(a, b)
	return out
}

// U256Sub subtracts two uint256 values: out = a - b
func U256Sub(a, b, out *uint256.Int) *uint256.Int {
	out.Sub(a, b)
	return out
}

// CalculatePriceImpactU256 calculates price impact using uint256 (zero allocation)
// Returns price impact in basis points (0-10000)
func CalculatePriceImpactU256(
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
		// A->B: currentPrice = sqrtPrice
		currentPrice.Set(sqrtPrice)
		// effectivePrice = amountOut * Q64 / amountInEffective
		effectivePrice.Mul(amountOutU, u256Q64)
		effectivePrice.Div(effectivePrice, amountInEffective)
	} else {
		// B->A: currentPrice = Q64^2 / sqrtPrice
		currentPrice.Mul(u256Q64, u256Q64)
		currentPrice.Div(currentPrice, sqrtPrice)
		// effectivePrice = amountOut * Q64 / amountInEffective
		effectivePrice.Mul(amountOutU, u256Q64)
		effectivePrice.Div(effectivePrice, amountInEffective)
	}

	if currentPrice.IsZero() {
		return 0
	}

	// If effective >= current, no negative impact
	if effectivePrice.Cmp(currentPrice) >= 0 {
		return 0
	}

	// impact = (currentPrice - effectivePrice) * 10000 / currentPrice
	temp.Sub(currentPrice, effectivePrice)
	temp.Mul(temp, u256BpsDenom)
	temp.Div(temp, currentPrice)

	// Cap at max uint16
	if temp.IsUint64() {
		val := temp.Uint64()
		if val > 65535 {
			return 65535
		}
		return uint16(val)
	}
	return 65535
}

// CalculateSplitAmountU256 calculates split amount: total * percent / 100
func CalculateSplitAmountU256(total uint64, percent uint8) uint64 {
	if percent == 0 || percent > 100 {
		return 0
	}
	return (total * uint64(percent)) / 100
}

// CompareLiquidity compares two liquidity values for ranking
// Returns: 1 if a > b, -1 if a < b, 0 if equal
func CompareLiquidity(a, b *big.Int) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	return a.Cmp(b)
}

// SafeUint64 safely converts big.Int to uint64, returning 0 if overflow
func SafeUint64(b *big.Int) uint64 {
	if b == nil || b.Sign() <= 0 {
		return 0
	}
	if !b.IsUint64() {
		return 0
	}
	return b.Uint64()
}

// MulDiv performs (a * b) / c with full precision intermediate
func MulDiv(a, b, c uint64) uint64 {
	if c == 0 {
		return 0
	}
	// Use uint256 for overflow-safe multiplication
	result := GetU256()
	temp := GetU256()
	defer func() {
		PutU256(result)
		PutU256(temp)
	}()

	result.SetUint64(a)
	temp.SetUint64(b)
	result.Mul(result, temp)
	temp.SetUint64(c)
	result.Div(result, temp)

	if result.IsUint64() {
		return result.Uint64()
	}
	return 0
}
