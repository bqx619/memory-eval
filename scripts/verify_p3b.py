#!/usr/bin/env python3
"""Offline BEAM acceptance. Safe from any cwd; outputs and protection evidence stay in scratch."""
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
  cmd=[str(work.binary()),'--env-file',str(out/'empty-config')]+args;t=time.monotonic();r=subprocess.run(cmd,cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
  (out/(name+'.stdout')).write_bytes(r.stdout);(out/(name+'.stderr')).write_bytes(r.stderr);commands.append({'command':cmd,'exit':r.returncode,'seconds':time.monotonic()-t});assert r.returncode==0,(name,r.stderr.decode());return json.loads(r.stdout)
 reg=json.loads((ROOT/'data/registry/beam.json').read_text());assert reg['implementation_status']=='imported' and reg['variants']==['100K','500K','1M'];evidence=[]
 for ref in reg['suites']:
  path=ROOT/ref['path'];v=ref['protocol_ref'].split('#')[1];expected={'100K':(20,400,5732,90),'500K':(35,700,38058,350),'1M':(35,700,74630,350)}[v]
  val=run(['validate',str(path),'--expected-sha256',ref['suite_sha256']],v+'-validate');assert val['valid'] and not val['leak_scan']['problems']
  h=json.loads((path/'suite.json').read_bytes());idx=(path/'episodes.index.jsonl').read_bytes();assert digest(idx)==h['container']['index_sha256'];root=digest(canonical(h)+b'\n'+idx);assert root==ref['suite_sha256']
  cats=collections.Counter();n,events,anchors,rubrics=0,0,0,0
  with (path/'episodes.jsonl').open('rb') as f:
   for i,line in enumerate(idx.splitlines()):
    x=json.loads(line);assert x['line']==i and f.tell()==x['offset'];raw=f.read(x['bytes']);assert digest(raw)==x['sha256'] and f.read(1)==b'\n';ep=json.loads(raw);assert ep['id']==x['id'];assert len(ep['events'])==x['events'] and len(ep['cases'])==x['cases'];n+=1;events+=len(ep['events'])
    anchors+=sum(bool(e.get('timestamp')) for e in ep['events']);ids={e['source_id'] for e in ep['events']};assert len(ids)==len(ep['events'])
    for e in ep['events']:assert not e['attributes'] and 'question_type' not in e and 'index' not in e
    for c in ep['cases']:
     cats[c['category']]+=1;rubrics+=len(c['gold']['rubric']['items']);assert c['kind']=='rubric';assert c['cutoff']==len(ep['events']);assert set(c['gold']['evidence_ids'])<=ids
     assert all(r['polarity']=='as_written' and r['weight']==1 for r in c['gold']['rubric']['items'])
     if c['category']=='event_ordering':assert c['gold']['sequence']==[r['text'] for r in c['gold']['rubric']['items']]
   assert f.read()==b''
  assert (n,sum(cats.values()),events,anchors)==expected;assert len(cats)==10 and set(cats.values())=={expected[1]//10}
  stats=json.loads((path/'import.json').read_text());assert stats['diagnostics']['probing_parse_failures']==0;assert stats['diagnostics']['rubric_items']==rubrics
  if a.reimport:
   target=out/('reimport-'+v);r=run(['import','beam','--input',str(ROOT/('data/upstream/beam/data/'+v+'-00000-of-00001.parquet')),'--variant',v,'--out',str(target)],v+'-reimport');assert r['suite_sha256']==root
   for name in ['suite.json','episodes.index.jsonl','episodes.jsonl','import.json']:
    # Stream compare to keep the 1M bundle bounded in memory.
    with (path/name).open('rb') as left,(target/name).open('rb') as right:
     while True:
      b=left.read(1048576);assert b==right.read(1048576),(v,name)
      if not b:break
  evidence.append({'variant':v,'root':root,'counts':{'conversations':n,'questions':sum(cats.values()),'turns':events,'anchors':anchors,'rubric_items':rubrics},'categories':dict(cats),'diagnostics':stats['diagnostics'],'reimport_bytes_equal':a.reimport})
 registry=run(['registry','check'],'registry-check');assert registry['ok'];(out/'evidence.json').write_text(json.dumps({'commands':commands,'bundles':evidence,'registry':registry},ensure_ascii=False,indent=2)+'\n');print(json.dumps({'bundles':3,'conversations':90,'questions':1800,'valid':True,'leaks':0,'merkle_equal':True,'reimport_equal':a.reimport,'registry_ok':True}))
if __name__=='__main__':
 p=argparse.ArgumentParser()
 p.add_argument('--reimport',action='store_true')
 verification_main(main,p)
