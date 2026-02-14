package aggregator

import (
	"context"
	"math/big"
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rs/zerolog/log"
	container "github.com/thehyperflames/dicontainer-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/adapters/blockchain"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/services/builder"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/services/market"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/services/router"
	"github.com/thehyperflames/hyperion-runtime/internal/config"
	"github.com/thehyperflames/hyperion-api/internal/services"
)

const AGGREGATOR_SERVICE = "aggregator-service"

var (
	VortexProgramID = solana.MustPublicKeyFromBase58("vnt1u7PzorND5JjweFWmDawKe2hLWoTwHU6QKz6XX98")

	// Error aliases
	ErrNoPoolFound           = router.ErrNoPoolFound
	ErrNoRoute               = router.ErrNoRoute
	ErrInvalidPool           = router.ErrInvalidPool
	ErrInsufficientLiquidity = router.ErrInsufficientLiquidity

	ErrMissingPoolData   = builder.ErrMissingPoolData
	ErrInvalidUserWallet = builder.ErrInvalidUserWallet
)

type Service struct {
	container.BaseDIInstance
	logger         *services.ServiceLogger
	mu             sync.RWMutex
	rpcClient      *rpc.Client
	blockhashCache *blockchain.BlockhashCacheService

	// Core components
	graph        *router.Graph
	cachedRouter *router.CachedRouter
	marketSvc    *market.Service
	builderSvc   *builder.BuilderService

	config *config.AggregatorConfig
}

func (svc *Service) ID() string {
	return AGGREGATOR_SERVICE
}

func (svc *Service) Configure(c container.IContainer) error {
	svc.logger = services.NewServiceLogger(svc)
	rpcConfig := c.GetConfig(config.RPC_CONFIG_KEY).(*config.RPCConfig)
	svc.config = c.GetConfig(config.AGGREGATOR_CONFIG_KEY).(*config.AggregatorConfig)
	svc.builderSvc = c.Instance(builder.BUILDER_SERVICE_NAME).(*builder.BuilderService)
	svc.graph = c.Instance(router.ROUTER_SERVICE).(*router.Graph)
	svc.marketSvc = c.Instance(market.ServiceName).(*market.Service)
	svc.blockhashCache = c.Instance(blockchain.BLOCKHASH_CACHE_SERVICE).(*blockchain.BlockhashCacheService)

	svc.rpcClient = rpc.New(rpcConfig.RPCUrl)

	registry := market.NewDefaultMarketRegistry()
	vortexQuoter := router.NewVortexQuoter()
	registry.RegisterQuoter(vortexQuoter)

	svc.graph.SetReadyChecker(registry)
	svc.cachedRouter = router.NewCachedRouter(svc.graph, registry.GetQuote)

	// Set up fast quoter for zero-allocation hot path
	svc.cachedRouter.SetFastQuoter(func(pool *domain.Pool, amount uint64, aToB, exactIn bool) (*router.FastSwapQuote, error) {
		if exactIn {
			return vortexQuoter.GetFastQuoteExactIn(pool, amount, aToB)
		}
		return vortexQuoter.GetFastQuoteExactOut(pool, amount, aToB)
	})

	return nil
}

func (svc *Service) Start() error {
	if err := svc.blockhashCache.Start(); err != nil {
		return err
	}

	if err := svc.marketSvc.Start(); err != nil {
		return err
	}

	if err := svc.builderSvc.Start(); err != nil {
		return err
	}

	return nil
}

func (svc *Service) Stop() error {
	if err := svc.marketSvc.Stop(); err != nil {
		log.Error().Err(err).Msg("[aggregatorService] failed to stop market service")
	}
	return svc.blockhashCache.Stop()
}

func (svc *Service) GetCachedBlockhash(ctx context.Context) (solana.Hash, uint64, error) {
	return svc.blockhashCache.GetBlockhash(ctx)
}

func (svc *Service) GetStats() (int, uint64) {
	return svc.marketSvc.GetStats()
}

func (svc *Service) GetVaultStats() (int, uint64) {
	return svc.marketSvc.GetVaultStats()
}

func (svc *Service) GetTickArrayStats() (int, uint64) {
	return svc.marketSvc.GetTickArrayStats()
}

func (svc *Service) GetGraph() *router.Graph {
	return svc.graph
}

func (svc *Service) GetBuilderService() *builder.BuilderService {
	return svc.builderSvc
}

func (svc *Service) GetTokenDecimals(ctx context.Context, mint solana.PublicKey) (uint8, error) {
	return svc.marketSvc.GetTokenDecimals(ctx, mint)
}

func (svc *Service) GetQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.QuoteResult, error) {
	return svc.cachedRouter.GetQuote(inputMint, outputMint, amount, exactIn)
}

func (svc *Service) GetMultiHopQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	return svc.cachedRouter.GetMultiHopQuote(inputMint, outputMint, amount, exactIn)
}

// GetMultiHopQuoteFast uses the zero-allocation fast path with uint64
// Falls back to big.Int path if fast quoter is not available
func (svc *Service) GetMultiHopQuoteFast(inputMint, outputMint solana.PublicKey, amount uint64, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	return svc.cachedRouter.GetMultiHopQuoteFast(inputMint, outputMint, amount, exactIn)
}

func (svc *Service) BuildAndSignSessionSwapTransaction(ctx context.Context, req *domain.SwapRequest, sessionKey solana.PrivateKey, feePayer solana.PrivateKey) (*domain.SwapResponse, error) {
	exactIn := req.SwapMode == "ExactIn"
	quote, err := svc.GetQuote(req.InputMint, req.OutputMint, req.Amount, exactIn)
	if err != nil {
		return nil, err
	}
	return svc.builderSvc.BuildAndSignSessionSwapTransaction(ctx, req, quote, sessionKey, feePayer)
}

func (svc *Service) BuildMultiHopSessionSwapTransaction(ctx context.Context, req *domain.SwapRequest, sessionKey solana.PrivateKey, feePayer solana.PrivateKey) (*domain.MultiHopSwapResponse, error) {
	exactIn := req.SwapMode == "ExactIn"
	multiQuote, err := svc.getMultiHopQuoteAuto(req.InputMint, req.OutputMint, req.Amount, exactIn)
	if err != nil {
		return nil, err
	}
	return svc.builderSvc.BuildMultiHopSessionSwapTransaction(ctx, req, multiQuote, sessionKey, feePayer)
}

func (svc *Service) BuildMultiHopRouteTransaction(ctx context.Context, req *domain.SwapRequest) (*domain.MultiHopSwapResponse, error) {
	exactIn := req.SwapMode == "ExactIn"
	multiQuote, err := svc.getMultiHopQuoteAuto(req.InputMint, req.OutputMint, req.Amount, exactIn)
	if err != nil {
		return nil, err
	}
	return svc.builderSvc.BuildMultiHopSwapTransaction(ctx, req, multiQuote)
}

// getMultiHopQuoteAuto automatically selects fast path (uint64) or slow path (big.Int)
func (svc *Service) getMultiHopQuoteAuto(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	if amount.IsUint64() {
		return svc.GetMultiHopQuoteFast(inputMint, outputMint, amount.Uint64(), exactIn)
	}
	return svc.GetMultiHopQuote(inputMint, outputMint, amount, exactIn)
}

func (svc *Service) SimulateTransaction(ctx context.Context, tx *solana.Transaction) (*domain.SimulationResult, error) {
	return svc.builderSvc.SimulateTransaction(ctx, tx)
}

func (svc *Service) ValidateSwapSimulation(ctx context.Context, tx *solana.Transaction) error {
	return svc.builderSvc.ValidateSwapSimulation(ctx, tx)
}

func (svc *Service) EstimateComputeUnits(ctx context.Context, tx *solana.Transaction) (uint64, error) {
	return svc.builderSvc.EstimateComputeUnits(ctx, tx)
}
