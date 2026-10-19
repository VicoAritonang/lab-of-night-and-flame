"""End-to-end engine behaviour through the HTTP API, using only the fake/imported targets (zero network)."""
import asyncio
import io
import json
import time

from PIL import Image

from conftest import CANARY, dataset_id, wait_campaign


def plan(ds, targets, scorers=None, primary="s1", **proto):
    return {
        "dataset_id": ds,
        "targets": [{"provider": p, "model": m} for p, m in targets],
        "scorers": scorers or [{"id": "s1", "key": "choice"}],
        "primary_scorer": primary,
        "protocol": {"repetitions": 1, "concurrency": 8, **proto},
        "budget": {"max_usd": 1.0},
        "acceptance": {"min_assessed_per_arm": 10, "min_pass_rate": 0.5, "non_inferiority_margin": 0.05},
    }


async def launch(client, p, name="t"):
    r = await client.post("/api/experiments?launch=true", json={"name": name, "plan": p})
    assert r.status_code == 200, r.text
    return r.json()["campaign_id"]


async def test_fake_campaign_end_to_end(client):
    ds = await dataset_id(client, "general-knowledge-mcq")
    cid = await launch(client, plan(ds, [("fake", "fake/oracle-90"), ("fake", "fake/oracle-50")]))
    c = await wait_campaign(client, cid)
    assert c["status"] == "completed"
    assert "fake_runtime_test" in c["labels"]
    res = (await client.get(f"/api/campaigns/{cid}/results")).json()
    s = list(res["summaries"].values())
    assert all(x["planned"] == 24 and x["completed"] == 24 for x in s)
    assert s[0]["primary"]["pass_rate"]["estimate"] > s[1]["primary"]["pass_rate"]["estimate"]
    assert s[0]["cost"]["total_known_usd"] == 0 and s[0]["cost"]["complete"]
    assert len(res["comparisons"]) == 1 and res["comparisons"][0]["mcnemar_p"] is not None
    assert set(res["gate"]["arms"][res["baseline"]].keys()) >= {"verdict", "checks"}
    assert res["gate"]["execution_complete"]
    # paginated trials + detail never expose secrets
    trials = (await client.get(f"/api/campaigns/{cid}/trials?limit=5")).json()
    assert trials["total"] == 48 and len(trials["items"]) == 5
    detail = (await client.get(f"/api/trials/{trials['items'][0]['id']}")).json()
    assert detail["attempts"] and detail["assessments"]
    for fmt in ("html", "md", "json"):
        body = (await client.get(f"/api/campaigns/{cid}/report.{fmt}")).text
        assert CANARY not in body
        assert "FAKE" in body or "fake_runtime_test" in body
    rj = (await client.get(f"/api/campaigns/{cid}/report.json")).json()
    assert rj["checksum"]["algorithm"] == "sha256" and len(rj["trials"]) == 48


async def test_retries_are_recorded_as_attempts(client):
    ds = await dataset_id(client, "arithmetic-word-problems")
    p = plan(ds, [("fake", "fake/flaky-30")], scorers=[{"id": "s1", "key": "numeric"}], max_retries=3)
    cid = await launch(client, p)
    await wait_campaign(client, cid)
    res = (await client.get(f"/api/campaigns/{cid}/results")).json()
    s = next(iter(res["summaries"].values()))
    assert s["completed"] == 40 and s["errors"] == 0
    assert s["attempts"] > 40 and s["retries"] == s["attempts"] - 40
    db = client.app.state.lab.db
    statuses = {r["status"] for r in await db.fetchall("SELECT status FROM attempts")}
    assert "completed" in statuses and (statuses & {"failed", "unknown", "timeout"})


async def test_budget_cap_pauses_before_overspend(client):
    lab = client.app.state.lab
    lab.registry.price_per_token = lambda provider, model: (1e-3, 1e-3)  # make fake targets "paid"
    real_generate = lab.registry.fake.generate

    async def billed(req, cancel=None):
        res = await real_generate(req, cancel)
        res.cost_usd, res.cost_source = 0.5, "provider"
        return res

    lab.registry.fake.generate = billed
    ds = await dataset_id(client, "general-knowledge-mcq")
    p = plan(ds, [("fake", "fake/oracle-90")], concurrency=1)
    p["budget"] = {"max_usd": 2.0, "paid_run_confirmed": True}
    cid = await launch(client, p)
    c = await wait_campaign(client, cid)
    assert c["status"] == "paused" and c["stop_reason"] == "budget_exhausted"
    assert c["spent_usd"] + c["reserved_usd"] <= 2.0 + 1e-9
    done = await lab.db.scalar("SELECT COUNT(*) FROM trials WHERE campaign_id=? AND status='completed'", (cid,))
    pending = await lab.db.scalar("SELECT COUNT(*) FROM trials WHERE campaign_id=? AND status='pending'", (cid,))
    assert 0 < done < 24 and done + pending == 24
    # resume with a bigger cap finishes the remaining trials with new attempts
    r = await client.post(f"/api/campaigns/{cid}/resume", json={"budget_max_usd": 100})
    assert r.status_code == 200
    c = await wait_campaign(client, cid)
    assert c["status"] == "completed"


async def test_paid_run_requires_confirmation(client):
    ds = await dataset_id(client, "general-knowledge-mcq")
    p = plan(ds, [("openrouter", "openai/gpt-4o-mini")])
    lab = client.app.state.lab
    lab.registry.price_per_token = lambda provider, model: (1e-7, 1e-7)
    r = await client.post("/api/experiments?launch=true", json={"name": "x", "plan": p})
    assert r.status_code == 422 and "paid_run_not_confirmed" in r.text
    pf = (await client.post("/api/experiments/preflight", json=p)).json()
    assert pf["cost_high_usd"] > 0 and any(d["recipient"] == "openrouter" for d in pf["disclosure"])
    assert all("expected answers are never sent" in d["data"] for d in pf["disclosure"] if d["kind"] == "external")


async def test_cancel_stops_admission(client):
    ds = await dataset_id(client, "arithmetic-word-problems")
    p = plan(ds, [("fake", "fake/slow")], scorers=[{"id": "s1", "key": "numeric"}], concurrency=2)
    cid = await launch(client, p)
    await asyncio.sleep(0.6)
    r = await client.post(f"/api/campaigns/{cid}/cancel")
    assert r.status_code == 200
    t_cancel = time.time()
    c = await wait_campaign(client, cid)
    assert c["status"] == "cancelled"
    db = client.app.state.lab.db
    late = await db.scalar("SELECT COUNT(*) FROM attempts a JOIN trials t ON t.id=a.trial_id WHERE t.campaign_id=? AND a.started_at > ?",
                           (cid, t_cancel + 0.05))
    assert late == 0, "no new dispatch after the cancellation boundary"
    cancelled = await db.scalar("SELECT COUNT(*) FROM trials WHERE campaign_id=? AND status='cancelled'", (cid,))
    assert cancelled > 0
    res = (await client.get(f"/api/campaigns/{cid}/results")).json()
    assert res["gate"]["execution_complete"] is False


async def test_rescore_creates_revision_without_touching_outputs(client):
    ds = await dataset_id(client, "sentiment-multilingual")
    cid = await launch(client, plan(ds, [("fake", "fake/oracle-70")], scorers=[{"id": "s1", "key": "exact_match"}]))
    await wait_campaign(client, cid)
    db = client.app.state.lab.db
    before = await db.fetchall("SELECT id, output_text FROM trials WHERE campaign_id=? ORDER BY id", (cid,))
    r = await client.post(f"/api/campaigns/{cid}/rescore", json={
        "scorers": [{"id": "sim", "key": "similarity", "params": {"threshold": 0.5}}, {"id": "s1", "key": "normalized_match"}],
        "primary_scorer": "sim"})
    assert r.status_code == 200 and r.json()["revision"] == 2
    await wait_campaign(client, cid)
    for _ in range(100):
        c = (await client.get(f"/api/campaigns/{cid}")).json()
        if c["scoring"]["revision"] == 2:
            break
        await asyncio.sleep(0.05)
    after = await db.fetchall("SELECT id, output_text FROM trials WHERE campaign_id=? ORDER BY id", (cid,))
    assert before == after
    revs = {r["revision"] for r in await db.fetchall("SELECT revision FROM assessments")}
    assert revs == {1, 2}
    res = (await client.get(f"/api/campaigns/{cid}/results")).json()
    assert res["primary_scorer"] == "sim" and "re_scored" in res["labels"]


async def test_imported_predictions_and_dataset_import(client):
    buf = io.BytesIO()
    Image.new("RGB", (8, 6), "red").save(buf, format="PNG")
    records = "\n".join([
        json.dumps({"id": "a", "input": "What color?", "image": "red.png", "expected": "red", "lang": "en"}),
        json.dumps({"id": "b", "question": "2+2?", "answer": 4}),
        json.dumps({"id": "c"}),  # invalid: no input
    ])
    files = [("file", ("d.jsonl", records.encode(), "application/json")), ("images", ("red.png", buf.getvalue(), "image/png"))]
    r = await client.post("/api/datasets/import", data={"name": "mine"}, files=files)
    assert r.status_code == 422 and "invalid record" in r.text
    r = await client.post("/api/datasets/import", data={"name": "mine", "allow_partial": "true"}, files=files)
    assert r.status_code == 200, r.text
    ds = r.json()
    assert ds["sample_count"] == 2 and ds["modality"] == "image" and len(ds["rejected"]) == 1
    samples = (await client.get(f"/api/datasets/{ds['id']}/samples")).json()["items"]
    digest = samples[0]["input"]["images"][0]
    img = await client.get(f"/api/artifacts/{digest}")
    assert img.status_code == 200 and img.headers["content-type"] == "image/png"
    # idempotent re-import: identical content → same snapshot
    r2 = await client.post("/api/datasets/import", data={"name": "mine", "allow_partial": "true"}, files=files)
    assert r2.json()["id"] == ds["id"]

    preds = "\n".join([json.dumps({"id": "a", "output": "Red"}), json.dumps({"id": "b", "output": "5"})])
    r = await client.post(f"/api/datasets/{ds['id']}/predictions", data={"name": "legacy-v1"},
                          files=[("file", ("p.jsonl", preds.encode(), "application/json"))])
    assert r.json()["imported"] == 2
    p = plan(ds["id"], [("imported", "legacy-v1")], scorers=[{"id": "s1", "key": "normalized_match"}])
    cid = await launch(client, p)
    await wait_campaign(client, cid)
    res = (await client.get(f"/api/campaigns/{cid}/results")).json()
    s = next(iter(res["summaries"].values()))
    assert s["primary"]["passes"] == 1 and s["latency_ms"]["p50"] is None  # imported: no latency claimed
    assert "imported_outputs" in res["labels"]


async def test_crash_recovery_marks_unknown_and_resumes(client):
    lab = client.app.state.lab
    ds = await dataset_id(client, "general-knowledge-mcq")
    cid = await launch(client, plan(ds, [("fake", "fake/slow")], concurrency=2))
    await asyncio.sleep(0.5)
    runner = lab.runners[cid]
    runner.cancel_event.set()  # simulate abrupt death: stop the runner without the normal cancel bookkeeping
    for t in list(runner._inflight):
        t.cancel()
    await wait_campaign(client, cid)
    # rewind state to look like the process died mid-flight
    await lab.db.execute("UPDATE campaigns SET status='running' WHERE id=?", (cid,))
    tr = await lab.db.fetchone("SELECT id FROM trials WHERE campaign_id=? AND status='cancelled' LIMIT 1", (cid,))
    await lab.db.execute("UPDATE trials SET status='running' WHERE id=?", (tr["id"],))
    await lab.db.execute("INSERT INTO attempts(id, trial_id, n, status, provider, requested_model, reserved_usd, started_at) "
                         "VALUES ('att_x', ?, 9, 'running', 'fake', 'fake/slow', 0, ?)", (tr["id"], time.time()))
    assert await lab.recover() == 1
    c = (await client.get(f"/api/campaigns/{cid}")).json()
    assert c["status"] == "requires_reconciliation"
    assert await lab.db.scalar("SELECT status FROM attempts WHERE id='att_x'") == "unknown"
    await lab.db.execute("UPDATE trials SET status='pending' WHERE campaign_id=? AND status='cancelled'", (cid,))
    assert (await client.post(f"/api/campaigns/{cid}/resume")).status_code == 200
    c = await wait_campaign(client, cid)
    assert c["status"] == "completed"
    assert await lab.db.scalar("SELECT COUNT(*) FROM attempts WHERE id='att_x'") == 1  # old attempt kept for audit


async def test_vision_modality_applicability(client):
    lab = client.app.state.lab
    ds = await dataset_id(client, "vision-shape-counting")
    p = plan(ds, [("fake", "fake/oracle-90"), ("groq", "openai/gpt-oss-20b")], scorers=[{"id": "s1", "key": "numeric"}])
    lab.registry.specs["groq"].api_key = "gsk_test_not_used_000000"
    pf = (await client.post("/api/experiments/preflight", json=p)).json()
    assert any("not_applicable" in w for w in pf["warnings"])
    arm = next(a for a in pf["arms"] if a["provider"] == "groq")
    assert arm["applicable"] is False and arm["trials"] == 0


async def test_auth_required_when_token_set(tmp_path, monkeypatch):
    import httpx
    from arl.config import load_settings
    from arl.main import create_app
    monkeypatch.setenv("ARL_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("ARL_AUTH_TOKEN", "owner-secret-token")
    app = create_app(load_settings())
    async with app.router.lifespan_context(app):
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c:
            assert (await c.get("/api/health")).status_code == 200
            assert (await c.get("/api/datasets")).status_code == 401
            assert (await c.post("/api/auth/login", json={"token": "wrong"})).status_code == 401
            assert (await c.post("/api/auth/login", json={"token": "owner-secret-token"})).status_code == 200
            assert (await c.get("/api/datasets")).status_code == 200
            assert (await c.get("/api/datasets", headers={"Authorization": "Bearer nope"})).status_code == 401
