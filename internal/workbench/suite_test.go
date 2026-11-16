package workbench

import (
	"testing"

	"go.vicoworks.com/agent-reliability-lab/internal/domain"
)

func run(scenario, status, verdict string) M {
	return M{"scenario_id": scenario, "mode": "llm", "status": status, "verdict": verdict, "created_at": 1., "request": M{"name": scenario}, "result": M{"latency_ms": 10., "cost_usd": 0.01}}
}

func TestSuiteGateAndRegressions(t *testing.T) {
	suite := M{"gate": M{"min_pass_rate": 0.5}}
	base := Summarize(suite, []M{run("a", "completed", "pass"), run("b", "completed", "no_checks"), run("c", "failed", "fail")}, nil, 100)
	if domain.Obj(base["gate"])["status"] != "passed" || base["pass"] != 2 {
		t.Fatal(base)
	}
	cand := Summarize(M{"gate": M{}}, []M{run("a", "completed", "fail"), run("b", "completed", "pass"), run("c", "completed", "pass")}, base, 100)
	cmp := domain.Obj(cand["comparison"])
	if len(domain.Arr(cmp["regressions"])) != 1 || len(domain.Arr(cmp["improvements"])) != 1 || domain.Obj(cand["gate"])["status"] != "failed" {
		t.Fatal(cand)
	}
	pending := Summarize(suite, []M{run("a", "queued", "")}, nil, 100)
	if domain.Obj(pending["gate"])["status"] != "pending" {
		t.Fatal(pending)
	}
	stale := run("a", "dispatched", "")
	stale["mode"] = "agent"
	if Outcome(stale, StaleAfter+10) != "fail" || Outcome(stale, 5) != "pending" {
		t.Fatal("a lost agent replay must fail after the stale window")
	}
}

func TestApplyOverridesMergesParams(t *testing.T) {
	s := Apply(M{"config": M{"model": "a", "params": M{"temperature": 0.2, "seed": 1}}}, M{"model": "b", "params": M{"temperature": 0.9}})
	c := domain.Obj(s["config"])
	if c["model"] != "b" || domain.Obj(c["params"])["temperature"] != 0.9 || domain.Num(domain.Obj(c["params"])["seed"]) != 1 {
		t.Fatal(c)
	}
}
