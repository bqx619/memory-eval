#!/usr/bin/env python3
"""Offline HaluMem evidence. Outputs stay in scratch; ND text never enters output proofs."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import argparse, collections, hashlib, json, pathlib, subprocess, time
ROOT = pathlib.Path(__file__).resolve().parents[1]
def sha(data): return hashlib.sha256(data).hexdigest()
def canonical(v): return json.dumps(v, ensure_ascii=False, sort_keys=True, separators=(',', ':')).replace('\u2028','\\u2028').replace('\u2029','\\u2029').encode()
def main(args, work):
    a=args;out=work.out;empty=work.env_file;env=work.env;commands=[]
    def run(args,name):
        cmd=[str(work.binary()),'--env-file',str(empty)]+args;start=time.monotonic();r=subprocess.run(cmd,cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        (out/(name+'.stdout')).write_bytes(r.stdout);(out/(name+'.stderr')).write_bytes(r.stderr);commands.append(dict(command=cmd,exit=r.returncode,seconds=time.monotonic()-start));assert r.returncode==0,(name,r.stderr.decode());return json.loads(r.stdout)
    reg=json.loads((ROOT/'data/registry/halumem.json').read_text());assert reg['implementation_status']=='imported' and reg['license']['redistribute_normalized'] is False
    results=[]
    for ref in reg['suites']:
        variant=ref['protocol_ref'].split('#')[1];path=ROOT/ref['path'];source=ROOT/'data/upstream/halumem'/('HaluMem-'+variant.title()+'.jsonl')
        users=sessions=turns=points=updates=questions=generated=0;sources=collections.Counter();types=collections.Counter();qtypes=collections.Counter()
        with source.open() as f:
            for line in f:
                u=json.loads(line);users+=1
                for s in u['sessions']:
                    sessions+=1;turns+=len(s['dialogue']);generated+=bool(s.get('is_generated_qa_session',False))
                    for m in s['memory_points']:
                        points+=1;updates+=m['is_update']=='True';sources[m['memory_source']]+=1;types[m['memory_type']]+=1
                    for q in s.get('questions',[]):questions+=1;qtypes[q['question_type']]+=1
        st=json.loads((path/'import.json').read_text());d=st['diagnostics'];assert (users,points,updates,questions)==(20,14948,3122,3467)
        assert (sessions,turns,generated)=={'medium':(1387,60146,0),'long':(2417,107032,1030)}[variant]
        assert d['users']==users and d['sessions']==sessions and st['events']==turns and d['memory_points']==points and d['updates']==updates and st['questions']==questions
        assert d['memory_sources']==sources and d['memory_types']==types and d['questions_by_type']==qtypes and st['time_parse_failures']==0
        assert st['counts']['cases']==sessions+questions and st['counts']['by_kind']=={'memory_audit':sessions,'qa':questions}
        assert st['upstream_scale']['declared']['dialogues']==turns//2
        val=run(['validate',str(path),'--expected-sha256',ref['suite_sha256']],variant+'-validate');assert val['valid'] and not val['leak_scan']['problems']
        header=json.loads((path/'suite.json').read_text());idx=(path/'episodes.index.jsonl').read_bytes();assert sha(idx)==header['container']['index_sha256'];root=sha(canonical(header)+b'\n'+idx);assert root==ref['suite_sha256'];assert header['license']['redistribute_normalized'] is False
        with (path/'episodes.jsonl').open('rb') as f:
            for n,line in enumerate(idx.splitlines()):
                x=json.loads(line);assert x['line']==n and x['offset']==f.tell();b=f.read(x['bytes']);assert sha(b)==x['sha256'] and f.read(1)==b'\n';ep=json.loads(b);assert ep['id']==x['id'];ends={}
                for i,e in enumerate(ep['events']):
                    assert e['attributes']=={} and e['timestamp'] and e['time_raw'];assert not set(e)&{'persona_info','memory_points','importance','memory_source','evidence'};ends[e['session']]=i+1
                audits=0
                for c in ep['cases']:
                    assert c['query_point']=='after_session_k' and c['cutoff']==ends[c['session_checkpoint']] and c['inject_current_date']
                    if c['kind']=='memory_audit':
                        audits+=1;assert 'memory_points' in c['gold'];assert len(c['gold']['memory_points']) or c['attributes']['is_generated_qa_session']
                assert audits==len(ends)
            assert not f.read()
        results.append(dict(variant=variant,root=root,users=users,sessions=sessions,turns=turns,dialogue_pairs=turns//2,memory_points=points,updates=updates,questions=questions,generated_qa_sessions=generated,memory_sources=sources,memory_types=types,question_types=qtypes,valid=True,leaks=0,merkle_equal=True))
    registry=run(['registry','check'],'registry-check');assert registry['ok']
    (out/'evidence.json').write_text(json.dumps(dict(commands=commands,bundles=results,registry=registry),ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(dict(bundles=2,users_each=20,valid=True,leaks=0,merkle_equal=True,stats_equal=True,registry_ok=True)))
if __name__=='__main__':
 p=argparse.ArgumentParser()
 verification_main(main,p)
