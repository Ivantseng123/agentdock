package queue

// Test-only exports so queue_test (external-package tests) can exercise
// Watchdog's internal check and killAndPublish methods without promoting
// them to the public API. Only compiled into the test binary.

func (w *Watchdog) Check() { w.check() }

func (w *Watchdog) KillAndPublish(state *JobState, reason string) {
	w.killAndPublish(state, reason)
}
