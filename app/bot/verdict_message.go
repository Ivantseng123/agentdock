package bot

import (
	"fmt"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// RenderSoftWarn produces the trigger-time soft warning. Returns "" when the
// verdict doesn't warrant user-facing notice (OK, or BusyEnqueueOK with no
// ETA).
//
// Two cases are rendered:
//   - NoWorkers: no worker online, submit may fail
//   - BusyEnqueueOK with ETA: all workers are busy — warn early so the user
//     knows clicking through the selectors will end in a queued submit, not
//     an immediate handoff
func RenderSoftWarn(v queue.Verdict) string {
	switch v.Kind {
	case queue.VerdictNoWorkers:
		return ":warning: 目前沒有 worker 在線，你仍可繼續選擇，送出時會再確認一次。"
	case queue.VerdictBusyEnqueueOK:
		if v.EstimatedWait <= 0 {
			return ""
		}
		mins := int(v.EstimatedWait.Round(time.Minute).Minutes())
		// ActiveJobs is capacity-occupying count at check time; subtracting
		// TotalSlots yields jobs queued beyond capacity (i.e. ahead of a
		// hypothetical submit). When == 0, no one is queued yet — the caller
		// will be first into the wait line.
		ahead := v.ActiveJobs - v.TotalSlots
		if ahead > 0 {
			return fmt.Sprintf(":hourglass_flowing_sand: 目前所有 worker 都在忙，前面還有 %d 個請求在等，送出後預估等候 ~%dm。你仍可繼續選擇。", ahead, mins)
		}
		return fmt.Sprintf(":hourglass_flowing_sand: 目前所有 worker 都在忙，送出後預估等候 ~%dm。你仍可繼續選擇。", mins)
	default:
		return ""
	}
}

// RenderHardReject produces the submit-time rejection message.
func RenderHardReject(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":x: 目前沒有 worker 在線，無法處理。請稍後再試。"
}

// RenderBusyHint produces the suffix appended to the lifecycle queue
// message when the verdict is BusyEnqueueOK with a non-zero ETA.
func RenderBusyHint(v queue.Verdict) string {
	if v.EstimatedWait <= 0 {
		return ""
	}
	return fmt.Sprintf("(預估等候 ~%dm)",
		int(v.EstimatedWait.Round(time.Minute).Minutes()))
}
