import { useEffect, useMemo, useState } from "react";
import { api } from "../api";
import { demoTrace, labWebSocketURL, LabRun, LabStatus, RunDetail, TraceEvent } from "../agentLab";
import { FeedbackPanel } from "../components/Feedback";
import { Badge, Card, ErrorBox, Icon, useToast } from "../components/ui";
import { ago, num } from "../format";
import { useApi } from "../hooks";
import { navigate, useLocation } from "../router";

const pretty = (value: unknown) => JSON.stringify(value, null, 2);
const money = (value: number) => `$${value.toFixed(6)}`;
const tone = (status: string) => status === "completed" || status === "pass" ? "good" : status === "failed" || status === "fail" ? "bad" : "warn";

function Content({ data }: { data: Record<string, any> }) {
  return <>{Array.isArray(data.content) && data.content.map((part: any, i: number) => <div key={i} className="al-content">
    {part.type === "text" ? <div className="al-text">{part.text}</div> : part.type === "image" ?
      <a href={`/api/artifacts/${part.artifact}`} target="_blank" rel="noreferrer"><img className="al-image" loading="lazy" src={`/api/artifacts/${part.artifact}`} alt={part.alt || "Trace attachment"} /></a> :
      part.type === "json" ? <pre className="al-json">{pretty(part.value)}</pre> : <div className="muted">{part.type} attachment · {String(part.name || part.mime || "Inspect payload for reference")}</div>}
  </div>)}</>;
}

function Graph({ events, onSelect }: { events: TraceEvent[]; onSelect: (e: TraceEvent) => void }) {
  const [picked, setPicked] = useState("");
  const graph = useMemo(() => {
    const nodes = new Map<string, { id: string; label: string; visits: number; depth: number; selected: boolean; event: TraceEvent }>();
    const edges = new Map<string, { source: string; target: string; label: string; traversals: number }>();
    const ensure = (id: string, e: TraceEvent) => {
      if (!nodes.has(id)) nodes.set(id, { id, label: id, visits: 0, depth: 0, selected: false, event: e });
      return nodes.get(id)!;
    };
    let last = "";
    for (const e of events) {
      const d = e.data;
      if (e.event === "graph.node" || e.event === "graph.visit") {
        const n = ensure(d.node_id, e); n.event = e;
        if (d.label) n.label = String(d.label);
        if (e.event === "graph.visit") { n.visits++; last = n.id; }
        if (typeof d.depth === "number") n.depth = Math.min(d.depth, 12);
        if (d.selected) n.selected = true;
      }
      if (e.event === "graph.edge" || (e.event === "graph.visit" && d.from_node)) {
        const source = d.source || d.from_node, target = d.target || d.node_id;
        ensure(source, e); ensure(target, e);
        const key = `${source}\0${target}`;
        const edge = edges.get(key) || { source, target, label: "", traversals: 0 };
        if (d.label) edge.label = String(d.label);
        if (e.event === "graph.visit") edge.traversals++;
        edges.set(key, edge);
      }
    }
    const visible = [...nodes.values()].slice(0, 120);
    const groups = new Map<number, typeof visible>();
    // Explicit hop depth produces columns; otherwise distribute nodes over three columns.
    const hasDepth = visible.some((n) => n.depth > 0);
    visible.forEach((n, i) => { const col = hasDepth ? n.depth : i % 3; groups.set(col, [...(groups.get(col) || []), n]); });
    const height = Math.max(260, Math.max(0, ...[...groups.values()].map((g) => g.length)) * 82 + 50);
    const width = Math.max(680, (Math.max(0, ...groups.keys()) + 1) * 220);
    const positions = new Map<string, { x: number; y: number }>();
    groups.forEach((group, col) => group.forEach((n, i) => positions.set(n.id, { x: 100 + col * 220, y: (i + 1) * height / (group.length + 1) })));
    return { nodes: visible, edges: [...edges.values()], positions, width, height, last, total: nodes.size };
  }, [events]);
  if (!graph.nodes.length) return <div className="al-empty"><Icon name="search" size={30} /><b>Waiting for graph evidence</b><span>Send graph.node, graph.edge and graph.visit events from your retriever. Vector and custom retrieval events appear in the timeline.</span></div>;
  return <><div className="al-graph-scroll graticule"><svg role="img" aria-label="Knowledge graph traversal. Select a node to inspect its evidence." viewBox={`0 0 ${graph.width} ${graph.height}`} style={{ minWidth: graph.width, width: "100%", height: graph.height }}>
    <defs><marker id="al-arrow" viewBox="0 0 10 10" refX="29" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="currentColor" /></marker></defs>
    {graph.edges.map((edge, i) => { const a = graph.positions.get(edge.source), b = graph.positions.get(edge.target); if (!a || !b) return null;
      return <g key={i} className={edge.traversals ? "al-edge visited" : "al-edge"}><path d={edge.source === edge.target ? `M${a.x},${a.y} c80,-90 -80,-90 0,0` : `M${a.x},${a.y} Q${(a.x + b.x) / 2},${(a.y + b.y) / 2 - 15} ${b.x},${b.y}`} fill="none" stroke="currentColor" strokeWidth={edge.traversals ? 2.5 : 1} markerEnd="url(#al-arrow)" /><text x={(a.x + b.x) / 2} y={(a.y + b.y) / 2 - 20} textAnchor="middle">{edge.label.slice(0, 25)}{edge.traversals ? ` · ${edge.traversals}×` : ""}</text></g>;
    })}
    {graph.nodes.map((node) => { const p = graph.positions.get(node.id)!; return <g key={node.id} role="button" tabIndex={0} aria-label={`${node.label}, ${node.visits} visits`} className={`al-node ${node.visits ? "visited" : ""} ${picked === node.id || graph.last === node.id ? "current" : ""}`} transform={`translate(${p.x},${p.y})`} onClick={() => { setPicked(node.id); onSelect(node.event); }} onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setPicked(node.id); onSelect(node.event); } }}>
      <title>{node.id} · {node.visits} visits{node.selected ? " · selected context" : ""}</title><circle r="24" /><text textAnchor="middle" dy="5" className="al-node-count">{node.visits || "·"}</text><text textAnchor="middle" y="43">{node.label.length > 25 ? node.label.slice(0, 23) + "…" : node.label}{node.selected ? " ✓" : ""}</text>
    </g>; })}
  </svg></div><div className="al-legend">Number = visits · highlighted paths = traversed edges · ✓ = selected context · outlined node = latest visit or selected node{graph.total > 120 && ` · Showing 120 / ${graph.total} nodes; all evidence is in the event timeline and export.`}</div></>;
}

function RunView({ id }: { id: string }) {
  const { data: run, error } = useApi<RunDetail>(`/api/agent-lab/runs/${id}`, { refreshMs: 1600 });
  const [events, setEvents] = useState<TraceEvent[]>([]);
  const [eventError, setEventError] = useState<Error | null>(null);
  const [selected, setSelected] = useState<TraceEvent | null>(null);
  const [phase, setPhase] = useState("");
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState<number | null>(null);
  const [graphSpan, setGraphSpan] = useState("");
  const [tab, setTab] = useState("timeline");
  const [page, setPage] = useState(0);
  const [opening, setOpening] = useState(false);
  const toast = useToast();
  /** Saves this trace (or one LLM step of it) as an editable Workbench scenario. */
  const openInWorkbench = async (step = "") => {
    setOpening(true);
    try { const s = await api.post<{ id: string }>("/api/workbench/from-trace", { trace_id: id, step, save: true }); navigate(`/workbench?scenario=${encodeURIComponent(s.id)}`); }
    catch (e) { toast((e as Error).message, "bad"); } finally { setOpening(false); }
  };
  useEffect(() => {
    let disposed = false, timer = 0, after = 0;
    async function poll() {
      try {
        let more = true;
        while (more && !disposed) {
          const res = await api.get<{ items: TraceEvent[]; has_more: boolean }>(`/api/agent-lab/runs/${id}/events?after=${after}&limit=1000`);
          if (disposed) return;
          if (res.items.length) { after = res.items[res.items.length - 1].ordinal; setEvents((old) => [...old, ...res.items]); }
          more = res.has_more;
        }
        setEventError(null);
      } catch (e) { if (!disposed) setEventError(e as Error); }
      if (!disposed) timer = window.setTimeout(poll, 1200);
    }
    poll();
    return () => { disposed = true; clearTimeout(timer); };
  }, [id]);
  const visible = cursor === null ? events : events.slice(0, cursor);
  const filtered = useMemo(() => visible.filter((e) => (!phase || e.phase === phase) && (!query || `${e.event} ${e.span_id || ""} ${pretty(e.data)}`.toLowerCase().includes(query.toLowerCase()))), [visible, phase, query]);
  const graphEvents = visible.filter((e) => !graphSpan || e.span_id === graphSpan);
  const graphSpans = [...new Set(events.filter((e) => e.event.startsWith("graph.")).map((e) => e.span_id).filter(Boolean))];
  useEffect(() => setPage(0), [phase, query, cursor]);
  if (error) return <ErrorBox error={error} />;
  if (!run) return <div className="al-empty">Loading trace…</div>;
  const summary = run.summary, cost = summary.cost;
  const shown = selected || visible[visible.length - 1];
  const costs = visible.filter((e) => e.event === "cost.record");
  const spans = visible.filter((e) => e.event === "span.start");
  return <div className="al-run">
    <div className="row wrap"><div className="grow"><h2 className="al-run-title">{run.name}</h2><div className="mono muted">{run.service} / {run.external_id}{run.source === "otlp" ? " · OpenTelemetry" : ""}</div>{(run.thread_id || run.user_id || !!run.tags?.length) && <div className="row wrap" style={{ gap: 4, marginTop: 4 }}>{run.thread_id && <a className="tag mono" href={`/monitor?thread=${encodeURIComponent(run.thread_id)}`} title="conversation thread">⇄ {run.thread_id}</a>}{run.user_id && <span className="tag mono">user {run.user_id}</span>}{run.tags?.map((t) => <span key={t} className="tag">{t}</span>)}</div>}</div><Badge tone={tone(run.status)}>{run.status}</Badge><Badge tone={tone(summary.verdict)}>{summary.verdict.replaceAll("_", " ")}</Badge><button className="btn sm" disabled={opening} onClick={() => openInWorkbench()}><Icon name="flask" />Open in Workbench</button><a className="btn sm" href={`/api/agent-lab/runs/${id}/export`} download><Icon name="download" />Export JSON</a></div>
    {run.config.metadata?.simulated && <div className="al-notice">SIMULATED TRACE · Protocol demonstration. Costs and timings are illustrative; no AI provider is called.</div>}
    <div className="al-stats">
      <div><span>Trace events</span><b>{num(summary.events)}</b></div><div><span>Graph visits / nodes</span><b>{summary.visits} / {summary.unique_nodes}</b></div><div><span>Retries / tool calls</span><b>{summary.retries} / {summary.tool_calls}</b></div><div><span>Duration</span><b>{(summary.duration_ms / 1000).toFixed(2)}s</b></div><div><span>Known cost (USD)</span><b>{cost.entries ? money(cost.known_usd) : "Unreported"}</b><small>{cost.unknown_entries} unknown · {cost.estimated_entries} estimated</small></div>
    </div>
    <Card title="Execution flow" sub="Select a phase to filter the timeline. Custom phases come directly from the handshake.">
      <div className="al-phases">{run.config.phases.map((p, i) => <button key={p} className={`al-phase ${phase === p ? "selected" : ""}`} onClick={() => setPhase(phase === p ? "" : p)}><span className="mono">0{i + 1}</span><b>{p}</b><span>{summary.phases[p] || 0} events</span><Icon name="arrow" /></button>)}</div>
      <div className="al-replay"><button className={`btn sm ${cursor === null ? "primary" : ""}`} onClick={() => setCursor(null)}><Icon name="bolt" />Follow live</button><label htmlFor="al-replay">Replay</label><input id="al-replay" aria-label="Replay trace events" type="range" min={0} max={events.length} value={cursor ?? events.length} onChange={(e) => { setCursor(Number(e.target.value)); setSelected(null); }} /><span className="mono">{cursor ?? events.length} / {events.length}</span></div>
    </Card>
    <div className="al-evidence">{["input", "output"].map((kind) => <Card key={kind} title={kind === "input" ? "Input & attachments" : "Output & attachments"} sub={cursor === null ? "Latest evidence in this trace" : "Evidence at replay position"}>
      {visible.filter((e) => e.event === kind).length ? visible.filter((e) => e.event === kind).map((e) => <div key={e.ordinal}><Content data={e.data} />{!e.data.content?.length && <pre className="al-json">{pretty(e.data)}</pre>}</div>) : <p className="muted">No {kind} event received at this position.</p>}
    </Card>)}</div>
    <Card title="Context retrieval · knowledge graph" sub="Replay shows only the evidence received up to that event. Repeated visits and retrieval retries remain visible." flush actions={<select className="input" aria-label="Retrieval span" value={graphSpan} onChange={(e) => setGraphSpan(e.target.value)}><option value="">All retrieval spans</option>{graphSpans.map((s) => <option key={s} value={s}>{s}</option>)}</select>}>
      <Graph events={graphEvents} onSelect={setSelected} />
      <div className="al-visits">{graphEvents.filter((e) => e.event === "graph.visit" || e.event.includes("retry")).map((e) => <button key={e.ordinal} className={`btn sm ${e.event.includes("retry") ? "warn" : ""}`} onClick={() => setSelected(e)} title={pretty(e.data)}>{e.event.includes("retry") ? `↻ Retry ${e.data.attempt ?? ""}` : e.data.node_id}<small>{e.data.score != null ? ` · ${e.data.score}` : ""}</small></button>)}</div>
    </Card>
    <div className="al-tabs" role="tablist" aria-label="Trace evidence">{["timeline", "spans", "costs", "checks"].map((t) => <button key={t} className={`btn ${tab === t ? "primary" : ""}`} role="tab" aria-selected={tab === t} onClick={() => setTab(t)}>{t === "timeline" ? "Event timeline" : t === "spans" ? "Agent & tool spans" : t === "costs" ? "Cost ledger" : "Checks & evidence"}</button>)}</div>
    {tab === "timeline" && <div className="al-evidence"><Card title="Event timeline" sub="Request, response, tool calls, retries, chunks and custom events." actions={<input className="input" aria-label="Search events" placeholder="Search event or payload…" value={query} onChange={(e) => setQuery(e.target.value)} />}>
      {eventError && <ErrorBox error={eventError} />}
      <div className="row wrap al-filter"><button className="btn sm" onClick={() => setPhase("")}>All phases</button>{phase && <Badge>{phase}</Badge>}<span className="muted">{filtered.length} events</span></div>
      <div className="al-events">{filtered.slice(page * 100, (page + 1) * 100).map((e) => <button key={e.ordinal} className={`al-event ${shown?.event_id === e.event_id ? "selected" : ""}`} onClick={() => setSelected(e)}><span className="mono faint">#{e.ordinal}</span><span className="grow"><b>{e.event}</b><small>{e.phase}{e.span_id ? ` / ${e.span_id}` : ""}</small></span><span className="mono muted">+{((e.received_at - run.started_at) * 1000).toFixed(0)}ms</span></button>)}</div>
      {!filtered.length && <div className="al-empty">No events at this replay position or filter.</div>}
      {filtered.length > 100 && <div className="row al-filter"><button className="btn sm" disabled={!page} onClick={() => setPage(page - 1)}>Previous</button><span>{page + 1} / {Math.ceil(filtered.length / 100)}</span><button className="btn sm" disabled={(page + 1) * 100 >= filtered.length} onClick={() => setPage(page + 1)}>Next</button></div>}
    </Card><Card title={shown?.event || "Event inspector"} sub={shown ? `Received ${new Date(shown.received_at * 1000).toLocaleTimeString()} · ${shown.phase}` : "Select a timeline event or graph node"}>
      {shown && <>{shown.event === "model.request" && shown.data.request && <button className="btn sm" disabled={opening} onClick={() => openInWorkbench(shown.span_id || "")}><Icon name="flask" />Edit & re-send this LLM step</button>}<Content data={shown.data} /><div className="mono muted">span: {shown.span_id || "—"} · parent: {shown.parent_span_id || "—"}</div><pre className="al-json">{pretty(shown)}</pre></>}
    </Card></div>}
    {tab === "spans" && <Card title="Agent loop & tool calls" sub="Use span.start / span.end and parent_span_id to describe nested or parallel operations.">
      {!spans.length && <p className="muted">No spans emitted. Tool calls and model events are still visible in the timeline.</p>}
      {spans.map((e) => { const end = visible.find((v) => v.event === "span.end" && v.span_id === e.span_id); return <div key={e.ordinal} className="al-span" style={{ marginLeft: e.parent_span_id ? 24 : 0 }}><div className="row wrap"><b>{e.data.name || e.span_id}</b><Badge>{e.phase}</Badge><span className="mono muted">{e.parent_span_id ? `parent: ${e.parent_span_id}` : "root"}</span><span className="spacer" /><span>{end ? `${end.data.duration_ms ?? ((end.received_at - e.received_at) * 1000).toFixed(0)} ms` : "open"}</span></div>{visible.filter((v) => v.span_id === e.span_id || v.parent_span_id === e.span_id).filter((v) => !v.event.startsWith("span.")).map((v) => <details key={v.ordinal}><summary>{v.event} · {v.data.tool || v.data.call_id || v.data.model || v.span_id}</summary><Content data={v.data} /><pre className="al-json">{pretty(v.data)}</pre></details>)}</div>; })}
      <h3>Tool exchanges</h3>{visible.filter((e) => e.event === "tool.call").map((e) => { const result = visible.find((v) => v.event === "tool.result" && v.data.call_id === e.data.call_id); return <details key={e.ordinal} className="al-span"><summary>{e.data.tool} · {e.data.call_id} · {result?.data.status || (result ? "returned" : "awaiting result")}</summary><pre className="al-json">{pretty({ arguments: e.data.arguments, result: result?.data })}</pre></details>; })}
    </Card>}
    {tab === "costs" && <Card title="Cost ledger · USD" sub="One cost.record per billable operation, including failed attempts. Token usage is counted from this ledger only.">
      <div className="row wrap"><Badge>{num(cost.tokens_in)} input tokens</Badge><Badge>{num(cost.tokens_out)} output tokens</Badge><Badge tone={cost.unknown_entries ? "warn" : "good"}>{cost.entries ? `${money(cost.known_usd)} known subtotal` : "No cost evidence received"}</Badge></div>
      {!!cost.unreported_operations.length && <p className="warn">Missing cost.record for: {cost.unreported_operations.join(", ")}. Use the model request span_id or tool call_id as billing_id, including a zero-cost record for free operations.</p>}
      <div className="al-table-wrap"><table className="al-table"><thead><tr><th>Billing operation</th><th>Model / category</th><th>Phase</th><th>Input / output</th><th>Cost</th><th>Source</th></tr></thead><tbody>{costs.map((e) => <tr key={e.ordinal}><td>{e.data.billing_id}</td><td>{e.data.model || e.data.category || "—"}</td><td>{e.phase}</td><td>{e.data.tokens_in ?? "—"} / {e.data.tokens_out ?? "—"}</td><td className="mono">{e.data.amount_usd == null ? "Unknown" : money(e.data.amount_usd)}</td><td><Badge tone={e.data.source === "unknown" ? "warn" : ""}>{e.data.source}</Badge></td></tr>)}</tbody></table></div>
      <p className="muted">Replay filters the ledger rows. Summary totals and check verdicts always describe the full trace. Configure model rates in Listener settings; each estimate keeps the rates used at ingestion.</p>
    </Card>}
    {tab === "checks" && <Card title="Checks & evidence" sub="Execution completion and test success are evaluated separately. Missing cost evidence cannot pass a cost gate.">
      {!summary.checks.length && <div className="al-empty">No checks configured. Add defaults in Listener settings, send checks with run.start, or emit evaluation events.</div>}
      {summary.checks.map((c, i) => <details key={i} className="al-span"><summary><Badge tone={c.passed === true ? "good" : c.passed === false ? "bad" : "warn"}>{c.passed === true ? "PASS" : c.passed === false ? "FAIL" : "PENDING / UNKNOWN"}</Badge> {c.name}</summary><pre className="al-json">{pretty(c.evidence)}</pre></details>)}
      <details className="al-span"><summary>Frozen checks for this run</summary><pre className="al-json">{pretty(run.check_config)}</pre></details>
    </Card>}
    {tab === "checks" && <FeedbackPanel traceId={id} />}
  </div>;
}

export default function AgentLab() {
  const { query } = useLocation();
  const chosen = query.get("run") || "";
  const { data: status, error: statusError, reload: refreshStatus } = useApi<LabStatus>("/api/agent-lab/status", { refreshMs: 2000 });
  const { data: sessions, reload: refreshSessions } = useApi<any[]>("/api/agent-lab/sessions", { refreshMs: 2500 });
  const [serviceFilter, setServiceFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const { data: runs, error: runsError, reload: refreshRuns } = useApi<LabRun[]>(`/api/agent-lab/runs?limit=200&service_name=${encodeURIComponent(serviceFilter)}&status=${statusFilter}`, { refreshMs: 1600 });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<Error | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [configText, setConfigText] = useState("");
  const [rateModel, setRateModel] = useState("");
  const [rateIn, setRateIn] = useState(0);
  const [rateOut, setRateOut] = useState(0);
  const draft = useMemo(() => { try { return JSON.parse(configText); } catch { return null; } }, [configText]);
  const editConfig = (edit: (c: any) => void) => {
    try { const c = JSON.parse(configText); edit(c); setConfigText(pretty(c)); } catch { setErr(new Error("Correct the configuration JSON before editing the form")); }
  };
  const toast = useToast();
  const select = (id: string) => navigate(`/agent-lab?run=${encodeURIComponent(id)}`);
  const action = async (fn: () => Promise<unknown>) => {
    setBusy(true); setErr(null);
    try { await fn(); refreshStatus(); refreshRuns(); refreshSessions(); } catch (e) { setErr(e as Error); } finally { setBusy(false); }
  };
  const endpoint = labWebSocketURL();
  return <div className="page">
    <div className="page-head"><div className="grow"><div className="eyebrow">Connected service testing</div><h1 className="page-title">Agent lab</h1><div className="page-sub">Inspect the whole journey: input, retrieved context, agent decisions, tools and output. Connect any backend through one versioned WebSocket protocol.</div></div><div className="row wrap"><button disabled={busy || !status} className={`btn ${status?.listening ? "" : "primary"}`} onClick={() => action(() => api.post(`/api/agent-lab/listener/${status?.listening ? "stop" : "start"}`))}><Icon name={status?.listening ? "stop" : "play"} />{status?.listening ? "Stop listening" : "Start listening"}</button><button className="btn" onClick={() => { setConfigText(pretty(status?.config || { pricing: {}, checks: {} })); setSettingsOpen(!settingsOpen); }}><Icon name="settings" />Listener settings</button></div></div>
    {(err || statusError || runsError) && <ErrorBox error={err || statusError || runsError} />}
    <Card title={<div className="row"><span className={`led ${status?.listening ? "al-live" : ""}`} />{status?.listening ? "Listening for backend services" : "Listener stopped"}</div>} sub={`${status?.active_connections || 0} live connections · arl/1 · STAGE=dev`} actions={<button className="btn sm" disabled={busy || !status} onClick={() => action(async () => { if (!status?.listening) await api.post("/api/agent-lab/listener/start"); await demoTrace(select); toast("Simulated trace captured through WebSocket", "good"); })}><Icon name="flask" />{busy ? "Working…" : "Run protocol demo"}</button>}>
      <div className="al-connect"><code>LAB_WS_URL={endpoint}<br />STAGE=dev</code><button className="btn sm" onClick={() => { navigator.clipboard.writeText(`LAB_WS_URL=${endpoint}\nSTAGE=dev`).then(() => toast("Connection environment copied")).catch(() => setErr(new Error("Clipboard unavailable; copy the displayed environment manually"))); }}><Icon name="copy" />Copy env</button></div><p className="muted al-hint">Start listening, then connect from your backend before each job. The handshake declares the lab type and phases. Integration reference: <a href="/api/agent-lab/connection.md" target="_blank" rel="noreferrer">connection.md</a>. In Vite development the /api WebSocket proxy forwards to port 8000.</p>
      {sessions && sessions.length > 0 && <details className="al-session-list"><summary>Connected services & session history ({sessions.length})</summary>{sessions.map((s) => <div key={s.id} className="row wrap al-session"><Badge tone={s.connected ? "good" : ""}>{s.connected ? "connected" : "offline"}</Badge><b>{s.service}</b><span className="mono muted">{s.config.lab} · {s.id}</span><span className="spacer" /><span className="muted">{ago(s.last_seen)}</span></div>)}</details>}
    </Card>
    {settingsOpen && <Card title="Listener settings" sub="Persisted locally. Default checks are frozen at run.start; pricing changes apply only to new cost records. All prices are USD per million tokens.">
      <div className="al-settings-grid"><div><h3>Model pricing</h3><p className="muted">Use the exact model name emitted by your service. Each stored estimate retains its own rates.</p>
        <div className="al-table-wrap"><table className="al-table"><thead><tr><th>Model</th><th>Input / 1M</th><th>Output / 1M</th><th /></tr></thead><tbody>{Object.entries(draft?.pricing || {}).map(([model, price]: [string, any]) => <tr key={model}><td className="mono">{model}</td><td>{price?.input_per_million}</td><td>{price?.output_per_million}</td><td><button className="btn sm" aria-label={`Remove pricing for ${model}`} onClick={() => editConfig((c) => { delete c.pricing[model]; })}><Icon name="x" /></button></td></tr>)}</tbody></table></div>
        <div className="al-rate-form"><label>Model<input className="input" placeholder="provider/model" value={rateModel} onChange={(e) => setRateModel(e.target.value)} /></label><label>Input USD / 1M<input className="input" type="number" min="0" step="any" value={rateIn} onChange={(e) => setRateIn(Number(e.target.value))} /></label><label>Output USD / 1M<input className="input" type="number" min="0" step="any" value={rateOut} onChange={(e) => setRateOut(Number(e.target.value))} /></label></div><button className="btn sm" disabled={!rateModel.trim() || rateIn < 0 || rateOut < 0 || !Number.isFinite(rateIn + rateOut)} onClick={() => { editConfig((c) => { c.pricing = { ...c.pricing, [rateModel.trim()]: { input_per_million: rateIn, output_per_million: rateOut } }; }); setRateModel(""); }}>Add / update model rate</button>
      </div><div><h3>Default run checks</h3><p className="muted">A backend can override individual defaults at run.start. Empty limits disable that check.</p><div className="al-check-form">
        {[["max_cost_usd", "Maximum cost (USD)"], ["max_latency_ms", "Maximum latency (ms)"], ["max_retries", "Maximum retries"]].map(([key, label]) => <label key={key}>{label}<input className="input" type="number" min="0" step={key === "max_retries" ? "1" : "any"} value={draft?.checks?.[key] ?? ""} onChange={(e) => editConfig((c) => { c.checks = { ...c.checks, [key]: e.target.value === "" ? null : Number(e.target.value) }; })} /></label>)}
        {[["output_contains", "Required output text (one per line)"], ["required_nodes", "Required graph node IDs (one per line)"], ["forbidden_tools", "Forbidden tools (one per line)"]].map(([key, label]) => <label key={key}>{label}<textarea className="input mono" value={Array.isArray(draft?.checks?.[key]) ? draft.checks[key].join("\n") : ""} onChange={(e) => editConfig((c) => { c.checks = { ...c.checks, [key]: e.target.value.split("\n") }; })} /></label>)}
      </div></div></div>
      <details className="al-span"><summary>Advanced configuration JSON</summary><textarea className="input mono al-config" aria-label="Listener configuration JSON" value={configText} onChange={(e) => setConfigText(e.target.value)} spellCheck={false} /></details><button className="btn primary" disabled={busy} onClick={() => action(async () => { const config = JSON.parse(configText); for (const key of ["output_contains", "required_nodes", "forbidden_tools"]) { if (Array.isArray(config.checks?.[key])) config.checks[key] = config.checks[key].filter((line: unknown) => line !== ""); } await api.post("/api/agent-lab/config", config); toast("Listener configuration saved", "good"); setSettingsOpen(false); })}>Save settings</button>
    </Card>}
    <div className="al-workspace"><aside><Card title="Captured runs" sub="Independent traces across agents and backend services.">
      <div className="al-run-filters"><select className="input" aria-label="Filter by service" value={serviceFilter} onChange={(e) => setServiceFilter(e.target.value)}><option value="">All services</option>{[...new Set((sessions || []).map((s) => s.service))].map((s) => <option key={s}>{s}</option>)}</select><select className="input" aria-label="Filter by status" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}><option value="">All statuses</option>{["running", "completed", "failed", "cancelled", "interrupted"].map((s) => <option key={s}>{s}</option>)}</select></div>
      <div className="al-run-list">{runs?.map((r) => <button key={r.id} className={`al-run-item ${chosen === r.id ? "selected" : ""}`} onClick={() => select(r.id)}><div className="row"><b className="grow">{r.name}</b><span className={tone(r.status)}>●</span></div><span className="muted">{r.service}</span><small className="mono">{r.lab} · {r.event_count} events · {ago(r.started_at)}</small></button>)}</div>
      {!runs?.length && <p className="muted">No matching runs yet. Connect a backend or run the free protocol demo.</p>}
    </Card></aside><section className="al-detail">{chosen ? <RunView key={chosen} id={chosen} /> : <Card title="From backend to evidence"><div className="al-empty"><Icon name="campaign" size={42} /><h2>See every step your agent takes.</h2><span>Start the listener and send a hello handshake. Each job becomes an independent trace with graph traversal, agent loop, media, cost ledger and test evidence.</span><div className="al-empty-flow"><span>Input</span><Icon name="arrow" /><span>Context retrieval</span><Icon name="arrow" /><span>Agent loop</span><Icon name="arrow" /><span>Output</span></div><p className="muted">The demo uses this same WebSocket path and makes no paid requests.</p></div></Card>}</section></div>
  </div>;
}
