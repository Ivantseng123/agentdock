package workflow

// Tests verifying that parse-failure log lines redact known secret values.
// Each test builds a minimal workflow with a fake secret in cfg.Secrets,
// calls HandleResult with an output that contains the raw secret value, and
// asserts that the captured log line contains "***" but NOT the raw value.
// A companion regression test confirms that ordinary output (no secret) is
// preserved byte-for-byte.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// captureLogger returns a *slog.Logger that writes text to buf, plus the buf
// itself. The text handler emits key=value pairs that we can grep for.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func cfgWithSecret(secret string) *config.Config {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.Secrets = map[string]string{"MY_SECRET": secret}
	return cfg
}

// ── IssueWorkflow ─────────────────────────────────────────────────────────────

func TestIssueHandleResult_ParseFail_RedactsSecret(t *testing.T) {
	const fakeSecret = "supersecret-ghp-abc123"
	cfg := cfgWithSecret(fakeSecret)
	var buf bytes.Buffer
	w := NewIssueWorkflow(cfg, newFakeSlackPort(), &fakeIssueCreator{}, nil, nil, captureLogger(&buf))

	state := &queue.JobState{Job: &queue.Job{TaskType: "issue"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: `{"bad json containing ` + fakeSecret + ` inside"}`,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("log line contains raw secret %q: %s", fakeSecret, logged)
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("log line should contain '***' redaction placeholder: %s", logged)
	}
}

func TestIssueHandleResult_ParseFail_NoSecret_Unchanged(t *testing.T) {
	cfg := cfgWithSecret("supersecret-ghp-abc123")
	var buf bytes.Buffer
	w := NewIssueWorkflow(cfg, newFakeSlackPort(), &fakeIssueCreator{}, nil, nil, captureLogger(&buf))

	const harmlessOutput = "not valid json at all - totally normal debug text"
	state := &queue.JobState{Job: &queue.Job{TaskType: "issue"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: harmlessOutput,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if !strings.Contains(logged, harmlessOutput) {
		t.Errorf("harmless output should appear unchanged in log; logged: %s", logged)
	}
}

// ── AskWorkflow ───────────────────────────────────────────────────────────────

func TestAskHandleResult_ParseFail_RedactsSecret(t *testing.T) {
	// Under the 2026-04-26 fallback extension, ask's parse-fail log only
	// fires for stdout that fails the syntactic gate (truly empty / below
	// askFallbackMinLength = 10 runes). Driver therefore uses a short raw
	// output that equals the secret. The secret length must be >= the
	// shared/logging minRedactLength (6 bytes) AND the whole stdout must
	// remain under 10 runes; "ASKXYZ" (6 bytes) satisfies both.
	const fakeSecret = "ASKXYZ"
	cfg := cfgWithSecret(fakeSecret)
	var buf bytes.Buffer
	w := NewAskWorkflow(cfg, newFakeSlackPort(), nil, captureLogger(&buf))

	state := &queue.JobState{Job: &queue.Job{TaskType: "ask"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: fakeSecret,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("log line contains raw secret %q: %s", fakeSecret, logged)
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("log line should contain '***' redaction placeholder: %s", logged)
	}
}

func TestAskHandleResult_ParseFail_NoSecret_Unchanged(t *testing.T) {
	cfg := cfgWithSecret("ASKXYZ")
	var buf bytes.Buffer
	w := NewAskWorkflow(cfg, newFakeSlackPort(), nil, captureLogger(&buf))

	// Short stdout with no secret. Same gate constraint as the redaction test
	// above: the parse-fail path is now reserved for sub-min-length stdout.
	const harmlessOutput = "harmless"
	state := &queue.JobState{Job: &queue.Job{TaskType: "ask"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: harmlessOutput,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if !strings.Contains(logged, harmlessOutput) {
		t.Errorf("harmless output should appear unchanged in log; logged: %s", logged)
	}
}

// cfgWithSecretPRReview is like cfgWithSecret but also enables PRReview.
func cfgWithSecretPRReview(secret string) *config.Config {
	cfg := cfgWithSecret(secret)
	tp := true
	cfg.Workflows.PRReview.Enabled = &tp
	return cfg
}

// ── PRReviewWorkflow ──────────────────────────────────────────────────────────

func TestPRReviewHandleResult_ParseFail_RedactsSecret(t *testing.T) {
	const fakeSecret = "supersecret-pr-review-qwe456"
	cfg := cfgWithSecretPRReview(fakeSecret)

	var buf bytes.Buffer
	w := NewPRReviewWorkflow(cfg, newFakeSlackPort(), &fakeGitHubPR{}, nil, captureLogger(&buf))

	state := &queue.JobState{Job: &queue.Job{TaskType: "pr_review", WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/1"}}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: `malformed output leaking ` + fakeSecret + ` in it`,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("log line contains raw secret %q: %s", fakeSecret, logged)
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("log line should contain '***' redaction placeholder: %s", logged)
	}
}

// TestPRReviewHandleResult_ParseFail_RedactsSecretPastTruncation pins the
// redact-before-truncate ordering: a secret that appears only after byte 2000
// still gets redacted because firstN runs on the already-redacted string.
func TestPRReviewHandleResult_ParseFail_RedactsSecretPastTruncation(t *testing.T) {
	const fakeSecret = "supersecret-pr-review-late-zzz987"
	cfg := cfgWithSecretPRReview(fakeSecret)

	var buf bytes.Buffer
	w := NewPRReviewWorkflow(cfg, newFakeSlackPort(), &fakeGitHubPR{}, nil, captureLogger(&buf))

	padding := strings.Repeat("a", 2500) // push secret past the 2000-byte cut
	state := &queue.JobState{Job: &queue.Job{TaskType: "pr_review", WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/1"}}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: padding + fakeSecret + padding,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("secret past the 2000-byte boundary leaked through truncation: redact ordering is wrong")
	}
}

func TestPRReviewHandleResult_ParseFail_NoSecret_Unchanged(t *testing.T) {
	cfg := cfgWithSecretPRReview("supersecret-pr-review-qwe456")

	var buf bytes.Buffer
	w := NewPRReviewWorkflow(cfg, newFakeSlackPort(), &fakeGitHubPR{}, nil, captureLogger(&buf))

	const harmlessOutput = "plain output string with no secrets inside"
	state := &queue.JobState{Job: &queue.Job{TaskType: "pr_review", WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/1"}}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: harmlessOutput,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if !strings.Contains(logged, harmlessOutput) {
		t.Errorf("harmless output should appear unchanged in log; logged: %s", logged)
	}
}
