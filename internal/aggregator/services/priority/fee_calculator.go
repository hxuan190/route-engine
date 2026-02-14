package priority

import (
	"context"
	"sort"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Urgency represents the priority level for a transaction
type Urgency uint8

const (
	// UrgencyLow uses p50 (median) priority fee - non-urgent swaps
	UrgencyLow Urgency = iota
	// UrgencyMedium uses p75 priority fee - normal swaps
	UrgencyMedium
	// UrgencyHigh uses p90 priority fee - time-sensitive
	UrgencyHigh
	// UrgencyExtreme uses p99 priority fee - MEV protection
	UrgencyExtreme
)

// DefaultFees are fallback fees when RPC fails (microLamports per CU)
var DefaultFees = map[Urgency]uint64{
	UrgencyLow:     1000,    // 0.001 lamports per CU
	UrgencyMedium:  10000,   // 0.01 lamports per CU
	UrgencyHigh:    100000,  // 0.1 lamports per CU
	UrgencyExtreme: 1000000, // 1 lamport per CU
}

// FeeCalculator calculates optimal priority fees based on network conditions
type FeeCalculator struct {
	rpcClient *rpc.Client
}

// NewFeeCalculator creates a new fee calculator
func NewFeeCalculator(rpcClient *rpc.Client) *FeeCalculator {
	return &FeeCalculator{rpcClient: rpcClient}
}

// PriorityFeeResult holds the calculated fee information
type PriorityFeeResult struct {
	FeePerCU    uint64 // microLamports per compute unit
	Urgency     Urgency
	Percentile  int // Which percentile was used
	SampleCount int // Number of recent fees sampled
}

// GetOptimalFee calculates the optimal priority fee based on urgency
func (f *FeeCalculator) GetOptimalFee(ctx context.Context, urgency Urgency, accounts []solana.PublicKey) (*PriorityFeeResult, error) {
	// Fetch recent prioritization fees for relevant accounts
	recentFees, err := f.rpcClient.GetRecentPrioritizationFees(ctx, accounts)
	if err != nil {
		// Return default fee on error
		return &PriorityFeeResult{
			FeePerCU:    DefaultFees[urgency],
			Urgency:     urgency,
			Percentile:  getPercentileForUrgency(urgency),
			SampleCount: 0,
		}, nil
	}

	// Extract fee values
	fees := make([]uint64, 0, len(recentFees))
	for _, fee := range recentFees {
		if fee.PrioritizationFee > 0 {
			fees = append(fees, fee.PrioritizationFee)
		}
	}

	if len(fees) == 0 {
		return &PriorityFeeResult{
			FeePerCU:    DefaultFees[urgency],
			Urgency:     urgency,
			Percentile:  getPercentileForUrgency(urgency),
			SampleCount: 0,
		}, nil
	}

	// Sort fees for percentile calculation
	sort.Slice(fees, func(i, j int) bool { return fees[i] < fees[j] })

	// Calculate percentile based on urgency
	percentile := getPercentileForUrgency(urgency)
	feePerCU := calculatePercentile(fees, percentile)

	// Apply minimum floor
	if feePerCU < 100 {
		feePerCU = 100 // Minimum 100 microLamports
	}

	return &PriorityFeeResult{
		FeePerCU:    feePerCU,
		Urgency:     urgency,
		Percentile:  percentile,
		SampleCount: len(fees),
	}, nil
}

// getPercentileForUrgency returns the percentile to use for each urgency level
func getPercentileForUrgency(urgency Urgency) int {
	switch urgency {
	case UrgencyLow:
		return 50
	case UrgencyMedium:
		return 75
	case UrgencyHigh:
		return 90
	case UrgencyExtreme:
		return 99
	default:
		return 75
	}
}

// calculatePercentile returns the value at the given percentile
func calculatePercentile(sorted []uint64, percentile int) uint64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 100 {
		return sorted[len(sorted)-1]
	}

	// Calculate index
	k := float64(percentile) / 100.0 * float64(len(sorted)-1)
	f := int(k)
	c := f + 1
	if c >= len(sorted) {
		c = len(sorted) - 1
	}

	// Linear interpolation
	d := k - float64(f)
	return uint64(float64(sorted[f])*(1-d) + float64(sorted[c])*d)
}

// GetFeeForAmount calculates total priority fee for given CU amount
func (r *PriorityFeeResult) GetFeeForAmount(computeUnits uint32) uint64 {
	return r.FeePerCU * uint64(computeUnits)
}
