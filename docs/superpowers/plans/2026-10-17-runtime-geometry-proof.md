# Runtime Geometry Proof Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task by task. Steps use checkbox syntax.

**Goal:** 有限取得pool当前几何，保持Start默认拒绝。

**Architecture:** 新scratch固定collector，exact复用旧guard/transport；Root单次实机，离线实现/审查。

**Tech Stack:** Python3、Linux aarch64 XFS FSGEOMETRY v5、Lima。

## Global Constraints

完整固定合同见同日runtime-geometry-proof-design.md与task-1-brief.md。唯一候选observability-master-integration-1009/HEAD fbebfe754045bdbf1af40871a5018eed33743011；无Git写入/产品修改/旧native重放/Engine/Worker/Create/Start/provider/产品付费。Root独占VM/SSH/native；agents仅新scratch离线generated fixtures/source/review。保持旧四seal、current/frozen862。固定10GiB/80%；capacity_ready=false、execution_enabled=false、paid_consumed=false。新exclusive actual一次消费。

## Task 1: 固定几何collector

Files: 新.scratch/runtime-geometry-proof-1017/collector/{guest.py,host.py,test_geometry.py,record.py,freeze.py,verify.py,manifest.json,policy.py,transport.py,guard_source.py}及evidence；task-1-report.md。

Interfaces: guest.decode_geometry(raw:bytes)->dict；guest.inspect(...offline fixture seam...)->envelope；host.verify_local(expected_sha)->bound payload；host.prepare(expected_sha)->fixed argv/transport/payload（不dispatch）；host.attempt(...injected transport...)->receipt；host.main仅Root执行。

- [x] 按brief实现pure ABI decoder/strict held FD sampler和fixed guest main；native只固定pool、两次FSGEOMETRY，无通用query/path输入。
- [x] 新generated fixtures验证非平凡拒绝与raw保留，先记录关键RED再实现；命令 /opt/homebrew/bin/python3 -B record.py final-tests-01 /opt/homebrew/bin/python3 -B -m unittest -v test_geometry；原始stdout/stderr/exit与源码snapshot保留，不运行旧suite。
- [x] freeze独占manifest；verify.py仅调用verify_local/prepare，输出current/frozen862及bootstrap尺寸，无native；记录原回执。
- [x] fresh reviewer审查exact源码diff+source/evidence；C/I交fresh fixer+covering test+rereview，无豁免。
- [x] Root本地source gate后single native，原始raw/exit/termination保留；失败不重放。

## Task 2: 条件推导和累计门禁

Files: native-result.md、root-native-verification.json、cumulative-review.md、handoff.md、phase-seal.json。

- [x] Root独立decode实际raw/UUID/geometry/12guard hash/exit；失败保持unavailable。审视研究源与chunk条件，保留persistent mapping和容量未知边界。
- [x] fresh最强reviewer累计审查全部新source/evidence/result，修复C/I后再完成。
- [x] 刷新四旧seal与current/frozen862；计划行政勾选留native-time before/after，封存新证据；不Git写入、不服务cleanup。
