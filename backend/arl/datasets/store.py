"""Dataset import, validation, immutable snapshots, and content-addressed media (FR-007/008/017)."""
from __future__ import annotations

import base64
import csv
import hashlib
import io
import json
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable

from PIL import Image, UnidentifiedImageError

from ..db import Database, dumps, loads, new_id
from ..scorers.builtin import normalize

MAX_SAMPLES = 50_000
MAX_TEXT_CHARS = 200_000
MAX_IMAGE_BYTES = 20 * 1024 * 1024
MAX_IMAGE_PIXELS = 40_000_000
ALLOWED_IMAGE_FORMATS = {"PNG": "image/png", "JPEG": "image/jpeg", "WEBP": "image/webp", "GIF": "image/gif"}
SPLITS = {"train", "dev", "calibration", "test", "holdout"}

_INPUT_KEYS = ("input", "prompt", "question", "text", "query")
_EXPECTED_KEYS = ("expected", "answer", "label", "target", "reference", "output")
_RESERVED = set(_INPUT_KEYS + _EXPECTED_KEYS + ("id", "key", "split", "group", "group_id", "meta", "image", "images"))


class DatasetError(ValueError):
    def __init__(self, message: str, errors: list[dict[str, Any]] | None = None):
        super().__init__(message)
        self.errors = errors or []


@dataclass
class SampleIn:
    key: str
    text: str
    images: list[str] = field(default_factory=list)  # artifact digests
    expected: Any = None
    split: str = "test"
    group: str | None = None
    meta: dict[str, Any] = field(default_factory=dict)

    def content_hash(self) -> str:
        material = dumps({"text": self.text, "images": self.images, "expected": self.expected})
        return hashlib.sha256(material.encode()).hexdigest()


# ------------------------------------------------------------------ artifacts
class ArtifactStore:
    def __init__(self, db: Database, root: Path):
        self.db = db
        self.root = root

    def path_for(self, digest: str, ext: str) -> Path:
        return self.root / digest[:2] / f"{digest}.{ext}"

    async def put_image(self, data: bytes) -> dict[str, Any]:
        """Validate and store image bytes. Originals are never resized or re-encoded."""
        if len(data) > MAX_IMAGE_BYTES:
            raise DatasetError(f"image exceeds {MAX_IMAGE_BYTES // (1024 * 1024)} MB")
        try:
            with Image.open(io.BytesIO(data)) as im:
                fmt = im.format
                width, height = im.size
                im.verify()
        except (UnidentifiedImageError, OSError, SyntaxError) as e:
            raise DatasetError(f"not a valid image: {e}") from e
        if fmt not in ALLOWED_IMAGE_FORMATS:
            raise DatasetError(f"unsupported image format {fmt}")
        if width * height > MAX_IMAGE_PIXELS:
            raise DatasetError("image has too many pixels")
        digest = hashlib.sha256(data).hexdigest()
        existing = await self.db.fetchone("SELECT * FROM artifacts WHERE digest=?", (digest,))
        if existing:
            return existing
        ext = fmt.lower().replace("jpeg", "jpg")
        path = self.path_for(digest, ext)
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(".tmp")
        tmp.write_bytes(data)
        tmp.replace(path)  # atomic publish
        row = {"digest": digest, "mime": ALLOWED_IMAGE_FORMATS[fmt], "bytes": len(data), "width": width,
               "height": height, "path": str(path.relative_to(self.root)), "created_at": time.time()}
        await self.db.execute(
            "INSERT OR IGNORE INTO artifacts(digest, mime, bytes, width, height, path, created_at) VALUES (?,?,?,?,?,?,?)",
            tuple(row.values()),
        )
        return row

    async def get(self, digest: str) -> tuple[dict[str, Any], bytes] | None:
        row = await self.db.fetchone("SELECT * FROM artifacts WHERE digest=?", (digest,))
        if not row:
            return None
        path = self.root / row["path"]
        data = path.read_bytes()
        if hashlib.sha256(data).hexdigest() != digest:
            raise DatasetError(f"artifact {digest[:12]} failed integrity check")
        return row, data

    async def data_uri(self, digest: str) -> str:
        got = await self.get(digest)
        if got is None:
            raise DatasetError(f"missing artifact {digest[:12]}")
        row, data = got
        return f"data:{row['mime']};base64,{base64.b64encode(data).decode()}"


# ------------------------------------------------------------------ parsing
def _first(rec: dict[str, Any], keys: Iterable[str]) -> Any:
    for k in keys:
        if k in rec and rec[k] not in (None, ""):
            return rec[k]
    return None


def _maybe_json(v: Any) -> Any:
    if isinstance(v, str) and v[:1] in "{[":
        try:
            return json.loads(v)
        except json.JSONDecodeError:
            return v
    return v


def parse_records(content: bytes, filename: str) -> list[dict[str, Any]]:
    name = filename.lower()
    text = content.decode("utf-8-sig")
    if name.endswith(".jsonl") or name.endswith(".ndjson"):
        out = []
        for i, line in enumerate(text.splitlines(), 1):
            if not line.strip():
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError as e:
                raise DatasetError(f"line {i}: invalid JSON ({e.msg})", [{"line": i, "error": e.msg}]) from e
            if not isinstance(rec, dict):
                raise DatasetError(f"line {i}: record must be an object", [{"line": i, "error": "not an object"}])
            out.append(rec)
        return out
    if name.endswith(".json"):
        data = json.loads(text)
        if isinstance(data, dict) and "samples" in data:
            data = data["samples"]
        if not isinstance(data, list):
            raise DatasetError("JSON file must be a list of records or {samples: [...]}")
        return data
    if name.endswith(".csv") or name.endswith(".tsv"):
        reader = csv.DictReader(io.StringIO(text), delimiter="\t" if name.endswith(".tsv") else ",")
        return [{k: _maybe_json(v) for k, v in row.items() if k is not None} for row in reader]
    raise DatasetError("unsupported file type (use .jsonl, .json, .csv, .tsv)")


async def normalize_records(records: list[dict[str, Any]], artifacts: ArtifactStore,
                            images: dict[str, bytes] | None = None) -> tuple[list[SampleIn], list[dict[str, Any]]]:
    """Map loose records onto the canonical sample shape. Returns (samples, errors)."""
    images = images or {}
    samples: list[SampleIn] = []
    errors: list[dict[str, Any]] = []
    seen: set[str] = set()
    if len(records) > MAX_SAMPLES:
        raise DatasetError(f"too many samples ({len(records)} > {MAX_SAMPLES})")
    for i, rec in enumerate(records, 1):
        raw_input = _first(rec, _INPUT_KEYS)
        img_refs: list[str] = []
        if isinstance(raw_input, dict):
            text = str(raw_input.get("text") or raw_input.get("prompt") or "")
            img_refs = list(raw_input.get("images") or [])
        else:
            text = "" if raw_input is None else str(raw_input)
        for k in ("image", "images"):
            v = rec.get(k)
            if isinstance(v, str) and v:
                img_refs += [s.strip() for s in v.split("|") if s.strip()]
            elif isinstance(v, list):
                img_refs += [str(s) for s in v]
        if not text and not img_refs:
            errors.append({"record": i, "error": "missing input (text or image)"})
            continue
        if len(text) > MAX_TEXT_CHARS:
            errors.append({"record": i, "error": f"input text exceeds {MAX_TEXT_CHARS} chars"})
            continue
        digests = []
        bad = False
        for ref in img_refs:
            try:
                if ref in images:
                    digests.append((await artifacts.put_image(images[ref]))["digest"])
                elif ref.startswith("data:image"):
                    digests.append((await artifacts.put_image(base64.b64decode(ref.split(",", 1)[1])))["digest"])
                elif len(ref) == 64 and await artifacts.db.fetchone("SELECT 1 FROM artifacts WHERE digest=?", (ref,)):
                    digests.append(ref)
                else:
                    raise DatasetError(f"image '{ref[:60]}' not provided (upload it with the dataset)")
            except (DatasetError, ValueError) as e:
                errors.append({"record": i, "error": str(e)})
                bad = True
                break
        if bad:
            continue
        key = str(rec.get("id") or rec.get("key") or f"s{i:05d}")
        if key in seen:
            errors.append({"record": i, "error": f"duplicate sample id '{key}'"})
            continue
        seen.add(key)
        split = str(rec.get("split") or "test").lower()
        if split not in SPLITS:
            errors.append({"record": i, "error": f"unknown split '{split}' (allowed: {sorted(SPLITS)})"})
            continue
        meta = dict(rec.get("meta") or {}) if isinstance(rec.get("meta"), dict) else {}
        for k, v in rec.items():
            if k not in _RESERVED and v not in (None, ""):
                meta.setdefault(k, v)
        expected = _maybe_json(_first(rec, _EXPECTED_KEYS))
        samples.append(SampleIn(key=key, text=text, images=digests, expected=expected, split=split,
                                group=str(rec.get("group") or rec.get("group_id")) if (rec.get("group") or rec.get("group_id")) else None,
                                meta=meta))
    return samples, errors


def leakage_report(samples: list[SampleIn]) -> dict[str, Any]:
    """Exact and normalized-text duplicates that cross split boundaries, plus groups spanning splits."""
    by_hash: dict[str, set[str]] = {}
    by_norm: dict[str, set[str]] = {}
    by_group: dict[str, set[str]] = {}
    for s in samples:
        by_hash.setdefault(s.content_hash(), set()).add(s.split)
        norm_key = hashlib.sha1((normalize(s.text) + "|" + ",".join(s.images)).encode()).hexdigest()
        by_norm.setdefault(norm_key, set()).add(s.split)
        if s.group:
            by_group.setdefault(s.group, set()).add(s.split)
    return {
        "exact_cross_split_duplicates": sum(1 for v in by_hash.values() if len(v) > 1),
        "near_cross_split_duplicates": sum(1 for v in by_norm.values() if len(v) > 1),
        "groups_spanning_splits": sorted(g for g, v in by_group.items() if len(v) > 1)[:50],
        "method": "sha256(content) + sha1(normalized text + image digests); group ids",
    }


def dataset_digest(samples: list[SampleIn]) -> str:
    h = hashlib.sha256()
    for s in sorted(samples, key=lambda x: x.key):
        h.update(f"{s.key}:{s.split}:{s.group}:{s.content_hash()}\n".encode())
    return h.hexdigest()


async def create_dataset(db: Database, *, name: str, samples: list[SampleIn], description: str = "",
                         instructions: str = "", task_type: str = "generic", card: dict[str, Any] | None = None,
                         builtin: bool = False, synthetic: bool = False) -> dict[str, Any]:
    if not samples:
        raise DatasetError("dataset has no valid samples")
    modality = "image" if any(s.images for s in samples) else "text"
    digest = dataset_digest(samples)
    existing = await db.fetchone("SELECT * FROM datasets WHERE name=? AND digest=?", (name, digest))
    if existing:
        return existing  # identical snapshot already stored: content addressing makes import idempotent
    version = (await db.scalar("SELECT COALESCE(MAX(version), 0) FROM datasets WHERE name=?", (name,))) + 1
    card = dict(card or {})
    splits: dict[str, int] = {}
    for s in samples:
        splits[s.split] = splits.get(s.split, 0) + 1
    card.setdefault("splits", splits)
    card["leakage"] = leakage_report(samples)
    card.setdefault("synthetic", synthetic)
    ds_id = new_id("ds")
    now = time.time()
    async with db.tx() as conn:
        await conn.execute(
            "INSERT INTO datasets(id, name, version, digest, modality, task_type, description, instructions, card_json, "
            "sample_count, builtin, synthetic, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
            (ds_id, name, version, digest, modality, task_type, description, instructions, dumps(card),
             len(samples), int(builtin), int(synthetic), now),
        )
        await conn.executemany(
            "INSERT INTO samples(id, dataset_id, ordinal, key, split, group_id, input_json, expected_json, meta_json, content_hash) "
            "VALUES (?,?,?,?,?,?,?,?,?,?)",
            [
                (new_id("smp"), ds_id, i, s.key, s.split, s.group, dumps({"text": s.text, "images": s.images}),
                 None if s.expected is None else dumps(s.expected), dumps(s.meta), s.content_hash())
                for i, s in enumerate(samples)
            ],
        )
    await db.audit("dataset.create", ds_id, {"name": name, "version": version, "digest": digest, "samples": len(samples)})
    return await db.fetchone("SELECT * FROM datasets WHERE id=?", (ds_id,))


def dataset_row(row: dict[str, Any]) -> dict[str, Any]:
    out = dict(row)
    out["card"] = loads(out.pop("card_json"))
    out["builtin"] = bool(out["builtin"])
    out["synthetic"] = bool(out["synthetic"])
    return out


def sample_row(row: dict[str, Any], include_expected: bool = True) -> dict[str, Any]:
    out = {
        "id": row["id"], "key": row["key"], "ordinal": row["ordinal"], "split": row["split"], "group": row["group_id"],
        "input": loads(row["input_json"]), "meta": loads(row["meta_json"]), "content_hash": row["content_hash"],
    }
    if include_expected:
        out["expected"] = loads(row["expected_json"])
    return out
