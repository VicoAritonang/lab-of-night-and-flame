package server

import (
	"errors"

	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/workbench"
)

func (s *Server) suiteRoutes() {
	s.route("POST /api/workbench/suites", createSuite)
	s.route("GET /api/workbench/suites", func(r *Request) (any, error) {
		filter := M{}
		if c := r.R.URL.Query().Get("collection"); c != "" {
			filter["collection"] = c
		}
		rows, err := r.DB.List(r.R.Context(), workbench.Suites, filter, 0)
		if err != nil {
			return nil, err
		}
		store.Order(rows, "created_at", true)
		if n := queryInt(r.R, "limit", 50, 1, 500); len(rows) > n {
			rows = rows[:n]
		}
		for _, row := range rows {
			delete(row, "run_ids")
			if res := domain.Obj(row["result"]); len(res) > 0 {
				row["result"] = M{"pass_rate": res["pass_rate"], "gate": res["gate"], "cost_usd": res["cost_usd"], "runs": res["runs"], "comparison": res["comparison"] != nil}
			}
		}
		return rows, nil
	})
	s.route("GET /api/workbench/suites/{id}", func(r *Request) (any, error) {
		suite, err := r.DB.Get(r.R.Context(), workbench.Suites, r.R.PathValue("id"))
		if err != nil {
			return nil, err
		}
		result, runs, err := suiteResult(r, suite, true)
		if err != nil {
			return nil, err
		}
		suite["result"], suite["runs"] = result, runs
		return suite, nil
	})
}

// suiteResult aggregates a suite's runs (cached once complete).
func suiteResult(r *Request, suite M, withRuns bool) (M, []M, error) {
	ctx := r.R.Context()
	if res := domain.Obj(suite["result"]); domain.Bool(res["complete"]) && !withRuns {
		return res, nil, nil
	}
	runs, err := r.DB.List(ctx, workbench.Runs, M{"suite_id": suite["id"]}, 0)
	if err != nil {
		return nil, nil, err
	}
	for i, run := range runs {
		if run["mode"] == "agent" {
			runs[i] = workbench.Enrich(ctx, r.DB, run)
		}
	}
	var baseline M
	if id := domain.Str(suite["baseline_suite_id"]); id != "" {
		if b, err := r.DB.Get(ctx, workbench.Suites, id); err == nil {
			if baseline, _, err = suiteResult(r, b, false); err != nil {
				return nil, nil, err
			}
			baseline = domain.Copy(baseline)
			baseline["suite_id"] = id
			baseline["cost_usd"] = domain.Obj(b["result"])["cost_usd"]
		}
	}
	res := workbench.Summarize(suite, runs, baseline, domain.Now())
	if domain.Bool(res["complete"]) && !domain.Bool(domain.Obj(suite["result"])["complete"]) {
		_, err = r.DB.Update(ctx, workbench.Suites, domain.Str(suite["id"]), func(m M) error {
			m["result"], m["status"], m["finished_at"] = res, "completed", domain.Now()
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	store.Order(runs, "created_at", false)
	return res, runs, nil
}

// createSuite runs every scenario of a collection (times repeats) with the
// given overrides. baseline is a suite id, "latest" (the newest completed suite
// of the collection) or empty.
func createSuite(r *Request) (any, error) {
	body, err := r.Body(1 << 20)
	if err != nil {
		return nil, err
	}
	suite, err := workbench.ValidateSuite(body)
	if err != nil {
		return nil, invalid(err.Error())
	}
	ctx := r.R.Context()
	scenarios, err := r.DB.List(ctx, workbench.Scenarios, M{"collection": suite["collection"]}, 500)
	if err != nil {
		return nil, err
	}
	if len(scenarios) == 0 {
		return nil, invalid("collection has no scenarios")
	}
	if len(scenarios)*domain.Int(suite["repeats"]) > 2000 {
		return nil, invalid("at most 2000 runs per suite")
	}
	switch b := domain.Str(suite["baseline"]); b {
	case "", "none":
	case "latest":
		previous, err := r.DB.List(ctx, workbench.Suites, M{"collection": suite["collection"]}, 0)
		if err != nil {
			return nil, err
		}
		store.Order(previous, "created_at", true)
		// Completion is recorded when a suite is read, so settle each
		// candidate before deciding whether it can be the baseline.
		for _, p := range previous {
			res, _, err := suiteResult(r, p, false)
			if err != nil {
				return nil, err
			}
			if domain.Bool(res["complete"]) {
				suite["baseline_suite_id"] = p["id"]
				break
			}
		}
	default:
		if _, err = r.DB.Get(ctx, workbench.Suites, b); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, invalid("baseline suite not found")
			}
			return nil, err
		}
		suite["baseline_suite_id"] = b
	}
	id := domain.ID("suite")
	suite["status"], suite["created_at"], suite["scenarios"] = "running", domain.Now(), len(scenarios)
	if err = r.DB.Create(ctx, workbench.Suites, id, suite); err != nil {
		return nil, err
	}
	store.Order(scenarios, "name", false)
	dispatched, failures := 0, []string{}
	for _, raw := range scenarios {
		sc, err := workbench.Normalize(raw)
		if err != nil {
			failures = append(failures, domain.Str(raw["name"])+": "+err.Error())
			continue
		}
		delete(sc, "steps")
		sc = workbench.Apply(sc, domain.Obj(suite["overrides"]))
		mode := domain.Str(suite["mode"])
		if mode == "" {
			mode = domain.Str(sc["mode"])
		}
		for rep := 1; rep <= domain.Int(suite["repeats"]); rep++ {
			if _, err = dispatchRun(r, domain.Str(raw["id"]), sc, mode, "", M{"suite_id": id, "repeat": rep, "label": suite["label"]}); err != nil {
				failures = append(failures, domain.Str(raw["name"])+": "+err.Error())
				break
			}
			dispatched++
		}
	}
	if dispatched == 0 {
		_ = r.DB.Delete(ctx, workbench.Suites, id)
		return nil, conflict("no scenario run could be started: " + failures[0])
	}
	_ = r.DB.Audit(ctx, "workbench.suite.create", id, M{"collection": suite["collection"], "runs": dispatched})
	return r.DB.Update(ctx, workbench.Suites, id, func(m M) error {
		m["dispatched"], m["dispatch_errors"] = dispatched, failures
		return nil
	})
}
