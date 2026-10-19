"""Portable reports (FR-019): standalone HTML (inline CSS + SVG, no external assets), Markdown, JSON.

Every report answers: what was tested, which versions, which data, how it was measured,
outcomes, failures, missing evidence, cost coverage, and the gate decision (product.md §29).
"""
from __future__ import annotations

import hashlib
import html
import json
import time
from typing import Any

import orjson

SERIES = ["#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#008300", "#9085e9", "#e66767"]
VERDICT_LABEL = {"pass": "PASS", "fail": "FAIL", "insufficient_evidence": "INSUFFICIENT EVIDENCE", "invalid_experiment": "INVALID"}
LABEL_TEXT = {
    "fake_runtime_test": "FAKE TARGET — runtime test, not model quality",
    "synthetic_data": "Synthetic data",
    "imported_outputs": "Imported outputs — no inference performance measured",
    "model_quality_test": "Model quality test",
    "re_scored": "Re-scored (new assessment revision)",
}


def _pct(v: float | None, signed: bool = False) -> str:
    if v is None:
        return "—"
    return f"{v * 100:+.1f}%" if signed else f"{v * 100:.1f}%"


def _ms(v: float | None) -> str:
    return "—" if v is None else (f"{v / 1000:.2f} s" if v >= 1000 else f"{v:.0f} ms")


def _usd(v: float | None) -> str:
    if v is None:
        return "unknown"
    if v == 0:
        return "$0"
    return f"${v:.4f}" if v >= 0.01 else f"${v:.6f}"


def _e(s: Any) -> str:
    return html.escape(str(s), quote=True)


def _ci_bars_svg(rows: list[tuple[str, dict, str]]) -> str:
    """Horizontal bars with 95% CI whiskers. rows: (label, interval, color)."""
    bar_h, gap, left, width = 18, 14, 280, 360
    h = len(rows) * (bar_h + gap) + 30
    parts = [f'<svg viewBox="0 0 {left + width + 70} {h}" role="img" aria-label="Pass rate by arm with 95% CI" class="chart">']
    for t in range(0, 101, 25):
        x = left + width * t / 100
        parts.append(f'<line x1="{x}" y1="0" x2="{x}" y2="{h - 22}" class="grid"/>'
                     f'<text x="{x}" y="{h - 6}" class="tick" text-anchor="middle">{t}%</text>')
    for i, (label, iv, color) in enumerate(rows):
        y = i * (bar_h + gap) + 4
        parts.append(f'<text x="{left - 10}" y="{y + bar_h - 5}" class="lbl" text-anchor="end">{_e(label[:42])}</text>')
        est = iv.get("estimate")
        if est is None:
            parts.append(f'<text x="{left + 6}" y="{y + bar_h - 5}" class="tick">no assessed trials</text>')
            continue
        w = width * est
        parts.append(f'<rect x="{left}" y="{y}" width="{max(w, 1):.1f}" height="{bar_h}" rx="3" fill="{color}"/>')
        if iv.get("low") is not None:
            x1, x2 = left + width * iv["low"], left + width * iv["high"]
            cy = y + bar_h / 2
            parts.append(f'<line x1="{x1:.1f}" y1="{cy}" x2="{x2:.1f}" y2="{cy}" class="ci"/>'
                         f'<line x1="{x1:.1f}" y1="{cy - 5}" x2="{x1:.1f}" y2="{cy + 5}" class="ci"/>'
                         f'<line x1="{x2:.1f}" y1="{cy - 5}" x2="{x2:.1f}" y2="{cy + 5}" class="ci"/>')
        parts.append(f'<text x="{left + width + 8}" y="{y + bar_h - 5}" class="val">{_pct(est)}</text>')
    parts.append("</svg>")
    return "".join(parts)


def _latency_svg(rows: list[tuple[str, dict, str]]) -> str:
    vals = [s for _, s, _ in rows if s.get("p50") is not None]
    if not vals:
        return "<p class='muted'>No latency measurements (imported outputs or no completed trials).</p>"
    vmax = max(s["max"] for s in vals) or 1
    bar_h, gap, left, width = 14, 16, 280, 360
    h = len(rows) * (bar_h + gap) + 30
    parts = [f'<svg viewBox="0 0 {left + width + 90} {h}" role="img" aria-label="Latency distribution by arm" class="chart">']
    for t in range(5):
        v = vmax * t / 4
        x = left + width * t / 4
        parts.append(f'<line x1="{x}" y1="0" x2="{x}" y2="{h - 22}" class="grid"/>'
                     f'<text x="{x}" y="{h - 6}" class="tick" text-anchor="middle">{_ms(v)}</text>')
    sx = lambda v: left + width * v / vmax  # noqa: E731
    for i, (label, s, color) in enumerate(rows):
        y = i * (bar_h + gap) + 4
        parts.append(f'<text x="{left - 10}" y="{y + bar_h - 3}" class="lbl" text-anchor="end">{_e(label[:42])}</text>')
        if s.get("p50") is None:
            continue
        cy = y + bar_h / 2
        parts.append(f'<line x1="{sx(s["min"]):.1f}" y1="{cy}" x2="{sx(s["max"]):.1f}" y2="{cy}" class="ci"/>'
                     f'<rect x="{sx(s["p50"]) - 1:.1f}" y="{y}" width="{max(2, sx(s["p95"]) - sx(s["p50"])):.1f}" height="{bar_h}" rx="3" fill="{color}" opacity=".55"/>'
                     f'<line x1="{sx(s["p50"]):.1f}" y1="{y - 2}" x2="{sx(s["p50"]):.1f}" y2="{y + bar_h + 2}" stroke="{color}" stroke-width="3"/>'
                     f'<text x="{left + width + 8}" y="{y + bar_h - 3}" class="val">p50 {_ms(s["p50"])} · p95 {_ms(s["p95"])}</text>')
    parts.append("</svg>")
    return "".join(parts)


CSS = """
:root{--bg:#0d0d0d;--panel:#161615;--ink:#f4f3ee;--ink2:#c3c2b7;--muted:#898781;--line:#2c2c2a;--pass:#0ca30c;--fail:#d03b3b;--warn:#fab219}
@media (prefers-color-scheme: light){:root{--bg:#f9f9f7;--panel:#fff;--ink:#0b0b0b;--ink2:#52514e;--muted:#6f6d68;--line:#e1e0d9}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1080px;margin:0 auto;padding:32px 20px 80px}h1{font-size:24px;margin:0 0 4px}h2{font-size:16px;margin:36px 0 10px;letter-spacing:.02em;text-transform:uppercase;color:var(--ink2)}
.mono,code,td.num{font-family:ui-monospace,"Cascadia Code","JetBrains Mono",Menlo,monospace;font-size:12.5px}
.muted{color:var(--muted)}.panel{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:16px 18px;margin:10px 0}
table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;padding:7px 8px;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--muted);font-weight:500}
td.num{text-align:right;font-variant-numeric:tabular-nums}.chip{display:inline-block;padding:2px 8px;border:1px solid var(--line);border-radius:999px;font-size:12px;margin:2px 4px 2px 0}
.v-pass{color:var(--pass)}.v-fail{color:var(--fail)}.v-insufficient_evidence{color:var(--warn)}.v-invalid_experiment{color:var(--fail)}
.chart{width:100%;height:auto}.chart .grid{stroke:var(--line)}.chart .tick{fill:var(--muted);font-size:11px}.chart .lbl{fill:var(--ink2);font-size:12px}
.chart .val{fill:var(--ink);font-size:12px}.chart .ci{stroke:var(--ink);stroke-width:1.5}.dot{display:inline-block;width:10px;height:10px;border-radius:3px;margin-right:6px;vertical-align:-1px}
pre{white-space:pre-wrap;word-break:break-word;margin:0}footer{margin-top:40px;color:var(--muted);font-size:12px}
"""


def render_html(b: dict[str, Any]) -> str:
    r, c, plan = b["results"], b["campaign"], b["plan"]
    arms = r["arms"]
    color = {a["key"]: SERIES[i % len(SERIES)] for i, a in enumerate(arms)}
    label = {a["key"]: a["label"] for a in arms}
    S = r["summaries"]
    out = [f"<!doctype html><html lang='en'><head><meta charset='utf-8'><meta name='viewport' content='width=device-width,initial-scale=1'>"
           f"<title>Lab of Night and Flame report — {_e(c['name'])}</title><style>{CSS}</style></head><body><main>"]
    out.append(f"<div class='muted mono'>Lab of Night and Flame · campaign {_e(c['id'])}</div><h1>{_e(c['name'])}</h1>")
    if c.get("hypothesis"):
        out.append(f"<p class='muted'>Hypothesis: {_e(c['hypothesis'])}</p>")
    out.append("<div>" + "".join(f"<span class='chip'>{_e(LABEL_TEXT.get(l, l))}</span>" for l in r["labels"]) + "</div>")
    note = r["gate"].get("note")
    out.append("<h2>Qualification gate</h2><div class='panel'>")
    if note:
        out.append(f"<p class='v-insufficient_evidence'>{_e(note)}</p>")
    out.append("<table><tr><th>Arm</th><th>Verdict</th><th>Checks</th></tr>")
    for k, g in r["gate"]["arms"].items():
        checks = "<br>".join(f"<span class='v-{_e(ch['result'])}'>{_e(ch['result'])}</span> · {_e(ch['check'])}: {_e(ch['detail'])}" for ch in g["checks"])
        base = " <span class='muted'>(baseline)</span>" if g["is_baseline"] else ""
        out.append(f"<tr><td><span class='dot' style='background:{color[k]}'></span>{_e(label[k])}{base}</td>"
                   f"<td class='v-{_e(g['verdict'])}'><b>{VERDICT_LABEL.get(g['verdict'], g['verdict'])}</b></td><td>{checks}</td></tr>")
    out.append("</table><p class='muted'>Execution status is reported separately from the gate: a completed campaign is not a pass.</p></div>")

    prim = r["primary_scorer"]
    out.append(f"<h2>Primary metric — {_e(prim)} pass rate (95% Wilson CI)</h2><div class='panel'>")
    out.append(_ci_bars_svg([(label[k], S[k]["primary"]["pass_rate"], color[k]) for k in S]))
    out.append("</div>")
    out.append("<h2>Summary by arm</h2><div class='panel'><table><tr><th>Arm</th><th>Planned</th><th>Assessed</th><th>Pass rate</th>"
               "<th>All-admitted</th><th>Errors</th><th>p50 / p95 latency</th><th>TTFT p50</th><th>Cost (known)</th><th>Cost / pass</th></tr>")
    for k, s in S.items():
        pr = s["primary"]["pass_rate"]
        cost = s["cost"]
        out.append(
            f"<tr><td><span class='dot' style='background:{color[k]}'></span>{_e(label[k])}</td><td class='num'>{s['planned']}</td>"
            f"<td class='num'>{s['primary']['assessed']}</td><td class='num'>{_pct(pr['estimate'])} [{_pct(pr['low'])}, {_pct(pr['high'])}]</td>"
            f"<td class='num'>{_pct(s['primary']['all_admitted_rate']['estimate'])}</td><td class='num'>{s['errors']}</td>"
            f"<td class='num'>{_ms(s['latency_ms']['p50'])} / {_ms(s['latency_ms']['p95'])}</td><td class='num'>{_ms(s['ttft_ms']['p50'])}</td>"
            f"<td class='num'>{_usd(cost['total_known_usd'])}{'' if cost['complete'] else ' + unknown'}</td><td class='num'>{_usd(cost['per_pass_usd'])}</td></tr>")
    out.append("</table><p class='muted'>Pass rate = passes / assessed (pass+fail). All-admitted = passes / planned "
               "(errors and missing assessments count as non-pass). Unknown costs are never shown as $0.</p></div>")

    if r["comparisons"]:
        out.append(f"<h2>Paired comparison vs baseline — {_e(label.get(r['baseline'], ''))}</h2><div class='panel'><table>"
                   "<tr><th>Candidate</th><th>Paired samples</th><th>W / L / T</th><th>Δ pass rate [95% CI]</th><th>p (McNemar)</th></tr>")
        for cm in r["comparisons"]:
            d = cm["diff"] or {}
            out.append(f"<tr><td>{_e(label[cm['candidate']])}</td><td class='num'>{cm['paired_samples']}</td>"
                       f"<td class='num'>{cm['wins']} / {cm['losses']} / {cm['ties']}</td>"
                       f"<td class='num'>{_pct(d.get('estimate'), True)} [{_pct(d.get('low'), True)}, {_pct(d.get('high'), True)}]</td>"
                       f"<td class='num'>{'—' if cm['mcnemar_p'] is None else f'{cm['mcnemar_p']:.4f}'}</td></tr>")
        out.append("</table></div>")

    out.append("<h2>Latency distribution (min–max, p50 marker, p50–p95 band)</h2><div class='panel'>")
    out.append(_latency_svg([(label[k], S[k]["latency_ms"], color[k]) for k in S]))
    out.append("</div>")

    if r["slices"]:
        out.append("<h2>Slices</h2>")
        for sl in r["slices"]:
            tag = " <span class='muted'>(exploratory — not pre-registered)</span>" if sl["exploratory"] else ""
            out.append(f"<div class='panel'><b>{_e(sl['key'])}</b>{tag}<table><tr><th>Value</th>"
                       + "".join(f"<th>{_e(label[a['key']][:28])}</th>" for a in arms) + "</tr>")
            for row in sl["rows"]:
                cells = "".join(f"<td class='num'>{_pct(row['arms'][a['key']]['estimate'])} <span class='muted'>n={row['arms'][a['key']]['n']}</span></td>" for a in arms)
                out.append(f"<tr><td>{_e(row['value'])}</td>{cells}</tr>")
            out.append("</table></div>")

    out.append("<h2>Failure samples</h2><div class='panel'>")
    if not b["failures"]:
        out.append("<p class='muted'>No failed or inconclusive trials.</p>")
    else:
        out.append("<table><tr><th>Sample</th><th>Arm</th><th>Outcome</th><th>Output (truncated)</th><th>Expected</th></tr>")
        for f in b["failures"][:40]:
            exp = f["expected"] if isinstance(f["expected"], str) else json.dumps(f["expected"], ensure_ascii=False)
            oc = f["error_category"] and f"error: {f['error_category']}" or f["outcome"]
            out.append(f"<tr><td class='mono'>{_e(f['sample_key'])}</td><td>{_e(label.get(f['arm_key'], f['arm_key']))}</td>"
                       f"<td>{_e(oc)}</td><td><pre>{_e((f['output'] or '')[:300])}</pre></td><td><pre>{_e((exp or '')[:200])}</pre></td></tr>")
        out.append("</table>")
    out.append("</div>")

    out.append("<h2>Manifest &amp; reproducibility</h2><div class='panel'><table>")
    ds = r["dataset"]
    rows = [
        ("Dataset", f"{ds['name']} v{ds['version']} ({ds['modality']})"), ("Dataset digest", ds["digest"]),
        ("Plan digest", c["plan_digest"]), ("Seed / order", f"{plan['protocol']['seed']} / {plan['protocol']['order']}"),
        ("Repetitions", plan["protocol"]["repetitions"]), ("Splits", ", ".join(plan["protocol"]["splits"])),
        ("Scorers", ", ".join(f"{s['id']}={s['key']}" for s in r["scorers"]) + f" (revision {r['scoring_revision']})"),
        ("Budget cap / spent", f"{_usd(c['budget_cap_usd'])} / {_usd(c['spent_usd'])} (+{c['unknown_cost_attempts']} unknown-cost attempts)"),
        ("Attempts", f"{b['attempts']['n']} total, {b['attempts']['ok'] or 0} completed, {b['attempts']['unknown'] or 0} unknown outcome"),
        ("Methods", "; ".join(f"{k}: {v}" for k, v in r["methods"].items())),
    ]
    for a in arms:
        rows.append((f"Arm {a['key']}", f"{a['provider']}/{a['model']} params={json.dumps(a['params'])} prompt={a['prompt_id']}"))
    out += [f"<tr><th>{_e(k)}</th><td class='mono'>{_e(v)}</td></tr>" for k, v in rows]
    out.append("</table></div>")
    out.append("<h2>Limitations</h2><div class='panel'><ul>"
               "<li>Results apply only to the dataset version, splits, prompts and parameters listed above.</li>"
               "<li>Provider-hosted models may change behind a stable model name; resolved model ids are recorded per attempt.</li>"
               "<li>Model-judge verdicts are not ground truth unless the judge has been calibrated against reviewed labels.</li>"
               "<li>Latency is client-observed end-to-end time and includes network and provider queueing.</li></ul></div>")
    out.append(f"<footer>Generated {time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime(b['generated_at']))} by Lab of Night and Flame "
               f"v{_e(b['lab_version'])}.</footer></main></body></html>")
    return "".join(out)


def render_markdown(b: dict[str, Any]) -> str:
    r, c, plan = b["results"], b["campaign"], b["plan"]
    label = {a["key"]: a["label"] for a in r["arms"]}
    S = r["summaries"]
    L = [f"# Lab of Night and Flame report — {c['name']}", "", f"Campaign `{c['id']}` · status **{c['status']}**", ""]
    if c.get("hypothesis"):
        L += [f"> Hypothesis: {c['hypothesis']}", ""]
    L += ["Labels: " + ", ".join(LABEL_TEXT.get(l, l) for l in r["labels"]), ""]
    L += ["## Qualification gate", ""]
    if r["gate"].get("note"):
        L += [f"_{r['gate']['note']}_", ""]
    L += ["| Arm | Verdict | Checks |", "|---|---|---|"]
    for k, g in r["gate"]["arms"].items():
        checks = "<br>".join(f"{ch['result']} · {ch['check']}: {ch['detail']}" for ch in g["checks"])
        L.append(f"| {label[k]}{' (baseline)' if g['is_baseline'] else ''} | **{VERDICT_LABEL.get(g['verdict'])}** | {checks} |")
    L += ["", f"## Summary (primary scorer: `{r['primary_scorer']}`)", "",
          "| Arm | Planned | Assessed | Pass rate [95% CI] | All-admitted | Errors | p50 / p95 | Cost (known) | Cost/pass |",
          "|---|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for k, s in S.items():
        pr = s["primary"]["pass_rate"]
        L.append(f"| {label[k]} | {s['planned']} | {s['primary']['assessed']} | {_pct(pr['estimate'])} [{_pct(pr['low'])}, {_pct(pr['high'])}] | "
                 f"{_pct(s['primary']['all_admitted_rate']['estimate'])} | {s['errors']} | {_ms(s['latency_ms']['p50'])} / {_ms(s['latency_ms']['p95'])} | "
                 f"{_usd(s['cost']['total_known_usd'])}{'' if s['cost']['complete'] else ' + unknown'} | {_usd(s['cost']['per_pass_usd'])} |")
    if r["comparisons"]:
        L += ["", f"## Paired comparison vs baseline ({label.get(r['baseline'])})", "",
              "| Candidate | Pairs | W/L/T | Δ [95% CI] | p (McNemar) |", "|---|---:|---:|---:|---:|"]
        for cm in r["comparisons"]:
            d = cm["diff"] or {}
            p = "—" if cm["mcnemar_p"] is None else f"{cm['mcnemar_p']:.4f}"
            L.append(f"| {label[cm['candidate']]} | {cm['paired_samples']} | {cm['wins']}/{cm['losses']}/{cm['ties']} | "
                     f"{_pct(d.get('estimate'), True)} [{_pct(d.get('low'), True)}, {_pct(d.get('high'), True)}] | {p} |")
    ds = r["dataset"]
    L += ["", "## Manifest", "", f"- Dataset: {ds['name']} v{ds['version']} (`{ds['digest']}`)", f"- Plan digest: `{c['plan_digest']}`",
          f"- Seed {plan['protocol']['seed']}, order {plan['protocol']['order']}, repetitions {plan['protocol']['repetitions']}",
          f"- Budget cap {_usd(c['budget_cap_usd'])}, spent {_usd(c['spent_usd'])}, unknown-cost attempts {c['unknown_cost_attempts']}",
          f"- Methods: " + "; ".join(f"{k}: {v}" for k, v in r["methods"].items())]
    for a in r["arms"]:
        L.append(f"- Arm `{a['key']}`: {a['provider']}/{a['model']} `{json.dumps(a['params'])}` prompt `{a['prompt_id']}`")
    L += ["", "## Limitations", "", "- Results are scoped to the listed dataset version, prompts and parameters.",
          "- Model-judge verdicts are not ground truth without calibration.", ""]
    return "\n".join(L)


def render_json(b: dict[str, Any]) -> dict[str, Any]:
    body = {"schema": "arl-report-v1", **b}
    digest = hashlib.sha256(orjson.dumps(body, option=orjson.OPT_SORT_KEYS)).hexdigest()
    body["checksum"] = {"algorithm": "sha256", "over": "report body without this field (sorted keys)", "value": digest}
    return body
