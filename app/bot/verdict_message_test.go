package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestRenderSoftWarn_NoWorkers(t *testing.T) {
	got := RenderSoftWarn(queue.Verdict{Kind: queue.VerdictNoWorkers})
	if !strings.Contains(got, ":warning:") {
		t.Errorf("missing :warning: prefix; got %q", got)
	}
	if !strings.Contains(got, "沒有 worker") {
		t.Errorf("missing key phrase '沒有 worker'; got %q", got)
	}
}

func TestRenderSoftWarn_BusyWithETA_NoQueue(t *testing.T) {
	v := queue.Verdict{
		Kind:          queue.VerdictBusyEnqueueOK,
		EstimatedWait: 6 * time.Minute,
		ActiveJobs:    1,
		TotalSlots:    1,
	}
	got := RenderSoftWarn(v)
	if got == "" {
		t.Fatal("expected non-empty warning for BusyEnqueueOK with ETA")
	}
	if !strings.Contains(got, "都在忙") {
		t.Errorf("missing key phrase '都在忙'; got %q", got)
	}
	if !strings.Contains(got, "6m") {
		t.Errorf("missing ETA '6m'; got %q", got)
	}
	if strings.Contains(got, "前面還有") {
		t.Errorf("no-queue branch should not mention '前面還有'; got %q", got)
	}
	if !strings.Contains(got, "下一個") {
		t.Errorf("no-queue branch should reassure '下一個'; got %q", got)
	}
}

func TestRenderSoftWarn_BusyWithETA_WithQueue(t *testing.T) {
	v := queue.Verdict{
		Kind:          queue.VerdictBusyEnqueueOK,
		EstimatedWait: 9 * time.Minute,
		ActiveJobs:    3,
		TotalSlots:    1,
	}
	got := RenderSoftWarn(v)
	if !strings.Contains(got, "前面還有 2 個請求") {
		t.Errorf("missing '前面還有 2 個請求'; got %q", got)
	}
	if !strings.Contains(got, "9m") {
		t.Errorf("missing ETA '9m'; got %q", got)
	}
}

func TestRenderSoftWarn_BusyZeroETA_ReturnsEmpty(t *testing.T) {
	v := queue.Verdict{Kind: queue.VerdictBusyEnqueueOK, EstimatedWait: 0}
	if got := RenderSoftWarn(v); got != "" {
		t.Errorf("expected empty for zero-ETA busy; got %q", got)
	}
}

func TestRenderSoftWarn_OK_ReturnsEmpty(t *testing.T) {
	if got := RenderSoftWarn(queue.Verdict{Kind: queue.VerdictOK}); got != "" {
		t.Errorf("expected empty for OK verdict; got %q", got)
	}
}

func TestRenderHardReject_NoWorkers(t *testing.T) {
	got := RenderHardReject(queue.Verdict{Kind: queue.VerdictNoWorkers})
	if !strings.Contains(got, ":x:") {
		t.Errorf("missing :x: prefix; got %q", got)
	}
	if !strings.Contains(got, "無法處理") {
		t.Errorf("missing '無法處理'; got %q", got)
	}
}

func TestRenderBusyHint_WithETA(t *testing.T) {
	v := queue.Verdict{
		Kind:          queue.VerdictBusyEnqueueOK,
		EstimatedWait: 9 * time.Minute,
	}
	got := RenderBusyHint(v)
	if !strings.Contains(got, "預估等候") {
		t.Errorf("missing '預估等候'; got %q", got)
	}
	if !strings.Contains(got, "9m") {
		t.Errorf("expected '9m' in output; got %q", got)
	}
}

func TestRenderBusyHint_ZeroETA_ReturnsEmpty(t *testing.T) {
	v := queue.Verdict{Kind: queue.VerdictBusyEnqueueOK, EstimatedWait: 0}
	if got := RenderBusyHint(v); got != "" {
		t.Errorf("expected empty string for zero ETA; got %q", got)
	}
}
