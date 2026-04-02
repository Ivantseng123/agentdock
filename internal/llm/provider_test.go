package llm

import (
	"context"
	"errors"
	"testing"
)

type mockProvider struct {
	name string
	err  error
	resp DiagnoseResponse
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	return m.resp, m.err
}

func TestFallbackChain_FirstSucceeds(t *testing.T) {
	chain := NewFallbackChain([]Provider{
		&mockProvider{name: "p1", resp: DiagnoseResponse{Summary: "found it"}},
		&mockProvider{name: "p2", resp: DiagnoseResponse{Summary: "backup"}},
	}, 1)

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
	chain := NewFallbackChain([]Provider{
		&mockProvider{name: "p1", err: errors.New("timeout")},
		&mockProvider{name: "p2", resp: DiagnoseResponse{Summary: "backup works"}},
	}, 1)

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
	chain := NewFallbackChain([]Provider{
		&mockProvider{name: "p1", err: errors.New("fail1")},
		&mockProvider{name: "p2", err: errors.New("fail2")},
	}, 2)

	_, err := chain.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "login broken",
	})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}
