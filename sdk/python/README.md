# arl-lab — Python SDK for Agent Reliability Lab

Stream what your agent does (input, state, LLM calls, tools, output) to a local
[Agent Reliability Lab](https://github.com/VicoAritonang/lab-of-night-and-flame)
and let its **Workbench** replay any captured run with a different model,
prompt or state — like Postman, but for agents.

```bash
pip install arl-lab
```

```python
from arl_lab import Lab

lab = Lab("support-agent")          # reads LAB_WS_URL, STAGE, LAB_TOKEN

def answer(question, config):
    with lab.run("Support question", checks={"max_latency_ms": 10_000}) as run:
        run.input(question)
        run.snapshot(state={"memory": memory}, config=config)   # what a replay needs
        request = {"model": config["model"], "messages": [
            {"role": "system", "content": config["system_prompt"]},
            {"role": "user", "content": question}]}
        reply = llm(request)
        run.model_call(request, reply.text, tokens_in=reply.prompt_tokens, tokens_out=reply.completion_tokens)
        run.output(reply.text)
        return reply.text

@lab.on_replay                       # the Workbench calls this with edited values
def replay(r):
    answer(r.input_text(), {"model": r.model("gpt-4o-mini"),
                            "system_prompt": r.system_prompt(DEFAULT_PROMPT)})

lab.connect(wait=30)
```

Run your agent with `STAGE=dev LAB_WS_URL=ws://localhost:8000/api/agent-lab/ws`.
Without `STAGE=dev` every call is a no-op, so the instrumentation can stay in
production code.

| API | Purpose |
| --- | --- |
| `Lab(service, phases=..., required=False)` | Connection. Fail-open by default; `required=True` (or `LAB_REQUIRED=true`) raises on failures, for integration tests. |
| `lab.run(name, checks=...)` | Context manager for one job; ends as `failed` on exceptions. |
| `run.input / run.output` | Strings, content parts or any JSON value. |
| `run.snapshot(state, config)` | Functional state used by Workbench replays. |
| `run.model_call(request, response, ...)` | OpenAI-compatible request + response, tokens, cost. Re-sendable as an *LLM step*. |
| `run.tool(name, args, result)` | Tool call and result. |
| `run.emit(event, phase, data)` | Any custom `arl/1` event. |
| `@lab.on_replay` | Replay handler; runs started inside it are linked to the replay automatically. |
| `lab.upload_image(name, bytes)` | Store an image and reference it as `{"type": "image", "artifact": digest}`. |

The protocol is documented in [connection.md](../../connection.md).
