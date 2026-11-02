# PID 1 修复后的 native 验证候选设计

日期：2026-10-21。状态：待确认离线组装范围。本文件不授权进入 guest 或真实执行。

目标：将已验证的 PID 1 零 starttime 修复装入新的 Linux arm64 部署诊断 ELF，以最少变更复用已验证的 F carrier，准备一次可审阅的真实 native 验证候选。

## 当前依据

- 修复结果 (private evidence retained)的五个源码/测试哈希已重新核实；当前源码与最终离线验证一致。共享 parser 的修复后哈希为 9b1c10cbae79bf81c6c1cbfff6e6451ced342a58792d8e4f85260d0ddb1ca4f6。
- 当前仅查询宿主 Lima 列表：switchback-core Running，aarch64，4 CPU、10 GiB；没有进入 guest。真实窗口仍须重新预检 Running，不能启动 VM。
- 只读 readiness 检查 (private evidence retained)：F 的五份 HISTORY seals 与共 814 项绑定均匹配。无需修改历史校验方式。
- 旧 ELF/build receipt 中的 pool_native.go 仍是修复前哈希 0eb1dde28cd0938441bde8ef1dcdc1f33159b124a3e297093eb4665715cd8b19；当前为修复后哈希。旧 ELF 不包含本次修复，旧构建收据不能作为新源码证明。
- 最近一次普通用户取证已消费；旧真实窗口均不可复用。B3-F 当轮原文、清理与远端终止的未知结论仍保留。

## 路线选择

推荐：复用 F 的 v1 诊断输出契约，构建 cmd/switchback-deployment-diagnostic，采用修复后的 success mapper，并重绑新身份。host/guest 的四步顺序、once、持久化、监督、raw-first 和失败停止逻辑保持。这样直接验证当前源码中的已知修复。

备选：改用 B3-G v2 命令，再增加成功 v1 与失败 v2 的组合 mapper/输出适配。它可提供更细失败事实，但增加当前验证候选的协议适配面。本片暂不接入；已实现的 v2 API 与纯 mapper 保留供后续确有需要时使用。

不会重新实现 carrier，也不会修改 Go API、DTO、预算、Trust、PG、Start、IPC、publisher 或生产门禁。

## 固定新身份

| 项目 | 新值 |
| --- | --- |
| 候选目录 | .scratch/runtime-m1-deployment-pid1-native-candidate-1021 |
| 真实窗口目录 | .scratch/runtime-m1-deployment-pid1-native-window-1021 |
| guest stage | /var/lib/switchback-deployment-diagnostic1021-pid1 |
| unit | switchback-deployment-diagnostic1021-pid1.service |
| cgroup | /system.slice/switchback-deployment-diagnostic1021-pid1.service |
| nonce | a37bf58d-b4df-461d-8055-440a80bd5338 |
| 部署 ELF | 候选目录/artifact/switchback-deployment-diagnostic-linux-arm64 |
| build receipt | 候选目录/build-receipt.json |

当前候选目录和真实窗口目录均不存在。本次组装仅可新建候选目录；固定真实窗口目录必须继续不存在。任何固定路径冲突都停止，不自动换 nonce/路径规避一次性边界。guest 路径的不存在只在获授权后的窗口预检，不为设计另开 guest 调用。

## 离线实施范围

1. 从当前已修复源码构建新的 Linux arm64 v1 部署诊断 ELF；仅构建，不执行。沿用旧构建方式与固定工具链 /usr/local/go/bin/go（当前已验证版本 go1.26.4），GOTOOLCHAIN=local、GOPROXY/GOSUMDB=off、CGO_ENABLED=0；使用专属临时 cache/tmp。
2. 新构建收据记录 exact argv/env、工具版本/哈希、退出码、stdout/stderr、完整参与 Go 输入及 go.mod/go.sum 的前后哈希和集合、ELF 大小/哈希/架构。关键 parser 输入必须是本设计记录的修复后字节；不能复制旧输入列表冒充当前构建。构建成功后 ELF 验证 ELF64 LE / machine 183、≤8 MiB，并设宿主 mode 0400。
3. 新候选复制 F 的八个源文件：host.py、guest.py、output_mapper.py、manager_forensics.py、test_host.py、test_guest.py、test_output_mapper.py、verify.py。manager_forensics.py 保持原字节；output_mapper.py/test_output_mapper.py 采用已验证的 fix-candidate 版本，并仅重绑新 cgroup/unit 和必要夹具路径。
4. host/guest/verify/test 的其他修改仅限 PHASE、WINDOW、stage/unit/cgroup/nonce、ELF hash/size、build receipt path/hash、批准设计/计划/清单及相应测试期望。构建收据改指本候选目录，去掉对旧 PRIOR 构建收据的执行依赖；旧 HISTORY 校验、manager hash 与协议检查保持。不得增加搜索路径、自动回退、可选参数或旧 unit 接管。
5. 保留 switchback.capacity.deployment_diagnostic.v1 成功、deployment_failure.v1 失败和原 output assessment 契约；PID 1 的零值原样保留。固定 Native CLI 仍只有 window；公开的独立 preflight/stage/actual/readback 入口仍不开放。
6. 重新生成 candidate/source-manifest/release、source-final 和保留范围记录，绑定八份最终字节、新 build receipt 与新 ELF。沿用现有 native flags=false 与 authority=none 语义；原 seal 不改写。构建/测试过程中的中间哈希不得写成最终构建证明。
7. 复用完整 host/guest/mapper/verify 故障回归，补新身份、旧 ELF/旧 build/旧 mapper 拒绝、PID1 zero packet 保留、caller zero 与 0→1 漂移拒绝。仅允许宿主临时文件/pipe/自有子进程夹具，禁止构造可进入 guest 的真实 native 执行。
8. 自检完整 ELF→build inputs→source snapshot→candidate→release 绑定和 bootstrap 大小；回归的构造期/输出/once/持久化/超时/cleanup 边界不得降低。同一 Go 源码已完成的相关 suites 不因清单变化重复全量跑；如有新的失败或源码变更则按影响范围复验。
9. 形成独立评审、候选固定清单和下一次单次真实窗口的具体操作清单；不实际执行 host.py window。临时 cache/tmp 清理，最终 ELF 和必要证据保留。

最终 ELF hash/size 与 build/source/candidate/release hashes 必须来自上述真实离线构建及核验结果。本设计不预填未构建产物的虚构哈希；获得真实窗口授权前这些值必须全部具体化。

## 沿用的真实窗口契约（本片不执行）

原 F 的 preflight 30s → stage 45s → actual 45s → readback 30s，在同一进程窗口内完成，总协议上限 150s。预算、once、guard identity 修法、raw-first、cleanup 判读和 stdout writable nonblocking pipe/socket 要求均保持，不允许重试、续跑、独立 readback 或 Stop。

未来单位权限与资源保持 F guest PROPERTIES (private evidence retained)，仅 unit/stage 输出路径改为新身份：root/Group=root、CapabilityBoundingSet=CAP_SYS_ADMIN、NoNewPrivileges=yes、AF_UNIX、RestrictNamespaces=yes、原 system-call filter、无 restart、CPUQuota=20%、MemoryMax=128M、MemorySwapMax=0、TasksMax=64、原 CPU/文件/超时限制。本次不实际授予这些权限。

只在 switchback-core 已 Running 时允许未来窗口；不启动/停止 VM，不调用 Engine、PG、Backend、Worker、publisher 或 provider，不初始化 key/journal，不改变三个生产门禁。

## 判读与验收

- 新 ELF 的实际构建来源包含修复后 parser，旧 ELF 和旧 receipt 在新 binding 中被拒绝；原始 proc 重放与 mapper 的零值语义不能因重绑而退回旧规则。
- 所有变更都能对应新身份、构建绑定或 PID 1 mapper；状态机和权限/资源没有扩张。固定真实窗口不存在，旧候选/窗口/once/ELF/原始读取记录的字节与记录 metadata 保持。
- 获得新的真实窗口授权前，只能声明“离线候选准备完成”。获授权后的 recorded 父 summary、最终 exit 与 worker/control/cleanup 完整性必须联合判读，不能用单个 raw packet 宣称调用成功。
- 成功 packet exit 0 与 namespace denial exit 2 分别记录；后者只证明诊断返回了受限事实，不能宣称完整 namespace 观测或部署资格通过。所有投影仍 diagnostic_only、authority=none、execution_linkage=unproven。
- 即使完整 deployment native 观测通过，也不等于容量事实已发布/被消费，更不等于 Create/quota/Start 或页面闭环已验收。之后按已有容量 publisher 与准入设计推进，不用这个诊断结果直接打开门禁。

本次离线组装确认包含：复制/修改上述候选、构建新的部署 ELF、完成故障回归、绑定及评审。实际进入 guest、写新 stage、创建 root/CAP_SYS_ADMIN unit 和执行 ELF，待具体清单完成后另行取得一次授权。

