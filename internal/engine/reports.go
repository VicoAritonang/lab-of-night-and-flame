package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"html"
	"strings"
)

func (e *Engine) Report(ctx context.Context, cid string) (M, error) {
	if _, err := e.Analyze(ctx, cid); err != nil {
		return nil, err
	}
	results, err := e.DB.Get(ctx, "results", cid)
	if err != nil {
		return nil, err
	}
	c, err := e.DB.Get(ctx, "campaigns", cid)
	if err != nil {
		return nil, err
	}
	rows, err := e.DB.Pool.Query(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind='trials' AND data->>'campaign_id'=$2 ORDER BY (data->>'order_index')::integer", e.DB.Project, cid)
	if err != nil {
		return nil, err
	}
	trials := []M{}
	size := 0
	for rows.Next() {
		var t M
		if err = rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		size += len(domain.JSON(t))
		if size > 32<<20 {
			rows.Close()
			return nil, errors.New("report exceeds 32 MiB; export trial pages instead")
		}
		trials = append(trials, t)
	}
	rows.Close()
	attempts, err := e.DB.List(ctx, "attempts", M{"campaign_id": cid}, 350000)
	if err != nil {
		return nil, err
	}
	assess, err := e.DB.List(ctx, "assessments", M{"campaign_id": cid}, 600000)
	if err != nil {
		return nil, err
	}
	bundle := M{"schema": "arl-report-go-v1", "campaign": c, "manifest": c["plan"], "results": results, "trials": trials, "attempts": attempts, "assessments": assess, "labels": c["labels"], "limitations": []string{"Synthetic and fake targets measure lab runtime, not model intelligence", "LLM judge decisions require calibration", "Incomplete cost evidence is not zero spending", "Exploratory slices are not corrected for multiplicity"}}
	checksum := domain.Hash(bundle)
	bundle["checksum"] = M{"algorithm": "sha256", "value": checksum, "scope": "canonical JSON excluding checksum"}
	jsonBody := string(domain.JSON(bundle))
	if len(jsonBody) > 48<<20 {
		return nil, errors.New("report exceeds 48 MiB; export trial pages instead")
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\nCampaign: %s\n\nStatus: **%v**\n\nLabels: %v\n\nFAKE and synthetic runs are demonstrations, not model-quality claims.\n\n| Arm | Pass / assessed | 95%% Wilson interval | Known cost USD | Verdict |\n|---|---:|---|---:|---|\n", domain.Str(c["name"]), cid, c["status"], c["labels"])
	var table strings.Builder
	for _, arm := range domain.Maps(results["arms"]) {
		key := domain.Str(arm["key"])
		s := domain.Obj(domain.Obj(results["summaries"])[key])
		p := domain.Obj(s["primary"])
		iv := domain.Obj(p["pass_rate"])
		cost := domain.Obj(s["cost"])
		gate := domain.Obj(domain.Obj(domain.Obj(results["gate"])["arms"])[key])
		fmt.Fprintf(&md, "| %s | %v / %v | %v – %v | %.6f | %v |\n", strings.ReplaceAll(domain.Str(arm["label"]), "|", "/"), p["passes"], p["assessed"], iv["low"], iv["high"], domain.Num(cost["total_known_usd"]), gate["verdict"])
		fmt.Fprintf(&table, "<tr><td>%s</td><td>%v / %v</td><td>%v – %v</td><td>%.6f</td><td>%s</td></tr>", html.EscapeString(domain.Str(arm["label"])), p["passes"], p["assessed"], iv["low"], iv["high"], domain.Num(cost["total_known_usd"]), html.EscapeString(domain.Str(gate["verdict"])))
	}
	fmt.Fprintf(&md, "\nMethods: 95%% Wilson intervals, paired sample bootstrap (2000 replicates), exact McNemar. Execution completion is separate from acceptance.\n\nChecksum: %s\n", checksum)
	htmlBody := fmt.Sprintf("<!doctype html><html lang='en'><meta charset='utf-8'><meta name='viewport' content='width=device-width'><title>%s</title><style>body{font:15px system-ui;max-width:1100px;margin:40px auto;padding:20px;color:#202835;background:#f8f9fb}table{border-collapse:collapse;width:100%%}td,th{padding:12px;border-bottom:1px solid #ccd3dd;text-align:left}.notice{padding:15px;background:#fff0cc}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><h1>%s</h1><p>Campaign %s · Status %s</p><p class='notice'>Labels: %s. FAKE/synthetic runs demonstrate the lab runtime; they do not measure model intelligence.</p><table><thead><tr><th>Arm</th><th>Pass / assessed</th><th>95%% Wilson interval</th><th>Known cost USD</th><th>Gate</th></tr></thead><tbody>%s</tbody></table><h2>Methods and limitations</h2><p>Wilson confidence intervals, paired sample bootstrap with 2000 resamples, exact McNemar. Cost unknowns remain unknown. LLM judges need calibration; exploratory slices are not corrected for multiplicity.</p><h2>Frozen manifest</h2><pre>%s</pre><p>Checksum SHA256: %s</p></html>", html.EscapeString(domain.Str(c["name"])), html.EscapeString(domain.Str(c["name"])), cid, html.EscapeString(domain.Str(c["status"])), html.EscapeString(fmt.Sprint(c["labels"])), table.String(), html.EscapeString(string(domain.JSON(c["plan"]))), checksum)
	doc := M{"json": jsonBody, "md": md.String(), "html": htmlBody}
	if err = e.DB.Put(ctx, "reports", cid, doc); err != nil {
		return nil, err
	}
	return M{"result_kind": "reports", "result_id": cid}, nil
}
