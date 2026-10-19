import { useState } from "react";
import { api } from "../api";
import { A, Badge, Card, Empty, Icon, Labels, Loading, StatusBadge, Tabs, useToast } from "../components/ui";
import { ago, duration, pct, short, usd } from "../format";
import { useApi } from "../hooks";
import { navigate } from "../router";
import type { CampaignRow } from "../types";

interface ExpRow { id: string; name: string; hypothesis: string; status: string; plan_digest: string; created_at: number; frozen_at: number | null; dataset_id: string; campaigns: number; last_campaign: string | null }

export default function Experiments() {
  const [tab, setTab] = useState<"campaigns" | "experiments">("campaigns");
  const { data: camps, loading } = useApi<CampaignRow[]>("/api/campaigns?limit=200", { refreshMs: 3000, staleMs: 1500 });
  const { data: exps } = useApi<ExpRow[]>("/api/experiments", { refreshMs: 10000 });
  const toast = useToast();
  const relaunch = async (id: string) => {
    try { const r = await api.post<{ campaign_id: string }>(`/api/experiments/${id}/launch`); navigate(`/campaigns/${r.campaign_id}`); }
    catch (e: any) { toast(e.message, "bad"); }
  };
  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">Experiments</h1>
          <div className="page-sub">An experiment is a frozen plan (hashed). Each launch is a campaign; re-running a plan creates a new campaign so earlier evidence is never overwritten.</div>
        </div>
        <A href="/experiments/new" className="btn primary"><Icon name="plus" />New experiment</A>
      </div>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: "campaigns", label: `Campaigns (${camps?.length ?? 0})` }, { value: "experiments", label: `Plans (${exps?.length ?? 0})` }]} />
      {tab === "campaigns" ? (
        <Card flush>
          {loading && !camps ? <Loading rows={6} /> : !camps?.length ? (
            <Empty title="No campaigns yet" action={<A href="/experiments/new" className="btn primary">Design an experiment</A>}>Campaigns appear here as soon as they are launched.</Empty>
          ) : (
            <div className="table-wrap">
              <table className="t">
                <thead><tr><th>Campaign</th><th>Status</th><th>Progress</th><th className="r">Pass rate</th><th className="r">Spent / cap</th><th className="r">Duration</th><th>Created</th></tr></thead>
                <tbody>
                  {camps.map((c) => {
                    const p = c.planned_trials ? c.done / c.planned_trials : 0;
                    return (
                      <tr key={c.id} className="click" onClick={() => navigate(`/campaigns/${c.id}`)}>
                        <td style={{ maxWidth: 380 }}>
                          <div className="trunc" style={{ fontWeight: 550 }}>{c.name}</div>
                          <div className="row wrap" style={{ gap: 4, marginTop: 3 }}><span className="mono faint">{short(c.id)}</span><Labels labels={c.labels} /></div>
                        </td>
                        <td><StatusBadge status={c.status} />{c.stop_reason && <div className="muted" style={{ fontSize: 11 }}>{c.stop_reason.replace(/_/g, " ")}</div>}</td>
                        <td style={{ minWidth: 140 }}><div className={`progress ${c.live ? "live" : ""}`}><i style={{ width: `${p * 100}%` }} /></div><div className="muted num" style={{ fontSize: 11.5, marginTop: 3 }}>{c.done} / {c.planned_trials} trials</div></td>
                        <td className="r">{c.assessed ? pct(c.passes / c.assessed) : "—"}</td>
                        <td className="r">{usd(c.spent_usd)} <span className="faint">/ {c.budget_cap_usd == null ? "∞" : usd(c.budget_cap_usd)}</span>{c.unknown_cost_attempts > 0 && <div className="warn" style={{ fontSize: 11 }}>+{c.unknown_cost_attempts} unknown</div>}</td>
                        <td className="r">{c.started_at ? duration((c.finished_at ?? Date.now() / 1000) - c.started_at) : "—"}</td>
                        <td className="muted">{ago(c.created_at)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      ) : (
        <Card flush>
          {!exps?.length ? <Empty title="No plans yet" /> : (
            <div className="table-wrap">
              <table className="t">
                <thead><tr><th>Plan</th><th>Status</th><th>Digest</th><th className="r">Campaigns</th><th>Created</th><th /></tr></thead>
                <tbody>
                  {exps.map((e) => (
                    <tr key={e.id}>
                      <td style={{ maxWidth: 420 }}><div className="trunc" style={{ fontWeight: 550 }}>{e.name}</div><div className="muted trunc" style={{ fontSize: 12 }}>{e.hypothesis || "—"}</div></td>
                      <td><Badge tone={e.status === "frozen" ? "info" : ""}>{e.status}</Badge></td>
                      <td className="mono muted">{e.plan_digest?.slice(0, 12)}</td>
                      <td className="r">{e.campaigns}</td>
                      <td className="muted">{ago(e.created_at)}</td>
                      <td className="r"><div className="row" style={{ justifyContent: "flex-end" }}>
                        {e.last_campaign && <A href={`/campaigns/${e.last_campaign}`} className="btn sm ghost">Latest</A>}
                        <button className="btn sm" onClick={() => relaunch(e.id)}><Icon name="refresh" size={13} />{e.status === "draft" ? "Freeze & run" : "Fresh rerun"}</button>
                      </div></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}
