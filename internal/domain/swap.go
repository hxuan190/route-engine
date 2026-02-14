package domain

import (
	"math/big"

	"github.com/gagliardetto/solana-go"
)

type SwapRequest struct {
	UserWallet solana.PublicKey

	InputMint solana.PublicKey

	OutputMint solana.PublicKey

	Amount *big.Int

	SwapMode string

	SlippageBps uint16

	SkipSimulation bool
}

type SwapResponse struct {
	Transaction string `json:"transaction"`

	TxSignature string `json:"txSignature,omitempty"`

	LastValidBlockHeight uint64 `json:"lastValidBlockHeight"`

	AmountIn string `json:"amountIn"`

	AmountOut string `json:"amountOut"`

	MinAmountOut string `json:"minAmountOut,omitempty"`

	MaxAmountIn string `json:"maxAmountIn,omitempty"`

	PoolAddress string `json:"poolAddress"`

	FeeAmount string `json:"feeAmount"`

	Simulation *SimulationResult `json:"simulation,omitempty"`

	ComputeUnitsEstimate uint64 `json:"computeUnitsEstimate,omitempty"`
}

type MultiHopSwapResponse struct {
	SwapResponse
	Route         []string `json:"route"`
	HopCount      int      `json:"hopCount"`
	Pools         []string `json:"pools"`
	IsSplitRoute  bool     `json:"isSplitRoute,omitempty"`
	SplitPercents []uint8  `json:"splitPercents,omitempty"`
}

type SimulationResult struct {
	Success              bool     `json:"success"`
	Logs                 []string `json:"logs"`
	ComputeUnitsConsumed uint64   `json:"computeUnitsConsumed"`
	Error                string   `json:"error,omitempty"`

	ComputeUnitsTotal uint64 `json:"computeUnitsTotal,omitempty"`

	AccountsNeeded []string `json:"accountsNeeded,omitempty"`

	InsufficientFunds bool `json:"insufficientFunds"`

	SlippageExceeded bool `json:"slippageExceeded"`
}
