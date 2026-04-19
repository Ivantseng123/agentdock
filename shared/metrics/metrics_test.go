package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRegister_NoPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 1, store)
	defer bundle.Close()
	Register(reg, bundle.Queue, store)

	// Gather should succeed with zero-value metrics.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(families) == 0 {
		t.Fatal("expected at least one metric family after registration")
	}
}

func TestRegister_GaugeFuncWorks(t *testing.T) {
	reg := prometheus.NewRegistry()
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 1, store)
	defer bundle.Close()
	Register(reg, bundle.Queue, store)

	// Submit a job so queue_depth has something to report.
	// Note: the InMemJobQueue dispatch loop moves jobs from the priority
	// queue to the buffered channel very quickly, so QueueDepth() (which
	// reads the priority queue length) may already be 0 by the time we
	// gather. Instead we verify the gauge metric exists and is gatherable.
	err := bundle.Queue.Submit(context.Background(), &queue.Job{
		ID:          "j1",
		Priority:    1,
		SubmittedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	want := map[string]bool{
		"agentdock_queue_depth":   false,
		"agentdock_worker_active": false,
		"agentdock_worker_idle":   false,
	}
	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %q not found in gathered metrics", name)
		}
	}
}

func TestRegister_AllMetricFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 1, store)
	defer bundle.Close()
	Register(reg, bundle.Queue, store)

	// Touch every counter/histogram so they appear in Gather output.
	RequestTotal.WithLabelValues("accepted").Inc()
	RequestDuration.Observe(1)
	QueueSubmittedTotal.WithLabelValues("1").Inc()
	QueueWait.Observe(1)
	QueueJobDuration.WithLabelValues("completed").Observe(1)
	AgentExecution.WithLabelValues("claude").Observe(1)
	AgentExecutionsTotal.WithLabelValues("claude", "success").Inc()
	AgentPrepare.Observe(1)
	AgentToolCalls.WithLabelValues("claude").Observe(1)
	AgentFilesRead.WithLabelValues("claude").Observe(1)
	AgentCostUSD.WithLabelValues("claude").Add(0.01)
	AgentTokensTotal.WithLabelValues("claude", "input").Add(100)
	IssueCreatedTotal.WithLabelValues("high", "false").Inc()
	IssueRejectedTotal.WithLabelValues("wrong_repo").Inc()
	IssueRetryTotal.WithLabelValues("submitted").Inc()
	HandlerDedupRejectionsTotal.Inc()
	HandlerRateLimitTotal.WithLabelValues("user").Inc()
	WatchdogKillsTotal.WithLabelValues("timeout").Inc()
	ExternalDuration.WithLabelValues("slack", "post_message").Observe(0.5)
	ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Build a set of gathered metric names.
	gathered := make(map[string]bool, len(families))
	for _, mf := range families {
		gathered[mf.GetName()] = true
	}

	// All 23 expected metric names.
	expected := []string{
		"agentdock_request_total",
		"agentdock_request_duration_seconds",
		"agentdock_queue_depth",
		"agentdock_queue_submitted_total",
		"agentdock_queue_wait_seconds",
		"agentdock_queue_job_duration_seconds",
		"agentdock_agent_execution_seconds",
		"agentdock_agent_executions_total",
		"agentdock_agent_prepare_seconds",
		"agentdock_agent_tool_calls",
		"agentdock_agent_files_read",
		"agentdock_agent_cost_usd",
		"agentdock_agent_tokens_total",
		"agentdock_issue_created_total",
		"agentdock_issue_rejected_total",
		"agentdock_issue_retry_total",
		"agentdock_handler_dedup_rejections_total",
		"agentdock_handler_rate_limit_total",
		"agentdock_watchdog_kills_total",
		"agentdock_external_duration_seconds",
		"agentdock_external_errors_total",
		"agentdock_worker_active",
		"agentdock_worker_idle",
	}
	for _, name := range expected {
		if !gathered[name] {
			t.Errorf("missing metric: %s", name)
		}
	}
	if t.Failed() {
		t.Logf("gathered metrics:")
		for name := range gathered {
			t.Logf("  %s", name)
		}
	}
}

func TestRegister_NilDeps(t *testing.T) {
	// Passing nil for q and store should not panic — only static
	// collectors are registered, no GaugeFuncs.
	reg := prometheus.NewRegistry()
	Register(reg, nil, nil)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Should have the 20 static collectors but not the 3 GaugeFuncs.
	for _, mf := range families {
		switch mf.GetName() {
		case "agentdock_queue_depth", "agentdock_worker_active", "agentdock_worker_idle":
			t.Errorf("GaugeFunc %q should not be registered when q is nil", mf.GetName())
		}
	}
}
