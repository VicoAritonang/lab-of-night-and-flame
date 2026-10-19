import { useEffect, useMemo, useState } from "react";
import { api } from "../api";
import { ModelPicker } from "../components/ModelPicker";
import { Badge, Card, CopyButton, ErrorBox, Icon, Loading, Modal, useToast } from "../components/ui";
import { num, pct, seriesColor, usd } from "../format";
import { useApi, useDebounced } from "../hooks";
import { navigate, useLocation } from "../router";
import type { Dataset, Plan, Preflight, ScorerInfo, ScorerSpec, TargetSpec } from "../types";

const STEPS = ["Data", "Targets & factors", "Scorers", "Protocol & gate", "Review & launch"];

const defaultPlan = (): Plan => ({
  dataset_id: "", targets: [], prompts: [{ id: "default", system: "{instructions}", user: "{input}" }], params_grid: {},
  scorers: [], primary_scorer: "",
  protocol: { repetitions: 1, order: "interleaved", seed: 20261011, concurrency: 6, timeout_s: 120, max_retries: 2, splits: ["test"], sample_limit: null, stream: true },
  budget: { max_usd: 0.5, paid_run_confirmed: false },
  acceptance: { min_pass_rate: null, baseline_arm: null, non_inferiority_margin: 0.05, min_assessed_per_arm: 20, max_p95_latency_ms: null, max_cost_per_pass_usd: null, max_error_rate: 0.1 },
});

function numOrNull(v: string): number | null { const n = parseFloat(v); return Number.isFinite(n) ? n : null; }

export default function Builder() {
  const { query } = useLocation();
  const [step, setStep] = useState(0);
  const [plan, setPlan] = useState<Plan>(defaultPlan);
  const [name, setName] = useState("");
  const [hypothesis, setHypothesis] = useState("");
  const [picker, setPicker] = useState(false);
  const [busy, setBusy] = useState(false);
  const [launchErr, setLaunchErr] = useState<Error | null>(null);
  const [gridText, setGridText] = useState("");
  const toast = useToast();
  const { data: datasets } = useApi<Dataset[]>("/api/datasets");
  const { data: scorerCatalog } = useApi<ScorerInfo[]>("/api/scorers", { staleMs: 600000 });
  const ds = datasets?.find((d) => d.id === plan.dataset_id);
  const set = (patch: Partial<Plan>) => setPlan((p) => ({ ...p, ...patch }));

  // seed from query (?dataset=) or from the model registry selection
  useEffect(() => {
    const dsId = query.get("dataset");
    if (dsId && datasets && !plan.dataset_id) chooseDataset(datasets.find((d) => d.id === dsId));
    const seeded = sessionStorage.getItem("arl-builder-targets");
    if (seeded) {
      sessionStorage.removeItem("arl-builder-targets");
      try { set({ targets: (JSON.parse(seeded) as { provider: string; model: string }[]).map((t) => ({ provider: t.provider, model: t.model, params: { temperature: 0, max_tokens: 512 } })) }); } catch { /* ignore */ }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [datasets]);

  function chooseDataset(d?: Dataset) {
    if (!d) return;
    const rec: ScorerSpec[] = (d.card?.recommended_scorers ?? []).map((s: any, i: number) => ({ id: i === 0 ? s.key : `${s.key}_${i + 1}`, key: s.key, params: s.params ?? {} }));
    const splits = Object.keys(d.card?.splits ?? { test: 1 });
    setPlan((p) => ({
      ...p, dataset_id: d.id, scorers: rec.length ? rec : p.scorers, primary_scorer: rec.length ? rec[0].id : p.primary_scorer,
      protocol: { ...p.protocol, splits: splits.includes("test") ? ["test"] : [splits[0]] },
      targets: d.modality === "text" ? p.targets.map((t) => ({ ...t, params: { ...t.params, json_mode: d.task_type === "extraction" ? t.params.json_mode : t.params.json_mode } })) : p.targets,
    }));
    if (!name) setName(`${d.name} · model comparison`);
  }

  const dplan = useDebounced(plan, 350);
  const [pf, setPf] = useState<Preflight | null>(null);
  const [pfErr, setPfErr] = useState<Error | null>(null);
  useEffect(() => {
    if (!dplan.dataset_id || !dplan.targets.length || !dplan.scorers.length || !dplan.primary_scorer) { setPf(null); return; }
    let live = true;
    api.post<Preflight>("/api/experiments/preflight", dplan).then((r) => { if (live) { setPf(r); setPfErr(null); } }).catch((e) => live && setPfErr(e));
    return () => { live = false; };
  }, [dplan]);

  const launch = async (doLaunch: boolean) => {
    setBusy(true); setLaunchErr(null);
    try {
      const r = await api.post<{ id: string; campaign_id: string | null }>(`/api/experiments?launch=${doLaunch}`, { name: name || "Untitled experiment", hypothesis, plan });
      if (r.campaign_id) navigate(`/campaigns/${r.campaign_id}`);
      else { toast("Draft saved", "good"); navigate("/experiments"); }
    } catch (e: any) { setLaunchErr(e); } finally { setBusy(false); }
  };

  const paid = pf ? (pf.cost_high_usd ?? 0) > 0 || pf.unknown_price_arms.length > 0 : false;
  const canNext = [!!plan.dataset_id, plan.targets.length > 0, plan.scorers.length > 0 && !!plan.primary_scorer, true, true][step];

  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">New experiment</h1>
          <div className="page-sub">Define the question before seeing results: factors, scorers, budget and acceptance gate are frozen at launch.</div>
        </div>
      </div>
      <div className="steps">
        {STEPS.map((s, i) => (
          <button key={s} className={`step ${i === step ? "on" : ""} ${i < step ? "done" : ""}`} onClick={() => setStep(i)}>
            <span className="step-n">{i < step ? "✓" : i + 1}</span>{s}
          </button>
        ))}
      </div>

      <div className="grid g-main" style={{ alignItems: "start" }}>
        <div className="col" style={{ gap: 14 }}>
          {step === 0 && (
            <Card title="Dataset" sub="Text and image datasets; expected answers stay private to scorers">
              {!datasets ? <Loading /> : (
                <div className="grid g2">
                  {datasets.map((d) => (
                    <button key={d.id} className={`card ${plan.dataset_id === d.id ? "sel" : ""}`} onClick={() => chooseDataset(d)}
                      style={{ textAlign: "left", cursor: "pointer", padding: 12, borderColor: plan.dataset_id === d.id ? "var(--signal)" : undefined, background: plan.dataset_id === d.id ? "var(--signal-wash)" : undefined }}>
                      <div className="row"><Icon name={d.modality === "image" ? "image" : "text"} /><b className="trunc grow">{d.name}</b><span className="mono faint">v{d.version}</span></div>
                      <div className="muted" style={{ fontSize: 12, margin: "6px 0" }}>{d.description}</div>
                      <div className="row wrap" style={{ gap: 4 }}><Badge>{d.sample_count} samples</Badge><Badge>{d.task_type}</Badge>{d.synthetic && <Badge>synthetic</Badge>}</div>
                    </button>
                  ))}
                </div>
              )}
              {ds && (
                <div className="grid g2" style={{ marginTop: 14 }}>
                  <div className="field"><label>Splits</label>
                    <div className="row wrap">{Object.entries(ds.card?.splits ?? {}).map(([s, n]) => (
                      <label key={s} className="check"><input type="checkbox" checked={plan.protocol.splits.includes(s)}
                        onChange={(e) => set({ protocol: { ...plan.protocol, splits: e.target.checked ? [...plan.protocol.splits, s] : plan.protocol.splits.filter((x) => x !== s) } })} />{s} <span className="faint">({String(n)})</span></label>
                    ))}</div>
                  </div>
                  <div className="field"><label>Sample limit (optional, for a cheap smoke run)</label>
                    <input className="input" type="number" min={1} placeholder="all" value={plan.protocol.sample_limit ?? ""} onChange={(e) => set({ protocol: { ...plan.protocol, sample_limit: numOrNull(e.target.value) } })} />
                  </div>
                </div>
              )}
            </Card>
          )}

          {step === 1 && (
            <>
              <Card title={`Targets (${plan.targets.length})`} sub="Each target × prompt variant × parameter combination becomes one arm"
                actions={<button className="btn sm primary" onClick={() => setPicker(true)}><Icon name="plus" size={13} />Add models</button>}>
                {!plan.targets.length ? <div className="empty">No targets yet. Add models from OpenRouter, OpenAI, Groq, a local server, or the fake target for a zero-cost dry run.</div> : (
                  <div className="col" style={{ gap: 8 }}>
                    {plan.targets.map((t, i) => (
                      <TargetRow key={`${t.provider}:${t.model}:${i}`} t={t} i={i} onChange={(nt) => set({ targets: plan.targets.map((x, j) => (j === i ? nt : x)) })} onRemove={() => set({ targets: plan.targets.filter((_, j) => j !== i) })} />
                    ))}
                  </div>
                )}
              </Card>
              <Card title="Factor sweep (optional)" sub="Cartesian product with targets. Arms with identical effective config are de-duplicated.">
                <div className="grid g2">
                  <div className="field"><label>Temperature values (comma separated)</label>
                    <input className="input mono" placeholder="e.g. 0, 0.7" value={gridText} onChange={(e) => {
                      setGridText(e.target.value);
                      const vals = e.target.value.split(",").map((s) => parseFloat(s.trim())).filter((n) => Number.isFinite(n) && n >= 0 && n <= 2);
                      const g = { ...plan.params_grid };
                      if (vals.length) g.temperature = Array.from(new Set(vals)).slice(0, 8); else delete g.temperature;
                      set({ params_grid: g });
                    }} />
                  </div>
                  <div className="field"><label>Arms</label><div className="num" style={{ fontSize: 18, fontWeight: 650 }}>{pf?.arms.length ?? plan.targets.length * plan.prompts.length * (plan.params_grid.temperature?.length ?? 1)}</div></div>
                </div>
              </Card>
              <Card title={`Prompt variants (${plan.prompts.length})`} sub="Placeholders: {instructions} (dataset task instructions), {input} (sample text), {meta.<field>}"
                actions={<button className="btn sm" disabled={plan.prompts.length >= 8} onClick={() => set({ prompts: [...plan.prompts, { id: `v${plan.prompts.length + 1}`, system: "{instructions}\nThink step by step, then give the final answer.", user: "{input}" }] })}><Icon name="plus" size={13} />Variant</button>}>
                <div className="col" style={{ gap: 12 }}>
                  {plan.prompts.map((p, i) => (
                    <div key={i} className="col" style={{ gap: 6, paddingBottom: 12, borderBottom: i < plan.prompts.length - 1 ? "1px solid var(--grat)" : 0 }}>
                      <div className="row"><input className="input mono" style={{ width: 160 }} value={p.id} onChange={(e) => set({ prompts: plan.prompts.map((x, j) => (j === i ? { ...x, id: e.target.value.replace(/[^A-Za-z0-9_\-.]/g, "") } : x)) })} />
                        <span className="spacer" />{plan.prompts.length > 1 && <button className="btn sm ghost" onClick={() => set({ prompts: plan.prompts.filter((_, j) => j !== i) })}><Icon name="trash" size={13} /></button>}</div>
                      <textarea className="textarea mono" rows={2} value={p.system} onChange={(e) => set({ prompts: plan.prompts.map((x, j) => (j === i ? { ...x, system: e.target.value } : x)) })} placeholder="system" />
                      <textarea className="textarea mono" rows={1} value={p.user} onChange={(e) => set({ prompts: plan.prompts.map((x, j) => (j === i ? { ...x, user: e.target.value } : x)) })} placeholder="user" />
                    </div>
                  ))}
                </div>
              </Card>
            </>
          )}

          {step === 2 && <ScorersStep plan={plan} set={set} catalog={scorerCatalog ?? []} ds={ds} />}

          {step === 3 && (
            <>
              <Card title="Protocol">
                <div className="grid g3">
                  <div className="field"><label>Repetitions per sample</label><input className="input" type="number" min={1} max={20} value={plan.protocol.repetitions} onChange={(e) => set({ protocol: { ...plan.protocol, repetitions: Math.max(1, Math.min(20, +e.target.value || 1)) } })} /><span className="hint">≥3 enables pass@k and all-k consistency</span></div>
                  <div className="field"><label>Run order</label><select className="select" value={plan.protocol.order} onChange={(e) => set({ protocol: { ...plan.protocol, order: e.target.value as any } })}><option value="interleaved">randomized interleaved</option><option value="blocked">blocked by sample</option></select><span className="hint">removes time / drift confounds</span></div>
                  <div className="field"><label>Seed</label><input className="input mono" type="number" value={plan.protocol.seed} onChange={(e) => set({ protocol: { ...plan.protocol, seed: +e.target.value || 0 } })} /><span className="hint">metadata, not a determinism promise</span></div>
                  <div className="field"><label>Concurrency</label><input className="input" type="number" min={1} max={64} value={plan.protocol.concurrency} onChange={(e) => set({ protocol: { ...plan.protocol, concurrency: Math.max(1, Math.min(64, +e.target.value || 1)) } })} /></div>
                  <div className="field"><label>Timeout (s)</label><input className="input" type="number" min={5} max={900} value={plan.protocol.timeout_s} onChange={(e) => set({ protocol: { ...plan.protocol, timeout_s: +e.target.value || 120 } })} /></div>
                  <div className="field"><label>Max retries (each is a recorded attempt)</label><input className="input" type="number" min={0} max={6} value={plan.protocol.max_retries} onChange={(e) => set({ protocol: { ...plan.protocol, max_retries: Math.max(0, Math.min(6, +e.target.value || 0)) } })} /></div>
                </div>
              </Card>
              <Card title="Budget" sub="Cost is reserved before every dispatch; admission stops before the cap is crossed">
                <div className="grid g2">
                  <div className="field"><label>Campaign cap (USD)</label><input className="input" type="number" min={0} step={0.05} value={plan.budget.max_usd ?? ""} onChange={(e) => set({ budget: { ...plan.budget, max_usd: numOrNull(e.target.value) } })} /></div>
                  <div className="field"><label>Paid run</label><label className="check" style={{ height: 32 }}><input type="checkbox" checked={plan.budget.paid_run_confirmed} onChange={(e) => set({ budget: { ...plan.budget, paid_run_confirmed: e.target.checked } })} />I confirm this run may incur provider charges</label></div>
                </div>
              </Card>
              <Card title="Acceptance gate" sub="Decided before results exist. Verdicts: pass · fail · insufficient evidence · invalid experiment">
                <div className="grid g3">
                  <div className="field"><label>Min pass rate (CI-based)</label><input className="input" type="number" min={0} max={1} step={0.05} placeholder="none" value={plan.acceptance.min_pass_rate ?? ""} onChange={(e) => set({ acceptance: { ...plan.acceptance, min_pass_rate: numOrNull(e.target.value) } })} /><span className="hint">pass if CI low ≥ threshold, fail if CI high &lt; threshold</span></div>
                  <div className="field"><label>Baseline arm</label><select className="select" value={plan.acceptance.baseline_arm ?? ""} onChange={(e) => set({ acceptance: { ...plan.acceptance, baseline_arm: e.target.value || null } })}>
                    <option value="">first target</option>{(pf?.arms ?? []).map((a) => <option key={a.key} value={a.key}>{a.label}</option>)}</select></div>
                  <div className="field"><label>Non-inferiority margin</label><input className="input" type="number" min={0} max={1} step={0.01} placeholder="none" value={plan.acceptance.non_inferiority_margin ?? ""} onChange={(e) => set({ acceptance: { ...plan.acceptance, non_inferiority_margin: numOrNull(e.target.value) } })} /><span className="hint">Δ CI low must be ≥ −margin</span></div>
                  <div className="field"><label>Min assessed trials per arm</label><input className="input" type="number" min={1} value={plan.acceptance.min_assessed_per_arm} onChange={(e) => set({ acceptance: { ...plan.acceptance, min_assessed_per_arm: Math.max(1, +e.target.value || 1) } })} /></div>
                  <div className="field"><label>Max p95 latency (ms)</label><input className="input" type="number" min={1} placeholder="none" value={plan.acceptance.max_p95_latency_ms ?? ""} onChange={(e) => set({ acceptance: { ...plan.acceptance, max_p95_latency_ms: numOrNull(e.target.value) } })} /></div>
                  <div className="field"><label>Max cost per verified pass (USD)</label><input className="input" type="number" min={0} step={0.0001} placeholder="none" value={plan.acceptance.max_cost_per_pass_usd ?? ""} onChange={(e) => set({ acceptance: { ...plan.acceptance, max_cost_per_pass_usd: numOrNull(e.target.value) } })} /></div>
                  <div className="field"><label>Max infrastructure error rate</label><input className="input" type="number" min={0} max={1} step={0.01} value={plan.acceptance.max_error_rate} onChange={(e) => set({ acceptance: { ...plan.acceptance, max_error_rate: numOrNull(e.target.value) ?? 0.1 } })} /></div>
                </div>
              </Card>
            </>
          )}

          {step === 4 && (
            <>
              <Card title="Identity">
                <div className="col" style={{ gap: 10 }}>
                  <div className="field"><label>Name</label><input className="input" value={name} onChange={(e) => setName(e.target.value)} /></div>
                  <div className="field"><label>Hypothesis / decision question</label><textarea className="textarea" rows={2} value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} placeholder="e.g. A cheaper model preserves critical-field correctness within 2 points." /></div>
                </div>
              </Card>
              {pf && (
                <Card title="Arms" sub="Per-arm trial count and worst-case cost" flush>
                  <table className="t"><thead><tr><th>Arm</th><th className="r">Trials</th><th className="r">Est. cost range</th><th>Applicable</th></tr></thead>
                    <tbody>{pf.arms.map((a, i) => (
                      <tr key={a.key}><td><span className="row"><span className="swatch" style={{ background: seriesColor(i) }} /><span className="trunc">{a.label}</span></span></td><td className="r">{a.trials}</td>
                        <td className="r">{a.price_known ? `${usd(a.cost_low_usd)} – ${usd(a.cost_high_usd)}` : <span className="warn">unknown price</span>}</td>
                        <td>{a.applicable ? <Badge tone="good">yes</Badge> : <Badge tone="warn">no — modality</Badge>}</td></tr>
                    ))}</tbody></table>
                </Card>
              )}
              <Card title="Frozen manifest (arl-experiment-v1)" actions={<CopyButton text={JSON.stringify({ name, hypothesis, plan }, null, 2)} />}>
                <pre className="code" style={{ maxHeight: 300 }}>{JSON.stringify(plan, null, 2)}</pre>
              </Card>
              <ErrorBox error={launchErr} />
            </>
          )}

          <div className="row">
            <button className="btn" disabled={step === 0} onClick={() => setStep(step - 1)}><Icon name="back" />Back</button>
            <span className="spacer" />
            {step < 4 ? <button className="btn primary" disabled={!canNext} onClick={() => setStep(step + 1)}>Next<Icon name="arrow" /></button> : (
              <>
                <button className="btn" disabled={busy} onClick={() => launch(false)}>Save draft</button>
                <button className="btn primary" disabled={busy || !pf?.ok || (paid && !plan.budget.paid_run_confirmed)} onClick={() => launch(true)}><Icon name="play" />{busy ? "Launching…" : "Freeze & launch"}</button>
              </>
            )}
          </div>
        </div>

        <div className="col" style={{ gap: 14, position: "sticky", top: 70 }}>
          <PreflightPanel pf={pf} err={pfErr} plan={plan} ds={ds} paid={paid} />
        </div>
      </div>

      <Modal open={picker} onClose={() => setPicker(false)} title="Add targets" footer={<button className="btn primary" onClick={() => setPicker(false)}>Done ({plan.targets.length})</button>}>
        <ModelPicker max={24} selected={plan.targets} requireImage={ds?.modality === "image"}
          onToggle={(m) => {
            const exists = plan.targets.some((t) => t.provider === m.provider && t.model === m.id);
            set({ targets: exists ? plan.targets.filter((t) => !(t.provider === m.provider && t.model === m.id))
              : [...plan.targets, { provider: m.provider, model: m.id, params: { temperature: 0, max_tokens: ds?.task_type === "reasoning" ? 1024 : 512, json_mode: false } }] });
          }} />
      </Modal>
    </div>
  );
}

function TargetRow({ t, i, onChange, onRemove }: { t: TargetSpec; i: number; onChange: (t: TargetSpec) => void; onRemove: () => void }) {
  const p = t.params;
  const setP = (patch: Partial<TargetSpec["params"]>) => onChange({ ...t, params: { ...p, ...patch } });
  return (
    <div className="row wrap" style={{ gap: 10, padding: "8px 10px", border: "1px solid var(--grat)", borderRadius: 2, background: "var(--plate-2)" }}>
      <span className="swatch" style={{ background: seriesColor(i) }} />
      <div style={{ minWidth: 180, flex: "1 1 200px" }}><div className="trunc" style={{ fontWeight: 550 }}>{t.model}</div><div className="mono muted">{t.provider}</div></div>
      <label className="field" style={{ width: 90 }}><span className="label">temp</span><input className="input" type="number" min={0} max={2} step={0.1} value={p.temperature ?? ""} onChange={(e) => setP({ temperature: numOrNull(e.target.value) })} /></label>
      <label className="field" style={{ width: 100 }}><span className="label">max tokens</span><input className="input" type="number" min={1} max={32768} value={p.max_tokens} onChange={(e) => setP({ max_tokens: Math.max(1, +e.target.value || 1) })} /></label>
      <label className="field" style={{ width: 110 }}><span className="label">reasoning</span><select className="select" value={p.reasoning_effort ?? ""} onChange={(e) => setP({ reasoning_effort: e.target.value || null })}><option value="">default</option><option>minimal</option><option>low</option><option>medium</option><option>high</option></select></label>
      <label className="check" style={{ marginTop: 16 }}><input type="checkbox" checked={!!p.json_mode} onChange={(e) => setP({ json_mode: e.target.checked })} />JSON mode</label>
      <button className="btn sm ghost icon" style={{ marginTop: 16 }} onClick={onRemove} aria-label="remove"><Icon name="trash" size={13} /></button>
    </div>
  );
}

function ScorersStep({ plan, set, catalog, ds }: { plan: Plan; set: (p: Partial<Plan>) => void; catalog: ScorerInfo[]; ds?: Dataset }) {
  const [adding, setAdding] = useState("");
  const add = (key: string) => {
    const info = catalog.find((c) => c.key === key);
    if (!info) return;
    let id = key, n = 2;
    while (plan.scorers.some((s) => s.id === id)) id = `${key}_${n++}`;
    const next = [...plan.scorers, { id, key, params: { ...info.params } }];
    set({ scorers: next, primary_scorer: plan.primary_scorer || id });
  };
  return (
    <Card title="Scorers" sub="Deterministic scorers first; a model judge is never ground truth — calibrate before gating on it">
      <div className="col" style={{ gap: 10 }}>
        {ds?.card?.recommended_scorers && <div className="hint">Recommended for {ds.name}: {ds.card.recommended_scorers.map((s: any) => s.key).join(", ")}</div>}
        {plan.scorers.map((s, i) => {
          const info = catalog.find((c) => c.key === s.key);
          return (
            <div key={s.id} style={{ border: "1px solid var(--grat)", borderRadius: 2, padding: 10, background: plan.primary_scorer === s.id ? "var(--signal-wash)" : "var(--plate-2)" }}>
              <div className="row">
                <label className="check"><input type="radio" name="primary" checked={plan.primary_scorer === s.id} onChange={() => set({ primary_scorer: s.id })} />primary</label>
                <b>{info?.label ?? s.key}</b><span className="mono faint">{s.id}</span>{info?.kind === "judge" && <Badge tone="warn">model judge</Badge>}
                <span className="spacer" />
                <button className="btn sm ghost icon" onClick={() => { const next = plan.scorers.filter((_, j) => j !== i); set({ scorers: next, primary_scorer: plan.primary_scorer === s.id ? next[0]?.id ?? "" : plan.primary_scorer }); }}><Icon name="trash" size={13} /></button>
              </div>
              <div className="muted" style={{ fontSize: 12, margin: "4px 0 8px" }}>{info?.description}</div>
              <ParamsEditor value={s.params} onChange={(params) => set({ scorers: plan.scorers.map((x, j) => (j === i ? { ...x, params } : x)) })} />
            </div>
          );
        })}
        <div className="row">
          <select className="select" value={adding} onChange={(e) => setAdding(e.target.value)} style={{ maxWidth: 320 }}>
            <option value="">Add scorer…</option>
            {catalog.map((c) => <option key={c.key} value={c.key}>{c.label} ({c.kind})</option>)}
          </select>
          <button className="btn" disabled={!adding} onClick={() => { add(adding); setAdding(""); }}><Icon name="plus" />Add</button>
        </div>
      </div>
    </Card>
  );
}

function ParamsEditor({ value, onChange }: { value: Record<string, any>; onChange: (v: Record<string, any>) => void }) {
  const [text, setText] = useState(() => JSON.stringify(value, null, 1));
  const [err, setErr] = useState("");
  if (!Object.keys(value).length && !text.trim().replace(/[{}\s]/g, "")) return <div className="hint">No parameters.</div>;
  return (
    <div>
      <textarea className="textarea mono" rows={Math.min(8, text.split("\n").length + 1)} value={text}
        onChange={(e) => { setText(e.target.value); try { onChange(JSON.parse(e.target.value)); setErr(""); } catch { setErr("invalid JSON"); } }} />
      {err && <div className="bad" style={{ fontSize: 11.5 }}>{err}</div>}
    </div>
  );
}

function PreflightPanel({ pf, err, plan, ds, paid }: { pf: Preflight | null; err: Error | null; plan: Plan; ds?: Dataset; paid: boolean }) {
  const steps = useMemo(() => [
    { ok: !!plan.dataset_id, label: ds ? `${ds.name} (${ds.modality})` : "Choose a dataset" },
    { ok: plan.targets.length > 0, label: `${plan.targets.length} target(s)` },
    { ok: plan.scorers.length > 0 && !!plan.primary_scorer, label: plan.primary_scorer ? `primary: ${plan.primary_scorer}` : "Add a scorer" },
  ], [plan, ds]);
  return (
    <Card title="Preflight" sub="Validated live against the server before anything runs">
      <div className="col" style={{ gap: 8 }}>
        {steps.map((s, i) => <div key={i} className="row"><span className={s.ok ? "good" : "faint"}><Icon name={s.ok ? "check" : "info"} size={14} /></span><span className={s.ok ? "" : "muted"}>{s.label}</span></div>)}
        {err && <ErrorBox error={err} />}
        {pf && (
          <>
            <div className="divider" />
            <div className="grid g2" style={{ gap: 10 }}>
              <div><div className="stat-label">Samples</div><div className="num" style={{ fontSize: 18, fontWeight: 650 }}>{num(pf.samples)}</div></div>
              <div><div className="stat-label">Trials</div><div className="num" style={{ fontSize: 18, fontWeight: 650 }}>{num(pf.trials)}</div></div>
              <div><div className="stat-label">Model calls</div><div className="num" style={{ fontSize: 18, fontWeight: 650 }}>{num(pf.calls_estimate)}</div></div>
              <div><div className="stat-label">Judge calls</div><div className="num" style={{ fontSize: 18, fontWeight: 650 }}>{num(pf.judge_calls_estimate)}</div></div>
            </div>
            <div><div className="stat-label">Estimated cost</div>
              <div className="num" style={{ fontSize: 18, fontWeight: 650 }}>{pf.cost_high_usd === 0 && !pf.unknown_price_arms.length ? "$0" : `${usd(pf.cost_low_usd)} – ${usd(pf.cost_high_usd)}`}</div>
              <div className="hint">range: short outputs → every call hits max_tokens{pf.unknown_price_arms.length ? "; excludes unknown prices" : ""}</div>
              {plan.budget.max_usd != null && pf.cost_high_usd != null && <div className="progress" style={{ marginTop: 6 }}><i style={{ width: `${Math.min(100, (pf.cost_high_usd / (plan.budget.max_usd || 1e-9)) * 100)}%`, background: pf.cost_high_usd > plan.budget.max_usd ? "var(--warn)" : "var(--signal)" }} /></div>}
            </div>
            <div className="row wrap" style={{ gap: 4 }}>{pf.labels.map((l) => <Badge key={l}>{l.replace(/_/g, " ")}</Badge>)}</div>
            <div className="divider" />
            <div className="stat-label">Data disclosure</div>
            <div className="col" style={{ gap: 6 }}>
              {pf.disclosure.map((d, i) => (
                <div key={i} style={{ fontSize: 12 }}><Badge tone={d.kind === "external" || d.kind === "judge" ? "warn" : "good"}>{d.kind}</Badge> <b>{d.recipient}</b><div className="muted">{d.data}</div></div>
              ))}
            </div>
            {pf.errors.map((e, i) => <div key={i} className="callout bad" style={{ fontSize: 12 }}>{e}</div>)}
            {pf.warnings.map((w, i) => <div key={i} className="callout warn" style={{ fontSize: 12 }}>{w}</div>)}
            {paid && !plan.budget.paid_run_confirmed && <div className="callout warn" style={{ fontSize: 12 }}>Paid run — confirm in <b>Protocol & gate → Budget</b> before launch.</div>}
            <div className="mono faint" style={{ fontSize: 11 }}>plan {pf.plan_digest.slice(0, 16)} · min pass {pct(plan.acceptance.min_pass_rate, 0)}</div>
          </>
        )}
      </div>
    </Card>
  );
}
