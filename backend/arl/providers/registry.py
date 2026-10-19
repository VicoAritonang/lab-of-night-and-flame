"""Provider registry + model catalog (capabilities and pricing)."""
from __future__ import annotations

import asyncio
import os
import time
from pathlib import Path
from typing import Any

import httpx
import orjson

from ..config import Settings
from ..engine.ratelimit import TokenBucket
from .base import Capabilities, TargetAdapter
from .fake import FAKE_MODELS, FakeAdapter
from .imported import ImportedAdapter
from .openai_compat import OpenAICompatAdapter, ProviderSpec

PER_M = 1_000_000

# Static price tables ($ per 1M tokens: input, output). Marked as "static_table" in the UI: may be stale.
OPENAI_PRICES: dict[str, tuple[float, float]] = {
    "gpt-4o-mini": (0.15, 0.60), "gpt-4o": (2.50, 10.0),
    "gpt-4.1": (2.0, 8.0), "gpt-4.1-mini": (0.40, 1.60), "gpt-4.1-nano": (0.10, 0.40),
    "gpt-5": (1.25, 10.0), "gpt-5-mini": (0.25, 2.0), "gpt-5-nano": (0.05, 0.40),
    "gpt-5.1": (1.25, 10.0), "o4-mini": (1.10, 4.40), "o3-mini": (1.10, 4.40), "o3": (2.0, 8.0),
    "gpt-3.5-turbo": (0.50, 1.50),
}
OPENAI_VISION = ("gpt-4o", "gpt-4.1", "gpt-5", "o3", "o4", "gpt-6")
GROQ_PRICES: dict[str, tuple[float, float]] = {
    "openai/gpt-oss-20b": (0.075, 0.30), "openai/gpt-oss-120b": (0.15, 0.60),
    "llama-3.1-8b-instant": (0.05, 0.08), "llama-3.3-70b-versatile": (0.59, 0.79),
}
NON_CHAT_HINTS = ("whisper", "tts", "transcribe", "embedding", "image", "dall-e", "moderation",
                  "realtime", "audio", "sora", "guard", "orpheus", "search", "davinci", "babbage", "instruct", "live")


def _strip_date(model: str) -> str:
    parts = model.split("-")
    if len(parts) >= 4 and parts[-3].isdigit() and len(parts[-3]) == 4:
        return "-".join(parts[:-3])
    return model


class Registry:
    CATALOG_TTL = 6 * 3600

    def __init__(self, settings: Settings):
        self.settings = settings
        self.client = httpx.AsyncClient(
            http2=False,
            limits=httpx.Limits(max_connections=64, max_keepalive_connections=32),
            headers={"User-Agent": f"agent-reliability-lab/{settings.version}"},
        )
        self.adapters: dict[str, TargetAdapter] = {}
        self.specs: dict[str, ProviderSpec] = {}
        self.buckets: dict[str, TokenBucket] = {}
        self._catalog: dict[str, list[dict[str, Any]]] = {}
        self._catalog_ts: dict[str, float] = {}
        self._catalog_lock = asyncio.Lock()
        self._model_index: dict[tuple[str, str], dict[str, Any]] = {}
        self.imported = ImportedAdapter()
        self.fake = FakeAdapter()
        self._cache_dir = settings.data_dir
        self._register_builtin()

    # ------------------------------------------------------------------ providers
    def _register_builtin(self) -> None:
        k = self.settings.provider_keys
        builtin = [
            ProviderSpec("openrouter", "https://openrouter.ai/api/v1", k.get("openrouter"), "OpenRouter",
                         extra_headers={"HTTP-Referer": "https://github.com/agent-reliability-lab", "X-Title": "Agent Reliability Lab"},
                         reports_cost=True, stream_usage_option=False, rpm=int(os.environ.get("ARL_OPENROUTER_RPM", "120"))),
            ProviderSpec("openai", "https://api.openai.com/v1", k.get("openai"), "OpenAI",
                         max_tokens_field="max_completion_tokens", rpm=int(os.environ.get("ARL_OPENAI_RPM", "300"))),
            ProviderSpec("groq", "https://api.groq.com/openai/v1", k.get("groq"), "Groq", rpm=int(os.environ.get("ARL_GROQ_RPM", "30"))),
            ProviderSpec("local", self.settings.local_base_url, None, "Local (OpenAI-compatible)",
                         requires_key=False, rpm=600, kind="local"),
        ]
        for spec in builtin:
            self.add_spec(spec)
        self.adapters["fake"] = self.fake
        self.buckets["fake"] = TokenBucket(100000, burst=1000)
        self.adapters["imported"] = self.imported
        self.buckets["imported"] = TokenBucket(100000, burst=1000)

    def add_spec(self, spec: ProviderSpec) -> None:
        self.specs[spec.name] = spec
        self.adapters[spec.name] = OpenAICompatAdapter(spec, self.client, self.price_per_token, self.capabilities_for)
        self.buckets[spec.name] = TokenBucket(spec.rpm, burst=max(2, spec.rpm // 10))

    def add_connection(self, row: dict[str, Any]) -> None:
        self.add_spec(ProviderSpec(
            name=row["name"], base_url=row["base_url"], api_key=self.settings.key_for(row.get("key_env")),
            label=row["label"], requires_key=bool(row.get("key_env")), rpm=int(row.get("rpm") or 60), kind="custom",
        ))

    def get(self, provider: str) -> TargetAdapter:
        try:
            return self.adapters[provider]
        except KeyError:
            raise KeyError(f"unknown provider '{provider}'") from None

    def provider_status(self) -> list[dict[str, Any]]:
        out = []
        for name, spec in self.specs.items():
            out.append({
                "name": name, "label": spec.label or name, "kind": spec.kind, "base_url": spec.base_url,
                "configured": bool(spec.api_key) or not spec.requires_key, "requires_key": spec.requires_key,
                "rpm": spec.rpm, "reports_cost": spec.reports_cost,
            })
        out.append({"name": "fake", "label": "Fake (runtime tests)", "kind": "fake", "base_url": None,
                    "configured": True, "requires_key": False, "rpm": None, "reports_cost": True})
        out.append({"name": "imported", "label": "Imported outputs", "kind": "imported", "base_url": None,
                    "configured": True, "requires_key": False, "rpm": None, "reports_cost": False})
        return out

    # ------------------------------------------------------------------ catalog
    def price_per_token(self, provider: str, model: str) -> tuple[float, float] | None:
        m = self._model_index.get((provider, model))
        if m and m.get("price_in") is not None and m.get("price_out") is not None:
            return m["price_in"] / PER_M, m["price_out"] / PER_M
        if provider in ("fake", "imported", "local"):
            return (0.0, 0.0)
        if model.endswith(":free"):
            return (0.0, 0.0)
        table = OPENAI_PRICES if provider == "openai" else GROQ_PRICES if provider == "groq" else {}
        p = table.get(model) or table.get(_strip_date(model))
        return (p[0] / PER_M, p[1] / PER_M) if p else None

    def capabilities_for(self, provider: str, model: str) -> Capabilities:
        m = self._model_index.get((provider, model))
        if m:
            return Capabilities(
                modalities=tuple(m.get("modalities") or ("text",)), streaming=True,
                structured_output=m.get("structured_output"), seed=m.get("seed"),
                usage=True, cost_reporting=provider == "openrouter", max_context=m.get("context_length"),
            )
        if provider == "openai":
            vision = model.startswith(OPENAI_VISION)
            return Capabilities(modalities=("text", "image") if vision else ("text",), streaming=True,
                                structured_output=True, seed=None, usage=True, cost_reporting=False)
        return Capabilities(streaming=True, usage=None)

    async def catalog(self, provider: str, refresh: bool = False) -> list[dict[str, Any]]:
        async with self._catalog_lock:
            fresh = time.time() - self._catalog_ts.get(provider, 0) < self.CATALOG_TTL
            if provider in self._catalog and fresh and not refresh:
                return self._catalog[provider]
            try:
                models = await self._fetch_catalog(provider, refresh)
            except Exception:
                models = self._catalog.get(provider, [])
            self._catalog[provider] = models
            self._catalog_ts[provider] = time.time()
            for m in models:
                self._model_index[(provider, m["id"])] = m
            return models

    async def full_catalog(self, refresh: bool = False) -> list[dict[str, Any]]:
        names = list(self.specs) + ["fake"]
        results = await asyncio.gather(*(self.catalog(n, refresh) for n in names), return_exceptions=True)
        out: list[dict[str, Any]] = []
        for r in results:
            if isinstance(r, list):
                out.extend(r)
        return out

    def _cache_path(self, provider: str) -> Path:
        return self._cache_dir / f"catalog_{provider}.json"

    async def _fetch_catalog(self, provider: str, refresh: bool) -> list[dict[str, Any]]:
        if provider == "fake":
            return [{"provider": "fake", "id": m["id"], "name": m["name"], "description": m["description"],
                     "modalities": ["text", "image"], "context_length": None, "price_in": 0.0, "price_out": 0.0,
                     "price_source": "free", "structured_output": True, "seed": True, "fake": True} for m in FAKE_MODELS]
        cache = self._cache_path(provider)
        if not refresh and cache.is_file() and time.time() - cache.stat().st_mtime < self.CATALOG_TTL:
            return orjson.loads(cache.read_bytes())
        spec = self.specs.get(provider)
        if spec is None:
            return []
        if spec.requires_key and not spec.api_key and provider != "openrouter":
            return []
        headers = {"Authorization": f"Bearer {spec.api_key}"} if spec.api_key else {}
        resp = await self.client.get(spec.base_url.rstrip("/") + "/models", headers=headers, timeout=15)
        resp.raise_for_status()
        raw = resp.json().get("data", [])
        if provider == "openrouter":
            models = [self._norm_openrouter(m) for m in raw]
        else:
            models = [self._norm_generic(provider, m) for m in raw]
            models = [m for m in models if m is not None]
        models.sort(key=lambda m: m["id"])
        if spec.kind != "local":
            cache.write_bytes(orjson.dumps(models))
        return models

    @staticmethod
    def _norm_openrouter(m: dict[str, Any]) -> dict[str, Any]:
        pricing = m.get("pricing") or {}

        def per_m(v: Any) -> float | None:
            try:
                f = float(v)
            except (TypeError, ValueError):
                return None
            return None if f < 0 else round(f * PER_M, 6)

        arch = m.get("architecture") or {}
        params = set(m.get("supported_parameters") or [])
        return {
            "provider": "openrouter", "id": m["id"], "name": m.get("name") or m["id"],
            "description": (m.get("description") or "")[:280],
            "modalities": arch.get("input_modalities") or ["text"],
            "output_modalities": arch.get("output_modalities") or ["text"],
            "context_length": m.get("context_length"),
            "price_in": per_m(pricing.get("prompt")), "price_out": per_m(pricing.get("completion")),
            "price_source": "provider_catalog",
            "structured_output": ("response_format" in params) or None,
            "seed": ("seed" in params) or None,
            "reasoning": "reasoning" in params,
            "created": m.get("created"),
            "free": m["id"].endswith(":free"),
        }

    def _norm_generic(self, provider: str, m: dict[str, Any]) -> dict[str, Any] | None:
        mid = m.get("id", "")
        if any(h in mid.lower() for h in NON_CHAT_HINTS):
            return None
        if provider == "openai" and not (mid.startswith(("gpt-", "o1", "o3", "o4", "chatgpt"))):
            return None
        price = self.price_per_token(provider, mid)
        caps = self.capabilities_for(provider, mid)
        modalities = list(caps.modalities)
        if provider == "groq" and ("scout" in mid or "maverick" in mid or "vision" in mid):
            modalities = ["text", "image"]
        return {
            "provider": provider, "id": mid, "name": mid, "description": "",
            "modalities": modalities, "context_length": m.get("context_window") or m.get("context_length"),
            "price_in": price[0] * PER_M if price else None, "price_out": price[1] * PER_M if price else None,
            "price_source": ("static_table" if price else "unknown") if provider not in ("local",) else "local",
            "structured_output": caps.structured_output, "seed": caps.seed, "created": m.get("created"),
            "free": False,
        }

    async def close(self) -> None:
        await self.client.aclose()
