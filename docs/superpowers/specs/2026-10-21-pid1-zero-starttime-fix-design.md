# PID 1 零 starttime 最小修复设计

日期：2026-10-21。状态：待用户确认；本文件没有授权新的 guest 操作或源码实施。本次普通用户单次取证已成功结束。

## 问题与证据

实际读取 switchback-core 的 PID 1：starttime=0，cgroup 为 0::/init.scope；有效 UID 501；前后 boot ID 相同，且与 B3-F 记录相同。当前未修改的 poolProcess 对原始输入返回 pool_invalid；poolUnified 接受原始 cgroup；仅把 stat 的第 22 字段从字符 0 改成 1（完整输入只差一个 byte）即通过联合解析。重复两次得到相同拒绝。

证据在 当前诊断记录 (private evidence retained)。当前快照不能复原上次 unit 的全部上下文，也不证明旧进程清理；这些历史未知保持原结论。

Linux 的第 22 字段是启动后时间的 clock ticks，非身份“已观察”标志。内核进行整数单位转换，值域并不保证大于零；这里不依据零值猜测精确启动纳秒数。见 [proc_pid_stat(5)](https://man7.org/linux/man-pages/man5/proc_pid_stat.5.html) 和 [Linux nsec_to_clock_t](https://raw.githubusercontent.com/torvalds/linux/v6.8/kernel/time/time.c)。

## 方案与取舍

推荐修正现有共享纯 parser：仅当调用参数 expected PID 明确为 1、原始 stat 中 PID 也通过等于 1 的绑定校验时，允许 starttime=0。其余 PID 和 expected PID=0 的通用读取保持正值要求。这是已有观测格式的窄修正，不增加 API、DTO、协议或执行框架。

另一种方式是新增部署专用 PID 1 parser，保持共享 parser 字节不变；但会增加解析分支，且 B3-G 的旁路检查必须同步选择新 parser。当前证据没有支持为这一条件创建第二套解析实现。

不采用全局删除零值检查，也不把零值替换为 1、null 或“未观测”：原值是事实，不能编造。

## 具体修改

1. internal/platform/capacityprobe/pool_native.go：在保留 decimal、范围、实际 PID、comm、长度和 cgroup 全部原检查的前提下，只收窄 ticks==0 的拒绝条件。前面的 PID 匹配检查必须继续先执行；expected PID=1 不能接受其他实际 PID。
2. 补纯 parser 和完整 deployment fixture 回归。B3-G 的检查仍调用同一 poolProcess，修正相关“零 PID 1 必须拒绝”的旧测试假设，保留其他 process 拒绝用例。
3. 下游旧成功 mapper 也拒绝 PID 1 零值。旧封存 mapper/carrier 不改；在本进度目录下的新 fix-candidate 子目录建立纯 mapper 离线候选与测试，唯一语义差异为 PID 1 的 start_time_ticks 可为 uint64 零值；caller 仍要求正值。固定 unit/cgroup 绑定沿用基线用于纯夹具，不能据此部署或复用旧 unit。未来真实候选必须重新绑定新的 exact identity。
4. 不改成功/失败 schema、pool 资源预算、Trust、PG、Start、IPC、publisher、capability 或环境。旧报告、快照、seal、ELF、once marker 都保留原字节。源码变化另记前后差异，不能重写旧封存来伪称历史输入未变。

## 验收

- 本次完整原始 stat/cgroup 离线重放通过，返回 ticks=0 和 /sys/fs/cgroup/init.scope；只改 ticks=1 的对照仍通过。
- 非 PID 1 的 zero ticks、expected PID=0 + actual PID 1 zero、PID 不匹配、负数、溢出、缺字段、坏 cgroup 仍拒绝；PID=0 不因本修复合法化。
- 完整 observeDeployment 夹具能保留 PID 1 零值；前后或 canonical 重读的 ticks 从 0 变为 1 仍拒绝。boot、FD、namespace、status、UID/capability 与 cgroup 的身份比较不减少。
- 新 mapper 接受前后相同的 PID 1 零值，仍拒绝 caller 零值、bool、负数、溢出、身份错配和前后漂移；authority 仍 none。旧 mapper 的既有拒绝回归保留。
- 运行相关纯 parser、deployment/API/CLI 和 mapper 回归；Linux amd64/arm64 仅 compile-only。环境相关 HTTP/Engine/PG/provider 测试不在本片启动。
- 最小原始重放 RED → GREEN 有实际日志；旧封存产物不变，索引仍为空。不构建部署 ELF、不进入 guest、不启动 unit。

本修复完成后只能宣称“已修复并离线验证当前零值拒绝”，不能宣称 native 观测整体通过或容量门禁可开启。后续真实窗口仍需要覆盖新的候选、身份与单次授权。

