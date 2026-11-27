"""Historical, read-only oracle for pre-P5 fake-provider M2b artifacts.
Current Jev-only acceptance is scripts/verify_m3.py.
No model call: execute ONLY the vendored pure prompt function / nl loop AST.
"""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
from upstream import require_upstream
import ast
from collections import defaultdict
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
def load(path):
    return json.loads((root / path).read_text())
def main(args, work):
    judge_path = require_upstream('longmemeval/src/evaluation/evaluate_qa.py', root)
    reader_path = require_upstream('longmemeval/src/generation/run_generation.py', root)
    judge_tree = ast.parse(judge_path.read_text())
    fn = next(n for n in judge_tree.body if isinstance(n, ast.FunctionDef) and n.name == "get_anscheck_prompt")
    namespace = {}
    exec(compile(ast.Module(body=[fn], type_ignores=[]), "vendored-pure-judge", "exec"), namespace)
    judge_prompt = namespace["get_anscheck_prompt"]
    reader_tree = ast.parse(reader_path.read_text())
    fn = next(n for n in reader_tree.body if isinstance(n, ast.FunctionDef) and n.name == "prepare_prompt")
    start = next(i for i,n in enumerate(fn.body) if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id == "history_string" for t in n.targets))
    nl_loop = compile(ast.Module(body=fn.body[start:start+2], type_ignores=[]), "vendored-nl-history", "exec")
    templates = [n.value.value for n in ast.walk(fn) if isinstance(n,ast.Assign) and isinstance(n.value,ast.Constant) and any(isinstance(t,ast.Name) and t.id == "answer_prompt_template" for t in n.targets)]
    reader_template = next(s for s in templates if s.startswith("I will give you several history chats") and "facts" not in s and "step" not in s)
    references = load("data/upstream/longmemeval-s/longmemeval_s_cleaned.json")
    by_id = {q["question_id"]:q for q in references}
    report = load("reports/m2b-fake-record/report.json")
    by_type = defaultdict(list)
    for c in report["cases"]:
        q = by_id[c["case_id"]]
        selected = set(c["retrieved_source_ids"])
        groups = defaultdict(list)
        offsets = defaultdict(int)
        for sid, date, turns in zip(q["haystack_session_ids"], q["haystack_dates"], q["haystack_sessions"]):
            for i,turn in enumerate(turns):
                if f"{sid}:{offsets[sid]+i}" in selected:
                    groups[sid].append((offsets[sid]+i,date,turn))
            offsets[sid] += len(turns)
        chunks = []
        for sid, entries in groups.items():
            entries.sort(key=lambda x:x[0])
            chunks.append((min(x[1] for x in entries),sid,[x[2] for x in entries]))
        chunks.sort(key=lambda x:(x[0],x[1]))
        chunks = [(date,turns) for date,_,turns in chunks]
        while True:
            env = {"retrieved_chunks":chunks,"merge_key_expansion_into_value":"none","history_format":"nl","retriever_type":"orig-session"}
            exec(nl_loop,env)
            history=env["history_string"]
            if len(history)<=c["budget"]["max_context_chars"]:break
            chunks=chunks[1:]
        prompt=reader_template.format(history,q["question_date"],q["question"])
        hypothesis="fixture answer "+hashlib.sha256(prompt.encode()).hexdigest()[:12]
        assert hypothesis==c["qa"]["hypothesis"], (c["case_id"],"reader differs from official nl/template/date")
        prompt=judge_prompt(q["question_type"],q["question"],q["answer"],hypothesis,abstention="_abs" in q["question_id"])
        label=hashlib.sha256(prompt.encode()).digest()[0]%2==0
        assert label==c["qa"]["judge_label"], (c["case_id"],"judge prompt or label differs")
        by_type[q["question_type"]].append(int(label))
    correct=sum(map(sum,by_type.values()))
    assert report["scores"]["official"]["score"]==correct/50
    for kind,values in by_type.items():
        assert report["scores"]["official"]["by_type"][kind]=={"n":len(values),"correct":sum(values),"accuracy":sum(values)/len(values)}
    result={"status":"passed","fixture_only":True,"reader_prompts_checked":50,"judge_prompts_and_labels_checked":50,"upstream_ast_executed_without_imports_or_network":True,"official_aggregation_equal":True}
    (work.out/"oracle.json").write_text(json.dumps(result,indent=2)+"\n")
    print(json.dumps(result))

if __name__ == '__main__':
    verification_main(main)
