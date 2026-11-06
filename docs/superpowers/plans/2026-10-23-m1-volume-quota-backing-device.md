# M1 双固定 quota backing-device Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 独立inventory v3严格支持两个已证明的固定quota helper，完整交付新kit/bundle、离线回归和独立评审。

**Architecture:** 保持现有scanner/contract深模块接口，绑定扫描改为v3，v1/v2验收保留。真实packet原始帧机械内嵌现有测试，固定父dirfd的metadata-only分支复用；不扩大普通volume恢复或容量准入边界。

**Tech Stack:** Python3.9.6、现有Linux stat/O_PATH模拟seams、Go1.26.4离线交叉构建、纯Go候选binding。

## Global Constraints

- 固定 WT `/path/to/switchback`；phase `.scratch/runtime-m1-volume-quota-backing-device-1023`；HEAD/index/所有既有dirty paths受保护，不stage、不提交、不切branch。
- 用户授权 `call_98cfe5c2eaec4b1cbf336ead89b69d78`；接受原文和本设计均为实现约束；仅完成源码、离线回归、完整新kit/bundle、独立task和whole-slice复审。
- 不进入guest，不运行Lima/lsof/Engine/API/PG/provider，不生成秘密/journal，不执行编译候选或nativefixture，不读取或操作真实固定副本/stage，不改存储/权限/分区，不重试旧实际窗口。
- 新schema `switchback.engine.store_inventory.v3`、rule `m1-engine-store-v3`；v1与v2的schema/rule/验收语义保留，v2仍只接纳overlay2设备；未知schema/字段、版本混合、其他special和不明来源继续拒绝。
- v3仅支持 `DATA_ROOT/overlay2/backingFsBlockDev` 与 `DATA_ROOT/volumes/backingFsBlockDev`；允许0/1/2节点，每路径至多一次，不按basename、glob或任意子目录放行。
- 每设备独立要求Linux block0600/root:root/nlink1/size0、正inode、非bool精确整数；st_dev/st_rdev/rootfd和同窗口完整XFS major/minor一致；只用固定父dirfd下 `O_PATH|O_NOFOLLOW|O_CLOEXEC` 获取元数据，不设备payload I/O/mknod/unlink/chmod/chown/quotactl。
- two production scans均传同窗口完整filesystem；validate_preflight将v2/v3 backing_filesystem与自身filesystem完整绑定。保留before/fd/pathname/scan末尾复验、原bounds、普通payload完整read/hash/decoder和restore_refs拒绝策略。
- 不改变120/240秒窗口、300秒barrier、CAP_SYS_ADMIN+CAP_SYS_PTRACE、PG/Start/Trust、三个生产门禁或现行共享卷保证。
- `deploy/experience/switchback-capacity-publisher.service` 与 `internal/workerruntime/adapter/capacitypublisher/publish_test.go` 不动。所有旧candidates、seals、raw、真实失败phase和现存归档逐字节保留。
- 每个验证尝试使用独占的新stdout/stderr/receipt路径，保留失败和RED；不得覆盖旧尝试。报告给出实际命令、exit、日志hash、自评和疑虑。

本计划的“本设计”指 `docs/superpowers/specs/2026-10-23-m1-volume-quota-backing-device-design.md`。HEAD固定fbebfe754045bdbf1af40871a5018eed33743011，index二进制SHA固定2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5。Global Constraints逐字继承正式设计。所有相对路径以固定WT为根。

---

### Task 1: v3 source, legacy compatibility and actual packet regression

**Files:**
- Modify: `deploy/experience/engine/store.py`, `contract.py`, `README.md`。
- Test/Modify: `deploy/experience/engine/test_store.py`, `test_contract.py`, `test_guest.py`。
- Optional: `deploy/experience/engine/guest.py`，仅在已有两处绑定需修正时；`internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`，仅增加本phase bundle发现。
- Evidence: phase `task-1/`、`task-1-report.md`；before快照在phase `before/`。

**Interfaces:**
- Consumes: `_scan_store(..., *, expected_filesystem=None)`、`validate_store_inventory(facts)`、`validate_preflight(facts)`、`_filesystem`、`_linux_device_number`和两处真实guest调用。
- Produces: 同签名scanner，unbound→v1、bound→v3；v1/v2/v3闭合validator、机械绑定packet fixture/receipt、离线回归、完整diff/source freeze供Task2。

- [x] **Step 1: Embed mechanically extracted raw frames and save RED.** 原packet `.scratch/runtime-m1-quota-backing-device-entry-execution-1023/engine-host-outcome.json` SHA fe34d055c294b4935787f3d4e7c19b790aeb1b67e6f22cfba9d0581952ceab21；从其 `guest-frame-raw` base64校验bytes/hash后选择五节点stat原帧，机器生成内嵌test_store.py，写phase extraction receipt记录observation序号/raw SHA/selected路径及test fixture SHA。测试从原帧解析metadata，完整filesystem包装单独标为synthetic；不增加fixture源文件或bundle输入。

```python
# 改源码前，完整实际五节点正例经过原scanner必须RED。
inventory = scan_actual_packet_nodes(expected_filesystem=synthetic_xfs)
assert inventory['schema'] == 'switchback.engine.store_inventory.v3'
assert {x['path'] for x in inventory['entries'] if x['kind'] == 'block_device'} == {
    DATA_ROOT + '/overlay2/backingFsBlockDev', DATA_ROOT + '/volumes/backingFsBlockDev'}
```

其中 `scan_actual_packet_nodes` 是现有mock syscall harness内测试helper；不得新建真实block设备。独占保存聚焦RED命令stdout/stderr/exit。

- [x] **Step 2: Implement minimal version and fixed-path seam.** 新增v3常量与两固定relative path；bound scanner条目rule/schema为v3。validator使用明确schema→rule/路径集合关系，v2仍仅overlay；v1顶层不接受backing_filesystem，v2/v3必须有完整filesystem。复用严格元数据分支，禁止basename宽放行；原特殊文件/普通payload校验原样。

```python
# 生产scanner唯一元数据设备分支；具体常量放现有contract边界。
elif (stat.S_ISBLK(before.st_mode) and expected_device is not None
      and relative in ('overlay2/backingFsBlockDev', 'volumes/backingFsBlockDev')):
    # 复用原strict owner/type/nlink/size/device/rdev/rootfd、O_PATH与race校验。
    fd = os.open(path.name, os.O_PATH | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent_fd)
# preflight绑定保留v2并加入v3。
if facts['store']['schema'] in ('switchback.engine.store_inventory.v2',
                              'switchback.engine.store_inventory.v3'):
    _require(facts['store']['backing_filesystem'] == facts['filesystem'],
             'store backing filesystem differs from preflight')
```

- [x] **Step 3: Finish strict GREEN and real caller coverage.** 用同一harness覆盖0/1/2固定设备、第三设备/错误位置、特殊类型、owner/mode/nlink/size、missing/bad rdev、bool/major/minor/跨FS/rootfd、每路径inode/rdev/parent/末尾race；设备只O_PATH不read，普通payload继续全读/hash。覆盖v1拒绝所有block，v2接受overlay且拒绝volumes，v3双路径、重复/缺parent/混rule/schema/unknown fields拒绝。两处真实guest调用保留同窗口完整filesystem与v3结果。

```sh
/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check
```

Python统一PYTHONDONTWRITEBYTECODE=1，记录实际CLT工具身份；源码调试跑具体新case，最终全套一次。Go discovery必要时用 `/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v`；离线env GOTOOLCHAIN=local/GOPROXY=off/GOSUMDB=off/GOFLAGS=-mod=readonly/GOCACHE=/private/tmp/switchback-qualification-1022-go-cache/GOMAXPROCS=4/CGO_ENABLED=0/GOENV=off/GOWORK=off。新kit前final binding预期SKIP仅记录；Task2必须实际新kit PASS/noSKIP。

- [x] **Step 4: Freeze and independent review.** 保存允许源码SHA、完整task evidence list及report；root从before→current生成上下文完整scoped diff，不把历史1983dirty paths当本任务diff。task review Spec+Quality通过且C/I闭合后继续Task2；没有Git提交。

### Task 2: Full new kit/bundle, final binding and future retention specification

**Files:**
- Read-only source: Task1 frozen source、`deploy/experience/qualification/build_candidate.py`和canonical `deploy/experience/engine/bundle.py`。
- Create: 原builder下一独占完整kit（预期 `task-4/kit-0012`，以实际输出为准）；本phase `task-2/bundle-0001`、audits、receipts、`task-2-report.md`与`operation-checklist.md`。
- 不修改生产源码，不patch旧/新候选，不改builder；source问题回Task1 gate。

**Interfaces:**
- Consumes: Task1 source freeze与已通过review；完整1076source+7template输入，原direct builder与canonical bundle；旧kit11/bundle1和最新276file host-stage冻结证据。
- Produces: 新完整kit/bundle的真实manifest/SourceRoot/TemplatesRoot/20format/43receipt/17reverse input audit、实际新kit纯Go binding PASS/noSKIP/runtime reads0、候选专属未来保留切换清单、delivery freeze。

- [x] **Step 1: Original direct build exactly once.** 工具身份、source freeze、HEAD/index及保护文件核验后运行下列原entry；使用独占stdout/stderr/receipt，完整保留43内部构建收据；不import-main/wrapper/复制改写builder/输出覆盖。编译仅产生文件，不执行候选。

```sh
env PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build
```

- [x] **Step 2: Canonical bundle and complete audit.** 新kit生成本phase独占bundle；用可信current `verify_bundle`，从current/copied/build before-after和每leaf path/type/mode/bytes/hash重算source/template roots、1151covered+manifest、20formats（16ELF64+2compatELF32+2MachO64）、43exit0、17reverseinputs、13null、4false。

```python
from bundle import build_bundle, verify_bundle
created = build_bundle(new_kit, new_bundle)
assert created == verify_bundle(new_bundle, new_kit)
assert created['runtime_qualification'] is False
```

不要猜测或预填新SHA；canonical coverage摘要使用原key顺序与`json.dumps(records,separators=(',', ':'))`，无sort_keys/末尾newline。index文件二进制hash与git-ls-files文本hash必须使用明确不同字段名。

- [x] **Step 3: Bind actual new candidate.** 先确认Go测试发现本phase新bundle与实际新kit，最终仅执行一次：

```sh
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run '^TestQualificationEngineDeploymentFinalKitBinding$' -count=1 -v
```

使用Task1同离线env；要求PASS/noSKIP/runtime reads0，不能用旧kit、stub或仅compile代替。

- [x] **Step 4: Concrete future retention and review.** 只用冻结证据写清单，不访问真实对象。当前四固定副本kit0011/bundle0001；manifest分别3c6b11aebefbba65436b44c98668bb5c19ad731e5074a3d38955a05a4c3d2544和cbdef817fc597e53b91a9da0f35d0c6ac5975f32bab53bd94bf169f2713a9ef1。最新276file host-stage证据在`.scratch/runtime-m1-quota-backing-device-entry-execution-1023/host-stage-preservation.json`。未来核验exact root/leaf/grant/claim/boot/binding，五旧对象新固定archive no-replace保留、四新独占复制。已存在archive-quota-backing-device-1023-kit-0011-bundle-0001及其五旧对象原样保留；新archive名称绑定本次真实新kit/bundle，不能复用旧target/stage。沿用已通过FD修正和no-replace协议、原120/240/300秒、失败/unknown即停、不retry/resume/remoteStop；本片无真实动作或执行权。交付report/freeze，独立task review后fresh whole review。

## Root integration gate

- [x] Task1 scoped review和Task2 scoped review通过，fresh most-capable whole-slice review完成，C/I处理、Minor逐条裁决。
- [x] Root独立核对源fixture与原packet字节、所有候选leaf/roots/receipt和实际final binding；完整baseline/prefix/source/protected/oldsealed/HEAD/index保持。
- [x] 更新current critical-path文档与append-only ledger；completion与phase-seal最后独占发布，再核对seal hash与coverage。

**Plan self-review:** 接受原文各项分别落到Task1源码/兼容/frame回归、Task2候选/保留清单、root保护/复审门禁。无版本语义冲突；不新增fixture文件以维持17bundle inputs。现行dirty工作树禁止Git mutation覆盖skill通用commit步骤。用户已授权完整离线实施，任务间自动继续，不再次询问执行方式。
