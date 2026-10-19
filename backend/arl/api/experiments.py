"""Experiments, campaigns, trials, results, reports."""
from __future__ import annotations

import asyncio
import time
from typing import Any

import orjson
from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import HTMLResponse, ORJSONResponse, PlainTextResponse, StreamingResponse

from ..context import Lab
from ..db import dumps, loads, new_id
from ..engine.analysis import analyze, latest_assessments
from ..engine.planner import canonical_digest, expand_arms, preflight
from ..engine.runner import CampaignRunner, insert_assessment
from ..models import ExperimentCreate, ExperimentPlan, RescoreRequest, ResumeRequest
from ..reports import render_html, render_json, render_markdown
from ..scorers.registry import is_judge, run_scorer, validate_spec
from .deps import lab, not_found, page

router = APIRouter(prefix="/api")

STATUS_CODE = {"pending": 0, "running": 1, "completed": 2, "error": 3, "cancelled": 4, "not_applicable": 5, "interrupted": 6}
OUTCOME_CODE = {"not_assessed": 0, "pass": 1, "fail": 2, "inconclusive": 3, "not_applicable": 4}


# ------------------------------------------------------------------ experiments
@router.post("/experiments/preflight")
async def experiment_preflight(request: Request, plan: ExperimentPlan):
    lab_ = lab(request)
    return (await preflight(lab_.db, lab_.registry, plan)).as_dict()


async def _start_campaign(lab_: Lab, exp_id: str, plan: ExperimentPlan, labels: list[str], parent: str | None = None) -> str:
    arms = expand_arms(plan)
    from ..engine.planner import load_samples
    n_samples = len(await load_samples(lab_.db, plan))
    camp_id = new_id("cmp")
    plan_json = plan.model_dump()
    await lab_.db.execute(
        "INSERT INTO campaigns(id, experiment_id, mode, status, plan_json, plan_digest, seed, budget_cap_usd, planned_trials, "
        "scoring_json, labels_json, parent_campaign_id, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (camp_id, exp_id, "fresh", "queued", dumps(plan_json), canonical_digest(plan_json), plan.protocol.seed,
         plan.budget.max_usd, len(arms) * n_samples * plan.protocol.repetitions,
         dumps({"scorers": [s.model_dump() for s in plan.scorers], "primary": plan.primary_scorer, "revision": 1}),
         dumps(labels), parent, time.time()))
    await lab_.db.audit("campaign.create", camp_id, {"experiment": exp_id, "cap_usd": plan.budget.max_usd, "parent": parent})
    runner = CampaignRunner(lab_, camp_id)
    lab_.runners[camp_id] = runner
    lab_.spawn(runner.run())
    return camp_id


def _check_launchable(pf: dict[str, Any], plan: ExperimentPlan) -> None:
    if not pf["ok"]:
        raise HTTPException(422, {"message": "plan failed validation", "errors": pf["errors"]})
    paid = (pf["cost_high_usd"] or 0) > 0 or bool(pf["unknown_price_arms"])
    if paid and not plan.budget.paid_run_confirmed:
        raise HTTPException(422, {"message": "paid run requires explicit confirmation (budget.paid_run_confirmed=true)",
                                  "errors": ["paid_run_not_confirmed"]})


@router.post("/experiments")
async def create_experiment(request: Request, body: ExperimentCreate, launch: bool = False):
    lab_ = lab(request)
    pf = (await preflight(lab_.db, lab_.registry, body.plan)).as_dict()
    if launch:
        _check_launchable(pf, body.plan)
    exp_id = new_id("exp")
    plan_json = body.plan.model_dump()
    now = time.time()
    await lab_.db.execute(
        "INSERT INTO experiments(id, name, hypothesis, status, plan_json, plan_digest, created_at, frozen_at) VALUES (?,?,?,?,?,?,?,?)",
        (exp_id, body.name, body.hypothesis, "frozen" if launch else "draft", dumps(plan_json),
         canonical_digest(plan_json), now, now if launch else None))
    await lab_.db.audit("experiment.create", exp_id, {"frozen": launch, "plan_digest": canonical_digest(plan_json)})
    camp_id = await _start_campaign(lab_, exp_id, body.plan, pf["labels"]) if launch else None
    return {"id": exp_id, "campaign_id": camp_id, "preflight": pf}


@router.get("/experiments")
async def list_experiments(request: Request):
    rows = await lab(request).db.fetchall(
        "SELECT e.id, e.name, e.hypothesis, e.status, e.plan_digest, e.created_at, e.frozen_at, "
        "json_extract(e.plan_json,'$.dataset_id') dataset_id, "
        "(SELECT COUNT(*) FROM campaigns c WHERE c.experiment_id=e.id) campaigns, "
        "(SELECT c.id FROM campaigns c WHERE c.experiment_id=e.id ORDER BY c.created_at DESC LIMIT 1) last_campaign "
        "FROM experiments e ORDER BY e.created_at DESC")
    return rows


@router.get("/experiments/{exp_id}")
async def get_experiment(request: Request, exp_id: str):
    db = lab(request).db
    e = await db.fetchone("SELECT * FROM experiments WHERE id=?", (exp_id,))
    if not e:
        raise not_found("experiment")
    e["plan"] = loads(e.pop("plan_json"))
    e["campaigns"] = await db.fetchall(
        "SELECT id, status, created_at, finished_at, spent_usd, planned_trials, stop_reason FROM campaigns "
        "WHERE experiment_id=? ORDER BY created_at DESC", (exp_id,))
    return e


@router.post("/experiments/{exp_id}/launch")
async def launch_experiment(request: Request, exp_id: str):
    """Freeze (if draft) and launch a fresh campaign. Re-launching = fresh rerun on the same frozen plan."""
    lab_ = lab(request)
    e = await lab_.db.fetchone("SELECT * FROM experiments WHERE id=?", (exp_id,))
    if not e:
        raise not_found("experiment")
    plan = ExperimentPlan.model_validate(loads(e["plan_json"]))
    pf = (await preflight(lab_.db, lab_.registry, plan)).as_dict()
    _check_launchable(pf, plan)
    if e["status"] == "draft":
        await lab_.db.execute("UPDATE experiments SET status='frozen', frozen_at=? WHERE id=?", (time.time(), exp_id))
        await lab_.db.audit("experiment.freeze", exp_id, {"plan_digest": e["plan_digest"]})
    camp_id = await _start_campaign(lab_, exp_id, plan, pf["labels"])
    return {"campaign_id": camp_id}


# ------------------------------------------------------------------ campaigns
def _campaign_out(lab_: Lab, c: dict[str, Any]) -> dict[str, Any]:
    plan = loads(c.pop("plan_json"))
    c["plan"] = plan
    c["scoring"] = loads(c.pop("scoring_json"))
    c["labels"] = loads(c.pop("labels_json"))
    c["arms"] = [a.as_dict() for a in expand_arms(ExperimentPlan.model_validate(plan))]
    c["live"] = c["id"] in lab_.runners
    return c


@router.get("/campaigns")
async def list_campaigns(request: Request, limit: int = 50):
    lab_ = lab(request)
    rows = await lab_.db.fetchall(
        "SELECT c.id, c.experiment_id, c.status, c.created_at, c.started_at, c.finished_at, c.spent_usd, c.reserved_usd, "
        "c.unknown_cost_attempts, c.planned_trials, c.stop_reason, c.labels_json, c.budget_cap_usd, e.name, "
        "json_extract(c.plan_json,'$.dataset_id') dataset_id, json_extract(c.plan_json,'$.targets') targets_json, "
        "(SELECT COUNT(*) FROM trials t WHERE t.campaign_id=c.id AND t.status IN ('completed','error','cancelled','not_applicable')) done, "
        "(SELECT COUNT(*) FROM trials t WHERE t.campaign_id=c.id AND t.outcome='pass') passes, "
        "(SELECT COUNT(*) FROM trials t WHERE t.campaign_id=c.id AND t.outcome IN ('pass','fail')) assessed "
        "FROM campaigns c JOIN experiments e ON e.id=c.experiment_id ORDER BY c.created_at DESC LIMIT ?", (min(limit, 500),))
    for r in rows:
        r["labels"] = loads(r.pop("labels_json"))
        r["targets"] = [f"{t['provider']}/{t['model']}" for t in (loads(r.pop("targets_json")) or [])]
        r["live"] = r["id"] in lab_.runners
    return rows


@router.get("/campaigns/{cid}")
async def get_campaign(request: Request, cid: str):
    lab_ = lab(request)
    c = await lab_.db.fetchone(
        "SELECT c.*, e.name, e.hypothesis FROM campaigns c JOIN experiments e ON e.id=c.experiment_id WHERE c.id=?", (cid,))
    if not c:
        raise not_found("campaign")
    out = _campaign_out(lab_, c)
    counts = await lab_.db.fetchall("SELECT status, outcome, COUNT(*) n FROM trials WHERE campaign_id=? GROUP BY status, outcome", (cid,))
    out["counts"] = counts
    runner = lab_.runners.get(cid)
    out["state"] = runner._state(c["status"]) if runner else None
    return out


@router.get("/campaigns/{cid}/grid")
async def campaign_grid(request: Request, cid: str):
    """Compact columnar trial state for the live canvas (≈4 bytes/trial over the wire)."""
    lab_ = lab(request)
    c = await lab_.db.fetchone("SELECT plan_json FROM campaigns WHERE id=?", (cid,))
    if not c:
        raise not_found("campaign")
    arm_keys = [a.key for a in expand_arms(ExperimentPlan.model_validate(loads(c["plan_json"])))]
    arm_idx = {k: i for i, k in enumerate(arm_keys)}
    rows = await lab_.db.fetchall("SELECT id, order_index, arm_key, status, outcome FROM trials WHERE campaign_id=? ORDER BY order_index", (cid,))
    return ORJSONResponse({
        "arms": arm_keys,
        "ids": [r["id"] for r in rows],
        "arm": [arm_idx.get(r["arm_key"], 0) for r in rows],
        "status": [STATUS_CODE.get(r["status"], 0) for r in rows],
        "outcome": [OUTCOME_CODE.get(r["outcome"], 0) for r in rows],
        "status_codes": STATUS_CODE, "outcome_codes": OUTCOME_CODE,
    })


@router.get("/campaigns/{cid}/events")
async def campaign_events(request: Request, cid: str):
    lab_ = lab(request)
    if not await lab_.db.fetchone("SELECT 1 FROM campaigns WHERE id=?", (cid,)):
        raise not_found("campaign")
    q = lab_.bus.subscribe(cid)

    async def stream():
        try:
            yield b"retry: 2000\n\n"
            runner = lab_.runners.get(cid)
            if runner:
                yield b"data: " + orjson.dumps({"type": "state", "state": runner._state("running")}) + b"\n\n"
            while True:
                if await request.is_disconnected():
                    break
                try:
                    ev = await asyncio.wait_for(q.get(), timeout=15)
                except asyncio.TimeoutError:
                    yield b": keep-alive\n\n"
                    if cid not in lab_.runners:
                        st = await lab_.db.scalar("SELECT status FROM campaigns WHERE id=?", (cid,))
                        yield b"data: " + orjson.dumps({"type": "idle", "status": st}) + b"\n\n"
                    continue
                yield b"data: " + orjson.dumps(ev) + b"\n\n"
        finally:
            lab_.bus.unsubscribe(cid, q)

    return StreamingResponse(stream(), media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})


@router.get("/campaigns/{cid}/trials")
async def campaign_trials(request: Request, cid: str, offset: int = 0, limit: int = 50, arm: str | None = None,
                          outcome: str | None = None, status: str | None = None, q: str | None = None, sort: str = "order"):
    db = lab(request).db
    where, params = ["t.campaign_id=?"], [cid]
    if arm:
        where.append("t.arm_key=?")
        params.append(arm)
    if outcome:
        where.append("t.outcome=?")
        params.append(outcome)
    if status:
        where.append("t.status=?")
        params.append(status)
    if q:
        where.append("(s.key LIKE ? OR t.output_text LIKE ?)")
        params += [f"%{q}%", f"%{q}%"]
    w = " AND ".join(where)
    order = {"order": "t.order_index", "latency": "t.latency_ms DESC", "cost": "t.cost_usd DESC", "sample": "s.ordinal, t.arm_key"}.get(sort, "t.order_index")
    limit = max(1, min(limit, 200))
    total = await db.scalar(f"SELECT COUNT(*) FROM trials t JOIN samples s ON s.id=t.sample_id WHERE {w}", params)
    rows = await db.fetchall(
        f"SELECT t.id, t.arm_key, t.repetition, t.order_index, t.status, t.outcome, t.error_category, t.latency_ms, t.ttft_ms, "
        f"t.tokens_in, t.tokens_out, t.cost_usd, t.attempt_count, t.resolved_model, substr(t.output_text,1,240) output_preview, "
        f"s.key sample_key, s.id sample_id FROM trials t JOIN samples s ON s.id=t.sample_id WHERE {w} ORDER BY {order} LIMIT ? OFFSET ?",
        (*params, limit, offset))
    return page(rows, total, offset, limit)


@router.get("/trials/{tid}")
async def trial_detail(request: Request, tid: str):
    db = lab(request).db
    t = await db.fetchone("SELECT * FROM trials WHERE id=?", (tid,))
    if not t:
        raise not_found("trial")
    s = await db.fetchone("SELECT * FROM samples WHERE id=?", (t["sample_id"],))
    camp = await db.fetchone("SELECT plan_json, scoring_json FROM campaigns WHERE id=?", (t["campaign_id"],))
    plan = ExperimentPlan.model_validate(loads(camp["plan_json"]))
    arm = next((a.as_dict() for a in expand_arms(plan) if a.key == t["arm_key"]), None)
    ds = await db.fetchone("SELECT instructions FROM datasets WHERE id=?", (plan.dataset_id,))
    t["sample"] = {"key": s["key"], "input": loads(s["input_json"]), "expected": loads(s["expected_json"]),
                   "meta": loads(s["meta_json"]), "split": s["split"]}
    t["arm"] = arm
    t["instructions"] = ds["instructions"] if ds else ""
    t["attempts"] = await db.fetchall("SELECT * FROM attempts WHERE trial_id=? ORDER BY n", (tid,))
    assessments = await db.fetchall("SELECT * FROM assessments WHERE trial_id=? ORDER BY revision DESC, scorer_key", (tid,))
    for a in assessments:
        a["evidence"] = loads(a.pop("evidence_json"))
    t["assessments"] = assessments
    t["scoring"] = loads(camp["scoring_json"])
    return t


@router.post("/campaigns/{cid}/cancel")
async def cancel_campaign(request: Request, cid: str):
    lab_ = lab(request)
    runner = lab_.runners.get(cid)
    if runner is None:
        st = await lab_.db.scalar("SELECT status FROM campaigns WHERE id=?", (cid,))
        if st is None:
            raise not_found("campaign")
        if st in ("paused", "requires_reconciliation", "queued"):
            await lab_.db.execute("UPDATE campaigns SET status='cancelled', stop_reason='user_cancelled', finished_at=? WHERE id=?",
                                  (time.time(), cid))
            await lab_.db.execute("UPDATE trials SET status='cancelled' WHERE campaign_id=? AND status IN ('pending','interrupted')", (cid,))
            await lab_.db.audit("campaign.cancelled", cid, {"while": st})
            return {"ok": True, "status": "cancelled"}
        raise HTTPException(409, f"campaign is {st}; nothing to cancel")
    runner.cancel()
    await lab_.db.audit("campaign.cancel_requested", cid, {})
    return {"ok": True, "status": "cancelling"}


@router.post("/campaigns/{cid}/resume")
async def resume_campaign(request: Request, cid: str, body: ResumeRequest | None = None):
    """Resume a paused (budget) or interrupted campaign. Remaining trials get NEW attempts; old ones stay audited."""
    lab_ = lab(request)
    c = await lab_.db.fetchone("SELECT status FROM campaigns WHERE id=?", (cid,))
    if not c:
        raise not_found("campaign")
    if cid in lab_.runners:
        raise HTTPException(409, "campaign already running")
    if c["status"] not in ("paused", "requires_reconciliation", "failed"):
        raise HTTPException(409, f"cannot resume a {c['status']} campaign")
    if body and body.budget_max_usd is not None:
        await lab_.db.execute("UPDATE campaigns SET budget_cap_usd=? WHERE id=?", (body.budget_max_usd, cid))
    await lab_.db.audit("campaign.resume", cid, {"from": c["status"], "new_cap": body.budget_max_usd if body else None})
    runner = CampaignRunner(lab_, cid)
    lab_.runners[cid] = runner
    lab_.spawn(runner.run())
    return {"ok": True}


@router.post("/campaigns/{cid}/rerun")
async def rerun_campaign(request: Request, cid: str):
    """Fresh rerun: new campaign, same frozen plan, new calls."""
    lab_ = lab(request)
    c = await lab_.db.fetchone("SELECT experiment_id, plan_json, labels_json FROM campaigns WHERE id=?", (cid,))
    if not c:
        raise not_found("campaign")
    plan = ExperimentPlan.model_validate(loads(c["plan_json"]))
    pf = (await preflight(lab_.db, lab_.registry, plan)).as_dict()
    _check_launchable(pf, plan)
    new_cid = await _start_campaign(lab_, c["experiment_id"], plan, pf["labels"], parent=cid)
    return {"campaign_id": new_cid}


@router.post("/campaigns/{cid}/rescore")
async def rescore_campaign(request: Request, cid: str, body: RescoreRequest):
    """Re-score stored outputs with new/changed scorers → new assessment revision. Outputs are untouched."""
    lab_ = lab(request)
    c = await lab_.db.fetchone("SELECT status, scoring_json, plan_json FROM campaigns WHERE id=?", (cid,))
    if not c:
        raise not_found("campaign")
    if cid in lab_.runners:
        raise HTTPException(409, "campaign is running")
    errs = [e for s in body.scorers for e in validate_spec(s.model_dump())]
    ids = [s.id for s in body.scorers]
    if len(set(ids)) != len(ids):
        errs.append("scorer ids must be unique")
    primary = body.primary_scorer or ids[0]
    if primary not in ids:
        errs.append("primary_scorer must reference a scorer id")
    if errs:
        raise HTTPException(422, {"message": "invalid scorers", "errors": errs})
    old = loads(c["scoring_json"]) or {"revision": 1}
    scoring = {"scorers": [s.model_dump() for s in body.scorers], "primary": primary, "revision": old.get("revision", 1) + 1}
    prev_status = c["status"]
    await lab_.db.execute("UPDATE campaigns SET status='grading' WHERE id=?", (cid,))
    await lab_.db.audit("campaign.rescore", cid, {"revision": scoring["revision"], "scorers": ids, "primary": primary})

    async def job():
        sem = asyncio.Semaphore(4)
        try:
            trials = await lab_.db.fetchall(
                "SELECT t.id, t.output_text, s.input_json, s.expected_json, s.meta_json FROM trials t JOIN samples s ON s.id=t.sample_id "
                "WHERE t.campaign_id=? AND t.status='completed'", (cid,))

            async def one(t):
                expected = loads(t["expected_json"])
                task_text = loads(t["input_json"])["text"]
                outcome = None
                for spec in scoring["scorers"]:
                    if is_judge(spec):
                        async with sem:
                            v = await run_scorer(lab_, spec, task_text, t["output_text"] or "", expected, loads(t["meta_json"]))
                    else:
                        v = await run_scorer(lab_, spec, task_text, t["output_text"] or "", expected, loads(t["meta_json"]))
                    await insert_assessment(lab_, t["id"], spec, scoring["revision"], v)
                    if spec["id"] == primary:
                        outcome = v.verdict
                o = outcome if outcome in ("pass", "fail", "inconclusive", "not_applicable") else "not_assessed"
                await lab_.db.execute("UPDATE trials SET outcome=? WHERE id=?", (o, t["id"]))

            await asyncio.gather(*(one(t) for t in trials))
            await lab_.db.execute("UPDATE campaigns SET scoring_json=?, status=? WHERE id=?", (dumps(scoring), prev_status, cid))
        except Exception as e:  # noqa: BLE001
            await lab_.db.execute("UPDATE campaigns SET status=?, stop_reason=? WHERE id=?", (prev_status, f"rescore_failed: {e}", cid))
        lab_.analysis_cache.pop(cid, None)
        lab_.bus.publish(cid, {"type": "rescored", "revision": scoring["revision"]})

    lab_.spawn(job())
    return {"ok": True, "revision": scoring["revision"]}


@router.get("/campaigns/{cid}/results")
async def campaign_results(request: Request, cid: str):
    try:
        return ORJSONResponse(await analyze(lab(request), cid))
    except KeyError:
        raise not_found("campaign")


async def _report_bundle(lab_: Lab, cid: str) -> dict[str, Any]:
    try:
        results = await analyze(lab_, cid)
    except KeyError:
        raise not_found("campaign")
    c = await lab_.db.fetchone(
        "SELECT c.*, e.name, e.hypothesis, e.plan_digest exp_digest FROM campaigns c JOIN experiments e ON e.id=c.experiment_id WHERE c.id=?",
        (cid,))
    failures = await lab_.db.fetchall(
        "SELECT t.id, t.arm_key, t.outcome, t.status, t.error_category, substr(t.output_text,1,400) output, s.key sample_key, "
        "s.expected_json FROM trials t JOIN samples s ON s.id=t.sample_id WHERE t.campaign_id=? AND (t.outcome IN ('fail','inconclusive') "
        "OR t.status='error') ORDER BY t.order_index LIMIT 60", (cid,))
    for f in failures:
        f["expected"] = loads(f.pop("expected_json"))
    attempts = await lab_.db.fetchone(
        "SELECT COUNT(*) n, SUM(CASE WHEN a.status='completed' THEN 1 ELSE 0 END) ok, SUM(CASE WHEN a.status='unknown' THEN 1 ELSE 0 END) unknown "
        "FROM attempts a JOIN trials t ON t.id=a.trial_id WHERE t.campaign_id=?", (cid,))
    return {"campaign": {k: v for k, v in c.items() if k not in ("plan_json", "scoring_json", "labels_json")},
            "plan": loads(c["plan_json"]), "results": results, "failures": failures, "attempts": attempts,
            "generated_at": time.time(), "lab_version": lab_.settings.version}


@router.get("/campaigns/{cid}/report.html")
async def report_html(request: Request, cid: str):
    return HTMLResponse(render_html(await _report_bundle(lab(request), cid)),
                        headers={"Content-Disposition": f'inline; filename="arl-report-{cid}.html"'})


@router.get("/campaigns/{cid}/report.md")
async def report_md(request: Request, cid: str):
    return PlainTextResponse(render_markdown(await _report_bundle(lab(request), cid)), media_type="text/markdown",
                             headers={"Content-Disposition": f'attachment; filename="arl-report-{cid}.md"'})


@router.get("/campaigns/{cid}/report.json")
async def report_json(request: Request, cid: str):
    lab_ = lab(request)
    bundle = await _report_bundle(lab_, cid)
    trials = await lab_.db.fetchall(
        "SELECT t.id, t.arm_key, s.key sample_key, t.repetition, t.status, t.outcome, t.output_text, t.latency_ms, t.ttft_ms, "
        "t.tokens_in, t.tokens_out, t.cost_usd, t.attempt_count, t.resolved_model, t.error_category "
        "FROM trials t JOIN samples s ON s.id=t.sample_id WHERE t.campaign_id=? ORDER BY t.order_index", (cid,))
    assess = await latest_assessments(lab_, cid)
    for t in trials:
        t["assessments"] = {k: {"verdict": v["verdict"], "score": v["score"], "version": v["scorer_version"]}
                            for k, v in assess.get(t["id"], {}).items()}
    bundle["trials"] = trials
    return ORJSONResponse(render_json(bundle),
                          headers={"Content-Disposition": f'attachment; filename="arl-results-{cid}.json"'})
