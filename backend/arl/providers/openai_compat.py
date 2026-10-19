"""One adapter for every OpenAI-compatible Chat Completions endpoint.

OpenRouter, OpenAI, Groq, Ollama, vLLM and LM Studio speak the same wire protocol, so a single
well-tested path (streaming, usage capture, error taxonomy) serves them all. Provider quirks are
expressed as data in ``ProviderSpec`` rather than subclasses.
"""
from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field
from typing import Any, Callable

import httpx
import orjson

from .base import (
    AdapterError, Capabilities, GenerationRequest, GenerationResult, Message, redact,
)

PriceFn = Callable[[str, str], "tuple[float, float] | None"]  # (provider, model) -> ($/token in, $/token out)
CapsFn = Callable[[str, str], Capabilities]

_REASONING_PREFIXES = ("gpt-5", "o1", "o3", "o4")


@dataclass
class ProviderSpec:
    name: str
    base_url: str
    api_key: str | None = field(default=None, repr=False)
    label: str = ""
    extra_headers: dict[str, str] = field(default_factory=dict)
    max_tokens_field: str = "max_tokens"
    stream_usage_option: bool = True       # send stream_options.include_usage
    reports_cost: bool = False              # usage.cost present in responses (OpenRouter)
    requires_key: bool = True
    rpm: int = 60
    kind: str = "cloud"                     # cloud | local


def _messages_payload(messages: list[Message]) -> list[dict[str, Any]]:
    out = []
    for m in messages:
        if len(m.content) == 1 and m.content[0].type == "text":
            out.append({"role": m.role, "content": m.content[0].text or ""})
            continue
        parts = []
        for p in m.content:
            if p.type == "text":
                parts.append({"type": "text", "text": p.text or ""})
            elif p.type == "image":
                parts.append({"type": "image_url", "image_url": {"url": p.data_uri}})
        out.append({"role": m.role, "content": parts})
    return out


def _status_to_category(status: int) -> str:
    if status == 429:
        return "rate_limited"
    if status in (401, 403):
        return "auth"
    if status in (400, 404, 413, 422):
        return "bad_request"
    if status == 408:
        return "timeout"
    if status >= 500:
        return "provider_error"
    return "unknown"


def _error_message(body: bytes) -> str:
    try:
        data = orjson.loads(body)
        err = data.get("error", data)
        if isinstance(err, dict):
            msg = err.get("message") or str(err)
            meta = err.get("metadata") or {}
            raw = meta.get("raw") if isinstance(meta, dict) else None
            return f"{msg} {raw}" if raw else msg
        return str(err)
    except Exception:
        return body[:500].decode("utf-8", "replace")


class OpenAICompatAdapter:
    def __init__(self, spec: ProviderSpec, client: httpx.AsyncClient,
                 price_fn: PriceFn | None = None, caps_fn: CapsFn | None = None):
        self.spec = spec
        self.name = spec.name
        self._client = client
        self._price_fn = price_fn
        self._caps_fn = caps_fn

    def capabilities(self, model: str) -> Capabilities:
        if self._caps_fn:
            return self._caps_fn(self.name, model)
        return Capabilities(streaming=True, usage=True, cost_reporting=self.spec.reports_cost)

    def build_payload(self, req: GenerationRequest) -> dict[str, Any]:
        params = dict(req.params)
        payload: dict[str, Any] = {"model": req.model, "messages": _messages_payload(req.messages)}
        is_reasoning_openai = self.name == "openai" and req.model.startswith(_REASONING_PREFIXES)
        max_tokens = params.pop("max_tokens", None)
        if max_tokens:
            payload[self.spec.max_tokens_field] = int(max_tokens)
        for key in ("temperature", "top_p"):
            val = params.pop(key, None)
            if val is not None and not is_reasoning_openai:
                payload[key] = val
        seed = params.pop("seed", None)
        if seed is not None:
            payload["seed"] = int(seed)
        if params.pop("json_mode", False):
            payload["response_format"] = {"type": "json_object"}
        effort = params.pop("reasoning_effort", None)
        if effort:
            if self.name == "openrouter":
                payload["reasoning"] = {"effort": effort}
            else:
                payload["reasoning_effort"] = effort
        stop = params.pop("stop", None)
        if stop:
            payload["stop"] = stop
        if req.stream:
            payload["stream"] = True
            if self.spec.stream_usage_option:
                payload["stream_options"] = {"include_usage": True}
        if self.spec.reports_cost:
            payload["usage"] = {"include": True}
        return payload

    def _headers(self) -> dict[str, str]:
        h = {"Content-Type": "application/json", **self.spec.extra_headers}
        if self.spec.api_key:
            h["Authorization"] = f"Bearer {self.spec.api_key}"
        return h

    async def generate(self, req: GenerationRequest, cancel: asyncio.Event | None = None) -> GenerationResult:
        if self.spec.requires_key and not self.spec.api_key:
            raise AdapterError("auth", f"no API key configured for provider '{self.name}'")
        payload = self.build_payload(req)
        url = self.spec.base_url.rstrip("/") + "/chat/completions"
        started = time.perf_counter()
        try:
            if req.stream:
                result = await self._stream(url, payload, req, started, cancel)
            else:
                result = await self._once(url, payload, req, started)
        except httpx.ConnectError as e:
            raise AdapterError("network", f"connect failed: {e}") from e
        except httpx.TimeoutException as e:
            # Request bytes were sent; the provider may have processed (and billed) it.
            raise AdapterError("timeout", f"timeout after {req.timeout_s}s", maybe_processed=True) from e
        except httpx.HTTPError as e:
            raise AdapterError("network", f"transport error: {type(e).__name__}: {e}", maybe_processed=True) from e
        self._apply_cost(result)
        return result

    def _apply_cost(self, r: GenerationResult) -> None:
        if r.cost_usd is not None:
            return
        price = self._price_fn(self.name, r.requested_model) if self._price_fn else None
        if price is None or r.tokens_in is None or r.tokens_out is None:
            return
        pin, pout = price
        r.cost_usd = r.tokens_in * pin + r.tokens_out * pout
        r.cost_source = "free" if pin == 0 and pout == 0 else "price_table"

    def _raise_for_status(self, resp: httpx.Response, body: bytes) -> None:
        if resp.status_code < 400:
            return
        retry_after = None
        ra = resp.headers.get("retry-after")
        if ra:
            try:
                retry_after = float(ra)
            except ValueError:
                pass
        cat = _status_to_category(resp.status_code)
        raise AdapterError(cat, f"HTTP {resp.status_code}: {_error_message(body)}",
                           status=resp.status_code, retry_after=retry_after)

    def _fill_usage(self, r: GenerationResult, usage: dict[str, Any] | None) -> None:
        if not usage:
            return
        r.raw_usage = {k: v for k, v in usage.items() if isinstance(v, (int, float, str, dict))}
        r.tokens_in = usage.get("prompt_tokens", r.tokens_in)
        r.tokens_out = usage.get("completion_tokens", r.tokens_out)
        details = usage.get("completion_tokens_details") or {}
        if isinstance(details, dict) and details.get("reasoning_tokens") is not None:
            r.reasoning_tokens = details.get("reasoning_tokens")
        if self.spec.reports_cost and usage.get("cost") is not None:
            r.cost_usd = float(usage["cost"])
            r.cost_source = "provider"

    async def _once(self, url: str, payload: dict, req: GenerationRequest, started: float) -> GenerationResult:
        resp = await self._client.post(url, content=orjson.dumps(payload), headers=self._headers(), timeout=req.timeout_s)
        body = resp.content
        self._raise_for_status(resp, body)
        data = orjson.loads(body)
        if "error" in data:
            raise AdapterError("provider_error", _error_message(body), maybe_processed=True)
        choice = (data.get("choices") or [{}])[0]
        msg = choice.get("message") or {}
        r = GenerationResult(
            text=msg.get("content") or "",
            provider=self.name,
            requested_model=req.model,
            resolved_model=data.get("model"),
            upstream_provider=data.get("provider"),
            request_id=data.get("id") or resp.headers.get("x-request-id"),
            finish_reason=choice.get("finish_reason"),
            latency_ms=(time.perf_counter() - started) * 1000,
        )
        self._fill_usage(r, data.get("usage"))
        return r

    async def _stream(self, url: str, payload: dict, req: GenerationRequest, started: float,
                      cancel: asyncio.Event | None) -> GenerationResult:
        r = GenerationResult(text="", provider=self.name, requested_model=req.model)
        chunks: list[str] = []
        timeout = httpx.Timeout(req.timeout_s, connect=15.0)
        async with self._client.stream("POST", url, content=orjson.dumps(payload),
                                       headers=self._headers(), timeout=timeout) as resp:
            if resp.status_code >= 400:
                body = await resp.aread()
                self._raise_for_status(resp, body)
            r.request_id = resp.headers.get("x-request-id") or resp.headers.get("x-generation-id")
            got_done = False
            async for line in resp.aiter_lines():
                if cancel is not None and cancel.is_set():
                    raise AdapterError("cancelled", "cancelled during stream", maybe_processed=True)
                if not line or line.startswith(":"):
                    continue  # SSE comment / keep-alive
                if not line.startswith("data:"):
                    continue
                data_str = line[5:].strip()
                if data_str == "[DONE]":
                    got_done = True
                    break
                try:
                    data = orjson.loads(data_str)
                except orjson.JSONDecodeError:
                    continue
                if "error" in data and data["error"]:
                    raise AdapterError("provider_error", _error_message(orjson.dumps(data)), maybe_processed=True)
                r.request_id = r.request_id or data.get("id")
                r.resolved_model = data.get("model") or r.resolved_model
                r.upstream_provider = data.get("provider") or r.upstream_provider
                for choice in data.get("choices") or []:
                    delta = choice.get("delta") or {}
                    piece = delta.get("content")
                    if (piece or delta.get("reasoning") or delta.get("reasoning_content")) and r.ttft_ms is None:
                        r.ttft_ms = (time.perf_counter() - started) * 1000
                    if piece:
                        chunks.append(piece)
                        if req.on_delta is not None:
                            req.on_delta(piece)
                    if choice.get("finish_reason"):
                        r.finish_reason = choice["finish_reason"]
                usage = data.get("usage") or (data.get("x_groq") or {}).get("usage")
                if usage:
                    self._fill_usage(r, usage)
            if not got_done and r.finish_reason is None:
                raise AdapterError("provider_error", "stream ended without completion", maybe_processed=True)
        r.text = "".join(chunks)
        r.latency_ms = (time.perf_counter() - started) * 1000
        return r


def safe_error(e: BaseException) -> str:
    return redact(f"{type(e).__name__}: {e}") or ""
