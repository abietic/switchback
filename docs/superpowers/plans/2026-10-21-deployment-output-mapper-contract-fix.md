# Deployment Output Mapper Contract Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Make an independent pure mapper recognize the unchanged real PID1 packet using the current Go v1 contract, while retaining exit2/EACCES and all rejection boundaries.

**Architecture:** Copy the frozen PID1 mapper and its pure tests into one fresh offline directory. Correct two exact matching assumptions; add a fixed real-capture reader/replay and regressions at assess_output. The controller independently reviews, verifies preservation and seals the resulting offline artifact.

**Tech Stack:** Python3 standard library, unittest, JSON/base64/SHA256; no Go rebuild, guest or external services.

## Global Constraints

- Workdir: /path/to/switchback. New phase: .scratch/runtime-m1-deployment-output-contract-fix-1021. Only the new phase and this new plan may be written; existing spec is already approved and immutable.
- Approved spec SHA256 fe45d52fa5487cbdf73beb3ed437b4162bb9c5a9ba89165b065a8d0aa0660ec4. Human approval call_24a44c9c9ea140f7a197e83b6a4c0177 explicitly authorizes this offline implementation.
- mapper 的固定 caller cgroup 期望为 /sys/fs/cgroup/system.slice/switchback-deployment-diagnostic1021-pid1.service。保持完整精确相等；不 trim、prefix match、normalize 或接受旧相对路径。
- namespace entry owner 只接受 caller_thread 和 pid1；保持八项组合、唯一性、before/after typed equality、device/inode/errno/class 和 exit 对应校验。process packet 字段 caller_before/caller_after 不改名。
- PID1 零 ticks 的精确绑定与 caller 正值要求保持；不改 schema、资源、capability、Trust 或任何执行授权。
- 原 assess_output 仍返回现有 success_packet/expected_probe_exit_from_packet=2，表示格式有效的受限诊断包。离线结果必须显式保留四项 PID1 EACCES，deployment_ready=false、authority=none、execution_linkage=unproven，不能将 success_packet 文案等同完整 namespace 或部署资格。
- 不修改 host/guest/manager、Go、旧 mapper、sealed source/build/receipts/seals，不发布或替换运行中程序，不运行 ELF，不进入 guest，不创建 unit/stage，不改变三个门禁。真实窗口的一次授权已消费，不重试、不续跑、不独立 readback、不发 Stop。
- Frozen source: .scratch/runtime-m1-deployment-pid1-native-candidate-1021/output_mapper.py SHA256847844235393c74ba87ef08746e4febf08c16a6a779bfeeab147d9b150b417dc. Frozen test file in that same directory is the base of the new suite.
- Real captures: .scratch/runtime-m1-deployment-pid1-native-window-1021/{actual,readback}/stdout. Extract only complete diagnostic.json/stderr raw entries. Actual/readback diagnostic bytes must both remain3140 bytes / SHA2565a9b13ddc411556e76ab12cceecf14438b6154688802cf4ffabda3d604f91cb9.
- Preserve HEAD fbebfe754045bdbf1af40871a5018eed33743011, branch codex/observability-master-integration-1009, empty index,1803 baseline Git-visible files and933 protected metadata entries. New plan is the only added Git-visible file. No stage/commit/reset/clean/push.
- Python -I -B and explicit phase sys.path only. Do not rerun host/guest large matrices, Go/race suites or native execution: those sources remain unchanged.

---

### Task 1: Align the pure mapper and lock it to real captured bytes

**Files:** Create phase/output_mapper.py, test_output_mapper.py, replay.py, task-1-* diff/report/logs. Controller-owned baseline, authorization, preservation metadata, final report/seal are read-only to implementer.

**Interfaces:** Preserve assess_output(stdout:bytes, stderr:bytes, expected_boot:str)->dict and validate_diagnostic(raw,exit_code,expected_boot,pid). replay.py supplies load_capture(mode) for fixed actual/readback only, returning (stdout,stderr,boot), and run_replay()->dict. Its main prints the offline report and exits0 only if unchanged bytes are recognized with expected exit2 and retained denials/zero ticks. It cannot call any native adapter or subprocess.

- [x] Copy the two frozen files without modifying their originals. Add the real-packet regression first. Implement the fixed local reader: verify whole stdout against the existing receipt raw_logs fact; decode base64 with validate=True; check byte counts/hash/completeness and raw metadata equality; verify3140-byte fixed SHA, empty stderr and expected boot. No input normalization or packet rewriting.
- [x] Adapt only successful fixture contracts: caller cgroup absolute path, namespace owner caller_thread, PID1 fixture path /sys/fs/cgroup/init.scope. Leave process keys caller_before/caller_after and failure primitive caller.* enums unchanged.
- [x] Test-first RED on original mapper with the unchanged real packet. The core regression is:

```python
def test_real_captures_preserve_packet_and_denials(self):
    for mode in ('actual', 'readback'):
        stdout, stderr, boot = load_capture(mode)
        result = assess_output(stdout, stderr, boot)
        self.assertEqual(result['classification'], 'success_packet')
        self.assertEqual(result['expected_probe_exit_from_packet'], 2)
        self.assertEqual(result['packet'], json.loads(stdout))
        self.assertEqual(result['packet']['pid1_before']['start_time_ticks'], 0)
        self.assertEqual(result['packet']['pid1_after']['start_time_ticks'], 0)
        denied = [x for x in result['packet']['namespaces'] if x['before']['class'] == 'EACCES']
        self.assertEqual(len(denied), 4)
        self.assertTrue(all(x['owner'] == 'pid1' for x in denied))
        self.assertEqual(result['authority'], 'none')
        self.assertEqual(result['execution_linkage'], 'unproven')
```

- [x] Apply only the following production-mapper matching changes, each old literal expected exactly once; update stale module description if necessary:

```python
CGROUP = '/sys/fs/cgroup/system.slice/switchback-deployment-diagnostic1021-pid1.service'
# In validate_diagnostic namespace pair validation:
need(pair not in seen and pair[0] in ('caller_thread', 'pid1') and pair[1] in ('mnt', 'pid', 'user', 'cgroup'))
```

- [x] Add assess_output regressions for relative/incorrect/other-unit/suffix cgroup, old caller namespace alias and unknown/type-invalid owner, duplicate/missing combinations, current packet before/after drift, caller ticks0, PID1 0→1, bool/overflow. Keep existing pure suite's JSON/stderr/failure/exit mismatch and namespace validation tests; preserve the exact-PID1 zero tests. Validate exit mismatch directly through validate_diagnostic where needed.
- [x] Run the real regression GREEN, then the entire phase pure suite once with /usr/bin/python3 -I -B and explicit local sys.path; run replay.py once. Save argv/stdout/stderr/exit, method/subtest counts and exact original→new source/test diffs. Replay reports must show original mapper invalid_output and new success_packet/expected exit2 on the same bytes; it must assert authority none, unproven linkage, four EACCES, zero ticks and deployment_ready=false.
- [x] Write task-1-report.md with status, exact hashes, RED/GREEN results, minimal logic change and limitations. No native action is allowed. Controller task review independently checks actual diff, tests and replay.

## Controller closure

- [x] Review Spec and Quality on actual bytes/diff and evidence. Run a focused final pure suite/replay on the reviewed hashes; do not broaden to unchanged host/Go/native suites.
- [x] Whole-change review covers this one-task change and the interpretation of real packet/exit2; no namespace qualification or production wiring claim.
- [x] Verify original candidate/execution seals, all933 protected metadata/bytes,1803 baseline files, HEAD/branch/index, new plan-only visible delta. Save final source hashes, result and preservation proof.
- [x] Write a concise offline outcome/report, then seal/readback the new artifact. Finish this authorized slice without another permission prompt. PID1 namespace qualification and capacity/product integration remain separate outstanding work.
