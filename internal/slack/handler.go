package slack

import (
	"fmt"
	"sync"
	"time"
)

// TriggerEvent represents an @bot mention or /triage command.
type TriggerEvent struct {
	ChannelID string
	ThreadTS  string
	TriggerTS string
	UserID    string
	Text      string
}

type HandlerConfig struct {
	MaxConcurrent   int
	DedupTTL        time.Duration
	PerUserLimit    int
	PerChannelLimit int
	RateWindow      time.Duration
	OnEvent         func(event TriggerEvent)
	OnRejected      func(event TriggerEvent, reason string)
}

type Handler struct {
	threadDedup  *threadDedup
	userLimit    *rateLimiter
	channelLimit *rateLimiter
	onEvent      func(event TriggerEvent)
	onRejected   func(event TriggerEvent, reason string)
}

func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.DedupTTL <= 0 {
		cfg.DedupTTL = 5 * time.Minute
	}
	return &Handler{
		threadDedup:  newThreadDedup(cfg.DedupTTL),
		userLimit:    newRateLimiter(cfg.PerUserLimit, cfg.RateWindow),
		channelLimit: newRateLimiter(cfg.PerChannelLimit, cfg.RateWindow),
		onEvent:      cfg.OnEvent,
		onRejected:   cfg.OnRejected,
	}
}

func (h *Handler) HandleTrigger(event TriggerEvent) bool {
	if h.threadDedup.isDuplicate(event.ChannelID, event.ThreadTS) {
		return false
	}
	if !h.userLimit.allow(event.UserID) {
		if h.onRejected != nil {
			h.onRejected(event, "rate limit exceeded")
		}
		return false
	}
	if !h.channelLimit.allow(event.ChannelID) {
		if h.onRejected != nil {
			h.onRejected(event, "channel rate limit exceeded")
		}
		return false
	}
	go h.onEvent(event)
	return true
}

func (h *Handler) ClearThreadDedup(channelID, threadTS string) {
	h.threadDedup.Remove(channelID, threadTS)
}

// --- Thread dedup ---

type threadDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newThreadDedup(ttl time.Duration) *threadDedup {
	d := &threadDedup{seen: make(map[string]time.Time), ttl: ttl}
	go d.cleanup()
	return d
}

func (d *threadDedup) isDuplicate(channelID, threadTS string) bool {
	key := fmt.Sprintf("%s:%s", channelID, threadTS)
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[key]; ok && time.Since(t) < d.ttl {
		return true
	}
	d.seen[key] = time.Now()
	return false
}

func (d *threadDedup) Remove(channelID, threadTS string) {
	key := fmt.Sprintf("%s:%s", channelID, threadTS)
	d.mu.Lock()
	delete(d.seen, key)
	d.mu.Unlock()
}

func (d *threadDedup) cleanup() {
	ticker := time.NewTicker(d.ttl)
	for range ticker.C {
		d.mu.Lock()
		for k, t := range d.seen {
			if time.Since(t) >= d.ttl {
				delete(d.seen, k)
			}
		}
		d.mu.Unlock()
	}
}

// --- Event-level dedup (for Socket Mode) ---

type dedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newDedup(ttl time.Duration) *dedup {
	d := &dedup{seen: make(map[string]time.Time), ttl: ttl}
	go d.cleanup()
	return d
}

func (d *dedup) isDuplicate(eventID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[eventID]; ok && time.Since(t) < d.ttl {
		return true
	}
	d.seen[eventID] = time.Now()
	return false
}

func (d *dedup) cleanup() {
	ticker := time.NewTicker(d.ttl)
	for range ticker.C {
		d.mu.Lock()
		for k, t := range d.seen {
			if time.Since(t) >= d.ttl {
				delete(d.seen, k)
			}
		}
		d.mu.Unlock()
	}
}

// --- Rate limiter ---

type rateLimiter struct {
	mu     sync.Mutex
	counts map[string][]time.Time
	limit  int
	window time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		counts: make(map[string][]time.Time),
		limit:  limit,
		window: window,
	}
}

func (r *rateLimiter) allow(key string) bool {
	if r.limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	var valid []time.Time
	for _, t := range r.counts[key] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= r.limit {
		r.counts[key] = valid
		return false
	}

	r.counts[key] = append(valid, now)
	return true
}
