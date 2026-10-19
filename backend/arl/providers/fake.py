"""Deterministic fake target for runtime tests (FR-003). Never makes network calls; cost is 0.

Results from this adapter test the *Lab* (scheduling, retries, budget, scoring, reports), not model
intelligence; the UI and reports label them as FAKE.

Models:
  fake/echo            echoes the user text
  fake/oracle-<pct>    returns the reference answer with probability pct/100 (seeded per sample),
                       otherwise a plausible wrong answer. The reference is handed over through an
                       explicit, fake-only channel so real adapters can never see expectations.
  fake/flaky-<pct>     like oracle-80 but the first attempt fails with a retryable error pct% of the time
  fake/slow            like oracle-70 with 400-1200 ms latency
"""
from __future__ import annotations

import asyncio
import hashlib
import json
import re
import time
from collections import defaultdict
from typing import Any

from .base import AdapterError, Capabilities, GenerationRequest, GenerationResult

FAKE_MODELS = [
    {"id": "fake/oracle-90", "name": "Fake oracle 90%", "description": "Returns the reference 90% of the time"},
    {"id": "fake/oracle-70", "name": "Fake oracle 70%", "description": "Returns the reference 70% of the time"},
    {"id": "fake/oracle-50", "name": "Fake oracle 50%", "description": "Returns the reference 50% of the time"},
    {"id": "fake/flaky-30", "name": "Fake flaky", "description": "30% of first attempts fail with a retryable error"},
    {"id": "fake/slow", "name": "Fake slow", "description": "400-1200 ms latency, 70% accuracy"},
    {"id": "fake/router", "name": "Fake router", "description": "Routes hard-looking prompts to fake/large (92%), the rest to fake/small (65%)"},
    {"id": "fake/echo", "name": "Fake echo", "description": "Echoes the prompt (always wrong for most tasks)"},
]


def _unit(*parts: Any) -> float:
    h = hashlib.sha256("|".join(map(str, parts)).encode()).digest()
    return int.from_bytes(h[:8], "big") / 2**64


def _wrong(ref: Any, salt: float) -> Any:
    if isinstance(ref, bool):
        return not ref
    if isinstance(ref, (int, float)):
        return ref + (1 if salt < 0.5 else -2)
    if isinstance(ref, dict) and ref:
        out = dict(ref)
        k = sorted(out)[int(salt * len(out)) % len(out)]
        out[k] = _wrong(out[k], 1 - salt)
        return out
    if isinstance(ref, list) and ref:
        return ref[:-1]
    if isinstance(ref, str):
        if re.fullmatch(r"[A-Ea-e]", ref.strip()):
            letters = "ABCD"
            return letters[(letters.find(ref.strip().upper()) + 1) % 4]
        return "I am not sure." if salt < 0.5 else ref[::-1]
    return None


def _render(value: Any) -> str:
    if isinstance(value, str):
        return value
    return json.dumps(value)


class FakeAdapter:
    name = "fake"

    def __init__(self) -> None:
        self._calls: dict[str, int] = defaultdict(int)

    def capabilities(self, model: str) -> Capabilities:
        return Capabilities(modalities=("text", "image"), streaming=True, structured_output=True,
                            seed=True, usage=True, cost_reporting=True)

    async def generate(self, req: GenerationRequest, cancel: asyncio.Event | None = None) -> GenerationResult:
        started = time.perf_counter()
        model = req.model.removeprefix("fake/")
        seed = req.params.get("seed", 0)
        key = req.sample_key or ""
        call_key = f"{model}|{key}|{seed}"
        self._calls[call_key] += 1
        n_call = self._calls[call_key]

        u_latency = _unit("lat", call_key, n_call)
        base_ms = 400 + 800 * u_latency if model == "slow" else 15 + 90 * u_latency
        await asyncio.sleep(base_ms / 1000)
        if cancel is not None and cancel.is_set():
            raise AdapterError("cancelled", "cancelled")

        if model.startswith("flaky"):
            pct = int(model.split("-")[1]) if "-" in model else 30
            if n_call == 1 and _unit("flaky", call_key) < pct / 100:
                if _unit("kind", call_key) < 0.7:
                    raise AdapterError("rate_limited", "HTTP 429: simulated rate limit", status=429, retry_after=0.05)
                raise AdapterError("timeout", "simulated timeout", maybe_processed=True)
            accuracy = 0.8
        elif model.startswith("oracle"):
            accuracy = int(model.split("-")[1]) / 100
        elif model == "slow":
            accuracy = 0.7
        else:
            accuracy = None

        user_text = " ".join(p.text or "" for m in req.messages if m.role == "user" for p in m.content if p.type == "text")
        resolved = req.model
        if model == "router":
            # simulated router: "hard" prompts usually go to the large model; easy ones to the cheap one
            hard = "think carefully" in user_text.lower() or "json" in user_text.lower()
            go_large = _unit("route", key, seed) < (0.85 if hard else 0.15)
            resolved = "fake/large" if go_large else "fake/small"
            accuracy = 0.92 if go_large else 0.65
        if accuracy is None or req.fake_reference is None:
            text = user_text[:400]
        else:
            u = _unit("acc", resolved, key, seed)
            text = _render(req.fake_reference if u < accuracy else _wrong(req.fake_reference, _unit("w", key)))

        if req.on_delta is not None:
            req.on_delta(text)
        elapsed = (time.perf_counter() - started) * 1000
        return GenerationResult(
            text=text, provider="fake", requested_model=req.model, resolved_model=resolved,
            upstream_provider="fake-lab" if model == "router" else None,
            request_id=f"fake-{hashlib.md5(call_key.encode()).hexdigest()[:10]}-{n_call}",
            finish_reason="stop", tokens_in=max(1, len(user_text) // 4), tokens_out=max(1, len(text) // 4),
            cost_usd=0.0, cost_source="free", latency_ms=elapsed, ttft_ms=elapsed * 0.35,
        )
