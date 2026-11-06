# M1 空插件基础目录 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 精确修正 v3 两个空目录，完成56路径回归与完整新候选离线交付。

**Architecture:** 保持 scanner/contract/caller 接口，以两 exact paths 的 empty-only 规则复用现有 descriptor/race 边界；不新增版本或通用插件模型。

**Tech Stack:** Python3.9.6 unittest、现有 syscall mock seams、Go1.26.4离线构建和纯Go候选绑定。

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

## Task 1: Exact empty-directory source and full frozen-packet regression

**Files:** Modify `deploy/experience/engine/contract.py`, `store.py`, `README.md`; tests `test_contract.py`, `test_store.py`, `test_guest.py`。Optional仅 `guest.py` caller必要修正及 `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go` 增加本phase发现。Evidence仅本phase `task-1/` 和 `task-1-report.md`；Root控制spec/plan/ledger。

**Interfaces:** Consumes `_scan_store(..., *, expected_filesystem=None)`, `validate_store_inventory(facts)`, `validate_preflight(facts)` 和冻结56 DTO/raw。Produces同接口严格v3、机械fixture provenance、source/evidence freeze供Task2。

- [x] **Step 1: Mechanical fixture and RED.** 从封存 `engine-host-outcome.json` 校验packet、每guest-frame bytes/hash并重提取56metadata/16leaf；对比封存`synthetic-validator-input.json`机械派生结果。内嵌现有test文件，不加bundle输入；收据标记raw与derived边界。修改生产源码前保存完整56 DTO预期被接受但旧validator拒绝的RED；保留其真实exit/log。

```python
# Fixture仅DTO回归，不代表有全部payload或运行时inventory。
assert len(derived_inventory['entries']) == 56
validate_store_inventory(copy.deepcopy(derived_inventory))
```

- [x] **Step 2: Minimal strict implementation and focused GREEN.** shared exact path→mode常量可用下列形态；仅v3应用。contract要求空分类/nlink2/无任何descendant，scanner在目录names处立即证明空，并保留前后/fd/末尾检查。legacy路径仍拒绝。每个新拒绝给exact path。

```python
EMPTY_PLUGIN_DIRECTORY_MODES = {
    DATA_ROOT + '/plugins/storage': 0o755,
    DATA_ROOT + '/plugins/tmp': 0o700,
}
# schema == 'switchback.engine.store_inventory.v3' 才应用；
# 对应 mode、type(nlink) is int、nlink == 2、empty_directory、无descendant都必需。
```

- [x] **Step 3: Boundary matrix and real caller evidence.** 合法0/1/2、v1/v2拒绝、非空文件/目录/深层、第三路径/ID、wrongowner/mode/nlink/type/FS、bool metadata、duplicate/missingparent/rule/refs、names drift/fstat/pathname/末尾/父identityrace；普通payload decoder不放宽。真实scanner syscall tests与完整56 DTO明确区分，两处真实guest scan/preflight/claim必须覆盖。

```sh
/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v
```

所有Python用PYTHONDONTWRITEBYTECODE=1；Go env `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOCACHE=/private/tmp/switchback-qualification-1022-go-cache GOMAXPROCS=4 CGO_ENABLED=0 GOENV=off GOWORK=off`。新kit前finalbinding可预期SKIP，记录事实，Task2必须新kit真PASS。最终全套只在源码稳定后跑一次；新增修改只补覆盖测试。

- [x] **Step 4: Freeze and scoped review.** source freeze逐文件before/current SHA、证据清单/report带命令/exit/hash/selfreview。Root据before→current生成scopeddiff，fresh独立task reviewer按Spec+Quality评审；C/I闭合后Task2，不改Git。

## Task 2: Complete new kit/bundle and concrete retention specification

**Files:** Read-only Task1源码、原 `deploy/experience/qualification/build_candidate.py`、canonical `deploy/experience/engine/bundle.py`。Create原builder新独占kit（预期`.scratch/runtime-m1-qualification-delivery-1022/task-4/kit-0013`，actual输出为准）、本phase`task-2/bundle-0001`、审计/收据/`task-2-report.md`/`operation-checklist.md`；禁止改源码/builder或patch candidate。

**Interfaces:** Consumes Task1 freeze+review、1076source+7template、原directbuilder/canonicalbundle、kit12四copy及346stage冻结证据。Produces完整新artifact identities/audit、actual新candidate Go binding PASS/noSKIP、未来retention规格与deliveryfreeze。

- [x] **Step 1: Original direct build ONCE.** 先核对工具/source/HEAD/index/protected，独占日志收据，然后原入口完整新建，不import-main/wrapper/输出override/复用旧kit。

```sh
env PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build
```

- [x] **Step 2: Canonical bundle and complete audits.** `build_bundle(new_kit,new_bundle)` 然后 `verify_bundle(new_bundle,new_kit)`；重算所有leaf/path/type/mode/bytes/hash、current/copied/build前后roots、20format/43exit0/17reverseinputs/13null/4false。canonical root必须原keyorder和`json.dumps(records,separators=(',',':'))`无newline；不靠旧kit/freeze假PASS。所有验证收据必须实际exit0并校验paired日志bytes/hash，不只搜PASS字符串。

```python
from bundle import build_bundle, verify_bundle
assert build_bundle(new_kit, new_bundle) == verify_bundle(new_bundle, new_kit)
```

- [x] **Step 3: Actual new final Go binding.** 明确test发现本phase的新bundle与actualnewkit后执行一次最终绑定；输出必须新kit、新manifest/roots、PASS/noSKIP/runtime reads0，实际exit0。

```sh
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run '^TestQualificationEngineDeploymentFinalKitBinding$' -count=1 -v
```

- [x] **Step 4: Complete future operation and delivery freeze.** 只用kit12actualphase冻结证据编写五旧对象归档/四新复制清单。两个旧archive分别`archive-quota-backing-device-1023-kit-0011-bundle-0001`和`archive-volume-quota-backing-device-1023-kit-0012-bundle-0001` host/guest不动；新archive命名`archive-empty-plugin-directory-1023-<actualkit>-bundle-0001`。具体stage346 manifest/identity必须引用`runtime-m1-volume-quota-backing-device-execution-1023/host-stage-preservation.json`，不得live读取。沿用修正FD contract和no-replace、120/240/300、原权限门禁、失败/unknown停止。生成report/完整deliveryfreeze，fresh task review，Root再freshwhole。

## Root integration gate

- [x] Task1/task2 review与fresh most-capable whole-slice review完成，C/I闭合、Minor逐条裁决。
- [x] Root独立56fixture/provenance、artifact/actualbinding、baseline/prefix/protected/oldsealed/HEAD/index保护验证完成。
- [x] 更新currentcriticalpath和append-onlyledger，completion/seal最后独占发布，再只读核对coverage/hash。

**Plan self-review:** A的每项落到source/DTO/scanner/caller、候选/retention或rootreview/preservation；无冲突和新增授权。两task为源码与交付两独立可拒绝门禁。1985dirtybaseline禁止Gitmutation覆盖skill通用commit步骤；完整离线授权足以连续执行，无需重复问执行方式。
