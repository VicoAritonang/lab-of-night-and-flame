import { useEffect, useMemo, useRef, useState } from "react";
import { useWidth } from "./charts";

/**
 * The measurement signal path, drawn as a schematic.
 *
 *   DATASET ─▶ SCHEDULER ─┬─▶ target A ─┬─▶ SCORERS ─▶ GATE
 *                         └─▶ target B ─┘
 *
 * Each trial is a pulse. It carries its arm's colour until it is scored, then takes the outcome colour
 * (pass / fail / inconclusive / infra error). In demo mode the pulses are synthetic and a caption walks
 * through what each stage guarantees; in live mode pulses are real trial completions from the SSE stream.
 */
export interface PathArm { key: string; label: string; color: string; readout?: string }
export interface PathEvent { id: string; arm: number; outcome: "pass" | "fail" | "inconclusive" | "error" | "pending" }
export interface Readouts { dataset?: string; scheduler?: string; scorers?: string; gate?: string }

const STAGES = [
  { key: "dataset", label: "Dataset", caption: "Samples come from a frozen, content-hashed snapshot. Expected answers are split off here and never travel to the model." },
  { key: "scheduler", label: "Scheduler", caption: "Each trial is admitted in seeded random order. Its worst-case cost is reserved against the budget before any request is sent." },
  { key: "targets", label: "Targets", caption: "Arms run in parallel under per-provider rate limits. Every provider call, including retries and timeouts, is recorded as an attempt." },
  { key: "scorers", label: "Scorers", caption: "Outputs are graded against the private reference. The pulse changes colour here: green pass, red fail, amber inconclusive." },
  { key: "gate", label: "Gate", caption: "Results are aggregated with confidence intervals and checked against the acceptance plan you froze before the run." },
] as const;

const OUTCOME_COLOR: Record<string, string> = {
  pass: "var(--good)", fail: "var(--bad)", inconclusive: "var(--warn)", error: "var(--serious)", pending: "var(--signal)",
};

interface Pulse { id: string; arm: number; outcome: string; born: number; dur: number }

export function SignalPath({ arms, events, readouts, demo = false, height: hProp }: {
  arms: PathArm[]; events?: PathEvent[]; readouts?: Readouts; demo?: boolean; height?: number;
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const w = Math.max(width, 720);
  const lanes = Math.max(1, Math.min(arms.length, 6));
  const laneGap = 34;
  const h = hProp ?? Math.max(176, 56 + lanes * laneGap);
  const yc = h / 2;
  const boxW = 128, boxH = 60, armW = Math.min(190, w * 0.17), armH = 24;
  const xs = useMemo(() => {
    const pad = 4, usable = w - pad * 2;
    return { ds: pad, sch: pad + usable * 0.205, tgt: pad + usable * 0.43, sc: pad + usable * 0.68, gate: pad + usable - boxW };
  }, [w]);
  const armY = (i: number) => yc + (i - (lanes - 1) / 2) * laneGap;

  // polyline for a pulse travelling through arm i, with the index where scoring happens
  const route = (i: number): { pts: [number, number][]; scoreAt: number } => {
    const y = armY(i);
    const mid1 = (xs.sch + boxW + xs.tgt) / 2, mid2 = (xs.tgt + armW + xs.sc) / 2;
    const pts: [number, number][] = [
      [xs.ds + boxW, yc], [xs.sch, yc], [xs.sch + boxW, yc], [mid1, yc], [mid1, y], [xs.tgt, y],
      [xs.tgt + armW, y], [mid2, y], [mid2, yc], [xs.sc, yc], [xs.sc + boxW, yc], [xs.gate, yc],
    ];
    return { pts, scoreAt: 10 };
  };
  const hidden = (seg: number) => seg === 1 || seg === 5 || seg === 9; // inside boxes

  const pulses = useRef<Pulse[]>([]);
  const seen = useRef<Set<string>>(new Set());
  const hits = useRef<Record<string, number>>({});
  const [, setFrame] = useState(0);
  const [step, setStep] = useState(0);
  const reduce = typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

  // live: turn new events into pulses (rate-limited so a burst stays readable)
  useEffect(() => {
    if (!events) return;
    const now = performance.now();
    let spawned = 0;
    for (const e of events) {
      if (seen.current.has(e.id)) continue;
      seen.current.add(e.id);
      if (spawned++ > 24) continue;
      pulses.current.push({ id: e.id, arm: Math.min(e.arm, lanes - 1), outcome: e.outcome, born: now + spawned * 45, dur: 1700 });
    }
    if (seen.current.size > 5000) seen.current = new Set([...seen.current].slice(-2000));
    if (spawned) kick.current();
  }, [events, lanes]);

  // demo: synthetic pulses + a caption that walks through the stages
  useEffect(() => {
    if (!demo) return;
    let n = 0;
    const spawn = window.setInterval(() => {
      const arm = Math.floor(Math.random() * lanes);
      const r = Math.random();
      const pRate = [0.86, 0.62, 0.74, 0.5][arm % 4];
      const outcome = r < pRate ? "pass" : r < pRate + 0.06 ? "inconclusive" : r < 0.97 ? "fail" : "error";
      pulses.current.push({ id: `demo-${n++}`, arm, outcome, born: performance.now(), dur: 2400 });
      kick.current();
    }, 520);
    const cap = window.setInterval(() => setStep((s) => (s + 1) % STAGES.length), 3600);
    return () => { window.clearInterval(spawn); window.clearInterval(cap); };
  }, [demo, lanes]);

  // animation loop: runs only while pulses are in flight (idle pages cost nothing)
  const raf = useRef(0);
  const kick = useRef<() => void>(() => {});
  kick.current = () => {
    if (reduce || raf.current) return;
    const tick = () => {
      const t = performance.now();
      pulses.current = pulses.current.filter((p) => t - p.born < p.dur + 320); // linger so node lamps fade out
      setFrame((f) => (f + 1) % 1e6);
      raf.current = pulses.current.length || demo ? requestAnimationFrame(tick) : 0;
    };
    raf.current = requestAnimationFrame(tick);
  };
  useEffect(() => () => { cancelAnimationFrame(raf.current); raf.current = 0; }, []);

  const now = performance.now();
  const drawn = pulses.current.map((p) => {
    const k = Math.max(0, Math.min(1, (now - p.born) / p.dur));
    if (k <= 0) return null;
    if (k >= 1) { hits.current.gate = Math.max(hits.current.gate ?? 0, p.born + p.dur); return null; }
    const { pts, scoreAt } = route(p.arm);
    const lens = pts.slice(1).map((q, i) => Math.hypot(q[0] - pts[i][0], q[1] - pts[i][1]));
    const total = lens.reduce((a, b) => a + b, 0);
    let d = k * total, seg = 0;
    while (seg < lens.length - 1 && d > lens[seg]) { d -= lens[seg]; seg++; }
    const f = lens[seg] ? d / lens[seg] : 0;
    const x = pts[seg][0] + (pts[seg + 1][0] - pts[seg][0]) * f;
    const y = pts[seg][1] + (pts[seg + 1][1] - pts[seg][1]) * f;
    const stageHit = seg === 1 ? "scheduler" : seg === 5 ? `arm${p.arm}` : seg === 9 ? "scorers" : seg >= 11 ? "gate" : null;
    if (stageHit) hits.current[stageHit] = now;
    if (hidden(seg)) return null;
    const scored = seg >= scoreAt;
    const color = scored ? OUTCOME_COLOR[p.outcome] : arms[p.arm]?.color ?? "var(--signal)";
    return <circle key={p.id} cx={x} cy={y} r={scored ? 4.5 : 3.5} fill={color} stroke="var(--plate)" strokeWidth={1.5} />;
  });
  const lit = (key: string) => now - (hits.current[key] ?? -1e9) < 260;
  const stage = STAGES[step];
  const focus = demo ? stage.key : null;

  const node = (key: string, x: number, label: string, readout?: string) => {
    const on = lit(key) || focus === key;
    return (
      <g className={`node ${on ? "active" : ""}`} transform={`translate(${x}, ${yc - boxH / 2})`}>
        <rect width={boxW} height={boxH} rx={2} style={{ transition: "stroke .2s" }} />
        {on && <rect width={boxW} height={boxH} rx={2} fill="var(--signal-wash)" stroke="none" />}
        <text x={10} y={20} style={{ fontSize: 11, fontWeight: 600, letterSpacing: ".1em", fill: "var(--ink)" }}>{label.toUpperCase()}</text>
        <text x={10} y={42} style={{ fontSize: 12, fontFamily: "var(--mono)", fill: "var(--muted)" }}>{readout ?? ""}</text>
        <circle cx={boxW - 12} cy={14} r={3} fill={on ? "var(--signal)" : "var(--grat-2)"} />
      </g>
    );
  };

  const mid1 = (xs.sch + boxW + xs.tgt) / 2, mid2 = (xs.tgt + armW + xs.sc) / 2;
  return (
    <div>
      <div ref={ref} style={{ overflowX: "auto" }}>
        {width > 0 && (
          <svg className="schematic" width={w} height={h} role="img" aria-label="Signal path: dataset, scheduler, targets, scorers, gate">
            <line className="wire" x1={xs.ds + boxW} x2={xs.sch} y1={yc} y2={yc} />
            <line className="wire" x1={xs.sc + boxW} x2={xs.gate} y1={yc} y2={yc} />
            {Array.from({ length: lanes }, (_, i) => {
              const y = armY(i);
              return (
                <g key={i}>
                  <polyline className="wire" points={`${xs.sch + boxW},${yc} ${mid1},${yc} ${mid1},${y} ${xs.tgt},${y}`} />
                  <polyline className="wire" points={`${xs.tgt + armW},${y} ${mid2},${y} ${mid2},${yc} ${xs.sc},${yc}`} />
                  <circle cx={mid1} cy={y} r={2} fill="var(--grat-2)" />
                  <circle cx={mid2} cy={y} r={2} fill="var(--grat-2)" />
                </g>
              );
            })}
            {node("dataset", xs.ds, "Dataset", readouts?.dataset)}
            {node("scheduler", xs.sch, "Scheduler", readouts?.scheduler)}
            {node("scorers", xs.sc, "Scorers", readouts?.scorers)}
            {node("gate", xs.gate, "Gate", readouts?.gate)}
            {Array.from({ length: lanes }, (_, i) => {
              const a = arms[i];
              const on = lit(`arm${i}`) || focus === "targets";
              return (
                <g key={`arm-${i}`} transform={`translate(${xs.tgt}, ${armY(i) - armH / 2})`}>
                  <rect width={armW} height={armH} rx={2} fill={on ? "var(--signal-wash)" : "var(--plate)"} stroke={on ? "var(--signal)" : "var(--ink)"} strokeWidth={1.2} style={{ transition: "fill .2s, stroke .2s" }} />
                  <rect width={4} height={armH} fill={a?.color ?? "var(--grat-2)"} />
                  <text x={11} y={16} style={{ fontSize: 11.5, fontFamily: "var(--sans)", fill: "var(--ink)" }}>{(a?.label ?? "").slice(0, Math.floor((armW - 50) / 6.4))}</text>
                  {a?.readout && <text x={armW - 6} y={16} textAnchor="end" style={{ fontSize: 11, fontFamily: "var(--mono)", fill: "var(--muted)" }}>{a.readout}</text>}
                </g>
              );
            })}
            <g>{drawn}</g>
          </svg>
        )}
      </div>
      {demo && (
        <div className="row" style={{ marginTop: 12, gap: 12, alignItems: "flex-start", minHeight: 44 }}>
          <div className="row" style={{ gap: 4, paddingTop: 5 }}>
            {STAGES.map((s, i) => (
              <button key={s.key} onClick={() => setStep(i)} aria-label={`Explain ${s.label}`}
                style={{ width: i === step ? 18 : 6, height: 6, border: 0, borderRadius: 3, cursor: "pointer", padding: 0, transition: "width .3s var(--ease)", background: i === step ? "var(--ink)" : "var(--grat-2)" }} />
            ))}
          </div>
          <div key={step} className="fade-in" style={{ fontSize: 13.5, color: "var(--ink-2)", maxWidth: 780 }}>
            <span className="cond" style={{ fontWeight: 600, letterSpacing: ".08em", textTransform: "uppercase", color: "var(--ink)", marginRight: 8 }}>{stage.label}</span>{stage.caption}
          </div>
        </div>
      )}
      <div className="legend" style={{ marginTop: demo ? 8 : 12 }}>
        <span><i className="swatch" style={{ background: "var(--signal)", borderRadius: 2 }} />in flight (arm colour)</span>
        <span><i className="swatch" style={{ background: "var(--good)", borderRadius: 2 }} />pass</span>
        <span><i className="swatch" style={{ background: "var(--bad)", borderRadius: 2 }} />fail</span>
        <span><i className="swatch" style={{ background: "var(--warn)", borderRadius: 2 }} />inconclusive</span>
        <span><i className="swatch" style={{ background: "var(--serious)", borderRadius: 2 }} />infra error</span>
      </div>
    </div>
  );
}
