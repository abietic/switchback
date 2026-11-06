# 本机独立 Linux 体验环境设计

2026-10-11。用户已确认本文，并授权细化执行方案、收敛容量预留和队列结算。安装、下载、VM/服务启动和新盘初始化仍未授权；执行拆分见 [独立 Linux 执行计划](../plans/2026-10-11-isolated-linux-experience-execution.md)。

## 目标和范围

建立不影响旧体验环境的 Linux 执行环境，让每项改动都能通过真实页面验收。首轮只验一个 Worker 任务，不复制第二套完整观测基础设施，也不扩展 Workspace、Memory、Skills 管理或平台自主合入。

选择 Lima 稳定版和 macOS VZ，使用 arm64 Linux guest。Colima 封装更便捷，但增加 Engine 和 context 配置层；UTM 是挂载方案无法安全收敛时的备选。已研究的 Lima v2.2.1 是配置核查基线，执行前仍须锁定实际软件版本和 guest 镜像摘要。[Lima VZ](https://lima-vm.io/docs/config/vmtype/vz/)

## 服务和数据边界

- 新 VM、新 Engine endpoint、新数据盘、新数据库和独立端口；不迁移、修复或清理旧任务及旧数据。
- Backend 和 Worker 在同一新 Engine 内保持 sibling 容器拓扑。只有 Backend 挂 Engine socket；Worker 不获得宿主路径、Docker socket 或基础设施凭证。
- 工作副本及可写状态放在 guest 自有块盘。宿主源码只作为经过摘要核验的输入，不让 Worker 修改主工作树。
- 新页面仅绑定宿主 loopback。Grafana、Langfuse 等现有观测服务优先通过明确的服务通道复用，保留认证和新任务身份；跨 VM 可达性必须实测，不能假定与 Desktop 共享容器网络。连接失败则停止接入，不开放未认证 daemon 或公网入口。

## 磁盘和启动门槛

新 Engine 选择 classic overlay2，把数据目录置于启用 project quota 的 XFS 文件系统；`overlay2` 的容器 size 依赖 XFS 和 `pquota`。Docker 29 新安装的默认 containerd 存储后端不同，因此配置只作用于新 guest。[Docker 配额契约](https://docs.docker.com/reference/cli/docker/container/run/#set-storage-driver-options-per-container---storage-opt)、[存储后端说明](https://docs.docker.com/engine/storage/containerd/)

新盘候选名为 `sbquota`，创建前必须查重。以后单独批准初始化时，只能格式化已证明属于本任务、独占且为空的确切新盘一次；核验 UUID、分区、标签后停止 VM，再改为 `format:false`。后续身份或挂载异常应拒绝应用启动，绝不重格式化兜底。Lima v2.2.1 默认磁盘挂载不带 `pquota`，因此 quota 挂载与 daemon 启动顺序必须另行验证。[确切版本磁盘脚本](https://raw.githubusercontent.com/lima-vm/lima/v2.2.1/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/05-lima-disks.sh)

只有以下证据齐全，才允许进入产品体验：

1. 同一 Engine、同一 XFS pool 的身份、可用空间和 quota enforcement；小额度探针越界写入失败，更大额度控制组可完成相同写量，重启后仍有效。
2. 绑定 Engine 和文件系统身份的容量采样，以及可防并发超卖的持久预留。固定 Worker 仍为 2 vCPU、4 GiB、512 PIDs、10 GiB 写层，整体执行预算不超过已证明容量的 80%；镜像、日志、构建缓存和挂载数据另计，不能把写层配额当整个 Session 存储上限。
3. 不足或未知时保持排队，不创建执行许可、Turn 或 deadline；既有执行的报告、终态、取消和清理仍可收敛。

当前代码只有拒绝启动的安全接线，缺少正向容量采样和预留。安装 VM 本身不会使这项门禁放行；这部分与旧 ordinary 队列的 RuntimeRevision 问题都须在真实任务前补齐。

## 端到端验收

先验存储和容量，再绑定源码摘要及实际镜像 ID 部署新版本。之后使用已授权的一次小型只读任务，按真实页面走完 Assistant 派发、Worker 正式结束、报告预览和下载、刷新后身份一致、同任务观测关联、页面接受结果。任一步失败即保留证据，不把局部测试或健康检查算作整个平台通过；失败也不自动授权第二个付费任务。

## 安装前确认

执行约定必须一次列明 CPU、内存、系统盘和数据盘预算，具体软件及镜像版本，loopback 端口、观测通道，以及只针对新资源的停止和清理范围。宿主资源先重新测量；48 GiB 物理内存和 APFS 空间不是新 Engine 的容量证明。若无法证明与旧服务安全共存，就不启动，也不擅自停止旧服务。
