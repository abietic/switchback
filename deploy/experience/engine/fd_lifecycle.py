"""Strict, bounded metadata sampling of a proc FD filesystem object.

The O_PATH descriptor pins an object, not an open-file-description. All
operations are metadata-only; source flags always come from source fdinfo.
"""
import ctypes
import errno
import os
import re
import stat
import struct
import sys
from types import SimpleNamespace


def _linux_directory_api():
    """Fixed private glibc Linux64 ABI; resolve lazily, never on import.

    glibc sysdeps/unix/sysv/linux/{bits/dirent.h,opendir.c,readdir64.c,
    closedir.c}: opendir owns one CLOEXEC FD, readdir64 leaves it open at
    EOF, closedir alone releases it. No scandir(fd) hidden duplicate.
    """
    if sys.platform != 'linux' or ctypes.sizeof(ctypes.c_void_p) != 8 or os.uname().machine not in ('aarch64', 'x86_64'):
        raise OSError(errno.ENOSYS, 'unsupported Linux directory ABI')
    libc = ctypes.CDLL('libc.so.6', use_errno=True)
    libc.opendir.argtypes, libc.opendir.restype = [ctypes.c_char_p], ctypes.c_void_p
    libc.readdir64.argtypes, libc.readdir64.restype = [ctypes.c_void_p], ctypes.c_void_p
    libc.closedir.argtypes, libc.closedir.restype = [ctypes.c_void_p], ctypes.c_int
    return libc


class _FDDirectory:
    """Own DIR until explicit close, including after complete enumeration.

    Only proc FD names are consumed. The ABI header has uint64 inode,
    int64 offset, uint16 reclen and uint8 type (name offset 19). Read each
    name within reclen and the Linux procfs NAME_MAX bound, never an
    unchecked sizeof(dirent). The caller bounds descriptor count and owns
    the final close so a cleanup error cannot be discarded or retried.
    """
    def __init__(self, path, check):
        self.check, self.eof, self.handle = check, False, None
        check()
        self.api = _linux_directory_api()
        check()
        self.handle = self.api.opendir(os.fsencode(path))
        if not self.handle:
            number = ctypes.get_errno()
            raise OSError(number or errno.EIO, 'opendir failed', path)

    def __iter__(self):
        return self

    def __next__(self):
        if self.handle is None:
            raise ValueError('FD directory already closed')
        if self.eof:
            raise StopIteration
        # Only these two non-FD names may be skipped; repeated dot entries
        # fail closed instead of creating an unbounded internal loop.
        for unused in range(3):
            self.check()
            ctypes.set_errno(0)
            entry = self.api.readdir64(self.handle)
            if not entry:
                number = ctypes.get_errno()
                if number:
                    raise OSError(number, 'readdir64 failed')
                self.eof = True
                raise StopIteration
            reclen = struct.unpack('=QqHB', ctypes.string_at(entry, 19))[2]
            if not 20 <= reclen <= 280:
                raise ValueError('invalid proc FD directory record size')
            raw = ctypes.string_at(entry + 19, min(reclen - 19, 256))
            name, terminator, unused = raw.partition(b'\0')
            if not terminator or not name or len(name) > 255:
                raise ValueError('invalid proc FD directory name')
            if name not in (b'.', b'..'):
                return SimpleNamespace(name=name.decode('ascii', 'strict'))
        raise ValueError('duplicate proc FD dot entries')

    def close(self):
        if self.handle is not None:
            handle, self.handle = self.handle, None
            # Never retry closedir: a failure can still have released the FD.
            if self.api.closedir(handle) != 0:
                number = ctypes.get_errno()
                raise OSError(number or errno.EIO, 'closedir failed')


class FDSamplingRace(Exception):
    def __init__(self, pid, fd, primitive, original, anchor=None, partial=None):
        super().__init__('PID %s FD %s %s: %s' % (pid, fd, primitive, original))
        self.pid, self.fd, self.primitive = pid, fd, primitive
        self.original, self.anchor, self.partial = original, anchor, partial


class SamplingExhausted(Exception):
    def __init__(self, attempts):
        super().__init__('FD sampling exhausted after %d attempts' % len(attempts))
        self.attempts = tuple(attempts)


class CleanupFailure(Exception):
    def __init__(self, primary, errors):
        super().__init__('FD sampling cleanup failed: primary=%r; close=%r' % (primary, errors))
        self.primary, self.errors = primary, tuple(errors)


class SourceVanished(Exception):
    """Only the precise source metadata syscall, never recording or cleanup."""
    def __init__(self, primitive, original):
        super().__init__(str(original))
        self.primitive, self.original = primitive, original


def decode_fdinfo(raw):
    text = raw.decode('ascii')
    fields = {}
    for line in text.splitlines():
        name, sep, value = line.partition(':')
        if name not in ('flags', 'mnt_id', 'ino'):
            continue
        if not sep or name in fields:
            raise ValueError('duplicate or malformed fdinfo field: ' + name)
        value = value.strip()
        pattern = '[0-7]+' if name == 'flags' else '[0-9]+'
        if re.fullmatch(pattern, value) is None:
            raise ValueError('invalid fdinfo field: ' + name)
        fields[name] = int(value, 8 if name == 'flags' else 10)
    if (set(fields) != {'flags', 'mnt_id', 'ino'}
            or fields['flags'] >= 1 << 32 or fields['mnt_id'] >= 1 << 64 or fields['ino'] >= 1 << 64):
        raise ValueError('missing or invalid required fdinfo field')
    return fields


def _source(primitive, operation):
    try:
        return operation()
    except OSError as error:
        if error.errno == errno.ENOENT:
            raise SourceVanished(primitive, error) from error
        raise


def _target(value):
    if type(value) is not str or '\0' in value or len(value.encode('utf-8', 'strict')) > 4096:
        raise ValueError('invalid FD target')
    return value


def sample_fd(pid, fd, read_source_fdinfo, read_pin_fdinfo, check):
    """Return coherent source facts or throw; caller proves process identity."""
    path = '/proc/%d/fd/%d' % (pid, fd)
    pin = None
    primary = None
    result = None
    try:
        check()
        pin = _source('pin-open', lambda: os.open(path, 0o10000000 | os.O_CLOEXEC))
        check()
        pinned = os.fstat(pin)
        check()
        pin_target = _target(os.readlink('/proc/self/fd/%d' % pin))
        check()
        pin_info = decode_fdinfo(read_pin_fdinfo(pin))
        if pinned.st_ino != pin_info['ino']:
            raise ValueError('invalid pin metadata')
        check()
        target1 = _target(_source('source-readlink-before', lambda: os.readlink(path)))
        check()
        st1 = _source('source-stat-before', lambda: os.stat(path))
        check()
        source_info = decode_fdinfo(read_source_fdinfo(pid, fd))
        check()
        target2 = _target(_source('source-readlink-after', lambda: os.readlink(path)))
        check()
        st2 = _source('source-stat-after', lambda: os.stat(path))
        check()
        source_info_after = decode_fdinfo(read_source_fdinfo(pid, fd))
        if source_info_after['flags'] != source_info['flags']:
            raise ValueError('source FD flags drift')
        identity = lambda x: (x.st_dev, x.st_ino, stat.S_IFMT(x.st_mode))
        if (target1 != pin_target or target2 != pin_target or identity(st1) != identity(pinned)
                or identity(st2) != identity(pinned) or source_info['ino'] != pin_info['ino']
                or source_info['mnt_id'] != pin_info['mnt_id']
                or source_info_after['ino'] != pin_info['ino']
                or source_info_after['mnt_id'] != pin_info['mnt_id']):
            raise FDSamplingRace(pid, fd, 'object-drift', ValueError('FD object identity changed'))
        match = re.fullmatch(r'socket:\[([0-9]+)\]', pin_target)
        result = {'fd': fd, 'target': pin_target, 'flags': source_info['flags'],
                  'device': pinned.st_dev, 'inode': pinned.st_ino, 'mount_id': source_info['mnt_id'],
                  'socket_inode': int(match.group(1)) if match else None}
    except BaseException as error:
        primary = error
    close_errors = []
    if pin is not None:
        try:
            os.close(pin)
        except BaseException as error:
            close_errors.append(error)
    if close_errors:
        raise CleanupFailure(primary, close_errors) from primary
    if primary is not None:
        raise primary
    return result
