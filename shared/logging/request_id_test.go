package logging

import (
	"regexp"
	"testing"
)

func TestNewRequestID_Format(t *testing.T) {
	id := NewRequestID()
	// Expected: YYYYMMDD-HHmmss-xxxxxxxx
	pattern := `^\d{8}-\d{6}-[0-9a-f]{8}$`
	matched, _ := regexp.MatchString(pattern, id)
	if !matched {
		t.Errorf("request ID %q does not match pattern %s", id, pattern)
	}
}

func TestNewRequestID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := NewRequestID()
		if seen[id] {
			t.Fatalf("duplicate ID: %s", id)
		}
		seen[id] = true
	}
}
