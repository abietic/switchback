# 显式 Worker 执行配置设计

已获用户 call_648b90b2fd0944ac9ba30e8325224fdd 批准离线实施；不授权key/环境/provider。下文为已接受设计及Source核实细化。


_2026-10-25；仅当前Source调研与设计候选，没有修改产品代码、候选kit、凭据或环境。_

当前 kit18 能验证和启动关闭交付的 Core；它不能直接完成用户要求的真实 Worker/report/页面接受闭环。建议在真实首启前把可执行交付配置一次收齐，再生成一个完整候选，避免环境部署后才发现缺少运行配置。

| 当前源码事实 | 依据 | 影响 |
| --- | --- | --- |
| qualification Backend env 固定 `SWITCHBACK_M1_WORKER_DELIVERY_ENABLED=0`，没有 Worker image 和 provider credential paths | `internal/workerruntime/adapter/capacitysnapshot/qualification_material_linux.go:308` | 资格首启不等于 Worker 业务可执行 |
| 该开关映射 `Config.ExecutionEnabled` | `internal/localproduct/config.go:103` | 不能把环境校验成功当作已打开交付 |
| 只有开关为真才组装 RuntimeRunner；否则 Runtime start policy 为 unavailable | `internal/localproduct/app.go:168` | 当前不会进入业务 Create/quota/Start |
| 开启时 Worker image/network、内部Backend endpoints、Engine/Workspace roots、两个provider文件路径都必须有效 | `internal/localproduct/config.go:160` | 单改一个布尔值不够 |
| credential 在准备具体Runtime时读取；启动scanner也会尝试读取当前文件用于已知秘密过滤 | `internal/localproduct/runtime_environment.go:156`、`internal/localproduct/runtime_runner.go:478` | 凭据准备、权限与脱敏必须一起设计，不能先放假key或事后悄悄覆盖 |

两条路线：

- **先部署 kit18 的关闭交付环境**：可以先取得实际 Engine/原生资格证据，provider 仍不调用；真实 Task 需要后续配置、候选与资格/部署变更。它适合优先验证基础设施，但多一次部署后变更。
- **先补齐可执行交付配置再首启（建议）**：保留默认关闭和全部 PG/Trust/容量/quota/Start 校验，只新增显式选择的完整执行配置；一次绑定实际 Worker image ID、固定provider文件路径与受管secret注入、exact runtime/profile/Backend env和mount证据。先离线完成新的完整候选与测试/评审，再申请同一套具体环境准备与首启。

建议路线的边界：

1. 默认和旧资格输入保持关闭，不根据模型推断、source标签或存在key文件自动启用；新执行配置需要显式服务端选择与exact候选绑定。
2. 开启RuntimeRunner不豁免新鲜签名容量、PG原子预留、exact CID硬quota及单次Start。必须验证无有效容量或权限时仍零Worker Start、零Agent prompt。
3. 固定真实Worker image与已准入网络；provider凭据只由Backend读取/签发受管SecretLease，不把宿主凭据、Docker socket或任意宿主目录交给Worker。密码/key不进入argv、普通env日志、inspect导出、Trace或产物。
4. 凭据只在明确授权后准备；本设计不选择provider、不读取现有key、不生成假key、不默认授权付费调用。真实Task前另行固定provider/profile、单条任务与成本边界。
5. 首启只允许原有空PG/明确新scope，不悄悄导入旧Task/Session或自动重放历史队列；证明没有用户Task/Turn时不会主动发模型请求。
6. 若改变qualification Backend的冻结env/mount/profile，完整更新其intent、Create、同进程manifest等待、验收与最终绑定，不能部署后临时改env或绕过既有资格校验。新的实际候选完成前不授予环境启动权限。

## 建议实施的最小接口

新增私有producer输入 `/var/lib/switchback/qualification-candidate/execution-profile.json`，只允许以下closed DTO：

```json
{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}
```

`mode`只允许`disabled`或`worker_delivery`。文件缺失保持旧`disabled`语义；存在但schema/type/未知字段/重复key/权限或稳定性错误一律拒绝，不退回默认。producer只读固定root-owned0400/single-link/nofollow叶，模型/TaskSpec/客户端不能指定路径或覆盖该配置。

在已验证实际镜像的`material-intent`阶段读一次profile并生成既有不可变intent。`worker_delivery`时Backend env包含`ExecutionEnabled=1`对应开关、实际已验证Worker image ID，以及两个服务端固定credential路径：`/var/lib/switchback/provider-credentials/kimi.key`、`/var/lib/switchback/provider-credentials/deepseek.key`。Backend已有product挂载覆盖这两个路径；Worker仍只获其Session派生目录和受管secret资源，没有整个product root挂载。未选择provider的文件可以缺失，具体Task选择缺失credential时必须在Worker Start/Agent prompt前拒绝，不能生成假key。

后续Create和同进程资格验收继续使用已冻结intent中的完整env，不重新读取profile来改写配置。实际Worker image ID来自已验证Engine image导出链，而不是由profile填写；SourceRoot/模板、image/ELF、Backend角色env/mount/PID、Trust/manifest绑定仍完整校验。私有profile只选择配置，不授予任何Task/Turn或provider调用权限。Assistant继续关闭。

Source接线还必须完成Backend的固定control网络端点：当前`qualificationCoreOptions`仅把Core容器接到`switchback-core1011_core`，而Worker走既有`switchback-m1-worker-control`，目标为`switchback-backend:9090/8080`（`qualification_material_linux.go:309,372`）。新执行模式在Backend Create时同时连接这两个既有固定网络，提供匹配alias，并在Create/资格/最终校验中核对exact网络和端点；PG/Web仍只用Core网络。只使用已有已批准网络，不新增network/host port/host network，不把事后随意connect当作资格证据。该项是完整执行配置的必要接线；用户批准时的原稿另存approved-input，当前为Source核实后的具体化。

选中的真实provider凭据必须在Backend业务启动前，经独立明确授权从一个exact宿主源准备到Backend私有文件，权限和泄漏检查一起完成。这样现有启动scanner可捕获其已知秘密；不把在无key时启动后再临时补key作为默认路线。此设计与本轮均不读取实际credential，也不选择provider或产生付费请求。

## 源码与验证范围

建议只新增producer私有profile解析/mapper和回归，组合进`qualification_material_linux.go`的Backend env生成；补充对应准备器的public profile文件和未来credential准备清单。保留旧disabled组合回归和现行公开资格DTO，不把新profile写成第二套准入/权限聚合。

必须验证：缺失文件的旧行为；closed DTO及root/stability拒绝；`worker_delivery`生成可被真实`localproduct.LoadConfig`接受的完整env；imageID与静态intent绑定；缺失/过期容量、缺失所选credential时零Worker Start/零Agent prompt；无Task/Turn的空PG启动不主动调用模型。最后用原builder生成完整新kit/bundle并独立评审，不对已封存kit18原地修补。

本轮已授权的 kit18 执行器继续完成并评审，它不携带上面新配置。新设计获准后先实施全部Source与离线交付，不读取key、不修改现场、不打开当前门禁；新的真实候选及exact credential源、provider和费用范围完成后再申请实际执行。当前不是可以直接启动的批准书。
