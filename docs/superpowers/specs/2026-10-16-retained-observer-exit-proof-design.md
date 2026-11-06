# Retained observer exit-proof design

用户在上轮交接明确提出下一独立阶段补验退出回执、再推进 Engine/CID quota 后，要求“继续推进”。本设计落实该已选方向。没有 Git 写操作；不触碰 consumed Engine 单元、marker 或旧证据。

## 目标与选择

新的有限子阶段只证明修复后的 oneshot 退出留存，复用已验证的 observer/server/client ELF、固定有限角色 key 和 observer unit/drop-in。以精确旧身份比较后，显式接管自有 observer mask与公开deployment env，建立新公共digest、新client unit、新stage/output/receipt。旧客户端unit和旧phase目录只读。替代路径是重装全部资产，会增加key与文件变更且无助于Engine预算缺口；另一条可并行路径是先补增长源码证明，不需要等待本次IPC结果。采用复用和源码研究并行。

成功要求新client在 `Type=oneshot/RemainAfterExit=yes` 下返回 outer0、`ExecMainCode=1/ExecMainStatus=0/MainPID=0/Result=success`，状态为active/exited，然后记录真实认证partial、capabilities0、NNP1、相同namespace及严格拒绝。缺失原始exit仍拒绝；公开JSON不替代退出证据。失败保留一次消耗记录，不自动重试。

## 组件与数据流

新控制器复用已审R3 Python policy的参数化安全文件/command runner、环境/observer读回与client结果校验函数；不得调用旧main/actual/stage/cleanup或改其全局路径。复用已审host transport函数，避免重写中断/回执框架。所有复用源SHA固定；导入关闭pycache写入。

只读preflight → bounded source stage → exclusive新attempt marker → 归档旧公开env和mask metadata → exact-owner原子替换公开env与自有observer unit → 安装新client unit/output → native verify/effective guards → 单次observer/client → 原始退出及公开结果 → 独立owner cleanup/重新mask → 只读post。key始终只由现有native server/client读取，控制器不读取或hash key。

新stage固定 `/mnt/lima-sbquota/core/engine-quota1016-exit-v1`；新client `switchback-engine-quota1016-exit-client.service`。observer仍为固定原unit/cgroup。fresh公共digest为 `sha256:` 加新manifest的64hex SHA；manifest不包含自己的摘要，不形成循环。reuse的diagnostic ELF内部synthetic IDs不表示DB身份，新公共digest/nonce区分本次请求。

## 边界与验证

原VM boot/XFS/namespace/core limits、retained key metadata/ELF/unit/drop-in/public-env/mask identities都必须即时匹配；任何漂移或新目标已占用在effects前拒绝。不创建key、不重新编译Go、不改Backend capability、Docker data root、stock/consumed daemon/watchdog或旧client。Engine启动/恢复/Create尚无事前预算，不在本子阶段执行。

沿用observer独立45s RuntimeMaxSec；helper主阶段27s、cleanup截止40s，host54s且最多55s。保持原cgroup/security limits。cleanup按独立owned对象继续，unknown不得触碰，mask不冒充退出；post和直接PID/cgroup/socket读回分别记录。内核IO不可抢占限制及remote termination unknown保留。

新recording测试覆盖owner/config漂移、occupied/consumed、sha256前缀、same-filesystem原子replace与跨FS归档、无key读取、client code0拒绝/code1留存、局部cleanup失败及原始raw保留。不重复旧38项不变库测试，不用offline结果冒充native。实现包先独立Spec/Quality operational review，再由Root按既有授权执行一次。

另行定向研究 exact Engine bootstrap/recovery 的quota增长调用链与可证明上界。未知项必须指出具体源码或控制缺口，不新增一套永远拒绝的产品authority。完整容量、真实CID、Start、Task/PG/UI和付费验收保持各自门禁；OpenSpec6.5/8.4/9.3不关闭。
