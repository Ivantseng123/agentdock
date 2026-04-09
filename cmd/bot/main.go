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

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/config"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/mantis"
	slackclient "slack-issue-bot/internal/slack"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

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

	mantisClient := mantis.NewClient(
		cfg.Mantis.BaseURL,
		cfg.Mantis.APIToken,
		cfg.Mantis.Username,
		cfg.Mantis.Password,
	)
	if mantisClient.IsConfigured() {
		slog.Info("mantis integration enabled", "url", cfg.Mantis.BaseURL)
	}

	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient)

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

	if cfg.Server.Port > 0 {
		go func() {
			http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			})
			addr := fmt.Sprintf(":%d", cfg.Server.Port)
			slog.Info("health check listening", "addr", addr)
			http.ListenAndServe(addr, nil)
		}()
	}

	api := slack.New(cfg.Slack.BotToken,
		slack.OptionAppLevelToken(cfg.Slack.AppToken),
	)
	sm := socketmode.New(api)

	slog.Info("starting bot v2 (agent architecture)")

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
					if cfg.AutoBind {
						wf.RegisterChannel(inner.Channel)
					}
				case *slackevents.MemberLeftChannelEvent:
					if cfg.AutoBind {
						wf.UnregisterChannel(inner.Channel)
					}
				}

			case socketmode.EventTypeSlashCommand:
				sm.Ack(*evt.Request)
				cmd, ok := evt.Data.(slack.SlashCommand)
				if !ok || cmd.Command != "/triage" {
					continue
				}
				handler.HandleTrigger(slackclient.TriggerEvent{
					ChannelID: cmd.ChannelID,
					ThreadTS:  cmd.ChannelID,
					TriggerTS: "",
					UserID:    cmd.UserID,
					Text:      cmd.Text,
				})

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
