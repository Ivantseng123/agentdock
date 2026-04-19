package slack

import (
	"sync"
	"testing"
	"time"
)

func TestDedup_FirstEventPasses(t *testing.T) {
	d := newDedup(time.Minute)
	if d.isDuplicate("evt1") {
		t.Error("first event should not be duplicate")
	}
}

func TestDedup_SecondEventBlocked(t *testing.T) {
	d := newDedup(time.Minute)
	d.isDuplicate("evt1")
	if !d.isDuplicate("evt1") {
		t.Error("second event should be duplicate")
	}
}

func TestDedup_ExpiredEventPasses(t *testing.T) {
	d := newDedup(10 * time.Millisecond)
	d.isDuplicate("evt1")
	time.Sleep(20 * time.Millisecond)
	if d.isDuplicate("evt1") {
		t.Error("expired event should not be duplicate")
	}
}

func TestThreadDedup_SameThreadBlocked(t *testing.T) {
	d := newThreadDedup(time.Minute)
	if d.isDuplicate("C1", "T1") {
		t.Error("first trigger should not be duplicate")
	}
	if !d.isDuplicate("C1", "T1") {
		t.Error("second trigger on same thread should be duplicate")
	}
}

func TestThreadDedup_DifferentThreadAllowed(t *testing.T) {
	d := newThreadDedup(time.Minute)
	d.isDuplicate("C1", "T1")
	if d.isDuplicate("C1", "T2") {
		t.Error("different thread should not be duplicate")
	}
}

func TestThreadDedup_ClearAllowsRetrigger(t *testing.T) {
	d := newThreadDedup(time.Minute)
	d.isDuplicate("C1", "T1")
	d.Remove("C1", "T1")
	if d.isDuplicate("C1", "T1") {
		t.Error("cleared thread should allow re-trigger")
	}
}

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	r := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !r.allow("user1") {
			t.Errorf("request %d should be allowed", i)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	r := newRateLimiter(2, time.Minute)
	r.allow("user1")
	r.allow("user1")
	if r.allow("user1") {
		t.Error("third request should be blocked")
	}
}

func TestRateLimiter_NilDisabled(t *testing.T) {
	r := newRateLimiter(0, 0)
	if !r.allow("user1") {
		t.Error("disabled limiter should always allow")
	}
}

func TestHandler_DedupBlocksDuplicate(t *testing.T) {
	var count int
	var mu sync.Mutex
	h := NewHandler(HandlerConfig{
		MaxConcurrent: 5,
		DedupTTL:      time.Minute,
		OnEvent: func(e TriggerEvent) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	})

	e := TriggerEvent{ChannelID: "C1", ThreadTS: "T1", UserID: "U1", TriggerTS: "T1.1"}

	h.HandleTrigger(e)
	h.HandleTrigger(e)

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestHandler_RateLimitBlocksExcess(t *testing.T) {
	rejected := false
	h := NewHandler(HandlerConfig{
		MaxConcurrent: 5,
		DedupTTL:      time.Minute,
		PerUserLimit:  1,
		RateWindow:    time.Minute,
		OnEvent:       func(e TriggerEvent) {},
		OnRejected:    func(e TriggerEvent, reason string) { rejected = true },
	})

	h.HandleTrigger(TriggerEvent{ChannelID: "C1", ThreadTS: "T1", UserID: "U1", TriggerTS: "T1.1"})
	h.HandleTrigger(TriggerEvent{ChannelID: "C2", ThreadTS: "T2", UserID: "U1", TriggerTS: "T2.1"})

	time.Sleep(50 * time.Millisecond)
	if !rejected {
		t.Error("second trigger should be rate-limited")
	}
}
