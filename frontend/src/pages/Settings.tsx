import { useState } from "react";
import { api } from "../api";
import { Badge, Card, ErrorBox, Icon, Loading, useToast } from "../components/ui";
import { ago } from "../format";
import { invalidate, useApi } from "../hooks";
import type { Provider } from "../types";

export default function Settings() {
  const { data: providers, reload } = useApi<Provider[]>("/api/providers");
  const { data: audit } = useApi<{ id: number; ts: number; actor: string; action: string; resource: string; detail: any }[]>("/api/audit?limit=80", { refreshMs: 10000 });
  const { data: auth } = useApi<{ auth_required: boolean; authenticated: boolean }>("/api/auth/status");
  const [form, setForm] = useState({ name: "", label: "", base_url: "http://host.docker.internal:11434/v1", key_env: "", rpm: 60 });
  const [err, setErr] = useState<Error | null>(null);
  const toast = useToast();
  const add = async () => {
    setErr(null);
    try {
      await api.post("/api/connections", { ...form, key_env: form.key_env || null });
      toast(`Connection '${form.name}' added`, "good");
      invalidate("/api/providers"); invalidate("/api/catalog");
      reload();
    } catch (e: any) { setErr(e); }
  };
  return (
    <div className="page">
      <div className="page-head"><div className="grow"><h1 className="page-title">Settings</h1>
        <div className="page-sub">Provider connections, authentication and the audit trail. Secrets are never stored in the database, manifests, exports or logs — only environment variable names.</div></div></div>
      <div className="grid g-main" style={{ alignItems: "start" }}>
        <div className="col" style={{ gap: 14 }}>
          <Card title="Provider connections" flush>
            {!providers ? <Loading /> : (
              <table className="t"><thead><tr><th>Provider</th><th>Kind</th><th>Endpoint</th><th className="r">RPM limit</th><th>Cost reporting</th><th>Status</th></tr></thead>
                <tbody>{providers.map((p) => (
                  <tr key={p.name}><td><b>{p.label}</b><div className="mono muted">{p.name}</div></td><td><span className="tag">{p.kind}</span></td>
                    <td className="mono muted trunc" style={{ maxWidth: 260 }}>{p.base_url ?? "—"}</td><td className="r">{p.rpm ?? "—"}</td>
                    <td>{p.reports_cost ? "provider-reported" : p.kind === "fake" ? "free" : "price table"}</td>
                    <td>{p.configured ? <Badge tone="good" dot>ready</Badge> : <Badge tone="warn">{p.requires_key ? "missing key" : "unconfigured"}</Badge>}</td></tr>
                ))}</tbody></table>
            )}
          </Card>
          <Card title="Add an OpenAI-compatible endpoint" sub="Ollama, vLLM, LM Studio, TGI, LiteLLM, or any hosted gateway">
            <div className="grid g2">
              <div className="field"><label>Name (id)</label><input className="input mono" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value.toLowerCase() })} placeholder="ollama" /></div>
              <div className="field"><label>Label</label><input className="input" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} placeholder="Ollama on workstation" /></div>
              <div className="field"><label>Base URL (…/v1)</label><input className="input mono" value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} /></div>
              <div className="field"><label>API key env var (optional)</label><input className="input mono" value={form.key_env} onChange={(e) => setForm({ ...form, key_env: e.target.value.toUpperCase() })} placeholder="MY_GATEWAY_KEY" /></div>
              <div className="field"><label>Requests / minute</label><input className="input" type="number" min={1} value={form.rpm} onChange={(e) => setForm({ ...form, rpm: +e.target.value || 60 })} /></div>
            </div>
            <div className="row" style={{ marginTop: 12 }}><button className="btn primary" onClick={add} disabled={!form.name || !form.label}><Icon name="plus" />Add connection</button></div>
            <div style={{ marginTop: 10 }}><ErrorBox error={err} /></div>
          </Card>
        </div>
        <div className="col" style={{ gap: 14 }}>
          <Card title="Authentication">
            {auth ? (auth.auth_required ? (
              <div className="col"><div><Badge tone="good">owner token required</Badge></div>
                <button className="btn" onClick={async () => { await api.post("/api/auth/logout"); location.reload(); }}><Icon name="logout" />Sign out</button></div>
            ) : (
              <div className="callout warn" style={{ fontSize: 12.5 }}>Auth is off (local mode). Set <span className="mono">ARL_AUTH_TOKEN</span> before exposing the Lab beyond localhost. The Docker setup binds to 127.0.0.1 only.</div>
            )) : <Loading rows={2} />}
          </Card>
          <Card title="Audit trail" sub="Append-only record of state-changing actions" flush>
            <div style={{ maxHeight: 520, overflow: "auto" }}>
              <table className="t"><tbody>{(audit ?? []).map((a) => (
                <tr key={a.id}><td className="muted" style={{ whiteSpace: "nowrap" }}>{ago(a.ts)}</td><td><span className="mono">{a.action}</span><div className="mono faint trunc" style={{ maxWidth: 200 }}>{a.resource}</div></td><td className="muted">{a.actor}</td></tr>
              ))}</tbody></table>
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}
