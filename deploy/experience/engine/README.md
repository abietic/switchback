# M1 resident Engine candidate

This directory is source/offline material. Nothing imports or starts a daemon,
connects a socket, enters a guest, obtains secrets, changes a mask or grants
execution. Native/systemd/package compatibility and actual store/network
acceptance remain unproven. `testdata/receipt-v2.json` is the synthetic live-schema
producer/consumer fixture; `receipt-v1.json` retains historical semantics. Their
identities are fake. The unchanged `listeners-kit21-v1.json` contains all139 actual
Unix rows and remains rejected: supplemental filesystem facts were never captured.

The fixed configuration targets are `/etc/switchback/m1-engine` (root:root0700),
with regular0600 `containerd.toml` and `daemon.json`. New units are regular0644
under `/etc/systemd/system`; all installs are exclusive. The preexisting
`/run/switchback-engine` root0700 tmpfs128MiB/4096 inodes is verified, preserved
and never cleaned/remounted. All new transient state is under `m1-resident`.
The short `m1-resident/s` shim socket directory leaves room for containerd's
64-hex address hash and terminating NUL within Linux's108-byte AF_UNIX limit.

Both notify units use core Slice, Restart=no,30s Start,10s Stop and control-group
kill, without boot enablement. Engine Requisite+After does not start containerd.
An eventual executor starts containerd once, proves ready, then starts Engine
once. The Docker API endpoint is `unix:///run/switchback-engine/docker.sock`,
root:root0660 with root0700 parent. Backend retains its `/var/run/docker.sock`
view; Worker receives no Docker socket. Docker root is the existing
`/mnt/lima-sbquota/docker`, overlay2 with snapshotter=false. Core aggregate is
counted once at CPU120000/100000, memory3221225472, swap0, PIDs1024, VM4vCPU/10GiB.

Version4 server blocks and independent ttrpc configuration follow the pinned
[containerd2.4.1 configuration source](https://github.com/containerd/containerd/blob/v2.4.1/docs/man/containerd-config.toml.5.md).
Shim `socket_dir` follows its pinned
[manager source](https://github.com/containerd/containerd/blob/v2.4.1/core/runtime/v2/shim_manager.go).
CRI/image/runtime, NRI, dynamic opt, image verifier and restart monitor plugin
IDs are disabled explicitly. There are no imports/proxy plugins; the complete
six-role listener policy below includes Moby callback, metrics and libnetwork sockets. A finite strict TOML parser handles exactly this grammar under macOS
Python3.9 and Ubuntu Python3.12; unknown sections/keys/duplicate keys reject.
This static acceptance does not establish installed daemon compatibility.

Future actual effects include new root-private configuration/stage/unit files,
daemon-reload, resident daemon Starts, normal metadata writes/GC, docker0 and
guest bridge/NAT/firewall/forwarding changes, plus two fixed networks. Networks
use Docker's default daemon-managed bridge: daemon.json omits `bridge`, so no
precreated user-managed docker0 interface is required. Fixed networks
use Engine bridge/default IPAM with server allocated IPv4, empty options,
IPv6/attachable/ingress=false and exact labels
`com.switchback.owner=m1-resident-engine` and
`com.switchback.engine-profile=m1-resident-engine-v1`.
`switchback-core1011_core` is internal; `switchback-m1-worker-control` is not.
An owner/config mismatch rejects; no delete/recreate or unknown-result retry.

## Private pure interfaces

`contract.py` exports `validate_configuration(files: dict[str, bytes]) -> None`,
`validate_receipt(value: dict) -> None`, `validate_preflight(facts: dict) -> None`,
`validate_store_inventory(facts: dict) -> None`, and
`validate_listeners(facts: dict) -> None` retain historical v1 validation. Live paths
explicitly call `validate_listeners_v2(facts: dict) -> None`,
`validate_receipt_v2(value: dict) -> None`, and `validate_preflight_v2(facts: dict) -> None`.
The outer preflight schema remains v1, with mandatory nested listeners.v2. There is
no live v1 fallback or mixed-schema acceptance. All reject malformed, extra,
incomplete, unknown or inconsistent facts with ValueError. `_json(raw: bytes)
-> dict` is the bounded duplicate-key/NaN-rejecting decoder for all JSON input;
passing through a permissive JSON decoder first loses duplicate-key evidence.
`FILES` is an immutable basename→(absolute target, mode integer) mapping;
`UNIT_NAMES` is the ordered containerd/Engine tuple. `ENGINE_SOCKET`,
`RECEIPT_PATH`, `BUDGET_SECONDS=120` and `STEP_NAMES` are shared fixed constants.
No environment or configuration is read during import.

`bundle.py` exports `build_bundle(kit: pathlib.Path, destination: pathlib.Path)
-> dict` and `verify_bundle(bundle: pathlib.Path, kit: pathlib.Path, *, check=...) -> dict`.
The private checker propagates the caller's original deadline through complete
directory/record enumeration and every bounded read; it creates no new timeout.
These are offline library arguments, not actual executor CLI overrides. A fresh
phase-local `bundle-NNNN` is created exclusively with private0700 directories,
source modes preserved, regular0600 manifest, O_EXCL/no-follow leaves, fsync and
complete readback. Failures preserve partial output. Links, multiply-linked
leaves, unsafe owners/modes, occupied destinations and input drift reject.

The qualification manifest must cover every actual kit leaf and agree with both
architecture kit-input source/templates lists, copied records and before/after
build receipt. SourceRoot/TemplatesRoot are SHA256 over the existing builder's
compact ordered JSON arrays. Every leaf's exact bytes/hash/mode are checked.
Current daemon package pins must match. Required runtime Engine leaves are the
four configs/units plus contract.py,bundle.py,guest.py,prepare.py,store.py,binaries.py,README
and both receipt-v1.json and receipt-v2.json fixtures; the finite optional test leaves
are test_contract.py,test_prepare.py,test_guest.py,test_store.py,test_listener_v2.py,
testdata/listener-v2-corpus.json,testdata/listeners-kit21-v1.json,
testdata/listener-v2-provenance.json. `primary-pins.json` is included. New Engine code
is already in the qualification builder's Source records.

Bundle manifest exact keys: `schema`=`switchback.engine.offline_bundle.v1`,
`kit_manifest_sha256`, `source_root`, `templates_root`, `source_records`, `files`,
`runtime_qualification`, `capacity_ready`, `execution_enabled`, `paid_consumed`.
The final four are always false. Records retain `path,type,mode,bytes,digest`;
`files.path` adds `source/`, while source_records retain repository paths.
The manifest is downstream of the complete kit and never modifies it. Runtime
receipt/private runtime state cannot be an upstream source. Only the checked-in
receipt fixtures and the explicit test corpus/provenance/139-row historical fixture
are permitted; unrelated leaves still reject. Fixed future copies are host
`~/.switchback/engine-bundle`, guest `/opt/switchback/engine-bundle`; the existing
qualification-kit paths remain separate.

## Store classifier handoff

The store DTO exact top-level keys are `schema,root,containerd_root,docker_version,
containerd_version,scan_complete,entries,restoration`; schema is
`switchback.engine.store_inventory.v1`, roots are fixed, versions29.9.0/2.4.1,
and scan_complete must be true. Each entry has exactly
`path,kind,uid,gid,mode,nlink,bytes,sha256,classification,format,rule_revision,
scan_complete,restore_refs`. root ownership is mandatory, writable group/other
modes reject. Regular files require nlink1 and a nonempty bounded payload/SHA256.
Directories require nlink≥2, bytes0 (no leaf payload), null SHA256 and directory-v1.
Every ancestor within data-root must occur in the complete entries. Reconstructed
children distinguish empty_directory from engine_directory; a nonempty parent
may never claim empty_directory. The limit is8192 entries/1GiB total leaf bytes.
Unbound v1 regular files require `rule_revision=m1-engine-store-v1` and a full scan.

When the guest binds a scan to the current, fully validated XFS `filesystem`
fact, the independent `switchback.engine.store_inventory.v2` DTO adds exact
`backing_filesystem` and requires `rule_revision=m1-engine-store-v2` on every
entry. Its sole added kind is `block_device` at
`/mnt/lima-sbquota/docker/overlay2/backingFsBlockDev`: root:root, mode0600,
nlink1, size0, null SHA256, `quota_backing_device` classification and
`docker-xfs-quota-backing-device-v1` format. `device_identity` contains exactly
`device,inode,rdev,major,minor`; both raw Linux device numbers must match the
backing filesystem's major/minor and the pinned root directory device. The
collector opens this node only with `O_PATH|O_NOFOLLOW|O_CLOEXEC` under its
verified parent dirfd, then checks fstat, pathname and complete-walk identity.
It never reads or writes device payload. Existing v2 inventories remain valid
only with this overlay2 device path and their original v2 rules.

A bound scan now emits the independent `switchback.engine.store_inventory.v3`
DTO with `rule_revision=m1-engine-store-v3` on every entry. Its only permitted
block devices are the exact `overlay2/backingFsBlockDev` and
`volumes/backingFsBlockDev` paths under the fixed data root. Each may be absent
or appear once and must independently pass the same strict metadata, filesystem
identity and race checks. v3 uses the same closed `device_identity` DTO; it does
not admit other volume contents or special files. Preflight binds v2 and v3
`backing_filesystem` to its complete filesystem fact; the claim gate rechecks
that same binding and rescans. An unbound v1 scan still rejects block devices,
and all three versions reject
other special files or skips ordinary file payload validation. The recorded
failed guest packet lacks `st_rdev`; synthetic success tests do not fill that
missing real observation or prove the rest of that store acceptable.

The v3 rule also permits only the two optional empty Docker plugin base
directories `plugins/storage` (root:root, mode 0755) and `plugins/tmp`
(root:root, mode 0700). Each must have nlink 2, zero DTO bytes, null SHA256,
`empty_directory`/`directory-v1`, no descendant, and the same bound XFS device.
The guest proves empty names through the opened `O_DIRECTORY|O_NOFOLLOW`
descriptor before visiting any child, then retains the descriptor, pathname,
coverage and complete-scan identity checks. v1/v2 still reject these paths;
other plugin content and IDs remain unsupported. The 56-path regression DTO
uses raw recorded metadata and leaf hashes but derives classification and
wrapper fields offline; it is not a complete payload or runtime inventory replay.

Both restoration and every entry.restore_refs have exactly
`containers,plugins,volumes,tasks,sandboxes,external,unknown`, each integer zero.
These are classifier results from complete format parsing, never an operator's
claim. Nonempty accepted classes/formats/paths are:

| classification | format | fixed path below docker root |
| --- | --- | --- |
| engine_identity | docker-29.9.0-engine-id-v1 | engine-id |
| docker_empty_index | docker-29.9.0-repositories-v1 | image/overlay2/repositories.json |
| docker_empty_volume_index | docker-29.9.0-volume-bbolt-v1 | volumes/metadata.db |
| containerd_empty_metadata | containerd-2.4.1-bbolt-v4 | containerd/io.containerd.metadata.v1.bolt/meta.db |
| docker_network_metadata | docker-29.9.0-network-bbolt-v1 | network/files/local-kv.db |
| buildkit_cache | docker-29.9.0-buildkit-bbolt-v1 | buildkit/cache.db, metadata_v2.db, history.db |
| buildkit_empty_index | docker-29.9.0-buildkit-empty-bbolt-v1 | buildkit/containerdmeta.db, buildkit/snapshots.db |
| content_cache | containerd-2.4.1-content-sha256-v1 | containerd/io.containerd.content.v1.content/blobs/sha256/64hex |
| retained_bootstrap_input | switchback-retained-bootstrap-sha256-v1 | six exact historical paths and SHA256 in contract.RETAINED_INPUTS |

Content blob filename must equal its raw SHA256. For the other classes, the
collector supplies raw DB hash/size plus complete pinned-format semantic scans
and zero restore refs. The disk decoder is Task2's separate store.py; this
policy does not infer safety from size/string scans or arbitrary nonempty DBs.
Initialized empty indexes (including the nonzero-sized volumes/metadata.db
only after a complete scan proves zero volumes) and provable non-executing cache may be accepted.
The volume-index supported subset is bbolt format2 with the sole empty `volumes`
bucket and no other records, based on the official
[current Moby store source](https://github.com/moby/moby/blob/master/daemon/volume/service/store.go).
The volume subrule is a conservative parser boundary, not a native
version-compatibility claim. Other structures reject.
Containers, plugins, volumes, active tasks, sandboxes, external recovery refs,
unknown formats/origins or unsupported DB records reject without deleting data.
This is a bounded supported subset, not a claim of complete metadata-growth
physical upper bounds or universal acceptance of historical stores.

## Preconnection and receipt contracts

Live listener DTO exact keys are `schema,phase,boot_id,current_boot_id,complete,daemons,
identity_before,identity_after,sockets,socket_parents,unix,tcp,tcp6`; schema is
`switchback.engine.listeners.v2`. Phases preflight/containerd_ready/engine_ready have
0/1/2 daemons,0/2/6 sockets and0/2/5 parents. `complete` has exactly
unix,tcp,tcp6,fd_owners,processes, all true. Before/after daemon identities must equal
the collected identities. `/proc` Unix kernel inode is separate from pathname stat inode.

The six ordered roles are containerd API (`m1-resident/containerd.sock`,0660),
containerd ttrpc (`m1-resident/containerd.ttrpc`,0660), Docker API (`docker.sock`,0660),
extension callback (`m1-resident/docker/extensions/callback.sock`,0777), internal
metrics (`m1-resident/docker/metrics.sock`,0777), and libnetwork external key
(`m1-resident/docker/libnetwork/[0-9a-f]{12}.sock`,0600), all relative to
`/run/switchback-engine`. No random basename is pinned. Only the complete Unix table
may discover the sole matching libnetwork listener. Supplemental roles belong to the
exact dockerd PID/start/unit. All sockets retain the complete SOCKET_KEYS identity,
root:root,nlink1,unique positive kernel and filesystem inodes on the runtime tmpfs.

`socket_parents` entries have exactly `path,device,inode,kind,uid,gid,mode`:
ordered `/run/switchback-engine`, its `m1-resident`, `m1-resident/docker`,
`m1-resident/docker/extensions`, and `m1-resident/docker/libnetwork`. Every entry is
kind directory,root:root,0700,unique positive inode, on the same tmpfs device.
Each socket's immediate-parent identity must equal the corresponding entry. Ready
receipts also bind the first entry to runtime_parent. Future exclusive install_files
precreates only the four newly owned directories using existing no-replace/no-follow
mkdir0700; existing directories are never adopted or chmod-repaired. No capability
is added. Collection samples every active socket and every parent before and after
the complete process/FD scan and records both samples. Parent identities participate
in every preconnection,API-before/API-after,final-identity and publication comparison.
The Docker API role is selected by exact path, independent of ordered last role.

AF_UNIX entries have path,inode,listener,owners; TCP/TCP6 entries have
address,port,inode,owners. Every owner is main_pid,start_tick,exe,cgroup,fd. Full bounded
raw Unix/TCP/TCP6,process and FD inventories are retained. The full FD owner set must
match the exact daemon, with no hidden listener or extra owner. Daemon TCP/TCP6
listeners and default/foreign Docker/containerd Unix listeners reject. API health
cannot satisfy this gate. PID1 alone may have start_tick0. Approved daemon nonlisteners
are constrained to the six role paths or unnamed path and exact approved owners;
multiple listening inodes for a protected pathname reject. Established nonlisteners
may retain a protected pathname with another inode when all owners match approved
daemons. The original139-row inventory includes two such legitimate containerd FDs.
Across all rows/families, one `(main_pid,start_tick,fd)` identifies exactly one kernel
inode. Historical v1 validators retain their original0/2/3 socket policy.

`testdata/listener-v2-corpus.json` is a closed object with schema
`switchback.engine.listener_corpus.v2`, provenance `synthetic; no runtime authority`,
and cases with exactly `name,accepted,listeners,receipt`. A case is accepted only when
both strict listeners.v2 and ready deployment_receipt.v2 validation succeed. Decode
bytes through `_json` to preserve duplicate-key/integer rejection. The corpus includes
two distinct valid random IDs, the unchanged actual139-row negative fixture, and a
complete139-row positive with only missing filesystem/parent facts explicitly synthetic.
The augmented positive preserves all original table rows and their owners, including
established same-path nonlisteners; foreign-owner/path mutations still reject.
`listener-v2-provenance.json` records the original wrapper SHA256, extracted canonical
value SHA256 and fixture SHA256. All positive supplemental filesystem metadata is
explicitly synthetic; the actual fixture has no such facts and remains rejected.

Operational receipt is regular0600 root-private at
`/var/lib/switchback/m1-engine/deployment-receipt.json`, under a new exclusive
root-owned0700 stage. It is unsigned. Its exact property spelling and finite
nested DTO are shown in testdata/receipt-v2.json. Top-level exact keys are
`schema,status,bundle_manifest_sha256,kit_manifest_sha256,source_root,templates_root,
boot_id,filesystem,runtime_parent,daemons,installed_files,sockets,socket_parents,engine,networks,
steps,elapsed_ms,known_effects,unknown_effects,runtime_qualification,capacity_ready,
execution_enabled,paid_consumed`. Schema is switchback.engine.deployment_receipt.v2;
successful status is only engine_ready, and all four authority flags are false.

Daemon records have exact unit,main_pid,start_tick,exe,exe_sha256,cgroup,boot_id.
Installed records have source_path,target_path,sha256,mode,uid,gid,bytes; exactly
the four fixed installs must be present once. Socket records have
path,device,inode,kernel_inode,kind,nlink,uid,gid,mode,owner_unit,owner_pid,
owner_start_tick,parent_path,parent_device,parent_inode,parent_uid,parent_gid,
parent_mode. Exactly the six roles above are required in that order.
Engine socket parent identity binds the recorded existing tmpfs. Engine DTO
has id,version,data_root,storage_driver,containerd_snapshotter,cgroup_parent,
containers,volumes,plugins. The last three are zero. Network DTO matches the
fixture's closed bridge/default-IPAM schema and exact owner labels.

`steps` is a prefix of fixed STEP_NAMES, up to12 ordered records with exact
name,status,started_ms,finished_ms,exit_code,stdout,stderr,known_effects,
unknown_effects. Times are monotonic elapsed milliseconds; outputs are at
most16KiB each. Successful engine_ready requires elapsed_ms≤120000. Failed and
unknown receipts may retain actual later observation/receipt completion times,
with a finite integer upper bound of9223372036854775807; step order and times
within elapsed_ms remain mandatory. Times are never clipped to the deadline.
This does not extend/reset the one120s execution deadline or permit any new
Start/network/action after it; Task2 must enforce that gate separately and save
bounded raw rejected observations before excluding invalid typed identities.
Successful steps require real exit0.
Failed/unknown receipts retain the completed prefix and final failed/unknown
step, explicit nullable unobserved runtime identities and partial install,
daemon/socket/network records. socket_parents retains the latest fully validated
0/2/5 subset, including an empty list before a valid listener sample. Known/unknown effects exactly equal their step
unions. Ready requires all12 steps, complete identities and no unknown effects.
No receipt grants Trust, capacity, permit, Task or provider authority. Existing
qualification300s barrier/240s window, seven check IDs and raw/signing formats
remain unchanged.

Run pure tests:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/experience/engine -p test_contract.py -v
```

## Fixed preparation executor

`python3 deploy/experience/engine/prepare.py self-check` only parses the fixed
configuration, shared synthetic fixture and Python3.9 syntax. It creates no
adapter, subprocess, listener, guest operation, service, secret or runtime state.
The sole other CLI action is `actual`; there are no path, command, environment,
timeout, allow, skip, readback, resume or retry overrides. `guest.py` has no CLI.
The actual adapter is source material for later explicit execution authorization.

Host actual uses the existing exact Lima2.2.1 executable
`/Volumes/SwitchbackCore/toolchain/lima-2.2.1/bin/limactl`, fixed LIMA_HOME
`/Volumes/SwitchbackCore/lima`, VM `switchback-core` and that VM's `lima.yaml`.
The host home comes from the account database, ignoring HOME overrides. It
verifies the complete fixed host bundle/kit and an already Running4vCPU/10GiB VM
before entering a single fixed Python guest process. No VM Start is available.
That process first emits only one monotonic sample and accepts one control
envelope with the host's canonical binding, then EOF. Only under that converted
absolute deadline does it run the same complete bundle/kit verifier and compare
the verified binding exactly, before reading configuration or entering preflight.
G_deadline=G_sample+(H_deadline-H_receive) charges transport delay to the same120s
window. Local transport termination never proves that guest effects stopped.

`_run_window(ops, monotonic)` is the private coordinator shared by the real guest
adapter and deterministic tests. Commands use argument arrays, a fixed finite
environment, owned process groups, bounded pipes and the original deadline. Raw
observations and command exit/signal/output/effects precede validation. Invalid
identities remain in the bounded audit and never enter typed receipt fields.
Only the exact `/usr/bin/systemctl list-unit-files --all --type=service
--type=socket --no-legend --no-pager --plain` and corresponding `list-units`
argv have a256KiB stdout budget. All stderr, other command stdout and step
display summaries remain16KiB; argument changes retain the ordinary limit.
This private policy has no caller/CLI/environment override. Both complete unit
lists, the512-name limit, every ordinary unit/instance's properties/ExecStart, default masks and
foreign/new-unit rejection remain required. Overflow rejects the whole command;
no prefix, filtering, paging or retry substitutes for complete EOF/exit0 and
owned-process convergence. Raw observation limits remain1MiB/frame and64MiB
total. Only exact `[^@]+@\.(service|socket)` names are uninstantiated template
definitions: they must appear in the installed catalogue and be absent from the
manager catalogue. Each retains a `unit-catalog-template` raw observation with
exact name and both catalogue memberships, and still counts toward512. Templates
have no fabricated runtime properties or inactive `other_units` entry; no show,
load or placeholder instance is requested. Names such as `foo@bar.service` remain
runtime instances and use the unchanged six-property/ExecStart checks, including
renamed daemons. A template listed by the manager rejects the whole catalogue.
This checks current runtime instances; it does not interpret template text or
assert future execution behavior.

Only the exact catalogue property tuple `Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen`
enables repeated `Listen` and `ExecStart` rows at `_IO.show`. All nonempty items
retain order and identical multiplicity, joined with one newline; absent and
single empty fields keep their existing meaning. Any empty repeated item,
duplicate scalar/unknown key or malformed line rejects. Direct `_properties`
calls and every other show query remain strictly unique, including the installed
own-unit effective ExecStart query. Input fields cannot select this context.
Catalogue daemon recognition scans the complete joined ExecStart value, so a
daemon in a later item still reaches the existing active/unknown-unit rejection.

The older template regression has four captured ordinary show outputs. The
array regression reuses its complete list bytes only after byte equality with
the later saved raw frames was verified:311 union names,28 template definitions
and283 ordinary units/instances. It replays all39 show outputs captured in the
later failed window (38 parsed items and the two-Listen target);244 later
ordinary show responses are explicitly synthetic. The failed window traversed
only four template observations. Completing the offline replay traverses all28
definitions and283 ordinary items; it does not establish live runtime state or
preflight success. Synthetic offline tests do not prove that a live catalogue fits256KiB.
Every API connection repeats the complete process/FD/Unix/TCP/TCP6/socket/parent gate;
HTTP buffered reads check the absolute deadline before each recv. Unknown
NetworkCreate ends the window without another inspect/create. Local failure
cleanup signals only the owned new-session subprocess group before reaping its
leader, even if descendants have closed their pipes. Normal EOF first observes
the leader with non-reaping WNOWAIT, preserves its reserved PID/PGID through the
group SIGKILL fence, then reaps and checks the actual leader exit status. Timeout
also observes without reaping before local cleanup; ECHILD/already-reaped identity
never permits signalling a possibly reused numeric PGID. ESRCH,
signal/reap/FD errors and the original output/failure remain in the final audit.
No failure branch runs remote Stop, restart, enable, reset-failed or delete.

Linux uses Python's `os.waitid`. Darwin Python3.9 omits it, so actual lazily binds
the existing `/usr/lib/libSystem.B.dylib` waitid with the supported Darwin64 LP64
SDK ABI (104-byte siginfo, PID offset12, status20, reserved48; P_PID1,
WEXITED4/WNOWAIT32/WNOHANG1, CLD_EXITED/KILLED/DUMPED1/2/3). Other platform/ABI or
changed constants fail closed before creating a child. Imports and self-check
do not load this binding; offline tests use fake CDLL and memory siginfo only.

Darwin alone has a narrow EPERM convergence path after the original owned-group
SIGKILL attempt. A valid WNOWAIT terminal leader must remain unreaped, with the
same exact PID and terminal record observed again before and after one fixed
`/usr/lib/libproc.dylib` `proc_listpids(PROC_PGRP_ONLY=2, owned_pid, ...)` snapshot.
The supported Darwin64 integer ABI uses a fresh zero-initialized8192-byte buffer
(2048 signed32 PIDs), with ctypes errno cleared immediately before the call.
The audit preserves the actual byte count, errno and all8192 raw bytes before
validation, including failed/oversized returns. Only errno0, exactly4 bytes and
that sole retained leader PID prove convergence. Empty, extra (including zombie
or duplicate), wrong/negative PID, full/truncated/unaligned/negative results,
unsupported ABI, reader errors, ECHILD, reaped identity or terminal drift reject.
No extra signal or later numeric-PGID query follows a lost child identity.

On this proof alone, the cleanup event's `status=already_terminal` describes
group convergence. Its original `error`/EPERM remain, `signal_status=failed` and
`signalled=false` explicitly record that no signal succeeded; separate proof
events retain both terminal observations and the bounded snapshot in JSON-safe
raw form. Reaping and exit readback still use the original deadline. A timeout,
nonzero exit, output truncation or earlier failure cannot become success. Other
platforms, errno values and unproven terminal states keep the failed cleanup.
This same private supervisor serves the Engine command, host transport and
qualification subprocess adapter without another implementation or CLI/schema.
Offline fakes do not establish native compatibility; the separately bounded
local Darwin fixtures and the new complete kit/bundle binding remain required
before delivery. No guest/Engine/PG preparation is retried by this repair.

`binaries.py` reads only the fixed retained package directory
`/mnt/lima-sbquota/docker/bootstrap-1013`. It hashes the exact two pinned Debian
archives, parses bounded ar/tar data in memory and compares each target ELF with
the installed regular root-owned arm64 file and dpkg identity. The daemon's
`--version` runs only after that full pin proof; missing/mismatched archives or
installed bytes cause zero daemon command calls. It never
extracts to disk or executes archive content. gzip/xz/uncompressed tar are
supported; missing archives, other compression, corruption or mismatch reject.
No download or installation is attempted. The other three pinned bootstrap
archives and `.sb-proof-static-1013.bin` are accepted only by the six exact
historical path/hash records in RETAINED_INPUTS, with full owner/mode/link/bounds
and directory coverage checks. Historical records do not prove current guest state.

The read-only bbolt format2 decoder validates both committed meta pages, FNV
checksums/transaction selection, page bounds/extents/freelist, sorted branch/leaf
keys, nested/inline buckets and complete current-tree enumeration. Both meta
pages must be intact for this supported subset; corrupt newer metadata never
silently selects an older empty root. containerd schema4 uses Go signed varint08.
Limits are64MiB per DB,65536 records and depth32, within the existing8192-entry/
1GiB scan. These are parser limits, not a metadata physical-growth guarantee.
Empty initialized indexes may have nonzero file sizes. Unknown containers,
sandboxes/tasks/plugins/volumes/external refs or schemas reject.

The exact [Docker29.9.0 graphdriver controller](https://raw.githubusercontent.com/moby/moby/docker-v29.9.0/daemon/internal/builder-next/controller.go)
and [snapshot adapter](https://raw.githubusercontent.com/moby/moby/docker-v29.9.0/daemon/internal/builder-next/adapters/snapshot/snapshot.go)
establish the additional containerdmeta.db/snapshots.db empty-index paths. This
branch uses EngineID for Worker identity, so another branch's workerid is not
whitelisted. Docker29.9.0 pins BuildKit0.34.0 and bbolt1.5.0. Libnetwork bbolt
values contain an8-byte LE index followed by network JSON. Its nonempty supported
subset contains only exact host/null builtin networks, empty-string
`loadBalancerIP` (the pinned net.IP text encoding) and matching zero endpoint
counts. Full key enumeration independently rejects endpoint/sandbox/unknown keys;
zero Count never substitutes for complete restoration classification.
The store walker emits bounded raw path/stat facts before owner, mode, type,
link, size and identity checks; rejected unsafe objects are never opened for payload.
Path evidence uses `os.fsencode`, a bounded base64 prefix and full filesystem-byte
hash, plus JSON-safe escaped display text. Unsupported non-UTF8 root/leaf names
reject after stat audit persistence and before payload open.

Actual audit leaves are host
`~/.switchback/engine-preparation/{claim.json,observation-NNNNN.json,outcome.json}`
and guest `/var/lib/switchback/m1-engine/{claim.json,prepared-receipt.json,
deployment-receipt.json,outcome.json}`. Stages/leaves are exclusive; existing
targets reject. Host rejection before stage acquisition creates only an exclusive
`~/.switchback/engine-rejected-MONOTONIC_NS.json` audit leaf. Partial files, raw
rejected observations and unknown effects remain preserved and are never reused.

The fixed write_receipt step writes/fsyncs/readbacks `prepared-receipt.json`.
After it returns, its actual step result is added to the final receipt body.
Exclusive deployment-receipt publication/fsync/readback is a separate outer
outcome operation: the body never certifies its own publication. Publication
failure leaves the outer status failed/unknown and identifies the retained body
as a prepared candidate; it does not rewrite the12 completed step results.
Failure observation/audit persistence may finish after120s with actual times,
without authorizing another action or independent readback. Installed daemon,
systemd, historical-store and network compatibility still require later native
execution under explicit authorization.

## Qualification read-only handoff

`qualificationRuntimeReadback` first loads the fixed kit and calls private Go
`qualificationVerifyEngineDeployment(ctx, kit)` before creating its ordinary
Engine client. Both `runtime-preflight` (also called by explicit
`prepare-identity`) and `verify-runtime-preflight` take this path. Missing,
partial, malformed or mismatched operational receipts cause zero Engine calls.
The consumer accepts the ready subset of the exact shared fixture contract:
all properties and types are mandatory, including empty arrays/maps, integer
zero and the four false authority flags; duplicate JSON keys reject.

The handoff compares the exact complete kit manifest hash, downstream bundle
manifest hash, Python ordered compact SourceRoot/TemplatesRoot and canonical
architecture kit-inputs. Every bundled source record maps back to current kit
Source records; source copies and all four actual installed config/unit leaves
must match current hashes/bytes. Source0644 and installed config0600 are checked
in their separate roles. Receipt/source/bundle paths are fixed; actual receipt
or prepared runtime state never becomes an upstream source input.

Linux collection uses bounded read-only proc/mount/FD/XFS geometry/quota state,
protected no-follow file reads and fixed `systemctl show`. It checks current
boot/FS/tmpfs, root ownership, both main PID/start/exe/hash/cgroup records,
loaded unit fragment/no drop-ins/no pending reload, lifecycle settings, empty
extra environment and exact live argv. Full Unix/TCP/TCP6 tables plus the
complete process/FD owner inventory precede every fixed Engine GET connection.
The three read-only GETs are `/v1.56/info` and the two exact network-ID inspections;
there is no negotiation connection or fallback. Their owner proof is repeated
immediately before dialing, and boot/process/FD/files/socket/network/source
identities are checked around collection. An additional local30s cap only
shortens the caller's context; it does not reset either qualification window.
The later original Engine readback remains explicitly behind this gate.

The historical120s `elapsed_ms` is preparation duration, not a receipt TTL.
Current Engine metadata and network endpoint attachments may change during the
subsequent explicitly authorized image/qualification preparation. The handoff
does not re-run the prestart store classifier or demand historical metadata
file hashes remain constant. It checks current daemon/config/socket/network
identity, then retains the original qualification/PG/Trust/admission authority.
The original300s manifest barrier, shared240s window, seven checks, raw formats,
signing, reserve-before-permit, exact-CID quota and one Start are unchanged.

The offline builder and bundle library are the fixed delivery path; the final
kit/bundle digests and the whole later-operation checklist are kept in the
phase report outside SourceRoot inputs. Building a candidate or passing these
pure tests proves no Linux/systemd/network, Task, paid model or provider result.

## Process identity during FD enumeration

`_IO.processes` accepts an R/S state transition between its before/after
`/proc/<pid>/stat` reads only when PID, start tick, and every flags bit remain
identical. Same-state observations keep their existing behavior; other state
transitions and any identity change still fail closed. The collector retains
the before observation in its result and continues the same cgroup, executable,
FD, process-table, and listener checks.

The offline regression embeds captured stat, cgroup, and cmdline bytes and
executes the real `_IO.processes` method with synthetic OS syscall isolation.
The stat pair is byte-matched to guest frames 1918/1931. Its executable and FD
inventory are synthetic, so this proves the state-comparison boundary only; it
is not a complete FD inventory or guest replay.

## Containerd 2.4.1 dump imports representation

The installed `containerd.toml` keeps `imports = []`. Containerd 2.4.1's
[`config dump` initializes defaults](https://github.com/containerd/containerd/blob/v2.4.1/cmd/containerd/command/config.go),
including `/etc/containerd/conf.d/*.toml`. Its
[`LoadConfigWithPlugins` loader and slice merge](https://github.com/containerd/containerd/blob/v2.4.1/cmd/containerd/server/config/config.go)
retain an existing nonempty output slice when the input slice is empty, while
recursive imports are resolved from each input config's own imports list.
The displayed default list alone therefore does not show that matching files
were loaded. A task-private experiment with the unmodified official v2.4.1
library confirms that empty input imports retain a relocated default glob
without applying its sentinel, while explicit imports apply the sentinel.
This relocated library proof does not run a Linux daemon or inspect `/etc`.

At the original `daemon_reload` comparison, the validated preflight binary
record must identify exactly version `2.4.1` and the fixed input imports must
be an empty list. Only an empty output list or exactly
`['/etc/containerd/conf.d/*.toml']` is accepted. Other lists, duplicates, types,
versions and any checked policy-field or type drift reject before systemd
daemon-reload. Installed bytes, package pins and all subsequent unit, process,
listener and store checks retain their existing boundaries.

The regression embeds the exact saved4762-byte stdout with SHA256
`918541f7a970f4ac23c2084e2ef49601f34e5f7a32e42c28f5dd406984d72b64`.
Python3.14 replay verifies its typed Python3.9 fixture against stdlib `tomllib`
and the saved raw command. Negative mutations are synthetic parser observations
at the actual `_GuestOperations.call('daemon_reload', ...)` seam; they verify
that no systemctl command or reload effect occurs on rejection.
