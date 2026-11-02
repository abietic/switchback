# Engine 模板分类最小修复与完整离线交付计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development；串行完成两任务，每任务独立评审，最后 most-capable 整片终审。保护 dirty 工作树，不 commit/stage。

**Goal:** 保留完整两清单，将未实例化模板作为定义记录，保持所有普通 unit/实际实例的完整属性校验，并交付完整新候选。

**Architecture:** 仅在 `_unit_catalog` 保留 installed/loaded 两个集合并增加精确模板分支；原 show 后所有运行属性判定逐字保留。测试留在现有 test_prepare.py，通过真实 runner/recorder/parser 调用链覆盖来源与拒绝语义。Source 冻结后原 builder 一次构建，再 canonical bundle、完整审计及实际 Go 最终绑定。

**Tech Stack:** Python3.9、Go1.26.4、隔离 syscall seam、文件哈希/ELF/Mach-O只读审计。

## Global Constraints

- 用户 grant `call_edc25dff90614f499feaa126e1e2b57f` 接受 `docs/superpowers/specs/2026-10-24-m1-unit-template-catalog-semantics-design.md` 的A，授权完整源码与离线交付，不授权环境执行。
- 两条完整清单命令及exact argv、256KiB stdout保持。分别保留installed_names与loaded_names；all_names为其union，上限仍512个去重名称；缺mask/坏行/溢出/非零/超时等仍拒绝。
- 只将完整匹配`[^@]+@\.(service|socket)`的名称分类为未实例化模板；不按substring、basename或归一化参数分类。它必须出现在安装清单，且不得出现在完整manager清单；反例立即拒绝，不猜inactive。foo@bar.service的实例始终走原show路径。
- 每个模板写一份独立unit-catalog-template原始观测，记录exact name与两份清单的来源归属。不给它构造MainPID、ActiveState、ExecStart或Listen，不把它追加为伪造inactive的other_units。模板名称仍在总集合、512上限和coverage观测中，不能静默丢弃。
- 其余全部名称仍逐个读取原六项properties并记录unit-catalog-item，按现行Id/ExecStart/daemon name/mask/new unit规则校验。已实例化的改名dockerd/containerd/shim仍识别和拒绝；所有非模板show的exit1、未知、timeout/truncated/cleanup失败保持原拒绝。
- 默认mask和新专用unit保持原精确coverage/独占判定；不创建占位实例、不调用cat/load/start/reload/reset/Stop，不增加外部探测命令或公共override。_unit_catalog仍返回原(masks,others)，不改receipt/schema/Go生产readback协议。
- 原stderr/其他stdout/step摘要16KiB、raw frame1MiB/累计64MiB、Engine120秒/资格240秒/barrier300秒、PID/start/flags/R/S、FD/listener、权限/资源及PG/Trust/Start三门禁保持。
- Source只修改既有 `deploy/experience/engine/{guest.py,test_prepare.py,README.md}` 和 `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`；Go仅追加本phase新bundle发现glob。不新增Engine源叶，不扩大bundle/readback白名单。
- 不进入guest、不运行Lima/lsof/Engine/PG/provider/native fixtures/候选binary；不读写现行四副本、消费stage或真实archives。不生成key/secret/journal、不改权限、不重试旧窗口。仅读取WT保存证据；旧seal/失败/unknown保持。不commit/stage/reset/clean/切分支。
- WT `/path/to/switchback`；phase `.scratch/runtime-m1-unit-template-catalog-semantics-1024`。新证据独占创建，未捕获properties明确synthetic，不能称完整真实properties重放或环境成功。
- Python `-B`、`PYTHONDONTWRITEBYTECODE=1`、外层 `TMPDIR=/var/folders/w4/xd_p7c1d0sjfg1s3_jn_l75w0000gn/T/`。Go固定 `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOMAXPROCS=4 CGO_ENABLED=0 GOENV=off GOWORK=off GOPATH=/Users/example/go GOMODCACHE=/Users/example/go/pkg/mod GOCACHE=/private/tmp/switchback-qualification-1022-go-cache`，不升级依赖，保持原builder内部ENV。

### Task 1: 最小分类源码、完整清单回归与冻结

**Files:** 修改Global Constraints四文件；只新增本phase `task-1/`、`task-1-report.md`，不创建新Source叶。

**Interfaces:**
- Consumes: `guest._unit_catalog(io_ops)->(dict,list)`、`_IO.command(self,argv)`、`_IO.show(self,unit,properties)`、`prepare._Record`、test_prepare既有 `_CatalogRecord/_catalog_run` 和 fake child/selector/signal seam。
- Consumes: `.scratch/runtime-m1-unit-catalog-output-budget-execution-1024/{list-unit-files.stdout.bin,list-units.stdout.bin,engine-guest-frames.json,replay_template_show.py,template-replay.receipt.json}`，只读。真实两清单为19861/21630 bytes、279/194行、union311名称/28模板；只有前4 ordinary properties与模板exit1已捕获。
- Produces: 最终四Source、fixture字节来源核验、原始RED/GREEN/full suite收据及streams、`task-1/source-freeze.json`（files:path/bytes/sha256）和完整报告。

- [x] **Step 1:** 在现有test_prepare.py添加独立模板分类测试。嵌入或构造自包含fixture，不让源码测试依赖.scratch/live目标；对真实literals与保存字节做hash比对并记录provenance。重用实际 `_IO.command`/runner/recorder/parser seam，不复制生产分类表达式作为oracle。完整实际清单含28模板，所有283非模板properties均调用；前4真实，后续明确synthetic。
- [x] **Step 2:** 在修改guest.py前跑focused captured测试获得有效RED，错误须为模板show exit1的实际_Record拒绝，保留synthetic masks-only control。复制旧replay到新task1，改为指向旧保存材料；保留原始RED/GREEN streams和exit，不能修改旧sealed脚本。每次run独占证据名，不覆盖失败。
- [x] **Step 3:** 用以下等价实现替换原两清单合并部分，原properties后判定不改：

```python
catalogs = []
for command in commands:
    current = set()
    raw = io_ops.command(command)
    for line in raw.decode().splitlines():
        fields = line.split()
        _require(fields and re.fullmatch(r'[A-Za-z0-9_.:@\\-]+\.(service|socket)', fields[0]),
                 'complete unit catalog row encoding')
        current.add(fields[0])
    catalogs.append(current)
installed_names, loaded_names = catalogs
names = installed_names | loaded_names
_require(len(names) <= 512 and set(_MASKS) <= names, 'unit catalog coverage/bound')
templates = {name for name in names if re.fullmatch(r'[^@]+@\.(service|socket)', name)}
_require(templates <= installed_names and not templates & loaded_names,
         'unit template catalogue provenance')
masks, others = {}, []
for name in sorted(names):
    if name in templates:
        io_ops.record.raw('unit-catalog-template', {
            'name': name, 'in_installed_catalog': name in installed_names,
            'in_loaded_catalog': name in loaded_names})
        continue
    # Existing value=io_ops.show(...) and ALL subsequent statements unchanged.
```

- [x] **Step 4:** 测试service/socket模板、普通unit、实际实例、模板与多个实例并存、改名dockerd/containerd/shim实例、看似相关daemon模板没有捏造runtime字段；模板误入loaded（包括only-loaded）拒绝。验证全部模板raw与普通properties集合/数量，all_names含模板的512接受/513拒绝；缺mask/坏行及普通show的非零/timeout/truncated/cleanup拒绝保持。既有预算/完整覆盖回归不得弱化。
- [x] **Step 5:** README简述定义与运行实例区别；Go仅追加 `.scratch/runtime-m1-unit-template-catalog-semantics-1024/task-2/bundle-*/manifest.json`。原helpers、四项authority、schema、固定leaf白名单全部保持。运行一次focused GREEN与一次captured replay GREEN，然后以下完整离线验证各一次：

```text
/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v
```

- [x] **Step 6:** 自审、写报告并冻结四Source，原始receipt记录argv/cwd/env/exit/times/streams bytes/hash和guest.py hash。可复制上一片Task1 run_receipt.py后只把source哈希字段改为guest.py。Root核对baseline-relative diff并独立评审spec+quality，修复并覆盖验证后发布Task1 approval freeze才进入Task2。构建前final-binding的SKIP只能明确记为尚未交付，不能当PASS。

### Task 2: 完整新候选、实际绑定及五归档保留清单

**Files:** 只新增本phase `task-2/`、`task-2-report.md` 和原builder下一独占kit；不再修改Source/模板。

**Interfaces:**
- Consumes: `task-1/source-freeze.json`、Root `task-1-freeze.json`、本phaseauthorization/baseline；原 `build_candidate.inputs()`/固定ENV；canonical `build_bundle(kit:Path,destination:Path)->dict`、`verify_bundle(bundle:Path,kit:Path,*,check=lambda:None)->dict`。
- Consumes: `.scratch/runtime-m1-unit-catalog-output-budget-execution-1024/{copy-switch-verification.json,host-stage-preservation.json,create_archive.frames.json,prior-host-archive-after.json,host-effect-02.json}` 保存的kit15四副本、2425叶stage和五既有archives身份。不能读取live目标。
- Produces: 完整actual kit、`task-2/bundle-0001`、artifact-identities/audit/future-preservation/operation-checklist/self-review/delivery-freeze、原始构建与actual Go receipts、报告。

- [x] **Step 1:** 复制上一片 `.scratch/runtime-m1-unit-catalog-output-budget-1024/task-2/` 的交付脚本到本新phase后逐项适配。先验证Source approval freeze、builder/bundle/tool、HEAD/index、1076Source/7模板、原builder现有kit清单。读取canonical真实签名后结构校验；不依赖无注解签名字符串。
- [x] **Step 2:** 固定离线env直接调用一次原入口 `/usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build`。从真实stdout取得下一kit路径，不猜编号、不导入替换入口、不执行产物或重建旧kit。保持原builder内部ENV，保存每次外层命令实际receipt/stdout/stderr。
- [x] **Step 3:** canonical `build_bundle(actual_kit, task-2/bundle-0001)` 后立即verify。全1151 covered+manifest共1152kit叶、bundle17reverse+manifest共18叶，exact coverage/type/mode/bytes/hash；1076Source/7模板current/before/after/copied和原dict key order计算双roots一致。
- [x] **Step 4:** 全量验证20格式（16ELF64/2compat ELF32/2MachO64）、43exit0收据（20build/20build-info/3metadata）、Go1.26.4/tool hash/固定ENV/target/package、17reverse、13runtime null/4authority false。不能以旧测试SKIP替代实际新候选绑定。
- [x] **Step 5:** 运行 `/usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitysnapshot -run '^TestQualificationEngineDeploymentFinalKitBinding$' -count=1 -v`，固定证据名 `final-go-binding-1.{stdout,stderr,receipt.json}`。实际新kit/bundle/digests/双roots出现在日志，PASS/noSKIP/runtime reads zero，exit和streams hash纳入冻结。
- [x] **Step 6:** future-preservation保持原字段接口，绑定现行kit15四份保存身份与2425叶stage（inode141571427，host unknown/guest failed）；五既有archive含最新 `archive-unit-catalog-output-budget-1024-kit-0015-bundle-0001`，原名与内容保持。新archive命名 `archive-unit-template-catalog-semantics-1024-<actual-kit>-bundle-0001`；未来须重新live重验并no-replace保全五旧对象后独占复制。只准备120秒Engine成功后镜像/新秘密/新PG/240秒资格首启的具体清单，300秒barrier/所有门禁保持；失败/unknown即停、不重试/续跑/失败后guest核查/remoteStop，不启动VM/改分区/provider。任何live前提仅是未来动作，不由此片授权。
- [x] **Step 7:** 自审、报告、独占冻结delivery records（candidate identity/original_builder_invocations1/candidate_executions0/production_or_guest_calls0/records+record_count）。保持同次wrapper闭环排除项明确。Root独立核对实际产物/收据/保存事实后独立Task2 review，再交most-capable整片终审。问题修复不触碰旧sealed记录。

## Root 收尾

- [x] 裁决全部review findings/跨任务⚠；核对16079旧sealed records、1993 baseline、四Source差异、保护文件、ledger prefix、HEAD/index及两项freeze，保留全部失败回执。
- [x] 三评审通过后更新critical path/append-only ledger，独占completion/README/phase seal，最后只读核对exact coverage/hash。此片环境执行仍0；新真实窗口只在完整候选可审查后另行确认。
