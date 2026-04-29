# Changelog

## [3.2.0](https://github.com/Ivantseng123/agentdock/compare/v3.1.0...v3.2.0) (2026-04-29)


### Features

* **selector:** auto-upgrade to external_select for &gt;100 branches (closes [#153](https://github.com/Ivantseng123/agentdock/issues/153)) ([b71d6d2](https://github.com/Ivantseng123/agentdock/commit/b71d6d28b47f6d1307bc19a0163b592f4c927f44))
* **worker,config:** add extra_args field + {extra_args} placeholder ([f3acc33](https://github.com/Ivantseng123/agentdock/commit/f3acc3366e514be4dc945c05e5a056fdbaedfc88))
* **worker:** extra_args placeholder for per-agent flag customisation (closes [#190](https://github.com/Ivantseng123/agentdock/issues/190)) ([33f6828](https://github.com/Ivantseng123/agentdock/commit/33f6828cca99c0c3b0111b55252b4cffd141c706))


### Bug Fixes

* ask result race + agent CLI bumps ([5ef06c2](https://github.com/Ivantseng123/agentdock/commit/5ef06c2ef180b8825f34cdd329e89695aaa33641))
* **github:** log swallowed set-url error (closes [#193](https://github.com/Ivantseng123/agentdock/issues/193)) ([b045c0e](https://github.com/Ivantseng123/agentdock/commit/b045c0e68e967314261e91fc3af3dbde0b828dfb))
* **image:** bump opencode 1.4.11→1.14.29 and pin claude-code/codex ([63e03aa](https://github.com/Ivantseng123/agentdock/commit/63e03aa155e72217a6b1f6f0abf4a9b468186789))
* **worker/agent:** drop duplicate token const, runtime warn, and dead splice ([235b5f7](https://github.com/Ivantseng123/agentdock/commit/235b5f7556d7c82f50ac3c1d294fde78d60b5d71))
* **worker:** stamp terminal JobStatus on final status report ([76b3c77](https://github.com/Ivantseng123/agentdock/commit/76b3c77a1f5aea4099ec4d20dc224d0bf7786687))
* **workflow:** redact REJECTED.Message and POSTED.Severity ([#180](https://github.com/Ivantseng123/agentdock/issues/180)) ([24f034a](https://github.com/Ivantseng123/agentdock/commit/24f034a30e74a36416f764a484115ce22a1d83c1))

## [3.1.0](https://github.com/Ivantseng123/agentdock/compare/v3.0.0...v3.1.0) (2026-04-26)


### Features

* **config:** 把 agent 預設 timeout 拉到 30m，watchdog 配套 35m ([26fd672](https://github.com/Ivantseng123/agentdock/commit/26fd67266e39fb3960de394e4fcbe6b3af97a710))
* **config:** 把 agent 預設 timeout 拉到 30m，watchdog 配套 35m ([498bc1f](https://github.com/Ivantseng123/agentdock/commit/498bc1f2e1ff498d4da7ddf2cd15827df966283f))

## [3.0.0](https://github.com/Ivantseng123/agentdock/compare/v2.7.0...v3.0.0) (2026-04-26)


### ⚠ BREAKING CHANGES

* **workflow:** metric label WorkflowCompletionsTotal{workflow="ask",status="fallback_raw"} is removed and replaced by four fallback_* labels enumerated above. Dashboards keying on fallback_raw need to switch to a fallback_* regex or enumerate the new labels.

### Features

* **workflow:** extend Ask fallback to all parse failures with categorised metrics ([7580e1a](https://github.com/Ivantseng123/agentdock/commit/7580e1a8b76f1553fc0f2469e7023a52cb0e275b))
* **workflow:** extend Ask fallback to cover all parse failures ([d9a4886](https://github.com/Ivantseng123/agentdock/commit/d9a48867a3e9e51c4d3fc69cb46b6e115c51eeaa))


### Bug Fixes

* **pr-review:** 修正 diff 來源與 422 錯誤訊息誤譯 ([8c0f0d0](https://github.com/Ivantseng123/agentdock/commit/8c0f0d09854ae7a0612cfcc242462f54f6f2d6b9))
* **pr-review:** 將 diff 來源改為 PR API，並讓 422 錯誤帶出真正原因 ([d841108](https://github.com/Ivantseng123/agentdock/commit/d8411089873c3e8726b166919aecf0106ff45a42))

## [2.7.0](https://github.com/Ivantseng123/agentdock/compare/v2.6.3...v2.7.0) (2026-04-25)


### Features

* **app:** wire RedisJobStore via config + rehydrate ([#176](https://github.com/Ivantseng123/agentdock/issues/176)) ([6eebd8b](https://github.com/Ivantseng123/agentdock/commit/6eebd8b74a21a514dd4b6142155b7e49502f2983))
* **github:** strip PAT from bare clone's .git/config ([edfa068](https://github.com/Ivantseng123/agentdock/commit/edfa068132c4ae61d01978847c62c9d9d1e7bcde))
* **logging,workflow:** redact secrets from parse-failure logs ([f2da90f](https://github.com/Ivantseng123/agentdock/commit/f2da90fcf8bf072fd59ada1919f6712e6b1686c2))
* **prompt:** add security guardrail block to system prompt ([2cb9515](https://github.com/Ivantseng123/agentdock/commit/2cb951590e29fe72c4ac038c66f0ccb273fbbbc9))
* **skills:** add company-context skill for org + product-name lookup ([2365a36](https://github.com/Ivantseng123/agentdock/commit/2365a36275369622cd731c9ab2db9f1abee667d2))
* **worker,preflight:** gate startup on git &gt;= 2.31 for env-based auth ([c66a5c6](https://github.com/Ivantseng123/agentdock/commit/c66a5c663f5f47e4432672c3645b3038441bd6e7))
* **workflow:** add Ask missing-marker fallback to parser ([9499577](https://github.com/Ivantseng123/agentdock/commit/949957743952341b39d6b4cf5dc11366d92fe02b))
* **workflow:** Ask missing-marker fallback with transparency banner ([9ef00c5](https://github.com/Ivantseng123/agentdock/commit/9ef00c5cdbaae0acc9f0d31439d21f628c0a9dd8))
* **workflow:** wire Ask fallback banner and metric in HandleResult ([4f68a9c](https://github.com/Ivantseng123/agentdock/commit/4f68a9cb6605c7ee21989e8508543fde5f6a6a22))


### Bug Fixes

* **github:** close clone-time argv leak by routing clone through gitAuthEnv ([0b85532](https://github.com/Ivantseng123/agentdock/commit/0b855329543c329c330e354b010a8d3a2904a787))
* **github:** use Basic x-access-token scheme; Bearer rejected by git backend ([d26a522](https://github.com/Ivantseng123/agentdock/commit/d26a522deabd3c6aaa804397c3d1744528158bd0))
* **prompt:** cover x-access-token URL form in guardrail example ([55add2e](https://github.com/Ivantseng123/agentdock/commit/55add2e893ebff630624e7b91c03d3f96bb6d1cf))

## [2.6.3](https://github.com/Ivantseng123/agentdock/compare/v2.6.2...v2.6.3) (2026-04-24)


### Bug Fixes

* stop agent silent-fail on cwd-external writes ([#187](https://github.com/Ivantseng123/agentdock/issues/187)) ([e2c6adf](https://github.com/Ivantseng123/agentdock/commit/e2c6adf6e36787fd357bb5a1247427692b204e07))

## [2.6.2](https://github.com/Ivantseng123/agentdock/compare/v2.6.1...v2.6.2) (2026-04-24)


### Bug Fixes

* **ask:** resolve output_rules contradiction that killed skill structure ([#172](https://github.com/Ivantseng123/agentdock/issues/172)) ([7bb548a](https://github.com/Ivantseng123/agentdock/commit/7bb548a1aa2fd57580ce450d36599cccf3cc0147))
* **ask:** split prior-answer opt-in into its own prompt ([#175](https://github.com/Ivantseng123/agentdock/issues/175)) ([fc66fc7](https://github.com/Ivantseng123/agentdock/commit/fc66fc78a25a12679bf7a7b8f82679b2eb691f94))

## [2.6.1](https://github.com/Ivantseng123/agentdock/compare/v2.6.0...v2.6.1) (2026-04-24)


### Bug Fixes

* **ask:** drop prior-answer threshold + debug-log raw agent output ([#169](https://github.com/Ivantseng123/agentdock/issues/169)) ([e8be8e6](https://github.com/Ivantseng123/agentdock/commit/e8be8e61b2933ad2406d905ef24f1a21a5746b2f))
* **image:** upgrade opencode 1.4.3 → 1.4.11 to fix skill loading ([#170](https://github.com/Ivantseng123/agentdock/issues/170)) ([93109b5](https://github.com/Ivantseng123/agentdock/commit/93109b5c9eb558f4be445bcb5e183cfb41197e64))

## [2.6.0](https://github.com/Ivantseng123/agentdock/compare/v2.5.1...v2.6.0) (2026-04-24)


### Features

* **ask:** include prior bot answer for multi-turn continuity ([#167](https://github.com/Ivantseng123/agentdock/issues/167)) ([c443cbe](https://github.com/Ivantseng123/agentdock/commit/c443cbeb627bb1afba186057811afa4518952a49))

## [2.5.1](https://github.com/Ivantseng123/agentdock/compare/v2.5.0...v2.5.1) (2026-04-24)


### Bug Fixes

* **workflow:** walk marker segments so opencode fence pattern parses ([#165](https://github.com/Ivantseng123/agentdock/issues/165)) ([a5ca976](https://github.com/Ivantseng123/agentdock/commit/a5ca97627f1806db82a91c4d34e3f948bdeb2132))

## [2.5.0](https://github.com/Ivantseng123/agentdock/compare/v2.4.3...v2.5.0) (2026-04-23)


### Features

* **queue:** add RedisJobStore implementation (part 1/2 of [#123](https://github.com/Ivantseng123/agentdock/issues/123)) ([#147](https://github.com/Ivantseng123/agentdock/issues/147)) ([eaaedd2](https://github.com/Ivantseng123/agentdock/commit/eaaedd239c572d1bd0010f9501e017364646f013))
* **selector:** unify button/static/external selectors — fixes branch picker crash ([#149](https://github.com/Ivantseng123/agentdock/issues/149)) ([2f9e010](https://github.com/Ivantseng123/agentdock/commit/2f9e010e65c93ffaeecf546f16fd06d44b80dbdb))

## [2.4.3](https://github.com/Ivantseng123/agentdock/compare/v2.4.2...v2.4.3) (2026-04-23)


### Bug Fixes

* **workflow:** guard BuildJob against empty repo reference ([#142](https://github.com/Ivantseng123/agentdock/issues/142)) ([aecd528](https://github.com/Ivantseng123/agentdock/commit/aecd528c09b00d9be200e909603f8ce1031e94f0)), closes [#140](https://github.com/Ivantseng123/agentdock/issues/140) [#137](https://github.com/Ivantseng123/agentdock/issues/137)
* **workflow:** invalidate pending on back-to-repo + dedup in-flight selector clicks ([#143](https://github.com/Ivantseng123/agentdock/issues/143)) ([bd24c15](https://github.com/Ivantseng123/agentdock/commit/bd24c153a19dc5448e8fba907d6bb62e90b93e92))

## [2.4.2](https://github.com/Ivantseng123/agentdock/compare/v2.4.1...v2.4.2) (2026-04-23)


### Bug Fixes

* **docker:** symlink /agentdock into /usr/local/bin so PATH lookups resolve ([#136](https://github.com/Ivantseng123/agentdock/issues/136)) ([53d79a9](https://github.com/Ivantseng123/agentdock/commit/53d79a9c3c9493f49bf57c72ce63659269ec053e))

## [2.4.1](https://github.com/Ivantseng123/agentdock/compare/v2.4.0...v2.4.1) (2026-04-23)


### Bug Fixes

* PR Review uses head SHA + worker fetch-retries unknown refs ([#135](https://github.com/Ivantseng123/agentdock/issues/135)) ([78e3ce4](https://github.com/Ivantseng123/agentdock/commit/78e3ce4a1cdd93f8245923e2b29a6205be0b77f1))
* **queue:** log XReadGroup failures + exponential backoff ([#133](https://github.com/Ivantseng123/agentdock/issues/133)) ([226d476](https://github.com/Ivantseng123/agentdock/commit/226d476ea0e9745496ab46697712c4e3cce42241))

## [2.4.0](https://github.com/Ivantseng123/agentdock/compare/v2.3.1...v2.4.0) (2026-04-23)


### Features

* **prompt:** add response_schema default for issue workflow ([#132](https://github.com/Ivantseng123/agentdock/issues/132)) ([97707e0](https://github.com/Ivantseng123/agentdock/commit/97707e0a903889b904e249336c49db0b2aa49d6d))
* **prompt:** split response_schema from goal; render unescaped ([#130](https://github.com/Ivantseng123/agentdock/issues/130)) ([c7e4276](https://github.com/Ivantseng123/agentdock/commit/c7e427639ffb75ef5897c12e911cd742323101dc))

## [2.3.1](https://github.com/Ivantseng123/agentdock/compare/v2.3.0...v2.3.1) (2026-04-22)


### Bug Fixes

* **slack:** decode HTML entities in thread message text (closes [#89](https://github.com/Ivantseng123/agentdock/issues/89)) ([#128](https://github.com/Ivantseng123/agentdock/issues/128)) ([9d37291](https://github.com/Ivantseng123/agentdock/commit/9d37291241c990be3384d4226390b6d1741102fa))

## [2.3.0](https://github.com/Ivantseng123/agentdock/compare/v2.2.0...v2.3.0) (2026-04-22)


### Features

* **workflow+skill:** Ask branch/cancel UX, modal-first fix, mention filter, ask-assistant skill + bot identity plumbing ([#124](https://github.com/Ivantseng123/agentdock/issues/124)) ([b6e2fca](https://github.com/Ivantseng123/agentdock/commit/b6e2fcaf5c7b7e1f00ad8bd06d9670883dc5b483))

## [2.2.0](https://github.com/Ivantseng123/agentdock/compare/v2.1.3...v2.2.0) (2026-04-22)


### Features

* add github-pr-review skill + pr-review-helper subcommand ([#117](https://github.com/Ivantseng123/agentdock/issues/117)) ([94f32ac](https://github.com/Ivantseng123/agentdock/commit/94f32ace21ffefef10984c881e28da653dcc8827))


### Bug Fixes

* **bot/parser:** handle doubled ===TRIAGE_RESULT=== fence markers ([#121](https://github.com/Ivantseng123/agentdock/issues/121)) ([2649b81](https://github.com/Ivantseng123/agentdock/commit/2649b813b9dd1d89f6e008402bddd810aa74eca6))

## [2.1.3](https://github.com/Ivantseng123/agentdock/compare/v2.1.2...v2.1.3) (2026-04-21)


### Bug Fixes

* **app:** show worker nickname in Slack result and failure messages ([#115](https://github.com/Ivantseng123/agentdock/issues/115)) ([91fb917](https://github.com/Ivantseng123/agentdock/commit/91fb9172b641207c5dba85733f85b64b6a9d8d6c))

## [2.1.2](https://github.com/Ivantseng123/agentdock/compare/v2.1.1...v2.1.2) (2026-04-21)


### Bug Fixes

* **worker:** add --pure to opencode CLI invocation ([7351deb](https://github.com/Ivantseng123/agentdock/commit/7351debcb9bba5854901779d39d4cb871dbdb9fb))
* **worker:** add --pure to opencode CLI invocation ([64d12af](https://github.com/Ivantseng123/agentdock/commit/64d12af2d19924c02e36375500284d533205d052))

## [2.1.1](https://github.com/Ivantseng123/agentdock/compare/v2.1.0...v2.1.1) (2026-04-20)


### Bug Fixes

* **worker:** make github.token optional when app delivers via encrypted secrets ([29f3201](https://github.com/Ivantseng123/agentdock/commit/29f3201af75b3e5f29da46a16d76a7c7895c6efd))
* **worker:** make github.token optional when app delivers via encrypted secrets ([6608c4b](https://github.com/Ivantseng123/agentdock/commit/6608c4b9045e8ddfa96466bf4a8b18971b8b864a))

## [2.1.0](https://github.com/Ivantseng123/agentdock/compare/v2.0.0...v2.1.0) (2026-04-20)


### Features

* **app:** add formatWorkerLabel (nickname wins, else shortWorker) ([cf1d9d5](https://github.com/Ivantseng123/agentdock/commit/cf1d9d59c82489a0eedd1e299c8119bad0ad01d9))
* **app:** add slackEscape for Slack mrkdwn safety ([c364f29](https://github.com/Ivantseng123/agentdock/commit/c364f295b9bda0578b52de533f82d46906b20ca9))
* **app:** playful status text + Slack escape ([4cdc3bd](https://github.com/Ivantseng123/agentdock/commit/4cdc3bd4de7c4d997ff0ea8604aa58bb93236cc7))
* **pool:** statusAccumulator carries nickname ([a75a48a](https://github.com/Ivantseng123/agentdock/commit/a75a48a3f060e81b3d338796d49e361916e095e8))
* **pool:** thread Nicknames[] into Config and executors ([b5c96b4](https://github.com/Ivantseng123/agentdock/commit/b5c96b4dec08eaeaed7e64f80ff26be706aad1f7))
* **queue:** add Nickname/WorkerNickname fields ([68376e6](https://github.com/Ivantseng123/agentdock/commit/68376e6702ea5445e85bc3d0df2d466a669da2bc))
* **queue:** surface Nickname on /jobs workerEntry ([476a084](https://github.com/Ivantseng123/agentdock/commit/476a08470790862d445bb60cf9cb153d7628efc9))
* worker nicknames in Slack + playful status text ([f4ff9bd](https://github.com/Ivantseng123/agentdock/commit/f4ff9bdfe496a02b45efecbcd232489af48ad199))
* **worker:** add NicknamePool to Config ([709f4e8](https://github.com/Ivantseng123/agentdock/commit/709f4e838bc45f2efe09ad260acf2cd4a78db158))
* **worker:** add pickNicknames for nickname pool selection ([458de68](https://github.com/Ivantseng123/agentdock/commit/458de6803b8ce8e388cbf87a8991026ed0fc64ae))
* **worker:** pick nicknames at startup and warn on undersized pool ([a5009bf](https://github.com/Ivantseng123/agentdock/commit/a5009bfb24066e47b8cdfd97ebebee712a537ecb))
* **worker:** validate nickname_pool entries ([d76f41c](https://github.com/Ivantseng123/agentdock/commit/d76f41cdff62c4cd2ee639b852270128ddb989e2))

## [2.0.0](https://github.com/Ivantseng123/agentdock/compare/v1.4.0...v2.0.0) (2026-04-19)


### ⚠ BREAKING CHANGES

* the single config.yaml splits into app.yaml and worker.yaml. Operators must rebuild configs via 'agentdock init app' and 'agentdock init worker'. See docs/MIGRATION-v2.md for the field mapping table and K8s ConfigMap update steps.

### Features

* **app/config:** add load/validate/preflight helpers ([0452c8a](https://github.com/Ivantseng123/agentdock/commit/0452c8a1a5abfbd5d336d8d5fbecbfba34ac98e6))
* **app/config:** introduce AppConfig struct + ApplyDefaults ([5a4d193](https://github.com/Ivantseng123/agentdock/commit/5a4d19333a59cfcd635625a5aa0632841b59a075))
* **app:** introduce app module skeleton with shared dep ([073080e](https://github.com/Ivantseng123/agentdock/commit/073080ec79df443b298e3b3fca4953830e96e4d9))
* **shared/prompt:** extract interactive CLI helpers ([715b6c7](https://github.com/Ivantseng123/agentdock/commit/715b6c7489049e0a97fad38054892bc75f64c108))
* **shared:** extract configloader and connectivity helpers ([3dad48a](https://github.com/Ivantseng123/agentdock/commit/3dad48a1f9b4444a7f4d80e778cea452fca21d02))
* **shared:** introduce shared module skeleton with replace directive ([d872792](https://github.com/Ivantseng123/agentdock/commit/d8727927820245ab6c70fc470a103591176ee8cc))
* v2.0.0 — app/worker module split and config cutover ([0c6cb21](https://github.com/Ivantseng123/agentdock/commit/0c6cb21aee80da99d79d9280e5a10c7e632bf268))
* wire app+worker module split through cmd/agentdock ([f450623](https://github.com/Ivantseng123/agentdock/commit/f450623d5cab2633d4d803fb8aad67a79fca66be))
* **worker/config:** add load/validate/preflight helpers ([41bc050](https://github.com/Ivantseng123/agentdock/commit/41bc050b96bf2e9823d6df5207b790f4f4318e20))
* **worker/config:** introduce WorkerConfig struct with flat schema ([7f73a06](https://github.com/Ivantseng123/agentdock/commit/7f73a06eb4b484c1a8d9bcf9cf471c16c9b1fa0f))
* **worker:** introduce worker module skeleton with shared dep ([d714a9f](https://github.com/Ivantseng123/agentdock/commit/d714a9f42a256601d34b4b0e89e1eaa3519aaa0a))


### Bug Fixes

* **app:** size inmem status buffer for up to 10 workers ([62cdaa5](https://github.com/Ivantseng123/agentdock/commit/62cdaa562f12c1e613071b55cdec7b432bdeb9c1))
* **logging:** widen request_id suffix to 32 bits to stop test flake ([579718b](https://github.com/Ivantseng123/agentdock/commit/579718b3b56adea537b2cd1877f11d9193a9f885))
* **worker:** codex skill_dir should be .agents/skills, not .codex/skills ([6bb5e2f](https://github.com/Ivantseng123/agentdock/commit/6bb5e2f41c5bcb107f6e50284d09b8540a889f1b))

## [1.4.0](https://github.com/Ivantseng123/agentdock/compare/v1.3.0...v1.4.0) (2026-04-17)


### Features

* **config:** add Goal, OutputRules, AllowWorkerRules to PromptConfig ([daa3d23](https://github.com/Ivantseng123/agentdock/commit/daa3d230f2742fc2f4c025783d88fef81ac774d3))
* **config:** specific migration warns for prompt-refactor legacy keys ([9e67200](https://github.com/Ivantseng123/agentdock/commit/9e672001f11a63ec2b60ce5757da73b7261c4c19))
* **log:** add Debug lines dumping full PromptContext / XML prompt ([a3f9ede](https://github.com/Ivantseng123/agentdock/commit/a3f9edea0b5c613e7843454584d1b99dcad0f1fe))
* **queue:** add PromptContext and ThreadMessage types ([0a8700f](https://github.com/Ivantseng123/agentdock/commit/0a8700f62bf320a3588d2de3258d031125d9f9f5))
* **slack:** single lifecycle message + preserve worker diagnostics ([39eeaa8](https://github.com/Ivantseng123/agentdock/commit/39eeaa8afe8471f7c6fca7008a244105fedddabc))
* **worker:** add XML prompt builder ([3ebc9cf](https://github.com/Ivantseng123/agentdock/commit/3ebc9cf53a4b9b44aba7a84b6f3376fbcc0b3771))


### Bug Fixes

* /jobs shows worker state accurately (status + queue_depth) ([32ac957](https://github.com/Ivantseng123/agentdock/commit/32ac957ec5c2448bda99b0b9babb93f547a96be4))
* **bot,worker:** propagate JobStatus across pods via StatusBus ([c7975e5](https://github.com/Ivantseng123/agentdock/commit/c7975e5dfa5ae0c6d9ae7af4babfec29471a898c))
* **bot:** propagate PromptContext through retry, unify allow-worker-rules default ([56c9d0b](https://github.com/Ivantseng123/agentdock/commit/56c9d0b0805a370bb2d681e7024e8838701b02d4))
* **queue:** use consumer-group lag for queue_depth, not raw XLEN ([2077cb6](https://github.com/Ivantseng123/agentdock/commit/2077cb6fdf718444f43cce9f983065e4c7ecdb49))
* **worker:** preserve whitespace in xml escape ([20161f8](https://github.com/Ivantseng123/agentdock/commit/20161f89ec98f84221ffc5c03743d3f78b5b6398))
* **worker:** respect log_level + write to jsonl file like app ([b61843a](https://github.com/Ivantseng123/agentdock/commit/b61843a02da1656ffaecc25e1bfdf277539b9ba2))

## [1.3.0](https://github.com/Ivantseng123/agentdock/compare/v1.2.7...v1.3.0) (2026-04-17)


### Features

* **bot:** add handle back-to-repo for mis-clicked repo ([77dd0c4](https://github.com/Ivantseng123/agentdock/commit/77dd0c43308a342fc399792ecb7e37449fa715bb))
* **bot:** add render helpers for status message templates ([d118501](https://github.com/Ivantseng123/agentdock/commit/d118501694c8c0805b623e070bf022fa6fbfa10a))
* **bot:** defensive double-write of final status message ([c6dedb5](https://github.com/Ivantseng123/agentdock/commit/c6dedb5daa3a8a1bb8a061266090e5c99b43fcdb))
* **bot:** gate back button on description prompt ([bc4fd26](https://github.com/Ivantseng123/agentdock/commit/bc4fd2646816d20fff9135ec3398be2b00459d7d))
* **bot:** introduce slackAPI interface; add RepoWasPicked gate ([a37f21e](https://github.com/Ivantseng123/agentdock/commit/a37f21e8bfc90c54e065dadac635d481ec06cce9))
* **bot:** push status progress updates to slack ([7d9cf93](https://github.com/Ivantseng123/agentdock/commit/7d9cf933ac816a879251328f979e46fb5da7003a))
* Slack UX — progress visibility + repo re-select back button ([57c098c](https://github.com/Ivantseng123/agentdock/commit/57c098cbaa18932c9ca839155c604c07cd15181f))
* **slack:** add PostSelectorWithBack with optional trailing back button ([e3f0aa5](https://github.com/Ivantseng123/agentdock/commit/e3f0aa58ef9f328139778dcb466ecb095dcd06b5))
* **slack:** add UpdateMessageWithButton for status updates ([2cef28d](https://github.com/Ivantseng123/agentdock/commit/2cef28d071353aaf2b05def5b673e942b5073443))
* **worker:** emit prep-phase StatusReport on job pickup ([edfe79a](https://github.com/Ivantseng123/agentdock/commit/edfe79a23eafb18d73ae6945015341b1e8d863ec))


### Bug Fixes

* **bot:** route REJECTED/ERROR parser results to skip/fail lanes ([7995d78](https://github.com/Ivantseng123/agentdock/commit/7995d78c4ba663cdd764a68f3beb3f2aa50491e7))
* **bot:** route REJECTED/ERROR parser results to skip/fail lanes ([df2e43b](https://github.com/Ivantseng123/agentdock/commit/df2e43b4f85c08d183312cc9128ade6030263be1))
* **worker:** replace time.Sleep with signaling channel in prep test ([c4f4103](https://github.com/Ivantseng123/agentdock/commit/c4f4103be177663dae49b5729c6e0ff0a0216899))

## [1.2.7](https://github.com/Ivantseng123/agentdock/compare/v1.2.6...v1.2.7) (2026-04-17)


### Bug Fixes

* **bot:** switch codex to stdout+marker, drop --output-last-message ([9574294](https://github.com/Ivantseng123/agentdock/commit/9574294ff739f5e58d2834aafc1cc6bf38373f94))
* **bot:** switch codex to stdout+marker, drop --output-last-message ([8db8565](https://github.com/Ivantseng123/agentdock/commit/8db8565f0c2abde3ee49427338984ee19c37c185))

## [1.2.6](https://github.com/Ivantseng123/agentdock/compare/v1.2.5...v1.2.6) (2026-04-17)


### Bug Fixes

* **bot:** reject CREATED triage results with empty title ([1072735](https://github.com/Ivantseng123/agentdock/commit/10727358e2cf9c438769e6cafe59b83b3c50cc04))
* **bot:** reject CREATED triage results with empty title ([bf73615](https://github.com/Ivantseng123/agentdock/commit/bf73615681b53dfc112265703eab024f10bcee7a))

## [1.2.5](https://github.com/Ivantseng123/agentdock/compare/v1.2.4...v1.2.5) (2026-04-17)


### Bug Fixes

* **bot:** tolerate non-array labels + prevent null labels to GitHub ([1068b77](https://github.com/Ivantseng123/agentdock/commit/1068b771039b8416eaba9bc92605cdacc0db2145))
* **bot:** tolerate non-array labels + prevent null labels to GitHub ([d4b51f1](https://github.com/Ivantseng123/agentdock/commit/d4b51f147572a5e15c521a0f25ee5b7c168b3bb4))

## [1.2.4](https://github.com/Ivantseng123/agentdock/compare/v1.2.3...v1.2.4) (2026-04-17)


### Bug Fixes

* **bot:** revert opencode to default format; drop unused stream_format ([506c18b](https://github.com/Ivantseng123/agentdock/commit/506c18bf1b9578edd83329bfd002aade1594322b))
* **bot:** revert opencode to default format; drop unused stream_format ([c028524](https://github.com/Ivantseng123/agentdock/commit/c028524df7584eb78be5afa04e3b35799df90aff))

## [1.2.3](https://github.com/Ivantseng123/agentdock/compare/v1.2.2...v1.2.3) (2026-04-17)


### Bug Fixes

* **bot:** correct opencode/codex CLI flags and extract clean output ([651be0f](https://github.com/Ivantseng123/agentdock/commit/651be0f344507d51c9cb6c5df935a2fdb0f7a17f))
* **bot:** correct opencode/codex CLI flags and extract clean output ([6bce545](https://github.com/Ivantseng123/agentdock/commit/6bce5457a59b7df8e12b11e3f2faa9f536b94db1))
* **repo:** validate repo_cache.dir and use detached worktrees ([7cf9418](https://github.com/Ivantseng123/agentdock/commit/7cf94189fc81706af78b688d7f50f50e43a54c60))
* **repo:** validate repo_cache.dir and use detached worktrees ([ef782ad](https://github.com/Ivantseng123/agentdock/commit/ef782ad6317edbe605ab64cd4b1e38154df60a64))

## [1.2.2](https://github.com/Ivantseng123/agentdock/compare/v1.2.1...v1.2.2) (2026-04-17)


### Bug Fixes

* **bot:** update job store status on success paths ([07aad0f](https://github.com/Ivantseng123/agentdock/commit/07aad0f889520edd2b887dcff626c8c5f9dcca9e))
* **bot:** update job store status on success paths ([0f3d7c0](https://github.com/Ivantseng123/agentdock/commit/0f3d7c0117b60422dda4a844c3154811fa8f147b))

## [1.2.1](https://github.com/Ivantseng123/agentdock/compare/v1.2.0...v1.2.1) (2026-04-17)


### Bug Fixes

* **github:** inject token into cleaned https github.com URL ([673245a](https://github.com/Ivantseng123/agentdock/commit/673245a5ad1a9e398b2a146d0bd30a6b909b6a21))
* **github:** inject token into cleaned https github.com URL ([bf8488c](https://github.com/Ivantseng123/agentdock/commit/bf8488ca44187914bc294d7400694429d5b8ac98))

## [1.2.0](https://github.com/Ivantseng123/agentdock/compare/v1.1.2...v1.2.0) (2026-04-17)


### Features

* add secret_key to interactive preflight with auto-generate option ([5eff3bc](https://github.com/Ivantseng123/agentdock/commit/5eff3bcfe88e73aef7c7281e4e338fd10158fbaf))
* **agent:** generic secret injection via RunOptions.Secrets ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([48ad639](https://github.com/Ivantseng123/agentdock/commit/48ad6396be162b20ac6279c89494b875621011f9))
* **agent:** short-circuit provider chain on ctx.Canceled ([eeebcf2](https://github.com/Ivantseng123/agentdock/commit/eeebcf2df484c9b0a1959e3011b1d3f5a2bf85ea))
* app-to-worker encrypted secret passing ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([da6cab8](https://github.com/Ivantseng123/agentdock/commit/da6cab8c2b77ffd38502680224676b6399f7f6a7))
* **bot:** handle cancelled results with store-first semantics ([7fd1346](https://github.com/Ivantseng123/agentdock/commit/7fd1346d3aa825879c27447c0a66af14fb5314ab))
* **config:** add queue.cancel_timeout with 60s default ([1504fba](https://github.com/Ivantseng123/agentdock/commit/1504fba6c669a8ade5513637033c884b2f4b4d97))
* **config:** add SecretKey, Secrets fields with env var scan ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([a8575e0](https://github.com/Ivantseng123/agentdock/commit/a8575e0ca36a6078454de904303cd7a63d483c1a))
* **crypto:** add AES-256-GCM encrypt/decrypt module ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([803dd23](https://github.com/Ivantseng123/agentdock/commit/803dd23aa4a902625fb1fae2ed63d43f9445c86e))
* **queue:** add EncryptedSecrets field to Job struct ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([a6aeccd](https://github.com/Ivantseng123/agentdock/commit/a6aeccdb8ab1ea41b10c212a6d1337778186c522))
* **queue:** add JobCancelled status and CancelledAt timestamp ([50fc26f](https://github.com/Ivantseng123/agentdock/commit/50fc26fdf9ad3657402fefec3c7a6fcc41e76d78))
* **queue:** stamp CancelledAt when transitioning to JobCancelled ([a4699bf](https://github.com/Ivantseng123/agentdock/commit/a4699bf0a4c9ac3d87e99824b1e532e0e94b90d1))
* **registry:** add RegisterPending and SetStarted ([10ac0b6](https://github.com/Ivantseng123/agentdock/commit/10ac0b69900fc2abe9d92a203c20ad4edff7e162))
* **repo:** add per-call token to EnsureRepo ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([38b8eca](https://github.com/Ivantseng123/agentdock/commit/38b8eca162d75ca258344e58790f7cbe799c65c6))
* require secret_key for Redis workers + beacon key verification ([b71a707](https://github.com/Ivantseng123/agentdock/commit/b71a70743c23444dea68166d89333c48b5cc8817))
* **watchdog:** handle JobCancelled with fallback and back-off ([a6d7c34](https://github.com/Ivantseng123/agentdock/commit/a6d7c34096c37931295bad60603a41e5f5f6810f))
* **worker:** add classifyResult to split cancel from failure ([cc90921](https://github.com/Ivantseng123/agentdock/commit/cc9092168898996ee308ea9ff63b905c20db2e17))
* **worker:** add token param to RepoProvider.Prepare ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([3c0742c](https://github.com/Ivantseng123/agentdock/commit/3c0742c1ead2ae9090e8044dd78e4b65f253dd72))
* **worker:** decrypt, merge, and inject secrets per-job ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([7f179ed](https://github.com/Ivantseng123/agentdock/commit/7f179edb51c8d55661c2c4302d5d41ee7f889b25))
* **worker:** route executor failures through classifier; guard Prepare ([16b652a](https://github.com/Ivantseng123/agentdock/commit/16b652aa17deb66138183805702af0dbcc539d51))
* **worker:** switch short-circuit, RegisterPending wiring, cancelled log ([f9667fa](https://github.com/Ivantseng123/agentdock/commit/f9667fab0b1d5b2643430723134b431e2c4852d8))
* **workflow:** encrypt secrets into Job, clean CloneURL ([#56](https://github.com/Ivantseng123/agentdock/issues/56)) ([cd74421](https://github.com/Ivantseng123/agentdock/commit/cd744212095e96c4906c4277fe315701f0b3fa17))


### Bug Fixes

* address code review — decode secret key once, fix branch listing token ([1eaf64d](https://github.com/Ivantseng123/agentdock/commit/1eaf64dafd6a04e163d4bcc7a3ba5bb23713016a))
* **app:** order UpdateStatus before kill; wire cancel_timeout; guard cancelled ([d9533e0](https://github.com/Ivantseng123/agentdock/commit/d9533e09ba8fc43944afa62456e6a933e5c87156))
* distinguish user cancellation from agent failure ([#36](https://github.com/Ivantseng123/agentdock/issues/36)) ([6983cd1](https://github.com/Ivantseng123/agentdock/commit/6983cd147c2908f802a0c5e7d2f565c5c98afe2c))
* only allow auto-generate secret_key in app scope, not worker ([78c6113](https://github.com/Ivantseng123/agentdock/commit/78c6113ae3d1c70c9eae27c86883fe2b5a305e2d))
* **queue:** admin force-kill guard includes JobCancelled ([a0b8b61](https://github.com/Ivantseng123/agentdock/commit/a0b8b613991ed5f9126c125c7bf7078f7818002b))
* **test:** race guard, dead code, missing integration scenarios ([b3c18ca](https://github.com/Ivantseng123/agentdock/commit/b3c18ca106b18351d66485fbb85729f71ed47970))
* verify beacon during preflight, not after startup ([b09a7cb](https://github.com/Ivantseng123/agentdock/commit/b09a7cb4a64aa31e7c70ccb1fa653cb784172ee6))
* **watchdog:** log cancel_timeout on startup ([3616d45](https://github.com/Ivantseng123/agentdock/commit/3616d45d99011808a21fa8b1bf4a722dc16a2d02))
* **worker:** neutral wording for pre-execution terminated jobs ([d8469dd](https://github.com/Ivantseng123/agentdock/commit/d8469ddfd8152a4bad6ec10cdaf8af8ac4a8d51c))

## [1.1.2](https://github.com/Ivantseng123/agentdock/compare/v1.1.1...v1.1.2) (2026-04-16)


### Bug Fixes

* use platform-safe defaults for repo_cache and worktree paths ([d0d412e](https://github.com/Ivantseng123/agentdock/commit/d0d412e1443d6717f8b84441a95abb80965a7760))

## [1.1.1](https://github.com/Ivantseng123/agentdock/compare/v1.1.0...v1.1.1) (2026-04-16)


### Bug Fixes

* **release:** correct brews PR flow — head on feature branch, base on main ([332bf9a](https://github.com/Ivantseng123/agentdock/commit/332bf9a260b63f515bf4e9be6f87dfcdd70021c8)), closes [#29](https://github.com/Ivantseng123/agentdock/issues/29)

## [1.1.0](https://github.com/Ivantseng123/agentdock/compare/v1.0.1...v1.1.0) (2026-04-16)


### Features

* **release:** publish Homebrew formula to Ivantseng123/homebrew-tap ([30609f2](https://github.com/Ivantseng123/agentdock/commit/30609f2ddb3d5990f7a00846eda3d9303d87d129)), closes [#29](https://github.com/Ivantseng123/agentdock/issues/29)


### Bug Fixes

* **release:** drop unsupported pull_request.branch field ([0337514](https://github.com/Ivantseng123/agentdock/commit/03375146327e3f5bf74d13550a15324141b45d67))

## [1.0.1](https://github.com/Ivantseng123/agentdock/compare/v1.0.0...v1.0.1) (2026-04-16)


### Bug Fixes

* **validate:** skip workers.count check when transport is redis ([f1a5212](https://github.com/Ivantseng123/agentdock/commit/f1a5212f8d478f73ae72d0aff51af9114164eb6b)), closes [#42](https://github.com/Ivantseng123/agentdock/issues/42)

## [1.0.0](https://github.com/Ivantseng123/agentdock/compare/v0.3.0...v1.0.0) (2026-04-16)


### Features

* add Grafana dashboard ConfigMap for sidecar auto-loading ([995df33](https://github.com/Ivantseng123/agentdock/commit/995df333279019877d3d3ea156d9d6e359b0ebba))
* add Grafana dashboard JSON (6 rows, 27 panels) ([be3af34](https://github.com/Ivantseng123/agentdock/commit/be3af344670e61c57af2e43e59f098d533f1fb89))
* add internal/metrics package with 23 Prometheus metric definitions ([98090aa](https://github.com/Ivantseng123/agentdock/commit/98090aaa2dccdcf45b168d3361aa3149adc5521d))
* add PrepareSeconds to StatusReport and JobResult ([02421fe](https://github.com/Ivantseng123/agentdock/commit/02421fedee73e708e53ae9c6777d957ee612e017))
* add Prometheus scrape annotations and ServiceMonitor ([c2ee84c](https://github.com/Ivantseng123/agentdock/commit/c2ee84c75dcb880bf306d1a8709c1fa7628cf26e))
* instrument handler with request_total, dedup, rate_limit metrics ([1a161a9](https://github.com/Ivantseng123/agentdock/commit/1a161a9eaf9c95ca3b186ed75e02c3390bf46218))
* instrument result_listener with duration, agent, and issue metrics ([62fd510](https://github.com/Ivantseng123/agentdock/commit/62fd51028b6fc735733bd5426b157aa7c4bfcc08))
* instrument retry, queue_submitted, and watchdog_kills metrics ([e384621](https://github.com/Ivantseng123/agentdock/commit/e384621c7026be8b605c618ebfadb6e8b94e7d16))
* instrument Slack and GitHub API calls with external_duration metrics ([6ca9157](https://github.com/Ivantseng123/agentdock/commit/6ca91570f643d0eb9a779f0bdfa89b1dac4fc847))
* wire /metrics endpoint with promhttp.Handler() ([bb47ba4](https://github.com/Ivantseng123/agentdock/commit/bb47ba4158872eb9e6e85078447c117d07064243))

## [0.3.0](https://github.com/Ivantseng123/agentdock/compare/v0.2.7...v0.3.0) (2026-04-15)


### ⚠ BREAKING CHANGES

* binary renamed bot -> agentdock; subcommand required; default config path moved to ~/.config/agentdock/config.yaml; env priority inverted (YAML now wins over env). See docs/MIGRATION-v1.md.

### Features

* attachment transfer via Redis + worker cleanup ([15eba6d](https://github.com/Ivantseng123/agentdock/commit/15eba6d243838a931cde02dc8ba00244b77bc091))
* clean up workflow attachment handling and add Prepare error handling ([d6c4d9a](https://github.com/Ivantseng123/agentdock/commit/d6c4d9afc58459a879c29c487ff25c92314d1e5f))
* **cli:** add checkSlackToken helper for Slack auth.test validation ([2469928](https://github.com/Ivantseng123/agentdock/commit/246992827e61f70a14060535ba3df4b9bf44dcca))
* **cli:** add init subcommand skeleton ([60c3946](https://github.com/Ivantseng123/agentdock/commit/60c39465e4dd9cd2c51789205c8f67ef58551895))
* **cli:** add pflag enum types for queue-transport and log-level ([2ca6314](https://github.com/Ivantseng123/agentdock/commit/2ca6314446b95860f4af49a44fde095e4c389220))
* **cli:** add validate(cfg) cross-field validation in PreRunE ([de05ce3](https://github.com/Ivantseng123/agentdock/commit/de05ce36b34796ad7f299fbd5c237e3a9cb876b0))
* **cli:** delta-only save-back wired into PreRunE after preflight ([e83793d](https://github.com/Ivantseng123/agentdock/commit/e83793df56de8f15f83180880e1b608dc1bb1448))
* **cli:** implement init -i interactive prompts (5 fields) ([14a3e81](https://github.com/Ivantseng123/agentdock/commit/14a3e813c5dd164fa87bbdfc687df0953c368d91))
* **cli:** implement init non-interactive (YAML and JSON) ([8f44f23](https://github.com/Ivantseng123/agentdock/commit/8f44f236f61353559e4c23266a587139488f5651))
* **cli:** implement koanf two-instance load with built-in agents merge ([effdd11](https://github.com/Ivantseng123/agentdock/commit/effdd118b2d50e74d4b79524c49eaef8e751677e))
* **cli:** introduce cobra root + app subcommand wrapping main bot ([e0ad634](https://github.com/Ivantseng123/agentdock/commit/e0ad634b9f3dd4960584918210029f29d8d43e3b))
* **cli:** register persistent and app-specific flags with flagToKey map ([aacd9b8](https://github.com/Ivantseng123/agentdock/commit/aacd9b8bfc26c8d50d145bf4ead191384be7d88c))
* **cli:** replace config.Load with koanf-based PersistentPreRunE ([68a9572](https://github.com/Ivantseng123/agentdock/commit/68a95728f2b7e93cc3eac6031aad324b83edd288))
* **cli:** scope-aware preflight (App / Worker) with Slack token checks ([b147b4f](https://github.com/Ivantseng123/agentdock/commit/b147b4f9fb0dbe27c436c33b2b6396607a05af3b))
* **cli:** warn on unknown config keys (replaces v1 detection) ([abb1401](https://github.com/Ivantseng123/agentdock/commit/abb1401b41685e83d0da11eda231d49da42f9c9d))
* **config:** add DefaultsMap derived from applyDefaults round-trip ([58e559b](https://github.com/Ivantseng123/agentdock/commit/58e559ba34b2c694335818ebe07df4160b83cccf))
* **config:** add EnvOverrideMap helper for koanf env layer ([465afc6](https://github.com/Ivantseng123/agentdock/commit/465afc6fd70e5ac8877a3a24a3f1c47992d07884))
* **config:** extract BuiltinAgents map for runtime fallback ([5b552d3](https://github.com/Ivantseng123/agentdock/commit/5b552d34f2a40b57ae1682ac9de0c8d4219721d4))
* extract AppendAttachmentSection, remove from BuildPrompt ([bc909b4](https://github.com/Ivantseng123/agentdock/commit/bc909b4e7b15afd6ccddaf574aac3fa5b6345e6c))
* **logging:** add component/phase constants, attribute keys, and ComponentLogger helper ([8c1ad16](https://github.com/Ivantseng123/agentdock/commit/8c1ad169b9fc0fe64063b4028bbcb40d99d95c1c))
* **logging:** add StyledTextHandler with [Component][Phase] prefix rendering ([8cd0b2a](https://github.com/Ivantseng123/agentdock/commit/8cd0b2ad30667836b29f267018be30c7351242d3))
* **logging:** final sweep — verify no remaining English messages or camelCase keys ([62099ce](https://github.com/Ivantseng123/agentdock/commit/62099ce3f581c9f191d242fef84155fe5941f21e))
* **logging:** migrate cmd/agentdock to Chinese messages with component/phase ([5575337](https://github.com/Ivantseng123/agentdock/commit/557533728553998773e335a197ea5c6a50bd523b))
* **logging:** migrate config to Chinese messages ([b163c56](https://github.com/Ivantseng123/agentdock/commit/b163c56d163029cb1100dad3be8d5526947bf2d2))
* **logging:** migrate internal/bot to Chinese messages with component/phase injection ([80e0a0c](https://github.com/Ivantseng123/agentdock/commit/80e0a0c72891bfedd0b01e7e41f1e68d1b35711e))
* **logging:** migrate internal/github to Chinese messages with component/phase injection ([a82c89f](https://github.com/Ivantseng123/agentdock/commit/a82c89f9b49ff12c7eca4267caa5dcfa522e0e85))
* **logging:** migrate internal/skill to Chinese messages with component/phase injection ([12e67c3](https://github.com/Ivantseng123/agentdock/commit/12e67c3dfcf352ec842669525189fb3a65c65d66))
* **logging:** migrate internal/slack to Chinese messages with component/phase injection ([b7c2139](https://github.com/Ivantseng123/agentdock/commit/b7c21393108c0cfd27564716eca6666c02183aaf))
* **logging:** migrate internal/worker to Chinese messages with component/phase injection ([f4a2fbd](https://github.com/Ivantseng123/agentdock/commit/f4a2fbd63b7e921f110a7e5fc85e14939d60a90c))
* **logging:** migrate Watchdog to Chinese messages with component/phase injection ([1d7634d](https://github.com/Ivantseng123/agentdock/commit/1d7634da695b224a02ed22c3a984511b65755fd1))
* **logging:** wire StyledTextHandler as stderr handler in app and worker ([f0c233b](https://github.com/Ivantseng123/agentdock/commit/f0c233b6b5ccd006fc528e0971cf3b1adf7088dc))
* redesign logging with Chinese messages and component/phase classification ([a20c31b](https://github.com/Ivantseng123/agentdock/commit/a20c31b592120cf912cb68a3734d168998813281))
* store attachment bytes in Redis with size limits ([c34e2d8](https://github.com/Ivantseng123/agentdock/commit/c34e2d84578ecd3d7c41c1bed42905105e93251d))
* switch RepoCache to bare clone with worktree support and cleanup methods ([10ce7a6](https://github.com/Ivantseng123/agentdock/commit/10ce7a6520c405eb42748aacf45fdcc701719ad5))
* update AttachmentReady/AttachmentPayload data model for file transfer ([03a1f29](https://github.com/Ivantseng123/agentdock/commit/03a1f295e18fba414f35040b5ac22a828eeff7c0))
* update in-memory attachment store for payload bytes ([19b0487](https://github.com/Ivantseng123/agentdock/commit/19b0487bd140ccdda396dcbdda07030481977fa1))
* wire bare repo + worktree adapter and startup purge ([4e689e5](https://github.com/Ivantseng123/agentdock/commit/4e689e5bce15d354b62ac543e8835284609495cb))
* worktree cleanup after job completion and on shutdown ([1a734a9](https://github.com/Ivantseng123/agentdock/commit/1a734a9f35e23dd704ee9896899fc8079f4d3ee3))
* write attachment bytes to disk with filename dedup ([3e97573](https://github.com/Ivantseng123/agentdock/commit/3e975737ed9dc9a6df945eb48680fc9551a8eef4))


### Bug Fixes

* addWorktree use branch name directly for bare repos (not origin/ prefix) ([f3cab84](https://github.com/Ivantseng123/agentdock/commit/f3cab8485df91b6450d9332b86d62a331b766929))
* **cli:** reject init -i when stdin is not a TTY ([9de0171](https://github.com/Ivantseng123/agentdock/commit/9de017148e1f6d5b849e894564d75a0db531f72f))
* **cli:** remove stale .tmp before atomicWrite to preserve file mode ([507c31f](https://github.com/Ivantseng123/agentdock/commit/507c31f0464896118ff56d38d675af80f61c15cc))
* **cli:** warn on unknown nested struct keys in config ([7a6ec68](https://github.com/Ivantseng123/agentdock/commit/7a6ec68a435157f2df9013ee87618f19adbd2ccf))
* detect default branch from HEAD and put it first in branch list ([3718f6d](https://github.com/Ivantseng123/agentdock/commit/3718f6de149e2f23d091a78568337752d1df59ea))
* listBranches to work with bare repos using for-each-ref ([8f2740d](https://github.com/Ivantseng123/agentdock/commit/8f2740d9aee8cbc770faa28a97355261f31b9770))
* **logging:** keep domain terms (skill, bus, queue, transport) in English ([e4d2540](https://github.com/Ivantseng123/agentdock/commit/e4d25408ed1439b2bad3326a633804d8823220fb))
* preserve Authorization header across Slack file download redirects ([383b50d](https://github.com/Ivantseng123/agentdock/commit/383b50d8bc957a803d30e27f9474768fe6d7fcdb))
* put main/master first in branch list ([6684a7f](https://github.com/Ivantseng123/agentdock/commit/6684a7f5e53e199a30e9b700b61057b625e29ed0))
* update fakeRepo mock in integration tests for new RepoProvider interface ([e314cbf](https://github.com/Ivantseng123/agentdock/commit/e314cbf659711973761bd34d6a088d99d8790c18))


### Reverts

* restore slack-go GetFile for downloads, keep url_private fallback ([4c1e01b](https://github.com/Ivantseng123/agentdock/commit/4c1e01b09d3d7d1c3adfc7ba8ea337c04ab22d1d))


### Documentation

* add MIGRATION-v1.md and remove config.example.yaml ([ab248cf](https://github.com/Ivantseng123/agentdock/commit/ab248cf17ed2a8f7da2b1bfe9618d6b3b1e93230))

## [0.2.7](https://github.com/Ivantseng123/agentdock/compare/v0.2.6...v0.2.7) (2026-04-15)


### Features

* add latest and major-only docker tags ([91c6139](https://github.com/Ivantseng123/agentdock/commit/91c6139fd1cbb3fc90f9e3f2ecf136b423f0f2f6))


### Bug Fixes

* address review feedback from grill-me session ([657fe7a](https://github.com/Ivantseng123/agentdock/commit/657fe7aba12981cf7f74d3081107254983784bde))
* improve worker observability, security, and reliability ([4eaf9da](https://github.com/Ivantseng123/agentdock/commit/4eaf9dace161b413137ba8977b63cbeb8104b3a9))
* improve worker observability, security, and reliability ([8a52d05](https://github.com/Ivantseng123/agentdock/commit/8a52d05d629ffa040b333afbc2f0402c295c612d))

## [0.2.6](https://github.com/Ivantseng123/agentdock/compare/v0.2.5...v0.2.6) (2026-04-15)


### Features

* add preflight check functions (redis, github, agent cli) ([edda6bf](https://github.com/Ivantseng123/agentdock/commit/edda6bf1b4372967dd419cb657ffb3b25f1e6cc4))
* add preflight prompt helpers and runPreflight orchestrator ([c8c27ce](https://github.com/Ivantseng123/agentdock/commit/c8c27ceb80a007344bc31511f6907ff403e845b7))
* wire preflight into worker startup ([3ee8d36](https://github.com/Ivantseng123/agentdock/commit/3ee8d36ea1727c213f735ef3f51a4b1a545e2c65))


### Bug Fixes

* add HTTP timeout and non-2xx status handling to checkGitHubToken ([666be4f](https://github.com/Ivantseng123/agentdock/commit/666be4fb702e64e98be0dd5221b18a0097cccee3))
* handle empty skills_config path in NewLoader ([cbacaf3](https://github.com/Ivantseng123/agentdock/commit/cbacaf31cf3fd1f5d9179a6d6436e5d6b571496d))
* handle empty skills_config path in NewLoader ([28d43e5](https://github.com/Ivantseng123/agentdock/commit/28d43e52b59d12719e043431d1a805765d9c412f))

## [0.2.5](https://github.com/Ivantseng123/agentdock/compare/v0.2.4...v0.2.5) (2026-04-14)


### Features

* expose registered workers in /jobs endpoint ([1848f96](https://github.com/Ivantseng123/agentdock/commit/1848f96876b5708c78e34b77ff46220fb433dcd8))
* expose registered workers in /jobs endpoint ([36a538a](https://github.com/Ivantseng123/agentdock/commit/36a538afc32ab7c2d4e880e15a5f2eab9b0e76da))


### Bug Fixes

* allow skill .md files in Docker build ([f5b0b9b](https://github.com/Ivantseng123/agentdock/commit/f5b0b9b64be049afeaf7a261ca6830059371888a))
* allow skill .md files in Docker build ([042ec3e](https://github.com/Ivantseng123/agentdock/commit/042ec3e92e52e6b62bdb7cc2fc28147f6f367c0d))

## [0.2.4](https://github.com/Ivantseng123/agentdock/compare/v0.2.3...v0.2.4) (2026-04-14)


### Features

* add fsnotify watcher for skills.yaml hot reload ([7c239e6](https://github.com/Ivantseng123/agentdock/commit/7c239e6f7267385f61c0f0486ee75b3a3da50fc4))
* add npx package scanning and skill file reading ([5437c9a](https://github.com/Ivantseng123/agentdock/commit/5437c9abb839657e42edd3287198fb62e6257ee3))
* add skill file validation (size, extension whitelist, path safety) ([e05e83b](https://github.com/Ivantseng123/agentdock/commit/e05e83b6f56dc2dd18da733495e714864bf8aedf))
* add SkillLoader with cache, singleflight, and fallback ([b31af19](https://github.com/Ivantseng123/agentdock/commit/b31af1977d52709402e8f0e4e290098251052981))
* add skills.yaml config types and loading ([59e6abe](https://github.com/Ivantseng123/agentdock/commit/59e6abec375f1ff0750e2f7834e5f63b322b8775))
* change Job.Skills to SkillPayload with multi-file support ([92c3686](https://github.com/Ivantseng123/agentdock/commit/92c368620d4a5dd77f2009b22a22c0c532b14c34))
* NPX dynamic skill loading with cache and hot reload ([633532e](https://github.com/Ivantseng123/agentdock/commit/633532ed014fdbc7d8f4bf40fcd2882fb2f11138))
* rename type npx→remote, add same-name conflict fail fast ([84aba6d](https://github.com/Ivantseng123/agentdock/commit/84aba6d816ede8b19f98596bb482c86ca85f5e83))
* replace skills map with SkillProvider interface in Workflow ([2d7de88](https://github.com/Ivantseng123/agentdock/commit/2d7de882faf823a08ce728129fd9c0f3e0d67f7a))
* wire SkillLoader into app startup and workflow ([81e143e](https://github.com/Ivantseng123/agentdock/commit/81e143e8c0240b89efb26a3b2b7bb2de8a6d8722))


### Performance Improvements

* add GHA buildx cache for docker images in release ([94631d5](https://github.com/Ivantseng123/agentdock/commit/94631d5df04ba01e544a3fcf7a61e45a7ecd6196))

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
