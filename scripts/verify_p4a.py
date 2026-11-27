#!/usr/bin/env python3
"""Independent PrefEval statistics, normalized history and Merkle checks; scratch-only outputs."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import ast
from collections import Counter
import hashlib
import json
from pathlib import Path
import subprocess
import time
ROOT = Path(__file__).resolve().parents[1]
def canonical(v):
    return json.dumps(v,ensure_ascii=False,sort_keys=True,separators=(',',':')).replace('\u2028','\\u2028').replace('\u2029','\\u2029').encode()
def sha(b): return hashlib.sha256(b).hexdigest()
def parsed(v): return ast.literal_eval(v) if isinstance(v,str) else v

def main(args,work):
    commands=[]
    def cli(cmd,name):
        command=work.cli(*cmd);start=time.monotonic()
        r=subprocess.run(command,cwd=ROOT,env=work.env,capture_output=True)
        (work.out/(name+'.stdout')).write_bytes(r.stdout);(work.out/(name+'.stderr')).write_bytes(r.stderr)
        commands.append(dict(command=command,exit=r.returncode,seconds=time.monotonic()-start))
        assert r.returncode==0,(name,r.stderr.decode())
        return json.loads(r.stdout)
    source=ROOT/'data/upstream/prefeval/benchmark_dataset'
    topics={p.stem:json.loads(p.read_text()) for p in sorted((source/'explicit_preference').glob('*.json'))}
    counts={k:len(v) for k,v in topics.items()};assert len(topics)==20 and sum(counts.values())==1000
    fillers=json.loads((source/'filtered_inter_turns.json').read_text());flat=[m for x in fillers for m in x['conversation']]
    assert len(fillers)==24 and len(flat)==632
    for n in [5,10,30,70,300]: assert flat[2*n-1]['role']=='assistant'
    dirs={'explicit':'explicit_preference','implicit-choice':'implicit_preference/choice-based','implicit-persona':'implicit_preference/persona-driven'}
    rows={form:{k:json.loads((source/d/(k+'.json')).read_text()) for k in topics} for form,d in dirs.items()}
    for form in rows: assert {k:len(v) for k,v in rows[form].items()}==counts
    mcq={k:json.loads((source/'mcq_options'/(k+'.json')).read_text()) for k in topics}
    orphan={p.name:len(json.loads(p.read_text())) for p in (source/'mcq_options').glob('*.json') if p.stem not in topics}
    differences=sum(a['preference']!=b['preference'] for k in topics for a,b in zip(topics[k],mcq[k]));assert differences==30 and orphan=={'travel_hotel copy.json':52}
    def history(form,row):
        if form=='explicit': return [('user',row['preference'])]
        conv=parsed(row['conversation'])
        if form=='implicit-choice':return [(r,conv[k]) for r,k in [('user','query'),('assistant','assistant_options'),('user','user_selection'),('assistant','assistant_acknowledgment')]]
        return [(r,conv[k][r]) for k in sorted(conv,key=int) for r in ['user','assistant']]
    forms={f:dict(tasks=sum(len(v) for v in rs.values()),topics=len(rs),preference_messages=sum(len(history(f,r)) for vs in rs.values() for r in vs)) for f,rs in rows.items()}
    assert forms['implicit-persona']['preference_messages']==10620
    registry=json.loads((ROOT/'data/registry/prefeval.json').read_text());assert registry['implementation_status']=='imported' and len(registry['suites'])==4 and len(registry['variants'])==4
    bundles=[]
    for ref in registry['suites']:
        path=ROOT/ref['path'];stats=json.loads((path/'import.json').read_text());d=stats['diagnostics'];form,task,n=d['form'],d['task'],d['inter_turns']
        assert d['tasks_by_topic']==counts and d['tasks']==1000 and d['topics']==20 and d['filler_messages_per_task']==2*n
        assert d['filler_conversations']==24 and d['filler_messages_available']==632 and d['literal_parse_failures']==0 and d['orphan_mcq_files']==orphan
        assert d['mcq_preference_differences_from_explicit']==(differences if task=='mcq' else 0)
        header=json.loads((path/'suite.json').read_text());idx=(path/'episodes.index.jsonl').read_bytes()
        assert sha(idx)==header['container']['index_sha256'];root=sha(canonical(header)+b'\n'+idx);assert root==ref['suite_sha256']
        assert header['license']['noncommercial'] and header['license']['id']=='CC-BY-NC-4.0'
        episodes=events=0;prefdist=Counter()
        with (path/'episodes.jsonl').open('rb') as stream:
            for j,line in enumerate(idx.splitlines()):
                ix=json.loads(line);assert ix['line']==j and ix['offset']==stream.tell()
                raw=stream.read(ix['bytes']);assert sha(raw)==ix['sha256'] and stream.read(1)==b'\n'
                ep=json.loads(raw);assert ep['id']==ix['id'];topic,i=ep['id'].split(':');i=int(i)
                source_row=mcq[topic][i] if task=='mcq' and form=='explicit' else rows[form][topic][i]
                pref=history(form,source_row);expected=pref+[(m['role'],m['content']) for m in flat[:2*n]]
                assert [(e['role'],e['content']) for e in ep['events']]==expected
                for e in ep['events']: assert e['attributes']=={} and not e.get('session') and not e.get('timestamp')
                assert ep['cluster_id']==topic and len(ep['cases'])==1
                case=ep['cases'][0];assert case['query']==source_row['question'] and case['cutoff']==len(expected) and case['category']==topic
                gold=case['gold']
                if task=='generation':
                    assert gold['reference']==source_row['preference'] and gold['rubric']['aggregate']=='prefeval'
                    assert [x['text'] for x in gold['rubric']['items']]==['acknow','violate','hallucinate','helpful']
                else:
                    spec=gold['mcq'];assert spec['seed']==41 and sorted(spec['order'])==[0,1,2,3] and spec['parse']=='prefeval_choice_tag'
                    options=parsed(mcq[topic][i]['classification_task_options'])
                    assert case['options']=={chr(65+k):options[v] for k,v in enumerate(spec['order'])}
                    assert gold['choice']==chr(65+spec['order'].index(0))
                episodes+=1;events+=len(expected);prefdist[str(len(pref))]+=1
            assert not stream.read()
        assert episodes==1000 and stats['events']==events and d['preference_messages_distribution']==prefdist
        val=cli(['validate',str(path),'--expected-sha256',root],ref['suite_id']+'-validate')
        assert val['valid'] and not val['leak_scan']['problems']
        bundles.append(dict(suite_id=ref['suite_id'],root=root,episodes=episodes,tasks=episodes,topics=20,events=events,filler_messages_per_task=n*2,valid=True,leaks=0,merkle_equal=True))
    checked=cli(['registry','check'],'registry-check');assert checked['ok']
    result=dict(forms=forms,by_topic=counts,filler_conversations=24,filler_messages=632,orphan_mcq_files=orphan,mcq_preference_differences_from_explicit=differences,bundles=bundles,registry_ok=True,commands=commands)
    (work.out/'evidence.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(dict(bundles=4,tasks_each=1000,topics=20,events=[x['events'] for x in bundles],valid=True,leaks=0,merkle_equal=True,stats_equal=True,registry_ok=True)))
if __name__=='__main__': verification_main(main)
