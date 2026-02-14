package blockchain

import (
	"context"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rs/zerolog/log"

	pb "github.com/andrew-solarstorm/yellowstone-grpc-client-go/proto"
	container "github.com/thehyperflames/dicontainer-go"
	"github.com/thehyperflames/hyperion-runtime/internal/config"
	"github.com/thehyperflames/yellowstone"
)

const BLOCKHASH_CACHE_SERVICE = "cache-blockhash-svc"

type CachedBlockhash struct {
	Blockhash            solana.Hash
	LastValidBlockHeight uint64
	Slot                 uint64
	UpdatedAt            time.Time
}

type BlockhashCacheService struct {
	container.BaseDIInstance

	mu        sync.RWMutex
	current   *CachedBlockhash
	ySvc      *yellowstone.Service
	rpcClient *rpc.Client
	subID     string
}

func (svc *BlockhashCacheService) ID() string {
	return BLOCKHASH_CACHE_SERVICE
}

func (svc *BlockhashCacheService) Configure(c container.IContainer) error {
	svc.ySvc = c.Instance(yellowstone.YELLOWSTONE_SERVICE).(*yellowstone.Service)
	rpcConfig := c.GetConfig(config.RPC_CONFIG_KEY).(*config.RPCConfig)

	svc.rpcClient = rpc.New(rpcConfig.RPCUrl)
	return nil
}

func (svc *BlockhashCacheService) Start() error {
	ctx := context.Background()
	if err := svc.fetchInitialBlockhash(ctx); err != nil {
		log.Warn().Err(err).Msg("[BlockhashCacheService] failed to fetch initial blockhash, will retry on first request")
	}

	subID, err := svc.ySvc.SubscribeBlockMeta(svc.handleBlockMeta)
	if err != nil {
		log.Error().Err(err).Msg("[BlockhashCacheService] failed to subscribe to block meta")
		return err
	}
	svc.subID = subID
	log.Info().Str("subID", subID).Msg("[BlockhashCacheService] subscribed to block meta for blockhash updates")

	return nil
}

func (svc *BlockhashCacheService) Stop() error {
	if svc.subID != "" {
		return svc.ySvc.Unsubscribe(svc.subID)
	}
	return nil
}

func (svc *BlockhashCacheService) fetchInitialBlockhash(ctx context.Context) error {
	res, err := svc.rpcClient.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return err
	}

	svc.mu.Lock()
	svc.current = &CachedBlockhash{
		Blockhash:            res.Value.Blockhash,
		LastValidBlockHeight: res.Value.LastValidBlockHeight,
		Slot:                 res.Context.Slot,
		UpdatedAt:            time.Now(),
	}
	svc.mu.Unlock()

	log.Info().
		Str("blockhash", res.Value.Blockhash.String()).
		Uint64("slot", res.Context.Slot).
		Msg("[BlockhashCacheService] initialized with blockhash")

	return nil
}

func (svc *BlockhashCacheService) handleBlockMeta(update *pb.SubscribeUpdate) error {
	blockMeta := update.GetBlockMeta()
	if blockMeta == nil {
		return nil
	}

	blockhashStr := blockMeta.GetBlockhash()
	if blockhashStr == "" {
		return nil
	}

	blockhash, err := solana.HashFromBase58(blockhashStr)
	if err != nil {
		return nil
	}

	slot := blockMeta.GetSlot()
	blockHeight := uint64(0)
	if bh := blockMeta.GetBlockHeight(); bh != nil {
		blockHeight = bh.GetBlockHeight()
	}

	lastValidBlockHeight := blockHeight + 150

	svc.mu.Lock()
	svc.current = &CachedBlockhash{
		Blockhash:            blockhash,
		LastValidBlockHeight: lastValidBlockHeight,
		Slot:                 slot,
		UpdatedAt:            time.Now(),
	}
	svc.mu.Unlock()

	return nil
}

func (svc *BlockhashCacheService) GetBlockhash(ctx context.Context) (solana.Hash, uint64, error) {
	svc.mu.RLock()
	cached := svc.current
	svc.mu.RUnlock()

	if cached != nil && time.Since(cached.UpdatedAt) < 2*time.Second {
		return cached.Blockhash, cached.LastValidBlockHeight, nil
	}

	res, err := svc.rpcClient.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		if cached != nil {
			return cached.Blockhash, cached.LastValidBlockHeight, nil
		}
		return solana.Hash{}, 0, err
	}

	svc.mu.Lock()
	svc.current = &CachedBlockhash{
		Blockhash:            res.Value.Blockhash,
		LastValidBlockHeight: res.Value.LastValidBlockHeight,
		Slot:                 res.Context.Slot,
		UpdatedAt:            time.Now(),
	}
	svc.mu.Unlock()

	return res.Value.Blockhash, res.Value.LastValidBlockHeight, nil
}
