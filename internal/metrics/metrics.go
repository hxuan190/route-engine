package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Pool metrics
	PoolCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_pool_count",
		Help: "Total number of pools in the routing graph",
	})

	PoolUpdates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_pool_updates_total",
		Help: "Total number of pool updates received",
	})

	VaultCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_vault_count",
		Help: "Total number of tracked vault accounts",
	})

	VaultUpdates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_vault_updates_total",
		Help: "Total number of vault balance updates received",
	})

	// Quote metrics
	QuoteRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hyperion_quote_requests_total",
			Help: "Total number of quote requests",
		},
		[]string{"swap_mode", "status"},
	)

	QuoteDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hyperion_quote_duration_seconds",
			Help:    "Quote request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"swap_mode"},
	)

	// Router phase metrics for performance analysis
	DirectQuoteDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_direct_quote_duration_seconds",
		Help:    "Direct quote calculation duration in seconds",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05},
	})

	MultiHopDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_multihop_duration_seconds",
		Help:    "Multi-hop routing duration in seconds",
		Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1},
	})

	SplitRouteDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_split_route_duration_seconds",
		Help:    "Split route calculation duration in seconds",
		Buckets: []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.02},
	})

	QuoteCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_quote_cache_hits_total",
		Help: "Total number of quote cache hits",
	})

	QuoteCacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_quote_cache_misses_total",
		Help: "Total number of quote cache misses",
	})

	GraphSnapshotRebuilds = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_graph_snapshot_rebuilds_total",
		Help: "Total number of graph snapshot rebuilds",
	})

	GraphIncrementalUpdates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_graph_incremental_updates_total",
		Help: "Total number of incremental graph updates",
	})

	// Cache metrics
	QuoteCacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_quote_cache_size",
		Help: "Current number of entries in quote cache",
	})

	DecimalsCacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_decimals_cache_size",
		Help: "Current number of entries in decimals cache",
	})

	TokenProgramCacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_token_program_cache_size",
		Help: "Current number of entries in token program cache",
	})

	TickArrayPDACacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_tick_array_pda_cache_size",
		Help: "Current number of entries in tick array PDA cache",
	})

	// Pool readiness metrics
	ReadyPoolCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_ready_pool_count",
		Help: "Number of pools ready for trading",
	})

	// Routing metrics
	TwoHopQuoteCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_two_hop_quote_cache_hits_total",
		Help: "Total number of two-hop quote cache hits within request",
	})

	SplitRoutingIterations = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_split_routing_iterations",
		Help:    "Number of iterations in split routing optimization",
		Buckets: []float64{1, 2, 3, 5, 7, 10, 15, 20},
	})

	// Granular router phase metrics for profiling
	VortexQuoteDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_vortex_quote_duration_seconds",
		Help:    "Single Vortex quote calculation duration in seconds",
		Buckets: []float64{0.00001, 0.00005, 0.0001, 0.0005, 0.001, 0.005, 0.01, 0.02, 0.05},
	})

	BinarySearchSplitDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_binary_search_split_duration_seconds",
		Help:    "Binary search split optimization duration in seconds",
		Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1},
	})

	CalculateSplitDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_calculate_split_duration_seconds",
		Help:    "Single split calculation duration in seconds",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.02},
	})

	TwoHopQuoteDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_two_hop_quote_duration_seconds",
		Help:    "Two-hop quote calculation duration in seconds",
		Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1},
	})

	ThreeWaySplitDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_three_way_split_duration_seconds",
		Help:    "Three-way split optimization duration in seconds",
		Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1},
	})

	PoolsEvaluated = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_pools_evaluated",
		Help:    "Number of pools evaluated per quote request",
		Buckets: []float64{1, 2, 3, 5, 10, 20, 50},
	})

	PriceImpact = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hyperion_price_impact_bps",
			Help:    "Price impact in basis points",
			Buckets: []float64{0, 10, 50, 100, 300, 500, 1000, 5000, 10000},
		},
		[]string{"severity"},
	)

	// Swap metrics
	SwapRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hyperion_swap_requests_total",
			Help: "Total number of swap transaction build requests",
		},
		[]string{"swap_mode", "use_v2", "status"},
	)

	SwapDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hyperion_swap_duration_seconds",
			Help:    "Swap transaction build duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"swap_mode"},
	)

	// Simulation metrics
	SimulationRequests = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_simulation_requests_total",
		Help: "Total number of transaction simulations",
	})

	SimulationSuccess = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hyperion_simulation_success_total",
		Help: "Total number of successful transaction simulations",
	})

	SimulationFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hyperion_simulation_failures_total",
			Help: "Total number of failed transaction simulations",
		},
		[]string{"reason"},
	)

	ComputeUnits = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hyperion_compute_units",
		Help:    "Compute units consumed by transactions",
		Buckets: []float64{1000, 5000, 10000, 50000, 100000, 200000, 400000},
	})

	// HTTP metrics
	HTTPRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hyperion_http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	HTTPDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hyperion_http_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// Historical fetch metric
	HistoricalFetchDuration = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_historical_fetch_duration_seconds",
		Help: "Duration of the last historical pool fetch",
	})

	HistoricalPoolsDiscovered = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hyperion_historical_pools_discovered",
		Help: "Number of pools discovered during historical fetch",
	})
)
