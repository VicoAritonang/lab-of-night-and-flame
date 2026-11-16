import { useState } from "react";
import { api } from "../api";
import { ago, num } from "../format";
import { useApi, useLocalState } from "../hooks";
import { Badge, Card, Icon, useToast } from "./ui";

interface Feedback { id: string; name: string; value: number | null; label: string | null; comment: string | null; source: string; author: string; created_at: number; metadata: Record<string, any> }

/** Feedback on one trace (users, reviewers, evaluators) with a quick human score form. */
export function FeedbackPanel({ traceId }: { traceId: string }) {
  const { data, reload } = useApi<Feedback[]>(`/api/agent-lab/runs/${traceId}/feedback`, { refreshMs: 10000 });
  const { data: queues } = useApi<{ id: string; name: string }[]>("/api/annotation/queues", { staleMs: 30000 });
  const [author] = useLocalState("arl-annotator", "");
  const [form, setForm] = useState({ name: "quality", comment: "" });
  const toast = useToast();
  const send = async (value: number) => {
    await api.post(`/api/agent-lab/runs/${traceId}/feedback`, { name: form.name, value, comment: form.comment || undefined, source: "human", author: author || "owner" });
    setForm({ ...form, comment: "" }); reload();
  };
  const enqueue = async (queue: string) => {
    if (!queue) return;
    const r = await api.post<{ added: number }>(`/api/annotation/queues/${queue}/items`, { trace_ids: [traceId] });
    toast(r.added ? "Added to the annotation queue" : "Already in that queue", "good");
  };
  return <Card title="Feedback" sub="End-user ratings, reviewer scores and online-evaluator results for this run">
    <div className="row wrap" style={{ gap: 6, marginBottom: 10 }}>
      <input className="input" style={{ width: 120 }} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} aria-label="Feedback name" />
      <input className="input grow" placeholder="comment (optional)" value={form.comment} onChange={(e) => setForm({ ...form, comment: e.target.value })} />
      <button className="btn sm" onClick={() => send(1)} aria-label="Good"><Icon name="check" size={13} />Good</button>
      <button className="btn sm danger" onClick={() => send(0)} aria-label="Bad"><Icon name="x" size={13} />Bad</button>
      {!!queues?.length && <select className="select" value="" onChange={(e) => enqueue(e.target.value)} aria-label="Send to annotation queue"><option value="">Send to queue…</option>{queues.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}</select>}
    </div>
    {!data?.length ? <div className="al-empty">No feedback yet. Applications send it with <span className="mono">lab.feedback(name, value, run_id=...)</span>.</div> :
      data.map((f) => <div key={f.id} className="row wrap al-span" style={{ gap: 8 }}>
        <Badge tone={f.source === "auto" ? "" : f.source === "human" ? "good" : ""}>{f.source}</Badge><b>{f.name}</b>
        {f.value != null && <span className="num">{num(f.value, 2)}</span>}{f.label && <span className="tag">{f.label}</span>}
        <span className="muted trunc" style={{ maxWidth: 420 }}>{f.comment}</span><span className="spacer" />
        <span className="faint">{f.author} · {ago(f.created_at)}</span>
        <button className="btn sm ghost" aria-label="Delete feedback" onClick={async () => { await api.del(`/api/feedback/${f.id}`); reload(); }}><Icon name="trash" size={12} /></button>
      </div>)}
  </Card>;
}
