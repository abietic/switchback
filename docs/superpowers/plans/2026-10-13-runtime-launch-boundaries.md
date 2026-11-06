# 真实 Runtime 启动资源边界实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 补齐真实 Docker Worker 与秘密复制容器的 cgroup/swap 约束，并把已实测的 Lima 端口规则带回候选源码。

**Architecture:** 沿用现有 DockerProvisioner 和 deployment-owned systemd slices，不引入新领域状态或调度器。资源值仅由服务端固定；复用容器必须验证这些不可变配置。create-before-start 物理 quota 验证与首次 Prompt 前实际 membership 门禁仍是独立待实现步骤，不能由本任务的 API 参数检查冒充。

**Tech Stack:** Go、已锁定 Moby API v1.55、Lima 2.2.1、Linux cgroup v2 / systemd。

## Global Constraints

- 唯一工作树 `/path/to/switchback`；保护既有 dirty tree，不 stage/commit/checkout/reset/stash/clean/push/merge。
- Worker 固定 `switchback-workers.slice`、2 vCPU、4 GiB memory、512 PIDs、10 GiB writable layer；Docker `MemorySwap` 必须等于 `Memory`，表示总内存加 swap 上限等于 memory。
- secret populator 固定 `switchback-core.slice`、100000000 NanoCPUs、67108864 Memory/MemorySwap、32 PIDs；只读 root、无网络、无 Docker socket、无 provider secret argv/env。
- 旧容器的 cgroup parent/swap 与目标不一致时拒绝 Provision，不原地修改、重建或删除。Cleanup/Inspect 的已有职责保持；本任务不宣称 Inspect 证明实际资源执行。
- Lima 显式 Web44175/Backend48081 都为 TCP；其后分别为 TCP/UDP 全端口 ignore，`guestIP: 0.0.0.0`、`guestIPMustBeZero: false`，覆盖所有 guest interface。
- 不启动 VM/Engine/core/Worker，不调用 provider/model，不配置 capacity Trust、签名或 ready source；只做源码和本地验证。
- 原 R3 805 文件与物理证据均保留为历史身份；本轮基线 `.scratch/experience-launch-1013/baseline.json`。所有运行证据单独绑定当前 bytes。

## Task 1: 固定 Docker 资源参数与 Lima 规则

**Files:**
- Modify: `internal/workerruntime/adapter/docker/provisioner.go`
- Modify: `internal/workerruntime/adapter/docker/secrets.go`
- Modify: `internal/workerruntime/adapter/docker/provisioner_test.go`
- Create: `internal/workerruntime/adapter/docker/resource_boundary_test.go`
- Modify: `deploy/experience/lima.yaml`
- Modify: `deploy/experience/README.md`

**Interfaces:** 继续使用现有 `Options` / `RuntimeProvisionRequest`；不加入客户端资源字段或公开协议。`Provisioner.createOptions` 输出固定 Worker 参数，`verifyExisting` 验证 cgroup parent 与 MemorySwap，`secretPopulatorOptions` 输出固定 core 参数。

- [ ] **Step 1: 编写生产 Provision 路径回归**：创建时检查实际送入 Engine 的 Worker/secret-populator HostConfig；用已有请求及 inspect fixture，分别把 parent 置空/置其他域、swap置0/-1/大于memory，验证 running/stopped 旧容器均拒绝且不 Start/Create/Remove。当前实现应失败，原始 RED JSON 保存在本任务目录。
- [ ] **Step 2: 运行 focused RED**：`env GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOCACHE=/private/tmp/switchback-control-backpressure-1011.39rHe8 go test -json -p 1 -parallel 1 -count=1 ./internal/workerruntime/adapter/docker -run 'Test.*ResourceBoundary'`，必须实际 FAIL，编译错误不作为行为 RED。
- [ ] **Step 3: 最小实现**：Worker Resources 增加 `CgroupParent: "switchback-workers.slice"`、`MemorySwap: resources.MemoryBytes`；existing verifier 比较这两项。secret populator HostConfig Resources 使用 Global Constraints 的固定值。禁止改动 Task/Run 状态机或存储额度算法。
- [ ] **Step 4: Lima 修正**：保持原显式两个 host loopback 端口；补上 protocol tcp；全端口 ignore 分成 protocol tcp/udp 两条，均包含 `guestIP: 0.0.0.0`、`guestIPMustBeZero: false`。README 更新源码参数已经接线的范围，明确 quota/actual membership/全时段容量证明仍缺失；环境授权已另行发生，但本任务无运行操作。
- [ ] **Step 5: 定向验证**：一次 docker adapter 全包 `go test -json -p 1 -parallel 1 -count=1 ./internal/workerruntime/adapter/docker`；`gofmt -l` 修改Go文件；`PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s test/acceptance/experienceprepare -v`。不重复整个仓库测试，不运行 PG/VM/付费任务。
- [ ] **Step 6: 冻结并独立评审**：报告 `.scratch/experience-launch-1013/task-1-report.md` 记录 RED/GREEN 原始输出、命令、exit、文件SHA及局限；控制器相对 frozen before 生成 diff package，独立 Spec+Quality 评审。无 Git commit，用户授权边界优先于技能的默认提交步骤。

## 后续阶段的准入

本计划只交付资源参数接线，不开放真实执行。下一阶段需要精确评审 Backend 可访问的 quota/cgroup 只读验证边界与 create-before-start 生命周期：不能给 Worker CAP_SYS_ADMIN 或任意宿主路径；不能把 Docker inspect 声明值、静态默认 quota、空 slice 或有限写入测试当实际证明。全时段 Mmax/Delta_inode/exact SRU/host backing UNKNOWN 时，仍停止在无模型阶段。真实报告/页面接受/同任务观测是最终目标；本任务不勾选它们。
