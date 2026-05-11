package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRegister_NoPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 1, store)
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
	bundle := queuetest.NewBundle(10, 1, store)
	defer bundle.Close()
	Register(reg, bundle.Queue, store)

	// Submit a job so queue_depth has something to report.
	// Note: the queuetest JobQueue dispatch loop moves jobs from the priority
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
	bundle := queuetest.NewBundle(10, 1, store)
	defer bundle.Close()
	Register(reg, bundle.Queue, store)

	// Touch every counter/histogram so they appear in Gather output.
	RequestTotal.WithLabelValues("accepted").Inc()
	RequestDuration.Observe(1)
	QueueSubmittedTotal.WithLabelValues("1").Inc()
	QueueWait.Observe(1)
	QueueJobDuration.WithLabelValues("issue", "completed").Observe(1)
	AgentExecution.WithLabelValues("claude").Observe(1)
	AgentExecutionsTotal.WithLabelValues("claude", "issue", "success").Inc()
	AgentExitCodeTotal.WithLabelValues("claude", "0").Inc()
	AgentPrepare.Observe(1)
	AgentToolCalls.WithLabelValues("claude").Observe(1)
	AgentFilesRead.WithLabelValues("claude").Observe(1)
	AgentCostUSD.WithLabelValues("claude").Add(0.01)
	AgentTokensTotal.WithLabelValues("claude", "input").Add(100)
	WorkflowCompletionsTotal.WithLabelValues("issue", "success").Inc()
	WorkflowRetryTotal.WithLabelValues("issue", "exhausted").Inc()
	HandlerDedupRejectionsTotal.Inc()
	HandlerRateLimitTotal.WithLabelValues("user").Inc()
	SlackEventsTotal.WithLabelValues("app_mention").Inc()
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

	// All 22 expected metric names.
	expected := []string{
		"agentdock_request_total",
		"agentdock_request_duration_seconds",
		"agentdock_queue_depth",
		"agentdock_queue_submitted_total",
		"agentdock_queue_wait_seconds",
		"agentdock_queue_job_duration_seconds",
		"agentdock_agent_execution_seconds",
		"agentdock_agent_executions_total",
		"agentdock_agent_exit_code_total",
		"agentdock_agent_prepare_seconds",
		"agentdock_agent_tool_calls",
		"agentdock_agent_files_read",
		"agentdock_agent_cost_usd",
		"agentdock_agent_tokens_total",
		"agentdock_workflow_completions_total",
		"agentdock_workflow_retry_total",
		"agentdock_handler_dedup_rejections_total",
		"agentdock_handler_rate_limit_total",
		"agentdock_slack_events_total",
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

func TestWorkflowCompletionsTotal_Registered(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg, nil, nil)
	// WithLabelValues panics if the metric isn't registered with this registry.
	// We use a fresh registry here, so we just verify the var exists and is touchable.
	WorkflowCompletionsTotal.WithLabelValues("issue", "success").Inc()
}

func TestWorkflowRetryTotal_Registered(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg, nil, nil)
	WorkflowRetryTotal.WithLabelValues("issue", "exhausted").Inc()
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
	// Should have the static collectors but not the 3 GaugeFuncs.
	for _, mf := range families {
		switch mf.GetName() {
		case "agentdock_queue_depth", "agentdock_worker_active", "agentdock_worker_idle":
			t.Errorf("GaugeFunc %q should not be registered when q is nil", mf.GetName())
		}
	}
}

// gatherCounterSeries returns {labelKey: value} for every series of the named
// counter family in reg, where labelKey is "name1=v1,name2=v2,..." with labels
// in the order Prometheus emits them (sorted). Returns nil if the family is
// absent or has no samples.
func gatherCounterSeries(t *testing.T, reg *prometheus.Registry, family string) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != family {
			continue
		}
		out := make(map[string]float64, len(mf.GetMetric()))
		for _, m := range mf.GetMetric() {
			parts := make([]string, 0, len(m.GetLabel()))
			for _, lp := range m.GetLabel() {
				parts = append(parts, lp.GetName()+"="+lp.GetValue())
			}
			out[strings.Join(parts, ",")] = m.GetCounter().GetValue()
		}
		return out
	}
	return nil
}

func TestAgentExitCodeTotal(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg, nil, nil)

	// Use a provider name no other test touches — the metric var is a
	// process-global CounterVec, so colliding label sets would accumulate
	// across tests and break the exact-count assertions below.
	AgentExitCodeTotal.WithLabelValues("gemini", "0").Inc()
	AgentExitCodeTotal.WithLabelValues("gemini", "137").Inc()
	AgentExitCodeTotal.WithLabelValues("gemini", "137").Inc()
	AgentExitCodeTotal.WithLabelValues("gemini", "124").Inc()

	got := gatherCounterSeries(t, reg, "agentdock_agent_exit_code_total")
	want := map[string]float64{
		"exit_code=0,provider=gemini":   1,
		"exit_code=137,provider=gemini": 2,
		"exit_code=124,provider=gemini": 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("series %q = %v, want %v (full: %v)", k, got[k], v, got)
		}
	}
}

func TestSlackEventsTotal(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg, nil, nil)

	SlackEventsTotal.WithLabelValues("slash_command").Inc()
	SlackEventsTotal.WithLabelValues("block_action").Inc()
	SlackEventsTotal.WithLabelValues("block_action").Inc()
	SlackEventsTotal.WithLabelValues("block_action").Inc()

	got := gatherCounterSeries(t, reg, "agentdock_slack_events_total")
	if got["type=slash_command"] != 1 {
		t.Errorf("slash_command = %v, want 1 (full: %v)", got["type=slash_command"], got)
	}
	if got["type=block_action"] != 3 {
		t.Errorf("block_action = %v, want 3 (full: %v)", got["type=block_action"], got)
	}
}

// parseDesc extracts the fqName and variable label names from a Desc's
// String() rendering. The format (client_golang v1.23.2) is:
//
//	Desc{fqName: "agentdock_x", help: "...", constLabels: {a="b"}, variableLabels: {l1,l2}}
//
// We anchor on `fqName: "` (first quoted token) and the last `variableLabels: {`
// block. TestLabelCardinality has a sanity sub-test that fails loudly if a
// client_golang upgrade changes this layout.
func parseDesc(d *prometheus.Desc) (fqName string, varLabels []string) {
	s := d.String()
	if i := strings.Index(s, `fqName: "`); i >= 0 {
		rest := s[i+len(`fqName: "`):]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			fqName = rest[:j]
		}
	}
	if i := strings.LastIndex(s, "variableLabels: {"); i >= 0 {
		rest := s[i+len("variableLabels: {"):]
		if j := strings.IndexByte(rest, '}'); j >= 0 {
			if inner := rest[:j]; inner != "" {
				for _, p := range strings.Split(inner, ",") {
					varLabels = append(varLabels, strings.TrimSpace(p))
				}
			}
		}
	}
	return fqName, varLabels
}

// TestLabelCardinality guards against high-cardinality identifiers leaking
// into Prometheus labels (where they belong in logs / span attrs instead).
// It walks the same staticCollectors slice Register() uses, so a metric that
// forgets to register is also outside this check — keep them in sync.
func TestLabelCardinality(t *testing.T) {
	// Unbounded-cardinality keys that must never be a Prometheus label.
	banned := map[string]struct{}{
		"channel_id":   {},
		"user_id":      {},
		"thread_ts":    {},
		"repo":         {},
		"pr_number":    {},
		"issue_number": {},
	}
	// Justified exceptions: (fqName, label) pairs whose value set is in fact
	// bounded. Adding an entry here REQUIRES a PR-visible justification — the
	// reviewer gate is on this map, not on the test passing.
	type exception struct{ metric, label string }
	allowed := map[exception]string{
		{"agentdock_ref_write_violations_total", "repo"}: "ref repos are a fixed set chosen in channel config — not user-supplied URLs; cardinality is bounded",
	}

	t.Run("parser_sanity", func(t *testing.T) {
		// Single-label metric.
		fq, labels := parseDesc(descOf(t, RequestTotal))
		if fq != "agentdock_request_total" || len(labels) != 1 || labels[0] != "status" {
			t.Fatalf("parseDesc(RequestTotal) = (%q, %v), want (agentdock_request_total, [status]) — Desc.String() layout may have changed", fq, labels)
		}
		// Multi-label metric, exercises comma splitting.
		fq, labels = parseDesc(descOf(t, AgentExecutionsTotal))
		want := []string{"provider", "workflow", "status"}
		if fq != "agentdock_agent_executions_total" || !equalStrings(labels, want) {
			t.Fatalf("parseDesc(AgentExecutionsTotal) = (%q, %v), want (agentdock_agent_executions_total, %v) — Desc.String() layout may have changed", fq, labels, want)
		}
	})

	for _, c := range staticCollectors {
		ch := make(chan *prometheus.Desc, 8)
		c.Describe(ch)
		close(ch)
		for d := range ch {
			fqName, varLabels := parseDesc(d)
			for _, lbl := range varLabels {
				if _, isBanned := banned[lbl]; !isBanned {
					continue
				}
				if _, ok := allowed[exception{fqName, lbl}]; ok {
					continue
				}
				t.Errorf("metric %q carries high-cardinality label %q — move it to logs/span attrs, or add a justified entry to the allowlist", fqName, lbl)
			}
		}
	}
}

func descOf(t *testing.T, c prometheus.Collector) *prometheus.Desc {
	t.Helper()
	ch := make(chan *prometheus.Desc, 1)
	c.Describe(ch)
	close(ch)
	d, ok := <-ch
	if !ok {
		t.Fatal("collector emitted no Desc")
	}
	return d
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
