package diagnosis

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"slack-issue-bot/internal/llm"
)

type cacheEntry struct {
	resp   llm.DiagnoseResponse
	expiry time.Time
}

// Cache is an in-memory response cache with TTL-based expiration.
type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
	done    chan struct{} // signals the cleanup goroutine to stop
}

// NewCache creates a cache with the given TTL and starts a background
// goroutine that evicts expired entries every ttl/2 (min 1s).
func NewCache(ttl time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
		done:    make(chan struct{}),
	}

	interval := ttl / 2
	if interval < time.Second {
		interval = time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.evict()
			case <-c.done:
				return
			}
		}
	}()

	return c
}

// Stop terminates the background cleanup goroutine.
func (c *Cache) Stop() {
	close(c.done)
}

// Key builds a deterministic cache key from the diagnosis parameters.
func (c *Cache) Key(repo, branch, message, language string, extraRules []string) string {
	sorted := make([]string, len(extraRules))
	copy(sorted, extraRules)
	sort.Strings(sorted)

	raw := strings.Join([]string{
		repo,
		branch,
		message,
		language,
		strings.Join(sorted, "|"),
	}, "\x00")

	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}

// Get returns the cached response if present and not expired.
func (c *Cache) Get(key string) (llm.DiagnoseResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok {
		return llm.DiagnoseResponse{}, false
	}
	if time.Now().After(e.expiry) {
		delete(c.entries, key)
		return llm.DiagnoseResponse{}, false
	}
	return e.resp, true
}

// Set stores a response with the configured TTL.
func (c *Cache) Set(key string, resp llm.DiagnoseResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{
		resp:   resp,
		expiry: time.Now().Add(c.ttl),
	}
}

// evict removes all expired entries.
func (c *Cache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expiry) {
			delete(c.entries, k)
		}
	}
}
