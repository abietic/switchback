# M1 kit21 Carrier Rebinding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将现有完整准备执行器绑定到已封存 kit-0021/bundle-0001 和已保存现场事实，形成可审阅但没有执行授权的新 carrier。

**Architecture:** 在新私有 phase 克隆已评审的旧 carrier，仅重绑固定数据、路径、候选和相应一致性断言。产品 Source、旧证据、候选和权限/进程/资源门禁不变；未来真实执行仍须重新证明完整现场前提并取得 exact grant。

**Tech Stack:** 固定 Python 3.9、标准库、现有完整准备/Engine/qualification 组合。

## Global Constraints

- Worktree `/path/to/switchback`；HEAD `fbebfe754045bdbf1af40871a5018eed33743011`，index SHA256 `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5`。不做 Git mutation，不回滚历史 dirty changes。
- 本轮仅离线。`environment_execution_authorized=false`、`provider_calls_authorized=false`；不创建 execution phase、live authorization、claim，不进入 guest，不读 live host stage/VM 状态，不执行 limactl/Engine/PG/provider。
- 不读取、stat 或扫描 `.scratch/runtime-m1-worker-execution-credential-1026-01`。不读取 `.scratch/runtime-m1-containerd-dump-imports-1025` 历史私密 full-env 记录。
- 当前 Source 1093 条、旧不可变输入 5767 条、kit21 和 bundle 均由 `ROOT-baseline.json` 固定；不修改、重建或重新封存旧阶段。
- 新 phase `.scratch/runtime-m1-kit21-carrier-rebind-1026`；未来唯一 execution phase `.scratch/runtime-m1-kit21-carrier-execution-next`。新 archive `archive-kit21-carrier-1026-kit-0021-bundle-0001`；新安装保留前缀 `.preserve-kit21-carrier-1026-kit-0020`；新 guest audit `/var/lib/switchback/kit21-carrier-1026-audit`。
- 保留原 `unlinked_exe_object_identity_v1`、v2 binding/grant 协议结构、全量 fresh quiescence、9 节点 exact inode/path、writer FD/maps 拒绝、no-replace rename、无重试/续跑/补查/远程 Stop。`ownership_guard.py` 必须保持 SHA256 `0d3099391a61c7380e7be56afcaebe6f7740da43d92d3fd0a6c9e361aa4e0e64`。
- 只对现有固定数据及身份重绑；不新增 loaded-unit 例外。已保存观察为 static unit files、loaded 集合空、jobs 空，仅是过去证据，不替代未来完整预检。
- 外部准备总预算 7200 秒、Engine 120 秒、qualification 240 秒、barrier 300 秒；普通输出各 16KiB、仅 Source 两条 exact catalogue stdout 256KiB，六种 guest action 程序 `<65536` 字节；不增加任何预算。
- guest outcome 只被现场 stat，未读内容；`reconstructed-old-guest-outcome.raw.json` 是从完整旧观察流离线重建的期望字节。原始与语义 hash 均在离线固定；未来 guard 必须重新读取并校验 exact 原始 hash 后才可写入或归档，不新增运行时 JSON 解析。
- DeepSeek 只是已选 provider；新模板 host_source=null/credential_preparation_authorized=false。不把旧 grant、旧 reviewer 结论、通用“继续”写成新真实授权。

---

### Task 1: 完整 carrier 固定输入重绑与离线交付

**Files:**
- 新 phase 已由 Root 克隆旧 phase artifact-binding 的 44 个 operative inputs、binding 和旧 guard review，修改仅限新 phase。
- Modify: `preparation_runtime.py`, `copy_common.py`, `copy_switch.py`, `installed_preserve.py`, `guest_copy.py`, `launch_engine.py`, `worker_delivery.py` 中固定 namespace/candidate/身份数据；其他逻辑不变。
- Modify: `installed-contract.json`, `preparation-layout.json`, `preservation-inputs.json`, `conditional-boot-existing-contract.json`, `authorization-template.json`, `operation-window.md`, `operator_offline_check.py` 和测试的相应固定断言。
- Create: `saved-current-engine-stage.json`, `saved-state-binding.json`, `test_kit21_rebind.py`, `task-1-report.md`, `task-1-freeze.json`、有界测试/验证 raw 与命令 metadata。
- Regenerate: `artifact-binding.json`，覆盖所有 operative inputs 和当前 Source1093，canonical binding 不含 authority。

**Interfaces:**
- 原 `run_preparation(ops,binding)`、`validate_authorization(grant,binding,phase,now_ns)`、`installed_preserve.preserve(ops,contract,deadline)` 和 6 个 `guest_program` actions 保持。
- Seed: `.scratch/runtime-m1-empty-manager-jobs-1026`，旧环境执行回执 `.scratch/runtime-m1-empty-manager-jobs-execution-next`。
- Current observation: `.scratch/runtime-m1-engine-residual-readonly-execution-next/installed-nodes-observed.json`、`guest-receipt.json`、两个 reconstructed raw JSON 及 provenance；三个目录、四公开文件、claim/outcome 共9节点。
- Current copies: 旧执行 `four-copy-verification.json` 的 `host_new`/`guest_new` 变为新的 current_host/current_guest；其 host_old/guest_old 与 guest/host archive identity 形成第8个 prior archive。已有7个 archive 和独立3372叶 stage 原样保留。
- Current consumed host stage: 旧执行 `ROOT-local-engine-stage-inventory.json`，device16777230/inode149485766、5002叶。仅转换保存格式，不访问现场路径。

- [x] **Step 1: 固定新旧候选与保存事实，添加拒绝回归。**

当前已安装旧 binding：kit `eb77d938dab0909a00c95529c6eab2397fa47f4003b60cbeca61af96093dd38a` / bundle `4029c440783319ce82ebdae5f7edc255cc04b3d94846139c5248cfef86fc3e6b` / Source `sha256:d115d2758925bd8198a4f83b7ba76159b7febcd9ea23aa6705f84d6d0a588541`。

新候选：kit `.scratch/runtime-m1-qualification-delivery-1022/task-4/kit-0021` / manifest `bbfede8dd573c64863b51c33375c36300905b5d82b17200a50bfd7b9f6ee6220`；bundle `.scratch/runtime-m1-engine-watchdog-lifecycle-1026/bundle-0001` / manifest `b74d80cc48e23e3dcbede94478f18196cf653de12e9ec4909b3cde509e28bbd7`；Source `sha256:98b6a77f3e5f05a7b84da7e766939a088beeca0a783bb74c2626139796c6ea17`。两者 templates 均 `sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`。

测试核心断言：

```python
def test_fixed_new_and_previous_binding(self):
    self.assertEqual(common.OLD, ('eb77d938dab0909a00c95529c6eab2397fa47f4003b60cbeca61af96093dd38a',
                                  '4029c440783319ce82ebdae5f7edc255cc04b3d94846139c5248cfef86fc3e6b'))
    self.assertEqual(common.NEW, ('bbfede8dd573c64863b51c33375c36300905b5d82b17200a50bfd7b9f6ee6220',
                                  'b74d80cc48e23e3dcbede94478f18196cf653de12e9ec4909b3cde509e28bbd7'))
    self.assertEqual(runtime.TOTAL_SECONDS, 7200)
```

新增回归必须实际覆盖：current old kit20 1161-record count 与 historical archives1151 区分；5002叶 current stage 和第8组旧 archive；9节点重绑；derived outcome 改一字节时归档前拒绝；旧 grant/phase/manifest 不接受。可复用旧测试 fake operations；不调用现场适配器。新增准确 RED 原始记录与 metadata，避免仅测实现常量或 fabricated PASS。

- [x] **Step 2: 机械迁移固定数据与 selector。**

所有变更在新 phase。用 `ast.literal_eval`/固定 literal 记录更新模块常量，不运行任何 mutation adapter。嵌入 contract 的唯一更新方式：

```python
tree = ast.parse(raw)
node = next(n for n in tree.body if isinstance(n, ast.Assign)
            and any(isinstance(t, ast.Name) and t.id == '_FIXED' for t in n.targets))
lines = raw.splitlines(keepends=True)
lines[node.lineno - 1:node.end_lineno] = [
    '_FIXED = json.loads(' + repr(json.dumps(contract, sort_keys=True, separators=(',', ':'))) + ')\n'
]
```

contract 9节点全部取自已封存 observation；公开 hash/claim 使用观察值；outcome raw SHA `4b2c355fbca656c7473e9584bbd19dca6245478633bea830713a73970a81430a` 及 semantic hash从已封存重建 raw 计算。snapshot provenance准确标记观察/重建。`preserve()` 和 LocalInstalledOperations 原语 AST不变。

`verify_pair()` 仅将 `digests==OLD` 的 count 从旧1151重绑为当前1161，`NEW`仍1161，其他历史archives仍1151。不从现场 manifest 决定 count。当前stage固定5002/inode149485766；旧 archive 增为8，新增内容来自上一实际窗口成功的保留与四副本回执。数据转换保持每个文件身份/hash/bytes；保留旧独立stage3372和所有其他旧stage。

新 grant ID 前缀 `human-kit21-carrier-rebind-`，ACTIONS中仅 `copy-four-kit20-inputs` 改为 `copy-four-kit21-inputs`。保持 legacy v2 schema字段与严格键/TTL校验。旧 guard SHA不变；旧 guard approval只证明未改的 guard，不可充作新carrier review或执行授权。

- [x] **Step 3: 全套原有回归加新增回归，固定 verifier 和长度验证。**

使用最小替换 env、固定Python、phase内私有 TMPDIR、`-B`。命令：

```sh
/Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B -m unittest discover -s .scratch/runtime-m1-kit21-carrier-rebind-1026 -p 'test_*.py' -v
/Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B .scratch/runtime-m1-kit21-carrier-rebind-1026/operator_offline_check.py
```

保留原175测试方法和已有拒绝覆盖，任何fixture固定值迁移要有明确新旧对照。verifier需核对 Source1093/new candidate1161+17records/20现成ELF与Mach-O header，无构建或运行ELF；六program最大public payload都严格 `<65536`，模块raw/packed hash和AST绑定保持。不要重复 Go/产品 Source suite，产品 Source本轮无改动。

每个实际测试命令的 argv/cwd/最小env/exit/timeout、stdout/stderr完整raw与SHA进入新phase。无SKIP、无真实network/VM/服务调用。元数据只写公共值。

- [x] **Step 4: 自审、冻结和交付。**

`task-1-report.md` 列出变更、准确实际测试结果、最大程序长度、固定输入/derived outcome边界和剩余真实授权/实跑风险；`task-1-freeze.json` 固定 operative inputs、binding、测试与metadata。不得伪造 Root/newreview approval。Root负责固定文件diff、独立任务评审、最强模型完整切片终审和最终seal。

operation-window.md必须是完整具体的新清单：已保存观察不等于fresh预检；当前kit20四副本+5002stage和五安装对象先no-replace保留，再四新副本；原Engine120成功才允许原条件首启准备及qualification240。复用已有私有DeepSeek输入但模板中不放路径/授权，具体新grant由真实人另行批准；不启动VM、不改分区、不调用模型，failed/unknown即停。离线交付不证明实跑或业务闭环。

不提交Git。任务结束后停止修改并报告 `DONE` / `DONE_WITH_CONCERNS`，给出新冻结路径/hash与实际测试数量。


执行结果（2026-10-27）：单次只读补证成功后完成全部四步；最终184测试/verifier通过，任务复审及整体终审C0/I0/M0。只完成离线交付，真实准备和条件首启待最终独立授权。证据：`.scratch/runtime-m1-kit21-carrier-rebind-1026/ROOT-offline-completion.json`。
