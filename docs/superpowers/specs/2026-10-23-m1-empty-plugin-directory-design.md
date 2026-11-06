# M1 两个空插件基础目录的最小修正设计

状态：用户已接受 A，完整离线实施。来源是封存的 `empty-plugin-directory-next-design.md`；本文件细化其实施与验收，不新增真实环境授权。

目标：修复 kit12 preflight 对 Docker 两个空基础目录的拒绝，用完整观察集合验证后交付可审阅的新候选。成功标准是严格 v3 接纳、legacy 拒绝保留、scanner/caller 边界回归、完整原 builder 新 kit/bundle 与独立评审；真实 Engine/资格首启及体验闭环仍须新运行证据。

## Global Constraints

- 固定 WT `/path/to/switchback`；phase `.scratch/runtime-m1-empty-plugin-directory-1023`；HEAD/index/所有既有 dirty paths 受保护，不 stage、不提交、不切 branch。
- 用户授权 `call_1d794348fe0249239f09e7b174e3ee9b`，接受 A 的完整离线修复；仅完成源码、离线回归、完整新 kit/bundle、独立 task 和 whole-slice 复审。
- 不进入 guest，不运行 Lima/lsof/Engine/API/PG/provider，不生成秘密/journal，不执行编译候选或 native fixture，不读取或操作真实固定副本/stage，不改存储/权限/分区，不重试或续跑旧 actual。
- 仅在 `switchback.engine.store_inventory.v3` / `m1-engine-store-v3` 接纳精确路径 `DATA_ROOT/plugins/storage`（0755）和 `DATA_ROOT/plugins/tmp`（0700）的空目录；允许 0/1/2 处存在，不强制创建，不新增 v4。v1/v2 验收语义保留。
- 两目录分别要求 root:root、非 bool 精确整数元数据、nlink2、同窗口 XFS/device 和固定父目录身份、O_DIRECTORY/NOFOLLOW 真实 names 为空；完整 coverage、before/fd/pathname/全扫描末尾身份与竞态检查必须保留。
- 任意 descendant、非空内容、第三插件路径、真实 plugin ID、容器/卷恢复状态、错误 owner/mode/nlink/类型/FS 或竞态继续拒绝；现有普通文件完整 read/hash/decoder、bounds、零 restore_refs 和双 quota helper 规则保留；相关拒绝携带精确 offending path。
- 使用冻结 packet 的完整 56 路径派生 DTO 回归；metadata/16 leaf hashes 源于 raw，wrapper/classification 明确为离线派生，不宣称完整 payload/scanner 回放或 runtime authority。真实 scanner syscall 边界测试与该 DTO 回归分开标明。
- 两处生产 scan/preflight/claim 均保留同窗口完整 filesystem 绑定；不改 120/240 秒窗口、300 秒 barrier、CAP_SYS_ADMIN+CAP_SYS_PTRACE、PG/Start/Trust、三个生产门禁或共享卷保证。
- `deploy/experience/switchback-capacity-publisher.service` 与 `internal/workerruntime/adapter/capacitypublisher/publish_test.go` 不动。旧 candidates/seals/raw/失败 phase/现存归档逐字节保留，不升级旧失败证据。
- 每个验证尝试使用独占新 stdout/stderr/receipt，记录实际 argv/env/exit/bytes/hash，保留失败/RED；只跑覆盖改动的迭代测试及最终必要全套，不重复已通过的历史测试或 native fixtures。

## 事实来源和证据边界

冻结来源 `.scratch/runtime-m1-volume-quota-backing-device-execution-1023/engine-host-outcome.json`，SHA256 `247a6add4860b62215545908d757f063d6899a3cd3c8a6be510b394d1801fa1a`；该 phase 的 seal SHA256 `d85edf30a680e184e3e0157e3d26eff93d0111125dedb6bc77eabc2b142e63a2`。325 个原帧含 56 个路径、16 个 regular leaf hashes；已完成完整 walk 的观察仍不是接受的 inventory。完整派生 DTO 位于同 phase `synthetic-validator-input.json`；分类、scan_complete/restoration 等 wrapper 必须继续标为 derived，不能把缺失的完整 bbolt/retained payload 补成真实材料。

两个目录分别 inode67240075/33554569、device64785、root:root、mode040755/040700、nlink2，没有观测后代。完整原始 metadata 和机械提取脚本保留在上个 phase；新片仅消费冻结材料。旧 v3 尚无成功准入，采用用户选择的 v3 最小修正，并以新 exact SourceRoot/manifest 区分实现；不改旧封存。

## 模块边界

`contract.py` 保持 `validate_store_inventory(facts)` / `validate_preflight(facts)` 接口，在 directory 分支增加两条 exact empty-only 规则。可用一个固定 path→mode 常量供 scanner 共享；不做通用插件目录许可框架。nlink 精确等于2、empty classification、directory-v1、bytes0、sha256None 与无任何 descendant 同时成立。缺父/重复/伪 empty/rule 混合仍由完整 DTO 验证拒绝。

`store.py` 保持 `_scan_store(..., *, expected_filesystem=None)`。bound v3 scanner 识别两精确路径并在同一真实 descriptor walk 中证明 expected metadata、空 names、同 FS 和所有原 race 检查。非空固定目录在打开子 payload 前拒绝；单凭名称或 DTO 声明不能跳过 scanner。生产 root:root 固定；uid/gid 测试 seams 不改变生产事实。legacy unbound v1 不获例外。

`guest.py` 的 preflight scan 与 claim 边界再 scan 均必须传本窗口完整 FS；以现有真实调用测试证明，不另写替代编排。仅如实际需要才改 guest.py；优先保持生产 caller 不变。

## 测试与候选交付

在现有 test_contract/test_store 内机械内嵌完整56 DTO/所需 raw metadata，写独立 extraction receipt，使17 bundle input roster不变。先保存旧源码 RED，然后实现 GREEN。覆盖两目录0/1/2、legacy、精确路径/前后缀、类型/mode/owner/nlink/FS、非空/后代/缺父、restore_refs、目录与父身份各阶段竞态，以及生产两处调用和普通文件拒绝边界。真实 scanner 测试使用实际 `_scan_store` 和明确的 synthetic syscall harness，不冒充完整 raw payload 回放。

Task1 source freeze通过独立评审后，原 `build_candidate.py build` direct entry仅运行一次，生成下一独占 kit（预期0013，以输出为准），canonical bundle新建在本 phase `task-2/bundle-0001`。全查 current/copied/build roots、1151 covered+manifest（若格式未变）、1076 source+7 template、20 formats、43 exit0 receipts、17 reverse inputs、13 runtime null、4 false；数量由实际产物核对，不写假 PASS。实际新 final Go binding 必须 PASS/noSKIP/runtime reads0。

未来真实清单只读取冻结证据，精确保留当前 kit12四固定副本和346-file已消费host stage。两个更早 archive 原样保留。新 archive 名绑定实际新 kit/bundle；no-replace保留、四新独占复制，原 fail/unknown即停、不 retry/resume/remoteStop。只准备清单，不执行。

## 完成门禁

Task1和Task2独立评审、fresh最强模型 whole-slice review；Critical/Important闭合，Minor逐项记录和裁决。Root独立 provenance/candidate/binding/保护核验后更新当前关键路径和追加ledger，completion/seal最后发布并只读复核。1985 Git-visible baseline、原ledger前缀、HEAD `fbebfe754045bdbf1af40871a5018eed33743011`、index SHA256 `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5`受保护；历史大集合复用已封存全hash与本次稳定stat连续性，明确区分fresh full hash。
