"""Scorer catalog and dispatch."""
from __future__ import annotations

import hashlib
import json
from typing import Any

from .builtin import BUILTIN, Verdict
from .judge import DEFAULT_RUBRIC, judge_version, llm_judge

JUDGE_META = {
    "label": "LLM judge", "version": "judge-v1",
    "description": "Model-graded rubric with structured verdict. Not ground truth; calibrate before gating.",
    "params": {"provider": "openai", "model": "gpt-4.1-nano", "rubric": DEFAULT_RUBRIC,
               "include_reference": True, "pass_threshold": 0.5, "temperature": 0},
    "kind": "judge",
}


AUTO_META = {
    "label": "Per-sample (auto)", "version": "auto-1.0", "kind": "deterministic",
    "description": "Dispatches each sample to the scorer named in its metadata (meta.scorer / meta.scorer_params). "
                   "For mixed-task suites such as router evaluation.",
    "params": {},
}


def catalog() -> list[dict[str, Any]]:
    out = [{"key": k, "label": v["label"], "version": v["version"], "description": v["description"],
            "params": v["params"], "kind": "deterministic"} for k, v in BUILTIN.items()]
    out.append({"key": "auto", **AUTO_META})
    out.append({"key": "llm_judge", **JUDGE_META})
    return out


def scorer_version(spec: dict[str, Any]) -> str:
    key = spec["key"]
    params = spec.get("params") or {}
    if key == "llm_judge":
        return judge_version(params)
    base = AUTO_META["version"] if key == "auto" else BUILTIN[key]["version"]
    if not params:
        return base
    h = hashlib.sha256(json.dumps(params, sort_keys=True).encode()).hexdigest()[:8]
    return f"{base}+{h}"


def validate_spec(spec: dict[str, Any]) -> list[str]:
    errs = []
    key = spec.get("key")
    if key not in ("llm_judge", "auto") and key not in BUILTIN:
        errs.append(f"unknown scorer '{key}'")
    if key == "json_schema" and not (spec.get("params") or {}).get("schema"):
        errs.append("json_schema scorer requires params.schema")
    if key == "regex":
        import re
        pat = (spec.get("params") or {}).get("pattern")
        if pat:
            try:
                re.compile(pat)
            except re.error as e:
                errs.append(f"invalid regex: {e}")
    return errs


def is_judge(spec: dict[str, Any]) -> bool:
    return spec.get("key") == "llm_judge"


async def run_scorer(ctx: Any, spec: dict[str, Any], task_text: str, output: str, expected: Any,
                     meta: dict[str, Any] | None = None) -> Verdict:
    params = spec.get("params") or {}
    key = spec["key"]
    if key == "llm_judge":
        return await llm_judge(ctx, task_text, output, expected, params)
    if key == "auto":
        meta = meta or {}
        key = meta.get("scorer")
        if key not in BUILTIN:
            return Verdict("not_applicable", None, {"reason": f"sample declares no known scorer ({key!r})"})
        params = {**(meta.get("scorer_params") or {}), **params}
    try:
        v = BUILTIN[key]["fn"](output or "", expected, params)
        if spec["key"] == "auto":
            v.evidence["dispatched_to"] = key
        return v
    except Exception as e:  # scorer bugs must not masquerade as model failures
        return Verdict("inconclusive", None, {"reason": "scorer_error", "detail": f"{type(e).__name__}: {e}"})
