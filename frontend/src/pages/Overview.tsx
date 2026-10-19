import { useState } from "react";
import { api } from "../api";
import { SignalPath } from "../charts/SignalPath";
import { Sparkline } from "../charts/charts";
import { A, Badge, Card, Empty, Icon, Labels, Loading, Stat, StatusBadge, useToast } from "../components/ui";
import { ago, pct, usd } from "../format";
import { useApi } from "../hooks";
import { navigate } from "../router";
import type { Dataset, Provider } from "../types";

interface OverviewData {
  totals: { campaigns: number; trials: number; datasets: number; experiments: number; spent_usd: number; unknown_cost_attempts: number; passes: number; assessed: number; attempts: number };
  recent: { id: string; status: string; created_at: number; spent_usd: number; planned_trials: number; labels: string[]; name: string; done: number; passes: number; assessed: number; experiment_id: string }[];
  providers: Provider[];
  daily: { day: number; attempts: number; ok: number; cost: number }[];
  active: string[];
}

const DEMO_ARMS = [
  { key: "a", label: "model A · temp 0", color: "var(--s1)" },
  { key: "b", label: "model B · temp 0", color: "var(--s2)" },
  { key: "c", label: "router", color: "var(--s3)" },
];

export default function Overview() {
  const { data, loading } = useApi<OverviewData>("/api/overview", { refreshMs: 4000, staleMs: 2000 });
  const { data: datasets } = useApi<Dataset[]>("/api/datasets");
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const runDemo = async () => {
    const ds = datasets?.find((d) => d.name === "general-knowledge-mcq");
    if (!ds) return;
    setBusy(true);
    try {
      const r = await api.post<{ campaign_id: string }>("/api/experiments?launch=true", {
        name: "Demo: strong vs weak vs flaky fake target",
        hypothesis: "The Lab separates a strong from a weak target with paired statistics, and records every retry of a flaky one.",
        plan: {
          dataset_id: ds.id,
          targets: [{ provider: "fake", model: "fake/oracle-90", params: { max_tokens: 64 } }, { provider: "fake", model: "fake/oracle-50", params: { max_tokens: 64 } }, { provider: "fake", model: "fake/flaky-30", params: { max_tokens: 64 } }],
          scorers: [{ id: "choice", key: "choice", params: {} }], primary_scorer: "choice",
          protocol: { repetitions: 3, concurrency: 6, order: "interleaved", max_retries: 3 },
          budget: { max_usd: 0 }, acceptance: { min_pass_rate: 0.7, non_inferiority_margin: 0.05, min_assessed_per_arm: 30 },
        },
      });
      navigate(`/campaigns/${r.campaign_id}`);
    } catch (e: any) { toast(e.message, "bad"); } finally { setBusy(false); }
  };

  if (loading && !data) return <div className="page"><Loading rows={6} /></div>;
  const t = data?.totals;
  const spark = (data?.daily ?? []).map((d) => d.attempts);
  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <div className="eyebrow">Bench</div>
          <h1 className="page-title">Every number here has a denominator.</h1>
          <div className="page-sub">The Lab runs frozen experiments against models, records every provider call, and reports results with confidence intervals and a pre-registered pass/fail gate.</div>
        </div>
        <button className="btn" onClick={runDemo} disabled={busy || !datasets}><Icon name="bolt" />Run free demo</button>
        <A href="/router-lab" className="btn primary"><Icon name="models" />Open router lab</A>
      </div>

      <Card title="Signal path" sub="How one measurement travels through the Lab. Each dot is a trial." graticule style={{ marginBottom: 14 }}>
        <SignalPath demo arms={DEMO_ARMS}
          readouts={{ dataset: `${t?.datasets ?? 0} snapshots`, scheduler: "seeded order", scorers: "private ref", gate: "CI vs plan" }} />
      </Card>

      <div className="grid g4" style={{ marginBottom: 14 }}>
        <Stat label="Campaigns" value={t?.campaigns} foot={`${t?.experiments ?? 0} frozen plans · ${data?.active.length ?? 0} running`} />
        <Stat label="Trials" value={t?.trials} foot={`${(t?.attempts ?? 0).toLocaleString()} provider attempts incl. retries`}>
          <Sparkline values={spark} color="var(--signal)" />
        </Stat>
        <Stat label="Pass rate, all assessed" value={t && t.assessed ? t.passes / t.assessed : null} format={(v) => pct(v)}
          foot={t ? `${t.passes.toLocaleString()} of ${t.assessed.toLocaleString()} assessed trials` : ""} />
        <Stat label="Known spend" value={t?.spent_usd} format={(v) => usd(v, { unknown: "—" })}
          foot={t?.unknown_cost_attempts ? <span className="warn">plus {t.unknown_cost_attempts} attempts with unknown cost</span> : "every attempt's cost is accounted for"} />
      </div>

      <div className="grid g-main">
        <Card title="Recent campaigns" sub="Run status and gate verdict are reported separately" flush
          actions={<A href="/experiments" className="btn sm ghost">All campaigns<Icon name="arrow" size={13} /></A>}>
          {!data?.recent.length ? (
            <Empty title="Nothing measured yet" action={<button className="btn primary" onClick={runDemo} disabled={busy}>Run the free demo</button>}>
              The demo uses fake targets, so it costs nothing and finishes in about 30 seconds.
            </Empty>
          ) : (
            <div className="table-wrap">
              <table className="t">
                <thead><tr><th>Experiment</th><th>Status</th><th>Progress</th><th className="r">Pass rate</th><th className="r">Spend</th><th>Started</th></tr></thead>
                <tbody>
                  {data.recent.map((c) => {
                    const prog = c.planned_trials ? c.done / c.planned_trials : 0;
                    return (
                      <tr key={c.id} className="click" onClick={() => navigate(`/campaigns/${c.id}`)}>
                        <td style={{ maxWidth: 340 }}><div className="trunc" style={{ fontWeight: 500 }}>{c.name}</div><div className="row wrap" style={{ gap: 4, marginTop: 4 }}><Labels labels={c.labels} /></div></td>
                        <td><StatusBadge status={c.status} /></td>
                        <td style={{ minWidth: 130 }}><div className={`progress ${["running", "grading"].includes(c.status) ? "live" : ""}`}><i style={{ width: `${prog * 100}%` }} /></div><div className="mono muted" style={{ fontSize: 11, marginTop: 4 }}>{c.done}/{c.planned_trials}</div></td>
                        <td className="r">{c.assessed ? pct(c.passes / c.assessed) : "—"}</td>
                        <td className="r">{usd(c.spent_usd)}</td>
                        <td className="muted">{ago(c.created_at)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>

        <div className="col" style={{ gap: 14 }}>
          <Card title="Providers" sub="API keys are read from the environment, never stored" actions={<A href="/settings" className="btn sm ghost">Manage</A>}>
            <div className="col" style={{ gap: 10 }}>
              {(data?.providers ?? []).map((p) => (
                <div key={p.name} className="row">
                  <span style={{ width: 8, height: 8, borderRadius: 2, background: p.configured ? "var(--signal)" : "var(--grat-2)", boxShadow: p.configured ? "0 0 0 3px var(--signal-wash)" : "none" }} />
                  <span>{p.label}</span>
                  <span className="spacer" />
                  {p.configured ? <Badge tone="good">ready</Badge> : <Badge>{p.requires_key ? "no key" : "not reachable"}</Badge>}
                </div>
              ))}
            </div>
          </Card>
          <Card title="Start a measurement">
            <ol className="col" style={{ margin: 0, paddingLeft: 18, gap: 7 }}>
              <li><A href="/datasets">Pick or import a dataset</A> <span className="muted">— text or images, frozen by digest</span></li>
              <li><A href="/models">Choose models</A> <span className="muted">— OpenRouter, OpenAI, Groq, local</span></li>
              <li><A href="/experiments/new">Freeze the plan</A> <span className="muted">— scorers, budget, pass/fail gate</span></li>
              <li>Watch trials flow, then read the verdict</li>
            </ol>
          </Card>
        </div>
      </div>
    </div>
  );
}
