package github

import "sync"

const defaultDedupeSize = 1000

// Tracks recently seen GitHub delivery IDs to prevent a webhook from being
// processed more than once.
type DedupeCache struct {
	mutex    sync.Mutex
	seen     map[string]struct{}
	ring     []string // circular buffer of delivery IDs in insertion order
	head     int      // next write position in ring
	capacity int
}

// Creates a cache that remembers the last capacity delivery IDs.
func NewDedupeCache(capacity int) *DedupeCache {
	if capacity <= 0 {
		capacity = defaultDedupeSize
	}
	return &DedupeCache{
		seen:     make(map[string]struct{}, capacity),
		ring:     make([]string, capacity),
		capacity: capacity,
	}
}

func (cache *DedupeCache) Seen(id string) bool {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	if _, ok := cache.seen[id]; ok {
		return true
	}

	if old := cache.ring[cache.head]; old != "" {
		delete(cache.seen, old)
	}
	cache.ring[cache.head] = id
	cache.seen[id] = struct{}{}
	cache.head = (cache.head + 1) % cache.capacity
	return false
}
