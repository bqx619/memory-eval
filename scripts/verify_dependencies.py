#!/usr/bin/env python3
"""Offline numerical acceptance against prior dependency replay artifacts.
--inputs is read-only; uses the cached NumPy oracle environment.
"""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
from upstream import require_upstream
import importlib.util
import json
import math
import shutil
from pathlib import Path
import subprocess

from parity_diff import check
from recompute_ci import calculate

ROOT = Path(__file__).resolve().parents[1]
OUT = None
WORK = None
SEED = '42' * 32

def invoke(*args):
    p = subprocess.run(WORK.cli(*args), cwd=ROOT,
                       env=WORK.env, capture_output=True, text=True)
    if p.returncode:
        raise RuntimeError(p.stderr)
    return json.loads(p.stdout)

def read(path):
    return json.loads(Path(path).read_text())

def main(args, work):
    oracle_path = require_upstream('longmemeval/src/retrieval/eval_utils.py', ROOT)
    global OUT, WORK
    OUT, WORK = work.out, work
    inputs = args.inputs.resolve()
    source = OUT/'pmb.json'
    invoke('import','pmb','--input','docs/data/pmb_projection.json','--format','single','--out',source)
    rows=[]
    for name in ('null','recent','bm25'):
        dest=OUT/f'm0-{name}'
        invoke('run','--suite',source,'--sut',name,'--mode','retrieval','--run-seed',SEED,'--out',dest)
        # M1b added a zero-valued provenance diagnostic absent in M0 Python.
        # Assert it separately; project only that additive schema field away.
        view=OUT/f'm0-{name}-python-view';view.mkdir(exist_ok=True)
        projected=read(dest/'report.json')
        for summary in [projected['summary'],*projected['by_category'].values()]:
            assert summary.pop('untraceable_hits')==0
        (view/'report.json').write_text(json.dumps(projected,ensure_ascii=False)+'\n')
        shutil.copyfile(dest/'cases.jsonl',view/'cases.jsonl')
        for file in view.iterdir():file.chmod(0o600)
        diff=check(view,ROOT/'docs/reports'/name)
        assert not diff, '\n'.join(diff)
        s=read(dest/'report.json')['summary']
        rows.append({'sut':name,'parity_diff':diff,'passed':s['passed'],'judged':s['n_with_pass_judgment'],'unscored':s['status_counts'].get('unscored',0)})
    dest=OUT/'m0-compare/delta.json'
    c=invoke('compare',OUT/'m0-null/report.json',OUT/'m0-bm25/report.json','--metric','pass','--out',dest)
    gold=read(ROOT/'docs/reports/paired-null-bm25.json')
    for key in ('paired_cases','clusters','excluded_cases','ci95','delta_right_minus_left'):
        assert c[key]==gold[key],key
    for name in ('null','bm25'):
        invoke('run','--suite','testdata/two-clusters.json','--sut',name,'--run-seed',SEED,'--out',OUT/f'ci-{name}')
    left,right=OUT/'ci-null/report.json',OUT/'ci-bm25/report.json'
    ci=invoke('compare',left,right,'--metric','recall','--resample-indices','testdata/resample_indices.jsonl','--out',OUT/'ci/delta.json')
    oracle_ci=calculate(left,right,ROOT/'testdata/resample_indices.jsonl','recall')
    assert all(math.isclose(a,b,rel_tol=0,abs_tol=1e-12) for a,b in zip(ci['ci95'],oracle_ci['ci95']))
    assert math.isclose(ci['delta_right_minus_left'],oracle_ci['delta_right_minus_left'],rel_tol=0,abs_tol=1e-12)
    # Check the newly replayed full runs directly against the untouched upstream.
    import numpy as np
    if not hasattr(np,'asfarray'): np.asfarray=lambda a:np.asarray(a,dtype=float)
    spec=importlib.util.spec_from_file_location('official',oracle_path)
    oracle=importlib.util.module_from_spec(spec);spec.loader.exec_module(oracle)
    suite=read(ROOT/'data/normalized/longmemeval-s.json')
    episodes={ep['id']:ep for ep in suite['episodes']}
    checks=0;max_diff=0.;summaries=[]
    for name in ('bm25','octop'):
        r=read(inputs/f'500-{name}-replay/report.json');values={}
        for case in r['cases']:
            ep=episodes[case['episode_id']];gold=ep['cases'][0]['gold']
            if gold.get('abstain'):continue
            corpus=[e['source_id'] for e in ep['events']];index={v:i for i,v in enumerate(corpus)}
            ranking=[index[v] for v in case['retrieved_source_ids']]
            sessions=[e['session']+'_'+str(i) for i,e in enumerate(ep['events'])]
            session_gold=[s+'_gold' for s in gold.get('evidence_session_ids',[])]
            want={}
            for k in (5,10,50):
                for metric,v in zip(('recall_any','recall_all','ndcg_any'),oracle.evaluate_retrieval(ranking,gold['evidence_ids'],corpus,k)):
                    want[f'turn.{metric}@{k}']=float(v)
            for k in (5,10):
                _,all_,ndcg=oracle.evaluate_retrieval_turn2session(ranking,session_gold,sessions,k)
                want[f'session.recall_all@{k}']=float(all_);want[f'session.ndcg_any@{k}']=float(ndcg)
            for metric,v in want.items():
                delta=abs(v-case['metrics']['longmemeval_official.'+metric]);max_diff=max(max_diff,delta);checks+=1
                assert delta<1e-12,(name,case['case_id'],metric)
                values.setdefault(metric,[]).append(v)
        for key,v in values.items():
            unit,metric=key.split('.')
            got=r['scores']['memory_only']['longmemeval_official'][unit][metric]
            assert got['n']==len(v)==470
            assert abs(got['mean']-sum(v)/len(v))<1e-12
        summaries.append({'sut':name,'judged':500,'memory_n':470,'qa_score':r['scores']['official']['score']})
    c=invoke('compare',inputs/'500-bm25-replay/report.json',inputs/'500-octop-replay/report.json','--metric','judge.correct','--out',OUT/'500-compare/delta.json')
    old=read(ROOT/'reports/live/compare-500-octop-vs-bm25.json')
    for key in ('paired_cases','clusters','ci95','delta_right_minus_left'):
        assert c[key]==old[key],(key,c[key],old[key])
    artifact={'m0':rows,'m0_compare_delta':read(dest)['delta_right_minus_left'],
              'fixed_indices_ci':oracle_ci,'new_live_oracle_checks':checks,'new_live_oracle_max_abs_diff':max_diff,
              'live_runs':summaries,'live_compare':{key:c[key] for key in ('paired_cases','clusters','ci95','delta_right_minus_left')}}
    path=OUT/'numerical-acceptance.json'
    path.write_text(json.dumps(artifact,ensure_ascii=False,indent=2)+'\n');path.chmod(0o600)
    print('M0 null/recent/bm25: empty parity diffs; compare and fixed-index CI unchanged.')
    print(f'New live replays: {checks} official oracle checks, max diff {max_diff:.3g}; 500-pair CI unchanged.')

if __name__=='__main__':
    import argparse
    parser=argparse.ArgumentParser()
    parser.add_argument('--inputs', type=Path, default=ROOT/'reports/dependency-verification', help='read-only prior 500-question replay artifacts')
    verification_main(main, parser)
