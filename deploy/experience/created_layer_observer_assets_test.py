"""Offline source contract; optional Compose config JSON is client parser evidence only."""
import configparser
import json
from pathlib import Path
import re
import sys
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
COMPOSE_OUTPUT = None
if len(sys.argv) > 1 and sys.argv[1].endswith('.log'):
    COMPOSE_OUTPUT = Path(sys.argv.pop(1))


class ObserverAssets(unittest.TestCase):
    def test_native_unit_exact_bounded_host_namespace_profile(self):
        unit = configparser.ConfigParser(interpolation=None, strict=True)
        unit.optionxform = str
        with (HERE / 'switchback-created-layer-observer.service').open() as f:
            unit.read_file(f)
        self.assertEqual(set(unit.sections()), {'Unit', 'Service'})
        self.assertEqual(dict(unit['Unit']), {
            'Description': 'Switchback finite created-layer observer (undeployed template)',
        })
        self.assertEqual(dict(unit['Service']), {
            'Type': 'simple', 'User': 'root', 'Group': 'root',
            'EnvironmentFile': '/etc/switchback/created-layer-observer/deployment.env',
            'ExecStart': '/opt/switchback/bin/switchback-created-layer-observer serve ${SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST}',
            'RuntimeDirectory': 'switchback-created-layer', 'RuntimeDirectoryMode': '0700',
            'Slice': 'switchback-core.slice', 'CapabilityBoundingSet': 'CAP_SYS_ADMIN',
            'NoNewPrivileges': 'yes', 'RestrictAddressFamilies': 'AF_UNIX',
            'RestrictNamespaces': 'yes', 'SystemCallFilter': '~@mount @reboot @swap',
            'Restart': 'no', 'UMask': '0077', 'CPUQuota': '20%',
            'CPUQuotaPeriodSec': '100ms', 'MemoryMax': '128M', 'MemorySwapMax': '0',
            'TasksMax': '64', 'KillMode': 'control-group', 'TimeoutStopSec': '10s',
        })
        # An absent namespace directive means the host mount namespace is inherited.
        # No Install/WantedBy or ExecStartPre: no enabling or key generation.
        source = (ROOT / 'internal/platform/createdlayeripc/types.go').read_text()
        core = re.search(r'CoreCgroupPath\s*=\s*"([^"]+)"', source).group(1)
        self.assertEqual(core, '/switchback.slice/' + unit['Service']['Slice'])
        self.assertRegex(source, r'ObserverCgroupPath\s*= CoreCgroupPath \+ "/switchback-created-layer-observer.service"')

    def test_overlay_changes_only_fixed_backend_mounts(self):
        overlay = json.loads((HERE / 'created-layer-observer.compose.yaml').read_text())
        self.assertEqual(set(overlay), {'services'})
        self.assertEqual(set(overlay['services']), {'switchback-backend'})
        backend = overlay['services']['switchback-backend']
        self.assertEqual(set(backend), {'volumes'})
        expected = {
            '/var/run/docker.sock': ('/run/switchback-engine/docker.sock', False),
            '/run/switchback-created-layer': ('/run/switchback-created-layer', True),
            '/run/switchback-observer-auth/key': ('/etc/switchback/created-layer-observer/key', True),
        }
        self.assertEqual(len(backend['volumes']), len(expected))
        for mount in backend['volumes']:
            source, readonly = expected[mount['target']]
            self.assertEqual(mount, {'type': 'bind', 'source': source, 'target': mount['target'],
                                    'read_only': readonly, 'bind': {'create_host_path': False}})
        base = json.loads((HERE / 'core.compose.yaml').read_text())
        b = base['services']['switchback-backend']
        self.assertEqual(b['cap_drop'], ['ALL'])
        self.assertEqual(b['user'], '0:0')
        self.assertCountEqual(b['cap_add'], ['CHOWN', 'DAC_OVERRIDE', 'FOWNER'])
        self.assertTrue(b['read_only'])
        self.assertEqual(b['security_opt'], ['no-new-privileges:true'])
        self.assertNotIn('privileged', b)
        self.assertEqual(b['environment']['SWITCHBACK_M1_WORKER_DELIVERY_ENABLED'], '0')
        self.assertEqual(b['environment']['SWITCHBACK_M2_ASSISTANT_ENABLED'], '0')

    @unittest.skipUnless(COMPOSE_OUTPUT, 'Compose merge checked separately with isolated absent endpoint')
    def test_real_compose_config_merge_by_target(self):
        merged = json.loads(COMPOSE_OUTPUT.read_text())
        base = json.loads((HERE / 'core.compose.yaml').read_text())
        overlay = json.loads((HERE / 'created-layer-observer.compose.yaml').read_text())
        self.assertEqual(set(merged['services']), set(base['services']))
        backend = merged['services']['switchback-backend']
        self.assertEqual(backend['cap_drop'], ['ALL'])
        self.assertEqual(backend['user'], '0:0')
        self.assertFalse(backend.get('privileged', False))
        self.assertCountEqual(backend['cap_add'], ['CHOWN', 'DAC_OVERRIDE', 'FOWNER'])
        self.assertTrue(backend['read_only'])
        self.assertEqual(backend['security_opt'], ['no-new-privileges:true'])
        self.assertNotEqual(backend.get('network_mode'), 'host')
        self.assertEqual(backend['environment'], base['services']['switchback-backend']['environment'])
        mounts = {v['target']: v for v in backend['volumes']}
        self.assertEqual(len(mounts), len(base['services']['switchback-backend']['volumes']) + 2)
        for v in overlay['services']['switchback-backend']['volumes']:
            self.assertEqual(mounts[v['target']], v)
        self.assertEqual(backend['ports'], [{'mode': 'ingress', 'host_ip': '127.0.0.1', 'target': 8080, 'published': '48081', 'protocol': 'tcp'}])
        for name, service in merged['services'].items():
            if name != 'switchback-backend':
                self.assertFalse(any(v['target'].startswith('/run/switchback-created-layer') or v['target'].startswith('/run/switchback-observer-auth') for v in service.get('volumes', [])))


if __name__ == '__main__':
    unittest.main()
