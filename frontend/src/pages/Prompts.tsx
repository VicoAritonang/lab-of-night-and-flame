import { useEffect, useState } from "react";
import { api } from "../api";
import { Diff } from "../components/Diff";
import { Badge, Card, Empty, ErrorBox, Icon, Loading, useToast } from "../components/ui";
import { ago } from "../format";
import { invalidate, useApi } from "../hooks";
import { navigate, useLocation } from "../router";

interface Msg { role: string; content: string }
interface Prompt { id: string; name: string; description: string; latest: number; labels: Record<string, number>; updated_at: number }
interface Version { id: string; prompt: string; version: number; template: { system: string; messages: Msg[] }; config: { model?: string; provider?: string; params?: Record<string, any> }; variables: string[]; message: string; author: string; created_at: number }

const asText = (v: Version | undefined) => v ? [v.template.system ? `[system]\n${v.template.system}` : "", ...v.template.messages.map((m) => `[${m.role}]\n${m.content}`)].filter(Boolean).join("\n\n") : "";

function Editor({ base, name, onDone }: { base?: Version; name?: string; onDone: (v: Version) => void }) {
  const [form, setForm] = useState({ name: name ?? "", description: "", system: base?.template.system ?? "", messages: base?.template.messages ?? [] as Msg[], model: base?.config.model ?? "", provider: base?.config.provider ?? "", message: "", labels: "" });
  const [err, setErr] = useState<Error | null>(null);
  const save = async () => {
    try {
      const body = { name: form.name, description: form.description || undefined, template: { system: form.system, messages: form.messages }, config: { ...(form.model ? { model: form.model } : {}), ...(form.provider ? { provider: form.provider } : {}), ...(base?.config.params ? { params: base.config.params } : {}) }, message: form.message, labels: form.labels.split(",").map((s) => s.trim()).filter(Boolean) };
      const v = await api.post<Version & { created: boolean }>(name ? `/api/prompts/${encodeURIComponent(name)}/versions` : "/api/prompts", body);
      onDone(v);
    } catch (e: any) { setErr(e); }
  };
  return <div className="col" style={{ gap: 10 }}>
    {!name && <div className="grid g2"><input className="input mono" placeholder="name (slug, e.g. support.answer)" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value.toLowerCase() })} /><input className="input" placeholder="description" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} /></div>}
    <label className="field"><span className="wb-label">System prompt · use {"{{variables}}"}</span><textarea className="textarea mono wb-code" rows={8} value={form.system} onChange={(e) => setForm({ ...form, system: e.target.value })} /></label>
    {form.messages.map((m, i) => <div key={i} className="row" style={{ alignItems: "start" }}>
      <select className="select" value={m.role} onChange={(e) => { const ms = [...form.messages]; ms[i] = { ...m, role: e.target.value }; setForm({ ...form, messages: ms }); }}><option>user</option><option>assistant</option><option>system</option></select>
      <textarea className="textarea mono grow" rows={2} value={m.content} onChange={(e) => { const ms = [...form.messages]; ms[i] = { ...m, content: e.target.value }; setForm({ ...form, messages: ms }); }} />
      <button className="btn sm ghost" aria-label="Remove message" onClick={() => setForm({ ...form, messages: form.messages.filter((_, j) => j !== i) })}><Icon name="trash" size={13} /></button>
    </div>)}
    <button className="btn sm" style={{ alignSelf: "start" }} onClick={() => setForm({ ...form, messages: [...form.messages, { role: "user", content: "{{question}}" }] })}><Icon name="plus" size={13} />Message</button>
    <div className="grid g2">
      <input className="input mono" placeholder="provider (optional)" value={form.provider} onChange={(e) => setForm({ ...form, provider: e.target.value })} />
      <input className="input mono" placeholder="model (optional)" value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} />
      <input className="input" placeholder="commit message" value={form.message} onChange={(e) => setForm({ ...form, message: e.target.value })} />
      <input className="input mono" placeholder="labels, e.g. staging" value={form.labels} onChange={(e) => setForm({ ...form, labels: e.target.value })} />
    </div>
    <ErrorBox error={err} />
    <div className="row"><button className="btn primary" onClick={save} disabled={!form.name || (!form.system && !form.messages.length)}><Icon name="check" />{name ? "Commit version" : "Create prompt"}</button></div>
  </div>;
}

function Detail({ name }: { name: string }) {
  const { data: prompt, reload: reloadPrompt } = useApi<Version & { labels: Record<string, number>; description: string; latest: number }>(`/api/prompts/${encodeURIComponent(name)}?label=latest`);
  const { data: versions, reload } = useApi<Version[]>(`/api/prompts/${encodeURIComponent(name)}/versions`);
  const [pick, setPick] = useState<number>(0);
  const [compare, setCompare] = useState<number>(0);
  const [editing, setEditing] = useState(false);
  const [vars, setVars] = useState<Record<string, string>>({});
  const [rendered, setRendered] = useState<{ messages: Msg[]; missing: string[] } | null>(null);
  const toast = useToast();
  useEffect(() => { if (versions?.length && !pick) { setPick(versions[0].version); setCompare(versions[1]?.version ?? 0); } }, [versions, pick]);
  if (!prompt || !versions) return <Loading rows={6} />;
  const v = versions.find((x) => x.version === pick) ?? versions[0];
  const other = versions.find((x) => x.version === compare);
  const labelsOf = (n: number) => Object.entries(prompt.labels).filter(([, ver]) => ver === n).map(([l]) => l);
  const setLabel = async (label: string, version: number) => { await api.post(`/api/prompts/${encodeURIComponent(name)}/labels`, { label, version }); reloadPrompt(); invalidate("/api/prompts"); toast(`${label} → v${version}`, "good"); };
  const render = async () => setRendered(await api.post(`/api/prompts/${encodeURIComponent(name)}/render`, { version: v.version, variables: vars }));
  const tryIt = async () => {
    const s = await api.post<{ id: string }>("/api/workbench/scenarios", { name: `${name} v${v.version}`, collection: "Prompts", mode: "llm", input: "", config: { model: v.config.model ?? "", provider: v.config.provider ?? "fake", system_prompt: v.template.system, params: v.config.params ?? { temperature: 0.2 }, prompt: { name, version: v.version } }, llm: { messages: v.template.messages }, checks: {} });
    navigate(`/workbench?scenario=${encodeURIComponent(s.id)}`);
  };
  return <div className="grid g-main" style={{ alignItems: "start" }}>
    <div className="col" style={{ gap: 14 }}>
      <Card title={<span className="mono">{name}</span>} sub={prompt.description || "No description"} actions={<div className="row" style={{ gap: 6 }}>
        <button className="btn sm" onClick={tryIt}><Icon name="play" size={13} />Try in Workbench</button>
        <button className="btn sm primary" onClick={() => setEditing(!editing)}><Icon name="plus" size={13} />New version</button></div>}>
        {editing ? <Editor base={v} name={name} onDone={(nv) => { setEditing(false); reload(); reloadPrompt(); setPick(nv.version); invalidate("/api/prompts"); }} /> : <>
          <div className="row wrap" style={{ marginBottom: 10 }}>
            <b>v{v.version}</b>{labelsOf(v.version).map((l) => <Badge key={l} tone={l === "production" ? "good" : ""}>{l}</Badge>)}
            <span className="muted">{v.message || "no message"} · {ago(v.created_at)}</span><span className="spacer" />
            {!labelsOf(v.version).includes("production") && <button className="btn sm" onClick={() => setLabel("production", v.version)}>Promote to production</button>}
            {!labelsOf(v.version).includes("staging") && <button className="btn sm ghost" onClick={() => setLabel("staging", v.version)}>Mark staging</button>}
          </div>
          {v.config.model && <div className="mono muted" style={{ marginBottom: 8 }}>{v.config.provider ? `${v.config.provider} · ` : ""}{v.config.model}</div>}
          <pre className="al-json" style={{ whiteSpace: "pre-wrap" }}>{asText(v)}</pre>
        </>}
      </Card>
      {!editing && versions.length > 1 && <Card title="Diff" actions={<select className="select" value={compare} onChange={(e) => setCompare(+e.target.value)} aria-label="Compare with">{versions.filter((x) => x.version !== v.version).map((x) => <option key={x.version} value={x.version}>vs v{x.version}</option>)}</select>}>
        {other ? <Diff a={asText(other)} b={asText(v)} /> : <span className="muted">Pick a version to compare</span>}
      </Card>}
      {!editing && v.variables.length > 0 && <Card title="Render" sub="Fill variables to preview the exact messages an agent receives">
        <div className="grid g2">{v.variables.map((k) => <input key={k} className="input mono" placeholder={k} value={vars[k] ?? ""} onChange={(e) => setVars({ ...vars, [k]: e.target.value })} />)}</div>
        <div className="row" style={{ marginTop: 10 }}><button className="btn" onClick={render}>Render</button>{rendered?.missing.length ? <span className="warn">missing: {rendered.missing.join(", ")}</span> : null}</div>
        {rendered && <pre className="al-json" style={{ whiteSpace: "pre-wrap", marginTop: 10 }}>{rendered.messages.map((m) => `[${m.role}]\n${m.content}`).join("\n\n")}</pre>}
      </Card>}
    </div>
    <Card title="Versions" flush>
      <table className="t"><tbody>{versions.map((x) => <tr key={x.version} className={`click ${x.version === v.version ? "wb-current" : ""}`} onClick={() => { setPick(x.version); setEditing(false); setRendered(null); }}>
        <td className="num"><b>v{x.version}</b></td><td><div className="row wrap" style={{ gap: 4 }}>{labelsOf(x.version).map((l) => <Badge key={l} tone={l === "production" ? "good" : ""}>{l}</Badge>)}</div><div className="faint trunc" style={{ maxWidth: 200 }}>{x.message}</div></td><td className="muted">{ago(x.created_at)}</td></tr>)}</tbody></table>
    </Card>
  </div>;
}

export default function Prompts() {
  const { query } = useLocation();
  const name = query.get("name") || "";
  const { data, error } = useApi<Prompt[]>("/api/prompts", { refreshMs: 15000 });
  const [creating, setCreating] = useState(false);
  return <div className="page">
    <div className="page-head"><div className="grow">
      <div className="eyebrow">Prompt registry</div>
      <h1 className="page-title">{name || "Versioned prompts with deploy labels"}</h1>
      <div className="page-sub">Every change is an immutable version; labels such as <span className="mono">production</span> and <span className="mono">staging</span> decide what agents fetch with <span className="mono">lab.prompt("name")</span>. Diff versions, render variables and try them in the Workbench.</div>
    </div>
      {name ? <button className="btn" onClick={() => navigate("/prompts")}><Icon name="back" />All prompts</button> : <button className="btn primary" onClick={() => setCreating(!creating)}><Icon name="plus" />New prompt</button>}
    </div>
    <ErrorBox error={error} />
    {name ? <Detail name={name} /> : <>
      {creating && <Card title="New prompt" style={{ marginBottom: 14 }}><Editor onDone={(v) => { setCreating(false); invalidate("/api/prompts"); navigate(`/prompts?name=${encodeURIComponent(v.prompt)}`); }} /></Card>}
      {!data ? <Loading /> : !data.length ? <Empty title="No prompts yet">Create one here or commit from code with <span className="mono">POST /api/prompts</span>.</Empty> :
        <Card flush><table className="t"><thead><tr><th>Prompt</th><th>Labels</th><th className="r">Versions</th><th>Updated</th></tr></thead>
          <tbody>{data.map((p) => <tr key={p.id} className="click" onClick={() => navigate(`/prompts?name=${encodeURIComponent(p.name)}`)}>
            <td><b className="mono">{p.name}</b><div className="muted trunc" style={{ maxWidth: 360 }}>{p.description}</div></td>
            <td><div className="row wrap" style={{ gap: 4 }}>{Object.entries(p.labels).map(([l, v]) => <Badge key={l} tone={l === "production" ? "good" : ""}>{l} → v{v}</Badge>)}</div></td>
            <td className="r num">{p.latest}</td><td className="muted">{ago(p.updated_at)}</td></tr>)}</tbody></table></Card>}
    </>}
  </div>;
}
