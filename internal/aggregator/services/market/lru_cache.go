package market

import (
	"container/list"
	"sync"
)

// BoundedLRUCache is a thread-safe bounded LRU cache with generic key-value types
type BoundedLRUCache[K comparable, V any] struct {
	mu       sync.RWMutex
	cache    map[K]*list.Element
	lru      *list.List
	maxSize  int
	zeroVal  V
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

// NewBoundedLRUCache creates a new bounded LRU cache
func NewBoundedLRUCache[K comparable, V any](maxSize int) *BoundedLRUCache[K, V] {
	return &BoundedLRUCache[K, V]{
		cache:   make(map[K]*list.Element, maxSize),
		lru:     list.New(),
		maxSize: maxSize,
	}
}

// Get retrieves a value from the cache and moves it to front (most recently used)
func (c *BoundedLRUCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	elem, ok := c.cache[key]
	c.mu.RUnlock()

	if !ok {
		return c.zeroVal, false
	}

	// Promote to front (requires write lock)
	c.mu.Lock()
	// Re-check after acquiring write lock
	elem, ok = c.cache[key]
	if ok {
		c.lru.MoveToFront(elem)
		entry := elem.Value.(*lruEntry[K, V])
		c.mu.Unlock()
		return entry.value, true
	}
	c.mu.Unlock()
	return c.zeroVal, false
}

// GetWithoutPromote retrieves a value without affecting LRU order (faster for read-heavy workloads)
func (c *BoundedLRUCache[K, V]) GetWithoutPromote(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	elem, ok := c.cache[key]
	if !ok {
		return c.zeroVal, false
	}
	entry := elem.Value.(*lruEntry[K, V])
	return entry.value, true
}

// Set adds or updates a value in the cache
func (c *BoundedLRUCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if elem, ok := c.cache[key]; ok {
		c.lru.MoveToFront(elem)
		entry := elem.Value.(*lruEntry[K, V])
		entry.value = value
		return
	}

	// Evict LRU entries if at capacity
	for len(c.cache) >= c.maxSize {
		c.evictLRU()
	}

	// Add new entry
	entry := &lruEntry[K, V]{key: key, value: value}
	elem := c.lru.PushFront(entry)
	c.cache[key] = elem
}

// evictLRU removes the least recently used entry
// Must be called with mu held
func (c *BoundedLRUCache[K, V]) evictLRU() {
	back := c.lru.Back()
	if back == nil {
		return
	}
	entry := back.Value.(*lruEntry[K, V])
	c.lru.Remove(back)
	delete(c.cache, entry.key)
}

// Size returns current cache size
func (c *BoundedLRUCache[K, V]) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// Contains checks if a key exists without affecting LRU order
func (c *BoundedLRUCache[K, V]) Contains(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.cache[key]
	return ok
}

// Clear removes all entries from the cache
func (c *BoundedLRUCache[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[K]*list.Element, c.maxSize)
	c.lru.Init()
}
