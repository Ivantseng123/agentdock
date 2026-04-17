package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"agentdock/internal/bot"
	"agentdock/internal/config"
	"agentdock/internal/crypto"
	ghclient "agentdock/internal/github"
	"agentdock/internal/logging"
	"agentdock/internal/mantis"
	"agentdock/internal/metrics"
	"agentdock/internal/queue"
	"agentdock/internal/skill"
	slackclient "agentdock/internal/slack"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/spf13/cobra"
)

var appConfigPath string

var appCmd = &cobra.Command{
	Use:          "app",
	Short:        "Run the main Slack bot",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return loadAndStash(cmd, appConfigPath, ScopeApp)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runApp(cfgFromCtx(cmd.Context()))
	},
}

func init() {
	appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "", "path to config file (default ~/.config/agentdock/config.yaml)")
	rootCmd.AddCommand(appCmd)
	rootCmd.AddCommand(workerCmd)
	addAppFlags(appCmd)
}

func runApp(cfg *config.Config) error {
	// Use INFO until config is loaded.
	slog.SetDefault(slog.New(logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Re-init logger with configured level.
	stderrHandler := logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})

	rotator, err := logging.NewRotator(cfg.Logging.Dir)
	if err != nil {
		return fmt.Errorf("failed to init log rotator: %w", err)
	}
	rotator.StartCleanup(cfg.Logging.RetentionDays)

	fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)})
	slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))

	appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)
	githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
	slackLogger := logging.ComponentLogger(slog.Default(), logging.CompSlack)

	slackClient := slackclient.NewClient(cfg.Slack.BotToken, slackLogger)

	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)
	repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token, githubLogger)

	if cfg.AutoBind {
		go func() {
			_, err := repoDiscovery.ListRepos(context.Background())
			if err != nil {
				appLogger.Warn("Repo 快取預熱失敗", "phase", "失敗", "error", err)
			}
		}()
	}

	agentRunner := bot.NewAgentRunnerFromConfig(cfg)

	// Load skills via SkillLoader.
	bakedInDir := "agents/skills"
	if _, err := os.Stat("/opt/agents/skills"); err == nil {
		bakedInDir = "/opt/agents/skills"
	}
	skillLogger := logging.ComponentLogger(slog.Default(), logging.CompSkill)
	skillLoader, err := skill.NewLoader(cfg.SkillsConfig, bakedInDir, skillLogger)
	if err != nil {
		return fmt.Errorf("failed to create skill loader: %w", err)
	}
	skillLoader.Warmup(context.Background())
	if cfg.SkillsConfig != "" {
		stopWatcher, err := skillLoader.StartWatcher(cfg.SkillsConfig)
		if err != nil {
			appLogger.Warn("Skill 設定監視器啟動失敗", "phase", "失敗", "error", err)
		} else {
			defer stopWatcher()
		}
	}

	mantisClient := mantis.NewClient(
		cfg.Mantis.BaseURL,
		cfg.Mantis.APIToken,
		cfg.Mantis.Username,
		cfg.Mantis.Password,
	)
	if mantisClient.IsConfigured() {
		appLogger.Info("Mantis 整合已啟用", "phase", "處理中", "url", cfg.Mantis.BaseURL)
	}

	jobStore := queue.NewMemJobStore()
	jobStore.StartCleanup(1 * time.Hour)

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
		bundle = queue.NewRedisBundle(rdb, jobStore, "triage")
		appLogger.Info("使用 Redis transport", "phase", "處理中", "addr", cfg.Redis.Addr)

		// Write secret key beacon so workers can verify key match at startup.
		if cfg.SecretKey != "" {
			sk, err := config.DecodeSecretKey(cfg.SecretKey)
			if err != nil {
				return fmt.Errorf("invalid secret_key: %w", err)
			}
			if err := crypto.WriteBeacon(context.Background(), rdb, sk); err != nil {
				return fmt.Errorf("failed to write secret beacon: %w", err)
			}
			appLogger.Info("Secret beacon 已寫入 Redis", "phase", "完成")
		}
	default:
		bundle = queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
		appLogger.Info("使用 in-memory transport", "phase", "處理中")
	}

	// Collect skill dirs from all agents in provider chain.
	seen := make(map[string]bool)
	var skillDirs []string
	for _, name := range cfg.Providers {
		if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" && !seen[agent.SkillDir] {
			skillDirs = append(skillDirs, agent.SkillDir)
			seen[agent.SkillDir] = true
		}
	}
	if len(skillDirs) == 0 && cfg.ActiveAgent != "" {
		if agent, ok := cfg.Agents[cfg.ActiveAgent]; ok && agent.SkillDir != "" {
			skillDirs = append(skillDirs, agent.SkillDir)
		}
	}

	// Create Coordinator (JobQueue decorator for TaskType routing).
	coordinator := queue.NewCoordinator(bundle.Queue,
		queue.WithCoordinatorSubmitHook(func(priority string) {
			metrics.QueueSubmittedTotal.WithLabelValues(priority).Inc()
		}),
	)
	coordinator.RegisterQueue("triage", bundle.Queue)

	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	queueLogger := logging.ComponentLogger(slog.Default(), logging.CompQueue)

	// Create and start LocalAdapter (owns worker.Pool lifecycle).
	// In redis mode, workers are separate pods — skip local agent execution.
	if cfg.Queue.Transport != "redis" {
		localAdapter := NewLocalAdapter(LocalAdapterConfig{
			Runner:         &agentRunnerAdapter{runner: agentRunner},
			RepoCache:      &repoCacheAdapter{cache: repoCache},
			SkillDirs:      skillDirs,
			WorkerCount:    cfg.Workers.Count,
			StatusInterval: cfg.Queue.StatusInterval,
			Capabilities:   []string{"triage"},
			Store:          jobStore,
			Logger:         workerLogger,
		})
		if err := localAdapter.Start(queue.AdapterDeps{
			Jobs:        bundle.Queue,
			Results:     bundle.Results,
			Status:      bundle.Status,
			Commands:    bundle.Commands,
			Attachments: bundle.Attachments,
		}); err != nil {
			return fmt.Errorf("failed to start local adapter: %w", err)
		}
	}

	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader)

	handler := slackclient.NewHandler(slackclient.HandlerConfig{
		MaxConcurrent:   cfg.MaxConcurrent,
		DedupTTL:        5 * time.Minute,
		PerUserLimit:    cfg.RateLimit.PerUser,
		PerChannelLimit: cfg.RateLimit.PerChannel,
		RateWindow:      cfg.RateLimit.Window,
		OnEvent:         wf.HandleTrigger,
		OnRejected: func(e slackclient.TriggerEvent, reason string) {
			slackClient.PostMessage(e.ChannelID,
				fmt.Sprintf(":warning: %s", reason), e.ThreadTS)
		},
	})
	wf.SetHandler(handler)

	agentLogger := logging.ComponentLogger(slog.Default(), logging.CompAgent)

	issueClient := ghclient.NewIssueClient(cfg.GitHub.Token, githubLogger)
	resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
		&slackPosterAdapter{client: slackClient, logger: slackLogger}, issueClient,
		func(channelID, threadTS string) {
			handler.ClearThreadDedup(channelID, threadTS)
		}, agentLogger)
	go resultListener.Listen(context.Background())

	retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient, logger: slackLogger}, workerLogger)

	statusListener := bot.NewStatusListener(bundle.Status, jobStore, slackClient, queueLogger)
	go statusListener.Listen(context.Background())

	// Job watchdog — detect stuck jobs and publish failures to ResultBus.
	watchdog := queue.NewWatchdog(jobStore, bundle.Commands, bundle.Results, queue.WatchdogConfig{
		JobTimeout:     cfg.Queue.JobTimeout,
		IdleTimeout:    cfg.Queue.AgentIdleTimeout,
		PrepareTimeout: cfg.Queue.PrepareTimeout,
		CancelTimeout:  cfg.Queue.CancelTimeout,
	}, queueLogger,
		queue.WithWatchdogKillHook(func(reason string) {
			metrics.WatchdogKillsTotal.WithLabelValues(reason).Inc()
		}),
	)
	go watchdog.Start(make(chan struct{})) // runs until process exits

	metrics.Register(prometheus.DefaultRegisterer, coordinator, jobStore)

	if cfg.Server.Port > 0 {
		go func() {
			http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			})
			http.HandleFunc("/jobs", queue.StatusHandler(jobStore, coordinator))
			http.HandleFunc("/jobs/", queue.KillHandler(jobStore, bundle.Commands))
			http.Handle("/metrics", promhttp.Handler())
			addr := fmt.Sprintf(":%d", cfg.Server.Port)
			appLogger.Info("HTTP 端點已啟動", "phase", "處理中", "addr", addr, "endpoints", []string{"/healthz", "/jobs", "/jobs/{id}", "/metrics"})
			http.ListenAndServe(addr, nil)
		}()
	}

	api := slack.New(cfg.Slack.BotToken,
		slack.OptionAppLevelToken(cfg.Slack.AppToken),
	)
	sm := socketmode.New(api)

	// Resolve bot's own user ID for auto-bind filtering.
	botUserID := ""
	if authResp, err := api.AuthTest(); err == nil {
		botUserID = authResp.UserID
		appLogger.Info("Bot 身份已解析", "phase", "處理中", "user_id", botUserID)
	} else {
		appLogger.Warn("Bot 身份解析失敗", "phase", "失敗", "error", err)
	}

	appLogger.Info("啟動 Bot", "phase", "處理中", "version", version, "commit", commit, "date", date)

	go func() {
		for evt := range sm.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				sm.Ack(*evt.Request)
				ea, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				switch inner := ea.InnerEvent.Data.(type) {
				case *slackevents.AppMentionEvent:
					handler.HandleTrigger(slackclient.TriggerEvent{
						ChannelID: inner.Channel,
						ThreadTS:  inner.ThreadTimeStamp,
						TriggerTS: inner.TimeStamp,
						UserID:    inner.User,
						Text:      inner.Text,
					})
				case *slackevents.MemberJoinedChannelEvent:
					if cfg.AutoBind && inner.User == botUserID {
						wf.RegisterChannel(inner.Channel)
					}
				case *slackevents.MemberLeftChannelEvent:
					if cfg.AutoBind && inner.User == botUserID {
						wf.UnregisterChannel(inner.Channel)
					}
				}

			case socketmode.EventTypeSlashCommand:
				sm.Ack(*evt.Request)
				cmd, ok := evt.Data.(slack.SlashCommand)
				if !ok || cmd.Command != "/triage" {
					continue
				}
				// Slash commands don't reliably carry thread_ts.
				// If no thread context, tell user to use @mention instead.
				if cmd.ChannelID == "" {
					continue
				}
				// Use @bot mention for thread-based triage.
				// /triage without thread context posts a help message.
				slackClient.PostMessage(cmd.ChannelID,
					":point_right: 請在對話串中使用 `@bot` 來觸發 triage，或直接在 thread 中 mention bot。\n`/triage` 指令目前不支援 thread 偵測。", "")

			case socketmode.EventTypeInteractive:
				cb, ok := evt.Data.(slack.InteractionCallback)
				if !ok {
					sm.Ack(*evt.Request)
					continue
				}

				// BlockSuggestion must ack WITH options — don't ack early.
				if cb.Type == slack.InteractionTypeBlockSuggestion {
					appLogger.Info("收到搜尋建議", "phase", "接收", "action_id", cb.ActionID, "value", cb.Value)
					if cb.ActionID == "repo_search" {
						options := wf.HandleRepoSuggestion(cb.Value)
						appLogger.Info("Repo 搜尋結果", "phase", "處理中", "query", cb.Value, "count", len(options))
						var opts []*slack.OptionBlockObject
						for _, r := range options {
							opts = append(opts, slack.NewOptionBlockObject(r, slack.NewTextBlockObject("plain_text", r, false, false), nil))
						}
						sm.Ack(*evt.Request, slack.OptionsResponse{Options: opts})
					} else {
						sm.Ack(*evt.Request)
					}
					continue
				}

				sm.Ack(*evt.Request)

				switch cb.Type {
				case slack.InteractionTypeBlockActions:
					if len(cb.ActionCallback.BlockActions) == 0 {
						continue
					}
					action := cb.ActionCallback.BlockActions[0]
					selectorTS := cb.Message.Timestamp
					appLogger.Info("收到按鈕互動", "phase", "接收", "action_id", action.ActionID, "value", action.Value, "selector_ts", selectorTS)

					switch {
					case action.ActionID == "repo_search" && action.SelectedOption.Value != "":
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.SelectedOption.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "repo_select"):
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "branch_select"):
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "description_action"):
						wf.HandleDescriptionAction(cb.Channel.ID, action.Value, selectorTS, cb.TriggerID)

					case action.ActionID == "retry_job":
						retryHandler.Handle(cb.Channel.ID, action.Value, selectorTS)

					case strings.HasPrefix(action.ActionID, "cancel_job"):
						jobID := action.Value
						state, err := jobStore.Get(jobID)
						if err == nil &&
							state.Status != queue.JobFailed &&
							state.Status != queue.JobCompleted &&
							state.Status != queue.JobCancelled {
							// Order matters: UpdateStatus BEFORE Send so the worker's classifyResult
							// observes JobCancelled when ctx cancellation propagates.
							jobStore.UpdateStatus(jobID, queue.JobCancelled)
							bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
							slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
							handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
						} else {
							slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
						}
					}

				case slack.InteractionTypeViewSubmission:
					meta := cb.View.PrivateMetadata
					desc := ""
					if v, ok := cb.View.State.Values["description_block"]["description_input"]; ok {
						desc = v.Value
					}
					wf.HandleDescriptionSubmit(meta, desc)

				case slack.InteractionTypeViewClosed:
					meta := cb.View.PrivateMetadata
					wf.HandleDescriptionSubmit(meta, "")
				}
			}
		}
	}()

	if err := sm.Run(); err != nil {
		return fmt.Errorf("socket mode error: %w", err)
	}
	return nil
}
