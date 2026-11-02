# M1 共享卷保证：源码与离线实现交付

2026-10-22。范围为 accepted existing-volume capacity design 的源码实现与离线回归；不代表真实部署资格、物理隔离或产品体验闭环完成。

## 合同与实现范围

`m1_shared_volume_capacity_v1` revision 1 已加入固定 catalog，规范定义为 482 bytes，digest `sha256:31b0caa11d3ff6cf95631815a0879771ffce565c735f6fd5f6ecab58638dcb01`。原 `m1_accounted_capacity_v1` 的 367-byte 定义和 digest 保持精确兼容，nil 保持 legacy，未知 ref 拒绝。新保证取消独立宿主物理上界证明义务，保留实际 APFS 卷身份、quota/reserve、受管路径和 native 验证。

Task 2 已实现共享资格判定、当前 publisher/Backend 角色重验和两个生产组装入口；独立 Spec/Quality re-review 为 PASS，详见本阶段 `task-2-rereview.md`。这些是合成资格数据的离线结论。Backend 在首个真实样本前仍以 stale 组装，qualification 不授予 reserve 或 Start 权限。

Task 3 仅新增原 owner 兼容测试和修正一个已经过时的 localproduct AST 结构测试，无 production 修改：真实 Mapper 保持调用方实际 sequence、原始 Engine/storage accounting 输入及 inode hard=0 的“无已证明 inode 上界”诊断；真实 raw v1/v2 签名验证与 manifest 绑定拒绝旧 manifest，raw schema 不增加 guarantee 字段。v1 的完整检查是 `Verify` 后调用 `CheckFacts`，不能把签名真实性误作部署一致性。

真实 created-layer reader/verifier 覆盖 hard/used 全 uint64 矩阵、错误 manifest/domain/schema/key；原 coordinator 在 finite check 后重验 authority，只允许一次 Start，撤权或错误 quota/owner/manifest 不 Start。新增 observer 回归还将新 ref 和独立 manifest 经过原 `capacityobserver.Checker` 的绑定、observationFacts 和 verifier 行为链（仅替代私有 observation transport）；inode hard=0 成功，错 CID/resource/source/manifest 及二次撤权均在原 coordinator 中拒绝 Start，合法路径精确一次 Start。原 PG 纯 helper 保持 persisted Trust 精确投影、detached copy、旧/新/nil ref 的 settlement 不相等，未改 PG schema、reserve/consume 或 unknown-effect 收敛。localproduct 回归证明新 ref 到达原 stale owner，且缺失具体 checker 的组装不能 Start。80% aggregate 算法未改；本轮 Mapper 测试证明输入不变，不声称已执行真实 PG 原子准入。

```mermaid
flowchart LR
    accTitle: Source delivery and remaining acceptance
    accDescr: Offline source delivery precedes actual volume qualification, signed publication, original owner consumption and product acceptance.
    A[源码与离线回归] --> B[实际卷及 native 资格]
    B --> C[真实签名发布与原 PG 消费]
    C --> D[Create quota Start]
    D --> E[任务 报告 接受 观测]
```

## 实际验证

所有 Go 命令均显式移除 `SWITCHBACK_TEST_DATABASE_URL`，使用 `GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 GOCACHE=/private/tmp/switchback-shared-volume-1022-go-cache`，最终可复制命令见本阶段 `task-3-report.md`。

- 七包完整 offline 最终 exit 0：config、capacityprobe、capacitysnapshot、capacitypublisher、capacitypublication、capacityobserver、postgres。
- localproduct focused 包含 `RuntimeCapacity|InitialCreatedLayer|RuntimeCreatedLayer|SharedVolume|DeploymentQualification`，最终 exit 0。
- review 补齐原 IPC Checker 行为回归后，完整 capacityobserver 包 fresh exit 0；其余 7 包回执保留，未为 test-only 修改重复整套。
- integration tag 的 postgres/localproduct 编译通过；所有选中的真实 PG 子用例因缺失 DB URL 明确 SKIP，localproduct 无匹配测试，仅编译。父测试显示 PASS 不能当作 PG 执行证据。
- Linux arm64/amd64 编译复用 Task 2 final repair state 的准确 exit 0 回执；Task 3 无 production 修改，没有重跑架构编译或宣称执行 Linux 二进制。没有新增生产并发 guard，未运行 race。

首轮失败均保留：新增测试遗漏 bytes import；新增 raw v1 测试最初遗漏 `CheckFacts`；旧 AST 测试仍固定拆分前调用链；Store reader 拒绝默认临时目录祖先中的 symlink。最后一项根因是 TMPDIR 路径，**不是目录权限要求**：最终命令设置 `TMPDIR=/private/tmp` 后通过，未修改 reader 或 Store fixture。

## 待实际验收

旧 C06 的独立物理证明只在新 shared-volume 保证下不适用，不能标为物理证明通过。新卷级资格仍等待真实、当时绑定的 UUID/container/quota/reserve/镜像及受管路径重验，随后执行受界 native enforcement 资格；历史窗口和 seals 不重用。

下一里程碑依次为实际卷/native 资格、真实签名容量发布与原 PG/Create→quota→Start、任务→正式报告→页面接受→同 Task 观测。真实 PG 并发/事务、Engine/VM/服务、provider、listener、unit、密钥/Trust/journal 初始化均未执行。`openspec/changes/production-web-core-experience/tasks.md` 的 6.5/8.4/9.3 保持 unchecked。本轮不声称生产就绪、独立物理隔离、实际容量 ready 或体验闭环完成。
