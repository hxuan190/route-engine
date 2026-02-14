package market

import (
	"context"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"time"

	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rs/zerolog/log"
	container "github.com/thehyperflames/dicontainer-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/adapters/persistence"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/services/router"
	"github.com/thehyperflames/hyperion-runtime/internal/common"
	"github.com/thehyperflames/hyperion-runtime/internal/config"
	"github.com/thehyperflames/hyperion-api/internal/market"
	"github.com/thehyperflames/hyperion-runtime/internal/metrics"
	"github.com/thehyperflames/hyperion-api/internal/price"
	"github.com/thehyperflames/yellowstone"
)

var (
	VortexProgramID = solana.MustPublicKeyFromBase58("vnt1u7PzorND5JjweFWmDawKe2hLWoTwHU6QKz6XX98")
)

const (
	// Cache size limits
	decimalsCacheMaxSize         = 10000
	mintTokenProgramCacheMaxSize = 10000
)

// decimalsFetchJob represents a pending decimals fetch request
type decimalsFetchJob struct {
	mint     solana.PublicKey
	poolAddr solana.PublicKey
}

const (
	ServiceName = "MarketService"
)

type Service struct {
	container.DIContainer

	mu        sync.RWMutex // Only for non-sharded operations
	rpcClient *rpc.Client
	ySvc      *yellowstone.Service
	priceSvc  *price.PriceService
	candleSvc *market.CandlesService
	graph     *router.Graph
	storage   *persistence.Storage
	config    *config.AggregatorConfig

	// Sharded maps for reduced lock contention
	pools           *ShardedPoolMap
	vaultToPool     *ShardedVaultMap
	tickArrayToPool *ShardedTickArrayMap

	pendingVaults       []string
	pendingVaultsMu     sync.Mutex
	pendingTickArrays   []string
	pendingTickArraysMu sync.Mutex
	pendingPools        []*domain.Pool
	pendingPoolsMu      sync.Mutex

	// Atomic counters for lock-free stats
	updateCount          atomic.Uint64
	vaultUpdateCount     atomic.Uint64
	tickArrayUpdateCount atomic.Uint64

	env string

	// Bounded LRU caches
	decimalsCache         *BoundedLRUCache[solana.PublicKey, uint8]
	mintTokenProgramCache *BoundedLRUCache[solana.PublicKey, solana.PublicKey]

	// Async decimals fetching
	decimalsFetchQueue chan decimalsFetchJob
	decimalsFetchDone  chan struct{}
}

func (svc *Service) ID() string {
	return ServiceName
}

func (svc *Service) Configure(c container.IContainer) error {
	var err error
	rpcConfig := c.GetConfig(config.RPC_CONFIG_KEY).(*config.RPCConfig)
	svc.config = c.GetConfig(config.AGGREGATOR_CONFIG_KEY).(*config.AggregatorConfig)

	svc.rpcClient = rpc.New(rpcConfig.RPCUrl)
	svc.ySvc = c.Instance(yellowstone.YELLOWSTONE_SERVICE).(*yellowstone.Service)
	svc.priceSvc = c.Instance(price.PRICE_SERVICE).(*price.PriceService)
	svc.candleSvc = c.Instance(market.CANDLES_SERVICE).(*market.CandlesService)
	svc.graph = c.Instance(router.ROUTER_SERVICE).(*router.Graph)
	svc.storage, err = persistence.NewStorage(svc.config.DBPath)
	if err != nil {
		return err
	}
	svc.env = os.Getenv("ENV")

	svc.pools = NewShardedPoolMap()
	svc.vaultToPool = NewShardedVaultMap()
	svc.tickArrayToPool = NewShardedTickArrayMap()
	svc.decimalsCache = NewBoundedLRUCache[solana.PublicKey, uint8](decimalsCacheMaxSize)
	svc.mintTokenProgramCache = NewBoundedLRUCache[solana.PublicKey, solana.PublicKey](mintTokenProgramCacheMaxSize)
	svc.pendingVaults = make([]string, 0)
	svc.pendingTickArrays = make([]string, 0)
	svc.pendingPools = make([]*domain.Pool, 0)
	svc.decimalsFetchQueue = make(chan decimalsFetchJob, 1000)
	svc.decimalsFetchDone = make(chan struct{})

	return nil
}

func (svc *Service) Start() error {
	go svc.processDecimalsFetchQueue()

	if svc.env != "dev" {
		if svc.storage != nil {
			svc.loadPoolsFromStorage()
		}

		currentPoolCount := svc.graph.GetPoolCount()
		if currentPoolCount == 0 {
			log.Info().Msg("[MarketService] no persisted pools found, fetching historical pools from RPC")
			go svc.fetchHistoricalPools()
		} else {
			log.Info().Int("pools", currentPoolCount).Msg("[MarketService] loaded persisted pools, skipping historical fetch")
		}
	} else {
		log.Info().Msg("[MarketService] running in dev mode, skipping pool loading")
	}

	if err := svc.SubscribeToVortexPools(); err != nil {
		log.Error().Err(err).Msg("[MarketService] failed to subscribe to Vortex pools")
	}

	go func() {
		time.Sleep(5 * time.Second)
		svc.WarmMintTokenProgramCache(context.Background())
		log.Info().Msg("[MarketService] token program cache warmed")
	}()

	go svc.logStats()
	go svc.processVaultSubscriptions()
	go svc.processTickArraySubscriptions()

	if svc.storage != nil {
		go svc.processPersistence()
	}

	return nil
}

func (svc *Service) Stop() error {
	// Signal decimals fetcher to stop
	close(svc.decimalsFetchDone)

	if svc.storage != nil {
		allPools := svc.graph.GetAllPools()
		if len(allPools) > 0 {
			log.Info().Int("count", len(allPools)).Msg("[MarketService] persisting all pools before shutdown")
			if err := svc.storage.SavePoolBatch(allPools); err != nil {
				log.Error().Err(err).Msg("[MarketService] failed to persist pools on shutdown")
			}
		}
		if err := svc.storage.Close(); err != nil {
			log.Error().Err(err).Msg("[MarketService] failed to close storage")
		}
	}
	return nil
}

func (svc *Service) loadPoolsFromStorage() {
	pools, err := svc.storage.LoadAllPools()
	if err != nil {
		log.Error().Err(err).Msg("[MarketService] failed to load pools from storage")
		return
	}

	log.Info().Int("count", len(pools)).Msg("[MarketService] loading pools from storage")

	svc.graph.AddPoolsBatch(pools)

	// Use sharded maps - no global lock needed
	for _, pool := range pools {
		svc.pools.Set(pool.Address, pool)

		// Only register vaults and tick arrays for pools that have liquidity to save resources
		// Active pools will be registered via registerVaults/registerTickArrays when they receive updates
		if clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData); ok && clmmData != nil && clmmData.Liquidity.Cmp(big.NewInt(0)) > 0 {
			svc.vaultToPool.Set(pool.TokenVaultA, domain.VaultInfo{PoolAddress: pool.Address, IsVaultA: true})
			svc.vaultToPool.Set(pool.TokenVaultB, domain.VaultInfo{PoolAddress: pool.Address, IsVaultA: false})

			for i, ta := range clmmData.TickArrays {
				svc.tickArrayToPool.Set(ta, domain.TickArrayInfo{PoolAddress: pool.Address, TickArrayIndex: i})
			}
		}
	}

	// Fetch initial tick arrays and vaults in background so app starts fast
	go func() {
		log.Info().Int("pools", len(pools)).Msg("[MarketService] starting background pool hydration")
		svc.fetchInitialTickArraysBatched(pools)
		svc.fetchInitialVaultsBatched(pools)
		log.Info().
			Int("pools", svc.graph.GetPoolCount()).
			Int("ready_pools", svc.graph.GetReadyPoolCount()).
			Msg("[MarketService] background pool hydration complete")
	}()

	log.Info().
		Int("pools", svc.graph.GetPoolCount()).
		Int("ready_pools", svc.graph.GetReadyPoolCount()).
		Msg("[MarketService] startup complete")
}

func (svc *Service) ProcessVaultSubscriptions() {
	svc.processVaultSubscriptions()
}

func (svc *Service) ProcessTickArraySubscriptions() {
	svc.processTickArraySubscriptions()
}

func (svc *Service) GetStats() (int, uint64) {
	return svc.graph.GetPoolCount(), svc.updateCount.Load()
}

func (svc *Service) GetVaultStats() (int, uint64) {
	return svc.vaultToPool.Len(), svc.vaultUpdateCount.Load()
}

func (svc *Service) GetTickArrayStats() (int, uint64) {
	return svc.tickArrayToPool.Len(), svc.tickArrayUpdateCount.Load()
}

func (svc *Service) GetTokenDecimals(ctx context.Context, mint solana.PublicKey) (uint8, error) {
	if decimals, ok := svc.decimalsCache.Get(mint); ok {
		return decimals, nil
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	info, err := svc.rpcClient.GetAccountInfo(timeoutCtx, mint)
	if err != nil {
		return 0, err
	}

	var mintState token.Mint
	if err := bin.NewBinDecoder(info.Value.Data.GetBinary()).Decode(&mintState); err != nil {
		return 0, err
	}

	svc.decimalsCache.Set(mint, mintState.Decimals)
	return mintState.Decimals, nil
}

// getCachedDecimalsOrDefault returns cached decimals or a default value (non-blocking)
func (svc *Service) getCachedDecimalsOrDefault(mint solana.PublicKey) uint8 {
	// Check well-known tokens first
	if price.IsStableCoin(mint) {
		return 6
	}
	if mint.Equals(solana.SolMint) {
		return 9
	}

	if decimals, ok := svc.decimalsCache.GetWithoutPromote(mint); ok {
		return decimals
	}

	return 9 // Default to 9 decimals (SOL standard)
}

// getCachedTokenProgramOrDefault returns cached token program or default SPL Token program
func (svc *Service) getCachedTokenProgramOrDefault(mint solana.PublicKey) solana.PublicKey {
	if cached, ok := svc.mintTokenProgramCache.GetWithoutPromote(mint); ok {
		return cached
	}
	return common.TokenProgramID // Default to SPL Token program
}

// queueDecimalsFetch queues a mint for async decimals fetching
func (svc *Service) queueDecimalsFetch(mint, poolAddr solana.PublicKey) {
	// Skip if already cached
	if svc.decimalsCache.Contains(mint) {
		return
	}

	// Skip well-known tokens
	if price.IsStableCoin(mint) || mint.Equals(solana.SolMint) {
		return
	}

	select {
	case svc.decimalsFetchQueue <- decimalsFetchJob{mint: mint, poolAddr: poolAddr}:
	default:
		// Queue full, skip
	}
}

// processDecimalsFetchQueue processes async decimals fetch requests
func (svc *Service) processDecimalsFetchQueue() {
	batch := make([]solana.PublicKey, 0, 100)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-svc.decimalsFetchDone:
			return
		case job := <-svc.decimalsFetchQueue:
			// Check if already cached
			if !svc.decimalsCache.Contains(job.mint) {
				batch = append(batch, job.mint)
			}

			if len(batch) >= 100 {
				svc.fetchDecimalsBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				svc.fetchDecimalsBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

// fetchDecimalsBatch fetches decimals for multiple mints in one RPC call
// Also caches token programs from the account owner field
func (svc *Service) fetchDecimalsBatch(mints []solana.PublicKey) {
	if len(mints) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	infos, err := svc.rpcClient.GetMultipleAccounts(ctx, mints...)
	if err != nil {
		return
	}

	for i, info := range infos.Value {
		if info == nil {
			continue
		}
		var mintState token.Mint
		if err := bin.NewBinDecoder(info.Data.GetBinary()).Decode(&mintState); err != nil {
			continue
		}
		svc.decimalsCache.Set(mints[i], mintState.Decimals)
		// Cache token program from account owner
		svc.mintTokenProgramCache.Set(mints[i], info.Owner)
	}
}

func (svc *Service) queuePoolForPersistence(pool *domain.Pool) {
	if svc.storage == nil {
		return
	}
	svc.pendingPoolsMu.Lock()
	defer svc.pendingPoolsMu.Unlock()
	svc.pendingPools = append(svc.pendingPools, pool)
}

func (svc *Service) queueVaultForSubscription(vaultAddress string) {
	svc.pendingVaultsMu.Lock()
	defer svc.pendingVaultsMu.Unlock()
	svc.pendingVaults = append(svc.pendingVaults, vaultAddress)
}

func (svc *Service) queueTickArrayForSubscription(tickArrayAddress string) {
	svc.pendingTickArraysMu.Lock()
	defer svc.pendingTickArraysMu.Unlock()
	svc.pendingTickArrays = append(svc.pendingTickArrays, tickArrayAddress)
}

func (svc *Service) logStats() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		poolCount, updateCount := svc.GetStats()
		readyPoolCount := svc.graph.GetReadyPoolCount()
		vaultCount, vaultUpdates := svc.GetVaultStats()
		tickArrayCount, tickArrayUpdates := svc.GetTickArrayStats()

		metrics.PoolCount.Set(float64(poolCount))
		metrics.ReadyPoolCount.Set(float64(readyPoolCount))
		metrics.VaultCount.Set(float64(vaultCount))

		log.Info().
			Int("pools", poolCount).
			Int("ready_pools", readyPoolCount).
			Uint64("pool_updates", updateCount).
			Int("vaults", vaultCount).
			Uint64("vault_updates", vaultUpdates).
			Int("tick_arrays", tickArrayCount).
			Uint64("tick_array_updates", tickArrayUpdates).
			Msg("[MarketService] stats")
	}
}

func (svc *Service) processVaultSubscriptions() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		svc.subscribePendingVaults()
	}
}

func (svc *Service) subscribePendingVaults() {
	svc.pendingVaultsMu.Lock()
	if len(svc.pendingVaults) == 0 {
		svc.pendingVaultsMu.Unlock()
		return
	}
	vaults := svc.pendingVaults
	svc.pendingVaults = make([]string, 0)
	svc.pendingVaultsMu.Unlock()

	if err := svc.SubscribeToVaults(); err != nil {
		log.Error().Err(err).Msg("[MarketService] failed to subscribe to vault accounts")
		svc.pendingVaultsMu.Lock()
		svc.pendingVaults = append(svc.pendingVaults, vaults...)
		svc.pendingVaultsMu.Unlock()
	}
}

func (svc *Service) processTickArraySubscriptions() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		svc.subscribePendingTickArrays()
	}
}

func (svc *Service) subscribePendingTickArrays() {
	svc.pendingTickArraysMu.Lock()
	if len(svc.pendingTickArrays) == 0 {
		svc.pendingTickArraysMu.Unlock()
		return
	}
	tickArrays := svc.pendingTickArrays
	svc.pendingTickArrays = make([]string, 0)
	svc.pendingTickArraysMu.Unlock()

	if err := svc.subscribeToTickArrayAddresses(tickArrays); err != nil {
		log.Error().Err(err).Msg("[MarketService] failed to subscribe to tick array accounts")
		svc.pendingTickArraysMu.Lock()
		svc.pendingTickArrays = append(svc.pendingTickArrays, tickArrays...)
		svc.pendingTickArraysMu.Unlock()
	}
}

func (svc *Service) processPersistence() {
	interval := time.Duration(svc.config.PersistInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		svc.persistPendingPools()
	}
}

func (svc *Service) persistPendingPools() {
	svc.pendingPoolsMu.Lock()
	if len(svc.pendingPools) == 0 {
		svc.pendingPoolsMu.Unlock()
		return
	}
	pools := svc.pendingPools
	svc.pendingPools = make([]*domain.Pool, 0)
	svc.pendingPoolsMu.Unlock()

	if err := svc.storage.SavePoolBatch(pools); err != nil {
		log.Error().Err(err).Int("count", len(pools)).Msg("[MarketService] failed to persist pools")
		svc.pendingPoolsMu.Lock()
		svc.pendingPools = append(svc.pendingPools, pools...)
		svc.pendingPoolsMu.Unlock()
		return
	}

	log.Debug().Int("count", len(pools)).Msg("[MarketService] persisted pools to storage")
}

func (svc *Service) persistVaultMapping(vaultAddress solana.PublicKey, info domain.VaultInfo) {
	if svc.storage == nil {
		return
	}
	if err := svc.storage.SaveVaultMapping(vaultAddress, info); err != nil {
		log.Warn().Err(err).Str("vault", vaultAddress.String()).Msg("[MarketService] failed to persist vault mapping")
	}
}

func (svc *Service) registerVaults(pool *domain.Pool) {
	existsA := svc.vaultToPool.Exists(pool.TokenVaultA)
	existsB := svc.vaultToPool.Exists(pool.TokenVaultB)

	if !existsA {
		infoA := domain.VaultInfo{PoolAddress: pool.Address, IsVaultA: true}
		svc.vaultToPool.Set(pool.TokenVaultA, infoA)
		svc.queueVaultForSubscription(pool.TokenVaultA.String())
		go svc.persistVaultMapping(pool.TokenVaultA, infoA)
	}

	if !existsB {
		infoB := domain.VaultInfo{PoolAddress: pool.Address, IsVaultA: false}
		svc.vaultToPool.Set(pool.TokenVaultB, infoB)
		svc.queueVaultForSubscription(pool.TokenVaultB.String())
		go svc.persistVaultMapping(pool.TokenVaultB, infoB)
	}
}

func (svc *Service) registerTickArrays(pool *domain.Pool, tickArrayAddresses []solana.PublicKey) {
	for i, tickArrayAddr := range tickArrayAddresses {
		if svc.tickArrayToPool.Exists(tickArrayAddr) {
			continue
		}
		info := domain.TickArrayInfo{PoolAddress: pool.Address, TickArrayIndex: i}
		svc.tickArrayToPool.Set(tickArrayAddr, info)
		svc.queueTickArrayForSubscription(tickArrayAddr.String())
	}
}

func (svc *Service) WarmMintTokenProgramCache(ctx context.Context) {
	seen := make(map[string]bool)
	mints := make([]solana.PublicKey, 0, len(router.IntermediateTokens)+200)

	for _, mint := range router.IntermediateTokens {
		mints = append(mints, mint)
		seen[mint.String()] = true
	}

	readyPools := svc.graph.GetAllPools()
	for _, pool := range readyPools {
		if !pool.IsReady() {
			continue
		}
		if !seen[pool.TokenMintA.String()] {
			mints = append(mints, pool.TokenMintA)
			seen[pool.TokenMintA.String()] = true
		}
		if !seen[pool.TokenMintB.String()] {
			mints = append(mints, pool.TokenMintB)
			seen[pool.TokenMintB.String()] = true
		}
	}

	if len(mints) == 0 {
		return
	}

	const batchSize = 100
	for i := 0; i < len(mints); i += batchSize {
		end := i + batchSize
		if end > len(mints) {
			end = len(mints)
		}
		_, _ = svc.GetMintTokenProgramsBatch(ctx, mints[i:end])
	}
}

func (svc *Service) GetMintTokenProgram(ctx context.Context, mint solana.PublicKey) (solana.PublicKey, error) {
	if cached, ok := svc.mintTokenProgramCache.Get(mint); ok {
		return cached, nil
	}

	info, err := svc.rpcClient.GetAccountInfo(ctx, mint)
	if err != nil {
		return common.TokenProgramID, err
	}
	if info == nil || info.Value == nil {
		return common.TokenProgramID, nil // Default to Token Program
	}

	owner := info.Value.Owner
	var tokenProgram solana.PublicKey
	if owner == common.Token2022ID {
		tokenProgram = common.Token2022ID
	} else {
		tokenProgram = common.TokenProgramID
	}

	svc.mintTokenProgramCache.Set(mint, tokenProgram)
	return tokenProgram, nil
}

func (svc *Service) GetMintTokenProgramsBatch(ctx context.Context, mints []solana.PublicKey) ([]solana.PublicKey, error) {
	results := make([]solana.PublicKey, len(mints))
	uncachedIndices := make([]int, 0, len(mints))
	uncachedMints := make([]solana.PublicKey, 0, len(mints))

	for i, mint := range mints {
		if cached, ok := svc.mintTokenProgramCache.GetWithoutPromote(mint); ok {
			results[i] = cached
		} else {
			uncachedIndices = append(uncachedIndices, i)
			uncachedMints = append(uncachedMints, mint)
		}
	}

	if len(uncachedMints) == 0 {
		return results, nil
	}

	infos, err := svc.rpcClient.GetMultipleAccounts(ctx, uncachedMints...)
	if err != nil {
		return nil, err
	}

	for i, info := range infos.Value {
		var tokenProgram solana.PublicKey
		if info != nil && info.Owner == common.Token2022ID {
			tokenProgram = common.Token2022ID
		} else {
			tokenProgram = common.TokenProgramID
		}
		results[uncachedIndices[i]] = tokenProgram
		svc.mintTokenProgramCache.Set(uncachedMints[i], tokenProgram)
	}

	return results, nil
}

func (svc *Service) parseTokenAccountBalance(data []byte) (*big.Int, error) {
	if len(data) < 72 {
		return nil, nil
	}
	amount := uint64(data[64]) | uint64(data[65])<<8 | uint64(data[66])<<16 | uint64(data[67])<<24 |
		uint64(data[68])<<32 | uint64(data[69])<<40 | uint64(data[70])<<48 | uint64(data[71])<<56
	return big.NewInt(int64(amount)), nil
}
