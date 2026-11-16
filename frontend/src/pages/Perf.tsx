import { useState } from "react";
import { api } from "../api";
import { ShareBar } from "../charts/timeseries";
import { Badge, Card, Empty, ErrorBox, Icon, Loading } from "../components/ui";
import { ago, ms, num, pct, usd } from "../format";
import { invalidate, useApi } from "../hooks";
import { navigate, useLocation } from "../router";
import type { Dist, Provider } from "../types";

interface Level { concurrency: number; requests: number; ok: number; errors: Record<string, number>; error_rate: number; wall_s: number; latency_ms: Dist; ttft_ms: Dist; decode_tokens_per_s: Dist; output_tokens: number; requests_per_s?: number; output_tokens_per_s?: number; cost_usd: number }
interface PerfRun { id: string; provider: string; model: string; status: string; created_at: number; concurrency: number[]; requests: number; max_tokens: number; stream: boolean; base_url: string; label: string; levels: Level[]; environment: Record<string, any>; estimated_cost_usd: number | null; saturation?: { concurrency: number | null; requests_per_s?: number; output_tokens_per_s?: number; note: string } }

function RunDetail({ id }: { id: string }) {
  const { data: r } = useApi<PerfRun>(`/api/perf/${id}`, { refreshMs: 2000 });
  if (!r) return <Loading rows={5} />;
  const maxRps = Math.max(1e-9, ...r.levels.map((l) => l.requests_per_s ?? 0));
  const maxTps = Math.max(1e-9, ...r.levels.map((l) => l.output_tokens_per_s ?? 0));
  const maxP95 = Math.max(1e-9, ...r.levels.map((l) => l.latency_ms.p95 ?? 0));
  return <div className="col" style={{ gap: 14 }}>
    <Card title={<span className="mono">{r.provider} · {r.model}</span>} sub={<>{r.requests} requests per level · max {r.max_tokens} tokens · {r.stream ? "streaming" : "non-streaming"} · {r.base_url || "built-in"}</>}
      actions={<Badge tone={r.status === "completed" ? "good" : r.status === "cancelled" ? "" : "live"} dot={r.status === "running"}>{r.status}</Badge>}>
      {r.saturation?.concurrency != null ? <div className="callout good">Sweet spot: <b>concurrency {r.saturation.concurrency}</b> — {num(r.saturation.requests_per_s, 2)} req/s, {num(r.saturation.output_tokens_per_s, 0)} output tokens/s. <span className="muted">{r.saturation.note}</span></div>
        : r.saturation ? <div className="callout warn">{r.saturation.note}</div> : null}
      <div className="muted" style={{ fontSize: 12, marginTop: 8 }}>Load generator: {r.environment.os}/{r.environment.arch}, {r.environment.cpus} CPUs, {r.environment.go}. Latency includes the network path from the lab to the endpoint.</div>
    </Card>
    <Card title="Per concurrency level" flush reading="Throughput should rise with concurrency until the server saturates; past that point latency climbs while requests/s flattens. Decode speed is per request (tokens after the first, divided by generation time).">
      {!r.levels.length ? <Loading /> : <div className="table-wrap"><table className="t">
        <thead><tr><th className="r">Concurrency</th><th>Requests/s</th><th>Output tokens/s</th><th className="r">TTFT p50</th><th>Latency p50 · p95</th><th className="r">Decode tok/s p50</th><th className="r">Errors</th><th className="r">Cost</th></tr></thead>
        <tbody>{r.levels.map((l) => <tr key={l.concurrency} className={r.saturation?.concurrency === l.concurrency ? "wb-current" : ""}>
          <td className="r num"><b>{l.concurrency}</b></td>
          <td style={{ minWidth: 140 }}><div className="row" style={{ gap: 8 }}><span className="num" style={{ width: 48 }}>{num(l.requests_per_s, 2)}</span><div className="grow"><ShareBar value={l.requests_per_s ?? 0} max={maxRps} /></div></div></td>
          <td style={{ minWidth: 140 }}><div className="row" style={{ gap: 8 }}><span className="num" style={{ width: 48 }}>{num(l.output_tokens_per_s, 0)}</span><div className="grow"><ShareBar value={l.output_tokens_per_s ?? 0} max={maxTps} color="var(--s2)" /></div></div></td>
          <td className="r num">{ms(l.ttft_ms.p50)}</td>
          <td style={{ minWidth: 160 }}><div className="row" style={{ gap: 8 }}><span className="num" style={{ width: 96 }}>{ms(l.latency_ms.p50)} · {ms(l.latency_ms.p95)}</span><div className="grow"><ShareBar value={l.latency_ms.p95 ?? 0} max={maxP95} color="var(--s3)" /></div></div></td>
          <td className="r num">{num(l.decode_tokens_per_s.p50, 0)}</td>
          <td className={`r num ${l.error_rate ? "bad" : ""}`} title={Object.entries(l.errors).map(([k, v]) => `${k}: ${v}`).join(", ")}>{pct(l.error_rate, 0)}</td>
          <td className="r num">{usd(l.cost_usd)}</td></tr>)}</tbody></table></div>}
    </Card>
  </div>;
}

export default function Perf() {
  const { query } = useLocation();
  const id = query.get("id") || "";
  const { data: providers } = useApi<Provider[]>("/api/providers");
  const { data: runs } = useApi<PerfRun[]>("/api/perf", { refreshMs: 5000 });
  const [form, setForm] = useState({ provider: "local", model: "", prompt: "", max_tokens: 256, concurrency: "1,2,4,8", requests: 8, stream: true, confirm_paid: false, temperature: 0.2 });
  const [err, setErr] = useState<Error | null>(null);
  const start = async () => {
    setErr(null);
    try {
      const r = await api.post<PerfRun>("/api/perf", { ...form, concurrency: form.concurrency.split(",").map((s) => +s.trim()).filter(Boolean) });
      invalidate("/api/perf"); navigate(`/perf?id=${r.id}`);
    } catch (e: any) { setErr(e); }
  };
  return <div className="page">
    <div className="page-head"><div className="grow">
      <div className="eyebrow">Performance lab</div>
      <h1 className="page-title">Load-test a model endpoint</h1>
      <div className="page-sub">Send the same request at rising concurrency and measure time-to-first-token, latency percentiles, decode speed and throughput — to size a local model on your hardware (Ollama, vLLM, LM Studio) or compare hosted providers under your agent's load.</div>
    </div>{id && <button className="btn" onClick={() => navigate("/perf")}><Icon name="back" />All runs</button>}</div>
    {id ? <RunDetail id={id} /> : <div className="grid g-main" style={{ alignItems: "start" }}>
      <Card title="New load test">
        <div className="col" style={{ gap: 10 }}>
          <div className="grid g2">
            <label className="field"><span className="wb-label">Provider</span><select className="select" value={form.provider} onChange={(e) => setForm({ ...form, provider: e.target.value })}>{(providers ?? []).filter((p) => p.kind !== "imported").map((p) => <option key={p.name} value={p.name}>{p.label}{p.configured ? "" : " (not configured)"}</option>)}</select></label>
            <label className="field"><span className="wb-label">Model</span><input className="input mono" value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} placeholder={form.provider === "fake" ? "fake/oracle-90" : "llama3.1:8b"} /></label>
            <label className="field"><span className="wb-label">Concurrency levels</span><input className="input mono" value={form.concurrency} onChange={(e) => setForm({ ...form, concurrency: e.target.value })} /></label>
            <label className="field"><span className="wb-label">Requests per level</span><input className="input" type="number" min={1} max={200} value={form.requests} onChange={(e) => setForm({ ...form, requests: +e.target.value })} /></label>
            <label className="field"><span className="wb-label">Max output tokens</span><input className="input" type="number" min={1} max={8192} value={form.max_tokens} onChange={(e) => setForm({ ...form, max_tokens: +e.target.value })} /></label>
            <label className="row" style={{ alignSelf: "end" }}><input type="checkbox" checked={form.stream} onChange={(e) => setForm({ ...form, stream: e.target.checked })} />Stream (measures TTFT)</label>
          </div>
          <label className="field"><span className="wb-label">Prompt (default: a short paragraph request)</span><textarea className="textarea" rows={3} value={form.prompt} onChange={(e) => setForm({ ...form, prompt: e.target.value })} /></label>
          <label className="row"><input type="checkbox" checked={form.confirm_paid} onChange={(e) => setForm({ ...form, confirm_paid: e.target.checked })} />I accept the cost of paid providers for this test</label>
          <ErrorBox error={err} />
          <div className="row"><button className="btn primary" disabled={!form.model} onClick={start}><Icon name="bolt" />Start</button></div>
        </div>
      </Card>
      <Card title="Runs" flush>
        {!runs ? <Loading /> : !runs.length ? <Empty title="No load tests yet" /> : <table className="t"><tbody>{runs.map((r) => <tr key={r.id} className="click" onClick={() => navigate(`/perf?id=${r.id}`)}>
          <td><b className="mono trunc" style={{ maxWidth: 200, display: "block" }}>{r.model}</b><div className="faint">{r.provider} · c={r.concurrency.join(",")}</div></td>
          <td><Badge tone={r.status === "completed" ? "good" : "live"} dot={r.status === "running"}>{r.status}</Badge></td>
          <td className="r num">{r.saturation?.requests_per_s ? `${num(r.saturation.requests_per_s, 1)} req/s` : "—"}</td>
          <td className="muted">{ago(r.created_at)}</td></tr>)}</tbody></table>}
      </Card>
    </div>}
  </div>;
}
