# Production Web Core Experience 验收状态

变更：`production-web-core-experience`

计划日期：2026-07-28

证据采集：2026-07-29（Asia/Shanghai）

## 结论

本轮完成了生产前端单 owner 清理、仓库静态 shield、自动化门禁和真实本地 Compose 的部分验收。自动化门禁全部通过；真实 Compose 已证明 Home 权威读模型、显式 Assistant Conversation 创建、Workspace 绑定以及四个核心页面的桌面/移动与深/浅主题展示。

本轮不能宣称完整迁移已验收或归档。真实 Assistant 歧义请求在模型调用处被供应商以 HTTP 400 拒绝，导致 clarification/ready draft 之前终止，后续 Task → Worker → Artifact → acceptance → Home 闭环无法继续。因此：

- OpenSpec `6.1`、`6.2`、`6.4` 有匹配证据；
- `6.3`、`6.5` 保持未完成；
- change 保持 active，不同步、不归档；
- 未修改凭证、模型 profile 或请求 temperature，也未删除/重置现有 Compose 数据。

## 单 owner 与仓库清理

- `web/src/styles.css` 已迁移为 `web/src/styles/application.css`，基础 reset、字体、表单和 visually-hidden 规则归入 `web/src/styles/base.css`。
- 已删除无消费者的旧 shell/view/style selector、空的 `web/src/views/` 和 `web/src/workbench/`。
- Worker 只保留 `ArtifactWorkbench` 和分组 durable Timeline：
  - 删除永久 Workbench build flag、Compose/Docker 透传和第二 Artifact UI 路径；
  - 删除 flat Timeline 展示 fallback、Web wire type、fixture、Backend 投影字段及无界 SQL；
  - Backend startup 会把既有 `worker_detail` 快照通过正常 Projection event
    幂等升级，避免 `ReadView` 继续原样发布持久化的旧字段；
  - `timeline_groups`、cursor、has-more 和分页 endpoint 继续作为唯一权威 Timeline 契约。
- `web-demo/` 是用户现有未提交工作，本轮未修改、未暂存。
- 三份 active architecture/acceptance 文档已改为唯一 Workbench owner；Web
  renderer 回滚使用常规 Git revert 和镜像重建，不再描述隐藏 fallback。

先写入的 closeout 契约测试准确失败于仓库级 build flag 和 flat Timeline
runtime contract；Go integration RED 同时证明真实 WorkerDetail snapshot 仍含
顶层 flat 字段。完成删除后的 fresh GREEN 见下节。

R2 进一步发现，只停止新 materialization 不能升级 PostgreSQL 中的既有
`view_projections.data`。修复选择 startup contract upgrade，而不是让 GET
产生写副作用：

- `Materializer.Bootstrap` 只枚举当前 Tenant/User 下仍含旧字段的
  `worker_detail`；
- 每个 View 在独立事务内 `FOR UPDATE`，删除唯一旧字段并保留原
  grouped Timeline、cursor/has-more、actions、refs 和 query fingerprint；
- 通过正常 Projection event 追加一个 critical delta 和 receipt，
  source identity 绑定 WorkerSession、原 epoch 和 cursor；
- 并发刷新与升级共享 View 行锁；重复 restart 在字段已不存在时 no-op，
  不增加 cursor、version、delta 或 receipt；
- 若升级失败，Bootstrap 返回错误，Backend 不以仍会外发旧契约的状态继续启动。

仓库 shield 扩展到全部 tracked files，并对 flat Timeline 的实际 owner 路径
做精确扫描：

```bash
workbench_flag="$(printf '%s%s' 'VITE_WORKER_' 'WORKBENCH_ENABLED')"
disabled_artifact_owner="$(printf '%s%s%s' 'Legacy' 'Artifact' 'List')"
legacy_icons="$(printf '%s%s' 'legacy' 'Icons')"
if git grep -n -F "$workbench_flag" -- .; then exit 1; fi
if git grep -n -F "$disabled_artifact_owner" -- .; then exit 1; fi
if git grep -n -F "$legacy_icons" -- .; then exit 1; fi
if rg -n 'web-demo|web-demo/src/mock|mock/engine' web/src web/tests; then exit 1; fi
make publication-check
if rg -n '"timeline"\s*:' internal/eventingprojection/adapter/postgres/materializer.go; then exit 1; fi
if rg -n 'TimelineEntry|^\s+timeline:\s' web/src/api/contracts.ts web/src/demo/data.ts web/tests/frontierMock.ts; then exit 1; fi
test ! -e web/src/app/router.ts
test ! -e web/src/components/AppShell.tsx
test ! -e web/src/styles.css
test ! -d web/src/views
test ! -d web/src/workbench
git diff --check
```

最终执行时间和结果见下节；全部命令必须 exit `0` 且扫描无匹配输出。

## 自动化门禁

| 时间（Asia/Shanghai） | 命令 | Exit | 结果 |
| --- | --- | ---: | --- |
| `10:38:18` | 仓库级 static shield + `git diff --check` | 0 | 全部 tracked files 无旧 build flag、disabled UI owner、legacy icon owner；生产 Web 无 demo/mock/旧 product copy；flat Timeline materialization owner 路径无匹配 |
| `10:09:14–10:09:21` | `make openspec-check` | 0 | OpenSpec all strict `10/10` 通过，含 `production-web-core-experience` |
| `10:09:26–10:09:27` | `make generated-protocol-check` | 0 | `internal/contracts/v1` 通过 |
| `10:09:31–10:09:34` | `npm --prefix web test` | 0 | 29 files，256 tests 通过，含 closeout repository contract |
| `10:09:42–10:09:43` | `npm --prefix web run build` | 0 | TypeScript 与 Vite production build 通过；2760 modules |
| `10:09:48–10:10:04` | `npm --prefix web run test:browser` | 0 | Chromium 45/45 通过，含 16 个 desktop/mobile × dark/light 视觉组合 |
| `10:10:30–10:12:47` | `env SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58778/switchback_test?sslmode=disable' make verify` | 0 | format、migration、generated、image provenance、Go unit/race/vet/integration、secret scan、OpenSpec all strict 全部通过；投影集成测试证明 flat 字段不再发布且 grouped contract 保留 |
| `10:19:38–10:19:51` | OpenSpec change strict validate + `list --json` | 0 | change valid，`29/31`，状态 `in-progress`；`6.3`、`6.5` 保持未完成，未 sync、未 archive |
| `10:30:15–10:30:16` | preexisting WorkerDetail startup-upgrade integration RED | 1 | 准确失败于 Bootstrap 后既有快照仍存储并发布 flat Timeline |
| `10:31:27–10:31:29` | focused projection integration | 0 | preexisting upgrade、repeat-bootstrap idempotency、replay delta、WorkerTimeline paging、Runtime recovery、Home upgrade 全部通过 |
| `10:31:59–10:32:00` | focused Web closeout/Worker tests | 0 | 3 files，31 tests 通过 |
| `10:32:17–10:32:20` | `npm --prefix web test` | 0 | 29 files，256 tests 通过 |
| `10:32:24–10:32:26` | `npm --prefix web run build` | 0 | TypeScript 与 Vite production build 通过；既有 chunk-size advisory 不变 |
| `10:32:30–10:32:46` | `npm --prefix web run test:browser` | 0 | Chromium 45/45 通过 |
| `10:33:09–10:34:55` | fresh PostgreSQL `make verify` | 0 | format、migration、generated、image provenance、Go unit/race/vet/all integration、secret scan、OpenSpec all strict 全部通过 |
| `10:37:46–10:38:01` | final OpenSpec change strict validate + `list --json` | 0 | change valid，`29/31`，状态 `in-progress`；`6.3`、`6.5` 未变，未 sync、未 archive |

`make verify` 使用一次性 `postgres:16-alpine` 测试容器
`switchback-worker-detail-upgrade-verify-postgres`。门禁后已停止，`--rm`
删除已确认，没有触碰产品数据卷。

Build 保留一个非阻塞 advisory：部分 minified chunks 大于 500 kB；主 JS
约 1.30 MB，gzip 约 381.81 kB。这不是本轮迁移阻塞，但后续可独立做按路由/语言包拆分。

Playwright 使用 Frontier fixtures 验证浏览器契约、失败状态和视觉组合；它不是下节真实 Compose 端到端流的替代证据。

## 本地 Compose

| 时间（Asia/Shanghai） | 命令 | Exit | 结果 |
| --- | --- | ---: | --- |
| `09:29:59` | `make local-config` | 0 | 现有本地配置通过 bootstrap；未改凭证 |
| `10:13:18–10:14:33` | `make local-up` | 0 | 从 review-fix 最终候选工作树重建并拉起 Web、Backend、Assistant LiteLLM、Worker image；migration exit 0 |
| `10:14:38` | `docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml ps` | 0 | Web、Assistant LiteLLM、PostgreSQL、Redis、MinIO、ClickHouse healthy；Backend running |
| `10:14:38` | `curl --fail http://127.0.0.1:4173/` 与 `curl --fail http://127.0.0.1:8080/healthz` | 0 | Web 可访问；Backend 返回 `{"service":"switchback-backend","status":"ok"}` |
| `10:35:28–10:35:49` | R2 `make local-up` | 0 | 从包含 startup upgrade 的最终候选工作树重建并启动；Bootstrap 成功 |
| `10:35:59` | PostgreSQL before/after contract query | 0 | 既有 flat 字段从 `6/6` 变为 `0/6`；6/6 grouped/cursor/has-more 保留；6 receipts、6 critical deltas |
| `10:36:25` | restart Backend + PostgreSQL idempotency check | 0 | 旧字段仍为 `0/6`，receipt 仍为 6；全部 View cursor/version 总和保持 `138/138` |
| `10:36` | Frontier snapshot + WorkerTimeline paging API | 0 | snapshot 无旧字段并保留 grouped contract；`limit=1` paging 返回原 completed Agent turn |

最终重建固定的本地 Worker image digest 为：

```text
sha256:718f051e625379f8e6f8b4d854202bea3a087432538817ebe2075aff39c6f59d
```

没有执行 `local-down`、volume clear、数据库 reset 或其他数据删除动作。

真实升级前后逐 View 比对还证明：

- epoch 全部保持 `1`，cursor/version 各只增加 `1`；
- `(data - legacy field)` 的六个 MD5 全部逐项相同；
- `available_actions`、`related_refs`、query fingerprint 的 digest/value
  全部逐项相同；
- grouped turn 数仍为 `1,1,1,1,1,2`，has-more 均保持 `false`。

## 真实 Compose 流程证据

验收入口：`http://127.0.0.1:4173/`，没有注入 route fixture 或 prototype mock。

| 步骤 | 状态 | 真实可见/运行证据 |
| ---: | --- | --- |
| 1. Home 权威数据 | PASS | Home 展示 viewer-scoped current work、Tasks、WorkerSessions、deliveries、canonical Worker/Artifact refs 和 continuity capacity `6/10`。初次可见过 reconnecting 提示，但可信快照仍展示。 |
| 2. 显式创建 Conversation | PASS | final rebuild 后点击“开始 Assistant 对话”创建 `assistant-conversation-53c565770ce1fb4c1ff8e8c62bc415dd`；打开 `/assistant` 本身没有隐式创建。初始 Frontier snapshot 一次显示 unavailable，点击 retry 后恢复同一权威 Conversation。 |
| 2a. Workspace 绑定 | PASS | 通过 UI 选择 `workspace-local`，绑定 `workspace-revision-local-v1`、`resolution-local` revision 1、membership revision 1；pending command 随权威投影清除。 |
| 3. 发送歧义请求 | FAIL | 发送 `请把这个项目改得更好` 后，UI 显示 `Needs attention`、`The Assistant could not finish this response.` 和 `Retry this response`，未产生 clarification 或 ready draft。 |
| 4. 确认 exact draft | BLOCKED | 步骤 3 未产生 draft；没有可确认的 exact revision。 |
| 5. Task / Worker 进展 | BLOCKED | 没有由该 Conversation 创建 Task；未伪造或复用旧交付作为本次成功证据。 |
| 6. 打开产物 | BLOCKED | 新流没有产物；仅对现有权威 Worker/Artifact 做独立 smoke。 |
| 7. accept / rework | BLOCKED | 新流没有 delivered Task，因此没有执行副作用动作。 |
| 8. Home 更新 | BLOCKED | 没有新 Task/Artifact/acceptance 可投影。 |
| 9. 跨页面 smoke | PASS | review fix final rebuild 后，真实 Compose 的 Home、Assistant、现有 Worker `worker-session-aee26a4ae9ba0bdc14653839e38f6f0a`、现有 Artifact `artifact-report-cf5339c90161405dc4c1ce4050cfdc00` 在 1440×900 与 390×844、dark 与 light 共 16 组合均渲染；0 个组合出现 document overflow；移动导航按钮可见；Worker 继续渲染 grouped Agent turn，Artifact 显示 `Checksum verified`。 |

## 阻塞分类与根因证据

这不是 Docker、网络或 credential 缺失。Assistant LiteLLM 健康且上游返回了结构化
HTTP 400。`docker compose ... logs assistant-litellm` 的 exact evidence：

```text
02:17:53 - LiteLLM Proxy:ERROR
litellm.BadRequestError: OpenAIException -
invalid temperature: only 1 is allowed for this model.
Received Model Group=switchback-assistant-kimi
Available Model Group Fallbacks=None
...
openai.BadRequestError: Error code: 400 -
{'error': {'message': 'invalid temperature: only 1 is allowed for this model',
'type': 'invalid_request_error'}}
...
POST /v1/chat/completions HTTP/1.1" 400 Bad Request
```

当前源码把问题收敛为产品与 provider 的请求契约不匹配：

- `internal/interaction/adapter/litellm/client.go` 的 `completionRequest` 总是包含
  `temperature`，`Complete` 当前硬编码 `Temperature: 0`；
- `internal/platform/config/runtime_profiles.go` 的 `AssistantModelProfile` 只拥有
  `LiteLLMModel`、`ResponseSchema` 和 fallback profile identity，没有请求参数 owner；
- `kimi-assistant-openai-v1` 映射到 `switchback-assistant-kimi`，本地配置实际选择该 profile；
- Kimi Code 官方概览给出的 coding endpoint/model 是 OpenAI-compatible
  `https://api.kimi.com/coding/v1` + `kimi-for-coding`；官方第三方工具说明也提示 Kimi Code
  benefits 只在列出的客户端受到支持，其他平台可能受限：
  [Kimi Code 概览](https://www.kimi.com/code/docs/en/)、
  [第三方工具说明](https://www.kimi.com/help/kimi-code/third-party-agents)。

因此本轮把它分类为 **product/provider contract failure**，不是可通过重试证明通过的
environment blocker。修复需要独立决策参数应由 profile 拥有、切换到现有 DeepSeek
Assistant profile，还是更换合规 endpoint；这些都会改变模型运行策略，超出本次 UI
closeout 授权，不能静默处理。

## 下一步

1. 对 Assistant 请求参数 ownership 做独立设计和回归：禁止继续由 client 为所有
   provider 硬编码同一个 temperature。
2. 在获批方案与可用 provider profile 下，从一个新 Conversation 重新执行步骤 1–9；
   记录 exact draft revision、Task、WorkerRun、Artifact、accept/rework 和最终 Home
   projection identity。
3. 只有该真实闭环通过后，才勾选 `6.3`、`6.5`，同步 main specs 并归档 change。

## 2026-07-29 Kimi Code resolution

本节是对上文历史 HTTP 400 的追加 resolution，不改写当时证据。Assistant 的实际
provider contract 已从 `kimi-assistant-openai-v1` 迁移为
`kimi-code-assistant-openai-v1`，本轮真实产品闭环和全部自动化门禁均通过，因此
`kimi-code-assistant-profile` 的 `18.1` 与本 change 的 `6.3` 现可标记完成。
`19.1` 与 `6.5` 仍保持未完成；本轮没有同步 main specs，也没有归档任一 change。

当前运行契约为：

- Backend profile：`kimi-code-assistant-openai-v1`
- LiteLLM alias：`switchback-assistant-kimi-code`
- model：`openai/kimi-for-coding`
- API base：`https://api.kimi.com/coding/v1`
- Worker image digest：
  `sha256:72303ee7ff4bbc78b1a92e6da5a6bf455309c9d70856a1c8b2deafb887c2a28f`

实际配置只命中 Kimi Code coding endpoint、alias 和 model；没有命中 Kimi Platform
surface，也没有 fallback。上文 `switchback-assistant-kimi` 的 400 仍作为历史失败
证据保留，不能代表当前 active profile。

### Resumed 自动化门禁

| 命令 | Exit | 结果 |
| --- | ---: | --- |
| focused config/localproduct/LiteLLM/Interaction/PostgreSQL/architecture tests | 0 | 当前 Kimi profile 与 Interaction 主路径通过 |
| `go test -race ./internal/interaction/adapter/litellm ./internal/interaction/application` | 0 | race 通过 |
| `validate kimi-code-assistant-profile --strict` | 0 | valid |
| fresh PostgreSQL 16 下 `make verify` | 0 | format、migration、generated、image provenance、Go unit/race/vet/integration、secret scan、OpenSpec all strict 全部通过 |
| `npm --prefix web test` | 0 | 29/29 files，256/256 tests |
| `npm --prefix web run build` | 0 | TypeScript/Vite production build 通过；2760 modules |
| `npm --prefix web run test:browser` | 0 | Chromium 45/45 |

`make verify` 使用无 volume 的一次性 `postgres:16-alpine` 容器
`switchback-task18-resumed-verify-postgres-20260729-a2fdebf-151507-46510`，
仅绑定 `127.0.0.1:60247`。门禁结束后按 exact name 清理并由
`docker inspect` 确认不存在；没有触碰其他 container 或 volume。

首次 closeout 在 fresh PostgreSQL 上暴露出 5 个 Interaction integration fixture
仍读取已退役 `kimi-assistant-openai-v1`。修复提交
`a2fdebf8180b` 只把这 5 个测试夹具迁移到当前
`KimiCodeAssistantOpenAIV1`，没有修改生产、部署或 Web。修复后同一 full gate 全绿，
证明先前失败不是 PostgreSQL、Docker、provider 或 credential 故障。

### 真实产品流与 sanitized evidence

真实验收摘要：

```text
/path/to/switchback/runtime-data/m2-real-provider/run-20260729T064828Z/m2-real-provider-summary.json
```

- schema：`switchback.m2-real-provider-acceptance.v1`
- outcome：`passed`
- SHA-256：
  `a180622165ee899e150dbb4c7e203106ea10cde03aca668be977566bbbab74c2`
- Conversation：
  `assistant-conversation-4555e93e9d736e348ac9fca62a9b8eaa`
- Task：`task-42a8e28fc15be543e08ff887452bba55`
- WorkerSession：
  `worker-session-96667eb6457346eab1537dd1a0f6c1e9`
- initial Run / Turn：
  `worker-run-59382586e87dd4aaa85086d745ef92ff` /
  `agent-turn-7f825af83502d41a6430ed1e96483b38`
- direct Task chat Run / Turn：
  `worker-run-a0a7b1c1353c5c534b482eb8055b74bd` /
  `agent-turn-461ad676197c013c03c164aa3aa0fc60`
- same-Task rework Run / Turn：
  `worker-run-5a82cedf44e34eec13598999b56b2179` /
  `agent-turn-a44ca62585baef830ca36d7f6f75eee5`
- terminal-Task standalone Run / Turn：
  `worker-run-8d0a22016890a3e1cacb68c7a246b5ac` /
  `agent-turn-385487ad43377c41aed66f7e589cfa09`

真实流程覆盖 clarification、user answer、ready draft、exact confirmation、Task handoff、
initial Worker delivery、direct Task chat、same-Task rework、accept、terminal-Task
standalone follow-up、8 个 Artifact detail 及最终 Home projection。Task 最终为
`completed` revision 9；四组 Run/Turn identity 两两不同，Task 和 WorkerSession
identity 在协议要求的边界内保持不变。

Artifact provenance：

| Artifact | Type | Checksum |
| --- | --- | --- |
| `artifact-patch-e7a74abb3f76c56dc8ae301808486651` | patch | `sha256:6a0bbaa2da2de96b04b7176bde58e4e73d66b602ff09e16fc226ff2b62999f51` |
| `artifact-report-91b9c7396bce91308b01c84f088d5a29` | report | `sha256:fda1f146eebf4acfe6dad02388e62a2a4257031f8bc07ebcd2fcb36e2cfd7506` |
| `artifact-patch-7f9cf3638c00dfacaf79f5db8c3c856f` | patch | `sha256:6a0bbaa2da2de96b04b7176bde58e4e73d66b602ff09e16fc226ff2b62999f51` |
| `artifact-report-9322f6979106405815a900e324899c63` | report | `sha256:fc8b21bd28e16107508140445d1a6b23432884a285b66bb4545b7ac73bd339a3` |
| `artifact-patch-7eff08f48c66cf836b9896c6a905adf1` | patch | `sha256:6a0bbaa2da2de96b04b7176bde58e4e73d66b602ff09e16fc226ff2b62999f51` |
| `artifact-report-de96bea3fb5f28357e7dbdea87cf711d` | report | `sha256:f4aba134d4b03b672e5d7ebb3aaf3794f30f42af26490e5ff4907e97a356763d` |
| `artifact-patch-5eaa1dece7cd76412d69ede2f07ccbcc` | patch | `sha256:6a0bbaa2da2de96b04b7176bde58e4e73d66b602ff09e16fc226ff2b62999f51` |
| `artifact-report-9ca3f37270e011e38541bba3326d2d02` | report | `sha256:02954e429a06913e3ef9b07f6e08f0510adf6fe3acbc73254a6fb3fe2e02fd19` |

摘要在 resumed closeout 中被重新验证：路径存在，schema/outcome、sanitized
identity/stage refs 和全部结果布尔值匹配，摘要中 provider envelope、authorization、
API key、raw request/response、`reasoning_content` 与 hidden chain-of-thought marker
均无匹配。原始验收也已验证 browser-facing Worker/Artifact 投影不存在这些字段。
报告不读取、不打印、不复制或 hash key，也不包含 raw provider body。

本轮可安全复用该真实摘要，而不重复消耗 provider 调用：从真实验收基线
`e2eaca5` 到 resumed HEAD 的 changed paths 精确等于两份
`*_integration_test.go` fixture；生产、部署与 Web diff 为 0，摘要 checksum 不变。

### Remaining closeout

- `kimi-code-assistant-profile` 为 `4/5`，只保留 `19.1` 未完成。
- `production-web-core-experience` 为 `30/31`，只保留 `6.5` 未完成。
- 未同步 main specs，未归档 change。
- Vite 仍有既有的 >500 kB chunk advisory，不阻塞本次产品流验收。
- raw ACP Trace 中非 hidden 的 model-metadata wording 可另行降噪；它未进入
  browser projection，也不是 authorization、key、provider envelope 或 hidden
  reasoning。

## 2026-07-30 Kimi Code revision-2 strict-output acceptance

本节只追加 revision 2 的新证据，不改写上文原始 HTTP 400 或 2026-07-29
revision-1 closeout。`kimi-code-assistant-openai-v1` 的历史 revision 1 digest 为
`sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c`；
本次新 AssistantRun 均绑定 revision 2 和
`sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c`。
revision 2 在保留 `temperature=1` 的同时增加 repository-owned strict JSON Schema，
没有重写历史 Run。

### Runtime 与 focused gates

通过 repository-owned `make local-up` 重建 Assistant LiteLLM、Backend、Web 和
Worker image。最终验收重建后的 Worker image digest 为
`sha256:2f1f684ca57c507205a3bd665764b1f17805e30680a939f0e3e6c49b66a5dcf6`。
sanitized preflight 证明：

- Backend active profile 为 `kimi-code-assistant-openai-v1` revision 2，digest 与
  registry 一致；
- sidecar alias 为 `switchback-assistant-kimi-code`，没有 published host port；
- selected alias 精确使用 `openai/kimi-for-coding`、
  `https://api.kimi.com/coding/v1` 和 Kimi Code credential route；
- 静态 dual-profile Compose 仍保留既有 Kimi Code 与 DeepSeek 两个 read-only
  mount，但本次 active alias 只选择 Kimi Code；生产源码中的 Kimi Platform
  endpoint/credential surface 计数为 0。

Task 18 review 遗留的两个 Minor 均确认有效并关闭：Task B 的异步分配收敛窗口从
1 秒扩为有界 5 秒；`activeTurnSupervisors` 新增 completed cleanup 后同 ID 可重启、
以及 `stopAndWait` 后拒绝新 start 的 deterministic channel-driven tests。新测试的
常规和 `-race` 各重复 20 次通过；临时删除 completed-ID cleanup 后 restart test
按预期失败，恢复后重新通过。runtime public contract 和 authority 未改变。

| 命令 | Exit | 结果 |
| --- | ---: | --- |
| brief 原 focused 命令（含不存在的 `./internal/frontier/application`） | 1 | 仅因 stale package path `stat ... directory not found` 失败；未创建假 package |
| corrected focused Go gate：原 packages 加 `internal/eventingprojection/application`、`internal/frontier/http`、`internal/frontier/sse` | 0 | 当前 projection owner、Frontier adapters 与其余 focused packages 全部通过 |
| `go test -race ./internal/interaction/adapter/litellm ./internal/interaction/application` | 0 | race 通过 |
| `sh -n scripts/run-m2-real-provider-acceptance` | 0 | shell syntax 通过 |
| `validate kimi-code-assistant-profile --strict` | 0 | valid |
| `git diff --check` | 0 | clean |
| `SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=kimi-code-assistant-openai-v1 make m2-real-provider-acceptance` | 0 | revision-2 real provider flow passed |
| disposable PostgreSQL 16 下 `make verify` | 0 | format、migration、generated、image、unit、race、vet、integration、secret、OpenSpec 全通过 |
| `npm --prefix web test` | 0 | 29/29 files，256/256 tests |
| `npm --prefix web run build` | 0 | TypeScript/Vite production build 通过；2760 modules |
| `npm --prefix web run test:browser` | 0 | Chromium 45/45 |

`make verify` 使用无 volume 的唯一容器
`switchback-task23-verify-postgres-20260730-64865`，只绑定
`127.0.0.1:50496` 并通过唯一 `SWITCHBACK_TEST_DATABASE_URL` 注入。门禁后按 exact
name 停止、删除，`docker inspect` 已确认不存在；未触碰 Compose PostgreSQL 或其他
container/volume。Vite 仍只有既有的 >500 kB chunk advisory。

### Revision-2 real product evidence

真实验收摘要：

```text
/path/to/switchback/runtime-data/m2-real-provider/run-20260729T161845Z/m2-real-provider-summary.json
```

- summary SHA-256：
  `5d11d91dec3a97924dc4b93896a1696e742183d8f6ed7c51529bd54a08241ed0`
- schema / outcome：
  `switchback.m2-real-provider-acceptance.v1` / `passed`
- Conversation：
  `assistant-conversation-c35069c8c6cd524d4aeb0ff4ffcbc28f`
- AssistantRun：
  `assistant-run-f38530edb335ae8ee4727a893729d404`、
  `assistant-run-5e46a3c03d7d9e3d6354d9cb87f7ec99`
- Task / WorkerSession：
  `task-2b92900c94cdf6ee2bed8eac2c866b2e` /
  `worker-session-77761e860aa40de835d15eb91699a783`
- initial Run / Turn：
  `worker-run-9932089dcc7926291d337f6abaa8e0ec` /
  `agent-turn-76bdc25e98883e87d2d7c1c9b621b4a1`
- direct Task chat Run / Turn：
  `worker-run-1632849f451c2286715993ee84a16ddd` /
  `agent-turn-614dea75a3954cc4a6aa7840b97ccb3e`
- same-Task rework Run / Turn：
  `worker-run-da7c5453593dbe2fd2d485cd0b9ed483` /
  `agent-turn-347e1e64aa6704c746b004a46f29e3b1`
- terminal-Task standalone Run / Turn：
  `worker-run-7fdb28cb0ab581999d4c105a3fa068b1` /
  `agent-turn-4b679833b1f6d0190ac4dc74067b6e93`

canonical direct summary fields 证明 alias 精确匹配、confirmation 后只创建一个 Task、
Home exact-one completed delivery、8/8 Artifact detail projection、rework/accept 和
standalone follow-up 均收敛。Task 最终为 `completed` revision 9，四组 Run/Turn
identity 两两不同，Task 与 WorkerSession identity 按协议保持。

| Artifact | Type | Checksum | Provenance Run / Turn |
| --- | --- | --- | --- |
| `artifact-patch-940c42956a7416b90699e560851bd57d` | patch | `sha256:a2b9acc2f1bf65fd4f54e66c8724bbf34a95af956eba36aa40b41c3e6ad9d966` | initial Run / Turn |
| `artifact-report-8e43fb7c593ef26eb24ab48a6ecaf3dd` | report | `sha256:5c24f4867129a82aced6b93626245eaed12b466fb7ec3e7e5f71bbc631da4072` | initial Run / Turn |
| `artifact-patch-da4590721e8aa991c1721c12f00a9bae` | patch | `sha256:3c24317af8b9a10dc0d405d3522e2f2256d0682606f656997e6ee689a4912e77` | direct Task chat Run / Turn |
| `artifact-report-8681ff49454690b5549a9dabe5c88bc9` | report | `sha256:d4fa46953104e606be0b19635e67b6bb3c37c64405561d9eab55f986dfe775a6` | direct Task chat Run / Turn |
| `artifact-patch-afd14f326f0c26f57e86d9fbb3528252` | patch | `sha256:9f89d985b0bf1306acb38a8370a1cc6f3b7385662bb16e65219c8611fd0da528` | same-Task rework Run / Turn |
| `artifact-report-5825bcb3f6b55ccd75ddb8a596d985e6` | report | `sha256:a359731af4c96391640d87b8bb9b167dd16f63b42ee856c97feee09adae4d280` | same-Task rework Run / Turn |
| `artifact-patch-a8981bc2b2e8fd38ee593703d26a5255` | patch | `sha256:b00361f59d5543afc78065b2fc3a00bb15ee123dcd1b623282fe0a7b887b0f46` | standalone Run / Turn |
| `artifact-report-35fd07cec7e9bc36e5675048f1e0510b` | report | `sha256:23b9e744a7f71526f0d9a86e91ec904ebbdc107d66e489cb038db25bd5657de5` | standalone Run / Turn |

summary、Assistant/Task/Worker/Home 以及 8 个 Artifact browser projection 的 sanitized
scan 均未命中 provider envelope、authorization/API-key field、raw request/response、
`reasoning_content` 或 hidden chain-of-thought marker；Assistant persistence schema
也没有 raw provider request/response surface。验收只在内存中读取 exact credential
用于 absence scan，没有输出、hash 或持久化 key，也没有保存 raw provider body。

`web-demo/` task-entry 与 final tracked diff SHA-256 均为
`149c02e4597397371d0f16d163599afa32193f78c0d9166493f0dfcb34fc21f2`；
8 个原始 untracked 文件逐项 checksum 也完全一致，且未 stage。Task 23 因此完成
revision-2 implementation/product evidence；Kimi Task 19 与 Production Web 6.5 仍未
完成，main specs 未 sync，两个 change 均未 archive，等待新的显式用户批准。

## 🔍 2026-07-30 Task 23 review-fix evidence

本节只追加 independent review 触发的 fail-closed evidence，不改写上文历史 HTTP 400、
revision-1 closeout 或第一次 revision-2 success。Review 的 4 个 Important 与 1 个
Minor 已关闭：

- real verdict path 移除 automatic Retry，每条 Assistant message 只等待 exact Run；
- candidate 在 `$work` 构建，所有 gates 与 final contract 通过后才原子发布 summary；
- exact Conversation 的每个 AssistantRun 都用 sanitized read-only PostgreSQL evidence
  绑定 profile ID/revision/digest；
- privacy gate 覆盖除本地 request fixture 外的全部 browser response JSON；
- Retry key 补齐 same Run/different revision sensitivity。

严格 no-retry 首次执行时，Run
`assistant-run-e85d3b83faac435bd398979a3f4f14e2` 在 Conversation
`assistant-conversation-4fd18d8cd4dbb57a201b175965e2ba8b` 以
`model_policy_conflict`、`attempt_count=1` 失败。验收立即停止且未发布 summary，
没有 Retry。脱敏源码/数据库证据排除了 profile revision/digest 与 ContextPack schema
漂移；selected policy item 已有 exact revision，但模型可见 ContextPack 顶层缺少该
server-owned 标量。它被记录为最高概率、可验证输入缺口，而不是读取 provider body
后得到的唯一根因断言。

新增 regression test 通过真实
`RunClaim -> ContextCompiler -> persisted ContextPack -> ModelRequest -> LiteLLM HTTP`
链路，在修复前稳定 RED 两次：
`model-visible ContextPack policy_revision is absent or invalid`。修复后，
`claim.Run.PolicyRevision` 显式进入持久化 ContextPack 顶层与 `ModelRequest`，
adapter 在 HTTP 前校验两者一致，PostgreSQL writer 拒绝新路径缺失该字段。
`assistant.context_pack.v1` 保持 additive compatibility；旧历史 pack 不回写、不
replay。Decision validator、decision/provider strict schema、fallback 与 retry policy
均未放宽。

修复后 repository-owned rebuild exit `0`，Worker image digest：
`sha256:68c50f539b0d9891351b805b370a642022d2eae4120b2d391d5f1f8d615cdfa5`。
静态 dual-profile Compose 仍保留两个既有 read-only mount；active alias 只使用 Kimi
Code，Kimi Platform 生产 surface 仍为 0。

使用全新 Conversation、全新 output 目录只运行一次 strict no-retry acceptance，
exit `0`。summary：

```text
/path/to/switchback/runtime-data/m2-real-provider/review-fix-policy-20260730-SZLTIG/m2-real-provider-summary.json
```

SHA-256：
`4dbaa84097ae2a28b3c43e999161bb91348060c6dabb786db749e4b2f8f21e36`

Direct identities：

- Conversation `assistant-conversation-ecc09309138a4b866d2be8589a6e167e`
- AssistantRuns `assistant-run-4adf2a1f656a2d150d781c1b219dc89c` 与
  `assistant-run-28bba5e1fec9a9f0193ee2fc4068a06e`
- Task `task-16cbfa03897681a60002c41da1cc40fc`
- WorkerSession `worker-session-01821588f297dd8333bd7526b3808753`
- initial Run/Turn
  `worker-run-1001ae986ec80b202aa3e4ac5243d74e` /
  `agent-turn-6f3f10361656f9e031c16a75ddfa5e54`
- direct Run/Turn
  `worker-run-f4fb6a9ecd477c6c63ad167c2d50a369` /
  `agent-turn-79aa3c67531b2bdd14117f88ced9e96c`
- rework Run/Turn
  `worker-run-c8d4f1c5b7d07b317c876fc88f8f8146` /
  `agent-turn-e3003dba148714f427c5c19c23746446`
- standalone Run/Turn
  `worker-run-a8fa3480b83e93fbc4021fb9bdf57abf` /
  `agent-turn-dfe207748de2a34d9ffa8d46a11cf224`

两个 AssistantRun 均为 `completed`、`attempt_count=1`、Kimi revision `2`/exact
digest；每个 Run 的 Run、ContextPack 顶层与 selected policy revision 均为
`assistant-readiness-v1`。summary 直接记录 count `2`/verified `true`，最终 Task
revision `9`、Task/WorkerRun/AgentTurn 均 `completed`，Home match `1`，Artifact
projection count `8`。旧失败 Run 仍保留原终态。

| Gate | Exit | Evidence |
| --- | ---: | --- |
| focused + race + PostgreSQL policy integration | 0 | 新 policy audit seam 与相关 owners 全通过 |
| `make verify` | 0 | format/migration/generated/image/unit/race/vet/integration/secret/OpenSpec 11/11 |
| Web unit/build/Playwright | 0 | 29/256、2760 modules、45/45 |
| Kimi + Production Web strict validate/list | 0 | valid；11/12 与 30/31 |
| `git diff --check` | 0 | clean |

完整 `make verify` 使用唯一 PostgreSQL 16 容器
`switchback-task23-fix-verify-pg16-8bd38f6b-26b9-411f-bb29-951e4daa1ae6`，
loopback `127.0.0.1:55618`，无 volume；门禁后 exact remove，`docker inspect`
确认不存在。

`web-demo/` tracked diff SHA-256 仍为
`149c02e4597397371d0f16d163599afa32193f78c0d9166493f0dfcb34fc21f2`，
8 个 untracked checksum 逐项不变且未 stage。Kimi `19.1` 与 Production Web `6.5`
保持未勾，未 sync/未 archive。Vite >500 kB chunks 仍是唯一 non-blocking advisory。

## ⚠️ 2026-07-30 final-review correction

上面的 Task 23 summary 与通过结论是历史证据，保持原文件和历史记录不变；它不满足
本轮修复后新增的 exact browser command receipt、Snapshot batch reconciliation 与
Replica fail-closed settlement 契约。当前修复 HEAD 尚未执行新的真实 provider
canonical acceptance，因此不能把历史 summary 解释为当前 HEAD 的 closeout 证据。

Kimi OpenSpec Task 23.1/23.2 已重新打开。只有 Task 29 在冻结本修复的 exact HEAD 和
三类唯一不可变镜像后，以一次、隔离、`--no-build`/no-pull/no-retag 执行产出并验证
新的 canonical summary，才允许重新完成这两项。Kimi 19.1、spec sync 与 archive
仍不在本轮授权范围。

### 2026-07-30 follow-up review hardening

后续复审进一步收紧了 command receipt 契约，但不改变上面的真实 provider 边界：

- `event.v1` 保持可读且 schema 中不存在 `client_command_id`；当前 writer 统一写
  `event.v2`。Outbox dispatcher 在调用 consumer 前检查其显式版本能力，旧
  `event.v1` consumer 遇到 `event.v2` 时释放 lease 并保持 unpublished。
- 浏览器 Replica 把 HTTP 返回前到达的 raw/server delta 只作为待确认观察；只有匹配
  HTTP 结果或 exact Snapshot receipt 才结算。所有 active `commandId` 与
  `observedCommandId` 参与一一映射冲突检查，歧义传输失败不再删除待确认命令。
- receipt reader 使用命名的 30 天 reconciliation horizon。Projection transaction
  每次按 exact tenant/user/View 最多清理 256 条 horizon 外记录；该批量大于单事件
  每 View 最多新增的一条记录。30 天内 receipt 不按数量淘汰，避免高活跃 View
  破坏仍合法的浏览器 retry；低活跃 View 无新事件时物理历史不会继续增长。
- receipt-bearing ETag 同时绑定规范化请求集合和规范化返回映射，因此不同的空结果
  reconciliation query 不再共享缓存身份。
- 已提交的 `000021_projection_command_receipts.sql` 保持历史字节不变，SHA-256
  固定为 `5a152e096295c0e60ad7b069e8a52e71aec487b2aa65818d322f2c5aed05fefc`。
  v2 CHECK、v1/client `NOT VALID` 约束和 receipt cleanup index 改由增量
  `000022_integration_event_v2_and_receipt_retention.sql` 应用。其 up 在同一 runner
  事务中的首句取得 `SHARE ROW EXCLUSIVE` Outbox 锁，再在任何 DDL 前拒绝仍未
  published 的 v1+client 行，并要求先由 v1-compatible binary 完成 drain；current
  dispatcher 不兼容该非法旧 wire shape，也不代替旧 binary 改写 digest。
  drain 完成后，`NOT VALID` 只为已 published 的 v1+client 行保留原始 wire bytes 和
  source receipt digest；这些 grandfather 行是只读保真数据，不做 v2 重写，迁移后的
  新 INSERT/UPDATE 仍拒绝 v1+client。down 同样先锁 Outbox，再在任何 DDL 前检查已
  持久化 `event.v2`，存在时原样 fail closed；没有 v2 行时恢复 exact `000021`
  schema，grandfather 行和 digest 也保持不变。确定性 PG16 并发测试覆盖 writer-first
  与 migration-first：迁移等待未提交旧写入后能看见并拒绝它；迁移先持锁时，等待中的
  旧 INSERT 会在释放后被新约束拒绝；down 也会等待并看见并发提交的 v2。PG16 fixture
  通过生产 `switchback_schema_migrations` 和真实
  `deploy/local/migrate-postgres.sh` 证明：runner 跳过已记录的 000021，只应用并记录
  000022，重复执行为 no-op；`SWITCHBACK_MIGRATIONS_DIR` 仅提供测试/部署目录覆盖，
  默认仍为 `/opt/switchback/migrations`，空目录 fail closed。

这些变更只完成离线实现与验证，不调用真实 provider、不构建 Task 29 镜像，也不把
历史 canonical summary 提升为当前 HEAD 的验收证据。
