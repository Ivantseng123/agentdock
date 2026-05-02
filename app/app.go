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
	"github.com/Ivantseng123/agentdock/app/dispatch"
	"github.com/Ivantseng123/agentdock/app/githubapp"
	"github.com/Ivantseng123/agentdock/app/skill"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/app/workflow"
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
	githubAppLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHubApp)
	slackLogger := logging.ComponentLogger(slog.Default(), logging.CompSlack)
	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	queueLogger := logging.ComponentLogger(slog.Default(), logging.CompQueue)
	agentLogger := logging.ComponentLogger(slog.Default(), logging.CompAgent)

	tokenSource, err := githubapp.NewFromConfig(cfg.GitHub, githubAppLogger)
	if err != nil {
		return nil, fmt.Errorf("init github auth: %w", err)
	}

	slackClient := slackclient.NewClient(cfg.Slack.BotToken, slackLogger)

	repoCache := ghclient.NewRepoCacheWithTokenFn(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, tokenSource.Get, githubLogger)
	repoDiscovery := ghclient.NewRepoDiscovery(tokenSource.Get, githubLogger)

	if cfg.AutoBind {
		go func() {
			if _, err := repoDiscovery.ListRepos(context.Background()); err != nil {
				appLogger.Warn("Repo 快取預熱失敗", "phase", "失敗", "error", err)
			}
		}()
	}

	bakedInDir := "app/agents/skills"
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

	// Transport + JobStore selection. The two switches are intentionally
	// adjacent and symmetric — both exist to let operators pick backends
	// without code changes. Today "redis" is the only supported transport,
	// but the switch stays so adding a new backend (e.g. github-runner) is
	// additive. JobStore defaults to "mem" for back-compat; set
	// queue.store: redis to persist state across app restarts (#123, #146).
	var (
		bundle   *queue.Bundle
		jobStore queue.JobStore
	)
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

		switch cfg.Queue.Store {
		case "mem":
			ms := queue.NewMemJobStore()
			ms.StartCleanup(1 * time.Hour)
			jobStore = ms
		case "redis":
			rs := queue.NewRedisJobStore(rdb, "jobstore", cfg.Queue.StoreTTL)
			// Rehydrate: log the count of jobs that were still live in the
			// previous instance (TTL hadn't evicted them). ResultListener
			// resolves jobs via store.Get directly against Redis, so no
			// in-memory index needs rebuilding. Terminal-state jobs are
			// left to TTL — we do not proactively delete them.
			rehydrateCtx, rehydrateCancel := context.WithTimeout(context.Background(), 10*time.Second)
			states, listErr := rs.ListAll(rehydrateCtx)
			rehydrateCancel()
			if listErr != nil {
				appLogger.Warn("JobStore 重新水合失敗（ListAll）", "phase", "失敗", "error", listErr)
			} else {
				inflight := 0
				for _, st := range states {
					switch st.Status {
					case queue.JobCompleted, queue.JobFailed, queue.JobCancelled:
					default:
						inflight++
					}
				}
				appLogger.Info("rehydrated in-flight jobs from previous instance",
					"phase", "完成", "in_flight", inflight, "total_records", len(states))
			}
			jobStore = rs
		default:
			return nil, fmt.Errorf("unsupported queue.store %q (supported: mem, redis)", cfg.Queue.Store)
		}

		bundle = queue.NewRedisBundle(rdb, jobStore, "triage",
			queue.WithRedisJobQueueLogger(queueLogger))
		appLogger.Info("已連線至 Redis", "phase", "處理中",
			"addr", cfg.Redis.Addr, "jobstore", cfg.Queue.Store)

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

	// Decode secret key for job encryption (used in submitJob below).
	var secretKey []byte
	if cfg.SecretKey != "" {
		var skErr error
		secretKey, skErr = crypto.DecodeSecretKey(cfg.SecretKey)
		if skErr != nil {
			appLogger.Warn("secret_key 解碼失敗，secret 加密功能停用", "phase", "失敗", "error", skErr)
		}
	}

	issueClient := ghclient.NewIssueClient(tokenSource.Get, githubLogger)
	githubClient := ghclient.NewClient(tokenSource.Get)

	// Build workflow registry + dispatcher. The slack adapter owns the bot
	// identity so FetchThreadContext always drops our own posts regardless of
	// which workflow fires it.
	slackPort := &slackAdapterPort{client: slackClient, logger: slackLogger, identity: identity}
	issueWorkflow := workflow.NewIssueWorkflow(cfg, slackPort, issueClient, repoCache, repoDiscovery, agentLogger)
	askWorkflow := workflow.NewAskWorkflow(cfg, slackPort, repoCache, agentLogger)
	prReviewWorkflow := workflow.NewPRReviewWorkflow(cfg, slackPort, githubClient, repoCache, agentLogger)
	reg := workflow.NewRegistry()
	reg.Register(issueWorkflow)
	reg.Register(askWorkflow)
	reg.Register(prReviewWorkflow)
	dispatcher := workflow.NewDispatcher(reg, slackPort, appLogger)

	availability := queue.NewWorkerAvailability(coordinator, jobStore, queue.AvailabilityConfig{
		AvgJobDuration: cfg.Availability.AvgJobDuration,
	},
		queue.WithVerdictHook(func(kind, stage string, d time.Duration) {
			metrics.WorkerAvailabilityVerdictTotal.WithLabelValues(kind, stage).Inc()
			metrics.WorkerAvailabilityCheckDuration.Observe(d.Seconds())
		}),
		queue.WithDepErrorHook(func(dep string) {
			metrics.WorkerAvailabilityCheckErrors.WithLabelValues(dep).Inc()
		}),
	)

	wf := bot.NewWorkflow(cfg, dispatcher, slackPort, repoDiscovery, repoCache, appLogger, availability)

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

	// submitJob is the queue-submission closure wired to the workflow shim.
	// It mirrors the old Workflow.runTriage logic, now accepting a *workflow.Pending
	// and calling BuildJob on the matching registered workflow.
	submitJob := func(ctx context.Context, p *workflow.Pending) {
		wfImpl, ok := reg.Get(p.TaskType)
		if !ok {
			appLogger.Error("submitJob: unknown task_type", "phase", "失敗", "task_type", p.TaskType)
			_ = slackPort.PostMessage(p.ChannelID, ":x: internal error: unknown workflow type", p.ThreadTS)
			return
		}

		job, statusText, err := wfImpl.BuildJob(ctx, p)
		if err != nil {
			appLogger.Error("BuildJob failed", "phase", "失敗", "error", err)
			_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: %v", err), p.ThreadTS)
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}

		// Append worker-availability busy hint if the pre-submit check set one.
		if p.BusyHint != "" {
			statusText += " " + p.BusyHint
		}

		// Post lifecycle status message.
		statusMsgTS, postErr := slackPort.PostMessageWithTS(p.ChannelID, statusText, p.ThreadTS)
		if postErr != nil {
			appLogger.Warn("狀態訊息發送失敗", "phase", "失敗", "error", postErr)
			statusMsgTS = ""
		}

		// Fetch thread context. slackPort knows the bot identity and drops
		// our own posts internally.
		rawMsgs, err := slackPort.FetchThreadContext(p.ChannelID, p.ThreadTS, p.TriggerTS, cfg.MaxThreadMessages)
		if err != nil {
			appLogger.Error("Failed to read thread", "phase", "失敗", "error", err)
			_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: Failed to read thread: %v", err), p.ThreadTS)
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}

		// Shape messages for the queue. (Mantis enrichment is now the agent's
		// job via the mantis skill + MANTIS_API_* env vars.)
		var threadMsgs []queue.ThreadMessage
		for _, m := range rawMsgs {
			threadMsgs = append(threadMsgs, queue.ThreadMessage{
				User:      slackPort.ResolveUser(m.User),
				Timestamp: m.Timestamp,
				Text:      m.Text,
			})
		}

		if len(threadMsgs) == 0 {
			_ = slackPort.PostMessage(p.ChannelID, ":x: Thread has no messages to process", p.ThreadTS)
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}

		// Download attachments.
		tempDir, tempErr := os.MkdirTemp("", "triage-meta-*")
		if tempErr != nil {
			_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: Failed to create temp dir: %v", tempErr), p.ThreadTS)
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}
		defer os.RemoveAll(tempDir)

		downloads := slackPort.DownloadAttachments(rawMsgs, tempDir)

		// Build attachment metadata and payloads.
		var attachMeta []queue.AttachmentMeta
		var attachPayloads []queue.AttachmentPayload
		for _, d := range downloads {
			if d.Failed {
				continue
			}
			attachMeta = append(attachMeta, queue.AttachmentMeta{
				Filename: d.Name,
				MimeType: d.Type,
			})
			data, readErr := os.ReadFile(d.Path)
			if readErr != nil {
				appLogger.Warn("Failed to read attachment", "name", d.Name, "error", readErr)
				continue
			}
			attachPayloads = append(attachPayloads, queue.AttachmentPayload{
				Filename: d.Name,
				MimeType: d.Type,
				Data:     data,
				Size:     int64(len(data)),
			})
		}

		// Enrich job with thread context and attachments. BotName lets the
		// agent self-refer using the actual Slack handle instead of inventing
		// persona labels like "@bot ask 助理".
		if job.PromptContext != nil {
			job.PromptContext.ThreadMessages = threadMsgs
			job.PromptContext.BotName = identity.Username
		}
		job.Attachments = attachMeta
		job.Skills = loadSkills(ctx, skillLoader, appLogger)

		// Cross-installation guard: if the App isn't installed at the
		// primary repo's owner, fall back to PAT for this job (warn) or
		// fail loudly (no PAT). PAT mode's IsAccessible always returns
		// true, so this branch is a no-op outside App mode.
		jobSource := tokenSource
		if !tokenSource.IsAccessible(job.Repo) {
			if cfg.GitHub.Token == "" {
				owner := job.Repo
				if i := strings.Index(owner, "/"); i > 0 {
					owner = owner[:i]
				}
				appLogger.Error("App not installed at owner and no PAT configured",
					"phase", "失敗",
					"owner", owner,
					"repo", job.Repo,
				)
				_ = slackPort.PostMessage(p.ChannelID,
					fmt.Sprintf(":x: GitHub App not installed at owner=%s; install at the org or set github.token", owner),
					p.ThreadTS)
				if handler != nil {
					handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
				}
				return
			}
			appLogger.Warn("App not installed at owner, falling back to PAT",
				"phase", "降級",
				"repo", job.Repo,
			)
			jobSource = githubapp.NewPATSource(cfg.GitHub.Token)
		}

		// Per-job secrets: fork cfg.Secrets, MintFresh GH_TOKEN, encrypt.
		// In PAT mode this is byte-for-byte equivalent to the legacy
		// inline marshal/encrypt path; in App mode it ensures the worker
		// receives a token close to the full 60min TTL.
		if len(secretKey) > 0 {
			encrypted, eErr := dispatch.BuildEncryptedSecrets(cfg, jobSource, secretKey)
			if eErr != nil {
				_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: Failed to prepare secrets: %v", eErr), p.ThreadTS)
				if handler != nil {
					handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
				}
				return
			}
			job.EncryptedSecrets = encrypted
		}

		// Submit to queue.
		if err := coordinator.Submit(ctx, job); err != nil {
			if err == queue.ErrQueueFull {
				_ = slackPort.PostMessage(p.ChannelID, ":warning: 系統忙碌，請稍後再試", p.ThreadTS)
			} else {
				_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: Failed to submit job: %v", err), p.ThreadTS)
			}
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}

		// Prepare attachment payloads so workers can retrieve them.
		if len(attachPayloads) > 0 {
			if err := bundle.Attachments.Prepare(ctx, job.ID, attachPayloads); err != nil {
				appLogger.Error("附件上傳至 Redis 失敗", "phase", "失敗", "error", err)
				jobStore.UpdateStatus(ctx, job.ID, queue.JobFailed)
				bundle.Results.Publish(ctx, &queue.JobResult{
					JobID:      job.ID,
					Status:     "failed",
					Error:      fmt.Sprintf("attachment prepare failed: %v", err),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return
			}
		}

		// Update the status message to show queue position.
		//
		// BusyHint is only set when CheckHard saw all worker slots occupied, so
		// saying "正在處理" in that case contradicts the wait-estimate tail. When
		// busy, the head is queued regardless of its position in the pending
		// list (the occupant worker holds a non-pending job).
		pos, _ := coordinator.QueuePosition(job.ID)
		var queueMsg string
		switch {
		case pos > 1:
			queueMsg = fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1)
		case p.BusyHint != "":
			queueMsg = ":hourglass_flowing_sand: 已加入排隊，將盡快處理"
		default:
			queueMsg = ":hourglass_flowing_sand: 正在處理你的請求..."
		}
		if p.BusyHint != "" {
			queueMsg += " " + p.BusyHint
		}

		if statusMsgTS != "" {
			if upErr := slackPort.UpdateMessageWithButton(p.ChannelID, statusMsgTS,
				queueMsg, "cancel_job", "取消", job.ID); upErr != nil {
				appLogger.Warn("狀態訊息更新失敗，改為另起新訊息", "phase", "失敗", "error", upErr)
				statusMsgTS = ""
			}
		}
		if statusMsgTS == "" {
			if ts, pErr := slackPort.PostMessageWithButton(p.ChannelID,
				queueMsg, p.ThreadTS, "cancel_job", "取消", job.ID); pErr == nil {
				statusMsgTS = ts
			}
		}
		if statusMsgTS != "" {
			job.StatusMsgTS = statusMsgTS
			jobStore.Put(ctx, job)
		}
	}
	wf.SetSubmitHook(submitJob)
	resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
		&slackPosterAdapter{client: slackClient, logger: slackLogger}, reg,
		func(channelID, threadTS string) {
			handler.ClearThreadDedup(channelID, threadTS)
		}, agentLogger)
	go resultListener.Listen(context.Background())

	retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient, logger: slackLogger}, workerLogger, cfg, tokenSource, secretKey)

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

	// One goroutine per event: Slack BlockSuggestion responses have a ~3s
	// server-side deadline, so a single slow event (PostMessage, thread
	// fetch, selector update) in front of type-ahead events makes the
	// external selector silently render "no results" until the queue
	// drains. All shared state (handler, workflow pending map, retry
	// handler) is mu-protected, so parallel dispatch is safe.
	go func() {
		for evt := range sm.Events {
			evt := evt
			go handleSocketEvent(evt, sm, handler, wf, slackClient, jobStore, bundle, retryHandler, cfg, identity.UserID, appLogger)
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
			// ackSuggestions builds an OptionsResponse from a list of strings
			// and ACKs the BlockSuggestion request. Shared by every
			// external_select handler below.
			ackSuggestions := func(label string, options []string) {
				appLogger.Info(label, "phase", "處理中", "query", cb.Value, "count", len(options))
				opts := make([]*slack.OptionBlockObject, 0, len(options))
				for _, v := range options {
					opts = append(opts, slack.NewOptionBlockObject(v, slack.NewTextBlockObject("plain_text", v, false, false), nil))
				}
				sm.Ack(*evt.Request, slack.OptionsResponse{Options: opts})
			}
			switch cb.ActionID {
			case "repo_search", "ask_repo":
				// Issue uses "repo_search"; Ask uses "ask_repo" (fallback
				// when the channel has no repos configured).
				ackSuggestions("Repo 搜尋結果", wf.HandleRepoSuggestion(cb.Value))
			case "ask_ref", "issue_ref":
				// Ref repo type-ahead — same shape as ask_repo but the
				// handler also filters out primary + already-picked refs
				// via the pending state's RefExclusionReader. Both Ask and
				// Issue route here; askState / issueState both implement
				// the interface.
				ackSuggestions("Ref repo 搜尋結果", wf.HandleRefRepoSuggestion(cb.Container.MessageTs, cb.Value))
			case "branch_select", "ask_branch", "ask_ref_branch", "issue_ref_branch":
				// Branch selector auto-upgrades to external_select when a
				// repo has >100 branches (issue #153). The handler resolves
				// the pending by selector ts and reads BranchSelectedRepo()
				// from state — both askState and issueState toggle their
				// BranchTargetRepo across primary vs ref phases so this
				// single handler serves both workflows.
				ackSuggestions("Branch 搜尋結果", wf.HandleBranchSuggestion(cb.Container.MessageTs, cb.Value))
			default:
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
		case strings.HasPrefix(action.ActionID, "description_action"):
			wf.HandleDescriptionAction(cb.Channel.ID, action.Value, selectorTS, cb.TriggerID)
		case action.ActionID == "back_to_repo":
			wf.HandleBackToRepo(cb.Channel.ID, selectorTS)
		case action.ActionID == "retry_job":
			retryHandler.Handle(cb.Channel.ID, action.Value, selectorTS)
		case strings.HasPrefix(action.ActionID, "cancel_job"):
			jobID := action.Value
			cancelCtx, cancelCancel := context.WithTimeout(context.Background(), queue.DefaultStoreOpTimeout)
			state, err := jobStore.Get(cancelCtx, jobID)
			if err == nil &&
				state.Status != queue.JobFailed &&
				state.Status != queue.JobCompleted &&
				state.Status != queue.JobCancelled {
				jobStore.UpdateStatus(cancelCtx, jobID, queue.JobCancelled)
				bundle.Commands.Send(cancelCtx, queue.Command{JobID: jobID, Action: "kill"})
				slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
				handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
			} else {
				slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
			}
			cancelCancel()
		default:
			// Any other selector/button routes through the dispatcher via
			// HandleSelection. External selectors (repo_search, ask_repo
			// when rendered as a search menu) carry the pick in
			// SelectedOption.Value; buttons carry it in action.Value.
			// cb.TriggerID is forwarded so workflows can return
			// NextStepOpenModal in response (e.g. pr_review_confirm "manual").
			value := action.Value
			if action.SelectedOption.Value != "" {
				value = action.SelectedOption.Value
			}
			wf.HandleSelection(cb.Channel.ID, action.ActionID, value, selectorTS, cb.TriggerID)
		}
	case slack.InteractionTypeViewSubmission:
		meta := cb.View.PrivateMetadata
		// Each workflow picks its own ModalInputName (Issue/Ask: "description",
		// PR Review: "pr_url"), which becomes the action_id; the block_id is
		// "<name>_block". Iterate instead of hardcoding so one modal handler
		// serves every workflow.
		wf.HandleDescriptionSubmit(meta, firstModalValue(cb.View.State.Values))
	case slack.InteractionTypeViewClosed:
		meta := cb.View.PrivateMetadata
		wf.HandleModalClosed(meta)
	}
}

// firstModalValue returns the single text input from a modal's State.Values.
// Every modal opened by OpenTextInputModal has one input block with one
// action, so iteration order doesn't matter — we take whatever is there.
// Returns "" when the modal is empty or malformed.
func firstModalValue(values map[string]map[string]slack.BlockAction) string {
	for _, block := range values {
		for _, v := range block {
			return v.Value
		}
	}
	return ""
}

// loadSkills loads the current skill set from the skill loader. Returns nil on
// error so callers can treat a missing skill-set as "no skills" gracefully.
func loadSkills(ctx context.Context, loader *skill.Loader, logger *slog.Logger) map[string]*queue.SkillPayload {
	if loader == nil {
		return nil
	}
	skills, err := loader.LoadAll(ctx)
	if err != nil {
		logger.Warn("載入 skill 失敗", "phase", "失敗", "error", err)
		return nil
	}
	return skills
}
