import type {
  ArtifactItem,
  EventLogItem,
  MemoryTruth,
  ProxyModelStat,
  SkillItem,
  TaskItem,
  WorkerSession,
  Workspace,
} from '@/types/switchback';

export const workspaces: Workspace[] = [
  {
    id: 'ws-payments',
    name: 'payments-service',
    repos: ['git@github.local:pay/payments-service.git @ 4f2c9a1'],
    description: '支付中台核心服务：收单、清结算、对账。Go + PostgreSQL + RocketMQ。',
    truthCount: 18,
    artifactCount: 42,
    activeWorkers: 2,
    updatedAt: '2026-07-27 09:41',
  },
  {
    id: 'ws-switchback',
    name: 'switchback',
    repos: ['git@github.local:switchback/switchback.git @ 3ba6b50'],
    description: 'Switchback / Switchback 平台本体仓库，DDD monorepo。',
    truthCount: 31,
    artifactCount: 57,
    activeWorkers: 1,
    updatedAt: '2026-07-27 10:12',
  },
  {
    id: 'ws-reco',
    name: 'reco-pipeline',
    repos: ['git@github.local:algo/reco-pipeline.git @ d817e2c'],
    description: '推荐算法离线/在线链路，Spark + FeatureStore。',
    truthCount: 9,
    artifactCount: 15,
    activeWorkers: 0,
    updatedAt: '2026-07-25 18:03',
  },
];

export const tasks: TaskItem[] = [
  {
    id: 'task-9f31',
    title: '支付回调重试导致的重复入账修复',
    status: 'running',
    acceptanceMode: 'manual',
    workspaceId: 'ws-payments',
    workspaceName: 'payments-service',
    workerSessionId: 'wks-a17f',
    initiator: '于海川',
    revision: 6,
    createdAt: '2026-07-27 09:58',
    updatedAt: '2026-07-27 10:43',
    deliverable: 'patch',
  },
  {
    id: 'task-2b7c',
    title: 'Frontier SSE 断线重连 cursor 语义梳理报告',
    status: 'delivered',
    acceptanceMode: 'manual',
    workspaceId: 'ws-switchback',
    workspaceName: 'switchback',
    workerSessionId: 'wks-b208',
    initiator: '于海川',
    revision: 11,
    createdAt: '2026-07-26 15:20',
    updatedAt: '2026-07-27 08:31',
    deliverable: 'report',
    summary: '报告已交付，等待验收。覆盖 replay/resync 全部分支与 4 个边界 case。',
  },
  {
    id: 'task-77e0',
    title: '对账文件 T+1 延迟告警接入 EventCenter',
    status: 'waiting_user',
    acceptanceMode: 'manual',
    workspaceId: 'ws-payments',
    workspaceName: 'payments-service',
    workerSessionId: 'wks-c3d9',
    initiator: '陈晓',
    revision: 4,
    createdAt: '2026-07-26 11:02',
    updatedAt: '2026-07-27 10:05',
    deliverable: 'patch',
    summary: 'Worker 请求审批：向 staging 的 RocketMQ 写入测试消息以验证告警链路。',
  },
  {
    id: 'task-51aa',
    title: '推荐特征口径月度对比报告',
    status: 'completed',
    acceptanceMode: 'auto',
    workspaceId: 'ws-reco',
    workspaceName: 'reco-pipeline',
    workerSessionId: 'wks-d41e',
    initiator: '李暮',
    revision: 9,
    createdAt: '2026-07-24 14:47',
    updatedAt: '2026-07-24 16:12',
    deliverable: 'report',
    summary: '自动验收通过：report-only 且验收证据确定。',
  },
  {
    id: 'task-83f2',
    title: 'WorkerSession absolute expiry 边界单测补齐',
    status: 'queued',
    acceptanceMode: 'manual',
    workspaceId: 'ws-switchback',
    workspaceName: 'switchback',
    initiator: '于海川',
    revision: 1,
    createdAt: '2026-07-27 10:38',
    updatedAt: '2026-07-27 10:38',
    deliverable: 'patch',
    summary: '等待 Session 容量分配（session_capacity）。',
  },
  {
    id: 'task-6c19',
    title: '清结算批次内存溢出排查',
    status: 'failed',
    acceptanceMode: 'manual',
    workspaceId: 'ws-payments',
    workspaceName: 'payments-service',
    workerSessionId: 'wks-e5f0',
    initiator: '陈晓',
    revision: 7,
    createdAt: '2026-07-25 09:14',
    updatedAt: '2026-07-25 10:02',
    deliverable: 'report',
    summary: 'Runtime OOM：resource_exhausted，已保留 checkpoint 与 Trace。',
  },
  {
    id: 'task-a3d4',
    title: 'Skilling 权限声明文档结构化',
    status: 'cancelled',
    acceptanceMode: 'manual',
    workspaceId: 'ws-switchback',
    workspaceName: 'switchback',
    workerSessionId: 'wks-f6ab',
    initiator: '李暮',
    revision: 3,
    createdAt: '2026-07-23 17:55',
    updatedAt: '2026-07-23 18:20',
    deliverable: 'report',
    summary: '发起人在澄清阶段取消，改用人工整理。',
  },
];

const reportMarkdown = `# Frontier SSE 断线重连 cursor 语义梳理

## 结论

ViewCursor 的 replay/resync 语义在 P0 契约下是自洽的，但有 **2 个实现偏差** 与 **2 个未覆盖边界** 需要处理。

## 1. 契约回顾

- \`DisplayCheckpoint\` 完整物化用户可见 parts，推进 durable ViewCursor；
- \`TransientDisplayDelta\` 只做 checkpoint 之间的低延迟拼接，**不推进** durable cursor；
- gap 检测：transient delta 绑定 stream/part/base checkpoint/sequence，出现 gap 即停止该 part 拼接并查询 checkpoint。

## 2. 实现偏差

### 2.1 resync 后 transient buffer 未清空（P1 必修）

\`frontierViewController.ts\` 在收到 \`ProjectionControlEvent.resync\` 后重建了 snapshot，但保留了旧 stream 的 transient buffer。断线 30s+ 的场景下，旧 delta 会拼接到新 checkpoint 之后，造成**重复段落**。

复现：\`clientViewReplica.test.ts\` 增加用例 \`resync clears transient overlay\` 即可稳定复现。

### 2.2 terminal checkpoint 缺少 parts 完整性校验

契约要求 terminal checkpoint 在清空 transient buffer 后仍能完整渲染。当前实现依赖 writer 自觉，建议在 Frontier 写路径加 digest 断言。

## 3. 未覆盖边界

| 场景 | 现状 | 建议 |
| --- | --- | --- |
| SSE 重连发生在 approval pending 期间 | durable 已含 approval part，正常 | 补 E2E 用例 |
| 重连期间 run terminal | transient 丢弃、以 terminal checkpoint 为准 | 补单测 |
| 多 tab 同时订阅同一 view | cursor 各自独立，无冲突 | 无需处理 |
| checkpoint 版本超过 DisplayDurabilityPolicy 字节上限 | 未实现截断策略 | 写路径加 policy 校验 |

## 4. 验证

- 新增 3 个单测 + 1 个 E2E，全部通过；
- 手动验证：断网 60s 恢复后 timeline 无重复、无丢段。
`;

const patchDiff = `diff --git a/internal/settlement/retry/idem.go b/internal/settlement/retry/idem.go
index 9c41f2b..7d3aa08 100644
--- a/internal/settlement/retry/idem.go
+++ b/internal/settlement/retry/idem.go
@@ -12,11 +12,19 @@ type IdempotencyStore struct {
 func (s *IdempotencyStore) Claim(ctx context.Context, key string, ttl time.Duration) (bool, error) {
-\tres, err := s.db.ExecContext(ctx, "INSERT INTO retry_claim(key, expires_at) VALUES ($1, $2)", key, time.Now().Add(ttl))
-\tif err != nil {
-\t\treturn false, err
-\t}
-\treturn res.RowsAffected() == 1, nil
+\t// 支付回调重试场景下，同一 notify_id 可能被并发投递。
+\t// 使用 ON CONFLICT DO NOTHING + 检查 ctid 保证只有一个投递者获得 claim，
+\t// 避免旧实现中 RowsAffected 在部分驱动上不可靠导致的重复入账。
+\tvar claimed bool
+\terr := s.db.QueryRowContext(ctx,
+\t\t"INSERT INTO retry_claim(key, expires_at) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING RETURNING true",
+\t\tkey, time.Now().Add(ttl)).Scan(&claimed)
+\tif errors.Is(err, sql.ErrNoRows) {
+\t\treturn false, nil
+\t}
+\treturn claimed, err
 }
 ${''}
 func (s *IdempotencyStore) Release(ctx context.Context, key string) error {
diff --git a/internal/settlement/retry/idem_test.go b/internal/settlement/retry/idem_test.go
index 1a2b3c4..e8f91d2 100644
--- a/internal/settlement/retry/idem_test.go
+++ b/internal/settlement/retry/idem_test.go
@@ -0,0 +1,28 @@
+package retry
+
+import (
+\t"context"
+\t"testing"
+\t"time"
+)
+
+func TestClaim_ConcurrentDuplicateNotify(t *testing.T) {
+\tstore := newTestStore(t)
+\tctx := context.Background()
+\tok1, err1 := store.Claim(ctx, "notify-001", time.Minute)
+\tok2, err2 := store.Claim(ctx, "notify-001", time.Minute)
+\tif err1 != nil || err2 != nil {
+\t\tt.Fatalf("claim err: %v %v", err1, err2)
+\t}
+\tif !ok1 || ok2 {
+\t\tt.Fatalf("expected exactly one claim, got ok1=%v ok2=%v", ok1, ok2)
+\t}
+}
`;

export const artifacts: ArtifactItem[] = [
  {
    id: 'art-report-frontier',
    kind: 'report',
    title: 'Frontier SSE 断线重连 cursor 语义梳理报告',
    taskId: 'task-2b7c',
    workerSessionId: 'wks-b208',
    workspaceName: 'switchback',
    createdAt: '2026-07-27 08:29',
    sizeBytes: 4211,
    digest: 'sha256:9be2…c771',
    content: reportMarkdown,
  },
  {
    id: 'art-patch-idem',
    kind: 'patch',
    title: '重复入账修复 ChangeSet（2 个文件）',
    taskId: 'task-9f31',
    workerSessionId: 'wks-a17f',
    workspaceName: 'payments-service',
    createdAt: '2026-07-27 10:47',
    sizeBytes: 2867,
    digest: 'sha256:41aa…f0d2',
    content: patchDiff,
  },
  {
    id: 'art-trace-a17f',
    kind: 'trace',
    title: 'WorkerRun trace JSONL（segment 3/3）',
    workerSessionId: 'wks-a17f',
    workspaceName: 'payments-service',
    createdAt: '2026-07-27 10:46',
    sizeBytes: 184_203,
    digest: 'sha256:77c1…9a00',
    content: '{"seq":312,"dir":"out","kind":"session/update","update":"agent_message_chunk"}',
  },
];

const a17fParts: WorkerSession['parts'] = [
  {
    id: 'p1', ordinal: 1, type: 'text', lane: 'durable', timestamp: '10:41:02',
    text: '我先定位重复入账的写入路径，重点看回调重试的幂等处理。',
  },
  {
    id: 'p2', ordinal: 2, type: 'reasoning_summary', lane: 'durable', timestamp: '10:41:05',
    text: '计划：① 搜索 notify/claim 相关代码 → ② 读 IdempotencyStore 实现 → ③ 复现并发 claim → ④ 给出最小修复与回归测试。',
  },
  {
    id: 'p3', ordinal: 3, type: 'tool', lane: 'durable', timestamp: '10:41:08',
    toolName: 'shell', toolState: 'done',
    toolInput: 'rg -n "retry_claim|IdempotencyStore" --type go',
    toolOutput: 'internal/settlement/retry/idem.go:15: func (s *IdempotencyStore) Claim(...)\ninternal/settlement/consumer/notify.go:42:  claimed, err := store.Claim(ctx, notify.ID, ...)',
  },
  {
    id: 'p4', ordinal: 4, type: 'tool', lane: 'durable', timestamp: '10:41:31',
    toolName: 'read_file', toolState: 'done',
    toolInput: 'internal/settlement/retry/idem.go',
    toolOutput: 'Claim 使用 INSERT + RowsAffected 判断；在部分 PostgreSQL 驱动配置下 RowsAffected 不可靠。',
  },
  {
    id: 'p5', ordinal: 5, type: 'activity', lane: 'durable', timestamp: '10:42:10',
    activityLabel: '工作区已挂载 payments-service @ 4f2c9a1 · WorkingCopy 干净 · base revision 已快照',
  },
];

export const workerSessions: WorkerSession[] = [
  {
    id: 'wks-a17f',
    title: '支付回调重试导致的重复入账修复',
    status: 'running',
    workspaceId: 'ws-payments',
    workspaceName: 'payments-service',
    boundTaskId: 'task-9f31',
    activeRunId: 'run-0082',
    runtimeGeneration: 1,
    containerId: 'switchback-wks-a17f-g1',
    agent: 'Codex (ACP)',
    agentProfile: 'codex-acp@1.1.2 · codex@0.144.1',
    networkProfile: 'trusted_full_network_v1',
    resourceProfile: 'local_standard_v1 · 2 vCPU / 4 GiB',
    createdAt: '2026-07-27 09:58',
    lastActiveAt: '2026-07-27 10:43',
    idleBudgetHours: 48,
    absoluteBudgetDays: 14,
    heartbeatSeq: 113,
    heartbeatAgeSec: 4,
    tokensUsed: 182_400,
    costCny: 6.42,
    parts: a17fParts,
    trace: [
      { seq: 301, ts: '10:41:08.120', direction: 'out', kind: 'session/update', payload: '{"update":"tool_call","name":"shell","input":"rg -n retry_claim"}' },
      { seq: 302, ts: '10:41:09.004', direction: 'in', kind: 'tool_result', payload: '{"status":"done","lines":2}' },
      { seq: 303, ts: '10:41:31.551', direction: 'out', kind: 'session/update', payload: '{"update":"tool_call","name":"read_file"}' },
      { seq: 304, ts: '10:41:31.990', direction: 'sys', kind: 'checkpoint', payload: '{"checkpoint":"ckpt-0912","parts":4,"digest":"sha256:…"}' },
    ],
    queuedPrompts: [],
  },
  {
    id: 'wks-b208',
    title: 'Frontier SSE 断线重连 cursor 语义梳理',
    status: 'idle',
    workspaceId: 'ws-switchback',
    workspaceName: 'switchback',
    boundTaskId: 'task-2b7c',
    runtimeGeneration: 2,
    containerId: 'switchback-wks-b208-g2',
    agent: 'Codex (ACP)',
    agentProfile: 'codex-acp@1.1.2 · codex@0.144.1',
    networkProfile: 'trusted_full_network_v1',
    resourceProfile: 'local_standard_v1 · 2 vCPU / 4 GiB',
    createdAt: '2026-07-26 15:20',
    lastActiveAt: '2026-07-27 08:31',
    idleBudgetHours: 48,
    absoluteBudgetDays: 14,
    heartbeatSeq: 40,
    heartbeatAgeSec: 9,
    tokensUsed: 402_800,
    costCny: 13.97,
    reportArtifactId: 'art-report-frontier',
    parts: [
      { id: 'q1', ordinal: 1, type: 'text', lane: 'durable', timestamp: '08:12:40', text: '我会先读 Frontier 的 projection 契约与前端 clientViewReplica 实现，再对照 replay/resync 分支逐项核对。' },
      { id: 'q2', ordinal: 2, type: 'tool', lane: 'durable', timestamp: '08:13:02', toolName: 'shell', toolState: 'done', toolInput: 'rg -n "resync|ViewCursor|TransientDisplayDelta" web/src internal/frontier', toolOutput: '命中 47 处，集中在 frontierViewController.ts / clientViewReplica.ts / projector.go' },
      { id: 'q3', ordinal: 3, type: 'artifact', lane: 'durable', timestamp: '08:29:55', artifactRef: 'art-report-frontier', artifactKind: 'report', artifactTitle: 'Frontier SSE 断线重连 cursor 语义梳理报告' },
      { id: 'q4', ordinal: 4, type: 'text', lane: 'durable', timestamp: '08:30:10', text: '报告已交付（task-2b7c → delivered）。发现 2 个实现偏差与 2 个未覆盖边界，详情见 Artifact。如需我直接修复 resync 后 transient buffer 未清空的问题，回复即可。' },
    ],
    trace: [
      { seq: 88, ts: '08:29:55.112', direction: 'sys', kind: 'artifact', payload: '{"ref":"art-report-frontier","kind":"report","digest":"sha256:9be2…c771"}' },
      { seq: 89, ts: '08:30:10.402', direction: 'sys', kind: 'checkpoint', payload: '{"checkpoint":"ckpt-terminal","terminal":true}' },
    ],
    queuedPrompts: [],
  },
  {
    id: 'wks-c3d9',
    title: '对账文件 T+1 延迟告警接入 EventCenter',
    status: 'waiting_user',
    workspaceId: 'ws-payments',
    workspaceName: 'payments-service',
    boundTaskId: 'task-77e0',
    activeRunId: 'run-0091',
    runtimeGeneration: 1,
    containerId: 'switchback-wks-c3d9-g1',
    agent: 'Codex (ACP)',
    agentProfile: 'codex-acp@1.1.2 · codex@0.144.1',
    networkProfile: 'trusted_full_network_v1',
    resourceProfile: 'local_standard_v1 · 2 vCPU / 4 GiB',
    createdAt: '2026-07-26 11:02',
    lastActiveAt: '2026-07-27 10:05',
    idleBudgetHours: 48,
    absoluteBudgetDays: 14,
    heartbeatSeq: 87,
    heartbeatAgeSec: 6,
    tokensUsed: 96_300,
    costCny: 3.18,
    parts: [
      { id: 'r1', ordinal: 1, type: 'text', lane: 'durable', timestamp: '10:02:14', text: '告警规则与 EventCenter publisher 已就绪。为了端到端验证，需要向 staging 的 RocketMQ topic 写入一条测试对账消息。' },
      { id: 'r2', ordinal: 2, type: 'approval', lane: 'durable', timestamp: '10:02:40', approvalId: 'apv-301', approvalAction: '向 staging RocketMQ topic `RECON_DELAY_TEST` 写入 1 条测试消息（可逆，带固定 tag）', approvalRisk: 'medium', approvalState: 'pending' },
    ],
    trace: [
      { seq: 150, ts: '10:02:40.881', direction: 'out', kind: 'session/request_permission', payload: '{"action":"mq_publish","target":"staging/RECON_DELAY_TEST"}' },
    ],
    queuedPrompts: [],
  },
  {
    id: 'wks-d41e',
    title: '推荐特征口径月度对比报告',
    status: 'closed',
    workspaceId: 'ws-reco',
    workspaceName: 'reco-pipeline',
    boundTaskId: 'task-51aa',
    runtimeGeneration: 1,
    containerId: 'switchback-wks-d41e-g1',
    agent: 'Codex (ACP)',
    agentProfile: 'codex-acp@1.1.2 · codex@0.144.1',
    networkProfile: 'trusted_full_network_v1',
    resourceProfile: 'local_standard_v1 · 2 vCPU / 4 GiB',
    createdAt: '2026-07-24 14:47',
    lastActiveAt: '2026-07-24 16:12',
    idleBudgetHours: 48,
    absoluteBudgetDays: 14,
    heartbeatSeq: 0,
    heartbeatAgeSec: -1,
    tokensUsed: 233_100,
    costCny: 8.05,
    parts: [
      { id: 's1', ordinal: 1, type: 'text', lane: 'durable', timestamp: '16:11:50', text: '报告已完成并自动验收。Session 已关闭，仅供只读回看；继续工作请显式创建 continuation。' },
    ],
    trace: [],
    queuedPrompts: [],
  },
];

export const memoryTruths: MemoryTruth[] = [
  { id: 'truth-101', scope: 'workspace', title: 'payments 幂等约定', statement: '支付域所有外部回调必须以 notify_id 作为幂等键，写入路径必须经过 retry_claim；禁止依赖驱动 RowsAffected 语义。', status: 'in_review', confidence: 0.86, version: 3, source: 'Worker wks-a17f · run-0082', evidenceRefs: ['trace#312', 'art-patch-idem'], projectedToViking: false, updatedAt: '2026-07-27 10:50' },
  { id: 'truth-098', scope: 'user', title: '交付偏好：report 先行', statement: '于海川偏好先交付 report 再决定是否进入 patch 阶段；patch 任务默认 manual acceptance。', status: 'active', confidence: 0.93, version: 5, source: 'Assistant 对话 ×7', evidenceRefs: ['conv#88f1', 'conv#91ab'], projectedToViking: true, updatedAt: '2026-07-26 19:22' },
  { id: 'truth-094', scope: 'workspace', title: 'switchback 迁移纪律', statement: 'migrations/ 只增不改；回滚通过新迁移完成。合并前必须通过 internal/architecture/migration_schema_test。', status: 'active', confidence: 0.97, version: 8, source: '人工录入 · 架构组', evidenceRefs: ['AGENTS.md'], projectedToViking: true, updatedAt: '2026-07-25 11:00' },
  { id: 'truth-090', scope: 'tenant', title: '模型路由基线', statement: 'Assistant 轻量推理走 k2-thinking；Worker Coding 走 k2.5-code；成本异常时降级到 k2 并告警。', status: 'active', confidence: 0.9, version: 2, source: 'LLMProxy 策略', evidenceRefs: ['llmproxy#policy-14'], projectedToViking: true, updatedAt: '2026-07-24 09:40' },
  { id: 'truth-087', scope: 'workspace', title: '对账批次内存画像', statement: '清结算批次峰值常驻内存约 3.2 GiB，接近 local_standard_v1 上限；大促前需要评估拆分批次。', status: 'candidate', confidence: 0.71, version: 1, source: 'Worker wks-e5f0 · OOM incident', evidenceRefs: ['trace#incident-6c19'], projectedToViking: false, updatedAt: '2026-07-25 10:05' },
  { id: 'truth-081', scope: 'user', title: '陈晓关注告警链路', statement: '陈晓多次追问对账延迟告警；新告警接入 EventCenter 时应默认抄送其负责人群组。', status: 'rejected', confidence: 0.55, version: 1, source: 'Evolution 窗口 2026-07-24', evidenceRefs: ['conv#7e20'], projectedToViking: false, updatedAt: '2026-07-24 20:14', reviewNote: '样本不足（n=2），且涉及跨用户通知策略，需人工确认后再立项。' },
];

export const skills: SkillItem[] = [
  { id: 'sk-01', name: 'go-ddd-scaffold', version: '1.4.2', latestVersion: '1.4.2', kind: 'skill', scope: 'preinstalled', enabled: true, permissions: ['repo:read', 'workcopy:write'], description: '按 DDD 分层生成 domain/application/adapter 骨架代码。', installs: 312, reviewState: 'builtin' },
  { id: 'sk-02', name: 'pg-migration-guard', version: '0.9.1', latestVersion: '1.0.0', kind: 'skill', scope: 'workspace', enabled: true, permissions: ['repo:read', 'migrations:lint'], description: '校验迁移只增不改、命名规范与回滚配对。', installs: 87, reviewState: 'approved' },
  { id: 'sk-03', name: 'rocketmq-producer', version: '2.1.0', latestVersion: '2.1.0', kind: 'mcp', scope: 'workspace', enabled: true, permissions: ['mq:publish:staging', 'network:egress'], description: '向受管 RocketMQ topic 发布消息（需显式 ApprovalGrant）。', installs: 41, reviewState: 'approved' },
  { id: 'sk-04', name: 'langfuse-trace-export', version: '0.3.7', latestVersion: '0.4.0', kind: 'skill', scope: 'preinstalled', enabled: true, permissions: ['trace:read', 'network:egress'], description: '把 Trace JSONL 增量导出到 Langfuse。', installs: 208, reviewState: 'builtin' },
  { id: 'sk-05', name: 'evolution-skill-draft', version: '0.1.0', latestVersion: '0.1.0', kind: 'skill', scope: 'evolution', enabled: false, permissions: ['repo:read'], description: 'Evolution 从轨迹归纳的候选 Skill：自动整理对账排障清单。需人工内容/安全审核后才能发布。', installs: 0, reviewState: 'pending_review' },
  { id: 'sk-06', name: 'feishu-doc-reader', version: '1.2.3', latestVersion: '1.2.3', kind: 'mcp', scope: 'user', enabled: false, permissions: ['feishu:doc:read'], description: '读取用户授权的飞书文档，作为对话上下文。', installs: 156, reviewState: 'approved' },
];

export const modelStats: ProxyModelStat[] = [
  { model: 'k2.5-code', requests: 1284, inputTokens: 41_200_000, outputTokens: 6_830_000, costCny: 486.2, p95LatencyMs: 9200, errorRate: 0.004 },
  { model: 'k2-thinking', requests: 3561, inputTokens: 28_400_000, outputTokens: 3_120_000, costCny: 214.7, p95LatencyMs: 3100, errorRate: 0.002 },
  { model: 'k2', requests: 690, inputTokens: 5_100_000, outputTokens: 610_000, costCny: 31.5, p95LatencyMs: 1800, errorRate: 0.001 },
];

export const seedEvents: EventLogItem[] = [
  { id: 'ev-1', ts: '10:50:12', module: 'Evolution', kind: 'candidate.created', message: '从 wks-a17f 轨迹生成 ContextCandidate truth-101', level: 'info' },
  { id: 'ev-2', ts: '10:47:03', module: 'Worker', kind: 'artifact.published', message: 'art-patch-idem 已发布（2 个文件，digest sha256:41aa…f0d2）', level: 'info' },
  { id: 'ev-3', ts: '10:43:55', module: 'Frontier', kind: 'checkpoint.committed', message: 'wks-a17f ckpt-0912 已物化 4 个 DisplayPart', level: 'info' },
  { id: 'ev-4', ts: '10:38:20', module: 'Task', kind: 'task.queued', message: 'task-83f2 等待 Session 容量（allocation_wait_reason=session_capacity）', level: 'warn' },
  { id: 'ev-5', ts: '10:05:41', module: 'Worker', kind: 'approval.requested', message: 'wks-c3d9 请求 staging MQ 写入授权（apv-301）', level: 'warn' },
  { id: 'ev-6', ts: '09:58:02', module: 'Task', kind: 'task.created', message: 'task-9f31 创建成功（revision=1，acceptance=manual）', level: 'info' },
  { id: 'ev-7', ts: '09:57:50', module: 'Assistant', kind: 'readiness.ready', message: 'TaskReadinessAssessment 通过，无 blocking_unknown', level: 'info' },
  { id: 'ev-8', ts: '08:31:11', module: 'Delivery', kind: 'card.updated', message: '飞书结果卡片已更新：task-2b7c delivered', level: 'info' },
];

export const currentUser = '于海川';
