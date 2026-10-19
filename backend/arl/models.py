"""Typed contracts for the experiment manifest (schema arl-experiment-v1)."""
from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

SCHEMA_VERSION = "arl-experiment-v1"


class Strict(BaseModel):
    model_config = ConfigDict(extra="forbid")


class GenParams(Strict):
    temperature: float | None = Field(default=0.0, ge=0, le=2)
    top_p: float | None = Field(default=None, gt=0, le=1)
    max_tokens: int = Field(default=512, ge=1, le=32768)
    seed: int | None = None
    json_mode: bool = False
    reasoning_effort: Literal["minimal", "low", "medium", "high"] | None = None


class TargetSpec(Strict):
    provider: str
    model: str
    label: str | None = None
    params: GenParams = Field(default_factory=GenParams)


class PromptVariant(Strict):
    id: str = Field(default="default", pattern=r"^[A-Za-z0-9_\-.]{1,40}$")
    system: str = "{instructions}"
    user: str = "{input}"


class ScorerSpec(Strict):
    id: str = Field(pattern=r"^[A-Za-z0-9_\-.]{1,40}$")
    key: str
    params: dict[str, Any] = Field(default_factory=dict)


class Protocol(Strict):
    repetitions: int = Field(default=1, ge=1, le=20)
    order: Literal["interleaved", "blocked"] = "interleaved"
    seed: int = 20261011
    concurrency: int = Field(default=6, ge=1, le=64)
    timeout_s: float = Field(default=120, ge=5, le=900)
    max_retries: int = Field(default=2, ge=0, le=6)
    splits: list[str] = Field(default_factory=lambda: ["test"])
    sample_limit: int | None = Field(default=None, ge=1)
    stream: bool = True


class Budget(Strict):
    max_usd: float | None = Field(default=1.0, ge=0)
    paid_run_confirmed: bool = False


class Acceptance(Strict):
    min_pass_rate: float | None = Field(default=None, ge=0, le=1)
    baseline_arm: str | None = None          # arm label or key; None => first arm
    non_inferiority_margin: float | None = Field(default=None, ge=0, le=1)
    min_assessed_per_arm: int = Field(default=20, ge=1)
    max_p95_latency_ms: float | None = Field(default=None, gt=0)
    max_cost_per_pass_usd: float | None = Field(default=None, gt=0)
    max_error_rate: float = Field(default=0.1, ge=0, le=1)


class ExperimentPlan(Strict):
    schema_version: Literal["arl-experiment-v1"] = SCHEMA_VERSION
    dataset_id: str
    targets: list[TargetSpec] = Field(min_length=1, max_length=24)
    prompts: list[PromptVariant] = Field(default_factory=lambda: [PromptVariant()], min_length=1, max_length=8)
    params_grid: dict[str, list[Any]] = Field(default_factory=dict)
    scorers: list[ScorerSpec] = Field(min_length=1, max_length=12)
    primary_scorer: str
    protocol: Protocol = Field(default_factory=Protocol)
    budget: Budget = Field(default_factory=Budget)
    acceptance: Acceptance = Field(default_factory=Acceptance)

    @field_validator("params_grid")
    @classmethod
    def _grid(cls, v: dict[str, list[Any]]) -> dict[str, list[Any]]:
        allowed = {"temperature", "top_p", "max_tokens", "reasoning_effort"}
        for k, vals in v.items():
            if k not in allowed:
                raise ValueError(f"params_grid key '{k}' not sweepable (allowed: {sorted(allowed)})")
            if not vals or len(vals) > 8:
                raise ValueError(f"params_grid['{k}'] must have 1-8 values")
        return v

    @model_validator(mode="after")
    def _check(self) -> "ExperimentPlan":
        ids = [s.id for s in self.scorers]
        if len(set(ids)) != len(ids):
            raise ValueError("scorer ids must be unique")
        if self.primary_scorer not in ids:
            raise ValueError("primary_scorer must reference a scorer id")
        pids = [p.id for p in self.prompts]
        if len(set(pids)) != len(pids):
            raise ValueError("prompt variant ids must be unique")
        return self


class ExperimentCreate(Strict):
    name: str = Field(min_length=1, max_length=120)
    hypothesis: str = Field(default="", max_length=2000)
    plan: ExperimentPlan


class PlaygroundTarget(Strict):
    provider: str
    model: str
    params: GenParams = Field(default_factory=GenParams)


class PlaygroundRequest(Strict):
    system: str = ""
    prompt: str = Field(default="", max_length=100_000)
    images: list[str] = Field(default_factory=list, max_length=4)  # artifact digests
    targets: list[PlaygroundTarget] = Field(min_length=1, max_length=8)


class ConnectionCreate(Strict):
    name: str = Field(pattern=r"^[a-z][a-z0-9_\-]{1,30}$")
    label: str = Field(min_length=1, max_length=60)
    base_url: str = Field(pattern=r"^https?://")
    key_env: str | None = Field(default=None, pattern=r"^[A-Z][A-Z0-9_]{1,60}$")
    rpm: int = Field(default=60, ge=1, le=10000)


class RescoreRequest(Strict):
    scorers: list[ScorerSpec] = Field(min_length=1, max_length=12)
    primary_scorer: str | None = None


class ResumeRequest(Strict):
    budget_max_usd: float | None = Field(default=None, ge=0)
