package queue

import (
	"testing"
	"time"
)

// TestReceiveBackoffDuration pins the escalation curve used by RedisJobQueue.Receive
// when XReadGroup returns a non-nil, non-redis.Nil error. Keeping this deterministic
// (no jitter) means production logs can be read alongside these values to verify the
// loop is actually backing off as designed.
func TestReceiveBackoffDuration(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want time.Duration
	}{
		{"non-positive returns zero", 0, 0},
		{"negative returns zero", -3, 0},
		{"first error", 1, 1 * time.Second},
		{"second error doubles", 2, 2 * time.Second},
		{"third error doubles", 3, 4 * time.Second},
		{"fourth error doubles", 4, 8 * time.Second},
		{"fifth error doubles", 5, 16 * time.Second},
		{"sixth error caps at max", 6, 30 * time.Second},
		{"tenth error still capped", 10, 30 * time.Second},
		{"huge count stays capped", 1_000_000, 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := receiveBackoffDuration(c.n)
			if got != c.want {
				t.Errorf("receiveBackoffDuration(%d) = %v, want %v", c.n, got, c.want)
			}
		})
	}
}
