package router

import (
	"math/bits"
	"testing"
)

// TestMulRatioU64PrecisionLoss tests that mulRatioU64 maintains precision for large amounts
func TestMulRatioU64PrecisionLoss(t *testing.T) {
	// Test case: 1 SOL (1e18 lamports) * 0.333333
	amount := uint64(1_000_000_000_000_000_000)
	ratio := 0.333333

	// Our implementation
	resultOptimized := mulRatioU64(amount, ratio)

	// Naive implementation (what we had before)
	resultNaive := uint64(float64(amount) * ratio)

	// Calculate expected using high-precision math
	ratioFixed := uint64(ratio * ratioPrecision)
	hi, lo := bits.Mul64(amount, ratioFixed)
	expected, _ := bits.Div64(hi, lo, ratioPrecision)

	t.Logf("Amount: %d lamports (1 SOL)", amount)
	t.Logf("Ratio: %f", ratio)
	t.Logf("Optimized result: %d", resultOptimized)
	t.Logf("Naive result:     %d", resultNaive)
	t.Logf("Expected:         %d", expected)
	t.Logf("Optimized error:  %d lamports", int64(resultOptimized)-int64(expected))
	t.Logf("Naive error:      %d lamports", int64(resultNaive)-int64(expected))

	// Optimized should match expected exactly
	if resultOptimized != expected {
		t.Errorf("Optimized implementation has error: %d vs %d", resultOptimized, expected)
	}

	// Naive should have precision loss
	naiveError := int64(resultNaive) - int64(expected)
	if naiveError < 0 {
		naiveError = -naiveError
	}

	if naiveError > 0 {
		t.Logf("✓ Naive implementation has precision loss: %d lamports", naiveError)
	}
}

// TestMulRatioU64EdgeCases tests boundary conditions
func TestMulRatioU64EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		amount   uint64
		ratio    float64
		expected uint64
	}{
		{
			name:     "Ratio = 0",
			amount:   1000,
			ratio:    0.0,
			expected: 0,
		},
		{
			name:     "Ratio = 1",
			amount:   1000,
			ratio:    1.0,
			expected: 1000,
		},
		{
			name:     "Ratio > 1 (should clamp)",
			amount:   1000,
			ratio:    1.5,
			expected: 1000,
		},
		{
			name:     "Amount = 0",
			amount:   0,
			ratio:    0.5,
			expected: 0,
		},
		{
			name:     "Max uint64 * 0.5",
			amount:   18_446_744_073_709_551_615,
			ratio:    0.5,
			expected: 9_223_372_036_854_775_807,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mulRatioU64(tt.amount, tt.ratio)

			// Allow small rounding error (< 0.1%)
			diff := int64(result) - int64(tt.expected)
			if diff < 0 {
				diff = -diff
			}

			tolerance := uint64(float64(tt.expected) * 0.001)
			if tolerance == 0 {
				tolerance = 1
			}

			if uint64(diff) > tolerance {
				t.Errorf("mulRatioU64(%d, %f) = %d, expected %d (diff: %d)",
					tt.amount, tt.ratio, result, tt.expected, diff)
			}
		})
	}
}
