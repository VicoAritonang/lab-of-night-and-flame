import { Fragment, useMemo } from "react";
import { Badge, Drawer, ErrorBox, Loading } from "./ui";
import { ms, num, usd } from "../format";
import { useApi } from "../hooks";

/** Word-level diff via LCS (O(n·m), capped) — enough for answers and short JSON. */
export function wordDiff(a: string, b: string): { t: string; k: "eq" | "del" | "ins" }[] {
  const A = a.split(/(\s+)/).slice(0, 1500), B = b.split(/(\s+)/).slice(0, 1500);
  const n = A.length, m = B.length;
  const dp = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const out: { t: string; k: "eq" | "del" | "ins" }[] = [];
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (A[i] === B[j]) { out.push({ t: A[i], k: "eq" }); i++; j++; }
    else if (dp[i + 1][j] >= dp[i][j + 1]) out.push({ t: A[i++], k: "del" });
    else out.push({ t: B[j++], k: "ins" });
  }
  while (i < n) out.push({ t: A[i++], k: "del" });
  while (j < m) out.push({ t: B[j++], k: "ins" });
  return out;
}

const OUTCOME_TONE: Record<string, string> = { pass: "good", fail: "bad", inconclusive: "warn", not_assessed: "", not_applicable: "" };
export function OutcomeBadge({ outcome, status, error }: { outcome: string; status?: string; error?: string | null }) {
  if (status === "error") return <Badge tone="bad" title="infrastructure failure, not a model error">error · {error ?? "unknown"}</Badge>;
  if (status && status !== "completed") return <Badge>{status.replace(/_/g, " ")}</Badge>;
  return <Badge tone={OUTCOME_TONE[outcome] ?? ""}>{outcome.replace(/_/g, " ")}</Badge>;
}

export function TrialDrawer({ id, onClose, armLabel }: { id: string | null; onClose: () => void; armLabel?: (k: string) => string }) {
  const { data: t, error } = useApi<any>(id ? `/api/trials/${id}` : null);
  const expectedText = t ? (typeof t.sample.expected === "string" ? t.sample.expected : t.sample.expected == null ? "" : JSON.stringify(t.sample.expected)) : "";
  const diff = useMemo(() => (t && t.output_text && expectedText && expectedText.length < 4000 ? wordDiff(expectedText, t.output_text) : null), [t, expectedText]);
  const latestRev = t ? Math.max(0, ...t.assessments.map((a: any) => a.revision)) : 0;
  return (
    <Drawer open={!!id} onClose={onClose} title={t ? <span className="row" style={{ gap: 8 }}><span className="mono">{t.sample.key}</span><span className="muted">·</span><span className="trunc">{armLabel?.(t.arm_key) ?? t.arm?.label}</span></span> : "Trial"}>
      {error && <ErrorBox error={error} />}
      {!t ? <Loading rows={6} /> : (
        <>
          <div className="row wrap" style={{ gap: 6 }}>
            <OutcomeBadge outcome={t.outcome} status={t.status} error={t.error_category} />
            <Badge mono>rep {t.repetition}</Badge><Badge mono>{t.attempt_count} attempt{t.attempt_count === 1 ? "" : "s"}</Badge>
            {t.resolved_model && <Badge mono title="provider-resolved model">{t.resolved_model}</Badge>}
            <span className="spacer" /><span className="muted num">{ms(t.latency_ms)} · TTFT {ms(t.ttft_ms)} · {num(t.tokens_in)}→{num(t.tokens_out)} tok · {usd(t.cost_usd)}</span>
          </div>
          {t.sample.input.images.map((d: string) => <img key={d} src={`/api/artifacts/${d}`} alt="sample input" style={{ maxWidth: "100%", maxHeight: 280, objectFit: "contain", borderRadius: 2, border: "1px solid var(--grat)", background: "#fff", alignSelf: "flex-start" }} />)}
          <div className="field"><label>Input</label><pre className="code">{t.sample.input.text}</pre></div>
          <div className="grid g2">
            <div className="field"><label>Model output (raw)</label><pre className="code">{t.output_text ?? <span className="faint">no output</span>}</pre></div>
            <div className="field"><label>Expected (private reference)</label><pre className="code">{expectedText || <span className="faint">none</span>}</pre></div>
          </div>
          {diff && (
            <div className="field"><label>Diff (expected → output)</label>
              <pre className="code">{diff.map((d, i) => d.k === "eq" ? <Fragment key={i}>{d.t}</Fragment> : <span key={i} className={d.k === "del" ? "diff-del" : "diff-ins"}>{d.t}</span>)}</pre>
            </div>
          )}
          <div className="field"><label>Assessments (latest revision {latestRev} first; older revisions are kept, never overwritten)</label>
            <div className="col" style={{ gap: 8 }}>
              {t.assessments.map((a: any) => (
                <div key={a.id} style={{ border: "1px solid var(--grat)", borderRadius: 2, padding: 10, opacity: a.revision === latestRev ? 1 : 0.6 }}>
                  <div className="row wrap" style={{ gap: 6 }}>
                    <OutcomeBadge outcome={a.verdict} /><b>{a.scorer_key}</b><span className="mono faint">{a.scorer_version}</span>
                    <span className="spacer" /><Badge mono>rev {a.revision}</Badge>{a.score != null && <span className="num">score {a.score.toFixed(3)}</span>}{a.cost_usd ? <span className="muted num">{usd(a.cost_usd)}</span> : null}
                  </div>
                  {a.evidence?.rationale && <div className="muted" style={{ marginTop: 6 }}>“{a.evidence.rationale}”</div>}
                  {a.evidence?.fields ? (
                    <table className="t" style={{ marginTop: 6 }}><thead><tr><th>Field</th><th>Got</th><th>Expected</th><th /></tr></thead>
                      <tbody>{Object.entries(a.evidence.fields).map(([k, f]: [string, any]) => (
                        <tr key={k}><td className="mono">{k}</td><td className="mono">{JSON.stringify(f.got)}</td><td className="mono">{JSON.stringify(f.expected)}</td><td>{f.ok ? <span className="good">✓</span> : <span className="bad">✗</span>}</td></tr>
                      ))}</tbody></table>
                  ) : (
                    <details style={{ marginTop: 6 }}><summary className="muted" style={{ cursor: "pointer", fontSize: 12 }}>evidence</summary><pre className="code" style={{ marginTop: 6 }}>{JSON.stringify(a.evidence, null, 2)}</pre></details>
                  )}
                </div>
              ))}
              {!t.assessments.length && <div className="muted">Not assessed{t.status !== "completed" ? " — the trial produced no output" : " yet"}.</div>}
            </div>
          </div>
          <div className="field"><label>Attempts (every provider call, including failures and retries)</label>
            <div className="table-wrap"><table className="t">
              <thead><tr><th>#</th><th>Status</th><th>Error</th><th className="r">Latency</th><th className="r">Tokens</th><th className="r">Cost</th><th>Request id</th></tr></thead>
              <tbody>{t.attempts.map((a: any) => (
                <tr key={a.id}>
                  <td className="num">{a.n}</td>
                  <td><Badge tone={a.status === "completed" ? "good" : a.status === "unknown" ? "warn" : "bad"}>{a.status}</Badge></td>
                  <td style={{ maxWidth: 260 }}><div className="trunc mono" title={a.error_message ?? ""}>{a.error_category ? `${a.error_category}: ${a.error_message ?? ""}` : "—"}</div></td>
                  <td className="r">{ms(a.latency_ms)}</td>
                  <td className="r">{a.tokens_in != null ? `${a.tokens_in}→${a.tokens_out}` : "—"}</td>
                  <td className="r" title={a.cost_source ?? ""}>{a.cost_usd == null ? <span className="warn">unknown</span> : usd(a.cost_usd)}</td>
                  <td className="mono faint trunc" style={{ maxWidth: 160 }}>{a.request_id ?? "—"}</td>
                </tr>
              ))}</tbody>
            </table></div>
          </div>
          <details><summary className="muted" style={{ cursor: "pointer" }}>Prompt configuration for this arm</summary>
            <pre className="code" style={{ marginTop: 8 }}>{JSON.stringify({ provider: t.arm?.provider, model: t.arm?.model, params: t.arm?.params, system: t.arm?.system, user: t.arm?.user, instructions: t.instructions }, null, 2)}</pre>
          </details>
        </>
      )}
    </Drawer>
  );
}
