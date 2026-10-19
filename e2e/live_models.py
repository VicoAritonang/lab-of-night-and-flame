"""Live provider test: small real campaigns against OpenRouter (free), Groq and OpenAI.

Run against a running Lab:  python e2e/live_models.py http://127.0.0.1:8000
Budgets are capped per campaign; OpenRouter free tier allows ~50 requests/day.
"""
import json
import sys
import time
import urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8000"


def call(method, path, body=None):
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        raise SystemExit(f"{method} {path} -> {e.code}: {e.read().decode()[:800]}")


def ds(name):
    return next(d["id"] for d in call("GET", "/api/datasets") if d["name"] == name)


def run(name, plan, hypothesis=""):
    pf = call("POST", "/api/experiments/preflight", plan)
    print(f"\n=== {name}\n preflight ok={pf['ok']} trials={pf['trials']} cost=${pf['cost_low_usd']:.5f}-${pf['cost_high_usd']:.5f} warnings={pf['warnings']} errors={pf['errors']}")
    r = call("POST", "/api/experiments?launch=true", {"name": name, "hypothesis": hypothesis, "plan": plan})
    cid = r["campaign_id"]
    t0 = time.time()
    while True:
        c = call("GET", f"/api/campaigns/{cid}")
        if not c["live"] and c["status"] not in ("queued", "running", "grading"):
            break
        time.sleep(2)
    res = call("GET", f"/api/campaigns/{cid}/results")
    print(f" campaign {cid} -> {c['status']} in {time.time() - t0:.0f}s, spent ${c['spent_usd']:.5f}, unknown-cost attempts {c['unknown_cost_attempts']}")
    for k in res["ranking"] + [k for k in res["summaries"] if k not in res["ranking"]]:
        s = res["summaries"][k]
        pr = s["primary"]["pass_rate"]
        est = "—" if pr["estimate"] is None else f"{pr['estimate']:.0%} [{pr['low']:.0%},{pr['high']:.0%}]"
        print(f"  {s['arm']['label'][:46]:46s} pass {est:18s} n={s['primary']['assessed']:3d} err={s['errors']} {s['error_categories'] or ''} "
              f"p50={s['latency_ms']['p50'] and round(s['latency_ms']['p50'])}ms ttft={s['ttft_ms']['p50'] and round(s['ttft_ms']['p50'])}ms "
              f"cost=${s['cost']['total_known_usd']:.5f}{'' if s['cost']['complete'] else '+?'} retries={s['retries']}")
    for k, g in res["gate"]["arms"].items():
        print(f"  gate {res['summaries'][k]['arm']['label'][:40]:40s} {g['verdict']}")
    return cid


common = {"protocol": {"repetitions": 1, "concurrency": 4, "max_retries": 2, "timeout_s": 120, "splits": ["test"]},
          "acceptance": {"min_assessed_per_arm": 8, "min_pass_rate": 0.6, "non_inferiority_margin": 0.1}}

text_cid = run("LIVE · arithmetic · Groq vs OpenRouter-free vs OpenAI", {
    "dataset_id": ds("arithmetic-word-problems"),
    "targets": [
        {"provider": "openai", "model": "gpt-4.1-nano", "params": {"temperature": 0, "max_tokens": 400}},
        {"provider": "groq", "model": "openai/gpt-oss-20b", "params": {"temperature": 0, "max_tokens": 1200}},
        {"provider": "openrouter", "model": "google/gemma-4-26b-a4b-it:free", "params": {"temperature": 0, "max_tokens": 400}},
    ],
    "scorers": [{"id": "numeric", "key": "numeric", "params": {"abs_tol": 0.01}}], "primary_scorer": "numeric",
    "protocol": {**common["protocol"], "sample_limit": 12}, "budget": {"max_usd": 0.10, "paid_run_confirmed": True},
    "acceptance": common["acceptance"],
}, "Does a free open-weights model match a paid nano model on exact-answer arithmetic?")

vision_cid = run("LIVE · chart reading (vision) · OpenAI vs OpenRouter-free", {
    "dataset_id": ds("vision-chart-reading"),
    "targets": [
        {"provider": "openai", "model": "gpt-4.1-nano", "params": {"temperature": 0, "max_tokens": 60}},
        {"provider": "openai", "model": "gpt-4o-mini", "params": {"temperature": 0, "max_tokens": 60}},
        {"provider": "openrouter", "model": "google/gemma-4-31b-it:free", "params": {"temperature": 0, "max_tokens": 60}},
        {"provider": "groq", "model": "openai/gpt-oss-20b", "params": {"temperature": 0, "max_tokens": 60}},
    ],
    "scorers": [{"id": "numeric", "key": "numeric", "params": {"abs_tol": 0}}], "primary_scorer": "numeric",
    "protocol": {**common["protocol"], "sample_limit": 10}, "budget": {"max_usd": 0.10, "paid_run_confirmed": True},
    "acceptance": common["acceptance"],
}, "Which vision model reads bar charts reliably? (text-only gpt-oss must be reported not_applicable)")

judge_cid = run("LIVE · sentiment (EN+ID) · deterministic + LLM judge", {
    "dataset_id": ds("sentiment-multilingual"),
    "targets": [
        {"provider": "groq", "model": "openai/gpt-oss-20b", "params": {"temperature": 0, "max_tokens": 300}},
        {"provider": "openai", "model": "gpt-4.1-nano", "params": {"temperature": 0, "max_tokens": 20}},
    ],
    "scorers": [{"id": "match", "key": "normalized_match", "params": {}},
                {"id": "judge", "key": "llm_judge", "params": {"provider": "openai", "model": "gpt-4.1-mini", "include_reference": True,
                                                                 "rubric": "Pass if the response gives the same sentiment label as the reference (positive/negative/neutral), even if phrased differently.", "pass_threshold": 0.5, "temperature": 0}}],
    "primary_scorer": "match",
    "protocol": {**common["protocol"]}, "budget": {"max_usd": 0.10, "paid_run_confirmed": True},
    "acceptance": {**common["acceptance"], "min_pass_rate": 0.7},
}, "Agreement between exact label match and a model judge; multilingual slice.")
print("\ncampaigns:", text_cid, vision_cid, judge_cid)
