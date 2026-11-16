package server_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/cli"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
)

func TestPromptRegistryVersionsLabelsAndRender(t *testing.T) {
	f := setup(t)
	v1 := f.request("POST", "/api/prompts", M{"name": "support.answer", "description": "Answers tickets", "template": M{"system": "You help {{company}} customers.", "messages": []M{{"role": "user", "content": "{{ question }}"}}}, "config": M{"model": "fake/echo", "provider": "fake"}, "message": "first", "labels": []any{"production"}})
	if v1["version"] != 1. || v1["created"] != true || len(domain.Arr(v1["variables"])) != 2 {
		t.Fatal(v1)
	}
	same := f.request("POST", "/api/prompts/support.answer/versions", M{"template": M{"system": "You help {{company}} customers.", "messages": []M{{"role": "user", "content": "{{ question }}"}}}, "config": M{"model": "fake/echo", "provider": "fake"}})
	if same["version"] != 1. || same["created"] != false {
		t.Fatal("identical commit must not create a version", same)
	}
	v2 := f.request("POST", "/api/prompts/support.answer/versions", M{"template": M{"system": "You help {{company}} customers. Be brief."}, "labels": []any{"staging"}})
	if v2["version"] != 2. {
		t.Fatal(v2)
	}
	if got := f.request("GET", "/api/prompts/support.answer", nil); got["version"] != 1. {
		t.Fatal("default resolves production", got)
	}
	if got := f.request("GET", "/api/prompts/support.answer?label=latest", nil); got["version"] != 2. {
		t.Fatal(got)
	}
	f.request("POST", "/api/prompts/support.answer/labels", M{"label": "production", "version": 2})
	if got := f.request("GET", "/api/prompts/support.answer?label=production", nil); got["version"] != 2. {
		t.Fatal(got)
	}
	rendered := f.request("POST", "/api/prompts/support.answer/render", M{"version": 1, "variables": M{"company": "Acme"}})
	msgs := domain.Maps(rendered["messages"])
	if msgs[0]["content"] != "You help Acme customers." || domain.Strings(rendered["missing"])[0] != "question" {
		t.Fatal(rendered)
	}
	if code, _ := f.raw("POST", "/api/prompts", "application/json", []byte(`{"name":"Bad Name","template":{"text":"x"}}`)); code != 422 {
		t.Fatal("invalid slug accepted", code)
	}
	if versions := f.list("/api/prompts/support.answer/versions"); len(versions) != 2 {
		t.Fatal(versions)
	}
	f.request("DELETE", "/api/prompts/support.answer", nil)
	if code, _ := f.raw("GET", "/api/prompts/support.answer", "", nil); code != 404 {
		t.Fatal("deleted prompt still resolves", code)
	}
}

func (f *fixture) waitSuite(id string) M {
	f.t.Helper()
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		s := f.request("GET", "/api/workbench/suites/"+id, nil)
		if s["status"] == "completed" {
			return s
		}
		time.Sleep(60 * time.Millisecond)
	}
	f.t.Fatal("suite timed out", id)
	return nil
}

// A collection of LLM-step scenarios is run as a suite; a second suite with a
// model that breaks one scenario regresses against the first and fails the gate.
func TestRegressionSuiteGate(t *testing.T) {
	f := setup(t)
	for _, q := range []string{"refund policy", "shipping time"} {
		f.request("POST", "/api/workbench/scenarios", M{"name": q, "collection": "CI", "mode": "llm", "input": q, "config": M{"provider": "fake", "model": "fake/echo"}, "checks": M{"output_contains": []any{q}, "max_latency_ms": 300}})
	}
	base := f.request("POST", "/api/workbench/suites", M{"collection": "CI", "repeats": 2, "gate": M{"min_pass_rate": 0.9}})
	done := f.waitSuite(domain.Str(base["id"]))
	res := domain.Obj(done["result"])
	if domain.Obj(res["gate"])["status"] != "passed" || res["pass"] != 4. || len(domain.Arr(done["runs"])) != 4 {
		t.Fatal(res)
	}
	// fake/slow answers after 500 ms, so the latency check fails everywhere.
	candidate := f.request("POST", "/api/workbench/suites", M{"collection": "CI", "baseline": "latest", "overrides": M{"model": "fake/slow", "system_prompt": "x"}})
	if candidate["baseline_suite_id"] != base["id"] {
		t.Fatal(candidate)
	}
	done = f.waitSuite(domain.Str(candidate["id"]))
	res = domain.Obj(done["result"])
	cmp := domain.Obj(res["comparison"])
	gate := domain.Obj(res["gate"])
	if gate["status"] != "failed" || len(domain.Arr(cmp["regressions"])) != 2 || domain.Int(cmp["paired_scenarios"]) != 2 {
		t.Fatal(res)
	}
	if run := domain.Maps(done["runs"])[0]; domain.Obj(domain.Obj(run["request"])["config"])["model"] != "fake/slow" {
		t.Fatal("overrides not applied", run)
	}
	if code, _ := f.raw("POST", "/api/workbench/suites", "application/json", []byte(`{"collection":"missing"}`)); code != 422 {
		t.Fatal(code)
	}

	// The CI command: the regressing model fails against the good baseline.
	var out, errOut bytes.Buffer
	if code := cli.Suite([]string{"--url", f.HTTP.URL, "--collection", "CI", "--baseline", domain.Str(base["id"]), "--model", "fake/slow", "--timeout", "30s"}, &out, &errOut); code != 1 {
		t.Fatal("expected gate failure", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "2 regressed") || !strings.Contains(out.String(), "gate: failed") {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := cli.Suite([]string{"--url", f.HTTP.URL, "--collection", "CI", "--baseline", "none", "--min-pass-rate", "0.9", "--timeout", "30s"}, &out, &errOut); code != 0 {
		t.Fatal("expected gate pass", code, out.String(), errOut.String())
	}
}

func TestPerfConcurrencySweep(t *testing.T) {
	f := setup(t)
	if code, _ := f.raw("POST", "/api/perf", "application/json", []byte(`{"provider":"openai","model":"gpt-4.1-mini"}`)); code != 422 {
		t.Fatal("paid perf run must require confirmation", code)
	}
	run := f.request("POST", "/api/perf", M{"provider": "fake", "model": "fake/oracle-90", "concurrency": []any{4, 1}, "requests": 6})
	id := domain.Str(run["id"])
	var done M
	f.eventually("perf run", func() bool {
		done = f.request("GET", "/api/perf/"+id, nil)
		return done["status"] == "completed"
	})
	levels := domain.Maps(done["levels"])
	if len(levels) != 2 || levels[0]["concurrency"] != 1. || levels[1]["ok"] != 6. || domain.Num(levels[1]["requests_per_s"]) <= domain.Num(levels[0]["requests_per_s"]) {
		t.Fatal(levels)
	}
	if domain.Obj(done["saturation"])["concurrency"] == nil || domain.Obj(done["environment"])["cpus"] == nil {
		t.Fatal(done)
	}
}

// The safety probe pack runs as an ordinary campaign: an echoing model leaks
// every canary and refuses nothing, so injection/extraction/harmful probes fail
// while over-refusal probes pass.
func TestSafetyProbeCampaign(t *testing.T) {
	f := setup(t)
	p := plan(f.dataset("safety-probes"), "fake/echo")
	p["acceptance"] = M{"min_assessed_per_arm": 1}
	c := f.wait(f.launch(p))
	if c["status"] != "completed" {
		t.Fatal(c)
	}
	r := f.request("GET", "/api/campaigns/"+domain.Str(c["id"])+"/results", nil)
	for _, v := range domain.Obj(r["summaries"]) {
		s := domain.Obj(v)
		if domain.Int(s["completed"]) != 24 {
			t.Fatal(s)
		}
		// Over-refusal probes pass (6) and extraction probes pass (4) because
		// echo never sees the system-prompt canary; everything else leaks or complies.
		if passes := domain.Int(domain.Obj(s["primary"])["passes"]); passes != 10 {
			t.Fatal("unexpected echo safety passes", passes)
		}
	}
}

// "latest" must find a finished suite even if nobody has opened it yet.
func TestSuiteLatestBaselineWithoutReading(t *testing.T) {
	f := setup(t)
	f.request("POST", "/api/workbench/scenarios", M{"name": "q", "collection": "Lazy", "mode": "llm", "input": "q", "config": M{"provider": "fake", "model": "fake/echo"}, "checks": M{"output_contains": []any{"q"}}})
	first := f.request("POST", "/api/workbench/suites", M{"collection": "Lazy"})
	f.eventually("first suite runs", func() bool {
		for _, r := range f.list("/api/workbench/runs?limit=10") {
			if r["status"] != "completed" {
				return false
			}
		}
		return true
	})
	second := f.request("POST", "/api/workbench/suites", M{"collection": "Lazy", "baseline": "latest"})
	if second["baseline_suite_id"] != first["id"] {
		t.Fatal("unread completed suite not used as baseline", second)
	}
	done := f.waitSuite(domain.Str(first["id"]))
	if notes := domain.Arr(domain.Obj(domain.Obj(done["result"])["gate"])["notes"]); len(notes) != 1 {
		t.Fatal("a gate without conditions must say so", done["result"])
	}
}
