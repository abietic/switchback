"""Hand-encoded bbolt format2 tests; no bbolt/containerd executable is used."""
import hashlib
import copy
import json
import os
from pathlib import Path
import re
import stat
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from contract import DATA_ROOT, validate_store_inventory
from store import _decode_bbolt, _classify, _scan_store

# Exact raw stat observation frames selected from the immutable host packet.
# The extraction receipt in task-1 binds their observation indices and hashes.
PACKET_STAT_FRAMES = (
    b'{"kind":"observation","value":{"step":"preflight","kind":"store-stat-before-validation","value":{"path":"/mnt/lima-sbquota/docker","filesystem_path":"/mnt/lima-sbquota/docker","path_base64":"L21udC9saW1hLXNicXVvdGEvZG9ja2Vy","path_bytes":24,"path_sha256":"sha256:642c4485802049c630e87dba95a4ad1f5f7dadd5b7209275f33cc57edd09afe4","path_truncated":false,"stat":{"device":64785,"inode":132,"rdev":0,"mode":16840,"uid":0,"gid":0,"nlink":13,"bytes":233,"mtime_ns":1791914307433034021,"ctime_ns":1791914307433034021}}}}',
    b'{"kind":"observation","value":{"step":"preflight","kind":"store-stat-before-validation","value":{"path":"/mnt/lima-sbquota/docker/overlay2","filesystem_path":"/mnt/lima-sbquota/docker/overlay2","path_base64":"L21udC9saW1hLXNicXVvdGEvZG9ja2VyL292ZXJsYXky","path_bytes":33,"path_sha256":"sha256:eaaf4fbd8e2b9485697e3b21707ea0367364e16be47fd615d98d2728ad34d5b7","path_truncated":false,"stat":{"device":64785,"inode":67240077,"rdev":0,"mode":16840,"uid":0,"gid":0,"nlink":3,"bytes":40,"mtime_ns":1791914307431008304,"ctime_ns":1791914307431008304}}}}',
    b'{"kind":"observation","value":{"step":"preflight","kind":"store-stat-before-validation","value":{"path":"/mnt/lima-sbquota/docker/overlay2/backingFsBlockDev","filesystem_path":"/mnt/lima-sbquota/docker/overlay2/backingFsBlockDev","path_base64":"L21udC9saW1hLXNicXVvdGEvZG9ja2VyL292ZXJsYXkyL2JhY2tpbmdGc0Jsb2NrRGV2","path_bytes":51,"path_sha256":"sha256:fb802dc326683873c6402c73cc9f574a7834c78eedfb9ab3c59baca190b3728e","path_truncated":false,"stat":{"device":64785,"inode":67240078,"rdev":64785,"mode":24960,"uid":0,"gid":0,"nlink":1,"bytes":0,"mtime_ns":1791914307415815430,"ctime_ns":1791914307415815430}}}}',
    b'{"kind":"observation","value":{"step":"preflight","kind":"store-stat-before-validation","value":{"path":"/mnt/lima-sbquota/docker/volumes","filesystem_path":"/mnt/lima-sbquota/docker/volumes","path_base64":"L21udC9saW1hLXNicXVvdGEvZG9ja2VyL3ZvbHVtZXM=","path_bytes":32,"path_sha256":"sha256:26f94655d3d1b2e0b80e15e4647b3028a073ae4eb5ebb6a48274d8265e86d16c","path_truncated":false,"stat":{"device":64785,"inode":191,"rdev":0,"mode":16833,"uid":0,"gid":0,"nlink":2,"bytes":50,"mtime_ns":1791914307410751139,"ctime_ns":1791914307410751139}}}}',
    b'{"kind":"observation","value":{"step":"preflight","kind":"store-stat-before-validation","value":{"path":"/mnt/lima-sbquota/docker/volumes/backingFsBlockDev","filesystem_path":"/mnt/lima-sbquota/docker/volumes/backingFsBlockDev","path_base64":"L21udC9saW1hLXNicXVvdGEvZG9ja2VyL3ZvbHVtZXMvYmFja2luZ0ZzQmxvY2tEZXY=","path_bytes":50,"path_sha256":"sha256:4c61d30ec111c219908f4f7482637f5ac3cadb37f100fb259ad936a5c131ff8f","path_truncated":false,"stat":{"device":64785,"inode":72192,"rdev":64785,"mode":24960,"uid":0,"gid":0,"nlink":1,"bytes":0,"mtime_ns":1791914307410751139,"ctime_ns":1791914307410751139}}}}',
)

# Raw guest-frame-raw observations 247, 249, 253 (0-based) in the sealed
# packet; their full wire hashes are checked in task-1/fixture-provenance.json.
PLUGIN_PACKET_STATS = {
    DATA_ROOT + '/plugins': (189, 16832, 4, 32),
    DATA_ROOT + '/plugins/storage': (67240075, 16877, 2, 6),
    DATA_ROOT + '/plugins/tmp': (33554569, 16832, 2, 6),
}


class BackingDeviceScannerTests(unittest.TestCase):
    """Replay the three recorded nodes through the real scanner with fake syscalls."""

    filesystem = {'mountpoint': '/mnt/lima-sbquota', 'fs_type': 'xfs',
        'uuid': '8782738b-dfc2-4426-966f-6a7eb0889ddd', 'major': 253,
        'minor': 17, 'root_inode': 128, 'quota_enforced': True}

    def scan(self, changes=None, *, filesystem=None, fstat_change=None,
             path_change=None, root_fd_change=None, final_change=None,
             extra=None, coverage_drift=False, bound=True,
             name='backingFsBlockDev', regular=False, opath=0x200000,
             packet_paths=None, race_device=None, plugin_names=None,
             plugin_name_drift=False):
        # Exact modes/dev/inodes from the three preflight observations. The
        # failed packet omitted rdev; 64785 below is an independent synthetic
        # value for the success case, never a claim about the recorded node.
        root = DATA_ROOT
        overlay = root + '/overlay2'
        device = overlay + '/' + name
        def node(mode, inode, nlink, size, timestamp):
            return SimpleNamespace(st_dev=64785, st_ino=inode, st_mode=mode,
                st_uid=0, st_gid=0, st_nlink=nlink, st_size=size,
                st_mtime_ns=timestamp, st_ctime_ns=timestamp,
                st_rdev=0)
        stats = {
            root: node(16840, 132, 13, 233, 1791914307433034021),
            overlay: node(16840, 67240077, 3, 40, 1791914307431008304),
            device: node(24960, 67240078, 1, 0, 1791914307415815430),
        }
        stats[device].st_rdev = 64785
        if packet_paths is not None:
            stats = {}
            for frame in PACKET_STAT_FRAMES:
                observation = json.loads(frame)
                value = observation['value']['value']
                if value['path'] in packet_paths:
                    raw = value['stat']
                    stats[value['path']] = SimpleNamespace(**{
                        'st_dev': raw['device'], 'st_ino': raw['inode'], 'st_rdev': raw['rdev'],
                        'st_mode': raw['mode'], 'st_uid': raw['uid'], 'st_gid': raw['gid'],
                        'st_nlink': raw['nlink'], 'st_size': raw['bytes'],
                        'st_mtime_ns': raw['mtime_ns'], 'st_ctime_ns': raw['ctime_ns']})
            for path in packet_paths & PLUGIN_PACKET_STATS.keys():
                inode, mode, nlink, size = PLUGIN_PACKET_STATS[path]
                stats[path] = SimpleNamespace(st_dev=64785, st_ino=inode, st_rdev=0,
                    st_mode=mode, st_uid=0, st_gid=0, st_nlink=nlink, st_size=size,
                    st_mtime_ns=1791914307409738281, st_ctime_ns=1791914307409738281)
            self.assertEqual(set(stats), set(packet_paths))
        race_device = race_device or device
        if extra:
            stats[overlay + '/unexpected'] = node(extra, 67240079, 1, 0, 1791914307415815430)
        regular_path = root + '/engine-id'
        regular_raw = b'00000000-0000-4000-8000-000000000001'
        if regular:
            stats[regular_path] = node(stat.S_IFREG | 0o600, 67240080, 1,
                                       len(regular_raw), 1791914307415815430)
        if changes:
            changes(stats)
        fds, opened, observed, reads = {}, [], [], []
        self.last_opened = opened
        lookups = {}
        def resolve(path, dir_fd=None):
            return str(Path(fds[dir_fd]) / path) if dir_fd is not None else str(path)
        def lookup(path, dir_fd=None, follow_symlinks=False):
            self.assertFalse(follow_symlinks)
            key = resolve(path, dir_fd)
            lookups[key] = lookups.get(key, 0) + 1
            value = copy.copy(stats[key])
            if path_change and key == race_device and lookups[key] > 1:
                path_change(value)
            return value
        def fake_open(path, flags, dir_fd=None):
            key = resolve(path, dir_fd)
            opened.append((key, flags))
            if key in (device, root + '/volumes/backingFsBlockDev'):
                self.assertEqual(flags, opath | os.O_NOFOLLOW | os.O_CLOEXEC)
            elif key != regular_path:
                self.assertTrue(flags & os.O_DIRECTORY)
            fd = 100 + len(fds)
            fds[fd] = key
            return fd
        class Names:
            def __init__(self, fd): self.fd = fd
            def __enter__(self):
                path = fds[self.fd]
                if packet_paths is not None:
                    names = sorted(p.rsplit('/', 1)[-1] for p in stats
                                   if p != path and str(Path(p).parent) == path)
                else:
                    names = ['overlay2'] + (['engine-id'] if regular else []) if path == root else [name]
                if plugin_names and path in plugin_names:
                    names = list(plugin_names[path])
                if plugin_name_drift and path == root + '/plugins/storage':
                    calls = plugin_names_calls.get(path, 0)
                    if calls:
                        names = ['late-child']
                    plugin_names_calls[path] = calls + 1
                if path == overlay and extra: names.append('unexpected')
                if path == overlay and coverage_drift and names_calls[0] > 0: names = []
                if path == overlay: names_calls[0] += 1
                return iter(SimpleNamespace(name=name) for name in names)
            def __exit__(self, *args): return False
        names_calls = [0]
        plugin_names_calls = {}
        def fstat(fd):
            value = copy.copy(stats[fds[fd]])
            if fstat_change and fds[fd] == race_device:
                fstat_change(value)
            if root_fd_change and fds[fd] == root:
                root_fd_change(value)
            return value
        def read(fd, count):
            self.assertEqual(fds[fd], regular_path, 'device payload read')
            reads.append(count)
            return regular_raw if len(reads) == 1 else b''
        def final_stat(path):
            value = copy.copy(stats[str(path)])
            if final_change and str(path) == race_device:
                final_change(value)
            return value
        with (mock.patch('store.os.O_PATH', opath, create=True),
              mock.patch('store.os.stat', side_effect=lookup),
              mock.patch('store.os.open', side_effect=fake_open),
              mock.patch('store.os.fstat', side_effect=fstat),
              mock.patch('store.os.scandir', side_effect=Names),
              mock.patch('store.os.close'),
              mock.patch('store.os.read', side_effect=read),
              mock.patch.object(Path, 'lstat', new=final_stat)):
            result = _scan_store(expected_filesystem=(filesystem or self.filesystem) if bound else None,
                observe=lambda kind, value: observed.append((kind, value)))
        return result, opened, observed, reads

    def scan_actual_packet_nodes(self, suffixes=('', '/overlay2',
            '/overlay2/backingFsBlockDev', '/volumes', '/volumes/backingFsBlockDev'), **kwargs):
        # Metadata is decoded from immutable raw frames. The syscall responses,
        # absent nodes, and complete XFS filesystem wrapper are synthetic.
        return self.scan(packet_paths={DATA_ROOT + suffix for suffix in suffixes}, **kwargs)

    def scan_plugin_packet_nodes(self, suffixes=('/plugins/storage', '/plugins/tmp'), **kwargs):
        paths = ('', '/plugins') + suffixes if suffixes else ('',)
        return self.scan_actual_packet_nodes(paths, **kwargs)

    def test_packet_empty_plugin_directories_zero_one_two(self):
        for suffixes in ((), ('/plugins/storage',), ('/plugins/tmp',),
                         ('/plugins/storage', '/plugins/tmp')):
            with self.subTest(suffixes=suffixes):
                result, opened, _, reads = self.scan_plugin_packet_nodes(suffixes)
                self.assertEqual(result['schema'], 'switchback.engine.store_inventory.v4')
                self.assertEqual({entry['path'] for entry in result['entries']},
                                 {DATA_ROOT + suffix for suffix in (('', '/plugins') + suffixes
                                   if suffixes else ('',))})
                self.assertEqual(len(opened), len(result['entries']))
                self.assertEqual(reads, [])

    def test_plugin_empty_names_reject_before_child_payload_open(self):
        target = DATA_ROOT + '/plugins/storage'
        for child in ('container', 'nested-directory'):
            with self.subTest(child=child), self.assertRaisesRegex(ValueError,
                    re.escape(target)):
                self.scan_plugin_packet_nodes(('/plugins/storage',),
                    plugin_names={target: [child]})
            self.assertEqual([path for path, _ in self.last_opened],
                             [DATA_ROOT, DATA_ROOT + '/plugins', target])

    def test_plugin_directory_metadata_and_races_reject_with_path(self):
        target = DATA_ROOT + '/plugins/storage'
        changes = {
            'uid': lambda s: setattr(s[target], 'st_uid', 1),
            'gid': lambda s: setattr(s[target], 'st_gid', 1),
            'mode': lambda s: setattr(s[target], 'st_mode', stat.S_IFDIR | 0o700),
            'nlink': lambda s: setattr(s[target], 'st_nlink', 3),
            'bool-nlink': lambda s: setattr(s[target], 'st_nlink', True),
            'bool-uid': lambda s: setattr(s[target], 'st_uid', False),
            'bool-mode': lambda s: setattr(s[target], 'st_mode', True),
            'bool-device': lambda s: setattr(s[target], 'st_dev', True),
            'regular': lambda s: setattr(s[target], 'st_mode', stat.S_IFREG | 0o755),
            'foreign-device': lambda s: setattr(s[target], 'st_dev', 64786),
        }
        for label, change in changes.items():
            with self.subTest(label=label), self.assertRaisesRegex(ValueError, re.escape(target)):
                self.scan_plugin_packet_nodes(('/plugins/storage',), changes=change)
        races = (
            {'fstat_change': lambda s: setattr(s, 'st_ino', 9)},
            {'fstat_change': lambda s: setattr(s, 'st_rdev', False)},
            {'path_change': lambda s: setattr(s, 'st_ino', 9)},
            {'path_change': lambda s: setattr(s, 'st_rdev', False)},
            {'final_change': lambda s: setattr(s, 'st_ino', 9)},
            {'final_change': lambda s: setattr(s, 'st_rdev', False)},
            {'plugin_name_drift': True},
        )
        for case in races:
            with self.subTest(case=case), self.assertRaisesRegex(ValueError, re.escape(target)):
                self.scan_plugin_packet_nodes(('/plugins/storage',), race_device=target, **case)
        parent = DATA_ROOT + '/plugins'
        with self.assertRaisesRegex(ValueError, re.escape(parent)):
            self.scan_plugin_packet_nodes(('/plugins/storage',), race_device=parent,
                fstat_change=lambda s: setattr(s, 'st_ino', 9))
        with self.assertRaisesRegex(ValueError, re.escape(parent)):
            self.scan_plugin_packet_nodes(('/plugins/storage',), race_device=parent,
                fstat_change=lambda s: setattr(s, 'st_rdev', False))
        with self.assertRaises(ValueError):
            self.scan_plugin_packet_nodes(('/plugins/storage',), bound=False)

    def test_tmp_directory_and_other_plugin_path_reject(self):
        target = DATA_ROOT + '/plugins/tmp'
        for label, change in (
            ('mode', lambda s: setattr(s[target], 'st_mode', stat.S_IFDIR | 0o755)),
            ('owner', lambda s: setattr(s[target], 'st_gid', 1)),
            ('nlink', lambda s: setattr(s[target], 'st_nlink', 3)),
        ):
            with self.subTest(label=label), self.assertRaisesRegex(ValueError, re.escape(target)):
                self.scan_plugin_packet_nodes(('/plugins/tmp',), changes=change)
        other = DATA_ROOT + '/plugins/real-id'
        def rename(stats):
            stats[other] = stats.pop(target)
        with self.assertRaisesRegex(ValueError, re.escape(other)):
            self.scan_plugin_packet_nodes(('/plugins/tmp',), changes=rename)

    def test_actual_packet_five_nodes_v4(self):
        inventory, opened, _, reads = self.scan_actual_packet_nodes()
        self.assertEqual(inventory['schema'], 'switchback.engine.store_inventory.v4')
        self.assertEqual({x['path'] for x in inventory['entries'] if x['kind'] == 'block_device'},
                         {DATA_ROOT + '/overlay2/backingFsBlockDev',
                          DATA_ROOT + '/volumes/backingFsBlockDev'})
        self.assertEqual({x['device_identity']['inode'] for x in inventory['entries']
                          if x['kind'] == 'block_device'}, {67240078, 72192})
        self.assertEqual(len(opened), 5)
        self.assertEqual(reads, [])

    def test_fixed_block_device_is_metadata_only_v4(self):
        inventory, opened, observed, reads = self.scan()
        self.assertEqual(inventory['schema'], 'switchback.engine.store_inventory.v4')
        self.assertEqual(inventory['backing_filesystem'], self.filesystem)
        device = inventory['entries'][-1]
        self.assertEqual(device['kind'], 'block_device')
        self.assertEqual(device['device_identity'], {'device': 64785, 'inode': 67240078,
            'rdev': 64785, 'major': 253, 'minor': 17})
        self.assertEqual(len(opened), 3)
        self.assertEqual(reads, [])
        self.assertEqual(observed[-1][0], 'store-stat-before-validation')

    def test_regular_leaf_is_still_fully_read_under_v4(self):
        inventory, opened, _, reads = self.scan(regular=True)
        regular = next(item for item in inventory['entries'] if item['path'].endswith('/engine-id'))
        self.assertEqual(regular['kind'], 'regular')
        self.assertEqual(regular['sha256'], 'sha256:' + hashlib.sha256(
            b'00000000-0000-4000-8000-000000000001').hexdigest())
        self.assertEqual(len(reads), 2)
        self.assertEqual(len(opened), 4)

    def test_packet_paths_support_zero_one_or_two_devices(self):
        cases = (
            (('',), set()),
            (('', '/overlay2'), set()),
            (('', '/volumes'), set()),
            (('', '/overlay2', '/overlay2/backingFsBlockDev'),
             {DATA_ROOT + '/overlay2/backingFsBlockDev'}),
            (('', '/volumes', '/volumes/backingFsBlockDev'),
             {DATA_ROOT + '/volumes/backingFsBlockDev'}),
        )
        for suffixes, expected in cases:
            with self.subTest(suffixes=suffixes):
                inventory, opened, _, reads = self.scan_actual_packet_nodes(suffixes)
                self.assertEqual(inventory['schema'], 'switchback.engine.store_inventory.v4')
                self.assertEqual({x['path'] for x in inventory['entries']
                                  if x['kind'] == 'block_device'}, expected)
                self.assertEqual(reads, [])
                self.assertEqual(len(opened), len(suffixes))

    def test_packet_volumes_device_metadata_and_race_rejections(self):
        target = DATA_ROOT + '/volumes/backingFsBlockDev'
        mutations = {
            'uid': lambda s: setattr(s[target], 'st_uid', 1),
            'gid': lambda s: setattr(s[target], 'st_gid', 1),
            'mode': lambda s: setattr(s[target], 'st_mode', stat.S_IFBLK | 0o660),
            'nlink': lambda s: setattr(s[target], 'st_nlink', 2),
            'bool-nlink': lambda s: setattr(s[target], 'st_nlink', True),
            'size': lambda s: setattr(s[target], 'st_size', 1),
            'bool-size': lambda s: setattr(s[target], 'st_size', False),
            'missing-rdev': lambda s: delattr(s[target], 'st_rdev'),
            'bool-rdev': lambda s: setattr(s[target], 'st_rdev', True),
            'rdev': lambda s: setattr(s[target], 'st_rdev', 64786),
            'device': lambda s: setattr(s[target], 'st_dev', 64786),
            'inode': lambda s: setattr(s[target], 'st_ino', 0),
            'bool-inode': lambda s: setattr(s[target], 'st_ino', True),
            'char': lambda s: setattr(s[target], 'st_mode', stat.S_IFCHR | 0o600),
            'fifo': lambda s: setattr(s[target], 'st_mode', stat.S_IFIFO | 0o600),
            'socket': lambda s: setattr(s[target], 'st_mode', stat.S_IFSOCK | 0o600),
            'symlink': lambda s: setattr(s[target], 'st_mode', stat.S_IFLNK | 0o600),
        }
        for label, mutate in mutations.items():
            with self.subTest(label=label), self.assertRaises(ValueError):
                self.scan_actual_packet_nodes(changes=mutate)
        for label, options in (
            ('fd', {'fstat_change': lambda s: setattr(s, 'st_rdev', 64786)}),
            ('fd-bool-size', {'fstat_change': lambda s: setattr(s, 'st_size', False)}),
            ('pathname', {'path_change': lambda s: setattr(s, 'st_ino', 9)}),
            ('pathname-bool-size', {'path_change': lambda s: setattr(s, 'st_size', False)}),
            ('final', {'final_change': lambda s: setattr(s, 'st_ino', 9)}),
            ('final-bool-size', {'final_change': lambda s: setattr(s, 'st_size', False)}),
            ('root-fd', {'root_fd_change': lambda s: setattr(s, 'st_dev', 64786)}),
            ('coverage', {'coverage_drift': True}),
            ('no-opath', {'opath': None}),
        ):
            with self.subTest(label=label), self.assertRaises(ValueError):
                self.scan_actual_packet_nodes(race_device=target, **options)
        with self.assertRaises(ValueError):
            self.scan_actual_packet_nodes(filesystem={**self.filesystem, 'minor': 18})
        with self.assertRaises(ValueError):
            self.scan_actual_packet_nodes(filesystem={**self.filesystem, 'major': 8})
        with self.assertRaises(ValueError):
            self.scan_actual_packet_nodes(bound=False)
        with self.assertRaises(ValueError):
            self.scan_actual_packet_nodes(changes=lambda s: setattr(s[DATA_ROOT + '/volumes'],
                                                         'st_dev', 64786))
        def wrong_path(stats):
            stats[DATA_ROOT + '/volumes/other'] = stats.pop(target)
        with self.assertRaises(ValueError):
            self.scan_actual_packet_nodes(changes=wrong_path)
        def third_device(stats):
            stats[DATA_ROOT + '/volumes/extra'] = copy.copy(stats[target])
            stats[DATA_ROOT + '/volumes/extra'].st_ino += 1
        with self.assertRaises(ValueError):
            self.scan_actual_packet_nodes(changes=third_device)

    def test_recorded_failure_has_no_rdev_and_cannot_be_promoted(self):
        captured = {'mode': 24960, 'device': 64785, 'inode': 67240078,
                    'uid': 0, 'gid': 0, 'nlink': 1, 'bytes': 0}
        self.assertNotIn('rdev', captured)
        with self.assertRaises(ValueError):
            self.scan(lambda stats: delattr(stats[DATA_ROOT + '/overlay2/backingFsBlockDev'], 'st_rdev'))

    def test_fixed_device_rejects_bad_metadata_and_wrong_kind(self):
        device = DATA_ROOT + '/overlay2/backingFsBlockDev'
        changes = {
            'uid': lambda s: setattr(s[device], 'st_uid', 99),
            'gid': lambda s: setattr(s[device], 'st_gid', 99),
            'mode': lambda s: setattr(s[device], 'st_mode', stat.S_IFBLK | 0o660),
            'nlink': lambda s: setattr(s[device], 'st_nlink', 2),
            'size': lambda s: setattr(s[device], 'st_size', 1),
            'rdev': lambda s: setattr(s[device], 'st_rdev', 64786),
            'st_dev': lambda s: setattr(s[device], 'st_dev', 64786),
            'char': lambda s: setattr(s[device], 'st_mode', stat.S_IFCHR | 0o600),
            'fifo': lambda s: setattr(s[device], 'st_mode', stat.S_IFIFO | 0o600),
            'socket': lambda s: setattr(s[device], 'st_mode', stat.S_IFSOCK | 0o600),
            'symlink': lambda s: setattr(s[device], 'st_mode', stat.S_IFLNK | 0o600),
        }
        for label, change in changes.items():
            with self.subTest(label=label), self.assertRaises(ValueError): self.scan(change)
        with self.assertRaises(ValueError): self.scan(bound=False)
        with self.assertRaises(ValueError):
            self.scan(lambda s: setattr(s[DATA_ROOT], 'st_dev', 64786))
        with self.assertRaises(ValueError):
            self.scan(lambda s: setattr(s[DATA_ROOT + '/overlay2'], 'st_dev', 64786))
        with self.assertRaises(ValueError): self.scan(name='wrongName')
        for mode in (stat.S_IFBLK | 0o600, stat.S_IFCHR | 0o600,
                     stat.S_IFIFO | 0o600, stat.S_IFSOCK | 0o600):
            with self.subTest(extra=mode), self.assertRaises(ValueError): self.scan(extra=mode)

    def test_device_races_and_coverage_drift_reject(self):
        with self.assertRaises(ValueError):
            self.scan(fstat_change=lambda value: setattr(value, 'st_rdev', 64786))
        with self.assertRaises(ValueError):
            self.scan(root_fd_change=lambda value: setattr(value, 'st_dev', 64786))
        with self.assertRaises(ValueError):
            self.scan(path_change=lambda value: setattr(value, 'st_ino', 999))
        with self.assertRaises(ValueError):
            self.scan(final_change=lambda value: setattr(value, 'st_ino', 999))
        with self.assertRaises(ValueError): self.scan(coverage_drift=True)
        with self.assertRaises(ValueError): self.scan(opath=None)
        with self.assertRaises(ValueError):
            self.scan(filesystem={**self.filesystem, 'major': 8})


def fnv(raw):
    result = 14695981039346656037
    for byte in raw:
        result = ((result ^ byte) * 1099511628211) & ((1 << 64) - 1)
    return result


def leaf(items, page_id=0, padded=None):
    encoded = []
    for key, value in sorted(items.items()):
        bucket = isinstance(value, dict)
        if bucket:
            value = struct.pack('<QQ', 0, 0) + leaf(value)
        encoded.append((key, value, int(bucket)))
    offset = 16 + 16 * len(encoded)
    headers, content = bytearray(), bytearray()
    for index, (key, value, flags) in enumerate(encoded):
        headers.extend(struct.pack('<IIII', flags, offset - (16 + 16 * index), len(key), len(value)))
        content.extend(key + value)
        offset += len(key) + len(value)
    raw = struct.pack('<QHHI', page_id, 2, len(encoded), 0) + headers + content
    return raw if padded is None else raw.ljust(padded, b'\0')


def bolt(tree, second_tree=None, no_freelist=False):
    size = 4096
    second_tree = tree if second_tree is None else second_tree
    pages = [bytearray(size) for _ in range(6)]
    for index, txid, root in ((0, 2, 4), (1, 3, 5)):
        meta = struct.pack('<IIIIQQQQQ', 0xED0CDAED, 2, size, 0, root, 0,
                           (1 << 64) - 1 if no_freelist else 2, 6, txid)
        pages[index][:80] = struct.pack('<QHHI', index, 4, 0, 0) + meta + struct.pack('<Q', fnv(meta))
    # One current free page and an old root. Historical free bytes are not active records.
    pages[2][:32] = struct.pack('<QHHIQQ', 2, 16, 2, 0, 3, 4)
    pages[4][:] = leaf(tree, 4, size)
    pages[5][:] = leaf(second_tree, 5, size)
    return b''.join(pages)


class BoltTests(unittest.TestCase):
    def test_non_utf8_rejected_stat_survives_real_bounded_audit_encoding_and_save(self):
        import base64
        import stat
        import prepare
        for target in ('root', 'leaf'):
            for rejected_uid in (0, 999):
                with self.subTest(target=target, uid=rejected_uid), tempfile.TemporaryDirectory() as temporary:
                    audit = Path(temporary).resolve() / 'audit'; audit.mkdir(mode=0o700)
                    root = Path(os.fsdecode(b'/synthetic-\xff')) if target == 'root' else Path(DATA_ROOT)
                    name = os.fsdecode(b'unknown-\xff')
                    physical = root if target == 'root' else root / name
                    def info(directory):
                        return SimpleNamespace(st_dev=1, st_ino=2 if directory else 3,
                            st_mode=(stat.S_IFDIR | 0o700) if directory else (stat.S_IFREG | 0o600),
                            st_uid=0, st_gid=0, st_nlink=2 if directory else 1, st_size=4096,
                            st_mtime_ns=4, st_ctime_ns=5)
                    root_stat, leaf_stat = info(True), info(False)
                    (root_stat if target == 'root' else leaf_stat).st_uid = rejected_uid
                    saved, opened_payload, directory_opens = [], [], []
                    def emit(observation):
                        path = audit / ('observation-%03d.json' % len(saved))
                        prepare._exclusive(path, prepare._encode(observation), 0o600, os.getuid())
                        saved.append(path)
                    record = prepare._Record({'observations': []}, {'name': 'preflight'}, emit)
                    real_stat, real_open, real_fstat = os.stat, os.open, os.fstat
                    real_scan, real_close, real_lstat = os.scandir, os.close, Path.lstat
                    fake_fd = 1000000
                    def fake_stat(path, **kwargs):
                        if str(path) == str(root): return root_stat
                        if kwargs.get('dir_fd') == fake_fd: return leaf_stat
                        return real_stat(path, **kwargs)
                    def fake_open(path, flags, *args, **kwargs):
                        if str(path) == str(root):
                            directory_opens.append(str(path)); return fake_fd
                        if kwargs.get('dir_fd') == fake_fd:
                            opened_payload.append(str(path)); raise AssertionError('unsafe payload open')
                        return real_open(path, flags, *args, **kwargs)
                    def fake_scan(fd):
                        if fd != fake_fd: return real_scan(fd)
                        iterator = mock.MagicMock()
                        iterator.__enter__.return_value = iter([] if target == 'root' else [SimpleNamespace(name=name)])
                        return iterator
                    with (mock.patch('store.os.stat', side_effect=fake_stat),
                          mock.patch('store.os.open', side_effect=fake_open),
                          mock.patch('store.os.fstat', side_effect=lambda fd: root_stat if fd == fake_fd else real_fstat(fd)),
                          mock.patch('store.os.scandir', side_effect=fake_scan),
                          mock.patch.object(Path, 'lstat', new=lambda path: root_stat if str(path) == str(root) else real_lstat(path)),
                          mock.patch('store.os.close', side_effect=lambda fd: None if fd == fake_fd else real_close(fd))):
                        with self.assertRaises(ValueError): _scan_store(root=root, observe=record.raw)
                    decoded = [json.loads(path.read_bytes())['value'] for path in saved]
                    raw_path = os.fsencode(physical)
                    values = [v for v in decoded if v.get('path_sha256') == 'sha256:' + hashlib.sha256(raw_path).hexdigest()]
                    self.assertTrue(values, 'rejected raw filesystem bytes/stat must persist through actual JSON and exclusive save')
                    self.assertEqual(base64.b64decode(values[-1]['path_base64']), raw_path)
                    self.assertEqual(values[-1]['stat']['uid'], rejected_uid)
                    self.assertEqual(opened_payload, [])
                    self.assertEqual(directory_opens, [] if target == 'root' else [str(root)])

    def test_graphdriver_empty_indexes_reject_any_hidden_restoration_record(self):
        for name in ('buildkit/containerdmeta.db', 'buildkit/snapshots.db'):
            self.assertEqual(_classify(name, bolt({}))[0], 'buildkit_empty_index')
            for records in ({b'foreign': {}}, {b'ref': {b'parent': b'business'}}):
                with self.subTest(name=name, records=records), self.assertRaises(ValueError):
                    _classify(name, bolt(records))

    def test_builtin_networks_are_fully_decoded_and_unknown_records_reject(self):
        def value(name, driver, identity):
            return {'name': name, 'id': identity, 'created': '2026-10-13T10:00:00.123456789Z',
                'networkType': driver, 'scope': 'local', 'labels': None, 'ipamType': 'null',
                'ipamOptions': None, 'addrSpace': '', 'enableIPv4': True, 'enableIPv6': False,
                'generic': {'com.docker.network.generic': {}}, 'persist': True, 'internal': False,
                'attachable': False, 'inDelete': False, 'ingress': False, 'configOnly': False,
                'configFrom': '', 'loadBalancerIP': '', 'loadBalancerMode': 'NAT',
                'skipGwAllocIPv4': False, 'skipGwAllocIPv6': False}
        records = {}
        for number, name, driver in ((1, 'host', 'host'), (2, 'none', 'null')):
            identity = str(number) * 64
            prefix = ('docker/network/v1.0/network/' + identity + '/').encode()
            records[prefix] = struct.pack('<Q', number) + json.dumps(value(name, driver, identity)).encode()
            prefix = ('docker/network/v1.0/endpoint_count/' + identity + '/').encode()
            records[prefix] = struct.pack('<Q', number + 2) + b'{"Count":0}'
        tree = {b'libnetwork': records}
        self.assertEqual(_classify('network/files/local-kv.db', bolt(tree))[0], 'docker_network_metadata')
        for bad_ip in (None, '127.0.0.1', [], 0):
            rejected = copy.deepcopy(tree)
            key = b'docker/network/v1.0/network/' + b'1' * 64 + b'/'
            record = value('host', 'host', '1' * 64); record['loadBalancerIP'] = bad_ip
            rejected[b'libnetwork'][key] = struct.pack('<Q', 1) + json.dumps(record).encode()
            with self.subTest(loadBalancerIP=bad_ip), self.assertRaises(ValueError):
                _classify('network/files/local-kv.db', bolt(rejected))
        for bad_key, bad_value in (
            (b'docker/network/v1.0/endpoint/' + b'3' * 64 + b'/', struct.pack('<Q', 9) + b'{}'),
            (b'unknown', b'{}'),
            (b'docker/network/v1.0/endpoint_count/' + b'1' * 64 + b'/', struct.pack('<Q', 1) + b'{"Count":1}')):
            rejected = copy.deepcopy(tree)
            rejected[b'libnetwork'][bad_key] = bad_value
            with self.subTest(key=bad_key), self.assertRaises(ValueError):
                _classify('network/files/local-kv.db', bolt(rejected))

    def test_latest_tx_and_nested_inline_buckets_are_enumerated(self):
        tree = {b'v1': {b'version': b'\x08', b'moby': {b'containers': {}}}}
        self.assertEqual(_decode_bbolt(bolt({}, tree)), tree)
        self.assertEqual(_classify('containerd/io.containerd.metadata.v1.bolt/meta.db', bolt(tree))[0],
                         'containerd_empty_metadata')

    def test_newer_restore_record_is_not_hidden_by_empty_old_root(self):
        clean = {b'v1': {b'version': b'\x08'}}
        restore = {b'v1': {b'version': b'\x08', b'moby': {b'containers': {b'cid': {}}}}}
        with self.assertRaises(ValueError):
            _classify('containerd/io.containerd.metadata.v1.bolt/meta.db', bolt(clean, restore))

    def test_unknown_namespace_and_unsigned_or_old_schema_reject(self):
        for tree in ({b'v1': {b'version': b'\x04'}}, {b'v2': {}},
                     {b'v1': {b'version': b'\x08', b'foreign': {}}},
                     {b'v1': {b'version': b'\x08', b'moby': {b'tasks': {b'x': b'x'}}}}):
            with self.subTest(tree=tree), self.assertRaises(ValueError):
                _classify('containerd/io.containerd.metadata.v1.bolt/meta.db', bolt(tree))

    def test_checksum_bounds_overflow_duplicate_hidden_and_cycle_reject(self):
        good = bolt({b'volumes': {}})
        cases = []
        corrupt = bytearray(good); corrupt[72] ^= 1; cases.append(corrupt)
        corrupt = bytearray(good); struct.pack_into('<I', corrupt, 5 * 4096 + 12, 10000); cases.append(corrupt)
        corrupt = bytearray(good); struct.pack_into('<I', corrupt, 5 * 4096 + 20, 0); cases.append(corrupt)
        # A leaf bucket points back to its own page.
        corrupt = bytearray(good)
        offset = 5 * 4096 + 16
        pos = struct.unpack_from('<I', corrupt, offset + 4)[0]
        key_size = struct.unpack_from('<I', corrupt, offset + 8)[0]
        struct.pack_into('<Q', corrupt, offset + pos + key_size, 5); cases.append(corrupt)
        cases.append(good[:-4096])
        # Valid-shaped branch self-reference, rather than a malformed bucket.
        corrupt = bytearray(good)
        branch = struct.pack('<QHHI', 5, 1, 1, 0) + struct.pack('<IIQ', 16, 1, 5) + b'a'
        corrupt[5 * 4096:6 * 4096] = branch.ljust(4096, b'\0')
        cases.append(corrupt)
        # Two distinct leaf element slots hide the same key.
        corrupt = bytearray(bolt({b'a': b'1', b'b': b'2'}))
        offset = 5 * 4096 + 32
        position = struct.unpack_from('<I', corrupt, offset + 4)[0]
        corrupt[offset + position] = ord('a')
        cases.append(corrupt)
        for raw in cases:
            with self.subTest(digest=hashlib.sha256(raw).hexdigest()), self.assertRaises(ValueError):
                _decode_bbolt(bytes(raw))

    def test_branch_children_are_completely_enumerated(self):
        raw = bytearray(bolt({}) + bytes(8192))
        for index in (0, 1):
            start = index * 4096
            struct.pack_into('<Q', raw, start + 56, 8)
            struct.pack_into('<Q', raw, start + 72, fnv(raw[start + 16:start + 72]))
        branch = (struct.pack('<QHHI', 5, 1, 2, 0)
            + struct.pack('<IIQ', 32, 1, 6) + struct.pack('<IIQ', 17, 1, 7) + b'ab')
        raw[5 * 4096:6 * 4096] = branch.ljust(4096, b'\0')
        raw[6 * 4096:7 * 4096] = leaf({b'a': b'first'}, 6, 4096)
        raw[7 * 4096:8 * 4096] = leaf({b'b': b'last'}, 7, 4096)
        self.assertEqual(_decode_bbolt(bytes(raw)), {b'a': b'first', b'b': b'last'})

    def test_initialized_buildkit_and_volume_indexes_are_not_virgin_disk_only(self):
        for name, tree in (
            ('buildkit/cache.db', {key: {} for key in (b'_result', b'_links', b'_byresult', b'_backlinks')}),
            ('buildkit/metadata_v2.db', {}),
            ('volumes/metadata.db', {b'volumes': {}})):
            with self.subTest(name=name):
                classification, _ = _classify(name, bolt(tree, no_freelist=True))
                self.assertIn(classification, ('buildkit_cache', 'docker_empty_volume_index'))
        with self.assertRaises(ValueError):
            _classify('volumes/metadata.db', bolt({b'volumes': {b'business': b'{}'}}))

    def test_content_cache_requires_raw_filename_digest(self):
        raw = b'non-executing content blob'
        name = 'containerd/io.containerd.content.v1.content/blobs/sha256/' + hashlib.sha256(raw).hexdigest()
        self.assertEqual(_classify(name, raw)[0], 'content_cache')
        with self.assertRaises(ValueError):
            _classify(name[:-1] + ('0' if name[-1] != '0' else '1'), raw)

    def test_scan_is_read_only_and_file_identity_and_digest_stay_unchanged(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'docker'; root.mkdir(mode=0o700)
            volumes = root / 'volumes'; volumes.mkdir(mode=0o700)
            path = volumes / 'metadata.db'; path.write_bytes(bolt({b'volumes': {}})); path.chmod(0o600)
            before = path.stat(), path.read_bytes()
            inventory = _scan_store(root, DATA_ROOT, uid=os.getuid(), gid=os.getgid())
            after = path.stat(), path.read_bytes()
            self.assertEqual(before, after)
            self.assertEqual(inventory['schema'], 'switchback.engine.store_inventory.v1')
            self.assertNotIn('backing_filesystem', inventory)
            # Owner normalization belongs only to this temporary test fixture.
            for entry in inventory['entries']:
                entry['uid'] = entry['gid'] = 0
            validate_store_inventory(inventory)

    def test_unsafe_stat_is_observed_before_rejection_without_payload_open(self):
        import stat
        for target in ('root', 'leaf'):
            for mutation in ('owner', 'gid', 'mode', 'symlink', 'socket', 'links', 'empty'):
                if target == 'root' and mutation in ('links', 'empty'):
                    continue
                with self.subTest(target=target, mutation=mutation):
                    observed, opened = [], []
                    def info(directory):
                        return SimpleNamespace(st_dev=1, st_ino=2 if directory else 3,
                            st_mode=(stat.S_IFDIR | 0o700) if directory else (stat.S_IFREG | 0o600),
                            st_uid=0, st_gid=0, st_nlink=2 if directory else 1, st_size=4096,
                            st_mtime_ns=4, st_ctime_ns=5)
                    root, leaf = info(True), info(False)
                    unsafe = root if target == 'root' else leaf
                    if mutation == 'owner': unsafe.st_uid = 999
                    elif mutation == 'gid': unsafe.st_gid = 999
                    elif mutation == 'mode': unsafe.st_mode |= 0o022
                    elif mutation == 'symlink': unsafe.st_mode = stat.S_IFLNK | 0o700
                    elif mutation == 'socket': unsafe.st_mode = stat.S_IFSOCK | 0o600
                    elif mutation == 'links': unsafe.st_nlink = 2
                    elif mutation == 'empty': unsafe.st_size = 0
                    def open_directory(path, flags, **kwargs):
                        opened.append(str(path))
                        self.assertEqual(str(path), DATA_ROOT, 'unsafe payload must never be opened')
                        return 100
                    iterator = mock.MagicMock()
                    iterator.__enter__.return_value = iter([SimpleNamespace(name='unsafe')])
                    with (mock.patch('store.os.stat', side_effect=lambda path, **kwargs:
                              root if str(path) == DATA_ROOT else leaf),
                          mock.patch('store.os.open', side_effect=open_directory),
                          mock.patch('store.os.fstat', return_value=root),
                          mock.patch('store.os.scandir', return_value=iterator), mock.patch('store.os.close')):
                        with self.assertRaises(ValueError):
                            _scan_store(observe=lambda kind, value: observed.append((kind, value)))
                    actual_path = DATA_ROOT if target == 'root' else DATA_ROOT + '/unsafe'
                    values = [value for kind, value in observed if kind == 'store-stat-before-validation'
                              and value['path'] == actual_path]
                    self.assertTrue(values, 'actual rejected stat must survive in bounded raw audit')
                    self.assertEqual(values[-1]['stat']['uid'], unsafe.st_uid)
                    self.assertEqual(values[-1]['stat']['mode'], unsafe.st_mode)
                    self.assertEqual(values[-1]['stat']['inode'], unsafe.st_ino)
                    self.assertEqual(opened, [] if target == 'root' else [DATA_ROOT])


if __name__ == '__main__':
    unittest.main()
