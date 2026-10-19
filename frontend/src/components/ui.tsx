import { createContext, ReactNode, useCallback, useContext, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { LABELS, STATUS_TONE } from "../format";
import { linkHandler } from "../router";
import { useTween } from "../hooks";

/* ---------------------------------------------------------------- icons (inline, 24px grid, stroke) */
const P: Record<string, string> = {
  overview: "M3 13h8V3H3zM13 21h8V11h-8zM3 21h8v-6H3zM13 3v6h8V3z",
  play: "M6 4l14 8-14 8z",
  models: "M12 2 3 7l9 5 9-5zM3 12l9 5 9-5M3 17l9 5 9-5",
  data: "M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  flask: "M9 3h6M10 3v6l-5.5 9.5A2 2 0 0 0 6.2 21h11.6a2 2 0 0 0 1.7-2.5L14 9V3M7.5 15h9",
  campaign: "M3 12h4l3-8 4 16 3-8h4",
  settings: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z",
  plus: "M12 5v14M5 12h14",
  x: "M18 6 6 18M6 6l12 12",
  check: "M20 6 9 17l-5-5",
  alert: "M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z",
  stop: "M6 6h12v12H6z",
  refresh: "M21 12a9 9 0 1 1-2.6-6.4M21 3v6h-6",
  download: "M12 3v12M7 10l5 5 5-5M5 21h14",
  upload: "M12 21V9M7 14l5-5 5 5M5 3h14",
  search: "M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3",
  image: "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2zM8.5 10a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3zM21 15l-5-5L5 21",
  text: "M4 7V4h16v3M9 20h6M12 4v16",
  copy: "M9 9h11v11H9zM5 15H4V4h11v1",
  sun: "M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4",
  moon: "M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z",
  arrow: "M5 12h14M13 6l6 6-6 6",
  back: "M19 12H5M11 18l-6-6 6-6",
  trash: "M3 6h18M8 6V4h8v2M6 6l1 15h10l1-15",
  scale: "M12 3v18M5 7h14M5 7l-3 7a4 4 0 0 0 6 0zM19 7l-3 7a4 4 0 0 0 6 0z",
  bolt: "M13 2 3 14h9l-1 8 10-12h-9z",
  shield: "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
  report: "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6M8 13h8M8 17h5",
  pause: "M7 5h3v14H7zM14 5h3v14h-3z",
  info: "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16v-4M12 8h.01",
  eye: "M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
  logout: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9",
};
export function Icon({ name, size = 16, className }: { name: keyof typeof P | string; size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8}
      strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
      <path d={P[name] ?? P.info} />
    </svg>
  );
}

export function Logo() {
  // the Sword of Night and Flame inside a gilded seal: the blade is split azure | ember,
  // a crescent rises on the night side and a flame on the other
  return (
    <svg className="brand-mark" viewBox="0 0 32 32" aria-hidden="true">
      <circle cx="16" cy="16" r="15" fill="var(--plate)" stroke="var(--gild)" strokeWidth="1.2" />
      <circle cx="16" cy="16" r="12.6" fill="none" stroke="var(--gild)" strokeWidth="0.5" strokeDasharray="0.8 1.6" opacity="0.8" />
      <path d="M10.6 8.4a4.4 4.4 0 1 0 0 8.2 3.5 3.5 0 1 1 0-8.2z" fill="var(--azure)" className="mark-moon" />
      <path d="M22.4 7.6c.4 1.9 2.7 3.1 2.7 5.8a2.7 2.7 0 0 1-5.4 0c0-1.2.6-2.1 1.4-2.8.1.9.5 1.5 1 1.8-.2-1.7.3-3.4.3-4.8z" fill="var(--ember)" className="mark-flame" />
      <path d="M16 4.2 L14.8 6.6 V20.6 H16 Z" fill="var(--azure)" />
      <path d="M16 4.2 L17.2 6.6 V20.6 H16 Z" fill="var(--ember)" />
      <rect x="11.4" y="20.4" width="9.2" height="1.6" rx="0.8" fill="var(--gild)" />
      <line x1="16" y1="22.2" x2="16" y2="25.8" stroke="var(--ink)" strokeWidth="1.6" strokeLinecap="round" />
      <circle cx="16" cy="27" r="1.2" fill="var(--gild)" />
    </svg>
  );
}

/** Night ⇄ Flame switch: a sliding knob over a moon (night) and a flame (light). */
export function ThemeSwitch({ night, onToggle }: { night: boolean; onToggle: () => void }) {
  const label = night ? "Switch to Flame theme (light)" : "Switch to Night theme (dark)";
  return (
    <button className={`theme-switch ${night ? "is-night" : "is-flame"}`} onClick={onToggle} aria-label={label} title={label} role="switch" aria-checked={night}>
      <span className="ts-knob" />
      <svg className="ts-icon ts-moon" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.2 6.2 0 0 0 10.5 10.5z" /></svg>
      <svg className="ts-icon ts-flame" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2.5c.7 3.2 4.9 5.4 4.9 10.3a4.9 4.9 0 0 1-9.8 0c0-2.2 1-3.8 2.5-5 .1 1.6.8 2.8 1.9 3.3-.4-3.1.5-6.1.5-8.6z" /></svg>
    </button>
  );
}

/* ---------------------------------------------------------------- primitives */
export function A({ href, children, className }: { href: string; children: ReactNode; className?: string }) {
  return <a href={href} onClick={linkHandler} className={className}>{children}</a>;
}

export function Card({ title, sub, actions, children, flush, className, style, graticule, reading }: {
  title?: ReactNode; sub?: ReactNode; actions?: ReactNode; children?: ReactNode; flush?: boolean; className?: string; style?: React.CSSProperties;
  graticule?: boolean; reading?: ReactNode;
}) {
  return (
    <section className={`card ${className ?? ""}`} style={style}>
      {(title || actions) && (
        <div className="card-head">
          <div className="grow"><div className="card-title">{title}</div>{sub && <div className="card-sub">{sub}</div>}</div>
          {actions}
        </div>
      )}
      <div className={`card-body ${flush ? "flush" : ""} ${graticule ? "graticule" : ""}`}>
        {children}
        {reading && <div className="reading"><b>How to read</b><span>{reading}</span></div>}
      </div>
    </section>
  );
}

export function Badge({ tone = "", children, dot, mono, title }: { tone?: string; children: ReactNode; dot?: boolean; mono?: boolean; title?: string }) {
  return <span className={`badge ${tone} ${mono ? "mono" : ""}`} title={title}>{dot && <span className="dot" />}{children}</span>;
}

export function StatusBadge({ status }: { status: string }) {
  const tone = STATUS_TONE[status] ?? "";
  const live = tone === "live";
  return <Badge tone={tone} dot={live || status === "queued"}>{status.replace(/_/g, " ")}</Badge>;
}

export function Labels({ labels }: { labels: string[] }) {
  return <>{labels.map((l) => <Badge key={l} tone={LABELS[l]?.tone ?? ""}>{LABELS[l]?.text ?? l}</Badge>)}</>;
}

export function Stat({ label, value, foot, format, children }: {
  label: string; value: number | null | undefined; foot?: ReactNode; format?: (v: number | null) => string; children?: ReactNode;
}) {
  const v = useTween(value ?? null);
  return (
    <div className="card stat">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{format ? format(v) : v == null ? "—" : Math.round(v).toLocaleString()}</div>
      {foot && <div className="stat-foot">{foot}</div>}
      {children && <div className="stat-spark">{children}</div>}
    </div>
  );
}

export function Empty({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return <div className="empty"><div className="empty-title">{title}</div><div>{children}</div>{action && <div style={{ marginTop: 14 }}>{action}</div>}</div>;
}

export function Spinner() { return <span className="spinner" role="status" aria-label="loading" />; }

export function Loading({ rows = 3 }: { rows?: number }) {
  return <div className="col" style={{ padding: 16 }}>{Array.from({ length: rows }, (_, i) => <div key={i} className="skeleton" style={{ height: 18, width: `${90 - i * 12}%` }} />)}</div>;
}

export function ErrorBox({ error }: { error: Error | null | undefined }) {
  if (!error) return null;
  const errs: string[] = (error as any).errors ?? [];
  return (
    <div className="callout bad">
      <b>{error.message}</b>
      {errs.length > 0 && <ul style={{ margin: "6px 0 0", paddingLeft: 18 }}>{errs.slice(0, 12).map((e, i) => <li key={i} className="mono">{e}</li>)}</ul>}
    </div>
  );
}

export function Seg<T extends string>({ value, options, onChange }: { value: T; options: { value: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div className="seg" role="tablist">
      {options.map((o) => <button key={o.value} role="tab" aria-selected={o.value === value} className={o.value === value ? "on" : ""} onClick={() => onChange(o.value)}>{o.label}</button>)}
    </div>
  );
}

export function Tabs<T extends string>({ value, tabs, onChange }: { value: T; tabs: { value: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => <button key={t.value} role="tab" aria-selected={t.value === value} className={`tab ${t.value === value ? "on" : ""}`} onClick={() => onChange(t.value)}>{t.label}</button>)}
    </div>
  );
}

export function CopyButton({ text }: { text: string }) {
  const [ok, setOk] = useState(false);
  return (
    <button className="btn sm ghost icon" title="Copy" onClick={() => { navigator.clipboard?.writeText(text); setOk(true); setTimeout(() => setOk(false), 1200); }}>
      <Icon name={ok ? "check" : "copy"} size={13} />
    </button>
  );
}

export function Pager({ offset, limit, total, onChange }: { offset: number; limit: number; total: number; onChange: (o: number) => void }) {
  if (total <= limit) return <span className="muted num">{total} rows</span>;
  return (
    <div className="row">
      <span className="muted num">{offset + 1}–{Math.min(offset + limit, total)} of {total}</span>
      <button className="btn sm" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>Prev</button>
      <button className="btn sm" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>Next</button>
    </div>
  );
}

/* ---------------------------------------------------------------- overlays */
export function Drawer({ open, onClose, title, children, actions }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; actions?: ReactNode }) {
  useEffect(() => {
    if (!open) return;
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-modal="true">
        <div className="drawer-head"><div className="grow trunc" style={{ fontWeight: 600 }}>{title}</div>{actions}<button className="btn ghost icon" onClick={onClose} aria-label="Close"><Icon name="x" /></button></div>
        <div className="drawer-body">{children}</div>
      </aside>
    </>, document.body);
}

export function Modal({ open, onClose, title, children, footer }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode }) {
  useEffect(() => {
    if (!open) return;
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <div className="modal" role="dialog" aria-modal="true">
        <div className="drawer-head"><div className="grow" style={{ fontWeight: 600 }}>{title}</div><button className="btn ghost icon" onClick={onClose} aria-label="Close"><Icon name="x" /></button></div>
        <div className="drawer-body">{children}</div>
        {footer && <div className="drawer-head" style={{ borderTop: "1px solid var(--grat)", borderBottom: 0, justifyContent: "flex-end" }}>{footer}</div>}
      </div>
    </>, document.body);
}

/* ---------------------------------------------------------------- tooltip */
export interface TipState { x: number; y: number; content: ReactNode }
export function useTooltip() {
  const [tip, setTip] = useState<TipState | null>(null);
  const show = useCallback((e: { clientX: number; clientY: number }, content: ReactNode) => setTip({ x: e.clientX, y: e.clientY, content }), []);
  const hide = useCallback(() => setTip(null), []);
  const node = tip ? <Tooltip {...tip} /> : null;
  return { show, hide, node };
}
function Tooltip({ x, y, content }: TipState) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x + 14, top: y + 14 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ left: x + 14 + r.width > innerWidth - 8 ? x - r.width - 14 : x + 14, top: y + 14 + r.height > innerHeight - 8 ? y - r.height - 10 : y + 14 });
  }, [x, y]);
  return createPortal(<div ref={ref} className="tooltip" style={pos}>{content}</div>, document.body);
}

/* ---------------------------------------------------------------- toasts */
type Toast = { id: number; text: ReactNode; tone: string };
const ToastCtx = createContext<(text: ReactNode, tone?: string) => void>(() => {});
export const useToast = () => useContext(ToastCtx);
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const push = useCallback((text: ReactNode, tone = "") => {
    const id = Date.now() + Math.random();
    setItems((xs) => [...xs, { id, text, tone }]);
    setTimeout(() => setItems((xs) => xs.filter((t) => t.id !== id)), 4200);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      {createPortal(<div className="toast-wrap" aria-live="polite">{items.map((t) => <div key={t.id} className={`toast ${t.tone}`}>{t.text}</div>)}</div>, document.body)}
    </ToastCtx.Provider>
  );
}
