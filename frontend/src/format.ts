/** Formatting helpers. Unknown values render as an em dash, never as zero. */
export const DASH = "—";

export function pct(v: number | null | undefined, digits = 1, signed = false): string {
  if (v == null || Number.isNaN(v)) return DASH;
  const s = (v * 100).toFixed(digits);
  return signed && v > 0 ? `+${s}%` : `${s}%`;
}

export function ms(v: number | null | undefined): string {
  if (v == null) return DASH;
  if (v >= 10000) return `${(v / 1000).toFixed(1)} s`;
  if (v >= 1000) return `${(v / 1000).toFixed(2)} s`;
  return `${Math.round(v)} ms`;
}

export function usd(v: number | null | undefined, opts: { unknown?: string } = {}): string {
  if (v == null) return opts.unknown ?? "unknown";
  if (v === 0) return "$0";
  if (Math.abs(v) >= 100) return `$${v.toFixed(0)}`;
  if (Math.abs(v) >= 1) return `$${v.toFixed(2)}`;
  if (Math.abs(v) >= 0.01) return `$${v.toFixed(4)}`;
  return `$${v.toPrecision(2)}`;
}

export function perM(v: number | null | undefined): string {
  if (v == null) return DASH;
  if (v === 0) return "free";
  return v >= 10 ? `$${v.toFixed(0)}` : v >= 1 ? `$${v.toFixed(2)}` : `$${v.toFixed(3)}`;
}

export function num(v: number | null | undefined, digits = 0): string {
  if (v == null) return DASH;
  return v.toLocaleString(undefined, { maximumFractionDigits: digits, minimumFractionDigits: digits });
}

export function compact(v: number | null | undefined): string {
  if (v == null) return DASH;
  if (v >= 1e6) return `${(v / 1e6).toFixed(v >= 1e7 ? 0 : 1)}M`;
  if (v >= 1e3) return `${(v / 1e3).toFixed(v >= 1e4 ? 0 : 1)}k`;
  return String(Math.round(v));
}

export function ago(ts: number | null | undefined): string {
  if (!ts) return DASH;
  const s = Date.now() / 1000 - ts;
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

export function duration(s: number | null | undefined): string {
  if (s == null) return DASH;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
  return `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m`;
}

export function short(id: string, n = 8): string {
  const i = id.indexOf("_");
  return i >= 0 ? id.slice(i + 1, i + 1 + n) : id.slice(0, n);
}

export const SERIES = ["var(--s1)", "var(--s2)", "var(--s3)", "var(--s4)", "var(--s5)", "var(--s6)", "var(--s7)", "var(--s8)"];
export const seriesColor = (i: number) => SERIES[i % SERIES.length];

export const LABELS: Record<string, { text: string; tone: string }> = {
  fake_runtime_test: { text: "Fake target · runtime test", tone: "tape" },
  synthetic_data: { text: "synthetic data", tone: "" },
  imported_outputs: { text: "imported outputs", tone: "info" },
  model_quality_test: { text: "model quality test", tone: "info" },
  re_scored: { text: "re-scored", tone: "info" },
};

export const STATUS_TONE: Record<string, string> = {
  running: "live", queued: "info", grading: "live", analyzing: "live", completed: "good", cancelled: "",
  paused: "warn", failed: "bad", requires_reconciliation: "warn",
};

export const VERDICT: Record<string, { text: string; tone: string }> = {
  pass: { text: "PASS", tone: "good" },
  fail: { text: "FAIL", tone: "bad" },
  insufficient_evidence: { text: "INSUFFICIENT EVIDENCE", tone: "warn" },
  invalid_experiment: { text: "INVALID EXPERIMENT", tone: "bad" },
};
