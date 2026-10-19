"""Target adapter contract (product.md §10)."""
from __future__ import annotations

import asyncio
import re
from dataclasses import dataclass, field
from typing import Any, Callable, Protocol

# Error categories. Infrastructure failures are kept separate from model errors.
RETRYABLE = frozenset({"rate_limited", "timeout", "provider_error", "network"})
ERROR_CATEGORIES = (
    "rate_limited", "timeout", "provider_error", "auth", "bad_request",
    "content_filter", "network", "cancelled", "unknown", "unsupported",
)

_SECRET_PATTERNS = [
    re.compile(r"sk-[A-Za-z0-9_\-]{8,}"),
    re.compile(r"gsk_[A-Za-z0-9]{8,}"),
    re.compile(r"(?i)bearer\s+[A-Za-z0-9._\-]{8,}"),
    re.compile(r"(?i)(api[_-]?key[\"'=:\s]+)[A-Za-z0-9._\-]{8,}"),
]


def redact(text: str | None) -> str | None:
    """Strip credential-shaped substrings before anything is stored or logged."""
    if not text:
        return text
    out = text
    for pat in _SECRET_PATTERNS:
        out = pat.sub(lambda m: (m.group(1) if m.lastindex else "") + "[REDACTED]", out)
    return out


@dataclass(frozen=True)
class Capabilities:
    modalities: tuple[str, ...] = ("text",)
    streaming: bool | None = None
    structured_output: bool | None = None
    seed: bool | None = None
    usage: bool | None = None
    cost_reporting: bool | None = None
    max_context: int | None = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "modalities": list(self.modalities),
            "streaming": self.streaming,
            "structured_output": self.structured_output,
            "seed": self.seed,
            "usage": self.usage,
            "cost_reporting": self.cost_reporting,
            "max_context": self.max_context,
        }


@dataclass
class ContentPart:
    type: str  # "text" | "image"
    text: str | None = None
    data_uri: str | None = None  # images are passed inline; originals stay in the artifact store


@dataclass
class Message:
    role: str
    content: list[ContentPart]


@dataclass
class GenerationRequest:
    model: str
    messages: list[Message]
    params: dict[str, Any] = field(default_factory=dict)  # temperature, top_p, max_tokens, seed, json_mode
    timeout_s: float = 120.0
    stream: bool = True
    # Only used by the explicitly-labelled fake oracle target; never populated for real providers.
    fake_reference: Any = None
    sample_key: str | None = None
    on_delta: Callable[[str], None] | None = None  # streaming hook (playground)


@dataclass
class GenerationResult:
    text: str
    provider: str
    requested_model: str
    resolved_model: str | None = None
    upstream_provider: str | None = None  # routers/aggregators: who actually served the call
    request_id: str | None = None
    finish_reason: str | None = None
    tokens_in: int | None = None
    tokens_out: int | None = None
    reasoning_tokens: int | None = None
    cost_usd: float | None = None
    cost_source: str | None = None  # "provider" | "price_table" | "free" | None (unknown)
    latency_ms: float | None = None
    ttft_ms: float | None = None
    raw_usage: dict[str, Any] | None = None


class AdapterError(Exception):
    def __init__(self, category: str, message: str, *, status: int | None = None,
                 retry_after: float | None = None, maybe_processed: bool = False):
        super().__init__(redact(message))
        self.category = category
        self.status = status
        self.retry_after = retry_after
        # True when the provider may have processed the request (no receipt) -> attempt status "unknown"
        self.maybe_processed = maybe_processed

    @property
    def retryable(self) -> bool:
        return self.category in RETRYABLE


class TargetAdapter(Protocol):
    name: str

    def capabilities(self, model: str) -> Capabilities: ...

    async def generate(self, req: GenerationRequest, cancel: asyncio.Event | None = None) -> GenerationResult: ...
