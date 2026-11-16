# Agent Reliability Lab

**Postman + live tracing for AI agents, running next to your code.**
Add one container to your development `docker compose`, add a few SDK calls to
your agent, and you get:

- **Live traces** of every run — input, retrieved context, knowledge-graph walks,
  agent loop, LLM requests/responses, tool calls, cost, checks — streamed over one
  WebSocket while you develop.
- **Workbench** — capture a run's *functional state* (input, state snapshot, model,
  system prompt, parameters) as a scenario, edit anything, and **send it again**:
  as a full agent replay executed by your own code, or as a single LLM step the lab
  calls directly. Every run is stored for side-by-side comparison with a word diff.
- **Experiments** — benchmark models on datasets with seeded schedules, retries,
  budgets, scorers/judges, confidence intervals, paired comparisons (with Holm /
  Benjamini–Hochberg correction and effect sizes), power analysis and reports.
- **OpenTelemetry ingestion** — any agent framework that exports OTel GenAI or
  OpenInference spans (OpenAI Agents SDK, LangGraph, LlamaIndex, Vercel AI SDK,
  Pydantic AI, ADK, …) shows up as lab traces, no lab SDK required.
- **Monitor** — production-style dashboards: volume, error rate, p50/p95/p99,
  tokens and cost over time, per model / tool / service, threads and users, and a
  filterable trace explorer.
- **Quality loop** — end-user and reviewer feedback, annotation queues with
  rubrics, inter-annotator agreement (Krippendorff's α, Cohen's κ) and LLM-judge
  calibration against humans, online evaluators on sampled traces, and alert
  rules with webhooks.
- **Agent-trajectory checks** — required tools, tool order, step budgets, output
  regex / JSON schema / canary leaks, on live traces and Workbench runs.
- **Prompt registry** — immutable prompt versions with `production` / `staging`
  labels, diff and render; agents fetch them with `lab.prompt(...)` and a fallback.
- **Regression suites** — run a Workbench collection with a new model or prompt,
  repeated, compare with a baseline and gate CI (`arl suite …`, exit code 1 on
  regression).
- **Perf lab** — load-test any endpoint (including local models on your
  hardware): TTFT, latency percentiles, decode tokens/s and throughput per
  concurrency level, with the saturation point.
- **Safety probes** — a built-in red-team dataset (harmful requests, over-refusal,
  prompt injection, system-prompt extraction, PII leakage) with refusal and
  canary-leak scorers.

The SDKs are no-ops outside `STAGE=dev`, so the instrumentation stays in your code
and costs nothing in production.

```mermaid
flowchart LR
  subgraph compose["your docker compose (dev)"]
    AGENT["your agent<br/>+ arl SDK"] <-->|"arl/1 WebSocket<br/>events ⇄ replay"| LAB["Agent Reliability Lab<br/>API · UI · worker"]
    LAB --> DB[("PostgreSQL")]
  end
  YOU(["you, in the browser"]) --> LAB
  LAB -.->|"LLM step / experiments"| PROVIDERS["OpenRouter · OpenAI · Groq<br/>Ollama · any OpenAI-compatible"]
```

## Quick start

```bash
git clone https://github.com/VicoAritonang/lab-of-night-and-flame.git
cd lab-of-night-and-flame
docker compose up -d            # → http://localhost:8000
```

Try the complete loop with the example agent (no API key needed — it falls back
to a mock model):

```bash
docker compose -f examples/compose/docker-compose.yml up -d --build
# open http://localhost:8000/workbench → "Capture from trace" → edit model or prompt → Send
```

## Add it to your project

1. **Add the lab to your compose file** (copy the `arl` and `arl-db` services from
   [docker-compose.yml](docker-compose.yml), or use `include:`), and give each agent:

   ```yaml
   environment:
     LAB_WS_URL: ws://arl:8000/api/agent-lab/ws
     STAGE: dev
   ```

   A full layout is in [examples/compose/docker-compose.yml](examples/compose/docker-compose.yml).
   Agents running on the host use `ws://localhost:8000/api/agent-lab/ws`.

2. **Instrument the agent** with an SDK:

   | Language | Install | Docs |
   | --- | --- | --- |
   | Python | `pip install arl-lab` | [sdk/python](sdk/python/README.md) |
   | TypeScript / JS | `npm install arl-lab` | [sdk/typescript](sdk/typescript/README.md) |
   | Go | `go get go.vicoworks.com/agent-reliability-lab@latest` | [sdk/go](sdk/go/README.md) |
   | Anything else | JSON over WebSocket | [connection.md](connection.md) |

   ```python
   from arl_lab import Lab

   lab = Lab("support-agent")

   def answer(question, config):
       with lab.run("Support question") as run:
           run.input(question)
           run.snapshot(state={"memory": memory}, config=config)    # what a replay needs
           request = {"model": config["model"], "messages": [
               {"role": "system", "content": config["system_prompt"]},
               {"role": "user", "content": question}]}
           reply = llm(request)
           run.model_call(request, reply)                           # re-sendable LLM step
           run.output(reply)
           return reply

   @lab.on_replay                      # Workbench → your code, with edited values
   def replay(r):
       answer(r.input_text(), {"model": r.model(DEFAULT_MODEL),
                               "system_prompt": r.system_prompt(DEFAULT_PROMPT)})

   lab.connect(wait=30)
   ```

3. **Use the UI**: *Agent lab* shows live traces; *Workbench* turns any trace into
   an editable scenario (`Open in Workbench` on a trace, or *Capture from trace*).

### Already using OpenTelemetry?

Skip the SDK and point the exporter at the lab (protobuf or JSON, gzip OK):

```bash
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:8000/api/otel/v1/traces
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $ARL_AUTH_TOKEN"   # only if auth is on
```

Chat, tool, agent and retrieval spans become model calls (re-sendable from the
Workbench), tool calls, an agent graph and retrieval spans. Attribute mapping is
documented in [connection.md](connection.md#opentelemetry-otlp--framework-apa-pun-tanpa-sdk-lab).

### Feedback, prompts and threads from the SDK

```python
prompt = lab.prompt("support.answer", fallback="You are a support agent.")  # production label
with lab.run("Support question", run_id=request_id, thread_id=conversation_id, user_id=user.id) as run:
    run.snapshot(config={"model": model, "prompt": prompt.ref})
    messages = prompt.render(company="Acme", question=question)
    ...
lab.feedback("thumbs", True, run_id=request_id)          # later, from your API
```

## The Workbench flow

```mermaid
sequenceDiagram
  participant A as Agent (SDK)
  participant L as Lab
  participant U as You (Workbench)
  A->>L: run.start · input · state.snapshot · model.request(data.request) · output · run.end
  U->>L: Capture from trace → scenario (input, state, config, messages, checks)
  U->>U: edit model / system prompt / params / state / input
  alt Agent replay
    U->>L: Send (mode=agent)
    L->>A: {"type":"replay", input, state, config, checks}
    A->>L: replay.status started → new linked run → completed
  else LLM step
    U->>L: Send (mode=llm)
    L->>L: call provider with captured messages + edits
  end
  L-->>U: output, latency, tokens, cost, checks · history · diff
```

Scenarios are grouped into collections and can be exported/imported as JSON
(`arl-workbench/1`) to share with your team or keep in the repository.

## Regression suites in CI

Keep a Workbench collection in your repository (export it as `arl-workbench/1`),
then gate merges on it with the lab image:

```bash
docker run --rm --network host ghcr.io/vicoaritonang/agent-reliability-lab:latest suite \
  --url http://localhost:8000 --collection support --model gpt-4.1-mini \
  --repeats 3 --baseline latest --min-pass-rate 0.9 --max-regressions 0
```

Exit code 0 means the gate passed, 1 that it failed, 2 a usage or connection
error. A complete GitHub Actions job is in
[examples/ci/agent-regression.yml](examples/ci/agent-regression.yml).

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ARL_DATABASE_URL` (`NEON_DB_URL`) | — | PostgreSQL URL (compose sets it) |
| `ARL_AUTO_MIGRATE` | `true` in image | Apply [sql/init.sql](sql/init.sql) at startup (idempotent) |
| `ARL_LISTEN_ON_START` | `true` in image | Accept agents immediately |
| `ARL_EMBEDDED_WORKER` | `true` without SQS | Run jobs in process |
| `ARL_SERVE_UI` | `true` | Serve the web UI on `/` |
| `ARL_AUTH_TOKEN` | empty | Protect the UI/API/WebSocket; agents send `LAB_TOKEN` |
| `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY`, `ARL_LOCAL_BASE_URL` | empty | Providers for LLM steps, Playground and experiments (`fake` always works) |

The lab binds to `127.0.0.1` in the provided compose files. Set `ARL_AUTH_TOKEN`
before exposing it beyond your machine. Larger shared deployments (EC2 + Lambda/SQS +
Neon + Vercel) are described in [DEPLOYMENT.md](DEPLOYMENT.md).

## Development

```bash
# Backend (Go 1.26). Integration tests need an isolated PostgreSQL:
go test ./...
ARL_TEST_DB_URL='postgres://postgres@localhost:5432/arl_test?sslmode=disable' go test -p 1 ./...

# UI with hot reload (proxies /api to :8000)
cd frontend && npm ci && npm run dev

# SDKs
(cd sdk/go && go test ./...)
(cd sdk/python && pip install -e ".[test]" && pytest)
(cd sdk/typescript && npm install && npm test)
```

Repository layout: `main.go` (server), `internal/` (engine, tracing, workbench,
providers, storage), `web/` (embedded UI), `frontend/` (React UI), `sdk/` (Go,
Python, TypeScript), `examples/`, `sql/`, `cmd/` (worker, Lambda, admin, packager).

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## Roadmap

The feature research behind this release — what leading evaluation and
observability platforms and research evaluation frameworks provide, and what the
lab still lacks — is in [ROADMAP.md](ROADMAP.md).

## License

[Apache-2.0](LICENSE)
