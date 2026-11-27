"""Artificial software unit fixtures, never counted as public benchmark cases."""
import pytest
from pa_memory_eval.core import ContractError
from pa_memory_eval.metrics import retrieval_metrics, score_typed, lexical_answer_metrics, percentile

def response(p=(),r=(),q=(),text=""):
    return {"tiers":{"pinnedFacts":list(p),"relevantBeliefs":list(r),"openQuestions":list(q)},"personaPrelude":text}

def test_recall_not_hit():
    m=retrieval_metrics(["b","x"],["a","b","c"])
    assert m["recall"]==pytest.approx(1/3)
    assert m["hit"]==1 and m["all_evidence"]==0 and m["precision"] is None

def test_duplicate_ids_no_inflation():
    m=retrieval_metrics(["a","a","b"],["a","b"],True)
    assert m["recall"]==1 and m["precision"]==1

def test_mrr():
    assert retrieval_metrics(["z","a"],["a"])["mrr"]==.5

def test_empty_truth_unscored_recall():
    m=retrieval_metrics([],[],True)
    assert m["recall"] is None and m["precision"] is None and m["empty_result_correct"]==1

def test_negative_violation():
    m=retrieval_metrics(["z"],[],True)
    assert m["empty_result_correct"]==0 and m["precision"]==0

def test_no_effective_assertion_is_not_free_pass():
    s=score_typed({"pinnedFacts":{"mustInclude":[]}},response())
    assert s["status"]=="unscored" and s["passed"] is None

def test_explicit_empty_set_is_real_assertion():
    assert score_typed({"relevantBeliefs":{"shouldOnlyInclude":[]}},response())["passed"] is True

def test_must_include_uses_union_but_exact_only_uses_relevant():
    assert score_typed({"relevantBeliefs":{"mustInclude":["a"]}},response(p=["a"]))["passed"]
    assert not score_typed({"relevantBeliefs":{"shouldOnlyInclude":["a"]}},response(p=["a"]))["passed"]

def test_must_exclude_checks_pinned_too():
    assert not score_typed({"relevantBeliefs":{"mustExclude":["a"]}},response(p=["a"]))["passed"]

def test_source_substrings_case_sensitive():
    assert not score_typed({"personaPrelude":{"contains":["direct"]}},response(text="Direct"))["passed"]

def test_missing_tiers_is_error():
    with pytest.raises(ContractError): score_typed({}, {"items":[]})

def test_lexical_metrics_labeled_diagnostic():
    assert lexical_answer_metrics("The blue box.",["blue box"])["exact_match_diagnostic"]==1
    assert lexical_answer_metrics("blue",[])["token_f1_diagnostic"] is None

@pytest.mark.parametrize("values,q,expected",[([], .5, None),([1],.95,1),([0,10],.5,5),([0,10],.95,9.5)])
def test_percentile(values,q,expected):
    assert percentile(values,q)==expected
