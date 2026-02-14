package domain

import (
	"math/big"

	"github.com/gagliardetto/solana-go"
)

type QuoteResult struct {
	Pool           *Pool
	AmountIn       *big.Int
	AmountOut      *big.Int
	FeeAmount      *big.Int
	AToB           bool
	PriceImpactBps uint16
}

type MultiHopQuoteResult struct {
	Route          []solana.PublicKey
	Hops           []HopQuote
	AmountIn       *big.Int
	AmountOut      *big.Int
	TotalFee       *big.Int
	PriceImpactBps uint16
	// Split routing fields
	IsSplitRoute  bool
	SplitPercents []uint8
}

type HopQuote struct {
	Pool           *Pool
	AmountIn       *big.Int
	AmountOut      *big.Int
	FeeAmount      *big.Int
	AToB           bool
	PriceImpactBps uint16
}

type SwapQuote struct {
	AmountIn       *big.Int
	AmountOut      *big.Int
	PriceImpactBps uint16
	Fee            *big.Int
	Pool           *Pool
	AToB           bool
}

type SplitRoute struct {
	Pool           *Pool
	Percent        uint8
	AmountIn       *big.Int
	AmountOut      *big.Int
	FeeAmount      *big.Int
	AToB           bool
	PriceImpactBps uint16
}

type SplitQuoteResult struct {
	InputMint      solana.PublicKey
	OutputMint     solana.PublicKey
	Splits         []SplitRoute
	TotalAmountIn  *big.Int
	TotalAmountOut *big.Int
	TotalFee       *big.Int
	PriceImpactBps uint16
}
