package persistence

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	boltdb "github.com/andrew-solarstorm/bolt-db"
	"github.com/bytedance/sonic"
	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/rs/zerolog/log"
	vortex_go "github.com/thehyperflames/valiant_go/generated/valiant"

	"github.com/hxuan190/route-engine/internal/domain"
)

const (
	PoolsBucket  = "pools"
	VaultsBucket = "vaults"

	DefaultDBPath = "./data/route-engine.db"
)

type StoredPool struct {
	Address         string `json:"address"`
	Type            uint8  `json:"type"`
	ProgramID       string `json:"programId"`
	TokenMintA      string `json:"tokenMintA"`
	TokenMintB      string `json:"tokenMintB"`
	TokenVaultA     string `json:"tokenVaultA"`
	TokenVaultB     string `json:"tokenVaultB"`
	TokenProgramA   string `json:"tokenProgramA,omitempty"`
	TokenProgramB   string `json:"tokenProgramB,omitempty"`
	ReserveA        string `json:"reserveA"`
	ReserveB        string `json:"reserveB"`
	FeeRate         uint16 `json:"feeRate"`
	Active          bool   `json:"active"`
	LastUpdatedSlot uint64 `json:"lastUpdatedSlot"`

	CLMMData *StoredCLMMData `json:"clmmData,omitempty"`
}

type StoredCLMMData struct {
	TickSpacing      uint16   `json:"tickSpacing"`
	CurrentTickIndex int32    `json:"currentTickIndex"`
	SqrtPrice        string   `json:"sqrtPrice"`  // Uint128 as string
	Liquidity        string   `json:"liquidity"`  // Uint128 as string
	TickArrays       []string `json:"tickArrays"` // PublicKey array as strings
}

type StoredVaultInfo struct {
	PoolAddress string `json:"poolAddress"`
	IsVaultA    bool   `json:"isVaultA"`
}
type Storage struct {
	db     *boltdb.BoltDatabase
	dbPath string
}

func NewStorage(dbPath string) (*Storage, error) {
	os.MkdirAll(filepath.Dir(DefaultDBPath), 0755)
	if dbPath == "" {
		dbPath = DefaultDBPath
	}

	db := boltdb.NewBoltDatabase(dbPath)
	if db == nil {
		return nil, fmt.Errorf("failed to open database at %s", dbPath)
	}

	log.Info().Str("path", dbPath).Msg("[aggregatorStorage] opened database")

	return &Storage{
		db:     db,
		dbPath: dbPath,
	}, nil
}

func (s *Storage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Storage) SavePool(pool *domain.Pool) error {
	stored := poolToStored(pool)
	data, err := sonic.Marshal(stored)
	if err != nil {
		return fmt.Errorf("failed to marshal pool: %w", err)
	}

	return s.db.Set(PoolsBucket, []byte(pool.Address.String()), data)
}

func (s *Storage) SavePoolBatch(pools []*domain.Pool) error {
	if len(pools) == 0 {
		return nil
	}

	batch := s.db.NewBatch()
	for _, pool := range pools {
		stored := poolToStored(pool)
		data, err := sonic.Marshal(stored)
		if err != nil {
			return fmt.Errorf("failed to marshal pool %s: %w", pool.Address.String(), err)
		}

		value := data
		op := &boltdb.WriteOperation{
			Bucket: []byte(PoolsBucket),
			Key:    []byte(pool.Address.String()),
			Value:  &value,
			Op:     boltdb.OpSet,
		}
		if err := batch.Add(op); err != nil {
			return fmt.Errorf("failed to add pool %s to batch: %w", pool.Address.String(), err)
		}
	}

	if err := batch.Execute(); err != nil {
		log.Error().Err(err).Int("count", len(pools)).Msg("[aggregatorStorage] FAILED to execute batch")
		return err
	}

	log.Info().Int("count", len(pools)).Msg("[aggregatorStorage] saved pool batch")
	return nil
}

func (s *Storage) LoadAllPools() ([]*domain.Pool, error) {
	data, err := s.db.List(PoolsBucket)
	if err != nil {
		return nil, fmt.Errorf("failed to list pools: %w", err)
	}

	pools := make([]*domain.Pool, 0, len(data))
	unmarshalFailed := 0
	conversionFailed := 0

	for address, value := range data {
		var stored StoredPool
		if err := sonic.Unmarshal(value, &stored); err != nil {
			log.Error().Str("address", address).Err(err).Msg("[aggregatorStorage] failed to unmarshal pool, skipping")
			unmarshalFailed++
			continue
		}

		pool, err := storedToPool(&stored)
		if err != nil {
			log.Error().Str("address", address).Err(err).Msg("[aggregatorStorage] failed to convert stored pool, skipping")
			conversionFailed++
			continue
		}

		pools = append(pools, pool)
	}

	if unmarshalFailed > 0 || conversionFailed > 0 {
		log.Error().
			Int("total_in_db", len(data)).
			Int("loaded", len(pools)).
			Int("unmarshal_failed", unmarshalFailed).
			Int("conversion_failed", conversionFailed).
			Msg("[aggregatorStorage] pool loading completed with errors")
	} else {
		log.Info().
			Int("total_in_db", len(data)).
			Int("loaded", len(pools)).
			Msg("[aggregatorStorage] pool loading completed successfully")
	}

	return pools, nil
}

func (s *Storage) SaveVaultMapping(vaultAddress solana.PublicKey, info domain.VaultInfo) error {
	stored := StoredVaultInfo{
		PoolAddress: info.PoolAddress.String(),
		IsVaultA:    info.IsVaultA,
	}
	data, err := sonic.Marshal(stored)
	if err != nil {
		return fmt.Errorf("failed to marshal vault info: %w", err)
	}

	return s.db.Set(VaultsBucket, []byte(vaultAddress.String()), data)
}

func (s *Storage) LoadAllVaultMappings() (map[solana.PublicKey]domain.VaultInfo, error) {
	data, err := s.db.List(VaultsBucket)
	if err != nil {
		return nil, fmt.Errorf("failed to list vaults: %w", err)
	}

	vaults := make(map[solana.PublicKey]domain.VaultInfo, len(data))
	for address, value := range data {
		var stored StoredVaultInfo
		if err := sonic.Unmarshal(value, &stored); err != nil {
			log.Warn().Str("address", address).Err(err).Msg("[aggregatorStorage] failed to unmarshal vault, skipping")
			continue
		}

		vaultPubkey, err := solana.PublicKeyFromBase58(address)
		if err != nil {
			log.Warn().Str("address", address).Err(err).Msg("[aggregatorStorage] invalid vault address, skipping")
			continue
		}

		poolPubkey, err := solana.PublicKeyFromBase58(stored.PoolAddress)
		if err != nil {
			log.Warn().Str("poolAddress", stored.PoolAddress).Err(err).Msg("[aggregatorStorage] invalid pool address in vault, skipping")
			continue
		}

		vaults[vaultPubkey] = domain.VaultInfo{
			PoolAddress: poolPubkey,
			IsVaultA:    stored.IsVaultA,
		}
	}

	return vaults, nil
}

func (s *Storage) GetPoolCount() (int, error) {
	data, err := s.db.List(PoolsBucket)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func poolToStored(pool *domain.Pool) *StoredPool {
	reserveA := "0"
	reserveB := "0"
	if pool.ReserveA != nil {
		reserveA = pool.ReserveA.String()
	}
	if pool.ReserveB != nil {
		reserveB = pool.ReserveB.String()
	}

	tokenProgramA := ""
	tokenProgramB := ""
	if !pool.TokenProgramA.IsZero() {
		tokenProgramA = pool.TokenProgramA.String()
	}
	if !pool.TokenProgramB.IsZero() {
		tokenProgramB = pool.TokenProgramB.String()
	}

	stored := &StoredPool{
		Address:         pool.Address.String(),
		Type:            uint8(pool.Type),
		ProgramID:       pool.ProgramID.String(),
		TokenMintA:      pool.TokenMintA.String(),
		TokenMintB:      pool.TokenMintB.String(),
		TokenVaultA:     pool.TokenVaultA.String(),
		TokenVaultB:     pool.TokenVaultB.String(),
		TokenProgramA:   tokenProgramA,
		TokenProgramB:   tokenProgramB,
		ReserveA:        reserveA,
		ReserveB:        reserveB,
		FeeRate:         pool.FeeRate,
		Active:          pool.Active,
		LastUpdatedSlot: pool.LastUpdatedSlot,
	}

	if pool.Type == domain.PoolTypeVortex {
		if clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData); ok && clmmData != nil && clmmData.ParsedVortex != nil {
			vortex := clmmData.ParsedVortex
			tickArrayStrs := make([]string, len(clmmData.TickArrays))
			for i, ta := range clmmData.TickArrays {
				tickArrayStrs[i] = ta.String()
			}

			stored.CLMMData = &StoredCLMMData{
				TickSpacing:      vortex.TickSpacing,
				CurrentTickIndex: vortex.TickCurrentIndex,
				SqrtPrice:        vortex.SqrtPrice.BigInt().String(),
				Liquidity:        vortex.Liquidity.BigInt().String(),
				TickArrays:       tickArrayStrs,
			}
		}
	}

	return stored
}

func storedToPool(stored *StoredPool) (*domain.Pool, error) {
	address, err := solana.PublicKeyFromBase58(stored.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	programID, err := solana.PublicKeyFromBase58(stored.ProgramID)
	if err != nil {
		return nil, fmt.Errorf("invalid programId: %w", err)
	}

	tokenMintA, err := solana.PublicKeyFromBase58(stored.TokenMintA)
	if err != nil {
		return nil, fmt.Errorf("invalid tokenMintA: %w", err)
	}

	tokenMintB, err := solana.PublicKeyFromBase58(stored.TokenMintB)
	if err != nil {
		return nil, fmt.Errorf("invalid tokenMintB: %w", err)
	}

	tokenVaultA, err := solana.PublicKeyFromBase58(stored.TokenVaultA)
	if err != nil {
		return nil, fmt.Errorf("invalid tokenVaultA: %w", err)
	}

	tokenVaultB, err := solana.PublicKeyFromBase58(stored.TokenVaultB)
	if err != nil {
		return nil, fmt.Errorf("invalid tokenVaultB: %w", err)
	}

	var tokenProgramA, tokenProgramB solana.PublicKey
	if stored.TokenProgramA != "" {
		tokenProgramA, _ = solana.PublicKeyFromBase58(stored.TokenProgramA)
	}
	if stored.TokenProgramB != "" {
		tokenProgramB, _ = solana.PublicKeyFromBase58(stored.TokenProgramB)
	}

	reserveA := new(big.Int)
	reserveA.SetString(stored.ReserveA, 10)

	reserveB := new(big.Int)
	reserveB.SetString(stored.ReserveB, 10)

	pool := &domain.Pool{
		Address:         address,
		Type:            domain.PoolType(stored.Type),
		ProgramID:       programID,
		TokenMintA:      tokenMintA,
		TokenMintB:      tokenMintB,
		TokenVaultA:     tokenVaultA,
		TokenVaultB:     tokenVaultB,
		TokenProgramA:   tokenProgramA,
		TokenProgramB:   tokenProgramB,
		ReserveA:        reserveA,
		ReserveB:        reserveB,
		FeeRate:         stored.FeeRate,
		Active:          stored.Active,
		LastUpdatedSlot: stored.LastUpdatedSlot,
	}
	pool.UpdateFlags()
	pool.SyncU64Reserves() // Sync uint64 shadow fields from loaded big.Int reserves

	if pool.Type == domain.PoolTypeVortex && stored.CLMMData != nil {
		clmmData := stored.CLMMData

		sqrtPriceBig := new(big.Int)
		sqrtPriceBig.SetString(clmmData.SqrtPrice, 10)

		liquidityBig := new(big.Int)
		liquidityBig.SetString(clmmData.Liquidity, 10)

		tickArrays := make([]solana.PublicKey, 0, len(clmmData.TickArrays))
		for _, taStr := range clmmData.TickArrays {
			ta, err := solana.PublicKeyFromBase58(taStr)
			if err != nil {
				continue
			}
			tickArrays = append(tickArrays, ta)
		}

		vortex := &vortex_go.VortexAccount{
			TokenMintA:       tokenMintA,
			TokenMintB:       tokenMintB,
			TokenVaultA:      tokenVaultA,
			TokenVaultB:      tokenVaultB,
			FeeRate:          stored.FeeRate,
			TickSpacing:      clmmData.TickSpacing,
			TickCurrentIndex: clmmData.CurrentTickIndex,
			SqrtPrice:        bigIntToUint128(sqrtPriceBig),
			Liquidity:        bigIntToUint128(liquidityBig),
		}

		pool.TypeSpecific = &domain.VortexPoolData{
			TickSpacing:      clmmData.TickSpacing,
			CurrentTickIndex: clmmData.CurrentTickIndex,
			SqrtPriceX64:     sqrtPriceBig,
			Liquidity:        liquidityBig,
			TickArrays:       tickArrays,
			ParsedVortex:     vortex,
			ParsedTickArrays: nil,
		}
	} else {
		pool.TypeSpecific = nil
	}

	return pool, nil
}

func bigIntToUint128(value *big.Int) bin.Uint128 {
	if value == nil {
		return bin.Uint128{}
	}
	bytes := value.Bytes()

	if len(bytes) > 16 {
		bytes = bytes[len(bytes)-16:]
	}

	var result bin.Uint128

	if len(bytes) <= 8 {
		result.Lo = 0
		for i := 0; i < len(bytes); i++ {
			result.Lo |= uint64(bytes[len(bytes)-1-i]) << (8 * i)
		}
		result.Hi = 0
	} else {
		for i := 0; i < 8; i++ {
			result.Lo |= uint64(bytes[len(bytes)-1-i]) << (8 * i)
		}
		for i := 8; i < len(bytes) && i < 16; i++ {
			result.Hi |= uint64(bytes[len(bytes)-1-i]) << (8 * (i - 8))
		}
	}

	return result
}
