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
	ghclient "agentdock/internal/github"
	"agentdock/internal/logging"
	"agentdock/internal/mantis"
	"agentdock/internal/queue"
	"agentdock/internal/skill"
	slackclient "agentdock/internal/slack"

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

	githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
	slackLogger := logging.ComponentLogger(slog.Default(), logging.CompSlack)

	slackClient := slackclient.NewClient(cfg.Slack.BotToken, slackLogger)

	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)
	repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token, githubLogger)

	if cfg.AutoBind {
		go func() {
			_, err := repoDiscovery.ListRepos(context.Background())
			if err != nil {
				slog.Warn("failed to pre-warm repo cache", "error", err)
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
			slog.Warn("failed to start skill config watcher", "error", err)
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
		slog.Info("mantis integration enabled", "url", cfg.Mantis.BaseURL)
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
		slog.Info("using Redis transport", "addr", cfg.Redis.Addr)
	default:
		bundle = queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
		slog.Info("using in-memory transport")
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
	coordinator := queue.NewCoordinator(bundle.Queue)
	coordinator.RegisterQueue("triage", bundle.Queue)

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

	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, coordinator, jobStore, bundle.Attachments, skillLoader)

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

	issueClient := ghclient.NewIssueClient(cfg.GitHub.Token, githubLogger)
	resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
		&slackPosterAdapter{client: slackClient, logger: slackLogger}, issueClient,
		func(channelID, threadTS string) {
			handler.ClearThreadDedup(channelID, threadTS)
		})
	go resultListener.Listen(context.Background())

	retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient, logger: slackLogger})

	statusListener := bot.NewStatusListener(bundle.Status, jobStore)
	go statusListener.Listen(context.Background())

	// Job watchdog — detect stuck jobs and publish failures to ResultBus.
	watchdog := queue.NewWatchdog(jobStore, bundle.Commands, bundle.Results, queue.WatchdogConfig{
		JobTimeout:     cfg.Queue.JobTimeout,
		IdleTimeout:    cfg.Queue.AgentIdleTimeout,
		PrepareTimeout: cfg.Queue.PrepareTimeout,
	})
	go watchdog.Start(make(chan struct{})) // runs until process exits

	if cfg.Server.Port > 0 {
		go func() {
			http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			})
			http.HandleFunc("/jobs", queue.StatusHandler(jobStore, coordinator))
			http.HandleFunc("/jobs/", queue.KillHandler(jobStore, bundle.Commands))
			addr := fmt.Sprintf(":%d", cfg.Server.Port)
			slog.Info("http endpoints listening", "addr", addr, "endpoints", []string{"/healthz", "/jobs", "/jobs/{id}"})
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
		slog.Info("bot identity resolved", "userID", botUserID)
	} else {
		slog.Warn("failed to resolve bot identity, auto-bind may not filter correctly", "error", err)
	}

	slog.Info("starting bot", "version", version, "commit", commit, "date", date)

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
					slog.Info("block suggestion received", "actionID", cb.ActionID, "value", cb.Value)
					if cb.ActionID == "repo_search" {
						options := wf.HandleRepoSuggestion(cb.Value)
						slog.Info("repo suggestion results", "query", cb.Value, "count", len(options))
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
					slog.Info("block action received", "actionID", action.ActionID, "value", action.Value, "selectorTS", selectorTS)

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
						if err == nil && state.Status != queue.JobFailed && state.Status != queue.JobCompleted {
							bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
							jobStore.UpdateStatus(jobID, queue.JobFailed)
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
