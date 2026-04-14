package main

import "testing"

func TestVersionDefaults(t *testing.T) {
	// When ldflags are not injected, defaults must be stable strings.
	if version != "dev" {
		t.Errorf("version default = %q, want %q", version, "dev")
	}
	if commit != "unknown" {
		t.Errorf("commit default = %q, want %q", commit, "unknown")
	}
	if date != "unknown" {
		t.Errorf("date default = %q, want %q", date, "unknown")
	}
}
