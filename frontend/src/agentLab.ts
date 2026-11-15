export interface TraceEvent {
  event_id: string; run_id: string; event: string; phase: string; ordinal: number;
  received_at: number; timestamp?: number; span_id?: string; parent_span_id?: string;
  data: Record<string, any>;
}
export interface LabRun {
  id: string; session_id: string; external_id: string; name: string; status: string;
  service: string; lab: string; event_count: number; started_at: number; finished_at: number | null;
}
export interface LabConfig {
  pricing: Record<string, { input_per_million: number; output_per_million: number }>;
  checks: Record<string, any>;
}
export interface LabStatus { listening: boolean; active_connections: number; config: LabConfig }
export interface RunDetail extends LabRun {
  config: { lab: string; phases: string[]; capabilities: string[]; metadata: Record<string, any> };
  check_config: Record<string, any>;
  summary: {
    events: number; visits: number; unique_nodes: number; retries: number; tool_calls: number; duration_ms: number;
    phases: Record<string, number>; verdict: string;
    cost: { known_usd: number; unknown_entries: number; entries: number; estimated_entries: number; tokens_in: number; tokens_out: number; unreported_operations: string[] };
    checks: { name: string; passed: boolean | null; evidence: any }[];
  };
}

/** A real WebSocket dry run through exactly the same public protocol as external services. */
export function labWebSocketURL(): string {
  const configured = (import.meta as ImportMeta & { env?: Record<string, string> }).env?.VITE_LAB_WS_URL;
  return configured || `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/api/agent-lab/ws`;
}
export async function demoTrace(onTrace: (id: string) => void) {
  // A single-use ticket carries the existing UI login across the Vercel/API domains.
  const ticketResponse = await fetch("/api/agent-lab/ticket", { method: "POST", credentials: "same-origin" });
  if (!ticketResponse.ok) throw new Error("Cannot authorize the Lab WebSocket");
  const { ticket } = await ticketResponse.json();
  const url = new URL(labWebSocketURL());
  url.searchParams.set("ticket", ticket);
  const ws = new WebSocket(url);
  const receive = () => new Promise<any>((resolve, reject) => {
    const timeout = window.setTimeout(() => { cleanup(); reject(new Error("Lab acknowledgement timed out")); }, 7000);
    const cleanup = () => { clearTimeout(timeout); ws.removeEventListener("message", message); ws.removeEventListener("close", closed); };
    const message = (m: MessageEvent) => { cleanup(); const d = JSON.parse(m.data); d.type === "error" ? reject(new Error(d.message)) : resolve(d); };
    const closed = () => { cleanup(); reject(new Error("Lab connection closed")); };
    ws.addEventListener("message", message); ws.addEventListener("close", closed);
  });
  try {
    await new Promise<void>((resolve, reject) => { ws.onopen = () => resolve(); ws.onerror = () => reject(new Error("Cannot connect to Lab WebSocket")); });
    let pending = receive();
    ws.send(JSON.stringify({ type: "hello", protocol: "arl/1", service: "demo/travel-agent", stage: "dev", lab: "agent", phases: ["input", "retrieval", "agent", "output"], capabilities: ["knowledge_graph", "tools", "images", "costs"], metadata: { simulated: true } }));
    await pending;
    const runId = crypto.randomUUID();
    const items: [string, string, Record<string, any>, string?, string?][] = [
      ["run.start", "input", { name: "Travel agent · simulated trace", checks: { max_cost_usd: 0.01, max_retries: 2, output_contains: ["Bandung"], required_nodes: ["city:bandung"], forbidden_tools: ["payments.charge"] } }],
      ["input", "input", { content: [{ type: "text", text: "Plan a quiet weekend in Bandung. Find a train and places to visit." }] }],
      ["span.start", "retrieval", { name: "Find relevant context", query: "Bandung weekend train attractions" }, "retrieve"],
      ["graph.node", "retrieval", { node_id: "query", label: "Weekend query", kind: "query" }, "retrieve"],
      ["graph.node", "retrieval", { node_id: "city:bandung", label: "Bandung", kind: "city" }, "retrieve"],
      ["graph.node", "retrieval", { node_id: "transport:train", label: "Train routes", kind: "transport" }, "retrieve"],
      ["graph.node", "retrieval", { node_id: "place:forest", label: "Forest park", kind: "place" }, "retrieve"],
      ["graph.node", "retrieval", { node_id: "place:cafe", label: "Quiet cafés", kind: "place" }, "retrieve"],
      ["graph.edge", "retrieval", { source: "query", target: "city:bandung", label: "about" }, "retrieve"],
      ["graph.edge", "retrieval", { source: "city:bandung", target: "transport:train", label: "reachable via" }, "retrieve"],
      ["graph.edge", "retrieval", { source: "city:bandung", target: "place:forest", label: "has place" }, "retrieve"],
      ["graph.edge", "retrieval", { source: "city:bandung", target: "place:cafe", label: "has place" }, "retrieve"],
      ["graph.visit", "retrieval", { node_id: "query", attempt: 1, depth: 0 }, "retrieve"],
      ["graph.visit", "retrieval", { node_id: "city:bandung", from_node: "query", attempt: 1, depth: 1, score: 0.94 }, "retrieve"],
      ["graph.visit", "retrieval", { node_id: "transport:train", from_node: "city:bandung", attempt: 1, depth: 2, score: 0.51 }, "retrieve"],
      ["retrieval.retry", "retrieval", { attempt: 2, reason: "Train timetable is stale; broaden search" }, "retrieve"],
      ["graph.visit", "retrieval", { node_id: "city:bandung", attempt: 2, depth: 1, score: 0.98 }, "retrieve"],
      ["graph.visit", "retrieval", { node_id: "place:forest", from_node: "city:bandung", attempt: 2, depth: 2, score: 0.96, selected: true }, "retrieve"],
      ["graph.visit", "retrieval", { node_id: "place:cafe", from_node: "city:bandung", attempt: 2, depth: 2, score: 0.88, selected: true }, "retrieve"],
      ["retrieval.result", "retrieval", { content: [{ type: "text", text: "Selected: forest park, quiet cafés, train route. Context grounded in 3 graph nodes." }], node_ids: ["place:forest", "place:cafe", "transport:train"] }, "retrieve"],
      ["span.end", "retrieval", { duration_ms: 320 }, "retrieve"],
      ["span.start", "agent", { name: "Planner iteration 1", iteration: 1 }, "loop-1"],
      ["model.request", "agent", { model: "simulated/planner", content: [{ type: "text", text: "Use retrieved context. Verify train availability before answering." }], iteration: 1 }, "model-1", "loop-1"],
      ["model.response", "agent", { model: "simulated/planner", content: [{ type: "text", text: "Check train availability for the weekend." }], tokens_in: 650, tokens_out: 85 }, "model-1", "loop-1"],
      ["tool.call", "agent", { tool: "trains.search", call_id: "call-1", arguments: { destination: "Bandung", date: "next Saturday" } }, "tool-1", "loop-1"],
      ["tool.result", "agent", { call_id: "call-1", status: "ok", duration_ms: 180, result: { departures: ["07:30", "09:00"] } }, "tool-1", "loop-1"],
      ["cost.record", "agent", { billing_id: "model-1", model: "simulated/planner", tokens_in: 650, tokens_out: 85, amount_usd: 0.00082, source: "estimated", simulated: true }, "model-1"],
      ["cost.record", "agent", { billing_id: "call-1", category: "tool", amount_usd: 0, source: "reported", simulated: true }, "tool-1"],
      ["model.request", "agent", { model: "simulated/planner", content: [{ type: "text", text: "Summarize the verified itinerary." }], iteration: 2 }, "model-2", "loop-1"],
      ["model.response", "agent", { model: "simulated/planner", content: [{ type: "text", text: "The itinerary is ready, with a verified train and two quiet stops." }], iteration: 2 }, "model-2", "loop-1"],
      ["cost.record", "agent", { billing_id: "model-2", model: "simulated/planner", tokens_in: 820, tokens_out: 150, amount_usd: 0.0012, source: "estimated", simulated: true }, "model-2"],
      ["span.end", "agent", { duration_ms: 900 }, "loop-1"],
      ["output", "output", { content: [{ type: "text", text: "Take the 07:30 train to Bandung on Saturday. Visit the forest park, then spend the afternoon in a quiet café. Return on Sunday." }] }],
      ["evaluation", "output", { name: "Grounded itinerary (simulated)", passed: true, evidence: { selected_nodes: 3, verified_departures: 2 } }],
      ["run.end", "output", { status: "completed", duration_ms: 1450 }],
    ];
    for (const [event, phase, data, span_id, parent_span_id] of items) {
      pending = receive();
      ws.send(JSON.stringify({ type: "event", event_id: crypto.randomUUID(), run_id: runId, event, phase, data, span_id, parent_span_id, timestamp: Date.now() / 1000 }));
      const ack = await pending;
      if (event === "run.start") onTrace(ack.trace_id);
      await new Promise((r) => setTimeout(r, 100));
    }
  } finally { ws.close(); }
}
