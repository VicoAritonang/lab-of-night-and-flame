package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/quality"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
)

func (s *Server) qualityRoutes() {
	s.route("GET /api/agent-lab/runs/{id}/feedback", func(r *Request) (any, error) {
		return quality.ListFeedback(r.R.Context(), r.DB, r.R.PathValue("id"))
	})
	s.route("POST /api/agent-lab/runs/{id}/feedback", func(r *Request) (any, error) {
		return addFeedback(r, r.R.PathValue("id"))
	})
	s.route("POST /api/feedback", func(r *Request) (any, error) { return addFeedback(r, "") })
	s.route("DELETE /api/feedback/{id}", func(r *Request) (any, error) {
		tag, err := r.DB.Pool.Exec(r.R.Context(), "DELETE FROM arl.feedback WHERE id=$1 AND project_id=$2", r.R.PathValue("id"), r.DB.Project)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, store.ErrNotFound
		}
		return M{"deleted": r.R.PathValue("id")}, err
	})

	s.route("GET /api/annotation/queues", listQueues)
	s.route("POST /api/annotation/queues", func(r *Request) (any, error) { return saveDoc(r, quality.Queues, "", quality.ValidateQueue) })
	s.route("GET /api/annotation/queues/{id}", func(r *Request) (any, error) { return r.DB.Get(r.R.Context(), quality.Queues, r.R.PathValue("id")) })
	s.route("POST /api/annotation/queues/{id}", func(r *Request) (any, error) {
		return saveDoc(r, quality.Queues, r.R.PathValue("id"), quality.ValidateQueue)
	})
	s.route("DELETE /api/annotation/queues/{id}", deleteQueue)
	s.route("POST /api/annotation/queues/{id}/items", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		id := r.R.PathValue("id")
		if _, err = r.DB.Get(r.R.Context(), quality.Queues, id); err != nil {
			return nil, err
		}
		added, err := quality.AddItems(r.R.Context(), r.DB, id, body)
		if errors.Is(err, quality.ErrInvalid) {
			return nil, invalid(err.Error())
		}
		if err != nil {
			return nil, err
		}
		return M{"added": added}, nil
	})
	s.route("GET /api/annotation/queues/{id}/next", func(r *Request) (any, error) {
		author := r.R.URL.Query().Get("author")
		if author == "" {
			return nil, invalid("author is required")
		}
		item, err := quality.Next(r.R.Context(), r.DB, r.R.PathValue("id"), author)
		if err != nil || item == nil {
			// A typed nil map would serialize as {} instead of null.
			return M{"item": nil}, err
		}
		return M{"item": item}, nil
	})
	s.route("GET /api/annotation/queues/{id}/items", func(r *Request) (any, error) {
		rows, err := r.DB.List(r.R.Context(), quality.Items, M{"queue_id": r.R.PathValue("id")}, 0)
		if err != nil {
			return nil, err
		}
		store.Order(rows, "created_at", true)
		return page(rows, queryInt(r.R, "offset", 0, 0, 1<<30), queryInt(r.R, "limit", 100, 1, 500)), nil
	})
	s.route("POST /api/annotation/items/{id}/submit", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		item, err := r.DB.Get(r.R.Context(), quality.Items, r.R.PathValue("id"))
		if err != nil {
			return nil, err
		}
		queue, err := r.DB.Get(r.R.Context(), quality.Queues, domain.Str(item["queue_id"]))
		if err != nil {
			return nil, err
		}
		updated, err := quality.Submit(r.R.Context(), r.DB, queue, r.R.PathValue("id"), domain.Str(body["author"]), domain.Obj(body["scores"]), domain.Str(body["comment"]))
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, invalid(err.Error())
		}
		return updated, err
	})
	s.route("POST /api/annotation/items/{id}/skip", func(r *Request) (any, error) {
		body, err := r.Body(4096)
		if err != nil {
			return nil, err
		}
		if domain.Str(body["author"]) == "" {
			return nil, invalid("author is required")
		}
		return quality.Skip(r.R.Context(), r.DB, r.R.PathValue("id"), domain.Str(body["author"]))
	})
	s.route("GET /api/annotation/queues/{id}/agreement", func(r *Request) (any, error) {
		queue, err := r.DB.Get(r.R.Context(), quality.Queues, r.R.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return quality.Agreement(r.R.Context(), r.DB, queue)
	})

	s.route("GET /api/evaluators", func(r *Request) (any, error) { return listDocs(r, quality.Evaluators) })
	s.route("POST /api/evaluators", func(r *Request) (any, error) { return saveDoc(r, quality.Evaluators, "", quality.ValidateEvaluator) })
	s.route("POST /api/evaluators/{id}", func(r *Request) (any, error) {
		return saveDoc(r, quality.Evaluators, r.R.PathValue("id"), quality.ValidateEvaluator)
	})
	s.route("DELETE /api/evaluators/{id}", func(r *Request) (any, error) { return deleteDoc(r, quality.Evaluators) })
	s.route("POST /api/evaluators/test", testEvaluator)
	s.route("POST /api/evaluators/{id}/backfill", backfillEvaluator)

	s.route("GET /api/alerts", func(r *Request) (any, error) { return listDocs(r, quality.Alerts) })
	s.route("POST /api/alerts", func(r *Request) (any, error) { return saveDoc(r, quality.Alerts, "", quality.ValidateAlert) })
	s.route("POST /api/alerts/{id}", func(r *Request) (any, error) {
		return saveDoc(r, quality.Alerts, r.R.PathValue("id"), quality.ValidateAlert)
	})
	s.route("DELETE /api/alerts/{id}", func(r *Request) (any, error) { return deleteDoc(r, quality.Alerts) })
	s.route("GET /api/alerts/events", func(r *Request) (any, error) {
		rows, err := r.DB.List(r.R.Context(), quality.AlertEvents, nil, 0)
		if err != nil {
			return nil, err
		}
		store.Order(rows, "at", true)
		if n := queryInt(r.R, "limit", 100, 1, 500); len(rows) > n {
			rows = rows[:n]
		}
		return rows, nil
	})
	s.route("POST /api/alerts/check", func(r *Request) (any, error) {
		return r.Async("alerts.check", M{}, r.Key("alerts:manual"))
	})
}

func addFeedback(r *Request, traceID string) (any, error) {
	body, err := r.Body(64 << 10)
	if err != nil {
		return nil, err
	}
	ctx := r.R.Context()
	if traceID == "" {
		if traceID, err = quality.ResolveTrace(ctx, r.DB, body); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			return nil, invalid(err.Error())
		}
	}
	f, err := quality.ValidateFeedback(body)
	if err != nil {
		return nil, invalid(err.Error())
	}
	id := domain.ID("fb")
	if key := r.R.Header.Get("Idempotency-Key"); key != "" {
		id = "fb_" + domain.Hash([]string{traceID, key})[:24]
	}
	if err = quality.PutFeedback(ctx, r.DB, id, traceID, f); err != nil {
		return nil, err
	}
	f["id"], f["trace_id"] = id, traceID
	return f, nil
}

// saveDoc creates (id == "") or replaces a validated document. Replacements
// keep created_at and usage counters and bump revision, so evaluators re-run
// on traces after their definition changes.
func saveDoc(r *Request, kind, id string, validate func(M) (M, error)) (any, error) {
	body, err := r.Body(1 << 20)
	if err != nil {
		return nil, err
	}
	doc, err := validate(body)
	if err != nil {
		return nil, invalid(err.Error())
	}
	ctx := r.R.Context()
	now := domain.Now()
	doc["updated_at"] = now
	if id == "" {
		id = domain.ID(map[string]string{quality.Queues: "aq", quality.Evaluators: "ev", quality.Alerts: "al"}[kind])
		doc["created_at"], doc["revision"] = now, 1
		if err = r.DB.Create(ctx, kind, id, doc); err != nil {
			return nil, err
		}
		_ = r.DB.Audit(ctx, kind+".create", id, M{"name": doc["name"]})
	} else {
		updated, err := r.DB.Update(ctx, kind, id, func(m M) error {
			for _, k := range []string{"created_at", "usage", "total_evaluated", "last_run_at", "state", "last_value", "last_samples", "last_checked_at", "last_fired_at"} {
				if m[k] != nil {
					doc[k] = m[k]
				}
			}
			doc["revision"] = domain.Int(m["revision"]) + 1
			for k := range m {
				delete(m, k)
			}
			for k, v := range doc {
				m[k] = v
			}
			m["id"] = id
			return nil
		})
		if err != nil {
			return nil, err
		}
		_ = r.DB.Audit(ctx, kind+".update", id, M{"name": doc["name"]})
		return updated, nil
	}
	doc["id"] = id
	return doc, nil
}

func listDocs(r *Request, kind string) (any, error) {
	rows, err := r.DB.List(r.R.Context(), kind, nil, 500)
	if err != nil {
		return nil, err
	}
	store.Order(rows, "created_at", true)
	return rows, nil
}

func deleteDoc(r *Request, kind string) (any, error) {
	ctx := r.R.Context()
	id := r.R.PathValue("id")
	if _, err := r.DB.Get(ctx, kind, id); err != nil {
		return nil, err
	}
	if err := r.DB.Delete(ctx, kind, id); err != nil {
		return nil, err
	}
	_ = r.DB.Audit(ctx, kind+".delete", id, M{})
	return M{"deleted": id}, nil
}

func listQueues(r *Request) (any, error) {
	rows, err := r.DB.List(r.R.Context(), quality.Queues, nil, 500)
	if err != nil {
		return nil, err
	}
	progress, err := quality.Progress(r.R.Context(), r.DB)
	if err != nil {
		return nil, err
	}
	for _, q := range rows {
		p := progress[domain.Str(q["id"])]
		if p == nil {
			p = M{"pending": 0, "done": 0, "total": 0}
		}
		q["progress"] = p
	}
	store.Order(rows, "created_at", true)
	return rows, nil
}

// deleteQueue removes a queue and its items; the human feedback it produced
// stays attached to the traces.
func deleteQueue(r *Request) (any, error) {
	ctx := r.R.Context()
	id := r.R.PathValue("id")
	if _, err := r.DB.Get(ctx, quality.Queues, id); err != nil {
		return nil, err
	}
	if _, err := r.DB.Pool.Exec(ctx, "DELETE FROM arl.documents WHERE project_id=$1 AND kind=$2 AND data->>'queue_id'=$3", r.DB.Project, quality.Items, id); err != nil {
		return nil, err
	}
	if err := r.DB.Delete(ctx, quality.Queues, id); err != nil {
		return nil, err
	}
	_ = r.DB.Audit(ctx, "annotation_queues.delete", id, M{})
	return M{"deleted": id}, nil
}

// testEvaluator dry-runs an evaluator definition on one trace.
func testEvaluator(r *Request) (any, error) {
	body, err := r.Body(1 << 20)
	if err != nil {
		return nil, err
	}
	ev, err := quality.ValidateEvaluator(domain.Obj(body["evaluator"]))
	if err != nil {
		return nil, invalid(err.Error())
	}
	m, err := quality.Load(r.R.Context(), r.DB, domain.Str(body["trace_id"]))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.R.Context(), 150*time.Second)
	defer cancel()
	result, err := quality.Score(ctx, r.Engine.Reg, ev, m)
	if err != nil {
		return nil, invalid(err.Error())
	}
	return M{"result": result, "input": m.Input, "output": m.Output, "tool_calls": len(m.Tools)}, nil
}

// backfillEvaluator scores recent finished traces with an evaluator.
func backfillEvaluator(r *Request) (any, error) {
	body, err := r.Body(4096)
	if err != nil {
		return nil, err
	}
	ctx := r.R.Context()
	ev, err := r.DB.Get(ctx, quality.Evaluators, r.R.PathValue("id"))
	if err != nil {
		return nil, err
	}
	hours := domain.Num(body["hours"])
	if hours <= 0 {
		hours = 24
	}
	limit := domain.Int(body["limit"])
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := r.DB.Pool.Query(ctx, "SELECT id FROM arl.traces WHERE project_id=$1 AND status<>'running' AND started_at>=$2 ORDER BY started_at DESC LIMIT $3", r.DB.Project, domain.Now()-hours*3600, limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	queued := 0
	for _, tid := range ids {
		detail, err := tracelab.Detail(ctx, r.DB, tid)
		if err != nil {
			continue
		}
		f := domain.Obj(ev["filter"])
		if s := domain.Str(f["service"]); s != "" && s != detail["service"] {
			continue
		}
		key := fmt.Sprintf("eval:%s:%v:%s", ev["id"], ev["revision"], tid)
		if _, err = r.Q.Enqueue(ctx, "trace.evaluate", M{"trace_id": tid, "evaluator_id": ev["id"]}, key); err != nil {
			return nil, err
		}
		queued++
	}
	return M{"queued": queued, "considered": len(ids)}, nil
}
