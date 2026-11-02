# M1 已完成保存的恢复与检查点时间预算设计候选

状态：待用户审阅。本文件不授予真实恢复、guest 访问、服务操作、凭据读取或模型调用权限。旧实跑授权已消费；所有旧封存保持不可变。

## 目标、当前进度与依据

当前目标仍是先通过环境部署，再完成一条真实任务的“正式报告 → 页面接受 → 同任务观测关联”，最后据此精简。当前尚未通过 Engine/Core 首启，不能把离线 fake 通过当作产品实跑验收。

上一窗口封存为 `da06bb62a859c09fc443b12571d99c8769dc8b3175f09628eac39416a9eaba92`，共133项。guest 四项 noReplace 均成功，连同此前 var/etc/run 共七项已保存；5次 stopped proof通过。宿主两份旧副本已移动并校验；stage移动后的全量回读在共享维护120秒到期时中止。53,840帧全部验核，失败并非审计容量耗尽。本地group清理当时没有证明收敛。

本轮新增普通用户宿主只读证据位于 `.scratch/runtime-m1-checkpoint-budget-design-1029/`：

- `host-location-metadata.json`：八个固定位置两次稳定metadata；三个原位置为空，archive内两副本和9,735-leaf stage的inode与旧记录相符。只证明当前命名空间和根metadata，不代替stage内容验收。
- `host-group-metadata.json`：旧PGID33638当前两次采样均无成员；不改写过去的清理结论，不产生进程控制权限。
- `real-host-scan-benchmark.json`：原 `complete_tree_metadata` 在11个固定副本/archive根上实际读取31,787文件、1,938,901,440 bytes，两份完整结果匹配既有基线；两次分别16.774秒、16.707秒。它不包含独立的retained/current stage及`host_inputs`其他校验，不能声称整个T5只需17秒。

现有fake在 `test_carrier_integration.py` 将 `host_inputs` 替换为静态结果，这是未覆盖真实宿主扫描耗时的直接测试缺口。原 `host_inputs` 同时做manifest/hash校验和两次完整metadata/content采集；旧T5因真实移动而在前后各做一轮，约37秒收到guest保存checkpoint、约105秒才开始stage移动，120秒在后置回读耗尽。

## 路线比较与推荐

| 路线 | 变化 | 取舍 |
| --- | --- | --- |
| A：按已保存状态重新完整认证，维护总120秒内预留清理（推荐） | guest七项、宿主三项仅fresh验收；不重复移动；T5无命名空间修改，仅一轮完整fresh宿主证明；动作100秒＋本地收尾20秒 | 保留原总上限、全部内容校验及同child checkpoint；需真实宿主调用路径验证耗时，不能仅凭17秒采集器结果判定可行 |
| B：维护600秒，动作480秒＋收尾120秒 | 延长独立维护预算；需新增维护专用时钟合同，不能扩大或monkeypatch原Engine的120秒函数 | 余量较大，但改变原预算，并保留较多重复扫描；本候选不自动采用 |
| C：拆成guest保存、宿主保存、guest重验三个独立阶段 | 重建跨阶段证明与continuation | 协议改动最大，当前没有必要承担这项复杂度 |

推荐A。新只读证据说明移动已经完成，当前不需要再为相同保存执行mutation。A以新的fresh证明接纳现有归档，不沿用旧grant或旧运行时布尔状态。若完整调用路径的实测不能满足100秒动作上限及明确余量，A必须报告未达标并阻止进入真实窗口，不能静默改成B或再碰运气。

## A的固定行为

### 已保存状态的接纳

新增独立版本 `quiet_all_preserved_v1`，旧v1/v2、active/owned_pair语义保留。完整使用原1344-node/1187-file/196652178-byte guest内容义务、两个失败journal的成功mutation/parent回执，以及本轮宿主根metadata。

- guest七个非空source均须缺失；对应固定archive目标须存在，API仍双侧缺失。每个原始文件hash、身份、owner/mode/nlink、mount/alias、archive children和父目录必须在新窗口fresh完整证明。维护不Stop、不mkdir、不rename。
- 宿主三source须缺失，固定已有archive恰含两副本和stage；archive及三根与捕获身份绑定。stage仍须逐项对9,735文件旧snapshot重新校验；只有根metadata不能使其通过。
- T0读取并建立本次完整宿主基线；T5重新执行真实宿主完整验证并与本次基线比较，保留原manifest、历史archive、stage、两份完整metadata/content采集、writer/FD/ancestor等检查。T5不修改宿主namespace，因此不执行旧版“保存前证明 → 三次移动 → 保存后再次证明”链。
- 新模式不得伪造 `host_copy_renames_complete`、复用旧continuation、猜补缺失的mtime/ctime或把历史syscall推断当本次证明。新模式内部用显式 `already_preserved` 证明表达接纳，不冒充本次成功rename。

### 时间与收尾

从维护子进程创建前开始，总预算仍为120秒：动作deadline为起点＋100秒，local cleanup deadline为起点＋120秒，均受原7200秒driver硬上限约束。剩余driver时间不足完整120秒时，不启动此维护子进程。

guest clock control、解包、采集、T5宿主检查、ACK/EOF、reload/strict、完整outcome/EOF/exit0均须在100秒动作预算内完成。100秒不超过原Engine时钟函数的120秒上限，可继续使用原有时钟转换；不修改Engine的 `BUDGET_SECONDS`，不把cleanup时间传给任何guest动作或宿主业务检查。

动作结束后，只有原Source对本次new-session child的本地清理可使用剩余20秒；不发guest读回、systemd Stop或任何远程补偿。work completion的时刻与cleanup completion的时刻分别记录。晚到outcome、未完全EOF、identity lost、cleanup失败或组收敛未证均失败，不因预留清理时间而升级为成功。首次失败/unknown仍停止，无重试或续跑。

保持同child一次checkpoint/ACK/EOF、完整journal和独立failure预算。原Engine120、资格240、barrier300、driver7200、权限、容量/PG/Trust/Start、quota、Worker资源、Source/Go/templates和kit25/bundle0002保持。

## 必须补上的验证

1. 保存旧共享120秒失效的实际回执与RED边界回放；新增覆盖真实caller的时间回归，不能只调用`check_deadline`。
2. 完整验收正例不得将 `host_inputs`、`snapshot`、`verify_pair`、`verify_prior_archive`、`complete_tree_metadata` 或原strict validator替换为“直接通过”。外部OS/文件/时钟端口可以隔离，但必须执行真实collector和验证算法；明确区分合成metadata/内容与原始前像。
3. 用本次真实31,787-file/1.94GB规模，另加独立stage义务，测量完整新host checkpoint caller。可在本机对固定已保存目录做只读测量，不进入guest、不访问key/Engine/PG/provider，不移动真实对象。记录每阶段与总耗时，不用逻辑文件数或fake clock冒充wall-clock结果。
4. 完整端口隔离流程须调用实际宿主检查路径，guest外部端口使用已捕获规模；动作100秒内完成且预留至少10秒实测余量，才允许标记offline-timing-ready。必须分别记录真实宿主wall-clock、guest端口隔离回放wall-clock及clock注入值，不能把它表述为真实Linux全链路耗时保证。超时或余量不足则候选不可进入真实窗口，报告瓶颈和B的取舍，不能临时抬高上限。
5. 覆盖99/100/120秒边界、慢T5、迟到outcome、cleanup跨过动作deadline但仍在总预算、总deadline耗尽、EPERM singleton证明与失败、primary/secondary错误身份及一次continuation。超时后环境动作计数必须0。
6. 保留所有完整性保护负例、原始FD/maps/hash、双方对象union及新模式版本隔离；新模式0Stop/0mkdir/0rename、同driver四份新copy与Engine→Native→凭据/images/newPG→资格的gate必须真实接线。
7. 完整候选重绑、独立verifier、零写重建、任务及整片评审、Source/index/旧封存保护、全量封存验核后交付。授权继续关闭；不实际启动窗口。

## 验收层次与剩余风险

本片交付是可审阅的关闭候选和timing-ready证据。之后至少还有环境首启验收，以及一次真实任务的报告/页面接受/同Task观测闭环；当前没有完成这两项的运行证据，也没有模型调用授权。单次部署driver硬上限2小时不是“2小时内一定验收完成”的承诺。

原Source strict仍可能因独立socket FD波动拒绝。必须保留真实drift RED和stable control，明确披露；本片不修改这项Source合同，不宣称消除了它。若后续实跑在该点失败，仍按已批准的失败停止，不能跳过它。

拟请批准的范围仅为A的离线实施与普通宿主固定目录只读测量；不进入guest、不启停服务、不移动真实对象、不读provider/observer key、不调用模型、不授予新capability、不发新的真实grant。具体真实执行待完整候选完成后再最后审批。
