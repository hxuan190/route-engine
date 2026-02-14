package market

import (
	"context"
	"fmt"
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
)

type MarketRegistry struct {
	quoters     []PoolQuoter
	builders    []InstructionBuilder
	subscribers []PoolSubscriber
	validators  []PoolValidator
}

func NewMarketRegistry() *MarketRegistry {
	return &MarketRegistry{
		quoters:     make([]PoolQuoter, 0),
		builders:    make([]InstructionBuilder, 0),
		subscribers: make([]PoolSubscriber, 0),
		validators:  make([]PoolValidator, 0),
	}
}

func NewDefaultMarketRegistry() *MarketRegistry {
	r := NewMarketRegistry()
	r.RegisterValidator(NewVortexValidator())
	return r
}

func (r *MarketRegistry) RegisterQuoter(quoter PoolQuoter) {
	r.quoters = append(r.quoters, quoter)
}
func (r *MarketRegistry) RegisterBuilder(builder InstructionBuilder) {
	r.builders = append(r.builders, builder)
}

func (r *MarketRegistry) RegisterSubscriber(subscriber PoolSubscriber) {
	r.subscribers = append(r.subscribers, subscriber)
}
func (r *MarketRegistry) RegisterValidator(validator PoolValidator) {
	r.validators = append(r.validators, validator)
}

func (r *MarketRegistry) GetQuoteExactIn(pool *domain.Pool, amountIn *big.Int, aToB bool) (*domain.SwapQuote, error) {
	for _, quoter := range r.quoters {
		if quoter.SupportsPoolType(pool.Type) {
			return quoter.GetQuoteExactIn(pool, amountIn, aToB)
		}
	}
	return nil, fmt.Errorf("no quoter found for pool type: %v", pool.Type)
}

func (r *MarketRegistry) GetQuoteExactOut(pool *domain.Pool, amountOut *big.Int, aToB bool) (*domain.SwapQuote, error) {
	for _, quoter := range r.quoters {
		if quoter.SupportsPoolType(pool.Type) {
			return quoter.GetQuoteExactOut(pool, amountOut, aToB)
		}
	}
	return nil, fmt.Errorf("no quoter found for pool type: %v", pool.Type)
}

func (r *MarketRegistry) GetQuote(pool *domain.Pool, amount *big.Int, aToB bool, exactIn bool) (*domain.SwapQuote, error) {
	if exactIn {
		return r.GetQuoteExactIn(pool, amount, aToB)
	}
	return r.GetQuoteExactOut(pool, amount, aToB)
}

func (r *MarketRegistry) BuildSwapInstruction(
	ctx context.Context,
	pool *domain.Pool,
	hop *domain.HopQuote,
	userWallet solana.PublicKey,
	userSourceATA solana.PublicKey,
	userDestATA solana.PublicKey,
	amountIn uint64,
	minAmountOut uint64,
) (solana.Instruction, error) {
	for _, builder := range r.builders {
		if builder.SupportsPoolType(pool.Type) {
			return builder.BuildSwapInstruction(ctx, pool, hop, userWallet, userSourceATA, userDestATA, amountIn, minAmountOut)
		}
	}
	return nil, fmt.Errorf("no instruction builder found for pool type: %v", pool.Type)
}

func (r *MarketRegistry) BuildSwapAccounts(
	ctx context.Context,
	pool *domain.Pool,
	hop *domain.HopQuote,
	userWallet solana.PublicKey,
) ([]*solana.AccountMeta, error) {
	for _, builder := range r.builders {
		if builder.SupportsPoolType(pool.Type) {
			return builder.BuildSwapAccounts(ctx, pool, hop, userWallet)
		}
	}
	return nil, fmt.Errorf("no instruction builder found for pool type: %v", pool.Type)
}

func (r *MarketRegistry) IsPoolReady(pool *domain.Pool) bool {
	for _, validator := range r.validators {
		if validator.SupportsPoolType(pool.Type) {
			return validator.IsReady(pool)
		}
	}
	return pool.Active && pool.Ready
}

func (r *MarketRegistry) GetAllSubscribers() []PoolSubscriber {
	return r.subscribers
}
