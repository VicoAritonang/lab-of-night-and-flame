"""Analyzer: per-arm summaries, paired comparisons, slices, Pareto, and the qualification gate.

Denominators are always explicit (product.md §24):
  planned      trials admitted for the arm (excludes not_applicable)
  completed    trials that produced an output
  assessed     trials whose primary verdict is pass/fail (inconclusive excluded)
  pass_rate            = passes / assessed                (Wilson CI)
  all_admitted_rate    = passes / planned                 (errors & missing count as non-pass)
  task_balanced        = mean over samples of per-sample pass fraction (bootstrap over samples)
"""
from __future__ import annotations

import math
import re
from collections import defaultdict
from typing import Any

from ..context import Lab
from ..db import loads
from ..models import ExperimentPlan
from .. import stats
from .planner import expand_arms

MAX_SLICE_VALUES = 12
_DATE_SUFFIX = re.compile(r"-(?:\d{4}-\d{2}-\d{2}|\d{8}|\d{4})$")


def _model_base(m: str | None) -> str:
    if not m:
        return ""
    return _DATE_SUFFIX.sub("", m.split("/")[-1].removesuffix(":free").lower())


def suspect_labels(trials: list[dict[str, Any]], assess: dict[str, dict[str, dict[str, Any]]], primary: str,
                   sample_key: dict[str, str], expected: dict[str, Any], min_arms: int = 3) -> list[dict[str, Any]]:
    """Samples every arm fails, with the arms agreeing on the same answer.

    When independent models unanimously give one answer and the reference says another, the reference
    (or the task) is the first suspect. This is a review queue, not an automatic relabel.
    """
    from ..scorers.builtin import normalize, strip_reasoning

    by_sample: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for t in trials:
        v = assess.get(t["id"], {}).get(primary)
        if t["status"] == "completed" and v and v["verdict"] in ("pass", "fail"):
            by_sample[t["sample_id"]].append({"arm": t["arm_key"], "verdict": v["verdict"],
                                              "answer": normalize(strip_reasoning(t["output_head"] or ""))[:80]})
    out = []
    for sid, rows in by_sample.items():
        arms = {r["arm"] for r in rows}
        if len(arms) < min_arms or any(r["verdict"] == "pass" for r in rows):
            continue
        counts: dict[str, int] = defaultdict(int)
        for r in rows:
            counts[r["answer"]] += 1
        answer, n = max(counts.items(), key=lambda kv: kv[1])
        if answer and n / len(rows) >= 0.75:
            out.append({"sample_id": sid, "key": sample_key.get(sid, sid), "arms": len(arms), "trials": len(rows),
                        "consensus_answer": answer, "agreement": n / len(rows), "expected": expected.get(sid)})
    return sorted(out, key=lambda x: (-x["arms"], -x["agreement"]))


def routing_summary(arm: dict[str, Any], completed: list[dict[str, Any]], verdicts: dict[str, str | None],
                    sample_meta: dict[str, dict[str, Any]], slice_keys: list[str]) -> dict[str, Any] | None:
    """Which concrete model actually served each call, and how each route performed.

    For routers (e.g. typesafe/jev-router) this is the core evidence: route mix, quality per route,
    task → route matrix, and whether the same sample is routed consistently across repetitions.
    """
    resolved = [t for t in completed if t["resolved_model"]]
    if not resolved:
        return None
    bases = {_model_base(t["resolved_model"]) for t in resolved}
    name = arm["model"].lower()
    is_router = "router" in name or name.endswith("/auto") or len(bases) > 1 or _model_base(arm["model"]) not in bases
    routes: dict[str, dict[str, Any]] = {}
    for t in resolved:
        r = routes.setdefault(t["resolved_model"], {"model": t["resolved_model"], "n": 0, "passes": 0, "assessed": 0,
                                                    "cost": 0.0, "cost_known": True, "lat": [], "providers": set()})
        r["n"] += 1
        v = verdicts.get(t["id"])
        if v in ("pass", "fail"):
            r["assessed"] += 1
            r["passes"] += v == "pass"
        if t["cost_usd"] is None:
            r["cost_known"] = False
        else:
            r["cost"] += t["cost_usd"]
        if t["latency_ms"] is not None:
            r["lat"].append(t["latency_ms"])
        if t["upstream_provider"]:
            r["providers"].add(t["upstream_provider"])
    total = len(resolved)
    route_list = []
    for r in sorted(routes.values(), key=lambda x: -x["n"]):
        route_list.append({
            "model": r["model"], "n": r["n"], "share": r["n"] / total,
            "pass_rate": stats.wilson(r["passes"], r["assessed"]).as_dict(),
            "cost_usd": r["cost"] if r["cost_known"] else None,
            "cost_per_call_usd": r["cost"] / r["n"] if r["cost_known"] and r["n"] else None,
            "latency_p50_ms": stats.percentile(sorted(r["lat"]), 0.5), "providers": sorted(r["providers"]),
        })
    by_slice = []
    for key in slice_keys[:3]:
        cells: dict[str, dict[str, dict[str, int]]] = defaultdict(lambda: defaultdict(lambda: {"n": 0, "passes": 0, "assessed": 0}))
        for t in resolved:
            val = sample_meta.get(t["sample_id"], {}).get(key)
            if val is None or isinstance(val, (dict, list)):
                continue
            c = cells[str(val)][t["resolved_model"]]
            c["n"] += 1
            v = verdicts.get(t["id"])
            if v in ("pass", "fail"):
                c["assessed"] += 1
                c["passes"] += v == "pass"
        if len(cells) > 1:
            by_slice.append({"key": key, "rows": [{"value": val, "routes": {m: dict(c) for m, c in routes_.items()}}
                                                  for val, routes_ in sorted(cells.items())]})
    per_sample: dict[str, set[str]] = defaultdict(set)
    counts: dict[str, int] = defaultdict(int)
    for t in resolved:
        per_sample[t["sample_id"]].add(t["resolved_model"])
        counts[t["sample_id"]] += 1
    multi = [sid for sid, c in counts.items() if c > 1]
    stability = (sum(1 for sid in multi if len(per_sample[sid]) == 1) / len(multi)) if multi else None
    return {"is_router": bool(is_router), "routes": route_list, "distinct_models": len(routes), "by_slice": by_slice,
            "stability": stability, "stability_samples": len(multi), "calls": total}


async def latest_assessments(lab: Lab, campaign_id: str) -> dict[str, dict[str, dict[str, Any]]]:
    rows = await lab.db.fetchall(
        "SELECT a.trial_id, a.scorer_key, a.scorer_version, a.revision, a.score, a.passed, a.verdict, a.cost_usd "
        "FROM assessments a JOIN trials t ON t.id = a.trial_id WHERE t.campaign_id=?", (campaign_id,))
    out: dict[str, dict[str, dict[str, Any]]] = defaultdict(dict)
    for r in rows:
        cur = out[r["trial_id"]].get(r["scorer_key"])
        if cur is None or r["revision"] > cur["revision"]:
            out[r["trial_id"]][r["scorer_key"]] = r
    return out


def _arm_lookup(arms: list[dict[str, Any]], ref: str | None) -> str | None:
    if not arms:
        return None
    if ref:
        for a in arms:
            if ref in (a["key"], a["label"], f"{a['provider']}/{a['model']}"):
                return a["key"]
    return arms[0]["key"]


def _cache_key(camp: dict[str, Any], n_assess: int, n_done: int) -> str:
    return f"{camp['status']}|{n_assess}|{n_done}|{camp['scoring_json']}"


async def analyze(lab: Lab, campaign_id: str) -> dict[str, Any]:
    camp = await lab.db.fetchone("SELECT * FROM campaigns WHERE id=?", (campaign_id,))
    if camp is None:
        raise KeyError(campaign_id)
    n_assess = await lab.db.scalar(
        "SELECT COUNT(*) FROM assessments WHERE trial_id IN (SELECT id FROM trials WHERE campaign_id=?)", (campaign_id,))
    n_done = await lab.db.scalar(
        "SELECT COUNT(*) FROM trials WHERE campaign_id=? AND status IN ('completed','error','cancelled')", (campaign_id,))
    key = _cache_key(camp, n_assess, n_done)
    cached = lab.analysis_cache.get(campaign_id)
    if cached and cached[0] == key:
        return cached[1]
    result = await _analyze(lab, camp)
    lab.analysis_cache[campaign_id] = (key, result)
    return result


async def _analyze(lab: Lab, camp: dict[str, Any]) -> dict[str, Any]:
    plan = ExperimentPlan.model_validate(loads(camp["plan_json"]))
    scoring = loads(camp["scoring_json"]) or {"scorers": [s.model_dump() for s in plan.scorers],
                                              "primary": plan.primary_scorer, "revision": 1}
    primary = scoring["primary"]
    scorer_ids = [s["id"] for s in scoring["scorers"]]
    seed = plan.protocol.seed
    arms = [a.as_dict() for a in expand_arms(plan)]
    arm_by_key = {a["key"]: a for a in arms}
    dataset = await lab.db.fetchone("SELECT id, name, version, digest, card_json, modality, synthetic FROM datasets WHERE id=?",
                                    (plan.dataset_id,))
    card = loads(dataset["card_json"]) if dataset else {}

    trials = await lab.db.fetchall(
        "SELECT id, arm_key, sample_id, repetition, status, outcome, latency_ms, ttft_ms, tokens_in, tokens_out, cost_usd, "
        "attempt_count, error_category, resolved_model, upstream_provider, substr(output_text, 1, 300) output_head "
        "FROM trials WHERE campaign_id=?", (camp["id"],))
    assess = await latest_assessments(lab, camp["id"])
    samples = await lab.db.fetchall(
        "SELECT id, key, meta_json, split, expected_json FROM samples WHERE id IN (SELECT DISTINCT sample_id FROM trials WHERE campaign_id=?) "
        "ORDER BY ordinal", (camp["id"],))
    sample_meta = {s["id"]: loads(s["meta_json"]) or {} for s in samples}
    sample_key = {s["id"]: s["key"] for s in samples}
    attempts = await lab.db.fetchall(
        "SELECT t.arm_key, a.status, a.cost_usd, a.reserved_usd, a.error_category FROM attempts a "
        "JOIN trials t ON t.id=a.trial_id WHERE t.campaign_id=?", (camp["id"],))

    by_arm: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for t in trials:
        by_arm[t["arm_key"]].append(t)

    att_by_arm: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for a in attempts:
        att_by_arm[a["arm_key"]].append(a)

    reps = plan.protocol.repetitions
    summaries: dict[str, dict[str, Any]] = {}
    per_sample: dict[str, dict[str, list[int]]] = defaultdict(dict)  # arm -> sample -> [pass flags]
    for arm in arms:
        k = arm["key"]
        ts = by_arm.get(k, [])
        planned = [t for t in ts if t["status"] != "not_applicable"]
        completed = [t for t in planned if t["status"] == "completed"]
        errors = [t for t in planned if t["status"] == "error"]
        err_cats: dict[str, int] = defaultdict(int)
        for t in errors:
            err_cats[t["error_category"] or "unknown"] += 1

        scorer_stats = {}
        for sid in scorer_ids:
            verdicts = [assess.get(t["id"], {}).get(sid) for t in completed]
            got = [v for v in verdicts if v is not None]
            passes = sum(1 for v in got if v["verdict"] == "pass")
            fails = sum(1 for v in got if v["verdict"] == "fail")
            scores_by_sample: dict[str, list[float]] = defaultdict(list)
            for t, v in zip(completed, verdicts):
                if v is not None and v["score"] is not None:
                    scores_by_sample[t["sample_id"]].append(v["score"])
            sample_means = [sum(v) / len(v) for v in scores_by_sample.values()]
            scorer_stats[sid] = {
                "assessed": passes + fails, "passes": passes, "fails": fails,
                "inconclusive": sum(1 for v in got if v["verdict"] == "inconclusive"),
                "not_applicable": sum(1 for v in got if v["verdict"] == "not_applicable"),
                "missing": len(completed) - len(got),
                "pass_rate": stats.wilson(passes, passes + fails).as_dict(),
                "mean_score": stats.bootstrap_mean(sample_means, seed=seed).as_dict(),
                "versions": sorted({v["scorer_version"] for v in got}),
                "cost_usd": sum(v["cost_usd"] or 0 for v in got) if all(v["cost_usd"] is not None for v in got if v["verdict"] != "not_applicable") else None,
            }

        flags: dict[str, list[int]] = defaultdict(list)
        for t in completed:
            v = assess.get(t["id"], {}).get(primary)
            if v is not None and v["verdict"] in ("pass", "fail"):
                flags[t["sample_id"]].append(1 if v["verdict"] == "pass" else 0)
        per_sample[k] = flags
        prim = scorer_stats.get(primary, {})
        passes = prim.get("passes", 0)
        sample_fracs = [sum(f) / len(f) for f in flags.values() if f]
        pass_k = None
        if reps > 1:
            vals = [stats.pass_at_k(len(f), sum(f), reps) for f in flags.values() if len(f) == reps]
            allk = [1.0 if all(f) else 0.0 for f in flags.values() if len(f) == reps]
            pass_k = {"k": reps, "pass_at_k": stats.mean([v for v in vals if v is not None]),
                      "all_k": stats.mean(allk), "pass_at_1": stats.mean([stats.pass_at_k(len(f), sum(f), 1) for f in flags.values() if f]),
                      "samples_complete": len(allk)}

        lat = [t["latency_ms"] for t in completed if t["latency_ms"] is not None]
        ttft = [t["ttft_ms"] for t in completed if t["ttft_ms"] is not None]
        tps = [t["tokens_out"] / ((t["latency_ms"] - (t["ttft_ms"] or 0)) / 1000)
               for t in completed if t["tokens_out"] and t["latency_ms"] and t["latency_ms"] - (t["ttft_ms"] or 0) > 1]
        arm_att = att_by_arm.get(k, [])
        unknown_cost = sum(1 for a in arm_att if a["cost_usd"] is None and a["status"] in ("completed", "unknown") and a["reserved_usd"] > 0)
        known_cost = sum(a["cost_usd"] or 0 for a in arm_att)
        judge_cost = sum((scorer_stats[s]["cost_usd"] or 0) for s in scorer_ids)
        cost_complete = unknown_cost == 0 and all(scorer_stats[s]["cost_usd"] is not None for s in scorer_ids)
        total_cost = known_cost + judge_cost
        primary_verdicts = {t["id"]: (assess.get(t["id"], {}).get(primary) or {}).get("verdict") for t in completed}
        summaries[k] = {
            "routing": routing_summary(arm, completed, primary_verdicts, sample_meta,
                                       list(card.get("slices") or []) + [m for m in ("task_family", "difficulty") if m not in (card.get("slices") or [])]),
            "arm": arm,
            "planned": len(planned),
            "not_applicable": len(ts) - len(planned),
            "completed": len(completed),
            "errors": len(errors),
            "error_categories": dict(err_cats),
            "cancelled": sum(1 for t in planned if t["status"] == "cancelled"),
            "pending": sum(1 for t in planned if t["status"] in ("pending", "running", "interrupted")),
            "error_rate": len(errors) / len(planned) if planned else None,
            "attempts": len(arm_att),
            "retries": max(0, len(arm_att) - len(completed) - len(errors)),
            "scorers": scorer_stats,
            "primary": {
                "assessed": prim.get("assessed", 0), "passes": passes,
                "pass_rate": prim.get("pass_rate"),
                "all_admitted_rate": stats.wilson(passes, len(planned)).as_dict(),
                "task_balanced": stats.bootstrap_mean(sample_fracs, seed=seed).as_dict(),
                "pass_k": pass_k,
            },
            "latency_ms": stats.summarize(lat),
            "ttft_ms": stats.summarize(ttft),
            "latency_hist": stats.histogram(lat, 24),
            "latency_values": sorted(lat)[:: max(1, len(lat) // 400)],
            "output_tokens_per_s": stats.summarize(tps),
            "tokens_in_mean": stats.mean([t["tokens_in"] for t in completed if t["tokens_in"] is not None]),
            "tokens_out_mean": stats.mean([t["tokens_out"] for t in completed if t["tokens_out"] is not None]),
            "cost": {
                "target_known_usd": known_cost, "judge_known_usd": judge_cost, "total_known_usd": total_cost,
                "unknown_cost_attempts": unknown_cost, "complete": cost_complete,
                "per_trial_usd": total_cost / len(completed) if completed and cost_complete else None,
                "per_pass_usd": (total_cost / passes) if passes and cost_complete else None,
            },
        }

    baseline = _arm_lookup(arms, plan.acceptance.baseline_arm)
    comparisons = []
    for arm in arms:
        k = arm["key"]
        if k == baseline:
            continue
        a_flags, b_flags = per_sample.get(baseline, {}), per_sample.get(k, {})
        common = sorted(set(a_flags) & set(b_flags))
        a_vals = [sum(a_flags[s]) / len(a_flags[s]) for s in common]
        b_vals = [sum(b_flags[s]) / len(b_flags[s]) for s in common]
        wins = sum(1 for x, y in zip(a_vals, b_vals) if y > x)
        losses = sum(1 for x, y in zip(a_vals, b_vals) if y < x)
        diff = stats.paired_bootstrap_diff(a_vals, b_vals, seed=seed) if common else None
        p_mcnemar = None
        if reps == 1 and common:
            b_only = sum(1 for x, y in zip(a_vals, b_vals) if y == 1 and x == 0)
            a_only = sum(1 for x, y in zip(a_vals, b_vals) if y == 0 and x == 1)
            p_mcnemar = stats.mcnemar_exact(b_only, a_only)
        comparisons.append({
            "baseline": baseline, "candidate": k, "paired_samples": len(common),
            "missing_pairs": len(set(a_flags) ^ set(b_flags)),
            "wins": wins, "losses": losses, "ties": len(common) - wins - losses,
            "diff": diff.as_dict() if diff else None, "mcnemar_p": p_mcnemar,
            "test": "McNemar exact (two-sided)" if reps == 1 else "paired bootstrap over samples",
        })

    sample_ids = [s["id"] for s in samples]
    matrix = {
        "samples": [{"id": sid, "key": sample_key[sid], "meta": sample_meta.get(sid, {})} for sid in sample_ids],
        "arms": [a["key"] for a in arms],
        "values": [[(sum(per_sample[a["key"]][sid]) / len(per_sample[a["key"]][sid])) if per_sample[a["key"]].get(sid) else None
                    for a in arms] for sid in sample_ids],
    }

    slice_keys = list(card.get("slices") or [])
    meta_values: dict[str, set] = defaultdict(set)
    for m in sample_meta.values():
        for mk, mv in m.items():
            if isinstance(mv, (str, int, bool)) and not isinstance(mv, float):
                meta_values[mk].add(mv)
    for mk, vals in meta_values.items():
        if mk not in slice_keys and not mk.startswith("scorer") and 1 < len(vals) <= MAX_SLICE_VALUES:
            slice_keys.append(mk)
    slices = []
    for mk in slice_keys:
        values = sorted(meta_values.get(mk, set()), key=str)
        if len(values) < 2:
            continue
        rows = []
        for val in values:
            row = {"value": val, "arms": {}}
            for arm in arms:
                flags = per_sample[arm["key"]]
                p = n = 0
                for sid, f in flags.items():
                    if sample_meta.get(sid, {}).get(mk) == val:
                        p += sum(f)
                        n += len(f)
                row["arms"][arm["key"]] = stats.wilson(p, n).as_dict()
            rows.append(row)
        slices.append({"key": mk, "rows": rows, "exploratory": mk not in (card.get("slices") or [])})

    cost_pts = [(k, s["cost"]["per_trial_usd"], s["primary"]["pass_rate"]["estimate"]) for k, s in summaries.items()
                if s["cost"]["per_trial_usd"] is not None and s["primary"]["pass_rate"]["estimate"] is not None]
    lat_pts = [(k, s["latency_ms"]["p50"], s["primary"]["pass_rate"]["estimate"]) for k, s in summaries.items()
               if s["latency_ms"]["p50"] is not None and s["primary"]["pass_rate"]["estimate"] is not None]
    pareto = {"cost": stats.pareto_frontier(cost_pts), "latency": stats.pareto_frontier(lat_pts)}

    labels = set(loads(camp["labels_json"]) or [])
    if dataset and dataset["synthetic"]:
        labels.add("synthetic_data")
    if scoring.get("revision", 1) > 1:
        labels.add("re_scored")

    gate = evaluate_gate(plan, camp, summaries, comparisons, baseline, primary)
    ranking = sorted((k for k in summaries if summaries[k]["primary"]["pass_rate"]["estimate"] is not None),
                     key=lambda k: -summaries[k]["primary"]["pass_rate"]["estimate"])
    best = ranking[0] if ranking else None
    tied = []
    if best:
        blo = summaries[best]["primary"]["pass_rate"]["low"]
        tied = [k for k in ranking[1:] if (summaries[k]["primary"]["pass_rate"]["high"] or 0) >= (blo or 0)]

    return {
        "campaign_id": camp["id"], "status": camp["status"], "primary_scorer": primary,
        "scorers": scoring["scorers"], "scoring_revision": scoring.get("revision", 1),
        "dataset": {"id": plan.dataset_id, "name": dataset["name"] if dataset else None,
                    "version": dataset["version"] if dataset else None, "digest": dataset["digest"] if dataset else None,
                    "modality": dataset["modality"] if dataset else None},
        "labels": sorted(labels), "arms": arms, "baseline": baseline, "summaries": summaries,
        "comparisons": comparisons, "matrix": matrix, "slices": slices, "pareto": pareto, "gate": gate,
        "ranking": ranking, "best": best, "statistically_tied_with_best": tied,
        "suspect_labels": suspect_labels(trials, assess, primary, sample_key,
                                         {s["id"]: loads(s["expected_json"]) for s in samples}),
        "methods": {
            "pass_rate_ci": "Wilson score 95%", "mean_ci": f"percentile bootstrap B=2000 seed={seed}, resampling samples",
            "paired": "paired bootstrap over samples (reps averaged per sample); McNemar exact when repetitions=1",
            "pass_at_k": "unbiased estimator (Chen et al. 2021)",
        },
    }


def _ci_decision(low: float | None, high: float | None, threshold: float) -> str:
    if low is None or high is None:
        return "insufficient_evidence"
    if low >= threshold:
        return "pass"
    if high < threshold:
        return "fail"
    return "insufficient_evidence"


_SEVERITY = {"invalid_experiment": 3, "fail": 2, "insufficient_evidence": 1, "pass": 0}


def evaluate_gate(plan: ExperimentPlan, camp: dict[str, Any], summaries: dict[str, Any],
                  comparisons: list[dict[str, Any]], baseline: str | None, primary: str) -> dict[str, Any]:
    acc = plan.acceptance
    cmp_by = {c["candidate"]: c for c in comparisons}
    arms_out = {}
    for k, s in summaries.items():
        checks = []
        if s["planned"] == 0:
            checks.append({"check": "applicability", "result": "invalid_experiment", "detail": "no applicable trials for this arm"})
        prim = s["scorers"].get(primary, {})
        if s["completed"] and prim.get("not_applicable", 0) > s["completed"] / 2:
            checks.append({"check": "validity", "result": "invalid_experiment",
                           "detail": f"primary scorer not applicable to {prim['not_applicable']}/{s['completed']} outputs"})
        if prim and len(prim.get("versions", [])) > 1:
            checks.append({"check": "validity", "result": "invalid_experiment", "detail": "mixed primary scorer versions"})
        n = s["primary"]["assessed"]
        checks.append({"check": "evidence", "result": "pass" if n >= acc.min_assessed_per_arm else "insufficient_evidence",
                       "detail": f"{n} assessed (minimum {acc.min_assessed_per_arm})"})
        if s["pending"] or s["cancelled"]:
            checks.append({"check": "coverage", "result": "insufficient_evidence",
                           "detail": f"{s['pending']} pending, {s['cancelled']} cancelled of {s['planned']} planned"})
        if s["error_rate"] is not None:
            checks.append({"check": "reliability", "result": "pass" if s["error_rate"] <= acc.max_error_rate else "fail",
                           "detail": f"infrastructure error rate {s['error_rate']:.1%} (max {acc.max_error_rate:.0%})"})
        pr = s["primary"]["pass_rate"]
        if acc.min_pass_rate is not None:
            checks.append({"check": "utility", "result": _ci_decision(pr["low"], pr["high"], acc.min_pass_rate),
                           "detail": f"pass rate CI [{_fmt(pr['low'])}, {_fmt(pr['high'])}] vs threshold {acc.min_pass_rate:.0%}"})
        if acc.non_inferiority_margin is not None and k != baseline and k in cmp_by and cmp_by[k]["diff"]:
            d = cmp_by[k]["diff"]
            checks.append({"check": "non_inferiority", "result": _ci_decision(d["low"], d["high"], -acc.non_inferiority_margin),
                           "detail": f"Δ vs baseline CI [{_fmt(d['low'], True)}, {_fmt(d['high'], True)}], margin −{acc.non_inferiority_margin:.0%}"})
        if acc.max_p95_latency_ms is not None:
            p95 = s["latency_ms"]["p95"]
            checks.append({"check": "performance", "result": "insufficient_evidence" if p95 is None else ("pass" if p95 <= acc.max_p95_latency_ms else "fail"),
                           "detail": f"p95 latency {('%.0f ms' % p95) if p95 is not None else 'unknown'} (max {acc.max_p95_latency_ms:.0f} ms)"})
        if acc.max_cost_per_pass_usd is not None:
            cpp = s["cost"]["per_pass_usd"]
            checks.append({"check": "cost", "result": "insufficient_evidence" if cpp is None else ("pass" if cpp <= acc.max_cost_per_pass_usd else "fail"),
                           "detail": f"cost per verified pass {('$%.5f' % cpp) if cpp is not None else 'unknown/incomplete'}"})
        verdict = max((c["result"] for c in checks), key=lambda r: _SEVERITY[r]) if checks else "insufficient_evidence"
        arms_out[k] = {"verdict": verdict, "checks": checks, "is_baseline": k == baseline}
    execution_complete = camp["status"] == "completed"
    return {
        "arms": arms_out, "execution_status": camp["status"], "execution_complete": execution_complete,
        "note": None if execution_complete else "campaign not completed: gate results are provisional",
        "policy": acc.model_dump(),
    }


def _fmt(v: float | None, signed: bool = False) -> str:
    if v is None or (isinstance(v, float) and math.isnan(v)):
        return "—"
    return f"{v:+.1%}" if signed else f"{v:.1%}"
