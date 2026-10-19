"""Health, auth, overview, providers, catalog, scorers, artifacts, playground, audit."""
from __future__ import annotations

import asyncio
import time
from typing import Any

import orjson
from fastapi import APIRouter, File, HTTPException, Request, Response, UploadFile
from fastapi.responses import ORJSONResponse, StreamingResponse

from ..datasets.store import DatasetError
from ..db import loads
from ..models import ConnectionCreate, PlaygroundRequest
from ..providers.base import AdapterError, ContentPart, GenerationRequest, Message
from ..scorers.registry import catalog as scorer_catalog
from .deps import COOKIE, check_token, lab, not_found

router = APIRouter(prefix="/api")


@router.get("/health")
async def health(request: Request):
    lab_ = lab(request)
    await lab_.db.scalar("SELECT 1")
    return {"status": "ok", "version": lab_.settings.version, "uptime_s": round(time.time() - lab_.started_at, 1),
            "active_campaigns": len(lab_.runners), "auth_required": bool(lab_.settings.auth_token)}


@router.get("/auth/status")
async def auth_status(request: Request):
    lab_ = lab(request)
    return {"auth_required": bool(lab_.settings.auth_token), "authenticated": check_token(lab_, request)}


@router.post("/auth/login")
async def login(request: Request, response: Response):
    lab_ = lab(request)
    body = await request.json()
    token = lab_.settings.auth_token
    if not token:
        return {"authenticated": True}
    import hmac
    if not hmac.compare_digest(str(body.get("token", "")), token):
        await lab_.db.audit("auth.login_failed", "owner", {}, actor="anonymous")
        raise HTTPException(401, "invalid token")
    response.set_cookie(COOKIE, token, httponly=True, samesite="strict", max_age=60 * 60 * 24 * 14)
    return {"authenticated": True}


@router.post("/auth/logout")
async def logout(response: Response):
    response.delete_cookie(COOKIE)
    return {"ok": True}


@router.get("/overview")
async def overview(request: Request):
    lab_ = lab(request)
    db = lab_.db
    totals = await db.fetchone(
        "SELECT (SELECT COUNT(*) FROM campaigns) campaigns, (SELECT COUNT(*) FROM trials) trials, "
        "(SELECT COUNT(*) FROM datasets) datasets, (SELECT COUNT(*) FROM experiments) experiments, "
        "(SELECT COALESCE(SUM(spent_usd),0) FROM campaigns) spent_usd, "
        "(SELECT COALESCE(SUM(unknown_cost_attempts),0) FROM campaigns) unknown_cost_attempts, "
        "(SELECT COUNT(*) FROM trials WHERE outcome='pass') passes, "
        "(SELECT COUNT(*) FROM trials WHERE outcome IN ('pass','fail')) assessed, "
        "(SELECT COUNT(*) FROM attempts) attempts")
    recent = await db.fetchall(
        "SELECT c.id, c.status, c.created_at, c.finished_at, c.spent_usd, c.planned_trials, c.labels_json, e.name, e.id experiment_id, "
        "(SELECT COUNT(*) FROM trials t WHERE t.campaign_id=c.id AND t.status IN ('completed','error','cancelled','not_applicable')) done, "
        "(SELECT COUNT(*) FROM trials t WHERE t.campaign_id=c.id AND t.outcome='pass') passes, "
        "(SELECT COUNT(*) FROM trials t WHERE t.campaign_id=c.id AND t.outcome IN ('pass','fail')) assessed "
        "FROM campaigns c JOIN experiments e ON e.id=c.experiment_id ORDER BY c.created_at DESC LIMIT 8")
    for r in recent:
        r["labels"] = loads(r.pop("labels_json"))
    daily = await db.fetchall(
        "SELECT CAST(started_at/86400 AS INT) day, COUNT(*) attempts, SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END) ok, "
        "COALESCE(SUM(cost_usd),0) cost FROM attempts WHERE started_at > ? GROUP BY day ORDER BY day", (time.time() - 14 * 86400,))
    return {"totals": totals, "recent": recent, "providers": lab_.registry.provider_status(), "daily": daily,
            "active": list(lab_.runners)}


# ------------------------------------------------------------------ providers & catalog
@router.get("/providers")
async def providers(request: Request):
    return lab(request).registry.provider_status()


@router.post("/connections")
async def add_connection(request: Request, body: ConnectionCreate):
    lab_ = lab(request)
    if body.name in lab_.registry.adapters:
        raise HTTPException(409, f"provider '{body.name}' already exists")
    row = body.model_dump()
    await lab_.db.execute("INSERT INTO connections(name, label, base_url, key_env, rpm, created_at) VALUES (?,?,?,?,?,?)",
                          (row["name"], row["label"], row["base_url"], row["key_env"], row["rpm"], time.time()))
    lab_.registry.add_connection(row)
    await lab_.db.audit("connection.create", row["name"], {"base_url": row["base_url"], "key_env": row["key_env"]})
    return {"ok": True}


@router.get("/catalog")
async def catalog(request: Request, provider: str | None = None, q: str | None = None, modality: str | None = None,
                  free: bool = False, refresh: bool = False, offset: int = 0, limit: int = 100, sort: str = "name"):
    reg = lab(request).registry
    models = await (reg.catalog(provider, refresh) if provider else reg.full_catalog(refresh))
    if q:
        terms = q.lower().split()
        models = [m for m in models if all(t in (m["id"] + " " + m["name"]).lower() for t in terms)]
    if modality:
        models = [m for m in models if modality in (m.get("modalities") or [])]
    if free:
        models = [m for m in models if m.get("free") or (m.get("price_in") == 0 and m.get("price_out") == 0)]
    keyfn = {
        "name": lambda m: (m["provider"], m["name"].lower()),
        "price": lambda m: (m.get("price_in") is None, (m.get("price_in") or 0) + (m.get("price_out") or 0)),
        "context": lambda m: -(m.get("context_length") or 0),
        "newest": lambda m: -(m.get("created") or 0),
    }.get(sort, lambda m: m["id"])
    models = sorted(models, key=keyfn)
    limit = max(1, min(limit, 500))
    counts: dict[str, int] = {}
    for m in models:
        counts[m["provider"]] = counts.get(m["provider"], 0) + 1
    return ORJSONResponse({"items": models[offset: offset + limit], "total": len(models), "by_provider": counts})


@router.get("/scorers")
async def scorers():
    return scorer_catalog()


# ------------------------------------------------------------------ artifacts
@router.get("/artifacts/{digest}")
async def artifact(request: Request, digest: str):
    try:
        got = await lab(request).artifacts.get(digest)
    except DatasetError as e:
        raise HTTPException(500, str(e))
    if got is None:
        raise not_found("artifact")
    row, data = got
    return Response(data, media_type=row["mime"], headers={"Cache-Control": "public, max-age=31536000, immutable",
                                                           "ETag": f'"{digest}"', "X-Content-Type-Options": "nosniff"})


@router.post("/artifacts")
async def upload_artifact(request: Request, file: UploadFile = File(...)):
    lab_ = lab(request)
    data = await file.read(lab_.settings.max_upload_bytes + 1)
    try:
        row = await lab_.artifacts.put_image(data)
    except DatasetError as e:
        raise HTTPException(422, str(e))
    return {"digest": row["digest"], "width": row["width"], "height": row["height"], "mime": row["mime"], "bytes": row["bytes"]}


# ------------------------------------------------------------------ playground
def _sse(event: dict[str, Any]) -> bytes:
    return b"data: " + orjson.dumps(event) + b"\n\n"


@router.post("/playground")
async def playground(request: Request, body: PlaygroundRequest):
    """Run one prompt against N targets concurrently and stream deltas (multiplexed SSE)."""
    lab_ = lab(request)
    reg = lab_.registry
    for t in body.targets:
        if t.provider not in reg.adapters or t.provider == "imported":
            raise HTTPException(422, f"provider '{t.provider}' not available in playground")
    parts = [ContentPart("text", body.prompt)] if body.prompt else []
    for d in body.images:
        try:
            parts.append(ContentPart("image", data_uri=await lab_.artifacts.data_uri(d)))
        except DatasetError as e:
            raise HTTPException(422, str(e))
    if not parts:
        raise HTTPException(422, "prompt or image required")
    messages = ([Message("system", [ContentPart("text", body.system)])] if body.system.strip() else []) + [Message("user", parts)]
    queue: asyncio.Queue = asyncio.Queue()

    async def run_one(idx: int, t) -> None:
        def on_delta(piece: str) -> None:
            queue.put_nowait({"type": "delta", "i": idx, "text": piece})

        req = GenerationRequest(model=t.model, messages=messages, params=t.params.model_dump(exclude_none=True),
                                timeout_s=180, stream=True, on_delta=on_delta, sample_key="playground")
        queue.put_nowait({"type": "start", "i": idx})
        try:
            await reg.buckets[t.provider].acquire()
            res = await reg.get(t.provider).generate(req)
            queue.put_nowait({"type": "done", "i": idx, "result": {
                "text": res.text, "resolved_model": res.resolved_model, "latency_ms": res.latency_ms, "ttft_ms": res.ttft_ms,
                "tokens_in": res.tokens_in, "tokens_out": res.tokens_out, "reasoning_tokens": res.reasoning_tokens,
                "cost_usd": res.cost_usd, "cost_source": res.cost_source, "finish_reason": res.finish_reason,
                "request_id": res.request_id}})
        except AdapterError as e:
            queue.put_nowait({"type": "error", "i": idx, "category": e.category, "message": str(e)})
        except Exception as e:  # noqa: BLE001
            queue.put_nowait({"type": "error", "i": idx, "category": "unknown", "message": f"{type(e).__name__}"})

    async def stream():
        tasks = [asyncio.create_task(run_one(i, t)) for i, t in enumerate(body.targets)]
        remaining = len(tasks)
        try:
            while remaining:
                ev = await queue.get()
                if ev["type"] in ("done", "error"):
                    remaining -= 1
                yield _sse(ev)
            yield _sse({"type": "end"})
        finally:
            for t in tasks:
                t.cancel()

    await lab_.db.audit("playground.run", "playground", {"targets": [f"{t.provider}/{t.model}" for t in body.targets],
                                                         "images": len(body.images)})
    return StreamingResponse(stream(), media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})


@router.get("/audit")
async def audit(request: Request, limit: int = 100):
    rows = await lab(request).db.fetchall("SELECT * FROM audit_log ORDER BY id DESC LIMIT ?", (min(limit, 1000),))
    for r in rows:
        r["detail"] = loads(r.pop("detail_json"))
    return rows

