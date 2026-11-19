package server

import (
	"fmt"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/features"
	"go.vicoworks.com/agent-reliability-lab/internal/scoring"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

func scorers(r *Request) (any, error) { return scoring.Catalog(), nil }
func catalog(r *Request) (any, error) {
	ctx := r.R.Context()
	if r.R.URL.Query().Get("refresh") == "true" {
		if _, err := r.Async("catalog.refresh", M{}, "catalog:"+domain.ID("refresh")); err != nil {
			return nil, err
		}
	}
	rows := r.Engine.Reg.Models(ctx)
	filtered := []M{}
	q := r.R.URL.Query()
	terms := strings.Fields(strings.ToLower(q.Get("q")))
	for _, m := range rows {
		if prov := q.Get("provider"); prov != "" && m["provider"] != prov {
			continue
		}
		if modality := q.Get("modality"); modality != "" && !domain.Has(domain.Strings(m["modalities"]), modality) {
			continue
		}
		if q.Get("free") == "true" && !domain.Bool(m["free"]) {
			continue
		}
		match := true
		hay := strings.ToLower(domain.Str(m["id"]) + " " + domain.Str(m["name"]))
		for _, term := range terms {
			if !strings.Contains(hay, term) {
				match = false
			}
		}
		if match {
			filtered = append(filtered, m)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		switch q.Get("sort") {
		case "price":
			if a["price_in"] == nil {
				return false
			}
			if b["price_in"] == nil {
				return true
			}
			return domain.Num(a["price_in"])+domain.Num(a["price_out"]) < domain.Num(b["price_in"])+domain.Num(b["price_out"])
		case "context":
			return domain.Num(a["context_length"]) > domain.Num(b["context_length"])
		case "newest":
			return domain.Num(a["created"]) > domain.Num(b["created"])
		default:
			return domain.Str(a["provider"])+domain.Str(a["name"]) < domain.Str(b["provider"])+domain.Str(b["name"])
		}
	})
	counts := M{}
	for _, m := range filtered {
		p := domain.Str(m["provider"])
		counts[p] = domain.Int(counts[p]) + 1
	}
	out := page(filtered, queryInt(r.R, "offset", 0, 0, 200000), queryInt(r.R, "limit", 100, 1, 500))
	delete(out, "offset")
	delete(out, "limit")
	out["by_provider"] = counts
	return out, nil
}

var providerName = regexp.MustCompile("^[a-z][a-z0-9_-]{1,30}$")
var envName = regexp.MustCompile("^[A-Z][A-Z0-9_]{1,60}$")

func connection(r *Request) (any, error) {
	body, err := r.Body(32 << 10)
	if err != nil {
		return nil, err
	}
	name := domain.Str(body["name"])
	if !providerName.MatchString(name) || len(domain.Str(body["label"])) < 1 || len(domain.Str(body["label"])) > 60 {
		return nil, invalid("invalid provider name or label")
	}
	u, err := url.Parse(domain.Str(body["base_url"]))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, invalid("base_url must be HTTP(S)")
	}
	if key := domain.Str(body["key_env"]); key != "" && !envName.MatchString(key) {
		return nil, invalid("key_env must name an environment variable")
	}
	if _, exists := r.Engine.Reg.Spec(r.R.Context(), name); exists {
		return nil, conflict("provider already exists")
	}
	domain.Default(body, "rpm", 60)
	if domain.Int(body["rpm"]) < 1 || domain.Int(body["rpm"]) > 10000 {
		return nil, invalid("rpm must be 1-10000")
	}
	body["created_at"] = domain.Now()
	if err = r.DB.Create(r.R.Context(), "connections", name, body); err != nil {
		return nil, err
	}
	_ = r.DB.Audit(r.R.Context(), "connection.create", name, M{"base_url": body["base_url"], "key_env": body["key_env"]})
	_, _ = r.Q.Enqueue(r.R.Context(), "catalog.refresh", M{}, "catalog:connection:"+name)
	return M{"ok": true}, nil
}
func overview(r *Request) (any, error) {
	totals := M{"campaigns": 0, "trials": 0, "datasets": 0, "experiments": 0, "spent_usd": 0., "unknown_cost_attempts": 0, "passes": 0, "assessed": 0, "attempts": 0}
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT kind,count(*) FROM arl.documents WHERE project_id=$1 GROUP BY kind", r.DB.Project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var n int
		if err = rows.Scan(&kind, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := totals[kind]; ok {
			totals[kind] = n
		}
	}
	rows.Close()
	var spent float64
	var unknown int
	err = r.DB.Pool.QueryRow(r.R.Context(), "SELECT COALESCE(sum((data->>'spent_usd')::double precision),0),COALESCE(sum((data->>'unknown_cost_attempts')::integer),0) FROM arl.documents WHERE project_id=$1 AND kind='campaigns'", r.DB.Project).Scan(&spent, &unknown)
	if err != nil {
		return nil, err
	}
	totals["spent_usd"], totals["unknown_cost_attempts"] = spent, unknown
	var passes, assessed int
	err = r.DB.Pool.QueryRow(r.R.Context(), "SELECT count(*) FILTER(WHERE data->>'outcome'='pass'),count(*) FILTER(WHERE data->>'outcome' IN ('pass','fail')) FROM arl.documents WHERE project_id=$1 AND kind='trials'", r.DB.Project).Scan(&passes, &assessed)
	if err != nil {
		return nil, err
	}
	totals["passes"], totals["assessed"] = passes, assessed
	recent, err := campaignList(r, 8)
	if err != nil {
		return nil, err
	}
	active := []string{}
	for _, c := range recent {
		if domain.Bool(c["live"]) {
			active = append(active, domain.Str(c["id"]))
		}
	}
	daily := []M{}
	rows, err = r.DB.Pool.Query(r.R.Context(), "SELECT floor((data->>'started_at')::double precision/86400)::integer,count(*),count(*) FILTER(WHERE data->>'status'='completed'),COALESCE(sum((data->>'cost_usd')::double precision),0) FROM arl.documents WHERE project_id=$1 AND kind='attempts' AND (data->>'started_at')::double precision>$2 GROUP BY 1 ORDER BY 1", r.DB.Project, domain.Now()-14*86400)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day, n, ok int
		var cost float64
		if err = rows.Scan(&day, &n, &ok, &cost); err != nil {
			rows.Close()
			return nil, err
		}
		daily = append(daily, M{"day": day, "attempts": n, "ok": ok, "cost": cost})
	}
	rows.Close()
	return M{"totals": totals, "recent": recent, "providers": r.Engine.Reg.Status(r.R.Context()), "daily": daily, "active": active}, nil
}
func audit(r *Request) (any, error) {
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT id,ts,actor,action,resource,detail FROM arl.audit WHERE project_id=$1 ORDER BY id DESC LIMIT $2", r.DB.Project, queryInt(r.R, "limit", 100, 1, 1000))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id int64
		var ts float64
		var actor, action, resource string
		var detail M
		if err = rows.Scan(&id, &ts, &actor, &action, &resource, &detail); err != nil {
			return nil, err
		}
		out = append(out, M{"id": id, "ts": ts, "actor": actor, "action": action, "resource": resource, "detail": detail})
	}
	return out, rows.Err()
}
func projects(r *Request) (any, error) {
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT id,name,extract(epoch from created_at)::double precision FROM arl.projects ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, name string
		var created float64
		if err = rows.Scan(&id, &name, &created); err != nil {
			return nil, err
		}
		out = append(out, M{"id": id, "name": name, "created_at": created})
	}
	return out, rows.Err()
}
func createProject(r *Request) (any, error) {
	body, err := r.Body(8192)
	if err != nil {
		return nil, err
	}
	id, name := domain.Str(body["id"]), domain.Str(body["name"])
	if !regexp.MustCompile("^[a-z][a-z0-9_-]{0,63}$").MatchString(id) || name == "" || len(name) > 200 {
		return nil, invalid("project requires a lowercase slug ID and name")
	}
	tag, err := r.DB.Pool.Exec(r.R.Context(), "INSERT INTO arl.projects(id,name) VALUES($1,$2) ON CONFLICT DO NOTHING", id, name)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, conflict("project ID already exists")
	}
	if r.S.Config.Features.On(features.Datasets) {
		_, err = r.Q.Scope(id).Enqueue(r.R.Context(), "datasets.seed", M{}, "seed:v2")
	}
	if err == nil {
		_, err = r.Q.Scope(id).Enqueue(r.R.Context(), "catalog.refresh", M{}, "catalog:initial")
	}
	return M{"id": id, "name": name}, err
}
func jobStatus(r *Request) (any, error) {
	var id, kind, state string
	var attempts int
	var created, updated time.Time
	var message *string
	err := r.DB.Pool.QueryRow(r.R.Context(), "SELECT id,kind,state,attempts,error,created_at,updated_at FROM arl.jobs WHERE project_id=$1 AND id=$2", r.DB.Project, r.R.PathValue("id")).Scan(&id, &kind, &state, &attempts, &message, &created, &updated)
	if err != nil {
		return nil, err
	}
	return M{"id": id, "kind": kind, "state": state, "attempts": attempts, "error": message, "created_at": created, "updated_at": updated}, nil
}
func openAPI(r *Request) (any, error) {
	paths := M{}
	for path, methods := range map[string][]string{"/health": {"get"}, "/auth/status": {"get"}, "/auth/login": {"post"}, "/auth/logout": {"post"}, "/overview": {"get"}, "/providers": {"get"}, "/connections": {"post"}, "/catalog": {"get"}, "/scorers": {"get"}, "/artifacts": {"post"}, "/artifacts/{digest}": {"get"}, "/datasets": {"get"}, "/datasets/import": {"post"}, "/datasets/{id}": {"get", "delete"}, "/datasets/{id}/samples": {"get"}, "/datasets/{id}/predictions": {"post"}, "/experiments/preflight": {"post"}, "/experiments": {"get", "post"}, "/experiments/{id}": {"get"}, "/experiments/{id}/launch": {"post"}, "/campaigns": {"get"}, "/campaigns/{id}": {"get"}, "/campaigns/{id}/grid": {"get"}, "/campaigns/{id}/events": {"get"}, "/campaigns/{id}/trials": {"get"}, "/campaigns/{id}/results": {"get"}, "/campaigns/{id}/cancel": {"post"}, "/campaigns/{id}/resume": {"post"}, "/campaigns/{id}/rerun": {"post"}, "/campaigns/{id}/rescore": {"post"}, "/trials/{id}": {"get"}, "/playground": {"post"}, "/audit": {"get"}, "/projects": {"get", "post"}, "/jobs/{id}": {"get"}, "/agent-lab/status": {"get"}, "/agent-lab/sessions": {"get"}, "/agent-lab/config": {"post"}, "/agent-lab/listener/start": {"post"}, "/agent-lab/listener/stop": {"post"}, "/agent-lab/runs": {"get"}, "/agent-lab/runs/{id}": {"get"}, "/agent-lab/runs/{id}/events": {"get"}, "/agent-lab/runs/{id}/export": {"get"}, "/agent-lab/runs/{id}/feedback": {"get", "post"}, "/feedback": {"post"}, "/feedback/{id}": {"delete"}, "/otel/v1/traces": {"get", "post"}, "/monitor/metrics": {"get"}, "/monitor/facets": {"get"}, "/annotation/queues": {"get", "post"}, "/annotation/queues/{id}": {"get", "post", "delete"}, "/annotation/queues/{id}/items": {"get", "post"}, "/annotation/queues/{id}/next": {"get"}, "/annotation/queues/{id}/agreement": {"get"}, "/annotation/items/{id}/submit": {"post"}, "/annotation/items/{id}/skip": {"post"}, "/evaluators": {"get", "post"}, "/evaluators/{id}": {"post", "delete"}, "/evaluators/{id}/backfill": {"post"}, "/evaluators/test": {"post"}, "/alerts": {"get", "post"}, "/alerts/{id}": {"post", "delete"}, "/alerts/events": {"get"}, "/alerts/check": {"post"}, "/prompts": {"get", "post"}, "/prompts/{name}": {"get", "delete"}, "/prompts/{name}/versions": {"get", "post"}, "/prompts/{name}/labels": {"post"}, "/prompts/{name}/render": {"post"}, "/workbench/suites": {"get", "post"}, "/workbench/suites/{id}": {"get"}, "/perf": {"get", "post"}, "/perf/{id}": {"get", "delete"}, "/perf/{id}/cancel": {"post"}} {
		ops := M{}
		for _, method := range methods {
			op := M{"summary": strings.ToUpper(method) + " " + path, "responses": M{"200": M{"description": "Success"}, "401": M{"description": "Owner authentication required"}, "422": M{"description": "Validation error"}, "503": M{"description": "Durable job still pending; retry or inspect X-Lab-Job-ID"}}}
			if method == "post" {
				op["requestBody"] = M{"content": M{"application/json": M{"schema": M{"type": "object"}}}}
			}
			ops[method] = op
		}
		paths["/api"+path] = ops
	}
	return M{"openapi": "3.1.0", "info": M{"title": "Agent Reliability Lab · Go SYNC/ASYNC", "version": Version}, "paths": paths, "components": M{"securitySchemes": M{"owner": M{"type": "http", "scheme": "bearer"}}}, "security": []M{{"owner": []string{}}}, "externalDocs": M{"url": "/api/agent-lab/connection.md", "description": fmt.Sprint("arl/1 telemetry and asynchronous deployment")}}, nil
}
