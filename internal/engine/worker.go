package engine

import (
	"context"
	"errors"
	"go.vicoworks.com/agent-reliability-lab/internal/dataset"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/features"
	"go.vicoworks.com/agent-reliability-lab/internal/perf"
	"go.vicoworks.com/agent-reliability-lab/internal/quality"
	"go.vicoworks.com/agent-reliability-lab/internal/queue"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
	"go.vicoworks.com/agent-reliability-lab/internal/workbench"
	"time"
)

func (e *Engine) Handle(ctx context.Context, job *queue.Job) (M, error) {
	// A worker only runs the modules it was configured with (ARL_FEATURES).
	if f := features.ForJob(job.Kind); f != "" && !e.Config.Features.On(f) {
		return validation(errors.New(e.Config.Features.Reason(f))), nil
	}
	// Model calls remain parallel; serialize large JSON/image/statistics jobs per
	// invocation so several report/import jobs cannot exhaust the Lambda heap.
	if domain.Has([]string{"datasets.seed", "artifact.validate", "dataset.import", "predictions.import", "experiment.preflight", "experiment.create", "experiment.launch", "campaign.prepare", "campaign.rerun", "campaign.analyze", "campaign.report", "trace.summarize", "trace.export"}, job.Kind) {
		select {
		case e.heavy <- struct{}{}:
			defer func() { <-e.heavy }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	w := e.Scope(job.Project)
	w.Config.Mode = "async"
	w.Reg.Config.Mode = "async"
	p := job.Payload
	switch job.Kind {
	case "datasets.seed":
		return dataset.Seed(ctx, w.DB)
	case "workbench.llm":
		return workbench.Execute(ctx, w.DB, w.Reg, domain.Str(p["run_id"]))
	case "catalog.refresh":
		err := w.Reg.Refresh(ctx)
		return M{"ok": err == nil}, err
	case "artifact.validate":
		_, body, err := dataset.ReadUpload(ctx, w.DB, domain.Str(p["upload_id"]))
		if err != nil {
			return nil, err
		}
		m, err := dataset.PutImage(ctx, w.DB, body)
		if err != nil {
			return validation(err), nil
		}
		return m, nil
	case "dataset.import":
		m, err := dataset.Import(ctx, w.DB, p)
		if err != nil {
			return validation(err), nil
		}
		return m, nil
	case "predictions.import":
		m, err := dataset.Predictions(ctx, w.DB, p)
		if err != nil {
			return validation(err), nil
		}
		return m, nil
	case "dataset.delete":
		tx, err := w.DB.Pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, "DELETE FROM arl.documents WHERE project_id=$1 AND ((kind IN ('samples','predictions') AND data->>'dataset_id'=$2) OR (kind='datasets' AND id=$2))", w.DB.Project, p["dataset_id"])
		if err != nil {
			return nil, err
		}
		return M{"ok": true}, tx.Commit(ctx)
	case "experiment.preflight":
		return Preflight(ctx, w.DB, w.Reg, domain.Obj(p["plan"]))
	case "experiment.create":
		plan := domain.Obj(p["plan"])
		pf, err := Preflight(ctx, w.DB, w.Reg, plan)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return validation(errors.New("dataset not found")), nil
			}
			return nil, err
		}
		if domain.Bool(p["launch"]) && !domain.Bool(pf["ok"]) {
			return M{"validation_error": true, "message": "plan failed validation", "errors": pf["errors"]}, nil
		}
		if domain.Bool(p["launch"]) && (domain.Num(pf["cost_high_usd"]) > 0 || len(domain.Arr(pf["unknown_price_arms"])) > 0) && !domain.Bool(domain.Obj(plan["budget"])["paid_run_confirmed"]) {
			return M{"validation_error": true, "message": "paid run requires explicit confirmation", "errors": []string{"paid_run_not_confirmed"}}, nil
		}
		id := "exp_" + domain.Hash(job.ID)[:24]
		status := "draft"
		var frozen any
		if domain.Bool(p["launch"]) {
			status = "frozen"
			frozen = domain.Now()
		}
		exp := M{"id": id, "name": p["name"], "hypothesis": p["hypothesis"], "status": status, "plan": plan, "plan_digest": domain.Hash(plan), "created_at": domain.Now(), "frozen_at": frozen}
		if old, err := w.DB.Get(ctx, "experiments", id); err == nil {
			exp = old
		} else if err = w.DB.Create(ctx, "experiments", id, exp); err != nil && !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		var cid any
		if domain.Bool(p["launch"]) {
			existing, err := w.DB.List(ctx, "campaigns", M{"experiment_id": id}, 1)
			if err != nil {
				return nil, err
			}
			if len(existing) > 0 {
				cid = existing[0]["id"]
			} else {
				created, err := w.Launch(ctx, exp, "", "cmp_"+domain.Hash(job.ID)[:24])
				if err != nil {
					return validation(err), nil
				}
				cid = created
			}
		}
		return M{"id": id, "campaign_id": cid, "preflight": pf}, nil
	case "experiment.launch":
		exp, err := w.DB.Get(ctx, "experiments", domain.Str(p["experiment_id"]))
		if err != nil {
			return validation(errors.New("experiment not found")), nil
		}
		id, err := w.Launch(ctx, exp, "", "cmp_"+domain.Hash(job.ID)[:24])
		if err != nil {
			return validation(err), nil
		}
		_, err = w.DB.Update(ctx, "experiments", domain.Str(exp["id"]), func(m M) error { m["status"] = "frozen"; m["frozen_at"] = domain.Now(); return nil })
		return M{"campaign_id": id}, err
	case "campaign.prepare":
		return w.Prepare(ctx, domain.Str(p["campaign_id"]))
	case "trial.run":
		return w.Trial(ctx, domain.Str(p["trial_id"]))
	case "campaign.analyze":
		return w.Analyze(ctx, domain.Str(p["campaign_id"]))
	case "campaign.cancel":
		return w.Cancel(ctx, domain.Str(p["campaign_id"]))
	case "campaign.resume":
		m, err := w.Resume(ctx, domain.Str(p["campaign_id"]), p["budget_max_usd"])
		if err != nil {
			return validation(err), nil
		}
		return m, nil
	case "campaign.rerun":
		c, err := w.DB.Get(ctx, "campaigns", domain.Str(p["campaign_id"]))
		if err != nil {
			return nil, err
		}
		exp, err := w.DB.Get(ctx, "experiments", domain.Str(c["experiment_id"]))
		if err != nil {
			return nil, err
		}
		id, err := w.Launch(ctx, exp, domain.Str(c["id"]), "cmp_"+domain.Hash(job.ID)[:24])
		if err != nil {
			return validation(err), nil
		}
		return M{"campaign_id": id}, nil
	case "campaign.rescore":
		return w.Rescore(ctx, domain.Str(p["campaign_id"]))
	case "rescore.trial":
		return w.RescoreTrial(ctx, p)
	case "campaign.report":
		return w.Report(ctx, domain.Str(p["campaign_id"]))
	case "playground.target":
		play, err := w.DB.Get(ctx, "playgrounds", domain.Str(p["playground_id"]))
		if err != nil {
			return nil, err
		}
		if play["status"] != "running" {
			_ = w.DB.Emit(ctx, domain.Str(p["channel"]), M{"type": "error", "i": p["i"], "category": "cancelled", "message": "playground request closed"})
			return M{}, nil
		}
		p = domain.Copy(p)
		p["request"] = play["request"]
		return w.Playground(ctx, p)
	case "trace.summarize":
		result, err := tracelab.Summarize(ctx, w.DB, domain.Str(p["trace_id"]))
		if err != nil {
			return nil, err
		}
		// Finished traces feed the online evaluators.
		if w.Config.Features.On(features.Quality) {
			if _, err = quality.Dispatch(ctx, w.DB, w.Q, domain.Str(p["trace_id"])); err != nil {
				return nil, err
			}
		}
		return result, nil
	case "trace.evaluate":
		return quality.Evaluate(ctx, w.DB, w.Reg, domain.Str(p["trace_id"]), domain.Str(p["evaluator_id"]))
	case "alerts.check":
		return quality.Check(ctx, w.DB)
	case "perf.run":
		return perf.Execute(ctx, w.DB, w.Reg, domain.Str(p["run_id"]))
	case "trace.export":
		id := domain.Str(p["trace_id"])
		detail, events, err := tracelab.Snapshot(ctx, w.DB, id)
		if err != nil {
			return nil, err
		}
		detail["summary"] = tracelab.Summary(detail, events)
		doc := M{"json": string(domain.JSON(M{"protocol": "arl/1", "trace": detail, "events": events}))}
		if err = w.DB.Put(ctx, "trace_exports", id, doc); err != nil {
			return nil, err
		}
		return M{"result_kind": "trace_exports", "result_id": id}, nil
	}
	return validation(errors.New("unknown job kind")), nil
}
func validation(err error) M {
	return M{"validation_error": true, "message": err.Error(), "errors": []string{}}
}

// Periodic enqueues periodic jobs (alert rule checks, once a minute per
// project with rules) until ctx ends. Job dedup keys make concurrent
// schedulers on several gateways harmless.
func Periodic(ctx context.Context, db *store.Store, q *queue.Queue) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			rows, err := db.Pool.Query(ctx, "SELECT DISTINCT project_id FROM arl.documents WHERE kind=$1 AND data->>'enabled'='true'", quality.Alerts)
			if err != nil {
				continue
			}
			projects := []string{}
			for rows.Next() {
				var p string
				if rows.Scan(&p) == nil {
					projects = append(projects, p)
				}
			}
			rows.Close()
			minute := now.UTC().Format("200601021504")
			for _, p := range projects {
				_, _ = q.Scope(p).Enqueue(ctx, "alerts.check", M{}, "alerts:"+minute)
			}
		}
	}
}
