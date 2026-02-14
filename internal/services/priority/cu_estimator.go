package priority

import (
	"context"
	"errors"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Default compute units when estimation fails
const (
	DefaultComputeUnits       = 200000
	DefaultComputeUnitsBuffer = 220000 // DefaultComputeUnits * 1.1
	ComputeUnitBuffer         = 1.1    // 10% buffer
	MaxComputeUnits           = 1400000
)

var (
	ErrSimulationFailed = errors.New("transaction simulation failed")
	ErrNoUnitsConsumed  = errors.New("simulation returned no units consumed")
)

// CUEstimator estimates compute units for transactions
type CUEstimator struct {
	rpcClient *rpc.Client
}

// NewCUEstimator creates a new compute unit estimator
func NewCUEstimator(rpcClient *rpc.Client) *CUEstimator {
	return &CUEstimator{rpcClient: rpcClient}
}

// CUEstimateResult holds the estimation result
type CUEstimateResult struct {
	UnitsConsumed   uint64   // Actual units from simulation
	UnitsWithBuffer uint32   // Units with safety buffer
	SimulationLogs  []string // Logs from simulation (for debugging)
}

// EstimateCU simulates a transaction and returns the compute units consumed
func (e *CUEstimator) EstimateCU(ctx context.Context, tx *solana.Transaction) (*CUEstimateResult, error) {
	// Configure simulation options
	opts := rpc.SimulateTransactionOpts{
		SigVerify:              false,
		Commitment:             rpc.CommitmentProcessed,
		ReplaceRecentBlockhash: true,
	}

	// Simulate the transaction
	result, err := e.rpcClient.SimulateTransactionWithOpts(ctx, tx, &opts)
	if err != nil {
		return &CUEstimateResult{
			UnitsConsumed:   DefaultComputeUnits,
			UnitsWithBuffer: DefaultComputeUnitsBuffer,
		}, nil
	}

	// Check for simulation error
	if result.Value.Err != nil {
		return &CUEstimateResult{
			UnitsConsumed:   DefaultComputeUnits,
			UnitsWithBuffer: DefaultComputeUnitsBuffer,
			SimulationLogs:  result.Value.Logs,
		}, nil
	}

	// Extract units consumed
	if result.Value.UnitsConsumed == nil || *result.Value.UnitsConsumed == 0 {
		return &CUEstimateResult{
			UnitsConsumed:   DefaultComputeUnits,
			UnitsWithBuffer: DefaultComputeUnitsBuffer,
			SimulationLogs:  result.Value.Logs,
		}, nil
	}

	unitsConsumed := *result.Value.UnitsConsumed

	// Add buffer and cap at max
	unitsWithBuffer := uint32(float64(unitsConsumed) * ComputeUnitBuffer)
	if unitsWithBuffer > MaxComputeUnits {
		unitsWithBuffer = MaxComputeUnits
	}

	return &CUEstimateResult{
		UnitsConsumed:   unitsConsumed,
		UnitsWithBuffer: unitsWithBuffer,
		SimulationLogs:  result.Value.Logs,
	}, nil
}

// EstimateCUFromMessage estimates CU from a transaction message (before signing)
func (e *CUEstimator) EstimateCUFromMessage(ctx context.Context, msg *solana.Message, signers []solana.PublicKey) (*CUEstimateResult, error) {
	// Create unsigned transaction
	tx := &solana.Transaction{
		Message: *msg,
	}

	// Add placeholder signatures
	tx.Signatures = make([]solana.Signature, len(signers))

	return e.EstimateCU(ctx, tx)
}
