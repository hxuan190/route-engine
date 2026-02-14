package builder

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gagliardetto/solana-go"
	addresslookuptable "github.com/gagliardetto/solana-go/programs/address-lookup-table"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rs/zerolog/log"
)

// LUTManager fetches and caches Address Lookup Table states for V0 transactions.
// Uses atomic.Value for lock-free reads on the hot path.
type LUTManager struct {
	rpcClient    *rpc.Client
	lutAddresses []solana.PublicKey
	tables       atomic.Value // map[solana.PublicKey]solana.PublicKeySlice
	interval     time.Duration
}

// NewLUTManager creates a new LUT manager. If lutAddresses is empty, GetAddressTables
// returns an empty map and transactions fall back to legacy format.
func NewLUTManager(rpcClient *rpc.Client, lutAddresses []solana.PublicKey, refreshInterval time.Duration) *LUTManager {
	m := &LUTManager{
		rpcClient:    rpcClient,
		lutAddresses: lutAddresses,
		interval:     refreshInterval,
	}
	m.tables.Store(make(map[solana.PublicKey]solana.PublicKeySlice))
	return m
}

// Start fetches LUT states immediately, then refreshes in the background.
func (m *LUTManager) Start(ctx context.Context) {
	if len(m.lutAddresses) == 0 {
		log.Info().Msg("LUTManager: no LUT addresses configured, V0 transactions disabled")
		return
	}

	m.refresh(ctx)

	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.refresh(ctx)
			}
		}
	}()
}

// GetAddressTables returns the cached lookup tables for use with
// solana.TransactionAddressTables(). Returns empty map when no LUTs configured.
func (m *LUTManager) GetAddressTables() map[solana.PublicKey]solana.PublicKeySlice {
	return m.tables.Load().(map[solana.PublicKey]solana.PublicKeySlice)
}

func (m *LUTManager) refresh(ctx context.Context) {
	tables := make(map[solana.PublicKey]solana.PublicKeySlice, len(m.lutAddresses))

	for _, addr := range m.lutAddresses {
		state, err := addresslookuptable.GetAddressLookupTable(ctx, m.rpcClient, addr)
		if err != nil {
			log.Warn().Err(err).Str("lut", addr.String()).Msg("LUTManager: failed to fetch LUT")
			continue
		}
		if !state.IsActive() {
			log.Warn().Str("lut", addr.String()).Msg("LUTManager: LUT is deactivated, skipping")
			continue
		}
		tables[addr] = state.Addresses
		log.Info().
			Str("lut", addr.String()).
			Int("addresses", len(state.Addresses)).
			Msg("LUTManager: loaded LUT")
	}

	m.tables.Store(tables)
	log.Info().Int("tables", len(tables)).Msg("LUTManager: refresh complete")
}
