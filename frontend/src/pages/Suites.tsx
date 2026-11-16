import { useState } from "react";
import { api } from "../api";
import { MiniBar } from "../charts/charts";
import { Badge, Card, CopyButton, Empty, ErrorBox, Icon, Loading } from "../components/ui";
import { ago, ms, pct, usd } from "../format";
import { invalidate, useApi } from "../hooks";
import { navigate, useLocation } from "../router";
import type { Interval } from "../types";

interface ScenarioRow { scenario_id: string; name: string; pass: number; fail: number; unknown: number; pending: number; pass_rate: number | null }
interface Result {
  complete: boolean; runs: number; pass: number; fail: number; unknown: number; pending: number; pass_rate: Interval; cost_usd: number; latency_ms: { p50: number | null; p95: number | null };
  scenarios: ScenarioRow[]; gate: { status: string; reasons: string[]; notes?: string[]; policy?: Record<string, number> };
  comparison?: { baseline_suite_id: string; paired_scenarios: number; pass_rate_diff: Interval; regressions: any[]; improvements: any[]; mcnemar_p: number | null; baseline_pass_rate: Interval };
}
interface Suite { id: string; collection: string; mode: string; repeats: number; overrides: Record<string, any>; gate: Record<string, number>; baseline_suite_id?: string; label: string; status: string; created_at: number; dispatched: number; scenarios: number; dispatch_errors?: string[]; result?: Result; runs?: any[] }

const gateTone = (s?: string) => s === "passed" ? "good" : s === "failed" ? "bad" : "live";

function SuiteDetail({ id }: { id: string }) {
  const { data: s, error } = useApi<Suite>(`/api/workbench/suites/${id}`, { refreshMs: 2500 });
  if (error) return <ErrorBox error={error} />;
  if (!s?.result) return <Loading rows={5} />;
  const r = s.result, cmp = r.comparison;
  return <div className="col" style={{ gap: 14 }}>
    <div className="grid g4">
      <div className="card stat"><div className="stat-label">Gate</div><div className="stat-value"><Badge tone={gateTone(r.gate.status)} dot={r.gate.status === "pending"}>{r.gate.status}</Badge></div><div className="stat-foot">{r.gate.reasons.join("; ") || (r.gate.notes?.length ? <span className="warn">{r.gate.notes.join("; ")}</span> : "all conditions met")}</div></div>
      <div className="card stat"><div className="stat-label">Pass rate</div><div className="stat-value">{pct(r.pass_rate.estimate)}</div><div className="stat-foot">95% CI {pct(r.pass_rate.low)}–{pct(r.pass_rate.high)} · {r.pass}/{r.pass + r.fail}{r.unknown ? ` · ${r.unknown} undecided` : ""}</div></div>
      <div className="card stat"><div className="stat-label">Latency</div><div className="stat-value">{ms(r.latency_ms.p50)}</div><div className="stat-foot">p95 {ms(r.latency_ms.p95)}</div></div>
      <div className="card stat"><div className="stat-label">Cost</div><div className="stat-value">{usd(r.cost_usd)}</div><div className="stat-foot">{r.runs} runs · {r.pending} pending</div></div>
    </div>
    {cmp && <Card title="Against the baseline" sub={<>suite <a href={`/suites?id=${cmp.baseline_suite_id}`} className="mono">{cmp.baseline_suite_id}</a> · {cmp.paired_scenarios} paired scenarios</>}>
      <div className="row wrap" style={{ gap: 16 }}>
        <span>Pass-rate change <b className="num">{cmp.pass_rate_diff.estimate == null ? "—" : pct(cmp.pass_rate_diff.estimate, 1, true)}</b> <span className="muted">(95% CI {pct(cmp.pass_rate_diff.low, 1, true)} to {pct(cmp.pass_rate_diff.high, 1, true)})</span></span>
        <span className="bad">{cmp.regressions.length} regressed</span><span className="good">{cmp.improvements.length} improved</span>
        <span className="muted">exact McNemar p = {cmp.mcnemar_p == null ? "—" : cmp.mcnemar_p.toFixed(3)}</span>
      </div>
      {cmp.regressions.length > 0 && <ul style={{ margin: "10px 0 0", paddingLeft: 18 }}>{cmp.regressions.map((x) => <li key={x.scenario_id}><b>{x.name}</b> <span className="muted">{pct(x.before)} → {pct(x.after)}</span></li>)}</ul>}
    </Card>}
    <Card title="Scenarios" flush>
      <table className="t"><thead><tr><th>Scenario</th><th style={{ width: 220 }}>Pass rate</th><th className="r">Pass</th><th className="r">Fail</th><th className="r">Undecided</th></tr></thead>
        <tbody>{r.scenarios.map((x) => <tr key={x.scenario_id} className="click" onClick={() => navigate(`/workbench?scenario=${x.scenario_id}`)}>
          <td>{x.name}</td><td>{x.pass_rate == null ? <span className="faint">{x.pending ? "running…" : "—"}</span> : <MiniBar iv={{ estimate: x.pass_rate, low: null, high: null, n: x.pass + x.fail, method: "" }} color={x.pass_rate >= 0.5 ? "var(--s1)" : "var(--bad)"} />}</td>
          <td className="r num">{x.pass}</td><td className="r num">{x.fail}</td><td className="r num">{x.unknown + x.pending}</td></tr>)}</tbody></table>
    </Card>
    {!!s.dispatch_errors?.length && <div className="callout warn">{s.dispatch_errors.join("; ")}</div>}
  </div>;
}

export default function Suites() {
  const { query } = useLocation();
  const id = query.get("id") || "";
  const { data: scenarios } = useApi<{ collection: string }[]>("/api/workbench/scenarios");
  const { data: suites } = useApi<Suite[]>("/api/workbench/suites", { refreshMs: 5000 });
  const collections = [...new Set((scenarios ?? []).map((s) => s.collection))].sort();
  const [form, setForm] = useState({ collection: "", mode: "", provider: "", model: "", system_prompt: "", repeats: 1, baseline: "latest", min_pass_rate: "", max_regressions: 0 });
  const [err, setErr] = useState<Error | null>(null);
  const [busy, setBusy] = useState(false);
  const collection = form.collection || collections[0] || "";
  const run = async () => {
    setBusy(true); setErr(null);
    try {
      const overrides: Record<string, string> = {};
      for (const k of ["provider", "model", "system_prompt"] as const) if (form[k]) overrides[k] = form[k];
      const gate: Record<string, number> = { max_regressions: form.max_regressions };
      if (form.min_pass_rate !== "") gate.min_pass_rate = +form.min_pass_rate;
      const s = await api.post<Suite>("/api/workbench/suites", { collection, mode: form.mode, overrides, repeats: form.repeats, baseline: form.baseline, gate });
      invalidate("/api/workbench/suites"); navigate(`/suites?id=${s.id}`);
    } catch (e: any) { setErr(e); } finally { setBusy(false); }
  };
  const cli = `arl suite --url ${location.origin} --collection "${collection}"${form.model ? ` --model ${form.model}` : ""} --repeats ${form.repeats} --baseline latest${form.min_pass_rate ? ` --min-pass-rate ${form.min_pass_rate}` : ""} --max-regressions ${form.max_regressions}`;
  return <div className="page">
    <div className="page-head"><div className="grow">
      <div className="eyebrow">Regression suites</div>
      <h1 className="page-title">Did this change make the agent worse?</h1>
      <div className="page-sub">Run every scenario of a Workbench collection — with a new model, prompt or parameters, repeated to average out sampling noise — and compare it scenario by scenario with a baseline. The gate turns it into pass/fail for CI.</div>
    </div>{id && <button className="btn" onClick={() => navigate("/suites")}><Icon name="back" />All suites</button>}</div>
    {id ? <SuiteDetail id={id} /> : <div className="grid g-main" style={{ alignItems: "start" }}>
      <div className="col" style={{ gap: 14 }}>
        <Card title="Run a suite">
          {!collections.length ? <Empty title="No scenarios yet">Capture scenarios from traces in the Workbench first.</Empty> : <div className="col" style={{ gap: 10 }}>
            <div className="grid g2">
              <label className="field"><span className="wb-label">Collection</span><select className="select" value={collection} onChange={(e) => setForm({ ...form, collection: e.target.value })}>{collections.map((c) => <option key={c}>{c}</option>)}</select></label>
              <label className="field"><span className="wb-label">Mode</span><select className="select" value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value })}><option value="">each scenario's own</option><option value="llm">LLM step</option><option value="agent">agent replay</option></select></label>
              <label className="field"><span className="wb-label">Override provider</span><input className="input mono" value={form.provider} onChange={(e) => setForm({ ...form, provider: e.target.value })} placeholder="keep" /></label>
              <label className="field"><span className="wb-label">Override model</span><input className="input mono" value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} placeholder="keep" /></label>
            </div>
            <label className="field"><span className="wb-label">Override system prompt</span><textarea className="textarea mono" rows={3} value={form.system_prompt} onChange={(e) => setForm({ ...form, system_prompt: e.target.value })} placeholder="keep each scenario's prompt" /></label>
            <div className="grid g4">
              <label className="field"><span className="wb-label">Repeats</span><input className="input" type="number" min={1} max={20} value={form.repeats} onChange={(e) => setForm({ ...form, repeats: +e.target.value || 1 })} /></label>
              <label className="field"><span className="wb-label">Baseline</span><select className="select" value={form.baseline} onChange={(e) => setForm({ ...form, baseline: e.target.value })}><option value="latest">latest completed</option><option value="none">none</option>{(suites ?? []).filter((s) => s.collection === collection && s.status === "completed").map((s) => <option key={s.id} value={s.id}>{s.id.slice(0, 14)} · {ago(s.created_at)}</option>)}</select></label>
              <label className="field"><span className="wb-label">Min pass rate</span><input className="input" type="number" step={0.05} min={0} max={1} value={form.min_pass_rate} onChange={(e) => setForm({ ...form, min_pass_rate: e.target.value })} placeholder="none" /></label>
              <label className="field"><span className="wb-label">Max regressions</span><input className="input" type="number" min={0} value={form.max_regressions} onChange={(e) => setForm({ ...form, max_regressions: +e.target.value })} /></label>
            </div>
            <ErrorBox error={err} />
            <div className="row"><button className="btn primary" disabled={busy || !collection} onClick={run}><Icon name="play" />Run suite</button></div>
          </div>}
        </Card>
        <Card title="Same thing in CI" sub="The lab image includes the client; exit code 1 means the gate failed">
          <div className="row" style={{ alignItems: "start" }}><pre className="al-json grow" style={{ whiteSpace: "pre-wrap" }}>{cli}</pre><CopyButton text={cli} /></div>
        </Card>
      </div>
      <Card title="Recent suites" flush>
        {!suites ? <Loading /> : !suites.length ? <Empty title="No suites yet" /> : <table className="t"><tbody>{suites.map((s) => <tr key={s.id} className="click" onClick={() => navigate(`/suites?id=${s.id}`)}>
          <td><b>{s.collection}</b><div className="mono faint trunc" style={{ maxWidth: 220 }}>{Object.values(s.overrides).filter((v) => typeof v === "string").join(" · ").slice(0, 60) || "no overrides"} · ×{s.repeats}</div></td>
          <td>{s.result?.gate ? <Badge tone={gateTone(s.result.gate.status)}>{s.result.gate.status}</Badge> : <Badge tone="live" dot>running</Badge>}</td>
          <td className="r num">{s.result?.pass_rate ? pct(s.result.pass_rate.estimate) : "—"}</td>
          <td className="muted">{ago(s.created_at)}</td></tr>)}</tbody></table>}
      </Card>
    </div>}
  </div>;
}
