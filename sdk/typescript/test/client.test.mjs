import assert from "node:assert/strict";
import { test } from "node:test";
import { Lab, ProtocolError, text, withRun } from "../dist/index.js";

/** In-memory stand-in for the Lab's WebSocket endpoint. */
function fakeLab({ pushReplay = false, reject = "" } = {}) {
  const state = { events: [], statuses: [], hellos: [], sockets: 0 };
  class FakeWS {
    constructor(url, init) {
      state.sockets++;
      this.init = init;
      setTimeout(() => this.onopen?.(), 0);
    }
    reply(m) { setTimeout(() => this.onmessage?.({ data: JSON.stringify(m) }), 0); }
    send(raw) {
      const m = JSON.parse(raw);
      if (m.type === "hello") {
        state.hellos.push(m);
        this.reply({ type: "hello_ack", session_id: "s1", resume_token: "r1" });
        if (pushReplay) setTimeout(() => this.onmessage?.({ data: JSON.stringify({ type: "replay", replay_id: "rpl-1", input: "hi", config: { model: "m-2", system_prompt: "be kind" }, checks: { max_retries: 0 } }) }), 5);
      } else if (m.type === "replay.status") {
        state.statuses.push(m.status);
        this.reply({ type: "ack", replay_id: m.replay_id });
      } else if (m.type === "ping") {
        this.reply({ type: "pong" });
      } else if (m.event === reject) {
        this.reply({ type: "error", code: "unknown_phase", message: "nope" });
      } else {
        state.events.push(m);
        this.reply({ type: "ack", event_id: m.event_id, trace_id: "trace-1", ordinal: state.events.length });
      }
    }
    close() { this.onclose?.(); }
  }
  return { state, WebSocket: FakeWS };
}

test("run helpers emit protocol events in order", async () => {
  const { state, WebSocket } = fakeLab();
  const lab = await new Lab("ts-agent", { url: "ws://lab/api/agent-lab/ws", stage: "dev", required: true, token: "t", WebSocket }).connect();
  const run = await lab.startRun("demo", { checks: { output_contains: ["ok"] } });
  await run.input("question");
  await run.snapshot({ memory: 1 }, { model: "m-1" });
  await run.modelCall({ request: { model: "m-1", messages: [{ role: "user", content: "q" }] }, response: "ok", tokensIn: 3, tokensOut: 1 });
  await run.tool("search", { q: 1 }, ["r"]);
  await run.output(text("ok"));
  await run.end();
  lab.close();
  assert.equal(run.traceId, "trace-1");
  assert.deepEqual(state.events.map((e) => e.event), ["run.start", "input", "state.snapshot", "model.request", "model.response", "cost.record", "tool.call", "tool.result", "output", "run.end"]);
  assert.equal(state.events[3].data.request.messages[0].content, "q");
});

test("concurrent emits are serialized and all acknowledged", async () => {
  const { state, WebSocket } = fakeLab();
  const lab = await new Lab("ts-agent", { url: "ws://lab", stage: "dev", required: true, WebSocket }).connect();
  const acks = await Promise.all(Array.from({ length: 20 }, (_, i) => lab.emit("r", "custom.event", "agent", { i })));
  lab.close();
  assert.equal(state.events.length, 20);
  assert.deepEqual(acks.map((a) => a.ordinal), Array.from({ length: 20 }, (_, i) => i + 1));
});

test("replay handler runs and links the new run", async () => {
  const { state, WebSocket } = fakeLab({ pushReplay: true });
  const lab = new Lab("ts-agent", { url: "ws://lab", stage: "dev", required: true, WebSocket });
  const done = new Promise((resolve) => {
    lab.onReplay(async (r) => {
      await withRun(lab, "replayed", async (run) => { await run.output(`${r.model("m-1")} ${r.systemPrompt("")} ${r.inputText()}`); }, { replay: r });
      resolve();
    });
  });
  await lab.connect();
  await done;
  await new Promise((r) => setTimeout(r, 30));
  lab.close();
  assert.ok(state.hellos[0].capabilities.includes("replay"));
  assert.equal(state.events[0].data.replay.replay_id, "rpl-1");
  assert.equal(state.events[0].data.checks.max_retries, 0);
  assert.deepEqual(state.events[1].data.content[0].text, "m-2 be kind hi");
  assert.deepEqual(state.statuses, ["started", "completed"]);
});

test("rejections are fail-open unless required; disabled outside dev", async () => {
  const { state, WebSocket } = fakeLab({ reject: "input" });
  const lab = await new Lab("ts-agent", { url: "ws://lab", stage: "dev", WebSocket, logger: { warn() {} } }).connect();
  const run = await lab.startRun("x");
  await run.input("dropped");
  await run.end();
  assert.deepEqual(state.events.map((e) => e.event), ["run.start", "run.end"]);
  lab.close();
  const strict = await new Lab("ts-agent", { url: "ws://lab", stage: "dev", required: true, WebSocket }).connect();
  await assert.rejects((await strict.startRun("y")).input("rejected"), ProtocolError);
  strict.close();
  const off = await new Lab("ts-agent", { url: "ws://lab", stage: "production", WebSocket }).connect();
  assert.equal((await off.startRun("z")).traceId, null);
});
