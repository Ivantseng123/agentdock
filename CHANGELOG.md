# Changelog

## [0.2.3](https://github.com/Ivantseng123/agentdock/compare/v0.2.2...v0.2.3) (2026-04-14)


### Bug Fixes

* release pipeline — drop gomod.proxy and chain via reusable workflow ([5396afb](https://github.com/Ivantseng123/agentdock/commit/5396afb8593f737133e75965717d67322ebb74a5))

## [0.2.2](https://github.com/Ivantseng123/agentdock/compare/v0.2.1...v0.2.2) (2026-04-14)


### Features

* **bot:** add commit/date version metadata and -version flag ([be441b2](https://github.com/Ivantseng123/agentdock/commit/be441b2ed25743460ad49cb091bfdb9a22fb2fe0))

## [0.2.1](https://github.com/Ivantseng123/agentdock/compare/v0.2.0...v0.2.1) (2026-04-14)


### Features

* add retry handler for button interaction ([0cb813d](https://github.com/Ivantseng123/agentdock/commit/0cb813d30973f3174b13e0e4718255c69b707bbc))
* include hostname in worker ID for visibility ([5e413d2](https://github.com/Ivantseng123/agentdock/commit/5e413d239ff22e506ea30cd6f7b921341834601d))
* retry on failure with Slack button ([2905fd5](https://github.com/Ivantseng123/agentdock/commit/2905fd5558a369f5e1eb6b0db84fedfc39f62601))
* route retry_job button action to RetryHandler ([5984625](https://github.com/Ivantseng123/agentdock/commit/5984625d80e0467514a3815b4ad2d71990b26919))
* unified failure handling with retry button in result listener ([233eef2](https://github.com/Ivantseng123/agentdock/commit/233eef241bfbd3dbf07e0e6c39ef2fe889b1f044))

## [0.2.0](https://github.com/Ivantseng123/react2issue/compare/v0.1.1...v0.2.0) (2026-04-12)


### ⚠ BREAKING CHANGES

* Go module renamed from slack-issue-bot to agentdock. All import paths updated. Redis key prefix changed from r2i: to ad:.

### Features

* rename project from react2issue to AgentDock ([b516820](https://github.com/Ivantseng123/react2issue/commit/b5168204b55021d66e4f6c231dfe827a32466286))

## [0.1.1](https://github.com/Ivantseng123/react2issue/compare/v0.1.0...v0.1.1) (2026-04-12)


### Features

* 6 diagnosis tools (grep, read_file, list_files, read_context, search_code, git_log) ([cc03d30](https://github.com/Ivantseng123/react2issue/commit/cc03d30fdff5c57ffe68f102418c5e041d13b534))
* add ACTIVE_AGENT and FALLBACK env overrides ([3225510](https://github.com/Ivantseng123/react2issue/commit/3225510013d1ce100ed3f901a7652e137b6013f5))
* add Adapter interface and LocalAdapter wrapping worker.Pool ([2c12e04](https://github.com/Ivantseng123/react2issue/commit/2c12e04c6340b83d28850df841e56dab34a3ba60))
* add agent output file writer ([ecefb6f](https://github.com/Ivantseng123/react2issue/commit/ecefb6f409b023ea0b985bd2f8e41be84666e24b))
* add agent output parser with sanitization ([ceb056b](https://github.com/Ivantseng123/react2issue/commit/ceb056b5c7159736ebb58bd3000abd0fa6948b14))
* add AgentRunner with fallback chain ([3bb416c](https://github.com/Ivantseng123/react2issue/commit/3bb416c46a1d8f758b299c1435a20b59f6a65bc9))
* add bot worker subcommand for standalone Redis worker ([7d876d1](https://github.com/Ivantseng123/react2issue/commit/7d876d1bab55207326f80f71b5c6577102c96422))
* add bot workflow orchestrator connecting all modules ([0b336d0](https://github.com/Ivantseng123/react2issue/commit/0b336d03d9295b5e06771458ffcf5840b78a4f0b))
* add branch selection support ([d6bda47](https://github.com/Ivantseng123/react2issue/commit/d6bda47e44939b124cb566a4e824cf6e1864948e))
* add CLI provider for personal AI subscriptions ([f8fad4d](https://github.com/Ivantseng123/react2issue/commit/f8fad4da390a3b23e9c5b25a44d7a9461b3af428))
* add codex and opencode CLIs to Docker image ([20ef16b](https://github.com/Ivantseng123/react2issue/commit/20ef16bbab99dbcd131eda9033ddea3415f5c2de))
* add common Bundle struct with interface-typed fields ([d8c9bfc](https://github.com/Ivantseng123/react2issue/commit/d8c9bfcb62c1987bbd43b3c6b360851001938051))
* add conditional image guidance to agent system prompt ([ff25f0b](https://github.com/Ivantseng123/react2issue/commit/ff25f0bc59845112c7ee5d3efd65a9d86e27fb1e))
* add configurable prompt with language and extra_rules ([2f22901](https://github.com/Ivantseng123/react2issue/commit/2f22901c3dbe85d1e5c5a59340c6ebdc7e20766f))
* add Coordinator as JobQueue decorator with TaskType routing ([c3b0454](https://github.com/Ivantseng123/react2issue/commit/c3b045495d24caa34d9aa8a9deafc69fe3f00540))
* add date-based rotating log writer ([9d4da35](https://github.com/Ivantseng123/react2issue/commit/9d4da3544cf9124d6d50c46fb839144bba196e6e))
* add diagnosis engine that greps repo and calls LLM ([5b47094](https://github.com/Ivantseng123/react2issue/commit/5b47094ec0a6e585fe8968b95db15f6430582582))
* add Dockerfile and .gitignore ([e818d7b](https://github.com/Ivantseng123/react2issue/commit/e818d7b5130b1cb5df3f1dfe20cef25e6907f008))
* add FetchThreadContext and attachment download ([4e7b5a5](https://github.com/Ivantseng123/react2issue/commit/4e7b5a58808033d9c7826b41a6987661272d0e20))
* add git repo cache with shallow clone and auto-pull ([23c476c](https://github.com/Ivantseng123/react2issue/commit/23c476c0eb5946444a592a651051dbef59b92eef))
* add GitHub issue creation with bug/feature body templates ([eaf4a61](https://github.com/Ivantseng123/react2issue/commit/eaf4a6146eaf0553dff5707c5f0a2cc9d528f6c2))
* add go-redis dependency and RedisConfig ([3779efb](https://github.com/Ivantseng123/react2issue/commit/3779efb46c3d484392d077a730db82cd32b49bc3))
* add ImageContent struct and Images field to Message ([4243fc9](https://github.com/Ivantseng123/react2issue/commit/4243fc9342e424cdf9fc875f1e245db203a1746a))
* add k8s deployment manifests and Jenkins CI/CD pipelines ([9d488b4](https://github.com/Ivantseng123/react2issue/commit/9d488b48b30a7787ed2d58de15b7ba72fb0ac90e))
* add LLM provider interface with fallback chain ([78ba5d2](https://github.com/Ivantseng123/react2issue/commit/78ba5d25e6ce181316530bd9f984225d35bdc2d2))
* add LoggingConfig to config ([54678f4](https://github.com/Ivantseng123/react2issue/commit/54678f466759594623d0246f9c1e66d2af4c168b))
* add main entry point with Socket Mode and health check ([f787e40](https://github.com/Ivantseng123/react2issue/commit/f787e40a839b292b57ac28e9cb8ebae4a6a09fcd))
* add MaxTurns, MaxTokens, CacheTTL to DiagnosisConfig ([a84d11c](https://github.com/Ivantseng123/react2issue/commit/a84d11c2a5021218b8d28e51f2bbcadff9f1579b))
* add MultiHandler slog fan-out ([23251bf](https://github.com/Ivantseng123/react2issue/commit/23251bf289cdb59cdc7bdf5748bcc384286d7f5f))
* add OpenAI and Ollama LLM providers ([6f60674](https://github.com/Ivantseng123/react2issue/commit/6f606742f4b99e4e5de652f84b8b84d6fc89defe))
* add project scaffolding and config module with YAML + env override ([c58d7a8](https://github.com/Ivantseng123/react2issue/commit/c58d7a81c0d86e35e0c68351ab67766a8a992230))
* add prompt builder for agent invocation ([ec01ae6](https://github.com/Ivantseng123/react2issue/commit/ec01ae61f5d23714244312cdf81969d71fa5508a))
* add prompt templates and Claude LLM provider ([f86a4ac](https://github.com/Ivantseng123/react2issue/commit/f86a4ac10ab28e128129138dc237606962a84052))
* add rate limiting and lite diagnosis mode ([09e1679](https://github.com/Ivantseng123/react2issue/commit/09e1679d3a9146cf9a9819b0b53a66f7e60c2dc3))
* add Redis AttachmentStore implementation (key with polling) ([fee41f8](https://github.com/Ivantseng123/react2issue/commit/fee41f8125fb529f306926b591d86048e804cb5a))
* add Redis Bundle + transport switch (inmem/redis) in main.go ([5605113](https://github.com/Ivantseng123/react2issue/commit/56051134bb9e9828ac18bf892d621ea0c8d8968a))
* add Redis client helper and test infrastructure ([9b3412b](https://github.com/Ivantseng123/react2issue/commit/9b3412b75bc193518dd74150e3bde6d038a98c9e))
* add Redis CommandBus implementation (Pub/Sub) ([5d63701](https://github.com/Ivantseng123/react2issue/commit/5d6370184047ca19d1b273e729d3ac52e9c7fe3c))
* add Redis JobQueue implementation (Stream + Consumer Group + worker registry) ([de53334](https://github.com/Ivantseng123/react2issue/commit/de5333435b2db591e97ef6ac6bd058936ae61e83))
* add Redis ResultBus implementation (Stream + Consumer Group) ([2d9fcee](https://github.com/Ivantseng123/react2issue/commit/2d9fceef03c3805bf21ce5f6b3ee0ee15efab24c))
* add Redis StatusBus implementation (Pub/Sub) ([cff177c](https://github.com/Ivantseng123/react2issue/commit/cff177ceb5fb427bd22e42b7a24bff47cd2f83b6))
* add request correlation and agent output saving ([af00d50](https://github.com/Ivantseng123/react2issue/commit/af00d5016be81172572c250dbfed1177e2484e14))
* add request ID generation ([4ff8b85](https://github.com/Ivantseng123/react2issue/commit/4ff8b85b06b39cbcf6a6a0542f2a32caf3f89642))
* add Slack client for message fetch, user resolve, and keyword extraction ([6df4fce](https://github.com/Ivantseng123/react2issue/commit/6df4fcedad1f7988597005d4c6b04670bd1d80f0))
* add Slack event handler with dedup and bounded concurrency ([2343ac6](https://github.com/Ivantseng123/react2issue/commit/2343ac6a03a4687c0c0f89c7e72a09e69c23e318))
* add TaskType field to Job for capability routing ([caa162e](https://github.com/Ivantseng123/react2issue/commit/caa162ef296d0c6d54823260795478380bb61943))
* add xlsx parsing with per-sheet row truncation ([6e08b8b](https://github.com/Ivantseng123/react2issue/commit/6e08b8bca051520df62825bbb5a412979ae128f2))
* agent loop system prompt with tool descriptions ([202e82a](https://github.com/Ivantseng123/react2issue/commit/202e82a7da7e6c824e2d77103ae67383a6c9bdc1))
* agent loop with turn limit, forced finish, token budgeting ([42557d1](https://github.com/Ivantseng123/react2issue/commit/42557d1262e24f8dd00a3029b619aea2ae87c64e))
* agent-style diagnosis loop (search → read → analyze) ([8d1e28b](https://github.com/Ivantseng123/react2issue/commit/8d1e28b5b615474ad6499df7c1fb542fd64fcb49))
* attachment support (xlsx, jpg/png vision) + AI title ([5cd5601](https://github.com/Ivantseng123/react2issue/commit/5cd5601e75e8c0f97d473ceb1c6fbc5a1e2d5b2c))
* **bot:** add ResultListener — handles issue creation and Slack posting ([3c0fbc9](https://github.com/Ivantseng123/react2issue/commit/3c0fbc94f7a7dfbeea96c2a218d5de5267047446))
* **bot:** add StatusListener — updates JobStore from worker status reports ([727abcc](https://github.com/Ivantseng123/react2issue/commit/727abcc7360c8fb343b44a7ece97356f0f1edf5c))
* Claude provider vision support with content blocks ([dbf7f2e](https://github.com/Ivantseng123/react2issue/commit/dbf7f2e1ed2639d0ff8e47172ff2ce09f3a97b2a))
* CLI provider vision support with temp files and --file flags ([d3d1647](https://github.com/Ivantseng123/react2issue/commit/d3d1647e35fc95a9d89ed980c0be808f7c706ac3))
* **config:** add queue, workers, channel_priority, attachments config ([b93a3ec](https://github.com/Ivantseng123/react2issue/commit/b93a3ecad67bdb219337fca2a21453d91898fbe7))
* **config:** add stream, agent_idle_timeout, prepare_timeout, status_interval ([550d1e1](https://github.com/Ivantseng123/react2issue/commit/550d1e151b2dda883009a5611b4a2b5ea85a4d4f))
* delegate issue creation to agent, remove Go-side issue logic ([c116b31](https://github.com/Ivantseng123/react2issue/commit/c116b31450b1541bb9f39a89b9627adc411d9db9))
* enrich messages with Slack attachments and Mantis issues ([5516af5](https://github.com/Ivantseng123/react2issue/commit/5516af58d2b41ef323559442d62bc99b5c129094))
* FetchMessage returns FetchedMessage with images and xlsx ([af07bc0](https://github.com/Ivantseng123/react2issue/commit/af07bc09c5fe461794266b7a386846ebe9f4ae45))
* **http:** enhanced /jobs with agent status, add DELETE /jobs/{id} kill endpoint ([8d8caff](https://github.com/Ivantseng123/react2issue/commit/8d8caff659017888aebe6bd4e18ae718408497e7))
* implement ConversationProvider.Chat() for all 4 LLM providers ([b3577d2](https://github.com/Ivantseng123/react2issue/commit/b3577d26bcec8155b31c116baaa8cde9b230cf78))
* in-memory diagnosis response cache with TTL ([a6c7a38](https://github.com/Ivantseng123/react2issue/commit/a6c7a3844e635f7d555cd6fd0f50bcbe68647d0b))
* inject version string into binary via ldflags ([62d09ff](https://github.com/Ivantseng123/react2issue/commit/62d09ff3c61797321133806e3de346a299d59689))
* **main:** wire queue transport, worker pool, and result listener ([ab0566e](https://github.com/Ivantseng123/react2issue/commit/ab0566e6785658eb8857e1f9ed90c1400fcbb14c))
* **main:** wire StatusListener, kill endpoint, cancel button handler ([a124d3d](https://github.com/Ivantseng123/react2issue/commit/a124d3d0e0e85d7d727cfa6bd8103fa1c2835959))
* Ollama provider image text fallback ([b19da94](https://github.com/Ivantseng123/react2issue/commit/b19da94226e56b0278f6809ec39b0921d536603d))
* only reject on confidence=low, skip triage for weak results ([1dbaf30](https://github.com/Ivantseng123/react2issue/commit/1dbaf309821cf641d815cd0c4d43e7361b4faff2))
* OpenAI provider vision support with image_url content blocks ([b921f01](https://github.com/Ivantseng123/react2issue/commit/b921f01f44394facc65021d271aa0b1f0feb2b4d))
* optional description input after repo/branch selection ([d0773f9](https://github.com/Ivantseng123/react2issue/commit/d0773f98e4b49536ed53d3b7427500e9dd07946f))
* **parser:** support structured JSON output format with legacy fallback ([b38c8dd](https://github.com/Ivantseng123/react2issue/commit/b38c8dddb2c9dca0dd94a6e2d1d49ba80a0fee05))
* pass images through diagnosis pipeline with token estimation ([b108352](https://github.com/Ivantseng123/react2issue/commit/b10835284388b5b0289a6d038162aabf96092efd))
* per-provider max_retries configuration ([21480d1](https://github.com/Ivantseng123/react2issue/commit/21480d1c8e87fc3d676d2fa2444c56e5deece8f7))
* pre-grep, CLI tool-use fixes, prompt tuning, README zh/en ([183c0a0](https://github.com/Ivantseng123/react2issue/commit/183c0a0ff58cb15e57ce962c27afe3daecd71f77))
* **prompt:** remove RepoPath, github_repo, labels — agent no longer creates issues ([0092704](https://github.com/Ivantseng123/react2issue/commit/0092704561d8fa4333117d2abf4d89d03e291d6d))
* **queue:** add CommandBus, StatusBus interfaces; replace SetAgent with SetAgentStatus ([c353b90](https://github.com/Ivantseng123/react2issue/commit/c353b9001fac75db1c84e5269329a6265e2975ed))
* **queue:** add container/heap priority queue implementation ([5f7f279](https://github.com/Ivantseng123/react2issue/commit/5f7f2795c85b09bd3ff1e1e9225ab421a08e123f))
* **queue:** add in-memory job store with TTL cleanup ([29b87f1](https://github.com/Ivantseng123/react2issue/commit/29b87f19e4c115657c9877886da54ab7cc2f73af))
* **queue:** add in-memory transport (JobQueue + ResultBus + AttachmentStore) ([98039a1](https://github.com/Ivantseng123/react2issue/commit/98039a123817ea3c0ef14762fd69bd5a03a57585))
* **queue:** add job data types and transport interfaces ([e3531eb](https://github.com/Ivantseng123/react2issue/commit/e3531eb9a5175347ade8bf9455d4863d11b24cb6))
* **queue:** add ProcessRegistry with cancel-based kill ([b58548d](https://github.com/Ivantseng123/react2issue/commit/b58548d34c288d87b3db1cc4e737090cbbf257fa))
* **queue:** add stream-json parser with result event + message_delta fallback ([91e1212](https://github.com/Ivantseng123/react2issue/commit/91e1212bf0d62e82bb50f92601bf58db5f772972))
* rewrite config for v2 agent architecture ([cdeb684](https://github.com/Ivantseng123/react2issue/commit/cdeb6844fa37c2419ad432cb82b32e7e60a52278))
* rewrite handler for app_mention and slash command triggers ([0f48463](https://github.com/Ivantseng123/react2issue/commit/0f48463a56286eef30f6c9de104c6cb44e122860))
* rewrite main.go + delete diagnosis/ and llm/ packages ([4b41718](https://github.com/Ivantseng123/react2issue/commit/4b4171838ab67b59ec95c0c688b409a681be2eff))
* rewrite workflow for v2 agent architecture ([e7eb199](https://github.com/Ivantseng123/react2issue/commit/e7eb199ec91211425a3258fa9b42d8477d1b832b))
* simplify GitHub issue client — accept title + body directly ([39ee79f](https://github.com/Ivantseng123/react2issue/commit/39ee79f428a9237542cb49d979803172f13e986f))
* **skill:** output structured JSON triage result instead of creating issues directly ([3f21c9f](https://github.com/Ivantseng123/react2issue/commit/3f21c9f57164ab53771a03dfd964ca2c86502366))
* **slack:** add cancel button for running jobs ([6b13df1](https://github.com/Ivantseng123/react2issue/commit/6b13df15b659a1c861d34e172e8dc2cb73806123))
* support multiple repos per channel with interactive selector ([aeaf209](https://github.com/Ivantseng123/react2issue/commit/aeaf209bc204ed04665c80f611c8f29f58960693))
* two-pass LLM diagnosis (file picker + analysis) ([d43a10c](https://github.com/Ivantseng123/react2issue/commit/d43a10cc0c8f8a536b340735c81249922e31ab05))
* use AI summary as issue title when available ([67c06e1](https://github.com/Ivantseng123/react2issue/commit/67c06e1079c7da1d71e16a0d554d7ff8c52aff33))
* v2 agent architecture — delegate codebase triage to CLI agents ([5005b72](https://github.com/Ivantseng123/react2issue/commit/5005b72568d5005b71f34bd93c08444bb24c4004))
* **watchdog:** add idle detection, prepare timeout, CommandBus kill ([05fd6b8](https://github.com/Ivantseng123/react2issue/commit/05fd6b82611a348f3eb3d82f4d48b40eb1227761))
* wire agent loop engine with ChatFallbackChain and progress message ([85e22f9](https://github.com/Ivantseng123/react2issue/commit/85e22f955146e1e79155372d7bed9c17890dfc1a))
* wire images from pendingIssue through to diagnosis engine ([15ac15a](https://github.com/Ivantseng123/react2issue/commit/15ac15ae2ff7b8a89bc8e0c4efae366638de7f24))
* wire MultiHandler with file rotation in main ([7e5bce3](https://github.com/Ivantseng123/react2issue/commit/7e5bce3f3fa030959148301d29c797cddfde1d8e))
* worker runs with env vars only, no config file required ([3eae9b3](https://github.com/Ivantseng123/react2issue/commit/3eae9b343b23cc47081110d51fedd20f376cb65a))
* **worker:** add worker pool with executor, skill mounting, and error handling ([572ce0e](https://github.com/Ivantseng123/react2issue/commit/572ce0e7b07b82a7b885f6cc9567141db55af3d5))
* **worker:** command listener, status reporting, per-job context, post-kill cleanup ([a3ba58c](https://github.com/Ivantseng123/react2issue/commit/a3ba58c970a3b9104275c4407036ab16272f7acb))


### Bug Fixes

* address code review findings for Rotator ([4ed37f7](https://github.com/Ivantseng123/react2issue/commit/4ed37f72fe3fde06ce4729554442ba770c921302))
* address Codex review findings (P1+P2) ([3160102](https://github.com/Ivantseng123/react2issue/commit/31601022124963f43b357d693b8bfbae42a1f783))
* attachment resolution, stream parser, status updates, skill mount ([0250f17](https://github.com/Ivantseng123/react2issue/commit/0250f176c6e2b598dfbdb0fd18763516864cbb7f))
* auto-fallback to stdin when prompt exceeds arg length limit for CLI provider ([b23067a](https://github.com/Ivantseng123/react2issue/commit/b23067a4c5dcac7036975b9016e37f83b5af428a))
* go mod tidy + cache key test for image count differentiation ([d52c0bf](https://github.com/Ivantseng123/react2issue/commit/d52c0bf8d64d569c27ae8a9ce46e5108e797465e))
* re-clone broken repo when git fetch fails ([a46ac5e](https://github.com/Ivantseng123/react2issue/commit/a46ac5e3f5d5bb02b1276a159e5e4f8bbf4aeb1c))
* remove hardcoded absolute paths ([6ae3363](https://github.com/Ivantseng123/react2issue/commit/6ae3363b58cb8489f19dd79c878c82bde2c01ffd))
* replace Block Kit buttons with emoji reactions for repo selector ([b19c018](https://github.com/Ivantseng123/react2issue/commit/b19c0182b59f5f4e48b398eb069da2e5ba686bfe))
* restore Block Kit buttons for repo selector (tested working) ([c9524ba](https://github.com/Ivantseng123/react2issue/commit/c9524ba1736851c59d0cbb9a8a9067945caa27a0))
* simplify repo selector to avoid invalid_blocks error ([5093a84](https://github.com/Ivantseng123/react2issue/commit/5093a849fa4421b87b56a29637633cd4588b0385))
* store job in worker's local JobStore on Receive from Redis ([a4af28d](https://github.com/Ivantseng123/react2issue/commit/a4af28de62d1aba4cdab26a06cfd3a5b3cdce9d4))
* use repo file tree as LLM context when keyword grep finds no matches ([3e30b42](https://github.com/Ivantseng123/react2issue/commit/3e30b42d750944579dbc5a8a896b716ec3edb016))
* use strings.ToUpper and set go 1.22 in go.mod ([bb2f98d](https://github.com/Ivantseng123/react2issue/commit/bb2f98d48dc59862d17c319b1981c6bbf8a43270))
* use unique action_id per button and proper block_id for repo selector ([4eefd86](https://github.com/Ivantseng123/react2issue/commit/4eefd86ca61558177868e7c8263b9b3fc47e5e59))
* **worker:** start status reporting after agent PID available, not before ([84f3d5e](https://github.com/Ivantseng123/react2issue/commit/84f3d5e1a59395023d994f6ccf9ae26e14be3443))
