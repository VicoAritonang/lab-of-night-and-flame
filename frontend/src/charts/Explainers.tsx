import { useMemo } from "react";
import { useTooltip } from "../components/ui";
import { pct, usd, VERDICT } from "../format";
import type { GateCheck, Interval } from "../types";
import { useWidth } from "./charts";

/* ======================================================================
   EvidenceScope — why the gate said what it said.
   Sequence: estimate slides in → 95% interval opens around it → stamp.
   The sentence underneath states the rule that produced the verdict.
   ====================================================================== */
const Z = 1.96;

function samplesNeeded(p: number, thr: number): number | null {
  const gap = Math.abs(p - thr);
  if (gap < 0.005 || p <= 0 || p >= 1) return null;
  return Math.ceil((Z * Z * p * (1 - p)) / (gap * gap));
}

export function explainUtility(iv: Interval, thr: number | null | undefined): string {
  if (iv.estimate == null) return "No assessed trials yet — nothing to measure.";
  if (thr == null) return `Observed ${pct(iv.estimate)} with a 95% interval of ${pct(iv.low, 0)}–${pct(iv.high, 0)}. No pass-rate threshold was set for this gate.`;
  const t = pct(thr, 0);
  if (iv.low != null && iv.low >= thr) return `The whole 95% interval (${pct(iv.low, 0)}–${pct(iv.high, 0)}) sits above ${t}, so the threshold is met with 95% confidence.`;
  if (iv.high != null && iv.high < thr) return `The whole 95% interval (${pct(iv.low, 0)}–${pct(iv.high, 0)}) sits below ${t}, so the threshold is missed.`;
  const need = samplesNeeded(iv.estimate, thr);
  const more = need ? ` If the rate holds, roughly ${need} assessed trials would separate it from ${t} (you have ${iv.n}).` : "";
  return `The interval ${pct(iv.low, 0)}–${pct(iv.high, 0)} crosses ${t}: the data can't yet tell which side the true rate is on.${more}`;
}

export function EvidenceScope({ rows, threshold }: {
  rows: { key: string; label: string; color: string; iv: Interval; verdict: string; checks: GateCheck[]; baseline?: boolean }[];
  threshold?: number | null;
}) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const lw = Math.min(240, Math.max(130, w * 0.22)), stampW = 250;
  const pw = Math.max(120, w - lw - stampW - 70);
  const x = (v: number) => lw + v * pw;
  return (
    <div ref={ref} className="col" style={{ gap: 0 }}>
      {w > 0 && rows.map((r, i) => {
        const est = r.iv.estimate;
        const d = 0.15 + i * 0.12;
        const v = VERDICT[r.verdict] ?? { text: r.verdict, tone: "" };
        const failing = r.checks.filter((c) => c.result !== "pass");
        return (
          <div key={r.key} style={{ padding: "14px 0", borderBottom: i < rows.length - 1 ? "1px dashed var(--grat)" : 0 }}>
            <div style={{ position: "relative", height: 44 }}>
              <svg className="chart" width={w} height={44} style={{ position: "absolute", inset: 0 }}>
                <text x={0} y={18} className="lbl" style={{ fontWeight: 600 }}>{r.label.length > 30 ? r.label.slice(0, 29) + "…" : r.label}</text>
                <text x={0} y={35}>{r.baseline ? "baseline · " : ""}n = {r.iv.n}</text>
                <rect x={lw} y={14} width={pw} height={16} fill="var(--bench-2)" stroke="var(--grat)" />
                {[0, 0.25, 0.5, 0.75, 1].map((t) => <line key={t} x1={x(t)} x2={x(t)} y1={14} y2={30} stroke="var(--grat)" />)}
                {threshold != null && (
                  <g>
                    <line x1={x(threshold)} x2={x(threshold)} y1={8} y2={36} stroke="var(--ink)" strokeWidth={1.5} strokeDasharray="3 2" />
                    {i === 0 && <text x={x(threshold)} y={6} textAnchor="middle" style={{ fill: "var(--ink)", fontSize: 10.5 }}>threshold {pct(threshold, 0)}</text>}
                  </g>
                )}
                {est != null && r.iv.low != null && r.iv.high != null && (
                  <>
                    {/* the interval opens outward from the estimate */}
                    <rect x={x(r.iv.low)} y={17} width={Math.max(1, x(est) - x(r.iv.low))} height={10} fill={r.color} opacity={0.35}
                      style={{ transformBox: "fill-box", transformOrigin: "right center", animation: `bar-in .7s var(--ease) ${d + 0.55}s both` }} />
                    <rect x={x(est)} y={17} width={Math.max(1, x(r.iv.high) - x(est))} height={10} fill={r.color} opacity={0.35}
                      style={{ transformBox: "fill-box", transformOrigin: "left center", animation: `bar-in .7s var(--ease) ${d + 0.55}s both` }} />
                    <line x1={x(r.iv.low)} x2={x(r.iv.low)} y1={14} y2={30} stroke="var(--ink)" strokeWidth={1.5} style={{ animation: `fade .3s ${d + 1.1}s both` }} />
                    <line x1={x(r.iv.high)} x2={x(r.iv.high)} y1={14} y2={30} stroke="var(--ink)" strokeWidth={1.5} style={{ animation: `fade .3s ${d + 1.1}s both` }} />
                  </>
                )}
                {est != null && (
                  <g style={{ transform: `translateX(${x(est) - lw}px)`, animation: `scope-dot .6s var(--ease) ${d}s both`, ["--dx" as any]: `${x(est) - lw}px` }}>
                    <circle cx={lw} cy={22} r={6} fill={r.color} stroke="var(--plate)" strokeWidth={2} />
                  </g>
                )}
                <text x={lw + pw + 10} y={26} className="val">{pct(est)}</text>
              </svg>
              <div style={{ position: "absolute", right: 0, top: 8 }}>
                <span className={`stamp ${r.verdict}`} style={{ ["--d" as any]: `${d + 1.3}s` }}>{v.text}</span>
              </div>
            </div>
            <div style={{ paddingLeft: lw, fontSize: 13, color: "var(--ink-2)", marginTop: 4, animation: `fade .5s ${d + 1.4}s both` }}>
              <span className="eyebrow" style={{ marginRight: 8 }}>Pass rate</span>{explainUtility(r.iv, threshold)}
              {failing.filter((c) => c.check !== "utility").length > 0 && (
                <div className="row wrap" style={{ gap: 6, marginTop: 8 }}>
                  <span className="eyebrow">{r.verdict === "pass" ? "Notes" : "Also holding the verdict back"}</span>
                  {failing.filter((c) => c.check !== "utility").map((c, j) => (
                    <span key={j} className={`badge ${c.result === "insufficient_evidence" ? "warn" : "bad"}`} title={c.detail}>{c.check}: {c.detail.length > 60 ? c.detail.slice(0, 58) + "…" : c.detail}</span>
                  ))}
                </div>
              )}
            </div>
          </div>
        );
      })}
      <style>{`@keyframes scope-dot { from { transform: translateX(0); opacity: 0; } to { transform: translateX(var(--dx)); opacity: 1; } }`}</style>
    </div>
  );
}

/* ======================================================================
   RouterFlow — where a router sends each kind of task.
   Left: task slices. Right: concrete models the router resolved to.
   Ribbon width = calls; particles travel along each ribbon.
   ====================================================================== */
export interface FlowCell { n: number; passes: number; assessed: number }
export function RouterFlow({ rows, modelColor, sliceKey }: {
  rows: { value: string; routes: Record<string, FlowCell> }[]; modelColor: (m: string) => string; sliceKey: string;
}) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const tip = useTooltip();
  const models = useMemo(() => {
    const tot: Record<string, number> = {};
    rows.forEach((r) => Object.entries(r.routes).forEach(([m, c]) => { tot[m] = (tot[m] ?? 0) + c.n; }));
    return Object.entries(tot).sort((a, b) => b[1] - a[1]).map(([m, n]) => ({ m, n }));
  }, [rows]);
  const total = models.reduce((a, b) => a + b.n, 0);
  const gap = 10, nodeW = 10;
  const h = Math.max(220, Math.max(rows.length, models.length) * 46) + 16;
  const usable = h - gap * (Math.max(rows.length, models.length) - 1) - 8;
  const lx = Math.min(170, w * 0.22), rx = w - Math.min(260, w * 0.32);
  const scale = total ? usable / total : 0;
  const reduce = typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

  const left = useMemo(() => {
    let y = 4;
    return rows.map((r) => {
      const n = Object.values(r.routes).reduce((a, c) => a + c.n, 0);
      const node = { value: r.value, n, y, h: n * scale };
      y += n * scale + gap;
      return node;
    });
  }, [rows, scale]);
  const right = useMemo(() => {
    let y = 4;
    return models.map(({ m, n }) => {
      const node = { m, n, y, h: n * scale };
      y += n * scale + gap;
      return node;
    });
  }, [models, scale]);

  // label positions: centred on the node, pushed down so two-line labels never collide
  const labelY = (nodes: { y: number; h: number }[]) => {
    let last = -Infinity;
    return nodes.map((n) => { const y = Math.max(n.y + n.h / 2 + 4, last + 28); last = y; return y; });
  };
  const leftLY = labelY(left), rightLY = labelY(right);

  const ribbons = useMemo(() => {
    const lOff: Record<string, number> = {}, rOff: Record<string, number> = {};
    const out: { id: string; d: string; mid: string; th: number; color: string; cell: FlowCell; from: string; to: string }[] = [];
    rows.forEach((r, i) => {
      models.forEach(({ m }) => {
        const c = r.routes[m];
        if (!c || !c.n) return;
        const L = left[i], R = right.find((x) => x.m === m)!;
        const th = c.n * scale;
        const y0 = L.y + (lOff[r.value] ?? 0), y1 = R.y + (rOff[m] ?? 0);
        lOff[r.value] = (lOff[r.value] ?? 0) + th;
        rOff[m] = (rOff[m] ?? 0) + th;
        const x0 = lx + nodeW, x1 = rx, cx = (x0 + x1) / 2;
        const d = `M${x0},${y0} C${cx},${y0} ${cx},${y1} ${x1},${y1} L${x1},${y1 + th} C${cx},${y1 + th} ${cx},${y0 + th} ${x0},${y0 + th} Z`;
        const mid = `M${x0},${y0 + th / 2} C${cx},${y0 + th / 2} ${cx},${y1 + th / 2} ${x1},${y1 + th / 2}`;
        out.push({ id: `${r.value}->${m}`, d, mid, th, color: modelColor(m), cell: c, from: r.value, to: m });
      });
    });
    return out;
  }, [rows, models, left, right, scale, lx, rx, modelColor]);

  return (
    <div ref={ref}>
      {w > 0 && total > 0 && (
        <svg className="chart" width={w} height={h} role="img" aria-label={`Router decisions by ${sliceKey}`}>
          {ribbons.map((rb, i) => (
            <g key={rb.id} onMouseMove={(e) => tip.show(e, <>
              <div className="tt-title">{rb.from} → {rb.to}</div>
              <div className="tt-row">calls <b>{rb.cell.n}</b></div>
              <div className="tt-row">share of {rb.from} <b>{pct(rb.cell.n / (left.find((l) => l.value === rb.from)?.n || 1), 0)}</b></div>
              <div className="tt-row">pass rate on this route <b>{rb.cell.assessed ? pct(rb.cell.passes / rb.cell.assessed, 0) : "—"}</b></div>
            </>)} onMouseLeave={tip.hide}>
              <path d={rb.d} fill={rb.color} opacity={0.28} style={{ animation: `fade .6s var(--ease) ${0.1 + i * 0.04}s both` }} />
              {!reduce && Array.from({ length: Math.min(3, Math.max(1, Math.round(rb.cell.n / 4))) }, (_, k) => (
                <circle key={k} r={Math.max(1.6, Math.min(3.2, rb.th / 5))} fill={rb.color}>
                  <animateMotion dur={`${2.6 + (i % 3) * 0.4}s`} begin={`${k * 0.9 + i * 0.15}s`} repeatCount="indefinite" path={rb.mid} />
                </circle>
              ))}
            </g>
          ))}
          {left.map((l, i) => (
            <g key={l.value}>
              <rect x={lx} y={l.y} width={nodeW} height={Math.max(2, l.h)} fill="var(--ink)" />
              <text x={lx - 8} y={leftLY[i]} textAnchor="end" className="lbl">{l.value}</text>
              <text x={lx - 8} y={leftLY[i] + 13} textAnchor="end">{l.n} calls</text>
            </g>
          ))}
          {right.map((r, i) => (
            <g key={r.m}>
              <rect x={rx} y={r.y} width={nodeW} height={Math.max(2, r.h)} fill={modelColor(r.m)} />
              {rightLY[i] - 4 > r.y + r.h && <line x1={rx + nodeW} x2={rx + nodeW + 6} y1={r.y + r.h / 2} y2={rightLY[i] - 4} stroke="var(--grat-2)" />}
              <text x={rx + nodeW + 8} y={rightLY[i]} className="lbl">{r.m.length > 34 ? r.m.slice(0, 33) + "…" : r.m}</text>
              <text x={rx + nodeW + 8} y={rightLY[i] + 13}>{pct(r.n / total, 0)} of calls</text>
            </g>
          ))}
        </svg>
      )}
      {tip.node}
    </div>
  );
}

export function RouteTable({ routes, color }: {
  routes: { model: string; n: number; share: number; pass_rate: Interval; cost_usd: number | null; cost_per_call_usd: number | null; latency_p50_ms: number | null; providers: string[] }[];
  color: (m: string) => string;
}) {
  return (
    <table className="t">
      <thead><tr><th>Resolved model</th><th className="r">Calls</th><th>Share</th><th className="r">Pass rate [95% CI]</th><th className="r">Cost / call</th><th className="r">p50</th><th>Served by</th></tr></thead>
      <tbody>{routes.map((r) => (
        <tr key={r.model}>
          <td><span className="row" style={{ gap: 6 }}><span className="swatch" style={{ background: color(r.model) }} /><span className="mono">{r.model}</span></span></td>
          <td className="r">{r.n}</td>
          <td style={{ minWidth: 120 }}><div className="progress"><i className="bar-grow" style={{ width: `${r.share * 100}%`, background: color(r.model) }} /></div></td>
          <td className="r">{pct(r.pass_rate.estimate, 0)} <span className="faint">[{pct(r.pass_rate.low, 0)}, {pct(r.pass_rate.high, 0)}]</span></td>
          <td className="r">{usd(r.cost_per_call_usd)}</td>
          <td className="r">{r.latency_p50_ms == null ? "—" : `${Math.round(r.latency_p50_ms)} ms`}</td>
          <td className="muted">{r.providers.join(", ") || "—"}</td>
        </tr>
      ))}</tbody>
    </table>
  );
}
