import { useState } from "react";
import { api } from "../api";
import { Badge, ErrorBox, Icon, Loading, Modal, useToast } from "../components/ui";
import { invalidate, useApi } from "../hooks";
import { navigate } from "../router";
import type { Dataset } from "../types";

export default function Datasets() {
  const { data, loading, reload } = useApi<Dataset[]>("/api/datasets");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<"all" | "text" | "image">("all");
  const list = (data ?? []).filter((d) => filter === "all" || d.modality === filter);
  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">Datasets</h1>
          <div className="page-sub">Immutable, content-addressed snapshots. Re-importing identical content returns the same version; changes create a new one.</div>
        </div>
        <div className="seg">{(["all", "text", "image"] as const).map((f) => <button key={f} className={filter === f ? "on" : ""} onClick={() => setFilter(f)}>{f}</button>)}</div>
        <button className="btn primary" onClick={() => setOpen(true)}><Icon name="upload" />Import dataset</button>
      </div>
      {loading && !data ? <Loading rows={6} /> : (
        <div className="grid g3">
          {list.map((d, i) => (
            <a key={d.id} href={`/datasets/${d.id}`} onClick={(e) => { e.preventDefault(); navigate(`/datasets/${d.id}`); }} className="card fade-in"
              style={{ animationDelay: `${i * 35}ms`, color: "inherit", textDecoration: "none", display: "flex", flexDirection: "column" }}>
              <div className="card-body col" style={{ gap: 10, flex: 1 }}>
                <div className="row">
                  <span style={{ width: 30, height: 30, borderRadius: 2, display: "grid", placeItems: "center", background: d.modality === "image" ? "color-mix(in srgb, var(--s5) 18%, transparent)" : "var(--signal-wash)", color: d.modality === "image" ? "var(--s5)" : "var(--signal-ink)" }}>
                    <Icon name={d.modality === "image" ? "image" : "text"} />
                  </span>
                  <div className="grow"><div style={{ fontWeight: 600 }} className="trunc">{d.name}</div><div className="mono muted">v{d.version} · {d.digest.slice(0, 12)}</div></div>
                </div>
                <div className="muted" style={{ fontSize: 12.5, flex: 1 }}>{d.description || "No description."}</div>
                <div className="row wrap" style={{ gap: 5 }}>
                  <Badge>{d.sample_count} samples</Badge><Badge>{d.task_type}</Badge>
                  {d.builtin && <Badge tone="info">built-in</Badge>}{d.synthetic && <Badge>synthetic</Badge>}
                  {(d.card?.leakage?.exact_cross_split_duplicates ?? 0) > 0 && <Badge tone="warn">split leakage</Badge>}
                </div>
              </div>
            </a>
          ))}
        </div>
      )}
      <ImportModal open={open} onClose={() => setOpen(false)} onDone={(id) => { invalidate("/api/datasets"); reload(); setOpen(false); navigate(`/datasets/${id}`); }} />
    </div>
  );
}

const EXAMPLE = `{"id": "q1", "input": "What is 2+2?", "expected": 4, "split": "test", "difficulty": "easy"}
{"id": "q2", "input": {"text": "What color is the car?", "images": ["car.png"]}, "expected": "red"}`;

function ImportModal({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: (id: string) => void }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [instructions, setInstructions] = useState("");
  const [taskType, setTaskType] = useState("generic");
  const [file, setFile] = useState<File | null>(null);
  const [images, setImages] = useState<File[]>([]);
  const [partial, setPartial] = useState(false);
  const [err, setErr] = useState<Error | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const submit = async () => {
    if (!file || !name.trim()) return setErr(new Error("Name and file are required"));
    const fd = new FormData();
    fd.append("file", file);
    images.forEach((im) => fd.append("images", im, im.name));
    fd.append("name", name.trim());
    fd.append("description", description);
    fd.append("instructions", instructions);
    fd.append("task_type", taskType);
    fd.append("allow_partial", String(partial));
    setBusy(true);
    setErr(null);
    try {
      const ds = await api.form<Dataset & { rejected: any[] }>("/api/datasets/import", fd);
      toast(`Imported ${ds.sample_count} samples${ds.rejected?.length ? `, ${ds.rejected.length} rejected` : ""}`, "good");
      onDone(ds.id);
    } catch (e: any) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <Modal open={open} onClose={onClose} title="Import dataset"
      footer={<><button className="btn ghost" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy} onClick={submit}>{busy ? "Validating…" : "Validate & import"}</button></>}>
      <div className="grid g2">
        <div className="field"><label>Name</label><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="support-intents" /></div>
        <div className="field"><label>Task type</label>
          <select className="select" value={taskType} onChange={(e) => setTaskType(e.target.value)}>
            {["generic", "extraction", "classification", "multiple_choice", "reasoning", "qa", "summarization", "vision_qa", "vision_ocr", "vision_grounding"].map((t) => <option key={t}>{t}</option>)}
          </select>
        </div>
      </div>
      <div className="field"><label>Description</label><input className="input" value={description} onChange={(e) => setDescription(e.target.value)} /></div>
      <div className="field"><label>Task instructions (used as the default system prompt)</label><textarea className="textarea" rows={2} value={instructions} onChange={(e) => setInstructions(e.target.value)} /></div>
      <div className="grid g2">
        <div className="field"><label>Records (.jsonl / .json / .csv / .tsv)</label><input type="file" accept=".jsonl,.ndjson,.json,.csv,.tsv" onChange={(e) => setFile(e.target.files?.[0] ?? null)} /></div>
        <div className="field"><label>Images referenced by filename (optional)</label><input type="file" accept="image/*" multiple onChange={(e) => setImages(Array.from(e.target.files ?? []))} /></div>
      </div>
      <label className="check"><input type="checkbox" checked={partial} onChange={(e) => setPartial(e.target.checked)} />Import valid records even if some are invalid (rejected rows are reported)</label>
      <details><summary className="muted" style={{ cursor: "pointer" }}>Record format</summary>
        <pre className="code" style={{ marginTop: 8 }}>{EXAMPLE}</pre>
        <div className="hint" style={{ marginTop: 6 }}>Input keys: input | prompt | question | text. Expected keys: expected | answer | label | target. Optional: id, split (train/dev/calibration/test/holdout), group, meta. Extra columns become metadata for slicing.</div>
      </details>
      <ErrorBox error={err} />
    </Modal>
  );
}
