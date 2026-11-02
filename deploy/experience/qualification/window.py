#!/usr/bin/env python3
"""Fixed one-shot executor. `self-check` never creates production adapters.
Actual mode is a later, separately authorized operation with PG/service/Engine
and publication effects. It has no resume, retry, skip or caller-chosen paths.
"""
import base64
import builtins
import types
import sysconfig
import pwd
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import stat
import subprocess
import sys
import time
import threading

BUDGET=240.0
LIMIT=2*1024*1024
LIMA=Path('/Volumes/SwitchbackCore/toolchain/lima-2.2.1/bin/limactl')
LIMA_HOME='/Volumes/SwitchbackCore/lima'
VM='switchback-core'
GUEST='/opt/switchback/qualification-kit/bin/'
STAGES=('preflight','host-material','intent-input','intent','publication-preflight','create-core','staged','prepare-core','begin','start-waiting','prepare','copy-candidate','host-start','host-before','collect','native-transfer','host-finish','host-transfer','assemble','bootstrap-publication','check-pg','install')
WINDOW_STAGES=STAGES[8:]
class WindowFailure(RuntimeError):pass

def _run_window(ops):
    origin=None
    evidence={'schema':'switchback.qualification.driver_outcome.v1','status':'failed','stages':[],'owned':[]}
    try:
        for stage in STAGES:
            if stage=='begin':
                origin=ops.now();ops.origin(origin)
            deadline=origin+BUDGET if origin is not None else None
            if deadline is not None and ops.now()>=deadline:raise WindowFailure('original qualification deadline')
            value=ops.call(stage,deadline)
            evidence['stages'].append(stage)
            if stage=='create-core':evidence['owned']=value
            if deadline is not None and ops.now()>=deadline:raise WindowFailure('original qualification deadline')
        evidence['status']='installed'
        return evidence
    except BaseException as error:
        evidence['error']=str(error)
        raise WindowFailure(str(error)) from error
    finally:
        ops.preserve(evidence)

class _FakeOperations:
    """Private deterministic protocol tests; never selected by actual CLI."""
    def __init__(self,fail=None,expire=None):self.fail=fail;self.expire=expire;self.events=[];self.clock=0;self.starts=0;self.saved=None
    def now(self):return self.clock
    def origin(self,_):self.starts+=1
    def call(self,stage,deadline):
        self.events.append(stage)
        if stage==self.fail:raise WindowFailure('injected unknown operation')
        self.clock+=241 if stage==self.expire else .01
        return [{'role':'synthetic','cid':'synthetic-owned'}] if stage=='create-core' else None
    def preserve(self,evidence):self.saved=evidence

def _digest(raw):return 'sha256:'+hashlib.sha256(raw).hexdigest()
def _safe_directory(path,uid):
    path=Path(path)
    parts=[path]+list(path.parents)
    for item in parts:
        s=item.lstat()
        if not stat.S_ISDIR(s.st_mode) or s.st_uid not in (0,uid) or s.st_mode&0o022:raise WindowFailure('unsafe directory ancestor')
    s=path.lstat()
    if s.st_uid!=uid or stat.S_IMODE(s.st_mode)!=0o700:raise WindowFailure('unsafe candidate root')
    return (s.st_dev,s.st_ino,s.st_uid,stat.S_IMODE(s.st_mode))
def _read_leaf(root,leaf):
    identity=_safe_directory(root,os.getuid());fd=os.open(str(root/leaf),os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    try:
        before=os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_nlink!=1 or before.st_uid!=os.getuid() or before.st_mode&0o222 or not 0<before.st_size<=LIMIT:raise WindowFailure('unsafe immutable leaf')
        raw=bytearray()
        while True:
            data=os.read(fd,32768)
            if not data:break
            raw.extend(data)
            if len(raw)>LIMIT:raise WindowFailure('output oversized')
        after=os.fstat(fd);current=(root/leaf).lstat()
        if (before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns,before.st_ctime_ns)!=(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns,after.st_ctime_ns) or (current.st_dev,current.st_ino)!=(after.st_dev,after.st_ino) or identity!=_safe_directory(root,os.getuid()):raise WindowFailure('leaf/path changed')
        return bytes(raw)
    finally:os.close(fd)
def _exclusive(root,leaf,raw):
    before=_safe_directory(root,os.getuid());fd=os.open(str(root/leaf),os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400)
    try:
        for off in range(0,len(raw),32768):
            chunk=raw[off:off+32768]
            if os.write(fd,chunk)!=len(chunk):raise WindowFailure('short exclusive write')
        os.fsync(fd)
    finally:os.close(fd)
    d=os.open(str(root),os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:os.fsync(d)
    finally:os.close(d)
    if before!=_safe_directory(root,os.getuid()) or _read_leaf(root,leaf)!=raw:raise WindowFailure('exclusive write drift')

def _source_bytes(path, expected=None):
    """Fixed owned source read, without symlink/module-cache resolution."""
    path=Path(path)
    for parent in reversed(path.parents):
        value=parent.lstat()
        if not stat.S_ISDIR(value.st_mode) or value.st_uid not in (0,os.getuid()) or value.st_mode&0o022:
            raise WindowFailure('unsafe fixed helper source ancestor')
    fd=os.open(str(path),os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    try:
        before=os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_nlink!=1 or before.st_uid not in (0,os.getuid()) or before.st_mode&0o022 or not 0<before.st_size<=512*1024:
            raise WindowFailure('unsafe fixed helper source')
        raw=bytearray()
        while True:
            data=os.read(fd,32768)
            if not data:break
            raw.extend(data)
            if len(raw)>512*1024:raise WindowFailure('helper source bound')
        identity=lambda v:(v.st_dev,v.st_ino,v.st_mode,v.st_uid,v.st_gid,v.st_nlink,v.st_size,v.st_mtime_ns,v.st_ctime_ns)
        if identity(before)!=identity(os.fstat(fd)) or identity(before)!=identity(path.lstat()) or len(raw)!=before.st_size:
            raise WindowFailure('fixed helper source drift')
        if expected is not None and (expected.get('type')!='regular' or expected.get('digest')!=_digest(raw) or expected.get('bytes')!=len(raw) or expected.get('mode')!=stat.S_IMODE(before.st_mode)):
            raise WindowFailure('fixed helper source differs from verified kit')
        return bytes(raw)
    finally:os.close(fd)

def _fixed_source_module(path, dependencies=None, expected=None):
    dependencies=dependencies or {}
    private={'contract','bundle','prepare','guest','store','binaries','build_candidate'}
    standard={'ast','base64','copy','errno','hashlib','json','os','pwd','pathlib','selectors','signal','stat','subprocess','sys','time','ctypes','shutil','struct','re','configparser','functools','ipaddress','shlex','types','math'}
    stdlib=Path(sysconfig.get_path('stdlib')).resolve()
    def fixed_import(name, globals=None, locals=None, fromlist=(), level=0):
        if level:raise WindowFailure('relative helper dependency refused')
        if name in private:
            if name not in dependencies:raise WindowFailure('unverified private helper dependency')
            return dependencies[name]
        if name not in standard:raise WindowFailure('unrecognized helper dependency')
        module=builtins.__import__(name,globals,locals,fromlist,level)
        origin=getattr(getattr(module,'__spec__',None),'origin',None)
        if origin not in ('built-in','frozen'):
            if not origin:raise WindowFailure('helper dependency provenance missing')
            resolved=Path(origin).resolve()
            if stdlib not in resolved.parents or 'site-packages' in resolved.parts:raise WindowFailure('ambient helper dependency refused')
        return module
    module=types.ModuleType('_switchback_qualification_'+Path(path).stem)
    module.__file__=str(path)
    module.__dict__['__builtins__']=dict(vars(builtins),__import__=fixed_import)
    exec(compile(_source_bytes(path,expected),str(path),'exec'),module.__dict__)
    return module

def _qualification_home():
    return Path(pwd.getpwuid(os.getuid()).pw_dir)

def _load_process_support(kit=None, manifest=None):
    """Import-only: never resolves a waitid/CDLL reader or creates a child."""
    if kit is None:
        here=Path(__file__).resolve().parent
        if here.name!='qualification' or here.parent.name!='experience' or here.parent.parent.name!='deploy':
            raise WindowFailure('fixed repository helper source unavailable')
        root=here.parent/'engine'; records=None
    else:
        kit=Path(kit)
        if kit!=_qualification_home()/'.switchback/qualification-kit' or type(manifest) is not dict:
            raise WindowFailure('verified fixed kit helper source unavailable')
        root=kit/'source/deploy/experience/engine'
        records={v['path']:v for v in manifest['files']}
    def expected(name):
        if records is None:return None
        value=records.get('source/deploy/experience/engine/'+name)
        if value is None:raise WindowFailure('verified kit helper source missing')
        return value
    contract=_fixed_source_module(root/'contract.py',expected=expected('contract.py'))
    return _fixed_source_module(root/'prepare.py',{'contract':contract},expected('prepare.py'))

def _bounded_run(argv,deadline,env,log,input_data=None,cancel=None,support=None):
    """Original caps/return/env, using the reviewed shared owned-group helpers."""
    start=time.monotonic();out=bytearray();err=bytearray();process=None;reader=None;terminal=None;failure=None;observations=[]
    sel=selectors.DefaultSelector()
    try:
        if support is None:raise WindowFailure('verified fixed process support missing')
        if (cancel is not None and cancel.is_set()) or time.monotonic()>=deadline:raise WindowFailure('bounded subprocess timeout/cancel; outcome unknown')
        reader=support._waitid_reader()  # Fail closed before any child exists.
        process=subprocess.Popen(argv,stdin=subprocess.PIPE if input_data is not None else subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=env,start_new_session=True)
        for stream,label in ((process.stdout,'stdout'),(process.stderr,'stderr')):os.set_blocking(stream.fileno(),False);sel.register(stream,selectors.EVENT_READ,label)
        sent=0
        if input_data is not None:os.set_blocking(process.stdin.fileno(),False);sel.register(process.stdin,selectors.EVENT_WRITE,'stdin')
        while sel.get_map():
            if (cancel is not None and cancel.is_set()) or time.monotonic()>=deadline:raise WindowFailure('bounded subprocess timeout/cancel; outcome unknown')
            for key,_ in sel.select(min(.1,max(0,deadline-time.monotonic()))):
                if key.data=='stdin':
                    n=os.write(key.fileobj.fileno(),input_data[sent:sent+32768]);sent+=n
                    if sent==len(input_data):sel.unregister(key.fileobj);key.fileobj.close()
                    continue
                data=os.read(key.fileobj.fileno(),32768)
                if not data:sel.unregister(key.fileobj);key.fileobj.close();continue
                target=out if key.data=='stdout' else err
                bound=LIMIT if key.data=='stdout' else 65536
                if len(target)+len(data)>bound:
                    target.extend(data[:max(0,bound-len(target))]);raise WindowFailure('bounded subprocess output exceeded; outcome unknown')
                target.extend(data)
        terminal=support._wait_leader(process,reader,deadline,time.monotonic,observations.append)
        if terminal['exit_code']!=0:raise WindowFailure('fixed operation failed; no retry')
    except BaseException as error:failure=error
    finally:
        if support is None:
            sel.close();cleanup=[]
        else:
            cleanup=support._cleanup_process(process,sel,reader,terminal,deadline,time.monotonic,isinstance(failure,support._ChildIdentityLost))
        if failure is None and time.monotonic()>=deadline:failure=WindowFailure('bounded subprocess timeout; outcome unknown')
        if failure is None and any(v['status']=='failed' for v in cleanup):failure=WindowFailure('owned local subprocess cleanup failed/unknown')
        value={'argv':argv,'env':env,'cwd':os.getcwd(),'started_monotonic':start,'deadline_monotonic':deadline,'stdout':out.decode(errors='replace'),'stderr':err.decode(errors='replace'),'exit':process.returncode if process else None,
               'local_cleanup':cleanup,'leader_terminal':terminal,'process_observations':observations,'error':str(failure) if failure else None}
        try:log(value)
        except BaseException as error:
            if failure is None:failure=error
    if failure:raise WindowFailure(str(failure)) from failure
    return bytes(out)

class _ActualOperations:
    def __init__(self):
        self.home=_qualification_home();self.kit=self.home/'.switchback/qualification-kit';self.root=self.home/'.switchback/qualification-candidate';self.counter=0;self.threads=concurrent.futures.ThreadPoolExecutor(max_workers=1);self.host=None;self.original=None;self.owned=[];self.cancel=threading.Event();self.log_lock=threading.Lock();self.process_support=None
        self.env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(self.home),'LIMA_HOME':LIMA_HOME,'LC_ALL':'C'}
    def now(self):return time.monotonic()
    def origin(self,value):self.original=value
    def log(self,value):
        # Secret material is read only by fixed guest commands and never appears
        # in argv/env returned here. Every completed/failed operation is durable.
        with self.log_lock:
            self.counter+=1;_exclusive(self.root,'operation-%03d.json'%self.counter,json.dumps(value,separators=(',',':')).encode())
    def command(self,argv,deadline=None,input_data=None):return _bounded_run(argv,deadline if deadline is not None else self.now()+180,self.env,self.log,input_data,self.cancel,self.process_support)
    def guest(self,tool,phase,deadline=None,input_data=None):
        argv=[str(LIMA),'shell',VM,'sudo','--']
        if tool=='switchback-qualification-native':
            argv+=['/usr/bin/systemd-run','--wait','--pipe','--collect','--unit=switchback-qualification-native-'+phase,'--property=Slice=switchback-core.slice','--property=CapabilityBoundingSet=CAP_SYS_ADMIN CAP_SYS_PTRACE','--property=NoNewPrivileges=yes','--property=RestrictAddressFamilies=AF_UNIX','--property=RestrictNamespaces=yes','--property=SystemCallFilter=~@mount @reboot @swap']
        return self.command(argv+[GUEST+tool,phase],deadline,input_data)
    def transfer(self,leaf,to_guest,deadline):
        if to_guest:
            raw=_read_leaf(self.root,leaf);wrapper=json.dumps({'schema':'switchback.qualification.transfer.v1','leaf':leaf,'digest':_digest(raw),'data':base64.b64encode(raw).decode()},separators=(',',':')).encode()
            self.guest('switchback-qualification-material','receive',deadline,wrapper)
        else:
            wrapper=self.guest('switchback-qualification-material','emit-'+leaf,deadline);v=json.loads(wrapper);raw=base64.b64decode(v['data'],validate=True)
            if v['schema']!='switchback.qualification.transfer.v1' or v['leaf']!=leaf or v['digest']!=_digest(raw) or len(raw)>1024*1024:raise WindowFailure('exclusive transfer digest/identity mismatch')
            _exclusive(self.root,leaf,raw)
    def call(self,stage,deadline):
        if stage=='preflight':
            _safe_directory(self.root,os.getuid());_exclusive(self.root,'claimed-driver.json',b'{"phase":"one-shot"}')
            builder=_fixed_source_module(self.kit/'build_candidate.py')
            manifest=builder.verify_kit(self.kit)
            self.process_support=_load_process_support(self.kit,manifest)
            rows=self.command([str(LIMA),'list',VM,'--json']);vm=json.loads(rows)
            if isinstance(vm,list):
                if len(vm)!=1:raise WindowFailure('VM inventory ambiguous')
                vm=vm[0]
            if vm.get('name')!=VM or vm.get('status')!='Running':raise WindowFailure('VM must already be Running')
            # Preparation establishes directories/keys/units/images. No start,
            # image pull/build or service installation is hidden in preflight.
            self.guest('switchback-qualification-material','verify-runtime-preflight')
            self.guest('switchback-qualification-material','prepare-env-binds')
        elif stage=='host-material':
            self.command([str(self.kit/'bin/darwin-arm64/switchback-qualification-material'),'host-preflight']);self.transfer('host-material.json',True,deadline)
        elif stage=='publication-preflight':self.guest('switchback-qualification-install','preflight',deadline)
        elif stage in ('intent-input','create-core','staged','prepare-core','start-waiting','check-pg'):
            self.guest('switchback-qualification-material',stage,deadline)
            if stage=='create-core':
                self.transfer('created.json',False,deadline);v=json.loads(_read_leaf(self.root,'created.json'));self.owned=v['roles'];return self.owned
        elif stage in ('intent','begin','prepare','collect','assemble'):self.guest('switchback-qualification-native',stage,deadline)
        elif stage=='copy-candidate':
            for leaf in ('staged.json','window-open.json','candidate.json'):self.transfer(leaf,False,deadline)
        elif stage=='host-start':self.host=self.threads.submit(self.command,[str(self.kit/'bin/darwin-arm64/switchback-qualification-host')],deadline)
        elif stage=='host-before':
            identity=_safe_directory(self.root,os.getuid())
            while True:
                if self.now()>=deadline:raise WindowFailure('host-before deadline')
                if identity!=_safe_directory(self.root,os.getuid()):raise WindowFailure('host parent drift')
                try:_read_leaf(self.root,'host-before.json');break
                except FileNotFoundError:
                    if self.host.done():self.host.result();raise WindowFailure('host ended before before-facts')
                    time.sleep(min(.1,max(0,deadline-self.now())))
            self.transfer('host-before.json',True,deadline)
        elif stage=='native-transfer':self.transfer('native-observations.json',False,deadline)
        elif stage=='host-finish':self.host.result(timeout=max(.001,deadline-self.now()))
        elif stage=='host-transfer':
            for leaf in ('host-observations.json','common-window.json'):self.transfer(leaf,True,deadline)
        elif stage in ('bootstrap-publication','install'):self.guest('switchback-qualification-install',stage,deadline)
        else:raise WindowFailure('unrecognized fixed stage')
    def preserve(self,evidence):
        try:_exclusive(self.root,'outcome.json',json.dumps(evidence,separators=(',',':')).encode())
        finally:
            self.cancel.set();self.threads.shutdown(wait=True,cancel_futures=True)

def main():
    if sys.argv[1:]==['self-check']:
        result=_run_window(_FakeOperations());print(json.dumps({'self_check':result['status'],'runtime_qualification':False}));return
    if sys.argv[1:]!=['actual']:raise SystemExit('usage: window.py self-check|actual')
    if sys.platform!='darwin':raise SystemExit('actual driver requires Darwin host')
    _run_window(_ActualOperations())
if __name__=='__main__':main()
