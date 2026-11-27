from __future__ import annotations
import argparse
import json
from pathlib import Path
import sys
from .adapters import create_adapter
from .collect import fetch_dataset, verify_projection
from .core import read_json, validate_suite, write_json
from .importers import from_pmb, from_locomo, from_longmemeval, from_personamem
from .reporting import save_run, report_html, compare_runs
from .runner import run_suite


def main(argv=None):
    p=argparse.ArgumentParser(description="Source-grounded PA memory evaluation; no paid model is used by default")
    sub=p.add_subparsers(dest="command",required=True)
    r=sub.add_parser("run",help="Run normalized suite or bundled PMB projection")
    r.add_argument("--suite",default="data/pmb_projection.json")
    r.add_argument("--adapter",default="bm25",help="bm25/recent/null/http or module:factory")
    r.add_argument("--adapter-config",help="JSON bridge or factory configuration; env var references, not credentials")
    r.add_argument("--mode",choices=["retrieval","native_answer","fixed_reader"],default="retrieval")
    r.add_argument("--reader",help="module:factory implementing describe() and answer(request)")
    r.add_argument("--reader-config")
    r.add_argument("--out",default="reports/run")
    r.add_argument("--top-k",type=int,default=10)
    r.add_argument("--max-context-chars",type=int,default=100000)
    r.add_argument("--batch-size",type=int,default=64)
    r.add_argument("--limit",type=int)
    r.add_argument("--trace-text",action="store_true",help="Persist original query and response; may contain sensitive material")
    i=sub.add_parser("import",help="Convert original local data without generating questions or gold")
    i.add_argument("--format",required=True,choices=["pmb","locomo","longmemeval","personamem-v1"])
    i.add_argument("--input",required=True); i.add_argument("--out",required=True)
    i.add_argument("--contexts-file"); i.add_argument("--cases-file"); i.add_argument("--revision")
    i.add_argument("--variant",choices=["s","m","oracle"],default="s")
    v=sub.add_parser("validate"); v.add_argument("suite")
    f=sub.add_parser("fetch",help="Networked machines: resolve and lock upstream raw dataset revision")
    f.add_argument("dataset"); f.add_argument("--out",default="data/upstream"); f.add_argument("--ack-license",action="store_true")
    f.add_argument("--timeout",type=float,default=60); f.add_argument("--max-bytes",type=int,default=2000000000); f.add_argument("--revision")
    verify=sub.add_parser("verify-projection"); verify.add_argument("--projection",default="data/pmb_projection.json")
    verify.add_argument("--seeds",required=True); verify.add_argument("--cases",required=True)
    h=sub.add_parser("report"); h.add_argument("inputs",nargs="+"); h.add_argument("--out",required=True)
    comp=sub.add_parser("compare"); comp.add_argument("left"); comp.add_argument("right"); comp.add_argument("--metric",default="pass"); comp.add_argument("--out")
    cat=sub.add_parser("catalog"); cat.add_argument("--file",default="data/source_registry.json")
    args=p.parse_args(argv)
    if args.command=="run":
        suite=read_json(args.suite)
        if "beliefs" in suite: suite=from_pmb(args.suite)
        config=read_json(args.adapter_config) if args.adapter_config else {}
        adapter=create_adapter(args.adapter,config)
        reader=create_adapter(args.reader,read_json(args.reader_config) if args.reader_config else {}) if args.reader else None
        result=run_suite(suite,adapter,mode=args.mode,reader=reader,budget={"top_k":args.top_k,"max_context_chars":args.max_context_chars},
                         batch_size=args.batch_size,limit=args.limit,trace_text=args.trace_text)
        save_run(result,args.out)
        print(json.dumps({"report":str(Path(args.out)/"report.html"),**result["summary"]},ensure_ascii=False,indent=2))
        return 2 if result["summary"]["execution_errors"] else 0
    if args.command=="import":
        if args.format=="pmb": suite=from_pmb(args.input,args.cases_file,args.revision)
        elif args.format=="locomo": suite=from_locomo(args.input,args.revision)
        elif args.format=="longmemeval": suite=from_longmemeval(args.input,args.variant,args.revision)
        else:
            if not args.contexts_file: p.error("personamem-v1 requires --contexts-file")
            suite=from_personamem(args.input,args.contexts_file,args.revision)
        write_json(args.out,suite); print(json.dumps(validate_suite(suite),ensure_ascii=False))
    elif args.command=="validate":
        suite=read_json(args.suite)
        if "beliefs" in suite: suite=from_pmb(args.suite)
        print(json.dumps(validate_suite(suite),ensure_ascii=False))
    elif args.command=="fetch":
        print(json.dumps(fetch_dataset(args.dataset,args.out,ack_license=args.ack_license,timeout=args.timeout,max_bytes=args.max_bytes,revision=args.revision),ensure_ascii=False,indent=2))
    elif args.command=="verify-projection":
        result=verify_projection(args.projection,args.seeds,args.cases)
        print(json.dumps(result,ensure_ascii=False,indent=2)); return 0 if result["matches"] else 2
    elif args.command=="report":
        Path(args.out).parent.mkdir(parents=True,exist_ok=True)
        Path(args.out).write_text(report_html([read_json(x) for x in args.inputs]),encoding="utf-8")
        print(args.out)
    elif args.command=="compare":
        result=compare_runs(read_json(args.left),read_json(args.right),metric=args.metric)
        if args.out: write_json(args.out,result)
        print(json.dumps(result,ensure_ascii=False,indent=2))
    elif args.command=="catalog":
        print(json.dumps(read_json(args.file),ensure_ascii=False,indent=2))
    return 0

if __name__=="__main__":
    try: sys.exit(main())
    except Exception as e:
        print(f"{type(e).__name__}: {e}",file=sys.stderr); sys.exit(2)
