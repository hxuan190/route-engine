package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	aggregator "github.com/hxuan190/route-engine/internal"
	"github.com/hxuan190/route-engine/internal/http/httputil"
)

type PoolHandler struct {
	aggregatorSvc *aggregator.Service
}

func NewPoolHandler(aggregatorSvc *aggregator.Service) *PoolHandler {
	return &PoolHandler{aggregatorSvc: aggregatorSvc}
}

func (h *PoolHandler) SetRoutes(pub *gin.RouterGroup, private *gin.RouterGroup, admin *gin.RouterGroup) {
	pub.GET("/stats", h.getStats)
	pub.GET("/list", h.listPools)
	pub.GET("/:address", h.getPool)
}

func (h *PoolHandler) Root() string {
	return "/pools"
}

// PoolStatsResponse contains aggregated statistics about tracked liquidity pools
type PoolStatsResponse struct {
	// Total number of active liquidity pools being tracked
	// Includes pools from all supported DEXs (Raydium, Orca, Meteora, etc.)
	PoolCount int `json:"pool_count" example:"1247"`

	// Total number of pool state updates processed since service start
	// Indicates how many times pool data has been refreshed from on-chain
	UpdateCount uint64 `json:"update_count" example:"45892"`
}

func (h *PoolHandler) getStats(c *gin.Context) {
	poolCount, updateCount := h.aggregatorSvc.GetStats()
	httputil.HandleSuccess(c, PoolStatsResponse{
		PoolCount:   poolCount,
		UpdateCount: updateCount,
	})
}

// PoolInfo contains basic information about a liquidity pool
type PoolInfo struct {
	// Pool address (Solana public key)
	Address string `json:"address" example:"HJPjoWUrhoZzkNfRpHuieeFk9WcZWjwy6PBjZ81ngndJ"`

	// Pool type/DEX name (e.g., "Raydium", "Orca", "Meteora", "PumpFun")
	Type string `json:"type" example:"Raydium"`

	// First token mint address in the pair
	TokenMintA string `json:"token_mint_a" example:"So11111111111111111111111111111111111111112"`

	// Second token mint address in the pair
	TokenMintB string `json:"token_mint_b" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Whether the pool is currently active and available for routing
	Active bool `json:"active" example:"true"`
}

// PoolListResponse contains paginated list of liquidity pools
type PoolListResponse struct {
	// Array of pool information objects for the current page
	Pools []PoolInfo `json:"pools"`

	// Total number of pools across all pages
	Total int `json:"total" example:"1247"`

	// Current page number (1-indexed)
	Page int `json:"page" example:"1"`

	// Number of pools per page (max 500)
	Limit int `json:"limit" example:"100"`

	// Total number of pages available
	Pages int `json:"pages" example:"13"`
}

func (h *PoolHandler) listPools(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	graph := h.aggregatorSvc.GetGraph()
	allPools := graph.GetAllPools()
	total := len(allPools)

	pages := (total + limit - 1) / limit
	offset := (page - 1) * limit
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}

	pools := make([]PoolInfo, 0, end-offset)
	for _, pool := range allPools[offset:end] {
		pools = append(pools, PoolInfo{
			Address:    pool.Address.String(),
			Type:       pool.Type.String(),
			TokenMintA: pool.TokenMintA.String(),
			TokenMintB: pool.TokenMintB.String(),
			Active:     pool.Active,
		})
	}

	httputil.HandleSuccess(c, PoolListResponse{
		Pools: pools,
		Total: total,
		Page:  page,
		Limit: limit,
		Pages: pages,
	})
}

// PoolDetailResponse contains detailed information about a specific liquidity pool
type PoolDetailResponse struct {
	// Pool address (Solana public key)
	Address string `json:"address" example:"HJPjoWUrhoZzkNfRpHuieeFk9WcZWjwy6PBjZ81ngndJ"`

	// Pool type/DEX name
	Type string `json:"type" example:"Raydium"`

	// Program ID that owns this pool
	ProgramID string `json:"program_id" example:"675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8"`

	// First token mint address
	TokenMintA string `json:"token_mint_a" example:"So11111111111111111111111111111111111111112"`

	// Second token mint address
	TokenMintB string `json:"token_mint_b" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	// Token vault address for token A
	TokenVaultA string `json:"token_vault_a" example:"5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"`

	// Token vault address for token B
	TokenVaultB string `json:"token_vault_b" example:"36c6YqAwyGKQG66XEp2dJc5JqjaBNv7sVghEtJv4c7u6"`

	// Current reserve/liquidity of token A in smallest units
	ReserveA string `json:"reserve_a" example:"1234567890123"`

	// Current reserve/liquidity of token B in smallest units
	ReserveB string `json:"reserve_b" example:"9876543210987"`

	// Pool fee rate in basis points (1 bps = 0.01%)
	// Common values: 25 (0.25%), 30 (0.3%), 100 (1%)
	FeeRate uint16 `json:"fee_rate_bps" example:"25"`

	// Whether the pool is currently active
	Active bool `json:"active" example:"true"`

	// Solana slot number when pool data was last updated
	LastUpdatedSlot uint64 `json:"last_updated_slot" example:"245831456"`
}

func (h *PoolHandler) getPool(c *gin.Context) {
	address := c.Param("address")
	graph := h.aggregatorSvc.GetGraph()
	pool := graph.GetPoolByAddress(address)

	if pool == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool not found"})
		return
	}

	c.JSON(http.StatusOK, PoolDetailResponse{
		Address:         pool.Address.String(),
		Type:            pool.Type.String(),
		ProgramID:       pool.ProgramID.String(),
		TokenMintA:      pool.TokenMintA.String(),
		TokenMintB:      pool.TokenMintB.String(),
		TokenVaultA:     pool.TokenVaultA.String(),
		TokenVaultB:     pool.TokenVaultB.String(),
		ReserveA:        pool.ReserveA.String(),
		ReserveB:        pool.ReserveB.String(),
		FeeRate:         pool.FeeRate,
		Active:          pool.Active,
		LastUpdatedSlot: pool.LastUpdatedSlot,
	})
}
