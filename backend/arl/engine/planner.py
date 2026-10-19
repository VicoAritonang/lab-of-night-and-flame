"""Experiment planner: factor expansion, validation, preflight estimate (product.md §8)."""
from __future__ import annotations

import hashlib
import itertools
import json
import random
from dataclasses import dataclass, field
from typing import Any

from ..db import Database, loads
from ..models import ExperimentPlan
from ..providers.registry import Registry
from ..scorers.registry import is_judge, validate_spec

IMAGE_TOKEN_ESTIMATE = 800          # conservative per-image prompt token estimate
CHARS_PER_TOKEN = 3.5               # conservative (overestimates tokens)
FALLBACK_PRICE = (15 / 1e6, 60 / 1e6)  # used for reservations when price is unknown (exposure cap)


def canonical_digest(obj: Any) -> str:
    return hashlib.sha256(json.dumps(obj, sort_keys=True, separators=(",", ":"), default=str).encode()).hexdigest()


@dataclass
class Arm:
    key: str
    label: str
    provider: str
    model: str
    params: dict[str, Any]
    prompt_id: str
    system: str
    user: str
    target_index: int

    def as_dict(self) -> dict[str, Any]:
        return {"key": self.key, "label": self.label, "provider": self.provider, "model": self.model,
                "params": self.params, "prompt_id": self.prompt_id, "system": self.system, "user": self.user,
                "target_index": self.target_index}


def expand_arms(plan: ExperimentPlan) -> list[Arm]:
    """Cartesian product targets × prompts × params_grid. Arm key = hash of everything sent to the model."""
    grid_keys = sorted(plan.params_grid)
    combos = list(itertools.product(*(plan.params_grid[k] for k in grid_keys))) or [()]
    arms: list[Arm] = []
    multi_prompt = len(plan.prompts) > 1
    for ti, t in enumerate(plan.targets):
        for p in plan.prompts:
            for combo in combos:
                params = t.params.model_dump(exclude_none=True)
                params.update(dict(zip(grid_keys, combo)))
                ident = {"provider": t.provider, "model": t.model, "params": params, "system": p.system, "user": p.user}
                key = canonical_digest(ident)[:12]
                parts = [t.label or (t.model if t.model.startswith(t.provider + "/") else f"{t.provider}/{t.model}")]
                if multi_prompt:
                    parts.append(f"prompt:{p.id}")
                parts += [f"{k}={v}" for k, v in zip(grid_keys, combo)]
                arms.append(Arm(key, " · ".join(parts), t.provider, t.model, params, p.id, p.system, p.user, ti))
    seen: set[str] = set()
    unique = []
    for a in arms:
        if a.key not in seen:
            seen.add(a.key)
            unique.append(a)
    return unique


def schedule(arm_keys: list[str], sample_ids: list[str], repetitions: int, order: str, seed: int) -> list[tuple[str, str, int]]:
    """Return trial tuples (arm, sample, rep) in run order.

    interleaved: one Fisher-Yates shuffle over every trial (removes time-of-day/provider-drift confounds).
    blocked: samples shuffled, then all arms run back-to-back for each sample×rep (paired in time).
    """
    rng = random.Random(seed)
    if order == "interleaved":
        trials = [(a, s, r) for r in range(repetitions) for s in sample_ids for a in arm_keys]
        rng.shuffle(trials)
        return trials
    samples = list(sample_ids)
    rng.shuffle(samples)
    out = []
    for r in range(repetitions):
        for s in samples:
            arms = list(arm_keys)
            rng.shuffle(arms)
            out += [(a, s, r) for a in arms]
    return out


def estimate_tokens(text: str, n_images: int) -> int:
    return int(len(text) / CHARS_PER_TOKEN) + 8 + n_images * IMAGE_TOKEN_ESTIMATE


@dataclass
class Preflight:
    ok: bool
    errors: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    arms: list[dict[str, Any]] = field(default_factory=list)
    samples: int = 0
    trials: int = 0
    calls_estimate: int = 0
    judge_calls_estimate: int = 0
    cost_low_usd: float | None = 0.0
    cost_high_usd: float | None = 0.0
    unknown_price_arms: list[str] = field(default_factory=list)
    disclosure: list[dict[str, Any]] = field(default_factory=list)
    plan_digest: str = ""
    labels: list[str] = field(default_factory=list)

    def as_dict(self) -> dict[str, Any]:
        return self.__dict__.copy()


async def load_samples(db: Database, plan: ExperimentPlan) -> list[dict[str, Any]]:
    splits = plan.protocol.splits or ["test"]
    if "all" in splits:
        rows = await db.fetchall("SELECT * FROM samples WHERE dataset_id=? ORDER BY ordinal", (plan.dataset_id,))
    else:
        q = ",".join("?" * len(splits))
        rows = await db.fetchall(f"SELECT * FROM samples WHERE dataset_id=? AND split IN ({q}) ORDER BY ordinal",
                                 (plan.dataset_id, *splits))
    if plan.protocol.sample_limit:
        rows = rows[: plan.protocol.sample_limit]
    return rows


def render_prompt(template: str, instructions: str, sample_text: str, meta: dict[str, Any]) -> str:
    out = template.replace("{instructions}", instructions).replace("{input}", sample_text)
    for k, v in meta.items():
        out = out.replace("{meta." + k + "}", str(v))
    return out


async def preflight(db: Database, registry: Registry, plan: ExperimentPlan) -> Preflight:
    pf = Preflight(ok=True, plan_digest=canonical_digest(plan.model_dump()))
    ds = await db.fetchone("SELECT * FROM datasets WHERE id=?", (plan.dataset_id,))
    if not ds:
        pf.errors.append(f"dataset '{plan.dataset_id}' not found")
        pf.ok = False
        return pf
    if ds["synthetic"]:
        pf.labels.append("synthetic_data")
    samples = await load_samples(db, plan)
    if not samples:
        pf.errors.append(f"no samples in splits {plan.protocol.splits}")
    pf.samples = len(samples)
    for s in plan.scorers:
        pf.errors += [f"scorer '{s.id}': {e}" for e in validate_spec(s.model_dump())]
        if is_judge(s.model_dump()):
            jp = s.params.get("provider") or "openai"
            if jp not in registry.adapters:
                pf.errors.append(f"judge provider '{jp}' unknown")
            elif jp in registry.specs and registry.specs[jp].requires_key and not registry.specs[jp].api_key:
                pf.errors.append(f"judge provider '{jp}' has no API key")
    arms = expand_arms(plan)
    if len(arms) > 48:
        pf.errors.append(f"{len(arms)} arms exceeds the 48-arm limit; reduce the factor grid")
    has_images = ds["modality"] == "image"
    n_img = sum(1 for r in samples if loads(r["input_json"])["images"])
    tok_in = [estimate_tokens(loads(r["input_json"])["text"] + ds["instructions"], len(loads(r["input_json"])["images"])) for r in samples]
    reps = plan.protocol.repetitions
    low = high = 0.0
    cost_unknown = False
    providers_used: dict[str, set[str]] = {}
    for arm in arms:
        a = arm.as_dict()
        if arm.provider not in registry.adapters:
            pf.errors.append(f"unknown provider '{arm.provider}'")
            continue
        spec = registry.specs.get(arm.provider)
        if spec and spec.requires_key and not spec.api_key:
            pf.errors.append(f"provider '{arm.provider}' has no API key configured")
        if arm.provider == "fake":
            pf.labels.append("fake_runtime_test")
        if arm.provider == "imported":
            pf.labels.append("imported_outputs")
            n_pred = await db.scalar("SELECT COUNT(*) FROM predictions WHERE import_name=? AND dataset_id=?", (arm.model, plan.dataset_id))
            if not n_pred:
                pf.errors.append(f"no imported predictions named '{arm.model}' for this dataset")
        caps = registry.adapters[arm.provider].capabilities(arm.model)
        sees_images = "image" in caps.modalities
        applicable_samples = len(samples) if sees_images else len(samples) - n_img
        applicable = applicable_samples > 0
        a["applicable"] = applicable
        a["partial"] = 0 < applicable_samples < len(samples)
        if not sees_images and n_img:
            pf.warnings.append(f"{arm.label}: model does not declare image input; its {n_img} image sample(s) will be marked not_applicable")
        price = registry.price_per_token(arm.provider, arm.model)
        a["price_known"] = price is not None
        if price is None:
            pf.unknown_price_arms.append(arm.label)
            cost_unknown = True
        n_trials = applicable_samples * reps
        pin, pout = price or (0.0, 0.0)
        max_out = int(arm.params.get("max_tokens", 512))
        share = applicable_samples / len(samples) if samples else 0
        arm_low = sum(tok_in) * share * reps * pin + n_trials * min(32, max_out) * pout
        arm_high = sum(tok_in) * share * reps * pin + n_trials * max_out * pout
        a["trials"] = n_trials
        a["cost_low_usd"] = arm_low if price else None
        a["cost_high_usd"] = arm_high if price else None
        low += arm_low
        high += arm_high
        pf.trials += len(samples) * reps
        pf.calls_estimate += n_trials
        providers_used.setdefault(arm.provider, set()).add(arm.model)
        pf.arms.append(a)
    judge_specs = [s for s in plan.scorers if is_judge(s.model_dump())]
    pf.judge_calls_estimate = pf.calls_estimate * len(judge_specs)
    for js in judge_specs:
        jprov, jmodel = js.params.get("provider") or "openai", js.params.get("model") or "gpt-4.1-nano"
        jp = registry.price_per_token(jprov, jmodel)
        if jp is None:
            cost_unknown = True
        else:
            per_call_in = 450 + 300
            low += pf.calls_estimate * (per_call_in * jp[0] + 40 * jp[1])
            high += pf.calls_estimate * (per_call_in * 2 * jp[0] + 400 * jp[1])
        pf.disclosure.append({"recipient": f"judge {jprov}/{jmodel}", "kind": "judge",
                              "data": "task text, model outputs" + (", reference answers" if js.params.get("include_reference", True) else "")})
    for prov, models in providers_used.items():
        kind = "local" if prov in ("fake", "imported", "local") else "external"
        pf.disclosure.append({"recipient": prov, "kind": kind, "models": sorted(models),
                              "data": "sample inputs" + (" + images" if has_images else "") + " (expected answers are never sent)"})
    pf.cost_low_usd = round(low, 6)
    pf.cost_high_usd = round(high, 6)
    if cost_unknown:
        pf.warnings.append("some prices are unknown; estimate excludes them and reservations use a conservative fallback price")
    cap = plan.budget.max_usd
    paid = high > 0 or cost_unknown
    if paid and cap is None:
        pf.warnings.append("no budget cap set for a paid run")
    if paid and not plan.budget.paid_run_confirmed:
        pf.warnings.append("paid run: requires explicit confirmation (budget.paid_run_confirmed) before launch")
    if cap is not None and high > cap:
        pf.warnings.append(f"worst-case estimate ${high:.4f} exceeds the cap ${cap:.4f}; the campaign may pause on budget")
    if len(samples) * reps < plan.acceptance.min_assessed_per_arm:
        pf.warnings.append(f"only {len(samples) * reps} trials per arm; below the acceptance minimum of "
                           f"{plan.acceptance.min_assessed_per_arm} → gate will report insufficient_evidence")
    if not {"fake_runtime_test", "imported_outputs"} & set(pf.labels):
        pf.labels.append("model_quality_test")
    pf.labels = sorted(set(pf.labels))
    pf.ok = not pf.errors
    return pf
