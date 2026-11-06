# 显式 Worker 执行配置实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development sequentially with task and whole-slice review. No Git mutation. Prior operator phase is sealed; accepted Source implementation is released.

**Goal:** 保持旧默认交付关闭，显式选择时一次绑定真实Worker image、固定credential路径、Backend control网络和原业务准入，交付完整新候选及离线准备器。

**Architecture:** 使用producer私有closed DTO选择既有不可变intent中的Backend env/network配置；不新增公开权限聚合或准入协议。先完成Source与真实调用seam回归，再原builder完整产物与已审执行器重绑。真实key、VM、Engine、PG、provider均不触碰。

**Tech Stack:** Go1.26.4、现有Moby SDK、既有private qualification文件安全端口、Python3.9、原builder和已经评审的部署执行器。

## Global constraints

- Worktree固定为 `/path/to/switchback`；保护全部既有dirty Source、旧seal和产物，不stage/commit/reset/checkout/clean。
- 用户call_648b90b2fd0944ac9ba30e8325224fdd批准Source与完整新候选离线实施。当前不得读取真实key或部署path、调用Lima/guest/VM/Engine/PG/provider/服务、安装环境、构建Docker镜像或生成秘密。
- 新私有文件固定 `/var/lib/switchback/qualification-candidate/execution-profile.json`：`schema=switchback.qualification.execution_profile.v1`，`mode=disabled|worker_delivery`。缺失保留旧disabled；存在的坏字段/type/duplicate/unsafe identity与permissions一律拒绝。
- default/旧env/旧角色资格语义保持；不改raw v1/v2、PG预算、Trust语义、Start/CID/quota、2CPU/4GiB/512PIDs/10GiB、80%记账或shared-volume保证。
- Worker mode的image来自已验证实际Worker image ID，固定credential路径为 `/var/lib/switchback/provider-credentials/kimi.key` 和 `deepseek.key`，不读key值、不把provider权限来源于profile。
- Worker mode Backend在Create时连接既有Core和worker-control两个网络，固定Backend alias，无新network/hostport/hostnetwork。验证实际网络/端点匹配；PG/Web保持Core网络。
- profile只在intent生成时读一次，后续Create/prepare/install用同一不可变intent的完整env和profile结果；不事后重配Backend以制造匹配，不新增自动Task/模型请求。
- 选中provider凭据只在未来新的exact真人授权下准备，Source路径是固定服务端目标，Worker只得到受管SecretLease；不使用假key，不从历史泄漏证据或ambient env提取真实key。
- Source实施前必须封存前一operator phase；发布实际环境前仍需用户审批具体候选、credential源和完整副作用。当前只完成离线交付。

## Task 1: Source配置与网络接线

**Files:** 新增capacitysnapshot私有profile解析/mapper/回归；最小修改qualification_material_linux.go的intent/env、Create networking和实际网络校验；如需私有Linux reader可单独文件。测试通过真正localproduct.LoadConfig组合验证完整env，避免只测字典等于实现。

```go
type qualificationExecutionProfile struct {
    Schema string `json:"schema"`
    Mode string `json:"mode"`
}
```

- [x] Root固定before与允许文件清单，读取当前Source/active spec，先写缺失/disabled和worker_delivery的真实seam回归。
- [x] closed DTO（包括duplicate key）和0400 root/singlelink/nofollow/stability读取；用既有私有IO端口，不新建通用框架。
- [x] 旧`qualificationBackendEnvironment`结果不变；显式mode添加enable、actualWorkerImage、两个fixed credential paths，并将完整env冻结在原intent中。
- [x] Create时Backend双固定网络/alias；实际容器校验拒绝丢失、额外、错误network/alias/hostnetwork，不由可变profile二次决定。
- [x] 在既有installer候选名称闭集中接纳同一固定profile叶，真实目录回归保持其他unknown叶拒绝；最终binding测试仅追加本phase bundle发现路径，验证逻辑保留。该装配接线补充见本phase的ROOT-installer-profile-finding.json与ROOT-final-binding-path-finding.json。
- [x] 验证旧行为、malformed/unsafe输入、真实LoadConfig、sourceimage绑定、无有效capacity/credential时零Start/Prompt、空Task/Turn不主动调用模型。纯helper/fake/privatefixture，环境测试不启动PG或容器。
- [x] targeted Go tests、Linux arm64/amd64 compile与相关现有资格回归，保存原始stream/exit和限制，独立spec+quality review。

## Task 2: 完整新候选与准备器

**Files:** 本phase内完整builder receipts/newkit与bundle、旧执行器派生副本、profile/credential public输入模板、离线组合回归、operation-window和交付报告。

- [x] Task1冻结后用原builder一次完整创建下一fresh kit并构造canonical bundle；完整headers/Source/Templates/reverse bindings/authority-null-false与实际最终binding全部核对。
- [x] 从前一已审执行器精确派生，更新新候选/Source/guard合同和新的保留archive名，保留所有kernel/权限/原120/240/300秒门禁及parent修复。
- [x] 显式模式准备profile0400 once；未来selected credential源/metadata和provider选择仍是未授权空模板，缺失时actual在任何副作用前拒绝。准备器可复制未来exact授权源至Backend私有product子目录，普通日志无秘密，当前tests只用synthetic私有fixture，不读取实key。
- [x] 测试先canonical Engine再profile/secret/BackendStart；未授权、旧grant、输入漂移、缺失来源、unsafe credential、跨阶段parent、NoProvider/NoTask不主动请求、全失败门禁。Source更新不得让旧profile/legacy证据冒充新行为。
- [x] 独立delivery和whole-slice review；完整Source/旧证据/HEAD/index核验后封存。未来真实操作清单绑定具体新候选，而不是自动使用本轮离线批准启动环境。

## Completion criteria

全部Source与完整产物和准备器离线通过、复审通过、无实际环境或provider动作。报告明确当前实跑仍未完成，下一步需一份具体环境/credential授权；真实Task、正式报告、页面接受及同任务观测用匹配现场证据验收，随后再定向精简。
