# CodePlatform Worker Git Flexibility And Token Renewal Trade-offs

> 调研日期：2026-07-12；GitHub 官方认证事实于 2026-07-15 复核。
>
> 状态：研究完成。最终决策为受管 B2，见 [ADR 0030](../adr/0030-managed-native-codeplatform-cli-with-fallbacks.md)；本文保留方案 A 与混合 C 的比较和回退依据。

## 结论

如果“方案 B”只是把 PAT/OAuth token 作为环境变量或配置文件注入一个最长 14 天的 WorkerSession，它不适合作为 Switchback P0 基线：短期 token 必然过期，长期 token 又扩大泄漏与撤权风险。

如果 B 改为“标准 Git CLI + 动态 credential helper”，续期技术上可行，Agent 对 `fetch`、`rebase`、submodule、多 remote 等操作也最灵活。但 helper 必须把 token 返回给容器内 Git 进程，同一 OS 权限下的 Agent 也可以直接调用 credential protocol 获取 token。因此它降低了静态密钥风险，却没有建立 Agent 与 token 之间的保密边界。

调研阶段曾推荐 **方案 C（A/B 混合）**：

- 所有本地 Git 操作由 Agent 直接执行。
- 远端只读 Git 操作使用动态 helper，按 repository/path 获取短期 read-only credential。
- 远端写入、branch publication、PR、merge 和 CI 使用控制面 CodePlatformAdapter。
- P1 可为高信任 Tenant 增加“direct remote write” profile，但它是显式放宽，不是 P0 默认。

这个混合方案保留 Agent 的主要开发灵活性，同时避免把 repo-write token 交给任意 shell/tool process。用户随后明确接受受管 B2 的受托执行模型：RepositoryAccessGrant 生效后，短期 access token 可被 Agent 使用，换取最大原生 CLI 灵活性；A 与 C 保留为问题出现后的 fallback。

## 最终决策更新

P0 采用受管 B2，而不是调研阶段推荐的 C：

- Worker 镜像预装 Git、GitHub CLI 和 Skills。
- CredentialBroker 管理根凭证/refresh secret，只向 Worker 发短期 RepositoryCredentialLease。
- Agent 可直接运行 Token 允许的非管理类 `git`/`gh` 操作，不逐命令审批。
- administration、secrets、variables、workflow/Actions write、ruleset 管理和 repository delete 不进入 P0 profile。
- Provider ruleset 保护 default/protected branch；Switchback 负责续期、撤销、网络和 CodePlatformOperation Trace。
- 方案 A 与 C 的适用条件继续保留在本文与 ADR，避免未来重新经历同一轮分析。

M1 tracer bullet 进一步做了范围裁剪：它不是完整 P0 CodePlatform profile。M1 使用 SingleUserLocalProfile，只由宿主 Repository resolver/Importer 读取一个配置仓库的 read-only env/mounted credential；Worker 只接收已物化 WorkingCopy，不接收 token、credential helper 或远端 Git/`gh` 能力。GitHub App、CredentialBroker、动态 RepositoryAccessGrant 和受管 B2 在真实 Worker 链路验证后实现，见 [ADR 0058](../adr/0058-use-single-user-local-profile-for-m1.md)。

## 一手资料事实

### Git credential helper

Git 官方支持外部 credential helper。Git 会把 URL credential context 交给 helper，helper 可动态生成用户名和密码；`credential.useHttpPath=true` 可让 helper 区分同一 host 下的不同 repository path。Git credential protocol 还支持 `password_expiry_utc`；过期密码在 credential fill 时会被忽略。`oauth_refresh_token` 只是传给 helper 的敏感字段，Git 本身不负责刷新。

来源：

- [Git credentials documentation](https://git-scm.com/docs/gitcredentials.html)
- [Git credential protocol](https://git-scm.com/docs/git-credential)

这意味着 Switchback 可以实现 `git-credential-switchback`，在每次 credential lookup 时向后端换取短期 token，而不把固定 token 写入 remote URL 或 `.git-credentials`。

同时，这也意味着 token 会作为 credential helper 输出交给容器内调用者。只要 Agent 能运行任意命令，它原则上也能执行等价的 `git credential fill`。因此 helper 是续期机制，不是 Agent/token 隔离机制。

### GitHub

GitHub App installation token 可用于 HTTP Git，用户名使用 `x-access-token`。Installation token 一小时过期；生成时可以限制 repository ids 和 App 已获授权范围内的 permissions。官方生成接口没有 branch 级 token scope。

GitHub App user access token 适合需要把 provider 动作直接归因给 GitHub 用户的场景，其有效权限是“用户权限与 App 权限”的交集。当前官方文档把 user-token expiration 描述为可选且可能变化的特性，因此 Switchback 不把固定 8 小时/6 个月写成平台不变量；只有具体 App 配置和 contract test 验证后才能形成 adapter 参数。GitHub 建议代表具体用户执行动作时使用 user access token，而 installation token 更接近 App installation 服务身份。

截至 2026-07-15，GitHub 仍明确 installation token 一小时过期，并允许生成时进一步限制 repository ids 和 permissions。2026 年新签发 installation token 正在分阶段切换为 stateless 格式，CredentialBroker、redaction 和测试不得依赖固定长度或旧 `ghs_` 字符串形态，只能把 token 当作不透明 secret。

来源：

- [Authenticating as a GitHub App installation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation)
- [Generating an installation access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)
- [Generating a GitHub App user access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
- [Refreshing GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens)
- [GitHub App best practices](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/best-practices-for-creating-a-github-app)

设计影响：GitHub token 可以收窄到单 repository 和 read/write permission，但不能仅靠 token 约束“只允许 push `switchback/*` branch”。Branch restriction 还需要 repository ruleset、受控 adapter/proxy，或接受 direct-write token 在有效期内拥有更宽 authority。

### GitLab

GitLab OAuth access token 可使用 `read_repository` 或 `write_repository` scope 访问 Git over HTTPS。官方示例 access token 有效期为 7200 秒；refresh 会使旧 access token 和旧 refresh token 失效并返回一组新 token。GitLab 官方也明确说明 OAuth credential helper 可以自动处理刷新。

来源：

- [GitLab OAuth 2.0 provider API](https://docs.gitlab.com/api/oauth2/)
- [GitLab repository clone and token authentication](https://docs.gitlab.com/topics/git/clone/)

设计影响：GitLab user OAuth 的 scope 是能力级而不是天然的单-project临时授权。P0 若使用 direct helper，需要额外验证 GitLab App/OAuth、project access token 或服务身份如何实现单项目最小权限；不能直接套用 GitHub installation token 模型。

## 方案对比

这里把原方案 A 定义为“远端 Git/Provider 动作由控制面 Adapter 执行”，方案 B 定义为“Worker 容器持有可用 credential，并直接运行标准 Git CLI”。

| 维度 | 方案 A：控制面 Adapter | 方案 B1：静态 token 注入 | 方案 B2：动态 credential helper |
| --- | --- | --- | --- |
| 本地 Git 操作 | 完全灵活；不受 Adapter 影响 | 完全灵活 | 完全灵活 |
| `fetch/rebase` | 需要 Adapter/RepositoryProvider 暴露或预取 | 完全灵活 | 完全灵活 |
| submodule/多 remote | 每种路径需设计支持 | 原生 Git 最灵活 | 原生 Git 最灵活 |
| Git LFS | 需要专门验证/代理 | 可原生使用，但 token 过期可能中断 | 可原生使用，仍需 contract test 刷新行为 |
| 任意 `git push` | 不允许；只能调用已建模 publish | 允许 | 允许 |
| PR/merge/CI | Adapter 很适合，结构化且幂等 | 仍需 `gh/glab` 或 Provider API | 仍需 `gh/glab` 或 Provider API |
| Token 续期 | 最简单；每个远端操作前生成/刷新 | 最差；env 无法更新已运行进程 | 中等；helper 按需生成、缓存和刷新 |
| Token 暴露给 Agent | 最低；token 可留在控制面 | 最高；env/file/process 易读取 | 中等到高；Agent 可主动调用 helper 获取 token |
| 撤权生效 | 新操作立即拒绝，可撤销 token | 依赖 token 过期、容器停止和网络 fencing | helper 不再发新 token，但已签发 token 仍活到过期/撤销 |
| Repository scope | Adapter 可逐请求绑定 repo | 取决于 token 本身 | helper 可按 host/path 请求单 repo token |
| Branch scope | Adapter 可严格校验 branch/expected head | token 通常无法限制到单 branch | helper context 没有目标 ref，不能单独强制 branch |
| 幂等与恢复 | 可显式使用 operation id/expected head | Agent 自己判断 remote state | Agent/Wrapper 仍需 remote-head reconciliation |
| Provider 可移植性 | GitHub/GitLab Adapter 工作量较大 | Git 数据面较通用 | Git 数据面较通用；token broker 仍 provider-specific |
| Agent 使用体验 | 远端动作需要 Switchback tool，略不自然 | 与本地 Coding Agent 最接近 | 与本地 Coding Agent 接近 |

## Token 续期具体分析

### 方案 A

控制面在 `FetchRepository` 或 `PublishBranch` 开始前生成 token：

```text
authorize operation
-> acquire/refresh provider credential
-> run bounded remote operation
-> reconcile remote result
-> revoke or let short token expire
```

GitHub installation token 一小时、GitLab OAuth 两小时不会影响 14 天 WorkerSession，因为 token 生命周期与单次远端操作绑定，而不是与 WorkerSession 绑定。若一个远端操作跨越 token expiry，Adapter 可在安全检查 remote state 后使用新 token 重试。

### 方案 B1：静态 env/file

```text
start 14-day-capable container
-> inject token once
-> token expires
-> existing process environment cannot be updated
-> restart runtime or rewrite credential source
```

若改用长期 PAT 避免续期，则撤权窗口、泄漏半径和组织安全策略都变差。`.git-credentials` 还会明文保存 token；Git 官方文档明确提示该 helper 只依赖文件权限保护，不适合敏感场景。

### 方案 B2：动态 helper

```text
git fetch
-> Git invokes git-credential-switchback get(host, path)
-> helper calls Switchback credential broker
-> broker checks actor/session/repo/policy
-> provider adapter generates or refreshes short token
-> helper returns username/password/expiry
-> Git performs operation
```

配置必须启用 `credential.useHttpPath=true`，否则 Git 默认可能把同一 host 的 credential 复用于其他 repository path。

helper 可以在下一次 credential lookup 时避免返回过期 token，但不能保证一个已经开始的复杂 Git/LFS 操作在 token 失效时无缝续期。P0 仍需在固定镜像中测试 clone/fetch/submodule/LFS 的过期点，并为认证失败增加一次“清除 credential -> 换新 token -> 核查 remote state -> 重试”的有界恢复。这里是基于 Git credential 交互模型作出的工程推断。

## 为什么 B2 仍需要 Provider 控制面

标准 Git CLI 只抽象 Git object/ref 数据面，不抽象：

- PR 创建、更新、合并和 review。
- CI workflow/pipeline 触发、状态和日志。
- GitHub App installation、GitLab OAuth/project token 获取与刷新。
- Repository installation、组织 SSO、ruleset/protected branch 和 webhook。
- Provider error normalization、rate limit 和幂等 API。

受管 B2 不要求把这些能力都变成 Agent 必须调用的 Switchback operation adapter。GitHub CLI 可以直接覆盖 PR/CI read 等原生操作，但控制面仍必须实现 CredentialBroker、Grant/permission 编译、installation/SSO/webhook、provider result lookup、rate-limit/error normalization 和 revocation。换言之，B2 消除的是“每条命令代理”，不是 provider 集成本身。

## 调研阶段推荐的混合 C

以下边界是受管 B2 被接受之前的推荐，现在作为 fallback C 保留。

### Worker 直接执行

- `git status/diff/log/show/add/commit/branch/switch/rebase/merge/cherry-pick/bisect/worktree`。
- 使用短期、单 repository、read-only credential 的 `clone/fetch/pull/submodule update`。
- 对多个已授权 RepositoryCheckout 使用独立 path-scoped credential。

### 控制面执行

- 获取和刷新 provider token/installation。
- `PublishBranch`，包括 branch namespace、expected remote head、幂等和冲突检查。
- PR/create/update/merge/review。
- CI trigger/status/log projection。
- protected branch、force push、branch delete 和其他高风险写操作。

### 为什么读 token 可以交给 helper

Agent 已经被授权读取并挂载该 repository 内容。若 read token 严格限制到同一 repository、短 TTL、受限网络和无额外组织 API 权限，那么 Agent 取得 token 通常不会增加超出已授权源码读取的能力。仍需阻止 token 写入 Trace、日志、remote URL 和 Artifact。

### 混合 C 为什么不把写 token 交给 helper

Git credential context 包含 protocol、host、path 和 username，但不包含目标 branch/ref。一个 repo-write token 一旦返回给 Agent，就可能被用于任意 provider 允许的 ref 或 API，而不仅是已批准的 `switchback/*` branch。控制面 `PublishBranch` 更容易对 branch、expected head、ChangeSet、actor 和幂等键形成强约束。

## 当前 B2 P0 Spike 清单

- 固定 Worker 镜像实现 `git-credential-switchback`，只支持 HTTPS，不使用 plaintext credential store。
- GitHub App installation token：单 repo、read/write/PR/CI-read 分离 profile、1h expiry、cache-before-expiry。
- GitHub user token：仅作为需要 provider user attribution 的备选 profile；按 App 实际 expiration/refresh 配置测试轮换、用户撤权和 401 收敛，不硬编码 8h。
- Installation token 视为不透明 secret；不得依赖固定长度、旧 token prefix 或旧格式做校验/解析/redaction。
- GitLab OAuth：2h/refresh rotation、`read_repository`、helper 自动刷新和 refresh race。
- `credential.useHttpPath=true` 下两个同 host repository 不串 token。
- token 不进入 remote URL、process args、Trace、Langfuse、shell history 和 Artifact。
- clone/fetch/submodule/LFS 在 token 过期、401、网络中断后的有界重试。
- Membership/ExecutionAuthorizationLease 撤销后 helper 不再签发 credential，已发 token 被撤销或等待短 TTL，同时 runtime 网络被 fencing。
- GitHub Ruleset/Branch Protection 拒绝 protected/default branch、force push 和 delete。
- 原生 push/PR create 的响应丢失、401、网络重置和重复 command 通过 remote/provider lookup 收敛。
- A/C fallback 使用同一 RepositoryAccessGrant、CodePlatformOperation 和 provider-result contract。

## 已确认决策与回退条件

用户已接受受管 B2，即 Worker 在 RepositoryAccessGrant 范围内直接获得短期 remote write authority，并明确接受 Token 对 Agent 可用。当前不是“仍需确认”的问题。

- 若安全政策禁止任何 repository Token 进入 Agent sandbox，回退方案 A，由控制面执行全部远端操作。
- 若 read workflow 必须保持原生灵活，但 Provider 写 scope/ruleset 无法安全约束，回退混合 C：Agent 直连只读，控制面执行写入。
- 只要短期 Token、最小 permission、Provider ruleset、撤权、known-secret redaction 和结果核查能满足约束，就保留受管 B2。
