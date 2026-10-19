import { useEffect, useMemo, useState } from "react";
import { api } from "../api";
import { RouterFlow } from "../charts/Explainers";
import { A, Badge, Card, ErrorBox, Icon, Loading, StatusBadge, useToast } from "../components/ui";
import { ago, num, pct, seriesColor, usd } from "../format";
import { useApi, useDebounced } from "../hooks";
import { navigate } from "../router";
import type { CampaignRow, Dataset, Preflight } from "../types";

interface Candidate { provider: string; model: string; role: string; note: string; defaultOn: boolean }
const CANDIDATES: Candidate[] = [
  { provider: "openrouter", model: "typesafe/jev-router", role: "router", note: "TypeSafe Jev Router — picks a model and reasoning effort per request; dynamic pricing", defaultOn: true },
  { provider: "openrouter", model: "openai/gpt-6-luna", role: "cheap baseline", note: "$0.10 / $0.50 per 1M tokens", defaultOn: true },
  { provider: "openrouter", model: "openai/gpt-6-sol", role: "strong baseline", note: "$2 / $10 per 1M tokens", defaultOn: true },
  { provider: "openrouter", model: "anthropic/claude-haiku-5.5", role: "cheap baseline", note: "$0.10 / $0.50 per 1M tokens", defaultOn: false },
  { provider: "openrouter", model: "google/gemini-2.5-flash-lite", role: "cheap baseline", note: "$0.10 / $0.40 per 1M tokens", defaultOn: false },
  { provider: "fake", model: "fake/router", role: "simulated router", note: "Free dry run: routes hard-looking prompts to a 'large' fake model", defaultOn: false },
];

// Illustration only: what a well-behaved router's decisions look like (shown until a real router campaign exists).
const ILLUSTRATION = [
  { value: "arithmetic", routes: { "small-model": { n: 14, passes: 13, assessed: 14 }, "large-model": { n: 2, passes: 2, assessed: 2 } } },
  { value: "classification", routes: { "small-model": { n: 11, passes: 10, assessed: 11 }, "large-model": { n: 1, passes: 1, assessed: 1 } } },
  { value: "extraction", routes: { "small-model": { n: 5, passes: 4, assessed: 5 }, "large-model": { n: 7, passes: 7, assessed: 7 } } },
  { value: "reasoning", routes: { "small-model": { n: 3, passes: 1, assessed: 3 }, "large-model": { n: 13, passes: 12, assessed: 13 } } },
  { value: "vision_chart", routes: { "vision-model": { n: 12, passes: 11, assessed: 12 } } },
];

export default function RouterLab() {
  const { data: datasets } = useApi<Dataset[]>("/api/datasets");
  const { data: camps } = useApi<(CampaignRow & { targets: string[] })[]>("/api/campaigns?limit=200", { refreshMs: 5000 });
  const ds = datasets?.find((d) => d.name === "router-mix");
  const [picked, setPicked] = useState<Set<string>>(() => new Set(CANDIDATES.filter((c) => c.defaultOn).map((c) => c.model)));
  const [reps, setReps] = useState(1);
  const [limit, setLimit] = useState<number | null>(null);
  const [cap, setCap] = useState(1);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<Error | null>(null);
  const toast = useToast();

  // targets are "provider/model"; match on the model part so "openrouter/..." does not count as a router
  const routerCamps = (camps ?? []).filter((c) => c.targets?.some((t) => /router/i.test(t.split("/").slice(1).join("/"))));
  // show measured data from a real router first; the fake router is only a dry run
  const isReal = (c: { targets: string[] }) => c.targets.some((t) => !t.startsWith("fake/") && /router/i.test(t.split("/").slice(1).join("/")));
  const done = routerCamps.filter((c) => c.status === "completed");
  const latest = done.find(isReal) ?? done[0];
  const { data: latestRes } = useApi<any>(latest ? `/api/campaigns/${latest.id}/results` : null);
  const realFlow = useMemo(() => {
    if (!latestRes) return null;
    const s = Object.values<any>(latestRes.summaries).find((x) => x.routing?.is_router);
    const sl = s?.routing?.by_slice?.find((b: any) => b.key === "task_family") ?? s?.routing?.by_slice?.[0];
    return sl ? { rows: sl.rows, key: sl.key, label: s.arm.label, routing: s.routing } : null;
  }, [latestRes]);
  const flowModels = useMemo(() => {
    const rows = realFlow?.rows ?? ILLUSTRATION;
    return Array.from(new Set(rows.flatMap((r: any) => Object.keys(r.routes))));
  }, [realFlow]);
  const flowColor = (m: string) => seriesColor(Math.max(0, flowModels.indexOf(m)));

  const plan = useMemo(() => ds && {
    dataset_id: ds.id,
    targets: CANDIDATES.filter((c) => picked.has(c.model)).map((c) => ({ provider: c.provider, model: c.model, label: c.model, params: { temperature: 0, max_tokens: 2000 } })),
    scorers: [{ id: "auto", key: "auto", params: {} }], primary_scorer: "auto",
    protocol: { repetitions: reps, concurrency: 6, order: "interleaved", max_retries: 3, timeout_s: 180, splits: ["test"], sample_limit: limit, seed: 20261012, stream: true },
    budget: { max_usd: cap, paid_run_confirmed: confirm },
    acceptance: { min_pass_rate: 0.8, baseline_arm: CANDIDATES.find((c) => picked.has(c.model) && c.role !== "router")?.model ?? null, non_inferiority_margin: 0.05, min_assessed_per_arm: 30, max_error_rate: 0.1 },
  }, [ds, picked, reps, limit, cap, confirm]);
  const dplan = useDebounced(plan, 300);
  const [pf, setPf] = useState<Preflight | null>(null);
  useEffect(() => {
    if (!dplan || !dplan.targets.length) { setPf(null); return; }
    let live = true;
    api.post<Preflight>("/api/experiments/preflight", dplan).then((r) => live && setPf(r)).catch(() => live && setPf(null));
    return () => { live = false; };
  }, [dplan]);

  const launch = async () => {
    if (!plan) return;
    setBusy(true); setErr(null);
    try {
      const r = await api.post<{ campaign_id: string }>("/api/experiments?launch=true", {
        name: `Router lab: ${plan.targets.map((t) => t.model.split("/").pop()).join(" vs ")}`,
        hypothesis: "Does the router send easy tasks to cheap models and hard tasks to strong ones, and does it match the best fixed model at lower cost?",
        plan,
      });
      toast("Router campaign launched", "good");
      navigate(`/campaigns/${r.campaign_id}`);
    } catch (e: any) { setErr(e); } finally { setBusy(false); }
  };
  const paidSelected = CANDIDATES.some((c) => picked.has(c.model) && c.provider !== "fake");

  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <div className="eyebrow">Router lab</div>
          <h1 className="page-title">Where does the router send your request?</h1>
          <div className="page-sub">A router like TypeSafe <span className="mono">jev-router</span> picks a different model for each request. Here you measure which models it actually picks, whether hard tasks get strong models, and whether it beats fixed models on quality per dollar.</div>
        </div>
      </div>

      <div className="grid g-main" style={{ alignItems: "start" }}>
        <Card graticule title={realFlow ? `Routing decisions — ${realFlow.label}` : "Routing decisions (illustration)"}
          sub={realFlow ? <>From campaign <A href={`/campaigns/${latest!.id}`}>{latest!.name}</A>, sliced by {realFlow.key}</> : "Synthetic example of a well-behaved router. Run a campaign to replace it with measured data."}
          reading="Each band is a group of requests. Its width is the number of calls, the left side is the task type, and the right side is the model the router actually used. In a good router, wide bands run from hard tasks to strong models and from easy tasks to cheap ones.">
          <RouterFlow rows={(realFlow?.rows ?? ILLUSTRATION) as any} modelColor={flowColor} sliceKey={realFlow?.key ?? "task_family"} />
          {realFlow && (
            <div className="row wrap" style={{ gap: 16, marginTop: 10 }}>
              <span className="muted">distinct models used <b className="mono" style={{ color: "var(--ink)" }}>{realFlow.routing.distinct_models}</b></span>
              <span className="muted">routing stability across repetitions <b className="mono" style={{ color: "var(--ink)" }}>{realFlow.routing.stability == null ? "n/a (1 repetition)" : pct(realFlow.routing.stability, 0)}</b></span>
            </div>
          )}
        </Card>

        <Card title="Run a router campaign" sub={ds ? `Suite: router-mix · ${ds.sample_count} tasks across 6 families, text and images` : "Loading suite…"}>
          {!ds ? <Loading /> : (
            <div className="col" style={{ gap: 12 }}>
              <div className="col" style={{ gap: 6 }}>
                {CANDIDATES.map((c) => (
                  <label key={c.model} className="check" style={{ alignItems: "flex-start", padding: "6px 8px", border: "1px solid var(--grat)", borderRadius: 2, background: picked.has(c.model) ? "var(--signal-wash)" : "var(--plate)" }}>
                    <input type="checkbox" checked={picked.has(c.model)} onChange={(e) => { const n = new Set(picked); e.target.checked ? n.add(c.model) : n.delete(c.model); setPicked(n); }} style={{ marginTop: 3 }} />
                    <span className="grow" style={{ minWidth: 0 }}>
                      <span className="row" style={{ gap: 6 }}><span className="mono trunc" style={{ fontSize: 12.5 }}>{c.model}</span><Badge tone={c.role === "router" ? "info" : c.provider === "fake" ? "tape" : ""}>{c.role}</Badge></span>
                      <span className="hint">{c.note}</span>
                    </span>
                  </label>
                ))}
              </div>
              <div className="grid g3" style={{ gap: 10 }}>
                <div className="field"><label>Repetitions</label><select className="select" value={reps} onChange={(e) => setReps(+e.target.value)}><option value={1}>1</option><option value={2}>2</option><option value={3}>3</option></select></div>
                <div className="field"><label>Tasks</label><select className="select" value={limit ?? ""} onChange={(e) => setLimit(e.target.value ? +e.target.value : null)}><option value="">all 40</option><option value={12}>first 12</option></select></div>
                <div className="field"><label>Cap (USD)</label><input className="input" type="number" min={0} step={0.25} value={cap} onChange={(e) => setCap(+e.target.value || 0)} /></div>
              </div>
              <div className="hint">Repetitions ≥ 2 also measure routing stability: whether the same task goes to the same model each time.</div>
              {pf && (
                <div className="callout" style={{ fontSize: 12.5 }}>
                  <b className="mono">{num(pf.calls_estimate)}</b> model calls · estimated <b className="mono">{usd(pf.cost_low_usd)}–{usd(pf.cost_high_usd)}</b>
                  {pf.unknown_price_arms.length > 0 && <> + router calls at dynamic price (billed per call, reserved conservatively)</>}
                  {pf.warnings.filter((w) => w.includes("image")).map((w) => <div key={w} className="muted">{w}</div>)}
                </div>
              )}
              {paidSelected && <label className="check"><input type="checkbox" checked={confirm} onChange={(e) => setConfirm(e.target.checked)} />I confirm this run may charge my OpenRouter credit, up to the cap above</label>}
              <ErrorBox error={err} />
              <button className="btn signal" disabled={busy || !picked.size || (paidSelected && !confirm)} onClick={launch}><Icon name="play" />{busy ? "Launching…" : "Freeze plan and run"}</button>
            </div>
          )}
        </Card>
      </div>

      <Card title="Router campaigns" flush style={{ marginTop: 14 }}>
        {!camps ? <Loading /> : routerCamps.length === 0 ? <div className="empty">No router campaigns yet.</div> : (
          <table className="t">
            <thead><tr><th>Campaign</th><th>Status</th><th>Targets</th><th className="r">Pass rate</th><th className="r">Spend</th><th>Started</th></tr></thead>
            <tbody>{routerCamps.map((c) => (
              <tr key={c.id} className="click" onClick={() => navigate(`/campaigns/${c.id}`)}>
                <td className="trunc" style={{ maxWidth: 320 }}>{c.name}</td><td><StatusBadge status={c.status} /></td>
                <td className="mono muted trunc" style={{ maxWidth: 320, fontSize: 11.5 }}>{c.targets.map((t) => t.split("/").slice(1).join("/")).join(" · ")}</td>
                <td className="r">{c.assessed ? pct(c.passes / c.assessed) : "—"}</td><td className="r">{usd(c.spent_usd)}</td><td className="muted">{ago(c.created_at)}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
