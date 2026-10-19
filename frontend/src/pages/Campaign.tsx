import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { BudgetGauge, CIBars, GridData, Heatmap, LatencyStrip, MiniBar, Pareto, Sparkline, TrialGrid, useWidth } from "../charts/charts";
import { EvidenceScope, RouterFlow, RouteTable } from "../charts/Explainers";
import { PathEvent, SignalPath } from "../charts/SignalPath";
import { OutcomeBadge, TrialDrawer } from "../components/TrialDrawer";
import { A, Badge, Card, ErrorBox, Icon, Labels, Loading, Modal, Pager, Seg, StatusBadge, Tabs, useToast, useTooltip } from "../components/ui";
import { duration, ms, num, pct, seriesColor, short, usd } from "../format";
import { invalidate, useApi, useDebounced, useEventSource } from "../hooks";
import { navigate } from "../router";
import type { Campaign, Comparison, LiveState, Page, Results, TrialRow } from "../types";

const LIVE = ["queued", "running", "grading", "analyzing"];

export default function CampaignPage({ id }: { id: string }) {
  const [poll, setPoll] = useState(true);
  const { data: c, error, reload } = useApi<Campaign>(`/api/campaigns/${id}`, { staleMs: 1000, refreshMs: poll ? 2500 : undefined });
  const live = !!c && (c.live || LIVE.includes(c.status));
  useEffect(() => { if (c) setPoll(live); }, [c, live]);
  const [tab, setTab] = useState<"live" | "results" | "trials">("live");
  const [picked, setPicked] = useState<string | null>(null);
  const [rescore, setRescore] = useState(false);
  const toast = useToast();
  const tabChosen = useRef(false);
  useEffect(() => {
    if (c && !tabChosen.current) { setTab(live ? "live" : "results"); tabChosen.current = true; }
  }, [c, live]);

  const armIdx = useMemo(() => Object.fromEntries((c?.arms ?? []).map((a, i) => [a.key, i])), [c?.arms]);
  const armLabels = useMemo(() => Object.fromEntries((c?.arms ?? []).map((a) => [a.key, a.label])), [c?.arms]);
  const armColors = useMemo(() => Object.fromEntries((c?.arms ?? []).map((a, i) => [a.key, seriesColor(i)])), [c?.arms]);

  const act = async (fn: () => Promise<any>, ok: string) => {
    try { const r = await fn(); toast(ok, "good"); invalidate(`/api/campaigns/${id}`); reload(); return r; } catch (e: any) { toast(e.message, "bad"); }
  };

  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (!c) return <div className="page"><Loading rows={8} /></div>;
  return (
    <div className="page">
      <div className="page-head">
        <div className="grow" style={{ minWidth: 280 }}>
          <div className="row" style={{ gap: 8 }}><A href="/experiments" className="btn sm ghost"><Icon name="back" size={13} />Experiments</A><span className="mono faint">{short(c.id, 16)}</span></div>
          <h1 className="page-title" style={{ marginTop: 8 }}>{c.name}</h1>
          {c.hypothesis && <div className="page-sub">{c.hypothesis}</div>}
          <div className="row wrap" style={{ gap: 6, marginTop: 8 }}>
            <StatusBadge status={c.status} />{c.stop_reason && <Badge tone="warn">{c.stop_reason.replace(/_/g, " ")}</Badge>}
            <Labels labels={c.labels} />{c.scoring.revision > 1 && <Badge tone="info">assessment rev {c.scoring.revision}</Badge>}
            {c.parent_campaign_id && <A href={`/campaigns/${c.parent_campaign_id}`} className="badge">rerun of {short(c.parent_campaign_id)}</A>}
          </div>
        </div>
        <div className="row wrap">
          {c.live && <button className="btn danger" onClick={() => act(() => api.post(`/api/campaigns/${id}/cancel`), "Cancellation requested — no new dispatches")}><Icon name="stop" />Cancel</button>}
          {["paused", "requires_reconciliation", "failed"].includes(c.status) && !c.live && (
            <button className="btn" onClick={() => act(() => api.post(`/api/campaigns/${id}/resume`, {}), "Resumed — remaining trials get new attempts")}><Icon name="play" />Resume</button>
          )}
          {!c.live && <button className="btn" onClick={() => setRescore(true)}><Icon name="scale" />Re-score</button>}
          {!c.live && <button className="btn" onClick={async () => { const r = await act(() => api.post<{ campaign_id: string }>(`/api/campaigns/${id}/rerun`), "Fresh rerun launched"); if (r?.campaign_id) navigate(`/campaigns/${r.campaign_id}`); }}><Icon name="refresh" />Fresh rerun</button>}
          <ExportMenu id={id} />
        </div>
      </div>
      <Tabs value={tab} onChange={setTab} tabs={[
        { value: "live", label: <span className="row" style={{ gap: 6 }}>{live && <span className="dot pulse" style={{ width: 7, height: 7, borderRadius: 2, background: "var(--signal)", display: "inline-block" }} />}Execution</span> },
        { value: "results", label: "Results & gate" },
        { value: "trials", label: "Trials explorer" },
      ]} />
      {tab === "live" && <LiveTab c={c} live={live} armLabels={armLabels} armColors={armColors} armIdx={armIdx} onPick={setPicked} onChange={reload} />}
      {tab === "results" && <ResultsTab id={id} live={live} armColors={armColors} onPick={setPicked} />}
      {tab === "trials" && <TrialsTab id={id} c={c} armLabels={armLabels} armColors={armColors} onPick={setPicked} live={live} />}
      <TrialDrawer id={picked} onClose={() => setPicked(null)} armLabel={(k) => armLabels[k] ?? k} />
      <RescoreModal open={rescore} onClose={() => setRescore(false)} c={c} onDone={() => { setRescore(false); invalidate(`/api/campaigns/${id}`); reload(); setTab("results"); }} />
    </div>
  );
}

function ExportMenu({ id }: { id: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div style={{ position: "relative" }}>
      <button className="btn primary" onClick={() => setOpen(!open)} onBlur={() => setTimeout(() => setOpen(false), 150)}><Icon name="report" />Report</button>
      {open && (
        <div className="card fade-in" style={{ position: "absolute", right: 0, top: 38, zIndex: 30, width: 240, padding: 6 }}>
          <a className="nav-item" href={`/api/campaigns/${id}/report.html`} target="_blank" rel="noreferrer"><Icon name="eye" />Standalone HTML report</a>
          <a className="nav-item" href={`/api/campaigns/${id}/report.md`}><Icon name="download" />Markdown</a>
          <a className="nav-item" href={`/api/campaigns/${id}/report.json`}><Icon name="download" />JSON (all trials + checksum)</a>
        </div>
      )}
    </div>
  );
}

/* ================================================================ LIVE */
function LiveTab({ c, live, armLabels, armColors, armIdx, onPick, onChange }: {
  c: Campaign; live: boolean; armLabels: Record<string, string>; armColors: Record<string, string>; armIdx: Record<string, number>; onPick: (id: string) => void; onChange: () => void;
}) {
  const { data: grid0, reload: reloadGrid } = useApi<GridData & { status_codes: Record<string, number> }>(`/api/campaigns/${c.id}/grid`, { refreshMs: live ? 6000 : undefined, staleMs: 500 });
  const [grid, setGrid] = useState<GridData | null>(null);
  const [state, setState] = useState<LiveState | null>(c.state);
  const [log, setLog] = useState<{ ts: number; level: string; message: string }[]>([]);
  const [tp, setTp] = useState<number[]>([]);
  const [pathEvents, setPathEvents] = useState<PathEvent[]>([]);
  const idIndex = useRef<Map<string, number>>(new Map());
  useEffect(() => {
    if (!grid0) return;
    idIndex.current = new Map(grid0.ids.map((x, i) => [x, i]));
    setGrid({ arms: grid0.arms, ids: grid0.ids, arm: grid0.arm, status: grid0.status, outcome: grid0.outcome });
  }, [grid0]);

  const S: Record<string, number> = { pending: 0, running: 1, completed: 2, error: 3, cancelled: 4, not_applicable: 5, interrupted: 6 };
  const O: Record<string, number> = { not_assessed: 0, pass: 1, fail: 2, inconclusive: 3, not_applicable: 4 };
  const onEvent = useCallback((ev: any) => {
    if (ev.state) {
      setState(ev.state);
      if (ev.state.throughput_per_min != null) setTp((xs) => [...xs.slice(-59), ev.state.throughput_per_min]);
    }
    if (ev.type === "trials") {
      const fresh: PathEvent[] = [];
      for (const t of ev.trials) {
        if (t.status === "error") fresh.push({ id: `${t.id}:e`, arm: armIdx[t.arm] ?? 0, outcome: "error" });
        else if (t.status === "completed" && t.outcome) fresh.push({ id: `${t.id}:${t.outcome}`, arm: armIdx[t.arm] ?? 0, outcome: t.outcome === "pass" ? "pass" : t.outcome === "fail" ? "fail" : "inconclusive" });
      }
      if (fresh.length) setPathEvents((xs) => [...xs.slice(-300), ...fresh]);
      setGrid((g) => {
        if (!g) return g;
        const status = g.status.slice(), outcome = g.outcome.slice();
        for (const t of ev.trials) {
          const i = idIndex.current.get(t.id);
          if (i === undefined) continue;
          status[i] = S[t.status] ?? status[i];
          if (t.outcome) outcome[i] = O[t.outcome] ?? outcome[i];
        }
        return { ...g, status, outcome };
      });
    } else if (ev.type === "log") {
      setLog((l) => [...l.slice(-199), { ts: ev.ts, level: ev.level, message: ev.message }]);
    } else if (ev.type === "state") {
      setLog((l) => [...l.slice(-199), { ts: ev.ts, level: "state", message: `campaign → ${ev.state.status}` }]);
      if (!LIVE.includes(ev.state.status)) { onChange(); reloadGrid(); }
    } else if (ev.type === "idle") onChange();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onChange, reloadGrid, armIdx]);
  useEventSource(live ? `/api/campaigns/${c.id}/events` : null, onEvent);

  const agg = useMemo(() => {
    if (!grid) return null;
    const per = grid.arms.map(() => ({ done: 0, pass: 0, fail: 0, total: 0, err: 0 }));
    let done = 0, running = 0, pass = 0, assessed = 0, errors = 0, na = 0;
    for (let i = 0; i < grid.ids.length; i++) {
      const a = per[grid.arm[i]], s = grid.status[i], o = grid.outcome[i];
      if (s === 5) { na++; continue; }
      a.total++;
      if (s === 1) running++;
      if (s === 2 || s === 3 || s === 4) { done++; a.done++; }
      if (s === 3) { errors++; a.err++; }
      if (o === 1) { pass++; a.pass++; assessed++; }
      if (o === 2) { a.fail++; assessed++; }
    }
    return { per, done, running, pass, assessed, errors, na, total: grid.ids.length - na };
  }, [grid]);

  const st = state ?? c.state;
  const elapsed = c.started_at ? ((c.finished_at ?? Date.now() / 1000) - c.started_at) : null;
  return (
    <div className="col" style={{ gap: 14 }}>
      <div className="grid g4">
        <div className="card stat"><div className="stat-label">Progress</div>
          <div className="stat-value">{agg ? `${agg.done}/${agg.total}` : "—"}</div>
          <div className={`progress ${live ? "live" : ""}`} style={{ marginTop: 8 }}><i style={{ width: `${agg && agg.total ? (agg.done / agg.total) * 100 : 0}%` }} /></div>
          <div className="stat-foot">{agg?.running ?? 0} in flight · {agg?.errors ?? 0} infra errors{agg?.na ? ` · ${agg.na} not applicable` : ""}</div>
        </div>
        <div className="card stat"><div className="stat-label">Running pass rate</div>
          <div className="stat-value">{agg && agg.assessed ? pct(agg.pass / agg.assessed) : "—"}</div>
          <div className="stat-foot">{agg?.pass ?? 0} pass / {agg?.assessed ?? 0} assessed — provisional</div>
        </div>
        <div className="card stat"><div className="stat-label">Throughput</div>
          <div className="stat-value">{st?.throughput_per_min != null ? `${num(st.throughput_per_min, 0)}/min` : "—"}</div>
          <div className="stat-foot">elapsed {duration(live ? st?.elapsed_s ?? elapsed : elapsed)}</div>
          <div className="stat-spark"><Sparkline values={tp} color="var(--signal)" /></div>
        </div>
        <div className="card stat">
          <div className="stat-label" style={{ marginBottom: 8 }}>Budget</div>
          <BudgetGauge spent={st?.spent_usd ?? c.spent_usd} reserved={st?.reserved_usd ?? c.reserved_usd} cap={c.budget_cap_usd} unknown={st?.unknown_cost_attempts ?? c.unknown_cost_attempts} />
        </div>
      </div>

      <Card title="Signal path" sub={live ? "Live: each dot is a trial finishing right now; it turns green, red or amber once scored" : "Campaign idle — dots appear while trials are running"} graticule>
        <SignalPath arms={c.arms.map((a, i) => ({ key: a.key, label: a.label, color: armColors[a.key], readout: agg ? `${agg.per[i]?.done ?? 0}/${agg.per[i]?.total ?? 0}` : "" }))}
          events={pathEvents}
          readouts={{
            dataset: `${c.plan.protocol.sample_limit ?? "all"} samples`,
            scheduler: agg ? `${Math.max(0, agg.total - agg.done - agg.running)} queued` : "",
            scorers: agg ? `${agg.assessed} graded` : "",
            gate: agg && agg.assessed ? `${pct(agg.pass / agg.assessed, 0)} so far` : "waiting",
          }} />
      </Card>

      <Card title="Trial map" sub={`${c.plan.protocol.order === "interleaved" ? "Randomized interleaved" : "Blocked"} order · seed ${c.plan.protocol.seed} · one lane per arm · click a cell to inspect`}>
        {!grid ? <Loading rows={4} /> : <TrialGrid data={grid} armLabels={armLabels} armColors={armColors} onPick={onPick} />}
      </Card>

      <div className="grid g2">
        <Card title="Per-arm progress" sub="Provisional — final numbers with CIs are in Results">
          <div className="col" style={{ gap: 10 }}>
            {agg && grid?.arms.map((k, i) => {
              const a = agg.per[i];
              const rate = a.pass + a.fail ? a.pass / (a.pass + a.fail) : null;
              return (
                <div key={k} className="col" style={{ gap: 4 }}>
                  <div className="row"><span className="swatch" style={{ background: armColors[k] }} /><span className="trunc grow">{armLabels[k]}</span><span className="num muted">{a.done}/{a.total}</span><b className="num" style={{ width: 54, textAlign: "right" }}>{pct(rate, 0)}</b></div>
                  <div className="progress" style={{ height: 5 }}><i style={{ width: `${a.total ? (a.done / a.total) * 100 : 0}%`, background: armColors[k] }} /></div>
                </div>
              );
            })}
            {!armIdx && null}
          </div>
        </Card>
        <Card title="Event log" sub={live ? "streaming (SSE, coalesced every 250 ms)" : "campaign idle"}>
          <div className="log">
            {log.length === 0 ? <div className="muted">{live ? "Waiting for events…" : `Status: ${c.status}${c.stop_reason ? ` (${c.stop_reason})` : ""}.`}</div> : log.slice().reverse().map((l, i) => (
              <div key={`${l.ts}-${i}`} className="log-line"><span className="ts">{new Date(l.ts * 1000).toLocaleTimeString()}</span>
                <span className={l.level === "warn" ? "warn" : l.level === "state" ? "good" : "ink2"}>{l.message}</span></div>
            ))}
          </div>
        </Card>
      </div>
    </div>
  );
}

/* ================================================================ RESULTS */
function ResultsTab({ id, live, armColors, onPick }: { id: string; live: boolean; armColors: Record<string, string>; onPick: (id: string) => void }) {
  const { data: r, error, loading } = useApi<Results>(`/api/campaigns/${id}/results`, { refreshMs: live ? 5000 : undefined, staleMs: 1000 });
  const [axis, setAxis] = useState<"cost" | "latency">("cost");
  const [cellBusy, setCellBusy] = useState(false);
  if (error) return <ErrorBox error={error} />;
  if (loading && !r) return <Loading rows={10} />;
  if (!r) return null;
  const S = r.summaries;
  const keys = r.arms.map((a) => a.key);
  const label = (k: string) => S[k]?.arm.label ?? k;
  const color = (k: string) => armColors[k] ?? "var(--s1)";
  const candidates = keys.filter((k) => !r.gate.arms[k]?.is_baseline);
  const hasPassK = keys.some((k) => S[k].primary.pass_k);

  const openCell = async (row: number, col: number) => {
    if (cellBusy) return;
    setCellBusy(true);
    try {
      const sample = r.matrix.samples[row];
      const p = await api.get<Page<TrialRow>>(`/api/campaigns/${id}/trials?arm=${r.matrix.arms[col]}&q=${encodeURIComponent(sample.key)}&limit=20`);
      const t = p.items.find((x) => x.sample_key === sample.key);
      if (t) onPick(t.id);
    } finally { setCellBusy(false); }
  };

  return (
    <div className="col" style={{ gap: 14 }}>
      {r.gate.note && <div className="callout warn"><b>Provisional.</b> {r.gate.note}</div>}
      <div className="row wrap" style={{ gap: 6 }}><Labels labels={r.labels} /><span className="muted" style={{ fontSize: 12 }}>dataset <b>{r.dataset.name}</b> v{r.dataset.version} <span className="mono">{r.dataset.digest.slice(0, 10)}</span> · primary scorer <span className="mono">{r.primary_scorer}</span> · assessment rev {r.scoring_revision}</span></div>

      {r.suspect_labels?.length > 0 && (
        <Card title="Labels worth checking" sub="Every arm failed these samples and the arms agree with each other. The reference may be wrong or ambiguous."
          reading="Independent models giving the same answer that the reference rejects is a classic sign of a labelling problem. Review these before trusting the gate, then fix the dataset as a new version and re-score.">
          <table className="t">
            <thead><tr><th>Sample</th><th>Reference</th><th>What the models said</th><th className="r">Agreement</th><th className="r">Arms</th><th /></tr></thead>
            <tbody>{r.suspect_labels.map((x) => (
              <tr key={x.sample_id}>
                <td className="mono">{x.key}</td>
                <td><span className="badge bad">{typeof x.expected === "string" ? x.expected : JSON.stringify(x.expected)}</span></td>
                <td><span className="badge">{x.consensus_answer}</span></td>
                <td className="r">{pct(x.agreement, 0)}</td><td className="r">{x.arms}</td>
                <td className="r"><button className="btn sm" onClick={async () => {
                  const pg = await api.get<Page<TrialRow>>(`/api/campaigns/${id}/trials?q=${encodeURIComponent(x.key)}&limit=5`);
                  if (pg.items[0]) onPick(pg.items[0].id);
                }}>Inspect</button></td>
              </tr>
            ))}</tbody>
          </table>
        </Card>
      )}

      <Card title="Gate verdict" sub="Each arm's 95% interval against the threshold you froze before the run" graticule
        reading={<>The dot is the observed pass rate; the shaded band is the 95% interval. <b style={{ color: "var(--ink)" }}>Pass</b> needs the whole band above the dashed threshold, <b style={{ color: "var(--ink)" }}>fail</b> means the whole band is below it, and a band that crosses the line means more samples are needed.</>}>
        <EvidenceScope threshold={r.gate.policy?.min_pass_rate}
          rows={keys.map((k) => ({ key: k, label: label(k), color: color(k), iv: S[k].primary.pass_rate, verdict: r.gate.arms[k].verdict, checks: r.gate.arms[k].checks, baseline: r.gate.arms[k].is_baseline }))} />
      </Card>

      <div className="grid g2">
        <Card title={`Primary metric — ${r.primary_scorer} pass rate`} sub="Bars: passes / assessed · whiskers: 95% Wilson interval">
          <CIBars rows={keys.map((k) => ({ key: k, label: label(k), color: color(k), iv: S[k].primary.pass_rate }))} threshold={r.gate.policy?.min_pass_rate} />
        </Card>
        <Card title="Quality vs cost / latency" sub="Dashed line: Pareto frontier (no other arm is both cheaper/faster and better)"
          actions={<Seg value={axis} onChange={setAxis} options={[{ value: "cost", label: "cost / trial" }, { value: "latency", label: "p50 latency" }]} />}>
          <Pareto key={axis} frontier={r.pareto[axis]} logX={axis === "cost"} xLabel={axis === "cost" ? "cost per trial" : "p50 latency"} xFormat={axis === "cost" ? (v) => usd(v) : (v) => ms(v)}
            points={keys.map((k) => ({ key: k, label: label(k), color: color(k), x: axis === "cost" ? S[k].cost.per_trial_usd : S[k].latency_ms.p50, y: S[k].primary.pass_rate.estimate, yLow: S[k].primary.pass_rate.low, yHigh: S[k].primary.pass_rate.high }))} />
        </Card>
      </div>

      {keys.filter((k) => S[k].routing?.is_router).map((k) => {
        const rt = S[k].routing!;
        const sl = rt.by_slice.find((b) => b.key === "task_family") ?? rt.by_slice[0];
        const models = rt.routes.map((x) => x.model);
        const mc = (m: string) => `var(--s${(models.indexOf(m) % 8) + 1})`;
        return (
          <Card key={k} title={`Routing — ${label(k)}`} graticule
            sub={`${rt.calls} calls resolved to ${rt.distinct_models} model${rt.distinct_models === 1 ? "" : "s"}${rt.stability != null ? ` · same model for the same task ${pct(rt.stability, 0)} of the time` : ""}`}
            reading="Left: task type. Right: the concrete model the router used. Band width is the number of calls. The table shows how each route performed, so you can see whether the router's choices paid off.">
            {sl ? <RouterFlow rows={sl.rows} modelColor={mc} sliceKey={sl.key} /> : <div className="muted">No task metadata to slice by.</div>}
            <div className="table-wrap" style={{ marginTop: 14 }}><RouteTable routes={rt.routes} color={mc} /></div>
          </Card>
        );
      })}

      <Card title="Leaderboard" sub="Every denominator is explicit. Unknown costs are never shown as $0." flush>
        <div className="table-wrap">
          <table className="t">
            <thead><tr>
              <th>Arm</th><th className="r">Planned</th><th className="r">Assessed</th><th className="r">Pass rate [95% CI]</th><th className="r" title="passes / planned">All-admitted</th>{hasPassK && <><th className="r" title="mean of per-sample pass fractions">Task-balanced</th><th className="r">pass@k</th><th className="r">all-k</th></>}
              <th className="r">p50 / p95</th><th className="r">TTFT p50</th><th className="r">tok/s</th><th className="r">Cost · per pass</th><th className="r">Err · retry</th>
            </tr></thead>
            <tbody>
              {r.ranking.concat(keys.filter((k) => !r.ranking.includes(k))).map((k, i) => {
                const s = S[k], pr = s.primary.pass_rate;
                return (
                  <tr key={k}>
                    <td style={{ maxWidth: 300 }}><div className="row" style={{ gap: 6 }}><span className="mono faint" style={{ width: 16 }}>{i + 1}</span><span className="swatch" style={{ background: color(k) }} /><span className="trunc" style={{ fontWeight: 550 }}>{label(k)}</span>
                      {k === r.best && <Badge tone="good">best</Badge>}{r.statistically_tied_with_best.includes(k) && <Badge title="95% CIs overlap with the best arm">tied</Badge>}</div></td>
                    <td className="r">{s.planned}{s.not_applicable ? <span className="faint"> +{s.not_applicable} n/a</span> : null}</td>
                    <td className="r">{s.primary.assessed}</td>
                    <td className="r"><b>{pct(pr.estimate)}</b> <span className="faint">[{pct(pr.low, 0)}, {pct(pr.high, 0)}]</span></td>
                    <td className="r">{pct(s.primary.all_admitted_rate.estimate)}</td>
                    {hasPassK && <><td className="r">{pct(s.primary.task_balanced.estimate)}</td><td className="r">{pct(s.primary.pass_k?.pass_at_k)}</td><td className="r">{pct(s.primary.pass_k?.all_k)}</td></>}
                    <td className="r">{ms(s.latency_ms.p50)} / {ms(s.latency_ms.p95)}</td>
                    <td className="r">{ms(s.ttft_ms.p50)}</td>
                    <td className="r">{s.output_tokens_per_s.p50 != null ? num(s.output_tokens_per_s.p50) : "—"}</td>
                    <td className="r">{usd(s.cost.total_known_usd)}{!s.cost.complete && <span className="warn" title={`${s.cost.unknown_cost_attempts} attempts with unknown cost`}> +?</span>} <span className="faint">· {usd(s.cost.per_pass_usd, { unknown: s.primary.passes ? "incomplete" : "undefined" })}</span></td>
                    <td className="r">{s.errors ? <span className="bad">{s.errors}</span> : 0} · {s.retries}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Card>

      {r.comparisons.length > 0 && (
        <Card title={`Paired comparison vs baseline — ${label(r.baseline)}`} sub="Forest plot of Δ pass rate (candidate − baseline) on the same samples; dashed line = non-inferiority margin">
          <Forest comps={r.comparisons} label={label} color={color} margin={r.gate.policy?.non_inferiority_margin} />
        </Card>
      )}

      <Card title="Latency distribution" sub="Client-observed end-to-end latency per trial (includes network and provider queueing)">
        <LatencyStrip rows={keys.map((k) => ({ key: k, label: label(k), color: color(k), values: S[k].latency_values, p50: S[k].latency_ms.p50, p95: S[k].latency_ms.p95, ttft50: S[k].ttft_ms.p50 }))} />
      </Card>

      {r.slices.length > 0 && (
        <Card title="Slices" sub="Small slices carry wide intervals; exploratory slices were not pre-registered">
          <div className="grid g2">
            {r.slices.map((sl) => (
              <div key={sl.key} className="col" style={{ gap: 6 }}>
                <div className="row"><b>{sl.key}</b>{sl.exploratory && <Badge>exploratory</Badge>}</div>
                <table className="t"><thead><tr><th>value</th>{keys.map((k) => <th key={k}><span className="swatch" style={{ background: color(k) }} title={label(k)} /></th>)}</tr></thead>
                  <tbody>{sl.rows.map((row) => (
                    <tr key={String(row.value)}><td className="mono">{String(row.value)}</td>{keys.map((k) => <td key={k}><MiniBar iv={row.arms[k]} color={color(k)} /></td>)}</tr>
                  ))}</tbody></table>
              </div>
            ))}
          </div>
        </Card>
      )}

      <Card title="Sample × arm matrix" sub="Each cell = pass fraction for that sample (averaged over repetitions). Click to open the trial.">
        <Heatmap rows={r.matrix.samples.map((s) => s.key)} cols={keys.map((k) => ({ key: k, label: label(k), color: color(k) }))} values={r.matrix.values} onCell={openCell}
          rowMeta={(i) => <div className="tt-row muted">{Object.entries(r.matrix.samples[i].meta).slice(0, 3).map(([a, b]) => `${a}=${typeof b === "object" ? "…" : b}`).join(" · ")}</div>} />
      </Card>

      {r.scorers.length > 1 && (
        <Card title="All scorers" sub="Secondary scorers are reported separately; no composite 'quality' score" flush>
          <div className="table-wrap"><table className="t">
            <thead><tr><th>Arm</th>{r.scorers.map((s) => <th key={s.id} className="r">{s.id}{s.id === r.primary_scorer ? " ★" : ""}</th>)}</tr></thead>
            <tbody>{keys.map((k) => (
              <tr key={k}><td><span className="row" style={{ gap: 6 }}><span className="swatch" style={{ background: color(k) }} /><span className="trunc">{label(k)}</span></span></td>
                {r.scorers.map((s) => { const x = S[k].scorers[s.id]; return <td key={s.id} className="r">{pct(x?.pass_rate.estimate)} <span className="faint">· mean {x?.mean_score.estimate != null ? x.mean_score.estimate.toFixed(2) : "—"}{x?.inconclusive ? ` · ${x.inconclusive} inc.` : ""}</span></td>; })}</tr>
            ))}</tbody>
          </table></div>
        </Card>
      )}
      <div className="hint">Methods: {Object.entries(r.methods).map(([k, v]) => `${k}: ${v}`).join(" · ")}. Candidates evaluated: {candidates.length}.</div>
    </div>
  );
}

function Forest({ comps, label, color, margin }: { comps: Comparison[]; label: (k: string) => string; color: (k: string) => string; margin?: number | null }) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const tip = useTooltip();
  const lw = Math.min(260, Math.max(130, w * 0.3)), rw = 200;
  const pw = Math.max(80, w - lw - rw);
  const ext = Math.max(0.1, ...comps.flatMap((c) => [Math.abs(c.diff?.low ?? 0), Math.abs(c.diff?.high ?? 0)]), margin ?? 0) * 1.15;
  const x = (v: number) => lw + ((v + ext) / (2 * ext)) * pw;
  const rowH = 34, h = comps.length * rowH + 30;
  return (
    <div ref={ref}>
      {w > 0 && (
        <svg className="chart" width={w} height={h}>
          <line x1={x(0)} x2={x(0)} y1={0} y2={h - 22} className="axis" />
          {margin != null && <line x1={x(-margin)} x2={x(-margin)} y1={0} y2={h - 22} stroke="var(--warn)" strokeDasharray="4 3" />}
          {[-ext, -ext / 2, 0, ext / 2, ext].map((t) => <text key={t} x={x(t)} y={h - 6} textAnchor="middle">{pct(t, 0, true)}</text>)}
          {comps.map((c, i) => {
            const y = i * rowH + rowH / 2;
            const d = c.diff;
            return (
              <g key={c.candidate} onMouseMove={(e) => tip.show(e, <><div className="tt-title">{label(c.candidate)}</div>
                <div className="tt-row">Δ <b>{pct(d?.estimate, 1, true)}</b></div><div className="tt-row">95% CI <b>{pct(d?.low, 1, true)} … {pct(d?.high, 1, true)}</b></div>
                <div className="tt-row">W / L / T <b>{c.wins} / {c.losses} / {c.ties}</b></div><div className="tt-row">McNemar p <b>{c.mcnemar_p == null ? "—" : c.mcnemar_p.toFixed(4)}</b></div>
                <div className="tt-row">paired samples <b>{c.paired_samples}</b></div>{c.missing_pairs > 0 && <div className="tt-row warn">missing pairs {c.missing_pairs}</div>}</>)} onMouseLeave={tip.hide}>
                <rect x={0} y={y - rowH / 2} width={w} height={rowH} fill="transparent" />
                <text x={lw - 10} y={y + 4} textAnchor="end" className="lbl">{label(c.candidate).slice(0, 34)}</text>
                {d?.low != null && d?.high != null && <line x1={x(d.low)} x2={x(d.high)} y1={y} y2={y} stroke={color(c.candidate)} strokeWidth={3} strokeLinecap="round" className="bar-grow" style={{ transformOrigin: "center" }} />}
                {d?.estimate != null && <circle cx={x(d.estimate)} cy={y} r={6} fill={color(c.candidate)} stroke="var(--plate)" strokeWidth={2} className="pop-in" />}
                <text x={lw + pw + 12} y={y + 4} className="val">{pct(d?.estimate, 1, true)} <tspan fill="var(--muted)">W{c.wins}/L{c.losses}{c.mcnemar_p != null ? ` · p=${c.mcnemar_p.toFixed(3)}` : " · bootstrap CI"}</tspan></text>
              </g>
            );
          })}
        </svg>
      )}
      {tip.node}
    </div>
  );
}

/* ================================================================ TRIALS */
function TrialsTab({ id, c, armLabels, armColors, onPick, live }: { id: string; c: Campaign; armLabels: Record<string, string>; armColors: Record<string, string>; onPick: (id: string) => void; live: boolean }) {
  const [arm, setArm] = useState("");
  const [outcome, setOutcome] = useState("");
  const [status, setStatus] = useState("");
  const [sort, setSort] = useState("order");
  const [q, setQ] = useState("");
  const [offset, setOffset] = useState(0);
  const dq = useDebounced(q, 250);
  const limit = 40;
  const qs = new URLSearchParams({ offset: String(offset), limit: String(limit), sort });
  if (arm) qs.set("arm", arm);
  if (outcome) qs.set("outcome", outcome);
  if (status) qs.set("status", status);
  if (dq) qs.set("q", dq);
  const { data, loading } = useApi<Page<TrialRow>>(`/api/campaigns/${id}/trials?${qs}`, { refreshMs: live ? 4000 : undefined, staleMs: 1000 });
  const r0 = (fn: (v: string) => void) => (e: React.ChangeEvent<HTMLSelectElement | HTMLInputElement>) => { fn(e.target.value); setOffset(0); };
  return (
    <Card flush title={
      <div className="row wrap" style={{ gap: 8 }}>
        <input className="input" style={{ width: 220 }} placeholder="Search sample id or output…" value={q} onChange={r0(setQ)} />
        <select className="select" style={{ width: 220 }} value={arm} onChange={r0(setArm)} aria-label="Arm"><option value="">All arms</option>{c.arms.map((a) => <option key={a.key} value={a.key}>{a.label}</option>)}</select>
        <select className="select" style={{ width: 150 }} value={outcome} onChange={r0(setOutcome)} aria-label="Outcome"><option value="">Any outcome</option>{["pass", "fail", "inconclusive", "not_assessed", "not_applicable"].map((o) => <option key={o}>{o}</option>)}</select>
        <select className="select" style={{ width: 150 }} value={status} onChange={r0(setStatus)} aria-label="Status"><option value="">Any status</option>{["completed", "error", "cancelled", "pending", "running", "not_applicable"].map((o) => <option key={o}>{o}</option>)}</select>
        <select className="select" style={{ width: 150 }} value={sort} onChange={r0(setSort)} aria-label="Sort"><option value="order">Run order</option><option value="sample">Sample</option><option value="latency">Slowest</option><option value="cost">Most expensive</option></select>
      </div>
    } actions={data && <Pager offset={offset} limit={limit} total={data.total} onChange={setOffset} />}>
      {loading && !data ? <Loading rows={8} /> : (
        <div className="table-wrap">
          <table className="t">
            <thead><tr><th className="r">#</th><th>Sample</th><th>Arm</th><th>Outcome</th><th>Output</th><th className="r">Latency</th><th className="r">Tokens</th><th className="r">Cost</th><th className="r">Attempts</th></tr></thead>
            <tbody>
              {data?.items.map((t) => (
                <tr key={t.id} className="click" onClick={() => onPick(t.id)}>
                  <td className="r mono faint">{t.order_index + 1}</td>
                  <td className="mono">{t.sample_key}{t.repetition ? <span className="faint"> r{t.repetition}</span> : null}</td>
                  <td style={{ maxWidth: 200 }}><span className="row" style={{ gap: 6 }}><span className="swatch" style={{ background: armColors[t.arm_key] }} /><span className="trunc">{armLabels[t.arm_key]}</span></span></td>
                  <td><OutcomeBadge outcome={t.outcome} status={t.status} error={t.error_category} /></td>
                  <td style={{ maxWidth: 360 }}><div className="trunc mono" style={{ fontSize: 11.5 }}>{t.output_preview ?? <span className="faint">—</span>}</div></td>
                  <td className="r">{ms(t.latency_ms)}</td>
                  <td className="r">{t.tokens_in != null ? `${t.tokens_in}→${t.tokens_out}` : "—"}</td>
                  <td className="r">{t.status === "completed" || t.status === "error" ? usd(t.cost_usd) : "—"}</td>
                  <td className="r">{t.attempt_count > 1 ? <span className="warn">{t.attempt_count}</span> : t.attempt_count}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {data?.items.length === 0 && <div className="empty">No trials match.</div>}
        </div>
      )}
    </Card>
  );
}

/* ================================================================ RESCORE */
function RescoreModal({ open, onClose, c, onDone }: { open: boolean; onClose: () => void; c: Campaign; onDone: () => void }) {
  const [text, setText] = useState("");
  const [primary, setPrimary] = useState("");
  const [err, setErr] = useState<Error | null>(null);
  useEffect(() => {
    if (open) { setText(JSON.stringify(c.scoring.scorers, null, 2)); setPrimary(c.scoring.primary); setErr(null); }
  }, [open, c.scoring]);
  const submit = async () => {
    try {
      const scorers = JSON.parse(text);
      await api.post(`/api/campaigns/${c.id}/rescore`, { scorers, primary_scorer: primary || undefined });
      onDone();
    } catch (e: any) { setErr(e instanceof SyntaxError ? new Error("Scorers must be valid JSON") : e); }
  };
  return (
    <Modal open={open} onClose={onClose} title="Re-score stored outputs" footer={<><button className="btn ghost" onClick={onClose}>Cancel</button><button className="btn primary" onClick={submit}><Icon name="scale" />Create revision {c.scoring.revision + 1}</button></>}>
      <div className="callout">Re-scoring runs scorers on the <b>existing outputs</b> — no new model calls. It creates assessment revision {c.scoring.revision + 1}; earlier assessments stay in the audit trail. A changed rubric is a new measurement, not a better model.</div>
      <div className="field"><label>Scorer specs (JSON list of {"{id, key, params}"})</label><textarea className="textarea mono" rows={12} value={text} onChange={(e) => setText(e.target.value)} /></div>
      <div className="field"><label>Primary scorer id</label><input className="input mono" value={primary} onChange={(e) => setPrimary(e.target.value)} /></div>
      <ErrorBox error={err} />
    </Modal>
  );
}
