# M1 资格角色 UID 修复与首启推进记录

_2026-10-22 · 已交付源码兼容修复；新首启设计待审阅；本轮没有实际环境执行。_

## ✅ 本轮交付

修复了会阻断真实部署资格的 UID 错误：原 verifier 要求所有 core 角色 UID0，而当前 Compose 的 PostgreSQL 为 UID999、Assistant 为 UID10001。现在按每个冻结角色声明的 UID 比较 real/effective/saved/fs 四项 UID；Backend/publisher/observer、guest host-process 与 Backend handoff 继续要求 UID0。

声明值必须在 uint32 范围内再转换；溢出、单项 UID 漂移、capability/ambient/inheritable、NNP/seccomp 漂移、缺 core 与非 root handoff 仍拒绝。没有新增权限或改变 raw/签名/PG/Start 合同。

实现范围仅为两个原 verifier 文件和一份新回归文件。使用完整冻结资格夹具重现：相同 postgres role/CID/build 的 UID0 对照先通过，UID999 和 UID10001 先失败，再在修复后通过；这证明原资格规则与配置不兼容，不是仅改一个 helper 后推断完整资格成功。夹具是合成重放，未启动真实 PG。

## 🧪 验证与构建

受影响的 capacitysnapshot、publisher、observer、capacityprobe 全包及八项定向 localproduct 组装回归通过。snapshot/publisher/localproduct 在 Linux arm64、amd64 的六次测试编译均通过，未执行 Linux 测试程序。独立审阅的 Spec、Quality、单任务整片跨边界检查均为 PASS，无待处理问题。

- 实际 RED/GREEN、包回归和编译命令/结果 (private evidence retained)
- 独立审阅 (private evidence retained)
- 当前修复后的三个 Linux arm64 ELF 构建记录 (private evidence retained)

三个 ELF 是 Backend、publisher、observer，均为 AArch64 ELF64；371 个仓库实际构建输入在构建前后保持一致。新的 Backend/publisher hash 替代前一准备阶段的构建身份，不能把旧构建当作包含本次修复。构建产物未运行，不构成镜像、loaded artifact 或资格证明。

控制端核对 reviewed source hashes、1842 个基线文件及 1188 个历史 protected entries 的内容和身份元数据；没有无关变更，HEAD 未变、index 为空。记录见 preservation verification (private evidence retained)。未改旧 raw packet、seal、protected publisher unit 或 publish_test.go。

## ⏭️ 下一片与当前完成度

首启循环已定位：采集资格需要真实角色进程，但角色配置完整后缺资格文件就退出；observer 更在程序执行前依赖最终 manifest digest。下一片采用[完整采集、安装与首启设计](/path/to/switchback/docs/superpowers/specs/2026-10-22-m1-qualification-producer-bootstrap-design.md)：同一 ELF/PID 有界等待、最终 manifest 最后提交、完整资格后恢复原启动链，以及真实 ABI executor 的独立 native qualification v2。

这会新增启动行为和私有资格格式，当前仅完成设计、自审和用户审阅请求，没有实施。设计接受后连续完成 producer、安装器、入口、离线故障验证与候选装配，再提交具体真实操作清单。

距离完整实跑仍有三道验收：实际卷/native 资格；fresh signed sample → 原 PG → Create/quota/单次 Start；同 Task 的报告、页面接受与观测关联。当前关闭的是 UID 源码阻塞，不是这三道实际验收。第一条完整体验闭环通过后开始定向精简。没有新增外置盘、主盘分区或独立物理上界要求。
