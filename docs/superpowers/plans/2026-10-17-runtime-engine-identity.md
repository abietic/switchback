# Runtime Engine Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** 取得当前拟启动 binary/config、quota 类型与有限路径 owner/project 事实，收窄启动容量证明。

**Architecture:** 新 scratch 内离线可测的固定只读包；Root 经审查后 single-use native。复用已封存 guard/transport 的 exact bytes，产品代码无变化。

**Tech Stack:** Python3、ELF64、Linux XFS GET ABI、Lima。

## Global Constraints

完整约束与有限目标见同日 `runtime-engine-identity-design.md`；Task brief 是该设计的具体执行合同。Root 独占 VM/SSH/native/service；子 Agent 只在新 scratch 内离线实现/测试/审查。禁止 Git 写入、旧 role main 重放、产品源码修改、Engine/Worker/Create/Start/provider/付费。capacity_ready=false、execution_enabled=false；10GiB/80% 不变。

## Task 1: 固定只读 collector

**Files:** 新 `.scratch/runtime-engine-identity-1017/collector/{guest.py,host.py,test_identity.py,manifest.json}`、离线 evidence 和 `task-1-report.md`。exact reuse source 可作为 immutable copies，不改旧源。

**Interfaces:** guest pure parser/FD fixture seam 与 fixed main 分离；host `verify_local(expected)` 无副作用，`main` exclusive actual；仅 Root 调用 main。

- [x] 按 `task-1-brief.md` 实现有限 ELF/config/metadata/quota 采集和 carrier，fixture seam 无法由 native argv/env 触达。
- [x] 离线 generated fixtures 覆盖设计边界；为非平凡 parser/拒绝行为保留 RED→GREEN 原始回执。命令 `python3 -B -m unittest -v test_identity`，在 collector 内执行；预期无 failure，平台专属 skip 单列。py_compile 只写 exclusive scratch 目标。
- [x] independent Spec/Quality review exact source diff 与既有证据，不无故重跑相同测试；C/I fresh fixer + covering test + rereview。
- [x] Root read-only source gate 后 single native attempt，保留 raw/original exit；失败不重放。

## Task 2: 结果与累计门禁

**Files:** `native-result.md`、`root-verification.json`、`review-package.json`、`cumulative-review.md`、`handoff.md`、`phase-seal.json`。

- [x] Root 独立核对原始结构/命令 hash/exit/source；区分 metadata observation、matching config、conditional source、applied/plugin/full-capacity。
- [x] fresh 最强 reviewer 做累计审查；修复 C/I 并定向复审。
- [x] 核对 current/frozen862 与三个旧 seal，封存新增证据并明确下一最小启动容量义务。无 Git 写入，无服务 cleanup。
