package server

import (
	"context"
	"errors"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/tracelab"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/workbench"
)

func (s *Server) workbenchRoutes() {
	s.route("GET /api/workbench/agents", workbenchAgents)
	s.route("GET /api/workbench/scenarios", listScenarios)
	s.route("POST /api/workbench/scenarios", func(r *Request) (any, error) { return saveScenario(r, "") })
	s.route("GET /api/workbench/scenarios/{id}", func(r *Request) (any, error) {
		return r.DB.Get(r.R.Context(), workbench.Scenarios, r.R.PathValue("id"))
	})
	s.route("POST /api/workbench/scenarios/{id}", func(r *Request) (any, error) { return saveScenario(r, r.R.PathValue("id")) })
	s.route("DELETE /api/workbench/scenarios/{id}", deleteScenario)
	s.route("POST /api/workbench/from-trace", scenarioFromTrace)
	s.route("POST /api/workbench/run", runScenario)
	s.route("GET /api/workbench/runs", listScenarioRuns)
	s.route("GET /api/workbench/runs/{id}", func(r *Request) (any, error) {
		run, err := r.DB.Get(r.R.Context(), workbench.Runs, r.R.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return workbench.Enrich(r.R.Context(), r.DB, run), nil
	})
}

func workbenchAgents(r *Request) (any, error) {
	out := []M{}
	for _, p := range r.S.Hub.Peers(r.DB.Project) {
		out = append(out, p.Info())
	}
	store.Order(out, "connected_at", true)
	return out, nil
}

func listScenarios(r *Request) (any, error) {
	rows, err := r.DB.List(r.R.Context(), workbench.Scenarios, nil, 2000)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		delete(row, "steps")
	}
	store.Order(rows, "updated_at", true)
	return rows, nil
}

func saveScenario(r *Request, id string) (any, error) {
	body, err := r.Body(4 << 20)
	if err != nil {
		return nil, err
	}
	scenario, err := workbench.Normalize(body)
	if err != nil {
		return nil, invalid(err.Error())
	}
	ctx := r.R.Context()
	now := domain.Now()
	scenario["updated_at"] = now
	if id == "" {
		id = domain.ID("scn")
		scenario["created_at"] = now
		if err = r.DB.Create(ctx, workbench.Scenarios, id, scenario); err != nil {
			return nil, err
		}
		_ = r.DB.Audit(ctx, "workbench.scenario.create", id, M{"name": scenario["name"]})
	} else {
		previous, err := r.DB.Get(ctx, workbench.Scenarios, id)
		if err != nil {
			return nil, err
		}
		scenario["created_at"] = previous["created_at"]
		if len(domain.Arr(scenario["steps"])) == 0 {
			scenario["steps"] = previous["steps"]
		}
		if err = r.DB.Put(ctx, workbench.Scenarios, id, scenario); err != nil {
			return nil, err
		}
	}
	scenario["id"] = id
	return scenario, nil
}

func deleteScenario(r *Request) (any, error) {
	ctx := r.R.Context()
	id := r.R.PathValue("id")
	if _, err := r.DB.Get(ctx, workbench.Scenarios, id); err != nil {
		return nil, err
	}
	if _, err := r.DB.Pool.Exec(ctx, "DELETE FROM arl.documents WHERE project_id=$1 AND kind=$2 AND data->>'scenario_id'=$3", r.DB.Project, workbench.Runs, id); err != nil {
		return nil, err
	}
	if err := r.DB.Delete(ctx, workbench.Scenarios, id); err != nil {
		return nil, err
	}
	_ = r.DB.Audit(ctx, "workbench.scenario.delete", id, M{})
	return M{"deleted": id}, nil
}

func scenarioFromTrace(r *Request) (any, error) {
	body, err := r.Body(1 << 16)
	if err != nil {
		return nil, err
	}
	ctx := r.R.Context()
	detail, events, err := tracelab.Snapshot(ctx, r.DB, domain.Str(body["trace_id"]))
	if err != nil {
		return nil, err
	}
	scenario := workbench.FromTrace(detail, events, domain.Str(body["step"]))
	if !domain.Bool(body["save"]) {
		return scenario, nil
	}
	scenario, err = workbench.Normalize(scenario)
	if err != nil {
		return nil, invalid(err.Error())
	}
	id := domain.ID("scn")
	scenario["created_at"], scenario["updated_at"] = domain.Now(), domain.Now()
	if err = r.DB.Create(ctx, workbench.Scenarios, id, scenario); err != nil {
		return nil, err
	}
	scenario["id"] = id
	return scenario, nil
}

func runScenario(r *Request) (any, error) {
	body, err := r.Body(4 << 20)
	if err != nil {
		return nil, err
	}
	ctx := r.R.Context()
	scenarioID := domain.Str(body["scenario_id"])
	raw := domain.Obj(body["scenario"])
	if len(raw) == 0 && scenarioID != "" {
		if raw, err = r.DB.Get(ctx, workbench.Scenarios, scenarioID); err != nil {
			return nil, err
		}
	}
	scenario, err := workbench.Normalize(raw)
	if err != nil {
		return nil, invalid(err.Error())
	}
	delete(scenario, "steps")
	mode := domain.Str(body["mode"])
	if mode == "" {
		mode = domain.Str(scenario["mode"])
	}
	if !domain.Has(workbench.Modes, mode) {
		return nil, invalid("mode must be agent or llm")
	}
	id := domain.ID("wbr")
	run := M{"scenario_id": scenarioID, "mode": mode, "request": scenario, "status": "queued", "created_at": domain.Now(), "label": domain.Str(body["label"])}
	if mode == "llm" {
		req, err := workbench.Request(scenario)
		if err != nil {
			return nil, invalid(err.Error())
		}
		if _, ok := r.Engine.Reg.Spec(ctx, req.Provider); !ok || req.Provider == "imported" {
			return nil, invalid("unknown provider " + req.Provider)
		}
		run["target"] = M{"provider": req.Provider, "model": req.Model}
		if err = r.DB.Create(ctx, workbench.Runs, id, run); err != nil {
			return nil, err
		}
		if _, err = r.Q.Enqueue(ctx, "workbench.llm", M{"run_id": id}, "workbench:"+id); err != nil {
			return nil, err
		}
		run["id"] = id
		return run, nil
	}
	run["status"] = "dispatched"
	run["target"] = M{"service": scenario["service"], "session_id": body["session_id"]}
	if err = r.DB.Create(ctx, workbench.Runs, id, run); err != nil {
		return nil, err
	}
	pushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	peer, err := r.S.Hub.Push(pushCtx, r.DB.Project, domain.Str(body["session_id"]), domain.Str(scenario["service"]), workbench.Command(id, scenarioID, scenario))
	if err != nil {
		_ = r.DB.Delete(ctx, workbench.Runs, id)
		if errors.Is(err, tracelab.ErrNoPeer) {
			return nil, conflict("No connected agent accepts replays for this service. Start the agent with the SDK, register a replay handler (OnReplay / on_replay / onReplay), then retry — or switch to LLM step mode.")
		}
		return nil, conflict("The agent connection closed before the replay was delivered; retry.")
	}
	run, err = r.DB.Update(ctx, workbench.Runs, id, func(m M) error {
		m["target"] = M{"service": peer.Service, "session_id": peer.Session}
		return nil
	})
	return run, err
}

func listScenarioRuns(r *Request) (any, error) {
	ctx := r.R.Context()
	filter := M{}
	if id := r.R.URL.Query().Get("scenario_id"); id != "" {
		filter["scenario_id"] = id
	}
	rows, err := r.DB.List(ctx, workbench.Runs, filter, 0)
	if err != nil {
		return nil, err
	}
	store.Order(rows, "created_at", true)
	limit := queryInt(r.R, "limit", 30, 1, 200)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for i, row := range rows {
		if row["mode"] == "agent" {
			rows[i] = workbench.Enrich(ctx, r.DB, row)
		}
	}
	return rows, nil
}
