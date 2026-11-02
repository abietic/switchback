<!-- Structure adapted from the decision-record template in SuperiorByteWorks-LLC/agent-project; Apache-2.0. -->

# 私仓 CI 与最小 ACP 合入设计

_2026-10-05；用户已批准设计与 CI 全绿后的精确 HEAD 受控合入。服务器强制保护仍不可用。_

---

## 🎯 目标与范围

在现有私有 `abietic/switchback` 增加无真实供应商凭据的确定性 CI，然后只合入已审查的 Codex-only ACP 重构。不重命名仓库、不改变可见性、不创建公仓、不重写历史，不自动合入观测 PR #2，也不接入 Factory/profile/YHC。

本设计仅复用既有公仓 CI 方案的测试架构；公仓 snapshot、publication audit、secret push protection 和仓库迁移不进入本次私仓切片。私有源码不宣称已满足公开发布审计。

## 🔍 当前证据

2026-10-05 的只读 Git/GitHub 核查：

- 远端默认分支 `master` 为 `0cc8f877eb0ddea6eee4ac36fd0cca1eb2d0d7ed`；仓库 private，Actions enabled，没有 CI workflow。
- 原 ACP 分支 HEAD `43510da800cb5baeccd5c14fe3455a7018bc70e0` 相对默认分支有 276 个提交、364 文件净增量，不能直接合入。
- 只回移本会话七个 scoped commits；`codexacp` 在默认分支与原开发基线 `72a96845` 之间逐文件无差异。`application.AgentAdapter` 不变；ports.go 的六行差异是另一个 ObjectStore 错误值，与本次 ACP 无关，不复制它。
- 原 ACP 的三个任务及源码全分支审查 Approved；源码验证绑定 `0d09bf22b000ff3b38b01bb720dc52440f676aad`，末尾提交只闭合两份主题文档。回移后仍须在新 HEAD 跑验证，不能直接沿用旧 HEAD 的完成声明。
- 既有 CI 参考切片为 `codex/public-release-ci-design` / `d7611b76e4f589048afc62384e9139afcfda52e4`；保留该工作树及分支不变，只复用已存在的实现模式。
- rulesets 与 branch-protection 查询均返回 HTTP 403：需要 GitHub Pro 或公开仓库。私仓强制分支保护当前不可用；这与官方支持边界一致。[^1]

## ⚖️ 选择与取舍

| 路线 | 收益 | 限制 |
| --- | --- | --- |
| 私仓 CI 全绿后精确 SHA 受控合入 | 满足当前保密范围，增加远端执行证据 | 当前无法服务器强制保护，其他写权限人仍可绕过 |
| 私仓 CI 完成但保持 PR 未合入 | 不依赖人为维持强制门禁 | 需用户自行升级 Pro 后启用保护 |
| 先完成新公仓迁移 | 后续可配置公仓强制保护 | 涉及独立历史/隐私审计，非本次用户选定路线 |

推荐第一条，但必须显式接受“CI 执行门禁”和“服务器强制保护”不是一回事。没有该确认时，只准备 CI/ACP PR，不合入；不会自动购买订阅、改公开或绕过服务器规则。

## ⚙️ CI 架构

一个 `.github/workflows/ci.yml`，触发所有 PR 与 `master` push；不设路径过滤、不使用 `pull_request_target`。Token 仅 `contents: read`，第三方 actions 固定完整 SHA，同 PR superseded runs 可取消。Go `1.26.4`、Node `24.17.0`、`ubuntu-24.04` 沿用已有 CI 方案，实际 action/image pin 须在实施前核实。[^2]

| Job | 执行门禁 |
| --- | --- |
| Static & Contracts | fmt、migration、generated protocol、Worker image lock、vet、secret、strict OpenSpec；integration validator characterization |
| Go Unit & Race | `make unit`、`make race` |
| PostgreSQL Integration | digest-pinned PostgreSQL，先健康/连通性检查，再 `go test -json -count=1 -tags=integration ./...` |
| Production Web | `web/` 的 `npm ci`、测试、类型/生产构建 |
| Browser | production Web 的 Playwright Chromium 测试，失败证据保留 7 天 |
| Required | `if: always()`，仅五个 dependency result 全部恰为 `success` 才成功 |

2026-10-05，用户批准仅修正 CI PostgreSQL 的 runner 平台 pin。PR3/run37279664554 中旧镜像成功 pull，但其 `linux/arm64/v8` 与 AMD64 runner 不匹配，entrypoint 报 `exec format error`，数据库测试未运行。CI 现固定 PostgreSQL `17.10-1.pgdg13+1` 的 AMD64 manifest `sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b`；官方 [17.10-trixie tag 元数据](https://hub.docker.com/v2/repositories/library/postgres/tags/17.10-trixie) 已核实其平台与数据库版本。旧、新镜像构建日期不同，因此这里只确认数据库版本相同和新 pin 的 AMD64 平台，不推断 OS 或镜像内容相同。`deploy/local/compose.yaml` 保留原 ARM64 pin；元数据核验也不代表远端 CI 或数据库测试已通过。

集成证据校验器拒绝 malformed JSON、缺少/不一致 terminal events、fail 和 test-level skip；package no-test 输出不误报。复用既有脚本入口与 hermetic shell 测试模式，但既有实现只检查格式与 skip，本轮按上述已批准契约补齐终态检查，参考分支不变。Required 检查 exact 五个 dependency keys 且所有结果为 success，拒绝 failed/cancelled/skipped/missing，不能把空集合的 all() 或 GitHub 对部分 skipped checks 的默认处理当成绿测。[^3]

不使用真实 Codex/YHC、模型凭据或本地生产容器；生产 Web 不是 web-demo。CI 不缓存测试结果，不通过 `continue-on-error` 或 dummy success 降级质量门禁。

## 🔐 合入协议与失败处理

CI 与 ACP 分开 PR。先将 CI 分支从当前 `origin/master` 创建，完成独立审查、静态/契约验证及远端五 Job + Required 全绿。再从已合入 CI 的 master 创建 ACP 最小分支，仅回移原本七个 scoped commits，重新验证其源码与原已审查切片等价、主线兼容和文件范围。

每次合入前重新从 API 读取当前 PR head、base master、mergeability 和 CI run/checks：只接受对应 exact HEAD 的完整成功运行，不沿用旧 SHA。若 master 前移，先重新评审组合差异，并在更新后的候选上重新跑 CI。用 `gh pr merge --squash --match-head-commit <verified-sha>` 受控合入，不直接 push master、不用 `--admin`、不关闭/删除他人 PR 或分支。合入后读取 merged 状态/merge SHA 和默认分支，并验证 master push CI；PR 绿测不等于合入后 CI 完成。[^3]

若 Actions 额度、权限、依赖解析或测试失败，保留 PR，报告具体证据，不合入。基础代码已有测试失效时先诊断并仅修复有证据的 fixture/CI 兼容问题，不能趁机修产品行为；超出本次边界的缺陷单独交接。

分支保护 403 不会被伪报成已配置。用户选择保持未合入时，等用户升级后再配置、读回 strict Required/PR-only/admin-enforced/禁止 force-delete 等语义；用户选择受控合入时，只记录手工精确 SHA 门禁，服务器强制保护仍为未完成项。

## 🤝 并行与验收

只使用当前附属 ACP managed worktree；保留原 ACP ref、主工作树 dirty 改动及其他工作树/ref。任何本地 Go/DB 资源扩展先与观测 owner 同步；远端 CI 的 PostgreSQL 不借用本地验收容器、卷、端口或冻结证据。

本轮完成需要：私仓可见性不变；CI 与 ACP 最小 PR 各自独立审查；exact HEAD 五 Job + Required 全部成功；获批准合入规则满足并由 API 验证 merge；合入后 master CI 成功。服务器保护可用性单独记录，不混为 CI 通过。后续“下一步”只做观测组合差异评审与协调回归，不改变其 `capture_cancelled` 尚未解释的结论。

## 🔗 参考

[^1]: GitHub. About protected branches. https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches
[^2]: GitHub. Workflow syntax for GitHub Actions. https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
[^3]: GitHub. Troubleshooting required status checks. https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks
