package main

import (
	"github.com/hxuan190/route-engine/internal/aggregator"
	"github.com/hxuan190/route-engine/internal/aggregator/adapters/blockchain"
	"github.com/hxuan190/route-engine/internal/aggregator/services/builder"
	"github.com/hxuan190/route-engine/internal/aggregator/services/market"
	"github.com/hxuan190/route-engine/internal/aggregator/services/router"
	"github.com/hxuan190/route-engine/internal/common"
	"github.com/hxuan190/route-engine/internal/config"

	"github.com/hxuan190/route-engine/internal/http"
	topmarket "github.com/hxuan190/route-engine/internal/market"
	"github.com/hxuan190/route-engine/internal/price"
	"github.com/hxuan190/route-engine/internal/repository"
	"github.com/hxuan190/route-engine/internal/services"
	valiant "github.com/hxuan190/valiant_go"
	"github.com/hxuan190/yellowstone"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	container "github.com/hxuan190/dicontainer-go"
)

// @title Drogo Aggregator API
// @version 1.0-beta
// @description High-performance Fogo DEX aggregator API for optimal token swaps across multiple liquidity sources.
// @description
// @description ## - Features
//// @description - **Multi-DEX Aggregation**: Routes through Raydium, Orca, Meteora, PumpFun, and more
// @description - **Smart Routing**: Automatic direct or multi-hop routing through FOGO/USDC
// @description - **Price Impact Analysis**: Real-time price impact calculation with severity warnings
// @description - **Transaction Simulation**: Pre-flight validation to prevent failed transactions
// @description - **Slippage Protection**: Configurable slippage tolerance with automatic threshold calculation
// @description - **High Performance**: Optimized for low-latency trading with HFT-grade infrastructure
// @description
// @description ## - Supported DEXs
// @description | DEX | Program ID | Pool Types |
// @description |-----|-----------|------------|
// @description | **Valiant CLMM** | `vnt1u7PzorND5JjweFWmDawKe2hLWoTwHU6QKz6XX98` | Concentrated Liquidity |
// @description
// @description ## - API Status
// @description - **Version**: 1.0-beta
// @description - **Status**: Beta - Active Development
// @description - **Network**: Fogo Mainnet
// @description - **Rate Limit**: 10 requests/second (burst: 20)
// @description
// @description ## - Usage Tips
// @description - Use smallest token units (lamports for Fogo, base units for SPL tokens)
// @description - Fogo has 9 decimals: 1 Fogo = 1,000,000,000 lamports
// @description - USDC has 6 decimals: 1 USDC = 1,000,000 base units
// @description - Default slippage is 50 bps (0.5%)
// @description - Transactions expire after ~60 seconds (based on lastValidBlockHeight)
// @description
// @description ## - Social & Support
// @description - X: [@drogotrade](https://x.com/drogotrade)
// @description
// @host api.hypersvm.xyz
// @BasePath /
// @schemes https http
// @x-logo {"url": "https://drogo.trade/drogo.png", "altText": "Drogo Logo"}
// @tag.name quote
// @tag.description Get optimal swap quotes with price impact analysis and routing information
// @tag.name swap
// @tag.description Build unsigned swap transactions ready for signing and execution
// @tag.name price
// @tag.description Get token prices in USD
// @tag.name tokens
// @tag.description Search and discover token metadata

func main() {
	// Initialize HFT runtime optimizations (GOGC, GOMAXPROCS, GOMEMLIMIT)
	common.InitRuntimeForHFT()

	// load env
	err := godotenv.Load()
	if err != nil {
		log.Error().Err(err).Msg("failed to load env")
		return
	}

	// di container config
	conf := container.NewConf(
		&config.GeneralConfig{},
		&config.RPCConfig{},
		&yellowstone.Config{},
		&config.AggregatorConfig{},
		&config.LUTConfig{},
	)

	// di container
	dic, err := container.New(
		// config
		conf,

		// services
		// core (repository)
		&repository.Repository{},

		&yellowstone.Service{},

		&services.TransactionService{},
		&services.RPCAdapterService{},
		&services.IngestService{},
		&valiant.ValiantService{},
		&builder.BuilderService{},
		&router.Graph{},
		&aggregator.Service{},

		&blockchain.BlockhashCacheService{},

		&http.HTTPService{},
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to create di container")
		return
	}

	// Use RunBlock() - waits for SIGINT/SIGTERM
	if err := dic.Run(); err != nil {
		log.Error().Err(err).Msg("failed to run di container")
		return
	}

	// RunBlock() doesn't call Stop(), we must do it manually
	log.Info().Msg("Shutting down services...")
	if err := dic.Stop(); err != nil {
		log.Error().Err(err).Msg("error during shutdown")
	}
	log.Info().Msg("Shutdown complete")
}
