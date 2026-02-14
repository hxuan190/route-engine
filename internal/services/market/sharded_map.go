package market

import (
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/domain"
)

const numShards = 16

// ShardedPoolMap is a sharded map for pools to reduce lock contention
type ShardedPoolMap struct {
	shards [numShards]poolShard
}

type poolShard struct {
	mu    sync.RWMutex
	pools map[solana.PublicKey]*domain.Pool
}

// NewShardedPoolMap creates a new sharded pool map
func NewShardedPoolMap() *ShardedPoolMap {
	m := &ShardedPoolMap{}
	for i := 0; i < numShards; i++ {
		m.shards[i].pools = make(map[solana.PublicKey]*domain.Pool)
	}
	return m
}

// getShard returns the shard index for a given key
func (m *ShardedPoolMap) getShard(key solana.PublicKey) *poolShard {
	// Use first byte of public key for sharding (simple and fast)
	idx := key[0] % numShards
	return &m.shards[idx]
}

// Get retrieves a pool by address
func (m *ShardedPoolMap) Get(key solana.PublicKey) (*domain.Pool, bool) {
	shard := m.getShard(key)
	shard.mu.RLock()
	pool, ok := shard.pools[key]
	shard.mu.RUnlock()
	return pool, ok
}

// Set stores a pool
func (m *ShardedPoolMap) Set(key solana.PublicKey, pool *domain.Pool) {
	shard := m.getShard(key)
	shard.mu.Lock()
	shard.pools[key] = pool
	shard.mu.Unlock()
}

// Delete removes a pool
func (m *ShardedPoolMap) Delete(key solana.PublicKey) {
	shard := m.getShard(key)
	shard.mu.Lock()
	delete(shard.pools, key)
	shard.mu.Unlock()
}

// Len returns total count across all shards
func (m *ShardedPoolMap) Len() int {
	total := 0
	for i := 0; i < numShards; i++ {
		m.shards[i].mu.RLock()
		total += len(m.shards[i].pools)
		m.shards[i].mu.RUnlock()
	}
	return total
}

// Range iterates over all pools (acquires locks per shard)
func (m *ShardedPoolMap) Range(f func(key solana.PublicKey, pool *domain.Pool) bool) {
	for i := 0; i < numShards; i++ {
		m.shards[i].mu.RLock()
		for k, v := range m.shards[i].pools {
			if !f(k, v) {
				m.shards[i].mu.RUnlock()
				return
			}
		}
		m.shards[i].mu.RUnlock()
	}
}

// GetAll returns all pools as a slice
func (m *ShardedPoolMap) GetAll() []*domain.Pool {
	// Estimate total size
	total := m.Len()
	result := make([]*domain.Pool, 0, total)

	for i := 0; i < numShards; i++ {
		m.shards[i].mu.RLock()
		for _, pool := range m.shards[i].pools {
			result = append(result, pool)
		}
		m.shards[i].mu.RUnlock()
	}
	return result
}

// ShardedVaultMap is a sharded map for vault info to reduce lock contention
type ShardedVaultMap struct {
	shards [numShards]vaultShard
}

type vaultShard struct {
	mu     sync.RWMutex
	vaults map[solana.PublicKey]domain.VaultInfo
}

// NewShardedVaultMap creates a new sharded vault map
func NewShardedVaultMap() *ShardedVaultMap {
	m := &ShardedVaultMap{}
	for i := 0; i < numShards; i++ {
		m.shards[i].vaults = make(map[solana.PublicKey]domain.VaultInfo)
	}
	return m
}

// getShard returns the shard index for a given key
func (m *ShardedVaultMap) getShard(key solana.PublicKey) *vaultShard {
	idx := key[0] % numShards
	return &m.shards[idx]
}

// Get retrieves vault info by address
func (m *ShardedVaultMap) Get(key solana.PublicKey) (domain.VaultInfo, bool) {
	shard := m.getShard(key)
	shard.mu.RLock()
	info, ok := shard.vaults[key]
	shard.mu.RUnlock()
	return info, ok
}

// Set stores vault info
func (m *ShardedVaultMap) Set(key solana.PublicKey, info domain.VaultInfo) {
	shard := m.getShard(key)
	shard.mu.Lock()
	shard.vaults[key] = info
	shard.mu.Unlock()
}

// Exists checks if a key exists
func (m *ShardedVaultMap) Exists(key solana.PublicKey) bool {
	shard := m.getShard(key)
	shard.mu.RLock()
	_, ok := shard.vaults[key]
	shard.mu.RUnlock()
	return ok
}

// Len returns total count across all shards
func (m *ShardedVaultMap) Len() int {
	total := 0
	for i := 0; i < numShards; i++ {
		m.shards[i].mu.RLock()
		total += len(m.shards[i].vaults)
		m.shards[i].mu.RUnlock()
	}
	return total
}

// GetAllKeys returns all vault addresses
func (m *ShardedVaultMap) GetAllKeys() []solana.PublicKey {
	total := m.Len()
	result := make([]solana.PublicKey, 0, total)

	for i := 0; i < numShards; i++ {
		m.shards[i].mu.RLock()
		for key := range m.shards[i].vaults {
			result = append(result, key)
		}
		m.shards[i].mu.RUnlock()
	}
	return result
}

// ShardedTickArrayMap is a sharded map for tick array info
type ShardedTickArrayMap struct {
	shards [numShards]tickArrayShard
}

type tickArrayShard struct {
	mu         sync.RWMutex
	tickArrays map[solana.PublicKey]domain.TickArrayInfo
}

// NewShardedTickArrayMap creates a new sharded tick array map
func NewShardedTickArrayMap() *ShardedTickArrayMap {
	m := &ShardedTickArrayMap{}
	for i := 0; i < numShards; i++ {
		m.shards[i].tickArrays = make(map[solana.PublicKey]domain.TickArrayInfo)
	}
	return m
}

// getShard returns the shard index for a given key
func (m *ShardedTickArrayMap) getShard(key solana.PublicKey) *tickArrayShard {
	idx := key[0] % numShards
	return &m.shards[idx]
}

// Get retrieves tick array info by address
func (m *ShardedTickArrayMap) Get(key solana.PublicKey) (domain.TickArrayInfo, bool) {
	shard := m.getShard(key)
	shard.mu.RLock()
	info, ok := shard.tickArrays[key]
	shard.mu.RUnlock()
	return info, ok
}

// Set stores tick array info
func (m *ShardedTickArrayMap) Set(key solana.PublicKey, info domain.TickArrayInfo) {
	shard := m.getShard(key)
	shard.mu.Lock()
	shard.tickArrays[key] = info
	shard.mu.Unlock()
}

// Exists checks if a key exists
func (m *ShardedTickArrayMap) Exists(key solana.PublicKey) bool {
	shard := m.getShard(key)
	shard.mu.RLock()
	_, ok := shard.tickArrays[key]
	shard.mu.RUnlock()
	return ok
}

// Len returns total count across all shards
func (m *ShardedTickArrayMap) Len() int {
	total := 0
	for i := 0; i < numShards; i++ {
		m.shards[i].mu.RLock()
		total += len(m.shards[i].tickArrays)
		m.shards[i].mu.RUnlock()
	}
	return total
}
