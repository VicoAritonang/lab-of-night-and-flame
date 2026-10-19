from arl.providers.base import redact
from arl.scorers.builtin import (
    bbox_iou, choice, contains, exact_match, extract_json, json_fields, json_schema, normalized_match, numeric,
    regex, similarity, token_f1,
)


def test_known_good_bad_empty_controls():
    for fn, good, exp in [
        (exact_match, "Paris", "paris"), (normalized_match, "The Paris.", "paris"), (numeric, "Answer: 42", 42),
        (choice, "B", "B"), (contains, "it is in Paris today", "paris"), (token_f1, "the quick brown fox", "quick brown fox"),
        (similarity, "colour", "color"),
    ]:
        assert fn(good, exp, {}).verdict == "pass", fn.__name__
        empty = fn("", exp, {})
        assert empty.verdict == "fail" and empty.evidence["reason"] == "empty_output", fn.__name__
    assert exact_match("London", "Paris", {}).verdict == "fail"
    assert numeric("41", 42, {"abs_tol": 0}).verdict == "fail"


def test_not_applicable_without_expected():
    assert exact_match("x", None, {}).verdict == "not_applicable"
    assert json_fields("{}", "not a dict", {}).verdict == "not_applicable"


def test_parse_error_is_distinct_from_wrong():
    v = numeric("I don't know", 5, {})
    assert v.verdict == "fail" and v.evidence["reason"] == "parse_error"
    assert json_fields("not json", {"a": 1}, {}).evidence["reason"] == "parse_error"


def test_numeric_extraction_prefers_answer_marker():
    assert numeric("Step 1: 3*4=12, then 12+5=17. Answer: 17", 17, {}).verdict == "pass"
    assert numeric("<think>maybe 99</think> The total is 1,250.", 1250, {}).verdict == "pass"
    assert numeric(r"\boxed{8} and 9 others", 8, {}).verdict == "pass"


def test_choice_patterns():
    assert choice("The answer is (C).", "C", {}).verdict == "pass"
    assert choice("**B**", "B", {}).verdict == "pass"
    assert choice("Answer: D because", "D", {}).verdict == "pass"
    assert choice("none", "A", {}).evidence["reason"] == "parse_error"


def test_extract_json_variants():
    assert extract_json('```json\n{"a": 1}\n```')[0] == {"a": 1}
    assert extract_json('Sure! Here: {"a": {"b": "}"}} done')[0] == {"a": {"b": "}"}}
    assert extract_json("nope")[1] is not None


def test_json_fields_partial_and_critical():
    exp = {"total": 100.5, "vendor": "Acme Corp", "date": "2026-01-02"}
    v = json_fields('{"total": "100.50", "vendor": "ACME corp.", "date": "2026-01-03"}', exp, {})
    assert v.verdict == "fail" and abs(v.score - 2 / 3) < 1e-9
    v = json_fields('{"total": 100.5, "vendor": "acme corp", "date": "2026-01-03"}', exp, {"critical_fields": ["total"]})
    assert v.verdict == "pass"


def test_json_schema():
    schema = {"type": "object", "required": ["a"], "properties": {"a": {"type": "number"}}}
    assert json_schema('{"a": 1}', None, {"schema": schema}).verdict == "pass"
    v = json_schema('{"a": "x"}', None, {"schema": schema})
    assert v.verdict == "fail" and v.evidence["reason"] == "schema_violation"


def test_regex_invalid_pattern_is_inconclusive():
    assert regex("abc", None, {"pattern": "("}).verdict == "inconclusive"


def test_bbox_iou():
    assert bbox_iou('{"bbox": [10, 10, 50, 50]}', {"bbox": [10, 10, 50, 50]}, {}).score == 1.0
    assert bbox_iou("[0,0,5,5]", [10, 10, 20, 20], {}).verdict == "fail"


def test_redaction():
    msg = "auth failed for key sk-or-v1-abcdef1234567890 and gsk_ABCDEFGH12345678 Bearer abc.def.ghi123456"
    out = redact(msg)
    assert "abcdef1234567890" not in out and "ABCDEFGH12345678" not in out and "abc.def.ghi123456" not in out
