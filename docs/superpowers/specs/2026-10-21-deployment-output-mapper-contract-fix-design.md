# 部署诊断 output mapper：按现行 Go v1 契约修正

状态：待用户审阅；未实施新 mapper。本设计不授权再次进入 guest。

## 事实与目标

2026-10-21 的 PID1 修复真实窗口已在固定新身份下完成四步，父进程/worker 均 exit0，唯一 unit launch 返回0。本次新 ELF 的原始包保留 PID1 before/after start_time_ticks=0；manager ExecMainStatus=2，PID1 四类 namespace 均为 EACCES。旧 mapper 对实际3140-byte packet 判为 invalid_output。

真实回执位于 .scratch/runtime-m1-deployment-pid1-native-window-1021；宿主执行和离线回放证据位于 .scratch/runtime-m1-deployment-pid1-native-execution-1021。actual/readback 中的诊断原文相同，SHA256 为5a9b13ddc411556e76ab12cceecf14438b6154688802cf4ffabda3d604f91cb9。

当前源码依据：poolUnified 将 proc cgroup 路径转为 /sys/fs/cgroup 前缀的绝对路径；deploymentRead.process 直接使用该结果。observeDeployment 写入 namespace Owner=caller_thread|pid1；现有 Go fixture 也明确验证这两项。冻结 Python mapper 仍要求 /system.slice/... 相对路径及 caller|pid1 owner。

目标：仅修正这两项纯输出契约匹配，直接重放原始字节并保留受限权限事实。原始回执、已封存候选、生产 Go 和现有门禁保持原样。

## 路线比较

| 路线 | 代价与结论 |
| --- | --- |
| 新建独立纯 mapper，严格对齐现行 Go v1 | 推荐。两处固定匹配和对应 fixture 修正，无需新 ELF 或真实执行。 |
| 修改 Go producer 输出迎合旧 mapper | 影响共享 cgroup parser、现有 Go DTO/测试和新构建；本次不选。 |
| 同时接受两种路径和 owner，或自动标准化原文 | 无必要地扩展可接受输入，并可能掩盖格式漂移；本次不选。 |

## 拟实施边界

在新目录 .scratch/runtime-m1-deployment-output-contract-fix-1021 新建 output_mapper.py、test_output_mapper.py 及必要的真实回执 replay/evidence。目录冲突即停。复制当前 PID1 candidate 的 mapper 与测试，再仅做以下修订：

1. mapper 的固定 caller cgroup 期望为 /sys/fs/cgroup/system.slice/switchback-deployment-diagnostic1021-pid1.service。保持完整精确相等；不 trim、prefix match、normalize 或接受旧相对路径。
2. namespace entry owner 只接受 caller_thread 和 pid1；保持八项组合、唯一性、before/after typed equality、device/inode/errno/class 和 exit 对应校验。process packet 字段 caller_before/caller_after 不改名。
3. PID1 零 ticks 的精确绑定与 caller 正值要求保持；不改 schema、资源、capability、Trust 或任何执行授权。
4. 原 assess_output 仍返回现有 success_packet/expected_probe_exit_from_packet=2，表示格式有效的受限诊断包。离线结果必须显式保留四项 PID1 EACCES，deployment_ready=false、authority=none、execution_linkage=unproven，不能将 success_packet 文案等同完整 namespace 或部署资格。

不修改 host/guest/manager、Go、旧 mapper、sealed source/build/receipts/seals，不发布或替换运行中程序，不运行 ELF，不进入 guest，不创建 unit/stage，不改变三个门禁。真实窗口的一次授权已消费，不重试、不续跑、不独立 readback、不发 Stop。

## 验证与完成标准

- 用已保存 actual/readback packet 的原始 bytes 直接重放，不重写路径或 owner；两者均为格式有效、expected exit2，并保留 PID1 ticks0和四项 EACCES。
- 先记录冻结旧 mapper 对同原文 RED，再验证新 mapper GREEN；该 GREEN 只表示离线分类修复。
- 路径错误/旧相对路径、caller owner 旧别名、其他 unit、duplicate/missing namespace、owner/type 混淆、before/after 漂移、caller ticks0、PID1 0→1、bool/overflow、exit 不匹配、stderr/JSON 及原边界拒绝继续生效。
- 将现有纯 mapper 全套回归 fixture 对齐 Go 契约，再运行整套纯 mapper 回归和真实 packet replay；无需重跑未修改 host/guest 的大故障矩阵或 Go/race/native suites。
- 核对已封存候选/真实窗口和预存源码/metadata 不变；独立审查最小差异和真实回放；生成明确离线结果。

本片不解决 PID1 namespace 的 EACCES，也不增加权限。完整部署观测、容量发布/消费、Create→quota→Start 和产品闭环分别保留未验收状态。
