// Package metrics defines the Prometheus metrics used across the AgentDock
// app and worker processes. Metrics live in shared/ because shared-level
// packages (e.g. shared/github) instrument themselves with these counters
// and histograms. Both app and worker emit metrics; each process exposes
// them on its own /metrics endpoint.
package metrics

import (
	"context"

	"github.com/Ivantseng123/agentdock/shared/queue"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "agentdock"

// ---- Request Pipeline ----

var RequestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "request_total",
	Help:      "Total requests by acceptance status.",
}, []string{"status"})

var RequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "request_duration_seconds",
	Help:      "End-to-end request duration from Slack trigger to issue creation.",
	Buckets:   []float64{30, 60, 120, 300, 600, 900, 1200},
})

// ---- Queue ----

// QueueDepth is registered as a GaugeFunc inside Register().

var QueueSubmittedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "queue_submitted_total",
	Help:      "Jobs submitted to the queue by priority.",
}, []string{"priority"})

var QueueWait = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "queue_wait_seconds",
	Help:      "Time a job waits in queue before a worker picks it up.",
	Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
})

var QueueJobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "queue_job_duration_seconds",
	Help:      "Total job duration from dequeue to completion/failure, labelled by workflow and status.",
	Buckets:   []float64{30, 60, 120, 300, 600, 900, 1200},
}, []string{"workflow", "status"})

// ---- Agent ----

var AgentExecution = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "agent_execution_seconds",
	Help:      "CLI agent execution wall-clock time.",
	Buckets:   []float64{30, 60, 120, 300, 600, 900},
}, []string{"provider"})

var AgentExecutionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "agent_executions_total",
	Help:      "Agent execution outcomes.",
}, []string{"provider", "workflow", "status"})

var AgentPrepare = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "agent_prepare_seconds",
	Help:      "Time to prepare the agent environment (clone, checkout, skill files).",
	Buckets:   []float64{1, 5, 10, 30, 60, 120},
})

var AgentToolCalls = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "agent_tool_calls",
	Help:      "Number of tool calls made by the agent.",
	Buckets:   prometheus.LinearBuckets(0, 10, 20),
}, []string{"provider"})

var AgentFilesRead = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "agent_files_read",
	Help:      "Number of files read by the agent.",
	Buckets:   prometheus.LinearBuckets(0, 5, 20),
}, []string{"provider"})

var AgentCostUSD = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "agent_cost_usd",
	Help:      "Cumulative agent cost in USD.",
}, []string{"provider"})

var AgentTokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "agent_tokens_total",
	Help:      "Cumulative token usage.",
}, []string{"provider", "type"})

// ---- Workflow ----

var WorkflowCompletionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "workflow_completions_total",
	Help:      "Count of workflow completions, labelled by workflow and outcome status.",
}, []string{"workflow", "status"})

var WorkflowRetryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "workflow_retry_total",
	Help:      "Count of workflow retry attempts and exhaustions.",
}, []string{"workflow", "outcome"})

// ---- Handler ----

var HandlerDedupRejectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "handler_dedup_rejections_total",
	Help:      "Duplicate trigger events rejected by the handler.",
})

var HandlerRateLimitTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "handler_rate_limit_total",
	Help:      "Requests rejected by rate limiting.",
}, []string{"type"})

// ---- Watchdog ----

var WatchdogKillsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "watchdog_kills_total",
	Help:      "Jobs killed by the watchdog.",
}, []string{"reason"})

// ---- Availability ----

var WorkerAvailabilityVerdictTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "worker_availability_verdict_total",
	Help:      "Counts of availability verdicts by kind and stage.",
}, []string{"kind", "stage"})

var WorkerAvailabilityCheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "worker_availability_check_duration_seconds",
	Help:      "Latency of WorkerAvailability.compute.",
	Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
})

var WorkerAvailabilityCheckErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "worker_availability_check_errors_total",
	Help:      "Errors from availability dependencies.",
}, []string{"dependency"})

// ---- External ----

var ExternalDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "external_duration_seconds",
	Help:      "Latency of external service calls (Slack API, GitHub API, etc.).",
	Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10},
}, []string{"service", "operation"})

var ExternalErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "external_errors_total",
	Help:      "Errors from external service calls.",
}, []string{"service", "operation"})

// ---- Worker (GaugeFunc, registered in Register) ----
// WorkerActive and WorkerIdle are computed on each Prometheus scrape.

// Register registers all metrics with the given registerer. The q and store
// parameters power the three GaugeFunc metrics (queue_depth, worker_active,
// worker_idle) that are computed on scrape rather than incremented/decremented.
//
// Pass nil for q and store if the GaugeFunc metrics are not needed (e.g. in
// unit tests that only care about counters/histograms).
func Register(reg prometheus.Registerer, q queue.JobQueue, store queue.JobStore) {
	// Static collectors.
	reg.MustRegister(
		RequestTotal,
		RequestDuration,
		QueueSubmittedTotal,
		QueueWait,
		QueueJobDuration,
		AgentExecution,
		AgentExecutionsTotal,
		AgentPrepare,
		AgentToolCalls,
		AgentFilesRead,
		AgentCostUSD,
		AgentTokensTotal,
		WorkflowCompletionsTotal,
		WorkflowRetryTotal,
		HandlerDedupRejectionsTotal,
		HandlerRateLimitTotal,
		WatchdogKillsTotal,
		ExternalDuration,
		ExternalErrorsTotal,
		WorkerAvailabilityVerdictTotal,
		WorkerAvailabilityCheckDuration,
		WorkerAvailabilityCheckErrors,
	)

	// GaugeFunc metrics — computed on each scrape.
	if q != nil {
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "queue_depth",
			Help:      "Current number of pending jobs in the queue.",
		}, func() float64 {
			return float64(q.QueueDepth())
		}))

		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "worker_active",
			Help:      "Number of workers currently running a job.",
		}, func() float64 {
			return countActive(store)
		}))

		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "worker_idle",
			Help:      "Number of registered workers not running a job.",
		}, func() float64 {
			workers, err := q.ListWorkers(context.Background())
			if err != nil {
				return 0
			}
			return float64(len(workers)) - countActive(store)
		}))
	}
}

// countActive returns the number of jobs in Running status from the store.
func countActive(store queue.JobStore) float64 {
	if store == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), queue.DefaultStoreOpTimeout)
	defer cancel()
	all, err := store.ListAll(ctx)
	if err != nil {
		return 0
	}
	var n int
	for _, js := range all {
		if js.Status == queue.JobRunning {
			n++
		}
	}
	return float64(n)
}
