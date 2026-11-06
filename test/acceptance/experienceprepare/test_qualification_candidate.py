import importlib.util
from pathlib import Path
import tempfile
import unittest
import json
ROOT=Path(__file__).resolve().parents[3]
SPEC=importlib.util.spec_from_file_location('qualification_candidate',ROOT/'deploy/experience/qualification/build_candidate.py')
candidate=importlib.util.module_from_spec(SPEC);SPEC.loader.exec_module(candidate)
class CandidateTest(unittest.TestCase):
 def test_source_inputs_and_templates_are_separate_and_closed(self):
  files,templates=candidate.inputs()
  self.assertTrue(files);self.assertTrue(templates)
  self.assertFalse(any('qualification' in Path(f['path']).parts for f in files))
  self.assertTrue(any(f['path'].endswith('qualification/window.py') for f in templates))
  for required in ('go.mod','internal/workerruntime/adapter/capacitysnapshot/qualification_install.go','cmd/switchback-qualification-material/main.go','images/worker/Dockerfile'):
   self.assertIn(required,[f['path'] for f in files])
 def test_kit_does_not_accept_fabricated_runtime_or_drift(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d);p=root/'artifact';p.write_bytes(b'binary')
   m={'schema':'switchback.qualification.offline_kit.v1','runtime_qualification':False,'files':[{'path':'artifact','digest':candidate.digest(b'binary'),'bytes':6}],'unresolved_runtime':{'image_ids':None,'receipt':None}}
   (root/'manifest.json').write_text(json.dumps(m));candidate.verify_kit(root)
   m['unresolved_runtime']['image_ids']='fabricated';(root/'manifest.json').write_text(json.dumps(m))
   with self.assertRaises(RuntimeError):candidate.verify_kit(root)
   m['unresolved_runtime']['image_ids']=None;(root/'manifest.json').write_text(json.dumps(m));p.write_bytes(b'changed')
   with self.assertRaises(RuntimeError):candidate.verify_kit(root)
