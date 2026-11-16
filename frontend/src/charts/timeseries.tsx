import { ReactNode } from "react";
import { useTooltip } from "../components/ui";
import { useWidth } from "./charts";

/* Time-series charts for monitoring: one y-axis each, a hover band per bucket. */

export interface Series { key: string; label: string; color: string; values: (number | null)[]; dashed?: boolean }

const ticksFor = (max: number, n = 4): number[] => {
  if (max <= 0) return [0];
  const raw = max / n;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw;
  const out: number[] = [];
  for (let v = 0; v <= max + step * 0.001; v += step) out.push(+v.toFixed(10));
  return out;
};

const clock = (ts: number, spanS: number) => {
  const d = new Date(ts * 1000);
  return spanS > 2 * 86400 ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
};

function Frame({ times, bucket, max, fmt, h, children, tooltip, legend, label }: {
  times: number[]; bucket: number; max: number; fmt: (v: number) => string; h: number; label: string;
  children: (x: (i: number) => number, y: (v: number) => number, bw: number) => ReactNode;
  tooltip: (i: number) => ReactNode; legend?: ReactNode;
}) {
  const [ref, w] = useWidth<HTMLDivElement>();
  const tip = useTooltip();
  const left = 52, bottom = 22, top = 8;
  const pw = Math.max(40, w - left - 8);
  const n = Math.max(1, times.length);
  const bw = pw / n;
  const x = (i: number) => left + i * bw;
  const ticks = ticksFor(max).filter((t) => t <= max * 1.0001);
  const top1 = Math.max(max, ticks[ticks.length - 1] ?? 1);
  const y = (v: number) => top + (h - top - bottom) * (1 - v / (top1 || 1));
  const span = times.length > 1 ? times[times.length - 1] - times[0] : bucket;
  const labelEvery = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(pw / 70))));
  return (
    <div ref={ref}>
      {w > 0 && (
        <svg className="chart" width={w} height={h} role="img" aria-label={label}>
          <g className="grid">{ticks.map((t) => <g key={t}><line x1={left} x2={w - 8} y1={y(t)} y2={y(t)} /><text x={left - 6} y={y(t) + 3} textAnchor="end">{fmt(t)}</text></g>)}</g>
          {children(x, y, bw)}
          <g className="grid">{times.map((t, i) => i % labelEvery === 0 && <text key={t} x={x(i) + bw / 2} y={h - 6} textAnchor="middle">{clock(t, span)}</text>)}</g>
          {times.map((t, i) => <rect key={t} x={x(i)} y={top} width={bw} height={h - top - bottom} fill="transparent" onMouseMove={(e) => tip.show(e, <><div className="tt-title">{new Date(t * 1000).toLocaleString()}</div>{tooltip(i)}</>)} onMouseLeave={tip.hide} />)}
        </svg>
      )}
      {legend && <div className="legend" style={{ marginTop: 6 }}>{legend}</div>}
      {tip.node}
    </div>
  );
}

/** Stacked bars (e.g. ok vs errored traces) per time bucket. */
export function StackedBars({ times, bucket, stacks, fmt = (v) => String(Math.round(v)), height = 170, label, floor = 1 }: {
  times: number[]; bucket: number; stacks: Series[]; fmt?: (v: number) => string; height?: number; label: string;
  /** Smallest y-axis maximum (1 for counts; use a tiny value for currency). */
  floor?: number;
}) {
  const totals = times.map((_, i) => stacks.reduce((s, k) => s + (k.values[i] ?? 0), 0));
  const max = Math.max(floor, ...totals);
  return (
    <Frame times={times} bucket={bucket} max={max} fmt={fmt} h={height} label={label}
      tooltip={(i) => <>{stacks.map((s) => <div key={s.key} className="tt-row"><i className="swatch" style={{ background: s.color }} />{s.label} <b>{fmt(s.values[i] ?? 0)}</b></div>)}</>}
      legend={stacks.length > 1 && stacks.map((s) => <span key={s.key}><i className="swatch" style={{ background: s.color }} />{s.label}</span>)}>
      {(x, y, bw) => times.map((t, i) => {
        let base = 0;
        const gap = bw > 6 ? 2 : 0.5;
        return <g key={t}>{stacks.map((s) => {
          const v = s.values[i] ?? 0;
          if (v <= 0) return null;
          const y0 = y(base), y1 = y(base + v);
          base += v;
          return <rect key={s.key} className="bar-grow" x={x(i) + gap / 2} width={Math.max(1, bw - gap)} y={y1} height={Math.max(1, y0 - y1 - (base > v ? 2 : 0))} rx={Math.min(3, bw / 4)} fill={s.color} />;
        })}</g>;
      })}
    </Frame>
  );
}

/** Lines sharing one unit (e.g. p50 and p95 latency). Gaps where a bucket has no data. */
export function Lines({ times, bucket, series, fmt, height = 170, label }: {
  times: number[]; bucket: number; series: Series[]; fmt: (v: number) => string; height?: number; label: string;
}) {
  const max = Math.max(1e-9, ...series.flatMap((s) => s.values.filter((v): v is number => v != null)));
  return (
    <Frame times={times} bucket={bucket} max={max * 1.05} fmt={fmt} h={height} label={label}
      tooltip={(i) => <>{series.map((s) => <div key={s.key} className="tt-row"><i className="swatch" style={{ background: s.color }} />{s.label} <b>{s.values[i] == null ? "—" : fmt(s.values[i]!)}</b></div>)}</>}
      legend={series.length > 1 && series.map((s) => <span key={s.key}><svg width="16" height="10"><line x1="0" x2="16" y1="5" y2="5" stroke={s.color} strokeWidth="2" strokeDasharray={s.dashed ? "3 3" : undefined} /></svg>{s.label}</span>)}>
      {(x, y, bw) => series.map((s) => {
        const segs: string[] = [];
        let cur = "";
        s.values.forEach((v, i) => {
          if (v == null) { if (cur) segs.push(cur); cur = ""; return; }
          cur += `${cur ? "L" : "M"}${(x(i) + bw / 2).toFixed(1)},${y(v).toFixed(1)}`;
        });
        if (cur) segs.push(cur);
        return <g key={s.key}>
          {segs.map((d, j) => <path key={j} d={d} fill="none" stroke={s.color} strokeWidth={2} strokeDasharray={s.dashed ? "4 3" : undefined} strokeLinejoin="round" strokeLinecap="round" />)}
          {s.values.map((v, i) => v != null && (i === 0 || s.values[i - 1] == null) && (i === s.values.length - 1 || s.values[i + 1] == null) && <circle key={i} cx={x(i) + bw / 2} cy={y(v)} r={3} fill={s.color} />)}
        </g>;
      })}
    </Frame>
  );
}

/** Horizontal share bar for one row of a breakdown table. */
export function ShareBar({ value, max, color = "var(--s1)" }: { value: number; max: number; color?: string }) {
  return <div style={{ position: "relative", height: 6, background: "var(--plate-3)", borderRadius: 2, minWidth: 60 }}>
    <div className="bar-grow" style={{ position: "absolute", inset: 0, width: `${max > 0 ? (value / max) * 100 : 0}%`, background: color, borderRadius: 2 }} />
  </div>;
}
