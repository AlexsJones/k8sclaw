# Changelog

## [0.10.51](https://github.com/sympozium-ai/sympozium/compare/v0.10.50...v0.10.51) (2026-08-31)


### Features

* **celln:** enable labelled KVM hosts by default ([#385](https://github.com/sympozium-ai/sympozium/issues/385)) ([af3f0bb](https://github.com/sympozium-ai/sympozium/commit/af3f0bb9284da54094a58cb0ca6de83fea730df9))
* **harness:** select runtime while creating an agent ([#384](https://github.com/sympozium-ai/sympozium/issues/384)) ([bb9baf7](https://github.com/sympozium-ai/sympozium/commit/bb9baf7bd7960de5cc30de0c3b2b7166813eaf62))

## [0.10.50](https://github.com/sympozium-ai/sympozium/compare/v0.10.49...v0.10.50) (2026-08-31)


### Features

* **harness:** expose runtime selection in new run flow ([#369](https://github.com/sympozium-ai/sympozium/issues/369)) ([2745414](https://github.com/sympozium-ai/sympozium/commit/27454144c5f31350b6458fda3937d0069b196f4e))
* **harness:** manage agent runtime from UI ([#376](https://github.com/sympozium-ai/sympozium/issues/376)) ([5b9c67d](https://github.com/sympozium-ai/sympozium/commit/5b9c67d4624bcfb2c88c6c876a6a9480b7129cbf))
* **harness:** show runtime provenance and reject unsupported settings ([#371](https://github.com/sympozium-ai/sympozium/issues/371)) ([8c2e8e6](https://github.com/sympozium-ai/sympozium/commit/8c2e8e6ff8007ef0b4dcc5222b3e2f459bb139fa))
* **web:** add approved harness registry ([9eedf0c](https://github.com/sympozium-ai/sympozium/commit/9eedf0c855136c2ff0df79720a3bec2cc08f096d))
* **web:** add harness trust detail view ([43d7625](https://github.com/sympozium-ai/sympozium/commit/43d762507a9c15102bb00bb8fec482a4da008cc4))
* **web:** organize agent detail around harness and ensemble ([a6b4c64](https://github.com/sympozium-ai/sympozium/commit/a6b4c641793955b105e75ac87b5875d534e6fba7))


### Bug Fixes

* **api:** retain harness prompt for runtime runs ([#373](https://github.com/sympozium-ai/sympozium/issues/373)) ([efe766e](https://github.com/sympozium-ai/sympozium/commit/efe766e9e6ec6cbdb66a20c61cbfb65311fcc9ed))
* **harness:** allow default use context setting ([#374](https://github.com/sympozium-ai/sympozium/issues/374)) ([4b6733f](https://github.com/sympozium-ai/sympozium/commit/4b6733f89d15a4c85287bfc02b6c0df8f74fcf94))
* **harness:** preserve inherited runtime provenance ([#380](https://github.com/sympozium-ai/sympozium/issues/380)) ([f79f2f1](https://github.com/sympozium-ai/sympozium/commit/f79f2f15ffbce465c21131881ad18f1a67e386b1))
* **harness:** probe loopback skill tool server in-container ([#381](https://github.com/sympozium-ai/sympozium/issues/381)) ([e87ae07](https://github.com/sympozium-ai/sympozium/commit/e87ae07549c52b9de157f676fd84912dc7592f3e))

## [0.10.49](https://github.com/sympozium-ai/sympozium/compare/v0.10.48...v0.10.49) (2026-08-30)


### Features

* accelerator power draw from a discovered energy collector ([e5aa1e4](https://github.com/sympozium-ai/sympozium/commit/e5aa1e43369ef987839042d38b7c3b3e6cf87906))
* **collector+apiserver:** discover energy collectors, expose accelerator power ([a1bcbc2](https://github.com/sympozium-ai/sympozium/commit/a1bcbc2949f631f527e9ce601b991d7ff9e70789))
* **harness:** add the admin-owned AgentRuntime resource ([155c36f](https://github.com/sympozium-ai/sympozium/commit/155c36f7b4b0fa5d0943defce4ef30f307ecc161))
* **harness:** add the admin-owned AgentRuntime resource ([c05fd7e](https://github.com/sympozium-ai/sympozium/commit/c05fd7ec94a916a8a4132069c696bb77f71820ed))
* **harness:** inherit an Agent runtime across all run entrypoints ([480c719](https://github.com/sympozium-ai/sympozium/commit/480c71976190aaaec0b86a4992c538b8d70ec1fa))
* **harness:** reference an approved AgentRuntime from a run ([0e563f1](https://github.com/sympozium-ai/sympozium/commit/0e563f156ad12877e5a8b97091bd3a199ab3df4c))
* **harness:** reference an approved AgentRuntime from a run ([9abf37d](https://github.com/sympozium-ai/sympozium/commit/9abf37db01b48ae26547c3ac8a8f7dc73fe6fbd6))
* **security:** isolate Kubernetes identity per agent run ([f9229d4](https://github.com/sympozium-ai/sympozium/commit/f9229d4b9ce85bc881822ef3010ec0b6672655c1))
* **security:** isolate Kubernetes identity per AgentRun ([5cd37a7](https://github.com/sympozium-ai/sympozium/commit/5cd37a770df142fca52b6af3de7491829fcc2ec5))
* **security:** restrict harness NATS access ([925e6d9](https://github.com/sympozium-ai/sympozium/commit/925e6d92ef5a1475d39dc2dd71a1047c53a73ad3))
* **security:** restrict harness NATS access ([c63a2e5](https://github.com/sympozium-ai/sympozium/commit/c63a2e5a81e8a369068817d27641439bb8ed575d))
* **web:** live accelerator power on density and topology node views ([52c77a6](https://github.com/sympozium-ai/sympozium/commit/52c77a6e70a79ee3300835b524c812c0741e7030))
* **web:** simulated accelerator power in the topology demo ([409eceb](https://github.com/sympozium-ai/sympozium/commit/409eceb26961988a3484aab661196f05057433e8))


### Bug Fixes

* **eventbus:** bound core NATS publish flushes ([#365](https://github.com/sympozium-ai/sympozium/issues/365)) ([2c90d1b](https://github.com/sympozium-ai/sympozium/commit/2c90d1bee58f366e5f341c7c5a374993de3c3373))
* **harness:** inherit agent runtimes on normal runs ([058ec37](https://github.com/sympozium-ai/sympozium/commit/058ec3793dd99b59e655b2c5de0d5295116bc642))
* **harness:** require digest-pinned adapter images ([8636cf9](https://github.com/sympozium-ai/sympozium/commit/8636cf9efb5ccd4fc0ca994ef5ba7b5cbb4e3f8a))
* **harness:** require digest-pinned adapter images and record the digest ([9a9ce81](https://github.com/sympozium-ai/sympozium/commit/9a9ce8112ead856a3050e1272a435cab21395fb1))
* **harness:** require explicit policy opt-in ([1326b1f](https://github.com/sympozium-ai/sympozium/commit/1326b1f7a9bbdf177dc7225c0a3f6da2eb110737))
* **harness:** restrict model credentials to agent allowlist ([44ef22a](https://github.com/sympozium-ai/sympozium/commit/44ef22a5fbe0c38631a86d304047969ad35b2f43))
* **harness:** restrict model credentials to Agent allowlist ([a8d9ff5](https://github.com/sympozium-ai/sympozium/commit/a8d9ff52ca631e56641d8c169db7fcf55750b129))
* **ipc:** reconcile pre-existing result files on watch registration ([6e08fd9](https://github.com/sympozium-ai/sympozium/commit/6e08fd92a1d34d01fc95c69d8e098040a7d28390))
* **memory:** reconcile managed server image ([ee1c92f](https://github.com/sympozium-ai/sympozium/commit/ee1c92f39034b35b48d90100345a2cc3656717f8))
* **memory:** reconcile managed server image ([00c3ac7](https://github.com/sympozium-ai/sympozium/commit/00c3ac72b589c9e950230f99943db2ace4be0344))
* **nats:** authenticate density subscriber ([9b0c6fa](https://github.com/sympozium-ai/sympozium/commit/9b0c6fab41b69827268df3454a673f9b3575995c))
* **rbac:** grant controller and apiserver access to agentruntimes ([94ed965](https://github.com/sympozium-ai/sympozium/commit/94ed965a738b735e3a4851ead6db19225f056e0e))

## [0.10.48](https://github.com/sympozium-ai/sympozium/compare/v0.10.47...v0.10.48) (2026-08-18)


### Bug Fixes

* allow unauthenticated custom providers in wizard ([db1036d](https://github.com/sympozium-ai/sympozium/commit/db1036dcbf2895f737c043d94a4e4017341363b5))
* allow unauthenticated custom providers in wizard ([bccce53](https://github.com/sympozium-ai/sympozium/commit/bccce53e8a1882585d8c674ad0d72d16de023fc6))

## [0.10.47](https://github.com/sympozium-ai/sympozium/compare/v0.10.46...v0.10.47) (2026-08-09)


### Features

* Celln hermetic-action backend for AgentRun ([da364c4](https://github.com/sympozium-ai/sympozium/commit/da364c4c303f6b0a474934c2b322bbc99923fb87))
* Celln hermetic-action backend for AgentRun ([abea691](https://github.com/sympozium-ai/sympozium/commit/abea6914b3d410409d7752a1b5ef877e662adf19))
* deploy celln installer + router via Helm ([bda32eb](https://github.com/sympozium-ai/sympozium/commit/bda32ebed47a35ab9df1ab3cc56b2e47f0084d2b))
* enable Celln backend by default in Helm chart ([e92e58a](https://github.com/sympozium-ai/sympozium/commit/e92e58a14ca0f58554d2e4e9296a15858a0d287a))
* gate Celln behind explicit opt-in, add live status banner ([2d0c2ef](https://github.com/sympozium-ai/sympozium/commit/2d0c2ef34b2b71de65c2261ba6764800b48bcca6))
* migrate Celln dispatch to /v1/executions (forge mode) ([6b8b200](https://github.com/sympozium-ai/sympozium/commit/6b8b200db97f7ee573fcc546f575aa876d84af82))
* opt out for automatic memories ([8b74fa0](https://github.com/sympozium-ai/sympozium/commit/8b74fa0ab9115b308d4d5fc63ca4fd6bb0467f30))
* router DaemonSet with downward API, zero IP config ([348cd3c](https://github.com/sympozium-ai/sympozium/commit/348cd3caef9dbeb4122a68703bbaaa7ba06ba4b4))


### Bug Fixes

* **celln:** remove unused celln-dispatcher-token Secret ([183b8a5](https://github.com/sympozium-ai/sympozium/commit/183b8a52b97d507ad48fc93e61a62cef7646c322))
* derive Celln action IDs from AgentRun name+UID, not name alone ([7d8f85c](https://github.com/sympozium-ai/sympozium/commit/7d8f85cf8c2326d431491f0ba684087907e597bc))
* enforce a controller-side deadline for wedged Celln backends ([d4f46d7](https://github.com/sympozium-ai/sympozium/commit/d4f46d7c2cba31e30e265a49b2f64a4073835363))
* manually construct Azure OpenAI deployment URL ([58dbcb1](https://github.com/sympozium-ai/sympozium/commit/58dbcb1faf83a5e8d978e975013900a441608893))
* manually construct Azure OpenAI deployment URL to work around SDK middleware bug ([ac9cdb5](https://github.com/sympozium-ai/sympozium/commit/ac9cdb53ad7276d6631dc68d6ba1782e3d12f129))
* provider-agnostic API key secret (openaiApiKey, anthropicApiKey, deepseekApiKey) ([1321032](https://github.com/sympozium-ai/sympozium/commit/1321032c1f546c487aba14f2ca08ae2f56f10004))
* reject AgentRuns with both backend=celln and agentSandbox.enabled ([2de81c8](https://github.com/sympozium-ai/sympozium/commit/2de81c841eed5a91020cfac9daaf2c5406b81f13))
* retry Celln router-unreachable errors on a fixed 10s cadence ([eff0d03](https://github.com/sympozium-ai/sympozium/commit/eff0d03e0489ec71310750a04d2429e256d76e12))
* router port 8788 (dispatcher uses 8787), TCP probes ([8165c72](https://github.com/sympozium-ai/sympozium/commit/8165c7246cb38eb660cf85491718eeb086e0d3b1))

## [0.10.46](https://github.com/sympozium-ai/sympozium/compare/v0.10.45...v0.10.46) (2026-08-06)


### Bug Fixes

* **release:** enable Homebrew tap updates ([5aaf821](https://github.com/sympozium-ai/sympozium/commit/5aaf821ffdbb5ee3935f2796d2811916e73746e7))
* **release:** enable Homebrew tap updates ([e0a3512](https://github.com/sympozium-ai/sympozium/commit/e0a35126c6a52e399fae64b57afa8f5b73d6d0b4))

## [0.10.45](https://github.com/sympozium-ai/sympozium/compare/v0.10.44...v0.10.45) (2026-08-06)


### Features

* agent edit redirect ([#320](https://github.com/sympozium-ai/sympozium/issues/320)) ([c270aaf](https://github.com/sympozium-ai/sympozium/commit/c270aafc4ce120e3f558a1d3f1c2ac1ccf700554))

## [0.10.44](https://github.com/sympozium-ai/sympozium/compare/v0.10.43...v0.10.44) (2026-08-06)


### Features

* **agent-runner:** add opt-in detailed JSONL file logging with per-run directories ([#238](https://github.com/sympozium-ai/sympozium/issues/238)) ([6d52537](https://github.com/sympozium-ai/sympozium/commit/6d52537bcad973f0af37e0fa8afaceecaa910492))
* **apiserver:** reload SYMPOZIUM_UI_TOKEN from a mounted file ([#315](https://github.com/sympozium-ai/sympozium/issues/315)) ([28cb1b7](https://github.com/sympozium-ai/sympozium/commit/28cb1b7c52acfff50532a0aa002d46b302104681))
* compact old tool results ([#316](https://github.com/sympozium-ai/sympozium/issues/316)) ([460798d](https://github.com/sympozium-ai/sympozium/commit/460798d947938293e7fbfc5ce493c9680bcef8d2))
* **crd:** gate ensemble auto-start with stimulus.trigger and schedule.firstTick ([b5ea9d3](https://github.com/sympozium-ai/sympozium/commit/b5ea9d33cf614a4cd034c26e88e20653ca08c7cd))
* **sidecar-driven:** polymorphic task + TaskModeHandler registry ([#302](https://github.com/sympozium-ai/sympozium/issues/302)) ([c3038ea](https://github.com/sympozium-ai/sympozium/commit/c3038ea4065a02763059ac55c794e45e858ab9c1))
* stimulus trigger gating, workstation topology demo, and buildable demo hardware ([4d39487](https://github.com/sympozium-ai/sympozium/commit/4d394873a4132fba73416ce83c5dba9805ed7e46))
* **web:** draw demo topology nodes as workstations with accelerator bays ([129cdba](https://github.com/sympozium-ai/sympozium/commit/129cdbab774aa0423e639634a66dff40177029d0))
* **web:** let the ensemble builder choose the stimulus trigger mode ([bc5a8cb](https://github.com/sympozium-ai/sympozium/commit/bc5a8cbabb57b32570cd9822a4d1d60ed1012cc4))


### Bug Fixes

* **controller:** make sequential handoff truncation legible ([bae9701](https://github.com/sympozium-ai/sympozium/commit/bae9701bf97cdb55471fdf4706cf73b4def31854))
* **crd:** accept both string-form and object-form spec.task at admission ([#312](https://github.com/sympozium-ai/sympozium/issues/312)) ([a29016c](https://github.com/sympozium-ai/sympozium/commit/a29016c2d833ac23cc54da33a4e205d32fc3cd77))
* **examples:** give the research demo a brief that actually directs research ([de77fda](https://github.com/sympozium-ai/sympozium/commit/de77fdad7e00c05b68326831115894185e8cd0ad))
* **web:** make demo topology hardware physically buildable ([eaedf1d](https://github.com/sympozium-ai/sympozium/commit/eaedf1d910f03ead6a7d508799b144c2f9dfb87a))

## [0.10.43](https://github.com/sympozium-ai/sympozium/compare/v0.10.42...v0.10.43) (2026-07-15)


### Features

* AgentRun dollar cost estimation with simulated pricing overlay ([798aa3f](https://github.com/sympozium-ai/sympozium/commit/798aa3f36c1d211b94858931580e0768ba21b738))
* AgentRun dollar cost estimation with simulated pricing overlay ([dfe8fdf](https://github.com/sympozium-ai/sympozium/commit/dfe8fdfcf4cac3418cca117eb4c8469a0c9550df))
* **controller:** opt-in controller-side delegation executor with guardrails + telemetry ([0179b44](https://github.com/sympozium-ai/sympozium/commit/0179b4420465c44809f39fc1bb3e52c9c6334162))
* implement allow/deny tool policy semantics ([dfb12dd](https://github.com/sympozium-ai/sympozium/commit/dfb12dde8175aede5c1a45b556dcaed649444854))
* ToolPolicy allow/deny propagation and runner-side filtering ([ebcdafe](https://github.com/sympozium-ai/sympozium/commit/ebcdafef29e18c60f60e6feade0e641eecaffbe4))
* **web:** show estimated spend inline in the conversation panel ([87f4435](https://github.com/sympozium-ai/sympozium/commit/87f443574100c82c7be090a195bac5c4df1c0fca))
* **web:** single effective spend value with totals per page and conversation ([46e6537](https://github.com/sympozium-ai/sympozium/commit/46e6537f105f3edb0c628849fd4418c46a1b549d))
* wire ToolPolicy.Deny to agent-runner tool filtering (VEL-1084) ([6bd88e4](https://github.com/sympozium-ai/sympozium/commit/6bd88e4ca082e725fcd9afe78f8da4241767850e))


### Bug Fixes

* **controller:** reject forged token metrics from agent result markers ([8227556](https://github.com/sympozium-ai/sympozium/commit/8227556c3616030c4d25740fb730ef4822d12180))
* propagate Ensemble toolPolicy to AgentRun pods ([67b9161](https://github.com/sympozium-ai/sympozium/commit/67b9161e032c409534ade952a6ae05119afacd9a))

## [0.10.42](https://github.com/sympozium-ai/sympozium/compare/v0.10.41...v0.10.42) (2026-07-09)


### Features

* **web+apiserver:** decorate topology nodes with llmfit-dra accelerator inventory ([4bcaeef](https://github.com/sympozium-ai/sympozium/commit/4bcaeef1fe1912f6c988153fb6cdb69739cef544))
* **web:** accelerator tree leaves on density nodes and topology cards ([2bebd1a](https://github.com/sympozium-ai/sympozium/commit/2bebd1aec653c18a82c01eccac01d3ec38f93734))


### Bug Fixes

* **web:** stack and group topology accelerator chips ([77f63b3](https://github.com/sympozium-ai/sympozium/commit/77f63b3cdc9b32d4bee461a048d0f513912f25f5))

## [0.10.41](https://github.com/sympozium-ai/sympozium/compare/v0.10.40...v0.10.41) (2026-07-08)


### Features

* **serve:** auto-login web UI via token in the URL fragment ([5aa40ba](https://github.com/sympozium-ai/sympozium/commit/5aa40ba8233c826f44fa9e7b8a7f102b35d51433))
* **serve:** URL auto-login; standalone agent topology nodes ([f19d0be](https://github.com/sympozium-ai/sympozium/commit/f19d0bed5e25738a1eca6994ac338114e4961d42))
* **web:** render standalone agents on the topology with model edges ([ba22b3e](https://github.com/sympozium-ai/sympozium/commit/ba22b3e82f48bdaf9295ad3cb7d839bdb5ab773a))


### Bug Fixes

* **web:** repair type errors that broke npm run build ([20ce832](https://github.com/sympozium-ai/sympozium/commit/20ce8323dcd9bbb0b2c44d2ce193ef06d25feb04))

## [0.10.40](https://github.com/sympozium-ai/sympozium/compare/v0.10.39...v0.10.40) (2026-07-08)


### Bug Fixes

* **install:** make uninstall aware of the bundled llmfit-dra subchart ([92cbef8](https://github.com/sympozium-ai/sympozium/commit/92cbef80be3d581db2ac9f20e3f14fec7f72da0d))
* **install:** make uninstall aware of the bundled llmfit-dra subchart ([576b7b6](https://github.com/sympozium-ai/sympozium/commit/576b7b6a17839672c51a2044759c4328c9c8ec89))

## [0.10.39](https://github.com/sympozium-ai/sympozium/compare/v0.10.38...v0.10.39) (2026-07-08)


### Features

* Add skipped as AgentRun status - reachable via IPC similar to dry runs ([#228](https://github.com/sympozium-ai/sympozium/issues/228)) ([056421c](https://github.com/sympozium-ai/sympozium/commit/056421c6f1c7a0ad445ea3c2628da332103c12fa))
* **channels:** wire per-agent display name for Slack sender attribution ([#263](https://github.com/sympozium-ai/sympozium/issues/263)) ([1f52056](https://github.com/sympozium-ai/sympozium/commit/1f5205692e7e79eca88ace59d2e4300f66e0b971))
* **placement:** claim-based model placement via bundled llmfit-dra ([07e2524](https://github.com/sympozium-ai/sympozium/commit/07e252486a543f5670dfbc80c6be2a5de4105378))
* **placement:** claim-based model placement via bundled llmfit-dra ([21532a2](https://github.com/sympozium-ai/sympozium/commit/21532a24b1bc2690a46343a6fd89027e542522c1))
* **placement:** claim-based model placement via bundled llmfit-dra ([4d70a1f](https://github.com/sympozium-ai/sympozium/commit/4d70a1faf38a8933c03250a3e991a6275c0d66b2))
* **placement:** surface pending-pod scheduler verdicts; ensure claims on the deployment path ([d4e7132](https://github.com/sympozium-ai/sympozium/commit/d4e7132f2957db27efddfd445221c794d274de8f))
* **slack:** per-message sender attribution for multi-agent Ensembles ([#245](https://github.com/sympozium-ai/sympozium/issues/245)) ([5239b4d](https://github.com/sympozium-ai/sympozium/commit/5239b4d9945ee788ad16e01fe0c49866a73e74d8))
* **web:** waiting reasons on topology model nodes; consolidate Models nav ([8813105](https://github.com/sympozium-ai/sympozium/commit/881310578d8b68f2a86d1f3303b35d08e83bc0a6))
* **web:** waiting reasons on topology model nodes; consolidate Models nav ([ad5e566](https://github.com/sympozium-ai/sympozium/commit/ad5e5664b65657cc981be1f34f5bfa383230d4c9))


### Bug Fixes

* **agent-runner:** serialize Bedrock tool schemas as JSON objects ([#259](https://github.com/sympozium-ai/sympozium/issues/259)) ([f0f22c7](https://github.com/sympozium-ai/sympozium/commit/f0f22c78a0c64e8dacc38714354632a15e8cf907)), closes [#255](https://github.com/sympozium-ai/sympozium/issues/255)
* align Ensemble update path with create path for all Agent fields ([#264](https://github.com/sympozium-ai/sympozium/issues/264)) ([e33be9f](https://github.com/sympozium-ai/sympozium/commit/e33be9f1a51e6f92410d06ecbbdf99e900b0f2bd))
* **controller:** propagate runTimeout and wire agentEnv into pod spec ([#240](https://github.com/sympozium-ai/sympozium/issues/240)) ([d9f584e](https://github.com/sympozium-ai/sympozium/commit/d9f584e61f611084fd527389434eb875ab41c70c))
* **controller:** propagate sidecar tools from source SkillPack ([#250](https://github.com/sympozium-ai/sympozium/issues/250)) ([46b2cdc](https://github.com/sympozium-ai/sympozium/commit/46b2cdcade676d9751eae4391d36603b7018ee69))
* **eventbus:** recover NATS subscriptions after restart/recreate ([#253](https://github.com/sympozium-ai/sympozium/issues/253)) ([#254](https://github.com/sympozium-ai/sympozium/issues/254)) ([f341b37](https://github.com/sympozium-ai/sympozium/commit/f341b372f5a3c1ef69b30a4a7555b048f643e52e))
* **install:** vendor the bundled llmfit-dra chart for the embedded installer ([def0522](https://github.com/sympozium-ai/sympozium/commit/def0522e3910b7115b319afd328f5a40a67414a1))
* **security:** tier-1 hardening for delegation, channels, and API auth ([#265](https://github.com/sympozium-ai/sympozium/issues/265)) ([6fd740d](https://github.com/sympozium-ai/sympozium/commit/6fd740db84db9a37baffb3dd607fb48b2035e713))

## [0.10.38](https://github.com/sympozium-ai/sympozium/compare/v0.10.37...v0.10.38) (2026-06-28)


### Bug Fixes

* **controller:** honor SYMPOZIUM_IMAGE_TAG for run pods + memory sidecar (ISI-1406/ISI-1417) ([#244](https://github.com/sympozium-ai/sympozium/issues/244)) ([604dd57](https://github.com/sympozium-ai/sympozium/commit/604dd57dc46c43dd4ec1449dcd670b84d37feaed))
* **controller:** replace LastRunName check with List to prevent TOCTOU race ([#239](https://github.com/sympozium-ai/sympozium/issues/239)) ([a419afe](https://github.com/sympozium-ai/sympozium/commit/a419afe7fa898468ba4f11e2033bf7fb32d97a78))
* make subagent fanout limits and names reliable ([#247](https://github.com/sympozium-ai/sympozium/issues/247)) ([f156807](https://github.com/sympozium-ai/sympozium/commit/f15680767c6c403d4f51a924db7bf2e78f4bcc51))

## [0.10.37](https://github.com/sympozium-ai/sympozium/compare/v0.10.36...v0.10.37) (2026-06-25)


### Features

* **agent-runner:** log LLM round progress during agent runs ([#225](https://github.com/sympozium-ai/sympozium/issues/225)) ([c222b8e](https://github.com/sympozium-ai/sympozium/commit/c222b8e664f42a69cad765b46e7e24a30f7df2a6))
* native sidecar tools declared on SkillPack CRD ([#241](https://github.com/sympozium-ai/sympozium/issues/241)) ([32a5acc](https://github.com/sympozium-ai/sympozium/commit/32a5accb1dddc7ce4850b6a3c19952bb1c3ee392))
* Persist AgentRun.spec.Timeout and expose runTimeout via Ensemble config ([#230](https://github.com/sympozium-ai/sympozium/issues/230)) ([897c5f0](https://github.com/sympozium-ai/sympozium/commit/897c5f0a481e116374630e4feb040e97eb8e0379))


### Bug Fixes

* enforce sequential ordering in pipeline ensembles ([a86bcc6](https://github.com/sympozium-ai/sympozium/commit/a86bcc60b78ae9cedab266b4ed370834786610cd))
* preserve namespace for subagent spawning ([#234](https://github.com/sympozium-ai/sympozium/issues/234)) ([5c7bd03](https://github.com/sympozium-ai/sympozium/commit/5c7bd03bad449bd79f5ca3c412a040f7d333f89d))
* remove finish_reason gate for tool call extraction ([#227](https://github.com/sympozium-ai/sympozium/issues/227)) ([14cea32](https://github.com/sympozium-ai/sympozium/commit/14cea3235950088450d506d547b741e6f1b9d865))

## [0.10.36](https://github.com/sympozium-ai/sympozium/compare/v0.10.35...v0.10.36) (2026-06-15)


### Features

* evidence traces for shared memory + memory browser UI ([#219](https://github.com/sympozium-ai/sympozium/issues/219)) ([9ec7ccc](https://github.com/sympozium-ai/sympozium/commit/9ec7ccc68b1480c3370934fecaf7a56a94b0292a))


### Bug Fixes

* don't hijack canvas zoom/pan keys while typing in inputs ([#221](https://github.com/sympozium-ai/sympozium/issues/221)) ([39940e0](https://github.com/sympozium-ai/sympozium/commit/39940e0f076794076cb22b9bfad7549c2381e912))
* guard against unreachable cron expressions in schedule controller ([#220](https://github.com/sympozium-ai/sympozium/issues/220)) ([a776f4d](https://github.com/sympozium-ai/sympozium/commit/a776f4daa01d380092447cee2a26cb65416f3395))
* scope TUI resource lists to the active namespace ([#217](https://github.com/sympozium-ai/sympozium/issues/217)) ([6479fe5](https://github.com/sympozium-ai/sympozium/commit/6479fe5f27714f82da20ced42de6218b21018c4a)), closes [#215](https://github.com/sympozium-ai/sympozium/issues/215)

## [0.10.35](https://github.com/sympozium-ai/sympozium/compare/v0.10.34...v0.10.35) (2026-06-03)


### Features

* support custom headers for model provider requests ([#210](https://github.com/sympozium-ai/sympozium/issues/210)) ([8412662](https://github.com/sympozium-ai/sympozium/commit/8412662a1076b5d5a71e3626a69ab32bd95f8037))
* topology keyboard navigation and demo gif ([2d8b417](https://github.com/sympozium-ai/sympozium/commit/2d8b4176677ee0967ba85f527386d361cff89a02))
* **tui:** add search/filter, log improvements, status colors, and run table enhancements ([#209](https://github.com/sympozium-ai/sympozium/issues/209)) ([d70da82](https://github.com/sympozium-ai/sympozium/commit/d70da82a6c32d5acfcda666df731723f234d613d))


### Bug Fixes

* add watch verb to apiserver secrets RBAC ([3037f65](https://github.com/sympozium-ai/sympozium/commit/3037f65768d6b03b22ff5e1329e8ebe45e902aca))
* **ci:** disable fail-fast on release image matrix ([50661f8](https://github.com/sympozium-ai/sympozium/commit/50661f8a3a07b8a71e5591821e66acd5f7ab6723))
* clear stale baseURL when switching to a cloud provider ([6345d1f](https://github.com/sympozium-ai/sympozium/commit/6345d1fe21efac18806c1e84ca70b6f47fb2da62))
* pass baseURL as wizard default when re-enabling ensemble ([3e8b98d](https://github.com/sympozium-ai/sympozium/commit/3e8b98defff3d90801955ebda4707e3bbbcc363b))
* preserve ensemble auth config on disable and clean up stale runs ([00ac9e4](https://github.com/sympozium-ai/sympozium/commit/00ac9e468bbb15d2f20140337d4b91436e177352))

## [0.10.34](https://github.com/sympozium-ai/sympozium/compare/v0.10.33...v0.10.34) (2026-05-18)


### Features

* **tui:** update TUI colors to neo-industrial palette ([8402a5e](https://github.com/sympozium-ai/sympozium/commit/8402a5e61f4e5e66ccb7e0e1fa995aae54100ad6))
* update TUI, topology, and ensemble canvas to industrial palette ([f7a3bc7](https://github.com/sympozium-ai/sympozium/commit/f7a3bc721d95e57728367f056c29c8f6e5b20d3e))


### Bug Fixes

* replace remaining gradient buttons with theme-aware primary and update README demo image ([#203](https://github.com/sympozium-ai/sympozium/issues/203)) ([e85c1cf](https://github.com/sympozium-ai/sympozium/commit/e85c1cf95747360692650d3b6006d69575e1066b))

## [0.10.33](https://github.com/sympozium-ai/sympozium/compare/v0.10.32...v0.10.33) (2026-05-17)


### Features

* **web:** neo-industrial UX theme with classic toggle ([#200](https://github.com/sympozium-ai/sympozium/issues/200)) ([5af0144](https://github.com/sympozium-ai/sympozium/commit/5af0144a5543523c21f6d12d4b3941d7cd53b595))

## [0.10.32](https://github.com/sympozium-ai/sympozium/compare/v0.10.31...v0.10.32) (2026-05-14)


### Features

* add Suspended field to MCPServer to prevent crash-looping defaults ([#195](https://github.com/sympozium-ai/sympozium/issues/195)) ([6200f97](https://github.com/sympozium-ai/sympozium/commit/6200f9737bbeef0487f0b3ca7e5bfb902d4b8fa3))


### Bug Fixes

* mcp-bridge missing notifications/initialized and sequential discovery ([#197](https://github.com/sympozium-ai/sympozium/issues/197)) ([6e5d4a1](https://github.com/sympozium-ai/sympozium/commit/6e5d4a16c5cac5022952a7f22f551bfa7e32f8ec)), closes [#194](https://github.com/sympozium-ai/sympozium/issues/194)

## [0.10.31](https://github.com/sympozium-ai/sympozium/compare/v0.10.30...v0.10.31) (2026-05-13)


### Bug Fixes

* **ci:** add llmfit-daemon to build and release image matrices ([df3f7e1](https://github.com/sympozium-ai/sympozium/commit/df3f7e10c313680b5d9a3b28d8b4edd398edbdda))

## [0.10.30](https://github.com/sympozium-ai/sympozium/compare/v0.10.29...v0.10.30) (2026-05-13)


### Bug Fixes

* channelTriggers propagation from Ensembles ([#191](https://github.com/sympozium-ai/sympozium/issues/191)) ([9192ae9](https://github.com/sympozium-ai/sympozium/commit/9192ae914aa8f3c3b0a211c338b107f546b79c56))

## [0.10.29](https://github.com/sympozium-ai/sympozium/compare/v0.10.28...v0.10.29) (2026-05-13)


### Features

* add llmfit DaemonSet for continuous cluster hardware fitness telemetry ([#186](https://github.com/sympozium-ai/sympozium/issues/186)) ([ec896fa](https://github.com/sympozium-ai/sympozium/commit/ec896fa37c5ba42f184e08e71e22224df9aaf59d))


### Bug Fixes

* update TUI terminology from Personas/Instances to Ensembles/Agents ([#182](https://github.com/sympozium-ai/sympozium/issues/182)) ([7e48fe5](https://github.com/sympozium-ai/sympozium/commit/7e48fe578083190c62999b93f4e107c0adf0a334))
* **web:** resolve ReactFlow render loop and Cypress test failures ([#184](https://github.com/sympozium-ai/sympozium/issues/184)) ([ad06e12](https://github.com/sympozium-ai/sympozium/commit/ad06e12ca7217f06aebd92aa1901d0c6058087a4))

## [0.10.28](https://github.com/sympozium-ai/sympozium/compare/v0.10.27...v0.10.28) (2026-05-09)


### Bug Fixes

* correct CHANGELOG header to match actual version 0.10.27 ([3e5ee70](https://github.com/sympozium-ai/sympozium/commit/3e5ee70a943459beaf7d81514b068926921fd9b0))

## [0.10.27](https://github.com/sympozium-ai/sympozium/compare/v0.8.12...v0.10.27) (2026-05-09)


### ⚠ BREAKING CHANGES

* This is a full ontology rename that affects CRDs, API routes, Go types, controllers, frontend, Helm charts, docs, and tests.
* Ensemble CRD replaces PersonaPack (see commit 432355b).
* The PersonaPack CRD has been renamed to Ensemble. All API endpoints, labels, controllers, and UI references updated.

### Features

* add apiKey support for provider models fetching ([369fab3](https://github.com/sympozium-ai/sympozium/commit/369fab35e02dd9a5effadb9ce68ccd39d14f6b0e))
* add apiKey support for provider models fetching ([fb4bb53](https://github.com/sympozium-ai/sympozium/commit/fb4bb53b302ff0e11b176e9dba2e19a8856d2295))
* add apiKey support for provider models fetching ([f9b69a9](https://github.com/sympozium-ai/sympozium/commit/f9b69a95f681ee0384b0e63e018750ac3aaab441))
* add automated demo walkthrough recording for README GIF ([0945630](https://github.com/sympozium-ai/sympozium/commit/09456301cb845e8720abb64ce59b833fa87ea181))
* add channel access control with sender/chat allowlists and denylists ([b6310ec](https://github.com/sympozium-ai/sympozium/commit/b6310ec55df556a608dd2b6c4867cc3f1d4a454e)), closes [#43](https://github.com/sympozium-ai/sympozium/issues/43)
* add Concepts modal explaining Sympozium ontology ([9d4bef3](https://github.com/sympozium-ai/sympozium/commit/9d4bef347b1b27b6c3446b254117c581b9c85f11))
* add Cypress UX tests for instance creation and persona packs ([2ffb502](https://github.com/sympozium-ai/sympozium/commit/2ffb5026b82b116ab027c09bed58be9b9a02e8f1))
* add Cypress UX tests for instance creation and persona packs ([55e5590](https://github.com/sympozium-ai/sympozium/commit/55e5590af21dbea24e594ec7437052cc89ded4dc))
* add edit capability for schedule cron expressions ([7abd745](https://github.com/sympozium-ai/sympozium/commit/7abd745af1cd9f2cd51f7854020f47baf294dc81))
* add edit capability for schedule cron expressions ([8f5c0b4](https://github.com/sympozium-ai/sympozium/commit/8f5c0b4e3e37288e446c055d34c43b3683f63f72)), closes [#110](https://github.com/sympozium-ai/sympozium/issues/110)
* add ensemble delete button + auto-derive permeability from relationships ([93a8ec1](https://github.com/sympozium-ai/sympozium/commit/93a8ec1c3496742275365ee2f410de7ac488e08a))
* add envtest-based system tests for API server + controllers ([2344132](https://github.com/sympozium-ai/sympozium/commit/2344132a7483162e66fb6f5deea341ff8e39d017))
* add Helm chart repository via GitHub Pages ([a589e82](https://github.com/sympozium-ai/sympozium/commit/a589e821bf112710dfc02bbc8bb22d1f7bbb9503))
* Add image pull secret propagation for agent run container ([51858a3](https://github.com/sympozium-ai/sympozium/commit/51858a3686d9a7593eaf20def93e77ad726825b6))
* Add image pull secret propagation for agentrun sidecar container ([d5f4852](https://github.com/sympozium-ai/sympozium/commit/d5f4852515320378b2a36a31a7ff3e6e083f0f9f))
* add inline editing for PersonaPack personas (system prompt & skills) ([6a46163](https://github.com/sympozium-ai/sympozium/commit/6a461631f871d343162da4ad09b49e2dadef2999))
* add llama-server as a first-class AI provider ([86ec4ae](https://github.com/sympozium-ai/sympozium/commit/86ec4ae6b202488ff5adfd012b9c790557d1a097))
* add LM Studio support for local inference ([53cc0f4](https://github.com/sympozium-ai/sympozium/commit/53cc0f4cf1385ff6b4a4041eccb842e556aa9e1e))
* add Local Model as provider option in ensemble builder ([83f032a](https://github.com/sympozium-ai/sympozium/commit/83f032acada1e360dc57538d7a662b8c70e37c9d))
* add node probe discovery to ensemble builder provider setup ([0576c7e](https://github.com/sympozium-ai/sympozium/commit/0576c7e44191d39e15c2ea7f5ef92a525d80724a))
* add Open Graph and Twitter Card meta tags for link previews ([3f17f3b](https://github.com/sympozium-ai/sympozium/commit/3f17f3be0363db9cb6e5510140478234e82229e2))
* add persona relationships schema and workflow canvas ([ace2bcf](https://github.com/sympozium-ai/sympozium/commit/ace2bcf788612c25e28d0e3e8c582f973d80c90f))
* Add Provider button on builder and detail workflow canvases ([a962f69](https://github.com/sympozium-ai/sympozium/commit/a962f69df181244fe9a6b8f71e3317c68c894a7e))
* add provider icons to wizard dropdown and llama-server docs ([25fca6d](https://github.com/sympozium-ai/sympozium/commit/25fca6dfddf43c18725d6e8ef4f0fa963c097ed3))
* add RBAC permissions for metrics access on pods and nodes ([013b02e](https://github.com/sympozium-ai/sympozium/commit/013b02eede3918664eed3f0018d93e8d66782be8))
* add RBAC permissions for metrics access on pods and nodes ([d94ed79](https://github.com/sympozium-ai/sympozium/commit/d94ed79da573375e22186ebc8e6d5c264e56549d))
* add research-team PersonaPack with relationships and Cypress tests ([9357e0a](https://github.com/sympozium-ai/sympozium/commit/9357e0a2ec3fd0ac354ccc80da5c7c3a79db9d43))
* add Settings page with Agent Sandbox CRD install/uninstall, MCP server auth & defaults ([833bbdc](https://github.com/sympozium-ai/sympozium/commit/833bbdce455457252b7ffc7abf879b74a98a13cd))
* add shared workflow memory for cross-persona knowledge sharing ([3a163dc](https://github.com/sympozium-ai/sympozium/commit/3a163dc5656e9cce1fa8cf5b2cd775e4f91f33a9))
* add SQLite-backed persistent memory with FTS5 search ([#45](https://github.com/sympozium-ai/sympozium/issues/45)) ([28013b7](https://github.com/sympozium-ai/sympozium/commit/28013b7f06299d4265fea0c12867ca4ab43e80ea))
* add Stimulus node type for auto-triggered workflow prompts ([59fc3be](https://github.com/sympozium-ai/sympozium/commit/59fc3be965733570e91da4e6aa2b3fb06ccf7fd3))
* add structured health check matrix to canary UI ([73d54c1](https://github.com/sympozium-ai/sympozium/commit/73d54c1ab07d5d74af2a9ecd0ef68ad28af5df74))
* add subagent ensemble examples to install defaults ([ddb8b0d](https://github.com/sympozium-ai/sympozium/commit/ddb8b0d3f2eb525fd4bbf75b1690fcbffb41d7cf))
* add subagents skill for ad-hoc sub-agent spawning ([#171](https://github.com/sympozium-ai/sympozium/issues/171)) ([2929a80](https://github.com/sympozium-ai/sympozium/commit/2929a80ea9ccb79dde3cc8d8df03f03b4f105937))
* add synthetic membrane layer for shared workflow memory ([5a30192](https://github.com/sympozium-ai/sympozium/commit/5a3019269a3ee9f7e73e5eab6cc30755b52f552d))
* add System Canary — built-in synthetic health testing ensemble ([fef2742](https://github.com/sympozium-ai/sympozium/commit/fef27420c9bff4c4492c14c0df4b71cdf1fdb904))
* add tool-call circuit breaker and configurable run timeout ([b5a3b94](https://github.com/sympozium-ai/sympozium/commit/b5a3b94cefeb6c7cf68a1c6f90181a2f45f28344))
* add topology page to demo walkthrough recording ([ae6d8fc](https://github.com/sympozium-ai/sympozium/commit/ae6d8fc88d4ecdfa81dafc2f044fbdb2a99135f0))
* add workflow relationships to developer-team ensemble ([49d8e85](https://github.com/sympozium-ai/sympozium/commit/49d8e851d14583d40ed8e8f7f42c77869cd0f4ad))
* add workflows to all default ensembles ([6ad01b9](https://github.com/sympozium-ai/sympozium/commit/6ad01b9be9a4c7a23658c120a47269073bdf0ad5))
* add YAML export button to ensemble detail page ([f970d44](https://github.com/sympozium-ai/sympozium/commit/f970d448476a159a2d6d076eff42cafeb6f43dd7))
* adding actions update ([a26187d](https://github.com/sympozium-ai/sympozium/commit/a26187d4ef81e9b1c145cdeda913c969d6833b99))
* adding bedrock ([9359ab1](https://github.com/sympozium-ai/sympozium/commit/9359ab1aea71f2347e38a8a56ca0fa844ae41473))
* adding mcp docs ([9c8955d](https://github.com/sympozium-ai/sympozium/commit/9c8955def47291e3101a6e3dbb5f075b174b7dea))
* agentsandboxing ([25c0bb2](https://github.com/sympozium-ai/sympozium/commit/25c0bb25bb2df5438072c931c8b8f05f3172d4f7))
* auto node placement via llmfit, namespace-aware models, and ModelPolicy groundwork ([2c13faf](https://github.com/sympozium-ai/sympozium/commit/2c13faf67c0139e6bd44b839cc736b4af8245c07))
* auto-detect Agent Sandbox CRDs and surface toggle in UI ([47d56ee](https://github.com/sympozium-ai/sympozium/commit/47d56eee03938cd03e13cd2d3b49a6102eb3ed13))
* auto-detect node probe providers and allow changing ensemble provider ([e79310f](https://github.com/sympozium-ai/sympozium/commit/e79310f0950c9d2e740f37dddc70b4ba2f36f8fb))
* auto-inject delegation/supervision context from ensemble relationships ([e38e93e](https://github.com/sympozium-ai/sympozium/commit/e38e93ef6f930baf3149c4765a14644a1307154f))
* AwaitingDelegate phase, Cypress workflow tests, hooks fix ([8fee27b](https://github.com/sympozium-ai/sympozium/commit/8fee27b9645729c6990d3471dd2240224f26c6c2))
* channel pod CSI compatibility and dedicated service account ([1aa9a99](https://github.com/sympozium-ai/sympozium/commit/1aa9a992d6ca92ec2317c7d30dc2ea12ec27dafc))
* **cypress:** parameterize test model via CYPRESS_TEST_MODEL env var ([b4f68ea](https://github.com/sympozium-ai/sympozium/commit/b4f68ea8dd5ba0ad6eef18476d5630d4d0c486dc))
* **cypress:** parameterize test model via CYPRESS_TEST_MODEL env var ([af6310b](https://github.com/sympozium-ai/sympozium/commit/af6310b0f3ebfe6d361e75b6242bed6572546e53))
* declarative local model inference via Model CRD ([1a6da42](https://github.com/sympozium-ai/sympozium/commit/1a6da42cb691fa0f4569e3fe8cb450f5408f4494))
* declarative local model inference via Model CRD ([4095ea8](https://github.com/sympozium-ai/sympozium/commit/4095ea88ef85f3f32f2a4b7bb809907f648c04a8))
* delegate_to_persona tool and dashboard team canvas widget ([5b25b59](https://github.com/sympozium-ai/sympozium/commit/5b25b596c956ea3896d14a5d8d64d81177b0db6b))
* doc colors ([54178f7](https://github.com/sympozium-ai/sympozium/commit/54178f736f2ba28f4ff34b1b06280e5dd7cae52e))
* enforce ExposeTags and MaxTokensPerRun membrane fields ([b6aa66c](https://github.com/sympozium-ai/sympozium/commit/b6aa66c1b2054169fbe5608163ae5aa50b68b078))
* enhanced dashboard with OTEL observability panels and draggable layout ([c839f33](https://github.com/sympozium-ai/sympozium/commit/c839f335d11666eea4582243a4d3c264ab018afe))
* envtest-based system tests + Cypress fixes ([e173d95](https://github.com/sympozium-ai/sympozium/commit/e173d95afc89f193ccab21eaed7ed2b638d10022))
* expand default MCP server catalog ([ab27fac](https://github.com/sympozium-ai/sympozium/commit/ab27fac64b0b1ebdc6072de351c511439d8869a8))
* expand default MCP server catalog with grafana, kubernetes, argocd, and postgres ([b620dbf](https://github.com/sympozium-ai/sympozium/commit/b620dbfb5aed5a2767bd4d50917e4f4a19ec897f))
* expose run timeout in web UI and CLI TUI ([3bca472](https://github.com/sympozium-ai/sympozium/commit/3bca472642dcf85df6a4f6d0f242f2ed08e3553e))
* fixed token auth ([93cd905](https://github.com/sympozium-ai/sympozium/commit/93cd9052e663628e3790c65de27deea0ee2b6fcb))
* fmt code ([f6f61c3](https://github.com/sympozium-ai/sympozium/commit/f6f61c39e008fc489b2a5ad27ed1bb86295cc8f3))
* fmt code ([fee9454](https://github.com/sympozium-ai/sympozium/commit/fee9454e5cf31cd8e4b8e7e067ba8271bb4ee036))
* **gate:** add response gate hooks with manual approval flow ([0e5ad97](https://github.com/sympozium-ai/sympozium/commit/0e5ad9718826a2b0b776131890a6aad9dcaa5a49))
* global persona canvas and live run status highlighting ([5e69827](https://github.com/sympozium-ai/sympozium/commit/5e69827d36f4e7d9c053c29631ef4071e46833a3))
* implement blocking delegation between ensemble personas ([dc2c7a6](https://github.com/sympozium-ai/sympozium/commit/dc2c7a6cba1cced245ae3390d618e2352b2fd6c7))
* implement sequential workflow trigger in controller ([c5b9e45](https://github.com/sympozium-ai/sympozium/commit/c5b9e456f78261a35043e45e672342dc3eeac0f0))
* improving test quality ([ccecf51](https://github.com/sympozium-ai/sympozium/commit/ccecf5105e54276602a4394695abea9fd49503fe))
* improving UX + whatsapp ([1483d2a](https://github.com/sympozium-ai/sympozium/commit/1483d2a9cd62688c095b41d9e395895ec1fc749c))
* installed status for lm studio ([1e55013](https://github.com/sympozium-ai/sympozium/commit/1e5501346cb5bc51f8f77e6583ecaa1748c06f22))
* interactive canvas editing and persona-targeted spawning ([c3af2ea](https://github.com/sympozium-ai/sympozium/commit/c3af2ea143186de52c9f99f6e499cf48a646a860))
* lifecycle hooks — preRun and postRun containers for agent runs ([a29a8c9](https://github.com/sympozium-ai/sympozium/commit/a29a8c99a67287f063f2b1398b9e499b57e51d35))
* lifecycle hooks — preRun and postRun containers for agent runs ([#67](https://github.com/sympozium-ai/sympozium/issues/67)) ([46250af](https://github.com/sympozium-ai/sympozium/commit/46250afb1e379378e0a82d1d450a811f0a2181dc))
* **makefile:** add ux-tests-serve target for running Cypress against sympozium serve ([e9c3202](https://github.com/sympozium-ai/sympozium/commit/e9c3202d98105eff3d1b7d6008b9b4f7cd7a4d2e))
* multi-provider inference (vLLM, TGI) and cluster topology page ([c434df4](https://github.com/sympozium-ai/sympozium/commit/c434df48788878d3dee87224cde2345a3cca66a7))
* node-probe reverse proxy and keyless provider support ([0a08529](https://github.com/sympozium-ai/sympozium/commit/0a085298246934c6703b20d132d1a8f6d005a8ed))
* promote Team Canvas to prominent dashboard position ([958600a](https://github.com/sympozium-ai/sympozium/commit/958600a3e7cd7d3f506f62607a6e97ce466e965a))
* provider nodes on canvas + per-persona provider overrides ([4bf004a](https://github.com/sympozium-ai/sympozium/commit/4bf004aaf435c44fb7d4e44270e26898a04f56b9))
* provider nodes on dashboard canvas, fix provider-to-agent wiring ([7350791](https://github.com/sympozium-ai/sympozium/commit/73507911d4450d548e8fd8fa494ee61bc6384942))
* **providers:** add Unsloth as a supported local LLM provider ([9c246c1](https://github.com/sympozium-ai/sympozium/commit/9c246c13ba8947b4fe026836e764786b43329126))
* real-time workflow canvas updates via WebSocket ([e3fe61f](https://github.com/sympozium-ai/sympozium/commit/e3fe61f2cfa3ef2d5e6ddaf6e5e215e1399afd35))
* rebase MCP bridge sidecar onto upstream/main ([5d1f215](https://github.com/sympozium-ai/sympozium/commit/5d1f21576385c8a12ad18addc74fa0197d710908))
* recover qwen-native tool_calls from reasoning_content ([f807de1](https://github.com/sympozium-ai/sympozium/commit/f807de172243672997d25c3cd311740b34396fcb))
* rename Instance→Agent, Persona→AgentConfig across entire codebase ([df230ee](https://github.com/sympozium-ai/sympozium/commit/df230eeab513085d4fd713702efd5cfefda41766))
* rename PersonaPack to Ensemble + canvas-first builder ([432355b](https://github.com/sympozium-ai/sympozium/commit/432355bca86ddf8b78d4ac6ec5be708613634bcd))
* replace LLM-based canary with deterministic health checks ([2e25fd1](https://github.com/sympozium-ai/sympozium/commit/2e25fd1a98481362ba382d4240cecf2069533d9b))
* reworked memory implementation ([81fdd0c](https://github.com/sympozium-ai/sympozium/commit/81fdd0c83725dc068bc869f01b5d1af5c421c282))
* shared stimulus view/edit/retrigger dialog across all canvases ([#165](https://github.com/sympozium-ai/sympozium/issues/165)) ([196f219](https://github.com/sympozium-ai/sympozium/commit/196f219b90632b201e8d4fb765ceeb7872a65c9b))
* show local model node on ensemble workflow canvas ([13b08e5](https://github.com/sympozium-ai/sympozium/commit/13b08e5e2f28afd57f7097440d5ba01cc265957a))
* show model node on global ensemble canvas ([3f00fef](https://github.com/sympozium-ai/sympozium/commit/3f00fef205b22c188c55346f6ea07daad63f03f7))
* stimulus node support in builder, unified canvas primitives, and UX fixes ([#162](https://github.com/sympozium-ai/sympozium/issues/162)) ([a57c8f1](https://github.com/sympozium-ai/sympozium/commit/a57c8f1c1ff7d41dcde2bb34ae0c84bf5ce79473))
* subagents skill for ad-hoc sub-agent spawning ([#175](https://github.com/sympozium-ai/sympozium/issues/175)) ([3b6e354](https://github.com/sympozium-ai/sympozium/commit/3b6e3549739079baf4d184594bb6201a88f4fd07))
* synthetic membrane layer for shared workflow memory ([a582317](https://github.com/sympozium-ai/sympozium/commit/a5823176a3e03bd80489ea9542c0c78b2c0b4154))
* topology dagre layout, synthetic membrane page, and UX improvements ([4cef6a2](https://github.com/sympozium-ai/sympozium/commit/4cef6a27b4cf6c01ffd89d7a9659243cf12bc94b))
* updated logo ([2ed44ff](https://github.com/sympozium-ai/sympozium/commit/2ed44ffc0fad6109be0f21deb15c32ebe3aee473))
* updated logo in docs ([a614c20](https://github.com/sympozium-ai/sympozium/commit/a614c20de27b9593d246fc3f64d13a8fecaf6e39))
* updated logo in docs ([9014708](https://github.com/sympozium-ai/sympozium/commit/9014708ec941ead52a23ff419d85045dc213fff4))
* updated tests ([92255c7](https://github.com/sympozium-ai/sympozium/commit/92255c77fdb9e131b5d4666b8dc751ada35ee3da))
* updated UX data ([3841ecc](https://github.com/sympozium-ai/sympozium/commit/3841eccd3902e216d3ccd2548c0f358aab910ecb))
* **web:** add run notifications, unseen watermark, and polling ([42bb00b](https://github.com/sympozium-ai/sympozium/commit/42bb00b9cceae427a0ce3a0c2b12895b94e5e6af))
* **web:** improve sidebar hierarchy, breadcrumbs, and detail page UX ([0a622d1](https://github.com/sympozium-ai/sympozium/commit/0a622d176c0ee0ad536273d5eb61c277a5e778d1))


### Bug Fixes

* add build tag to system tests so go test ./... skips them ([50052f0](https://github.com/sympozium-ai/sympozium/commit/50052f0d10ea250ec7e4984b28db97b98a00347c))
* add mcp-bridge to release image matrix ([0575e8c](https://github.com/sympozium-ai/sympozium/commit/0575e8cec412b22de9e674d2c9450f1e66b22de1))
* add metrics.k8s.io RBAC to config/rbac/role.yaml for sympozium install ([0c1a51c](https://github.com/sympozium-ai/sympozium/commit/0c1a51c8d11354aa5e2df694e8557c120b474857))
* add missing DryRun field and supporting changes omitted from dc2c7a6 ([7f0a4aa](https://github.com/sympozium-ai/sympozium/commit/7f0a4aaf9f17ee46f408a839d512c18590833098))
* add missing MCP server resources to ClusterRole ([0f0786b](https://github.com/sympozium-ai/sympozium/commit/0f0786b5500a3357c5c31b0046caffc8e9df6d3a))
* add missing MCP server resources to ClusterRole ([6a4c744](https://github.com/sympozium-ai/sympozium/commit/6a4c744b6ac3b532a017801e058b923b038544d1))
* add missing nodes RBAC for apiserver — restores topology and cluster status ([58ad746](https://github.com/sympozium-ai/sympozium/commit/58ad746c8fba7d1d18365ee023d5492372acacd7))
* add missing observability-mcp-team persona pack to Helm chart ([fc0105c](https://github.com/sympozium-ai/sympozium/commit/fc0105c0d243bb0adc58680e29a4827b7aad88bd))
* add namespace selection to onboard wizard ([a153585](https://github.com/sympozium-ai/sympozium/commit/a1535853089d0f17055f7ddb089b7c654f619fbc))
* add namespace selection to onboard wizard ([0349276](https://github.com/sympozium-ai/sympozium/commit/0349276d990580e2ffbbd0f8b14446eb8ca663d5)), closes [#24](https://github.com/sympozium-ai/sympozium/issues/24)
* add namespace to default PersonaPack manifests ([25b490a](https://github.com/sympozium-ai/sympozium/commit/25b490ad0ad8ee47aa00b9725c5a19521bd9c50f))
* add new guides and agent-sandbox to mkdocs nav ([efebd3b](https://github.com/sympozium-ai/sympozium/commit/efebd3b911a576a7174a52a0e7528a649caa1a93))
* add part-of label to agent pods so OTEL network policy applies ([eebaf9d](https://github.com/sympozium-ai/sympozium/commit/eebaf9d9a959acb7381e7d672bee7e3b58f851ee))
* adding network policy issues ([c925d19](https://github.com/sympozium-ai/sympozium/commit/c925d194e21944d6e54d2b0886b02c2ee7e699e9))
* AgentRun status concurrency update ([87dbb22](https://github.com/sympozium-ai/sympozium/commit/87dbb2226b22de4106d7c7c90fb77101c4217f38))
* align Agent Sandbox with upstream agents.x-k8s.io API group ([0cd4d69](https://github.com/sympozium-ai/sympozium/commit/0cd4d6928d794920ca9121f0751caa8c45949d8d))
* auto-store task/response in memory server after each agent run ([8f475fb](https://github.com/sympozium-ai/sympozium/commit/8f475fbc2bf600ca7fad12394e7c417dd63e2509))
* canary first run never triggers after duplicate-prevention change ([0bbf126](https://github.com/sympozium-ai/sympozium/commit/0bbf12614d18fb260acb498514d204f34b0f1126))
* canary first run never triggers after duplicate-prevention change ([2e1caeb](https://github.com/sympozium-ai/sympozium/commit/2e1caeb2e0fbdf33b07463f059a5e6f90ec2a2ac))
* canary NetworkPolicy, RBAC, provider resolution, and node-probe routing ([5be1db0](https://github.com/sympozium-ai/sympozium/commit/5be1db0031bcdf19be09521036740ca5861414de))
* canvas empty state — use controlled ReactFlow props for read-only canvases ([58697be](https://github.com/sympozium-ai/sympozium/commit/58697bef2f880488db35c81c82a7a0370fa69f71))
* cascade-delete scheduled AgentRuns when their Schedule is removed ([eb1ad6a](https://github.com/sympozium-ai/sympozium/commit/eb1ad6af113686ae5b77c5d3b28c4ba9a913aabb))
* chain release workflow from release-please via workflow_call ([22c9e1e](https://github.com/sympozium-ai/sympozium/commit/22c9e1e9a17a52907e6c3424855bc82ce1cfb5b1))
* channel status never persisted, always shown as "Unknown" ([#25](https://github.com/sympozium-ai/sympozium/issues/25)) ([065a9bb](https://github.com/sympozium-ai/sympozium/commit/065a9bb5e71465c881ac59ee2eba4108fa468f57))
* correct MCP server configs after local testing ([6d56e57](https://github.com/sympozium-ai/sympozium/commit/6d56e57d17d23cc5db1505cd90299ed1409f2a84))
* CRD detection false negatives and missing provider in instances list ([0ca8398](https://github.com/sympozium-ai/sympozium/commit/0ca8398f84b0d12d14b84bc89fb6841a6f95e628))
* create namespace before Helm config init to fix fresh installs ([e49fa50](https://github.com/sympozium-ai/sympozium/commit/e49fa50f26604688a1dcbba6a3d06543b0442ea8))
* crop gray borders from demo GIF recording ([c300672](https://github.com/sympozium-ai/sympozium/commit/c3006725a6b23bba0ca9200e6404324151a11e74))
* default MCP server catalog to disabled (opt-in) ([d164dc0](https://github.com/sympozium-ai/sympozium/commit/d164dc01fa7488daf8beac7c7f31d43a839ca5fc))
* docs ([c5a5bdb](https://github.com/sympozium-ai/sympozium/commit/c5a5bdb232053c54fbf1321d6601d4ee14495997))
* fail AgentRun when skill RBAC creation fails instead of silently continuing ([99ddb4d](https://github.com/sympozium-ai/sympozium/commit/99ddb4d698bedd758c7d5512e6da354dad5db754))
* fixing otel/memory ([26624aa](https://github.com/sympozium-ai/sympozium/commit/26624aaa5bd60548ac77851cbe8c4c779dec326f))
* format files ([82c867b](https://github.com/sympozium-ai/sympozium/commit/82c867b7b55fd9b1a4ed7151482a482aaaf3860b))
* generate human-readable random agent names instead of persona-1 ([6c53dd3](https://github.com/sympozium-ai/sympozium/commit/6c53dd352a15d67b0f0d156c9da4cc21bad41652))
* gofmt formatting for inference backend files ([8d837bd](https://github.com/sympozium-ai/sympozium/commit/8d837bdd332b68cd544c8ef45962237cf5237710))
* guard stale Job-not-found reconcile during postRun transition ([8d2ff41](https://github.com/sympozium-ai/sympozium/commit/8d2ff41972acb551a9aabc13cc02c1807ca50560))
* heredoc syntax error in ux-test preflight script ([abd0f5d](https://github.com/sympozium-ai/sympozium/commit/abd0f5d5cad7eff3e3983b0ec1603b547e6cc8f6))
* hide system canary from ensembles list ([f7c051c](https://github.com/sympozium-ai/sympozium/commit/f7c051cf84e607a18bd350b54ec922c34467f824))
* include node-probe in release manifests and use localhost for probe targets ([b45749d](https://github.com/sympozium-ai/sympozium/commit/b45749d3c0d8d4b5f61bf0e3589c8db4026b0a77))
* include SympoziumConfig in CLI uninstall resource cleanup ([4d296e4](https://github.com/sympozium-ai/sympozium/commit/4d296e4ea46b60c55d06d184ccb2cad0160b65a2))
* infer local model provider from agent model fields ([6393251](https://github.com/sympozium-ai/sympozium/commit/6393251ff0d8f48ab65fa3361b4a29bab3607566))
* **install:** disable chart namespace template to avoid collision ([e0aae1c](https://github.com/sympozium-ai/sympozium/commit/e0aae1c3a54a95ee6bd5a8d0a2cf1c9c5d9b4b50))
* **install:** recover from failed releases and simplify ns creation ([4c84612](https://github.com/sympozium-ai/sympozium/commit/4c846129d61b99829ef7219c7dc1ed7c4edb6607))
* missing image ([8a8c05b](https://github.com/sympozium-ai/sympozium/commit/8a8c05b9e1ce43c6f51969b32dfc6d34d6a3a5e9))
* model detail page namespace resolution ([894f808](https://github.com/sympozium-ai/sympozium/commit/894f808e533d4d2b8f40de5d411815016b506153))
* multi-arch Docker builds and IPv4 port-forward binding ([c25d378](https://github.com/sympozium-ai/sympozium/commit/c25d378a4d09800f842b5d2a4eeb163777e34863))
* nil pointer dereference in admission webhook decoder ([538c6a0](https://github.com/sympozium-ai/sympozium/commit/538c6a021783ee818a4d78cb881c4f435f642085))
* nil pointer dereference in admission webhook decoder ([bc90f2c](https://github.com/sympozium-ai/sympozium/commit/bc90f2c5f68308191b0101d761b7862918f546c5)), closes [#29](https://github.com/sympozium-ai/sympozium/issues/29)
* node-probe crashloop caused by circular logger reference ([b3068da](https://github.com/sympozium-ai/sympozium/commit/b3068da172c2fe7e8d93ac5c9dfc196e1f39a220))
* node-probe host detection, verbose logging, and LM Studio model listing ([deb19a3](https://github.com/sympozium-ai/sympozium/commit/deb19a36431e9af20bac37062ccb72e36cebd009))
* node-probe issues ([41c13af](https://github.com/sympozium-ai/sympozium/commit/41c13afb8dd65e545aa27f49f9cf4858010f01ff))
* ollama specific test ([89290f6](https://github.com/sympozium-ai/sympozium/commit/89290f693ea72c6a5f334009d74f5c551ba6240d))
* otel/memory write timeout ([2c0f848](https://github.com/sympozium-ai/sympozium/commit/2c0f84804cc490a53cd1acd30d1b7486bbe96bf3))
* per-persona Discord channel routing and truncated run results ([9407420](https://github.com/sympozium-ai/sympozium/commit/9407420c06c64b3289800c83d804a8f62f4acd31))
* per-persona Discord channel routing and truncated run results ([822f9ab](https://github.com/sympozium-ai/sympozium/commit/822f9ab02891673e59cbe2b45d2c6d2071d269fd)), closes [#106](https://github.com/sympozium-ai/sympozium/issues/106) [#107](https://github.com/sympozium-ai/sympozium/issues/107)
* persist baseURL from TUI persona wizard to PersonaPack spec ([186b1f8](https://github.com/sympozium-ai/sympozium/commit/186b1f80f79209d1be3d58160a3397003473fb1d)), closes [#39](https://github.com/sympozium-ai/sympozium/issues/39)
* **personas:** harden platform-team prompts + propagate systemPrompt edits ([079986d](https://github.com/sympozium-ai/sympozium/commit/079986d5e8edc00cd85cf9ed4d715b36f85a589b))
* populate default baseURL for local providers (Ollama, LM Studio) ([e9cd653](https://github.com/sympozium-ai/sympozium/commit/e9cd653b96a9e751a779194fc6d2bd70b69c36bc)), closes [#39](https://github.com/sympozium-ai/sympozium/issues/39)
* prevent apiserver image build timeout on multi-arch builds ([830329d](https://github.com/sympozium-ai/sympozium/commit/830329d94295f04a496594ff494100a9e48fd1e1)), closes [#60](https://github.com/sympozium-ai/sympozium/issues/60)
* prevent canary agent from looping on empty memory ([23e9088](https://github.com/sympozium-ai/sympozium/commit/23e908830326d196bf37ce77802bdbbd2ab8eec3))
* prevent canvas crash when model data hasn't loaded yet ([ddb5410](https://github.com/sympozium-ai/sympozium/commit/ddb541073fc81afe65901c58fd7595078ea5b3f2))
* prevent duplicate canary runs on first schedule trigger ([1f5e286](https://github.com/sympozium-ai/sympozium/commit/1f5e2864ecc5bd421ddfc0fa73f0533e963c7f55))
* prevent duplicate canary runs on first schedule trigger ([1428d68](https://github.com/sympozium-ai/sympozium/commit/1428d68df148a004e60e9e7e47d11902a094fea6))
* prevent panic when AgentRun has nil Labels map ([#170](https://github.com/sympozium-ai/sympozium/issues/170)) ([6f0792f](https://github.com/sympozium-ai/sympozium/commit/6f0792ff7da72faf76b5f9012337c2d9f35b375c))
* prevent port-forward reconnect loop when port is already bound ([c39199e](https://github.com/sympozium-ai/sympozium/commit/c39199eb4d07541f61c88ea018fa50b990b6234e))
* prevent reconcile race from overriding Succeeded AgentRuns as Failed ([d681a33](https://github.com/sympozium-ai/sympozium/commit/d681a3359f1d64ec2d8755402c0abe3849d96e8a))
* prevent UI token mismatch after helm upgrade ([32bd78c](https://github.com/sympozium-ai/sympozium/commit/32bd78c8532efd0c4fdd1df49b7b432c31e1b928))
* prevent UI token mismatch after helm upgrade ([dac1e87](https://github.com/sympozium-ai/sympozium/commit/dac1e8783bcc8fca0122f470b1d3800587bb5e7d)), closes [#113](https://github.com/sympozium-ai/sympozium/issues/113)
* propagate baseURL through personapack pipeline for local providers ([5025077](https://github.com/sympozium-ai/sympozium/commit/5025077347be219bd8c7fd022eab472d9b08c201))
* propagate Memory.SystemPrompt to AgentRuns ([#169](https://github.com/sympozium-ai/sympozium/issues/169)) ([bc20d3d](https://github.com/sympozium-ai/sympozium/commit/bc20d3dbe78ef560218140987c1043f89750ceec))
* propagate skill changes to existing Agents on ensemble update ([2a498c7](https://github.com/sympozium-ai/sympozium/commit/2a498c733bf10b5494572e850410b7c1339983b7))
* publish TopicAgentRunFailed from controller so web proxy unblocks on failure ([b98841f](https://github.com/sympozium-ai/sympozium/commit/b98841fe441a3c3f478640963c270fd7ed31dd85))
* recover from stuck Terminating namespace during install ([#173](https://github.com/sympozium-ai/sympozium/issues/173)) ([30d59a8](https://github.com/sympozium-ai/sympozium/commit/30d59a8b9976a542b7cc0e60bf97413f8345d51a))
* regenerate deepcopy and sync CRDs/Helm ([5703c23](https://github.com/sympozium-ai/sympozium/commit/5703c23215ea4c2d02cf818fc4e101f1be3a77af))
* regenerate PersonaPack CRD to include baseURL field ([0650240](https://github.com/sympozium-ai/sympozium/commit/0650240706c8ca5c777d890a8de52fd7f914945b))
* remove conflicting namespace pre-creation in helm install ([9930ba4](https://github.com/sympozium-ai/sympozium/commit/9930ba4497989fa40d2461e9bef7039586c67aa0))
* remove duplicate YamlButton import in ensemble-detail ([a82a493](https://github.com/sympozium-ai/sympozium/commit/a82a4931072bef5f35a088ee26542819d2b8c41a))
* remove explicit host from node-probe targets to restore auto-detection ([f91229a](https://github.com/sympozium-ai/sympozium/commit/f91229afa5ba5ad0674ba6c9b202932b2a869f3f))
* remove hardcoded dark fills from architecture diagrams ([328827b](https://github.com/sympozium-ai/sympozium/commit/328827b6e6aa57da765953d3504b391cd6662a60))
* remove redundant lock button from topology zoom controls ([2e07dc9](https://github.com/sympozium-ai/sympozium/commit/2e07dc9491f8c4086c2113536eee4d41eea32136))
* rename Add Persona→Add Agent, hide GPT models for local model, sort skills ([827f695](https://github.com/sympozium-ai/sympozium/commit/827f6953992244abf28e6987ee5cc49c8dda8127))
* rename personas→agentConfigs in default ensemble YAML files ([95c4453](https://github.com/sympozium-ai/sympozium/commit/95c445389d60fd623e2548e52daa39a7ba761c94))
* render markdown in feed task messages ([7275510](https://github.com/sympozium-ai/sympozium/commit/72755103e0b679330a7576f378cea4a02eb0e22d))
* replace all remaining Instance→Agent in user-facing UI strings ([b3ceb3d](https://github.com/sympozium-ai/sympozium/commit/b3ceb3d1ed2b3c22ef21eba32c1c205dfac271a2))
* resolve all Cypress TypeScript errors ([008266e](https://github.com/sympozium-ai/sympozium/commit/008266efbcec1f39e4929c89c3bf79cb581e3d23))
* resolve Docker build TS errors for provider nodes ([91147fb](https://github.com/sympozium-ai/sympozium/commit/91147fbb9dfe589c24aa9dacc64a8270879d4545))
* resolve Docker build TS errors from Instance→Agent rename ([926b5d7](https://github.com/sympozium-ai/sympozium/commit/926b5d7c5115d3ced126d2fe6f25be1d5223ddfc))
* resolve flaky Cypress tests for run-delete and run-notifications ([74bab5a](https://github.com/sympozium-ai/sympozium/commit/74bab5a59cca869862facb1bd9e62edb9fbbcc71))
* resolve integration test hang and flaky secret-not-found error ([2fb431f](https://github.com/sympozium-ai/sympozium/commit/2fb431f99b42e14f6f123dbf6f62229ea3a06db0))
* resolve remaining TypeScript index signature errors in yaml-panel ([8cea011](https://github.com/sympozium-ai/sympozium/commit/8cea0119064536a30ba8a1a15d119af73c9380a9))
* resolve TypeScript errors in ensemble canvas model node types ([b0fa56f](https://github.com/sympozium-ai/sympozium/commit/b0fa56f531990e3ceb6f97417538a4443563a543))
* resolve TypeScript index signature errors in yaml-panel ([4a576a1](https://github.com/sympozium-ai/sympozium/commit/4a576a1b8db3f77c7ee6cb610b08f212b3ab9cd0))
* restore node-probe-daemonset.yaml removed during rebase ([1b3f81f](https://github.com/sympozium-ai/sympozium/commit/1b3f81fd80721438e2280f8486d026289d33a1ce))
* restrict WhatsApp channel to self-chat messages only ([3425eb8](https://github.com/sympozium-ai/sympozium/commit/3425eb80290f95ce229e91850caef1f1db4e8e6b))
* restrict WhatsApp channel to self-chat messages only ([6af4dca](https://github.com/sympozium-ai/sympozium/commit/6af4dcaad6f9e2372e917c4f95e3dd952c706c3e)), closes [#138](https://github.com/sympozium-ai/sympozium/issues/138)
* run make generate for new policy and model types ([2449644](https://github.com/sympozium-ai/sympozium/commit/244964411417572c54ca07f7ec2028a73c048842))
* run only smoke tests in CI integration workflow ([bf1c293](https://github.com/sympozium-ai/sympozium/commit/bf1c293374c6a90fa842f704d99efbad45783fdd))
* scale down controller before stripping finalizers on uninstall ([ef8381f](https://github.com/sympozium-ai/sympozium/commit/ef8381fcadb372fb0a28c05fd076cc5229af9b06))
* scheduler picks next free run-number suffix to avoid ghost runs ([205829a](https://github.com/sympozium-ai/sympozium/commit/205829a2c1525d2b2cf5fbdb09829b254790f601))
* security hardening for Model, AgentRun, and Ensemble features ([21fc58d](https://github.com/sympozium-ai/sympozium/commit/21fc58dc46b3ad0935f184afd90fd5359cd8e5eb))
* show API key input for all credential-based providers ([e0bcf58](https://github.com/sympozium-ai/sympozium/commit/e0bcf586590fe9a6366ad1e5fa67598c7dcd2cd7)), closes [#37](https://github.com/sympozium-ai/sympozium/issues/37)
* skip global namespace filter on model API endpoints ([454c872](https://github.com/sympozium-ai/sympozium/commit/454c8720d3d3e9140ba75c70fdc5cacaab81fdb5))
* skip Helm CreateNamespace when sympozium-system already exists ([e40b157](https://github.com/sympozium-ai/sympozium/commit/e40b157a238de6b91cd8f0e0e18c771eb66e5a0d))
* skip mcp-bridge skill in projected volume to prevent FailedMount ([7ad48f6](https://github.com/sympozium-ai/sympozium/commit/7ad48f6ae49a69df65fd90f76667b354f80a6211))
* sort runs list by creation date descending (fixes [#151](https://github.com/sympozium-ai/sympozium/issues/151)) ([bed055c](https://github.com/sympozium-ai/sympozium/commit/bed055c97af6abdff50edb8e93e2bf14bd164fca))
* split architecture diagram into readable sections and fix middot entity ([bc2be59](https://github.com/sympozium-ai/sympozium/commit/bc2be594b447d3d7fba4dd20bbb045bdc2725760))
* stabilize workflow canvas layout across data refreshes ([b83378a](https://github.com/sympozium-ai/sympozium/commit/b83378a1ff88c4546781598fc4bd52e65dd22ce9))
* stimulus save, canvas sync, agentrun cleanup, and default MCP servers ([ad6752f](https://github.com/sympozium-ai/sympozium/commit/ad6752fa3bad572062aa6c88269f2bb761c5a203))
* stop Helm template from overriding node-probe host auto-detection ([4f0e5f4](https://github.com/sympozium-ai/sympozium/commit/4f0e5f41217d5ec9bf165dda7796be0df3fd307d))
* strip directory prefix from CRD names when writing to temp dir ([1906327](https://github.com/sympozium-ai/sympozium/commit/1906327b3abd32dc887f5a09c98eada9e0fb09b6))
* surface reasoning-model responses when terminal turn is empty ([045f7d7](https://github.com/sympozium-ai/sympozium/commit/045f7d74a2f95b5ebab7eba392c6d4441734368b))
* tighten canary system prompt to prevent command retries ([c226a02](https://github.com/sympozium-ai/sympozium/commit/c226a02fa28a71bd81780c005a07eed2fca3c7c3))
* topology page TypeScript build errors ([8a9b712](https://github.com/sympozium-ai/sympozium/commit/8a9b712e55827c975e653c9d3f4a3779ea5684af))
* trigger docs rebuild after Helm chart publish ([9b0e03c](https://github.com/sympozium-ai/sympozium/commit/9b0e03c8a10c477f0e64995ca578c4a15021eccd))
* unify canary connection test and fix agent-run NetworkPolicy ([3909012](https://github.com/sympozium-ai/sympozium/commit/39090124376dcd2b94a481cfb2e87e8aa6406dd6))
* update all stale Persona Pack UI strings to Ensemble ([12fdaec](https://github.com/sympozium-ai/sympozium/commit/12fdaec4c6f73cc9f9febe87bd9d3ed61644f3ed))
* update API key retrieval to use header instead of query parameter ([e320e8d](https://github.com/sympozium-ai/sympozium/commit/e320e8d8361107acf30af4d35b9df2cd866c0cda))
* update API key retrieval to use header instead of query parameter ([ba6281a](https://github.com/sympozium-ai/sympozium/commit/ba6281a546a18f2b42193c5203049b08eb4eb983))
* update cypress tests for stimulus relationship in research-team pack ([e21dff9](https://github.com/sympozium-ai/sympozium/commit/e21dff97b4a6db335e56dec7db1dd787f8822ef4))
* update expected default Ensemble count from 5 to 6 ([e2aedf3](https://github.com/sympozium-ai/sympozium/commit/e2aedf3d5bf23f1ccddf6f9191338ad005d929bb))
* update RBAC rules to include metrics.k8s.io permissions for skill sidecars ([cad5b4a](https://github.com/sympozium-ai/sympozium/commit/cad5b4a7eef051efd239604e472be905b4d28d21))
* update RBAC rules to include metrics.k8s.io permissions for skill sidecars ([3f90317](https://github.com/sympozium-ai/sympozium/commit/3f90317d172cc8d43a0d37b952196f48b3f73fe5))
* use correct installedPersonas field in ensemble detail page ([b62eea4](https://github.com/sympozium-ai/sympozium/commit/b62eea46f38e92d2d052b0a3b3afa7b1d7a0af71))
* use namespace dropdown in model deploy dialog ([e2eef80](https://github.com/sympozium-ai/sympozium/commit/e2eef80ef844f4a574c3605c7e6014018b3a4810))
* use sentinel value for run timeout Select to avoid Radix crash ([1553b75](https://github.com/sympozium-ai/sympozium/commit/1553b75912c1ed4037bd622de09abeaed57f290d))
* validate instance name as RFC 1123 subdomain in wizard ([714a405](https://github.com/sympozium-ai/sympozium/commit/714a4059ebd356e434bdfe941ed68cf1ca2501e7))
* **web-proxy:** close subscribe-before-find race and skip terminal runs ([71786b7](https://github.com/sympozium-ai/sympozium/commit/71786b736dc6ec8ef93c0ecaf31d04f5a2771a45))
* **web-proxy:** close subscribe-before-find race and skip terminal runs ([77c1267](https://github.com/sympozium-ai/sympozium/commit/77c12673dad1d21d063418a527c88ae1c85018b7))
* **web-proxy:** dedupe retried chat requests ([bec7af2](https://github.com/sympozium-ai/sympozium/commit/bec7af286bd761016659c838af7d1819172bc48b))
* **web-proxy:** dedupe retried chat requests ([d4233f3](https://github.com/sympozium-ai/sympozium/commit/d4233f3f4fc7c8153e1b7f2d9737d36c7340b988))
* **web:** prevent gateway toggle from disconnecting UI ([9ededbd](https://github.com/sympozium-ai/sympozium/commit/9ededbd830eb7c409e4340a40b222fd1c7651de4))
* **web:** prevent gateway toggle from disconnecting UI ([3ef4772](https://github.com/sympozium-ai/sympozium/commit/3ef4772500b15a0149be8c1242ff48154ceb8ee0))
* wire modelRef into ensemble creation and fix persona default model ([85a504a](https://github.com/sympozium-ai/sympozium/commit/85a504a2f25da1e999ff58c4d9283a4251db9c2e))
* wiring a local model provider updates agent config panel and node ([05b8f28](https://github.com/sympozium-ai/sympozium/commit/05b8f28bc1788536acbc3492d6346e7a5f8c0d25))


### Miscellaneous Chores

* release 0.8.13 ([8a6fa7b](https://github.com/sympozium-ai/sympozium/commit/8a6fa7b870da36f0df6ab0bcccaeda6b1f04fec4))

## [0.10.25](https://github.com/sympozium-ai/sympozium/compare/v0.10.24...v0.10.25) (2026-05-08)


### Bug Fixes

* use correct installedPersonas field in ensemble detail page ([b62eea4](https://github.com/sympozium-ai/sympozium/commit/b62eea46f38e92d2d052b0a3b3afa7b1d7a0af71))

## [0.10.24](https://github.com/sympozium-ai/sympozium/compare/v0.10.23...v0.10.24) (2026-05-08)


### Features

* subagents skill for ad-hoc sub-agent spawning ([#175](https://github.com/sympozium-ai/sympozium/issues/175)) ([3b6e354](https://github.com/sympozium-ai/sympozium/commit/3b6e3549739079baf4d184594bb6201a88f4fd07))


### Bug Fixes

* recover from stuck Terminating namespace during install ([#173](https://github.com/sympozium-ai/sympozium/issues/173)) ([30d59a8](https://github.com/sympozium-ai/sympozium/commit/30d59a8b9976a542b7cc0e60bf97413f8345d51a))

## [0.10.23](https://github.com/sympozium-ai/sympozium/compare/v0.10.22...v0.10.23) (2026-05-08)


### Features

* add subagents skill for ad-hoc sub-agent spawning ([#171](https://github.com/sympozium-ai/sympozium/issues/171)) ([2929a80](https://github.com/sympozium-ai/sympozium/commit/2929a80ea9ccb79dde3cc8d8df03f03b4f105937))


### Bug Fixes

* prevent panic when AgentRun has nil Labels map ([#170](https://github.com/sympozium-ai/sympozium/issues/170)) ([6f0792f](https://github.com/sympozium-ai/sympozium/commit/6f0792ff7da72faf76b5f9012337c2d9f35b375c))

## [0.10.22](https://github.com/sympozium-ai/sympozium/compare/v0.10.21...v0.10.22) (2026-05-07)


### Bug Fixes

* prevent port-forward reconnect loop when port is already bound ([c39199e](https://github.com/sympozium-ai/sympozium/commit/c39199eb4d07541f61c88ea018fa50b990b6234e))
* propagate Memory.SystemPrompt to AgentRuns ([#169](https://github.com/sympozium-ai/sympozium/issues/169)) ([bc20d3d](https://github.com/sympozium-ai/sympozium/commit/bc20d3dbe78ef560218140987c1043f89750ceec))

## [0.10.21](https://github.com/sympozium-ai/sympozium/compare/v0.10.20...v0.10.21) (2026-05-07)


### Features

* shared stimulus view/edit/retrigger dialog across all canvases ([#165](https://github.com/sympozium-ai/sympozium/issues/165)) ([196f219](https://github.com/sympozium-ai/sympozium/commit/196f219b90632b201e8d4fb765ceeb7872a65c9b))


### Bug Fixes

* update cypress tests for stimulus relationship in research-team pack ([e21dff9](https://github.com/sympozium-ai/sympozium/commit/e21dff97b4a6db335e56dec7db1dd787f8822ef4))

## [0.10.20](https://github.com/sympozium-ai/sympozium/compare/v0.10.19...v0.10.20) (2026-05-07)


### Features

* topology dagre layout, synthetic membrane page, and UX improvements ([4cef6a2](https://github.com/sympozium-ai/sympozium/commit/4cef6a27b4cf6c01ffd89d7a9659243cf12bc94b))


### Bug Fixes

* sort runs list by creation date descending (fixes [#151](https://github.com/sympozium-ai/sympozium/issues/151)) ([bed055c](https://github.com/sympozium-ai/sympozium/commit/bed055c97af6abdff50edb8e93e2bf14bd164fca))

## [0.10.19](https://github.com/sympozium-ai/sympozium/compare/v0.10.18...v0.10.19) (2026-05-06)


### Features

* add envtest-based system tests for API server + controllers ([2344132](https://github.com/sympozium-ai/sympozium/commit/2344132a7483162e66fb6f5deea341ff8e39d017))
* channel pod CSI compatibility and dedicated service account ([1aa9a99](https://github.com/sympozium-ai/sympozium/commit/1aa9a992d6ca92ec2317c7d30dc2ea12ec27dafc))
* envtest-based system tests + Cypress fixes ([e173d95](https://github.com/sympozium-ai/sympozium/commit/e173d95afc89f193ccab21eaed7ed2b638d10022))
* stimulus node support in builder, unified canvas primitives, and UX fixes ([#162](https://github.com/sympozium-ai/sympozium/issues/162)) ([a57c8f1](https://github.com/sympozium-ai/sympozium/commit/a57c8f1c1ff7d41dcde2bb34ae0c84bf5ce79473))


### Bug Fixes

* add build tag to system tests so go test ./... skips them ([50052f0](https://github.com/sympozium-ai/sympozium/commit/50052f0d10ea250ec7e4984b28db97b98a00347c))
* propagate skill changes to existing Agents on ensemble update ([2a498c7](https://github.com/sympozium-ai/sympozium/commit/2a498c733bf10b5494572e850410b7c1339983b7))
* resolve flaky Cypress tests for run-delete and run-notifications ([74bab5a](https://github.com/sympozium-ai/sympozium/commit/74bab5a59cca869862facb1bd9e62edb9fbbcc71))

## [0.10.18](https://github.com/sympozium-ai/sympozium/compare/v0.10.17...v0.10.18) (2026-05-05)


### Features

* add Stimulus node type for auto-triggered workflow prompts ([59fc3be](https://github.com/sympozium-ai/sympozium/commit/59fc3be965733570e91da4e6aa2b3fb06ccf7fd3))

## [0.10.17](https://github.com/sympozium-ai/sympozium/compare/v0.10.16...v0.10.17) (2026-05-03)


### Features

* **cypress:** parameterize test model via CYPRESS_TEST_MODEL env var ([b4f68ea](https://github.com/sympozium-ai/sympozium/commit/b4f68ea8dd5ba0ad6eef18476d5630d4d0c486dc))
* **cypress:** parameterize test model via CYPRESS_TEST_MODEL env var ([af6310b](https://github.com/sympozium-ai/sympozium/commit/af6310b0f3ebfe6d361e75b6242bed6572546e53))


### Bug Fixes

* restrict WhatsApp channel to self-chat messages only ([3425eb8](https://github.com/sympozium-ai/sympozium/commit/3425eb80290f95ce229e91850caef1f1db4e8e6b))
* restrict WhatsApp channel to self-chat messages only ([6af4dca](https://github.com/sympozium-ai/sympozium/commit/6af4dcaad6f9e2372e917c4f95e3dd952c706c3e)), closes [#138](https://github.com/sympozium-ai/sympozium/issues/138)
* **web-proxy:** close subscribe-before-find race and skip terminal runs ([71786b7](https://github.com/sympozium-ai/sympozium/commit/71786b736dc6ec8ef93c0ecaf31d04f5a2771a45))
* **web-proxy:** close subscribe-before-find race and skip terminal runs ([77c1267](https://github.com/sympozium-ai/sympozium/commit/77c12673dad1d21d063418a527c88ae1c85018b7))
* **web-proxy:** dedupe retried chat requests ([bec7af2](https://github.com/sympozium-ai/sympozium/commit/bec7af286bd761016659c838af7d1819172bc48b))
* **web-proxy:** dedupe retried chat requests ([d4233f3](https://github.com/sympozium-ai/sympozium/commit/d4233f3f4fc7c8153e1b7f2d9737d36c7340b988))
* **web:** prevent gateway toggle from disconnecting UI ([9ededbd](https://github.com/sympozium-ai/sympozium/commit/9ededbd830eb7c409e4340a40b222fd1c7651de4))
* **web:** prevent gateway toggle from disconnecting UI ([3ef4772](https://github.com/sympozium-ai/sympozium/commit/3ef4772500b15a0149be8c1242ff48154ceb8ee0))

## [0.10.16](https://github.com/sympozium-ai/sympozium/compare/v0.10.15...v0.10.16) (2026-05-01)


### Features

* auto-inject delegation/supervision context from ensemble relationships ([e38e93e](https://github.com/sympozium-ai/sympozium/commit/e38e93ef6f930baf3149c4765a14644a1307154f))


### Bug Fixes

* canary first run never triggers after duplicate-prevention change ([0bbf126](https://github.com/sympozium-ai/sympozium/commit/0bbf12614d18fb260acb498514d204f34b0f1126))
* canary first run never triggers after duplicate-prevention change ([2e1caeb](https://github.com/sympozium-ai/sympozium/commit/2e1caeb2e0fbdf33b07463f059a5e6f90ec2a2ac))

## [0.10.15](https://github.com/sympozium-ai/sympozium/compare/v0.10.14...v0.10.15) (2026-05-01)


### Features

* expand default MCP server catalog ([ab27fac](https://github.com/sympozium-ai/sympozium/commit/ab27fac64b0b1ebdc6072de351c511439d8869a8))
* expand default MCP server catalog with grafana, kubernetes, argocd, and postgres ([b620dbf](https://github.com/sympozium-ai/sympozium/commit/b620dbfb5aed5a2767bd4d50917e4f4a19ec897f))


### Bug Fixes

* correct MCP server configs after local testing ([6d56e57](https://github.com/sympozium-ai/sympozium/commit/6d56e57d17d23cc5db1505cd90299ed1409f2a84))
* default MCP server catalog to disabled (opt-in) ([d164dc0](https://github.com/sympozium-ai/sympozium/commit/d164dc01fa7488daf8beac7c7f31d43a839ca5fc))
* prevent duplicate canary runs on first schedule trigger ([1f5e286](https://github.com/sympozium-ai/sympozium/commit/1f5e2864ecc5bd421ddfc0fa73f0533e963c7f55))
* prevent duplicate canary runs on first schedule trigger ([1428d68](https://github.com/sympozium-ai/sympozium/commit/1428d68df148a004e60e9e7e47d11902a094fea6))

## [0.10.14](https://github.com/sympozium-ai/sympozium/compare/v0.10.13...v0.10.14) (2026-05-01)


### Features

* add structured health check matrix to canary UI ([73d54c1](https://github.com/sympozium-ai/sympozium/commit/73d54c1ab07d5d74af2a9ecd0ef68ad28af5df74))
* replace LLM-based canary with deterministic health checks ([2e25fd1](https://github.com/sympozium-ai/sympozium/commit/2e25fd1a98481362ba382d4240cecf2069533d9b))


### Bug Fixes

* canary NetworkPolicy, RBAC, provider resolution, and node-probe routing ([5be1db0](https://github.com/sympozium-ai/sympozium/commit/5be1db0031bcdf19be09521036740ca5861414de))
* hide system canary from ensembles list ([f7c051c](https://github.com/sympozium-ai/sympozium/commit/f7c051cf84e607a18bd350b54ec922c34467f824))
* tighten canary system prompt to prevent command retries ([c226a02](https://github.com/sympozium-ai/sympozium/commit/c226a02fa28a71bd81780c005a07eed2fca3c7c3))

## [0.10.13](https://github.com/sympozium-ai/sympozium/compare/v0.10.12...v0.10.13) (2026-04-30)


### Bug Fixes

* include SympoziumConfig in CLI uninstall resource cleanup ([4d296e4](https://github.com/sympozium-ai/sympozium/commit/4d296e4ea46b60c55d06d184ccb2cad0160b65a2))
* prevent canary agent from looping on empty memory ([23e9088](https://github.com/sympozium-ai/sympozium/commit/23e908830326d196bf37ce77802bdbbd2ab8eec3))

## [0.10.12](https://github.com/sympozium-ai/sympozium/compare/v0.10.11...v0.10.12) (2026-04-30)


### Features

* add System Canary — built-in synthetic health testing ensemble ([fef2742](https://github.com/sympozium-ai/sympozium/commit/fef27420c9bff4c4492c14c0df4b71cdf1fdb904))


### Bug Fixes

* render markdown in feed task messages ([7275510](https://github.com/sympozium-ai/sympozium/commit/72755103e0b679330a7576f378cea4a02eb0e22d))
* unify canary connection test and fix agent-run NetworkPolicy ([3909012](https://github.com/sympozium-ai/sympozium/commit/39090124376dcd2b94a481cfb2e87e8aa6406dd6))

## [0.10.11](https://github.com/sympozium-ai/sympozium/compare/v0.10.10...v0.10.11) (2026-04-29)


### Features

* enforce ExposeTags and MaxTokensPerRun membrane fields ([b6aa66c](https://github.com/sympozium-ai/sympozium/commit/b6aa66c1b2054169fbe5608163ae5aa50b68b078))


### Bug Fixes

* add missing nodes RBAC for apiserver — restores topology and cluster status ([58ad746](https://github.com/sympozium-ai/sympozium/commit/58ad746c8fba7d1d18365ee023d5492372acacd7))

## [0.10.10](https://github.com/sympozium-ai/sympozium/compare/v0.10.9...v0.10.10) (2026-04-29)


### Bug Fixes

* run make generate for new policy and model types ([2449644](https://github.com/sympozium-ai/sympozium/commit/244964411417572c54ca07f7ec2028a73c048842))

## [0.10.9](https://github.com/sympozium-ai/sympozium/compare/v0.10.8...v0.10.9) (2026-04-29)


### Bug Fixes

* security hardening for Model, AgentRun, and Ensemble features ([21fc58d](https://github.com/sympozium-ai/sympozium/commit/21fc58dc46b3ad0935f184afd90fd5359cd8e5eb))

## [0.10.8](https://github.com/sympozium-ai/sympozium/compare/v0.10.7...v0.10.8) (2026-04-28)


### Features

* add ensemble delete button + auto-derive permeability from relationships ([93a8ec1](https://github.com/sympozium-ai/sympozium/commit/93a8ec1c3496742275365ee2f410de7ac488e08a))
* add synthetic membrane layer for shared workflow memory ([5a30192](https://github.com/sympozium-ai/sympozium/commit/5a3019269a3ee9f7e73e5eab6cc30755b52f552d))
* synthetic membrane layer for shared workflow memory ([a582317](https://github.com/sympozium-ai/sympozium/commit/a5823176a3e03bd80489ea9542c0c78b2c0b4154))


### Bug Fixes

* update expected default Ensemble count from 5 to 6 ([e2aedf3](https://github.com/sympozium-ai/sympozium/commit/e2aedf3d5bf23f1ccddf6f9191338ad005d929bb))

## [0.10.7](https://github.com/sympozium-ai/sympozium/compare/v0.10.6...v0.10.7) (2026-04-28)


### Bug Fixes

* add missing DryRun field and supporting changes omitted from dc2c7a6 ([7f0a4aa](https://github.com/sympozium-ai/sympozium/commit/7f0a4aaf9f17ee46f408a839d512c18590833098))

## [0.10.6](https://github.com/sympozium-ai/sympozium/compare/v0.10.5...v0.10.6) (2026-04-27)


### Features

* add topology page to demo walkthrough recording ([ae6d8fc](https://github.com/sympozium-ai/sympozium/commit/ae6d8fc88d4ecdfa81dafc2f044fbdb2a99135f0))
* implement blocking delegation between ensemble personas ([dc2c7a6](https://github.com/sympozium-ai/sympozium/commit/dc2c7a6cba1cced245ae3390d618e2352b2fd6c7))

## [0.10.5](https://github.com/sympozium-ai/sympozium/compare/v0.10.4...v0.10.5) (2026-04-27)


### Bug Fixes

* topology page TypeScript build errors ([8a9b712](https://github.com/sympozium-ai/sympozium/commit/8a9b712e55827c975e653c9d3f4a3779ea5684af))

## [0.10.4](https://github.com/sympozium-ai/sympozium/compare/v0.10.3...v0.10.4) (2026-04-27)


### Features

* multi-provider inference (vLLM, TGI) and cluster topology page ([c434df4](https://github.com/sympozium-ai/sympozium/commit/c434df48788878d3dee87224cde2345a3cca66a7))


### Bug Fixes

* gofmt formatting for inference backend files ([8d837bd](https://github.com/sympozium-ai/sympozium/commit/8d837bdd332b68cd544c8ef45962237cf5237710))
* remove redundant lock button from topology zoom controls ([2e07dc9](https://github.com/sympozium-ai/sympozium/commit/2e07dc9491f8c4086c2113536eee4d41eea32136))

## [0.10.3](https://github.com/sympozium-ai/sympozium/compare/v0.10.2...v0.10.3) (2026-04-26)


### Features

* add automated demo walkthrough recording for README GIF ([0945630](https://github.com/sympozium-ai/sympozium/commit/09456301cb845e8720abb64ce59b833fa87ea181))


### Bug Fixes

* crop gray borders from demo GIF recording ([c300672](https://github.com/sympozium-ai/sympozium/commit/c3006725a6b23bba0ca9200e6404324151a11e74))

## [0.10.2](https://github.com/sympozium-ai/sympozium/compare/v0.10.1...v0.10.2) (2026-04-26)


### Features

* add YAML export button to ensemble detail page ([f970d44](https://github.com/sympozium-ai/sympozium/commit/f970d448476a159a2d6d076eff42cafeb6f43dd7))


### Bug Fixes

* remove duplicate YamlButton import in ensemble-detail ([a82a493](https://github.com/sympozium-ai/sympozium/commit/a82a4931072bef5f35a088ee26542819d2b8c41a))

## [0.10.1](https://github.com/sympozium-ai/sympozium/compare/v0.10.0...v0.10.1) (2026-04-26)


### Bug Fixes

* infer local model provider from agent model fields ([6393251](https://github.com/sympozium-ai/sympozium/commit/6393251ff0d8f48ab65fa3361b4a29bab3607566))

## [0.10.0](https://github.com/sympozium-ai/sympozium/compare/v0.9.5...v0.10.0) (2026-04-26)


### ⚠ BREAKING CHANGES

* This is a full ontology rename that affects CRDs, API routes, Go types, controllers, frontend, Helm charts, docs, and tests.

### Features

* add Concepts modal explaining Sympozium ontology ([9d4bef3](https://github.com/sympozium-ai/sympozium/commit/9d4bef347b1b27b6c3446b254117c581b9c85f11))
* add Local Model as provider option in ensemble builder ([83f032a](https://github.com/sympozium-ai/sympozium/commit/83f032acada1e360dc57538d7a662b8c70e37c9d))
* Add Provider button on builder and detail workflow canvases ([a962f69](https://github.com/sympozium-ai/sympozium/commit/a962f69df181244fe9a6b8f71e3317c68c894a7e))
* add workflows to all default ensembles ([6ad01b9](https://github.com/sympozium-ai/sympozium/commit/6ad01b9be9a4c7a23658c120a47269073bdf0ad5))
* provider nodes on canvas + per-persona provider overrides ([4bf004a](https://github.com/sympozium-ai/sympozium/commit/4bf004aaf435c44fb7d4e44270e26898a04f56b9))
* provider nodes on dashboard canvas, fix provider-to-agent wiring ([7350791](https://github.com/sympozium-ai/sympozium/commit/73507911d4450d548e8fd8fa494ee61bc6384942))
* rename Instance→Agent, Persona→AgentConfig across entire codebase ([df230ee](https://github.com/sympozium-ai/sympozium/commit/df230eeab513085d4fd713702efd5cfefda41766))


### Bug Fixes

* generate human-readable random agent names instead of persona-1 ([6c53dd3](https://github.com/sympozium-ai/sympozium/commit/6c53dd352a15d67b0f0d156c9da4cc21bad41652))
* model detail page namespace resolution ([894f808](https://github.com/sympozium-ai/sympozium/commit/894f808e533d4d2b8f40de5d411815016b506153))
* prevent canvas crash when model data hasn't loaded yet ([ddb5410](https://github.com/sympozium-ai/sympozium/commit/ddb541073fc81afe65901c58fd7595078ea5b3f2))
* rename Add Persona→Add Agent, hide GPT models for local model, sort skills ([827f695](https://github.com/sympozium-ai/sympozium/commit/827f6953992244abf28e6987ee5cc49c8dda8127))
* rename personas→agentConfigs in default ensemble YAML files ([95c4453](https://github.com/sympozium-ai/sympozium/commit/95c445389d60fd623e2548e52daa39a7ba761c94))
* replace all remaining Instance→Agent in user-facing UI strings ([b3ceb3d](https://github.com/sympozium-ai/sympozium/commit/b3ceb3d1ed2b3c22ef21eba32c1c205dfac271a2))
* resolve Docker build TS errors for provider nodes ([91147fb](https://github.com/sympozium-ai/sympozium/commit/91147fbb9dfe589c24aa9dacc64a8270879d4545))
* resolve Docker build TS errors from Instance→Agent rename ([926b5d7](https://github.com/sympozium-ai/sympozium/commit/926b5d7c5115d3ced126d2fe6f25be1d5223ddfc))
* wire modelRef into ensemble creation and fix persona default model ([85a504a](https://github.com/sympozium-ai/sympozium/commit/85a504a2f25da1e999ff58c4d9283a4251db9c2e))
* wiring a local model provider updates agent config panel and node ([05b8f28](https://github.com/sympozium-ai/sympozium/commit/05b8f28bc1788536acbc3492d6346e7a5f8c0d25))

## [0.9.5](https://github.com/sympozium-ai/sympozium/compare/v0.9.4...v0.9.5) (2026-04-25)


### Features

* show local model node on ensemble workflow canvas ([13b08e5](https://github.com/sympozium-ai/sympozium/commit/13b08e5e2f28afd57f7097440d5ba01cc265957a))
* show model node on global ensemble canvas ([3f00fef](https://github.com/sympozium-ai/sympozium/commit/3f00fef205b22c188c55346f6ea07daad63f03f7))


### Bug Fixes

* resolve TypeScript errors in ensemble canvas model node types ([b0fa56f](https://github.com/sympozium-ai/sympozium/commit/b0fa56f531990e3ceb6f97417538a4443563a543))
* skip global namespace filter on model API endpoints ([454c872](https://github.com/sympozium-ai/sympozium/commit/454c8720d3d3e9140ba75c70fdc5cacaab81fdb5))
* use namespace dropdown in model deploy dialog ([e2eef80](https://github.com/sympozium-ai/sympozium/commit/e2eef80ef844f4a574c3605c7e6014018b3a4810))

## [0.9.4](https://github.com/sympozium-ai/sympozium/compare/v0.9.3...v0.9.4) (2026-04-25)


### Features

* auto node placement via llmfit, namespace-aware models, and ModelPolicy groundwork ([2c13faf](https://github.com/sympozium-ai/sympozium/commit/2c13faf67c0139e6bd44b839cc736b4af8245c07))

## [0.9.3](https://github.com/sympozium-ai/sympozium/compare/v0.9.2...v0.9.3) (2026-04-25)


### Features

* declarative local model inference via Model CRD ([1a6da42](https://github.com/sympozium-ai/sympozium/commit/1a6da42cb691fa0f4569e3fe8cb450f5408f4494))
* declarative local model inference via Model CRD ([4095ea8](https://github.com/sympozium-ai/sympozium/commit/4095ea88ef85f3f32f2a4b7bb809907f648c04a8))


### Bug Fixes

* prevent UI token mismatch after helm upgrade ([32bd78c](https://github.com/sympozium-ai/sympozium/commit/32bd78c8532efd0c4fdd1df49b7b432c31e1b928))
* prevent UI token mismatch after helm upgrade ([dac1e87](https://github.com/sympozium-ai/sympozium/commit/dac1e8783bcc8fca0122f470b1d3800587bb5e7d)), closes [#113](https://github.com/sympozium-ai/sympozium/issues/113)

## [0.9.2](https://github.com/sympozium-ai/sympozium/compare/v0.9.1...v0.9.2) (2026-04-22)


### Bug Fixes

* per-persona Discord channel routing and truncated run results ([9407420](https://github.com/sympozium-ai/sympozium/commit/9407420c06c64b3289800c83d804a8f62f4acd31))
* per-persona Discord channel routing and truncated run results ([822f9ab](https://github.com/sympozium-ai/sympozium/commit/822f9ab02891673e59cbe2b45d2c6d2071d269fd)), closes [#106](https://github.com/sympozium-ai/sympozium/issues/106) [#107](https://github.com/sympozium-ai/sympozium/issues/107)

## [0.9.1](https://github.com/sympozium-ai/sympozium/compare/v0.9.0...v0.9.1) (2026-04-19)


### Features

* add node probe discovery to ensemble builder provider setup ([0576c7e](https://github.com/sympozium-ai/sympozium/commit/0576c7e44191d39e15c2ea7f5ef92a525d80724a))
* add workflow relationships to developer-team ensemble ([49d8e85](https://github.com/sympozium-ai/sympozium/commit/49d8e851d14583d40ed8e8f7f42c77869cd0f4ad))
* auto-detect node probe providers and allow changing ensemble provider ([e79310f](https://github.com/sympozium-ai/sympozium/commit/e79310f0950c9d2e740f37dddc70b4ba2f36f8fb))


### Bug Fixes

* heredoc syntax error in ux-test preflight script ([abd0f5d](https://github.com/sympozium-ai/sympozium/commit/abd0f5d5cad7eff3e3983b0ec1603b547e6cc8f6))
* stabilize workflow canvas layout across data refreshes ([b83378a](https://github.com/sympozium-ai/sympozium/commit/b83378a1ff88c4546781598fc4bd52e65dd22ce9))

## [0.9.0](https://github.com/sympozium-ai/sympozium/compare/v0.8.28...v0.9.0) (2026-04-19)


### ⚠ BREAKING CHANGES

* Ensemble CRD replaces PersonaPack (see commit 432355b).
* The PersonaPack CRD has been renamed to Ensemble. All API endpoints, labels, controllers, and UI references updated.

### Features

* add shared workflow memory for cross-persona knowledge sharing ([3a163dc](https://github.com/sympozium-ai/sympozium/commit/3a163dc5656e9cce1fa8cf5b2cd775e4f91f33a9))
* implement sequential workflow trigger in controller ([c5b9e45](https://github.com/sympozium-ai/sympozium/commit/c5b9e456f78261a35043e45e672342dc3eeac0f0))
* real-time workflow canvas updates via WebSocket ([e3fe61f](https://github.com/sympozium-ai/sympozium/commit/e3fe61f2cfa3ef2d5e6ddaf6e5e215e1399afd35))
* rename PersonaPack to Ensemble + canvas-first builder ([432355b](https://github.com/sympozium-ai/sympozium/commit/432355bca86ddf8b78d4ac6ec5be708613634bcd))


### Bug Fixes

* resolve all Cypress TypeScript errors ([008266e](https://github.com/sympozium-ai/sympozium/commit/008266efbcec1f39e4929c89c3bf79cb581e3d23))
* update all stale Persona Pack UI strings to Ensemble ([12fdaec](https://github.com/sympozium-ai/sympozium/commit/12fdaec4c6f73cc9f9febe87bd9d3ed61644f3ed))

## [0.8.28](https://github.com/sympozium-ai/sympozium/compare/v0.8.27...v0.8.28) (2026-04-16)


### Features

* promote Team Canvas to prominent dashboard position ([958600a](https://github.com/sympozium-ai/sympozium/commit/958600a3e7cd7d3f506f62607a6e97ce466e965a))

## [0.8.27](https://github.com/sympozium-ai/sympozium/compare/v0.8.26...v0.8.27) (2026-04-16)


### Features

* add persona relationships schema and workflow canvas ([ace2bcf](https://github.com/sympozium-ai/sympozium/commit/ace2bcf788612c25e28d0e3e8c582f973d80c90f))
* add research-team PersonaPack with relationships and Cypress tests ([9357e0a](https://github.com/sympozium-ai/sympozium/commit/9357e0a2ec3fd0ac354ccc80da5c7c3a79db9d43))
* AwaitingDelegate phase, Cypress workflow tests, hooks fix ([8fee27b](https://github.com/sympozium-ai/sympozium/commit/8fee27b9645729c6990d3471dd2240224f26c6c2))
* delegate_to_persona tool and dashboard team canvas widget ([5b25b59](https://github.com/sympozium-ai/sympozium/commit/5b25b596c956ea3896d14a5d8d64d81177b0db6b))
* global persona canvas and live run status highlighting ([5e69827](https://github.com/sympozium-ai/sympozium/commit/5e69827d36f4e7d9c053c29631ef4071e46833a3))
* interactive canvas editing and persona-targeted spawning ([c3af2ea](https://github.com/sympozium-ai/sympozium/commit/c3af2ea143186de52c9f99f6e499cf48a646a860))


### Bug Fixes

* canvas empty state — use controlled ReactFlow props for read-only canvases ([58697be](https://github.com/sympozium-ai/sympozium/commit/58697bef2f880488db35c81c82a7a0370fa69f71))

## [0.8.26](https://github.com/sympozium-ai/sympozium/compare/v0.8.25...v0.8.26) (2026-04-16)


### Features

* add Settings page with Agent Sandbox CRD install/uninstall, MCP server auth & defaults ([833bbdc](https://github.com/sympozium-ai/sympozium/commit/833bbdce455457252b7ffc7abf879b74a98a13cd))


### Bug Fixes

* validate instance name as RFC 1123 subdomain in wizard ([714a405](https://github.com/sympozium-ai/sympozium/commit/714a4059ebd356e434bdfe941ed68cf1ca2501e7))

## [0.8.25](https://github.com/sympozium-ai/sympozium/compare/v0.8.24...v0.8.25) (2026-04-12)


### Features

* add provider icons to wizard dropdown and llama-server docs ([25fca6d](https://github.com/sympozium-ai/sympozium/commit/25fca6dfddf43c18725d6e8ef4f0fa963c097ed3))

## [0.8.24](https://github.com/sympozium-ai/sympozium/compare/v0.8.23...v0.8.24) (2026-04-12)


### Features

* add llama-server as a first-class AI provider ([86ec4ae](https://github.com/sympozium-ai/sympozium/commit/86ec4ae6b202488ff5adfd012b9c790557d1a097))
* fmt code ([f6f61c3](https://github.com/sympozium-ai/sympozium/commit/f6f61c39e008fc489b2a5ad27ed1bb86295cc8f3))

## [0.8.23](https://github.com/sympozium-ai/sympozium/compare/v0.8.22...v0.8.23) (2026-04-11)


### Bug Fixes

* **install:** disable chart namespace template to avoid collision ([e0aae1c](https://github.com/sympozium-ai/sympozium/commit/e0aae1c3a54a95ee6bd5a8d0a2cf1c9c5d9b4b50))
* **install:** recover from failed releases and simplify ns creation ([4c84612](https://github.com/sympozium-ai/sympozium/commit/4c846129d61b99829ef7219c7dc1ed7c4edb6607))

## [0.8.22](https://github.com/sympozium-ai/sympozium/compare/v0.8.21...v0.8.22) (2026-04-10)


### Features

* fmt code ([fee9454](https://github.com/sympozium-ai/sympozium/commit/fee9454e5cf31cd8e4b8e7e067ba8271bb4ee036))

## [0.8.21](https://github.com/sympozium-ai/sympozium/compare/v0.8.20...v0.8.21) (2026-04-10)


### Features

* **gate:** add response gate hooks with manual approval flow ([0e5ad97](https://github.com/sympozium-ai/sympozium/commit/0e5ad9718826a2b0b776131890a6aad9dcaa5a49))

## [0.8.20](https://github.com/sympozium-ai/sympozium/compare/v0.8.19...v0.8.20) (2026-04-07)


### Features

* **web:** add run notifications, unseen watermark, and polling ([42bb00b](https://github.com/sympozium-ai/sympozium/commit/42bb00b9cceae427a0ce3a0c2b12895b94e5e6af))

## [0.8.19](https://github.com/sympozium-ai/sympozium/compare/v0.8.18...v0.8.19) (2026-04-07)


### Features

* **providers:** add Unsloth as a supported local LLM provider ([9c246c1](https://github.com/sympozium-ai/sympozium/commit/9c246c13ba8947b4fe026836e764786b43329126))
* **web:** improve sidebar hierarchy, breadcrumbs, and detail page UX ([0a622d1](https://github.com/sympozium-ai/sympozium/commit/0a622d176c0ee0ad536273d5eb61c277a5e778d1))


### Bug Fixes

* **personas:** harden platform-team prompts + propagate systemPrompt edits ([079986d](https://github.com/sympozium-ai/sympozium/commit/079986d5e8edc00cd85cf9ed4d715b36f85a589b))

## [0.8.18](https://github.com/sympozium-ai/sympozium/compare/v0.8.17...v0.8.18) (2026-04-05)


### Bug Fixes

* cascade-delete scheduled AgentRuns when their Schedule is removed ([eb1ad6a](https://github.com/sympozium-ai/sympozium/commit/eb1ad6af113686ae5b77c5d3b28c4ba9a913aabb))
* scheduler picks next free run-number suffix to avoid ghost runs ([205829a](https://github.com/sympozium-ai/sympozium/commit/205829a2c1525d2b2cf5fbdb09829b254790f601))

## [0.8.17](https://github.com/sympozium-ai/sympozium/compare/v0.8.16...v0.8.17) (2026-04-05)


### Features

* **makefile:** add ux-tests-serve target for running Cypress against sympozium serve ([e9c3202](https://github.com/sympozium-ai/sympozium/commit/e9c3202d98105eff3d1b7d6008b9b4f7cd7a4d2e))


### Bug Fixes

* prevent reconcile race from overriding Succeeded AgentRuns as Failed ([d681a33](https://github.com/sympozium-ai/sympozium/commit/d681a3359f1d64ec2d8755402c0abe3849d96e8a))

## [0.8.16](https://github.com/sympozium-ai/sympozium/compare/v0.8.15...v0.8.16) (2026-04-04)


### Features

* recover qwen-native tool_calls from reasoning_content ([f807de1](https://github.com/sympozium-ai/sympozium/commit/f807de172243672997d25c3cd311740b34396fcb))

## [0.8.15](https://github.com/sympozium-ai/sympozium/compare/v0.8.14...v0.8.15) (2026-04-04)


### Bug Fixes

* surface reasoning-model responses when terminal turn is empty ([045f7d7](https://github.com/sympozium-ai/sympozium/commit/045f7d74a2f95b5ebab7eba392c6d4441734368b))

## [0.8.14](https://github.com/sympozium-ai/sympozium/compare/v0.8.13...v0.8.14) (2026-04-04)


### Bug Fixes

* skip Helm CreateNamespace when sympozium-system already exists ([e40b157](https://github.com/sympozium-ai/sympozium/commit/e40b157a238de6b91cd8f0e0e18c771eb66e5a0d))

## [0.8.13](https://github.com/sympozium-ai/sympozium/compare/v0.8.12...v0.8.13) (2026-04-04)


### Miscellaneous Chores

* release 0.8.13 ([8a6fa7b](https://github.com/sympozium-ai/sympozium/commit/8a6fa7b870da36f0df6ab0bcccaeda6b1f04fec4))

## [0.8.12](https://github.com/sympozium-ai/sympozium/compare/v0.8.11...v0.8.12) (2026-04-04)


### Bug Fixes

* publish TopicAgentRunFailed from controller so web proxy unblocks on failure ([b98841f](https://github.com/sympozium-ai/sympozium/commit/b98841fe441a3c3f478640963c270fd7ed31dd85))

## [0.8.11](https://github.com/sympozium-ai/sympozium/compare/v0.8.10...v0.8.11) (2026-04-04)


### Features

* add Cypress UX tests for instance creation and persona packs ([2ffb502](https://github.com/sympozium-ai/sympozium/commit/2ffb5026b82b116ab027c09bed58be9b9a02e8f1))
* add Cypress UX tests for instance creation and persona packs ([55e5590](https://github.com/sympozium-ai/sympozium/commit/55e5590af21dbea24e594ec7437052cc89ded4dc))
* add tool-call circuit breaker and configurable run timeout ([b5a3b94](https://github.com/sympozium-ai/sympozium/commit/b5a3b94cefeb6c7cf68a1c6f90181a2f45f28344))
* expose run timeout in web UI and CLI TUI ([3bca472](https://github.com/sympozium-ai/sympozium/commit/3bca472642dcf85df6a4f6d0f242f2ed08e3553e))


### Bug Fixes

* resolve integration test hang and flaky secret-not-found error ([2fb431f](https://github.com/sympozium-ai/sympozium/commit/2fb431f99b42e14f6f123dbf6f62229ea3a06db0))
* use sentinel value for run timeout Select to avoid Radix crash ([1553b75](https://github.com/sympozium-ai/sympozium/commit/1553b75912c1ed4037bd622de09abeaed57f290d))

## [0.8.10](https://github.com/sympozium-ai/sympozium/compare/v0.8.9...v0.8.10) (2026-04-04)


### Bug Fixes

* remove conflicting namespace pre-creation in helm install ([9930ba4](https://github.com/sympozium-ai/sympozium/commit/9930ba4497989fa40d2461e9bef7039586c67aa0))

## [0.8.9](https://github.com/sympozium-ai/sympozium/compare/v0.8.8...v0.8.9) (2026-04-02)


### Bug Fixes

* auto-store task/response in memory server after each agent run ([8f475fb](https://github.com/sympozium-ai/sympozium/commit/8f475fbc2bf600ca7fad12394e7c417dd63e2509))
* guard stale Job-not-found reconcile during postRun transition ([8d2ff41](https://github.com/sympozium-ai/sympozium/commit/8d2ff41972acb551a9aabc13cc02c1807ca50560))

## [0.8.8](https://github.com/sympozium-ai/sympozium/compare/v0.8.7...v0.8.8) (2026-04-01)


### Features

* reworked memory implementation ([81fdd0c](https://github.com/sympozium-ai/sympozium/commit/81fdd0c83725dc068bc869f01b5d1af5c421c282))


### Bug Fixes

* add missing observability-mcp-team persona pack to Helm chart ([fc0105c](https://github.com/sympozium-ai/sympozium/commit/fc0105c0d243bb0adc58680e29a4827b7aad88bd))

## [0.8.7](https://github.com/sympozium-ai/sympozium/compare/v0.8.6...v0.8.7) (2026-03-31)


### Bug Fixes

* stop Helm template from overriding node-probe host auto-detection ([4f0e5f4](https://github.com/sympozium-ai/sympozium/commit/4f0e5f41217d5ec9bf165dda7796be0df3fd307d))

## [0.8.6](https://github.com/sympozium-ai/sympozium/compare/v0.8.5...v0.8.6) (2026-03-31)


### Bug Fixes

* create namespace before Helm config init to fix fresh installs ([e49fa50](https://github.com/sympozium-ai/sympozium/commit/e49fa50f26604688a1dcbba6a3d06543b0442ea8))

## [0.8.5](https://github.com/sympozium-ai/sympozium/compare/v0.8.4...v0.8.5) (2026-03-31)


### Bug Fixes

* remove explicit host from node-probe targets to restore auto-detection ([f91229a](https://github.com/sympozium-ai/sympozium/commit/f91229afa5ba5ad0674ba6c9b202932b2a869f3f))

## [0.8.4](https://github.com/sympozium-ai/sympozium/compare/v0.8.3...v0.8.4) (2026-03-31)


### Bug Fixes

* strip directory prefix from CRD names when writing to temp dir ([1906327](https://github.com/sympozium-ai/sympozium/commit/1906327b3abd32dc887f5a09c98eada9e0fb09b6))

## [0.8.3](https://github.com/sympozium-ai/sympozium/compare/v0.8.2...v0.8.3) (2026-03-31)


### Bug Fixes

* add metrics.k8s.io RBAC to config/rbac/role.yaml for sympozium install ([0c1a51c](https://github.com/sympozium-ai/sympozium/commit/0c1a51c8d11354aa5e2df694e8557c120b474857))

## [0.8.2](https://github.com/sympozium-ai/sympozium/compare/v0.8.1...v0.8.2) (2026-03-31)


### Bug Fixes

* resolve remaining TypeScript index signature errors in yaml-panel ([8cea011](https://github.com/sympozium-ai/sympozium/commit/8cea0119064536a30ba8a1a15d119af73c9380a9))

## [0.8.1](https://github.com/sympozium-ai/sympozium/compare/v0.8.0...v0.8.1) (2026-03-31)


### Bug Fixes

* fail AgentRun when skill RBAC creation fails instead of silently continuing ([99ddb4d](https://github.com/sympozium-ai/sympozium/commit/99ddb4d698bedd758c7d5512e6da354dad5db754))
* resolve TypeScript index signature errors in yaml-panel ([4a576a1](https://github.com/sympozium-ai/sympozium/commit/4a576a1b8db3f77c7ee6cb610b08f212b3ab9cd0))

## [0.8.0](https://github.com/sympozium-ai/sympozium/compare/v0.7.0...v0.8.0) (2026-03-30)


### Features

* lifecycle hooks — preRun and postRun containers for agent runs ([a29a8c9](https://github.com/sympozium-ai/sympozium/commit/a29a8c99a67287f063f2b1398b9e499b57e51d35))
* lifecycle hooks — preRun and postRun containers for agent runs ([#67](https://github.com/sympozium-ai/sympozium/issues/67)) ([46250af](https://github.com/sympozium-ai/sympozium/commit/46250afb1e379378e0a82d1d450a811f0a2181dc))


### Bug Fixes

* update API key retrieval to use header instead of query parameter ([e320e8d](https://github.com/sympozium-ai/sympozium/commit/e320e8d8361107acf30af4d35b9df2cd866c0cda))
* update API key retrieval to use header instead of query parameter ([ba6281a](https://github.com/sympozium-ai/sympozium/commit/ba6281a546a18f2b42193c5203049b08eb4eb983))
* update RBAC rules to include metrics.k8s.io permissions for skill sidecars ([cad5b4a](https://github.com/sympozium-ai/sympozium/commit/cad5b4a7eef051efd239604e472be905b4d28d21))
* update RBAC rules to include metrics.k8s.io permissions for skill sidecars ([3f90317](https://github.com/sympozium-ai/sympozium/commit/3f90317d172cc8d43a0d37b952196f48b3f73fe5))

## [0.7.0](https://github.com/sympozium-ai/sympozium/compare/v0.6.1...v0.7.0) (2026-03-29)


### Features

* add apiKey support for provider models fetching ([369fab3](https://github.com/sympozium-ai/sympozium/commit/369fab35e02dd9a5effadb9ce68ccd39d14f6b0e))
* add apiKey support for provider models fetching ([fb4bb53](https://github.com/sympozium-ai/sympozium/commit/fb4bb53b302ff0e11b176e9dba2e19a8856d2295))


### Bug Fixes

* AgentRun status concurrency update ([87dbb22](https://github.com/sympozium-ai/sympozium/commit/87dbb2226b22de4106d7c7c90fb77101c4217f38))
* prevent apiserver image build timeout on multi-arch builds ([830329d](https://github.com/sympozium-ai/sympozium/commit/830329d94295f04a496594ff494100a9e48fd1e1)), closes [#60](https://github.com/sympozium-ai/sympozium/issues/60)

## [0.6.1](https://github.com/sympozium-ai/sympozium/compare/v0.6.0...v0.6.1) (2026-03-28)


### Bug Fixes

* chain release workflow from release-please via workflow_call ([22c9e1e](https://github.com/sympozium-ai/sympozium/commit/22c9e1e9a17a52907e6c3424855bc82ce1cfb5b1))

## [0.6.0](https://github.com/sympozium-ai/sympozium/compare/v0.5.8...v0.6.0) (2026-03-28)


### Features

* Add image pull secret propagation for agent run container ([51858a3](https://github.com/sympozium-ai/sympozium/commit/51858a3686d9a7593eaf20def93e77ad726825b6))
* Add image pull secret propagation for agentrun sidecar container ([d5f4852](https://github.com/sympozium-ai/sympozium/commit/d5f4852515320378b2a36a31a7ff3e6e083f0f9f))
* add RBAC permissions for metrics access on pods and nodes ([013b02e](https://github.com/sympozium-ai/sympozium/commit/013b02eede3918664eed3f0018d93e8d66782be8))
* add RBAC permissions for metrics access on pods and nodes ([d94ed79](https://github.com/sympozium-ai/sympozium/commit/d94ed79da573375e22186ebc8e6d5c264e56549d))
