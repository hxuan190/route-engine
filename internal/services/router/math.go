package router

import (
	"errors"
	"fmt"

	valiant "github.com/thehyperflames/valiant_go"
	vortex_go "github.com/thehyperflames/valiant_go/generated/valiant"
)

var (
	ErrInvalidLiquidity2   = errors.New("invalid liquidity")
	ErrInvalidSqrtPrice2   = errors.New("invalid sqrt price")
	ErrInvalidTickSpacing2 = errors.New("invalid tick spacing")
	ErrInvalidAmount2      = errors.New("invalid amount")
	ErrCLMMCalculation2    = errors.New("CLMM calculation failed")
)

type CLMMSwapResult struct {
	AmountIn  uint64
	AmountOut uint64
	FeeAmount uint64
}

func ComputeSwapExactIn(
	amountIn uint64,
	aToB bool,
	vortex *vortex_go.VortexAccount,
	tickArrays []*vortex_go.TickArrayAccount,
) (*CLMMSwapResult, error) {
	if vortex == nil {
		return nil, ErrInvalidPool
	}
	if amountIn == 0 {
		return nil, ErrInvalidAmount2
	}

	result, err := valiant.ComputeSwapExactIn(amountIn, aToB, vortex, tickArrays)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCLMMCalculation2, err)
	}

	return &CLMMSwapResult{
		AmountIn:  result.AmountIn,
		AmountOut: result.AmountOut,
		FeeAmount: result.FeeAmount,
	}, nil
}

func ComputeSwapExactOut(
	amountOut uint64,
	aToB bool,
	vortex *vortex_go.VortexAccount,
	tickArrays []*vortex_go.TickArrayAccount,
) (*CLMMSwapResult, error) {
	if vortex == nil {
		return nil, ErrInvalidPool
	}
	if amountOut == 0 {
		return nil, ErrInvalidAmount2
	}

	result, err := valiant.ComputeSwapExactOut(amountOut, aToB, vortex, tickArrays)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCLMMCalculation2, err)
	}

	return &CLMMSwapResult{
		AmountIn:  result.AmountIn,
		AmountOut: result.AmountOut,
		FeeAmount: result.FeeAmount,
	}, nil
}
