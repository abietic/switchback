# Runtime geometry proof design

_2026-10-17 · 承接已批准的有限只读诊断路线_

---

## 📋 目标与范围

取得当前 pool 文件系统 blocksize/UUID 的有限实机事实，补齐 quota cluster 条件推导中的几何缺口。用户在前轮提出的下一步后明确“继续推进”，本次沿该授权推进可逆的只读核验；不变更产品架构、资源数值或 Start 权限。

方案比较：采用既存 v5 FSGEOMETRY/256B，可沿用产品固定 ABI；换V1虽少health读取但引入另一ABI；statfs/GET0已知会调度inodegc，不采用。已完成的 kernel-query-research.md 说明v5的XFS显式分支只读现有几何/health，取锁后写局部输出及用户缓冲；通用VFS/LSM/uaccess/trace hooks与loaded kernel全身份仍unknown。有限诊断不声称整个syscall/VM零effect，不作完整容量证明。

## 🔒 约束

唯一候选 /path/to/switchback；HEAD fbebfe754045bdbf1af40871a5018eed33743011。仅新scratch与本轮spec/plan；无Git写入、产品修改、服务安装/启停、Engine binary执行、Create/Start、Worker/provider/产品付费。旧四seal780/1872/12065/1964及产品current/frozen862必须保持。

Root独占VM/SSH/native；agents只离线generated fixtures/source/review，不派生agents。固定10GiB/80%不变；capacity_ready=false、execution_enabled=false、paid_consumed=false。所有已消费actual/旧角色main禁止重放。新collector有独立exclusive actual，一次成功或失败均消费，不自动retry。

## 🔍 固定查询

只在Linux aarch64、LP64、little-endian、kernel release6.8.0-142-generic执行。复用前轮exact policy/transport/guard bytes。guard前后各一次，既存boot751079a6-74a4-4570-bca6-18e460c7f33d、mountns mnt:[4026531841]、XFS UUID8782738b-dfc2-4426-966f-6a7eb0889ddd、known units/cgroup/read-unit前提不变；这是复用TCB，不证明全局writer排他。

固定目标 /mnt/lima-sbquota；逐component no-follow持有ancestor，O_PATH祖先、最终O_RDONLY|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC|O_NOATIME。每个目录root-owned、非group/world-writable。pool预期dev64785/ino128来自前轮actual，至少核对类型/owner/权限和该identity；仅os.fstat，不调用fstatfs/statfs/statvfs。保留全部held stat，查询前后及postguard后相等；独立重走canonical路径比较全部identity，避免仅heldFD掩盖替换。

同一pool FD前后各一次 ioctl0x8100587e，aligned32 uint64/256B。每次原始return、errno、raw256/hash先保留，再判定成功与decode；失败buffer只记无效payload，不解释为合法零。成功需version5、非零合法power-of-two blocksize512..65536、非零agcount/datablocks、UUID等于固定值、reserved[120:256]全零。前后raw及decoded相等才observed_stable。只输出blocksize、sectsize、inodesize、agcount、datablocks、rtblocks、version、flags、UUID、health两个uint32，不据health0声称healthy。

不查询quota、GET0、statvfs、free、原始块、DB或任意用户路径；不枚举目标。fixture root/uid/hooks仅pure inspect接口，native argv/env不能选择；main不接受target或query输入。

## 🧪 证据与门禁

guest CPU4s/wall20s/AS256MiB/FD128/FSIZE0/core0；host35s，structured2MiB/transport3MiB。两份256B+12条最大guard capture和固定shape须有静态reserve证明，不能overflow时丢已取得raw或commands。

host verify_local(expected_manifest_sha256)只做本地绑定：newmembers、design/brief/baseline/research、10份kernel源、reuse三份、prior seal及862 current/frozen。prepare构造固定Lima argv/bootstrap，不dispatch。main独占actual，snapshot源码/manifest/argv和invocation，transport原stdout/stderr+exit+termination先落盘再strict decode/校验。异常原始errno和raw保留；negative不可签发通过；remote/group termination仍unknown。

离线测试覆盖ABI/version/UUID/size/reserved/geometry变化、symlink/unsafe/missing/identity/rewalk、ioctl errno mutatedbuffer、postguard fail仍保留raw、limits/static reserve、source drift、raw-first malformed/negative/overflow、single-use。仅新suite，保留original receipts/source，不无故复跑旧suite或测试不变源码。Task独立Spec/Quality gate无C/I才允许Root新single native；之后Root独立解码与fresh累计审查、binding refresh、handoff/seal。

## 📍 成功标准与后续

阶段成功是geometry有限观测或明确保留拒绝，以及审查/证据链完整；不是Start通过。几何成功后将4096条件（若实测如此）代入已绑定dquot结构，仍分别核实quota持久mapping、999/1000 chunk增量、kernel实现对应、named initialization/writer、Q0/metadata/恢复/并发、host backing和80%aggregate合同。OpenSpec38/41、6.5/8.4/9.3保持OPEN；完整容量和真实体验闭环仍待证。
