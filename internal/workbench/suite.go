package workbench

import (
	"errors"
	"sort"
	"strings"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/stats"
)

// Suites are regression runs: every scenario of a collection, optionally with
// overridden model/prompt/parameters, repeated N times and compared with a
// baseline suite. The gate turns the comparison into pass/fail for CI.
const Suites = "suites"

// StaleAfter is when a dispatched agent replay that never reported back counts
// as failed (agent crashed or disconnected).
const StaleAfter = 600.

// ValidateSuite normalizes a suite request.
func ValidateSuite(raw M) (M, error) {
	collection := strings.TrimSpace(domain.Str(raw["collection"]))
	if collection == "" {
		return nil, errors.New("collection is required")
	}
	mode := domain.Str(raw["mode"])
	if mode != "" && !domain.Has(Modes, mode) {
		return nil, errors.New("mode must be agent or llm")
	}
	repeats := domain.Int(raw["repeats"])
	if repeats == 0 {
		repeats = 1
	}
	if repeats < 1 || repeats > 20 {
		return nil, errors.New("repeats must be 1-20")
	}
	overrides := M{}
	for k, v := range domain.Obj(raw["overrides"]) {
		if !domain.Has([]string{"model", "provider", "system_prompt", "params"}, k) {
			return nil, errors.New("overrides may set model, provider, system_prompt and params")
		}
		if v != nil && v != "" {
			overrides[k] = v
		}
	}
	gate := M{}
	g := domain.Obj(raw["gate"])
	if g["min_pass_rate"] != nil {
		p := domain.Num(g["min_pass_rate"])
		if p < 0 || p > 1 {
			return nil, errors.New("gate.min_pass_rate must be between 0 and 1")
		}
		gate["min_pass_rate"] = p
	}
	if g["max_regressions"] != nil {
		if domain.Num(g["max_regressions"]) < 0 {
			return nil, errors.New("gate.max_regressions must be non-negative")
		}
		gate["max_regressions"] = domain.Int(g["max_regressions"])
	}
	if g["max_cost_usd"] != nil {
		gate["max_cost_usd"] = domain.Num(g["max_cost_usd"])
	}
	return M{"collection": collection, "mode": mode, "repeats": repeats, "overrides": overrides, "gate": gate, "baseline": strings.TrimSpace(domain.Str(raw["baseline"])), "label": domain.Str(raw["label"]), "commit": domain.Str(raw["commit"])}, nil
}

// Apply returns a scenario with the suite's overrides.
func Apply(scenario M, overrides M) M {
	s := domain.Copy(scenario)
	config := domain.Copy(domain.Obj(s["config"]))
	for k, v := range overrides {
		if k == "params" {
			params := domain.Copy(domain.Obj(config["params"]))
			for pk, pv := range domain.Obj(v) {
				params[pk] = pv
			}
			config["params"] = params
		} else {
			config[k] = v
		}
	}
	s["config"] = config
	return s
}

// Outcome classifies a finished run for gating: pass, fail or unknown
// (insufficient evidence). A completed run without checks passes.
func Outcome(run M, now float64) string {
	status := domain.Str(run["status"])
	if !Terminal(status) {
		if run["mode"] == "agent" && now-domain.Num(run["created_at"]) > StaleAfter {
			return "fail"
		}
		return "pending"
	}
	switch {
	case status != "completed" || run["verdict"] == "fail":
		return "fail"
	case run["verdict"] == "pass" || run["verdict"] == "no_checks" || run["verdict"] == nil:
		return "pass"
	}
	return "unknown"
}

type scenarioStats struct {
	pass, fail, unknown, pending int
}

func (s scenarioStats) rate() any {
	if s.pass+s.fail == 0 {
		return nil
	}
	return float64(s.pass) / float64(s.pass+s.fail)
}

// Summarize aggregates suite runs per scenario and overall, compares with a
// baseline summary (may be nil) and applies the gate.
func Summarize(suite M, runs []M, baseline M, now float64) M {
	per := map[string]*scenarioStats{}
	names := map[string]string{}
	latencies, costs := []float64{}, 0.
	unknownCost := 0
	total := scenarioStats{}
	for _, run := range runs {
		sid := domain.Str(run["scenario_id"])
		if per[sid] == nil {
			per[sid] = &scenarioStats{}
		}
		names[sid] = domain.Str(domain.Obj(run["request"])["name"])
		s := per[sid]
		switch Outcome(run, now) {
		case "pass":
			s.pass++
			total.pass++
		case "fail":
			s.fail++
			total.fail++
		case "unknown":
			s.unknown++
			total.unknown++
		default:
			s.pending++
			total.pending++
		}
		res := domain.Obj(run["result"])
		if res["latency_ms"] != nil {
			latencies = append(latencies, domain.Num(res["latency_ms"]))
		}
		if res["cost_usd"] != nil {
			costs += domain.Num(res["cost_usd"])
		} else if Terminal(domain.Str(run["status"])) {
			unknownCost++
		}
	}
	ids := []string{}
	for id := range per {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	scenarios := []M{}
	for _, id := range ids {
		s := per[id]
		scenarios = append(scenarios, M{"scenario_id": id, "name": names[id], "pass": s.pass, "fail": s.fail, "unknown": s.unknown, "pending": s.pending, "pass_rate": s.rate()})
	}
	complete := total.pending == 0
	out := M{"complete": complete, "runs": len(runs), "pass": total.pass, "fail": total.fail, "unknown": total.unknown, "pending": total.pending, "pass_rate": stats.Wilson(total.pass, total.pass+total.fail), "latency_ms": stats.Summary(latencies), "cost_usd": costs, "unknown_cost_runs": unknownCost, "scenarios": scenarios}

	regressions, improvements := []M{}, []M{}
	if baseline != nil {
		before := map[string]M{}
		for _, s := range domain.Maps(baseline["scenarios"]) {
			before[domain.Str(s["scenario_id"])] = s
		}
		a, b := []float64{}, []float64{}
		for _, s := range scenarios {
			prev := before[domain.Str(s["scenario_id"])]
			if prev == nil || prev["pass_rate"] == nil || s["pass_rate"] == nil {
				continue
			}
			pa, pb := domain.Num(prev["pass_rate"]), domain.Num(s["pass_rate"])
			a, b = append(a, pa), append(b, pb)
			change := M{"scenario_id": s["scenario_id"], "name": s["name"], "before": pa, "after": pb}
			// A flip of the majority outcome counts; repeats make this robust to noise.
			if pa >= 0.5 && pb < 0.5 {
				regressions = append(regressions, change)
			} else if pa < 0.5 && pb >= 0.5 {
				improvements = append(improvements, change)
			}
		}
		out["comparison"] = M{"baseline_suite_id": baseline["suite_id"], "paired_scenarios": len(a), "pass_rate_diff": stats.Paired(a, b, 7), "regressions": regressions, "improvements": improvements, "mcnemar_p": stats.McNemar(len(improvements), len(regressions)), "baseline_cost_usd": baseline["cost_usd"], "baseline_pass_rate": baseline["pass_rate"]}
	}

	gate := domain.Obj(suite["gate"])
	reasons := []string{}
	if !complete {
		out["gate"] = M{"status": "pending", "reasons": []string{"runs still in progress"}}
		return out
	}
	rate := domain.Obj(out["pass_rate"])["estimate"]
	if gate["min_pass_rate"] != nil && (rate == nil || domain.Num(rate) < domain.Num(gate["min_pass_rate"])) {
		reasons = append(reasons, "pass rate below minimum")
	}
	maxReg := 0
	if gate["max_regressions"] != nil {
		maxReg = domain.Int(gate["max_regressions"])
	}
	if baseline != nil && len(regressions) > maxReg {
		reasons = append(reasons, "scenarios regressed against the baseline")
	}
	if gate["max_cost_usd"] != nil && costs > domain.Num(gate["max_cost_usd"]) {
		reasons = append(reasons, "cost above budget")
	}
	if total.pass+total.fail == 0 {
		reasons = append(reasons, "no run produced a decidable outcome")
	}
	status := "passed"
	if len(reasons) > 0 {
		status = "failed"
	}
	notes := []string{}
	if baseline == nil && gate["min_pass_rate"] == nil && gate["max_cost_usd"] == nil {
		notes = append(notes, "no gate condition applied: no baseline to compare with and no minimum pass rate")
	}
	out["gate"] = M{"status": status, "reasons": reasons, "notes": notes, "policy": gate}
	return out
}
