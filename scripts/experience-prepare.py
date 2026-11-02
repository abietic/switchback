#!/usr/bin/env python3
"""Pure preparation renderer. It never executes the rendered deployment."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys

# Loading the frozen COPY-closure helper must not create files outside output.
sys.dont_write_bytecode = True

PINS_SHA = "80b79a98b6c71f7cf90c8be802885945c36b40b87a61165bb6eb012b1c04bacc"
UNKNOWN = ["actual_engine_identity", "boot_mount_namespace_fs_uuid", "quota_enforcement_and_inode_limits",
           "nonoverlapping_domains_and_all_writers", "all_time_Mmax_and_Delta_inode", "exact_Ubuntu_SRU",
           "host_backing_and_peak_growth", "aggregate_cgroup_membership_and_swap",
           "dynamic_project_range_and_layer_proof_before_start", "provisioner_missing_CgroupParent_and_MemorySwap",
           "provisioner_Create_immediately_Start", "post_start_cgroup_before_prompt",
           "retained_ingestion_browser_scrape_transport", "actual_frozen_image_ids", "migration_result", "system_disk_containerd_journal_transport_writers", "runtime_secret_product101_mapping_not_engine999"]


def require(ok, message):
    if not ok:
        raise ValueError(message)


def canonical(path):
    text = os.fspath(path)
    require(os.path.isabs(text) and os.path.normpath(text) == text, "canonical absolute path required")
    current = Path('/')
    for part in Path(text).parts[1:]:
        current /= part
        info = current.lstat()
        require(stat.S_ISDIR(info.st_mode), "symlink or non-directory ancestor")
    return Path(text)


def read(root, name, limit=16*1024*1024):
    require(not Path(name).is_absolute() and '..' not in Path(name).parts, "unsafe input path")
    canonical(root / Path(name).parent)
    fd = os.open(root / name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(fd)
        require(stat.S_ISREG(before.st_mode) and before.st_size <= limit, "unbounded or special input")
        with os.fdopen(fd, 'rb', closefd=False) as f:
            data = f.read(limit+1)
        after = os.fstat(fd)
        require(len(data) <= limit and (before.st_size, before.st_mtime_ns, before.st_ctime_ns) ==
                (after.st_size, after.st_mtime_ns, after.st_ctime_ns) and
                os.path.samestat(after, (root / name).lstat()), "input changed")
        return data
    finally:
        os.close(fd)


def encoded(value):
    return (json.dumps(value, sort_keys=True, indent=2)+"\n").encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def plan(root):
    root = canonical(root)
    pins = read(root, 'deploy/experience/primary-pins.json', 16384)
    require(digest(pins) == PINS_SHA, 'recorded primary pins changed')
    # Reuse the independently reviewed four-role COPY closure, without invoking a command.
    require(digest(read(root, "scripts/experience-dirty-build.py")) == "429ccf766a4fd77c2fd8db35e01f926349276dabe33af390a221ae9bd4b9fe28", "four-role closure implementation drift")
    spec = importlib.util.spec_from_file_location('dirty_freeze', root/'scripts/experience-dirty-build.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.profile_check(root, original=True)
    build = module.inventory(root, limited=True)['entries']
    names = {'scripts/experience-prepare.py', 'scripts/experience-dirty-build.py', 'deploy/local/migrate-postgres.sh'}
    for folder in ('deploy/experience', 'migrations', 'internal/platform/capacityprobe',
                   'cmd/switchback-capacity-probe', 'test/acceptance/experienceprepare'):
        base = canonical(root/folder)
        for parent, dirs, leaves in os.walk(base, followlinks=False):
            dirs[:] = sorted(d for d in dirs if d != '__pycache__')
            for d in dirs:
                canonical(Path(parent)/d)
            for leaf in sorted(leaves):
                if leaf.endswith('.pyc'):
                    continue
                names.add(str((Path(parent)/leaf).relative_to(root)))
    extra = [{'path':name, 'bytes':len(data), 'sha256':digest(data)}
             for name in sorted(names) for data in [read(root,name)]]
    bindings = {'four_image_copy_closure':build, 'deployment_probe_script_migrations_tests':extra}
    return {'schema':'switchback.experience.preparation.v1', 'status':'preparation_blocked',
            'execution_enabled':False, 'capacity_source':None, 'trust':None,
            'render_permissions':{'output_root':'0700', 'default_file':'0600', 'migration_directories':'0755',
                                  'nonsecret_bind_files':{name:'0644' for name in
                                      ['init-postgres.sh','migrate-postgres.sh']+
                                      [entry['path'] for entry in extra if entry['path'].startswith('migrations/') and entry['path'].endswith('.sql')]}},
            'pins_sha256':digest(pins), 'source_sha256':digest(encoded(bindings)), 'bindings':bindings,
            'objects':{'vm':'switchback-core','disk':'sbquota','compose_project':'switchback-core1011',
                       'guest_root':'/mnt/lima-sbquota','web_host_port':44175,'backend_host_port':48081},
            'actual':{'engine':None,'filesystem':None,'boot':None,'mount_namespace':None,'host_backing':None,
                      'image_ids':dict.fromkeys(('backend','web','worker','assistant'))},
            'blocking_unknowns':UNKNOWN}


def render(root, output, expected=None):
    root = canonical(root)
    manifest = plan(root)
    require(expected is None or expected == manifest['source_sha256'], 'source digest drift')
    require(os.path.isabs(os.fspath(output)) and os.path.normpath(os.fspath(output)) == os.fspath(output), "canonical output required")
    output = Path(output)
    require(str(output).startswith('/') and os.path.normpath(str(output)) == str(output), 'canonical output required')
    parent = canonical(output.parent)
    require(not output.is_relative_to(root/'deploy') and not output.is_relative_to(root/'scripts') and
            not output.is_relative_to(root/'internal') and not output.is_relative_to(root/'cmd') and
            not output.is_relative_to(root/'test') and not output.is_relative_to(root/'migrations') and
            not output.is_relative_to(root/'web') and not output.is_relative_to(root/'images'), 'output overlaps source')
    # Only these non-secret bind leaves are consumed by the non-owner PG UID.
    def material_mode(name):
        if name in ('init-postgres.sh', 'migrate-postgres.sh') or (name.startswith('migrations/') and name.endswith('.sql')):
            return 0o644
        return 0o600

    pfd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        parent_info = os.fstat(pfd)
        os.mkdir(output.name, mode=0o700, dir_fd=pfd)
        fd = os.open(output.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=pfd)
        try:
            os.fchmod(fd, 0o700)
            # All paths and bytes selected before publication; no user-supplied template substitution.
            payload = {'manifest.json':encoded(manifest), 'collector-manifest.json':encoded({
                'schema':'switchback.capacity.collection_manifest.v1', 'profile':'switchback-core1011',
                'socket':'/var/run/docker.sock', 'root':'/mnt/lima-sbquota'})}
            for entry in manifest['bindings']['deployment_probe_script_migrations_tests']:
                name = entry['path']
                if name.startswith('deploy/experience/'):
                    payload[name.removeprefix('deploy/experience/')] = read(root,name)
                elif name.startswith('migrations/'):
                    payload[name] = read(root,name)
                elif name == 'deploy/local/migrate-postgres.sh':
                    payload['migrate-postgres.sh'] = read(root,name)
            require(plan(root) == manifest, 'source changed during render')
            for name, data in payload.items():
                parts = Path(name).parts
                current = os.dup(fd)
                try:
                    prefix = []
                    for part in parts[:-1]:
                        prefix.append(part)
                        directory_mode = 0o755 if prefix[0] == 'migrations' else 0o700
                        try: os.mkdir(part, mode=directory_mode, dir_fd=current)
                        except FileExistsError: pass
                        nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=current)
                        os.fchmod(nxt, directory_mode)
                        os.close(current); current = nxt
                    leaf = os.open(parts[-1], os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=current)
                    with os.fdopen(leaf, 'wb') as f:
                        os.fchmod(f.fileno(), material_mode(name))
                        f.write(data)
                finally:
                    os.close(current)
            require(os.path.samestat(parent_info,parent.lstat()) and
                    os.path.samestat(os.fstat(fd),output.lstat()), 'output directory replaced')
        finally:
            os.close(fd)
    finally:
        os.close(pfd)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('plan','render'))
    parser.add_argument('--source', required=True)
    parser.add_argument('--output')
    parser.add_argument('--expected-source-sha256')
    args = parser.parse_args()
    try:
        require(args.mode != 'render' or args.output, 'render needs a new output directory')
        value = plan(args.source) if args.mode == 'plan' else render(args.source,args.output,args.expected_source_sha256)
        print(json.dumps(value,sort_keys=True,indent=2))
    except (ValueError,OSError) as exc:
        parser.exit(2,'preparation rejected: '+str(exc)+'\n')

if __name__ == '__main__':
    main()
