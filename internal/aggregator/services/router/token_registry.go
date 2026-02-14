package router

import (
	"sync"
	"sync/atomic"

	"github.com/gagliardetto/solana-go"
)

// TokenID is a compact integer identifier for tokens
type TokenID uint32

// InvalidTokenID represents an invalid/unknown token
const InvalidTokenID TokenID = 0xFFFFFFFF

// TokenRegistry maps PublicKey to compact integer IDs for O(1) array access
type TokenRegistry struct {
	mu     sync.RWMutex
	toID   map[solana.PublicKey]TokenID // PublicKey -> ID (write path)
	toMint []solana.PublicKey           // ID -> PublicKey (read path)
	nextID atomic.Uint32
}

// NewTokenRegistry creates a new token registry
func NewTokenRegistry() *TokenRegistry {
	return &TokenRegistry{
		toID:   make(map[solana.PublicKey]TokenID, 1024),
		toMint: make([]solana.PublicKey, 0, 1024),
	}
}

// GetOrCreate returns the ID for a mint, creating one if it doesn't exist
func (r *TokenRegistry) GetOrCreate(mint solana.PublicKey) TokenID {
	// Fast path: read lock check
	r.mu.RLock()
	if id, ok := r.toID[mint]; ok {
		r.mu.RUnlock()
		return id
	}
	r.mu.RUnlock()

	// Slow path: write lock to create
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double check after acquiring write lock
	if id, ok := r.toID[mint]; ok {
		return id
	}

	id := TokenID(r.nextID.Add(1) - 1)
	r.toID[mint] = id
	r.toMint = append(r.toMint, mint)
	return id
}

// GetID returns the ID for a mint, or InvalidTokenID if not found
func (r *TokenRegistry) GetID(mint solana.PublicKey) (TokenID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.toID[mint]
	return id, ok
}

// GetMint returns the mint for an ID
func (r *TokenRegistry) GetMint(id TokenID) solana.PublicKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if int(id) >= len(r.toMint) {
		return solana.PublicKey{}
	}
	return r.toMint[id]
}

// Size returns the number of registered tokens
func (r *TokenRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.toMint)
}

// GetAllMints returns all registered mints (for iteration)
func (r *TokenRegistry) GetAllMints() []solana.PublicKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]solana.PublicKey, len(r.toMint))
	copy(result, r.toMint)
	return result
}
