package logging

import (
	"crypto/rand"
	"fmt"
	"time"
)

// NewRequestID generates a short, time-based request ID: YYYYMMDD-HHmmss-xxxxxxxx.
// 32 bits of random suffix — previous 16-bit version flaked in tight loops
// (100 IDs in one second → ~7% collision on 65k slots).
func NewRequestID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%s-%08x", time.Now().Format("20060102-150405"), b)
}
