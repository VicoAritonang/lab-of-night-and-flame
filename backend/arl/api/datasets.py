"""Datasets: list, detail, paginated samples, import (JSONL/CSV + images), predictions import."""
from __future__ import annotations

import json
import time

from fastapi import APIRouter, File, Form, HTTPException, Request, UploadFile

from ..datasets.store import (
    DatasetError, create_dataset, dataset_row, normalize_records, parse_records, sample_row,
)
from ..db import new_id
from .deps import lab, not_found, page

router = APIRouter(prefix="/api/datasets")


@router.get("")
async def list_datasets(request: Request):
    rows = await lab(request).db.fetchall("SELECT * FROM datasets ORDER BY builtin DESC, name, version DESC")
    return [dataset_row(r) for r in rows]


@router.get("/{dataset_id}")
async def get_dataset(request: Request, dataset_id: str):
    db = lab(request).db
    row = await db.fetchone("SELECT * FROM datasets WHERE id=?", (dataset_id,))
    if not row:
        raise not_found("dataset")
    out = dataset_row(row)
    out["used_by_campaigns"] = await db.scalar(
        "SELECT COUNT(*) FROM campaigns c JOIN experiments e ON e.id=c.experiment_id WHERE json_extract(e.plan_json,'$.dataset_id')=?",
        (dataset_id,))
    out["predictions"] = await db.fetchall(
        "SELECT import_name, COUNT(*) n FROM predictions WHERE dataset_id=? GROUP BY import_name", (dataset_id,))
    meta_keys = await db.fetchall(
        "SELECT DISTINCT j.key FROM samples, json_each(samples.meta_json) j WHERE dataset_id=? LIMIT 50", (dataset_id,))
    out["meta_keys"] = [r["key"] for r in meta_keys]
    return out


@router.get("/{dataset_id}/samples")
async def samples(request: Request, dataset_id: str, offset: int = 0, limit: int = 50, split: str | None = None,
                  q: str | None = None):
    db = lab(request).db
    where, params = ["dataset_id=?"], [dataset_id]
    if split:
        where.append("split=?")
        params.append(split)
    if q:
        where.append("(key LIKE ? OR input_json LIKE ?)")
        params += [f"%{q}%", f"%{q}%"]
    w = " AND ".join(where)
    limit = max(1, min(limit, 200))
    total = await db.scalar(f"SELECT COUNT(*) FROM samples WHERE {w}", params)
    rows = await db.fetchall(f"SELECT * FROM samples WHERE {w} ORDER BY ordinal LIMIT ? OFFSET ?", (*params, limit, offset))
    return page([sample_row(r) for r in rows], total, offset, limit)


@router.post("/import")
async def import_dataset(
    request: Request,
    file: UploadFile = File(...),
    images: list[UploadFile] = File(default=[]),
    name: str = Form(...),
    description: str = Form(""),
    instructions: str = Form(""),
    task_type: str = Form("generic"),
    allow_partial: bool = Form(False),
):
    lab_ = lab(request)
    max_b = lab_.settings.max_upload_bytes
    content = await file.read(max_b * 5 + 1)
    if len(content) > max_b * 5:
        raise HTTPException(413, "dataset file too large")
    name = name.strip()
    if not name or len(name) > 80:
        raise HTTPException(422, "name must be 1-80 characters")
    img_map = {}
    for up in images:
        data = await up.read(max_b + 1)
        if len(data) > max_b:
            raise HTTPException(413, f"image '{up.filename}' too large")
        img_map[up.filename or ""] = data
    try:
        records = parse_records(content, file.filename or "data.jsonl")
        parsed, errors = await normalize_records(records, lab_.artifacts, img_map)
        if errors and not allow_partial:
            raise DatasetError(f"{len(errors)} invalid record(s)", errors)
        ds = await create_dataset(lab_.db, name=name, samples=parsed, description=description, instructions=instructions,
                                  task_type=task_type, card={"origin": "user import", "source_file": file.filename,
                                                             "rejected_records": len(errors)})
    except DatasetError as e:
        raise HTTPException(422, {"message": str(e), "errors": e.errors[:50]})
    except (UnicodeDecodeError, ValueError) as e:
        raise HTTPException(422, {"message": f"could not parse file: {e}", "errors": []})
    out = dataset_row(ds)
    out["rejected"] = errors[:50]
    return out


@router.post("/{dataset_id}/predictions")
async def import_predictions(request: Request, dataset_id: str, name: str = Form(...), file: UploadFile = File(...)):
    """Imported-output evaluation: JSONL/CSV rows with {id, output}."""
    db = lab(request).db
    if not await db.fetchone("SELECT 1 FROM datasets WHERE id=?", (dataset_id,)):
        raise not_found("dataset")
    try:
        records = parse_records(await file.read(), file.filename or "p.jsonl")
    except DatasetError as e:
        raise HTTPException(422, str(e))
    keys = {r["key"] for r in await db.fetchall("SELECT key FROM samples WHERE dataset_id=?", (dataset_id,))}
    rows, unknown = [], []
    now = time.time()
    for r in records:
        k = str(r.get("id") or r.get("key") or "")
        out = r.get("output") if r.get("output") is not None else r.get("prediction")
        if k not in keys or out is None:
            unknown.append(k)
            continue
        rows.append((new_id("pred"), name, dataset_id, k, out if isinstance(out, str) else json.dumps(out), now))
    if not rows:
        raise HTTPException(422, "no rows matched dataset sample ids")
    async with db.tx() as conn:
        await conn.executemany(
            "INSERT OR REPLACE INTO predictions(id, import_name, dataset_id, sample_key, output_text, created_at) VALUES (?,?,?,?,?,?)",
            rows)
    await db.audit("predictions.import", dataset_id, {"name": name, "rows": len(rows), "unmatched": len(unknown)})
    return {"imported": len(rows), "unmatched": unknown[:50]}


@router.delete("/{dataset_id}")
async def delete_dataset(request: Request, dataset_id: str):
    db = lab(request).db
    used = await db.scalar(
        "SELECT COUNT(*) FROM experiments WHERE json_extract(plan_json,'$.dataset_id')=? AND status='frozen'", (dataset_id,))
    if used:
        raise HTTPException(409, "dataset is referenced by frozen experiments; archived evidence must stay reproducible")
    await db.execute("DELETE FROM datasets WHERE id=? AND builtin=0", (dataset_id,))
    await db.audit("dataset.delete", dataset_id, {})
    return {"ok": True}
