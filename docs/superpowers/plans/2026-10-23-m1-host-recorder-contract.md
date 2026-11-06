# M1 Host recorder contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** 修复真实Host作为共享监督器recorder时缺raw的缺陷，交付经过真实caller回归与独立复审的完整新kit/bundle。

**Architecture:** 只给Host新增raw适配，将事实交给原_wire与有界emit。测试穿过真实Host和真实共享监督器；完整候选沿用原builder，不补丁旧副本。分源码与交付两个可独立拒绝的任务，最后做整片复审。

**Tech Stack:** Python3.9、stdlib unittest/mock、Go1.26.4、现有资格builder/bundle verifier；无新依赖。

## Global Constraints

- 工作树固定 `/path/to/switchback`；基线HEAD `fbebfe754045bdbf1af40871a5018eed33743011`，index SHA256 `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5`。不staging/commit/Git mutation，保护未提交改动。
- 人类授权call_c9b3acfef36e4490b570a4ea82d9540b，只实施设计A和整片离线交付；spec SHA256 `494013faca54ac8c05e2460e1cbf4854e3eea1e6ae0a6b61fe121d7766a94462`。
- phase固定 `.scratch/runtime-m1-host-recorder-contract-1023`。原始源码、失败packet、claims、seals与旧kit/bundle保留，不覆盖。baseline/current-files.json的1980记录和baseline/historical-files.json的110761记录由root最终校验。
- 生产修改限定 `deploy/experience/engine/prepare.py::_Host.raw`；测试限定现有 `deploy/experience/engine/test_prepare.py` 及Go最终候选测试 `qualification_engine_deployment_test.go` 的一行新phase发现路径。不修改 `deploy/experience/switchback-capacity-publisher.service`、`internal/workerruntime/adapter/capacitypublisher/publish_test.go`。
- 不改变 `_bounded_run`、`_wait_leader`、Darwin ABI、fencing/signal/reap、首错优先级或120秒/240秒预算。原资格loader、Trust、raw-v1/v2、quota、PG预留、单次Start、资源/网络、capability与三个生产门禁均不变。
- 不进入guest，不调用VM/Engine/PG/Worker/provider，不生成部署秘密，不执行候选ELF，不重跑已消费的三个本机native fixture。本机0755卷根及现有host/guest固定副本不修改、不改名；副本切换仅写离线清单。
- 所有验证移除SWITCHBACK_TEST_DATABASE_URL。Python用-B/PYTHONDONTWRITEBYTECODE=1；Go使用Go1.26.4，GOTOOLCHAIN=local、GOPROXY/GOSUMDB=off、GOENV/GOWORK=off、GOFLAGS=-mod=readonly、CGO_ENABLED=0、GOCACHE=/private/tmp/switchback-qualification-1022-go-cache，串行共用缓存，无网络下载。
- 只能依真实回执宣称结果。旧actual错误、v1无效harness、v2目标RED与新生产回归分别留证，离线通过不表示部署成功。

---

### Task 1: 补齐Host recorder并锁定真实caller回归

**Files:** Modify `deploy/experience/engine/prepare.py`、`deploy/experience/engine/test_prepare.py`；Test-only `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`（仅新增新phase bundle glob）。Create phase/task-1内的回执与报告。生产文件baseline均在phase/baseline/before，不能改旧phase。

**Interfaces:** 消费现有 `_bounded_run(argv, deadline, record, monotonic=..., input_data=None)`、`_Host.emit(value)`、`_wire(value)`；产出 `_Host.raw(kind, value)`，不改变任何既有签名。Task2消费审阅后冻结的这三个文件与完整当前SourceRoot。

- [x] Step 1：用现有SyntheticProcess/selector/reader测试设施建立真实Host recorder组合。可给已有normal_boundary增加可选真实recorder接线以复用假OS边界；避免复制整段监督器或假实现待验证的方法。加入独立HostRecorderTests，实际调用Host.preflight与共享监督器；测试Host.raw/emit/command和_bounded_run均不替换。仅替换OS/process/native操作，临时私有tiny kit/config允许使用原真实校验和stage写入。确定性clock、FD假值与io branch必须避免截获真实临时文件读取。
- [x] Step 2：先跑新增组合测试并留RED，必须至少一个是exact `'_Host' object has no attribute 'raw'`；无效harness失败单独记录，不接受其他错误为目标RED。

```sh
env PYTHONDONTWRITEBYTECODE=1 /Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B -m unittest discover -s deploy/experience/engine -p test_prepare.py -k HostRecorder -v
```

- [x] Step 3：唯一生产补丁为Host内以下方法，不增加fallback/no-op、吞错或放宽校验：

```python
def raw(self, kind, value):
    self.emit({'kind': kind, 'value': _wire(value)})
```

- [x] Step 4：新增组合GREEN并验证这些行为：真实Host+共享监督器保留raw与command帧并返回原stdout；真实Host.preflight走到VM/config复查和新stage声明；binary原值经_wire保留；无stage仅内存记录、owned stage独占0600持久化；单帧/总量越界、持久化失败、原timeout/truncation/nonzero/unknown exit/cleanup failure仍拒绝，首错和partial事实保留。可复用既有ProcessBoundaryTests故障注入以覆盖真实recorder组合，避免重复成套测试逻辑。
- [x] Step 5：执行一次完整受影响离线验证，保存argv/cwd/明确env/stdout/stderr/exit/SHA；失败后只重跑被修改代码或缺口对应检查。既有Python全套、self-check和资格driver：

```sh
env PYTHONDONTWRITEBYTECODE=1 /Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py' -v
env PYTHONDONTWRITEBYTECODE=1 /Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B deploy/experience/engine/prepare.py self-check
env PYTHONDONTWRITEBYTECODE=1 /Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
```

- [x] Step 6：先在Go最终候选绑定测试已有bundle patterns中仅追加 `".scratch/runtime-m1-host-recorder-contract-1023/task-2/bundle-*/manifest.json",`。该测试自身digest因而变化，旧kit不再匹配；源码阶段的final binding保持expected SKIP，新完整kit/bundle之后才可无SKIP通过。原Go共享loader/部署读取binding的聚焦检查与Linux arm64/amd64测试编译。使用已声明offline Go env。先从现有Task1回执定位相同受影响run表达式，保留最终kit绑定原expected SKIP；Task2才运行新完整实际绑定并要求无SKIP。仅编译Linux测试ELF，不执行：

```sh
go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v
env GOOS=linux GOARCH=arm64 go test -c -o .scratch/runtime-m1-host-recorder-contract-1023/task-1/capacitysnapshot-linux-arm64.test ./internal/workerruntime/adapter/capacitysnapshot
env GOOS=linux GOARCH=amd64 go test -c -o .scratch/runtime-m1-host-recorder-contract-1023/task-1/capacitysnapshot-linux-amd64.test ./internal/workerruntime/adapter/capacitysnapshot
```

- [x] Step 7：自审并写phase/task-1-report.md，完整列出RED/GREEN、最终检查与所有concern。root生成相对本任务dirty baseline的固定diff文件，独立scoped reviewer读取同一brief/report/diff，返回Spec与Quality及C/I/M；修复任何C/I后同reviewer复审。源码gate通过后冻结文件hash，不commit。

### Task 2: 完整新候选、审计与离线副本切换清单

**Files:** 只Create phase/task-2内的产物、helpers、回执和operation-checklist；原builder独占分配 `.scratch/runtime-m1-qualification-delivery-1022/task-4/kit-NNNN`。不再修改生产源码、Task1测试或旧文件。root更新当前进度文档并做全片复审/seal。

**Interfaces:** 消费Task1审阅后source-freeze.json与当前完整源码；消费原 `build_candidate.py build`、`bundle.build_bundle/verify_bundle`；产出完整新manifest/roots、最终Go实际绑定、scoped review材料、精确未来操作清单及纯离线副本切换清单。

- [x] Step 1：核对冻结源码、原builder SHA `993a9a83500f3759f5167806f13576354b1c89e65e20b6f4a1429d4f2e7c7e79`、Go1.26.4和受影响检查回执。phase/task-2可读取已封存 `runtime-m1-darwin-group-convergence-1023/task-2/audit_artifacts.py` 等参考，但新helpers必须保存到本phase，不能修改旧helper或旧输出。
- [x] Step 2：从WT以原direct builder执行一次完整新kit构建，不wrapper/imported-main、不改phase/output、不复用或补丁旧kit；保存实际全部输出和退出码：

```sh
env -i HOME=/Users/example PATH=/Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin:/usr/local/go/bin:/usr/bin:/bin:/usr/sbin:/sbin LC_ALL=C LANG=C PYTHONDONTWRITEBYTECODE=1 TMPDIR=/private/tmp python3 deploy/experience/qualification/build_candidate.py build
```

- [x] Step 3：从新kit构建phase/task-2/bundle-0001，使用现有bundle API并运行canonical verifier。17输入全部可反查新kit源；上下游SHA不形成回写循环。不执行20个compiled candidates。
- [x] Step 4：审计新kit的完整manifest coverage、当前与复制source/templates及build前后roots、Go tool身份、20格式、43份exit0构建/版本/模块/build-info回执、13runtime null字段及四authority false。不可只沿用旧审计JSON；新helper从真实当前候选重算，报告绑定实际path/hash/count。
- [x] Step 5：在上述offline Go env中显式绑定实际新kit与新bundle，运行唯一最终窄纯Go测试，须PASS、无SKIP、runtime reads0：

```sh
go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run '^TestQualificationEngineDeploymentFinalKitBinding$' -count=1 -v
```

- [x] Step 6：写具体新operation-checklist，以原完整kit9清单为范围来源，替换为实际新manifest/roots/seal引用，保留原120/240/300、quota/PG/Trust/Start与所有权限/资源/网络拒绝边界。旧窗口不重放；本轮不执行任何真实动作。
- [x] Step 7：同一清单给出已有固定host/guest旧kit9/bundle1的精确保留与切换步骤：仅在VM已Running，旧四个副本完整manifest匹配且明确新归档目标不存在时，分别同父改名保留旧副本，再独占复制新完整候选并校验pristine；不覆盖、不补丁、不递归删除。新归档名必须固定且与本phase/候选绑定，不能接受用户任意路径或误把已失败stage当新目标。权限保持当前owner/root0700、文件manifest模式；所有真实归档/复制仍需未来具体授权，此处仅离线文档或无副作用检查代码。完整副本切换后原Engine120秒窗口内重新做当前VM/preflight，不把旧Running证据当现在事实。
- [x] Step 8：冻结命令/审计/manifest身份，写phase/task-2-report.md。root生成artifact/document/helpers scoped diff文件；独立reviewer验证Spec/Quality和C/I/M。修C/I统一提交同reviewer复审，不重复已绿suite或构建，除非输入确实变化。

## Final gate（root）

- [x] 两个任务各有Spec compliant及Quality Approved；C/I闭合，记录并处置Minor。
- [x] fresh whole-slice reviewer审阅相对本phase baseline的完整三文件源码/测试变化、测试、完整候选身份与未来副本切换边界；独立报告。
- [x] root核对实际source/manifest/receipt、1980现有工作树记录及110761历史记录；只允许三个生产/测试文件、root当前progress文档/ledger追加和本轮新产物，HEAD/index与保护文件不变。
- [x] 更新当前首跑关键路径文档；发布completion和独占phase-seal。最终报告区分源码修复/离线候选完成与仍未执行的真实部署，真实操作只有具体候选可审阅后才提出授权。
