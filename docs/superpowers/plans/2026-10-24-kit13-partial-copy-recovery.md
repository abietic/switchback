# kit13 剩余复制恢复执行计划

> **For agentic workers:** 按用户已批准的 `next-copy-recovery-operation.md` 在当前会话串行执行；没有独立实现子任务，不派发并发 agent。

**Goal:** 保留已归档宿主三对象，补齐 guest 两次 no-replace 归档与四份新副本，随后执行获批的单次 Engine/资格首启。

**Architecture:** 新操作 phase 复用旧 copy primitives、writer validator 和产品实际入口。仅调整部分归档状态的接受条件与复制阶段单命令 SSH 参数；旧 phase、封存输入、kit13 和产品源码保持原样。

**Tech Stack:** Python 3.9、Darwin、Lima 2.2.1、Linux renameat2。

## Global Constraints

- Grant `call_1a18d9ab87614eeba6153af1d0e787db`；VM 必须已经 Running，4 CPU / 10 GiB。
- 宿主三旧对象必须位于既定 archive，固定位置缺席；guest 两旧对象必须仍在 fixed，既定 archive 为空。所有 inode、完整内容、前后 boot 与冻结证明匹配。
- guest 两次归档使用 RENAME_NOREPLACE；新四副本独占创建。复制阶段每个命令仍限 90 秒；关闭 SSH 复用仅作用于相关子进程。
- Engine 原入口一次 120 秒；仅成功后继续原范围的准备及一次 240 秒资格首启。原 300 秒 barrier、权限和门禁保留。
- 任一 failure/unknown 立即停止，不 retry/resume/remote Stop，不启动 VM、不改分区、不调用 provider。

### Task 1: 恢复操作入口与离线边界验证

**Files:** `.scratch/runtime-m1-copy-recovery-execution-1024/{copy_switch.py,guest_copy.py,launch_copy.py,launch_engine.py,operator_offline_check.py}`；`copy_common.py`、`writer_contract.py` 从旧 phase 原字节复用。

**Interfaces:** `validate_host_state(layout,parent,archive,children,trees,stage)`；`validate_guest_state(expected,parent,archive,children,trees,boot)`；`guest_argv(code)`。

- [x] 先加入正例与位置/身份/内容/boot/归档子项漂移拒绝测试，确认缺少新接口时失败。
- [x] 新 main 重新核验现有归档、两更早归档、候选逆绑定、VM、FD 和固定位置缺席。只调用 guest `preflight/archive_old/copy_new`；不创建 archive parents，不重命名宿主旧对象。
- [x] 命令前缀固定为 `/usr/bin/env HOME=/Users/example LIMA_HOME=/Volumes/SwitchbackCore/lima SSH=… limactl --tty=false shell …`，保留原 `_bounded_run` 与子进程清理。
- [x] 运行：`python3 -B .scratch/runtime-m1-copy-recovery-execution-1024/operator_offline_check.py`；应通过边界测试，记录脚本哈希；这一步无 live IO。

### Task 2: 一次实际复制恢复与 Engine

- [x] `launch_copy.py` 独占消费新 claim 并重定向原始回执；执行一次。任何失败不进入下一步。
- [x] 核验 `copy-switch-outcome.json` 为 `all_four_new_pristine_all_five_old_retained`，逐项保存四新副本与五旧对象证明。
- [x] 执行一次 `launch_engine.py`，它 exec 原字节 `deploy/experience/engine/prepare.py actual`。保存原 stdout/stderr、退出码及窗口结果；仅 `engine_ready` 后继续。
- [ ] **未执行：Engine preflight失败，触发失败即停。** Engine 成功后，在原审阅范围内准备镜像、新秘密、新 PG/迁移与有限双能力采集，执行原资格→安装→同进程首启一次 240 秒入口。遇到 failure/unknown 停止整个后续链。

### Task 3: 实际结果与保护验证

- [x] 只分析本次保存的回执；失败后不追加 guest/readback/Stop。
- [x] 验证旧 seal 全覆盖、工作树 baseline、受保护文件、HEAD/index，更新关键路径与 append-only ledger。
- [x] 独占发布 completion/README/phase seal，再只读核验封存覆盖及哈希；报告真实达到的阶段和未通过事项。

不 stage/commit 既有 dirty 工作树，不因传输问题重建候选。

实际结果：复制恢复成功，原Engine实际入口一次在preflight因S→R身份误判失败，触发失败即停；后续准备/资格未执行。2022项本轮证据封存，seal SHA256为 `0cdaecc51e301b4a231ed646d173ee6f7689d2a70c32de2aa6a81fd686bed759`。最小R/S源码修复为下一片待接受设计，不算本轮已完成工作。
