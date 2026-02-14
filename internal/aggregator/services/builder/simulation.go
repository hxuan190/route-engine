package builder

import (
	"context"
	"fmt"

	"github.com/gagliardetto/solana-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
)

func (svc *BuilderService) SimulateTransaction(ctx context.Context, tx *solana.Transaction) (*domain.SimulationResult, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is nil")
	}

	result, err := svc.rpcClient.SimulateTransaction(ctx, tx)

	if err != nil {
		return &domain.SimulationResult{
			Success: false,
			Error:   fmt.Sprintf("simulation failed: %v", err),
		}, nil
	}

	simResult := &domain.SimulationResult{
		Success: result.Value.Err == nil,
		Logs:    result.Value.Logs,
	}

	// Extract compute units if available
	if result.Value.UnitsConsumed != nil {
		simResult.ComputeUnitsConsumed = *result.Value.UnitsConsumed
	}

	// Check for errors
	if result.Value.Err != nil {
		simResult.Error = fmt.Sprintf("%v", result.Value.Err)

		// Parse common error patterns
		errorStr := simResult.Error
		simResult.InsufficientFunds = contains(errorStr, "insufficient") || contains(errorStr, "not enough")
		simResult.SlippageExceeded = contains(errorStr, "slippage") || contains(errorStr, "ExceededSlippage")

		// Check for missing accounts
		if contains(errorStr, "AccountNotFound") || contains(errorStr, "InvalidAccountData") {
			simResult.AccountsNeeded = extractMissingAccounts(simResult.Logs)
		}
	}

	return simResult, nil
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// extractMissingAccounts parses simulation logs to find missing account addresses
func extractMissingAccounts(logs []string) []string {
	var accounts []string
	// Simple implementation - can be enhanced to parse specific error patterns
	for _, log := range logs {
		if contains(log, "Account") && contains(log, "not found") {
			// Extract account address from log if possible
			// This is a simplified version
			accounts = append(accounts, "account_creation_needed")
		}
	}
	return accounts
}

// ValidateSwapSimulation validates a swap transaction before returning it to the user
func (svc *BuilderService) ValidateSwapSimulation(ctx context.Context, tx *solana.Transaction) error {
	simResult, err := svc.SimulateTransaction(ctx, tx)
	if err != nil {
		return fmt.Errorf("simulation error: %w", err)
	}

	if !simResult.Success {
		if simResult.InsufficientFunds {
			return fmt.Errorf("insufficient funds: %s", simResult.Error)
		}
		if simResult.SlippageExceeded {
			return fmt.Errorf("slippage tolerance exceeded: %s", simResult.Error)
		}
		return fmt.Errorf("transaction would fail: %s", simResult.Error)
	}

	return nil
}

// EstimateComputeUnits estimates the compute units needed for a transaction
func (svc *BuilderService) EstimateComputeUnits(ctx context.Context, tx *solana.Transaction) (uint64, error) {
	simResult, err := svc.SimulateTransaction(ctx, tx)
	if err != nil {
		return 0, err
	}

	if !simResult.Success {
		return 0, fmt.Errorf("cannot estimate compute units for failing transaction: %s", simResult.Error)
	}

	// Add 20% buffer to the consumed units for safety
	buffer := simResult.ComputeUnitsConsumed * 20 / 100
	return simResult.ComputeUnitsConsumed + buffer, nil
}
