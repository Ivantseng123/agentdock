// Package worker is the module entry point for the agentdock worker process.
// cmd/agentdock/worker.go wraps Run with cobra.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ivantseng123/agentdock/shared/crypto"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/agent"
	"github.com/Ivantseng123/agentdock/worker/config"
	"github.com/Ivantseng123/agentdock/worker/pool"
)

// Build info propagated from cmd at link time. cmd/agentdock/worker.go copies
// goreleaser-injected main.* values into these vars before Run executes so
// startup logs and JSON base attrs report the correct build.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Run starts the worker process: initializes logging, connects Redis, builds
// the pool, and waits for SIGTERM/SIGINT. Returns on clean shutdown or error.
func Run(cfg *config.Config) error {
	// Stderr base attrs gate on stderr_format; file base attrs apply always
	// (file output is JSON regardless of stderr_format and post-mortem
	// readers need build identity on every record).
	var stderrHandler slog.Handler = logging.BuildStderrHandler(cfg.Logging.StderrFormat, logging.ParseLevel(cfg.LogLevel))
	if attrs := logging.StderrBaseAttrs(cfg.Logging.StderrFormat, "agentdock", Version, Commit); len(attrs) > 0 {
		stderrHandler = stderrHandler.WithAttrs(attrs)
	}

	rotator, err := logging.NewRotator(cfg.Logging.Dir)
	if err != nil {
		return fmt.Errorf("log rotator: %w", err)
	}
	rotator.StartCleanup(cfg.Logging.RetentionDays)

	var fileHandler slog.Handler = slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: logging.ParseLevel(cfg.Logging.Level)})
	if attrs := logging.FileBaseAttrs("agentdock", Version, Commit); len(attrs) > 0 {
		fileHandler = fileHandler.WithAttrs(attrs)
	}

	rootHandler := slog.Handler(logging.NewMultiHandler(stderrHandler, fileHandler))
	rootHandler = logging.NewTraceIDHandler(rootHandler)
	slog.SetDefault(slog.New(rootHandler))
	appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)

	// Transport selection. Kept in sync with app/app.go so a future backend
	// (e.g. github-runner) only needs a new case on both sides.
	jobStore := queue.NewMemJobStore()
	var bundle *queue.Bundle
	switch cfg.Queue.Transport {
	case "redis":
		rdb, err := queue.NewRedisClient(queue.RedisConfig{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
			TLS:      cfg.Redis.TLS,
		})
		if err != nil {
			return fmt.Errorf("failed to connect to Redis: %w", err)
		}
		appLogger.Info("已連線至 Redis", "phase", "處理中", "addr", cfg.Redis.Addr)
		queueLogger := logging.ComponentLogger(slog.Default(), logging.CompQueue)
		bundle = queue.NewRedisBundle(rdb, jobStore, "triage",
			queue.WithRedisJobQueueLogger(queueLogger))
	default:
		return fmt.Errorf("unsupported queue.transport %q (supported: redis)", cfg.Queue.Transport)
	}

	agentRunner := agent.NewRunnerFromConfig(cfg)
	agentRunner.LogVersions(context.Background(), appLogger)

	if cfg.Opencode.Mode == config.OpencodeModeServer {
		// Deliberately stricter than LogVersions's warn-and-continue: server
		// mode wires HTTP/SSE contracts validated only against
		// MinimumOpencodeVersion (see worker/agent/opencode_version.go and
		// docs/specs/opencode-server-mode-poc-report.md). Booting below
		// floor or with a broken binary would silently degrade ask answers,
		// so this gate fails fast at the same phase as LogVersions.
		versionLogger := logging.ComponentLogger(slog.Default(), "OpencodeVersion")
		opencodeAgent, ok := cfg.Agents["opencode"]
		if !ok {
			versionLogger.Error("opencode.mode=server 但 worker.yaml agents.opencode 未定義",
				"phase", "失敗",
				"hint", "在 agents.opencode 加入定義，或將 opencode.mode 改回 spawn",
			)
			return fmt.Errorf("opencode provider not configured (worker.yaml agents.opencode missing while opencode.mode=server)")
		}
		detected, err := agent.CheckOpencodeVersion(context.Background(), opencodeAgent.Command)
		if err != nil {
			versionLogger.Error("opencode server-mode 版本檢查失敗",
				"phase", "失敗",
				"command", opencodeAgent.Command,
				"detected", detected,
				"required", agent.MinimumOpencodeVersion,
				"error", err,
			)
			return fmt.Errorf("opencode server-mode version check failed: %w", err)
		}
		versionLogger.Info("opencode server-mode 版本通過門檻",
			"phase", "完成",
			"command", opencodeAgent.Command,
			"detected", detected,
			"required", agent.MinimumOpencodeVersion,
		)

		// Spawn the long-running `opencode serve` child. Always-on at
		// Stage 2 (lazy lifecycle stays Task 3.2-8 / Stage 3). Worker
		// pool's N goroutines share the single supervisor via per-
		// request `x-opencode-directory` header isolation.
		serverLogger := logging.ComponentLogger(slog.Default(), "OpencodeServer")
		supervisor := agent.NewSupervisor(agent.SupervisorConfig{
			BinaryPath: opencodeAgent.Command,
			StorageDir: cfg.Opencode.StorageDir,
			Logger:     serverLogger,
		})
		if err := supervisor.Start(context.Background()); err != nil {
			return fmt.Errorf("start opencode supervisor: %w", err)
		}
		agentRunner.SetOpencodeSupervisor(supervisor)
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCancel()
			if err := supervisor.Stop(stopCtx); err != nil {
				appLogger.Warn("opencode supervisor stop 失敗",
					"phase", "失敗",
					"error", err,
				)
			}
		}()
	}

	secretKey, err := crypto.DecodeSecretKey(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("invalid secret_key: %w", err)
	}

	githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)

	repoAdapter := &pool.RepoCacheAdapter{Cache: repoCache}
	if err := repoAdapter.PurgeStale(); err != nil {
		appLogger.Warn("啟動時清理舊 repo 快取失敗", "phase", "處理中", "error", err)
	}

	var skillDirs []string
	seen := make(map[string]bool)
	for _, name := range cfg.Providers {
		if ag, ok := cfg.Agents[name]; ok && ag.SkillDir != "" && !seen[ag.SkillDir] {
			skillDirs = append(skillDirs, ag.SkillDir)
			seen[ag.SkillDir] = true
		}
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	if n := len(cfg.NicknamePool); n > 0 && n < cfg.Count {
		appLogger.Warn("nickname 池小於 worker 數，部份 worker 將無暱稱",
			"phase", "處理中", "pool_size", n, "worker_count", cfg.Count)
	}
	nicknameRNG := rand.New(rand.NewSource(time.Now().UnixNano()))
	nicknames := pool.PickNicknames(cfg.NicknamePool, cfg.Count, nicknameRNG)

	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	workerPool := pool.NewPool(pool.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &pool.AgentRunnerAdapter{Runner: agentRunner},
		RepoCache:      repoAdapter,
		WorkerCount:    cfg.Count,
		Nicknames:      nicknames,
		Hostname:       hostname,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
		Logger:         workerLogger,
		SecretKey:      secretKey,
		WorkerSecrets:  cfg.Secrets,
		ExtraRules:     cfg.Prompt.ExtraRules,
	})

	ctx, cancel := context.WithCancel(context.Background())
	workerPool.Start(ctx)
	appLogger.Info("Worker 已啟動", "phase", "完成", "workers", cfg.Count)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	appLogger.Info("正在關閉", "phase", "完成", "signal", sig)
	cancel()
	bundle.Close()
	return nil
}
