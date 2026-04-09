package diagnosis

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"slack-issue-bot/internal/llm"
)

// mockConvProvider returns scripted responses in order.
type mockConvProvider struct {
	responses []llm.ChatResponse
	calls     int
}

func (m *mockConvProvider) Name() string { return "mock-conv" }
func (m *mockConvProvider) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	idx := m.calls
	m.calls++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return llm.ChatResponse{Content: `{"summary":"fallback","confidence":"low"}`, StopReason: llm.StopReasonFinish}, nil
}

// triageJSON builds a minimal valid triage JSON string.
func triageJSON(summary, confidence string) string {
	return `{"summary":"` + summary + `","files":[],"suggestions":[],"open_questions":[],"confidence":"` + confidence + `"}`
}

// TestRunLoop_DirectFinish verifies that when the LLM immediately returns a
// triage card, RunLoop completes in 1 call.
func TestRunLoop_DirectFinish(t *testing.T) {
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{
				Content:    triageJSON("login bug in auth module", "high"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "login crashes",
		RepoPath: t.TempDir(),
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
	if resp.Summary != "login bug in auth module" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
	if resp.Confidence != "high" {
		t.Errorf("unexpected confidence: %q", resp.Confidence)
	}
}

// TestRunLoop_ToolCallThenFinish verifies the loop executes a tool call
// then finishes on the next turn. Uses a real git repo for the grep tool.
func TestRunLoop_ToolCallThenFinish(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/auth/login.go": "package auth\n\nfunc Login() {}",
		"src/models/user.go": "package models\n\ntype User struct{}",
	})

	grepArgs, _ := json.Marshal(map[string]string{"pattern": "Login"})

	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: LLM calls grep tool.
			{
				ToolCalls: []llm.ToolCall{
					{ID: "tc_1", Name: "grep", Args: grepArgs},
				},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: LLM returns triage card.
			{
				Content:    triageJSON("login handler found in auth/login.go", "high"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "login crashes",
		RepoPath: repoDir,
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
	if resp.Summary != "login handler found in auth/login.go" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
}

// TestRunLoop_UnknownTool verifies that calling an unknown tool returns an
// error message and the loop continues.
func TestRunLoop_UnknownTool(t *testing.T) {
	unknownArgs, _ := json.Marshal(map[string]string{"query": "test"})

	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: LLM calls nonexistent tool.
			{
				ToolCalls: []llm.ToolCall{
					{ID: "tc_1", Name: "nonexistent_tool", Args: unknownArgs},
				},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: LLM returns triage card after error.
			{
				Content:    triageJSON("could not find related code", "low"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "something broke",
		RepoPath: t.TempDir(),
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
	if resp.Summary != "could not find related code" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
}

// TestRunLoop_ForcedFinish verifies that after MaxTurns of tool calls, the
// loop forces a finish and returns a fallback response.
func TestRunLoop_ForcedFinish(t *testing.T) {
	grepArgs, _ := json.Marshal(map[string]string{"pattern": "test"})

	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: tool call.
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc_1", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: tool call.
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc_2", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 3 (maxTurns): tool call.
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc_3", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 4 (maxTurns+1, extra turn): LLM STILL returns tool call.
			// This should be ignored and the loop should produce a fallback.
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc_4", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
		},
	}

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "something broke",
		RepoPath: t.TempDir(),
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}
	// 3 normal turns + 1 extra turn = 4 calls.
	if mock.calls != 4 {
		t.Errorf("expected 4 calls, got %d", mock.calls)
	}
	if resp.Confidence != "low" {
		t.Errorf("expected confidence 'low' for forced finish, got %q", resp.Confidence)
	}
	if resp.Summary == "" {
		t.Error("expected non-empty summary for fallback")
	}
}

// TestEstimateMessages_WithImages verifies that images add tokensPerImage tokens each.
func TestEstimateMessages_WithImages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello", Images: []llm.ImageContent{
			{Name: "a.png", MimeType: "image/png", Data: make([]byte, 100)},
			{Name: "b.jpg", MimeType: "image/jpeg", Data: make([]byte, 100)},
		}},
	}
	withImages := estimateMessages(msgs)

	msgsNoImg := []llm.Message{
		{Role: "user", Content: "hello"},
	}
	withoutImages := estimateMessages(msgsNoImg)

	expected := withoutImages + 2*1600
	if withImages != expected {
		t.Errorf("expected %d tokens with images, got %d", expected, withImages)
	}
}

// TestEstimateTokens checks the simple rune-based estimator.
func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens("hello"); got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
	// CJK characters are 1 rune each.
	if got := estimateTokens("測試"); got != 2 {
		t.Errorf("expected 2, got %d", got)
	}
}

// capturingProvider wraps another ConversationProvider to record all ChatRequests it receives.
type capturingProvider struct {
	inner    llm.ConversationProvider
	requests *[]llm.ChatRequest
}

func (c *capturingProvider) Name() string { return c.inner.Name() }
func (c *capturingProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	*c.requests = append(*c.requests, req)
	return c.inner.Chat(ctx, req)
}

// TestRunLoop_ImagesOnlyInFirstMessage verifies that images attached to the
// initial user message flow through the agent loop correctly: the first user
// message carries the images in every request (conversation history grows),
// but no other messages (tool_result, subsequent turns) ever have images.
func TestRunLoop_ImagesOnlyInFirstMessage(t *testing.T) {
	grepArgs, _ := json.Marshal(map[string]string{"pattern": "Login"})

	var receivedRequests []llm.ChatRequest
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: tool call.
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc_1", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: triage card.
			{
				Content:    triageJSON("found it", "high"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	capturing := &capturingProvider{inner: mock, requests: &receivedRequests}

	images := []llm.ImageContent{
		{Name: "err.png", MimeType: "image/png", Data: []byte("fakepng")},
	}

	_, err := RunLoop(context.Background(), capturing, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "login crashes",
		Images:   images,
		RepoPath: t.TempDir(),
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}

	if len(receivedRequests) != 2 {
		t.Fatalf("expected 2 Chat calls, got %d", len(receivedRequests))
	}

	// Count messages with images across all requests.
	// Only the original first user message should have images.
	imgCount := 0
	for _, req := range receivedRequests {
		for _, m := range req.Messages {
			if len(m.Images) > 0 {
				imgCount++
			}
		}
	}
	// The first user message appears in both requests (conversation history grows),
	// so we expect it to show up with images in both. But NO other messages should have images.
	// The first user message has images, and it's the same message in both request[0].Messages[0] and request[1].Messages[0].
	// So imgCount should be 2 (same message seen in 2 requests).
	if imgCount != 2 {
		t.Errorf("expected 2 message-with-images occurrences (same first msg in 2 requests), got %d", imgCount)
	}

	// Verify that only user messages at index 0 have images (not tool_result or later messages).
	for i, req := range receivedRequests {
		for j, m := range req.Messages {
			if j == 0 && m.Role == "user" {
				if len(m.Images) == 0 {
					t.Errorf("request %d: first user message should have images", i)
				}
			} else if len(m.Images) > 0 {
				t.Errorf("request %d, message %d (role=%s): should NOT have images", i, j, m.Role)
			}
		}
	}
}
