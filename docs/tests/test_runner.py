"""Contract tests use deliberate synthetic fixtures; they are not benchmark data."""
import copy
import json
import threading
import pytest
from pa_memory_eval.adapters import BaselineAdapter, HTTPAdapter
from pa_memory_eval.core import ContractError, UnsupportedCapability, validate_suite
from pa_memory_eval.runner import run_suite
from pa_memory_eval.reporting import compare_runs


def toy():
    return {"schema_version":"1.0","suite_id":"software-unit-fixture","provenance":{"source_url":"unit-test://not-a-benchmark"},"episodes":[{
      "id":"private-episode-id","events":[
        {"source_id":"private-event-a","owner":"u","session":"session-label","role":"user","content":"blue box","attributes":{"has_answer":True,"gold":"SECRET-GOLD"}},
        {"source_id":"private-event-b","owner":"u","session":"session-label","role":"assistant","content":"future forbidden text","attributes":{}}],
      "cases":[{"id":"PRIVATE-QUESTION-ID","kind":"qa","category":"PRIVATE-CATEGORY","query":"box?","cutoff":1,"user":"u","scope":[],
                 "gold":{"answers":["SECRET-GOLD"],"evidence_ids":["private-event-a"],"exhaustive":False}}]}]}

class Spy(BaselineAdapter):
    def __init__(self):super().__init__(); self.inputs=[]
    def ingest(self,ns,events):self.inputs.extend(copy.deepcopy(events));super().ingest(ns,events)
    def retrieve(self,ns,req):self.request=copy.deepcopy(req);return super().retrieve(ns,req)


def test_labels_and_future_never_reach_adapter():
    s=Spy(); r=run_suite(toy(),s)
    wire=json.dumps([s.inputs,s.request])
    for forbidden in ("SECRET-GOLD","PRIVATE-QUESTION-ID","PRIVATE-CATEGORY","has_answer","future forbidden text","private-event-a","private-episode-id"):
        assert forbidden not in wire
    assert r["summary"]["metrics"]["recall"]["mean"]==1


def test_source_isolation_per_case():
    x=toy(); x["episodes"][0]["cases"].append({**x["episodes"][0]["cases"][0],"id":"second"})
    class Counts(Spy):
        def __init__(self):super().__init__();self.resets=[]
        def reset(self,n):self.resets.append(n);super().reset(n)
    a=Counts();r=run_suite(x,a)
    assert len(set(a.resets))==2 and len(r["cases"])==2


def test_future_evidence_label_rejected():
    x=toy();x["episodes"][0]["cases"][0]["gold"]["evidence_ids"]=["private-event-b"]
    with pytest.raises(ContractError):validate_suite(x)


def test_errors_not_discarded():
    class Error(Spy):
        def retrieve(self,*args):raise RuntimeError("secret-token")
    r=run_suite(toy(),Error())
    assert r["summary"]["planned"]==1 and r["summary"]["execution_errors"]==1
    assert "secret-token" not in json.dumps(r)


def test_unsupported_not_scored_as_zero():
    r=run_suite(toy(),BaselineAdapter(),mode="native_answer")
    assert r["cases"][0]["status"]=="unsupported" and not r["cases"][0]["metrics"]


def test_invalid_citation_not_granted_credit():
    class Bad(Spy):
        def retrieve(self,*args):return {"items":[{"text":"blah","source_ids":["fake-id"]}]}
    assert run_suite(toy(),Bad())["cases"][0]["status"]=="error"


def test_budget_violation_not_silently_clipped():
    class Bad(Spy):
        def retrieve(self,n,q):return {"items":[{"text":"x"*100,"source_ids":[self.inputs[0]["id"]]}]}
    assert run_suite(toy(),Bad(),budget={"max_context_chars":2})["cases"][0]["status"]=="error"


def test_no_citation_explicitly_unsupported():
    class Bad(Spy):
        def retrieve(self,n,q):return {"items":[{"text":"x","source_ids":[]}]}
    assert run_suite(toy(),Bad())["cases"][0]["status"]=="unsupported"


def test_ready_barrier_before_read():
    class Ready(Spy):
        def ready(self,n):self.ready_done=True
        def retrieve(self,n,q):assert self.ready_done;return super().retrieve(n,q)
    assert run_suite(toy(),Ready())["cases"][0]["status"]=="scored"


def test_native_choice_exact_parser():
    x=toy();c=x["episodes"][0]["cases"][0];c.update(kind="mcq",options={"A":"blue","B":"green"},gold={"choice":"A"})
    class Answer(Spy):
        def describe(self):return {**super().describe(),"capabilities":["answer"]}
        def answer(self,n,q):return {"choice":"A"}
    assert run_suite(x,Answer(),mode="native_answer")["cases"][0]["passed"]
    class Bad(Answer):
        def answer(self,n,q):return {"choice":"I think A or B"}
    assert run_suite(x,Bad(),mode="native_answer")["cases"][0]["status"]=="error"


def test_fixed_reader_sees_no_reference():
    class Reader:
        def describe(self):return {"name":"unit-test-reader"}
        def answer(self,req):
            assert "SECRET-GOLD" not in json.dumps(req)
            return {"text":"blue box"}
    r=run_suite(toy(),Spy(),mode="fixed_reader",reader=Reader())
    assert r["cases"][0]["official_score"] is None
    assert "answer_ms" in r["cases"][0]["timings"]


def test_compare_rejects_different_coverage():
    a=run_suite(toy(),Spy()); b=copy.deepcopy(a);b["cases"][0]["status"]="error"
    with pytest.raises(ContractError):compare_runs(a,b,metric="recall")


def test_single_cluster_no_population_ci():
    a=run_suite(toy(),Spy()); b=run_suite(toy(),BaselineAdapter("null"))
    comp=compare_runs(a,b,metric="recall")
    assert comp["ci95"] is None and comp["delta_right_minus_left"]==-1


def test_cors_or_adapter_acl_not_in_runner():
    x=toy();x["episodes"][0]["events"][0]["owner"]="other"
    class Leak(Spy):
        def retrieve(self,n,q):return {"items":[{"text":"blue box","source_ids":[self.inputs[0]["id"]]}]}
    r=run_suite(x,Leak())
    assert r["cases"][0]["diagnostics"]["cross_user_source_count"]==1


def test_remote_http_requires_consent():
    with pytest.raises(ValueError):HTTPAdapter("https://example.com/memory")
    with pytest.raises(ValueError):HTTPAdapter("http://example.com/memory",allow_data_egress=True)


def test_loopback_actual_http_protocol():
    from examples.bridge_server import make_server
    server=make_server(BaselineAdapter(),0)
    thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
    try:
        remote=HTTPAdapter(f"http://127.0.0.1:{server.server_port}/memory",allow_http_local=True)
        r=run_suite(toy(),remote)
        assert r["summary"]["metrics"]["recall"]["mean"]==1
    finally:server.shutdown();server.server_close();thread.join(timeout=2)
