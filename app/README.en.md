# app/

[繁體中文](README.md)

The Slack side of AgentDock. Holds the code for `agentdock app`, published as its own Go module (`github.com/Ivantseng123/agentdock/app`).

## Responsibilities

- Accept Slack events (`@bot <verb>` in a thread)
- Workflow dispatcher: `issue` / `ask` / `review` verbs plus a three-button selector for bare `@bot`
- Workflow-specific UX: repo / branch pickers, PR URL modal, description modal, cancel buttons
- Read thread context, build the prompt, submit to the job queue
- Per-workflow follow-up: create GitHub issue (issue), post answer in thread (ask), report PR review status (review)
- Secret management (AES-256 encrypt before handing off to worker)
- HTTP endpoints: `/healthz`, `/jobs`, `/metrics`
- Watchdog for stuck jobs

App **does not run agent CLIs**; that is worker's job. App and worker always run as separate processes, communicating through whichever backend `queue.transport` selects.

## Configuration

Full schema in [docs/configuration-app.en.md](../docs/configuration-app.en.md).

Scaffold:

```bash
agentdock init app -i -c ~/.config/agentdock/app.yaml
```

Run:

```bash
agentdock app -c ~/.config/agentdock/app.yaml
```

## Mode switching

`queue.transport` selects the queue backend:

- `redis` (only value supported today): Redis streams / pub-sub; app and worker are independent processes.
- Future backends: add a case to the transport switch in `app/app.go`.

## Tests

```bash
(cd app && go test ./... -race)
```

## Dependencies

- `shared/` — queue, logging, crypto, github, configloader, connectivity, prompt
- Does **not** depend on `worker/` (app ✗ worker boundary is enforced)

## See also

- [Top-level README](../README.en.md)
- [worker/README.en.md](../worker/README.en.md)
- [docs/configuration.en.md](../docs/configuration.en.md) — config overview
- [docs/MIGRATION-v2.en.md](../docs/MIGRATION-v2.en.md) — v1 → v2 migration
