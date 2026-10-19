import { ReactNode, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTooltip } from "../components/ui";
import { ms, pct, usd } from "../format";
import type { Interval } from "../types";

/* Width-aware container: charts are drawn in real pixels (crisp text, no viewBox scaling of fonts). */
export function useWidth<T extends HTMLElement>(): [React.RefObject<T>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver((es) => setW(Math.floor(es[0].contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

const niceTicks = (max: number, n = 4): number[] => {
  if (max <= 0) return [0];
  const raw = max / n;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw;
  const out = [];
  for (let v = 0; v <= max + step * 0.001; v += step) out.push(+v.toFixed(10));
  return out;
};

/* ---------------------------------------------------------------- CI bars */
export interface CIRow { key: string; label: string; color: string; iv: Interval | null; sub?: ReactNode; tag?: ReactNode }

export function CIBars({ rows, threshold, labelWidth = 230 }: { rows: CIRow[]; threshold?: number | null; labelWidth?: number }) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const tip = useTooltip();
  const barH = 16, gap = 18, top = 6;
  const lw = Math.min(labelWidth, Math.max(120, w * 0.34));
  const plotW = Math.max(60, w - lw - 70);
  const h = rows.length * (barH + gap) + 28;
  const x = (v: number) => lw + v * plotW;
  return (
    <div ref={ref}>
      {w > 0 && (
        <svg className="chart" width={w} height={h} role="img" aria-label="Pass rate with 95% confidence intervals">
          <g className="grid">{[0, 0.25, 0.5, 0.75, 1].map((t) => (
            <g key={t}><line x1={x(t)} x2={x(t)} y1={0} y2={h - 22} /><text x={x(t)} y={h - 6} textAnchor="middle">{t * 100}%</text></g>
          ))}</g>
          {threshold != null && (
            <g><line x1={x(threshold)} x2={x(threshold)} y1={0} y2={h - 22} stroke="var(--warn)" strokeDasharray="3 3" />
              <text x={x(threshold) + 4} y={10} style={{ fill: "var(--warn)" }}>threshold {pct(threshold, 0)}</text></g>
          )}
          {rows.map((r, i) => {
            const y = top + i * (barH + gap);
            const est = r.iv?.estimate;
            return (
              <g key={r.key} onMouseMove={(e) => tip.show(e, <>
                <div className="tt-title">{r.label}</div>
                <div className="tt-row">estimate <b>{pct(est)}</b></div>
                <div className="tt-row">95% CI <b>{pct(r.iv?.low)} – {pct(r.iv?.high)}</b></div>
                <div className="tt-row">n assessed <b>{r.iv?.n ?? 0}</b></div>
                <div className="tt-row">method <b>{r.iv?.method}</b></div>
              </>)} onMouseLeave={tip.hide}>
                <rect x={0} y={y - 6} width={w} height={barH + 12} fill="transparent" />
                <text x={lw - 10} y={y + barH - 4} textAnchor="end" className="lbl">{r.label.length > 34 ? r.label.slice(0, 33) + "…" : r.label}</text>
                {est == null ? (
                  <text x={lw + 6} y={y + barH - 4}>no assessed trials</text>
                ) : (
                  <>
                    <rect className="bar-grow" style={{ animationDelay: `${i * 60}ms` }} x={lw} y={y} width={Math.max(2, est * plotW)} height={barH} rx={4} fill={r.color} />
                    {r.iv?.low != null && r.iv?.high != null && (
                      <g className="fade-in" style={{ animationDelay: `${300 + i * 60}ms` }}>
                        <line className="ci" x1={x(r.iv.low)} x2={x(r.iv.high)} y1={y + barH / 2} y2={y + barH / 2} />
                        <line className="ci" x1={x(r.iv.low)} x2={x(r.iv.low)} y1={y + 3} y2={y + barH - 3} />
                        <line className="ci" x1={x(r.iv.high)} x2={x(r.iv.high)} y1={y + 3} y2={y + barH - 3} />
                      </g>
                    )}
                    <text x={lw + plotW + 8} y={y + barH - 4} className="val">{pct(est)}</text>
                  </>
                )}
              </g>
            );
          })}
        </svg>
      )}
      {tip.node}
    </div>
  );
}

/* ---------------------------------------------------------------- Pareto scatter */
export interface ScatterPoint { key: string; label: string; color: string; x: number | null; y: number | null; yLow?: number | null; yHigh?: number | null }

export function Pareto({ points, frontier, xLabel, xFormat, logX }: {
  points: ScatterPoint[]; frontier: string[]; xLabel: string; xFormat: (v: number) => string; logX?: boolean;
}) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const tip = useTooltip();
  const pts = points.filter((p) => p.x != null && p.y != null) as (ScatterPoint & { x: number; y: number })[];
  const h = 280, m = { l: 48, r: 18, t: 14, b: 40 };
  const pw = Math.max(50, w - m.l - m.r), ph = h - m.t - m.b;
  const xs = pts.map((p) => p.x);
  const useLog = !!logX && xs.length > 0 && Math.min(...xs) > 0;
  const minX = xs.length ? Math.min(...xs) : 0, maxX = xs.length ? Math.max(...xs) : 1;
  const tx = (v: number) => {
    if (useLog) {
      const a = Math.log10(minX / 1.6), b = Math.log10(maxX * 1.6);
      return m.l + ((Math.log10(v) - a) / (b - a || 1)) * pw;
    }
    const hi = maxX * 1.12 || 1;
    return m.l + (v / hi) * pw;
  };
  const ty = (v: number) => m.t + (1 - v) * ph;
  const xTicks = useLog
    ? (() => { const out: number[] = []; for (let e = Math.floor(Math.log10(minX / 1.6)); e <= Math.ceil(Math.log10(maxX * 1.6)); e++) out.push(Math.pow(10, e)); return out.filter((t) => tx(t) >= m.l - 1 && tx(t) <= m.l + pw + 1); })()
    : niceTicks(maxX * 1.12 || 1);
  const front = frontier.map((k) => pts.find((p) => p.key === k)).filter(Boolean) as typeof pts;
  const path = front.map((p, i) => `${i ? "L" : "M"}${tx(p.x).toFixed(1)},${ty(p.y).toFixed(1)}`).join(" ");
  return (
    <div ref={ref}>
      {w > 0 && (pts.length === 0 ? <div className="empty">Not enough data: needs known cost/latency and assessed trials.</div> : maxX === 0 ? <div className="empty">Every arm has a known cost of $0 (free or fake targets), so there is no cost trade-off to plot. Switch to latency.</div> : (
        <svg className="chart" width={w} height={h} role="img" aria-label={`Quality vs ${xLabel}`}>
          <g className="grid">
            {[0, 0.25, 0.5, 0.75, 1].map((t) => <g key={t}><line x1={m.l} x2={m.l + pw} y1={ty(t)} y2={ty(t)} /><text x={m.l - 8} y={ty(t) + 4} textAnchor="end">{t * 100}%</text></g>)}
            {xTicks.map((t) => <g key={t}><line x1={tx(t)} x2={tx(t)} y1={m.t} y2={m.t + ph} /><text x={tx(t)} y={h - 22} textAnchor="middle">{xFormat(t)}</text></g>)}
          </g>
          <text x={m.l + pw / 2} y={h - 4} textAnchor="middle">{xLabel}{useLog ? " (log)" : ""} →</text>
          <text x={12} y={m.t + ph / 2} transform={`rotate(-90 12 ${m.t + ph / 2})`} textAnchor="middle">pass rate →</text>
          {path && <path d={path} className="draw-line" fill="none" stroke="var(--ink-2)" strokeWidth={1.5} strokeDasharray="4 4" opacity={0.7} />}
          {pts.map((p, i) => (
            <g key={p.key} className="pop-in" style={{ animationDelay: `${150 + i * 70}ms` }}
              onMouseMove={(e) => tip.show(e, <>
                <div className="tt-title">{p.label}</div>
                <div className="tt-row">pass rate <b>{pct(p.y)}</b></div>
                {p.yLow != null && <div className="tt-row">95% CI <b>{pct(p.yLow)} – {pct(p.yHigh)}</b></div>}
                <div className="tt-row">{xLabel} <b>{xFormat(p.x)}</b></div>
                <div className="tt-row">Pareto-optimal <b>{frontier.includes(p.key) ? "yes" : "no"}</b></div>
              </>)} onMouseLeave={tip.hide}>
              {p.yLow != null && p.yHigh != null && <line x1={tx(p.x)} x2={tx(p.x)} y1={ty(p.yLow)} y2={ty(p.yHigh)} stroke={p.color} strokeWidth={2} opacity={0.45} />}
              <circle cx={tx(p.x)} cy={ty(p.y)} r={14} fill="transparent" />
              <circle cx={tx(p.x)} cy={ty(p.y)} r={frontier.includes(p.key) ? 6.5 : 5} fill={p.color} stroke="var(--plate)" strokeWidth={2} />
              {frontier.includes(p.key) && <text x={tx(p.x) + 10} y={ty(p.y) - 8} className="lbl">{p.label.length > 26 ? p.label.slice(0, 25) + "…" : p.label}</text>}
            </g>
          ))}
        </svg>
      ))}
      {tip.node}
    </div>
  );
}

/* ---------------------------------------------------------------- latency strip / box */
export interface LatRow { key: string; label: string; color: string; values: number[]; p50: number | null; p95: number | null; ttft50?: number | null }

export function LatencyStrip({ rows, labelWidth = 230 }: { rows: LatRow[]; labelWidth?: number }) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const tip = useTooltip();
  const all = rows.flatMap((r) => r.values);
  if (!all.length) return <div ref={ref} className="empty">No latency measurements (imported outputs or nothing completed yet).</div>;
  const lw = Math.min(labelWidth, Math.max(120, w * 0.34));
  const max = Math.max(...all) * 1.05;
  const pw = Math.max(60, w - lw - 20);
  const laneH = 30, h = rows.length * laneH + 30;
  const x = (v: number) => lw + (v / max) * pw;
  const ticks = niceTicks(max, 5).filter((t) => t <= max);
  return (
    <div ref={ref}>
      {w > 0 && (
        <svg className="chart" width={w} height={h} role="img" aria-label="Latency distribution per arm">
          <g className="grid">{ticks.map((t) => <g key={t}><line x1={x(t)} x2={x(t)} y1={0} y2={h - 22} /><text x={x(t)} y={h - 6} textAnchor="middle">{ms(t)}</text></g>)}</g>
          {rows.map((r, i) => {
            const cy = i * laneH + laneH / 2 + 2;
            return (
              <g key={r.key} onMouseMove={(e) => tip.show(e, <>
                <div className="tt-title">{r.label}</div>
                <div className="tt-row">p50 <b>{ms(r.p50)}</b></div><div className="tt-row">p95 <b>{ms(r.p95)}</b></div>
                {r.ttft50 != null && <div className="tt-row">TTFT p50 <b>{ms(r.ttft50)}</b></div>}
                <div className="tt-row">samples shown <b>{r.values.length}</b></div>
              </>)} onMouseLeave={tip.hide}>
                <rect x={0} y={cy - laneH / 2} width={w} height={laneH} fill="transparent" />
                <text x={lw - 10} y={cy + 4} textAnchor="end" className="lbl">{r.label.length > 34 ? r.label.slice(0, 33) + "…" : r.label}</text>
                {r.values.map((v, j) => {
                  const jitter = ((j * 7919) % 13) / 13 - 0.5;
                  return <circle key={j} cx={x(v)} cy={cy + jitter * 14} r={2.4} fill={r.color} opacity={0.55} className="pop-in" style={{ animationDelay: `${Math.min(j, 60) * 8 + i * 40}ms` }} />;
                })}
                {r.p50 != null && <line x1={x(r.p50)} x2={x(r.p50)} y1={cy - 11} y2={cy + 11} stroke="var(--ink)" strokeWidth={2} />}
                {r.p95 != null && <line x1={x(r.p95)} x2={x(r.p95)} y1={cy - 8} y2={cy + 8} stroke="var(--ink)" strokeWidth={1.2} strokeDasharray="2 2" />}
              </g>
            );
          })}
        </svg>
      )}
      <div className="legend" style={{ marginTop: 6 }}><span><svg width="14" height="12"><line x1="7" x2="7" y1="0" y2="12" stroke="var(--ink)" strokeWidth="2" /></svg>p50</span><span><svg width="14" height="12"><line x1="7" x2="7" y1="0" y2="12" stroke="var(--ink)" strokeDasharray="2 2" /></svg>p95</span><span className="muted">dots: individual trials (subsampled)</span></div>
      {tip.node}
    </div>
  );
}

/* ---------------------------------------------------------------- heatmap (canvas) */
// Sequential single-hue blue ramp; the near-zero end recedes toward the surface in each theme.
const RAMP = ["#cde2fb", "#9ec5f4", "#6da7ec", "#3987e5", "#256abf", "#184f95"];
function isDarkTheme(): boolean {
  const t = document.documentElement.dataset.theme;
  return t === "dark" || (t !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
}
const heatRamp = () => (isDarkTheme() ? [...RAMP].reverse() : RAMP);

export function Heatmap({ rows, cols, values, onCell, rowMeta }: {
  rows: string[]; cols: { key: string; label: string; color: string }[]; values: (number | null)[][];
  onCell?: (row: number, col: number) => void; rowMeta?: (i: number) => ReactNode;
}) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const canvas = useRef<HTMLCanvasElement>(null);
  const tip = useTooltip();
  const labelW = 120, headH = 8;
  const cellW = cols.length ? Math.max(18, Math.min(120, Math.floor((w - labelW) / cols.length))) : 0;
  const cellH = rows.length > 300 ? 4 : rows.length > 120 ? 7 : 14;
  const h = rows.length * cellH + headH;
  useEffect(() => {
    const c = canvas.current;
    if (!c || !w) return;
    const dpr = window.devicePixelRatio || 1;
    c.width = Math.floor(w * dpr); c.height = Math.floor(h * dpr);
    const ctx = c.getContext("2d")!;
    ctx.scale(dpr, dpr);
    const css = getComputedStyle(document.documentElement);
    const muted = css.getPropertyValue("--muted").trim();
    const empty = css.getPropertyValue("--plate-3").trim();
    const ink2 = css.getPropertyValue("--ink-2").trim();
    const palette = heatRamp();
    ctx.clearRect(0, 0, w, h);
    ctx.font = "11px " + css.getPropertyValue("--mono");
    ctx.textBaseline = "middle";
    for (let i = 0; i < rows.length; i++) {
      const y = headH + i * cellH;
      if (cellH >= 12) { ctx.fillStyle = muted; ctx.fillText(rows[i].slice(0, 16), 0, y + cellH / 2); }
      for (let j = 0; j < cols.length; j++) {
        const v = values[i][j];
        ctx.fillStyle = v == null ? empty : palette[Math.min(palette.length - 1, Math.floor(v * (palette.length - 1) + 0.0001))];
        ctx.fillRect(labelW + j * cellW + 1, y + 1, cellW - 2, cellH - (cellH > 6 ? 2 : 1));
      }
    }
    if (!rows.length) { ctx.fillStyle = ink2; ctx.fillText("no data", labelW, 20); }
  }, [w, h, rows, cols, values, cellH, cellW]);

  const locate = (e: React.MouseEvent) => {
    const r = (e.target as HTMLCanvasElement).getBoundingClientRect();
    const x = e.clientX - r.left, y = e.clientY - r.top - headH;
    const j = Math.floor((x - labelW) / cellW), i = Math.floor(y / cellH);
    return i >= 0 && i < rows.length && j >= 0 && j < cols.length ? [i, j] : null;
  };
  return (
    <div ref={ref}>
      <div style={{ display: "flex", paddingLeft: labelW, marginBottom: 4 }}>
        {cols.map((c) => <div key={c.key} style={{ width: cellW, display: "flex", justifyContent: "center" }} title={c.label}><span className="swatch" style={{ background: c.color }} /></div>)}
      </div>
      <div style={{ maxHeight: 520, overflowY: "auto" }}>
        <canvas ref={canvas} className="fade-in" style={{ width: w, height: h, display: "block", cursor: onCell ? "pointer" : "default" }}
          onMouseMove={(e) => {
            const hit = locate(e);
            if (!hit) return tip.hide();
            const [i, j] = hit;
            const v = values[i][j];
            tip.show(e, <><div className="tt-title">{rows[i]}</div><div className="tt-row">{cols[j].label}</div>
              <div className="tt-row">pass fraction <b>{v == null ? "not assessed" : pct(v, 0)}</b></div>{rowMeta?.(i)}</>);
          }}
          onMouseLeave={tip.hide}
          onClick={(e) => { const hit = locate(e); if (hit && onCell) onCell(hit[0], hit[1]); }} />
      </div>
      <div className="legend" style={{ marginTop: 8 }}>
        <span>fail</span>
        <span style={{ display: "inline-flex" }}>{heatRamp().map((c) => <i key={c} style={{ width: 18, height: 10, background: c }} />)}</span>
        <span>pass</span>
        <span><i className="swatch" style={{ background: "var(--plate-3)" }} /> not assessed</span>
        <span className="muted">columns follow arm colors · values average repetitions</span>
      </div>
      {tip.node}
    </div>
  );
}

/* ---------------------------------------------------------------- live trial grid (canvas) */
export interface GridData { arms: string[]; ids: string[]; arm: number[]; status: number[]; outcome: number[] }
const STATUS_IDX = { pending: 0, running: 1, completed: 2, error: 3, cancelled: 4, not_applicable: 5, interrupted: 6 };
const OUTCOME_IDX = { not_assessed: 0, pass: 1, fail: 2, inconclusive: 3, not_applicable: 4 };
export const GRID_CODES = { STATUS_IDX, OUTCOME_IDX };

export function TrialGrid({ data, armLabels, armColors, onPick }: {
  data: GridData; armLabels: Record<string, string>; armColors: Record<string, string>; onPick?: (trialId: string) => void;
}) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const canvas = useRef<HTMLCanvasElement>(null);
  const flash = useRef<Map<number, number>>(new Map());
  const prev = useRef<{ status: number[]; outcome: number[] } | null>(null);
  const tip = useTooltip();
  const lanes = useMemo(() => {
    const out: number[][] = data.arms.map(() => []);
    data.arm.forEach((a, i) => out[a]?.push(i));
    return out;
  }, [data.arms, data.arm]);
  const labelW = Math.min(220, Math.max(120, w * 0.25));
  const maxLane = Math.max(1, ...lanes.map((l) => l.length));
  const avail = Math.max(40, w - labelW - 8);
  const cell = Math.max(6, Math.min(16, Math.floor(Math.sqrt((avail * 26) / maxLane))));
  const perRow = Math.max(1, Math.floor(avail / cell));
  const laneRows = lanes.map((l) => Math.max(1, Math.ceil(l.length / perRow)));
  const laneY: number[] = [];
  let acc = 0;
  laneRows.forEach((r) => { laneY.push(acc); acc += r * cell + 14; });
  const h = acc;

  // register state transitions so they flash
  useEffect(() => {
    const p = prev.current;
    if (p) {
      const now = performance.now();
      for (let i = 0; i < data.status.length; i++) if (p.status[i] !== data.status[i] || p.outcome[i] !== data.outcome[i]) flash.current.set(i, now);
    }
    prev.current = { status: [...data.status], outcome: [...data.outcome] };
  }, [data.status, data.outcome]);

  useEffect(() => {
    const c = canvas.current;
    if (!c || !w) return;
    const dpr = window.devicePixelRatio || 1;
    c.width = Math.floor(w * dpr); c.height = Math.floor(h * dpr);
    const ctx = c.getContext("2d")!;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const css = getComputedStyle(document.documentElement);
    const col = {
      pending: css.getPropertyValue("--plate-3").trim(), running: css.getPropertyValue("--signal").trim(),
      pass: css.getPropertyValue("--good").trim(), fail: css.getPropertyValue("--bad").trim(),
      inconc: css.getPropertyValue("--warn").trim(), error: css.getPropertyValue("--serious").trim(),
      done: css.getPropertyValue("--signal").trim(), na: css.getPropertyValue("--grat").trim(),
      ink2: css.getPropertyValue("--ink-2").trim(), cancelled: css.getPropertyValue("--faint").trim(),
    };
    let raf = 0;
    const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const render = (t: number) => {
      ctx.clearRect(0, 0, w, h);
      ctx.font = "12px " + css.getPropertyValue("--sans");
      ctx.textBaseline = "middle";
      let animating = false;
      lanes.forEach((lane, a) => {
        const y0 = laneY[a];
        ctx.fillStyle = armColors[data.arms[a]] ?? col.ink2;
        ctx.fillRect(0, y0 + 2, 3, laneRows[a] * cell - 4);
        ctx.fillStyle = col.ink2;
        const lab = armLabels[data.arms[a]] ?? data.arms[a];
        ctx.fillText(lab.length > 28 ? lab.slice(0, 27) + "…" : lab, 10, y0 + cell / 2);
        lane.forEach((ti, j) => {
          const x = labelW + (j % perRow) * cell, y = y0 + Math.floor(j / perRow) * cell;
          const s = data.status[ti], o = data.outcome[ti];
          let fill = col.pending;
          if (s === 1) fill = col.running;
          else if (s === 2) fill = o === 1 ? col.pass : o === 2 ? col.fail : o === 3 ? col.inconc : col.done;
          else if (s === 3) fill = col.error;
          else if (s === 4) fill = col.cancelled;
          else if (s === 5) fill = col.na;
          let scale = 1, alpha = 1;
          const f = flash.current.get(ti);
          if (f !== undefined && !reduce) {
            const k = (t - f) / 450;
            if (k < 1) { scale = 1 + 0.5 * (1 - k) * (1 - k); animating = true; } else flash.current.delete(ti);
          }
          if (s === 1 && !reduce) { alpha = 0.55 + 0.45 * Math.abs(Math.sin(t / 260 + ti)); animating = true; }
          const sz = (cell - 2) * scale, off = (cell - 2 - sz) / 2;
          ctx.globalAlpha = alpha;
          ctx.fillStyle = fill;
          const r = Math.min(3, sz / 3);
          ctx.beginPath();
          ctx.roundRect(x + 1 + off, y + 1 + off, sz, sz, r);
          ctx.fill();
          ctx.globalAlpha = 1;
        });
      });
      if (animating) raf = requestAnimationFrame(render);
    };
    raf = requestAnimationFrame(render);
    return () => cancelAnimationFrame(raf);
  }, [w, h, data, lanes, laneY, laneRows, cell, perRow, labelW, armLabels, armColors]);

  const hit = (e: React.MouseEvent) => {
    const r = (e.target as HTMLCanvasElement).getBoundingClientRect();
    const x = e.clientX - r.left, y = e.clientY - r.top;
    for (let a = 0; a < lanes.length; a++) {
      const y0 = laneY[a];
      if (y >= y0 && y < y0 + laneRows[a] * cell) {
        const j = Math.floor((y - y0) / cell) * perRow + Math.floor((x - labelW) / cell);
        if (x >= labelW && j >= 0 && j < lanes[a].length) return lanes[a][j];
      }
    }
    return null;
  };
  const SN = ["pending", "running", "completed", "error", "cancelled", "not applicable", "interrupted"];
  const ON = ["not assessed", "pass", "fail", "inconclusive", "not applicable"];
  return (
    <div ref={ref}>
      <canvas ref={canvas} style={{ width: w, height: h, display: "block", cursor: onPick ? "pointer" : "default" }}
        onMouseMove={(e) => {
          const i = hit(e);
          if (i == null) return tip.hide();
          tip.show(e, <><div className="tt-title">Trial #{i + 1}</div><div className="tt-row">status <b>{SN[data.status[i]]}</b></div>
            <div className="tt-row">outcome <b>{ON[data.outcome[i]]}</b></div><div className="tt-row muted">{armLabels[data.arms[data.arm[i]]]}</div></>);
        }}
        onMouseLeave={tip.hide}
        onClick={(e) => { const i = hit(e); if (i != null && onPick) onPick(data.ids[i]); }} />
      <div className="legend" style={{ marginTop: 10 }}>
        {[["pending", "var(--plate-3)"], ["running", "var(--signal)"], ["pass", "var(--good)"], ["fail", "var(--bad)"], ["inconclusive", "var(--warn)"],
          ["awaiting grade", "var(--signal)"], ["infra error", "var(--serious)"], ["cancelled", "var(--faint)"]].map(([l, c]) => (
          <span key={l}><i className="swatch" style={{ background: c }} />{l}</span>
        ))}
      </div>
      {tip.node}
    </div>
  );
}

/* ---------------------------------------------------------------- sparkline & budget gauge */
export function Sparkline({ values, width = 110, height = 32, color = "var(--signal)" }: { values: number[]; width?: number; height?: number; color?: string }) {
  if (values.length < 2) return null;
  const max = Math.max(...values, 1e-9), min = Math.min(...values, 0);
  const pts = values.map((v, i) => [(i / (values.length - 1)) * width, height - 3 - ((v - min) / (max - min || 1)) * (height - 6)]);
  const d = pts.map((p, i) => `${i ? "L" : "M"}${p[0].toFixed(1)},${p[1].toFixed(1)}`).join(" ");
  return (
    <svg width={width} height={height} className="chart" aria-hidden="true">
      <path d={`${d} L${width},${height} L0,${height}Z`} fill={color} opacity={0.1} />
      <path d={d} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" className="draw-line" />
      <circle cx={pts[pts.length - 1][0]} cy={pts[pts.length - 1][1]} r={3} fill={color} />
    </svg>
  );
}

export function BudgetGauge({ spent, reserved, cap, unknown }: { spent: number; reserved: number; cap: number | null; unknown: number }) {
  const total = cap ?? Math.max(spent + reserved, 1e-9);
  const sp = Math.min(1, spent / total), rs = Math.min(1 - sp, reserved / total);
  return (
    <div className="col" style={{ gap: 6 }}>
      <div className="row"><b className="num" style={{ fontSize: 18 }}>{usd(spent)}</b><span className="muted">spent</span><span className="spacer" />
        <span className="muted num">{cap != null ? `cap ${usd(cap)}` : "no cap"}</span></div>
      <div className="progress" style={{ height: 8 }}>
        <i style={{ width: `${sp * 100}%`, background: "var(--signal)" }} />
        <i style={{ left: `${sp * 100}%`, width: `${rs * 100}%`, background: "var(--warn)", opacity: 0.6, borderRadius: 0 }} />
      </div>
      <div className="legend">
        <span><i className="swatch" style={{ background: "var(--signal)" }} />settled {usd(spent)}</span>
        <span><i className="swatch" style={{ background: "var(--warn)", opacity: 0.6 }} />reserved / exposure {usd(reserved)}</span>
        {unknown > 0 && <span className="warn">{unknown} unknown-cost attempt{unknown > 1 ? "s" : ""}</span>}
      </div>
    </div>
  );
}

/* ---------------------------------------------------------------- slice mini bars */
export function MiniBar({ iv, color }: { iv: Interval; color: string }) {
  if (iv.estimate == null) return <span className="faint">—</span>;
  return (
    <div className="row" style={{ gap: 8, minWidth: 120 }}>
      <div style={{ position: "relative", flex: 1, height: 8, background: "var(--plate-3)", borderRadius: 2 }}>
        <div className="bar-grow" style={{ position: "absolute", inset: 0, width: `${iv.estimate * 100}%`, background: color, borderRadius: 2 }} />
        {iv.low != null && iv.high != null && <div style={{ position: "absolute", top: 3, height: 2, left: `${iv.low * 100}%`, width: `${(iv.high - iv.low) * 100}%`, background: "var(--ink)", opacity: 0.6 }} />}
      </div>
      <span className="num" style={{ width: 44, textAlign: "right" }}>{pct(iv.estimate, 0)}</span>
      <span className="faint num" style={{ width: 38 }}>n={iv.n}</span>
    </div>
  );
}
