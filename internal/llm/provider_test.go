package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type mockChatProvider struct {
	name  string
	err   error
	resp  ChatResponse
	calls int
}

func (m *mockChatProvider) Name() string { return m.name }
func (m *mockChatProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	m.calls++
	return m.resp, m.err
}

func TestChatFallbackChain_FirstSucceeds(t *testing.T) {
	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: &mockChatProvider{name: "p1", resp: ChatResponse{Content: "found it", StopReason: StopReasonFinish}}, MaxRetries: 1},
		{Provider: &mockChatProvider{name: "p2", resp: ChatResponse{Content: "backup", StopReason: StopReasonFinish}}, MaxRetries: 1},
	})

	resp, err := chain.Chat(context.Background(), ChatRequest{
		SystemPrompt: "you are a helper",
		Messages:     []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "found it" {
		t.Errorf("expected 'found it', got %q", resp.Content)
	}
	if resp.StopReason != StopReasonFinish {
		t.Errorf("expected stop reason %q, got %q", StopReasonFinish, resp.StopReason)
	}
}

func TestChatFallbackChain_FallsBack(t *testing.T) {
	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: &mockChatProvider{name: "p1", err: errors.New("timeout")}, MaxRetries: 1},
		{Provider: &mockChatProvider{name: "p2", resp: ChatResponse{Content: "backup works", StopReason: StopReasonFinish}}, MaxRetries: 1},
	})

	resp, err := chain.Chat(context.Background(), ChatRequest{
		SystemPrompt: "you are a helper",
		Messages:     []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "backup works" {
		t.Errorf("expected 'backup works', got %q", resp.Content)
	}
}

func TestChatFallbackChain_AllFail(t *testing.T) {
	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: &mockChatProvider{name: "p1", err: errors.New("fail1")}, MaxRetries: 2},
		{Provider: &mockChatProvider{name: "p2", err: errors.New("fail2")}, MaxRetries: 2},
	})

	_, err := chain.Chat(context.Background(), ChatRequest{
		SystemPrompt: "you are a helper",
		Messages:     []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "all chat providers failed") {
		t.Errorf("error should mention all providers failed, got: %v", err)
	}
}

func TestChatFallbackChain_RetriesPerProvider(t *testing.T) {
	p1 := &mockChatProvider{name: "p1", err: errors.New("fail")}
	p2 := &mockChatProvider{name: "p2", resp: ChatResponse{Content: "ok", StopReason: StopReasonFinish}}

	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: p1, MaxRetries: 3},
		{Provider: p2, MaxRetries: 1},
	})

	resp, err := chain.Chat(context.Background(), ChatRequest{
		SystemPrompt: "you are a helper",
		Messages:     []Message{{Role: "user", Content: "test"}},
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
	if resp.Content != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Content)
	}
}

func TestChatFallbackChain_DefaultRetries(t *testing.T) {
	p := &mockChatProvider{name: "p1", err: errors.New("fail")}
	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: p, MaxRetries: 0}, // 0 should default to 1
	})

	chain.Chat(context.Background(), ChatRequest{
		SystemPrompt: "you are a helper",
		Messages:     []Message{{Role: "user", Content: "test"}},
	})
	if p.calls != 1 {
		t.Errorf("expected 1 call (default), got %d", p.calls)
	}
}

