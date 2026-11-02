# Fixed task-state inventory continuation

This implements the next fixed read-only observation already proposed in the sealed recovery-inventory handoff and approved by the user's continuation. It does not redesign capacity policy or request new execution authority.

## Goal and success

Observe only `/run/switchback-engine/containerd/io.containerd.runtime.v2.task`: present directory versus missing/invalid/incomplete, stable held-FD identity, complete direct item count, direct directory count, and nonhidden direct directory count. Only `present_directory + complete + eligible_namespace_dir_count=0` satisfies the fixed source `LoadExistingShims` empty-loop premise. Other results remain negative facts. A complete nonzero count is valid inventory but does not satisfy the empty-loop premise.

The fixed read also records bounded kernel-release metadata already available from uname, so named source kernel gates can be evaluated conditionally without another native command. This does not establish which binary/plugin was applied.

## Selected approach

A new one-shot Root-dispatched Python carrier uses no service installation or lifecycle. It reuses the already corrected, hash-bound policy/transport helpers as functions only; no old main/mode is executed. The fixed guest validates the existing boot/namespace/XFS/core/Engine state, safely opens the fixed directory, counts immediate entries without names/content export, rechecks held descriptors and freshly rewalks paths. Local synthetic fixtures and an independent review precede the single native attempt.

The alternatives were another staged systemd role, or recursive/per-record metadata export. A root-owned tmpfs directory count needs neither service setup nor record parsing, so the smaller carrier is sufficient. It remains an administrative Root read, not a backend IPC observer or proof that the backend can access privileged facts.

## Data and safety boundary

- Fixed native path and no user-supplied argv/env/config path. No namespace/task names, file contents, binary paths, socket addresses or arbitrary errors exported. No DB reads, directory creation, unit installation, daemon-reload, Start/Stop/reset/unmask, Engine/Worker/provider or Git operations.
- Linux O_PATH/NOFOLLOW/CLOEXEC ancestor handles; final directory O_RDONLY/DIRECTORY/NOFOLLOW/CLOEXEC/NOATIME. Root ownership and no group/world writes; no symlink traversal. Native requires exact known boot and Root PID1=self mount namespace. Test-only seams use generated temporary trees and their real test UID.
- Direct enumeration sampling limit4096:4096 accepted, first4097th yields incomplete and no partial counts. This is not a capacity allowance. Symlink-to-directory is not a directory, matching source DirEntry.IsDir semantics. Names beginning dot are excluded only from eligible directory count, not total counts.
- Enumeration/read/stat errors, invalid identity/type, mutations and unsupported platform stay explicit. Missing ancestors/target never become zero or a complete directory. Hold ancestors/target through scan and compare relevant stat identity, owner/mode, size/nlink/mtime/ctime plus a final no-follow rewalk. Observation is bounded in time and not a long-term writer-exclusivity proof.
- Corrected collectors preserve every actually captured byte through bounded one-read overshoot, raw outer/nested exit and termination facts. All local/native attempts use exclusive evidence directories; failures/consumed attempts are never overwritten or replayed.
- Fixed native budgets:15s guest wall,2s CPU,128MiB address space,core0,NOFILE128,FSIZE0. Guest bounded receipt maximum2MiB; verified transport total bound3MiB with threshold3080192/read65536; host deadline30s. Captured raw remains preserved even on malformed/deep/duplicate/scalar JSON or output overflow. No claim about bytes never read or remote process-group termination.

## Source research alongside implementation

Within a separate new subtree, source-only research follows the named remaining helpers: overlay SupportsIDMappedMounts/SupportsDType/NeedsUserXAttr/supportsIndex, storage.NewMetaStore and fsverity's kernel-version gate. Pin the existing containerd commit and any external dependency versions before reasoning. Maximum12 new official paths, reuse prior fixed sources where available. Stop at unresolved owners/budget; do not fabricate total growth or propose disabling plugins. Actual binary tags/applied config remain distinct from source imports.

## Global constraints

- Only candidate `/path/to/switchback`, branch `codex/observability-master-integration-1009`, HEAD `fbebfe754045bdbf1af40871a5018eed33743011`; preserve862 product inputs. No Git writes, commits, stash, reset, clean, push or merge.
- Preserve sealed prior phase SHA `55ae2f56b95223dd39c75c9c37afad1f70eab3179c770976eb446b33719a5184` (1872 bindings) and earlier12065 bindings; no old packet/mode/marker replay.
- New scratch only `.scratch/runtime-taskstate-inventory-1017/`. Root alone owns VM/SSH/native/service access; agents remain offline/source-only in assigned subtrees. One implementation writer at a time; independent source research may run alongside it.
- fullcapacityUNKNOWN/default-deny, capacity_ready=false, execution_enabled=false, paid unconsumed, OpenSpec38/41 with6.5/8.4/9.3OPEN. Fixed10GiB profile and80% policy remain unchanged. Actual Engine/Create/quota/Start and real product acceptance still require their separate matching proof.
