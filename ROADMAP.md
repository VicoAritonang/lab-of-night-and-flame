# Feature research and roadmap

This document records what a production-grade agent lab and a research-grade
evaluation lab are expected to provide (as of late 2026), how Agent Reliability
Lab compares, what v0.4 added, and what is still missing.

## What was surveyed

- **Evaluation and observability platforms**: LangSmith, Langfuse, Braintrust,
  Arize Phoenix / AX, W&B Weave, Comet Opik, Helicone, promptfoo. Across vendor
  comparisons the common definition of an evaluation platform is one shared
  system for traces, datasets, experiments, evaluators, human review,
  production monitoring and collaboration, and agent platforms are judged on
  scoring whole trajectories (tool calls, sessions), not only final answers.
- **Research evaluation frameworks**: Inspect AI (UK AI Security Institute) —
  tasks, solvers, scorers, epochs with score reducers, sandboxed tool execution,
  external agents; OpenAI Evals; HELM's emphasis on multi-metric reporting.
- **Standards**: OpenTelemetry GenAI semantic conventions (`gen_ai.*`, with
  agent spans `invoke_agent` / `execute_tool`, still *Development* status and
  moved to a dedicated repository in 2026) and OpenInference.
- **Agent-evaluation practice**: measure tool-call accuracy (selection,
  arguments, schema) separately from trajectory efficiency (calls, retries,
  tokens, latency per success); attach tool checks to tool spans and task
  completion to the root; calibrate LLM judges against human labels before
  trusting them; repeat runs and report uncertainty.

## Capability matrix

| Capability | What leading labs provide | ARL before v0.4 | ARL v0.4 |
| --- | --- | --- | --- |
| Tracing | SDKs + OpenTelemetry ingestion, nested spans, sessions | arl/1 WebSocket SDKs (Go/Python/TS) | **+ OTLP/HTTP (protobuf/JSON) with GenAI, OpenInference and OpenLLMetry mapping; threads, users, tags** |
| Production monitoring | Time-series dashboards, p50/p95/p99, cost/token, per-model breakdown, trace search | Live trace list | **Monitor page, rollup columns, metrics/facets API, filterable trace explorer** |
| Feedback | End-user feedback API, human scores | `evaluation` events only | **Feedback API + SDK methods, feedback on traces and in metrics** |
| Human review | Annotation queues, rubrics, multiple annotators | — | **Queues with binary/scale/label/text rubrics, N annotators per item, sampling by filter** |
| Judge calibration | Agreement between LLM judge and humans | — | **Krippendorff's α, Cohen's κ, judge accuracy / κ / confusion / Spearman against human consensus** |
| Online evaluation | Evaluators on sampled production traces | — | **LLM-judge, trajectory-check and scorer evaluators; deterministic sampling; daily judge budget; backfill; dry run** |
| Alerting | Threshold alerts to Slack/webhooks | — | **Rules on error rate, fail rate, p95, cost, volume, feedback mean; state transitions; webhooks** |
| Agent-specific checks | Tool selection/order, step efficiency, output format | Forbidden tools, required graph nodes, substrings | **Required tools, tool order, tool/model call budgets, regex, JSON schema, canary exclusion** |
| Prompt management | Versioned prompts, environment labels, playground link | System prompt edited per scenario | **Registry with immutable versions, labels, diff, render, SDK fetch with fallback, open in Workbench** |
| Experiments | Datasets × models × prompts, scorers, judges | Strong: seeded plans, budgets, Wilson / bootstrap / McNemar, pass@k, Pareto | **+ Holm and Benjamini–Hochberg adjustment, Cohen's h, power analysis / minimum detectable effect in preflight** |
| Regression testing / CI | Eval-gated pipelines, baseline comparison | Manual Workbench re-runs | **Suites over a collection with overrides and repeats, per-scenario regression detection, gate, `arl suite` CLI, GitHub Actions example** |
| Performance | Latency/throughput profiling | Latency per trial | **Perf lab: concurrency sweep, TTFT, decode tokens/s, throughput, saturation point, load-generator environment** |
| Safety | Red-team suites, refusal/leak scoring | — | **Safety probe dataset (harmful, over-refusal, injection, extraction, PII) + `refusal` and `not_contains` scorers** |
| Reproducibility | Frozen configs, environment capture | Plan digests, frozen checks, dataset digests | **+ SDK environment metadata, prompt version refs in snapshots** |

## Research-grade statistics in the lab

- Pass rates with Wilson intervals; sample-level bootstrap; paired bootstrap of
  differences; exact McNemar on discordant pairs; unbiased pass@k.
- Multiple comparisons: Holm–Bonferroni (family-wise) and Benjamini–Hochberg
  (false discovery rate) adjusted p-values whenever several candidates face one
  baseline.
- Effect size (Cohen's h) next to p-values; minimum detectable effect and
  required sample size shown before a campaign runs, with an "underpowered"
  warning.
- Agreement: Krippendorff's α (nominal or interval, any number of raters,
  missing ratings), Cohen's κ, Pearson and Spearman. The implementations are
  tested against published reference values (Krippendorff 2011 α = 0.743; the
  classic κ = 0.4 2×2 table; Cohen 1988 power tables).

## Roadmap (not yet implemented)

Ordered by value for teams running agents in production and for research use.

1. **Capture proxy** — an OpenAI-compatible endpoint that forwards to the real
   provider and records every call, so agents get traced by changing only
   `base_url`.
2. **Sandboxed agent tasks** — Inspect-style task suites where the lab runs the
   agent against tools in Docker sandboxes with environment reset, for
   benchmark-grade agent evaluation (SWE-style, τ-bench-style).
3. **Simulated users** — LLM-driven multi-turn user simulators with personas
   and goals, scored per thread.
4. **Traces → datasets** — curate failing or reviewed traces into versioned
   datasets and Workbench collections in one click.
5. **Pairwise human preference** — blind side-by-side review with a
   Bradley–Terry / Elo leaderboard, and judge bias tests (position, verbosity,
   self-preference).
6. **Team features** — user accounts, roles, SSO, per-user API keys and audit
   by user (today one owner token protects the lab).
7. **Data governance** — retention policies, PII scrubbing rules at ingest,
   per-project export and deletion.
8. **Scale-out storage** — a columnar store (ClickHouse) option for very high
   trace volumes; OTLP metrics/logs; forwarding traces to another backend.
9. **Hardware telemetry** — GPU utilisation and memory from local inference
   servers during perf runs.

## Sources

- [Arize: 7 best AI agent evaluation platforms compared for 2026](https://arize.com/resources/llm-and-agent-evaluation-platforms/)
- [Latitude: best AI agent observability tools 2026](https://latitude.so/blog/best-ai-agent-observability-tools-2026-comparison)
- [PromptLayer: LLM observability platforms in 2026](https://www.promptlayer.com/blog/llm-observability-platforms-in-2026-the-landscape-compared/)
- [Inspect AI documentation](https://inspect.aisi.org.uk) and [AISI sandboxing toolkit](https://aisi.gov.uk/blog/the-inspect-sandboxing-toolkit-scalable-and-secure-ai-agent-evaluations)
- [Honeycomb: agent spans with OpenTelemetry](https://docs.honeycomb.io/send-data/use-cases/agents.md), [Dash0: OpenTelemetry GenAI semantic conventions](https://www.dash0.com/knowledge/opentelemetry-genai-semantic-conventions-explained)
- [Langfuse: AI agent evaluation](https://langfuse.com/resources/engineering/ai-agent-evaluation.md), [Atlan: agent trajectory evaluation](https://atlan.com/know/ai-agent/ai-agent-trajectory-evaluation.md)
