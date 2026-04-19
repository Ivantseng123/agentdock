# Configuration

[繁體中文](configuration.md)

AgentDock v2 splits config into two files:

- [App configuration (configuration-app.en.md)](configuration-app.en.md) — Slack bot, channels, rate limiting, Mantis, prompt assembly
- [Worker configuration (configuration-worker.en.md)](configuration-worker.en.md) — agents, providers, worker count, repo cache

Upgrading from v1? See [MIGRATION-v2.en.md](MIGRATION-v2.en.md).

## Quick start

```bash
agentdock init app -i       # create ~/.config/agentdock/app.yaml, prompts for Slack/GitHub/Redis
agentdock init worker -i    # create ~/.config/agentdock/worker.yaml, prompts for GitHub/Redis/secret/providers
```

Then start the two processes:

```bash
agentdock app -c ~/.config/agentdock/app.yaml
agentdock worker -c ~/.config/agentdock/worker.yaml
```

`queue.transport` must match on both sides (only `redis` is supported today), and `secret_key` must be identical on both sides.
