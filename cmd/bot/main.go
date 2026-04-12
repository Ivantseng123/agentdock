package main

import (
	"context"
	"flag"
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
	slackclient "agentdock/internal/slack"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "worker":
			runWorker()
			return
		}
	}

	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Use INFO until config is loaded.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Re-init logger with configured level.
	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})

	rotator, err := logging.NewRotator(cfg.Logging.Dir)
	if err != nil {
		slog.Error("failed to init log rotator", "error", err)
		os.Exit(1)
	}
	rotator.StartCleanup(cfg.Logging.RetentionDays)

	fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)})
	slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))

	slackClient := slackclient.NewClient(cfg.Slack.BotToken)

	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token)
	repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token)

	if cfg.AutoBind {
		go func() {
			_, err := repoDiscovery.ListRepos(context.Background())
			if err != nil {
				slog.Warn("failed to pre-warm repo cache", "error", err)
			}
		}()
	}

	agentRunner := bot.NewAgentRunnerFromConfig(cfg)

	// Load skills for agent.
	skills := make(map[string]string)
	skillPath := "agents/skills/triage-issue/SKILL.md"
	if data, err := os.ReadFile(skillPath); err == nil {
		skills["triage-issue"] = string(data)
		slog.Info("skill loaded", "path", skillPath)
	} else {
		slog.Warn("skill not found, agents will run without skill", "path", skillPath, "error", err)
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
			slog.Error("failed to connect to Redis", "error", err)
			os.Exit(1)
		}
		bundle = queue.NewRedisBundle(rdb, jobStore, "triage")
		slog.Info("using Redis transport", "addr", cfg.Redis.Addr)
	default:
		bundle = queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
		slog.Info("using in-memory transport")
	}

	// Collect skill dirs from all agents in fallback chain.
	seen := make(map[string]bool)
	var skillDirs []string
	for _, name := range cfg.Fallback {
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
			slog.Error("failed to start local adapter", "error", err)
			os.Exit(1)
		}
	}

	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, coordinator, jobStore, bundle.Attachments, skills)

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

	issueClient := ghclient.NewIssueClient(cfg.GitHub.Token)
	resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments, &slackPosterAdapter{client: slackClient}, issueClient)
	go resultListener.Listen(context.Background())

	statusListener := bot.NewStatusListener(bundle.Status, jobStore)
	go statusListener.Listen(context.Background())

	// Job watchdog — detect stuck jobs and notify Slack.
	slackAdapter := &slackPosterAdapter{client: slackClient}
	watchdog := queue.NewWatchdog(jobStore, bundle.Commands, queue.WatchdogConfig{
		JobTimeout:     cfg.Queue.JobTimeout,
		IdleTimeout:    cfg.Queue.AgentIdleTimeout,
		PrepareTimeout: cfg.Queue.PrepareTimeout,
	}, func(job *queue.Job, status queue.JobStatus, reason string) {
		msg := queue.FormatStuckMessage(job, status, reason)
		slackAdapter.PostMessage(job.ChannelID, msg, job.ThreadTS)
		// Also clear dedup so user can re-trigger.
		handler.ClearThreadDedup(job.ChannelID, job.ThreadTS)
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

	slog.Info("starting bot", "version", version)

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
		slog.Error("socket mode error", "error", err)
		os.Exit(1)
	}
}

// agentRunnerAdapter wraps AgentRunner to satisfy worker.Runner interface.
type agentRunnerAdapter struct {
	runner *bot.AgentRunner
}

func (a *agentRunnerAdapter) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	return a.runner.Run(ctx, slog.Default(), workDir, prompt, opts)
}

// repoCacheAdapter wraps RepoCache to satisfy worker.RepoProvider interface.
type repoCacheAdapter struct {
	cache *ghclient.RepoCache
}

func (a *repoCacheAdapter) Prepare(cloneURL, branch string) (string, error) {
	repoPath, err := a.cache.EnsureRepo(cloneURL)
	if err != nil {
		return "", err
	}
	if branch != "" {
		if err := a.cache.Checkout(repoPath, branch); err != nil {
			return "", err
		}
	}
	return repoPath, nil
}

// slackPosterAdapter wraps slackclient.Client to satisfy bot.SlackPoster interface.
// SlackPoster.PostMessage has no return value, but Client.PostMessage returns error.
type slackPosterAdapter struct {
	client *slackclient.Client
}

func (a *slackPosterAdapter) PostMessage(channelID, text, threadTS string) {
	if err := a.client.PostMessage(channelID, text, threadTS); err != nil {
		slog.Warn("failed to post slack message", "channel", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) UpdateMessage(channelID, messageTS, text string) {
	if err := a.client.UpdateMessage(channelID, messageTS, text); err != nil {
		slog.Warn("failed to update slack message", "channel", channelID, "error", err)
	}
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
