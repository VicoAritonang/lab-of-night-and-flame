"""Token-bucket rate limiter (continuous refill)."""
from __future__ import annotations

import asyncio
import time


class TokenBucket:
    """Allows ``rate_per_min`` acquisitions per minute with bursts up to ``burst``.

    acquire() is O(1); waiters sleep exactly until the next token is due instead of polling.
    A 429 from the provider can push the bucket into a cool-down via ``penalize``.
    """

    def __init__(self, rate_per_min: float, burst: int | None = None, clock=time.monotonic):
        self.rate = max(rate_per_min, 0.001) / 60.0  # tokens per second
        self.capacity = float(burst if burst is not None else max(1, int(rate_per_min // 6) or 1))
        self.tokens = self.capacity
        self._clock = clock
        self._updated = clock()
        self._blocked_until = 0.0
        self._lock = asyncio.Lock()

    def _refill(self, now: float) -> None:
        self.tokens = min(self.capacity, self.tokens + (now - self._updated) * self.rate)
        self._updated = now

    async def acquire(self) -> float:
        """Wait for a token; returns seconds spent waiting (recorded as queue time)."""
        waited = 0.0
        async with self._lock:
            while True:
                now = self._clock()
                self._refill(now)
                block = self._blocked_until - now
                if block <= 0 and self.tokens >= 1:
                    self.tokens -= 1
                    return waited
                delay = max(block, (1 - self.tokens) / self.rate)
                waited += delay
                await asyncio.sleep(delay)

    def penalize(self, seconds: float) -> None:
        self._blocked_until = max(self._blocked_until, self._clock() + seconds)
        self.tokens = 0
