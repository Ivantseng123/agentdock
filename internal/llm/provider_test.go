package llm

import (
	"context"
	"errors"
	"testing"
)

type mockProvider struct {
	name  string
	err   error
	resp  DiagnoseResponse
	calls int
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	m.calls++
	return m.resp, m.err
}

func TestFallbackChain_FirstSucceeds(t *testing.T) {
	chain := NewFallbackChain([]ProviderEntry{
		{Provider: &mockProvider{name: "p1", resp: DiagnoseResponse{Summary: "found it"}}, MaxRetries: 1},
		{Provider: &mockProvider{name: "p2", resp: DiagnoseResponse{Summary: "backup"}}, MaxRetries: 1},
	})

	resp, err := chain.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "login broken",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Summary != "found it" {
		t.Errorf("expected 'found it', got %q", resp.Summary)
	}
}

func TestFallbackChain_FallsBackOnError(t *testing.T) {
	chain := NewFallbackChain([]ProviderEntry{
		{Provider: &mockProvider{name: "p1", err: errors.New("timeout")}, MaxRetries: 1},
		{Provider: &mockProvider{name: "p2", resp: DiagnoseResponse{Summary: "backup works"}}, MaxRetries: 1},
	})

	resp, err := chain.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "login broken",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Summary != "backup works" {
		t.Errorf("expected 'backup works', got %q", resp.Summary)
	}
}

func TestFallbackChain_AllFail(t *testing.T) {
	chain := NewFallbackChain([]ProviderEntry{
		{Provider: &mockProvider{name: "p1", err: errors.New("fail1")}, MaxRetries: 2},
		{Provider: &mockProvider{name: "p2", err: errors.New("fail2")}, MaxRetries: 2},
	})

	_, err := chain.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "login broken",
	})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestFallbackChain_RetriesPerProvider(t *testing.T) {
	p1 := &mockProvider{name: "p1", err: errors.New("fail")}
	p2 := &mockProvider{name: "p2", resp: DiagnoseResponse{Summary: "ok"}}

	chain := NewFallbackChain([]ProviderEntry{
		{Provider: p1, MaxRetries: 3},
		{Provider: p2, MaxRetries: 1},
	})

	resp, err := chain.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1.calls != 3 {
		t.Errorf("expected p1 called 3 times, got %d", p1.calls)
	}
	if p2.calls != 1 {
		t.Errorf("expected p2 called 1 time, got %d", p2.calls)
	}
	if resp.Summary != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Summary)
	}
}

func TestFallbackChain_DefaultRetries(t *testing.T) {
	p := &mockProvider{name: "p1", err: errors.New("fail")}
	chain := NewFallbackChain([]ProviderEntry{
		{Provider: p, MaxRetries: 0}, // 0 should default to 1
	})

	chain.Diagnose(context.Background(), DiagnoseRequest{Type: "bug", Message: "test"})
	if p.calls != 1 {
		t.Errorf("expected 1 call (default), got %d", p.calls)
	}
}
