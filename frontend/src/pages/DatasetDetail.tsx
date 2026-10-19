import { useState } from "react";
import { api } from "../api";
import { A, Badge, Card, Drawer, ErrorBox, Icon, Loading, Modal, Pager, useToast } from "../components/ui";
import { ago } from "../format";
import { invalidate, useApi, useDebounced } from "../hooks";
import { navigate } from "../router";
import type { Dataset, Page, Sample } from "../types";

export function Expected({ value }: { value: any }) {
  if (value == null) return <span className="faint">none</span>;
  return <span className="mono">{typeof value === "string" ? value : JSON.stringify(value)}</span>;
}

export default function DatasetDetail({ id }: { id: string }) {
  const { data: ds, error, reload } = useApi<Dataset>(`/api/datasets/${id}`);
  const [offset, setOffset] = useState(0);
  const [split, setSplit] = useState("");
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 250);
  const [open, setOpen] = useState<Sample | null>(null);
  const limit = 25;
  const qs = new URLSearchParams({ offset: String(offset), limit: String(limit) });
  if (split) qs.set("split", split);
  if (dq) qs.set("q", dq);
  const { data: samples, loading } = useApi<Page<Sample>>(`/api/datasets/${id}/samples?${qs}`);
  const toast = useToast();
  const [predOpen, setPredOpen] = useState(false);
  const [predName, setPredName] = useState("");
  const [predFile, setPredFile] = useState<File | null>(null);

  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (!ds) return <div className="page"><Loading rows={6} /></div>;
  const card = ds.card ?? {};
  const leak = card.leakage ?? {};

  const importPreds = async () => {
    if (!predName.trim() || !predFile) return toast("Name and file are required", "bad");
    const fd = new FormData();
    fd.append("name", predName.trim());
    fd.append("file", predFile);
    try {
      const r = await api.form<{ imported: number; unmatched: string[] }>(`/api/datasets/${id}/predictions`, fd);
      toast(`Imported ${r.imported} predictions${r.unmatched.length ? `; ${r.unmatched.length} unmatched ids` : ""}`, "good");
      invalidate(`/api/datasets/${id}`);
      reload();
      setPredOpen(false);
    } catch (e: any) { toast(e.message, "bad"); }
  };

  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <div className="row" style={{ gap: 8 }}><A href="/datasets" className="btn sm ghost"><Icon name="back" size={13} />Datasets</A></div>
          <h1 className="page-title" style={{ marginTop: 8 }}>{ds.name} <span className="muted mono" style={{ fontSize: 14 }}>v{ds.version}</span></h1>
          <div className="page-sub">{ds.description}</div>
          <div className="row wrap" style={{ gap: 5, marginTop: 8 }}>
            <Badge>{ds.modality}</Badge><Badge>{ds.task_type}</Badge><Badge>{ds.sample_count} samples</Badge>
            {ds.builtin && <Badge tone="info">built-in</Badge>}{ds.synthetic && <Badge>synthetic</Badge>}
          </div>
        </div>
        <button className="btn" onClick={() => setPredOpen(true)}><Icon name="upload" />Import predictions</button>
        <button className="btn primary" onClick={() => navigate(`/experiments/new?dataset=${ds.id}`)}><Icon name="flask" />Evaluate on this dataset</button>
      </div>

      <div className="grid g3" style={{ marginBottom: 14 }}>
        <Card title="Snapshot">
          <dl className="kv">
            <dt>Digest</dt><dd className="mono">{ds.digest.slice(0, 24)}…</dd>
            <dt>Created</dt><dd>{ago(ds.created_at)}</dd>
            <dt>Splits</dt><dd>{Object.entries(card.splits ?? {}).map(([k, v]) => <span key={k} className="tag" style={{ marginRight: 4 }}>{k}: {String(v)}</span>)}</dd>
            <dt>Used by</dt><dd>{ds.used_by_campaigns ?? 0} campaign(s)</dd>
          </dl>
        </Card>
        <Card title="Dataset card">
          <dl className="kv">
            <dt>Origin</dt><dd>{card.origin ?? "—"}</dd>
            <dt>Method</dt><dd>{card.collection_method ?? "—"}</dd>
            <dt>License</dt><dd>{card.license ?? "—"}</dd>
            <dt>Labels</dt><dd>{card.label_source ?? "—"}</dd>
            <dt>Contamination</dt><dd>{card.contamination ?? "unknown"}</dd>
          </dl>
        </Card>
        <Card title="Integrity checks" sub={leak.method}>
          <dl className="kv">
            <dt>Exact cross-split dupes</dt><dd className={leak.exact_cross_split_duplicates ? "warn" : "good"}>{leak.exact_cross_split_duplicates ?? "—"}</dd>
            <dt>Near cross-split dupes</dt><dd className={leak.near_cross_split_duplicates ? "warn" : "good"}>{leak.near_cross_split_duplicates ?? "—"}</dd>
            <dt>Groups across splits</dt><dd className={leak.groups_spanning_splits?.length ? "warn" : "good"}>{leak.groups_spanning_splits?.length ?? 0}</dd>
            <dt>Predictions</dt><dd>{ds.predictions?.length ? ds.predictions.map((p) => <span key={p.import_name} className="tag" style={{ marginRight: 4 }}>{p.import_name} ({p.n})</span>) : <span className="faint">none imported</span>}</dd>
          </dl>
        </Card>
      </div>

      {ds.instructions && <div className="callout" style={{ marginBottom: 14 }}><span className="muted">Task instructions (default system prompt): </span>{ds.instructions}</div>}

      <Card flush title={<div className="row wrap" style={{ gap: 8 }}>
        <span>Samples</span>
        <input className="input" style={{ width: 220 }} placeholder="Filter by id or text…" value={q} onChange={(e) => { setQ(e.target.value); setOffset(0); }} />
        <select className="select" style={{ width: 140 }} value={split} onChange={(e) => { setSplit(e.target.value); setOffset(0); }} aria-label="Split">
          <option value="">All splits</option>{Object.keys(card.splits ?? {}).map((s) => <option key={s}>{s}</option>)}
        </select>
      </div>} actions={samples && <Pager offset={offset} limit={limit} total={samples.total} onChange={setOffset} />}>
        {loading && !samples ? <Loading rows={6} /> : (
          <div className="table-wrap">
            <table className="t">
              <thead><tr><th>ID</th><th>Split</th>{ds.modality === "image" && <th>Image</th>}<th>Input</th><th>Expected</th><th>Meta</th></tr></thead>
              <tbody>
                {samples?.items.map((s) => (
                  <tr key={s.id} className="click" onClick={() => setOpen(s)}>
                    <td className="mono">{s.key}</td>
                    <td><span className="tag">{s.split}</span></td>
                    {ds.modality === "image" && <td>{s.input.images[0] && <img className="thumb" style={{ width: 52, height: 40 }} src={`/api/artifacts/${s.input.images[0]}`} alt="" loading="lazy" />}</td>}
                    <td style={{ maxWidth: 420 }}><div className="trunc">{s.input.text}</div></td>
                    <td style={{ maxWidth: 240 }}><div className="trunc"><Expected value={s.expected} /></div></td>
                    <td style={{ maxWidth: 220 }}><div className="trunc muted mono">{Object.entries(s.meta).map(([k, v]) => `${k}=${typeof v === "object" ? "…" : v}`).join(" ")}</div></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal open={predOpen} onClose={() => setPredOpen(false)} title="Import predictions (evaluate without regeneration)"
        footer={<><button className="btn ghost" onClick={() => setPredOpen(false)}>Cancel</button><button className="btn primary" onClick={importPreds}>Import</button></>}>
        <p className="muted" style={{ margin: 0 }}>Rows of <span className="mono">{"{"}"id": "&lt;sample id&gt;", "output": "…"{"}"}</span>. Imported outputs are scored like any target but carry no latency or cost claims.</p>
        <div className="field"><label>Name</label><input className="input" value={predName} onChange={(e) => setPredName(e.target.value)} placeholder="legacy-model-v1" /></div>
        <div className="field"><label>File (.jsonl / .json / .csv)</label><input type="file" accept=".jsonl,.json,.csv" onChange={(e) => setPredFile(e.target.files?.[0] ?? null)} /></div>
      </Modal>

      <Drawer open={!!open} onClose={() => setOpen(null)} title={<span className="mono">{open?.key}</span>}>
        {open && <>
          {open.input.images.map((d) => <img key={d} src={`/api/artifacts/${d}`} alt="" style={{ maxWidth: "100%", borderRadius: 2, border: "1px solid var(--grat)", background: "#fff" }} />)}
          <div className="field"><label>Input</label><pre className="code">{open.input.text}</pre></div>
          <div className="field"><label>Expected (private — never sent to targets)</label><pre className="code">{typeof open.expected === "string" ? open.expected : JSON.stringify(open.expected, null, 2)}</pre></div>
          <div className="field"><label>Metadata</label><pre className="code">{JSON.stringify(open.meta, null, 2)}</pre></div>
          <dl className="kv"><dt>Content hash</dt><dd className="mono">{open.content_hash}</dd><dt>Group</dt><dd>{open.group ?? "—"}</dd></dl>
        </>}
      </Drawer>
    </div>
  );
}
