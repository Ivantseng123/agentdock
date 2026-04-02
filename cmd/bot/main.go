package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/config"
	"slack-issue-bot/internal/diagnosis"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/llm"
	slackclient "slack-issue-bot/internal/slack"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	sc := slackclient.NewClient(cfg.Slack.BotToken)

	issueClient := ghclient.NewIssueClient(cfg.GitHub.Token)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token)

	var entries []llm.ProviderEntry
	for _, p := range cfg.LLM.Providers {
		var provider llm.Provider
		switch p.Name {
		case "claude":
			provider = llm.NewClaudeProvider(p.APIKey, p.Model, p.BaseURL, cfg.LLM.Timeout)
		case "openai":
			provider = llm.NewOpenAIProvider(p.APIKey, p.Model, p.BaseURL, cfg.LLM.Timeout)
		case "ollama":
			provider = llm.NewOllamaProvider(p.Model, p.BaseURL, cfg.LLM.Timeout)
		case "cli":
			provider = llm.NewCLIProvider(p.Name, p.Command, p.Args, cfg.LLM.Timeout)
		default:
			slog.Warn("unknown LLM provider, skipping", "name", p.Name)
			continue
		}
		slog.Info("loaded LLM provider", "name", p.Name, "max_retries", p.MaxRetries)
		entries = append(entries, llm.ProviderEntry{Provider: provider, MaxRetries: p.MaxRetries})
	}
	slog.Info("LLM fallback chain ready", "providers", len(entries))
	fallbackChain := llm.NewFallbackChain(entries)

	diagEngine := diagnosis.NewEngine(fallbackChain, 10)

	wf := bot.NewWorkflow(cfg, sc, issueClient, repoCache, diagEngine)

	handler := slackclient.NewHandler(slackclient.HandlerConfig{
		MaxConcurrent:   5,
		DedupTTL:        5 * time.Minute,
		PerUserLimit:    cfg.RateLimit.PerUser,
		PerChannelLimit: cfg.RateLimit.PerChannel,
		RateWindow:      cfg.RateLimit.Window,
		OnEvent:         wf.HandleReaction,
		OnRejected: func(event slackclient.ReactionEvent, reason string) {
			sc.PostMessage(event.ChannelID, fmt.Sprintf(":no_entry: %s — please wait before triggering again.", reason))
		},
	})

	// Health check endpoint
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		addr := fmt.Sprintf(":%d", cfg.Server.Port)
		slog.Info("health check listening", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("health check server error", "error", err)
		}
	}()

	// Socket Mode
	api := slack.New(
		cfg.Slack.BotToken,
		slack.OptionAppLevelToken(cfg.Slack.AppToken),
	)
	socketClient := socketmode.New(api)

	go func() {
		for evt := range socketClient.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				socketClient.Ack(*evt.Request)
				eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				if eventsAPIEvent.Type == slackevents.CallbackEvent {
					innerEvent := eventsAPIEvent.InnerEvent
					switch ev := innerEvent.Data.(type) {
					case *slackevents.ReactionAddedEvent:
						handler.HandleReaction(slackclient.ReactionEvent{
							EventID:   evt.Request.EnvelopeID,
							Reaction:  ev.Reaction,
							ChannelID: ev.Item.Channel,
							MessageTS: ev.Item.Timestamp,
							UserID:    ev.User,
						})
					}
				}

			case socketmode.EventTypeInteractive:
				socketClient.Ack(*evt.Request)
				callback, ok := evt.Data.(slack.InteractionCallback)
				if !ok {
					continue
				}
				if callback.Type == slack.InteractionTypeBlockActions {
					for _, action := range callback.ActionCallback.BlockActions {
						if action.ActionID == wf.RepoSelectCallbackID() {
							go wf.HandleRepoSelection(
								callback.Channel.ID,
								action.Value,
								callback.Message.Timestamp,
							)
						}
					}
				}
			}
		}
	}()

	slog.Info("bot starting in socket mode", "channels", len(cfg.Channels), "reactions", len(cfg.Reactions))
	if err := socketClient.Run(); err != nil {
		slog.Error("socket mode error", "error", err)
		os.Exit(1)
	}
}
