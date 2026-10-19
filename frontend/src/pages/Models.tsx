import { useState } from "react";
import { Badge, Card, Icon, Loading, Pager, Seg, useToast } from "../components/ui";
import { compact, perM } from "../format";
import { invalidate, useApi, useDebounced, useLocalState } from "../hooks";
import { navigate } from "../router";
import type { CatalogModel } from "../types";

export default function Models() {
  const [q, setQ] = useState("");
  const [provider, setProvider] = useState("");
  const [modality, setModality] = useState("");
  const [free, setFree] = useState(false);
  const [sort, setSort] = useState<"name" | "price" | "context" | "newest">("name");
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<CatalogModel[]>([]);
  const [, setPgTargets] = useLocalState<any[]>("arl-pg-targets", []);
  const toast = useToast();
  const dq = useDebounced(q, 200);
  const limit = 50;
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset), sort });
  if (dq) params.set("q", dq);
  if (provider) params.set("provider", provider);
  if (modality) params.set("modality", modality);
  if (free) params.set("free", "true");
  const url = `/api/catalog?${params}`;
  const { data, loading, reload } = useApi<{ items: CatalogModel[]; total: number; by_provider: Record<string, number> }>(url, { staleMs: 60000 });
  const reset = <T,>(fn: (v: T) => void) => (v: T) => { fn(v); setOffset(0); };
  const toggle = (m: CatalogModel) => setSelected((s) => s.some((x) => x.provider === m.provider && x.id === m.id) ? s.filter((x) => !(x.provider === m.provider && x.id === m.id)) : s.length >= 8 ? s : [...s, m]);

  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">Model registry</h1>
          <div className="page-sub">Capabilities and prices per provider. Unknown fields stay unknown — the Lab never fabricates a capability.</div>
        </div>
        <button className="btn" onClick={async () => { await fetch("/api/catalog?refresh=true&limit=1"); invalidate("/api/catalog"); reload(); toast("Catalog refreshed", "good"); }}><Icon name="refresh" />Refresh catalogs</button>
      </div>

      {selected.length > 0 && (
        <div className="callout" style={{ marginBottom: 14, display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
          <b>{selected.length} selected</b>
          {selected.map((m) => <span key={m.provider + m.id} className="tag">{m.id}</span>)}
          <span className="spacer" />
          <button className="btn sm" onClick={() => { setPgTargets(selected.map((m) => ({ provider: m.provider, model: m.id }))); navigate("/playground"); }}><Icon name="play" size={13} />Open in playground</button>
          <button className="btn sm primary" onClick={() => { sessionStorage.setItem("arl-builder-targets", JSON.stringify(selected.map((m) => ({ provider: m.provider, model: m.id })))); navigate("/experiments/new"); }}><Icon name="flask" size={13} />Compare in experiment</button>
          <button className="btn sm ghost" onClick={() => setSelected([])}>Clear</button>
        </div>
      )}

      <Card flush title={
        <div className="row wrap" style={{ gap: 8 }}>
          <div style={{ position: "relative", width: 280 }}>
            <span style={{ position: "absolute", left: 9, top: 8, color: "var(--muted)" }}><Icon name="search" size={15} /></span>
            <input className="input" style={{ paddingLeft: 30 }} placeholder="Search…" value={q} onChange={(e) => { setQ(e.target.value); setOffset(0); }} />
          </div>
          <Seg value={provider} onChange={reset(setProvider)} options={[{ value: "", label: "All" }, ...["openrouter", "openai", "groq", "local", "fake"].map((p) => ({ value: p, label: <>{p} <span className="faint num">{data?.by_provider?.[p] ?? ""}</span></> }))]} />
          <Seg value={modality} onChange={reset(setModality)} options={[{ value: "", label: "Any input" }, { value: "image", label: "Vision" }]} />
          <label className="check"><input type="checkbox" checked={free} onChange={(e) => { setFree(e.target.checked); setOffset(0); }} />Free only</label>
          <select className="select" style={{ width: 150 }} value={sort} onChange={(e) => reset(setSort)(e.target.value as any)} aria-label="Sort">
            <option value="name">Sort: name</option><option value="price">Sort: price</option><option value="context">Sort: context</option><option value="newest">Sort: newest</option>
          </select>
        </div>
      } actions={data && <Pager offset={offset} limit={limit} total={data.total} onChange={setOffset} />}>
        {loading && !data ? <Loading rows={8} /> : (
          <div className="table-wrap">
            <table className="t">
              <thead><tr><th style={{ width: 30 }} /><th>Model</th><th>Provider</th><th>Input</th><th className="r">Context</th><th className="r">$ / 1M in</th><th className="r">$ / 1M out</th><th>Structured</th><th>Price source</th></tr></thead>
              <tbody>
                {data?.items.map((m) => {
                  const on = selected.some((x) => x.provider === m.provider && x.id === m.id);
                  return (
                    <tr key={m.provider + m.id} className={`click ${on ? "sel" : ""}`} onClick={() => toggle(m)}>
                      <td><input type="checkbox" readOnly checked={on} style={{ accentColor: "var(--signal)" }} aria-label={`select ${m.id}`} /></td>
                      <td style={{ maxWidth: 380 }}>
                        <div className="row" style={{ gap: 6 }}><span className="trunc" style={{ fontWeight: 550 }}>{m.name}</span>{m.free && <Badge tone="good">free</Badge>}{m.fake && <Badge tone="warn">fake</Badge>}{m.reasoning && <Badge>reasoning</Badge>}</div>
                        <div className="mono muted trunc">{m.id}</div>
                      </td>
                      <td><span className="tag">{m.provider}</span></td>
                      <td><div className="row" style={{ gap: 4 }}>{m.modalities.map((x) => <span key={x} className="tag">{x}</span>)}</div></td>
                      <td className="r">{compact(m.context_length)}</td>
                      <td className="r">{perM(m.price_in)}</td>
                      <td className="r">{perM(m.price_out)}</td>
                      <td>{m.structured_output == null ? <span className="faint">unknown</span> : m.structured_output ? "yes" : "no"}</td>
                      <td className="muted">{m.price_source}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {data?.items.length === 0 && <div className="empty">No models match these filters. A provider without an API key shows no models.</div>}
          </div>
        )}
      </Card>
    </div>
  );
}
