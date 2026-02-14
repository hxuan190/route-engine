package http

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gin-gonic/gin"

	aggregator "github.com/hxuan190/route-engine/internal"
	"github.com/hxuan190/route-engine/internal/domain"
	"github.com/hxuan190/route-engine/internal/http/httputil"
	"github.com/hxuan190/route-engine/internal/metrics"
	"github.com/hxuan190/route-engine/internal/services/router"
)

// Object pools for reducing allocations in hot paths
var (
	routeInfoPool = sync.Pool{
		New: func() interface{} {
			return make([]RouteInfo, 0, 3) // Max 3 hops
		},
	}
	routePathPool = sync.Pool{
		New: func() interface{} {
			return make([]string, 0, 4) // Max 4 tokens in path
		},
	}
	bigIntPool = sync.Pool{
		New: func() interface{} {
			return new(big.Int)
		},
	}
)

type QuoteHandler struct {
	aggregatorSvc *aggregator.Service
}

func NewQuoteHandler(aggregatorSvc *aggregator.Service) *QuoteHandler {
	return &QuoteHandler{aggregatorSvc: aggregatorSvc}
}

func (h *QuoteHandler) SetRoutes(pub *gin.RouterGroup, private *gin.RouterGroup, admin *gin.RouterGroup) {
	pub.GET("", h.getQuote)
}

func (h *QuoteHandler) Root() string {
	return "/quote"
}

// QuoteRequest represents the parameters for requesting a swap quote
type QuoteRequest struct {
	// Input token mint address (Solana base58 public key)
	// Example: "So11111111111111111111111111111111111111112" (SOL)
	InputMint string `form:"inputMint" binding:"required" example:"So11111111111111111111111111111111111111112"`

	// Output token mint address (Solana base58 public key)
	// Example: "uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG" (USDC)
	OutputMint string `form:"outputMint" binding:"required" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Amount in smallest token units (lamports for SOL, base units for SPL tokens)
	// For SOL with 9 decimals: "1000000000" = 1 SOL
	// For USDC with 6 decimals: "1000000" = 1 USDC
	Amount string `form:"amount" binding:"required" example:"1000000000"`

	// Swap mode determines how the amount is interpreted
	// - "ExactIn": Amount is the exact input, output is estimated
	// - "ExactOut": Amount is the exact output desired, input is estimated
	SwapMode string `form:"swapMode" binding:"required" enums:"ExactIn,ExactOut" example:"ExactIn"`

	// Slippage tolerance in basis points (1 bps = 0.01%)
	// Default: 50 bps (0.5%)
	// Common values: 10 (0.1%), 50 (0.5%), 100 (1%), 300 (3%)
	SlippageBps uint16 `form:"slippageBps" example:"50"` // optional, default 50bps
}

// RouteInfo describes a single hop in the swap route
type RouteInfo struct {
	// Pool address used for this swap hop
	PoolAddress string `json:"poolAddress" example:"HJPjoWUrhoZzkNfRpHuieeFk9WcZWjwy6PBjZ81ngndJ"`

	// Type of liquidity pool (e.g., "Raydium", "Orca", "Meteora", "PumpFun")
	PoolType string `json:"poolType" example:"Raydium"`

	// Percentage of the swap amount routed through this pool (100 for single route)
	// In split routes, multiple pools may be used with different percentages
	Percent uint8 `json:"percent" example:"100"`

	// Input token mint for this specific hop
	InputMint string `json:"inputMint" example:"So11111111111111111111111111111111111111112"`

	// Output token mint for this specific hop
	OutputMint string `json:"outputMint" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`
}

// QuoteResponse contains the calculated swap quote with routing information
type QuoteResponse struct {
	// Input token mint address
	InputMint string `json:"inputMint" example:"So11111111111111111111111111111111111111112"`

	// Output token mint address
	OutputMint string `json:"outputMint" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Actual input amount in smallest token units
	// For ExactIn mode: same as requested amount
	// For ExactOut mode: calculated amount needed to get exact output
	AmountIn string `json:"amountIn" example:"1000000000"`

	// Actual output amount in smallest token units
	// For ExactIn mode: calculated output amount
	// For ExactOut mode: same as requested amount
	AmountOut string `json:"amountOut" example:"145320000"`

	// Price impact in basis points (1 bps = 0.01%)
	// Indicates how much the swap affects the pool price
	// Higher values mean larger price movement
	PriceImpactBps uint16 `json:"priceImpactBps" example:"25"`

	// Human-readable price impact percentage
	PriceImpactPercent string `json:"priceImpactPercent" example:"0.25%"`

	// Price impact severity classification
	// - "none": < 0.1% (< 10 bps)
	// - "low": 0.1% - 1% (10-100 bps)
	// - "moderate": 1% - 3% (100-300 bps)
	// - "high": 3% - 5% (300-500 bps)
	// - "extreme": > 5% (> 500 bps)
	PriceImpactSeverity string `json:"priceImpactSeverity" enums:"none,low,moderate,high,extreme" example:"low"`

	// User-friendly warning message about price impact
	// Empty if impact is negligible
	PriceImpactWarning string `json:"priceImpactWarning" example:"Price impact is low"`

	// Total fee in basis points across all hops
	// Sum of all pool fees in the route
	FeeBps uint16 `json:"feeBps" example:"25"`

	// Detailed information about each hop in the route
	Routes []RouteInfo `json:"routes"`

	// Complete token path from input to output
	// For direct swap: [inputMint, outputMint]
	// For multi-hop: [inputMint, intermediateMint1, ..., outputMint]
	// Example: [SOL, USDC] or [TokenA, SOL, USDC, TokenB]
	RoutePath []string `json:"routePath" example:"So11111111111111111111111111111111111111112,uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Number of swap hops in the route
	// 1 = direct swap, 2+ = multi-hop through intermediate tokens
	HopCount int `json:"hopCount" example:"1"`

	// Minimum output (ExactIn) or maximum input (ExactOut) after applying slippage
	// For ExactIn: minimum tokens you'll receive (amountOut * (1 - slippage))
	// For ExactOut: maximum tokens you'll spend (amountIn * (1 + slippage))
	OtherAmountThreshold string `json:"otherAmountThreshold" example:"144593400"`
}

// parsedQuoteRequest holds parsed quote request data
type parsedQuoteRequest struct {
	req         *QuoteRequest
	inputMint   solana.PublicKey
	outputMint  solana.PublicKey
	amount      *big.Int
	amountU64   uint64
	canUseFast  bool // true if amount fits in uint64
	exactIn     bool
	slippageBps uint16
}

func (h *QuoteHandler) parseQuoteRequest(c *gin.Context) (*parsedQuoteRequest, bool) {
	var req QuoteRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httputil.HandleBadRequest(c, "invalid query parameters: "+err.Error())
		return nil, false
	}

	inputMint, err := solana.PublicKeyFromBase58(req.InputMint)
	if err != nil {
		httputil.HandleBadRequest(c, "invalid inputMint address")
		return nil, false
	}

	outputMint, err := solana.PublicKeyFromBase58(req.OutputMint)
	if err != nil {
		httputil.HandleBadRequest(c, "invalid outputMint address")
		return nil, false
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		httputil.HandleBadRequest(c, "invalid amount: must be a positive integer")
		return nil, false
	}

	var exactIn bool
	switch req.SwapMode {
	case "ExactIn":
		exactIn = true
	case "ExactOut":
		exactIn = false
	default:
		httputil.HandleBadRequest(c, "invalid swapMode: must be ExactIn or ExactOut")
		return nil, false
	}

	slippageBps := req.SlippageBps
	if slippageBps == 0 {
		slippageBps = 50
	}

	// Check if amount fits in uint64 for fast path
	canUseFast := amount.IsUint64()
	var amountU64 uint64
	if canUseFast {
		amountU64 = amount.Uint64()
	}

	return &parsedQuoteRequest{
		req:         &req,
		inputMint:   inputMint,
		outputMint:  outputMint,
		amount:      amount,
		amountU64:   amountU64,
		canUseFast:  canUseFast,
		exactIn:     exactIn,
		slippageBps: slippageBps,
	}, true
}

func (h *QuoteHandler) buildQuoteResponse(req *QuoteRequest, multiQuote *domain.MultiHopQuoteResult, slippageBps uint16, exactIn bool) QuoteResponse {
	// Use pooled big.Int for threshold calculation
	temp := bigIntPool.Get().(*big.Int)
	defer bigIntPool.Put(temp)

	otherAmountThreshold := new(big.Int)
	if exactIn {
		temp.SetInt64(int64(10000 - slippageBps))
		otherAmountThreshold.Mul(multiQuote.AmountOut, temp)
		otherAmountThreshold.Div(otherAmountThreshold, temp.SetInt64(10000))
	} else {
		temp.SetInt64(int64(10000 + slippageBps))
		otherAmountThreshold.Mul(multiQuote.AmountIn, temp)
		otherAmountThreshold.Div(otherAmountThreshold, temp.SetInt64(10000))
	}

	priceImpactPercent := float64(multiQuote.PriceImpactBps) / 100.0
	priceImpactPercentStr := fmt.Sprintf("%.2f%%", priceImpactPercent)

	severity := router.GetPriceImpactSeverity(multiQuote.PriceImpactBps)
	warning := router.GetPriceImpactWarning(multiQuote.PriceImpactBps)

	metrics.PriceImpact.WithLabelValues(string(severity)).Observe(float64(multiQuote.PriceImpactBps))

	// Use pooled slice for routes
	routes := routeInfoPool.Get().([]RouteInfo)[:0]
	for _, hop := range multiQuote.Hops {
		// Defensive check for nil pool
		if hop.Pool == nil {
			continue
		}

		var hopInputMint, hopOutputMint string
		if hop.AToB {
			hopInputMint = hop.Pool.TokenMintA.String()
			hopOutputMint = hop.Pool.TokenMintB.String()
		} else {
			hopInputMint = hop.Pool.TokenMintB.String()
			hopOutputMint = hop.Pool.TokenMintA.String()
		}
		routes = append(routes, RouteInfo{
			PoolAddress: hop.Pool.Address.String(),
			PoolType:    hop.Pool.Type.String(),
			Percent:     100,
			InputMint:   hopInputMint,
			OutputMint:  hopOutputMint,
		})
	}

	// Use pooled slice for route path
	routePath := routePathPool.Get().([]string)[:0]
	for _, mint := range multiQuote.Route {
		routePath = append(routePath, mint.String())
	}

	var totalFeeBps uint16
	for _, hop := range multiQuote.Hops {
		if hop.Pool != nil {
			totalFeeBps += hop.Pool.FeeRate
		}
	}

	// Copy slices for response (pooled slices will be reused)
	routesCopy := make([]RouteInfo, len(routes))
	copy(routesCopy, routes)
	routePathCopy := make([]string, len(routePath))
	copy(routePathCopy, routePath)

	// Return pooled slices
	routeInfoPool.Put(routes[:0])
	routePathPool.Put(routePath[:0])

	return QuoteResponse{
		InputMint:            req.InputMint,
		OutputMint:           req.OutputMint,
		AmountIn:             multiQuote.AmountIn.String(),
		AmountOut:            multiQuote.AmountOut.String(),
		PriceImpactBps:       multiQuote.PriceImpactBps,
		PriceImpactPercent:   priceImpactPercentStr,
		PriceImpactSeverity:  string(severity),
		PriceImpactWarning:   warning,
		FeeBps:               totalFeeBps,
		OtherAmountThreshold: otherAmountThreshold.String(),
		Routes:               routesCopy,
		RoutePath:            routePathCopy,
		HopCount:             len(multiQuote.Hops),
	}
}

// @Summary Get swap quote
// @Description Calculate the best swap quote for a token pair. The aggregator automatically finds the optimal route:
// @Description - Direct swap if a pool exists between the tokens
// @Description - Multi-hop routing through SOL or USDC if no direct pool exists
// @Description - Supports multiple DEXs: Raydium, Orca, Meteora, PumpFun, and more
// @Description
// @Description The quote includes:
// @Description - Exact amounts for input/output based on swap mode
// @Description - Price impact analysis with severity warnings
// @Description - Complete routing path with pool information
// @Description - Slippage-adjusted thresholds for transaction building
// @Description
// @Description **Amount Format:**
// @Description - Use smallest token units (lamports for SOL, base units for SPL tokens)
// @Description - SOL (9 decimals): 1 SOL = 1000000000
// @Description - USDC (6 decimals): 1 USDC = 1000000
// @Description
// @Description **Swap Modes:**
// @Description - ExactIn: You specify exact input amount, output is estimated
// @Description - ExactOut: You specify exact output desired, input is estimated
// @Tags quote
// @Produce json
// @Param inputMint query string true "Input token mint address (Solana base58 public key)" example("So11111111111111111111111111111111111111112")
// @Param outputMint query string true "Output token mint address (Solana base58 public key)" example("uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG")
// @Param amount query string true "Amount in smallest token units (e.g., lamports for SOL)" example("1000000000")
// @Param swapMode query string true "Swap mode: ExactIn or ExactOut" Enums(ExactIn, ExactOut) example("ExactIn")
// @Param slippageBps query int false "Slippage tolerance in basis points (1 bps = 0.01%). Default: 50 (0.5%)" default(50) example(50)
// @Success 200 {object} QuoteResponse "Successful quote with routing information"
// @Failure 400 {object} map[string]string "Invalid request parameters (bad mint address, invalid amount, etc.)"
// @Failure 404 {object} map[string]string "No route found between the token pair"
// @Router /api/v1/quote [get]
func (h *QuoteHandler) getQuote(c *gin.Context) {
	parsed, ok := h.parseQuoteRequest(c)
	if !ok {
		return
	}

	start := time.Now()
	swapMode := parsed.req.SwapMode
	defer func() {
		metrics.QuoteDuration.WithLabelValues(swapMode).Observe(time.Since(start).Seconds())
	}()

	var multiQuote *domain.MultiHopQuoteResult
	var err error

	// Use fast path (uint64) when amount fits, otherwise fallback to big.Int
	if parsed.canUseFast {
		multiQuote, err = h.aggregatorSvc.GetMultiHopQuoteFast(parsed.inputMint, parsed.outputMint, parsed.amountU64, parsed.exactIn)
	} else {
		multiQuote, err = h.aggregatorSvc.GetMultiHopQuote(parsed.inputMint, parsed.outputMint, parsed.amount, parsed.exactIn)
	}

	if err != nil {
		metrics.QuoteRequests.WithLabelValues(swapMode, "error").Inc()
		httputil.HandleNotFound(c, "no route found: "+err.Error())
		return
	}

	metrics.QuoteRequests.WithLabelValues(swapMode, "success").Inc()
	httputil.HandleSuccess(c, h.buildQuoteResponse(parsed.req, multiQuote, parsed.slippageBps, parsed.exactIn))
}
