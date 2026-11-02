# 可体验的端到端最小闭环

_2026-10-11；用户已确认范围及书面约定。当前完整端到端体验未通过，本文不是完成证据。_

---

## 🎯 目标与选择

每阶段同时交付实现、生产页面入口和真实验收，不能只以单元测试通过计进度。

选择“最小纵向闭环”：任务跑完、状态可信、报告可看、观测可点开。仅修页面反馈最快，但不能解决 Worker 退出；一次补齐全部管理模块覆盖更广，但会推迟主链路反馈。Workspace、Memory、Skills/MCP 完整管理和平台自举另立阶段。

## 🔍 当前可核验的缺口

- `fbebfe7` 的真实体验：Worker 因 `WorkerControl queue is full` 退出，未提交终态证明；任务没有正式 Artifact，页面仍显示 Running。队列满是直接失败点，不等于完整根因。
- 本轮浏览器断言复现：`0 artifacts` 同时出现 `Loading artifact…`。当前 `ArtifactWorkbench` 把两个 `undefined` 比较为相等，先于空态分支进入 Loading。
- Worker 已有 Grafana/Logs 诊断链接，但导航仍把“可观测性 / Admin”整体标为规划中。Langfuse 客户端目前固定接受 `identity_unavailable`，不能宣称已完成真实 Worker 关联。

旧现场和验收证据原样保留；救回的 `/tmp` 报告不是正式 Artifact，不能用于将旧任务标为交付。

## 📦 实现边界与验收

| 工作项 | 实现边界 | 必须看见的真实结果 |
| --- | --- | --- |
| 无产物/加载 | Workbench 按 exact Artifact 身份区分空态、请求中、失败、内容；切换和取消不沿用旧请求状态 | 0 产物不转圈；有产物可看；失败有明确提示 |
| 控制背压 | 先复现真实 Shim/Control 调用模式，再修复；保持有界队列、ACK/replay、generation/epoch fencing、关键及终态事件保护 | 连续输出不因普通瞬时背压退出；慢/断链可取消且有界；不重复 Prompt、不丢终态 |
| 退出收敛 | Runtime/Task 各 owner 经既有事务、revision、幂等与 Outbox 推进；按用户后续确认，关闭自动恢复仍记录明确退出，但不自动重建或重跑，故障判定不放宽 | 明确退出不无限 Running；断链不被误判为执行失败；允许动作与后端一致 |
| 正式报告 | 复用 Trace/Artifact 上传、登记、权威 review 和权限链，不从页面或救回文件伪造 ArtifactRef | 新任务正式报告可预览/下载，刷新后仍可见，来源可追溯 |
| 观测入口 | 生产 `web/` 复用服务端受控诊断配置；可观测性与未实现 Admin 分开；Langfuse 仅使用经核验的投影身份 | 从页面打开已配置的 Grafana/日志；有真实投影时打开 exact Langfuse trace，无身份/服务故障明确提示且不阻断任务 |

不新增任务状态、不扩大执行权限、不放开 Worker 宿主路径或凭证，不引入 YHC harness，不通过无限队列、吞错、手工改数据库或前端猜测完成绕过协议。

## ✅ 验收与交付规则

先修无产物反馈并直接接体验；随后逐个完成控制背压、退出收敛、正式报告与观测导航。每项先写可复现的失败断言，再修复并运行相应 Web/Go/PG 验证；下一项不能把前一项单测结果当端到端证据。

每次部署使用独立的新候选源码摘要、构建日志和实际镜像 ID，不覆盖 `experience1011` 的旧失败证据。新 PG/镜像/端到端资源先与架构会话协调；新增付费模型调用另行明确授权。

最终必须在真实生产页面完成新任务：Assistant 澄清与确认、Worker 执行、后端终态、正式报告预览/下载、刷新一致、同身份观测查询。未通过则记录 exact 失败步骤，不关闭全链路验收。`G3/G4`、`capture_cancelled` 和保留期验收仍独立跟踪，不因本阶段通过自动关闭。

平台自举后续先以隔离副本的小改动为验收：平台内读取自身源码、产生 patch、跑测试、展示差异、人工接受；不包含自主合入、生产访问或权限改写。

## 🔗 依据

- [生产 Web 契约](../../../openspec/changes/production-web-core-experience/specs/production-web-core-experience/spec.md)
- [本地观测契约](../../../openspec/changes/local-observability-stack/specs/local-observability-stack/spec.md)
- 当前真实体验证据：`.scratch/experience1011.AqQmYs/live-experience-result.json`；本轮失败页面：同目录 `experience-repair-before-1.jpg`
- 架构会话于本轮确认相关源码无占用、资源窗口留给本会话；控制与产物协议边界保持不变

本文是阶段实施约定，不覆盖既有 ADR/OpenSpec；若实现需要变更协议，须先补对应 active change 并确认。
