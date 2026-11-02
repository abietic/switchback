# Engine 进程身份校验的 R/S 最小修复设计

状态：候选设计，尚未授权源码实施或再次实际执行。

## 事实与目标

kit13 的剩余复制恢复已成功：四新副本全部验证通过，五旧对象完整保留。随后单次 Engine 窗口在 preflight 失败，guest 报 `process identity drift during FD enumeration`；host 原样保留 `status=unknown / remote_effects=unknown`。后续安装/启动步骤、镜像、新秘密、新 PG 和资格窗口均未开始。失败窗口不得重跑或恢复。

本次 saved packet 的 observation frame 1918/1931 来自 `/proc/1348/stat`：

| 字段 | 前 | 后 |
| --- | --- | --- |
| main_pid | 1348 | 1348 |
| start_tick | 544 | 544 |
| flags | 4194624 | 4194624 |
| state | S | R |

cmdline 原字节是 `sshd: yuhaichuan@notty`。`_parse_stat` 的四字段字典在 `_IO.processes` 被直接整体比较；只有 state 变化便足以触发现行错误。[Linux 内核 proc 文档](https://docs.kernel.org/filesystems/proc.html) 将 R/S 分别定义为 running/sleeping，start_time 是启动后进程开始时间；运行状态不是 PID identity 的替代品。

离线复现：`python3 -B .scratch/runtime-m1-copy-recovery-execution-1024/replay_fd_identity.py`。未修改的生产 `_IO.processes` 在实际 stat pair 上抛出同一错误，same-stat control 通过。stat/cgroup/cmdline 为真实字节；exe 与空 FD 列表是隔离输入，因此只证明比较器问题，不是全量 FD inventory 或 guest 重放。

## 方案选择

推荐 A：仅为身份完全不变的 R/S 切换添加接受分支。它覆盖本次真实失败，不放宽 PID、start tick、任何 flags 位及其他状态转换。

B：从比较中彻底移除 state，并重设所有进程生命周期状态的接受策略。适用面更广，但会扩大 Z/X/unknown 等状态的安全语义，本次不采用。

C：原逻辑不改，遇到状态变化重试采集。它不能修正模型错误，且会引入额外读取和重试，本次不采用。

## 精确行为

只改 `deploy/experience/engine/guest.py` 的 `_IO.processes` 前后身份判定，使用内部纯 helper 或等价表达：

```python
stable_identity = all(before[k] == after[k]
                      for k in ('main_pid', 'start_tick', 'flags'))
same_state = before['state'] == after['state']
ordinary_switch = {before['state'], after['state']} <= {'R', 'S'}
_require(stable_identity and (same_state or ordinary_switch),
         'process identity drift during FD enumeration')
```

`_parse_stat` 输出、原始字节记录和 result 使用 before 的现行行为保留。其他状态相同的旧行为原样保留；其他状态之间的迁移继续拒绝。PID 1 零 start tick、kernel-thread 例外、FD/owner/socket/三类 listener 完整覆盖、exe/cgroup、进程表复查、限时与故障停止规则均不改。不能跳过该 sshd 或任何进程。

## 一片交付与验证

- 先把本次真实 stat pair 的隔离复现转为对应生产调用点的回归；确认旧比较失败，修复后通过。
- 增加 R→S 正例，以及任一 main_pid/start_tick/flags 变化、R/S 与 Z/X/其他/unknown 的转换拒绝回归。保留 same-state、PID1/kernel-thread 原测试。
- 运行相关完整离线测试与 source/binding 检查，不重跑历史本机隔离 fixtures，不启动 guest/Engine/PG/provider。
- 构建完整新 kit/bundle，核对 manifest、SourceRoot/TemplatesRoot、所有目标格式和逆绑定；独立复审源码与交付绑定。
- 四份现行 kit13/bundle 和本次已消费的 host stage 必须在未来实际切换前完整保留；不能清理或复用旧 claim/stage。其冻结证据为 `.scratch/runtime-m1-copy-recovery-execution-1024/host-stage-preservation.json`。

接受本设计只授权整片源码与离线交付；不包含写入现行 host/guest 副本、服务动作或新的实际窗口。新实际操作在具体候选、保留清单与验证完成后再确认。原 Engine120秒、资格240秒、barrier300秒、双能力和 PG/Trust/Start 三门禁保持。
