import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { Badge, Card, ErrorBox, Icon, Seg, Tabs, useToast } from "../components/ui";
import { ago, ms, num, usd } from "../format";
import { invalidate, useApi } from "../hooks";
import { navigate, useLocation } from "../router";
import type { LabRun } from "../agentLab";

type Mode = "agent" | "llm";
interface Scenario {
  id?: string; name: string; collection: string; service: string; source_trace_id?: string | null; mode: Mode;
  input: unknown; state: Record<string, any>; config: Record<string, any>; checks: Record<string, any>;
  llm: { messages?: any[]; tools?: any; tool_choice?: any; baseline?: { response?: string; tokens_in?: number; tokens_out?: number }; span_id?: string };
  steps?: any[]; snapshots?: number; notes?: string; updated_at?: number;
}
interface WbRun {
  id: string; scenario_id: string; mode: Mode; status: string; created_at: number; finished_at?: number; error?: string;
  request: Scenario; target?: Record<string, any>; output?: string; verdict?: string; trace_id?: string;
  checks?: { name: string; passed: boolean | null; evidence: any }[];
  result?: { latency_ms?: number; tokens_in?: number; tokens_out?: number; cost_usd?: number; resolved_model?: string; tool_calls?: unknown };
}
interface Peer { session_id: string; service: string; capabilities: string[]; replay: boolean; connected_at: number }

const TERMINAL = ["completed", "failed", "cancelled", "rejected"];
const pretty = (v: unknown) => JSON.stringify(v ?? null, null, 2);
const tone = (s?: string) => s === "completed" || s === "pass" ? "good" : s === "failed" || s === "fail" || s === "rejected" ? "bad" : "warn";
const blank = (): Scenario => ({ name: "Untitled scenario", collection: "Unsorted", service: "", mode: "agent", input: "", state: {}, config: { model: "", system_prompt: "", params: { temperature: 0.2 } }, checks: {}, llm: { messages: [] } });
const asText = (v: unknown) => typeof v === "string" ? v : pretty(v);

/** Textarea that edits a JSON value and only commits when it parses. */
function JsonField({ label, value, onChange, rows = 10, hint }: { label: string; value: unknown; onChange: (v: any) => void; rows?: number; hint?: string }) {
  const [textValue, setText] = useState(pretty(value));
  const [error, setError] = useState("");
  const last = useRef(pretty(value));
  useEffect(() => { const next = pretty(value); if (next !== last.current) { setText(next); last.current = next; setError(""); } }, [value]);
  return <label className="field">
    <span className="wb-label">{label}{error && <span className="bad"> · {error}</span>}</span>
    <textarea className="textarea mono wb-code" rows={rows} spellCheck={false} value={textValue} onChange={(e) => {
      setText(e.target.value);
      try { const parsed = JSON.parse(e.target.value); last.current = pretty(parsed); setError(""); onChange(parsed); } catch (err) { setError((err as Error).message.replace(/^JSON\.parse: /, "")); }
    }} />
    {hint && <span className="muted wb-hint">{hint}</span>}
  </label>;
}

/** Word-level diff for comparing two outputs. */
function Diff({ a, b }: { a: string; b: string }) {
  const parts = useMemo(() => {
    const x = a.split(/(\s+)/).slice(0, 800), y = b.split(/(\s+)/).slice(0, 800);
    const dp = Array.from({ length: x.length + 1 }, () => new Uint16Array(y.length + 1));
    for (let i = x.length - 1; i >= 0; i--) for (let j = y.length - 1; j >= 0; j--) dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    const out: { t: string; k: "same" | "del" | "ins" }[] = [];
    let i = 0, j = 0;
    while (i < x.length && j < y.length) {
      if (x[i] === y[j]) { out.push({ t: x[i], k: "same" }); i++; j++; } else if (dp[i + 1][j] >= dp[i][j + 1]) out.push({ t: x[i++], k: "del" }); else out.push({ t: y[j++], k: "ins" });
    }
    while (i < x.length) out.push({ t: x[i++], k: "del" });
    while (j < y.length) out.push({ t: y[j++], k: "ins" });
    return out;
  }, [a, b]);
  return <div className="al-text wb-diff">{parts.map((p, i) => p.k === "same" ? <span key={i}>{p.t}</span> : <span key={i} className={p.k === "del" ? "diff-del" : "diff-ins"}>{p.t}</span>)}</div>;
}

function RunCard({ run, label }: { run: WbRun; label?: string }) {
  const r = run.result || {};
  return <div className="wb-result">
    <div className="row wrap">
      {label && <b className="wb-label">{label}</b>}
      <Badge tone={tone(run.status)} dot>{run.status}</Badge>
      {run.verdict && run.verdict !== "no_checks" && <Badge tone={tone(run.verdict)}>{run.verdict.replaceAll("_", " ")}</Badge>}
      <Badge mono>{run.mode === "llm" ? "LLM step" : "agent replay"}</Badge>
      <span className="mono muted trunc">{run.request.config?.model || r.resolved_model || "default model"}</span>
      <span className="spacer" />
      {run.trace_id && <a className="btn sm" href={`/agent-lab?run=${encodeURIComponent(run.trace_id)}`}><Icon name="eye" size={13} />Trace</a>}
    </div>
    {run.error && <div className="callout bad">{run.error}</div>}
    <div className="wb-metrics">
      <div><span>Latency</span><b>{ms(r.latency_ms)}</b></div>
      <div><span>Tokens in / out</span><b>{num(r.tokens_in)} / {num(r.tokens_out)}</b></div>
      <div><span>Cost</span><b>{usd(r.cost_usd, { unknown: "unknown" })}</b></div>
      <div><span>Finished</span><b>{run.finished_at ? ago(run.finished_at) : TERMINAL.includes(run.status) ? "—" : "running…"}</b></div>
    </div>
    {run.output ? <div className="al-text wb-output">{run.output}</div> : !TERMINAL.includes(run.status) ? <div className="muted wb-wait"><span className="spinner" /> {run.status === "dispatched" ? "Waiting for the agent to accept the replay…" : "Running…"}</div> : <p className="muted">No output captured.</p>}
    {r.tool_calls != null && <details className="al-span"><summary>Tool calls</summary><pre className="al-json">{pretty(r.tool_calls)}</pre></details>}
    {!!run.checks?.length && <div className="wb-checks">{run.checks.map((c, i) => <div key={i} className="row"><Badge tone={c.passed === true ? "good" : c.passed === false ? "bad" : "warn"}>{c.passed === true ? "PASS" : c.passed === false ? "FAIL" : "N/A"}</Badge><span>{c.name}</span></div>)}</div>}
  </div>;
}

function CompareView({ runs, baseline }: { runs: WbRun[]; baseline?: string }) {
  const [a, b] = runs;
  const left = a?.output ?? baseline ?? "", right = b?.output ?? "";
  const rows: [string, (r: WbRun) => string][] = [
    ["Mode", (r) => r.mode === "llm" ? "LLM step" : "Agent replay"], ["Model", (r) => r.request.config?.model || "—"],
    ["System prompt", (r) => r.request.config?.system_prompt || "—"], ["Params", (r) => JSON.stringify(r.request.config?.params ?? {})],
    ["Latency", (r) => ms(r.result?.latency_ms)], ["Tokens", (r) => `${num(r.result?.tokens_in)} / ${num(r.result?.tokens_out)}`],
    ["Cost", (r) => usd(r.result?.cost_usd, { unknown: "unknown" })], ["Verdict", (r) => r.verdict ?? r.status],
  ];
  return <Card title="Compare" sub={b ? "Two runs side by side. The diff marks words removed from the left output and added in the right." : "Captured baseline vs the selected run."}>
    {b && <div className="al-table-wrap"><table className="al-table"><thead><tr><th /><th>{a.id.slice(-6)} · {ago(a.created_at)}</th><th>{b.id.slice(-6)} · {ago(b.created_at)}</th></tr></thead>
      <tbody>{rows.map(([k, f]) => <tr key={k}><td className="muted">{k}</td><td className={f(a) !== f(b) ? "wb-changed" : ""}>{f(a)}</td><td className={f(a) !== f(b) ? "wb-changed" : ""}>{f(b)}</td></tr>)}</tbody></table></div>}
    <h3 className="wb-h3">Output diff</h3>
    <Diff a={left} b={b ? right : a?.output ?? ""} />
  </Card>;
}

export default function Workbench() {
  const { query } = useLocation();
  const selected = query.get("scenario") || "";
  const toast = useToast();
  const { data: scenarios, reload: reloadList } = useApi<Scenario[]>("/api/workbench/scenarios", { refreshMs: 15000 });
  const { data: peers } = useApi<Peer[]>("/api/workbench/agents", { refreshMs: 3000 });
  const { data: providers } = useApi<{ name: string; label: string; configured: boolean }[]>("/api/providers", { staleMs: 60000 });
  const { data: traces } = useApi<LabRun[]>("/api/agent-lab/runs?limit=30", { refreshMs: 5000 });
  const [draft, setDraft] = useState<Scenario>(blank());
  const [dirty, setDirty] = useState(false);
  const [tab, setTab] = useState("input");
  const [filter, setFilter] = useState("");
  const [session, setSession] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<Error | null>(null);
  const [active, setActive] = useState<WbRun | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const historyPath = draft.id ? `/api/workbench/runs?scenario_id=${draft.id}&limit=50` : null;
  const { data: history, reload: reloadHistory } = useApi<WbRun[]>(historyPath, { refreshMs: 4000 });
  const catalogPath = draft.mode === "llm" && draft.config.provider ? `/api/catalog?provider=${encodeURIComponent(draft.config.provider)}&limit=200&sort=name` : null;
  const { data: catalog } = useApi<{ items: { id: string; name: string }[] }>(catalogPath, { staleMs: 60000 });
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setActive(null); setPicked([]); setErr(null);
    if (!selected) { setDraft(blank()); setDirty(false); return; }
    api.get<Scenario>(`/api/workbench/scenarios/${encodeURIComponent(selected)}`).then((s) => { setDraft(s); setDirty(false); }).catch(setErr);
  }, [selected]);

  // Follow the active run until it settles.
  useEffect(() => {
    if (!active || TERMINAL.includes(active.status)) return;
    const id = window.setTimeout(() => api.get<WbRun>(`/api/workbench/runs/${active.id}`).then((r) => { setActive(r); if (TERMINAL.includes(r.status)) reloadHistory(); }).catch(setErr), 700);
    return () => clearTimeout(id);
  }, [active, reloadHistory]);

  const edit = (patch: Partial<Scenario>) => { setDraft((d) => ({ ...d, ...patch })); setDirty(true); };
  const setConfig = (key: string, value: unknown) => edit({ config: { ...draft.config, [key]: value } });
  const setParam = (key: string, value: unknown) => setConfig("params", { ...(draft.config.params || {}), [key]: value === "" || value === null ? undefined : value });
  const replayPeers = (peers || []).filter((p) => p.replay && (!draft.service || p.service === draft.service));

  const save = async (copy = false): Promise<Scenario | null> => {
    setBusy(true); setErr(null);
    try {
      const body = copy ? { ...draft, name: `${draft.name} (copy)` } : draft;
      const saved = await api.post<Scenario>(draft.id && !copy ? `/api/workbench/scenarios/${draft.id}` : "/api/workbench/scenarios", body);
      setDraft(saved); setDirty(false); invalidate("/api/workbench/scenarios"); reloadList();
      if (saved.id !== selected) navigate(`/workbench?scenario=${encodeURIComponent(saved.id!)}`, true);
      toast(copy ? "Scenario duplicated" : "Scenario saved", "good");
      return saved;
    } catch (e) { setErr(e as Error); return null; } finally { setBusy(false); }
  };

  const send = async () => {
    setBusy(true); setErr(null);
    try {
      const run = await api.post<WbRun>("/api/workbench/run", { scenario_id: draft.id ?? "", scenario: draft, mode: draft.mode, session_id: draft.mode === "agent" ? session : undefined });
      setActive(run); setPicked([]); reloadHistory();
    } catch (e) { setErr(e as Error); } finally { setBusy(false); }
  };

  const capture = async (traceId: string) => {
    if (!traceId) return;
    setBusy(true); setErr(null);
    try {
      const s = await api.post<Scenario>("/api/workbench/from-trace", { trace_id: traceId, save: true });
      invalidate("/api/workbench/scenarios"); reloadList();
      navigate(`/workbench?scenario=${encodeURIComponent(s.id!)}`);
      toast("Scenario captured from trace", "good");
    } catch (e) { setErr(e as Error); } finally { setBusy(false); }
  };

  const remove = async () => {
    if (!draft.id) { setDraft(blank()); return; }
    setBusy(true);
    try { await api.del(`/api/workbench/scenarios/${draft.id}`); invalidate("/api/workbench/scenarios"); reloadList(); navigate("/workbench"); toast("Scenario deleted"); }
    catch (e) { setErr(e as Error); } finally { setBusy(false); }
  };

  const exportCollection = async () => {
    const full = await Promise.all((scenarios || []).map((s) => api.get<Scenario>(`/api/workbench/scenarios/${s.id}`)));
    const blob = new Blob([JSON.stringify({ format: "arl-workbench/1", exported_at: new Date().toISOString(), scenarios: full }, null, 2)], { type: "application/json" });
    const a = document.createElement("a"); a.href = URL.createObjectURL(blob); a.download = "arl-workbench.json"; a.click(); URL.revokeObjectURL(a.href);
  };
  const importCollection = async (file: File) => {
    try {
      const parsed = JSON.parse(await file.text());
      const items: Scenario[] = Array.isArray(parsed) ? parsed : parsed.scenarios ?? [];
      for (const s of items) { const { id: _id, ...rest } = s; await api.post("/api/workbench/scenarios", rest); }
      invalidate("/api/workbench/scenarios"); reloadList(); toast(`Imported ${items.length} scenarios`, "good");
    } catch (e) { setErr(e as Error); }
  };

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); if (!busy) void send(); }
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") { e.preventDefault(); if (!busy) void save(); }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  });

  const groups = useMemo(() => {
    const m = new Map<string, Scenario[]>();
    for (const s of scenarios || []) if (!filter || `${s.name} ${s.collection} ${s.service}`.toLowerCase().includes(filter.toLowerCase())) m.set(s.collection, [...(m.get(s.collection) || []), s]);
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [scenarios, filter]);

  const compared = (history || []).filter((r) => picked.includes(r.id));
  const baseline = draft.llm?.baseline?.response;
  const tabs = [
    { value: "input", label: "Input" }, { value: "state", label: "State" }, { value: "model", label: "Model & prompt" },
    ...(draft.mode === "llm" ? [{ value: "messages", label: `Messages (${draft.llm?.messages?.length ?? 0})` }] : []),
    { value: "checks", label: "Checks" }, { value: "steps", label: `Captured steps (${draft.steps?.length ?? 0})` }, { value: "notes", label: "Notes" },
  ];
  const canSend = draft.mode === "llm" ? !!draft.config.model : replayPeers.length > 0;

  return <div className="page">
    <div className="page-head">
      <div className="grow"><div className="eyebrow">Postman for agents</div><h1 className="page-title">Workbench</h1>
        <div className="page-sub">Capture an agent's functional state from a trace, edit input, state, model or prompt, and send it again — as a full agent replay or a single LLM step. Every run is kept for side-by-side comparison.</div></div>
      <div className="row wrap">
        <input ref={fileRef} type="file" accept="application/json" hidden onChange={(e) => { const f = e.target.files?.[0]; if (f) void importCollection(f); e.target.value = ""; }} />
        <button className="btn" onClick={() => fileRef.current?.click()}><Icon name="upload" size={14} />Import</button>
        <button className="btn" disabled={!scenarios?.length} onClick={() => void exportCollection()}><Icon name="download" size={14} />Export</button>
        <button className="btn primary" onClick={() => navigate("/workbench")}><Icon name="plus" size={14} />New scenario</button>
      </div>
    </div>
    {err && <ErrorBox error={err} />}
    <div className="wb-layout">
      <aside className="col" style={{ gap: 14 }}>
        <Card title="Collections" sub={`${scenarios?.length ?? 0} saved scenarios`} flush>
          <div className="wb-search"><input className="input" placeholder="Filter scenarios…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter scenarios" /></div>
          <div className="al-run-list">
            {groups.map(([collection, items]) => <div key={collection}>
              <div className="wb-group">{collection}</div>
              {items.map((s) => <button key={s.id} className={`al-run-item ${s.id === draft.id ? "selected" : ""}`} onClick={() => navigate(`/workbench?scenario=${encodeURIComponent(s.id!)}`)}>
                <div className="row"><b className="grow trunc">{s.name}</b><Badge mono>{s.mode === "llm" ? "LLM" : "AGENT"}</Badge></div>
                <small className="mono muted trunc">{s.config?.model || "default model"} · {ago(s.updated_at)}</small>
              </button>)}
            </div>)}
            {!scenarios?.length && <p className="muted wb-pad">No scenarios yet. Capture one from a trace below, or create one from scratch.</p>}
          </div>
        </Card>
        <Card title="Capture from trace" sub="Turn a captured run into an editable scenario.">
          <select className="select" aria-label="Recent traces" value="" disabled={busy} onChange={(e) => void capture(e.target.value)}>
            <option value="">Pick a recent trace…</option>
            {(traces || []).map((t) => <option key={t.id} value={t.id}>{t.name} · {t.service} · {ago(t.started_at)}</option>)}
          </select>
          <p className="muted wb-hint">Traces with <span className="mono">state.snapshot</span> and <span className="mono">model.request</span> (with <span className="mono">data.request</span>) give the richest scenarios.</p>
        </Card>
        <Card title="Connected agents" sub="Replay targets (SDK with a replay handler).">
          {(peers || []).length ? (peers || []).map((p) => <div key={p.session_id} className="row wrap wb-peer"><span className={`led ${p.replay ? "al-live" : ""}`} /><b className="trunc grow">{p.service}</b><Badge tone={p.replay ? "good" : ""}>{p.replay ? "replay" : "trace only"}</Badge></div>) : <p className="muted">No agent connected. Start your agent with <span className="mono">STAGE=dev</span> and <span className="mono">LAB_WS_URL</span>.</p>}
        </Card>
      </aside>

      <section className="col" style={{ gap: 16, minWidth: 0 }}>
        <Card className="wb-request">
          <div className="wb-bar">
            <Seg value={draft.mode} options={[{ value: "agent", label: "Agent replay" }, { value: "llm", label: "LLM step" }]} onChange={(mode) => { edit({ mode }); if (mode === "llm" && !draft.config.provider) setDraft((d) => ({ ...d, mode, config: { ...d.config, provider: "fake" } })); }} />
            <input className="input wb-name" value={draft.name} onChange={(e) => edit({ name: e.target.value })} aria-label="Scenario name" />
            {draft.mode === "agent" ? <select className="select wb-target" aria-label="Target agent" value={session} onChange={(e) => setSession(e.target.value)}>
              <option value="">{replayPeers.length ? `Latest ${draft.service || "agent"} session` : "No replay-capable agent"}</option>
              {replayPeers.map((p) => <option key={p.session_id} value={p.session_id}>{p.service} · {p.session_id.slice(-6)}</option>)}
            </select> : <select className="select wb-target" aria-label="Provider" value={draft.config.provider || ""} onChange={(e) => setConfig("provider", e.target.value)}>
              {(providers || []).filter((p) => p.name !== "imported").map((p) => <option key={p.name} value={p.name}>{p.label}{p.configured ? "" : " (no key)"}</option>)}
            </select>}
            <button className="btn" disabled={busy} onClick={() => void save()} title="Ctrl+S"><Icon name="check" size={14} />{dirty ? "Save*" : "Save"}</button>
            <button className="btn signal" disabled={busy || !canSend} onClick={() => void send()} title="Ctrl+Enter"><Icon name="play" size={14} />Send</button>
          </div>
          {draft.mode === "agent" && !replayPeers.length && <div className="callout warn">No connected agent accepts replays{draft.service ? <> for <b className="mono">{draft.service}</b></> : ""}. Register a replay handler in the SDK (<span className="mono">lab.on_replay</span> / <span className="mono">OnReplay</span> / <span className="mono">onReplay</span>), or switch to <b>LLM step</b> to call the model directly.</div>}
          <Tabs value={tab} tabs={tabs} onChange={setTab} />
          {tab === "input" && <div className="col" style={{ gap: 12 }}>
            {typeof draft.input === "string" || draft.input == null
              ? <label className="field"><span className="wb-label">Input text — sent to the agent as <span className="mono">replay.input</span></span><textarea className="textarea" rows={8} value={(draft.input as string) ?? ""} onChange={(e) => edit({ input: e.target.value })} /></label>
              : <JsonField label="Structured input (JSON)" value={draft.input} onChange={(input) => edit({ input })} />}
            <div className="row wrap"><button className="btn sm ghost" onClick={() => edit({ input: typeof draft.input === "string" ? { text: draft.input } : asText(draft.input) })}>{typeof draft.input === "string" ? "Switch to JSON input" : "Switch to text input"}</button>
              <label className="field wb-inline"><span className="wb-label">Collection</span><input className="input" value={draft.collection} onChange={(e) => edit({ collection: e.target.value })} /></label>
              <label className="field wb-inline"><span className="wb-label">Service</span><input className="input mono" placeholder="any agent" value={draft.service} onChange={(e) => edit({ service: e.target.value })} /></label></div>
          </div>}
          {tab === "state" && <JsonField label="Functional state — memory, variables, retrieved context (replay.state)" rows={16} value={draft.state} onChange={(state) => edit({ state })} hint={`${draft.snapshots ?? 0} state.snapshot event(s) in the source trace; the first one seeded this state.`} />}
          {tab === "model" && <div className="col" style={{ gap: 12 }}>
            <div className="wb-grid">
              <label className="field"><span className="wb-label">Model</span><input className="input mono" list="wb-models" placeholder={draft.mode === "llm" ? "provider model id" : "agent default"} value={draft.config.model || ""} onChange={(e) => setConfig("model", e.target.value)} />
                <datalist id="wb-models">{(catalog?.items || []).map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</datalist></label>
              <label className="field"><span className="wb-label">Temperature</span><input className="input" type="number" step="0.1" min="0" max="2" value={draft.config.params?.temperature ?? ""} onChange={(e) => setParam("temperature", e.target.value === "" ? null : Number(e.target.value))} /></label>
              <label className="field"><span className="wb-label">Max tokens</span><input className="input" type="number" min="1" value={draft.config.params?.max_tokens ?? ""} onChange={(e) => setParam("max_tokens", e.target.value === "" ? null : Number(e.target.value))} /></label>
              <label className="field"><span className="wb-label">Top p</span><input className="input" type="number" step="0.05" min="0" max="1" value={draft.config.params?.top_p ?? ""} onChange={(e) => setParam("top_p", e.target.value === "" ? null : Number(e.target.value))} /></label>
              <label className="field"><span className="wb-label">Seed</span><input className="input" type="number" value={draft.config.params?.seed ?? ""} onChange={(e) => setParam("seed", e.target.value === "" ? null : Number(e.target.value))} /></label>
            </div>
            <label className="field"><span className="wb-label">System prompt</span><textarea className="textarea" rows={8} value={draft.config.system_prompt || ""} onChange={(e) => setConfig("system_prompt", e.target.value)} /></label>
            <JsonField label="Full config (replay.config) — add any key your agent reads" rows={8} value={draft.config} onChange={(config) => edit({ config })} />
          </div>}
          {tab === "messages" && <JsonField label="Chat messages after the system prompt (OpenAI format)" rows={16} value={draft.llm?.messages ?? []} onChange={(messages) => edit({ llm: { ...draft.llm, messages } })} hint={draft.llm?.tools ? "This step also carries tool definitions from the captured request." : "Empty messages fall back to the text input as one user message."} />}
          {tab === "checks" && <div className="col" style={{ gap: 12 }}>
            <JsonField label="Checks" rows={10} value={draft.checks} onChange={(checks) => edit({ checks })} hint="Supported: max_cost_usd, max_latency_ms, max_retries, output_contains[], required_nodes[], forbidden_tools[]. Agent replays apply them at run.start; LLM steps evaluate output, latency and cost." />
          </div>}
          {tab === "steps" && <div className="col" style={{ gap: 8 }}>
            {!draft.steps?.length && <p className="muted">No model.request events were captured. Use <span className="mono">run.model_call(request=...)</span> in the SDK to record replayable LLM steps.</p>}
            {draft.steps?.map((s, i) => <details key={i} className="al-span"><summary><Badge mono>{s.phase}</Badge> {s.model || "model"} · {s.span_id} {s.replayable ? "" : "· not replayable"}</summary>
              {s.response && <div className="al-text">{s.response}</div>}<pre className="al-json">{pretty(s.request)}</pre></details>)}
          </div>}
          {tab === "notes" && <label className="field"><span className="wb-label">Notes</span><textarea className="textarea" rows={8} value={draft.notes || ""} onChange={(e) => edit({ notes: e.target.value })} /></label>}
          <div className="row wrap wb-foot">
            {draft.source_trace_id && <a className="btn sm ghost" href={`/agent-lab?run=${encodeURIComponent(draft.source_trace_id)}`}><Icon name="eye" size={13} />Source trace</a>}
            <span className="spacer" />
            {draft.id && <button className="btn sm ghost" disabled={busy} onClick={() => void save(true)}><Icon name="copy" size={13} />Duplicate</button>}
            <button className="btn sm ghost" disabled={busy} onClick={() => void remove()}><Icon name="trash" size={13} />{draft.id ? "Delete" : "Reset"}</button>
          </div>
        </Card>

        <Card title="Response" sub={active ? `Run ${active.id}` : "Press Send (Ctrl+Enter) to execute the scenario."}>
          {active ? <RunCard run={active} /> : baseline ? <div className="col" style={{ gap: 8 }}><span className="wb-label">Captured baseline response</span><div className="al-text wb-output">{baseline}</div></div> : <p className="muted">Nothing sent yet.</p>}
        </Card>

        {(compared.length > 0 || (active?.output && baseline)) && <CompareView runs={compared.length ? compared : [active!]} baseline={compared.length ? undefined : baseline} />}

        {draft.id && <Card title="History" sub="Select two runs to compare." flush>
          <div className="al-table-wrap"><table className="al-table"><thead><tr><th /><th>When</th><th>Mode</th><th>Model</th><th>Status</th><th>Latency</th><th>Cost</th><th>Output</th></tr></thead>
            <tbody>{(history || []).map((r) => <tr key={r.id} className={active?.id === r.id ? "wb-current" : ""} onClick={() => setActive(r)} style={{ cursor: "pointer" }}>
              <td onClick={(e) => e.stopPropagation()}><input type="checkbox" aria-label="Compare run" checked={picked.includes(r.id)} onChange={(e) => setPicked(e.target.checked ? [...picked, r.id].slice(-2) : picked.filter((p) => p !== r.id))} /></td>
              <td className="muted">{ago(r.created_at)}</td><td>{r.mode === "llm" ? "LLM" : "Agent"}</td><td className="mono trunc" style={{ maxWidth: 180 }}>{r.request.config?.model || "default"}</td>
              <td><Badge tone={tone(r.verdict && r.verdict !== "no_checks" ? r.verdict : r.status)} dot>{r.verdict && r.verdict !== "no_checks" ? r.verdict.replaceAll("_", " ") : r.status}</Badge></td>
              <td className="mono">{ms(r.result?.latency_ms)}</td><td className="mono">{usd(r.result?.cost_usd, { unknown: "?" })}</td>
              <td className="trunc" style={{ maxWidth: 280 }}>{r.output || r.error || "—"}</td></tr>)}</tbody></table></div>
          {!history?.length && <p className="muted wb-pad">No runs yet for this scenario.</p>}
        </Card>}
      </section>
    </div>
  </div>;
}
