// Package cli implements the lab binary's client subcommands.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

type M = map[string]any

// Suite runs a Workbench regression suite against a lab and exits non-zero
// when the gate fails, for use in CI:
//
//	arl suite --url http://localhost:8000 --collection support --model gpt-4.1-mini --baseline latest --min-pass-rate 0.9
//
// Exit codes: 0 gate passed, 1 gate failed, 2 usage or connection error.
func Suite(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("suite", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", env("ARL_URL", "http://localhost:8000"), "lab base URL (ARL_URL)")
	token := fs.String("token", env("ARL_AUTH_TOKEN", os.Getenv("LAB_TOKEN")), "owner token (ARL_AUTH_TOKEN or LAB_TOKEN)")
	project := fs.String("project", os.Getenv("ARL_PROJECT"), "project id (default project when empty)")
	collection := fs.String("collection", "", "Workbench collection to run (required)")
	mode := fs.String("mode", "", "llm or agent (default: each scenario's own mode)")
	model := fs.String("model", "", "override config.model")
	provider := fs.String("provider", "", "override config.provider")
	promptFile := fs.String("system-prompt-file", "", "override config.system_prompt with this file's contents")
	repeats := fs.Int("repeats", 1, "runs per scenario (1-20)")
	baseline := fs.String("baseline", "latest", "baseline suite id, 'latest' or 'none'")
	minPass := fs.Float64("min-pass-rate", -1, "fail when the pass rate is below this (0-1)")
	maxReg := fs.Int("max-regressions", 0, "regressed scenarios allowed against the baseline")
	maxCost := fs.Float64("max-cost-usd", -1, "fail when the suite costs more than this")
	label := fs.String("label", env("GITHUB_SHA", ""), "label stored on the suite (e.g. commit)")
	timeout := fs.Duration("timeout", 15*time.Minute, "give up waiting after this long")
	asJSON := fs.Bool("json", false, "print the suite result as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *collection == "" {
		fmt.Fprintln(stderr, "arl suite: --collection is required")
		return 2
	}
	overrides := M{}
	if *model != "" {
		overrides["model"] = *model
	}
	if *provider != "" {
		overrides["provider"] = *provider
	}
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			fmt.Fprintln(stderr, "arl suite: cannot read", *promptFile)
			return 2
		}
		overrides["system_prompt"] = string(b)
	}
	gate := M{"max_regressions": *maxReg}
	if *minPass >= 0 {
		gate["min_pass_rate"] = *minPass
	}
	if *maxCost >= 0 {
		gate["max_cost_usd"] = *maxCost
	}
	c := client{base: strings.TrimRight(*url, "/"), token: *token, project: *project, http: &http.Client{Timeout: 60 * time.Second}}
	suite, err := c.do("POST", "/api/workbench/suites", M{"collection": *collection, "mode": *mode, "overrides": overrides, "repeats": *repeats, "baseline": *baseline, "gate": gate, "label": *label})
	if err != nil {
		fmt.Fprintln(stderr, "arl suite:", err)
		return 2
	}
	id, _ := suite["id"].(string)
	fmt.Fprintf(stderr, "suite %s started: %v runs over %v scenarios (baseline %v)\n", id, suite["dispatched"], suite["scenarios"], or(suite["baseline_suite_id"], "none"))
	deadline := time.Now().Add(*timeout)
	for {
		suite, err = c.do("GET", "/api/workbench/suites/"+id, nil)
		if err != nil {
			fmt.Fprintln(stderr, "arl suite:", err)
			return 2
		}
		if suite["status"] == "completed" {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(stderr, "arl suite: timed out waiting for", id)
			return 2
		}
		time.Sleep(2 * time.Second)
	}
	result, _ := suite["result"].(M)
	if *asJSON {
		delete(suite, "runs")
		_ = json.NewEncoder(stdout).Encode(suite)
	} else {
		report(stdout, result)
	}
	g, _ := result["gate"].(M)
	if g["status"] != "passed" {
		return 1
	}
	return 0
}

func or(v any, def string) any {
	if v == nil || v == "" {
		return def
	}
	return v
}

func report(w io.Writer, result M) {
	t := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(t, "SCENARIO\tPASS\tFAIL\tUNKNOWN\tRATE")
	for _, raw := range list(result["scenarios"]) {
		s, _ := raw.(M)
		fmt.Fprintf(t, "%v\t%v\t%v\t%v\t%s\n", s["name"], s["pass"], s["fail"], s["unknown"], pct(s["pass_rate"]))
	}
	_ = t.Flush()
	rate, _ := result["pass_rate"].(M)
	fmt.Fprintf(w, "\npass rate %s (95%% CI %s–%s), cost $%.4f\n", pct(rate["estimate"]), pct(rate["low"]), pct(rate["high"]), num(result["cost_usd"]))
	if cmp, ok := result["comparison"].(M); ok {
		fmt.Fprintf(w, "vs baseline %v: %d regressed, %d improved\n", cmp["baseline_suite_id"], len(list(cmp["regressions"])), len(list(cmp["improvements"])))
		for _, raw := range list(cmp["regressions"]) {
			r, _ := raw.(M)
			fmt.Fprintf(w, "  ✗ %v: %s → %s\n", r["name"], pct(r["before"]), pct(r["after"]))
		}
	}
	g, _ := result["gate"].(M)
	fmt.Fprintf(w, "gate: %v", g["status"])
	if reasons := list(g["reasons"]); len(reasons) > 0 {
		fmt.Fprintf(w, " (%v)", reasons)
	}
	fmt.Fprintln(w)
	for _, n := range list(g["notes"]) {
		fmt.Fprintln(w, "note:", n)
	}
}

func list(v any) []any  { a, _ := v.([]any); return a }
func num(v any) float64 { f, _ := v.(float64); return f }
func pct(v any) string {
	f, ok := v.(float64)
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", f*100)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type client struct {
	base, token, project string
	http                 *http.Client
}

func (c client) do(method, path string, body any) (M, error) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.project != "" {
		req.Header.Set("X-Lab-Project", c.project)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the lab at %s", c.base)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	var out M
	_ = json.Unmarshal(data, &out)
	if res.StatusCode >= 400 {
		detail := out["detail"]
		if d, ok := detail.(M); ok {
			detail = d["message"]
		}
		return nil, errors.New(fmt.Sprint("HTTP ", res.StatusCode, ": ", or(detail, strings.TrimSpace(string(data)))))
	}
	return out, nil
}
