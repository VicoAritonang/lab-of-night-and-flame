package server

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
)

func (s *Server) monitorRoutes() {
	s.route("GET /api/monitor/metrics", monitorMetrics)
	s.route("GET /api/monitor/facets", monitorFacets)
}

// window reads the time window (hours, default 24, max 90 days) and the
// optional service filter shared by monitoring queries.
type window struct {
	Since, Bucket float64
	Hours         float64
	Service       string
}

func readWindow(r *Request) window {
	hours, err := strconv.ParseFloat(r.R.URL.Query().Get("hours"), 64)
	if err != nil || hours <= 0 || math.IsNaN(hours) {
		hours = 24
	}
	hours = math.Min(hours, 24*90)
	// About 48 buckets, rounded to a whole minute.
	bucket := math.Max(60, math.Round(hours*3600/48/60)*60)
	return window{Since: domain.Now() - hours*3600, Bucket: bucket, Hours: hours, Service: r.R.URL.Query().Get("service")}
}

func rows(ctx context.Context, db *store.Store, sql string, args ...any) ([]M, error) {
	res, err := db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	fields := res.FieldDescriptions()
	out := []M{}
	for res.Next() {
		values, err := res.Values()
		if err != nil {
			return nil, err
		}
		m := M{}
		for i, f := range fields {
			m[string(f.Name)] = values[i]
		}
		out = append(out, m)
	}
	return out, res.Err()
}

const traceScope = `FROM arl.traces t JOIN arl.sessions s ON s.id=t.session_id WHERE t.project_id=$1 AND t.started_at>=$2 AND ($3='' OR s.service=$3)`

func monitorMetrics(r *Request) (any, error) {
	ctx := r.R.Context()
	w := readWindow(r)
	args := []any{r.DB.Project, w.Since, w.Service}
	totals, err := rows(ctx, r.DB, `SELECT count(*)::int AS traces,
 count(*) FILTER (WHERE t.status='failed' OR t.errors>0)::int AS errored,
 count(*) FILTER (WHERE t.status='running')::int AS running,
 count(*) FILTER (WHERE t.verdict='pass')::int AS passed,
 count(*) FILTER (WHERE t.verdict='fail')::int AS failed,
 COALESCE(sum(t.cost_usd),0)::float8 AS cost_usd, COALESCE(sum(t.tokens_in),0)::bigint AS tokens_in, COALESCE(sum(t.tokens_out),0)::bigint AS tokens_out,
 COALESCE(sum(t.model_calls),0)::bigint AS model_calls, COALESCE(sum(t.tool_calls),0)::bigint AS tool_calls,
 percentile_cont(0.5) WITHIN GROUP (ORDER BY t.duration_ms) AS p50_ms,
 percentile_cont(0.95) WITHIN GROUP (ORDER BY t.duration_ms) AS p95_ms,
 percentile_cont(0.99) WITHIN GROUP (ORDER BY t.duration_ms) AS p99_ms,
 count(DISTINCT t.thread_id)::int AS threads, count(DISTINCT t.user_id)::int AS users `+traceScope, args...)
	if err != nil {
		return nil, err
	}
	series, err := rows(ctx, r.DB, `SELECT (floor(t.started_at/$4)*$4)::float8 AS ts, count(*)::int AS traces,
 count(*) FILTER (WHERE t.status='failed' OR t.errors>0)::int AS errored,
 count(*) FILTER (WHERE t.verdict='fail')::int AS failed,
 COALESCE(sum(t.cost_usd),0)::float8 AS cost_usd, COALESCE(sum(t.tokens_in+t.tokens_out),0)::bigint AS tokens,
 percentile_cont(0.5) WITHIN GROUP (ORDER BY t.duration_ms) AS p50_ms,
 percentile_cont(0.95) WITHIN GROUP (ORDER BY t.duration_ms) AS p95_ms `+traceScope+` GROUP BY 1 ORDER BY 1`, r.DB.Project, w.Since, w.Service, w.Bucket)
	if err != nil {
		return nil, err
	}
	services, err := rows(ctx, r.DB, `SELECT s.service, count(*)::int AS traces, count(*) FILTER (WHERE t.status='failed' OR t.errors>0)::int AS errored,
 COALESCE(sum(t.cost_usd),0)::float8 AS cost_usd, percentile_cont(0.95) WITHIN GROUP (ORDER BY t.duration_ms) AS p95_ms, max(t.source) AS source `+traceScope+` GROUP BY 1 ORDER BY 2 DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	models, err := rows(ctx, r.DB, `SELECT COALESCE(NULLIF(e.event#>>'{data,model}',''),'unknown') AS model, count(*)::int AS calls,
 count(*) FILTER (WHERE e.event#>>'{data,status}'='error')::int AS errored,
 COALESCE(sum((e.event#>>'{data,tokens_in}')::float8),0)::float8 AS tokens_in, COALESCE(sum((e.event#>>'{data,tokens_out}')::float8),0)::float8 AS tokens_out,
 percentile_cont(0.5) WITHIN GROUP (ORDER BY (e.event#>>'{data,duration_ms}')::float8) AS p50_ms,
 percentile_cont(0.95) WITHIN GROUP (ORDER BY (e.event#>>'{data,duration_ms}')::float8) AS p95_ms
 FROM arl.trace_events e JOIN arl.traces t ON t.id=e.trace_id JOIN arl.sessions s ON s.id=t.session_id
 WHERE t.project_id=$1 AND t.started_at>=$2 AND ($3='' OR s.service=$3) AND e.kind='model.response' GROUP BY 1 ORDER BY 2 DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	costs, err := rows(ctx, r.DB, `SELECT COALESCE(NULLIF(e.event#>>'{data,model}',''),'unknown') AS model, COALESCE(sum((e.event#>>'{data,amount_usd}')::float8),0)::float8 AS cost_usd,
 count(*) FILTER (WHERE e.event#>'{data,amount_usd}' IS NULL OR e.event#>'{data,amount_usd}'='null'::jsonb)::int AS unknown
 FROM arl.trace_events e JOIN arl.traces t ON t.id=e.trace_id JOIN arl.sessions s ON s.id=t.session_id
 WHERE t.project_id=$1 AND t.started_at>=$2 AND ($3='' OR s.service=$3) AND e.kind='cost.record' GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	byModel := map[string]M{}
	for _, c := range costs {
		byModel[domain.Str(c["model"])] = c
	}
	for _, m := range models {
		c := byModel[domain.Str(m["model"])]
		m["cost_usd"], m["unknown_cost_calls"] = c["cost_usd"], c["unknown"]
	}
	tools, err := rows(ctx, r.DB, `SELECT c.event#>>'{data,tool}' AS tool, count(*)::int AS calls,
 count(*) FILTER (WHERE res.event#>>'{data,status}'='error')::int AS errored,
 percentile_cont(0.95) WITHIN GROUP (ORDER BY (res.event#>>'{data,duration_ms}')::float8) AS p95_ms
 FROM arl.trace_events c JOIN arl.traces t ON t.id=c.trace_id JOIN arl.sessions s ON s.id=t.session_id
 LEFT JOIN arl.trace_events res ON res.trace_id=c.trace_id AND res.kind='tool.result' AND res.event#>>'{data,call_id}'=c.event#>>'{data,call_id}'
 WHERE t.project_id=$1 AND t.started_at>=$2 AND ($3='' OR s.service=$3) AND c.kind='tool.call' GROUP BY 1 ORDER BY 2 DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	feedback, err := rows(ctx, r.DB, `SELECT f.name, f.source, count(*)::int AS n, avg(f.value)::float8 AS mean,
 count(*) FILTER (WHERE f.value>=0.5)::int AS positive
 FROM arl.feedback f JOIN arl.traces t ON t.id=f.trace_id JOIN arl.sessions s ON s.id=t.session_id
 WHERE t.project_id=$1 AND t.started_at>=$2 AND ($3='' OR s.service=$3) GROUP BY 1,2 ORDER BY 3 DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	failing, err := rows(ctx, r.DB, `SELECT c->>'name' AS name, count(*)::int AS failures
 FROM arl.documents d JOIN arl.traces t ON t.id=d.id AND t.project_id=d.project_id JOIN arl.sessions s ON s.id=t.session_id,
 jsonb_array_elements(d.data#>'{summary,checks}') c
 WHERE d.project_id=$1 AND d.kind='trace_summaries' AND t.started_at>=$2 AND ($3='' OR s.service=$3) AND c->'passed'='false'::jsonb
 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	total := totals[0]
	n := domain.Num(total["traces"])
	if n > 0 {
		total["error_rate"] = domain.Num(total["errored"]) / n
	}
	if judged := domain.Num(total["passed"]) + domain.Num(total["failed"]); judged > 0 {
		total["pass_rate"] = domain.Num(total["passed"]) / judged
	}
	return M{"window": M{"hours": w.Hours, "since": w.Since, "bucket_s": w.Bucket, "service": w.Service}, "totals": total, "series": series, "services": services, "models": models, "tools": tools, "feedback": feedback, "failing_checks": failing}, nil
}

// monitorFacets lists the values trace filters can take.
func monitorFacets(r *Request) (any, error) {
	ctx := r.R.Context()
	services, err := rows(ctx, r.DB, "SELECT DISTINCT service FROM arl.sessions WHERE project_id=$1 ORDER BY 1 LIMIT 200", r.DB.Project)
	if err != nil {
		return nil, err
	}
	models, err := rows(ctx, r.DB, "SELECT DISTINCT unnest(models) AS model FROM arl.traces WHERE project_id=$1 ORDER BY 1 LIMIT 200", r.DB.Project)
	if err != nil {
		return nil, err
	}
	tags, err := rows(ctx, r.DB, "SELECT DISTINCT unnest(tags) AS tag FROM arl.traces WHERE project_id=$1 ORDER BY 1 LIMIT 200", r.DB.Project)
	if err != nil {
		return nil, err
	}
	names, err := rows(ctx, r.DB, "SELECT DISTINCT name FROM arl.feedback WHERE project_id=$1 ORDER BY 1 LIMIT 200", r.DB.Project)
	if err != nil {
		return nil, err
	}
	pick := func(rs []M, key string) []string {
		out := []string{}
		for _, row := range rs {
			if v := strings.TrimSpace(domain.Str(row[key])); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return M{"services": pick(services, "service"), "models": pick(models, "model"), "tags": pick(tags, "tag"), "feedback": pick(names, "name")}, nil
}
