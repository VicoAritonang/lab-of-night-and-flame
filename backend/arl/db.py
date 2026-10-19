"""Authoritative metadata store (SQLite, WAL).

One connection is shared by the process; aiosqlite runs it on a dedicated thread so all
statements are serialized. Multi-statement writes go through ``tx()`` which holds an
asyncio lock so concurrent coroutines never interleave inside a transaction.
"""
from __future__ import annotations

import asyncio
import contextlib
import time
import uuid
from pathlib import Path
from typing import Any, AsyncIterator, Iterable

import aiosqlite
import orjson

SCHEMA_VERSION = 1

SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS datasets (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  version INTEGER NOT NULL,
  digest TEXT NOT NULL,
  modality TEXT NOT NULL,
  task_type TEXT NOT NULL DEFAULT 'generic',
  description TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  card_json TEXT NOT NULL DEFAULT '{}',
  sample_count INTEGER NOT NULL,
  builtin INTEGER NOT NULL DEFAULT 0,
  synthetic INTEGER NOT NULL DEFAULT 0,
  created_at REAL NOT NULL,
  UNIQUE(name, version)
);

CREATE TABLE IF NOT EXISTS samples (
  id TEXT PRIMARY KEY,
  dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  key TEXT NOT NULL,
  split TEXT NOT NULL DEFAULT 'test',
  group_id TEXT,
  input_json TEXT NOT NULL,
  expected_json TEXT,
  meta_json TEXT NOT NULL DEFAULT '{}',
  content_hash TEXT NOT NULL,
  UNIQUE(dataset_id, key)
);
CREATE INDEX IF NOT EXISTS ix_samples_ds ON samples(dataset_id, ordinal);

CREATE TABLE IF NOT EXISTS artifacts (
  digest TEXT PRIMARY KEY,
  mime TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  width INTEGER,
  height INTEGER,
  path TEXT NOT NULL,
  created_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS experiments (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  hypothesis TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  plan_digest TEXT,
  parent_id TEXT,
  created_at REAL NOT NULL,
  frozen_at REAL
);

CREATE TABLE IF NOT EXISTS campaigns (
  id TEXT PRIMARY KEY,
  experiment_id TEXT NOT NULL REFERENCES experiments(id),
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  plan_digest TEXT NOT NULL,
  seed INTEGER NOT NULL,
  budget_cap_usd REAL,
  reserved_usd REAL NOT NULL DEFAULT 0,
  spent_usd REAL NOT NULL DEFAULT 0,
  unknown_cost_attempts INTEGER NOT NULL DEFAULT 0,
  planned_trials INTEGER NOT NULL,
  stop_reason TEXT,
  scoring_json TEXT,
  labels_json TEXT NOT NULL DEFAULT '[]',
  parent_campaign_id TEXT,
  created_at REAL NOT NULL,
  started_at REAL,
  finished_at REAL
);
CREATE INDEX IF NOT EXISTS ix_campaigns_exp ON campaigns(experiment_id);

CREATE TABLE IF NOT EXISTS trials (
  id TEXT PRIMARY KEY,
  campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  arm_key TEXT NOT NULL,
  sample_id TEXT NOT NULL,
  repetition INTEGER NOT NULL,
  order_index INTEGER NOT NULL,
  status TEXT NOT NULL,
  outcome TEXT NOT NULL DEFAULT 'not_assessed',
  output_text TEXT,
  error_category TEXT,
  latency_ms REAL,
  ttft_ms REAL,
  tokens_in INTEGER,
  tokens_out INTEGER,
  cost_usd REAL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  resolved_model TEXT,
  upstream_provider TEXT,
  started_at REAL,
  finished_at REAL
);
CREATE INDEX IF NOT EXISTS ix_trials_campaign ON trials(campaign_id, order_index);
CREATE INDEX IF NOT EXISTS ix_trials_arm ON trials(campaign_id, arm_key);
CREATE INDEX IF NOT EXISTS ix_trials_status ON trials(campaign_id, status);

CREATE TABLE IF NOT EXISTS attempts (
  id TEXT PRIMARY KEY,
  trial_id TEXT NOT NULL REFERENCES trials(id) ON DELETE CASCADE,
  n INTEGER NOT NULL,
  status TEXT NOT NULL,
  error_category TEXT,
  error_message TEXT,
  provider TEXT NOT NULL,
  requested_model TEXT NOT NULL,
  resolved_model TEXT,
  upstream_provider TEXT,
  request_id TEXT,
  finish_reason TEXT,
  latency_ms REAL,
  ttft_ms REAL,
  tokens_in INTEGER,
  tokens_out INTEGER,
  cost_usd REAL,
  cost_source TEXT,
  reserved_usd REAL NOT NULL DEFAULT 0,
  started_at REAL NOT NULL,
  finished_at REAL
);
CREATE INDEX IF NOT EXISTS ix_attempts_trial ON attempts(trial_id, n);

CREATE TABLE IF NOT EXISTS assessments (
  id TEXT PRIMARY KEY,
  trial_id TEXT NOT NULL REFERENCES trials(id) ON DELETE CASCADE,
  scorer_key TEXT NOT NULL,
  scorer_version TEXT NOT NULL,
  revision INTEGER NOT NULL,
  score REAL,
  passed INTEGER,
  verdict TEXT NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '{}',
  cost_usd REAL,
  created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_assess_trial ON assessments(trial_id, revision);

CREATE TABLE IF NOT EXISTS predictions (
  id TEXT PRIMARY KEY,
  import_name TEXT NOT NULL,
  dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  sample_key TEXT NOT NULL,
  output_text TEXT NOT NULL,
  created_at REAL NOT NULL,
  UNIQUE(import_name, dataset_id, sample_key)
);

CREATE TABLE IF NOT EXISTS connections (
  name TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  base_url TEXT NOT NULL,
  key_env TEXT,
  rpm INTEGER NOT NULL DEFAULT 60,
  created_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts REAL NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  resource TEXT NOT NULL,
  detail_json TEXT NOT NULL DEFAULT '{}'
);
"""


def new_id(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex[:16]}"


def dumps(obj: Any) -> str:
    return orjson.dumps(obj, option=orjson.OPT_SORT_KEYS | orjson.OPT_SERIALIZE_NUMPY).decode()


def loads(s: str | bytes | None) -> Any:
    return None if s is None else orjson.loads(s)


class Database:
    def __init__(self, path: Path):
        self.path = path
        self._conn: aiosqlite.Connection | None = None
        self._write_lock = asyncio.Lock()

    @property
    def conn(self) -> aiosqlite.Connection:
        if self._conn is None:
            raise RuntimeError("database not connected")
        return self._conn

    async def connect(self) -> None:
        self._conn = await aiosqlite.connect(self.path, isolation_level=None)
        self._conn.row_factory = aiosqlite.Row
        for pragma in (
            "PRAGMA journal_mode=WAL",
            "PRAGMA synchronous=NORMAL",
            "PRAGMA foreign_keys=ON",
            "PRAGMA busy_timeout=5000",
            "PRAGMA temp_store=MEMORY",
            "PRAGMA cache_size=-32000",
        ):
            await self._conn.execute(pragma)
        await self._conn.executescript(SCHEMA)
        await self._migrate()
        await self._conn.execute(
            "INSERT OR IGNORE INTO meta(key, value) VALUES('schema_version', ?)", (str(SCHEMA_VERSION),)
        )

    async def _migrate(self) -> None:
        """Additive, idempotent column migrations for databases created by older versions."""
        additions = {"attempts": [("upstream_provider", "TEXT")], "trials": [("upstream_provider", "TEXT")]}
        for table, cols in additions.items():
            async with self._conn.execute(f"PRAGMA table_info({table})") as cur:
                have = {r[1] for r in await cur.fetchall()}
            for name, typ in cols:
                if name not in have:
                    await self._conn.execute(f"ALTER TABLE {table} ADD COLUMN {name} {typ}")

    async def close(self) -> None:
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    @contextlib.asynccontextmanager
    async def tx(self) -> AsyncIterator[aiosqlite.Connection]:
        async with self._write_lock:
            await self.conn.execute("BEGIN IMMEDIATE")
            try:
                yield self.conn
            except BaseException:
                await self.conn.execute("ROLLBACK")
                raise
            else:
                await self.conn.execute("COMMIT")

    async def execute(self, sql: str, params: Iterable[Any] = ()) -> None:
        async with self._write_lock:
            await self.conn.execute(sql, tuple(params))

    async def fetchall(self, sql: str, params: Iterable[Any] = ()) -> list[dict[str, Any]]:
        async with self.conn.execute(sql, tuple(params)) as cur:
            rows = await cur.fetchall()
        return [dict(r) for r in rows]

    async def fetchone(self, sql: str, params: Iterable[Any] = ()) -> dict[str, Any] | None:
        async with self.conn.execute(sql, tuple(params)) as cur:
            row = await cur.fetchone()
        return dict(row) if row else None

    async def scalar(self, sql: str, params: Iterable[Any] = ()) -> Any:
        async with self.conn.execute(sql, tuple(params)) as cur:
            row = await cur.fetchone()
        return row[0] if row else None

    async def audit(self, action: str, resource: str, detail: dict[str, Any] | None = None, actor: str = "owner") -> None:
        await self.execute(
            "INSERT INTO audit_log(ts, actor, action, resource, detail_json) VALUES (?,?,?,?,?)",
            (time.time(), actor, action, resource, dumps(detail or {})),
        )
