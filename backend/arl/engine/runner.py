"""Campaign runner: admission → budget reservation → attempts → outputs → assessments.

Invariants (product.md §7, §25):
  * Budget is reserved BEFORE dispatch; actual cost reconciles the reservation afterwards.
  * Each provider call is an attempt row; retries create new attempts; nothing is deleted.
  * A request that may have been processed without a receipt is ``unknown`` and its reservation
    stays counted as exposure (conservative), never assumed free.
  * Cancellation closes admission immediately; in-flight calls are interrupted and recorded.
  * A trial is never marked successful because the model *says* it succeeded: outcome comes only
    from scorers.
"""
from __future__ import annotations

import asyncio
import random
import time
from collections import deque
from typing import Any

from ..context import Lab
from ..db import dumps, loads, new_id
from ..models import ExperimentPlan
from ..providers.base import AdapterError, ContentPart, GenerationRequest, Message, redact
from ..scorers.builtin import Verdict
from ..scorers.registry import is_judge, run_scorer, scorer_version
from .planner import FALLBACK_PRICE, Arm, estimate_tokens, expand_arms, render_prompt, schedule

TERMINAL_TRIAL = ("completed", "error", "cancelled", "not_applicable")


class CampaignRunner:
    def __init__(self, lab: Lab, campaign_id: str):
        self.lab = lab
        self.db = lab.db
        self.id = campaign_id
        self.cancel_event = asyncio.Event()
        self.budget_stop = False
        self._budget_lock = asyncio.Lock()
        self.spent = 0.0
        self.reserved = 0.0
        self.exposure = 0.0      # reservations held for unknown-cost attempts
        self.unknown_cost = 0
        self.counts: dict[str, int] = {}
        self.outcomes: dict[str, int] = {}
        self._judge_sem = asyncio.Semaphore(4)
        self._judge_tasks: set[asyncio.Task] = set()
        self._inflight: set[asyncio.Task] = set()
        self._uri_cache: dict[str, str] = {}
        self._started = time.time()
        self._completed_times: deque[float] = deque(maxlen=200)

    # ------------------------------------------------------------------ public
    def cancel(self) -> None:
        self.cancel_event.set()
        for t in list(self._inflight):
            t.cancel()

    async def run(self) -> None:
        try:
            await self._run()
        except Exception as e:  # pragma: no cover - defensive: never leave a campaign 'running'
            await self.db.execute("UPDATE campaigns SET status='failed', stop_reason=?, finished_at=? WHERE id=?",
                                  (redact(f"runner_error: {type(e).__name__}: {e}"), time.time(), self.id))
            self._publish_state("failed")
            raise
        finally:
            self.lab.runners.pop(self.id, None)
            self.lab.analysis_cache.pop(self.id, None)

    # ------------------------------------------------------------------ setup
    async def _run(self) -> None:
        camp = await self.db.fetchone("SELECT * FROM campaigns WHERE id=?", (self.id,))
        plan = ExperimentPlan.model_validate(loads(camp["plan_json"]))
        self.plan = plan
        self.scoring = loads(camp["scoring_json"]) or {"scorers": [s.model_dump() for s in plan.scorers],
                                                       "primary": plan.primary_scorer, "revision": 1}
        self.spent = camp["spent_usd"] or 0.0
        self.exposure = camp["reserved_usd"] or 0.0
        self.unknown_cost = camp["unknown_cost_attempts"] or 0
        self.cap = camp["budget_cap_usd"]
        self.dataset = await self.db.fetchone("SELECT * FROM datasets WHERE id=?", (plan.dataset_id,))
        self.arms: dict[str, Arm] = {a.key: a for a in expand_arms(plan)}

        n_trials = await self.db.scalar("SELECT COUNT(*) FROM trials WHERE campaign_id=?", (self.id,))
        if not n_trials:
            await self._create_trials(plan)

        imported = {a.model for a in self.arms.values() if a.provider == "imported"}
        for name in imported:
            rows = await self.db.fetchall("SELECT sample_key, output_text FROM predictions WHERE import_name=? AND dataset_id=?",
                                          (name, plan.dataset_id))
            self.lab.registry.imported.load(name, {r["sample_key"]: r["output_text"] for r in rows})

        samples = await self.db.fetchall(
            "SELECT * FROM samples WHERE id IN (SELECT DISTINCT sample_id FROM trials WHERE campaign_id=?)", (self.id,))
        self.samples = {s["id"]: s for s in samples}
        await self._refresh_counts()

        await self.db.execute(
            "UPDATE campaigns SET status='running', stop_reason=NULL, started_at=COALESCE(started_at, ?) WHERE id=?",
            (time.time(), self.id))
        await self.db.audit("campaign.start", self.id, {"cap_usd": self.cap})
        self._publish_state("running")

        pending = await self.db.fetchall(
            "SELECT id, arm_key, sample_id, repetition, order_index, attempt_count FROM trials "
            "WHERE campaign_id=? AND status IN ('pending','interrupted','running') ORDER BY order_index", (self.id,))
        queue: deque[dict[str, Any]] = deque(pending)
        workers = [asyncio.create_task(self._worker(queue)) for _ in range(min(plan.protocol.concurrency, max(1, len(queue))))]
        await asyncio.gather(*workers, return_exceptions=True)

        if self.cancel_event.is_set():
            await self.db.execute("UPDATE trials SET status='cancelled' WHERE campaign_id=? AND status IN ('pending','interrupted')",
                                  (self.id,))
        await self._refresh_counts()
        self._publish_state("grading")
        await self.db.execute("UPDATE campaigns SET status='grading' WHERE id=?", (self.id,))
        if self._judge_tasks:
            await asyncio.gather(*self._judge_tasks, return_exceptions=True)
        await self._persist_budget()

        if self.cancel_event.is_set():
            final, reason = "cancelled", "user_cancelled"
        elif self.budget_stop:
            final, reason = "paused", "budget_exhausted"
        else:
            final, reason = "completed", None
        await self.db.execute("UPDATE campaigns SET status=?, stop_reason=?, finished_at=? WHERE id=?",
                              (final, reason, time.time(), self.id))
        await self.db.audit(f"campaign.{final}", self.id, {"spent_usd": self.spent, "reason": reason})
        await self._refresh_counts()
        self._publish_state(final)
        self.lab.bus.flush()

    async def _create_trials(self, plan: ExperimentPlan) -> None:
        from .planner import load_samples

        samples = await load_samples(self.db, plan)
        # applicability is decided per trial: a text-only model can still answer the text samples
        # of a mixed dataset; only image samples become not_applicable for it.
        sees_images = {key: "image" in self.lab.registry.get(arm.provider).capabilities(arm.model).modalities
                       for key, arm in self.arms.items()}
        has_images = {s["id"]: bool(loads(s["input_json"])["images"]) for s in samples}
        order = schedule(list(self.arms), [s["id"] for s in samples], plan.protocol.repetitions,
                         plan.protocol.order, plan.protocol.seed)
        rows = []
        for idx, (arm_key, sample_id, rep) in enumerate(order):
            na = has_images[sample_id] and not sees_images[arm_key]
            rows.append((new_id("tr"), self.id, arm_key, sample_id, rep, idx,
                         "not_applicable" if na else "pending", "not_applicable" if na else "not_assessed"))
        async with self.db.tx() as conn:
            await conn.executemany(
                "INSERT INTO trials(id, campaign_id, arm_key, sample_id, repetition, order_index, status, outcome) "
                "VALUES (?,?,?,?,?,?,?,?)", rows)

    # ------------------------------------------------------------------ budget
    def _reservation(self, arm: Arm, sample: dict[str, Any]) -> float:
        price = self.lab.registry.price_per_token(arm.provider, arm.model)
        if price is None:
            price = FALLBACK_PRICE
        if price == (0.0, 0.0):
            return 0.0
        inp = loads(sample["input_json"])
        tok_in = estimate_tokens(inp["text"] + self.dataset["instructions"] + arm.system, len(inp["images"]))
        return tok_in * price[0] + int(arm.params.get("max_tokens", 512)) * price[1]

    async def _reserve(self, amount: float) -> bool:
        async with self._budget_lock:
            if self.cap is not None and amount > 0 and self.spent + self.reserved + self.exposure + amount > self.cap + 1e-12:
                return False
            self.reserved += amount
            return True

    async def _settle(self, reserved: float, cost: float | None, unknown: bool) -> None:
        async with self._budget_lock:
            self.reserved -= reserved
            if unknown:
                self.exposure += reserved
                self.unknown_cost += 1
            elif cost is not None:
                self.spent += cost

    async def _persist_budget(self) -> None:
        await self.db.execute(
            "UPDATE campaigns SET spent_usd=?, reserved_usd=?, unknown_cost_attempts=? WHERE id=?",
            (round(self.spent, 8), round(self.reserved + self.exposure, 8), self.unknown_cost, self.id))

    # ------------------------------------------------------------------ execution
    async def _worker(self, queue: deque[dict[str, Any]]) -> None:
        while queue and not self.cancel_event.is_set() and not self.budget_stop:
            trial = queue.popleft()
            task = asyncio.create_task(self._execute(trial))
            self._inflight.add(task)
            try:
                await task
            except asyncio.CancelledError:
                if not self.cancel_event.is_set():
                    raise
            finally:
                self._inflight.discard(task)

    async def _messages(self, arm: Arm, sample: dict[str, Any]) -> list[Message]:
        inp = loads(sample["input_json"])
        meta = loads(sample["meta_json"]) or {}
        system = render_prompt(arm.system, self.dataset["instructions"], inp["text"], meta).strip()
        user_text = render_prompt(arm.user, self.dataset["instructions"], inp["text"], meta)
        parts = [ContentPart("text", user_text)] if user_text else []
        for digest in inp["images"]:
            uri = self._uri_cache.get(digest)
            if uri is None:
                uri = await self.lab.artifacts.data_uri(digest)
                if len(self._uri_cache) < 256:
                    self._uri_cache[digest] = uri
            parts.append(ContentPart("image", data_uri=uri))
        msgs = [Message("system", [ContentPart("text", system)])] if system else []
        msgs.append(Message("user", parts))
        return msgs

    async def _execute(self, trial: dict[str, Any]) -> None:
        arm = self.arms[trial["arm_key"]]
        sample = self.samples[trial["sample_id"]]
        adapter = self.lab.registry.get(arm.provider)
        bucket = self.lab.registry.buckets[arm.provider]
        proto = self.plan.protocol
        params = dict(arm.params)
        if arm.provider == "fake":
            params.setdefault("seed", trial["repetition"])
        req = GenerationRequest(
            model=arm.model, messages=await self._messages(arm, sample), params=params,
            timeout_s=proto.timeout_s, stream=proto.stream and arm.provider != "imported",
            fake_reference=loads(sample["expected_json"]) if arm.provider == "fake" else None,
            sample_key=f"{sample['key']}#r{trial['repetition']}",
        )
        started = time.time()
        await self.db.execute("UPDATE trials SET status='running', started_at=COALESCE(started_at, ?) WHERE id=?",
                              (started, trial["id"]))
        self._bump("running", +1)
        self._emit(trial, "running")

        n = trial.get("attempt_count") or 0
        last_err: AdapterError | None = None
        max_attempts = n + proto.max_retries + 1
        while n < max_attempts:
            if self.cancel_event.is_set():
                break
            reserve = self._reservation(arm, sample)
            if not await self._reserve(reserve):
                self.budget_stop = True
                self.lab.bus.publish(self.id, {"type": "log", "level": "warn",
                                               "message": "budget cap reached: admission stopped"})
                await self.db.execute("UPDATE trials SET status='pending' WHERE id=?", (trial["id"],))
                self._bump("running", -1)
                self._emit(trial, "pending")
                return
            n += 1
            att_id = new_id("att")
            t0 = time.time()
            await self.db.execute(
                "INSERT INTO attempts(id, trial_id, n, status, provider, requested_model, reserved_usd, started_at) "
                "VALUES (?,?,?,?,?,?,?,?)", (att_id, trial["id"], n, "running", arm.provider, arm.model, reserve, t0))
            sent = False
            try:
                queue_wait = await bucket.acquire()
                sent = True
                res = await adapter.generate(req, self.cancel_event)
            except asyncio.CancelledError:
                await self._settle(reserve, 0.0 if not sent else None, unknown=sent and reserve > 0)
                await self._finish_attempt(att_id, "unknown" if sent else "cancelled", "cancelled",
                                           "cancelled while in flight" if sent else "cancelled before dispatch", None)
                await self.db.execute("UPDATE trials SET status='cancelled', attempt_count=?, finished_at=? WHERE id=?",
                                      (n, time.time(), trial["id"]))
                self._bump("running", -1)
                self._emit(trial, "cancelled")
                raise
            except AdapterError as e:
                last_err = e
                unknown = e.maybe_processed and reserve > 0
                await self._settle(reserve, 0.0 if not unknown else None, unknown=unknown)
                status = "unknown" if e.maybe_processed else ("timeout" if e.category == "timeout" else "failed")
                if e.category == "cancelled":
                    status = "unknown" if e.maybe_processed else "cancelled"
                await self._finish_attempt(att_id, status, e.category, str(e), None)
                if e.category == "rate_limited":
                    bucket.penalize(e.retry_after or min(10.0, 2.0 * n))
                if e.retryable and n < max_attempts and not self.cancel_event.is_set():
                    # full-jitter exponential backoff; rate limits back off harder (2s, 4s, 8s … ≤ 30s)
                    base = 2.0 if e.category == "rate_limited" else 0.5
                    delay = min(30.0, max(e.retry_after or 0, base * 2 ** (n - 1)) * random.uniform(0.75, 1.25))
                    self.lab.bus.publish(self.id, {"type": "log", "level": "info",
                                                   "message": f"retry {arm.label} · {sample['key']} after {e.category} ({delay:.1f}s)"})
                    await asyncio.sleep(delay)
                    continue
                break
            except Exception as e:  # unexpected adapter bug: record, never crash the campaign
                last_err = AdapterError("unknown", f"{type(e).__name__}: {e}", maybe_processed=True)
                await self._settle(reserve, None, unknown=reserve > 0)
                await self._finish_attempt(att_id, "unknown", "unknown", str(last_err), None)
                break
            else:
                unknown_cost = res.cost_usd is None and arm.provider not in ("imported",)
                await self._settle(reserve, res.cost_usd, unknown=unknown_cost and reserve > 0)
                if unknown_cost and reserve == 0:
                    self.unknown_cost += 1
                await self._finish_attempt(att_id, "completed", None, None, res, queue_wait)
                await self.db.execute(
                    "UPDATE trials SET status='completed', output_text=?, error_category=NULL, latency_ms=?, ttft_ms=?, "
                    "tokens_in=?, tokens_out=?, cost_usd=?, attempt_count=?, resolved_model=?, upstream_provider=?, finished_at=? WHERE id=?",
                    (res.text, res.latency_ms, res.ttft_ms, res.tokens_in, res.tokens_out,
                     await self._trial_cost(trial["id"]), n, res.resolved_model, res.upstream_provider, time.time(), trial["id"]))
                self._completed_times.append(time.time())
                self._bump("running", -1)
                self._bump("completed", +1)
                await self._persist_budget()
                await self._grade(trial, arm, sample, res.text)
                return

        # all attempts exhausted, non-retryable error, or cancelled between attempts
        cancelled = self.cancel_event.is_set() and (last_err is None or last_err.category == "cancelled")
        status = "cancelled" if cancelled else "error"
        await self.db.execute(
            "UPDATE trials SET status=?, error_category=?, attempt_count=?, cost_usd=?, finished_at=? WHERE id=?",
            (status, None if cancelled else (last_err.category if last_err else "unknown"), n,
             await self._trial_cost(trial["id"]), time.time(), trial["id"]))
        self._bump("running", -1)
        self._bump(status, +1)
        await self._persist_budget()
        self._emit(trial, status, error=last_err.category if last_err else None)

    async def _trial_cost(self, trial_id: str) -> float | None:
        """Total known cost across ALL attempts (failed retries included). None if any attempt's cost is unknown."""
        rows = await self.db.fetchall("SELECT status, cost_usd, reserved_usd FROM attempts WHERE trial_id=?", (trial_id,))
        total = 0.0
        for r in rows:
            if r["cost_usd"] is None:
                if r["status"] == "unknown" and r["reserved_usd"] > 0:
                    return None
                if r["status"] == "completed" and r["reserved_usd"] > 0:
                    return None
                continue
            total += r["cost_usd"]
        return total

    async def _finish_attempt(self, att_id: str, status: str, category: str | None, message: str | None,
                              res: Any, queue_wait: float = 0.0) -> None:
        if res is None:
            cost = 0.0 if status in ("failed", "cancelled", "timeout") else None  # unknown => NULL, never 0
            await self.db.execute(
                "UPDATE attempts SET status=?, error_category=?, error_message=?, cost_usd=?, finished_at=? WHERE id=?",
                (status, category, redact(message)[:1000] if message else None, cost, time.time(), att_id))
            return
        await self.db.execute(
            "UPDATE attempts SET status=?, resolved_model=?, upstream_provider=?, request_id=?, finish_reason=?, latency_ms=?, "
            "ttft_ms=?, tokens_in=?, tokens_out=?, cost_usd=?, cost_source=?, finished_at=? WHERE id=?",
            (status, res.resolved_model, res.upstream_provider, res.request_id, res.finish_reason, res.latency_ms, res.ttft_ms,
             res.tokens_in, res.tokens_out, res.cost_usd, res.cost_source, time.time(), att_id))

    # ------------------------------------------------------------------ grading
    async def _grade(self, trial: dict[str, Any], arm: Arm, sample: dict[str, Any], output: str) -> None:
        expected = loads(sample["expected_json"])
        task_text = loads(sample["input_json"])["text"]
        meta = loads(sample["meta_json"]) or {}
        rev = self.scoring["revision"]
        primary = self.scoring["primary"]
        outcome = None
        for spec in self.scoring["scorers"]:
            if is_judge(spec):
                t = asyncio.create_task(self._judge(trial, spec, task_text, output, expected, rev, spec["id"] == primary, meta))
                self._judge_tasks.add(t)
                t.add_done_callback(self._judge_tasks.discard)
                continue
            v = await run_scorer(self.lab, spec, task_text, output, expected, meta)
            await insert_assessment(self.lab, trial["id"], spec, rev, v)
            if spec["id"] == primary:
                outcome = v.verdict
        if outcome is not None:
            await self._set_outcome(trial, outcome)
        else:
            self._emit(trial, "completed")

    async def _judge(self, trial, spec, task_text, output, expected, rev, primary: bool, meta=None) -> None:
        async with self._judge_sem:
            async with self._budget_lock:
                over = self.cap is not None and self.spent + self.reserved + self.exposure >= self.cap
            if over:
                v = Verdict("inconclusive", None, {"reason": "not_assessed_budget_exhausted"})
            else:
                v = await run_scorer(self.lab, spec, task_text, output, expected, meta)
                async with self._budget_lock:
                    if v.cost_usd is not None:
                        self.spent += v.cost_usd
                    elif v.evidence.get("reason") != "empty_output":
                        self.unknown_cost += 1
            await insert_assessment(self.lab, trial["id"], spec, rev, v)
            if primary:
                await self._set_outcome(trial, v.verdict)

    async def _set_outcome(self, trial: dict[str, Any], verdict: str) -> None:
        outcome = verdict if verdict in ("pass", "fail", "inconclusive", "not_applicable") else "not_assessed"
        await self.db.execute("UPDATE trials SET outcome=? WHERE id=?", (outcome, trial["id"]))
        self.outcomes[outcome] = self.outcomes.get(outcome, 0) + 1
        self._emit(trial, "completed", outcome=outcome)

    # ------------------------------------------------------------------ live state
    def _bump(self, key: str, delta: int) -> None:
        self.counts[key] = self.counts.get(key, 0) + delta

    async def _refresh_counts(self) -> None:
        rows = await self.db.fetchall("SELECT status, outcome, COUNT(*) c FROM trials WHERE campaign_id=? GROUP BY status, outcome",
                                      (self.id,))
        self.counts, self.outcomes = {}, {}
        for r in rows:
            self.counts[r["status"]] = self.counts.get(r["status"], 0) + r["c"]
            if r["status"] == "completed":
                self.outcomes[r["outcome"]] = self.outcomes.get(r["outcome"], 0) + r["c"]

    def _throughput(self) -> float | None:
        if len(self._completed_times) < 2:
            return None
        span = self._completed_times[-1] - self._completed_times[0]
        return (len(self._completed_times) - 1) / span * 60 if span > 0 else None

    def _state(self, status: str) -> dict[str, Any]:
        return {"status": status, "counts": dict(self.counts), "outcomes": dict(self.outcomes),
                "spent_usd": round(self.spent, 8), "reserved_usd": round(self.reserved + self.exposure, 8),
                "unknown_cost_attempts": self.unknown_cost, "cap_usd": self.cap,
                "throughput_per_min": self._throughput(), "elapsed_s": time.time() - self._started}

    def _publish_state(self, status: str) -> None:
        st = self._state(status)
        self.lab.bus.set_state(self.id, st)
        self.lab.bus.publish(self.id, {"type": "state", "state": st})

    def _emit(self, trial: dict[str, Any], status: str, outcome: str | None = None, error: str | None = None) -> None:
        self.lab.bus.set_state(self.id, self._state("running"))
        self.lab.bus.trial_update(self.id, {"id": trial["id"], "i": trial["order_index"], "arm": trial["arm_key"],
                                            "status": status, "outcome": outcome, "error": error})


async def insert_assessment(lab: Lab, trial_id: str, spec: dict[str, Any], revision: int, v: Verdict) -> None:
    await lab.db.execute(
        "INSERT INTO assessments(id, trial_id, scorer_key, scorer_version, revision, score, passed, verdict, evidence_json, cost_usd, created_at) "
        "VALUES (?,?,?,?,?,?,?,?,?,?,?)",
        (new_id("as"), trial_id, spec["id"], f"{spec['key']}@{scorer_version(spec)}", revision, v.score,
         None if v.passed is None else int(v.passed), v.verdict, dumps(v.evidence),
         v.cost_usd if is_judge(spec) else 0.0, time.time()))  # deterministic scorers cost nothing
