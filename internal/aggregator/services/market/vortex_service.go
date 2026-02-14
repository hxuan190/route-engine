package market

import (
	"bytes"
	"context"
	"math/big"
	"sync"
	"time"

	pb "github.com/andrew-solarstorm/yellowstone-grpc-client-go/proto"
	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rs/zerolog/log"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
	"github.com/thehyperflames/hyperion-api/internal/services"
	valiant "github.com/thehyperflames/valiant_go"
	vortex_go "github.com/thehyperflames/valiant_go/generated/valiant"
)

func (svc *Service) SubscribeToVortexPools() error {
	commitment := pb.CommitmentLevel_CONFIRMED

	_, err := svc.ySvc.SubscribeAccountsByOwner(
		[]string{VortexProgramID.String()},
		nil,
		nil,
		&commitment,
		svc.handlePoolUpdate,
	)
	if err != nil {
		log.Error().Err(err).Msg("[aggregatorService] failed to subscribe to Vortex pools")
		return err
	}

	log.Info().Str("program", VortexProgramID.String()).Msg("[aggregatorService] subscribed to Vortex pool updates")
	return nil
}

func (svc *Service) handlePoolUpdate(update *pb.SubscribeUpdate) error {
	account := update.GetAccount()
	if account == nil || account.Account == nil {
		return nil
	}

	data := account.Account.Data
	if len(data) < 8 {
		return nil
	}

	var ownerPubkey solana.PublicKey
	copy(ownerPubkey[:], account.Account.Owner)

	if ownerPubkey != VortexProgramID {
		return nil
	}

	discriminator := data[:8]
	if !bytes.Equal(discriminator, vortex_go.VortexAccountDiscriminator[:]) {
		return nil
	}

	pubkey := solana.PublicKeyFromBytes(account.Account.Pubkey)
	return svc.processVortexPool(pubkey, data, account.Slot)
}

func (svc *Service) processVortexPool(pubkey solana.PublicKey, data []byte, slot uint64) error {
	if len(data) < 8 {
		return nil
	}

	var vortex vortex_go.VortexAccount
	if err := bin.NewBinDecoder(data[8:]).Decode(&vortex); err != nil {
		log.Debug().Err(err).Str("pubkey", pubkey.String()).Msg("[aggregatorService] failed to decode vortex account")
		return nil
	}

	tickArrayPDAs, err := valiant.GetRelevantTickArrayPDAs(VortexProgramID, pubkey, vortex.TickCurrentIndex, vortex.TickSpacing)
	if err != nil {
		tickArrayPDAs = nil
	}

	// Check if pool already exists - update instead of replace to preserve Ready state and ParsedTickArrays
	existingPool := svc.graph.GetPoolMutable(pubkey)
	var pool *domain.Pool

	if existingPool != nil {
		// Update existing pool
		pool = existingPool
		svc.graph.Lock()
		pool.FeeRate = vortex.FeeRate
		pool.SetActive(true)
		pool.LastUpdatedSlot = slot

		if clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData); ok {
			clmmData.TickSpacing = vortex.TickSpacing
			clmmData.CurrentTickIndex = vortex.TickCurrentIndex
			clmmData.SqrtPriceX64 = vortex.SqrtPrice.BigInt()
			clmmData.Liquidity = vortex.Liquidity.BigInt()
			clmmData.ParsedVortex = &vortex
			// Only update tick arrays if they changed (don't overwrite ParsedTickArrays)
			if len(tickArrayPDAs) > 0 && !tickArraysEqual(clmmData.TickArrays, tickArrayPDAs) {
				clmmData.TickArrays = tickArrayPDAs
				clmmData.ParsedTickArrays = nil // Reset parsed arrays since PDAs changed
				pool.SetReady(false)
			}
		}
		svc.graph.Unlock()
		svc.graph.MarkDirty()
	} else {
		// Create new pool
		pool = &domain.Pool{
			Address:         pubkey,
			Type:            domain.PoolTypeVortex,
			ProgramID:       VortexProgramID,
			TokenMintA:      vortex.TokenMintA,
			TokenMintB:      vortex.TokenMintB,
			TokenVaultA:     vortex.TokenVaultA,
			TokenVaultB:     vortex.TokenVaultB,
			TokenProgramA:   svc.getCachedTokenProgramOrDefault(vortex.TokenMintA),
			TokenProgramB:   svc.getCachedTokenProgramOrDefault(vortex.TokenMintB),
			ReserveA:        big.NewInt(0),
			ReserveB:        big.NewInt(0),
			ReserveAU64:     0,
			ReserveBU64:     0,
			FeeRate:         vortex.FeeRate,
			Active:          true,
			LastUpdatedSlot: slot,
			TypeSpecific: &domain.VortexPoolData{
				TickSpacing:      vortex.TickSpacing,
				CurrentTickIndex: vortex.TickCurrentIndex,
				SqrtPriceX64:     vortex.SqrtPrice.BigInt(),
				Liquidity:        vortex.Liquidity.BigInt(),
				ParsedVortex:     &vortex,
				TickArrays:       tickArrayPDAs,
			},
		}
		pool.UpdateFlags()
		svc.graph.AddPool(pool)
	}

	svc.queuePoolForPersistence(pool)
	svc.updateCount.Add(1)

	if len(tickArrayPDAs) > 0 {
		svc.registerTickArrays(pool, tickArrayPDAs)
	}

	svc.registerVaults(pool)

	// Queue async decimals fetch (non-blocking)
	svc.queueDecimalsFetch(vortex.TokenMintA, pubkey)
	svc.queueDecimalsFetch(vortex.TokenMintB, pubkey)

	// Use cached or default decimals immediately (non-blocking)
	decimalsA := svc.getCachedDecimalsOrDefault(vortex.TokenMintA)
	decimalsB := svc.getCachedDecimalsOrDefault(vortex.TokenMintB)

	svc.priceSvc.UpdatePriceFromSqrtPrice(vortex.TokenMintA, vortex.TokenMintB, vortex.SqrtPrice.BigInt(), decimalsA, decimalsB)

	return nil
}

func (svc *Service) processTickArray(pubkey solana.PublicKey, data []byte, slot uint64) error {
	if len(data) < 8 {
		return nil
	}

	var tickArray vortex_go.TickArrayAccount
	if err := bin.NewBinDecoder(data[8:]).Decode(&tickArray); err != nil {
		return nil
	}

	info, exists := svc.tickArrayToPool.Get(pubkey)
	if !exists {
		return nil
	}

	// Use GetPoolMutable to get the latest pool data that may not be in snapshot yet
	pool := svc.graph.GetPoolMutable(info.PoolAddress)
	if pool == nil {
		return nil
	}

	clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || clmmData == nil {
		return nil
	}

	svc.graph.Lock()
	if clmmData.ParsedTickArrays == nil {
		clmmData.ParsedTickArrays = make([]*vortex_go.TickArrayAccount, len(clmmData.TickArrays))
	}
	if info.TickArrayIndex >= 0 && info.TickArrayIndex < len(clmmData.ParsedTickArrays) {
		clmmData.ParsedTickArrays[info.TickArrayIndex] = &tickArray
	}
	pool.LastUpdatedSlot = slot

	// Check if pool has all tick arrays and mark as ready
	if !pool.Ready && svc.hasAllTickArrays(clmmData) {
		pool.SetReady(true)
		log.Debug().
			Str("pool", pool.Address.String()).
			Msg("[aggregatorService] pool ready with tick arrays from Yellowstone")
	}
	svc.graph.Unlock()

	// Refresh graph snapshot after tick array update
	svc.graph.RefreshSnapshot()

	svc.tickArrayUpdateCount.Add(1)

	return nil
}

func (svc *Service) hasAllTickArrays(clmmData *domain.VortexPoolData) bool {
	if len(clmmData.ParsedTickArrays) == 0 {
		return false
	}
	for _, ta := range clmmData.ParsedTickArrays {
		if ta == nil {
			return false
		}
	}
	return true
}

func tickArraysEqual(a, b []solana.PublicKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equals(b[i]) {
			return false
		}
	}
	return true
}

func (svc *Service) SubscribeToVaults() error {
	vaultKeys := svc.vaultToPool.GetAllKeys()
	vaultAddresses := make([]string, len(vaultKeys))
	for i, vault := range vaultKeys {
		vaultAddresses[i] = vault.String()
	}

	if len(vaultAddresses) == 0 {
		log.Info().Msg("[aggregatorService] no vaults to subscribe to")
		return nil
	}

	commitment := pb.CommitmentLevel_CONFIRMED

	_, err := svc.ySvc.SubscribeAccounts(
		vaultAddresses,
		nil,
		nil,
		&commitment,
		svc.handleVaultUpdate,
	)
	if err != nil {
		log.Error().Err(err).Msg("[aggregatorService] failed to subscribe to vaults")
		return err
	}

	log.Info().Int("count", len(vaultAddresses)).Msg("[aggregatorService] subscribed to vault updates")
	return nil
}

func (svc *Service) handleTransactionUpdate(update *pb.SubscribeUpdate) error {
	txProto := update.GetTransaction()
	if txProto == nil {
		return nil
	}

	// Use services.ParseNotiTransaction to handle proto parsing
	parsed, err := services.ParseNotiTransaction(txProto)
	if err != nil {
		return nil
	}

	if parsed.Meta == nil || parsed.Meta.Err != nil {
		return nil
	}

	// Use valiant package to parse swaps
	swaps, err := valiant.ParseSwapsFromTransaction(parsed.Transaction, parsed.Meta, VortexProgramID)
	if err != nil {
		return nil
	}

	for _, s := range swaps {
		// Lookup pool info
		infoIn, okIn := svc.vaultToPool.Get(s.VaultIn)
		infoOut, okOut := svc.vaultToPool.Get(s.VaultOut)

		if !okIn || !okOut {
			continue
		}
		if !infoIn.PoolAddress.Equals(infoOut.PoolAddress) {
			continue
		}

		pool := svc.graph.GetPool(infoIn.PoolAddress)
		if pool == nil {
			continue
		}

		var inMint, outMint solana.PublicKey

		if infoIn.IsVaultA {
			inMint = pool.TokenMintA
		} else {
			inMint = pool.TokenMintB
		}

		if infoOut.IsVaultA {
			outMint = pool.TokenMintA
		} else {
			outMint = pool.TokenMintB
		}

		// Queue async decimals fetch (non-blocking)
		svc.queueDecimalsFetch(inMint, infoIn.PoolAddress)
		svc.queueDecimalsFetch(outMint, infoOut.PoolAddress)

		// Use cached or default decimals immediately (non-blocking)
		inDecimals := svc.getCachedDecimalsOrDefault(inMint)
		outDecimals := svc.getCachedDecimalsOrDefault(outMint)

		svc.priceSvc.UpdatePriceFromSwap(inMint, outMint, s.AmountIn, s.AmountOut, inDecimals, outDecimals)
		svc.candleSvc.IngestCandleData(inMint, outMint, s.AmountIn, s.AmountOut, inDecimals, outDecimals, parsed.BlockTime.Time().Unix())
	}

	return nil
}

func (svc *Service) handleVaultUpdate(update *pb.SubscribeUpdate) error {
	account := update.GetAccount()
	if account == nil || account.Account == nil {
		return nil
	}

	vaultPubkey := solana.PublicKeyFromBytes(account.Account.Pubkey)
	data := account.Account.Data

	info, exists := svc.vaultToPool.Get(vaultPubkey)
	if !exists {
		return nil
	}

	balance, err := svc.parseTokenAccountBalance(data)
	if err != nil || balance == nil {
		return nil
	}

	pool := svc.graph.GetPool(info.PoolAddress)
	if pool == nil {
		return nil
	}

	svc.graph.Lock()
	if account.Slot < pool.LastUpdatedSlot {
		svc.graph.Unlock()
		return nil
	}

	if info.IsVaultA {
		pool.UpdateReserveA(balance)
	} else {
		pool.UpdateReserveB(balance)
	}
	pool.LastUpdatedSlot = account.Slot
	svc.graph.Unlock()

	svc.queuePoolForPersistence(pool)

	svc.vaultUpdateCount.Add(1)

	return nil
}

func (svc *Service) subscribeToTickArrayAddresses(addresses []string) error {
	if len(addresses) == 0 {
		return nil
	}

	commitment := pb.CommitmentLevel_CONFIRMED

	_, err := svc.ySvc.SubscribeAccounts(
		addresses,
		nil,
		nil,
		&commitment,
		svc.handleTickArrayUpdate,
	)
	if err != nil {
		return err
	}

	log.Info().Int("count", len(addresses)).Msg("[aggregatorService] subscribed to tick array accounts")
	return nil
}

func (svc *Service) handleTickArrayUpdate(update *pb.SubscribeUpdate) error {
	account := update.GetAccount()
	if account == nil || account.Account == nil {
		return nil
	}

	pubkey := solana.PublicKeyFromBytes(account.Account.Pubkey)
	data := account.Account.Data

	return svc.processTickArray(pubkey, data, account.Slot)
}

func (svc *Service) fetchHistoricalPools() {
	startTime := time.Now()
	log.Info().Msg("[aggregatorService] starting historical pool fetch via GetProgramAccounts")

	var wg sync.WaitGroup
	var vortexCount int
	var vortexMu sync.Mutex

	wg.Add(2)

	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		result, err := svc.rpcClient.GetProgramAccountsWithOpts(ctx, VortexProgramID, &rpc.GetProgramAccountsOpts{
			Commitment: rpc.CommitmentFinalized,
			Filters: []rpc.RPCFilter{
				{
					Memcmp: &rpc.RPCFilterMemcmp{
						Offset: 0,
						Bytes:  vortex_go.VortexAccountDiscriminator[:],
					},
				},
			},
		})
		if err != nil {
			log.Error().Err(err).Msg("[aggregatorService] failed to fetch historical Vortex pools")
			return
		}

		for _, keyedAccount := range result {
			data := keyedAccount.Account.Data.GetBinary()
			if len(data) == 0 {
				continue
			}
			if err := svc.processVortexPool(keyedAccount.Pubkey, data, 0); err == nil {
				vortexMu.Lock()
				vortexCount++
				vortexMu.Unlock()
			}
		}
		log.Info().Int("count", vortexCount).Msg("[aggregatorService] fetched historical Vortex pools")
	}()

	wg.Wait()

	log.Info().
		Int("vortex_pools", vortexCount).
		Dur("duration", time.Since(startTime)).
		Msg("[aggregatorService] completed historical pool fetch")

	allPools := svc.graph.GetAllPools()
	svc.fetchInitialTickArraysBatched(allPools)
	svc.fetchInitialVaultsBatched(allPools)

	if svc.storage != nil && vortexCount > 0 {
		if err := svc.storage.SavePoolBatch(allPools); err != nil {
			log.Error().Err(err).Msg("[aggregatorService] failed to persist historical pools")
		}
	}
}

type tickArrayFetchInfo struct {
	Pool           *domain.Pool
	TickArrayIndex int
	Address        solana.PublicKey
}

func (svc *Service) fetchInitialTickArraysBatched(pools []*domain.Pool) {
	var requests []tickArrayFetchInfo

	for _, pool := range pools {
		clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
		if !ok || clmmData == nil || len(clmmData.TickArrays) == 0 || clmmData.Liquidity.Cmp(big.NewInt(0)) <= 0 {
			continue
		}

		if clmmData.ParsedTickArrays == nil {
			clmmData.ParsedTickArrays = make([]*vortex_go.TickArrayAccount, len(clmmData.TickArrays))
		}

		for i, addr := range clmmData.TickArrays {
			requests = append(requests, tickArrayFetchInfo{
				Pool:           pool,
				TickArrayIndex: i,
				Address:        addr,
			})
		}
	}

	if len(requests) == 0 {
		return
	}

	const batchSize = 100
	const numWorkers = 20
	totalBatches := (len(requests) + batchSize - 1) / batchSize

	log.Info().
		Int("tick_arrays", len(requests)).
		Int("batches", totalBatches).
		Int("workers", numWorkers).
		Msg("[aggregatorService] fetching initial tick arrays with parallel workers")

	// Create batches
	type batch struct {
		index int
		items []tickArrayFetchInfo
	}
	batches := make(chan batch, totalBatches)
	for i := 0; i < len(requests); i += batchSize {
		end := i + batchSize
		if end > len(requests) {
			end = len(requests)
		}
		batches <- batch{index: i / batchSize, items: requests[i:end]}
	}
	close(batches)

	// Results
	var successCount, failCount int64
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Worker function
	worker := func() {
		defer wg.Done()
		for b := range batches {
			addresses := make([]solana.PublicKey, len(b.items))
			for j, req := range b.items {
				addresses[j] = req.Address
			}

			var accountInfos *rpc.GetMultipleAccountsResult
			var err error

			// Retry up to 3 times
			for retry := 0; retry < 3; retry++ {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				accountInfos, err = svc.rpcClient.GetMultipleAccounts(ctx, addresses...)
				cancel()

				if err == nil && accountInfos != nil && accountInfos.Value != nil {
					break
				}
				time.Sleep(time.Duration(100*(retry+1)) * time.Millisecond)
			}

			if err != nil || accountInfos == nil || accountInfos.Value == nil {
				mu.Lock()
				failCount += int64(len(b.items))
				mu.Unlock()
				continue
			}

			localSuccess := 0
			localFail := 0

			for j, accInfo := range accountInfos.Value {
				if accInfo == nil {
					localFail++
					continue
				}

				req := b.items[j]
				data := accInfo.Data.GetBinary()
				if len(data) < 8 {
					localFail++
					continue
				}

				var tickArray vortex_go.TickArrayAccount
				if err := bin.NewBinDecoder(data[8:]).Decode(&tickArray); err != nil {
					localFail++
					continue
				}

				clmmData := req.Pool.TypeSpecific.(*domain.VortexPoolData)
				svc.graph.Lock()
				clmmData.ParsedTickArrays[req.TickArrayIndex] = &tickArray
				if svc.hasAllTickArrays(clmmData) {
					req.Pool.SetReady(true)
				}
				svc.graph.Unlock()

				localSuccess++
			}

			mu.Lock()
			successCount += int64(localSuccess)
			failCount += int64(localFail)
			mu.Unlock()
		}
	}

	// Start workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker()
	}
	wg.Wait()

	log.Info().
		Int64("success", successCount).
		Int64("failed", failCount).
		Int("ready_pools", svc.graph.GetReadyPoolCount()).
		Msg("[aggregatorService] completed initial tick array fetch")
}

type vaultFetchInfo struct {
	Pool     *domain.Pool
	Address  solana.PublicKey
	IsVaultA bool
}

func (svc *Service) fetchInitialVaultsBatched(pools []*domain.Pool) {
	var requests []vaultFetchInfo

	for _, pool := range pools {
		clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
		if ok && clmmData != nil && clmmData.Liquidity.Cmp(big.NewInt(0)) > 0 {
			requests = append(requests,
				vaultFetchInfo{Pool: pool, Address: pool.TokenVaultA, IsVaultA: true},
				vaultFetchInfo{Pool: pool, Address: pool.TokenVaultB, IsVaultA: false},
			)
		}
	}

	if len(requests) == 0 {
		return
	}

	const batchSize = 100
	const numWorkers = 20
	totalBatches := (len(requests) + batchSize - 1) / batchSize

	log.Info().
		Int("vaults", len(requests)).
		Int("batches", totalBatches).
		Int("workers", numWorkers).
		Msg("[aggregatorService] fetching initial vault balances with parallel workers")

	type batch struct {
		index int
		items []vaultFetchInfo
	}
	batches := make(chan batch, totalBatches)
	for i := 0; i < len(requests); i += batchSize {
		end := i + batchSize
		if end > len(requests) {
			end = len(requests)
		}
		batches <- batch{index: i / batchSize, items: requests[i:end]}
	}
	close(batches)

	var successCount, failCount int64
	var mu sync.Mutex
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for b := range batches {
			addresses := make([]solana.PublicKey, len(b.items))
			for j, req := range b.items {
				addresses[j] = req.Address
			}

			var accountInfos *rpc.GetMultipleAccountsResult
			var err error

			for retry := 0; retry < 3; retry++ {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				accountInfos, err = svc.rpcClient.GetMultipleAccounts(ctx, addresses...)
				cancel()

				if err == nil && accountInfos != nil && accountInfos.Value != nil {
					break
				}
				time.Sleep(time.Duration(100*(retry+1)) * time.Millisecond)
			}

			if err != nil || accountInfos == nil || accountInfos.Value == nil {
				mu.Lock()
				failCount += int64(len(b.items))
				mu.Unlock()
				continue
			}

			localSuccess := 0
			localFail := 0

			for j, accInfo := range accountInfos.Value {
				if accInfo == nil {
					localFail++
					continue
				}

				req := b.items[j]
				data := accInfo.Data.GetBinary()
				balance, err := svc.parseTokenAccountBalance(data)
				if err != nil || balance == nil {
					localFail++
					continue
				}

			svc.graph.Lock()
			if req.IsVaultA {
				req.Pool.UpdateReserveA(balance)
			} else {
				req.Pool.UpdateReserveB(balance)
			}
			svc.graph.Unlock()

				localSuccess++
			}

			mu.Lock()
			successCount += int64(localSuccess)
			failCount += int64(localFail)
			mu.Unlock()
		}
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker()
	}
	wg.Wait()

	log.Info().
		Int64("success", successCount).
		Int64("failed", failCount).
		Msg("[aggregatorService] completed initial vault balance fetch")
}
