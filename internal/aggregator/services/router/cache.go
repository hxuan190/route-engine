package router

import (
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
	"github.com/thehyperflames/hyperion-runtime/internal/metrics"
)

const (
	quoteCacheTTL     = 300 * time.Millisecond // Cache for 300ms (pools update every ~400ms)
	quoteCacheMaxSize = 1024                   // Power of 2 for efficient modulo
	quoteCacheShards  = 16                     // Number of shards for reduced lock contention
)

// FNV-1a constants for zero-allocation hashing
const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// cacheEntry represents a cached quote in contiguous memory
type cacheEntry struct {
	key    uint64
	quote  *domain.MultiHopQuoteResult
	expiry int64  // Unix nano for faster comparison
	used   uint32 // Clock bit for eviction
}

// cacheShard is a single shard of the cache
type cacheShard struct {
	mu      sync.RWMutex
	entries []cacheEntry
	size    int
	hand    int // Clock hand for eviction
}

// QuoteCache implements a sharded clock-based cache with TTL for quotes
// Uses array-based storage for better cache locality
type QuoteCache struct {
	shards   [quoteCacheShards]cacheShard
	stopChan chan struct{}
}

func NewQuoteCache() *QuoteCache {
	qc := &QuoteCache{
		stopChan: make(chan struct{}),
	}
	// Initialize each shard with pre-allocated entries
	entriesPerShard := quoteCacheMaxSize / quoteCacheShards
	for i := 0; i < quoteCacheShards; i++ {
		qc.shards[i].entries = make([]cacheEntry, entriesPerShard)
	}
	go qc.cleanupLoop()
	return qc
}

// Stop stops the cleanup goroutine
func (qc *QuoteCache) Stop() {
	close(qc.stopChan)
}

// makeKeyInline generates a fast cache key using inline FNV-1a (zero allocation)
func makeKeyInline(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) uint64 {
	h := uint64(fnvOffset64)

	// Hash input mint (32 bytes)
	for _, b := range inputMint {
		h ^= uint64(b)
		h *= fnvPrime64
	}

	// Hash output mint (32 bytes)
	for _, b := range outputMint {
		h ^= uint64(b)
		h *= fnvPrime64
	}

	// Hash amount as uint64 if it fits, otherwise hash bytes
	if amount != nil && amount.IsUint64() {
		amountU64 := amount.Uint64()
		for i := 0; i < 8; i++ {
			h ^= (amountU64 >> (i * 8)) & 0xFF
			h *= fnvPrime64
		}
	} else if amount != nil {
		// Fallback for large amounts (rare)
		bytes := amount.Bytes()
		for _, b := range bytes {
			h ^= uint64(b)
			h *= fnvPrime64
		}
	}

	// Hash exactIn flag
	if exactIn {
		h ^= 1
	}
	h *= fnvPrime64

	return h
}

// getShard returns the shard for a given key
func (qc *QuoteCache) getShard(key uint64) *cacheShard {
	return &qc.shards[key%quoteCacheShards]
}

func (qc *QuoteCache) Get(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) *domain.MultiHopQuoteResult {
	key := makeKeyInline(inputMint, outputMint, amount, exactIn)
	now := time.Now().UnixNano()

	shard := qc.getShard(key)
	shard.mu.RLock()

	// Linear search in shard (good cache locality for small arrays)
	for i := 0; i < shard.size; i++ {
		entry := &shard.entries[i]
		if entry.key == key && now <= entry.expiry {
			// Mark as used (atomic for clock algorithm)
			atomic.StoreUint32(&entry.used, 1)
			quote := entry.quote
			shard.mu.RUnlock()
			return quote
		}
	}

	shard.mu.RUnlock()
	return nil
}

func (qc *QuoteCache) Set(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool, quote *domain.MultiHopQuoteResult) {
	key := makeKeyInline(inputMint, outputMint, amount, exactIn)
	expiry := time.Now().Add(quoteCacheTTL).UnixNano()

	shard := qc.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	// Check if entry already exists, update if so
	for i := 0; i < shard.size; i++ {
		entry := &shard.entries[i]
		if entry.key == key {
			entry.quote = quote
			entry.expiry = expiry
			atomic.StoreUint32(&entry.used, 1)
			return
		}
	}

	// Find a slot: either empty or use clock eviction
	entriesPerShard := len(shard.entries)

	if shard.size < entriesPerShard {
		// Have empty slot
		entry := &shard.entries[shard.size]
		entry.key = key
		entry.quote = quote
		entry.expiry = expiry
		entry.used = 1
		shard.size++
		return
	}

	// Clock eviction: find an entry to evict
	for attempts := 0; attempts < entriesPerShard*2; attempts++ {
		entry := &shard.entries[shard.hand]
		shard.hand = (shard.hand + 1) % entriesPerShard

		// If used bit is 0 or entry is expired, evict it
		now := time.Now().UnixNano()
		if atomic.LoadUint32(&entry.used) == 0 || now > entry.expiry {
			entry.key = key
			entry.quote = quote
			entry.expiry = expiry
			entry.used = 1
			return
		}

		// Clear used bit (second chance)
		atomic.StoreUint32(&entry.used, 0)
	}

	// Fallback: overwrite at current hand position
	entry := &shard.entries[shard.hand]
	entry.key = key
	entry.quote = quote
	entry.expiry = expiry
	entry.used = 1
	shard.hand = (shard.hand + 1) % entriesPerShard
}

// evictExpired removes expired entries from all shards
func (qc *QuoteCache) evictExpired() {
	now := time.Now().UnixNano()

	for i := 0; i < quoteCacheShards; i++ {
		shard := &qc.shards[i]
		shard.mu.Lock()

		// Mark expired entries as unused (they'll be evicted on next Set)
		for j := 0; j < shard.size; j++ {
			entry := &shard.entries[j]
			if now > entry.expiry {
				atomic.StoreUint32(&entry.used, 0)
			}
		}

		shard.mu.Unlock()
	}
}

// Size returns current cache size across all shards
func (qc *QuoteCache) Size() int {
	total := 0
	for i := 0; i < quoteCacheShards; i++ {
		shard := &qc.shards[i]
		shard.mu.RLock()
		total += shard.size
		shard.mu.RUnlock()
	}
	return total
}

func (qc *QuoteCache) cleanupLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-qc.stopChan:
			return
		case <-ticker.C:
			qc.evictExpired()
			// Update metrics
			metrics.QuoteCacheSize.Set(float64(qc.Size()))
		}
	}
}

// CachedRouter wraps Router with short-lived caching
type CachedRouter struct {
	*Router
	cache *QuoteCache
}

func NewCachedRouter(graph *Graph, quoter QuoterFunc) *CachedRouter {
	return &CachedRouter{
		Router: NewRouter(graph, quoter),
		cache:  NewQuoteCache(),
	}
}

func (r *CachedRouter) GetMultiHopQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.MultiHopQuoteResult, error) {
	// Check cache first
	if cached := r.cache.Get(inputMint, outputMint, amount, exactIn); cached != nil {
		metrics.QuoteCacheHits.Inc()
		return cached, nil
	}

	metrics.QuoteCacheMisses.Inc()

	// Compute quote
	quote, err := r.Router.GetMultiHopQuote(inputMint, outputMint, amount, exactIn)
	if err != nil {
		return nil, err
	}

	// Cache result
	r.cache.Set(inputMint, outputMint, amount, exactIn, quote)

	return quote, nil
}

func (r *CachedRouter) GetQuote(inputMint, outputMint solana.PublicKey, amount *big.Int, exactIn bool) (*domain.QuoteResult, error) {
	return r.Router.GetQuote(inputMint, outputMint, amount, exactIn)
}
