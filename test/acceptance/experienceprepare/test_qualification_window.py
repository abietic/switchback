import importlib.util
from pathlib import Path
import unittest
ROOT=Path(__file__).resolve().parents[3]
SPEC=importlib.util.spec_from_file_location('qualification_window',ROOT/'deploy/experience/qualification/window.py')
window=importlib.util.module_from_spec(SPEC);SPEC.loader.exec_module(window)
class WindowTest(unittest.TestCase):
 def test_one_origin_order(self):
  ops=window._FakeOperations();result=window._run_window(ops)
  self.assertEqual(result['status'],'installed');self.assertEqual(ops.starts,1)
  self.assertLess(ops.events.index('begin'),ops.events.index('start-waiting'))
  self.assertLess(ops.events.index('collect'),ops.events.index('assemble'))
  self.assertLess(ops.events.index('bootstrap-publication'),ops.events.index('install'))
 def test_failures_never_retry_or_cleanup_unrelated(self):
  for stage in window.STAGES:
   with self.subTest(stage=stage):
    ops=window._FakeOperations(fail=stage)
    with self.assertRaises(window.WindowFailure):window._run_window(ops)
    self.assertEqual(ops.events.count(stage),1)
    self.assertFalse(any('stop' in v or 'retry' in v for v in ops.events))
 def test_timeout_at_every_boundary_uses_original_budget(self):
  for stage in window.WINDOW_STAGES:
   with self.subTest(stage=stage):
    ops=window._FakeOperations(expire=stage)
    with self.assertRaises(window.WindowFailure):window._run_window(ops)
    self.assertEqual(ops.starts,1)
 def test_subprocess_output_and_timeout_are_bounded_and_preserved(self):
  # The same real adapter boundaries now use pure process/selector/waitid fakes.
  # No subprocess, native waitid or CDLL is exercised in offline delivery.
  boundary=QualificationProcessBoundaryTest()
  boundary.boundary(timeout=True)
  boundary.boundary(overflow=True)
 def test_unknown_preserves_exact_owned_only(self):
  ops=window._FakeOperations(fail='prepare')
  with self.assertRaises(window.WindowFailure):window._run_window(ops)
  self.assertEqual(ops.saved['owned'],[{'role':'synthetic','cid':'synthetic-owned'}])
  self.assertNotIn('collect',ops.events)

class QualificationProcessBoundaryTest(unittest.TestCase):
 def boundary(self, error=None, lost=None, timeout=False, overflow=False, facts=None, converged=False, support=None):
  import errno,os,signal
  from types import SimpleNamespace
  from unittest import mock
  support=support or window._load_process_support()
  clock=[10.0];events=[];logs=[]
  class Stream:
   def __init__(self,n):self.n=n;self.closed=False
   def fileno(self):return self.n
   def close(self):self.closed=True
  class Process:
   pid=43210
   returncode=None
   stdin=None
   stdout=Stream(11)
   stderr=Stream(12)
   def poll(self):raise AssertionError('poll would reap the reserved leader')
   def wait(self,timeout):events.append('reap');self.returncode=0;return 0
  p=Process()
  if lost=='reaped':p.returncode=0
  class Selector:
   def __init__(self):self.keys={};self.rounds=0
   def register(self,stream,event,label):self.keys[stream.n]=SimpleNamespace(fileobj=stream,fd=stream.n,data=label)
   def unregister(self,stream):del self.keys[stream.n]
   def get_map(self):return self.keys
   def select(self,timeout):
    self.rounds+=1;clock[0]+=.001
    if timeout_mode and self.rounds>1:clock[0]=131;return []
    return [(v,1) for v in list(self.keys.values())]
   def close(self):self.keys.clear()
  timeout_mode=timeout
  stdout=iter((b'x'*(window.LIMIT+1),) if overflow else ((b'partial output',) if timeout else (b'output',b'')))
  stderr=iter((b'partial error',) if timeout else (b'error',b''))
  def observe(pid):
   events.append('observe')
   if lost:raise ChildProcessError(errno.ECHILD,'lost child')
   return SimpleNamespace(si_pid=pid,si_uid=0,si_signo=int(signal.SIGCHLD),si_code=os.CLD_EXITED,si_status=0)
  def kill(pid,sig):
   self.assertIsNone(p.returncode);self.assertEqual((pid,sig),(43210,signal.SIGKILL));events.append('kill')
   if error:raise error
  def group(pid):
   self.assertEqual(pid,p.pid);self.assertIsNone(p.returncode);events.append('snapshot')
   if facts is None:raise AssertionError('no native query for old EPERM fake')
   return facts
  with (mock.patch.object(support,'_waitid_reader',return_value=observe),
        mock.patch.object(support,'_darwin_group_reader',return_value=group,create=True),
        mock.patch.object(support.sys,'platform','darwin' if facts is not None else 'linux'),
        mock.patch('ctypes.CDLL',side_effect=AssertionError('no native binding for synthetic process')),
        mock.patch.object(window.subprocess,'Popen',return_value=p),
        mock.patch.object(window.selectors,'DefaultSelector',return_value=Selector()),
        mock.patch.object(window.os,'set_blocking'),mock.patch.object(window.os,'read',side_effect=lambda fd,n:next(stdout if fd==11 else stderr)),
        mock.patch.object(window.os,'killpg',side_effect=kill),mock.patch.object(window.time,'monotonic',side_effect=lambda:clock[0])):
   call=lambda:window._bounded_run(['/synthetic'],130,{'PATH':'/usr/bin:/bin'},logs.append,support=support)
   if lost or timeout or overflow or (isinstance(error,PermissionError) and not converged):
    with self.assertRaises(window.WindowFailure) as caught:call()
    if timeout:self.assertIn('timeout',str(caught.exception))
   else:self.assertEqual(call(),b'output')
  self.assertEqual(len(logs),1);self.assertTrue(p.stdout.closed and p.stderr.closed)
  if lost=='reaped':self.assertEqual(events,[])
  elif lost:self.assertEqual(events,['observe'])
  elif converged:self.assertEqual(events,['observe','kill','observe','snapshot','observe','reap'])
  elif facts is not None:self.assertEqual(events,['observe','kill','observe','snapshot','reap'])
  else:self.assertEqual(events,['observe','kill','reap'])
  if error:self.assertIn(str(error),str(logs[0]['local_cleanup']))
  if timeout:self.assertIn('partial output',logs[0]['stdout']);self.assertIn('partial error',logs[0]['stderr'])
  if overflow:self.assertLessEqual(len(logs[0]['stdout']),window.LIMIT)
  return logs[0]
 def test_darwin_sole_terminal_group_uses_same_verified_source_and_truthful_errno(self):
  import errno,json,stat,sys,tempfile
  from unittest import mock
  # Exercise the actual exact-kit source/mode/digest loader with synthetic
  # verified records of the current shared source. No native reader is resolved.
  with tempfile.TemporaryDirectory() as temporary:
   home=Path(temporary).resolve();kit=home/'.switchback/qualification-kit'
   source=kit/'source/deploy/experience/engine';source.mkdir(parents=True,mode=0o700)
   records=[]
   for name in ('contract.py','prepare.py'):
    raw=(ROOT/'deploy/experience/engine'/name).read_bytes();target=source/name
    target.write_bytes(raw);target.chmod(0o644)
    records.append({'path':'source/deploy/experience/engine/'+name,'type':'regular',
     'bytes':len(raw),'mode':stat.S_IMODE(target.stat().st_mode),'digest':window._digest(raw)})
   with mock.patch.object(window,'_qualification_home',return_value=home),mock.patch('ctypes.CDLL',side_effect=AssertionError('loader must remain import-only')):
    support=window._load_process_support(kit,{'files':records})
   self.assertEqual(support._prove_darwin_terminal_group.__code__.co_filename,str(source/'prepare.py'))
   facts={'byte_count':4,'errno':0,'raw':(43210).to_bytes(4,sys.byteorder,signed=True)+bytes(8188)}
   value=self.boundary(error=PermissionError(errno.EPERM,'original signal denied'),facts=facts,converged=True,support=support)
   result=next(v for v in value['local_cleanup'] if v['operation']=='kill_owned_group')
   self.assertEqual((result['status'],result['signal_status'],result['signalled'],result['errno']),('already_terminal','failed',False,errno.EPERM))
   json.dumps(value)
 def test_shared_loader_rejects_extra_member_and_stale_errno(self):
  import errno,sys
  leader=(43210).to_bytes(4,sys.byteorder,signed=True)
  for facts in ({'byte_count':8,'errno':0,'raw':leader*2+bytes(8184)},
                {'byte_count':4,'errno':errno.EBUSY,'raw':leader+bytes(8188)}):
   with self.subTest(facts={k:v for k,v in facts.items() if k!='raw'}):
    value=self.boundary(error=PermissionError(errno.EPERM,'denied'),facts=facts)
    self.assertNotIn('already_terminal',str(value['local_cleanup']))
 def test_normal_eof_fences_before_reap_and_cleanup_errors_remain_audited(self):
  import errno
  for error in (None,ProcessLookupError(errno.ESRCH,'already absent'),PermissionError(errno.EPERM,'signal denied')):
   with self.subTest(error=error):self.boundary(error=error)
 def test_echild_and_previously_reaped_identity_never_signal_or_reap(self):
  for lost in ('echild','reaped'):
   with self.subTest(lost=lost):self.boundary(lost=lost)
 def test_timeout_preserves_first_error_and_partial_outputs_after_cleanup_errors(self):
  import errno
  for error in (None,ProcessLookupError(errno.ESRCH,'absent'),PermissionError(errno.EPERM,'denied')):
   with self.subTest(error=error):self.boundary(error=error,timeout=True)
 def test_output_limit_preserves_exact_old_cap(self):self.boundary(overflow=True)
 def test_fixed_helper_loader_ignores_ambient_private_modules(self):
  import sys
  from types import SimpleNamespace
  from unittest import mock
  poison=SimpleNamespace(_waitid_reader=lambda:(_ for _ in ()).throw(AssertionError('ambient helper')))
  with mock.patch.dict(sys.modules,{'contract':poison,'bundle':poison,'prepare':poison}):
   support=window._load_process_support()
   self.assertTrue(support._cleanup_process.__code__.co_filename.endswith('/deploy/experience/engine/prepare.py'))
   self.assertIs(sys.modules['prepare'],poison)
 def test_reader_is_resolved_before_any_child_creation(self):
  from unittest import mock
  support=window._load_process_support()
  with mock.patch.object(support,'_waitid_reader',side_effect=RuntimeError('reader unavailable')),mock.patch.object(window.subprocess,'Popen') as create:
   with self.assertRaises(window.WindowFailure):window._bounded_run(['/synthetic'],130,{},lambda value:None,support=support)
   create.assert_not_called()
 def test_import_selfcheck_and_support_load_never_resolve_native_reader(self):
  import ctypes,os
  from unittest import mock
  with mock.patch.object(ctypes,'CDLL',side_effect=AssertionError('no native CDLL')),mock.patch.object(os,'waitid',side_effect=AssertionError('no native waitid'),create=True),mock.patch.object(window.subprocess,'Popen',side_effect=AssertionError('no child')):
   support=window._load_process_support()
   self.assertTrue(callable(support._waitid_reader))
   self.assertTrue(callable(support._darwin_group_reader))
   self.assertEqual(window._run_window(window._FakeOperations())['status'],'installed')
 def test_fixed_kit_helper_requires_full_verification_evidence(self):
  with self.assertRaises(window.WindowFailure):window._load_process_support(Path('/not/the/fixed/kit'),{})
  with self.assertRaises(window.WindowFailure):window._load_process_support(window._qualification_home()/'.switchback/qualification-kit',None)
 def test_actual_adapter_verifies_kit_before_loading_helpers_and_any_child(self):
  from types import SimpleNamespace
  from unittest import mock
  events=[];manifest={'files':[]}
  builder=SimpleNamespace(verify_kit=lambda path:(events.append('verify-kit') or manifest))
  ops=window._ActualOperations()
  ops.command=lambda argv,*args:(events.append('child-command') or b'{"name":"switchback-core","status":"Running"}')
  ops.guest=lambda *args:events.append('guest-command')
  try:
   with mock.patch.object(window,'_safe_directory'),mock.patch.object(window,'_exclusive'),mock.patch.object(window,'_fixed_source_module',return_value=builder) as fixed,mock.patch.object(window,'_load_process_support',side_effect=lambda kit,proof:(events.append('load-verified-helpers') or object())) as load:
    ops.call('preflight',None)
    fixed.assert_called_once_with(ops.kit/'build_candidate.py')
    load.assert_called_once_with(ops.kit,manifest)
   self.assertEqual(events[:3],['verify-kit','load-verified-helpers','child-command'])
  finally:ops.threads.shutdown(wait=False)
