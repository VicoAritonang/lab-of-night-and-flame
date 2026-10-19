"""Router lab, live: TypeSafe Jev Router vs fixed OpenRouter models on the router-mix suite."""
import json, sys, time, urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8000"


def call(method, path, body=None):
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.loads(r.read())


ds = next(d["id"] for d in call("GET", "/api/datasets") if d["name"] == "router-mix")
models = ["typesafe/jev-router", "openai/gpt-6-luna", "openai/gpt-6-sol", "anthropic/claude-haiku-5.5"]
plan = {
    "dataset_id": ds,
    "targets": [{"provider": "openrouter", "model": m, "label": m, "params": {"temperature": 0, "max_tokens": 2000}} for m in models],
    "scorers": [{"id": "auto", "key": "auto", "params": {}}], "primary_scorer": "auto",
    "protocol": {"repetitions": 2, "concurrency": 8, "order": "interleaved", "max_retries": 3, "timeout_s": 180, "splits": ["test"], "seed": 20261012, "stream": True},
    "budget": {"max_usd": 3.0, "paid_run_confirmed": True},
    "acceptance": {"min_pass_rate": 0.8, "baseline_arm": "openai/gpt-6-sol", "non_inferiority_margin": 0.05, "min_assessed_per_arm": 30, "max_error_rate": 0.1},
}
pf = call("POST", "/api/experiments/preflight", plan)
print("preflight", pf["ok"], pf["calls_estimate"], f"${pf['cost_low_usd']:.4f}-${pf['cost_high_usd']:.4f}", pf["unknown_price_arms"], pf["errors"])
r = call("POST", "/api/experiments?launch=true", {"name": "Router lab: Jev Router vs GPT-6 Luna / GPT-6 Sol / Claude Haiku 5.5",
          "hypothesis": "Jev Router matches the strong fixed model (GPT-6 Sol) within 5 points while costing less, by sending easy tasks to cheap models.", "plan": plan})
cid = r["campaign_id"]
print("campaign", cid, flush=True)
while True:
    c = call("GET", f"/api/campaigns/{cid}")
    done = sum(x["n"] for x in c["counts"] if x["status"] in ("completed", "error", "cancelled", "not_applicable"))
    print(f"  {c['status']:10s} {done}/{c['planned_trials']} spent ${c['spent_usd']:.4f}", flush=True)
    if not c["live"] and c["status"] not in ("queued", "running", "grading"):
        break
    time.sleep(10)
res = call("GET", f"/api/campaigns/{cid}/results")
for k in res["ranking"]:
    s = res["summaries"][k]; pr = s["primary"]["pass_rate"]
    print(f"{s['arm']['label']:32s} pass {pr['estimate']:.1%} [{pr['low']:.0%},{pr['high']:.0%}] n={s['primary']['assessed']} err={s['errors']} "
          f"p50={s['latency_ms']['p50'] and round(s['latency_ms']['p50'])}ms cost=${s['cost']['total_known_usd']:.4f}{'' if s['cost']['complete'] else '+?'} "
          f"per-pass=${(s['cost']['per_pass_usd'] or 0):.5f} gate={res['gate']['arms'][k]['verdict']}")
    if s.get("routing") and s["routing"]["is_router"]:
        rt = s["routing"]
        print("   routes:", [(x["model"], x["n"], round(x["share"], 2), x["pass_rate"]["estimate"], x["providers"]) for x in rt["routes"]], "stability", rt["stability"])
        for b in rt["by_slice"]:
            print("   by", b["key"], {row["value"]: {m: c["n"] for m, c in row["routes"].items()} for row in b["rows"]})
for cm in res["comparisons"]:
    d = cm["diff"]; print("cmp", res["summaries"][cm["candidate"]]["arm"]["label"], "vs baseline", d and round(d["estimate"], 3), d and (round(d["low"], 3), round(d["high"], 3)))
