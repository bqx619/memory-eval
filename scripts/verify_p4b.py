#!/usr/bin/env python3
"""Independent locked ConvoMem source/normalized statistics, exact evidence and Merkle checks."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
from collections import Counter
from pathlib import Path
import ast
import hashlib
import json
import subprocess
import time
ROOT=Path(__file__).resolve().parents[1]
def canonical(v): return json.dumps(v,ensure_ascii=False,sort_keys=True,separators=(',',':')).replace('\u2028','\\u2028').replace('\u2029','\\u2029').encode()
def sha(b): return hashlib.sha256(b).hexdigest()
def main(args,work):
    commands=[]
    def cli(argv,name):
        cmd=work.cli(*argv);start=time.monotonic();r=subprocess.run(cmd,cwd=ROOT,env=work.env,capture_output=True)
        (work.out/(name+'.stdout')).write_bytes(r.stdout);(work.out/(name+'.stderr')).write_bytes(r.stderr)
        commands.append(dict(command=cmd,exit=r.returncode,seconds=time.monotonic()-start));assert r.returncode==0,(name,r.stderr.decode())
        return json.loads(r.stdout)
    source=ROOT/'data/upstream/convomem/core_benchmark/evidence_questions'
    registry=json.loads((ROOT/'data/registry/convomem.json').read_text());assert registry['implementation_status']=='imported' and len(registry['suites'])==5
    totals=Counter();all_convs=Counter();by_type_n={};proof=[]
    expected={'abstention':14910,'assistant_facts':12745,'changing':18323,'implicit_connection':7546,'preference':2749}
    for ref in registry['suites']:
        path=ROOT/ref['path'];header=json.loads((path/'suite.json').read_text());stats=json.loads((path/'import.json').read_text());d=stats['diagnostics'];typ=d['type']
        idx=(path/'episodes.index.jsonl').read_bytes();assert sha(idx)==header['container']['index_sha256'];root=sha(canonical(header)+b'\n'+idx);assert root==ref['suite_sha256'];assert header['license']['noncommercial']
        indexes=iter(idx.splitlines());n_items=n_events=n_sessions=n_exact=n_multi=n_unmatched=n_mismatch=0
        counts=Counter();messages=Counter();ns=Counter();speakers=Counter();categories=Counter();files=sorted((source/(typ+'_evidence')).glob('*_evidence/*.json'))
        with (path/'episodes.jsonl').open('rb') as stream:
            for file in files:
                count=int(file.parent.name.removesuffix('_evidence'));raw=json.loads(file.read_text())
                for i,row in enumerate(raw['evidence_items']):
                    ix=json.loads(next(indexes));assert ix['line']==n_items and ix['offset']==stream.tell()
                    b=stream.read(ix['bytes']);assert sha(b)==ix['sha256'] and stream.read(1)==b'\n';ep=json.loads(b)
                    eid=f"{typ}:{count}:{row['personId']}:{i}";assert ep['id']==ix['id']==eid and ep['cluster_id']==row['personId'];assert len(ep['cases'])==1
                    evs=[];matches={}
                    for j,conv in enumerate(row['conversations']):
                        assert conv['containsEvidence'] is True
                        for k,m in enumerate(conv['messages']):
                            assert m['speaker'] in ['User','Assistant'];sid=f'{eid}:c{j}:m{k}'
                            evs.append((sid,m['speaker'].lower(),m['text'],f'c{j}'));matches.setdefault((m['speaker'],m['text']),[]).append(sid);speakers[m['speaker']]+=1
                    assert [(e['source_id'],e['role'],e['content'],e['session']) for e in ep['events']]==evs
                    for e in ep['events']:assert e['attributes']=={} and not e.get('timestamp') and not e.get('time_raw')
                    c=ep['cases'][0];gold=c['gold'];a=c['attributes'];n=len(row['conversations'])
                    assert c['query']==row['question'] and c['category']==typ and c['cutoff']==len(evs) and c['cluster_id']==row['personId']
                    assert a['n_evidence']==n and a['file_n_evidence']==count and a['source_file']==file.relative_to(source).as_posix() and a['category']==row['category']
                    assert a['conversation_ids']=={f'c{j}':v['id'] for j,v in enumerate(row['conversations'])}
                    template='rubric' if typ in ['preference','implicit_connection'] else 'abstention' if typ=='abstention' else 'default'
                    assert gold['checker']=={'type':'convomem_judge','params':{'template':template}}
                    if typ=='abstention':assert gold['abstain'] and not gold.get('answers') and c['kind']=='qa'
                    elif typ=='preference':assert c['kind']=='rubric' and gold['rubric']=={'aggregate':'convomem_rubric','items':[{'text':row['answer'],'polarity':'as_written','weight':1}]}
                    else:assert c['kind']=='qa' and gold['answers']==[row['answer']]
                    evidence=row['message_evidences'];evidence=ast.literal_eval(evidence) if isinstance(evidence,str) else evidence
                    refs=[]
                    for v in evidence:
                        ids=matches.get((v['speaker'],v['text']),[])
                        if ids:n_exact+=1;n_multi+=len(ids)>1;refs.append(dict(kind='event',ref=ids[0]))
                        else:n_unmatched+=1;refs.append(dict(kind='text',ref=v['speaker']+': '+v['text']))
                    assert gold.get('evidence_refs',[])==refs and gold['evidence_ids'] is None
                    n_items+=1;n_events+=len(evs);n_sessions+=n;n_mismatch+=n!=count;counts[str(n)]+=1;messages[str(len(evs))]+=1;ns[str(count)]+=1;categories[row['category']]+=1
            assert not stream.read() and next(indexes,None) is None
        assert n_items==expected[typ]==stats['questions'] and n_events==stats['events'] and n_sessions==stats['sessions']
        for key,want in dict(files=len(files),items=n_items,conversation_counts=counts,message_counts=messages,items_by_n_evidence=ns,speakers=speakers,original_categories=categories,evidence_exact=n_exact,evidence_multiple=n_multi,evidence_unmatched=n_unmatched,evidence_total=n_exact+n_unmatched,contains_evidence_true=n_sessions,contains_evidence_false=0,conversation_count_directory_mismatches=n_mismatch,native_json_fields=n_items,python_literal_fields=0,speaker_errors=0,literal_parse_failures=0).items():assert d[key]==want,(typ,key,d[key],want)
        val=cli(['validate',str(path),'--expected-sha256',root],typ+'-validate');assert val['valid'] and not val['leak_scan']['problems'] and val['unresolved_evidence_refs']==n_unmatched
        totals.update(dict(files=len(files),items=n_items,messages=n_events,conversations=n_sessions,exact=n_exact,multiple=n_multi,unmatched=n_unmatched,directory_mismatches=n_mismatch));all_convs.update(counts);by_type_n[typ]=ns
        proof.append(dict(type=typ,suite_sha256=root,files=len(files),items=n_items,messages=n_events,conversations=n_sessions,evidence_exact=n_exact,evidence_multiple=n_multi,evidence_unmatched=n_unmatched,valid=True,leaks=0,merkle_equal=True))
    assert totals==dict(files=906,items=56273,messages=3801631,conversations=106989,exact=77261,multiple=0,unmatched=29780,directory_mismatches=106),totals
    check=cli(['registry','check'],'registry-check');assert check['ok']
    result=dict(totals=totals,by_type_n=by_type_n,conversation_counts=all_convs,bundles=proof,commands=commands,registry_ok=True)
    (work.out/'evidence.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(dict(totals=totals,bundles=5,valid=True,leaks=0,merkle_equal=True,registry_ok=True)))
if __name__=='__main__':verification_main(main)
