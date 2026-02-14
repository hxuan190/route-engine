package router

import (
	"math/big"
	"testing"

	"github.com/gagliardetto/solana-go"
	"github.com/holiman/uint256"
	"github.com/hxuan190/route-engine/internal/domain"
	vortex_go "github.com/thehyperflames/valiant_go/generated/valiant"
)

// BenchmarkVortexQuoterGetQuoteExactIn benchmarks the optimized quoter
func BenchmarkVortexQuoterGetQuoteExactIn(b *testing.B) {
	quoter := NewVortexQuoter()
	pool := createMockVortexPool()
	amountIn := big.NewInt(1000000000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = quoter.GetQuoteExactIn(pool, amountIn, true)
	}
}

// BenchmarkCalculatePriceImpactU256 benchmarks the uint256 price impact calculation
func BenchmarkCalculatePriceImpactU256(b *testing.B) {
	sqrtPriceX64 := uint256.NewInt(0).Lsh(uint256.NewInt(1), 64)
	amountIn := uint64(1000000000)
	amountOut := uint64(950000000)
	feeRate := uint32(3000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = CalculatePriceImpactU256(amountIn, amountOut, true, sqrtPriceX64, feeRate)
	}
}

// BenchmarkCalculatePriceImpactCLMM benchmarks the big.Int price impact calculation
func BenchmarkCalculatePriceImpactCLMM(b *testing.B) {
	sqrtPriceX64 := new(big.Int).Lsh(big.NewInt(1), 64)
	amountIn := big.NewInt(1000000000)
	amountOut := big.NewInt(950000000)
	feeRate := uint32(3000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = CalculatePriceImpactCLMM(amountIn, amountOut, true, sqrtPriceX64, feeRate)
	}
}

// BenchmarkBFSArena benchmarks the pooled BFS arena operations
func BenchmarkBFSArena(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		arena := getBFSArena()
		arena.markForwardVisited(TokenID(1), -1)
		arena.markForwardVisited(TokenID(2), 1)
		arena.markForwardVisited(TokenID(3), 2)
		arena.markBackwardVisited(TokenID(100), -1)
		arena.markBackwardVisited(TokenID(3), 100)
		_ = arena.isForwardVisited(TokenID(3))
		_ = arena.isBackwardVisited(TokenID(3))
		_ = arena.reconstructPath(TokenID(3), 0)
		putBFSArena(arena)
	}
}

// BenchmarkGetNeighborsInto benchmarks zero-allocation neighbor lookup
func BenchmarkGetNeighborsInto(b *testing.B) {
	adj := newAdjSlice(256)
	for i := 0; i < 10; i++ {
		pool := createMockVortexPool()
		adj.append(TokenID(0), TokenID(i+1), pool)
	}
	buf := make([]TokenID, 0, 64)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		buf = adj.getNeighborsInto(TokenID(0), buf)
	}
}

// BenchmarkGetNeighbors benchmarks the allocating neighbor lookup
func BenchmarkGetNeighbors(b *testing.B) {
	adj := newAdjSlice(256)
	for i := 0; i < 10; i++ {
		pool := createMockVortexPool()
		adj.append(TokenID(0), TokenID(i+1), pool)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = adj.getNeighbors(TokenID(0))
	}
}

// BenchmarkMulRatioOld benchmarks the old big.Float ratio multiplication
func BenchmarkMulRatioOld(b *testing.B) {
	amount := big.NewInt(1000000000000)
	ratio := 0.618

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mulRatioOld(amount, ratio)
	}
}

// mulRatioOld is the old implementation for benchmark comparison
func mulRatioOld(amount *big.Int, ratio float64) *big.Int {
	f := new(big.Float).SetInt(amount)
	f.Mul(f, big.NewFloat(ratio))
	result, _ := f.Int(nil)
	return result
}

// BenchmarkMulRatioU64Direct benchmarks pure uint64 ratio multiplication
func BenchmarkMulRatioU64Direct(b *testing.B) {
	amount := uint64(1000000000000)
	ratio := 0.618

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mulRatioU64(amount, ratio)
	}
}

// TestMulRatioAccuracy verifies the optimized mulRatio produces correct results
func TestMulRatioAccuracy(t *testing.T) {
	testCases := []struct {
		amount uint64
		ratio  float64
	}{
		{1000000000, 0.5},
		{1000000000, 0.618},
		{1000000000, 0.1},
		{1000000000, 0.9},
		{1000000000000, 0.333},
		{999999999999, 0.777},
	}

	for _, tc := range testCases {
		amount := big.NewInt(int64(tc.amount))
		newResult := mulRatio(amount, tc.ratio)
		oldResult := mulRatioOld(amount, tc.ratio)

		// Allow 1 unit difference due to rounding
		diff := new(big.Int).Sub(newResult, oldResult)
		diff.Abs(diff)
		if diff.Cmp(big.NewInt(1)) > 0 {
			t.Errorf("mulRatio(%d, %f): new=%s, old=%s, diff=%s",
				tc.amount, tc.ratio, newResult, oldResult, diff)
		}
	}
}

// BenchmarkRequestScopedCacheGet benchmarks the optimized cache lookup
func BenchmarkRequestScopedCacheGet(b *testing.B) {
	cache := newRequestScopedCache()
	pool := createMockVortexPool()
	amount := big.NewInt(1000000000)
	quote := &domain.SwapQuote{
		AmountIn:  amount,
		AmountOut: big.NewInt(950000000),
	}

	// Pre-populate cache
	cache.Set(pool, amount, true, true, quote)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(pool, amount, true, true)
	}
}

// BenchmarkRequestScopedCacheSet benchmarks the optimized cache set
func BenchmarkRequestScopedCacheSet(b *testing.B) {
	cache := newRequestScopedCache()
	pool := createMockVortexPool()
	amount := big.NewInt(1000000000)
	quote := &domain.SwapQuote{
		AmountIn:  amount,
		AmountOut: big.NewInt(950000000),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cache.Set(pool, amount, true, true, quote)
	}
}

// BenchmarkMakePoolQuoteKey benchmarks the FNV hash key generation
func BenchmarkMakePoolQuoteKey(b *testing.B) {
	pool := createMockVortexPool()
	amount := big.NewInt(1000000000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = makePoolQuoteKey(pool.Address, amount, true, true)
	}
}

// BenchmarkMakePoolQuoteKeyOld benchmarks the old string-based key generation
func BenchmarkMakePoolQuoteKeyOld(b *testing.B) {
	amount := big.NewInt(1000000000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Old approach: string conversion
		_ = amount.String()
	}
}

// BenchmarkSuperEdgeQuoterValue benchmarks stack-allocated SuperEdgeQuoter
func BenchmarkSuperEdgeQuoterValue(b *testing.B) {
	quoter := func(pool *domain.Pool, amount *big.Int, aToB, exactIn bool) (*domain.SwapQuote, error) {
		return &domain.SwapQuote{
			AmountIn:  amount,
			AmountOut: big.NewInt(950000000),
			Fee:       big.NewInt(1000),
		}, nil
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Value type - stack allocated
		seq := SuperEdgeQuoter{quoter: quoter}
		_ = seq
	}
}

// BenchmarkSuperEdgeQuoterPointer benchmarks heap-allocated SuperEdgeQuoter
func BenchmarkSuperEdgeQuoterPointer(b *testing.B) {
	quoter := func(pool *domain.Pool, amount *big.Int, aToB, exactIn bool) (*domain.SwapQuote, error) {
		return &domain.SwapQuote{
			AmountIn:  amount,
			AmountOut: big.NewInt(950000000),
			Fee:       big.NewInt(1000),
		}, nil
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Pointer type - heap allocated
		seq := NewSuperEdgeQuoter(quoter)
		_ = seq
	}
}

// BenchmarkGetPoolsDirect benchmarks zero-copy pool access
func BenchmarkGetPoolsDirect(b *testing.B) {
	se := NewSuperEdge(TokenID(1), TokenID(2),
		solana.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),
		solana.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"))

	for i := 0; i < 5; i++ {
		se.AddPool(createMockVortexPool())
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = se.GetPoolsDirect()
	}
}

// BenchmarkGetPoolsCopy benchmarks copying pool access
func BenchmarkGetPoolsCopy(b *testing.B) {
	se := NewSuperEdge(TokenID(1), TokenID(2),
		solana.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),
		solana.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"))

	for i := 0; i < 5; i++ {
		se.AddPool(createMockVortexPool())
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = se.GetPools()
	}
}

// BenchmarkVortexQuoterFastExactIn benchmarks the zero-allocation fast quoter
func BenchmarkVortexQuoterFastExactIn(b *testing.B) {
	quoter := NewVortexQuoter()
	pool := createMockVortexPool()
	amountIn := uint64(1000000000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = quoter.GetFastQuoteExactIn(pool, amountIn, true)
	}
}

// BenchmarkFastSuperEdgeQuoter benchmarks the fast super edge quoter
func BenchmarkFastSuperEdgeQuoter(b *testing.B) {
	vortexQuoter := NewVortexQuoter()
	fastQuoter := func(pool *domain.Pool, amount uint64, aToB, exactIn bool) (*FastSwapQuote, error) {
		if exactIn {
			return vortexQuoter.GetFastQuoteExactIn(pool, amount, aToB)
		}
		return vortexQuoter.GetFastQuoteExactOut(pool, amount, aToB)
	}

	se := NewImmutableSuperEdge(TokenID(1), TokenID(2),
		solana.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),
		solana.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"),
		[]*domain.Pool{createMockVortexPool()})

	seQuoter := FastSuperEdgeQuoter{quoter: fastQuoter}
	amount := uint64(1000000000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = seQuoter.GetFastQuote(se, amount, true)
	}
}

// BenchmarkSuperEdgeQuoterBigInt benchmarks the big.Int super edge quoter for comparison
func BenchmarkSuperEdgeQuoterBigInt(b *testing.B) {
	vortexQuoter := NewVortexQuoter()
	quoter := func(pool *domain.Pool, amount *big.Int, aToB, exactIn bool) (*domain.SwapQuote, error) {
		if exactIn {
			return vortexQuoter.GetQuoteExactIn(pool, amount, aToB)
		}
		return vortexQuoter.GetQuoteExactOut(pool, amount, aToB)
	}

	se := NewImmutableSuperEdge(TokenID(1), TokenID(2),
		solana.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),
		solana.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"),
		[]*domain.Pool{createMockVortexPool()})

	seQuoter := SuperEdgeQuoter{quoter: quoter}
	amount := big.NewInt(1000000000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = seQuoter.GetQuote(se, amount, true)
	}
}

func createMockVortexPool() *domain.Pool {
	return &domain.Pool{
		Address:    solana.MustPublicKeyFromBase58("11111111111111111111111111111111"),
		Type:       domain.PoolTypeVortex,
		TokenMintA: solana.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),
		TokenMintB: solana.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"),
		ReserveA:   big.NewInt(1000000000000),
		ReserveB:   big.NewInt(50000000000),
		FeeRate:    3000,
		Active:     true,
		Ready:      true,
		TypeSpecific: &domain.VortexPoolData{
			TickSpacing:      64,
			CurrentTickIndex: 0,
			SqrtPriceX64:     new(big.Int).Lsh(big.NewInt(1), 64),
			Liquidity:        big.NewInt(1000000000000),
			ParsedVortex: &vortex_go.VortexAccount{
				FeeRate: 3000,
			},
			ParsedTickArrays: []*vortex_go.TickArrayAccount{},
		},
	}
}
