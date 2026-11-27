#!/usr/bin/env python3
"""Offline Memora verifier. Safe from any cwd; never writes existing artifacts."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import argparse, collections, hashlib, json, pathlib, subprocess, time
ROOT=pathlib.Path(__file__).resolve().parents[1]
def digest(b):return hashlib.sha256(b).hexdigest()
def canonical(v):return json.dumps(v,ensure_ascii=False,sort_keys=True,separators=(',',':')).replace('\u2028','\\u2028').replace('\u2029','\\u2029').encode()
def main(args, work):
 a=args;out=work.out;empty=work.env_file;env=work.env;commands=[]
 def run(args,name):
  cmd=[str(work.binary()),'--env-file',str(out/'empty-config')]+args;t=time.monotonic();r=subprocess.run(cmd,cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE);(out/(name+'.stdout')).write_bytes(r.stdout);(out/(name+'.stderr')).write_bytes(r.stderr);commands.append({'command':cmd,'exit':r.returncode,'seconds':time.monotonic()-t});assert r.returncode==0,(name,r.stderr.decode());return json.loads(r.stdout)
 reg=json.loads((ROOT/'data/registry/memora.json').read_text());assert reg['implementation_status']=='imported';assert reg['variants']==['weekly','monthly','quarterly'];proof=[]
 # Independent input recount, not values copied from Go import stats.
 for ref in reg['suites']:
  path=ROOT/ref['path'];period=ref['protocol_ref'].split('#')[1];source=ROOT/'data/upstream/memora/data'/period;cats=collections.Counter();groups=collections.Counter();types=collections.Counter();ops=collections.Counter();sessions=turns=0;personas={}
  for persona in sorted(source.iterdir()):
   if not persona.is_dir():continue
   ss=list((persona/'conversations').glob('session_*.json'));sessions+=len(ss);n=0
   for f in ss:
    s=json.loads(f.read_text());types[s['session_type']]+=1;ops['null' if s['operation'] is None else s['operation']]+=1;n+=len(s['conversation'])
   turns+=n;q=json.loads((persona/f'evaluation_questions_{persona.name}.json').read_text());personas[persona.name]={'sessions':len(ss),'turns':n,'questions':sum(map(len,q['questions'].values()))}
   for cat,qs in q['questions'].items():
    cats[cat]+=len(qs)
    for c in qs:groups.update(e['evaluation_type'] for e in c['evaluation']['evaluation_questions'])
  stats=json.loads((path/'import.json').read_text());d=stats['diagnostics'];assert d['personas']==len(personas)==10;assert d['sessions']==sessions;assert stats['counts']['events']==turns;assert d['per_persona']==personas;assert d['session_types']==types and d['operations']==ops and d['questions_by_category']==cats and d['evaluation_types']==groups
  expected={'weekly':(1551,24451,150,479,256),'monthly':(6150,95944,150,534,781),'quarterly':(19913,311718,300,1934,2431)}[period];assert (sessions,turns,sum(cats.values()),groups['memory_presence'],groups['forgetting_absence'])==expected
  val=run(['validate',str(path),'--expected-sha256',ref['suite_sha256']],period+'-validate');assert val['valid'] and not val['leak_scan']['problems']
  header=json.loads((path/'suite.json').read_bytes());idx=(path/'episodes.index.jsonl').read_bytes();assert digest(idx)==header['container']['index_sha256'];root=digest(canonical(header)+b'\n'+idx);assert root==ref['suite_sha256'];rows=0;allitems=0
  with (path/'episodes.jsonl').open('rb') as f:
   for i,line in enumerate(idx.splitlines()):
    x=json.loads(line);assert x['line']==i and f.tell()==x['offset'];b=f.read(x['bytes']);assert digest(b)==x['sha256'] and f.read(1)==b'\n';ep=json.loads(b);rows+=1;assert ep['id']==x['id']
    for e in ep['events']:assert e['attributes']=={} and e['role'] in ['user','assistant'] and not set(e)&{'share_memory','operation','operation_details','session_type'}
    for c in ep['cases']:
     assert c['cutoff']==len(ep['events']) and c['inject_current_date'];r=c['gold']['rubric'];assert r['aggregate']=='fama';allitems+=len(r['items']);assert all(i['group'] in groups and i['weight']==1 and i['polarity'] in ['must_contain','must_not_contain'] for i in r['items'])
   assert not f.read()
  assert rows==10 and allitems==sum(groups.values())
  if a.reimport:
   dest=out/('reimport-'+period);imported=run(['import','memora','--input',str(ROOT/'data/upstream/memora/data'),'--period',period,'--out',str(dest)],period+'-reimport');assert imported[0]['suite_sha256']==root
   for name in ['suite.json','episodes.index.jsonl','episodes.jsonl','import.json']:
    with (path/name).open('rb') as l,(dest/name).open('rb') as r:
     while True:
      b=l.read(1048576);assert b==r.read(1048576),(period,name)
      if not b:break
  proof.append({'period':period,'root':root,'sessions':sessions,'turns':turns,'questions':sum(cats.values()),'subquestions':sum(groups.values()),'categories':cats,'groups':groups,'diagnostics':d,'reimport_bytes_equal':a.reimport})
 registry=run(['registry','check'],'registry-check');assert registry['ok'];(out/'evidence.json').write_text(json.dumps({'commands':commands,'bundles':proof,'registry':registry},ensure_ascii=False,indent=2)+'\n');print(json.dumps({'bundles':3,'personas_per_period':10,'episodes':30,'sessions':27614,'turns':432113,'questions':600,'subquestions':6415,'valid':True,'leaks':0,'merkle_equal':True,'reimport_equal':a.reimport,'registry_ok':True}))
if __name__=='__main__':
 p=argparse.ArgumentParser()
 p.add_argument('--reimport',action='store_true')
 verification_main(main,p)
