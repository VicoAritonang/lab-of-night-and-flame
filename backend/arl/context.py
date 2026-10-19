"""Process-wide application context."""
from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field
from typing import Any

from .config import Settings
from .datasets.store import ArtifactStore
from .db import Database
from .engine.events import EventBus
from .providers.registry import Registry


@dataclass
class Lab:
    settings: Settings
    db: Database
    registry: Registry
    artifacts: ArtifactStore
    bus: EventBus
    runners: dict[str, Any] = field(default_factory=dict)
    tasks: set[asyncio.Task] = field(default_factory=set)
    analysis_cache: dict[str, tuple[str, dict]] = field(default_factory=dict)
    started_at: float = field(default_factory=time.time)

    def spawn(self, coro) -> asyncio.Task:
        task = asyncio.create_task(coro)
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)
        return task

    async def recover(self) -> int:
        """Crash recovery: anything 'running' at startup was interrupted by process death.

        In-flight attempts may or may not have been processed by the provider, so they become
        ``unknown`` (never silently success/failure); their campaigns need reconciliation.
        """
        live = ("queued", "running", "grading", "analyzing")
        q = ",".join("?" * len(live))
        rows = await self.db.fetchall(f"SELECT id FROM campaigns WHERE status IN ({q})", live)
        if not rows:
            return 0
        now = time.time()
        async with self.db.tx() as conn:
            for r in rows:
                await conn.execute(
                    "UPDATE attempts SET status='unknown', error_category='unknown', "
                    "error_message='process stopped while attempt in flight', finished_at=? "
                    "WHERE status='running' AND trial_id IN (SELECT id FROM trials WHERE campaign_id=?)", (now, r["id"]))
                await conn.execute("UPDATE trials SET status='interrupted' WHERE campaign_id=? AND status='running'", (r["id"],))
                await conn.execute("UPDATE campaigns SET status='requires_reconciliation', stop_reason='process_restart' WHERE id=?",
                                   (r["id"],))
        for r in rows:
            await self.db.audit("campaign.reconcile_required", r["id"], {"reason": "process_restart"}, actor="system")
        return len(rows)
