## Context

2026-09-13 用户确认 [不可变模型补充来源设计](../../../docs/superpowers/specs/2026-09-11-immutable-model-observation-supplements-design.md)。本文件是 active change 的当前 v3 设计；旧 v2 设计、实现与物理失败证据保留在 Git 历史和 [运行记录](../../../docs/operations/durable-observability-projection.md)。v2 的本地过程门禁不证明本修订完成。

真实 Docker smoke 已证明：terminal seq 7 后合法 model seq 8 晚到约 4.196 ms，旧 terminal-cut source 冻结为 source_incomplete；Langfuse 恢复不能修改该来源。另有 waiter 按当前 Turn 排除迟到模型、idle 没有模型消费者的接收问题。需要来源契约和单 owner 接收同时修复。

## Goals / Non-Goals

Goals：Task 不等未来 callback/源发布/HTTP；原冻结事实不变；已接受并安全封存的模型可在重启及 registry 清理后独立补投；零模型 basis 有效；逻辑模型单计数；身份/凭据/冲突 fail closed。

Non-Goals：不新增 Task/Run 状态、请求 inventory/watermark、真实 fallback transport、自动 Runtime 恢复、公开 backfill API、历史导出执行、供应商升级、自动全盘 GC、指标日志看板、Tool/MCP 扩展或 YHC。未送达 Backend 的 callback 及缺原 basis proof 的 forced-stop/recovery 不承诺完整投影。

## Decisions

### 独立 immutable fragments

observation.fragment.v1 区分 run_basis、turn_basis 和 model。basis 由权威 Run/Turn、首次冻结 input/report 和 exact manifest/report refs 证明，不要求旧 source 有模型；model 每个逻辑 ObservationID 一份，早晚到共用唯一入口。原 source、Manifest、checkpoint/v2 job 保留，不扩展或重写旧 JSON。Frame 使用固定编码，I/O 原始 JSON 文本完整保留；publication ledger 存独立 provenance，不把变化的 source 集合塞进远端 payload。

### 单一接收 owner 和原子安全 seal

在现有 Hub pending pump 的业务分流 seam 注入传输无关 application acceptor，在向 active/startup/recovery waiter 发布前消费模型，idle 也不依赖 waiter。当前只有 gRPC 实现；未来 fallback 必须复用该应用规则，本片不声称有真实 fallback。不要新建抢读 live.events 的 goroutine。

重验 current runtime/channel authority、exact message/fingerprint 和历史唯一 agent_turn.start 的 Run/Turn/AgentSession 关联；合法 terminal Turn 可接收事实，不获得新执行授权。Apply 事务原子写 accepted outcome、连续 cursor、安全 candidate/receipt；ignored 不因 applied 成为可导出来源。永久 scan failure 只记安全诊断并允许 cursor 前进，不反复堵塞 heartbeat。数据库失败整体回滚。

Run/Turn basis 在首次 evidence-input FreezeCheckpoint 事务中写旁表，绑定 checkpoint/input/report digest；不能拖到含 ObjectStore I/O 之后的 EvidencePrepared。精确重试只确认既有证明，不为旧 checkpoint 自由补签。安全 payload 与 Worker application 的跨上下文事务参数须经 Backend-only 类型/注入式 adapter seam，避免 application import cycle、网络 DTO 暴露或任意 SQL callback。

### 原 scope receipt 和清理

ScanReceipt 绑定原 Session/Runtime generation、原 envelope/checkpoint/report、frame/wire digest 与 policy/compiler revision。先生成不含 receipt 的 frame，再生成固定 wire，最后写 receipt，避免自引用。扫描完整原数据及最终 wire；HTTP 前核验原证明、完全相同 wire，并扫描当前 Backend/Langfuse 凭据。不持久化 token，不以当前 scanner 替代历史 scanner。

Registry 删除须有 exact durable fence 证明；Hub.Revoke 无返回值或当前 cleanup confirmed 布尔值均不能自行充当证明。清理不等 ObjectStore/Langfuse；仅本次有界扫描可持短期原 scanner。forced-stop/recovery 若先清理且没有 basis proof，明确 basis_scan_unavailable；旧 generation 不由新 generation 补签。现有安全 receipts 仍保留。

### Durable publication、jobs 与对象引用

新增独立 candidate、publication、fragment job、Run activation/exclusion、exact historical request ledger，不改变 v2 表语义。成功来源 identity 与 checksum 分离且前者唯一；同内容幂等，不同内容拒绝覆盖。候选/lease 可有可变工作状态，frame/receipt/publication 核心绑定不可变。

Publisher 分页发现、短事务 claim，事务外校验对象和写入，再按未过期 fence 提交 publication；projector 独立发现它。复用现有 localfile 内容寻址及原子无覆盖 Finalize，不发明第二套 ObjectStore。publication 同事务登记 evidence_object_refs。现有 orphan claim 与 publication 没有共同对象锁，不能仅凭普通 SELECT 证明安全；新 observation-fragment kind 的 temporary/finalized 对象均从通用库存中过滤，并在 DeleteCandidate 再次拒绝删除。失败发布残留暂保留；本片不扩展 GC，旧类型竞态与未来 reservation/incarnation 回收协议另议。切换/回滚须停止旧 orphan 执行，不能让旧 binary 删除新 kind。

Claim/finish 使用 PG clock、SKIP LOCKED、递增 fence，lease 不超过既有 5 分钟上限。缺通知、对象写后/PG 提交后崩溃可收敛；父 publication 尚未存在为等待，已证明不可用为 blocked。文件/HTTP 不在事务中；安全 code 不携 payload。

### 固定 v3 compiler 与读回

langfuse.projection.v3 独立 namespace，不改 DurableProjectionSchemaVersion 的 v2 值。Run 根只有 original input/start/scope，Turn 带完整 report，model 只带自身原始完整 I/O/model/route/status/usage/time。逻辑 ID 不含到达时间、epoch、manifest/publication checksum。metadata 可含稳定 frame digest，不含可变 publication/receipt ID。

basis job 只写父级；model job 可以重放相同父 fragments。纯 compiler 由 capture、loader、exporter 共用。继承锁定 Langfuse adapter 的 json-text.v1、metadata 逐值 JSON 文本编码、UTC 毫秒 wire 时间、纳秒来源时间及 safe-integer usage 检查。读回逐精确对象比较，不要求 Run 总集合闭合；旧 batch 晚到只会重写相同内容。

### 激活、切换与 coverage

v3 首次激活单事务记录当时可见 Run 排除集合；旧 Run 即使无 manifest 或仍运行也排除。新 Run 晚提交不能因业务时间早被跳过；重启不重置边界。对适用 Run，所有模型统一补充，不按旧 source marker/passed 过滤。

配置切换停止 v2 discovery/claim/export 并确认旧进程退出；v2 证据只读，新 job 不由 v2 消费。历史仅允许真实 actor、exact Run/initial/target manifest/event digests/version/idempotency 的内部请求，不能自动扩大到未选成员；原证明与 registry 均不存在则拒绝。当前回合不执行历史导出。

Coverage 描述一个明确 accepted source snapshot，不称所有请求均回调。所有聚合固定 projection version，不能把两代历史相加。回滚先停观测，新表与证据保留，旧 binary 默认观测 disabled，不能恢复无约束的 v2 discovery。

## Risks / Trade-offs

- 扫描和同事务安全写入有本地成本：必须有界；不能用无限重试 optional scan 堵执行队头。
- 已导出的模型随后出现 identity conflict：停止新写并记冲突，不谎称可撤回原 HTTP 写入。
- parent basis 的原证明缺失：模型已安全封存也不能伪造父输入/报告；明确区分 pending 与 unavailable。
- 现有物理 producer 尚无完整连续服务区间/故障/清理证据：必须实现采证后再跑最终门禁，not_collected 不能变成通过。
- 当前没有真实 fallback，只有应用传输无关约束；不新增独立传输以扩大本片。

## Migration Plan

按 [2026-09-13 实现计划](../../../docs/superpowers/plans/2026-09-13-immutable-model-observation-supplements.md)执行：纯契约/编译器、安全 ledger、模型接收、basis 原子封存、清理、后台发布投影、v3 装配、确定性集成、真实采证。增量 migration 从当前 000023 后顺延，保留旧 bytes/state；down migration 明确拒绝删除证据。旧数据不隐式补投。

## Open Questions

无待定产品选择。真实 PG、Docker 和锁定 Langfuse 的可用性须在执行时重查；环境缺失只能如实报告，不能勾选最终门禁。实现计划/规格校验通过不代表生产代码或物理验收通过。
