#!/usr/bin/env python3
"""Offline Docker CLI contract fixture; never contacts a daemon."""
import contextlib
import hashlib
import re
import shutil
import fcntl
import json
import os
import secrets
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(os.environ['FAKE_DOCKER_STATE']).resolve()
ROOT.mkdir(parents=True, exist_ok=True)
if (ROOT/'control.json').exists():
    os.environ.update(json.loads((ROOT/'control.json').read_text()) or {})
ARGS = sys.argv[1:]
if len(ARGS) < 3 or ARGS[0] != '--context':
    sys.exit('explicit --context required')
CONTEXT, ARGS = ARGS[1], ARGS[2:]

@contextlib.contextmanager
def state():
    with (ROOT / 'state.lock').open('a+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        file = ROOT / 'state.json'
        if file.exists():
            raw = file.read_text()
            if not raw.strip():
                # Tolerate one transient empty read from legacy fixture writers;
                # do not reset or discard resources if it remains corrupt.
                time.sleep(0.01)
                raw = file.read_text()
            value = json.loads(raw)
        else:
            value = {'containers': {'other-container': {'Id': 'other-container', 'Name': '/other-container', 'Config': {'Labels': {}}, 'State': {'Running': True}}},
                     'networks': {'other-network': {'Id': 'other-network', 'Labels': {}}},
                     'volumes': {'other-volume': {'Name': 'other-volume', 'Labels': {}}},
                     'images': {'fixture:image': {'Id': 'fixture:image', 'Config': {'Labels': {}, 'Volumes': {}}},
                                'other-image': {'Id': 'other-image', 'Config': {'Labels': {}, 'Volumes': {}}}}}
        try:
            yield value
        finally:
            # Keep the separate lock inode held through the atomic replacement.
            # Readers not using flock also see a complete old or new snapshot.
            fd, temporary = tempfile.mkstemp(prefix='state-', suffix='.json', dir=ROOT)
            try:
                with os.fdopen(fd, 'w') as out:
                    json.dump(value, out)
                os.replace(temporary, file)
            finally:
                if os.path.exists(temporary):
                    os.unlink(temporary)

def fail(text):
    print(text, file=sys.stderr)
    sys.exit(1)

def emit(v):
    print(json.dumps(v), flush=True)

def option(name, default=None):
    try:
        return ARGS[ARGS.index(name) + 1]
    except ValueError:
        return default

def options(name):
    return [ARGS[i+1] for i, v in enumerate(ARGS[:-1]) if v == name]

def owned(v):
    return any(k.startswith('memeval.') for k in (v.get('Labels', v.get('Config', {}).get('Labels', {})) or {}))

def resolve(resources, ref, kind, missing_ok=False):
    # Tables are keyed by name, but Docker accepts names, full IDs and unique
    # abbreviated IDs. Volume names are never abbreviated.
    if ref in resources:
        return ref, resources[ref]
    matches = [(name, v) for name, v in resources.items()
               if kind != 'volume' and len(ref) >= 12 and v.get('Id', '').startswith(ref)]
    if len(matches) == 1:
        return matches[0]
    if matches:
        fail('ambiguous ' + kind + ' ID: ' + ref)
    if missing_ok:
        return None, None
    if op == 'inspect':
        emit([])
        fail('error: no such object: ' + ref)
    if sub == 'inspect':
        emit([])
    fail('Error response from daemon: No such ' + kind + ': ' + ref)

def tmp_writable(container, uid, gid):
    for spec in container['_tmpfs']:
        path, _, raw = spec.partition(':')
        if path != '/tmp':
            continue
        opts = dict((s.split('=', 1) + [''])[:2] for s in raw.split(','))
        # A missing mountpoint in FROM scratch inherits a root-owned 0755
        # directory, unlike the existing 1777 /tmp in uv/node base images.
        default_mode = '0755' if container['_scratch'] else '1777'
        mode = int(opts.get('mode', default_mode), 8)
        owner, group = int(opts.get('uid', '0')), int(opts.get('gid', '0'))
        bits = mode >> (6 if uid == owner else 3 if gid == group else 0)
        return 'ro' not in opts and (uid == 0 or bits & 0o3 == 0o3)
    return False

with (ROOT / 'calls.lock').open('a+') as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    with (ROOT / 'calls.jsonl').open('a') as f:
        f.write(json.dumps(sys.argv[1:]) + '\n')

op = ARGS[0]
sub = ARGS[1] if len(ARGS)>1 else ''
if os.environ.get('FAKE_DOCKER_FAIL') in (op, op + ' ' + sub):
    fail('injected failure: ' + op + ' ' + sub)
if any(x in ARGS for x in ['prune', '-p', '--publish', '--rm', '-t']) and op != 'build':
    fail('unsafe Docker operation')

if op == 'version':
    emit({'Client': {'Version': '29.8.2'}, 'Server': {'Version': '29.8.2', 'Os': 'linux', 'Arch': os.environ.get('FAKE_DOCKER_ARCH','arm64')}})
elif op == 'info':
    emit({'OSType': 'linux', 'Architecture': os.environ.get('FAKE_DOCKER_ARCH','arm64'), 'KernelVersion': '7.2.9', 'Driver': 'overlay2', 'SecurityOptions': ['name=no-new-privileges']})
elif op == 'image' and sub == 'inspect':
    with state() as s:
        name=ARGS[-1]
        if name.startswith('other-'): fail('unrelated image inspected')
        name,v=resolve(s['images'],name,'image')
        if name=='fixture:image': v['Config']['Volumes']={x:{} for x in json.loads(os.environ.get('FAKE_DOCKER_IMAGE_VOLUMES','[]'))}
        emit([v])
elif op == 'pull':
    ref=ARGS[-1]
    if not re.fullmatch(r'[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}',ref):fail('pull requires locked digest')
    digest=ref.rsplit('@',1)[1]
    volumes={'/var/lib/postgresql':{}} if 'pgvector/' in ref else {}
    with state() as s:s['images'][ref]={'Id':'sha256:'+hashlib.sha256(ref.encode()).hexdigest(),'Os':'linux','Architecture':option('--platform').split('/')[1],'RepoDigests':[ref],'Size':123000000,'Config':{'Labels':{},'Volumes':volumes,'Env':['PATH=/usr/local/bin:/usr/bin:/bin:/usr/lib/postgresql/18/bin']},'_preset':True}
    print(ref)
elif op == 'build':
    labels=dict(x.split('=',1) for x in options('--label'))
    context=Path(ARGS[-1]);recipe=Path(option('--file',str(context/'Dockerfile'))).read_text()
    tag=option('--tag',option('-t'))
    if option('--target') in ('lockfile','lockout'):
        dest=Path(option('--output').split('dest=',1)[1]);dest.mkdir(parents=True,exist_ok=True)
        if (context/'project/package.json').exists():
            package=json.loads((context/'project/package.json').read_text());package.update(lockfileVersion=3,packages={});(dest/'package-lock.json').write_text(json.dumps(package))
        else:
            req=(context/'project/requirements.in').read_text()
            out='# This file was autogenerated by uv via the following command:\n#    uv pip compile requirements.in --generate-hashes\n# memeval-lock-platform: '+option('--platform')+'\n'
            for line in req.splitlines():
                if line.strip() and not line.startswith('#'):out+=line+' --hash=sha256:'+hashlib.sha256(line.encode()).hexdigest()+'\n'
            (dest/'requirements.lock.txt').write_text(out)
        print('fixture lock exported')
    elif option('--target')=='env':
        prepares=[line for line in recipe.splitlines() if line.startswith('RUN HOME=/opt/memeval/env/models-home ')]
        if prepares:
            with state() as s:
                attempts=s.setdefault('prepare_builds',{})
                attempts[tag]=attempts.get(tag,0)+1
                attempt=attempts[tag]
            if attempt <= int(os.environ.get('FAKE_DOCKER_BUILD_FLAKY','0')):
                print('#15 [env 10/11] '+prepares[0],file=sys.stderr)
                print('#15 0.1 '+os.environ.get('FAKE_DOCKER_BUILD_ERROR','TypeError: fetch failed: Client network socket disconnected before secure TLS connection was established'),file=sys.stderr)
                fail('#15 ERROR: process "/bin/sh -c prepare" did not complete successfully: exit code: 1')
        wheels=context/'assets/_wheels'
        if wheels.exists():
            lock=(context/'requirements.lock.txt').read_text()
            for wheel in wheels.glob('*.whl'):
                if hashlib.sha256(wheel.read_bytes()).hexdigest() not in lock:fail('Hash mismatch for fixture wheel')
        digest=hashlib.sha256(recipe.encode()).hexdigest();identifier='sha256:'+digest
        destination=ROOT/'built'/digest
        if destination.exists():shutil.rmtree(destination)
        destination.mkdir(parents=True)
        for folder in ('project','bridge','assets'):shutil.copytree(context/folder,destination/folder)
        Path(option('--iidfile')).write_text(identifier+'\n')
        with state() as s:s['images'][tag]={'Id':identifier,'Os':'linux','Architecture':option('--platform').split('/')[1],'Size':204000000,'_runtime_version':(re.search(r"uv venv --python '([^']+)'",recipe).group(1) if 'uv venv --python' in recipe else '24.22.0'),'Config':{'Labels':labels,'Volumes':{}},'_root':str(destination)}
        print('fixture env image built '+identifier)
    else:
        assert recipe == 'FROM scratch\nCOPY memeval /memeval\n'
        identifier='sha256:'+hashlib.sha256(recipe.encode()+(context/'memeval').read_bytes()).hexdigest()
        with state() as s:s['images'][tag]={'Id':identifier,'Config':{'Labels':labels,'Volumes':{}},'_scratch':True}
        print(tag)
elif op=='image' and sub=='rm' or op=='rmi':
    with state() as s:
        name,v=resolve(s['images'],ARGS[-1],'image')
        if not owned(v) and not v.get('_preset'):fail('unrelated image removed')
        if os.environ.get('FAKE_DOCKER_LEAVE_IMAGE')!='1':s['images'].pop(name)
    print(ARGS[-1])
elif op == 'create':
    opts={}; labels={}; mounts=[]; tmpfs=[]; i=1
    boolean={'--read-only','--init','--interactive'}
    while i<len(ARGS) and ARGS[i].startswith('-'):
        k=ARGS[i]
        if k in boolean:opts[k]=True;i+=1;continue
        v=ARGS[i+1];i+=2
        if k=='--label':labels.update([v.split('=',1)])
        elif k=='-v':
            parts=v.split(':');mounts.append({'Type':'bind','Name':'','Source':parts[0],'Destination':parts[1],'RW':len(parts)<3 or parts[2]!='ro'})
        elif k=='--tmpfs':tmpfs.append(v)
        else:opts[k]=v
    image=ARGS[i];command=ARGS[i+1:];name=opts['--name'];env={}
    if opts.get('--init'):
        mounts.append({'Type':'bind','Name':'','Source':'/engine/docker-init','Destination':'/usr/sbin/docker-init','RW':os.environ.get('FAKE_DOCKER_INIT_INSPECT_RW')=='1'})
    path=opts.get('--env-file')
    if path:
        if os.stat(path).st_mode & 0o777 != 0o600:fail('env file not private')
        for line in Path(path).read_text().splitlines():k,v=line.split('=',1);env[k]=v
    with state() as s:
        _,image_info=resolve(s['images'],image,'image')
        # Docker inherits image labels, then applies explicit create overrides.
        # Copy the mapping so container overrides never mutate the image.
        labels={**(image_info['Config'].get('Labels') or {}), **labels}
        if os.environ.get('FAKE_DOCKER_LEAVE_VOLUME')=='1' and labels.get('memeval.role')!='probe':
            vol='anonymous-'+name;s['volumes'][vol]={'Name':vol,'Labels':{}}
            mounts.append({'Type':'volume','Name':vol,'Source':'','Destination':'/unexpected','RW':True})
        if image_info.get('_preset') and any(k in env for k in ('PATH','PYTHONPATH')):
            fail('independent image runtime environment overwritten: initdb: command not found')
        effective_env=dict(x.split('=',1) for x in image_info['Config'].get('Env',[]))
        effective_env.update(env)
        for target in image_info['Config'].get('Volumes',{}):
            if target not in [p.split(':',1)[0] for p in tmpfs]:
                vol='anonymous-volume-'+name;s['volumes'][vol]={'Name':vol,'Labels':{}}
                mounts.append({'Type':'volume','Name':vol,'Destination':target,'RW':True})
        network=opts.get('--network','none')
        identifier=secrets.token_hex(32)
        s['containers'][name]={'Id':identifier,'Name':'/'+name,'Created':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'Config':{'Labels':labels,'Image':image,'Cmd':command,'Env':[k+'='+v for k,v in sorted(effective_env.items())]},
          'State':{'Running':False,'OOMKilled':False,'ExitCode':0,'StartedAt':''},
          'HostConfig':{'ReadonlyRootfs':bool(opts.get('--read-only')),'NetworkMode':network,'PortBindings':{'8080/tcp':[{'HostPort':'123'}]} if os.environ.get('FAKE_DOCKER_PUBLISHED_PORT')=='1' else {},'CapDrop':['ALL'],'SecurityOpt':['no-new-privileges']},
          'NetworkSettings':{'Networks':{network:{'IPAddress':'172.22.0.'+str(len(s['containers'])+2),'Aliases':[opts['--network-alias']] if opts.get('--network-alias') else []}} if network!='none' else {},'Ports':{}},'Mounts':mounts,
          '_command':command,'_env':env,'_opts':opts,'_tmpfs':tmpfs,'_preset':image_info.get('_preset',False),'_scratch':image_info.get('_scratch',False),'_root':image_info.get('_root',''),'_runtime_version':image_info.get('_runtime_version','')}
    if labels.get('memeval.role')=='governor' and os.environ.get('FAKE_DOCKER_RELAY_EXTRA_TARGET')=='1':
        with state() as s:s['containers'][name]['Config']['Cmd']=command+['--target','127.0.0.1:9999']
    print(identifier)
elif op == 'start':
    with state() as s:
        name,v=resolve(s['containers'],ARGS[-1],'container')
        v['State'].update(Running=True,StartedAt='2026-10-18T00:00:00Z');command=v['_command'];env=v['_env'];cwd=v['_opts'].get('--workdir','/tmp')
        if v['Config']['Labels'].get('memeval.role')=='sut' and '--out' not in command:
            network=v['_opts'].get('--network')
            for g in s['containers'].values():
                if g['Config']['Labels'].get('memeval.role')=='governor' and network in g['NetworkSettings']['Networks']:
                    g['_sut_starts']=g.get('_sut_starts',0)+1
                    if os.environ.get('FAKE_DOCKER_GOVERNOR_METER')=='1':
                        dest=Path(next(m['Source'] for m in g['Mounts'] if m['Destination']=='/meter'))
                        ip=v['NetworkSettings']['Networks'][network]['IPAddress']
                        row={'t':time.strftime('%Y-%m-%dT%H:%M:%S',time.gmtime())+'.%06dZ'%int(time.time()%1*1000000),'episode_ip':ip,'path':'/v1/chat/completions','model':'fixture','status':200,'attempt':1,'usage':{'prompt_tokens':100,'completion_tokens':20},'queue_wait_ms':1}
                        with (dest/'attempts.jsonl').open('a') as log:log.write(json.dumps(row)+'\n')
                        if os.environ.get('FAKE_DOCKER_GOVERNOR_DISCONNECT')=='1':
                            disconnected={**row,'status':0,'error':'client disconnected','not_forwarded':True,'usage':None,'queue_wait_ms':28000}
                            with (dest/'attempts.jsonl').open('a') as log:log.write(json.dumps(disconnected)+'\n')
    if v.get('_preset') and not command:
        print('fixture sidecar stdout',flush=True);print('fixture sidecar stderr',file=sys.stderr,flush=True)
        while True:
            with state() as s:
                if name not in s['containers'] or not s['containers'][name]['State']['Running']:break
            time.sleep(.02)
    elif command[:2]==['/memeval','__container-governor']:
        # HTTP behavior is exercised against httptest independently of the CLI.
        meter=Path(next(m['Source'] for m in v['Mounts'] if m['Destination']=='/meter'))
        (meter/'attempts.jsonl').write_text('');(meter/'attempts.jsonl').chmod(0o600)
        with state() as s:s['containers'][name]['_sut_starts']=0
        print(name)
    elif command[:2]==['/memeval','__container-ready']:
        with state() as s:
            ref=v['_opts']['--network'].removeprefix('container:')
            _,relay=resolve(s['containers'],ref,'container')
            passed=relay['State']['Running'] and os.environ.get('FAKE_DOCKER_READY_FAIL')!='1'
            # Probe has exactly the target's network namespace; no host port is used.
            if not passed: s['containers'][name]['State']['_ready_error']='connection refused'
            s['containers'][name]['State'].update(Running=False,ExitCode=0 if passed else 1)
        if not passed:fail('relay TCP readiness failed')
    elif command[:2]==['/memeval','__container-probe'] and '--out' in command:
        opts=json.loads(Path(command[command.index('--options')+1]).read_text())
        network=v['_opts'].get('--network','none')
        with state() as s: restricted=network=='none' or s['networks'].get(network,{}).get('Internal',False)
        names=['gold_absent','host_paths_absent','write_episode','write_rootfs_fails','write_tmp_ok','env_read','uid_nonroot','caps_dropped','no_new_privs','rootfs_readonly','pids_limit','mounts_expected','environment']
        items={k:{'success':True,'duration_ms':0} for k in names}
        binary=next(m['Source'] for m in v['Mounts'] if m['Destination']=='/memeval')
        mountinfo=(Path(__file__).parent/'probe.mountinfo').read_text().replace('{ep}',opts['episode_dir']).replace('{binary}',binary)
        if not v['_opts'].get('--init'):
            mountinfo='\n'.join(line for line in mountinfo.splitlines() if '/usr/sbin/docker-init' not in line)
        elif not opts.get('init'):
            items['mounts_expected']['success']=False
        if os.environ.get('FAKE_DOCKER_INIT_MOUNT_RW')=='1':
            mountinfo=mountinfo.replace('/usr/sbin/docker-init ro,','/usr/sbin/docker-init rw,')
            items['mounts_expected']['success']=False
        for k in ['tcp4_control','udp_control','dns_external','https_external']:items[k]={'success':not restricted,'duration_ms':0}
        items['tcp6']={'success':False,'duration_ms':0,'detail':'offline fixture, not asserted'}
        if opts.get('gateway_url'):items['gateway_connect']={'success':True,'duration_ms':0}
        for name in opts.get('services',{}):
            for suffix in ['connect','dns']:items['sidecar_'+name+'_'+suffix]={'success':True,'duration_ms':0}
        failure=os.environ.get('FAKE_DOCKER_PROBE_FAIL','')
        if '--fail' in command:failure=command[command.index('--fail')+1]
        for name in failure.split(','):
            if name in items:items[name]['success']=not items[name]['success']
        output=Path(command[command.index('--out')+1]);output.write_text(json.dumps({'items':items,'facts':{'fixture':True,'mountinfo':mountinfo}}));output.chmod(0o600)
        with state() as s:s['containers'][v['Name'].lstrip('/')]['State'].update(Running=False,ExitCode=0)
    elif command==['/memeval','__container-probe','caps']:
        dest=next(m['Source'] for m in v['Mounts'] if m['Destination']=='/probe')
        (Path(dest)/'memeval-probe-owner').write_text('probe')
        uid,gid=map(int,v['_opts']['--user'].split(':'))
        r={'uid':uid,'gid':gid,'cap_eff_hex':'0000000000000000','no_new_privs':True,'rootfs_readonly':v['HostConfig']['ReadonlyRootfs'],'probe_writable':True,'tmp_writable':tmp_writable(v,uid,gid),'pid':1,'hostname':name,'env_keys':[]}
        k=os.environ.get('FAKE_DOCKER_CAPS_FALSE')
        if k: r[k]=0 if k=='uid' else (False if k!='cap_eff_hex' else '0001')
        emit(r)
        with state() as s:s['containers'][name]['State'].update(Running=False,ExitCode=0)
    elif any('memeval-runtime-probe' in x for x in command):
        version=v['_runtime_version'];version=version if version.count('.')==2 else version+'.12'
        emit({'version':version,'executable':'/usr/local/bin/node' if command[0].endswith('/node') else '/opt/memeval/env/.venv/bin/python','base_prefix':'/usr/local'})
        with state() as s:s['containers'][name]['State'].update(Running=False,ExitCode=0)
    else:
        if v.get('_root'):
            image_root=v['_root']
            command=[x.replace('/opt/memeval/env',image_root) for x in command]
            if command[0].endswith('/.venv/bin/python'):command[0]=sys.executable
            if command[0]=='/usr/local/bin/node':command[0]=shutil.which('node')
            env={k:x.replace('/opt/memeval/env',image_root) for k,x in env.items()}
            cwd=cwd.replace('/opt/memeval/env',image_root)
        child=subprocess.Popen(command,stdin=sys.stdin.buffer,stdout=sys.stdout.buffer,stderr=sys.stderr.buffer,cwd=cwd,env=env,start_new_session=True)
        with state() as s:s['containers'][name]['_pid']=child.pid
        code=child.wait();code=code if code>=0 else 128-code
        with state() as s:
            if name in s['containers']:s['containers'][name]['State'].update(Running=False,ExitCode=code,OOMKilled=os.environ.get('FAKE_DOCKER_OOM')=='1')
        sys.exit(code)
elif op == 'inspect' or op == 'container' and sub == 'inspect':
    with state() as s:
        name,v=resolve(s['containers'],ARGS[-1],'container')
        if not owned(v):fail('unrelated container inspected')
        v=json.loads(json.dumps(v))
        if v['Config']['Labels'].get('memeval.role')=='sidecar':
            mutation=os.environ.get('FAKE_DOCKER_SERVICE_AUDIT','')
            if mutation=='stopped':v['State']['Running']=False
            if mutation=='published':v['HostConfig']['PortBindings']={'9999/tcp':[{'HostPort':'12345'}]}
            if mutation=='extra_network':v['NetworkSettings']['Networks']['bridge']={}
            if mutation=='ports':v['NetworkSettings']['Ports']={'9999/tcp':[{'HostPort':'12345'}]}
        emit([v])
elif op=='kill':
    with state() as s:
        name,v=resolve(s['containers'],ARGS[-1],'container')
        if not owned(v):fail('unrelated container killed')
        if '_pid' not in v:v['State'].update(Running=False,ExitCode=143)
        if '_pid' in v:
            try:os.killpg(v['_pid'],signal.SIGTERM)
            except ProcessLookupError:pass
    print(ARGS[-1])
elif op=='wait':
    while True:
        with state() as s:
            name,v=resolve(s['containers'],ARGS[-1],'container')
            if not v['State']['Running']:
                print(v['State']['ExitCode']);break
        time.sleep(.01)
elif op=='rm':
    with state() as s:
        name,v=resolve(s['containers'],ARGS[-1],'container',missing_ok='-f' in ARGS)
        if v is None:sys.exit(0)
        if not owned(v):fail('unrelated container removed')
        if v.get('_pid'):
            try:os.killpg(v['_pid'],signal.SIGKILL)
            except ProcessLookupError:pass
        if not (v['Config']['Labels'].get('memeval.role')=='run-residue' and os.environ.get('FAKE_DOCKER_RUN_ORPHAN')=='stuck') and os.environ.get('FAKE_DOCKER_LEAVE_CONTAINER')!='1' and not ((v['Config']['Labels'].get('memeval.role')=='governor' and os.environ.get('FAKE_DOCKER_LEAVE_RELAY')=='1') or (v['Config']['Labels'].get('memeval.role')=='sidecar' and os.environ.get('FAKE_DOCKER_LEAVE_SIDECAR')=='1')):
            del s['containers'][name]
    print(ARGS[-1])
elif op=='images' or op=='ps' or op in ['network','volume'] and sub=='ls':
    filters=options('--filter')
    if not filters or not all(x.startswith(('label=memeval.','name=memeval-')) for x in filters):fail('unscoped list')
    kind={'images':'images','ps':'containers','network':'networks','volume':'volumes'}[op]
    with state() as s:
        run_filter=next((x for x in filters if x.startswith('label=memeval.run=')), '')
        if op=='ps' and run_filter and os.environ.get('FAKE_DOCKER_RUN_ORPHAN') and not s.get('run_orphan_injected'):
            s['run_orphan_injected']=True
            s['containers']['memeval-run-orphan']={'Id':secrets.token_hex(32),'Name':'/memeval-run-orphan','Config':{'Labels':{'memeval.run':run_filter.split('=',2)[2],'memeval.role':'run-residue'}},'State':{'Running':False}}
        seen=set()
        for name,v in s[kind].items():
            labels=v.get('Labels',v.get('Config',{}).get('Labels',{})) or {}
            def matches(expr):
                if expr.startswith('name='):return name.startswith(expr[5:])
                key,sep,value=expr[6:].partition('=')
                return key in labels and (not sep or labels[key]==value)
            if not all(matches(x) for x in filters):continue
            if os.environ.get('FAKE_DOCKER_HIDE_LIST') == op:continue
            identifier=v.get('Id',name)
            if option('--format'):
                row={'ID':identifier,'Name':name,'Names':name,'Labels':','.join(k+'='+v for k,v in labels.items())}
                if op=='images':
                    repository,_,tag=name.rpartition(':');row.update(Repository=repository,Tag=tag)
                emit(row)
            elif identifier not in seen:
                seen.add(identifier)
                print(name if op=='volume' else identifier if '--no-trunc' in ARGS else identifier[:12])
elif op=='network' and sub=='create':
    name=ARGS[-1];identifier=secrets.token_hex(32)
    with state() as s:s['networks'][name]={'Id':identifier,'Name':name,'Created':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'Internal':'--internal' in ARGS,'Labels':dict(x.split('=',1) for x in options('--label'))}
    print(identifier)
elif op=='network' and sub in ('connect','disconnect'):
    network=ARGS[-2]
    with state() as s:
        name,v=resolve(s['containers'],ARGS[-1],'container')
        if v['Config']['Labels'].get('memeval.role')!='governor':fail('only governor may share networks')
        if sub=='connect':
            if network not in s['networks'] or not s['networks'][network]['Internal']:fail('governor requires internal episode network')
            v['NetworkSettings']['Networks'][network]={'Aliases':[option('--alias')]}
        else:v['NetworkSettings']['Networks'].pop(network,None)
elif op in ['network','volume'] and sub=='inspect':
    with state() as s:
        name,v=resolve(s[op+'s'],ARGS[-1],op)
        if name.startswith('other-'):fail('unrelated resource inspected')
        v=json.loads(json.dumps(v))
        if op=='network' and os.environ.get('FAKE_DOCKER_SERVICE_AUDIT')=='external':v['Internal']=False
        emit([v])
elif op in ['network','volume'] and sub=='rm':
    with state() as s:
        name,v=resolve(s[op+'s'],ARGS[-1],op)
        if name.startswith('other-'):fail('unrelated resource removed')
        if op=='volume' and os.environ.get('FAKE_DOCKER_LEAVE_VOLUME')=='1':pass
        else:s[op+'s'].pop(name,None)
    print(ARGS[-1])
elif op=='exec':
    with state() as s:
        name,v=resolve(s['containers'],ARGS[1],'container')
        if not owned(v):fail('unrelated container exec')
        if not v['State']['Running']:fail('container not running')
        v['_exec_attempts']=v.get('_exec_attempts',0)+1
        attempt=v['_exec_attempts']
    if os.environ.get('FAKE_DOCKER_EXEC_FAIL')=='1' or attempt<=int(os.environ.get('FAKE_DOCKER_EXEC_RETRIES','0')):fail('fixture exec not ready')
    if ARGS[2:4]==['/memeval','__container-governor-status']:
        if os.environ.get('FAKE_DOCKER_READY_FAIL')=='1':fail('governor readiness failed')
        status={'state':os.environ.get('FAKE_DOCKER_GOVERNOR_STATE','closed'),'reason':'fixture','retry_after_s':float(os.environ.get('FAKE_DOCKER_GOVERNOR_RETRY_AFTER','0')),'billing':{},'circuit_events':[]}
        limit=int(os.environ.get('FAKE_DOCKER_GOVERNOR_AFTER_SUTS','0'))
        if limit and v.get('_sut_starts',0)>=limit:status['state']=os.environ.get('FAKE_DOCKER_GOVERNOR_STOP','budget')
        emit(status);sys.exit(0)
    if ARGS[2] in ['pg_isready','psql']:
        print('fixture exec stdout');print('fixture exec stderr',file=sys.stderr)
        sys.exit(0)
    command=ARGS[2:];env=v['_env'];cwd=v['_opts'].get('--workdir','/tmp')
    if v.get('_root'):
        command=[x.replace('/opt/memeval/env',v['_root']) for x in command]
        if command[0].endswith('/.venv/bin/python'):command[0]=sys.executable
        env={k:x.replace('/opt/memeval/env',v['_root']) for k,x in env.items()}
        cwd=cwd.replace('/opt/memeval/env',v['_root'])
    sys.exit(subprocess.call(command,env=env,cwd=cwd))
elif op=='system' and sub=='df':
    # Aggregate-only disclosure, deliberately includes unrelated shared usage.
    print(json.dumps({'Type':'Images','TotalCount':'30','Size':'7.8GB','Reclaimable':'0B'}))
elif op=='run':
    create=subprocess.run([sys.executable,__file__,'--context',CONTEXT,'create']+ARGS[1:],capture_output=True)
    if create.returncode:sys.stderr.buffer.write(create.stderr);sys.exit(create.returncode)
    name=create.stdout.decode().strip()
    sys.exit(subprocess.call([sys.executable,__file__,'--context',CONTEXT,'start','--attach','--interactive',name]))
else:
    fail('unsupported fake operation '+str(ARGS))
