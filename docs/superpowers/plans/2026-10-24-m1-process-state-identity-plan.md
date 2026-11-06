# Engine R/S 最小修复与完整离线交付计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development，串行完成两个任务和每任务独立评审，最后整片独立终审。当前dirty工作树不commit/stage。

**Goal:** 修正身份不变时的R/S误拒绝，交付源码、真实packet回归、完整新kit/bundle与评审证据，不执行环境。

**Architecture:** `_IO.processes` 只增加R/S切换判定，原parser和输出保持。测试在实际生产调用点使用冻结stat/cgroup/cmdline与synthetic系统调用隔离。Task1冻结全部Source后，Task2调用原builder一次并使用canonical bundle库完成完整候选。

**Tech Stack:** Python3.9、Go1.26.4、只读源与manifest审计，离线ELF/Mach-O读取。

## Global Constraints

- 用户grant `call_4ca77de93e9f48f1bcbd92dd589102de` 接受 `docs/superpowers/specs/2026-10-24-m1-process-state-identity-design.md`，仅离线源码与交付。
- 仅允许PID、start tick、flags完全不变时的R/S切换；其他拒绝规则保留。`_parse_stat`、result保留before、PID1/kernel-thread、FD/listener/cgroup/exe/进程表重验和限时不改。
- 原Engine120秒、资格240秒、barrier300秒、双能力和PG/Trust/Start三门禁保持。
- 不进入guest，不运行Lima/lsof/Engine/PG/provider/native fixtures/候选binary；不读或写四现行固定副本、已消费stage和真实archive。不生成key/secret/journal或改权限。
- 旧seal/失败/unknown保持；只读取WT内已保存packet与stage snapshot。不得复用旧claim/窗口。
- 允许修改的源码为四文件：`deploy/experience/engine/{guest.py,test_guest.py,README.md}` 与 `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`。Go仅新增本phase bundle发现路径。
- 不改 `deploy/experience/switchback-capacity-publisher.service`、`internal/workerruntime/adapter/capacitypublisher/publish_test.go`；不commit/stage/reset/clean/切分支。
- 所有实现/验证命令在WT `/path/to/switchback`。证据phase `.scratch/runtime-m1-process-state-identity-1024`，每文件独占写入。
- Python `-B`，`PYTHONDONTWRITEBYTECODE=1`；私有系统 `TMPDIR=/var/folders/w4/xd_p7c1d0sjfg1s3_jn_l75w0000gn/T/`。Go固定 `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOMAXPROCS=4 CGO_ENABLED=0 GOENV=off GOWORK=off GOPATH=/Users/example/go GOMODCACHE=/Users/example/go/pkg/mod GOCACHE=/private/tmp/switchback-qualification-1022-go-cache`，保持离线，不升级依赖。

### Task 1: 最小源码与真实pair回归

**Files:** 修改Global Constraints四文件；新增本phase `task-1/` 证据和 `task-1-report.md`。

**Consumes:** 本phase `task-1/fd-drift-pair.json` 与原封存 `.scratch/runtime-m1-copy-recovery-execution-1024/{fd-drift-pair.json,engine-guest-frames.json,fd-drift-diagnosis.json}`。

**Produces:** 四文件最终Source；fixture provenance；原始命令receipt/stdout/stderr；task-1 source freeze和报告。不可修改旧sealed replay脚本。

- [x] 在 `test_guest.py` 添加真实stat pair回归，通过实际 `_IO.processes` 和syscall隔离调用；原始stat/cgroup/cmdline以literal自包含，测试运行不依赖`.scratch`。记录与frame1918/1931的byte/hash校验。exe/FD隔离部分标为synthetic，不能宣称完整inventory重放。
- [x] 修改源码前运行focused真实pair测试，应因 `process identity drift during FD enumeration` 失败，并保存RED完整回执。
- [x] 在现行整体字典比较处使用等价表达，不加公开API、重试或进程过滤：

```python
stable_identity = all(before[k] == after[k]
                      for k in ('main_pid', 'start_tick', 'flags'))
same_state = before['state'] == after['state']
ordinary_switch = {before['state'], after['state']} <= {'R', 'S'}
_require(stable_identity and (same_state or ordinary_switch),
         'process identity drift during FD enumeration')
```

- [x] 回归通过actual method覆盖R→S/S→R、same-state、任意PID/start/flags变化（包括PF_KTHREAD位）、R/S与Z/X/其他/unknown互转拒绝，及PID1零tick/kernel-thread例外保持。只断言生产调用行为，不复制生产比较式当oracle；不要用assert源码字符串代替行为。
- [x] README记录本次窄边界和回放限制。Go发现列表仅追加 `.scratch/runtime-m1-process-state-identity-1024/task-2/bundle-*/manifest.json`，保留所有旧项。
- [x] 运行focused GREEN，再一次完整离线Engine套件：`/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'`；资格套件：`/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v`；`/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check`。
- [x] Go使用固定离线env：`/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v`。新kit前final-binding可SKIP，明确不能以此当交付PASS。保存每次原始exit和字节/哈希，失败回执不覆盖。
- [x] self-review并写报告，Root生成baseline-relative review package，由独立reviewer评审spec+quality；问题修复并覆盖验证后才冻结Task1并进入Task2。

### Task 2: 完整新候选、真实绑定及保留清单

**Files:** 只新增本phase `task-2/`、`task-2-report.md` 和原builder下一独占 `kit-NNNN`。不再改Source/模板。

**Consumes:** Task1最终Source freeze与已接受设计；原库 `deploy/experience/qualification/build_candidate.py`、`deploy/experience/engine/bundle.py`；本phase authorization/baseline；冻结旧状态 `.scratch/runtime-m1-copy-recovery-execution-1024/{copy-switch-verification.json,host-stage-preservation.json}`。

**Produces:** `artifact-identities.json`、完整audit、`delivery-freeze.json`、原命令receipts、`operation-checklist.md`、新bundle和report。

- [x] 先验证Source/模板冻结，记录当前builder目录清单（仅WT，不能读live副本）。不使用遗漏旧pattern的辅助binding-discovery脚本；本次使用明确的新候选identity和真实Go测试。
- [x] 直接调用原builder一次：`/usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build`；保持固定离线env。原builder自己选择下一新kit；记录actual输出，不能预造编号、导入替换其入口、执行构建产物或重建旧kit。
- [x] 使用canonical `build_bundle` 构建本phase `task-2/bundle-0001`，立即`verify_bundle`。从原builder输出取得kit路径，不猜测。可以复用旧audit代码到新phase并核验路径/语义；不修改旧脚本。先读取库当前签名再写调用。
- [x] 全量审计current/copied before-after Source/7模板与原key order root；1151 covered+manifest、逐叶mode/bytes/hash/type/coverage；20个格式（16 ELF64、2 compat ELF32、2 Mach-O64）；43条exit0内部构建receipt（20build/20build-info/3metadata）、Go identity/固定env；17 reverse inputs+manifest；13 runtime null、4authority false。若实际数量变化，追查所有新源记录并解释，不填固定数掩盖差异。
- [x] 执行真实Go `TestQualificationEngineDeploymentFinalKitBinding`，要求实际新kit/bundle identity及roots出现在PASS日志，no SKIP、`runtime reads zero`；退出码与stdout/stderr哈希写进冻结断言。
- [x] 准备具体未来切换清单，绑定现行四份kit13/bundle及已消费1954文件host stage的冻结identity/hash；三处更早archive及所有失败原样保留。仅离线记录，不读取live目标；新host/guest archive parent及五旧目标必须新独占no-replace，不能续跑旧窗口。
- [x] 自审、冻结全部交付records并写报告；Root独立核对后派独立Task2 reviewer，不重复同源码验证。问题修复后再交most-capable整片终审。

## Root 收尾

- [x] 核实8425旧sealed records、1989 baseline、允许四源码diff、ledger prefix、受保护文件、HEAD/index。Task1/Task2/whole review中的⚠与Minor逐项裁决。
- [x] 整片终审接受后更新关键路径和ledger，发布completion/phase seal并独立只读核对。真实环境执行次数仍0；下一次具体窗口只在候选完成后另行确认。
