import { useMemo, useState } from "react";
import { Lines, ShareBar, StackedBars } from "../charts/timeseries";
import { A, Badge, Card, Empty, ErrorBox, Icon, Loading, Seg, Stat } from "../components/ui";
import { ago, compact, ms, num, pct, usd } from "../format";
import { useApi, useDebounced } from "../hooks";
import { navigate, useLocation } from "../router";

interface Metrics {
  window: { hours: number; since: number; bucket_s: number; service: string };
  totals: { traces: number; errored: number; running: number; passed: number; failed: number; cost_usd: number; tokens_in: number; tokens_out: number; model_calls: number; tool_calls: number; p50_ms: number | null; p95_ms: number | null; p99_ms: number | null; threads: number; users: number; error_rate?: number; pass_rate?: number };
  series: { ts: number; traces: number; errored: number; failed: number; cost_usd: number; tokens: number; p50_ms: number | null; p95_ms: number | null }[];
  services: { service: string; traces: number; errored: number; cost_usd: number; p95_ms: number | null; source: string }[];
  models: { model: string; calls: number; errored: number; tokens_in: number; tokens_out: number; p50_ms: number | null; p95_ms: number | null; cost_usd: number | null; unknown_cost_calls: number | null }[];
  tools: { tool: string; calls: number; errored: number; p95_ms: number | null }[];
  feedback: { name: string; source: string; n: number; mean: number | null; positive: number }[];
  failing_checks: { name: string; failures: number }[];
}
interface Facets { services: string[]; models: string[]; tags: string[]; feedback: string[] }
export interface TraceRow {
  id: string; name: string; status: string; started_at: number; service: string; duration_ms: number | null; cost_usd: number; tokens_in: number; tokens_out: number;
  model_calls: number; tool_calls: number; errors: number; models: string[]; tags: string[]; thread_id: string | null; user_id: string | null; source: string; verdict: string | null; feedback_count: number;
}

const WINDOWS = [{ value: "1", label: "1h" }, { value: "24", label: "24h" }, { value: "168", label: "7d" }, { value: "720", label: "30d" }];
const tone = (v?: string | null) => v === "pass" || v === "completed" ? "good" : v === "fail" || v === "failed" ? "bad" : v === "running" ? "live" : "";

/** Filterable trace table shared by Monitor and Quality. */
export function TraceExplorer({ hours, service, onPick }: { hours: string; service: string; onPick?: (t: TraceRow) => void }) {
  const { data: facets } = useApi<Facets>("/api/monitor/facets", { staleMs: 30000 });
  const { query } = useLocation();
  const [f, setF] = useState({ q: "", verdict: "", model: "", tag: "", thread_id: query.get("thread") ?? "", errors: false, min_ms: "" });
  const q = useDebounced(f, 300);
  const params = new URLSearchParams({ hours, limit: "100" });
  if (service) params.set("service_name", service);
  for (const [k, v] of Object.entries(q)) if (v) params.set(k, String(v));
  const { data: rows, loading } = useApi<TraceRow[]>(`/api/agent-lab/runs?${params}`, { refreshMs: 8000 });
  const set = (k: keyof typeof f, v: any) => setF({ ...f, [k]: v });
  return <>
    <div className="row wrap" style={{ gap: 8, padding: "10px 14px", borderBottom: "1px solid var(--grat)" }}>
      <input className="input" style={{ maxWidth: 220 }} placeholder="Search name or id" value={f.q} onChange={(e) => set("q", e.target.value)} />
      <select className="select" style={{ width: "auto" }} value={f.verdict} onChange={(e) => set("verdict", e.target.value)} aria-label="Verdict"><option value="">Any verdict</option><option value="pass">pass</option><option value="fail">fail</option><option value="insufficient_evidence">insufficient evidence</option></select>
      <select className="select" style={{ width: "auto" }} value={f.model} onChange={(e) => set("model", e.target.value)} aria-label="Model"><option value="">Any model</option>{facets?.models.map((m) => <option key={m}>{m}</option>)}</select>
      {!!facets?.tags.length && <select className="select" style={{ width: "auto" }} value={f.tag} onChange={(e) => set("tag", e.target.value)} aria-label="Tag"><option value="">Any tag</option>{facets.tags.map((m) => <option key={m}>{m}</option>)}</select>}
      <input className="input mono" style={{ maxWidth: 160 }} placeholder="thread id" value={f.thread_id} onChange={(e) => set("thread_id", e.target.value)} />
      <input className="input" type="number" min={0} style={{ maxWidth: 120 }} placeholder="min ms" value={f.min_ms} onChange={(e) => set("min_ms", e.target.value)} />
      <label className="row" style={{ gap: 6 }}><input type="checkbox" checked={f.errors} onChange={(e) => set("errors", e.target.checked)} />errors only</label>
    </div>
    {loading && !rows ? <Loading rows={4} /> : !rows?.length ? <Empty title="No traces match">Agents report through the SDK WebSocket or OpenTelemetry (<span className="mono">/api/otel/v1/traces</span>).</Empty> : (
      <div className="table-wrap"><table className="t">
        <thead><tr><th>Trace</th><th>Status</th><th className="r">Latency</th><th className="r">Tokens</th><th className="r">Cost</th><th>Models · tools</th><th>Context</th><th>When</th></tr></thead>
        <tbody>{rows.map((t) => (
          <tr key={t.id} className="click" onClick={() => onPick ? onPick(t) : navigate(`/agent-lab?run=${encodeURIComponent(t.id)}`)}>
            <td style={{ maxWidth: 280 }}><div className="trunc" style={{ fontWeight: 500 }}>{t.name}</div><div className="mono faint trunc">{t.service}{t.source === "otlp" ? " · otel" : ""}</div></td>
            <td><div className="row" style={{ gap: 4 }}><Badge tone={tone(t.status)} dot={t.status === "running"}>{t.status}</Badge>{t.verdict && t.verdict !== "insufficient_evidence" && <Badge tone={tone(t.verdict)}>{t.verdict}</Badge>}{t.errors > 0 && <Badge tone="bad">{t.errors} err</Badge>}</div></td>
            <td className="r num">{ms(t.duration_ms)}</td>
            <td className="r num">{compact(t.tokens_in + t.tokens_out)}</td>
            <td className="r num">{usd(t.cost_usd)}</td>
            <td className="mono muted trunc" style={{ maxWidth: 200 }}>{t.models.join(", ") || "—"}{t.tool_calls ? ` · ${t.tool_calls} tools` : ""}</td>
            <td><div className="row wrap" style={{ gap: 4 }}>{t.thread_id && <span className="tag mono" title="thread">⇄ {t.thread_id.slice(0, 12)}</span>}{t.tags.map((g) => <span key={g} className="tag">{g}</span>)}{t.feedback_count > 0 && <span className="tag">★ {t.feedback_count}</span>}</div></td>
            <td className="muted" style={{ whiteSpace: "nowrap" }}>{ago(t.started_at)}</td>
          </tr>))}</tbody>
      </table></div>
    )}
  </>;
}

export default function Monitor() {
  const [hours, setHours] = useState("24");
  const [service, setService] = useState("");
  const { data: facets } = useApi<Facets>("/api/monitor/facets", { staleMs: 30000 });
  const { data, error, loading } = useApi<Metrics>(`/api/monitor/metrics?hours=${hours}${service ? `&service=${encodeURIComponent(service)}` : ""}`, { refreshMs: 10000, staleMs: 5000 });
  const series = useMemo(() => {
    if (!data) return null;
    // Fill empty buckets so the time axis is continuous.
    const b = data.window.bucket_s;
    const start = Math.floor(data.window.since / b) * b;
    const byTs = new Map(data.series.map((s) => [s.ts, s]));
    const out = [];
    for (let t = start; t <= Date.now() / 1000; t += b) out.push(byTs.get(t) ?? { ts: t, traces: 0, errored: 0, failed: 0, cost_usd: 0, tokens: 0, p50_ms: null, p95_ms: null });
    return out;
  }, [data]);
  const t = data?.totals;
  const maxModelCalls = Math.max(1, ...(data?.models.map((m) => m.calls) ?? []));
  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <div className="eyebrow">Production monitoring</div>
          <h1 className="page-title">How the agents behave right now</h1>
          <div className="page-sub">Volume, errors, latency percentiles, tokens and cost for every traced run — from the SDK or any OpenTelemetry exporter — with breakdowns per model, tool and service.</div>
        </div>
        <select className="select" style={{ width: "auto" }} value={service} onChange={(e) => setService(e.target.value)} aria-label="Service"><option value="">All services</option>{facets?.services.map((s) => <option key={s}>{s}</option>)}</select>
        <Seg value={hours} options={WINDOWS} onChange={setHours} />
      </div>
      <ErrorBox error={error} />
      {loading && !data ? <Loading rows={6} /> : t && series && <>
        <div className="grid g4" style={{ marginBottom: 14 }}>
          <Stat label="Traces" value={t.traces} foot={`${t.threads} threads · ${t.users} users · ${t.running} running`} />
          <Stat label="Error rate" value={t.error_rate ?? null} format={(v) => pct(v)} foot={`${t.errored} traces with an error or failed status`} />
          <Stat label="Check pass rate" value={t.pass_rate ?? null} format={(v) => pct(v)} foot={`${t.passed} pass · ${t.failed} fail (traces with checks)`} />
          <Stat label="Known cost" value={t.cost_usd} format={(v) => usd(v)} foot={`${compact(t.tokens_in)} in · ${compact(t.tokens_out)} out tokens`} />
        </div>
        <div className="grid g3" style={{ marginBottom: 14 }}>
          <Card title="Traffic" sub="Traces per bucket, errored on top">
            <StackedBars label="Traces per time bucket" times={series.map((s) => s.ts)} bucket={data!.window.bucket_s} stacks={[
              { key: "ok", label: "ok", color: "var(--s1)", values: series.map((s) => s.traces - s.errored) },
              { key: "err", label: "errored", color: "var(--bad)", values: series.map((s) => s.errored) }]} />
          </Card>
          <Card title="Latency" sub={`p50 ${ms(t.p50_ms)} · p95 ${ms(t.p95_ms)} · p99 ${ms(t.p99_ms)}`}>
            <Lines label="Trace latency percentiles" times={series.map((s) => s.ts)} bucket={data!.window.bucket_s} fmt={ms} series={[
              { key: "p50", label: "p50", color: "var(--s1)", values: series.map((s) => s.p50_ms) },
              { key: "p95", label: "p95", color: "var(--s2)", values: series.map((s) => s.p95_ms), dashed: true }]} />
          </Card>
          <Card title="Cost" sub="Known spend per bucket">
            <StackedBars label="Cost per time bucket" floor={0.0001} times={series.map((s) => s.ts)} bucket={data!.window.bucket_s} fmt={(v) => usd(v)} stacks={[{ key: "cost", label: "cost", color: "var(--s3)", values: series.map((s) => s.cost_usd) }]} />
          </Card>
        </div>
        <div className="grid g-main" style={{ marginBottom: 14, alignItems: "start" }}>
          <Card title="Models" sub="Per model call (model.response events)" flush>
            {!data!.models.length ? <Empty title="No model calls in this window" /> : <div className="table-wrap"><table className="t">
              <thead><tr><th>Model</th><th className="r">Calls</th><th /><th className="r">Errors</th><th className="r">p50</th><th className="r">p95</th><th className="r">Tokens in / out</th><th className="r">Cost</th></tr></thead>
              <tbody>{data!.models.map((m) => <tr key={m.model}>
                <td className="mono trunc" style={{ maxWidth: 220 }}>{m.model}</td><td className="r num">{num(m.calls)}</td><td style={{ width: 90 }}><ShareBar value={m.calls} max={maxModelCalls} /></td>
                <td className={`r num ${m.errored ? "bad" : ""}`}>{m.errored ? pct(m.errored / m.calls) : "0%"}</td><td className="r num">{ms(m.p50_ms)}</td><td className="r num">{ms(m.p95_ms)}</td>
                <td className="r num">{compact(m.tokens_in)} / {compact(m.tokens_out)}</td><td className="r num">{usd(m.cost_usd)}{m.unknown_cost_calls ? <span className="warn" title="calls without a reported or priced cost"> +{m.unknown_cost_calls}?</span> : null}</td>
              </tr>)}</tbody></table></div>}
          </Card>
          <div className="col" style={{ gap: 14 }}>
            <Card title="Tools" flush>
              {!data!.tools.length ? <Empty title="No tool calls" /> : <table className="t"><thead><tr><th>Tool</th><th className="r">Calls</th><th className="r">Errors</th><th className="r">p95</th></tr></thead>
                <tbody>{data!.tools.map((x) => <tr key={x.tool}><td className="mono trunc" style={{ maxWidth: 160 }}>{x.tool}</td><td className="r num">{x.calls}</td><td className={`r num ${x.errored ? "bad" : ""}`}>{x.errored}</td><td className="r num">{ms(x.p95_ms)}</td></tr>)}</tbody></table>}
            </Card>
            <Card title="Feedback" sub="End users, reviewers and online evaluators" flush actions={<A href="/quality" className="btn sm ghost">Quality<Icon name="arrow" size={13} /></A>}>
              {!data!.feedback.length ? <Empty title="No feedback yet">Send it with <span className="mono">lab.feedback(...)</span> or configure an evaluator.</Empty> : <table className="t"><thead><tr><th>Name</th><th>Source</th><th className="r">n</th><th className="r">Mean</th></tr></thead>
                <tbody>{data!.feedback.map((x) => <tr key={x.name + x.source}><td>{x.name}</td><td><span className="tag">{x.source}</span></td><td className="r num">{x.n}</td><td className="r num">{x.mean == null ? "—" : num(x.mean, 2)}</td></tr>)}</tbody></table>}
            </Card>
            {!!data!.failing_checks.length && <Card title="Most failed checks" flush>
              <table className="t"><tbody>{data!.failing_checks.map((c) => <tr key={c.name}><td className="trunc" style={{ maxWidth: 240 }}>{c.name}</td><td className="r num bad">{c.failures}</td></tr>)}</tbody></table>
            </Card>}
          </div>
        </div>
        {data!.services.length > 1 && <Card title="Services" flush style={{ marginBottom: 14 }}>
          <table className="t"><thead><tr><th>Service</th><th>Source</th><th className="r">Traces</th><th className="r">Error rate</th><th className="r">p95</th><th className="r">Cost</th></tr></thead>
            <tbody>{data!.services.map((s) => <tr key={s.service} className="click" onClick={() => setService(s.service)}><td>{s.service}</td><td><span className="tag">{s.source}</span></td><td className="r num">{s.traces}</td><td className="r num">{pct(s.errored / Math.max(1, s.traces))}</td><td className="r num">{ms(s.p95_ms)}</td><td className="r num">{usd(s.cost_usd)}</td></tr>)}</tbody></table>
        </Card>}
      </>}
      <Card title="Trace explorer" sub="Filter by verdict, model, tag, thread, latency or errors; open a trace for its full timeline" flush>
        <TraceExplorer hours={hours} service={service} />
      </Card>
    </div>
  );
}
