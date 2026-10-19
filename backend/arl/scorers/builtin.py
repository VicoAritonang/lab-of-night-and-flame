"""Deterministic scorers (FR-014).

Contract: ``fn(output, expected, params) -> Verdict``. Scorers never raise on bad model output;
parse problems are recorded as evidence so "correct but unparseable" stays distinguishable from
"wrong" (product.md §15.1).
"""
from __future__ import annotations

import json
import math
import re
import string
import unicodedata
from collections import Counter
from dataclasses import dataclass, field
from typing import Any, Callable

import jsonschema


@dataclass
class Verdict:
    verdict: str  # pass | fail | inconclusive | not_applicable
    score: float | None
    evidence: dict[str, Any] = field(default_factory=dict)
    cost_usd: float | None = None

    @property
    def passed(self) -> bool | None:
        if self.verdict == "pass":
            return True
        if self.verdict == "fail":
            return False
        return None


# ---------------------------------------------------------------- text helpers
_THINK_RE = re.compile(r"<think>.*?</think>", re.S | re.I)
_FENCE_RE = re.compile(r"```(?:json|JSON)?\s*(.*?)```", re.S)
_NUM_RE = re.compile(r"[-+]?(?:\d{1,3}(?:,\d{3})+|\d+)(?:\.\d+)?(?:[eE][-+]?\d+)?")
_ARTICLES = re.compile(r"\b(a|an|the)\b", re.I)


def strip_reasoning(text: str) -> str:
    return _THINK_RE.sub("", text or "").strip()


def normalize(text: Any) -> str:
    s = unicodedata.normalize("NFKC", str(text)).lower()
    s = "".join(ch for ch in s if ch not in string.punctuation)
    s = _ARTICLES.sub(" ", s)
    return " ".join(s.split())


def extract_json(text: str) -> tuple[Any, str | None]:
    """Return (value, error). Accepts raw JSON, fenced blocks, or the first balanced object/array."""
    t = strip_reasoning(text)
    candidates = [t]
    candidates += [m.strip() for m in _FENCE_RE.findall(t)]
    for opener, closer in (("{", "}"), ("[", "]")):
        start = t.find(opener)
        if start != -1:
            depth, in_str, esc = 0, False, False
            for i in range(start, len(t)):
                ch = t[i]
                if in_str:
                    if esc:
                        esc = False
                    elif ch == "\\":
                        esc = True
                    elif ch == '"':
                        in_str = False
                    continue
                if ch == '"':
                    in_str = True
                elif ch == opener:
                    depth += 1
                elif ch == closer:
                    depth -= 1
                    if depth == 0:
                        candidates.append(t[start:i + 1])
                        break
    for c in candidates:
        try:
            return json.loads(c), None
        except (json.JSONDecodeError, TypeError):
            continue
    return None, "no parseable JSON found"


def parse_number(text: Any) -> float | None:
    if isinstance(text, bool):
        return None
    if isinstance(text, (int, float)):
        return float(text)
    s = re.sub(r"[\s,$€£¥]|^(?:usd|idr|rp)", "", str(text).strip().lower())
    try:
        return float(s)
    except ValueError:
        return None


def last_number(text: str) -> float | None:
    """Prefer an explicit 'answer: N' / boxed value, else the last number in the text."""
    t = strip_reasoning(text)
    for pat in (r"\\boxed\{([^}]*)\}", r"(?i)(?:final answer|answer)\s*[:=]?\s*\**\s*([-+$]?[\d,]*\.?\d+)"):
        m = re.findall(pat, t)
        if m:
            n = _NUM_RE.findall(m[-1])
            if n:
                return float(n[-1].replace(",", ""))
    nums = _NUM_RE.findall(t)
    return float(nums[-1].replace(",", "")) if nums else None


def levenshtein(a: str, b: str) -> int:
    """Two-row DP; O(len(a)*len(b)) time, O(min) memory."""
    if len(a) < len(b):
        a, b = b, a
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i] + [0] * len(b)
        for j, cb in enumerate(b, 1):
            cur[j] = min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb))
        prev = cur
    return prev[-1]


def _empty(output: str) -> bool:
    return not strip_reasoning(output or "").strip()


def _needs_expected(expected: Any) -> Verdict | None:
    if expected is None:
        return Verdict("not_applicable", None, {"reason": "sample has no expected value"})
    return None


def _empty_verdict() -> Verdict:
    return Verdict("fail", 0.0, {"reason": "empty_output"})


# ---------------------------------------------------------------- scorers
def exact_match(output: str, expected: Any, params: dict) -> Verdict:
    if (na := _needs_expected(expected)) is not None:
        return na
    if _empty(output):
        return _empty_verdict()
    out = strip_reasoning(output)
    exp = expected if isinstance(expected, str) else json.dumps(expected)
    if params.get("strip", True):
        out, exp = out.strip(), exp.strip()
    if not params.get("case_sensitive", False):
        out, exp = out.lower(), exp.lower()
    ok = out == exp
    return Verdict("pass" if ok else "fail", float(ok), {"output": out[:200], "expected": exp[:200]})


def normalized_match(output: str, expected: Any, params: dict) -> Verdict:
    if (na := _needs_expected(expected)) is not None:
        return na
    if _empty(output):
        return _empty_verdict()
    accepted = expected if isinstance(expected, list) else [expected]
    out = normalize(strip_reasoning(output))
    norm = [normalize(e) for e in accepted]
    ok = out in norm
    return Verdict("pass" if ok else "fail", float(ok), {"normalized_output": out[:200], "accepted": norm[:10]})


def contains(output: str, expected: Any, params: dict) -> Verdict:
    needles = params.get("needles") or expected
    if needles is None:
        return Verdict("not_applicable", None, {"reason": "no needles"})
    if _empty(output):
        return _empty_verdict()
    needles = needles if isinstance(needles, list) else [needles]
    hay = normalize(strip_reasoning(output)) if params.get("normalize", True) else strip_reasoning(output)
    hits = [n for n in needles if (normalize(n) if params.get("normalize", True) else str(n)) in hay]
    mode = params.get("mode", "any")
    ok = bool(hits) if mode == "any" else len(hits) == len(needles)
    return Verdict("pass" if ok else "fail", len(hits) / len(needles), {"matched": hits, "mode": mode})


def regex(output: str, expected: Any, params: dict) -> Verdict:
    pattern = params.get("pattern") or (expected if isinstance(expected, str) else None)
    if not pattern:
        return Verdict("not_applicable", None, {"reason": "no pattern"})
    try:
        rx = re.compile(pattern, re.I if params.get("ignore_case", True) else 0)
    except re.error as e:
        return Verdict("inconclusive", None, {"reason": f"invalid regex: {e}"})
    m = rx.search(strip_reasoning(output or ""))
    return Verdict("pass" if m else "fail", float(bool(m)), {"match": m.group(0)[:200] if m else None})


def numeric(output: str, expected: Any, params: dict) -> Verdict:
    if (na := _needs_expected(expected)) is not None:
        return na
    exp = parse_number(expected)
    if exp is None:
        return Verdict("not_applicable", None, {"reason": "expected is not numeric"})
    if _empty(output):
        return _empty_verdict()
    got = last_number(output)
    if got is None:
        return Verdict("fail", 0.0, {"reason": "parse_error", "detail": "no number in output"})
    abs_tol = float(params.get("abs_tol", 1e-6))
    rel_tol = float(params.get("rel_tol", 0.0))
    ok = math.isclose(got, exp, abs_tol=abs_tol, rel_tol=rel_tol)
    return Verdict("pass" if ok else "fail", float(ok), {"parsed": got, "expected": exp, "abs_error": abs(got - exp)})


_CHOICE_PATTERNS = [
    r"(?i)answer\s*(?:is)?\s*[:\-]?\s*\(?\**([A-E])\b",
    r"^\s*\(?([A-E])[\).:\s]",
    r"^\s*\**([A-E])\**\s*$",
    r"\(([A-E])\)",
    r"\b([A-E])\b",
]


def choice(output: str, expected: Any, params: dict) -> Verdict:
    if (na := _needs_expected(expected)) is not None:
        return na
    if _empty(output):
        return _empty_verdict()
    t = strip_reasoning(output)
    got = None
    for pat in _CHOICE_PATTERNS:
        m = re.search(pat, t, re.M)
        if m:
            got = m.group(1).upper()
            break
    if got is None:
        return Verdict("fail", 0.0, {"reason": "parse_error", "detail": "no choice letter found"})
    ok = got == str(expected).strip().upper()
    return Verdict("pass" if ok else "fail", float(ok), {"parsed": got, "expected": str(expected).upper()})


def json_valid(output: str, expected: Any, params: dict) -> Verdict:
    if _empty(output):
        return _empty_verdict()
    value, err = extract_json(output)
    if err:
        return Verdict("fail", 0.0, {"reason": "parse_error", "detail": err})
    strict = params.get("strict", False)
    if strict and strip_reasoning(output).strip() != json.dumps(value) and _FENCE_RE.search(output):
        return Verdict("fail", 0.0, {"reason": "fenced_json_in_strict_mode"})
    return Verdict("pass", 1.0, {"type": type(value).__name__})


def json_schema(output: str, expected: Any, params: dict) -> Verdict:
    schema = params.get("schema")
    if not schema:
        return Verdict("not_applicable", None, {"reason": "no schema configured"})
    if _empty(output):
        return _empty_verdict()
    value, err = extract_json(output)
    if err:
        return Verdict("fail", 0.0, {"reason": "parse_error", "detail": err})
    try:
        validator = jsonschema.Draft202012Validator(schema)
    except jsonschema.SchemaError as e:
        return Verdict("inconclusive", None, {"reason": f"invalid schema: {e.message}"})
    errors = sorted(validator.iter_errors(value), key=lambda e: list(e.path))
    if errors:
        return Verdict("fail", 0.0, {"reason": "schema_violation",
                                     "errors": [f"{'/'.join(map(str, e.path)) or '$'}: {e.message}" for e in errors[:8]]})
    return Verdict("pass", 1.0, {})


def _field_equal(got: Any, exp: Any, num_tol: float) -> bool:
    if isinstance(exp, dict):
        return isinstance(got, dict) and all(_field_equal(got.get(k), v, num_tol) for k, v in exp.items())
    if isinstance(exp, list):
        return isinstance(got, list) and sorted(map(normalize, got)) == sorted(map(normalize, exp))
    if got is None or exp is None:
        return got is None and exp is None
    if not isinstance(exp, bool) and not isinstance(got, bool):
        ee, ge = parse_number(exp), parse_number(got)
        if ee is not None and ge is not None:
            return math.isclose(ge, ee, abs_tol=num_tol, rel_tol=0)
    return normalize(got) == normalize(exp)


def json_fields(output: str, expected: Any, params: dict) -> Verdict:
    """Per-field comparison against an expected object. Score = fraction of fields correct;
    pass requires all *critical* fields (default: all expected fields) to match."""
    if not isinstance(expected, dict):
        return Verdict("not_applicable", None, {"reason": "expected is not an object"})
    if _empty(output):
        return _empty_verdict()
    value, err = extract_json(output)
    if err or not isinstance(value, dict):
        return Verdict("fail", 0.0, {"reason": "parse_error", "detail": err or "JSON is not an object"})
    tol = float(params.get("numeric_tolerance", 0.01))
    critical = params.get("critical_fields") or list(expected)
    fields = {}
    for k, v in expected.items():
        got = value.get(k)
        fields[k] = {"ok": _field_equal(got, v, tol), "got": got, "expected": v}
    correct = sum(1 for f in fields.values() if f["ok"])
    crit_ok = all(fields[k]["ok"] for k in critical if k in fields)
    return Verdict("pass" if crit_ok else "fail", correct / len(fields) if fields else None,
                   {"fields": fields, "critical": critical, "extra_keys": sorted(set(value) - set(expected))})


def token_f1(output: str, expected: Any, params: dict) -> Verdict:
    if (na := _needs_expected(expected)) is not None:
        return na
    if _empty(output):
        return _empty_verdict()
    refs = expected if isinstance(expected, list) else [expected]
    pred = normalize(strip_reasoning(output)).split()
    best = 0.0
    for ref in refs:
        gold = normalize(ref).split()
        common = Counter(pred) & Counter(gold)
        same = sum(common.values())
        if same == 0:
            continue
        p, r = same / len(pred), same / len(gold)
        best = max(best, 2 * p * r / (p + r))
    thr = float(params.get("threshold", 0.5))
    return Verdict("pass" if best >= thr else "fail", best, {"f1": round(best, 4), "threshold": thr})


def similarity(output: str, expected: Any, params: dict) -> Verdict:
    if (na := _needs_expected(expected)) is not None:
        return na
    if _empty(output):
        return _empty_verdict()
    a, b = normalize(strip_reasoning(output)), normalize(expected)
    a, b = a[:2000], b[:2000]
    denom = max(len(a), len(b)) or 1
    ratio = 1 - levenshtein(a, b) / denom
    thr = float(params.get("threshold", 0.8))
    return Verdict("pass" if ratio >= thr else "fail", ratio, {"ratio": round(ratio, 4), "threshold": thr})


def _bbox(value: Any) -> list[float] | None:
    if isinstance(value, dict):
        for keys in (("x1", "y1", "x2", "y2"), ("xmin", "ymin", "xmax", "ymax")):
            if all(k in value for k in keys):
                return [float(value[k]) for k in keys]
        for k in ("bbox", "box"):
            if k in value:
                return _bbox(value[k])
    if isinstance(value, list) and len(value) == 4 and all(isinstance(v, (int, float)) for v in value):
        return [float(v) for v in value]
    return None


def bbox_iou(output: str, expected: Any, params: dict) -> Verdict:
    """IoU for [x1,y1,x2,y2] boxes in the image's original pixel coordinates."""
    exp = _bbox(expected)
    if exp is None:
        return Verdict("not_applicable", None, {"reason": "expected has no bbox"})
    if _empty(output):
        return _empty_verdict()
    value, err = extract_json(output)
    got = _bbox(value) if not err else None
    if got is None:
        nums = [float(n.replace(",", "")) for n in _NUM_RE.findall(strip_reasoning(output))]
        got = nums[:4] if len(nums) >= 4 else None
    if got is None:
        return Verdict("fail", 0.0, {"reason": "parse_error", "detail": "no bbox found"})
    ix1, iy1 = max(got[0], exp[0]), max(got[1], exp[1])
    ix2, iy2 = min(got[2], exp[2]), min(got[3], exp[3])
    inter = max(0.0, ix2 - ix1) * max(0.0, iy2 - iy1)
    area = lambda b: max(0.0, b[2] - b[0]) * max(0.0, b[3] - b[1])  # noqa: E731
    union = area(got) + area(exp) - inter
    iou = inter / union if union > 0 else 0.0
    thr = float(params.get("threshold", 0.5))
    return Verdict("pass" if iou >= thr else "fail", iou, {"iou": round(iou, 4), "predicted": got, "expected": exp,
                                                            "coordinate_convention": "pixels, original image, [x1,y1,x2,y2]"})


ScorerFn = Callable[[str, Any, dict], Verdict]

BUILTIN: dict[str, dict[str, Any]] = {
    "exact_match": {"fn": exact_match, "version": "1.0", "label": "Exact match",
                    "description": "Output equals expected after trimming (case-insensitive by default).",
                    "params": {"case_sensitive": False}},
    "normalized_match": {"fn": normalized_match, "version": "1.0", "label": "Normalized match",
                         "description": "SQuAD-style normalization (case, punctuation, articles, whitespace). Expected may be a list of accepted answers.",
                         "params": {}},
    "contains": {"fn": contains, "version": "1.0", "label": "Contains",
                 "description": "Output contains expected substring(s).", "params": {"mode": "any"}},
    "regex": {"fn": regex, "version": "1.0", "label": "Regex",
              "description": "Output matches a regular expression (param 'pattern' or expected).", "params": {"pattern": ""}},
    "numeric": {"fn": numeric, "version": "1.0", "label": "Numeric tolerance",
                "description": "Extracts final number and compares with abs/rel tolerance.", "params": {"abs_tol": 0.01, "rel_tol": 0}},
    "choice": {"fn": choice, "version": "1.0", "label": "Multiple choice",
               "description": "Extracts an A–E answer letter.", "params": {}},
    "json_valid": {"fn": json_valid, "version": "1.0", "label": "JSON valid",
                   "description": "Output contains parseable JSON.", "params": {}},
    "json_schema": {"fn": json_schema, "version": "1.0", "label": "JSON schema",
                    "description": "Output JSON validates against a JSON Schema (draft 2020-12).", "params": {"schema": {}}},
    "json_fields": {"fn": json_fields, "version": "1.0", "label": "JSON field match",
                    "description": "Per-field match vs expected object; pass requires all critical fields.",
                    "params": {"numeric_tolerance": 0.01, "critical_fields": []}},
    "token_f1": {"fn": token_f1, "version": "1.0", "label": "Token F1",
                 "description": "SQuAD token-overlap F1; pass at threshold.", "params": {"threshold": 0.5}},
    "similarity": {"fn": similarity, "version": "1.0", "label": "Edit similarity",
                   "description": "1 − normalized Levenshtein distance; pass at threshold.", "params": {"threshold": 0.8}},
    "bbox_iou": {"fn": bbox_iou, "version": "1.0", "label": "BBox IoU",
                 "description": "Grounding IoU against expected [x1,y1,x2,y2] in original pixels.", "params": {"threshold": 0.5}},
}
