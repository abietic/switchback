# kit18 已安装对象保留与切换实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement the two tasks sequentially, with task review and whole-slice review. Do not commit, stage or change branches in this existing dirty worktree.

**Goal:** 沿用已审阅的 kit18 保留清单和新增只读证据，一次完成下次真实准备所需的离线执行器、拒绝回归与操作清单。

**Architecture:** 保留独立的五个 guest 安装单位，复用旧 copy operator 的固定副本/七处 archive/host stage 校验与 no-replace 复制。新安装保留模块只接受固定 contract；所有现场入口在没有新的 exact grant 时拒绝执行。kit18 和产品源码保持封存。

**Tech Stack:** Python 3.9 stdlib，原受管 bounded supervisor，Darwin renameatx_np(RENAME_EXCL)，Linux renameat2(RENAME_NOREPLACE)。

## Global constraints

- Worktree `/path/to/switchback`，HEAD `fbebfe754045bdbf1af40871a5018eed33743011`，保护所有历史 dirty 路径、旧 seal、Source 与 kit18。
- 设计依据为已审阅的 `.scratch/runtime-m1-containerd-dump-imports-1025/task-2/operation-checklist.md`，身份补充为 `.scratch/runtime-m1-installed-directories-readonly-corrected-1025/ownership-evidence.json`；本轮用户“继续推进”只用于完成下一步离线实现，不产生新的环境授权。
- 本轮禁止 Lima/guest、服务、Engine、PG、VM/provider 调用；不实际读写 host/guest 固定安装路径，不构建镜像/候选、不生成秘密；测试使用内存或本 phase 内私有 fixture。
- 固定候选 kit18 manifest `f36b78c7a8e9688788a736569a43d76a1a7fbffd304ada0cae0ce13b62008673`；bundle manifest `63123fd5ffd1604d6c6fd242e0694899f45b5a1b8f52c9a489aba15a1d91b36e`。
- 保留 `/var/lib/switchback` 原位；安装保留恰为 stage/config/runtime 三个目录和两个 unit 文件。两个 config 文件随 config 目录整体移动，不另移一次。
- 当前四 kit17 副本、七处既有 archive、独立3372叶旧 host stage 和4858叶 consumed stage均有保存绑定；新 archive 独占创建。不得 mv/os.rename fallback、覆盖/删除、chmod/chown 修补、rollback/retry/resume 或失败后现场核查。
- 部署采用既有 owner 冻结模型；保留/复制检查和变更之间持有相关父目录的非阻塞 flock 和 pinned dirfd，明确其只约束协作执行器，不声称排除绕过流程的 root 操作。写入前重新验证 snapshot、boot、manager、进程/FD/mapping 和文件状态。
- 原 Engine 120秒、资格240秒、barrier300秒、资源/权限/PG/Trust/Start 门禁不变。Engine完整成功后才允许新秘密/PG/迁移和资格首启；provider仍排除在下一环境授权之外。
- 新增 copy/preparation/取证子进程采用 child-only SSH 无复用，不改全局配置或共享 master。原 Engine/qualification actual 保持冻结 Source 自带的 sanitized env/Lima transport；该 env 不转发 SSH，因此不宣称两个原actual也强制关闭复用，不为此猴补Source或重建kit。既有明确的“复制阶段”授权边界与本条一致；未来真实清单须写明两类调用的区别。

## Task 1: 五个固定安装单位的保留模块

**Files:** new phase `.scratch/runtime-m1-kit18-preparation-operator-1025/installed_preserve.py`、`test_installed_preserve.py`、`task-1-report.md`。

**Inputs:** Root生成的 `installed-contract.json`；每个单位含 exact source/destination、kind、identity及所有叶bytes/hash/mode/nlink。

**Produces:** `validate_contract(contract)` 和 `preserve(ops, contract, deadline)`；可由后续 guest operator 调用的 `LocalInstalledOperations`。模块 import 无副作用；传入的 ops 在同一排他上下文内完成全量预检、创建四个 archive parent、五次 no-replace rename和逐叶核验。

```python
def preserve(ops, contract, deadline):
    validate_contract(contract)
    with ops.exclusive_owner(contract, deadline):
        before = ops.preflight(contract, deadline)
        moved = []
        for unit in contract['units']:
            ops.check_deadline(deadline)
            ops.preserve_unit(unit, before, deadline)
            moved.append(unit['target'])
        return ops.verify_preserved(contract, before, moved, deadline)
```

- [ ] 先写真实调用 seam 的内存/私有 fixture 测试，覆盖全部五单位/两个config整体保留、目标存在、inode/hash/owner/type/link/mount/boot/writer/coverage漂移、rename不支持或失败、任一步partial后禁止后续动作/rollback。
- [ ] 实施 exact contract/type校验、nofollow/noatime只读快照、同FS父dirfd、非阻塞目录flock、exclusive0700 archive parent、renameat2 NOREPLACE、目录fsync及前后逐叶身份/hash校验。
- [ ] 开发与拒绝回归命令：`python3 -B -m unittest discover -s .scratch/runtime-m1-kit18-preparation-operator-1025 -p 'test_installed_preserve.py' -v`。只可使用 phase 内 fixture 或内存；不得选择真实 LinuxRoot adapter 进入现场。
- [ ] 保存测试原始 stdout/stderr、Source/contract hash和限制说明；任务 spec+quality 评审通过后冻结该模块供 Task2 组合。

## Task 2: 复用 copy operator 并装配下一次完整执行

**Files:** phase内 `copy_common.py`、`copy_switch.py`、`guest_copy.py`、`writer_contract.py`、`launch_copy.py`、`launch_engine.py`、`boot_preparation.py`、`guest_boot_preparation.py`、`operator_offline_check.py`、`test_preparation_operator.py`、`operation-window.md` 和完整验证/评审记录。

**Inputs:** Task1 API；旧 kit17 copy operator；固定 kit18/bundle；旧完整 preservation-inputs 和新安装 contract。

**Produces:** 未获环境授权时零现场调用的完整准备入口、固定状态机和新的具体操作清单；最终真实入口只接受随后用户审阅的 manifest/code/grant 绑定。

```python
def run_preparation(ops, binding):
    ops.require_exact_authorization(binding)
    ops.verify_local_inputs(binding)
    ops.verify_running_vm_and_frozen_ownership(binding)
    ops.preserve_old_objects_and_installation(binding)
    ops.copy_and_verify_new_kit(binding)
    result = ops.run_original_engine_once(binding)
    if result['status'] != 'ready':
        raise RuntimeError('Engine failed/unknown; terminal stop')
    return ops.run_authorized_conditional_boot_once(binding)
```

- [ ] 更新固定 OLD/NEW manifests、SourceRoot、archive名和七旧 archive；所有其余 copy primitive AST 保持或逐项解释必要差异。将安装保留组合进同一 guest变更进程与锁范围；先全集预检再改动，保留完整partial回执。
- [ ] 复用 unchanged Engine actual 与资格入口；入口需要当前新grant、前序stage成功、精确候选/Source/脚本hash，不允许旧 consumed claim、旧failed stage 或本轮 offline scope触发现场调用。
- [ ] 将既有qualification README第4至11步落成有界的显式准备：canonical Engine成功后才添加arch aliases、复制exact source/role binaries、创建新的固定目录和PG空目录、调用原prepare-identity和模板安装、镜像准备及原window actual。prepare-identity已先验Engine部署和必要容量再初始化秘密，可先于镜像构建以获得Native验证后的Engine入口；这个顺序与全部副作用必须写入新真实清单。prepare-identity不能拆成runtime-preflight后再调用，后者会重复消费原claim。
- [ ] 镜像必须使用kit18 source三份现有Dockerfile和两个exact labels，不修改Dockerfile/依赖或注入镜像捷径；PG用Source固定digest。每个外部命令和准备总时长有界，原120/240/300秒不延长；网络构建、秘密、PG目录和新unit明确属于未来环境授权，不在本轮运行。首个失败/unknown阻止后续所有阶段，不补查或Stop。
- [ ] 正例模拟完成全部顺序；逐阶段failed/unknown、未授权、manifest漂移、boot变更、占用、writer、错误unit状态、config双重移动、rollback/continuation尝试均必须停止在首失败处。
- [ ] 运行 `python3 -B -m unittest discover -s .scratch/runtime-m1-kit18-preparation-operator-1025 -p 'test_*.py' -v` 和 phase `operator_offline_check.py`；新组合真实调用点回放，不以编造的成功fixture冒充现场结果。
- [ ] 完成任务和整片独立评审、31534历史记录与工作树最终核对、精确文件coverage封存。操作清单同时写清：环境首启验收与真实Task交付验收、仍未知的构建/服务/容量/Agent风险；不承诺未经证明的完成时间。

## Completion criteria

两项任务和独立评审通过；完整离线执行器可审阅，未改产品或kit18，零现场调用。只有届时完整具体的准备＋Engine＋条件首启清单才向用户申请实际执行；真实provider/Task闭环仍独立验收。
