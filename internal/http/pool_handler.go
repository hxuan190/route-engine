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

type PoolStatsResponse struct {
	PoolCount int `json:"pool_count" example:"1247"`
	UpdateCount uint64 `json:"update_count" example:"45892"`
}

func (h *PoolHandler) getStats(c *gin.Context) {
	poolCount, updateCount := h.aggregatorSvc.GetStats()
	httputil.HandleSuccess(c, PoolStatsResponse{
		PoolCount:   poolCount,
		UpdateCount: updateCount,
	})
}

type PoolInfo struct {
	Address string `json:"address" example:"HJPjoWUrhoZzkNfRpHuieeFk9WcZWjwy6PBjZ81ngndJ"`
	Type string `json:"type" example:"Raydium"`

	TokenMintA string `json:"token_mint_a" example:"So11111111111111111111111111111111111111112"`

	TokenMintB string `json:"token_mint_b" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	Active bool `json:"active" example:"true"`
}

type PoolListResponse struct {
	Pools []PoolInfo `json:"pools"`

	Total int `json:"total" example:"1247"`

	Page int `json:"page" example:"1"`

	Limit int `json:"limit" example:"100"`

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

type PoolDetailResponse struct {
	Address string `json:"address" example:"HJPjoWUrhoZzkNfRpHuieeFk9WcZWjwy6PBjZ81ngndJ"`

	Type string `json:"type" example:"Raydium"`

	ProgramID string `json:"program_id" example:"675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8"`

	TokenMintA string `json:"token_mint_a" example:"So11111111111111111111111111111111111111112"`

	TokenMintB string `json:"token_mint_b" example:"uSd2czE61Evaf76RNbq4KPpXnkiL3irdzgLFUMe3NoG"`

	TokenVaultA string `json:"token_vault_a" example:"5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"`

	TokenVaultB string `json:"token_vault_b" example:"36c6YqAwyGKQG66XEp2dJc5JqjaBNv7sVghEtJv4c7u6"`

	ReserveA string `json:"reserve_a" example:"1234567890123"`

	ReserveB string `json:"reserve_b" example:"9876543210987"`

	FeeRate uint16 `json:"fee_rate_bps" example:"25"`

	Active bool `json:"active" example:"true"`

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
