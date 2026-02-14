package http

import (
	"math/big"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gin-gonic/gin"

	aggregator "github.com/hxuan190/route-engine/internal"
	"github.com/hxuan190/route-engine/internal/domain"
	"github.com/hxuan190/route-engine/internal/http/httputil"
	"github.com/hxuan190/route-engine/internal/metrics"
)

// RouteHandler handles HTTP requests for Hyperion Aggregator Route endpoints.
// This is separate from SwapHandler which uses Vortex directly.
type SwapHandler struct {
	aggregatorSvc *aggregator.Service
}

// NewRouteHandler creates a new route HTTP handler.
func NewSwapHandler(aggregatorSvc *aggregator.Service) *SwapHandler {
	return &SwapHandler{aggregatorSvc: aggregatorSvc}
}

func (h *SwapHandler) SetRoutes(pub *gin.RouterGroup, private *gin.RouterGroup, admin *gin.RouterGroup) {
	pub.POST("", h.buildSwap)
}

func (h *SwapHandler) Root() string {
	return "/swap"
}

// SwapHandlerRequest represents the parameters for building a swap transaction
type SwapHandlerRequest struct {
	// User's wallet address that will sign and execute the transaction
	// This wallet must have sufficient balance for the swap and transaction fees
	UserWallet string `json:"userWallet" binding:"required" example:"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"`

	// Input token mint address (Solana base58 public key)
	InputMint string `json:"inputMint" binding:"required" example:"So11111111111111111111111111111111111111112"`

	// Output token mint address (Solana base58 public key)
	OutputMint string `json:"outputMint" binding:"required" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Amount in smallest token units (lamports for SOL, base units for SPL tokens)
	Amount string `json:"amount" binding:"required" example:"1000000000"`

	// Swap mode: "ExactIn" (exact input) or "ExactOut" (exact output)
	SwapMode string `json:"swapMode" binding:"required" enums:"ExactIn,ExactOut" example:"ExactIn"`

	// Slippage tolerance in basis points (1 bps = 0.01%)
	// Default: 50 bps (0.5%) if not specified
	SlippageBps uint16 `json:"slippageBps" example:"50"`

	// Skip transaction simulation (faster but no validation)
	// Set to true only if you want to skip pre-flight checks
	// Default: false (simulation is performed)
	SkipSimulation bool `json:"skipSimulation" example:"true"`
}

// SwapHandlerResponse contains the unsigned transaction and swap details
type SwapHandlerResponse struct {
	// Base64-encoded unsigned transaction ready to be signed and submitted
	// Client must sign this transaction with the user's wallet before broadcasting
	Transaction string `json:"transaction" example:"AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACAAQAHEAo..."`

	// Last valid block height for this transaction
	// Transaction must be confirmed before this block height or it will expire
	// Typically valid for ~60 seconds from creation
	LastValidBlockHeight uint64 `json:"lastValidBlockHeight" example:"245832190"`

	// Actual input amount in smallest token units
	// For ExactIn: same as requested amount
	// For ExactOut: calculated amount needed
	AmountIn string `json:"amountIn" example:"1000000000"`

	// Actual output amount in smallest token units
	// For ExactIn: calculated output
	// For ExactOut: same as requested amount
	AmountOut string `json:"amountOut" example:"145320000"`

	// Minimum output amount after slippage (ExactIn mode only)
	// Transaction will fail if actual output is less than this
	MinAmountOut string `json:"minAmountOut,omitempty" example:"144593400"`

	// Maximum input amount after slippage (ExactOut mode only)
	// Transaction will fail if actual input exceeds this
	MaxAmountIn string `json:"maxAmountIn,omitempty" example:"1005000000"`

	// Total fee amount charged by the aggregator (in output token units)
	FeeAmount string `json:"feeAmount" example:"0"`

	// Simulation result (if skipSimulation was false)
	// Contains success status, compute units, logs, and error details
	Simulation *domain.SimulationResult `json:"simulation,omitempty"`

	// Estimated compute units required for transaction execution
	// Used to set appropriate compute budget
	ComputeUnitsEstimate uint64 `json:"computeUnitsEstimate,omitempty" example:"85000"`

	// Complete token path from input to output
	// Array of token mint addresses in order of swap execution
	Route []string `json:"route" example:"So11111111111111111111111111111111111111112,uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Number of swap hops in the route
	// 1 = direct swap, 2+ = multi-hop routing
	HopCount int `json:"hopCount" example:"1"`

	// Pool addresses used in the swap route
	// Array of pool public keys in order of execution
	Pools []string `json:"pools" example:"HJPjoWUrhoZzkNfRpHuieeFk9WcZWjwy6PBjZ81ngndJ"`

	// Whether this is a split route (liquidity split across multiple pools)
	// Currently always false (split routing not yet implemented)
	IsSplitRoute bool `json:"isSplitRoute,omitempty" example:"false"`

	// Percentage split across pools (only if isSplitRoute is true)
	// Array of percentages that sum to 100
	SplitPercents []uint8 `json:"splitPercents,omitempty"`
}

// @Summary Build swap transaction
// @Description Build an unsigned swap transaction ready to be signed and submitted to the Solana network.
// @Description
// @Description **Features:**
// @Description - Automatic route discovery (direct or multi-hop through SOL/USDC)
// @Description - Multi-DEX aggregation (Raydium, Orca, Meteora, PumpFun, etc.)
// @Description - Transaction simulation for validation (optional)
// @Description - Slippage protection with configurable tolerance
// @Description - Automatic ATA (Associated Token Account) creation if needed
// @Description - Compute budget optimization
// @Description
// @Description **Transaction Flow:**
// @Description 1. API builds unsigned transaction with optimal route
// @Description 2. Client receives base64-encoded transaction
// @Description 3. Client signs transaction with user's wallet
// @Description 4. Client submits signed transaction to Solana RPC
// @Description 5. Transaction must confirm before lastValidBlockHeight
// @Description
// @Description **Simulation Results:**
// @Description If skipSimulation is false (default), response includes:
// @Description - Success/failure status
// @Description - Compute units consumed
// @Description - Transaction logs
// @Description - Error details if simulation failed
// @Description - Slippage/insufficient funds warnings
// @Description
// @Description **Error Handling:**
// @Description - 400: Invalid parameters (bad addresses, amounts, etc.)
// @Description - 404: No route found between token pair
// @Description - 500: Transaction building failed (RPC issues, etc.)
// @Tags swap
// @Accept json
// @Produce json
// @Param request body SwapHandlerRequest true "Swap transaction request"
// @Success 200 {object} SwapHandlerResponse "Unsigned transaction ready to sign"
// @Failure 400 {object} map[string]string "Invalid request parameters"
// @Failure 404 {object} map[string]string "No route found or pool data unavailable"
// @Failure 500 {object} map[string]string "Transaction building failed"
// @Router /api/v1/swap [post]
func (h *SwapHandler) buildSwap(c *gin.Context) {
	start := time.Now()
	defer func() {
		metrics.SwapDuration.WithLabelValues("route_" + c.Request.FormValue("swapMode")).Observe(time.Since(start).Seconds())
	}()

	var req SwapHandlerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.HandleBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	userWallet, err := solana.PublicKeyFromBase58(req.UserWallet)
	if err != nil {
		httputil.HandleBadRequest(c, "invalid userWallet address")
		return
	}

	inputMint, err := solana.PublicKeyFromBase58(req.InputMint)
	if err != nil {
		httputil.HandleBadRequest(c, "invalid inputMint address")
		return
	}

	outputMint, err := solana.PublicKeyFromBase58(req.OutputMint)
	if err != nil {
		httputil.HandleBadRequest(c, "invalid outputMint address")
		return
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		httputil.HandleBadRequest(c, "invalid amount: must be a positive integer")
		return
	}

	if req.SwapMode != "ExactIn" && req.SwapMode != "ExactOut" {
		httputil.HandleBadRequest(c, "invalid swapMode: must be ExactIn or ExactOut")
		return
	}

	slippageBps := req.SlippageBps
	if slippageBps == 0 {
		slippageBps = 50
	}

	routeReq := &domain.SwapRequest{
		UserWallet:     userWallet,
		InputMint:      inputMint,
		OutputMint:     outputMint,
		Amount:         amount,
		SwapMode:       req.SwapMode,
		SlippageBps:    slippageBps,
		SkipSimulation: req.SkipSimulation,
	}

	routeRes, err := h.aggregatorSvc.BuildMultiHopRouteTransaction(c.Request.Context(), routeReq)
	if err != nil {
		metrics.SwapRequests.WithLabelValues("route_"+req.SwapMode, "false", "error").Inc()

		switch err {
		case aggregator.ErrNoPoolFound:
			httputil.HandleNotFound(c, "no pool found for token pair")
		case aggregator.ErrMissingPoolData:
			httputil.HandleNotFound(c, "pool data unavailable")
		case aggregator.ErrInvalidUserWallet:
			httputil.HandleBadRequest(c, "invalid user wallet")
		case aggregator.ErrNoRoute:
			httputil.HandleNotFound(c, "no route found between tokens")
		default:
			httputil.HandleInternalError(c, "failed to build route: "+err.Error())
		}
		return
	}

	metrics.SwapRequests.WithLabelValues("route_"+req.SwapMode, "false", "success").Inc()

	if routeRes.Simulation != nil {
		metrics.SimulationRequests.Inc()
		if routeRes.Simulation.Success {
			metrics.SimulationSuccess.Inc()
			if routeRes.Simulation.ComputeUnitsConsumed > 0 {
				metrics.ComputeUnits.Observe(float64(routeRes.Simulation.ComputeUnitsConsumed))
			}
		} else {
			reason := "unknown"
			if routeRes.Simulation.InsufficientFunds {
				reason = "insufficient_funds"
			} else if routeRes.Simulation.SlippageExceeded {
				reason = "slippage_exceeded"
			}
			metrics.SimulationFailures.WithLabelValues(reason).Inc()
		}
	}

	response := SwapHandlerResponse{
		Transaction:          routeRes.Transaction,
		LastValidBlockHeight: routeRes.LastValidBlockHeight,
		AmountIn:             routeRes.AmountIn,
		AmountOut:            routeRes.AmountOut,
		MinAmountOut:         routeRes.MinAmountOut,
		MaxAmountIn:          routeRes.MaxAmountIn,
		FeeAmount:            routeRes.FeeAmount,
		Simulation:           routeRes.Simulation,
		ComputeUnitsEstimate: routeRes.ComputeUnitsEstimate,
		Route:                routeRes.Route,
		HopCount:             routeRes.HopCount,
		Pools:                routeRes.Pools,
		IsSplitRoute:         routeRes.IsSplitRoute,
		SplitPercents:        routeRes.SplitPercents,
	}

	httputil.HandleSuccess(c, response)
}
