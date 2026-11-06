#!/usr/bin/env python3
"""Pinned local Go/offline kit build. No VM, Engine, provider or Docker calls."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import struct
import subprocess
import sys

ROOT=Path(__file__).resolve().parents[3]
PHASE=ROOT/'.scratch/runtime-m1-qualification-delivery-1022/task-4'
OUTPUT=PHASE/'kit'
GO_VERSION='go1.26.4'
ENV={'TMPDIR':'/private/tmp','GOCACHE':'/private/tmp/switchback-qualification-1022-go-cache','GOTOOLCHAIN':'local','GOPROXY':'off','GOMAXPROCS':'4','CGO_ENABLED':'0','GOSUMDB':'off','GOFLAGS':'-mod=readonly','GOENV':'off','GOWORK':'off','GOEXPERIMENT':''}
BLOCK={'.git','.scratch','.lima','.env','data','journals','keys','private','secrets','secret','state','qualification','trust.json','publisher-bindings.json','admission-manifest.json','deployment.env'}
COMMANDS={'backend':'switchback-backend','publisher':'switchback-capacity-publisher','observer':'switchback-created-layer-observer','worker':'worker-shim','native-producer':'switchback-qualification-native','installer':'switchback-qualification-install','material-producer':'switchback-qualification-material','worker-native-helper':'switchback-qualification-ioctl','worker-compat-helper':'switchback-qualification-ioctl'}
def digest(raw):return 'sha256:'+hashlib.sha256(raw).hexdigest()
def encode(value):return json.dumps(value,separators=(',',':'),ensure_ascii=False).encode()
def record(path,relative):
    s=path.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or s.st_mode&0o022 or s.st_size<=0:raise RuntimeError('unsafe source input '+str(relative))
    raw=path.read_bytes();a=path.lstat()
    if (s.st_dev,s.st_ino,s.st_size,s.st_mtime_ns,s.st_ctime_ns)!=(a.st_dev,a.st_ino,a.st_size,a.st_mtime_ns,a.st_ctime_ns):raise RuntimeError('source changed')
    return {'path':str(relative),'type':'regular','mode':stat.S_IMODE(s.st_mode),'bytes':len(raw),'digest':digest(raw)}
def inputs(root=ROOT):
    files=[];templates=[]
    selected=[root/'go.mod',root/'go.sum']
    for name in ('cmd','internal','images','migrations','web','deploy/experience'):
        selected.extend(p for p in (root/name).rglob('*') if p.is_file() and not any(x in ('node_modules','dist','__pycache__') for x in p.parts))
    selected.append(root/'deploy/local/migrate-postgres.sh')
    for p in sorted(set(selected)):
        rel=p.relative_to(root)
        if p.suffix in ('.pyc','.test'):continue
        if 'qualification' in rel.parts:
            templates.append(record(p,rel));continue
        if any(part.lower() in BLOCK for part in rel.parts):continue
        files.append(record(p,rel))
    if not files or len(files)>4096:raise RuntimeError('source coverage bound')
    return files,templates

def run(argv,name,extra=None):
    env=dict(os.environ);env.pop('SWITCHBACK_TEST_DATABASE_URL',None);env.update(ENV);env.update(extra or {})
    p=subprocess.run(argv,cwd=str(ROOT),env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=180)
    result={'argv':argv,'cwd':str(ROOT),'env':dict(ENV,**(extra or {})),'unset':['SWITCHBACK_TEST_DATABASE_URL'],'stdout':p.stdout.decode(errors='replace'),'stderr':p.stderr.decode(errors='replace'),'exit':p.returncode}
    (OUTPUT/'receipts'/('%s.json'%name)).write_bytes(encode(result))
    if p.returncode:raise RuntimeError('offline build failed: '+name)
    return p.stdout

def verify_kit(root):
    root=Path(root)
    manifest=json.loads((root/'manifest.json').read_bytes())
    if manifest.get('schema')!='switchback.qualification.offline_kit.v1' or manifest.get('runtime_qualification') is not False:raise RuntimeError('invalid offline kit schema')
    for f in manifest['files']:
        path=root/f['path']
        if path.is_symlink() or not path.is_file() or digest(path.read_bytes())!=f['digest'] or path.stat().st_size!=f['bytes']:raise RuntimeError('kit artifact drift: '+f['path'])
    if any(value is not None for value in manifest['unresolved_runtime'].values()):raise RuntimeError('offline kit fabricated runtime identity')
    return manifest

def build():
    global OUTPUT
    before,templates=inputs()
    for attempt in range(1,101):
        OUTPUT=PHASE/('kit-%04d'%attempt)
        try:OUTPUT.mkdir(mode=0o700,parents=False,exist_ok=False);break
        except FileExistsError:continue
    else:raise RuntimeError('offline attempt bound exhausted')
    (OUTPUT/'receipts').mkdir(mode=0o700)
    go=shutil.which('go')
    if not go:raise RuntimeError('local Go missing')
    version=run([go,'version'],'go-version').decode().split()
    if len(version)<3 or version[2]!=GO_VERSION:raise RuntimeError('pinned local Go differs')
    go_meta=record(Path(go).resolve(),Path(go).name)
    run([go,'list','-m'],'module-main')
    run([go,'env','GOVERSION','GOHOSTOS','GOHOSTARCH','GOMOD','GOTOOLDIR','CGO_ENABLED','GOPROXY','GOSUMDB'],'go-env')
    source=OUTPUT/'source';source.mkdir(mode=0o700)
    for entry in before+templates:
        p=source/entry['path'];p.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(ROOT/entry['path'],p);p.chmod(entry['mode'])
    for p in (source/'deploy/experience/qualification').glob('*.py'):shutil.copyfile(p,OUTPUT/p.name)
    binaries=[]
    for arch in ('arm64','amd64'):
        folder=OUTPUT/'bin'/('linux-'+arch);folder.mkdir(parents=True)
        for role,cmd in COMMANDS.items():
            name={'worker-native-helper':'switchback-qualification-ioctl-native','worker-compat-helper':'switchback-qualification-ioctl-compat'}.get(role,cmd)
            target=folder/name;build_arch=('arm' if arch=='arm64' else '386') if role=='worker-compat-helper' else arch
            extra={'GOOS':'linux','GOARCH':build_arch}
            if build_arch=='arm':extra['GOARM']='7'
            run([go,'build','-p','2','-trimpath','-buildvcs=false','-ldflags=-s -w','-o',str(target),'./cmd/'+cmd],arch+'-'+role,extra)
            header=target.read_bytes()[:64];want_class=1 if role=='worker-compat-helper' else 2;machine=struct.unpack('<H',header[18:20])[0];want_machine={'arm64':183,'amd64':62,'arm':40,'386':3}[build_arch]
            if header[:4]!=b'\x7fELF' or header[4]!=want_class or header[5]!=1 or machine!=want_machine:raise RuntimeError('ELF identity mismatch')
            run([go,'version','-m',str(target)],arch+'-'+role+'-build-info')
            f=record(target,target.relative_to(OUTPUT));f.update(role=role,goos='linux',goarch=build_arch,elf_class=want_class,elf_machine=machine);binaries.append(f)
    hostdir=OUTPUT/'bin/darwin-arm64';hostdir.mkdir(parents=True)
    for role,cmd in (('host-producer','switchback-qualification-host'),('host-material','switchback-qualification-material')):
        target=hostdir/cmd;run([go,'build','-p','2','-trimpath','-buildvcs=false','-ldflags=-s -w','-o',str(target),'./cmd/'+cmd],role,{'GOOS':'darwin','GOARCH':'arm64'});run([go,'version','-m',str(target)],role+'-build-info');f=record(target,target.relative_to(OUTPUT));f.update(role=role,goos='darwin',goarch='arm64');binaries.append(f)
    for arch in ('arm64','amd64'):
        native=[f for f in binaries if f.get('goos')=='linux' and f['path'].startswith('bin/linux-'+arch+'/')]
        host=next(f for f in binaries if f['role']=='host-producer')
        artifacts=[{'role':f['role'],'path':'/opt/switchback/qualification-kit/bin/'+Path(f['path']).name,'digest':f['digest']} for f in native]+[{'role':'host-producer','path':'/opt/switchback/qualification-kit/bin/switchback-qualification-host','digest':host['digest']}]
        runtime={'schema':'switchback.qualification.kit_inputs.v1','architecture':arch,'toolchain':GO_VERSION,'source':before,'artifacts':artifacts,'templates':templates}
        (OUTPUT/('kit-inputs-'+arch+'.json')).write_bytes(encode(runtime))
    # Context is the exact copied source tree and current pinned Dockerfiles.
    # Image construction remains a later explicit operation; this never calls it.
    after,after_templates=inputs()
    if before!=after or templates!=after_templates:raise RuntimeError('source input changed during build')
    receipts={'go':go_meta,'source_input_root':digest(encode(before)),'qualification_templates_root':digest(encode(templates)),'before':before,'after':after,'templates_before':templates,'templates_after':after_templates,'binaries':binaries}
    (OUTPUT/'build-receipt.json').write_bytes(encode(receipts))
    allfiles=[record(p,p.relative_to(OUTPUT)) for p in sorted(OUTPUT.rglob('*')) if p.is_file()]
    manifest={'schema':'switchback.qualification.offline_kit.v1','runtime_qualification':False,'toolchain':GO_VERSION,'source_input_root':receipts['source_input_root'],'qualification_templates_root':receipts['qualification_templates_root'],'files':allfiles,'unresolved_runtime':dict.fromkeys(('image_ids','container_ids','guest_boot','host_boot','public_identity','host_volume_readback','engine_identity','native_receipt','host_receipt','trust','manifest','journal','pg_preparation'))}
    (OUTPUT/'manifest.json').write_bytes(encode(manifest));verify_kit(OUTPUT)
    print(json.dumps({'kit':str(OUTPUT),'files':len(allfiles),'artifacts':len(binaries),'runtime_qualification':False}))

def main():
    if sys.argv[1:]==['self-check']:
        f,t=inputs();print(json.dumps({'source_files':len(f),'templates':len(t),'runtime_qualification':False}));return
    if sys.argv[1:]!=['build']:raise SystemExit('usage: build_candidate.py self-check|build')
    build()
if __name__=='__main__':main()
