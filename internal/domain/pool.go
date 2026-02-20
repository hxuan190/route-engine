package domain

import (
	"math/big"

	"github.com/gagliardetto/solana-go"
	vortex_go "github.com/thehyperflames/valiant_go/generated/valiant"
)

type PoolRegistry map[solana.PublicKey]*Pool
type TokenRegistry map[solana.PublicKey]struct{}

type VaultInfo struct {
	PoolAddress solana.PublicKey
	IsVaultA    bool
}

type TickArrayInfo struct {
	PoolAddress    solana.PublicKey
	TickArrayIndex int
}

type PoolType uint8

const (
	PoolTypeVortex PoolType = iota
	PoolTypeFlux
)

type PoolFlags uint64

const (
	FlagActive  PoolFlags = 1 << 0
	FlagReady   PoolFlags = 1 << 1
	FlagVortex  PoolFlags = 1 << 2
	FlagFlux    PoolFlags = 1 << 3
	FlagHighLiq PoolFlags = 1 << 4
	FlagLowFee  PoolFlags = 1 << 5
)

const (
	FlagReadyMask       = FlagActive | FlagReady
	FlagReadyVortexMask = FlagActive | FlagReady | FlagVortex
	FlagReadyFluxMask   = FlagActive | FlagReady | FlagFlux
)

func (p PoolType) String() string {
	switch p {
	case PoolTypeVortex:
		return "Vortex"
	case PoolTypeFlux:
		return "Flux"
	default:
		return "UNKNOWN"
	}
}

type Pool struct {
	Address         solana.PublicKey `json:"address"`
	Type            PoolType         `json:"type"`
	ProgramID       solana.PublicKey `json:"programId"`
	TokenMintA      solana.PublicKey `json:"tokenMintA"`
	TokenMintB      solana.PublicKey `json:"tokenMintB"`
	TokenVaultA     solana.PublicKey `json:"tokenVaultA"`
	TokenVaultB     solana.PublicKey `json:"tokenVaultB"`
	TokenProgramA   solana.PublicKey `json:"tokenProgramA"`
	TokenProgramB   solana.PublicKey `json:"tokenProgramB"`
	ReserveA        *big.Int         `json:"reserveA"`
	ReserveB        *big.Int         `json:"reserveB"`
	FeeRate         uint16           `json:"feeRate"`
	Active          bool             `json:"active"`
	Ready           bool             `json:"ready"`
	LastUpdatedSlot uint64           `json:"lastUpdatedSlot"`
	TypeSpecific    interface{}      `json:"-"`
	Flags           PoolFlags        `json:"-"`

	// uint64 shadow fields for zero-allocation hot path (router/quoter)
	// These are kept in sync with ReserveA/ReserveB via UpdateReserves()
	ReserveAU64 uint64 `json:"-"`
	ReserveBU64 uint64 `json:"-"`
}

func (p *Pool) IsReady() bool {
	return p.Flags&FlagReadyMask == FlagReadyMask
}

func (p *Pool) UpdateFlags() {
	p.Flags = 0
	if p.Active {
		p.Flags |= FlagActive
	}
	if p.Ready {
		p.Flags |= FlagReady
	}
	switch p.Type {
	case PoolTypeVortex:
		p.Flags |= FlagVortex
	case PoolTypeFlux:
		p.Flags |= FlagFlux
	}
	if p.FeeRate < 30 {
		p.Flags |= FlagLowFee
	}
}

func (p *Pool) SetActive(active bool) {
	p.Active = active
	if active {
		p.Flags |= FlagActive
	} else {
		p.Flags &^= FlagActive
	}
}

func (p *Pool) SetReady(ready bool) {
	p.Ready = ready
	if ready {
		p.Flags |= FlagReady
	} else {
		p.Flags &^= FlagReady
	}
}

func (p *Pool) HasFlags(mask PoolFlags) bool {
	return p.Flags&mask == mask
}

func (p *Pool) UpdateReserveA(reserve *big.Int) {
	p.ReserveA = reserve
	if reserve != nil && reserve.IsUint64() {
		p.ReserveAU64 = reserve.Uint64()
	} else if reserve != nil {
		// Clamp to max uint64 for very large reserves
		p.ReserveAU64 = ^uint64(0)
	} else {
		p.ReserveAU64 = 0
	}
}

func (p *Pool) UpdateReserveB(reserve *big.Int) {
	p.ReserveB = reserve
	if reserve != nil && reserve.IsUint64() {
		p.ReserveBU64 = reserve.Uint64()
	} else if reserve != nil {
		p.ReserveBU64 = ^uint64(0)
	} else {
		p.ReserveBU64 = 0
	}
}

func (p *Pool) UpdateReserves(reserveA, reserveB *big.Int) {
	p.UpdateReserveA(reserveA)
	p.UpdateReserveB(reserveB)
}

// SyncU64Reserves syncs uint64 shadow fields from existing big.Int reserves
// Call this after loading a pool from persistence or when reserves were set directly
func (p *Pool) SyncU64Reserves() {
	if p.ReserveA != nil && p.ReserveA.IsUint64() {
		p.ReserveAU64 = p.ReserveA.Uint64()
	} else if p.ReserveA != nil {
		p.ReserveAU64 = ^uint64(0)
	}
	if p.ReserveB != nil && p.ReserveB.IsUint64() {
		p.ReserveBU64 = p.ReserveB.Uint64()
	} else if p.ReserveB != nil {
		p.ReserveBU64 = ^uint64(0)
	}
}

type VortexPoolData struct {
	TickSpacing      uint16                        `json:"tickSpacing"`
	CurrentTickIndex int32                         `json:"currentTickIndex"`
	SqrtPriceX64     *big.Int                      `json:"sqrtPriceX64"`
	Liquidity        *big.Int                      `json:"liquidity"`
	TickArrays       []solana.PublicKey            `json:"tickArrays,omitempty"`
	ParsedVortex     *vortex_go.VortexAccount      `json:"-"`
	ParsedTickArrays []*vortex_go.TickArrayAccount `json:"-"`
}
