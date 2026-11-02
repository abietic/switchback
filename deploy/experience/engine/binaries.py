"""Read-only fixed Debian pin to installed ELF proof; no CLI or extraction.

The six historical bootstrap leaf hashes live in contract.RETAINED_INPUTS.
A missing or mismatched retained archive is a hard preflight failure. This file
never downloads, extracts to disk, installs, replaces or executes archive code.
"""
import io
from pathlib import Path
import re
import stat
import struct
import tarfile

from contract import DATA_ROOT, PACKAGE_PINS, _require
from prepare import _sha

_PACKAGE_ROOT = Path(DATA_ROOT) / 'bootstrap-1013'


def _ar_binary(raw, executable, check):
    """Read pinned Debian archive without extraction, installation or execution."""
    _require(raw.startswith(b'!<arch>\n') and len(raw) <= 64 << 20, 'Debian archive format/bound')
    offset, members = 8, {}
    while offset < len(raw):
        check()
        _require(offset + 60 <= len(raw), 'Debian ar header truncated')
        header = raw[offset:offset + 60]
        _require(header[58:] == b'`\n', 'Debian ar header magic')
        name = header[:16].decode('ascii').strip().rstrip('/')
        size = int(header[48:58])
        _require(name not in members and name in ('debian-binary', 'control.tar', 'control.tar.gz',
            'control.tar.xz', 'data.tar', 'data.tar.gz', 'data.tar.xz') and 0 < size <= 64 << 20,
            'unknown Debian archive member/size')
        begin, end = offset + 60, offset + 60 + size
        _require(end <= len(raw), 'Debian ar payload truncated')
        members[name] = raw[begin:end]
        offset = end + (size % 2)
        if size % 2:
            _require(raw[end:offset] == b'\n', 'Debian ar alignment byte')
    data_members = [name for name in members if name.startswith('data.tar')]
    _require(offset == len(raw) and len(members) == 3 and members.get('debian-binary') == b'2.0\n'
             and len(data_members) == 1, 'Debian complete member coverage')
    found, total, count = None, 0, 0
    with tarfile.open(fileobj=io.BytesIO(members[data_members[0]]), mode='r|*') as archive:
        for member in archive:
            check()
            count += 1
            total += member.size
            _require(count <= 16384 and total <= 512 << 20 and member.size <= 128 << 20,
                     'Debian tar enumeration/decompression bound')
            name = member.name[2:] if member.name.startswith('./') else member.name
            _require(not name.startswith('/') and '..' not in Path(name).parts, 'Debian tar traversal')
            if '/' + name != executable:
                continue
            _require(found is None and member.isfile() and member.uid == member.gid == 0
                     and member.mode == 0o755, 'Debian binary identity/type/duplicate')
            stream = archive.extractfile(member)
            payload = bytearray()
            while True:
                check()
                chunk = stream.read(65536)
                if not chunk:
                    break
                payload.extend(chunk)
                _require(len(payload) <= member.size, 'Debian binary grew')
            _require(len(payload) == member.size, 'Debian binary truncated')
            found = bytes(payload)
    _require(found is not None, 'pinned executable missing from Debian archive')
    return found



def _collect_binaries(io_ops):
    binaries = []
    for path, (package, package_version, version, package_sha256) in PACKAGE_PINS.items():
        raw, value = io_ops.read(path, 128 << 20, 0o755)
        io_ops.record.raw('installed-ELF-before-validation', {'path': path, 'sha256': _sha(raw),
                                                          'header': raw[:64]})
        _require(value.st_gid == 0 and raw[:6] == b'\x7fELF\x02\x01' and len(raw) >= 64
                 and struct.unpack_from('<H', raw, 18)[0] == 183, 'installed arm64 ELF identity')
        query = io_ops.command(['/usr/bin/dpkg-query', '--show',
            '--showformat=${binary:Package}\t${Version}\t${Architecture}\t${db:Status-Status}\n', package])
        parts = query.decode().strip().split('\t')
        _require(parts in ([package, package_version, 'arm64', 'installed'],
                          [package + ':arm64', package_version, 'arm64', 'installed']), 'installed dpkg identity')
        owner = io_ops.command(['/usr/bin/dpkg-query', '--search', path]).decode().strip()
        _require(owner in (package + ': ' + path, package + ':arm64: ' + path), 'dpkg file owner differs')
        leaf = (package + '_' + package_version.removeprefix('5:') + '_arm64.deb')
        archive, _ = io_ops.read(_PACKAGE_ROOT / leaf, 64 << 20)
        io_ops.record.raw('pinned-package-archive', {'path': str(_PACKAGE_ROOT / leaf), 'sha256': _sha(archive)})
        _require(_sha(archive) == package_sha256, 'actual retained Debian archive pin differs')
        packaged = _ar_binary(archive, path, io_ops.check)
        _require(packaged == raw, 'installed ELF differs from exact pinned archive')
        # The daemon itself is untrusted until its full installed bytes match
        # the target ELF in the exact pinned archive. No daemon command on reject.
        reported = io_ops.command([path, '--version']).decode().strip()
        match = re.search(r'(?<![0-9.])v?' + re.escape(version) + r'(?![0-9.])', reported)
        _require(match is not None, 'installed daemon version differs')
        binaries.append({'path': path, 'package': package, 'package_version': package_version,
            'version': version, 'architecture': 'arm64', 'uid': value.st_uid, 'gid': value.st_gid,
            'mode': stat.S_IMODE(value.st_mode), 'nlink': value.st_nlink, 'sha256': _sha(raw),
            'package_sha256': _sha(archive), 'pin_verified': packaged == raw})
    return binaries

