package market

import (
	"context"
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
)

type PoolQuoter interface {
	GetQuoteExactIn(pool *domain.Pool, amountIn *big.Int, aToB bool) (*domain.SwapQuote, error)

	GetQuoteExactOut(pool *domain.Pool, amountOut *big.Int, aToB bool) (*domain.SwapQuote, error)

	// SupportsPoolType returns true if this quoter can handle the given pool type
	SupportsPoolType(poolType domain.PoolType) bool
}

// InstructionBuilder defines the interface for building swap instructions
type InstructionBuilder interface {
	// BuildSwapInstruction creates a swap instruction for the pool
	BuildSwapInstruction(
		ctx context.Context,
		pool *domain.Pool,
		hop *domain.HopQuote,
		userWallet solana.PublicKey,
		userSourceATA solana.PublicKey,
		userDestATA solana.PublicKey,
		amountIn uint64,
		minAmountOut uint64,
	) (solana.Instruction, error)

	// BuildSwapAccounts builds the remaining accounts for multi-hop routing
	BuildSwapAccounts(
		ctx context.Context,
		pool *domain.Pool,
		hop *domain.HopQuote,
		userWallet solana.PublicKey,
	) ([]*solana.AccountMeta, error)

	// SupportsPoolType returns true if this builder can handle the given pool type
	SupportsPoolType(poolType domain.PoolType) bool
}

// PoolSubscriber defines the interface for subscribing to pool updates
type PoolSubscriber interface {
	// Subscribe starts listening for pool updates
	Subscribe() error

	// FetchHistorical fetches existing pools from the blockchain
	FetchHistorical()

	// GetProgramID returns the program ID this subscriber handles
	GetProgramID() solana.PublicKey

	// GetPoolType returns the pool type this subscriber handles
	GetPoolType() domain.PoolType
}

// PoolValidator defines the interface for validating pool readiness
type PoolValidator interface {
	// IsReady checks if a pool is ready for trading
	IsReady(pool *domain.Pool) bool

	// SupportsPoolType returns true if this validator can handle the given pool type
	SupportsPoolType(poolType domain.PoolType) bool
}
