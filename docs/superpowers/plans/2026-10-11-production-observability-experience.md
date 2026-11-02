# 生产观测入口与 exact 身份实施计划

> Task 1/2/3 已完成本地实现、覆盖回归与独立评审；Task 4 实机闭环尚未完成。用户批准的最小闭环，不新增完整 Admin/Workspace/Memory/Skills、不提交/推送/合入。

**Goal:** 生产导航能进入真实观测能力；用户从已交付 report/历史 Turn 选择目标后，打开同一 Run 的已核验 Langfuse trace、所选范围日志及聚合 Grafana。缺失/故障如实显示，不影响 Worker 执行或报告。

**Architecture:** 使用已授权 Home/Worker projections 与现有 diagnostics HTTP。ArtifactTrace-owned 只读 bridge 从 exact frame/receipt/publication/v3 passed job 获取身份；hash 仅由现行 Langfuse compiler 共享实现，不在 Frontend/PG复制。HTTP 无远端 ingest/readback；passed 是曾成功核验身份，不是当前服务健康。

**Proof boundary:** 现有 v3 job 没有持久化远端 endpoint/project/credential identity。`verified` 只表示 exact source/version/digest 的历史身份核验，不能表示当前 browser project 已核验；页面必须明确“身份曾核验；链接使用当前项目配置，未实时核对”。不为本切片迁移 proof schema。最终新候选保持同一 ingest/browser project 配置并对新任务实际 readback，才证明本次目标可访问；历史项目迁移后的链接不算已验收。

**Tech Stack:** Go/PG、React/TypeScript、Vitest、Vite；无新依赖。沿用 `production-web-core-experience` + `local-observability-stack` active changes；其未完门禁保持未完。

## Safety and proof

- owned WT `.worktrees/observability-master-integration-1009`，保持其现有 dirty与旧证据，主树不动。
- Backend固定M1 Tenant/actor/Workspace，只读 diagnostics 不接纳客户端scope/URL/凭证。不把projection可见性扩展为操作权限。
- Langfuse config使用公开browser origin/project ID，不暴露 Secret/Public API key、prompt、raw frame/receipt、异常正文。内部 ingest URL 不作browser origin。
- pinned image3.217.0/revision `1f28a2989cad75eeb102adf2544e7f76eb7d9fed` 已只读核验 `.next/routes-manifest.json` dynamic route `/project/[projectId]/traces/[traceId]`；官方 [Trace URLs](https://langfuse.com/docs/observability/features/url) 一致。证据 `.scratch/langfuse-browser-route-proof-1011.json`。不凭记忆拼URL、不使trace public。
- `langfuse-<turn>` 只是旧correlation hint，不是v3实际trace ID；未核验 source/digest/version禁止链接。session-only不猜latest Run。历史excluded需要既有explicit selection，不因点击隐式rebuild。
- 无远端网络在page request或PG事务；UI不得fake health/metrics。Langfuse bridge不可用仍保留合法Grafana/Logs，不阻断terminal或nextTurn。
- Integration使用独立PG 127.0.0.1:55439，只允许父会话分配的单进程-p1窗口。此计划不消耗那一个真实任务授权。

---

### Task 1: 生产导航暴露已有观测能力

**Files:** `web/src/components/layout/ProductNavigation.tsx`, `web/src/app/routes.tsx`, new `web/src/features/observability/ObservabilityPage.tsx`；对应ProductShell/routes/页面tests；production-web active design/spec/tasks最小同步。

- [x] 先RED：expanded/rail/drawer都有真实“可观测性”link到/observability，Admin独立仍为规划项；新route采用overview shell。
- [x] 小型页面复用productionViews.home、ViewBoundary和授权recentWorkers，让用户explicit进入exactWorker。没有Worker明确空态/创建入口；error可重试，不造统计/管理后端。
- [x] 复用真实WorkerDiagnostics：Grafana聚合overview、scope Logs、Langfuse proof状态。不能硬编码43000/43001或偷偷用web-demo。
- [x] targeted tests及productionbuild；记录RED/GREEN。真实导航UI会在最终候选重建后验收，mock/component不算实机。

### Task 2: ArtifactTrace-owned verified navigation bridge

**Files:** new `internal/artifacttrace/application/projection_navigation.go`, `adapter/postgres/projection_navigation.go`；small `adapter/langfuse/projection_navigation.go` wrapping既有compileFragmentProjection；`fragment_coverage.go`最小shared eligibility/basis selector；对应PG/compiler tests。r1 评审修复经架构占用确认后最小扩至既有 `fragment_history.go`（普通 SQL 错误传播）与 `fragment_evidence.go`（共享纯 proof binder），保留两个 SQL reader 的显式锁边界。

**Input:** server-owned Tenant/user/Workspace + exact Session/Run/optionalTurn +固定现行version。

**Output:** safe finite status（verified/pending/blocked/identity_unavailable）和仅verified可有trace/selected-observation IDs、version/source digest证明摘要；无原始内容。

- [x] 先RED：exact Run/Turn有合法receipt+publication+v3passed才能返回真正ingestion trace ID。rootpassed≠selectedTurnpassed；model coverage不全不能伪称complete，也不抹掉已证root/Turn。
- [x] 同一个只读PG snapshot重验scope、current source/version/digest、activation/exclusion和显式历史selection。复用coverage/ledger查询规则与typed receipt/frame证明，不凭`Verified>0`或任意candidate推断。
- [x] 新pure CompileProjectionNavigation仅接受run/turn basis、最多2帧，通过既有compileFragmentProjection的receipt/parent/wirehash验证后，从exact且唯一的trace-create/span-create事件提取ID，保证既有v3 IDs/batch bytes不变；model target、多目标、重复、invalid/source mismatch拒绝，不复制hashseed到其他层。不为了导航大改publication的FOR SHARE校验；其locking reader不能放进READ ONLY事务。
- [x] 定义no-content/pending/blocked/proof-invalid安全状态；malformed frame/receipt、错Tenant/user/workspace/Run/Turn、publicationdigest mismatch、wrongversion job、selectedTurn未passed、excluded history无selection全部不得生成伪链。
- [x] Integrationfake只用ObjectStore/remoteprojector界面fixture，PG rows来自真实publisher/jobfinish或现有validatedfixtures；尽量publicapplicationseam，禁止只验证私有SQLhelper或fakefetch200。
- [x] fixture recipe：postgres_test同包newFragmentFixture(t,false)→publishCore→FragmentPublisher.Tick两次→JobStore.Discover/Claim/Finish synthetic passed。明确没有真实readback；history用newFragmentFixtureMode(t,false,true)+historicalRequest+historyStore.Request。占位{} frame或seedObservationRun仅可负例。禁止修改已经immutable封存的candidate/job做负例。
- [x] 不需要新增authority table；如现有持久证明无法足够绑定身份，先报告具体缺口而非迁移新表。

本地证据：`.scratch/production-projection-navigation-1011-report.md`；r1 两项 Important 已修复，`.scratch/projection-navigation-review-1011-r2.md` Approved，无 Critical/Important/Minor；root 当前源关键 race exit0，`.scratch/projection-navigation-parent-race-1011-r2.log`。初始缺接口仅为编译 RED，不计证明契约行为；publication pending 与真实 PG42501 有行为 RED/GREEN。synthetic passed 不证明当前项目/远端，HTTP/Web 与实机门禁仍在 Task3/4。

### Task 3: Diagnostics与历史目标接入

**Files:** Frontier HTTP handler/diagnostics、localproduct config/app、必要eventingprojection authorizedtarget字段；Web contracts/workerDiagnostics/WorkerDiagnostics/WorkerDetailPage/Workbench target选择callback；Compose公开browser config与runbook、对应unit/component/PGtests，local-observability active spec/tasks。

- [x] 新publicconfig `SWITCHBACK_LANGFUSE_BROWSER_BASE_URL` +服务端project ID；严格沿用loopback origin validation、project canonical path segment、不使用ingestkey。Compose公开默认project取已有`LANGFUSE_INIT_PROJECT_ID`的同一配置，避免双定义漂移。
- [x] HTTP先exactdiagnostic授权，再读取artifactbridge；仅历史身份verified生成URL并明确当前project未实时核对，不能暗示endpoint/project绑定已持久证明。空config disabled、非法invalid_config、无身份/pending/blocked如实区分；bridgefailure不丢Grafana/Logs、不泄exception/secret。private,no-store保持。
- [x] HTTP公开状态使用historically_verified（ArtifactTrace内部verified可保留），避免误称current-project verified；dependency读失败的unavailable只表示身份读取失败，非远端offline。DTOdecoder校验finite状态、links kind唯一/合法loopbackURL、historically_verified↔Langfuse link一致、optionalidentity结构及target一致；非该状态无identity/link，不编造verified_at。abort/stale reply继承existingexacttarget key。
- [x] 从canonical ArtifactSummary/历史timeline的run/turn refs选择diagnostic目标；完成后activeIDs清空也不猜latest，用户所选report的真实来源继续生效。跨target旧links立即清空，刷新后重载canonical来源。
- [x] 独立review，修复后完整Webtests/build + focusedGo/compiler/HTTP/PGtests及OpenSpecstrictvalidate；不要勾G3/G4/旧fullscope或宣称即时health已通过。

本地证据：r2 独立评审 `.scratch/diagnostics-history-review-1011-r2.md` Approved，无 Critical/Important；32 文件 task source digest `1664d0ad1f7963259cc22abef94780f8cca2040ae3a09dc3fb85e7a00fd746b2` 由 root 核对。Root 完整 Web 46files/582tests（maxWorkers=2，37.38s）、TypeScript/Vite build exit0，原始日志 `.scratch/platform-loop-parent-web-build-1011.log`；当前受影响 20 个 Go packages 普通回归 exit0，`.scratch/platform-loop-parent-go-unit-1011.log`；OpenSpec 全部 strict 13/13。保留两个 Minor：既有大 chunk 告警、默认并行 reload 稳定性未调查。最终组合 PG/race 与 Task4 实机、同身份 readback 不因此关闭。

### Task 4: 最终新候选真实任务验收

- [ ] 与控制/退出修复合成新的source freeze、manifest/digest、rawbuildlogs、实际镜像IDs；新容器/端口，不覆旧experience1011或workbench-r2证据。
- [ ] 只使用已授权的1个新smallTask：read-only现有示例文件，正式Agent输出<=300字report正文，不仅写/tmp路径；manual acceptance。
- [ ] 页面Assistant确认→WorkerexactRun/Turn正常终态→PG正式ArtifactRefs/Taskresult_refs/delivered→生产report预览与下载digest→刷新一致→页面accept completed。
- [ ] 同一report/Turn打开serververifiedLangfuseexacttrace并readback核对root/turnidentity；scopeLogs返回该Task真实事件，Grafana明确aggregate。服务失败只算安全降级，不算同身份实际验收成功。
- [ ] 新任务的费用授权仅一次，若失败不得无授权重跑新任务。保留失败具体步骤与证据，不手工改Task/Artifact/DB来凑完成。

## Evidence levels

组件/HTTP单测、PG权威事务、固定镜像、页面行为、真实provider及远端投影是分开的证据。正式报告复用现成terminal→settlement→ObjectStore→ArtifactRef→Taskdelivery→authorizedcontent链，发现哪个真实步骤失败才修对应owner；不新增救回文件注册旁路。
