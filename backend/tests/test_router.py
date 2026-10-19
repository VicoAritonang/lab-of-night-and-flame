"""Router lab: mixed-task suite, per-sample scorer dispatch, per-trial applicability, routing analytics."""
from conftest import dataset_id, wait_campaign

from arl.scorers.registry import run_scorer


async def test_auto_scorer_dispatches_on_sample_meta():
    v = await run_scorer(None, {"id": "a", "key": "auto", "params": {}}, "", "Answer: 42", 42,
                         {"scorer": "numeric", "scorer_params": {"abs_tol": 0}})
    assert v.verdict == "pass" and v.evidence["dispatched_to"] == "numeric"
    v = await run_scorer(None, {"id": "a", "key": "auto", "params": {}}, "", "B", "B", {})
    assert v.verdict == "not_applicable"


async def test_router_mix_campaign_reports_routes(client):
    ds = await dataset_id(client, "router-mix")
    detail = (await client.get(f"/api/datasets/{ds}")).json()
    assert detail["sample_count"] == 40 and detail["modality"] == "image"
    plan = {
        "dataset_id": ds,
        "targets": [{"provider": "fake", "model": "fake/router"}, {"provider": "fake", "model": "fake/oracle-70"}],
        "scorers": [{"id": "auto", "key": "auto"}], "primary_scorer": "auto",
        "protocol": {"repetitions": 2, "concurrency": 8},
        "budget": {"max_usd": 0}, "acceptance": {"min_assessed_per_arm": 10},
    }
    r = await client.post("/api/experiments?launch=true", json={"name": "router", "plan": plan})
    assert r.status_code == 200, r.text
    cid = r.json()["campaign_id"]
    await wait_campaign(client, cid)
    res = (await client.get(f"/api/campaigns/{cid}/results")).json()
    router = next(s for s in res["summaries"].values() if s["arm"]["model"] == "fake/router")
    oracle = next(s for s in res["summaries"].values() if s["arm"]["model"] == "fake/oracle-70")
    rt = router["routing"]
    assert rt["is_router"] and {r["model"] for r in rt["routes"]} == {"fake/large", "fake/small"}
    assert abs(sum(r["share"] for r in rt["routes"]) - 1) < 1e-9
    fam = next(b for b in rt["by_slice"] if b["key"] == "task_family")
    hard = next(row for row in fam["rows"] if row["value"] == "reasoning")
    assert hard["routes"].get("fake/large", {}).get("n", 0) > hard["routes"].get("fake/small", {}).get("n", 0)
    assert rt["stability"] is not None and 0 <= rt["stability"] <= 1
    assert oracle["routing"]["is_router"] is False
    assert router["primary"]["assessed"] == 80  # auto scorer applied to every sample


async def test_text_only_model_is_partially_applicable_on_mixed_suite(client):
    lab = client.app.state.lab
    lab.registry.specs["groq"].api_key = "gsk_test_not_used_000000"
    ds = await dataset_id(client, "router-mix")
    plan = {"dataset_id": ds, "targets": [{"provider": "groq", "model": "openai/gpt-oss-20b"}],
            "scorers": [{"id": "auto", "key": "auto"}], "primary_scorer": "auto"}
    pf = (await client.post("/api/experiments/preflight", json=plan)).json()
    arm = pf["arms"][0]
    assert arm["applicable"] and arm["partial"] and arm["trials"] == 34  # 6 image samples excluded


def test_suspect_labels_flags_unanimous_disagreement():
    from arl.engine.analysis import suspect_labels
    trials, assess = [], {}
    for arm in ("a", "b", "c"):
        for sid, out, verdict in (("s1", "negative", "fail"), ("s2", "positive", "pass"), ("s3", f"x-{arm}", "fail")):
            tid = f"{arm}{sid}"
            trials.append({"id": tid, "arm_key": arm, "sample_id": sid, "status": "completed", "output_head": out})
            assess[tid] = {"p": {"verdict": verdict}}
    flagged = suspect_labels(trials, assess, "p", {"s1": "k1", "s2": "k2", "s3": "k3"}, {"s1": "neutral"})
    assert [f["key"] for f in flagged] == ["k1"]  # s3 fails everywhere but arms disagree -> genuinely hard, not suspect
    assert flagged[0]["consensus_answer"] == "negative" and flagged[0]["expected"] == "neutral"
