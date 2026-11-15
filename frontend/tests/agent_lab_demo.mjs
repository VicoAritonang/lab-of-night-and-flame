// Exercise the exact frontend demo function against a running lab, without a DOM.
// node frontend/tests/agent_lab_demo.mjs http://127.0.0.1:8011
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "../node_modules/typescript/lib/typescript.js";

const base = new URL(process.argv[2] || "http://127.0.0.1:8011");
globalThis.location = { protocol: base.protocol, host: base.host };
globalThis.window = { setTimeout, clearTimeout };
const nativeFetch = globalThis.fetch;
globalThis.fetch = (input, options) => nativeFetch(new URL(input, base), options);
const source = await readFile(new URL("../src/agentLab.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2021 } }).outputText;
const { demoTrace } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
const started = await fetch(new URL("/api/agent-lab/listener/start", base), { method: "POST" });
assert.equal(started.status, 200);
let traceId;
await demoTrace((id) => { traceId = id; });
const detail = await (await fetch(new URL(`/api/agent-lab/runs/${traceId}`, base))).json();
assert.equal(detail.status, "completed");
assert.equal(detail.summary.verdict, "pass");
assert.equal(detail.summary.retries, 1);
assert.equal(detail.summary.visits, 6);
assert.equal(detail.summary.cost.entries, 3);
assert.equal(detail.summary.cost.unknown_entries, 0);
assert.equal(detail.summary.cost.estimated_entries, 2);
assert.equal(detail.summary.tool_calls, 1);
console.log(JSON.stringify({ trace_id: traceId, events: detail.summary.events, verdict: detail.summary.verdict, source: "frontend demo over real WebSocket" }));
