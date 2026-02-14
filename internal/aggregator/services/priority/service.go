package priority

import (
	"context"
	"encoding/binary"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// ComputeBudgetProgramID is the compute budget program address
var ComputeBudgetProgramID = solana.MustPublicKeyFromBase58("ComputeBudget111111111111111111111111111111")

// Service provides priority fee estimation and CU optimization
type Service struct {
	cuEstimator   *CUEstimator
	feeCalculator *FeeCalculator
}

// NewService creates a new priority service
func NewService(rpcClient *rpc.Client) *Service {
	return &Service{
		cuEstimator:   NewCUEstimator(rpcClient),
		feeCalculator: NewFeeCalculator(rpcClient),
	}
}

// PriorityConfig holds the computed priority settings for a transaction
type PriorityConfig struct {
	ComputeUnits     uint32 // Compute unit limit
	PriorityFee      uint64 // microLamports per CU
	TotalFeeLamports uint64 // Total priority fee in lamports
	Urgency          Urgency
}

// GetPriorityConfig estimates CU and calculates optimal fee for a transaction
func (s *Service) GetPriorityConfig(ctx context.Context, tx *solana.Transaction, urgency Urgency) (*PriorityConfig, error) {
	// 1. Estimate compute units
	cuResult, err := s.cuEstimator.EstimateCU(ctx, tx)
	if err != nil {
		return nil, err
	}

	// 2. Extract accounts for fee calculation
	accounts := extractWritableAccounts(tx)

	// 3. Get optimal priority fee
	feeResult, err := s.feeCalculator.GetOptimalFee(ctx, urgency, accounts)
	if err != nil {
		return nil, err
	}

	// 4. Calculate total fee
	totalFee := feeResult.GetFeeForAmount(cuResult.UnitsWithBuffer)

	return &PriorityConfig{
		ComputeUnits:     cuResult.UnitsWithBuffer,
		PriorityFee:      feeResult.FeePerCU,
		TotalFeeLamports: totalFee / 1000000, // Convert microLamports to lamports
		Urgency:          urgency,
	}, nil
}

// BuildPriorityInstructions creates the compute budget instructions
func (s *Service) BuildPriorityInstructions(config *PriorityConfig) []solana.Instruction {
	instructions := make([]solana.Instruction, 0, 2)

	// SetComputeUnitLimit instruction (discriminator = 2)
	instructions = append(instructions, NewSetComputeUnitLimitInstruction(config.ComputeUnits))

	// SetComputeUnitPrice instruction (discriminator = 3)
	instructions = append(instructions, NewSetComputeUnitPriceInstruction(config.PriorityFee))

	return instructions
}

// SetComputeUnitLimitInstruction sets the compute unit limit
type SetComputeUnitLimitInstruction struct {
	Units uint32
}

// NewSetComputeUnitLimitInstruction creates a SetComputeUnitLimit instruction
func NewSetComputeUnitLimitInstruction(units uint32) *SetComputeUnitLimitInstruction {
	return &SetComputeUnitLimitInstruction{Units: units}
}

func (ix *SetComputeUnitLimitInstruction) ProgramID() solana.PublicKey {
	return ComputeBudgetProgramID
}

func (ix *SetComputeUnitLimitInstruction) Accounts() []*solana.AccountMeta {
	return nil
}

func (ix *SetComputeUnitLimitInstruction) Data() ([]byte, error) {
	data := make([]byte, 5)
	data[0] = 2 // Discriminator for SetComputeUnitLimit
	binary.LittleEndian.PutUint32(data[1:], ix.Units)
	return data, nil
}

// SetComputeUnitPriceInstruction sets the compute unit price
type SetComputeUnitPriceInstruction struct {
	MicroLamports uint64
}

// NewSetComputeUnitPriceInstruction creates a SetComputeUnitPrice instruction
func NewSetComputeUnitPriceInstruction(microLamports uint64) *SetComputeUnitPriceInstruction {
	return &SetComputeUnitPriceInstruction{MicroLamports: microLamports}
}

func (ix *SetComputeUnitPriceInstruction) ProgramID() solana.PublicKey {
	return ComputeBudgetProgramID
}

func (ix *SetComputeUnitPriceInstruction) Accounts() []*solana.AccountMeta {
	return nil
}

func (ix *SetComputeUnitPriceInstruction) Data() ([]byte, error) {
	data := make([]byte, 9)
	data[0] = 3 // Discriminator for SetComputeUnitPrice
	binary.LittleEndian.PutUint64(data[1:], ix.MicroLamports)
	return data, nil
}

// extractWritableAccounts returns writable accounts from a transaction
func extractWritableAccounts(tx *solana.Transaction) []solana.PublicKey {
	accounts := make([]solana.PublicKey, 0, 8)

	for i, acc := range tx.Message.AccountKeys {
		isWritable, err := tx.Message.IsWritable(acc)
		if err == nil && isWritable {
			accounts = append(accounts, tx.Message.AccountKeys[i])
		}
	}

	// Limit to first 8 accounts for RPC efficiency
	if len(accounts) > 8 {
		accounts = accounts[:8]
	}

	return accounts
}
