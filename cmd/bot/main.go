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

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/config"
	"slack-issue-bot/internal/diagnosis"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/llm"
	"slack-issue-bot/internal/mantis"
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
	repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token)
	// Pre-warm repo cache so first user doesn't wait
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		repos, err := repoDiscovery.ListRepos(ctx)
		if err != nil {
			slog.Warn("failed to pre-warm repo cache", "error", err)
		} else {
			slog.Info("repo cache warmed", "count", len(repos))
		}
	}()

	var entries []llm.ChatProviderEntry
	for _, p := range cfg.LLM.Providers {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = cfg.LLM.Timeout
		}
		var provider llm.ConversationProvider
		switch p.Name {
		case "claude":
			provider = llm.NewClaudeProvider(p.APIKey, p.Model, p.BaseURL, timeout)
		case "openai":
			provider = llm.NewOpenAIProvider(p.APIKey, p.Model, p.BaseURL, timeout)
		case "ollama":
			provider = llm.NewOllamaProvider(p.Model, p.BaseURL, timeout)
		case "cli":
			provider = llm.NewCLIProvider(p.Name, p.Command, p.Args, timeout)
		default:
			slog.Warn("unknown LLM provider, skipping", "name", p.Name)
			continue
		}
		slog.Info("loaded LLM provider", "name", p.Name, "max_retries", p.MaxRetries)
		entries = append(entries, llm.ChatProviderEntry{Provider: provider, MaxRetries: p.MaxRetries})
	}
	slog.Info("LLM fallback chain ready", "providers", len(entries))
	fallbackChain := llm.NewChatFallbackChain(entries)

	diagEngine := diagnosis.NewEngine(fallbackChain, diagnosis.EngineConfig{
		MaxFiles:  10,
		MaxTurns:  cfg.Diagnosis.MaxTurns,
		MaxTokens: cfg.Diagnosis.MaxTokens,
		CacheTTL:  cfg.Diagnosis.CacheTTL,
	})

	mantisClient := mantis.NewClient(
		cfg.Integrations.Mantis.BaseURL,
		cfg.Integrations.Mantis.APIToken,
		cfg.Integrations.Mantis.Username,
		cfg.Integrations.Mantis.Password,
	)
	if mantisClient.IsConfigured() {
		slog.Info("mantis integration enabled", "url", cfg.Integrations.Mantis.BaseURL)
	}

	wf := bot.NewWorkflow(cfg, sc, issueClient, repoCache, repoDiscovery, diagEngine, mantisClient)

	slackHandler := slackclient.NewHandler(slackclient.HandlerConfig{
		MaxConcurrent:   5,
		DedupTTL:        5 * time.Minute,
		PerUserLimit:    cfg.RateLimit.PerUser,
		PerChannelLimit: cfg.RateLimit.PerChannel,
		RateWindow:      cfg.RateLimit.Window,
		OnEvent:         wf.HandleReaction,
		OnRejected: func(event slackclient.ReactionEvent, reason string) {
			sc.PostMessage(event.ChannelID, fmt.Sprintf(":no_entry: %s — please wait before triggering again.", reason), event.MessageTS)
		},
	})
	wf.SetHandler(slackHandler)

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
						slackHandler.HandleReaction(slackclient.ReactionEvent{
							EventID:   evt.Request.EnvelopeID,
							Reaction:  ev.Reaction,
							ChannelID: ev.Item.Channel,
							MessageTS: ev.Item.Timestamp,
							UserID:    ev.User,
						})
					case *slackevents.MemberJoinedChannelEvent:
						if cfg.AutoBind {
							// Check if the joining member is our bot
							authTest, err := api.AuthTest()
							if err == nil && ev.User == authTest.UserID {
								wf.RegisterChannel(ev.Channel)
							}
						}
					case *slackevents.MemberLeftChannelEvent:
						if cfg.AutoBind {
							authTest, err := api.AuthTest()
							if err == nil && ev.User == authTest.UserID {
								wf.UnregisterChannel(ev.Channel)
							}
						}
					}
				}

			case socketmode.EventTypeInteractive:
				callback, ok := evt.Data.(slack.InteractionCallback)
				if !ok {
					socketClient.Ack(*evt.Request)
					continue
				}

				switch callback.Type {
				case slack.InteractionTypeBlockSuggestion:
					// Type-ahead repo search
					query := callback.Value
					repos := wf.HandleRepoSuggestion(query)
					var options []*slack.OptionBlockObject
					for _, r := range repos {
						options = append(options, slack.NewOptionBlockObject(
							r,
							slack.NewTextBlockObject(slack.PlainTextType, r, false, false),
							nil,
						))
					}
					resp := map[string]any{"options": options}
					socketClient.Ack(*evt.Request, resp)

				case slack.InteractionTypeBlockActions:
					socketClient.Ack(*evt.Request)
					channelID := callback.Channel.ID
					if channelID == "" {
						channelID = callback.Container.ChannelID
					}
					msgTS := callback.Message.Timestamp

					for _, action := range callback.ActionCallback.BlockActions {
						slog.Info("interactive callback",
							"action", action.ActionID,
							"value", action.Value,
							"selectedOption", selectedValue(action),
							"channelID", channelID,
							"msgTS", msgTS,
						)

						value := action.Value
						// External select uses SelectedOption instead of Value
						if value == "" && action.SelectedOption.Value != "" {
							value = action.SelectedOption.Value
						}

						if strings.HasPrefix(action.ActionID, "repo_select_") ||
							strings.HasPrefix(action.ActionID, "branch_select_") ||
							action.ActionID == "repo_search" {
							go wf.HandleSelection(channelID, action.ActionID, value, msgTS)
						}

						if strings.HasPrefix(action.ActionID, "description_action_") {
							go wf.HandleDescriptionAction(channelID, value, msgTS, callback.TriggerID)
						}
					}

				case slack.InteractionTypeViewSubmission:
					socketClient.Ack(*evt.Request)
					// Modal submitted — extract text from description input
					selectorMsgTS := callback.View.PrivateMetadata
					extraText := ""
					if block, ok := callback.View.State.Values["description_block"]; ok {
						if input, ok := block["description_input"]; ok {
							extraText = strings.TrimSpace(input.Value)
						}
					}
					slog.Info("modal submitted", "selectorTS", selectorMsgTS, "text_len", len(extraText))
					go wf.HandleDescriptionSubmit(selectorMsgTS, extraText)

				case slack.InteractionTypeViewClosed:
					socketClient.Ack(*evt.Request)
					// Modal closed (user clicked "跳過" / X) — treat as skip
					selectorMsgTS := callback.View.PrivateMetadata
					slog.Info("modal closed", "selectorTS", selectorMsgTS)
					go wf.HandleDescriptionSubmit(selectorMsgTS, "")

				default:
					socketClient.Ack(*evt.Request)
				}
			}
		}
	}()

	slog.Info("bot starting in socket mode",
		"channels", len(cfg.Channels),
		"reactions", len(cfg.Reactions),
		"auto_bind", cfg.AutoBind,
	)
	if err := socketClient.Run(); err != nil {
		slog.Error("socket mode error", "error", err)
		os.Exit(1)
	}
}

func selectedValue(action *slack.BlockAction) string {
	if action.SelectedOption.Value != "" {
		return action.SelectedOption.Value
	}
	return ""
}

