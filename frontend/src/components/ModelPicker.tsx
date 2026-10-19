import { useState } from "react";
import { compact, perM } from "../format";
import { useApi, useDebounced } from "../hooks";
import type { CatalogModel } from "../types";
import { Badge, Icon, Loading } from "./ui";

export const modelKey = (m: { provider: string; id?: string; model?: string }) => `${m.provider}:${m.id ?? m.model}`;

export function ModelPicker({ selected, onToggle, requireImage, max = 8 }: {
  selected: { provider: string; model: string }[]; onToggle: (m: CatalogModel) => void; requireImage?: boolean; max?: number;
}) {
  const [q, setQ] = useState("");
  const [provider, setProvider] = useState("");
  const [free, setFree] = useState(false);
  const [vision, setVision] = useState(!!requireImage);
  const dq = useDebounced(q, 200);
  const params = new URLSearchParams({ limit: "150", sort: "name" });
  if (dq) params.set("q", dq);
  if (provider) params.set("provider", provider);
  if (free) params.set("free", "true");
  if (vision) params.set("modality", "image");
  const { data, loading } = useApi<{ items: CatalogModel[]; total: number; by_provider: Record<string, number> }>(`/api/catalog?${params}`, { staleMs: 60000 });
  const sel = new Set(selected.map((s) => `${s.provider}:${s.model}`));
  return (
    <div className="col" style={{ gap: 10 }}>
      <div className="row wrap">
        <div style={{ position: "relative", flex: "1 1 240px" }}>
          <span style={{ position: "absolute", left: 9, top: 8, color: "var(--muted)" }}><Icon name="search" size={15} /></span>
          <input className="input" style={{ paddingLeft: 30 }} placeholder="Search models, e.g. gemma, gpt-4.1, llama…" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <select className="select" style={{ width: 160 }} value={provider} onChange={(e) => setProvider(e.target.value)} aria-label="Provider">
          <option value="">All providers</option>
          {["openrouter", "openai", "groq", "local", "fake"].map((p) => <option key={p} value={p}>{p}</option>)}
        </select>
        <label className="check"><input type="checkbox" checked={vision} onChange={(e) => setVision(e.target.checked)} disabled={requireImage} />image input</label>
        <label className="check"><input type="checkbox" checked={free} onChange={(e) => setFree(e.target.checked)} />free</label>
      </div>
      <div className="model-list">
        {loading && !data ? <Loading rows={4} /> : (data?.items ?? []).length === 0 ? <div className="empty">No models match.</div> : data!.items.map((m) => {
          const on = sel.has(`${m.provider}:${m.id}`);
          const disabled = !on && selected.length >= max;
          return (
            <div key={modelKey(m)} className={`model-row ${on ? "on" : ""}`} onClick={() => !disabled && onToggle(m)} style={{ opacity: disabled ? 0.5 : 1 }}
              role="option" aria-selected={on}>
              <div style={{ minWidth: 0 }}>
                <div className="row" style={{ gap: 6 }}>
                  <input type="checkbox" readOnly checked={on} tabIndex={-1} style={{ accentColor: "var(--signal)" }} />
                  <span className="trunc" style={{ fontWeight: 550 }}>{m.name}</span>
                  {m.fake && <Badge tone="warn">fake</Badge>}
                  {m.free && <Badge tone="good">free</Badge>}
                </div>
                <div className="mono muted trunc" style={{ paddingLeft: 21 }}>{m.provider} · {m.id}</div>
              </div>
              <div className="row" style={{ gap: 6 }}>
                {m.modalities.includes("image") && <span className="tag" title="accepts images"><Icon name="image" size={11} /></span>}
                {m.context_length ? <span className="tag">{compact(m.context_length)}</span> : null}
                <span className="tag" title={`$/1M tokens in · out (${m.price_source})`}>{m.price_in == null ? "price ?" : `${perM(m.price_in)} · ${perM(m.price_out)}`}</span>
              </div>
            </div>
          );
        })}
      </div>
      <div className="muted" style={{ fontSize: 11.5 }}>
        {data ? `${data.total} models` : ""} · prices in $/1M tokens (input · output){" "}
        <span className="faint">— OpenRouter from live catalog; OpenAI/Groq from a static table that may be stale</span>
      </div>
    </div>
  );
}
