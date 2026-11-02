# Engine unit 清单输出预算与完整离线交付计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development，串行完成两个任务和每任务独立评审，最后整片独立终审。当前 dirty 工作树不 commit/stage。

**Goal:** 仅提高两条 exact unit 清单命令的 stdout 预算，并交付完整新 kit/bundle 和独立复审；不执行环境。

**Architecture:** `prepare._bounded_run` 用私有完整 argv 白名单选择 stdout 上限，其他流与原 `_OUTPUT` 保持。现有测试文件中的独立测试类通过真实 runner→`_IO.command`→`_unit_catalog` 验证完整列表与拒绝行为。全部 Source 冻结后调用原 builder 一次，再用 canonical bundle 库交付。

**Tech Stack:** Python3.9、Go1.26.4、离线 subprocess/syscall seam、ELF/Mach-O 只读格式审计。

## Global Constraints

- 用户 grant `call_e4446650d7b14f6e9208fb98fa079a71` 接受 `docs/superpowers/specs/2026-10-24-m1-unit-catalog-output-budget-design.md`，仅整片源码与离线交付。
- 仅下列 exact argv 的 stdout 上限从 16384 增为 262144 bytes：`/usr/bin/systemctl list-unit-files --all --type=service --type=socket --no-legend --no-pager --plain`；`/usr/bin/systemctl list-units --all --type=service --type=socket --no-legend --no-pager --plain`。
- 使用私有固定 argv 策略；不提供 CLI/env/stdin/caller 可选 budget，不按 basename、前缀、忽略大小写或规范化参数匹配。参数不同、顺序不同或附加过滤时仍是原 16KiB。
- 所有命令 stderr 仍 16KiB，其他命令 stdout 仍 16KiB。原 `_OUTPUT`、`_text` 和 step stdout/stderr 摘要仍 16KiB；receipt/schema/字段格式不变。
- 原完整 EOF/exit0/timeout/truncation/owned-process 收敛要求保持；两份完整清单、512 名称上限、逐项属性/ExecStart 与 mask/foreign/new-unit 拒绝保持，不过滤或分页/重试。
- raw observation 单帧 1MiB、累计 64MiB，原 Engine120秒、资格240秒、barrier300秒、PID/start/flags/R/S、FD/listener、权限、资源及 PG/Trust/Start 门禁保持。
- 不进入 guest，不运行 Lima/lsof/Engine/PG/provider/native fixtures/候选 binary；不读写现行四副本、消费 stage 或真实 archives。不生成 key/secret/journal，不改权限。不重试旧窗口。
- 只读取 WT 内保存的回执；captured prefix、synthetic overflow、完整 synthetic catalogue 分开标记。真实完整 catalogue 长度仍未知，不能以离线通过声称环境首启成功。
- 可修改 Source：`deploy/experience/engine/{prepare.py,test_prepare.py,README.md}` 和 `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`。Go 仅追加本 phase bundle glob；`test_prepare.py` 新增独立预算回归类，既有隔离 seam 只增加 stderr 输入支持，其余旧测试逻辑保持。
- 不修改 `guest.py`、`contract.py`、`qualification/window.py`、原 builder/bundle 或两处受保护旧改动。不 commit/stage/reset/clean/切分支。旧 seals/失败/unknown 保留。
- 所有操作显式 WT `/path/to/switchback`；phase `.scratch/runtime-m1-unit-catalog-output-budget-1024`，新证据独占创建。
- Python `-B`、`PYTHONDONTWRITEBYTECODE=1`、外层 `TMPDIR=/var/folders/w4/xd_p7c1d0sjfg1s3_jn_l75w0000gn/T/`。Go 固定 `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOMAXPROCS=4 CGO_ENABLED=0 GOENV=off GOWORK=off GOPATH=/Users/example/go GOMODCACHE=/Users/example/go/pkg/mod GOCACHE=/private/tmp/switchback-qualification-1022-go-cache`；不升级依赖，保持原 builder 的内部 ENV。

### Task 1: 精确 stdout 策略与完整调用链回归

**Files:** 修改 Global Constraints 四个 Source 文件；新增本 phase `task-1/`、`task-1-report.md`。预算回归放入现有 `test_prepare.py` 的独立测试类，保持 Engine 固定源码叶白名单，不修改 bundle/readback 的生产接纳集合。

**Interfaces:**
- Consumes: `prepare._bounded_run(argv, deadline, record, monotonic=time.monotonic, input_data=None)`；`_Record(outcome,step,emit)`；`guest._IO.command(self,argv)`；`guest._unit_catalog(io_ops)`；`test_prepare.ProcessBoundaryTests.normal_boundary(...)`。
- Consumes: 旧封存 `.scratch/runtime-m1-process-state-identity-execution-1024/{unit-catalog-prefix.bin,replay_output_bound.py,output-bound-diagnosis.json}`，仅读取；不修改旧脚本。
- Produces: 最终 Source、RED/GREEN 原始回执和 streams、`task-1/source-freeze.json`（files:path/bytes/sha256）、可独立审查报告。

- [x] **Step 1:** 在现有测试文件新增独立测试类，导入实际生产函数、复用现有 fake child/selector/signal seam；给 `normal_boundary` 仅增加 `scenario.get('stderr', b'normal stderr')`。测试构造完整 exact argv，不读取 live 文件或启动真实子进程。测试控制记录器应保留 fixture 所需的 `host-command-result`，生产断言使用实际 `_Record.command` 和 raw 观测，不用源码字符串作 oracle。
- [x] **Step 2:** 修改生产代码前运行新 focused 用例，要求 exact argv 的 16385-byte stdout 因 `fixed command output exceeded bound` 失败并保存有效 RED。另将旧 replay 复制到本 task 适配输出位置，保留 captured16384 control 与 captured+1 synthetic 的 RED/GREEN 收据；不伪造完整实际清单。
- [x] **Step 3:** 在 `prepare.py` 新增私有 exact 策略，采用以下等价实现：

```python
_UNIT_CATALOG_COMMANDS = frozenset((
    ('/usr/bin/systemctl', 'list-unit-files', '--all', '--type=service', '--type=socket', '--no-legend', '--no-pager', '--plain'),
    ('/usr/bin/systemctl', 'list-units', '--all', '--type=service', '--type=socket', '--no-legend', '--no-pager', '--plain'),
))
_UNIT_CATALOG_STDOUT = 262144
# Inside _bounded_run, select once without adding a caller parameter:
stdout_limit = _UNIT_CATALOG_STDOUT if tuple(argv) in _UNIT_CATALOG_COMMANDS else _OUTPUT
# In the existing stdout/stderr branch:
bound = stdout_limit if key.data == 'stdout' else _OUTPUT
if len(target) + len(data) > bound:
    target.extend(data[:max(0, bound - len(target))])
    truncated = True
    raise _Failure('fixed command output exceeded bound', True, None)
```

- [x] **Step 4:** 两个 exact argv 各测试 16385、262144 完整返回与 262145 拒绝；所有 stderr16385 拒绝；普通、basename、近似、重排和加参命令 stdout16385 拒绝。检查原 raw bytes/hash/base64 完整保留而 step 摘要只有16384；同时验证 frame1MiB/累计64MiB仍拒绝，非零/timeout/truncated/cleanup不确定不进入 parser。
- [x] **Step 5:** 通过真实 `_unit_catalog`→`_IO.command`→runner，合成 >16KiB 完整列表验证每项属性读取与512/513、缺mask、坏行、改名daemon ExecStart、new-unit/foreign拒绝。不能 mock `_unit_catalog` 或把过滤后的列表当覆盖；系统调用和时间继续完全隔离。
- [x] **Step 6:** README 简述窄预算及限制，Go discovery 仅追加 `.scratch/runtime-m1-unit-catalog-output-budget-1024/task-2/bundle-*/manifest.json`，保留全部旧 pattern。完整 focused GREEN 后各一次运行下列离线验证，保存实际 exit/stdout/stderr/hash，不覆盖失败收据：

```text
/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v
```

- [x] **Step 7:** 自审、报告、冻结 Source；Root 从本 phase before snapshots 生成实际 task diff，独立 reviewer 评审 spec+quality。修复并覆盖验证后 Root 发布 task1 approval freeze，再进入构建。当前 dirty WT 不提交。

### Task 2: 完整新候选、交付审计与具体切换清单

**Files:** 只创建本 phase `task-2/`、`task-2-report.md` 和原 builder 的下一个独占 kit；不再修改 Source/模板。

**Interfaces:**
- Consumes: `task-1/source-freeze.json`、Root `task-1-freeze.json`、本 phase authorization/baseline；`build_candidate.inputs()` 和固定 ENV；canonical `build_bundle(kit:Path,destination:Path)->dict`、`verify_bundle(bundle:Path,kit:Path,*,check=lambda:None)->dict`。
- Consumes: 旧 phase `.scratch/runtime-m1-process-state-identity-execution-1024/{copy-switch-verification.json,host-stage-preservation.json,create_archive.frames.json,prior-host-archive-after.json}` 保存的四副本/2448叶stage/四历史archives事实。
- Produces: 完整新 kit、`task-2/bundle-0001`、artifact-identities、artifact audit、future-preservation、具体 operation-checklist、delivery-freeze、真实 final binding 回执和报告。

- [x] **Step 1:** 复制旧 task2 只读审计脚本到本新 task2 后逐项适配；验证 Source freeze、builder/bundle/tool identity、HEAD/index 与所有 Source/模板 before。记录原 builder 当前编号清单；只读 WT 路径。预期仍1076 Source/7模板和1152个完整kit叶；使用实际 inventory 完整核对，不修改固定源码叶白名单。
- [x] **Step 2:** 保持固定离线环境，直接调用原入口一次：`/usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build`。从原 stdout 获取新 kit 路径；不猜编号，不导入替换入口，不执行产物。原 builder 内部 ENV 不改，保存全部原始回执。
- [x] **Step 3:** 调用 canonical `build_bundle(actual_kit, phase/task-2/bundle-0001)`，立即 `verify_bundle`。审计每个叶的 exact覆盖、type/mode/bytes/hash、Source 与7模板 before/after/copied、保持原 dict key order 的双 root。
- [x] **Step 4:** 审计全部20格式（16ELF64、2compat ELF32、2MachO64）、43条exit0构建receipt（20build/20build-info/3metadata）、实际Go1.26.4/tool哈希/ENV；17逆向输入+manifest、13runtime null、4authority false，不能用旧套件SKIP当交付验证。
- [x] **Step 5:** 执行真实新绑定 `/usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitysnapshot -run '^TestQualificationEngineDeploymentFinalKitBinding$' -count=1 -v`；实际新kit/bundle/digest/双roots必须出现在 PASS 日志，noSKIP、`runtime reads zero`。将实际exit、streams哈希及验证结果纳入冻结。
- [x] **Step 6:** 仅读旧保存回执，绑定现行kit14四副本与2448叶消费stage的identity/hash/claim/status，保留四个既有archives。新archive命名 `archive-unit-catalog-output-budget-1024-<actual-kit>-bundle-0001`，下一真实copy需全部前提重验、独占/no-replace保留五旧对象，不能使用旧claim/窗口。准备120秒Engine→成功后镜像/新秘密/新PG→240秒资格首启清单，明确失败/unknown即停、不重试/续跑/远程Stop，不启动VM/改分区/provider；这里只记录，不执行。
- [x] **Step 7:** 自审、冻结交付record并报告；Root 独立实际产物和保存事实审计后，独立 Task2 reviewer。完成后交 most-capable 整片终审；所有评审针对本 slice baseline diff，不审历史1991脏路径。

## Root 收尾

- [x] 逐项处理评审 finding/⚠；核实12248旧sealed records、1991 baseline、允许 Source delta、ledger prefix、保护文件、HEAD/index，source/task2 freeze与全部stream回执。
- [x] 最终评审接受后更新关键路径/append-only ledger，独占写 completion/README/phase seal；只读复核全部phase/外部新kit覆盖和hash。此phase真实环境执行次数仍0，新实际窗口只在完整候选可审查后另行确认。
