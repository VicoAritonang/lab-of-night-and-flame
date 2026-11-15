# arl-lab — TypeScript SDK for Agent Reliability Lab

Zero-dependency client for Node.js ≥ 22, Bun, Deno and browsers. Streams agent
runs to [Agent Reliability Lab](https://github.com/VicoAritonang/lab-of-night-and-flame)
and accepts **Workbench** replays (re-run a captured scenario with another
model, prompt or state).

```bash
npm install arl-lab
```

```ts
import { Lab, Replay, withRun } from "arl-lab";

const lab = new Lab("support-agent"); // reads LAB_WS_URL, STAGE, LAB_TOKEN

async function answer(question: string, config: { model: string; system_prompt: string }, replay?: Replay) {
  return withRun(lab, "Support question", async (run) => {
    await run.input(question);
    await run.snapshot({ memory }, config);            // what a replay needs
    const request = { model: config.model, messages: [{ role: "system", content: config.system_prompt }, { role: "user", content: question }] };
    const reply = await llm(request);
    await run.modelCall({ request, response: reply.text, tokensIn: reply.usage.prompt_tokens, tokensOut: reply.usage.completion_tokens });
    await run.output(reply.text);
    return reply.text;
  }, { replay });
}

lab.onReplay((r) => answer(r.inputText(), { model: r.model("gpt-4o-mini"), system_prompt: r.systemPrompt(DEFAULT_PROMPT) }, r));
await lab.connect();
```

Pass `{ replay: r }` to `startRun`/`withRun` inside the handler so the new run is
linked to the replay (JavaScript has no implicit context across `await`).

Telemetry is enabled only with `STAGE=dev` and `LAB_WS_URL`; otherwise every call
resolves immediately. Default mode is fail-open; set `required: true` for strict
integration tests. On Node < 22 pass `WebSocket` from the `ws` package via options.

The protocol is documented in [connection.md](../../connection.md).
