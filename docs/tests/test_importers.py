"""Schema test fixtures are artificial and explicitly excluded from benchmark totals."""
import csv
import json
from pathlib import Path
import pytest
from pa_memory_eval.core import ContractError, validate_suite
from pa_memory_eval.importers import from_pmb, from_locomo, from_longmemeval, from_personamem
from pa_memory_eval.metrics import score_typed

ROOT=Path(__file__).resolve().parents[1]

def save(tmp,name,obj):
    p=tmp/name;p.write_text(json.dumps(obj));return p


def test_bundled_source_count_and_ids():
    suite=from_pmb(ROOT/"data/pmb_projection.json")
    assert validate_suite(suite)=={"episodes":1,"events":35,"cases":77}
    ep=suite["episodes"][0]; ids={e["source_id"] for e in ep["events"]}
    for c in ep["cases"]:
        for tier in ("pinnedFacts","relevantBeliefs","openQuestions"):
            for key in ("mustInclude","mustExclude","shouldInclude","shouldOnlyInclude"):
                assert set(c["gold"]["expect"].get(tier,{}).get(key,[]))<=ids


def test_exactly_one_vacuous_source_case():
    suite=from_pmb(ROOT/"data/pmb_projection.json")
    response={"tiers":{"pinnedFacts":[],"relevantBeliefs":[],"openQuestions":[]},"personaPrelude":""}
    vacuous=[c["id"] for c in suite["episodes"][0]["cases"] if score_typed(c["gold"]["expect"],response)["status"]=="unscored"]
    assert vacuous==["recency-tiebreak-reinforcement"]


def test_longmemeval_gold_flag_not_ingested(tmp_path):
    row={"question_id":"q1","question_type":"knowledge-update","question":"Now?","answer":"new",
         "question_date":"2025-01-01","haystack_session_ids":["s"],"haystack_dates":["2024-01-01"],
         "haystack_sessions":[[{"role":"user","content":"new","has_answer":True}]],"answer_session_ids":["s"]}
    suite=from_longmemeval(save(tmp_path,"data.json",[row]))
    assert "has_answer" not in json.dumps(suite["episodes"][0]["events"])
    assert suite["episodes"][0]["cases"][0]["gold"]["evidence_unit"]=="session"
    row["question_id"]="q1_abs"
    suite=from_longmemeval(save(tmp_path,"data.json",[row]))
    assert suite["episodes"][0]["cases"][0]["gold"]["evidence_ids"]==[]


def test_locomo_summary_excluded_and_order(tmp_path):
    row={"sample_id":"s","conversation":{"speaker_a":"A","speaker_b":"B",
         "session_2":[{"dia_id":"d2","speaker":"B","text":"two"}],
         "session_1":[{"dia_id":"d1","speaker":"A","text":"one"}]},
         "event_summary":"NEVER-INGEST-GOLD-SUMMARY","qa":[{"question":"what?","answer":"one","evidence":["d1"],"category":1}]}
    suite=from_locomo(save(tmp_path,"data.json",[row]))
    assert [e["content"] for e in suite["episodes"][0]["events"]]==["one","two"]
    assert "NEVER-INGEST" not in json.dumps(suite)
    row["qa"][0]["evidence"]=["bad"]
    with pytest.raises(ContractError):from_locomo(save(tmp_path,"data.json",[row]))

@pytest.mark.parametrize("options,correct",[("['first','second']","(a)"),("(a) first\n(b) second","(a)"),("['first','second']","first")])
def test_personamem_prefix_and_options(tmp_path,options,correct):
    ctx=tmp_path/"contexts.jsonl";ctx.write_text(json.dumps({"shared":[{"role":"user","content":"past"},{"role":"assistant","content":"FUTURE"}]})+"\n")
    q=tmp_path/"questions.csv"
    row={"persona_id":"p","question_id":"q","question_type":"pref","user_question_or_message":"what?","all_options":options,
         "correct_answer":correct,"shared_context_id":"shared","end_index_in_shared_context":1}
    with open(q,"w",newline="") as f:
        w=csv.DictWriter(f,fieldnames=list(row));w.writeheader();w.writerow(row)
    suite=from_personamem(q,ctx)
    assert suite["episodes"][0]["cases"][0]["cutoff"]==1
    assert suite["episodes"][0]["cases"][0]["gold"]["choice"]=="A"


def test_bad_cutoff_rejected(tmp_path):
    from tests.test_runner import toy
    x=toy();x["episodes"][0]["cases"][0]["cutoff"]=200
    with pytest.raises(ContractError):validate_suite(x)
