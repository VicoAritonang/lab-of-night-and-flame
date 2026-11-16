package server

import (
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/perf"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
)

func (s *Server) perfRoutes() {
	s.route("GET /api/perf", func(r *Request) (any, error) {
		rows, err := r.DB.List(r.R.Context(), perf.Runs, nil, 500)
		if err != nil {
			return nil, err
		}
		store.Order(rows, "created_at", true)
		return rows, nil
	})
	s.route("GET /api/perf/{id}", func(r *Request) (any, error) { return r.DB.Get(r.R.Context(), perf.Runs, r.R.PathValue("id")) })
	s.route("POST /api/perf", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		ctx := r.R.Context()
		run, err := perf.Validate(ctx, r.Engine.Reg, body)
		if err != nil {
			return nil, invalid(err.Error())
		}
		id := domain.ID("perf")
		run["status"], run["created_at"], run["environment"], run["levels"] = "queued", domain.Now(), perf.Environment(), []M{}
		if err = r.DB.Create(ctx, perf.Runs, id, run); err != nil {
			return nil, err
		}
		if _, err = r.Q.Enqueue(ctx, "perf.run", M{"run_id": id}, "perf:"+id); err != nil {
			return nil, err
		}
		_ = r.DB.Audit(ctx, "perf.create", id, M{"provider": run["provider"], "model": run["model"]})
		run["id"] = id
		return run, nil
	})
	s.route("POST /api/perf/{id}/cancel", func(r *Request) (any, error) {
		return r.DB.Update(r.R.Context(), perf.Runs, r.R.PathValue("id"), func(m M) error {
			if m["status"] == "queued" || m["status"] == "running" {
				m["status"], m["finished_at"] = "cancelled", domain.Now()
			}
			return nil
		})
	})
	s.route("DELETE /api/perf/{id}", func(r *Request) (any, error) { return deleteDoc(r, perf.Runs) })
}
