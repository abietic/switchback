# M1 Worker Runtime 单次恢复人工验收

_目标：证明 Runtime generation 可以安全替换一次，同时逻辑执行 identity、原 deadline 和持久证据保持一致。_

## 通过标准

验收必须同时满足：

- 一个 eligible hard signal 只产生一个 incident 和一个 replacement
- WorkerSession、WorkerRun、AgentSession 与 WorkingCopy 不变
- Runtime 从 generation 1 变为 generation 2，ChannelEpoch 与 SecretLease 更新
- 旧容器和旧 secret volume 在 replacement 获得写权限前已确认清理
- 恢复不会自动重放 generation 1 的 prompt 或 Tool 调用
- replacement 健康与 continuity 未证明前，incident 不得标记 recovered
- deadline/absolute expiry 可以抢占恢复，且不会被重置
- 恢复耗尽时进入可操作的 waiting-user 状态，不启动 generation 3
- 旧 generation 不能发布当前 Artifact 或 Display mutation
- 最终没有本次 Runtime container/secret volume 残留，源 fixture 与已知 token 检查通过

## 自动基线

在仓库根目录运行：

```bash
SWITCHBACK_DEEPSEEK_KEY_FILE=/absolute/path/to/deepseek.key \
SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=1 \
make runtime-recovery-acceptance
```

检查报告：

```bash
jq '{
  outcome,
  enablement_ready,
  missing_enablement_scenarios,
  deterministic_integration,
  fixed_image: {
    outcome: .fixed_image.outcome,
    profiles: [.fixed_image.profiles[] | {
      recovery_attempted,
      recovery_scenario,
      production_recovery_path,
      replacement_runtime_generation,
      agent_session_preserved,
      no_prompt_replay,
      identity_cardinality,
      managed_container_residue,
      managed_secret_volume_residue
    }]
  }
}' runtime-data/runtime-recovery/<run>/runtime-recovery-acceptance.json
```

当前自动基线预期 `outcome=passed`、`enablement_ready=false`，并显式列出尚缺的 enablement scenarios；fixed-image profile 必须是 `recovery_scenario=idle_between_turns_manual_replacement`、`production_recovery_path=false`。该 profile 的 identity cardinality 为 1 WorkerSession、1 WorkerRun、2 Runtime、1 AgentSession、2 AgentTurn，且两个 residue 字段均为 0。

不得把 `outcome=passed` 单独解释为生产自动恢复可启用。完整通过标准是 `enablement_ready=true`、`missing_enablement_scenarios=[]`，且所有 required scenario 都有 production/fixed-image evidence。

## 本地产品观察

1. 运行 `make local-up`，创建一个至少会执行两个独立 Turn 的 Worker Task。
2. 记录 Task、WorkerSession、WorkerRun、AgentSession、Runtime generation、AgentTurn `started_at/deadline_at`。
3. 第一个 Turn 完成后，在第二个 Turn 之前终止当前 Worker container；不要删除 Session 数据目录。
4. 打开 WorkerDetail，观察 `recovering_runtime` 及 phase 从 fencing 到 verifying。
5. 等 replacement 满足稳定窗口后发送一个新的显式 Turn。
6. 核对新 Turn 使用原 AgentSession 与 WorkingCopy，旧 prompt 没有再次执行。

可用的只读容器检查：

```bash
docker ps -a --filter label=com.switchback.worker-session-id=<worker-session-id>
docker volume ls --filter label=com.switchback.worker-session-id=<worker-session-id>
```

禁止把 `docker start` 旧容器、复制旧 secret、修改数据库 generation 或重发旧 prompt 当作验收步骤。

## 负向矩阵

| 场景 | 注入方式 | 预期 |
|---|---|---|
| 重复 hard signal | 同一 failed generation 先后报告 container missing、authenticated ACP EOF、heartbeat unhealthy | 一个 incident、一个 Outbox 语义结果、一个 replacement |
| 仅 WorkerControl transport close | 断开 gRPC，但保持原 Shim/container 存活并让 exact generation/epoch 重新完成 resume | 只记录 disconnect/reconnect；不创建 incident、不替换 Runtime、不重置 first-heartbeat anchor |
| ResumeComplete 后无首心跳 | 记录首次 `ResumeComplete` anchor，分别推进服务端时间至 T+44.999、T+45、T+90 | T+45 前仍 awaiting；T+45 为 suspect；T+90 普通 Runtime 仅打开一个 `heartbeat_unhealthy` incident，replacement 则以 `replacement_first_heartbeat_timeout` 耗尽现有 incident 且不创建 nested incident |
| 首心跳与 suspect 竞态 | 在 T+45 evaluator 提交前后送达一条 sequence/control sequence 均合法且 receive time 早于 T+90 的真实 heartbeat | watchdog 最终 `satisfied`，保存真实 sequence 与 receive time；pending T+90 command 被取消，Runtime 不进入 unhealthy |
| first-heartbeat watchdog 期间 Backend restart | 首次 `ResumeComplete` 后、真实 heartbeat 前重启 Backend，并让原 due time 跨过 T+45 或 T+90 | startup 在开放普通 dispatch 前 drain 到期 watchdog；liveness 使用原 heartbeat/watchdog anchor，不按 restart/reconnect 时间重置 |
| journal/observation 崩溃或 backlog 窗口 | 在更早 business event 尚未 applied 时，让一条合法 heartbeat 以 `received` 留在 durable journal，并在 watchdog evaluator 前重启或停住 ordered pump | T+45/T+90 evaluator 在原 Session/Runtime/watchdog 锁内先补 observation、satisfy proof 与普通 liveness command，但保持 heartbeat `effect_status=received`、不推进 `event_applied_sequence`；ordered pump 后续按序 duplicate-safe apply |
| 多条 journal heartbeat 积压 | 在同一未 applied business event 后顺序写入两条 T+90 前的合法 heartbeat | 第一条冻结 first-heartbeat proof，普通 observation 推进到第二条的 sequence/receive time；只保留一个以最新 heartbeat 为 anchor 的 pending liveness command |
| 无效或边界 journal heartbeat | 分别写入无法严格解码的 heartbeat payload，以及 `created_at == unhealthy_due_at` 的合法 payload | 两者都不能 satisfy watchdog 或创建 observation；T+90 仍按原 anchor 收敛为 unhealthy/recovery |
| fencing 无法证明 | 让精确 cleanup 返回未确认 | incident exhausted，不创建 replacement，durable cleanup/reconciliation 可见 |
| replacement provision 失败 | 在 generation 2 容器创建处失败 | attempt=1，Session/Run/Task waiting_user，不创建 generation 3 |
| ACP continuity 不可证明 | replacement 无法 resume/load exact AgentSession 或旧 active Turn effect 未知 | `recovery_continuity_unproven`，不重放 prompt |
| stability window 内退化 | replacement heartbeat 失效或 runtime 非 active | incident exhausted，不再尝试 |
| deadline 抢占 | 把服务端时间推进至原 AgentTurn deadline | Turn/Run `timed_out:agent_turn_deadline`，Session/Task waiting_user |
| absolute expiry 抢占 | 把服务端时间推进至 Session absolute expiry | Session expired，Task 依 expiry contract 收敛，恢复资源被 fencing |
| 旧代际晚到 | 用 generation 1/旧 ChannelEpoch 提交 event 或 Artifact publication | current aggregate/Display/Artifact 不变 |
| feature gate 关闭 | `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=0` 后注入新 hard signal | 不创建新 incident；已有 incident 继续收敛 |

## 证据记录

每轮人工验收至少记录：

- exact incident/session/run/turn/runtime identities
- failed/replacement generation 与 ChannelEpoch
- incident phase/revision/attempt/outcome reason
- 原始 AgentTurn `started_at/deadline_at`
- first-heartbeat watchdog 的 immutable anchor/revision、T+45/T+90 due time、terminal status/reason，以及真实首心跳的 heartbeat/control sequence 与 server receive time
- fence/cleanup/continuity/health 的引用，不复制 secret 或 provider payload
- Task/Session/Run 的最终状态
- 本次容器和 secret volume residue 数量
- 自动报告路径和 checksum

发现任何 prompt 重放、generation 3、双容器可写、deadline 被延长、AgentSession identity 改变或残留未被 reconciliation 管理，均判定失败并保持功能 gate 关闭。
