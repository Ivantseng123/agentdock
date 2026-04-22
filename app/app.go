// Package app is the module entry point for the agentdock app process (Slack
// orchestrator). cmd/agentdock/app.go wraps Run with cobra setup.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/app/bot"
	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/app/skill"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Build info propagated from cmd at link time (goreleaser -X flags target
// main.*; cmd/agentdock copies those values into these vars before Run
// executes so startup logs report the correct build).
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Handle is returned by Run. Call Wait to block on shutdown.
type Handle struct {
	done <-chan error
}

// Wait blocks until the socket-mode loop exits.
func (h *Handle) Wait() error {
	return <-h.done
}

// Run initializes the app runtime (logging, Slack, Redis buses, listeners,
// HTTP endpoints, socket-mode loop) and returns a Handle immediately. The
// caller must call Wait to block until shutdown.
func Run(cfg *config.Config, identity bot.Identity) (*Handle, error) {
	slog.SetDefault(slog.New(logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	stderrHandler := logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: logging.ParseLevel(cfg.LogLevel)})

	rotator, err := logging.NewRotator(cfg.Logging.Dir)
	if err != nil {
		return nil, fmt.Errorf("failed to init log rotator: %w", err)
	}
	rotator.StartCleanup(cfg.Logging.RetentionDays)

	fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: logging.ParseLevel(cfg.Logging.Level)})
	slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))

	appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)
	githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
	slackLogger := logging.ComponentLogger(slog.Default(), logging.CompSlack)
	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	queueLogger := logging.ComponentLogger(slog.Default(), logging.CompQueue)
	agentLogger := logging.ComponentLogger(slog.Default(), logging.CompAgent)

	slackClient := slackclient.NewClient(cfg.Slack.BotToken, slackLogger)

	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)
	repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token, githubLogger)

	if cfg.AutoBind {
		go func() {
			if _, err := repoDiscovery.ListRepos(context.Background()); err != nil {
				appLogger.Warn("Repo 快取預熱失敗", "phase", "失敗", "error", err)
			}
		}()
	}

	bakedInDir := "agents/skills"
	if _, err := os.Stat("/opt/agents/skills"); err == nil {
		bakedInDir = "/opt/agents/skills"
	}
	skillLogger := logging.ComponentLogger(slog.Default(), logging.CompSkill)
	skillLoader, err := skill.NewLoader(cfg.SkillsConfig, bakedInDir, skillLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create skill loader: %w", err)
	}
	skillLoader.Warmup(context.Background())
	if cfg.SkillsConfig != "" {
		if _, err := skillLoader.StartWatcher(cfg.SkillsConfig); err != nil {
			appLogger.Warn("Skill 設定監視器啟動失敗", "phase", "失敗", "error", err)
		}
	}

	jobStore := queue.NewMemJobStore()
	jobStore.StartCleanup(1 * time.Hour)

	// Transport selection. New backends (e.g. github-runner) add a case here;
	// flag + config validator already allow any value that reaches this switch.
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
			return nil, fmt.Errorf("failed to connect to Redis: %w", err)
		}
		bundle = queue.NewRedisBundle(rdb, jobStore, "triage")
		appLogger.Info("已連線至 Redis", "phase", "處理中", "addr", cfg.Redis.Addr)

		sk, err := crypto.DecodeSecretKey(cfg.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("invalid secret_key: %w", err)
		}
		if err := crypto.WriteBeacon(context.Background(), rdb, sk); err != nil {
			return nil, fmt.Errorf("failed to write secret beacon: %w", err)
		}
		appLogger.Info("Secret beacon 已寫入 Redis", "phase", "完成")
	default:
		return nil, fmt.Errorf("unsupported queue.transport %q (supported: redis)", cfg.Queue.Transport)
	}

	coordinator := queue.NewCoordinator(bundle.Queue,
		queue.WithCoordinatorSubmitHook(func(priority string) {
			metrics.QueueSubmittedTotal.WithLabelValues(priority).Inc()
		}),
	)
	coordinator.RegisterQueue("triage", bundle.Queue)

	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader, identity)

	handler := slackclient.NewHandler(slackclient.HandlerConfig{
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
		}, agentLogger)
	go resultListener.Listen(context.Background())

	retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient, logger: slackLogger}, workerLogger)

	statusListener := bot.NewStatusListener(bundle.Status, jobStore, slackClient, queueLogger)
	go statusListener.Listen(context.Background())

	resultListener.SetStatusJobClearer(statusListener.ClearJob)

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
	go watchdog.Start(make(chan struct{}))

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

	api := slack.New(cfg.Slack.BotToken, slack.OptionAppLevelToken(cfg.Slack.AppToken))
	sm := socketmode.New(api)

	appLogger.Info("Bot 身份已解析", "phase", "處理中",
		"user_id", identity.UserID, "bot_id", identity.BotID)

	appLogger.Info("啟動 Bot", "phase", "處理中", "version", Version, "commit", Commit, "date", Date)

	go func() {
		for evt := range sm.Events {
			handleSocketEvent(evt, sm, handler, wf, slackClient, jobStore, bundle, retryHandler, cfg, identity.UserID, appLogger)
		}
	}()

	done := make(chan error, 1)
	go func() {
		if err := sm.Run(); err != nil {
			done <- fmt.Errorf("socket mode error: %w", err)
			return
		}
		done <- nil
	}()

	return &Handle{done: done}, nil
}

// handleSocketEvent dispatches a single socketmode event. Extracted to keep
// Run shorter; behaviour is unchanged from the previous inline switch.
func handleSocketEvent(
	evt socketmode.Event,
	sm *socketmode.Client,
	handler *slackclient.Handler,
	wf *bot.Workflow,
	slackClient *slackclient.Client,
	jobStore queue.JobStore,
	bundle *queue.Bundle,
	retryHandler *bot.RetryHandler,
	cfg *config.Config,
	botUserID string,
	appLogger *slog.Logger,
) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		sm.Ack(*evt.Request)
		ea, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
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
			return
		}
		if cmd.ChannelID == "" {
			return
		}
		slackClient.PostMessage(cmd.ChannelID,
			":point_right: 請在對話串中使用 `@bot` 來觸發 triage，或直接在 thread 中 mention bot。\n`/triage` 指令目前不支援 thread 偵測。", "")
	case socketmode.EventTypeInteractive:
		cb, ok := evt.Data.(slack.InteractionCallback)
		if !ok {
			sm.Ack(*evt.Request)
			return
		}
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
			return
		}
		sm.Ack(*evt.Request)
		handleInteraction(cb, wf, slackClient, handler, jobStore, bundle, retryHandler, appLogger)
	}
}

func handleInteraction(
	cb slack.InteractionCallback,
	wf *bot.Workflow,
	slackClient *slackclient.Client,
	handler *slackclient.Handler,
	jobStore queue.JobStore,
	bundle *queue.Bundle,
	retryHandler *bot.RetryHandler,
	appLogger *slog.Logger,
) {
	switch cb.Type {
	case slack.InteractionTypeBlockActions:
		if len(cb.ActionCallback.BlockActions) == 0 {
			return
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
		case action.ActionID == "back_to_repo":
			wf.HandleBackToRepo(cb.Channel.ID, selectorTS)
		case action.ActionID == "retry_job":
			retryHandler.Handle(cb.Channel.ID, action.Value, selectorTS)
		case strings.HasPrefix(action.ActionID, "cancel_job"):
			jobID := action.Value
			state, err := jobStore.Get(jobID)
			if err == nil &&
				state.Status != queue.JobFailed &&
				state.Status != queue.JobCompleted &&
				state.Status != queue.JobCancelled {
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
