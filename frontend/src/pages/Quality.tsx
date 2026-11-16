import { useEffect, useState } from "react";
import { api } from "../api";
import { Badge, Card, Empty, ErrorBox, Icon, Loading, Modal, Tabs, useToast } from "../components/ui";
import { ago, num, pct, usd } from "../format";
import { invalidate, useApi, useLocalState } from "../hooks";
import { navigate, useLocation } from "../router";

type Criterion = { name: string; kind: "binary" | "scale" | "label" | "text"; description?: string; min?: number; max?: number; options?: string[] };
interface Queue { id: string; name: string; description: string; instructions: string; rubric: Criterion[]; required: number; created_at: number; progress: { pending: number; done: number; total: number } }
interface Item { id: string; queue_id: string; trace_id: string; status: string; annotators: string[] }
interface Evaluator { id: string; name: string; type: "llm_judge" | "checks" | "scorer"; enabled: boolean; sample_rate: number; feedback_name: string; config: Record<string, any>; filter: Record<string, string>; max_usd_per_day: number; revision: number; usage?: { day: string; spent_usd: number; evaluated: number; skipped_budget: number }; total_evaluated?: number; last_run_at?: number }
interface Alert { id: string; name: string; metric: string; op: string; threshold: number; window_minutes: number; min_samples: number; service: string; feedback_name: string; webhook_url: string; enabled: boolean; state?: string; last_value?: number | null; last_samples?: number; last_checked_at?: number }
interface AlertEvent { id: string; name: string; state: string; metric: string; value: number | null; threshold: number; op: string; at: number; delivered?: boolean }

const pretty = (v: unknown) => typeof v === "string" ? v : JSON.stringify(v, null, 2);
const partsText = (content: any) => Array.isArray(content) ? content.map((p: any) => p.type === "text" ? p.text : p.type === "json" ? JSON.stringify(p.value) : `[${p.type}]`).join("\n") : "";

/** Input, final output and tool calls of a trace, for reviewers. */
function TraceBrief({ id }: { id: string }) {
  const { data } = useApi<{ items: any[] }>(`/api/agent-lab/runs/${id}/events?limit=1000`);
  if (!data) return <Loading rows={4} />;
  const input = data.items.filter((e) => e.event === "input").map((e) => partsText(e.data.content)).join("\n");
  const output = data.items.filter((e) => e.event === "output").map((e) => partsText(e.data.content)).join("\n");
  const results = new Map(data.items.filter((e) => e.event === "tool.result").map((e) => [e.data.call_id, e.data]));
  const tools = data.items.filter((e) => e.event === "tool.call");
  const models = data.items.filter((e) => e.event === "model.response");
  return <div className="col" style={{ gap: 12 }}>
    <div><div className="wb-label">Input</div><div className="al-text">{input || <span className="muted">no input event</span>}</div></div>
    {tools.length > 0 && <div><div className="wb-label">Tool calls ({tools.length})</div>
      {tools.map((t) => { const r: any = results.get(t.data.call_id); return <details key={t.ordinal} className="al-json" style={{ marginBottom: 6 }}><summary><span className="mono">{t.data.tool}</span>{r?.status === "error" && <Badge tone="bad">error</Badge>}</summary><pre className="al-json">{pretty(t.data.arguments)}{"\n→ "}{pretty(r?.result ?? r?.error ?? null)}</pre></details>; })}
    </div>}
    {models.length > 0 && <div className="muted" style={{ fontSize: 12 }}>{models.length} model call{models.length > 1 ? "s" : ""}: {[...new Set(models.map((m) => m.data.model))].join(", ")}</div>}
    <div><div className="wb-label">Final output</div><div className="al-text">{output || <span className="muted">no output event</span>}</div></div>
    <a className="btn sm ghost" href={`/agent-lab?run=${encodeURIComponent(id)}`} target="_blank" rel="noreferrer"><Icon name="eye" size={13} />Full trace</a>
  </div>;
}

function RubricInput({ c, value, onChange }: { c: Criterion; value: any; onChange: (v: any) => void }) {
  if (c.kind === "binary") return <div className="row" style={{ gap: 6 }}>
    <button className={`btn sm ${value === true ? "primary" : ""}`} onClick={() => onChange(true)}><Icon name="check" size={13} />Pass</button>
    <button className={`btn sm ${value === false ? "danger primary" : ""}`} onClick={() => onChange(false)}><Icon name="x" size={13} />Fail</button>
  </div>;
  if (c.kind === "scale") {
    const steps = []; for (let v = c.min ?? 1; v <= (c.max ?? 5); v++) steps.push(v);
    return <div className="row wrap" style={{ gap: 4 }}>{steps.map((v) => <button key={v} className={`btn sm ${value === v ? "primary" : ""}`} onClick={() => onChange(v)}>{v}</button>)}</div>;
  }
  if (c.kind === "label") return <div className="row wrap" style={{ gap: 4 }}>{c.options?.map((o) => <button key={o} className={`btn sm ${value === o ? "primary" : ""}`} onClick={() => onChange(o)}>{o}</button>)}</div>;
  return <textarea className="textarea" rows={3} value={value ?? ""} onChange={(e) => onChange(e.target.value)} />;
}

function Annotate({ queue }: { queue: Queue }) {
  const [author, setAuthor] = useLocalState("arl-annotator", "");
  const [item, setItem] = useState<Item | null | undefined>(undefined);
  const [scores, setScores] = useState<Record<string, any>>({});
  const [comment, setComment] = useState("");
  const [err, setErr] = useState<Error | null>(null);
  const toast = useToast();
  const load = async () => {
    if (!author) return;
    try { const r = await api.get<{ item: Item | null }>(`/api/annotation/queues/${queue.id}/next?author=${encodeURIComponent(author)}`); setItem(r.item); setScores({}); setComment(""); setErr(null); } catch (e: any) { setErr(e); }
  };
  useEffect(() => { void load(); }, [queue.id, author]); // eslint-disable-line react-hooks/exhaustive-deps
  const submit = async () => {
    try { await api.post(`/api/annotation/items/${item!.id}/submit`, { author, scores, comment }); toast("Saved", "good"); invalidate("/api/annotation"); await load(); } catch (e: any) { setErr(e); }
  };
  const skip = async () => { await api.post(`/api/annotation/items/${item!.id}/skip`, { author }); await load(); };
  if (!author) return <Card title="Who is annotating?" sub="Your name is attached to every score so inter-annotator agreement can be measured.">
    <form className="row" onSubmit={(e) => { e.preventDefault(); setAuthor(String(new FormData(e.currentTarget).get("n") || "").trim()); }}><input name="n" className="input" placeholder="e.g. ana" autoFocus /><button className="btn primary">Start</button></form>
  </Card>;
  return <div className="grid g-main" style={{ alignItems: "start" }}>
    <Card title={item ? "Trace under review" : "Queue"} sub={item ? <span className="mono">{item.trace_id}</span> : undefined}>
      {item === undefined ? <Loading /> : item === null ? <Empty title="Nothing left for you in this queue">Add more traces, or ask another annotator to review the same items for agreement.</Empty> : <TraceBrief id={item.trace_id} />}
    </Card>
    <Card title="Rubric" sub={<>annotating as <b>{author}</b> · <button className="btn sm ghost" onClick={() => setAuthor("")}>change</button></>}>
      {queue.instructions && <div className="callout" style={{ marginBottom: 12, whiteSpace: "pre-wrap" }}>{queue.instructions}</div>}
      <div className="col" style={{ gap: 14 }}>
        {queue.rubric.map((c) => <div key={c.name}><div className="row"><b>{c.name}</b><span className="tag">{c.kind}{c.kind === "scale" ? ` ${c.min}–${c.max}` : ""}</span></div>
          {c.description && <div className="muted" style={{ fontSize: 12, margin: "2px 0 6px" }}>{c.description}</div>}
          <RubricInput c={c} value={scores[c.name]} onChange={(v) => setScores({ ...scores, [c.name]: v })} /></div>)}
        <label className="field"><span className="wb-label">Comment</span><textarea className="textarea" rows={2} value={comment} onChange={(e) => setComment(e.target.value)} /></label>
        <ErrorBox error={err} />
        <div className="row"><button className="btn primary" disabled={!item || !Object.keys(scores).length} onClick={submit}><Icon name="check" />Submit</button><button className="btn" disabled={!item} onClick={skip}>Skip</button></div>
      </div>
    </Card>
  </div>;
}

function Agreement({ queue }: { queue: Queue }) {
  const { data } = useApi<{ criteria: any[]; methods: Record<string, string> }>(`/api/annotation/queues/${queue.id}/agreement`, { refreshMs: 15000 });
  const quality = (a: number | null) => a == null ? "" : a >= 0.8 ? "good" : a >= 0.667 ? "warn" : "bad";
  return <Card title="Agreement & judge calibration" sub="Do reviewers agree with each other — and does the automated evaluator agree with them?" flush
    reading={data?.methods.interpretation}>
    {!data ? <Loading /> : <div className="table-wrap"><table className="t">
      <thead><tr><th>Criterion</th><th className="r">Items</th><th className="r">Annotators</th><th className="r">Krippendorff α</th><th className="r">Cohen κ</th><th>Automated evaluator vs humans</th></tr></thead>
      <tbody>{data.criteria.map((c) => <tr key={c.name}>
        <td><b>{c.name}</b> <span className="tag">{c.kind}</span></td><td className="r num">{c.items}</td><td className="r num">{c.annotators}</td>
        <td className={`r num ${quality(c.alpha)}`}>{c.alpha == null ? "—" : num(c.alpha, 3)}</td><td className="r num">{c.kappa == null ? "—" : num(c.kappa, 3)}</td>
        <td>{!c.judge.paired_items ? <span className="muted">no evaluator feedback named “{c.name}”</span> : c.judge.accuracy != null ?
          <span>{c.judge.paired_items} paired · accuracy <b>{pct(c.judge.accuracy)}</b>{c.judge.kappa != null && <> · κ {num(c.judge.kappa, 2)}</>}{c.judge.confusion && <span className="muted"> · catches {c.judge.confusion.tp}/{c.judge.confusion.tp + c.judge.confusion.fn} human-flagged failures, {c.judge.confusion.fp} false alarms</span>}</span> :
          <span>{c.judge.paired_items} paired · Spearman <b>{c.judge.spearman == null ? "—" : num(c.judge.spearman, 2)}</b> · MAE {num(c.judge.mean_abs_error, 3)}</span>}</td>
      </tr>)}</tbody></table></div>}
  </Card>;
}

const BLANK_QUEUE = { name: "", description: "", instructions: "", required: 1, rubric: [{ name: "correct", kind: "binary", description: "The final answer solves the user's task" }, { name: "helpfulness", kind: "scale", min: 1, max: 5, description: "" }] as Criterion[] };

function Queues() {
  const { data: queues, reload } = useApi<Queue[]>("/api/annotation/queues", { refreshMs: 10000 });
  const { query } = useLocation();
  const open = query.get("queue") || "";
  const setOpen = (id: string) => navigate(id ? `/quality?tab=annotate&queue=${encodeURIComponent(id)}` : "/quality?tab=annotate");
  const [draft, setDraft] = useState<typeof BLANK_QUEUE | null>(null);
  const [fill, setFill] = useState<{ hours: number; limit: number; verdict: string; errors: boolean; service: string }>({ hours: 24, limit: 50, verdict: "", errors: false, service: "" });
  const [err, setErr] = useState<Error | null>(null);
  const toast = useToast();
  const queue = queues?.find((q) => q.id === open);
  const create = async () => {
    try { const q = await api.post<Queue>("/api/annotation/queues", draft); setDraft(null); reload(); setOpen(q.id); } catch (e: any) { setErr(e); }
  };
  const addItems = async () => {
    try { const r = await api.post<{ added: number }>(`/api/annotation/queues/${open}/items`, { filter: fill }); toast(`${r.added} traces added`, "good"); reload(); } catch (e: any) { toast(e.message, "bad"); }
  };
  if (queue) return <div className="col" style={{ gap: 14 }}>
    <div className="row wrap"><button className="btn sm ghost" onClick={() => setOpen("")}><Icon name="back" size={13} />Queues</button><h2 className="wb-h3" style={{ margin: 0 }}>{queue.name}</h2>
      <Badge>{queue.progress.done}/{queue.progress.total} done</Badge><span className="spacer" />
      <div className="row wrap" style={{ gap: 6 }}>
        <span className="muted">Add traces: last</span><input className="input" type="number" style={{ width: 70 }} value={fill.hours} onChange={(e) => setFill({ ...fill, hours: +e.target.value })} /><span className="muted">h, up to</span>
        <input className="input" type="number" style={{ width: 70 }} value={fill.limit} onChange={(e) => setFill({ ...fill, limit: +e.target.value })} />
        <select className="select" value={fill.verdict} onChange={(e) => setFill({ ...fill, verdict: e.target.value })}><option value="">any verdict</option><option value="fail">failing</option><option value="pass">passing</option></select>
        <label className="row" style={{ gap: 4 }}><input type="checkbox" checked={fill.errors} onChange={(e) => setFill({ ...fill, errors: e.target.checked })} />errors</label>
        <button className="btn sm" onClick={addItems}><Icon name="plus" size={13} />Sample</button>
      </div>
    </div>
    <Annotate queue={queue} />
    <Agreement queue={queue} />
  </div>;
  return <>
    <div className="row" style={{ marginBottom: 12 }}><span className="muted">Human review of sampled traces with a fixed rubric. Scores become feedback; agreement statistics tell you whether the rubric — and any LLM judge — can be trusted.</span><span className="spacer" />
      <button className="btn primary" onClick={() => setDraft(structuredClone(BLANK_QUEUE))}><Icon name="plus" />New queue</button></div>
    {!queues ? <Loading /> : !queues.length ? <Empty title="No annotation queues yet" /> : <div className="grid g3">{queues.map((q) => (
      <div key={q.id} className="card click" style={{ padding: 16, cursor: "pointer" }} onClick={() => setOpen(q.id)}>
        <div className="row"><b>{q.name}</b><span className="spacer" /><Badge>{q.required} annotator{q.required > 1 ? "s" : ""}/item</Badge></div>
        <div className="muted" style={{ fontSize: 12.5, margin: "4px 0 10px" }}>{q.description || q.rubric.map((c) => c.name).join(" · ")}</div>
        <div className="progress"><i style={{ width: `${q.progress.total ? q.progress.done / q.progress.total * 100 : 0}%` }} /></div>
        <div className="mono muted" style={{ fontSize: 11, marginTop: 4 }}>{q.progress.done}/{q.progress.total} items done</div>
      </div>))}</div>}
    <Modal open={!!draft} onClose={() => setDraft(null)} title="New annotation queue" footer={<button className="btn primary" onClick={create} disabled={!draft?.name}>Create</button>}>
      {draft && <div className="col" style={{ gap: 10 }}>
        <input className="input" placeholder="Name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
        <textarea className="textarea" rows={2} placeholder="Instructions for reviewers" value={draft.instructions} onChange={(e) => setDraft({ ...draft, instructions: e.target.value })} />
        <label className="row">Annotators per item <input className="input" type="number" min={1} max={5} style={{ width: 70 }} value={draft.required} onChange={(e) => setDraft({ ...draft, required: +e.target.value })} /></label>
        <div className="wb-label">Rubric</div>
        {draft.rubric.map((c, i) => <div key={i} className="row wrap" style={{ gap: 6 }}>
          <input className="input" style={{ width: 140 }} value={c.name} onChange={(e) => { const r = [...draft.rubric]; r[i] = { ...c, name: e.target.value }; setDraft({ ...draft, rubric: r }); }} />
          <select className="select" value={c.kind} onChange={(e) => { const r = [...draft.rubric]; r[i] = { ...c, kind: e.target.value as Criterion["kind"], min: 1, max: 5, options: ["good", "bad"] }; setDraft({ ...draft, rubric: r }); }}>
            {["binary", "scale", "label", "text"].map((k) => <option key={k}>{k}</option>)}</select>
          {c.kind === "label" && <input className="input" style={{ width: 180 }} placeholder="options, comma separated" value={(c.options ?? []).join(",")} onChange={(e) => { const r = [...draft.rubric]; r[i] = { ...c, options: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) }; setDraft({ ...draft, rubric: r }); }} />}
          <input className="input grow" placeholder="description" value={c.description ?? ""} onChange={(e) => { const r = [...draft.rubric]; r[i] = { ...c, description: e.target.value }; setDraft({ ...draft, rubric: r }); }} />
          <button className="btn sm ghost" aria-label="Remove criterion" onClick={() => setDraft({ ...draft, rubric: draft.rubric.filter((_, j) => j !== i) })}><Icon name="trash" size={13} /></button>
        </div>)}
        <button className="btn sm" onClick={() => setDraft({ ...draft, rubric: [...draft.rubric, { name: `criterion_${draft.rubric.length + 1}`, kind: "binary" }] })}><Icon name="plus" size={13} />Criterion</button>
        <ErrorBox error={err} />
      </div>}
    </Modal>
  </>;
}

const BLANK_EVAL = { name: "", type: "llm_judge", sample_rate: 0.2, feedback_name: "", max_usd_per_day: 1, filter: { service: "" }, config: { provider: "openrouter", model: "openai/gpt-4.1-mini", rubric: "Pass if the final answer correctly and completely solves the user's task using the tool results, without inventing facts.", pass_threshold: 0.5, checks: { required_tools: [] as string[] }, key: "regex", params: { pattern: "" } } };

function Evaluators() {
  const { data, reload } = useApi<Evaluator[]>("/api/evaluators", { refreshMs: 10000 });
  const [draft, setDraft] = useState<any>(null);
  const [traceId, setTraceId] = useState("");
  const [test, setTest] = useState<any>(null);
  const [err, setErr] = useState<Error | null>(null);
  const toast = useToast();
  const body = (d: any) => {
    const config = d.type === "llm_judge" ? { provider: d.config.provider, model: d.config.model, rubric: d.config.rubric, pass_threshold: d.config.pass_threshold } : d.type === "checks" ? { checks: d.config.checks } : { key: d.config.key, params: d.config.params, expected: d.config.expected };
    return { ...d, config, id: undefined };
  };
  const save = async () => {
    try { if (draft.id) await api.post(`/api/evaluators/${draft.id}`, body(draft)); else await api.post("/api/evaluators", body(draft)); setDraft(null); reload(); } catch (e: any) { setErr(e); }
  };
  const runTest = async () => { setTest(null); try { setTest(await api.post("/api/evaluators/test", { evaluator: body(draft), trace_id: traceId })); } catch (e: any) { setErr(e); } };
  const toggle = async (e: Evaluator) => { await api.post(`/api/evaluators/${e.id}`, { ...e, enabled: !e.enabled }); reload(); };
  const backfill = async (e: Evaluator) => { const r = await api.post<{ queued: number }>(`/api/evaluators/${e.id}/backfill`, { hours: 24, limit: 200 }); toast(`${r.queued} traces queued for ${e.name}`, "good"); };
  return <>
    <div className="row" style={{ marginBottom: 12 }}><span className="muted">Online evaluators score a sample of finished traces automatically and write feedback. Calibrate an LLM judge against human annotations (same feedback name) before trusting it.</span><span className="spacer" />
      <button className="btn primary" onClick={() => { setErr(null); setTest(null); setDraft(structuredClone(BLANK_EVAL)); }}><Icon name="plus" />New evaluator</button></div>
    {!data ? <Loading /> : !data.length ? <Empty title="No evaluators" /> : <Card flush><div className="table-wrap"><table className="t">
      <thead><tr><th>Evaluator</th><th>Type</th><th>Scope</th><th className="r">Sample</th><th className="r">Today</th><th className="r">Total</th><th /></tr></thead>
      <tbody>{data.map((e) => <tr key={e.id}>
        <td><b>{e.name}</b><div className="mono faint">→ feedback “{e.feedback_name}” · rev {e.revision}</div></td>
        <td><span className="tag">{e.type}</span>{e.type === "llm_judge" && <div className="mono faint trunc" style={{ maxWidth: 180 }}>{e.config.model}</div>}</td>
        <td className="muted">{e.filter.service || "all services"}</td><td className="r num">{pct(e.sample_rate, 0)}</td>
        <td className="r num">{e.usage?.evaluated ?? 0}{e.type === "llm_judge" && <div className="faint">{usd(e.usage?.spent_usd ?? 0)} / {e.max_usd_per_day ? usd(e.max_usd_per_day) : "∞"}</div>}</td>
        <td className="r num">{e.total_evaluated ?? 0}</td>
        <td><div className="row" style={{ gap: 4, justifyContent: "flex-end" }}>
          <button className="btn sm" onClick={() => toggle(e)}>{e.enabled ? <><Icon name="pause" size={13} />Pause</> : <><Icon name="play" size={13} />Enable</>}</button>
          <button className="btn sm" onClick={() => backfill(e)} title="Score the last 24h of traces">Backfill</button>
          <button className="btn sm" onClick={() => { setErr(null); setTest(null); setDraft({ ...structuredClone(BLANK_EVAL), ...e, config: { ...BLANK_EVAL.config, ...e.config } }); }}>Edit</button>
          <button className="btn sm ghost" aria-label="Delete" onClick={async () => { await api.del(`/api/evaluators/${e.id}`); reload(); }}><Icon name="trash" size={13} /></button>
        </div></td></tr>)}</tbody></table></div></Card>}
    <Modal open={!!draft} onClose={() => setDraft(null)} title={draft?.id ? "Edit evaluator" : "New evaluator"} footer={<button className="btn primary" onClick={save} disabled={!draft?.name}>Save</button>}>
      {draft && <div className="col" style={{ gap: 10 }}>
        <div className="grid g2">
          <input className="input" placeholder="Name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          <input className="input" placeholder="Feedback name (default: name)" value={draft.feedback_name} onChange={(e) => setDraft({ ...draft, feedback_name: e.target.value })} />
          <select className="select" value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })}><option value="llm_judge">LLM judge (rubric)</option><option value="checks">Trajectory / output checks</option><option value="scorer">Deterministic scorer</option></select>
          <input className="input" placeholder="Service (all when empty)" value={draft.filter.service ?? ""} onChange={(e) => setDraft({ ...draft, filter: { ...draft.filter, service: e.target.value } })} />
          <label className="row">Sample rate <input className="input" type="number" step={0.05} min={0} max={1} style={{ width: 90 }} value={draft.sample_rate} onChange={(e) => setDraft({ ...draft, sample_rate: +e.target.value })} /></label>
          {draft.type === "llm_judge" && <label className="row">Max $/day <input className="input" type="number" step={0.5} min={0} style={{ width: 90 }} value={draft.max_usd_per_day} onChange={(e) => setDraft({ ...draft, max_usd_per_day: +e.target.value })} /></label>}
        </div>
        {draft.type === "llm_judge" && <>
          <div className="grid g2"><input className="input mono" placeholder="provider" value={draft.config.provider} onChange={(e) => setDraft({ ...draft, config: { ...draft.config, provider: e.target.value } })} /><input className="input mono" placeholder="model" value={draft.config.model} onChange={(e) => setDraft({ ...draft, config: { ...draft.config, model: e.target.value } })} /></div>
          <textarea className="textarea" rows={4} value={draft.config.rubric} onChange={(e) => setDraft({ ...draft, config: { ...draft.config, rubric: e.target.value } })} />
        </>}
        {draft.type === "checks" && <textarea className="textarea mono" rows={6} defaultValue={JSON.stringify(draft.config.checks, null, 2)} onChange={(e) => { try { setDraft({ ...draft, config: { ...draft.config, checks: JSON.parse(e.target.value) } }); } catch { /* keep last valid */ } }}
          placeholder='{"required_tools":["search"],"tool_sequence":["search","answer"],"max_tool_calls":6,"output_regex":"","output_not_contains":["As an AI"]}' />}
        {draft.type === "scorer" && <div className="grid g2">
          <select className="select" value={draft.config.key} onChange={(e) => setDraft({ ...draft, config: { ...draft.config, key: e.target.value } })}>{["regex", "json_valid", "json_schema", "contains", "not_contains", "refusal", "similarity"].map((k) => <option key={k}>{k}</option>)}</select>
          <input className="input mono" placeholder={draft.config.key === "regex" ? "pattern" : "expected"} value={draft.config.key === "regex" ? draft.config.params.pattern : draft.config.expected ?? ""} onChange={(e) => setDraft({ ...draft, config: draft.config.key === "regex" ? { ...draft.config, params: { pattern: e.target.value } } : { ...draft.config, expected: e.target.value } })} />
        </div>}
        <div className="row"><input className="input mono grow" placeholder="trace id to dry-run on" value={traceId} onChange={(e) => setTraceId(e.target.value)} /><button className="btn" disabled={!traceId} onClick={runTest}>Test</button></div>
        {test && <pre className="al-json">{JSON.stringify(test.result, null, 2)}</pre>}
        <ErrorBox error={err} />
      </div>}
    </Modal>
  </>;
}

const METRICS = [["error_rate", "Error rate (0-1)"], ["fail_rate", "Check fail rate (0-1)"], ["p95_latency_ms", "p95 latency (ms)"], ["cost_usd", "Cost (USD)"], ["traces", "Trace count"], ["feedback_mean", "Feedback mean"]];

function Alerts() {
  const { data, reload } = useApi<Alert[]>("/api/alerts", { refreshMs: 15000 });
  const { data: events } = useApi<AlertEvent[]>("/api/alerts/events?limit=50", { refreshMs: 15000 });
  const [draft, setDraft] = useState<any>(null);
  const [err, setErr] = useState<Error | null>(null);
  const save = async () => { try { await api.post("/api/alerts", draft); setDraft(null); reload(); } catch (e: any) { setErr(e); } };
  const check = async () => { await api.post("/api/alerts/check", {}); reload(); invalidate("/api/alerts/events"); };
  const fmt = (m: string, v: number | null | undefined) => v == null ? "—" : m.endsWith("rate") ? pct(v) : m === "cost_usd" ? usd(v) : m === "p95_latency_ms" ? `${Math.round(v)} ms` : num(v, 2);
  return <div className="grid g-main" style={{ alignItems: "start" }}>
    <Card title="Alert rules" sub="Checked every minute; webhooks get Slack-compatible JSON on firing and resolve" flush actions={<div className="row" style={{ gap: 6 }}><button className="btn sm" onClick={check}><Icon name="refresh" size={13} />Check now</button><button className="btn sm primary" onClick={() => { setErr(null); setDraft({ name: "", metric: "error_rate", op: ">", threshold: 0.1, window_minutes: 15, min_samples: 10, service: "", webhook_url: "" }); }}><Icon name="plus" size={13} />Rule</button></div>}>
      {!data ? <Loading /> : !data.length ? <Empty title="No alert rules" /> : <table className="t"><thead><tr><th>Rule</th><th>Condition</th><th>State</th><th className="r">Last value</th><th /></tr></thead>
        <tbody>{data.map((a) => <tr key={a.id}><td><b>{a.name}</b><div className="faint">{a.service || "all services"}{a.webhook_url ? " · webhook" : ""}</div></td>
          <td className="mono">{a.metric}{a.feedback_name ? `(${a.feedback_name})` : ""} {a.op} {a.threshold} <span className="faint">/ {a.window_minutes}m</span></td>
          <td><Badge tone={a.state === "firing" ? "bad" : a.state === "ok" ? "good" : ""} dot={a.state === "firing"}>{(a.state ?? "not checked").replace("_", " ")}</Badge></td>
          <td className="r num">{fmt(a.metric, a.last_value)}<div className="faint">{a.last_checked_at ? ago(a.last_checked_at) : ""}</div></td>
          <td><button className="btn sm ghost" aria-label="Delete" onClick={async () => { await api.del(`/api/alerts/${a.id}`); reload(); }}><Icon name="trash" size={13} /></button></td></tr>)}</tbody></table>}
    </Card>
    <Card title="Alert history" flush>
      {!events?.length ? <Empty title="Nothing fired yet" /> : <table className="t"><tbody>{events.map((e) => <tr key={e.id}><td><Badge tone={e.state === "firing" ? "bad" : "good"}>{e.state}</Badge></td><td><b>{e.name}</b><div className="mono faint">{e.metric} = {fmt(e.metric, e.value)}{e.delivered === false ? " · webhook failed" : ""}</div></td><td className="muted">{ago(e.at)}</td></tr>)}</tbody></table>}
    </Card>
    <Modal open={!!draft} onClose={() => setDraft(null)} title="New alert rule" footer={<button className="btn primary" onClick={save} disabled={!draft?.name}>Create</button>}>
      {draft && <div className="grid g2">
        <input className="input" placeholder="Name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
        <select className="select" value={draft.metric} onChange={(e) => setDraft({ ...draft, metric: e.target.value })}>{METRICS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
        <div className="row"><select className="select" value={draft.op} onChange={(e) => setDraft({ ...draft, op: e.target.value })}><option>{">"}</option><option>{"<"}</option></select><input className="input" type="number" step="any" value={draft.threshold} onChange={(e) => setDraft({ ...draft, threshold: +e.target.value })} /></div>
        <label className="row">Window (min) <input className="input" type="number" min={1} style={{ width: 90 }} value={draft.window_minutes} onChange={(e) => setDraft({ ...draft, window_minutes: +e.target.value })} /></label>
        <label className="row">Min samples <input className="input" type="number" min={0} style={{ width: 90 }} value={draft.min_samples} onChange={(e) => setDraft({ ...draft, min_samples: +e.target.value })} /></label>
        <input className="input" placeholder="Service (all when empty)" value={draft.service} onChange={(e) => setDraft({ ...draft, service: e.target.value })} />
        {draft.metric === "feedback_mean" && <input className="input" placeholder="Feedback name" value={draft.feedback_name ?? ""} onChange={(e) => setDraft({ ...draft, feedback_name: e.target.value })} />}
        <input className="input mono" style={{ gridColumn: "1 / -1" }} placeholder="Webhook URL (Slack, Mattermost, any JSON endpoint)" value={draft.webhook_url} onChange={(e) => setDraft({ ...draft, webhook_url: e.target.value })} />
        <div style={{ gridColumn: "1 / -1" }}><ErrorBox error={err} /></div>
      </div>}
    </Modal>
  </div>;
}

type Tab = "annotate" | "evaluators" | "alerts";
export default function Quality() {
  const { query } = useLocation();
  const tab = (query.get("tab") as Tab) || "annotate";
  return <div className="page">
    <div className="page-head"><div className="grow">
      <div className="eyebrow">Quality loop</div>
      <h1 className="page-title">Human review, automated evaluators, alerts</h1>
      <div className="page-sub">Production traces flow into annotation queues and online evaluators; their scores feed monitoring, calibrate LLM judges and trigger alerts.</div>
    </div></div>
    <Tabs value={tab} onChange={(t) => navigate(`/quality?tab=${t}`, true)} tabs={[{ value: "annotate", label: "Annotation queues" }, { value: "evaluators", label: "Online evaluators" }, { value: "alerts", label: "Alerts" }]} />
    <div style={{ marginTop: 14 }}>{tab === "annotate" ? <Queues /> : tab === "evaluators" ? <Evaluators /> : <Alerts />}</div>
  </div>;
}
