"""Exclusive offline bundle built downstream of one complete qualification kit.

Path arguments are private library/test seams. There is no runtime CLI override,
guest operation, subprocess, socket connection, receipt input or repair path.
"""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat

from contract import FILES, SOURCE_PREFIX, _digest, _json, _object, _require, validate_configuration

_ENGINE_LEAVES = frozenset((*FILES, 'contract.py', 'bundle.py', 'guest.py', 'prepare.py',
    'store.py', 'binaries.py', 'fd_lifecycle.py', 'test_fd_lifecycle.py', 'testdata/fd-lifecycle-corpus.json', 'test_contract.py', 'test_prepare.py', 'test_guest.py', 'test_store.py', 'README.md',
    'testdata/receipt-v1.json', 'testdata/receipt-v2.json', 'test_listener_v2.py',
    'testdata/listener-v2-corpus.json', 'testdata/listeners-kit21-v1.json',
    'testdata/listener-v2-provenance.json', 'test_default_bridge_store.py',
    'testdata/default-bridge-records.json', 'test_bridge_ipam.py',
    'testdata/default-bridge-ipam-records.json'))
_REQUIRED = frozenset((*FILES, 'contract.py', 'bundle.py', 'guest.py', 'prepare.py', 'store.py',
                       'binaries.py', 'fd_lifecycle.py', 'README.md', 'testdata/receipt-v1.json', 'testdata/receipt-v2.json'))
_UNRESOLVED = ('image_ids', 'container_ids', 'guest_boot', 'host_boot', 'public_identity',
    'host_volume_readback', 'engine_identity', 'native_receipt', 'host_receipt', 'trust',
    'manifest', 'journal', 'pg_preparation')
_BASE_RECORD = frozenset(('path', 'type', 'mode', 'bytes', 'digest'))
_BINARY_METADATA = frozenset(('role', 'goos', 'goarch', 'elf_class', 'elf_machine'))
_MAX_FILES = 8192
_MAX_FILE_BYTES = 1 << 28
_MAX_TOTAL_BYTES = 2 << 30


def _sha(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


def _encode(value):
    return json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode()


def _relative(value):
    _require(type(value) is str and 0 < len(value) <= 512 and '\x00' not in value
        and not value.startswith('/') and str(PurePosixPath(value)) == value
        and '..' not in PurePosixPath(value).parts, 'unsafe relative bundle path')
    return value


def _identity(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def _directory(path, private=False, check=lambda: None):
    check()
    path = Path(path)
    _require(path.is_absolute() and path.resolve() == path, 'directory must be absolute without links')
    check()
    info = path.lstat()
    check()
    _require(stat.S_ISDIR(info.st_mode) and info.st_uid in (0, os.geteuid())
             and not (info.st_mode & 0o022), 'unsafe directory owner/mode')
    _require(not private or stat.S_IMODE(info.st_mode) == 0o700, 'private directory mode differs')
    return info


def _read(root, relative, bound=_MAX_FILE_BYTES, check=lambda: None):
    """Read through no-follow directory FDs and compare both FD/path identities."""
    relative = _relative(relative)
    _directory(root, check=check)
    flags = os.O_RDONLY | os.O_NOFOLLOW
    check()
    descriptors = [os.open(root, flags | os.O_DIRECTORY)]
    try:
        parts = PurePosixPath(relative).parts
        for part in parts[:-1]:
            check()
            fd = os.open(part, flags | os.O_DIRECTORY, dir_fd=descriptors[-1])
            descriptors.append(fd)
            info = os.fstat(fd)
            _require(info.st_uid in (0, os.geteuid()) and not info.st_mode & 0o022,
                     'unsafe source ancestor')
        check()
        fd = os.open(parts[-1], flags, dir_fd=descriptors[-1])
        descriptors.append(fd)
        before = os.fstat(fd)
        _require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1
            and before.st_uid in (0, os.geteuid()) and not before.st_mode & 0o022
            and 0 < before.st_size <= bound, 'unsafe/oversized source leaf')
        chunks, remaining = [], before.st_size
        while remaining:
            check()
            chunk = os.read(fd, min(remaining, 1 << 20))
            check()
            _require(bool(chunk), 'short source read')
            chunks.append(chunk); remaining -= len(chunk)
        check()
        extra = os.read(fd, 1)
        check()
        _require(not extra, 'source grew during read')
        after = os.fstat(fd)
        named = os.stat(parts[-1], dir_fd=descriptors[-2], follow_symlinks=False)
        check()
        _require(_identity(before) == _identity(after) == _identity(named), 'source identity drift')
        return b''.join(chunks), stat.S_IMODE(before.st_mode)
    except OSError as exc:
        raise ValueError('unsafe/unreadable bundle input: ' + relative) from exc
    finally:
        for fd in reversed(descriptors):
            os.close(fd)


def _leaves(root, check=lambda: None):
    result, seen_directories, nodes = set(), set(), 0
    pending = [(Path(root), 0)]
    while pending:
        check()
        folder, depth = pending.pop()
        _require(depth <= 64, 'tree traversal depth bound')
        with os.scandir(folder) as iterator:
            for entry in iterator:
                check()
                nodes += 1
                _require(nodes <= _MAX_FILES * 2, 'tree traversal bound')
                path = folder / entry.name
                info = entry.stat(follow_symlinks=False)
                check()
                if stat.S_ISDIR(info.st_mode):
                    _require(info.st_uid in (0, os.geteuid()) and not info.st_mode & 0o022,
                             'unsafe tree directory/link')
                    seen_directories.add(str(path.relative_to(root)))
                    pending.append((path, depth + 1))
                else:
                    _require(stat.S_ISREG(info.st_mode), 'nonregular tree leaf')
                    result.add(str(path.relative_to(root)))
                    _require(len(result) <= _MAX_FILES, 'tree leaf bound')
    parents = set()
    for path in result:
        check()
        parents.update(str(parent) for parent in PurePosixPath(path).parents if str(parent) != '.')
    check()
    _require(seen_directories == parents, 'unlisted empty tree directory')
    return result


def _records(values, source=False, check=lambda: None):
    _require(type(values) is list and 0 < len(values) <= _MAX_FILES, 'record count/type')
    seen, ordered, total = set(), [], 0
    for record in values:
        check()
        _require(type(record) is dict and _BASE_RECORD <= set(record)
            and set(record) <= (_BASE_RECORD if source else _BASE_RECORD | _BINARY_METADATA), 'record properties')
        path = _relative(record['path'])
        _require(path not in seen and path != 'manifest.json'
            and 'deployment-receipt.json' not in PurePosixPath(path).parts
            and not any(p.startswith('bundle-') or p in ('private', 'secrets', 'trust.json')
                        for p in PurePosixPath(path).parts), 'duplicate/cyclic/runtime source record')
        seen.add(path)
        ordered.append(path)
        _require(record['type'] == 'regular' and type(record['mode']) is int
            and record['mode'] in (0o400, 0o500, 0o600, 0o644, 0o700, 0o755), 'record mode/type')
        _require(type(record['bytes']) is int and 0 < record['bytes'] <= _MAX_FILE_BYTES, 'record byte bound')
        total += record['bytes']; _require(total <= _MAX_TOTAL_BYTES, 'aggregate kit byte bound')
        _digest(record['digest'])
    check()
    _require(ordered == sorted(seen), 'records must have canonical path order')
    return seen


def _load_kit(kit, check=lambda: None):
    kit = Path(kit)
    _directory(kit, check=check)
    raw, _ = _read(kit, 'manifest.json', 1 << 22, check)
    # Kit manifests may cover thousands of files; shared decoder remains bounded.
    _require(len(raw) <= 1 << 20, 'kit manifest JSON bound')
    manifest = _json(raw)
    _object(manifest, ('schema', 'runtime_qualification', 'toolchain', 'source_input_root',
        'qualification_templates_root', 'files', 'unresolved_runtime'), 'qualification manifest')
    _require(manifest['schema'] == 'switchback.qualification.offline_kit.v1'
        and manifest['runtime_qualification'] is False and manifest['toolchain'] == 'go1.26.4', 'offline kit identity')
    _object(manifest['unresolved_runtime'], _UNRESOLVED, 'unresolved runtime')
    _require(all(v is None for v in manifest['unresolved_runtime'].values()), 'kit fabricated runtime identities')
    paths = _records(manifest['files'], check=check)
    _require(_leaves(kit, check) == paths | {'manifest.json'}, 'kit manifest coverage differs')
    material = {}
    for record in manifest['files']:
        check()
        data, mode = _read(kit, record['path'], check=check)
        _require(len(data) == record['bytes'] and mode == record['mode'] and _sha(data) == record['digest'],
                 'kit source bytes/hash/mode drift: ' + record['path'])
        check()
        # Keep only bounded JSON and selected deployment source bytes in memory.
        if record['path'].startswith('source/' + SOURCE_PREFIX) or record['path'] in (
                'source/deploy/experience/primary-pins.json', 'kit-inputs-arm64.json',
                'kit-inputs-amd64.json', 'build-receipt.json'):
            material[record['path']] = data
    inputs = []
    for arch in ('arm64', 'amd64'):
        check()
        value = _json(material['kit-inputs-' + arch + '.json'])
        _object(value, ('schema', 'architecture', 'toolchain', 'source', 'artifacts', 'templates'), 'kit inputs')
        _require(value['schema'] == 'switchback.qualification.kit_inputs.v1'
            and value['architecture'] == arch and value['toolchain'] == 'go1.26.4'
            and type(value['artifacts']) is list and len(value['artifacts']) <= 32, 'kit input identity')
        source_paths = _records(value['source'], source=True, check=check)
        template_paths = _records(value['templates'], source=True, check=check)
        _require(not source_paths & template_paths, 'source/template overlap')
        expected_paths, copied_paths = set(), set()
        for path in source_paths | template_paths:
            check()
            expected_paths.add('source/' + path)
        for path in paths:
            check()
            if path.startswith('source/'):
                copied_paths.add(path)
        _require(expected_paths == copied_paths, 'Source/template complete coverage differs')
        indexed = {}
        for record in manifest['files']:
            check()
            indexed[record['path']] = record
        for source_record in value['source'] + value['templates']:
            check()
            copied = dict(indexed['source/' + source_record['path']]); copied['path'] = source_record['path']
            _require(copied == source_record, 'kit Source record differs from copied bytes')
        _require(_sha(_encode(value['source'])) == manifest['source_input_root']
            and _sha(_encode(value['templates'])) == manifest['qualification_templates_root'], 'SourceRoot/TemplatesRoot differs')
        inputs.append(value)
    _require(inputs[0]['source'] == inputs[1]['source'] and inputs[0]['templates'] == inputs[1]['templates'],
             'architecture source/template mismatch')
    build = _json(material['build-receipt.json'])
    _object(build, ('go', 'source_input_root', 'qualification_templates_root', 'before', 'after',
                    'templates_before', 'templates_after', 'binaries'), 'kit build receipt')
    _require(build['before'] == build['after'] == inputs[0]['source']
        and build['templates_before'] == build['templates_after'] == inputs[0]['templates']
        and build['source_input_root'] == manifest['source_input_root']
        and build['qualification_templates_root'] == manifest['qualification_templates_root'], 'kit changed inputs')
    sources, leaves = [], set()
    for record in inputs[0]['source']:
        check()
        if record['path'].startswith(SOURCE_PREFIX) or record['path'] == 'deploy/experience/primary-pins.json':
            sources.append(record)
            if record['path'].startswith(SOURCE_PREFIX):
                leaves.add(record['path'][len(SOURCE_PREFIX):])
    _require(_REQUIRED <= leaves <= _ENGINE_LEAVES, 'incomplete/unknown Engine source inputs')
    pins = _json(material['source/deploy/experience/primary-pins.json'])
    _require(pins.get('schema') == 'switchback.experience.primary_pins.v1', 'pins schema')
    packages = pins.get('docker_packages')
    _require(type(packages) is list and len(packages) == 5, 'package pin coverage')
    _require(len({p.get('package') for p in packages if type(p) is dict}) == 5, 'duplicate package pin')
    from contract import PACKAGE_PINS
    for package, version, _, digest in PACKAGE_PINS.values():
        check()
        matching = [p for p in packages if p.get('package') == package]
        _require(len(matching) == 1 and matching[0].get('version') == version
            and matching[0].get('architecture') == 'arm64' and 'sha256:' + matching[0].get('sha256', '') == digest,
            'current pinned daemon packages differ')
    validate_configuration({name: material['source/' + SOURCE_PREFIX + name] for name in FILES})
    check()
    return manifest, raw, sources, material


def _manifest(kit_manifest, kit_raw, sources, check=lambda: None):
    files = []
    for record in sources:
        check()
        files.append(dict(record, path='source/' + record['path']))
    return {'schema': 'switchback.engine.offline_bundle.v1',
        'kit_manifest_sha256': _sha(kit_raw), 'source_root': kit_manifest['source_input_root'],
        'templates_root': kit_manifest['qualification_templates_root'], 'source_records': sources,
        'files': files, 'runtime_qualification': False, 'capacity_ready': False,
        'execution_enabled': False, 'paid_consumed': False}


def _fsync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try: os.fsync(fd)
    finally: os.close(fd)


def _exclusive(path, data, mode):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        os.fchmod(fd, mode)
        view = memoryview(data)
        while view:
            n = os.write(fd, view); _require(n > 0, 'short bundle write'); view = view[n:]
        os.fsync(fd)
    finally: os.close(fd)
    _fsync_directory(path.parent)


def build_bundle(kit: Path, destination: Path) -> dict:
    """Create only a fresh bundle-NNNN; never clean or overwrite partial output."""
    try:
        kit, destination = Path(kit), Path(destination)
        _directory(destination.parent)
        _require(re.fullmatch(r'bundle-[0-9]{4}', destination.name) is not None, 'bundle phase-local name')
        manifest, raw, sources, material = _load_kit(kit)
        result = _manifest(manifest, raw, sources)
        destination.mkdir(mode=0o700, exist_ok=False)
        destination.chmod(0o700); _fsync_directory(destination.parent)
        for record in result['files']:
            path = destination / record['path']
            missing = []
            parent = path.parent
            while parent != destination and not parent.exists():
                missing.append(parent); parent = parent.parent
            for directory in reversed(missing):
                directory.mkdir(mode=0o700, exist_ok=False); _fsync_directory(directory.parent)
            _exclusive(path, material[record['path']], record['mode'])
        _exclusive(destination / 'manifest.json', _encode(result), 0o600)
        # Re-scan upstream bytes and verify all output after durable exclusive writes.
        return verify_bundle(destination, kit)
    except (OSError, KeyError, TypeError, AttributeError) as exc:
        raise ValueError('bundle build rejected; preserve any partial output') from exc


def verify_bundle(bundle: Path, kit: Path, *, check=lambda: None) -> dict:
    """Read-only complete coverage, byte, mode and upstream identity verification."""
    try:
        bundle, kit = Path(bundle), Path(kit)
        _directory(bundle, private=True, check=check)
        kit_manifest, kit_raw, sources, _ = _load_kit(kit, check)
        raw, mode = _read(bundle, 'manifest.json', 1 << 20, check)
        actual = _json(raw); expected = _manifest(kit_manifest, kit_raw, sources, check)
        _require(actual == expected and mode == 0o600, 'bundle manifest identity/properties/mode differs')
        expected_paths = {'manifest.json'}
        for record in expected['files']:
            check()
            expected_paths.add(record['path'])
        _require(_leaves(bundle, check) == expected_paths, 'bundle coverage differs')
        for record in expected['files']:
            check()
            data, mode = _read(bundle, record['path'], check=check)
            _require(len(data) == record['bytes'] and mode == record['mode'] and _sha(data) == record['digest'],
                     'bundle leaf drift: ' + record['path'])
        check()
        return actual
    except (OSError, KeyError, TypeError, AttributeError) as exc:
        raise ValueError('bundle verification rejected') from exc
