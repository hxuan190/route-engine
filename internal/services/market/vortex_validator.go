package market

import (
	"github.com/hxuan190/route-engine/internal/domain"
)

// VortexValidator implements PoolValidator for Vortex CLMM pools
type VortexValidator struct{}

// NewVortexValidator creates a new Vortex validator
func NewVortexValidator() *VortexValidator {
	return &VortexValidator{}
}

// IsReady checks if a Vortex pool is ready for trading
func (v *VortexValidator) IsReady(pool *domain.Pool) bool {
	if !pool.Active || !pool.Ready {
		return false
	}

	vortexData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || vortexData == nil || vortexData.ParsedVortex == nil {
		return false
	}

	// Vortex pools need tick arrays loaded
	if len(vortexData.ParsedTickArrays) == 0 {
		return false
	}

	return true
}

// SupportsPoolType returns true for Vortex pools
func (v *VortexValidator) SupportsPoolType(poolType domain.PoolType) bool {
	return poolType == domain.PoolTypeVortex
}
