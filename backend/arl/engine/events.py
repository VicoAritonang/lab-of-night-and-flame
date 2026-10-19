"""In-process pub/sub for live campaign updates with event coalescing.

Trial updates are buffered and flushed every ``interval`` seconds as one batch, so a campaign doing
hundreds of trials/second produces a handful of SSE frames instead of thousands.
Subscriber queues are bounded; a slow client drops frames (it can resync from the snapshot endpoint)
instead of growing server memory.
"""
from __future__ import annotations

import asyncio
import time
from collections import defaultdict
from typing import Any


class EventBus:
    def __init__(self, interval: float = 0.25, queue_size: int = 256):
        self.interval = interval
        self.queue_size = queue_size
        self._subs: dict[str, set[asyncio.Queue]] = defaultdict(set)
        self._pending: dict[str, dict[str, dict[str, Any]]] = defaultdict(dict)
        self._state: dict[str, dict[str, Any]] = {}
        self._flusher: asyncio.Task | None = None

    def start(self) -> None:
        if self._flusher is None:
            self._flusher = asyncio.create_task(self._flush_loop())

    async def stop(self) -> None:
        if self._flusher:
            self._flusher.cancel()
            try:
                await self._flusher
            except asyncio.CancelledError:
                pass
            self._flusher = None

    def subscribe(self, channel: str) -> asyncio.Queue:
        q: asyncio.Queue = asyncio.Queue(maxsize=self.queue_size)
        self._subs[channel].add(q)
        return q

    def unsubscribe(self, channel: str, q: asyncio.Queue) -> None:
        self._subs[channel].discard(q)
        if not self._subs[channel]:
            self._subs.pop(channel, None)

    def has_subscribers(self, channel: str) -> bool:
        return bool(self._subs.get(channel))

    def _deliver(self, channel: str, event: dict[str, Any]) -> None:
        for q in list(self._subs.get(channel, ())):
            try:
                q.put_nowait(event)
            except asyncio.QueueFull:
                pass  # slow consumer: drop; client resyncs from snapshot

    def publish(self, channel: str, event: dict[str, Any]) -> None:
        """Immediate delivery (state changes, logs)."""
        event.setdefault("ts", time.time())
        self._deliver(channel, event)

    def trial_update(self, channel: str, trial: dict[str, Any]) -> None:
        """Coalesced delivery: last write per trial id wins inside a flush window."""
        self._pending[channel][trial["id"]] = trial

    def set_state(self, channel: str, state: dict[str, Any]) -> None:
        self._state[channel] = state

    async def _flush_loop(self) -> None:
        while True:
            await asyncio.sleep(self.interval)
            self.flush()

    def flush(self) -> None:
        for channel in list(self._pending):
            trials = self._pending.pop(channel)
            if not trials:
                continue
            event = {"type": "trials", "trials": list(trials.values()), "ts": time.time()}
            if channel in self._state:
                event["state"] = self._state[channel]
            self._deliver(channel, event)
