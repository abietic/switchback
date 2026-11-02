# Qualification candidate kit

This directory implements the source/offline delivery. It is undeployed. The
kit is **not qualification**, a signed sample, PG capacity readiness, or a Worker
Start permit. Runtime image IDs, CIDs, host/guest boot, keys, Trust, journal and
host/native receipts remain unresolved in `manifest.json`.

`build_candidate.py build` uses local **go1.26.4**, `CGO_ENABLED=0`, `GOPROXY=off`,
`GOSUMDB=off`, `GOENV=off` and readonly modules. Its only output is this phase's
`.scratch/runtime-m1-qualification-delivery-1022/task-4/kit-NNNN` (fresh exclusive attempt). It records all build
argv/env/unset/cwd/stdout/stderr/exit, tool digest/build information, exact source
before/after, separate qualification templates, ELF class/machine and hashes.
It builds Linux arm64/amd64 tools, real ARMv7/i386 compat helpers and Darwin
arm64 host tools. It never calls Docker, VM, PG, a provider or any built ELF.
`window.py self-check` exercises only the private fake protocol.

The source input root in build receipts covers the repository build/context
inputs. Qualification's normalized input root additionally binds current host
writer configuration and separately pinned deployment templates; those are
material dependencies, not a claim to have rebuilt Lima, PostgreSQL, nginx,
Docker or the kernel with this repository's Go toolchain. Current writer Go
version comes from its actual build information. Mutable virtual disk contents,
allocation and timestamps do not enter pre-Create static intent; actual writer
stat/allocation belongs to readbacks and the host receipt.

The current exact kit/source/templates/manifest identity is recorded in the phase
operation checklist (private evidence retained).
That checklist is outside build inputs; this content-pinned README cannot embed
its own final manifest hash. Historical kit attempts remain evidence only.

## Fixed protocol

`switchback-qualification-material` accepts one finite phase only:
`runtime-preflight`, `verify-runtime-preflight`, `prepare-identity`, `prepare-env-binds`, `host-preflight`,
`intent-input`, `create-core`, `staged`, `prepare-core`, `start-waiting`,
`check-pg`, `receive`, or `emit-<fixed leaf>`. Paths/resources/timeouts are not CLI
inputs. `prepare-identity` is an explicit **secret initialization** command;
the actual driver never invokes it implicitly. Backend/PG receive fixed actual digest-marker environment entries that bind the
complete DB URL/password to static intent. Plaintext comes only from the fresh
root0600/single-link/no-follow stable secret leaf, and is never written to
source or cross-host audit. Create/prepare/install recheck actual Env hashes,
fixed user/database/target and exact PG CID/shared network; markers alone cannot
substitute for the actual value. No operator writes a complete
source/inventory/Trust runtime JSON.

Typed intent comes from kit/source/template hashes, current ImageInspect and
image-export executable bytes, exact production resource/network references,
current XFS geometry/project quota, current host plist/writer material and the
prepared public identity. The separate image export endpoint is one fixed Unix
Engine `/v<negotiated ClientVersion>/images/get` stream, at most 2GiB total and
128MiB per executable. It checks config digest = image ID, every layer diff ID,
canonical paths/types/target links and exact artifact hashes. The ordinary
Engine JSON wrapper remains 512KiB. No pull/build fallback is available.

Order: verified resident Engine deployment/receipt → runtime/kit/VM checks → host material → typed intent-input → native intent
→ publication empty proof → exact Create-only core CIDs → staged → PG/web
preparation → one host monotonic origin → native begin → same Backend CID and
new publisher/observer processes enter their fixed 300s manifest waits → native
prepare freezes those actual identities/mount IDs → exclusive candidate
transfer → host before → native collect and owned cleanup → host after/common
end → native assemble → independent publication bootstrap from final prepared
Trust → PG initial-state recheck → installer.

The **same original 240s** bounds every stage from before the first
manifest-wait role Start through installation. Guest `window-open.json` adds an
independent boot/monotonic check. No stage restarts the budget. All native
producer phases run in fresh owned transient units with only SYS_ADMIN/PTRACE,
NNP and seccomp; publisher/observer use the guest-global namespace.

Cross-host transfer is canonical schema `switchback.qualification.transfer.v1`:
`schema,leaf,digest,data` (base64). The payload stays within 1MiB. Guest receives
only fixed allowlisted leaves, validates window/candidate/digests and exclusively
writes/fsyncs readonly bytes; host does the same. Existing output is never
replaced. A malformed present file fails; only host-before ENOENT under its
held safe parent is pending. Failure/unknown/cancellation/timeout consumes the
window. The driver preserves outcome and known owned CIDs, kills only its own
local subprocess groups, and performs no independent readback, retry, service
Stop, broad container cleanup or old-window recovery.

After the installed barrier/role/env checks, `serve-installed` selects only
`createdlayeripc.NewQualificationServer`, whose exact placement is
`/switchback.slice/switchback-core.slice/switchback-qualification-observer.service`.
Legacy `serve <digest>` and `NewServer` retain only
`/switchback.slice/switchback-core.slice/switchback-created-layer-observer.service`.
Both use the same strict root-owned cgroup2/resource checks: cpu.max20000/100000,
memory134217728, swap0, PIDs64. They never accept each other's unit, a prefix,
child/sibling path, or caller-provided cgroup. The socket/key/filesystem rules and
client/server peer UID/GID/PID rules remain unchanged; the client has no cgroup
comparison. No IPC wire, permission, limit or capability policy changes.

Installer independently reconstructs prepared material through the production
validator, current artifact/role/CID/namespace/private-dir/quota/cleanup checks,
and original deadline. It writes exclusive temporary leaves, checks metadata
and readback, chmods to0400, fsyncs, uses Linux `RENAME_NOREPLACE`, installs the
actual observer env first, and commits admission manifest last. The config
folder has exactly seven leaves. Any error at/after final rename attempt is
`qualification_install_commit_unknown`, never resumed or repaired.

The public observer directory and private writer are the **same inode**:
`/etc/switchback/created-layer-observer` is a fresh global RO bind and
`/var/lib/switchback/qualification-candidate/observer-env-writer` is a fresh RW
alias prepared before sealing the public view. No symlink, copy, namespace
substitute or temporary RW public view is accepted.

Publication bootstrap is separate from installation. Pre-Create it records
empty private/public directories and stable identities. After assemble, a
one-shot bootstrap creates only `writer.lock` and the original96-byte
`sequence.bin` at high0, bound to canonical **complete final Trust** by the
unchanged NUL domain. Its receipt stays in the candidate root, outside the seven
config leaves. Installer accepts only that candidate's exact initial state.
Key/journal/PG initialization is never performed by `InstallQualification`.

## Concrete later-operation checklist — not execution authorization

All following effects need the later concrete operation authorization. Missing
facts are unresolved; do not fill them with samples or reuse old windows.

1. Preserve old evidence/services/keys/journals/PG data and prove deployment
   ownership/admission fencing. Every fixed target below must be fresh or exact
   prepared kit material. An unrelated/existing target stops preparation; do not
   overwrite, reset, stop, delete or silently reuse it. VM `switchback-core` must
   already be Running; this driver does not start it or change CPU/memory/disks.
2. Prepare host0700 `~/.switchback/qualification-kit` and fresh0700
   `~/.switchback/qualification-candidate`, plus existing volume-mapped
   `/Volumes/SwitchbackCore/{cache,tmp,qualification}` and recognized Lima/VZ
   paths. The fixed Lima executable is
   `/Volumes/SwitchbackCore/toolchain/lima-2.2.1/bin/limactl`, with
   `LIMA_HOME=/Volumes/SwitchbackCore/lima`. Recheck exact APFS volume/container/
   physical store and quota/reserve120259084288; no partition/external disk,
   migration, fill-disk or independent physical-upper-bound requirement.
3. Exclusively copy the complete pristine kit to host0700
   `~/.switchback/qualification-kit` and root-owned guest0700
   `/opt/switchback/qualification-kit`, and the complete downstream Engine
   bundle to separate host0700 `~/.switchback/engine-bundle` and root-owned
   guest0700 `/opt/switchback/engine-bundle`. Preserve every manifest-covered
   path and mode. Defer architecture aliases until the Engine executor has
   canonically verified both complete original trees. Existing mismatched
   artifacts stop; there is no implicit replacement.
4. Before image or secret initialization, deploy the exact downstream Engine
   bundle using its fixed separately authorized preparation executor. The new
   root-private config/units/stage, resident containerd/Engine, metadata/GC,
   guest bridge/NAT/firewall effects and both fixed networks are whole effects
   listed in the Engine phase checklist. After that canonical verification,
   add ordinary extra copies of the matching arm64 or amd64 kit-inputs as
   guest `kit-inputs.json` and the matching `bin/linux-<arch>` leaves in guest
   `bin/`, retaining all manifest-covered originals. Prepare current
   `/mnt/lima-sbquota/core/source` from that exact source and install
   publisher/observer bytes at `/opt/switchback/bin/` from the same kit.
   The executor has one120s preparation
   deadline; a failed/unknown/occupied target rejects without cleanup or retry.
   Its unsigned Engine-ready receipt grants no qualification/capacity authority.
   Then prepare image inputs explicitly, using kit `source/` as the exact context:
   build `images/backend/Dockerfile`, `images/worker/Dockerfile`, and
   `images/web/Dockerfile` for the guest architecture, with candidate tags
   `switchback-{backend,worker,web}:qualification-candidate`. Pass labels
   `com.switchback.qualification-source=<source_input_root>` and
   `com.switchback.qualification-templates=<qualification_templates_root>`
   from manifest. This is a real image build with its pinned base/dependency
   inputs; obtain those separately if missing. The actual driver never pulls or
   builds. PostgreSQL must already exist at the digest pinned by core.compose.
   Guest preflight exports actual images and compares exact executable bytes
   with kit; tags/labels alone cannot prove it.
5. Establish existing XFS domains/quotas and the current core slice unchanged:
  120% aggregate CPU,3GiB,1024PIDs,swap0. Ensure the fixed Engine socket is
   `/run/switchback-engine/docker.sock`; Backend sees `/var/run/docker.sock`.
   Require both fixed networks `switchback-core1011_core` and
   `switchback-m1-worker-control`. No privileged Worker, Docker-socket Worker,
   host network, arbitrary host mount or host/public preview port is introduced.
6. Create only fresh fixed guest directories: candidate root0700; config0700;
   observer env0700; candidate `observer-env-writer`0700; publisher keys0700;
   publication-private0700 and publication-public0750; product
   `.qualification`0700; `/run/switchback-created-layer`0700. Prepare fresh
   `/mnt/lima-sbquota/core/postgres/{data,wal}` as UID/GID999 mode0700. Existing
   PG data is rejected. These directories/mounts are observable state changes.
7. Before key/journal/PG initialization, the explicit material
   `prepare-identity` operation first performs and exclusively records bounded
   `runtime-preflight`. Before the first Engine API, it validates the exact
   root-private deployment receipt, actual complete kit/bundle/source bindings,
   installed config/unit bytes and current boot/FS/daemon/PID/start/exe/argv/
   loaded-unit/cgroup/socket/FD/listener facts. Read-only EngineID and both network
   identities then follow native socket ownership proof. Missing/invalid/drifted
   receipt or native identity yields zero Engine calls. Historical120s elapsed
   is not a TTL, and normal metadata/GC does not invalidate deployment identity.
   With that verified handoff it reads actual Engine NCPU/memory and compares real core limits and proves only the necessary
   first-Worker condition: `core+2CPU<=80%EngineCPU`,
   `core+4GiB<=80%EngineMemory`. One aggregate core domain is counted once.
   4CPU/10GiB with1.2CPU/3GiB core satisfies this necessary condition; real PG
   reserve/quota/fresh-sample authority remains required. The driver uses read-only
   `verify-runtime-preflight` to compare the same prepared prerequisite with
   current facts; it never rewrites or retries a consumed preflight claim.
8. Explicitly run material `prepare-identity` once: new publisher Ed25519 seed
   at `capacity-publisher-keys/ed25519.seed`, new observer IPC key at the actual
   env directory `key`, new `core/postgres-password`, and generated public-only
   `candidate/public-identity.json`. This creates real secrets and is not a
   diagnostic. Existing secrets reject; no private key is generated this round.
9. Install the **new** unit templates under fresh names
   `/etc/systemd/system/switchback-qualification-publisher.service` and
   `switchback-qualification-observer.service`, daemon-reload, and prove they
   are inactive/unconsumed. Never replace/restart the old publisher unit. The
   actual driver then performs both global env directory binds and the public
   RO remount, proving matching dev/inode/root and distinct RO/RW mount IDs.
10. Review and authorize the complete `window.py actual` operation, including
    exact core Create-only CIDs, static-intent/nonce labels; PG and web Start;
    PG first initialization and official migrations; read-only initial local
    pool proof (uninitialized/rev1/seq0/NULL Trust, four history tables empty,
    same nonce/epoch); Backend/publisher/observer Start; finite nine Engine
    helper CIDs, XFS quota/setter/handoff I/O; same-window transfers; owned
    cleanup proof; final Trust-based journal bootstrap; manifest-last commit.
    Web inherits101:101, PG is999:999, Backend retains CHOWN/DAC_OVERRIDE/FOWNER.
    PG postmaster and web master are pinned to exact Engine InitPID, preventing
    same-executable child processes from becoming the declared role.
11. Manifest release continues the **same** role processes into their real
    behavior: Backend opens PG and business listeners; publisher reads its key,
    opens Store, burns sequence and samples/signs/publishes; observer reads IPC
    key and opens its IPC listener. Backend remains stale until a fresh valid
    publication and original PG reserve-before-permit. These are real effects,
    not qualification diagnostics. Provider delivery remains disabled.
12. Preserve all outcomes. A failed/unknown/expired window is abandoned, with
    no automatic retry/resume or unrelated Stop. Any recovery is a separately
    reviewed ownership decision. A later fresh signed sample→PG→exact CID
    quota→single Start and product Task/report/page acceptance still require
    their own real evidence; this offline kit closes none of those gates.

The qualification subprocess adapter shares the reviewed Engine executor's
private non-reaping leader observation and owned-group cleanup helpers. It loads
only fixed repository source or the exact current kit source after complete
`verify_kit`, compares helper source bytes/modes with manifest records, and
isolates private module dependencies from ambient `sys.path`/`sys.modules`.
Actual obtains a supported waitid reader before creating any child. Import,
self-check and offline process fakes resolve no real waitid/CDLL. The original
240s origin/deadline, argv/env/output caps, CLI and qualification wire formats
are unchanged. Normal EOF now retains the leader identity until local group
fencing; ECHILD/already-reaped identity never authorizes a numeric PGID signal.
ESRCH/EPERM, terminal observations, partial output and first errors remain in
independent local cleanup audit fields. No remote Stop or retry is added.

The shared Darwin EPERM path requires an already terminal, unreaped leader and
identical non-reaping terminal records before and after one complete fixed
libproc group snapshot. The reader binds only `/usr/lib/libproc.dylib` with the
supported Darwin64 integer ABI, uses `PROC_PGRP_ONLY=2` and a zero-initialized8192-
byte/2048-PID buffer, and resets ctypes errno immediately before the call. All
raw8192 bytes, the actual byte count and errno are audited before validation.
Only errno0, exactly4 bytes and the sole retained leader PID are accepted; empty,
extra/zombie/duplicate/wrong members, incomplete or malformed results, native
errors, ECHILD/reaped identity and terminal drift stay failed. This is the same
verified-source helper, with no additional source input or loader dependency.

`status=already_terminal` is convergence evidence only: the original EPERM/error
and `signal_status=failed`, `signalled=false` remain on the original attempt,
alongside separate proof events. There is no successful or additional signal.
The original240s qualification deadline,120s Engine deadline, output caps,
nonzero/timeout/truncation rejection and first-error preservation are unchanged.
Imports, self-check and fake tests load no real libproc/waitid binding. Native
Darwin fixtures and a source-matching complete new kit/bundle are separate
acceptance evidence; this path authorizes no guest retry, Engine, PG or provider
operation.
