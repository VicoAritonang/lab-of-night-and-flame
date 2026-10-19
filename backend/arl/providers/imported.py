"""Imported-output target (FR-005): evaluates stored predictions without regeneration.

Latency/TTFT are reported as unknown: imported outputs prove nothing about inference performance.
"""
from __future__ import annotations

import asyncio

from .base import AdapterError, Capabilities, GenerationRequest, GenerationResult


class ImportedAdapter:
    name = "imported"

    def __init__(self) -> None:
        self._outputs: dict[tuple[str, str], str] = {}

    def load(self, import_name: str, outputs: dict[str, str]) -> None:
        for key, text in outputs.items():
            self._outputs[(import_name, key)] = text

    def capabilities(self, model: str) -> Capabilities:
        return Capabilities(modalities=("text", "image"), streaming=False, usage=False, cost_reporting=False)

    async def generate(self, req: GenerationRequest, cancel: asyncio.Event | None = None) -> GenerationResult:
        key = (req.sample_key or "").split("#r")[0]
        text = self._outputs.get((req.model, key))
        if text is None:
            raise AdapterError("unsupported", f"no imported prediction for sample '{key}' in '{req.model}'")
        return GenerationResult(text=text, provider="imported", requested_model=req.model,
                                resolved_model=req.model, finish_reason="imported", cost_source=None)
