# M1 Default Bridge Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** 消除默认 bridge 配置与恢复扫描的合同缺口，交付完整、未启用的新版候选及维护载体。

**Architecture:** 保留旧网络分类和 inventory v1/v2/v3；新增有限默认桥分类、format v2 和 bound inventory v4。完整 owner、停止后扫描与正式新部署预检复用同一 Source；旧实例归属依据与新 Source identity 分别绑定。

**Tech Stack:** Python3.9 标准库、现有Go离线构建/纯夹具、LZMA2/base85载体、文件SHA256。

## Global Constraints

- 用户已接受 `docs/superpowers/specs/2026-10-28-m1-default-bridge-store-design.md` 的A，只授权完整离线实现/回归/候选/独立复审/封存。
- 显式工作目录 `/path/to/switchback`；复用既有分支，保护116条tracked dirty baseline。HEAD/index不变，不stage/commit，不修改无关文件。
- 新phase `.scratch/runtime-m1-default-bridge-store-1028`；完成状态只写本phase progress-ledger.md，既有ledger只读。
- 禁止读取、stat或递归扫描 `.scratch/runtime-m1-worker-execution-credential-1026-01` 与 `.scratch/runtime-m1-containerd-dump-imports-1025`。只允许保留已知凭据引用字符串，不读文件内容。
- 不进入guest，不调用Lima/SSH/Engine/PG/provider/真实D-Bus，不操作任何现场stage/四副本/归档，不增加native隔离夹具。旧失败grant不可重用。
- 维护120秒、Engine120秒、资格240秒、barrier300秒、总7200秒；argv<65536B、frame16KiB、event1MiB、audit64MiB、decoder128MiB、expanded<2MiB保持。
- 旧Source helper/DTO语义、完整50属性和typed Get、Stop传播、全部process/FD/maps、六→二→零监听角色、API空业务、PG/Trust/quota/Start和m1_shared_volume_capacity_v1保持。
- Source允许修改仅 store.py、contract.py、test_store.py、test_contract.py，以及bundle.py的两个精确新增测试/fixture叶名；新增 test_default_bridge_store.py 与 testdata/default-bridge-records.json。新增代码/格式按批准设计，旧候选与全部seal原样保留。其余候选绑定需要的生成物只在新路径创建。
- 精确字段、类型、ID/count覆盖和三层IPAM规则以批准设计为准。默认桥新classification为docker_default_bridge_metadata，format为docker-29.9.0-network-bbolt-v2；legacy host/none沿用旧classification/format。
- Root先冻结1100 Source记录、旧保护记录、当前305文件phase和3166文件失败run，提供实际audit2036的原value字节fixture；完整raw DB未保留，合成bbolt必须明确标记。
- 所有实现报告包含精确命令、exit/output、文件列表、红绿证据、冻结摘要及尚未实际证明事项。独立review包从任务前快照生成全上下文diff；不使用仓库全部dirty diff冒充本次改动。
- 完整实际Go绑定发现Source叶名镜像缺口后，允许qualification_engine_deployment_readback.go的allowedEngine仅新增与Python一致的两个固定测试叶名，以及qualification_engine_deployment_test.go的真实Bind回归；不修改任何就绪/Start/Source哈希、成员、required或回执判断。Root使用source-leaf-compat/before快照、独立评审、版本化v2控制文件与完整新kit25/bundle0002收口，kit24全部保留。

---

### Task 1: Source v4、默认桥语义与实际scanner回归

**Files:**
- Modify: `deploy/experience/engine/store.py`
- Modify: `deploy/experience/engine/contract.py`
- Modify: `deploy/experience/engine/test_store.py`
- Modify: `deploy/experience/engine/test_contract.py`
- Modify: `deploy/experience/engine/bundle.py`，仅_ENGINE_LEAVES增加两个新固定test/fixture叶名，_REQUIRED及其他策略不改（完整Source组装所必需）。
- Create: `deploy/experience/engine/test_default_bridge_store.py`
- Create: `deploy/experience/engine/testdata/default-bridge-records.json`
- Evidence: `.scratch/runtime-m1-default-bridge-store-1028/task-1/`

**Interfaces:**
- Consumes: unchanged `_network_metadata(tree)`, `_classify(relative, raw, check, observe)`, `_decode_bbolt`; Root fixture `fixtures/network-tree.json` with original bytes/hash/provenance.
- Produces: `_network_metadata_v2(tree, check=lambda:None) -> bool` (True only for完整默认桥集合，False only for原严格host/none子集；任何其他输入raise ValueError).
- Produces: `_classify_v4(relative, raw, check=lambda:None, observe=lambda *args:None) -> (classification, format)`；非network路径委托原_classify，网络仅解码一次并保存同类raw observation。
- Produces: `_scan_store` bound分支统一v4/rule-v4，unbound分支保持v1/legacy；validator接受新v4并保留旧版本。

- [x] Step1: Copy exact fixture, validate all original leaf byte lengths and SHA256; add RED cases for真实7条、legacy拒绝、所有缺失/额外/类型/ID/endpoint/IPAM错误及实际scanner生成v4。

```python
from store import _network_metadata_v2, _classify_v4
assert _network_metadata_v2(real_decoded_tree) is True
with self.assertRaises(ValueError):
    _network_metadata(real_decoded_tree)
```

RED must fail because新接口或v4尚不存在，不得因fixture无法读取而报红。复用已有独立hand-encoded bbolt builder，不把合成完整DB称作真实raw DB。

- [x] Step2: Implement independent bounded helper and classifier. Default bridge exactly7 records/3 unique IDs and21 driver fields；保留host/none helper原语义；嵌套JSON每字符串<=8192B，closed sets及RFC1918 canonical pool/gateway/PoolID/driver交叉一致，bool/int严格区分。调用check覆盖循环/嵌套解析阶段。

```python
STORE_RULE_REVISION_V4 = 'm1-engine-store-v4'
# Only the bound production scanner selects this version.
classification, format_name = _classify_v4(relative, raw, check, observe)
```

- [x] Step3: Wire v4 through all exact schema dispatch branches. v3/v4共享两固定device与两个空plugin目录政策；v4新classification仅固定network文件和新format，旧v1/v2/v3拒绝新classification。preflight对v4仍强制同一backing_filesystem。保留旧DTO夹具，不把旧v3数据全改成v4掩盖兼容。
- [x] Step4: Exercise actualdecoder→classifier→scanner→DTO→preflight using full explicitly synthetic filesystem/bbolt fixture containing all已遇到设备/空目录/metadata形态。保留最高tx/隐藏record/stat/内容/枚举竞态负例。更新旧scanner输出版本断言，只改新scanner生成值；旧DTO语义断言保持。
- [x] Step5: Run focused RED/GREEN then完整Engine Python suite一次，`python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py' -v`（由Root指定Python3.9绝对路径，禁止bytecode）。保存原输出。自检并写task-1-report.md、freeze.json；Root快照diff交独立Spec/Quality reviewer。没有Git提交。

### Task 2: 完整生命周期、候选重建与载体重绑

**Files:**
- Create: `.scratch/runtime-m1-default-bridge-store-1028/carrier/` (new完整44文件左右的封闭集合，精确数量由绑定导出)
- Create: `.scratch/runtime-m1-default-bridge-store-1028/{build_carrier.py,verify_carrier.py,test_carrier.py,test_lifecycle.py,task3-determinism.py}`
- Create: `.scratch/runtime-m1-default-bridge-store-1028/{README.md,operation-window.md,task-2-report.md}`
- Create: 新完整kit/bundle独占目录；优先使用既有完整kit assembler，编号从已存在候选推导，不覆盖kit23。
- Read-only seed: `.scratch/runtime-m1-owner-exec-array-1028/` canonical/core、carrier、builder/verifier、原回归。
- Read-only assembler: `deploy/experience/qualification/build_candidate.py`，以及 `.scratch/runtime-m1-complete-listener-contract-1027/whole-fix-delivery/{build_offline.py,build_delivery.py}` 的最小公开env/43实际子命令记录模式；`.scratch/runtime-m1-qualification-delivery-1022/task-4/` 的旧kit均保留。

**Interfaces:**
- Consumes: Root批准Task1 freeze及其审核过Source delta；全部其他Source/旧sealed输入必须原样。
- Consumes: unchanged owner sampler、policy、maintenance wire/stop/preserve顺序；新Source `_scan_store` bound返回v4。
- Produces: exact newSourceRoot/templatesRoot、完整kit/bundle manifests、独立build/verify输出及disabled grant；carrier全部Source模块逐字节等于当前Task1 Source和新kit。
- Produces: newfixed OFFLINE=`ROOT/'.scratch/runtime-m1-default-bridge-store-1028/carrier'`、EXECUTION=`ROOT/'.scratch/runtime-m1-default-bridge-store-execution-1028-01/carrier'`。不得创建后者。

- [x] Step1: Root验收Task1后seed新载体/核心与回归，替换新phase/execution/candidate身份。旧active实例的basis/saved facts保留，旧unit/config四文件若相等须独立证明，不把新Source根重写为旧安装来源。历史approval保留为历史，不伪造成当前批准。
- [x] Step2: Implement explicit approved Source revision mapping in newintegrity/binder。每个被授权替换的旧路径绑定before snapshot/hash与after approved hash；其余旧输入依旧严格hash/完整树覆盖，不以通用忽略/删历史记录绕过旧Source hash冲突。Root控制的baseline/Task1 approval是唯一定义delta的来源。
- [x] Step3: Add actual OwnerSampler/maintenance integration fixture for owned_pair→API空业务→containerd_only→quiet，以及正式Source preflight v4；使用实际新scanner与validator，不只stub inventory。OS/manager/API可由有限synthetic adapter替代，夹具明确标注。合法默认桥不跳过API；业务API/foreign process/unknown records/failure时无下一步Stop/move。至少一次走真实maintenance package decode接线和原wire emitter/decoder。
- [x] Step4: Root执行原完整assembler重建新kit/bundle和必要ELF，完成所有真实内容/格式/manifest/Source/模板/编译绑定核验；载体实现者消费Root的ROOT-candidate.json，不重复构建。Root只写task-2构建/审计子目录，载体实现者只写carrier/测试/载体helpers，文件归属不重叠。未改Go语义；新Source digest由完整原流程传播。必要Go纯夹具与Linux arm64/amd64交叉编译不启动任何程序环境。
- [x] Step5: Rebind all actual source/modules/hashes/argv/current helpers/approved evidence。七程序仍有界，dynamic字段标为synthetic且保留call-time cap。独立decoder验证LZMA2 EOF、128MiB decoder、expanded<2MiB、每实际code argv<65536；内存重算确定性要求0改写。正式preflight和维护模块均指向v4，disabled grant/environment/provider false。
- [x] Step6: Run最终相关组合suite和适用cloned回归，精确保存输出/exit，出freeze和delta。完成README/单次未来完整操作清单，明确仍需新真实授权，失败unknown停止且无事后读回。Root生成review package交独立Spec/Quality审阅。

## Root completion gates

- [x] Task1 review同时Spec compliant/Quality Approved；C/I统一fix并复审；Minor明确处理。
- [x] Go固定测试叶名镜像经真实Bind RED/GREEN与独立评审；仅两项生产map新增，无准入逻辑豁免；保存kit24中间态并生成完整新候选。
- [x] Task2 review同时Spec compliant/Quality Approved；审核新Source oldbasis分离、各生命周期、完整候选/manifest/digest/argv等价。
- [x] 独立whole-slice review（gpt-6-astra）覆盖本次全delta，包含Source版本、维护/API/正式预检组合、候选闭包及所有Minor；关闭所有离线材料性问题。
- [x] Root最终核对完整旧封存树、Source仅批准delta、原dirty/HEAD/index、全部七程序和实际candidate binding；不重复已通过且未变的suite。
- [x] 写简洁交付报告、Root最终批准及确定性phase seal，封存后只读复核。最终答复区分已完成离线与未完成真实首启，提供具体新操作清单；当前真实authority仍禁用。

## Plan self-review

批准设计各项分别落在Task1语义/Source/scanner/preflight、Task2生命周期/候选/载体、Root独立整片/保护/封存。唯一新业务行为为批准的默认桥识别；无新的恢复动作或权限。任务前快照取代commit range是当前已有dirty基线/固定HEAD/index约束要求，不能将其他既有改动混入review包。
