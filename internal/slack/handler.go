package slack

import (
	"log/slog"
	"sync"
	"time"
)

type dedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newDedup(ttl time.Duration) *dedup {
	d := &dedup{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
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
	defer ticker.Stop()
	for range ticker.C {
		d.mu.Lock()
		now := time.Now()
		for id, t := range d.seen {
			if now.Sub(t) > d.ttl {
				delete(d.seen, id)
			}
		}
		d.mu.Unlock()
	}
}

type ReactionEvent struct {
	EventID   string
	Reaction  string
	ChannelID string
	MessageTS string
	UserID    string
}

// rateLimiter tracks event counts per key within a sliding window.
type rateLimiter struct {
	mu     sync.Mutex
	counts map[string][]time.Time
	limit  int
	window time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	if limit <= 0 || window <= 0 {
		return nil // disabled
	}
	return &rateLimiter{
		counts: make(map[string][]time.Time),
		limit:  limit,
		window: window,
	}
}

func (r *rateLimiter) allow(key string) bool {
	if r == nil {
		return true // disabled
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	// Remove expired entries
	valid := r.counts[key][:0]
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

// messageDedup prevents the same message from being processed multiple times
// (different emojis on the same message).
type messageDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newMessageDedup(ttl time.Duration) *messageDedup {
	d := &messageDedup{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
	go func() {
		ticker := time.NewTicker(ttl)
		defer ticker.Stop()
		for range ticker.C {
			d.mu.Lock()
			now := time.Now()
			for k, t := range d.seen {
				if now.Sub(t) > d.ttl {
					delete(d.seen, k)
				}
			}
			d.mu.Unlock()
		}
	}()
	return d
}

func (d *messageDedup) isDuplicate(channelID, messageTS string) bool {
	key := channelID + ":" + messageTS
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[key]; ok && time.Since(t) < d.ttl {
		return true
	}
	d.seen[key] = time.Now()
	return false
}

// Remove clears the dedup entry so the same message can be re-triggered.
func (d *messageDedup) Remove(channelID, messageTS string) {
	key := channelID + ":" + messageTS
	d.mu.Lock()
	delete(d.seen, key)
	d.mu.Unlock()
}

// MessageDedup exposes the messageDedup for external clearing (e.g. on timeout).
func (h *Handler) ClearMessageDedup(channelID, messageTS string) {
	h.messageDedup.Remove(channelID, messageTS)
}

type Handler struct {
	dedup        *dedup
	messageDedup *messageDedup
	userLimit    *rateLimiter
	channelLimit *rateLimiter
	semaphore    chan struct{}
	onEvent      func(event ReactionEvent)
	onRejected   func(event ReactionEvent, reason string) // callback when rate limited
}

type HandlerConfig struct {
	MaxConcurrent   int
	DedupTTL        time.Duration
	PerUserLimit    int
	PerChannelLimit int
	RateWindow      time.Duration
	OnEvent         func(event ReactionEvent)
	OnRejected      func(event ReactionEvent, reason string)
}

func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		dedup:        newDedup(cfg.DedupTTL),
		messageDedup: newMessageDedup(cfg.DedupTTL),
		userLimit:    newRateLimiter(cfg.PerUserLimit, cfg.RateWindow),
		channelLimit: newRateLimiter(cfg.PerChannelLimit, cfg.RateWindow),
		semaphore:    make(chan struct{}, cfg.MaxConcurrent),
		onEvent:      cfg.OnEvent,
		onRejected:   cfg.OnRejected,
	}
}

func (h *Handler) HandleReaction(event ReactionEvent) bool {
	if h.dedup.isDuplicate(event.EventID) {
		slog.Debug("skipping duplicate event", "eventID", event.EventID)
		return false
	}

	if h.messageDedup.isDuplicate(event.ChannelID, event.MessageTS) {
		slog.Debug("skipping already-processed message", "channel", event.ChannelID, "ts", event.MessageTS)
		return false
	}

	if !h.userLimit.allow(event.UserID) {
		slog.Warn("rate limit hit for user", "userID", event.UserID)
		if h.onRejected != nil {
			h.onRejected(event, "rate limit exceeded for user")
		}
		return false
	}

	if !h.channelLimit.allow(event.ChannelID) {
		slog.Warn("rate limit hit for channel", "channelID", event.ChannelID)
		if h.onRejected != nil {
			h.onRejected(event, "rate limit exceeded for channel")
		}
		return false
	}

	h.semaphore <- struct{}{}
	go func() {
		defer func() { <-h.semaphore }()
		h.onEvent(event)
	}()
	return true
}
