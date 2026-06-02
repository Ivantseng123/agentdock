package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/config"
)

// tracer reads the global TracerProvider; cmd/agentdock/worker.go owns setup.
var tracer = otel.Tracer("agentdock/worker/agent")

// RunOptions provides per-call callbacks for agent execution.
type RunOptions struct {
	OnStarted func(pid int, command string)
	OnEvent   func(event queue.StreamEvent)
	// OnExit, when non-nil, is invoked from runOne's deferred closure with
	// the captured agent process exit code. Sentinel -1 means the process
	// was never started or never waited (e.g. cmd.Start failed, blocked
	// args, output-file path). Caller is expected to plumb this into
	// JobResult.ExitCode for app-side observation.
	OnExit  func(int)
	Secrets map[string]string
}

type Runner struct {
	agents      []config.AgentConfig
	githubToken string
	opencodeCfg config.OpencodeConfig
	// supervisor is the long-running `opencode serve` child shared by
	// the worker pool when cfg.Opencode.Mode == "server". Populated by
	// worker boot via SetOpencodeSupervisor after the version check
	// passes and Supervisor.Start succeeds. nil under Mode = "spawn"
	// or when constructed via NewRunner (no Config). runOneServer
	// guards against the nil case.
	supervisor *Supervisor
}

func NewRunner(agents []config.AgentConfig) *Runner {
	return &Runner{agents: agents}
}

func NewRunnerFromConfig(cfg *config.Config) *Runner {
	var chain []config.AgentConfig
	for _, name := range cfg.Providers {
		if agent, ok := cfg.Agents[name]; ok {
			chain = append(chain, agent)
		} else {
			slog.Warn("Provider 未找到", "phase", "失敗", "name", name)
		}
	}
	runner := NewRunner(chain)
	runner.githubToken = cfg.GitHub.Token
	runner.opencodeCfg = cfg.Opencode
	return runner
}

// SetOpencodeSupervisor wires the long-running opencode serve handle
// into the Runner so runOneServer can dispatch per-job HTTP/SSE work
// through it. Called by worker boot only when cfg.Opencode.Mode ==
// "server", after Supervisor.Start succeeds.
func (r *Runner) SetOpencodeSupervisor(s *Supervisor) {
	r.supervisor = s
}

func (r *Runner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string, opts RunOptions) (string, error) {
	var errs []string
	for i, agent := range r.agents {
		logger.Info("嘗試 agent", "phase", "處理中", "command", agent.Command, "index", i, "total", len(r.agents), "timeout", agent.Timeout)
		output, err := r.runOne(ctx, logger, agent, workDir, prompt, opts)
		if err != nil {
			if ctx.Err() == context.Canceled {
				logger.Info("Agent 執行已中斷", "phase", "完成", "command", agent.Command, "index", i)
				return "", fmt.Errorf("cancelled")
			}
			logger.Warn("Agent 失敗", "phase", "失敗", "command", agent.Command, "index", i, "error", err)
			errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
			continue
		}
		logger.Info("Agent 執行成功", "phase", "完成", "command", agent.Command, "output_len", len(output))
		return output, nil
	}
	logger.Error("所有 agent 已耗盡", "phase", "失敗", "errors", strings.Join(errs, "; "))
	return "", fmt.Errorf("all agents failed: %s", strings.Join(errs, "; "))
}

// dispatchTarget returns the name of the per-mode runner this agent call
// should route to. Pure decision function — no side effects, no exec —
// so the dispatch matrix can be unit-tested without depending on a real
// opencode/claude/codex/gemini binary on PATH.
//
// Routing rule: opencode + Mode=server → "server". Every other
// combination → "spawn". This includes any non-opencode agent regardless
// of mode, opencode with Mode unset (zero value), and opencode with
// Mode=="spawn".
//
// The Command == "opencode" check matches the built-in agent definition
// at worker/config/builtin_agents.go. Operator overrides that rename
// `command:` to a wrapper script will skip the server-mode dispatch and
// fall through to spawn — acceptable for Stage 1 per the manifest's
// §1.3 trade-off; revisit if an operator actually hits this.
func (r *Runner) dispatchTarget(agent config.AgentConfig) string {
	if agent.Command == "opencode" && r.opencodeCfg.Mode == config.OpencodeModeServer {
		return "server"
	}
	return "spawn"
}

// runOne dispatches an agent invocation based on dispatchTarget. Stage 1
// ships runOneServer as a stub that errors out, so even when the
// dispatcher picks "server" the worker stays alive via Run's
// agent-chain failure path (the stub error surfaces, next provider in
// the chain takes over). The dispatcher's signature won't need to
// change for Stage 2 — but Stage 2 will add new Runner fields (e.g. a
// `*Supervisor`) and populate them inside `NewRunnerFromConfig`, so
// Runner construction *will* evolve when the real server-mode lands.
func (r *Runner) runOne(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (output string, err error) {
	if r.dispatchTarget(agent) == "server" {
		return r.runOneServer(ctx, logger, agent, workDir, prompt, opts)
	}
	return r.runOneSpawn(ctx, logger, agent, workDir, prompt, opts)
}

// readOutput routes stdout through the appropriate reader based on the
// stream format. Empty format is the non-streaming path; recognized formats
// dispatch to per-CLI parsers in shared/queue. Unknown values fall back to
// raw stdout — same observable output as a non-streaming agent, only the
// live event channel is silent. Validate at config-load time to surface
// typos before they reach here.
func readOutput(ctx context.Context, r io.Reader, format string, onEvent func(queue.StreamEvent)) string {
	if format == "" {
		return queue.ReadRawOutput(r)
	}

	eventCh := make(chan queue.StreamEvent, 64)
	var result string
	var wg sync.WaitGroup

	// Forward events to callback in a context-aware goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case evt, ok := <-eventCh:
				if !ok {
					return
				}
				if onEvent != nil {
					onEvent(evt)
				}
			case <-ctx.Done():
				// Cancellation: forward whatever the parser already produced or
				// is still flushing as the killed subprocess closes its pipe —
				// e.g. a final "result" event carrying cost/tokens — instead of
				// discarding it (issue #253). eventCh is guaranteed to close
				// (CommandContext kills the process, stdout EOFs, the parser
				// returns), so this range terminates.
				//
				// This now forwards exactly like the eventCh case above. The
				// select is kept on purpose — NOT collapsed to a single
				// `for evt := range eventCh` (which would also let ctx be
				// dropped) — because it is the only handle that makes the
				// cancellation path deterministically testable: a test cancels
				// ctx while eventCh is empty to pin the goroutine here, then
				// asserts later events are still forwarded. Do not "simplify"
				// this away.
				for evt := range eventCh {
					if onEvent != nil {
						onEvent(evt)
					}
				}
				return
			}
		}
	}()

	switch format {
	case config.StreamFormatClaude:
		result = queue.ReadStreamJSONClaude(r, eventCh)
	case config.StreamFormatOpencode:
		result = queue.ReadStreamJSONOpencode(r, eventCh)
	default:
		// Unknown format — drain stdout into the raw reader so the agent's
		// final answer still surfaces. eventCh stays empty; the forwarder
		// goroutine exits on close.
		result = queue.ReadRawOutput(r)
	}
	close(eventCh)
	wg.Wait()
	return result
}

// expandExtraArgs replaces every standalone "{extra_args}" element with zero
// or more entries from extraArgs. nil/empty extraArgs drops the slot entirely
// (no empty-string element leaks into the resulting argv). Substring matches
// inside a larger string are NOT expanded — the token must stand alone as its
// own arg. This is the single splice site for extra_args across both the
// arg-prompt and stdin-prompt paths.
func expandExtraArgs(args []string, extraArgs []string) []string {
	result := make([]string, 0, len(args)+len(extraArgs))
	for _, a := range args {
		if a == config.ExtraArgsToken {
			result = append(result, extraArgs...)
			continue
		}
		result = append(result, a)
	}
	return result
}

// substituteStringPlaceholders applies strings.ReplaceAll for each (key, val)
// pair to every element of args. Used only for string-valued placeholders
// ({prompt}, {output_file}); list-valued {extra_args} must be expanded via
// expandExtraArgs beforehand.
func substituteStringPlaceholders(args []string, values map[string]string) []string {
	result := make([]string, 0, len(args))
	for _, a := range args {
		for k, v := range values {
			a = strings.ReplaceAll(a, k, v)
		}
		result = append(result, a)
	}
	return result
}

// blockedArgs is the set of CLI flags that bypass the agent's host sandbox.
// Memory feedback_worker_deployment_unknown rationale: workers may run on a
// user's real machine, not an isolated pod, so allowing these would let the
// agent touch $HOME, /etc, SSH keys, etc.
var blockedArgs = []string{
	"--dangerously-skip-permissions",
}

// claudeCodeEnvWhitelist is the set of CLAUDE_CODE_* env vars that pass
// through to agent processes. Anything else with the CLAUDE_CODE_ prefix
// inherited from the worker host is stripped to keep agent behavior
// deterministic across deployment environments. Add entries when a new var
// becomes load-bearing.
var claudeCodeEnvWhitelist = map[string]bool{
	"CLAUDE_CODE_NO_FLICKER": true, // see project_cmux_claude_flicker_workaround
}

// stderrTailLen caps how much of an agent's stderr survives into error
// messages and warn logs. Large stderr blobs (e.g. claude SDK's every-event
// JSON dumps) otherwise spam the logger and crowd out other signal.
const stderrTailLen = 2000

// tailStderr returns the trailing stderrTailLen bytes of s, prefixed with a
// "…" marker when truncation occurred. Single truncation site so tail size
// stays consistent across error and log surfaces.
func tailStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= stderrTailLen {
		return s
	}
	return "…" + s[len(s)-stderrTailLen:]
}

// detectBlockedArgs returns any blockedArgs entries present in args. The flag
// must stand alone or appear as `--flag=value`; substring matches inside
// other args are NOT detected. Caller surfaces the result; this function has
// no side effects.
func detectBlockedArgs(args []string) []string {
	var found []string
	for _, a := range args {
		for _, blocked := range blockedArgs {
			if a == blocked || strings.HasPrefix(a, blocked+"=") {
				found = append(found, a)
			}
		}
	}
	return found
}

// filterClaudeCodeEnv strips CLAUDE_CODE_* vars from env unless the key is
// in claudeCodeEnvWhitelist. Non-CLAUDE_CODE_* entries pass through unchanged.
// Output preserves input ordering for deterministic env substitution under
// exec.Command.
func filterClaudeCodeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDE_CODE_") {
			out = append(out, e)
			continue
		}
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		if claudeCodeEnvWhitelist[e[:i]] {
			out = append(out, e)
		}
	}
	return out
}
