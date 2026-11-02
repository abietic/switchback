"""Pure temp-file acceptance; never executes a deployment or external command."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location('prepare', ROOT/'scripts/experience-prepare.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)

class PreparationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.parent = Path(self.temp.name).resolve()
        self.root = self.parent/'source'
        self.root.mkdir()
        for name in ('deploy/experience','internal/platform/capacityprobe','cmd/switchback-capacity-probe',
                     'test/acceptance/experienceprepare'):
            shutil.copytree(ROOT/name,self.root/name,ignore=shutil.ignore_patterns('__pycache__'))
        for name in ('scripts/experience-prepare.py','scripts/experience-dirty-build.py','deploy/local/migrate-postgres.sh'):
            (self.root/name).parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(ROOT/name,self.root/name)
        spec = importlib.util.spec_from_file_location('dirty',ROOT/'scripts/experience-dirty-build.py')
        self.dirty = importlib.util.module_from_spec(spec);spec.loader.exec_module(self.dirty)
        for name in self.dirty.FILE_ROOTS:
            (self.root/name).parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(ROOT/name,self.root/name)
        # This suite exercises the immutable historical v1 preparation profile.
        shutil.copyfile(ROOT/'test/acceptance/experiencefreeze/testdata/worker-profile-v1.Dockerfile',
                        self.root/'images/worker/Dockerfile')
        for name in ('web/package.json','web/package-lock.json','migrations/000001_test.sql'):
            (self.root/name).parent.mkdir(parents=True,exist_ok=True)
            (self.root/name).write_text('synthetic fixture\n')
        for name in self.dirty.DIR_ROOTS:
            (self.root/name).mkdir(parents=True,exist_ok=True)

    def test_blocked_example_has_complete_separate_bindings(self):
        with patch('subprocess.run',side_effect=AssertionError('external execution')), patch('socket.socket',side_effect=AssertionError('network')):
            result = prepare.render(str(self.root),str(self.parent/'output'))
        self.assertEqual(result['status'],'preparation_blocked')
        self.assertFalse(result['execution_enabled'])
        self.assertIsNone(result['capacity_source'])
        self.assertTrue(all(x is None for x in result['actual']['image_ids'].values()))
        self.assertEqual(result['pins_sha256'],prepare.PINS_SHA)
        bundle=self.parent/'output'
        self.assertEqual(bundle.stat().st_mode & 0o777,0o700)
        self.assertFalse((bundle/'capacity.json').exists())
        binds=result['bindings'];self.assertIn('images/worker/Dockerfile',{x['path'] for x in binds['four_image_copy_closure']})
        self.assertIn('migrations/000001_test.sql',{x['path'] for x in binds['deployment_probe_script_migrations_tests']})
        compose=json.loads((bundle/'core.compose.yaml').read_text())
        self.assertEqual(len(compose['services']),5)
        self.assertEqual(compose['services']['switchback-backend']['environment']['SWITCHBACK_M1_WORKER_DELIVERY_ENABLED'],'0')

    def test_bind_materials_readable_by_nonowner_without_exposing_evidence(self):
        previous_umask=os.umask(0o077)
        try:
            manifest=prepare.render(str(self.root),str(self.parent/'consumer-output'))
        finally:
            os.umask(previous_umask)
        self.assertEqual(manifest['render_permissions']['nonsecret_bind_files']['init-postgres.sh'],'0644')
        output=self.parent/'consumer-output'
        # Model the Compose UID 999 consumer using mode bits; never switch UID or chown.
        for name in ('init-postgres.sh','migrate-postgres.sh','migrations/000001_test.sql'):
            info=(output/name).stat()
            self.assertNotEqual(info.st_uid,999,'fixture must represent a non-owner container UID')
            self.assertEqual(info.st_mode & 0o777,0o644,name)
            self.assertTrue(info.st_mode & 0o004,name+' must be readable by a different UID/GID')
        sql_dir=(output/'migrations').stat()
        self.assertEqual(sql_dir.st_mode & 0o777,0o755)
        self.assertTrue(sql_dir.st_mode & 0o005,'bind-mounted SQL root must be readable/traversable')
        self.assertEqual(output.stat().st_mode & 0o777,0o700)
        for name in ('manifest.json','collector-manifest.json','primary-pins.json','core.compose.yaml'):
            self.assertEqual((output/name).stat().st_mode & 0o777,0o600,name)

    def test_rejects_overwrite_symlink_and_relative_paths(self):
        for output in ('relative',str(self.root/'deploy'/'new'),str(self.root/'web'/'new'),str(self.parent)+'//new'):
            with self.assertRaises((ValueError,OSError)):prepare.render(str(self.root),output)
        dest=self.parent/'exists';dest.mkdir()
        with self.assertRaises(FileExistsError):prepare.render(str(self.root),str(dest))
        link=self.parent/'link';link.symlink_to(dest,target_is_directory=True)
        with self.assertRaises(ValueError):prepare.render(str(self.root),str(link/'new'))
        pins=self.root/'deploy/experience/primary-pins.json';pins.unlink();pins.symlink_to(ROOT/'deploy/experience/primary-pins.json')
        with self.assertRaises(OSError):prepare.plan(str(self.root))

    def test_pins_drift_and_source_digest_rejection(self):
        first=prepare.plan(str(self.root))
        (self.root/'go.mod').write_text('changed source\n')
        with self.assertRaisesRegex(ValueError,'source digest drift'):
            prepare.render(str(self.root),str(self.parent/'rejected'),first['source_sha256'])
        self.assertFalse((self.parent/'rejected').exists())
        pins=self.root/'deploy/experience/primary-pins.json';pins.write_bytes(pins.read_bytes()+b' ')
        with self.assertRaisesRegex(ValueError,'primary pins changed'):prepare.plan(str(self.root))

    def test_secret_paths_never_bundled_and_special_inputs_rejected(self):
        secret=self.root/'web'/'.env';secret.write_text('DO_NOT_CAPTURE_CANARY')
        result=prepare.render(str(self.root),str(self.parent/'output'))
        for path in (self.parent/'output').rglob('*'):
            if path.is_file():self.assertNotIn(b'DO_NOT_CAPTURE_CANARY',path.read_bytes())
        os.symlink('/dev/null',self.root/'migrations'/'evil.sql')
        with self.assertRaises((ValueError,OSError)):prepare.plan(str(self.root))

if __name__ == '__main__': unittest.main()
