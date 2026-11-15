package server

import (
	"context"
	"fmt"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/engine"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/queue"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/scoring"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) campaignRoutes() {
	s.route("POST /api/experiments/preflight", preflight)
	s.route("POST /api/experiments", createExperiment)
	s.route("GET /api/experiments", listExperiments)
	s.route("GET /api/experiments/{id}", experimentDetail)
	s.route("POST /api/experiments/{id}/launch", launchExperiment)
	s.route("GET /api/campaigns", listCampaigns)
	s.route("GET /api/campaigns/{id}", campaignDetail)
	s.route("GET /api/campaigns/{id}/grid", campaignGrid)
	s.route("GET /api/campaigns/{id}/events", campaignEvents)
	s.route("GET /api/campaigns/{id}/trials", campaignTrials)
	s.route("GET /api/campaigns/{id}/results", campaignResults)
	s.route("POST /api/campaigns/{id}/cancel", cancelCampaign)
	s.route("POST /api/campaigns/{id}/resume", resumeCampaign)
	s.route("POST /api/campaigns/{id}/rerun", rerunCampaign)
	s.route("POST /api/campaigns/{id}/rescore", rescoreCampaign)
	s.route("GET /api/trials/{id}", trialDetail)
	s.route("GET /api/campaigns/{id}/report.html", report)
	s.route("GET /api/campaigns/{id}/report.md", report)
	s.route("GET /api/campaigns/{id}/report.json", report)
	s.route("POST /api/playground", playground)
}
func preflight(r *Request) (any, error) {
	body, err := r.Body(2 << 20)
	if err != nil {
		return nil, err
	}
	p, err := engine.Plan(body)
	if err != nil {
		return nil, invalid(err.Error())
	}
	return r.Async("experiment.preflight", M{"plan": p}, fmt.Sprintf("preflight:%s:%d", domain.Hash(p), time.Now().Unix()/5))
}
func createExperiment(r *Request) (any, error) {
	body, err := r.Body(2 << 20)
	if err != nil {
		return nil, err
	}
	if name := domain.Str(body["name"]); name == "" || len(name) > 120 {
		return nil, invalid("name must be 1-120 characters")
	}
	p, err := engine.Plan(domain.Obj(body["plan"]))
	if err != nil {
		return nil, invalid(err.Error())
	}
	return r.Async("experiment.create", M{"name": body["name"], "hypothesis": domain.Str(body["hypothesis"]), "plan": p, "launch": r.R.URL.Query().Get("launch") == "true"}, r.Key("experiment:create"))
}
func listExperiments(r *Request) (any, error) {
	rows, err := r.DB.List(r.R.Context(), "experiments", nil, 10000)
	if err != nil {
		return nil, err
	}
	store.Order(rows, "created_at", true)
	for _, row := range rows {
		camps, err := r.DB.List(r.R.Context(), "campaigns", M{"experiment_id": row["id"]}, 500)
		if err != nil {
			return nil, err
		}
		store.Order(camps, "created_at", true)
		row["campaigns"] = len(camps)
		row["dataset_id"] = domain.Obj(row["plan"])["dataset_id"]
		row["last_campaign"] = nil
		if len(camps) > 0 {
			row["last_campaign"] = camps[0]["id"]
		}
	}
	return rows, nil
}
func experimentDetail(r *Request) (any, error) {
	exp, err := r.DB.Get(r.R.Context(), "experiments", r.R.PathValue("id"))
	if err != nil {
		return nil, err
	}
	camps, err := r.DB.List(r.R.Context(), "campaigns", M{"experiment_id": exp["id"]}, 500)
	if err != nil {
		return nil, err
	}
	store.Order(camps, "created_at", true)
	exp["campaigns"] = camps
	return exp, nil
}
func launchExperiment(r *Request) (any, error) {
	return r.Async("experiment.launch", M{"experiment_id": r.R.PathValue("id")}, r.Key("launch"))
}
func campaignList(r *Request, limit int) ([]M, error) {
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT data FROM arl.documents WHERE project_id=$1 AND kind='campaigns' ORDER BY created_at DESC LIMIT $2", r.DB.Project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var c M
		if err = rows.Scan(&c); err != nil {
			return nil, err
		}
		c["live"] = engine.Live(domain.Str(c["status"]))
		targets := []string{}
		for _, t := range domain.Maps(domain.Obj(c["plan"])["targets"]) {
			targets = append(targets, domain.Str(t["provider"])+"/"+domain.Str(t["model"]))
		}
		c["targets"] = targets
		out = append(out, c)
	}
	return out, rows.Err()
}
func listCampaigns(r *Request) (any, error) {
	return campaignList(r, queryInt(r.R, "limit", 50, 1, 500))
}
func campaignDetail(r *Request) (any, error) {
	id := r.R.PathValue("id")
	c, err := r.DB.Get(r.R.Context(), "campaigns", id)
	if err != nil {
		return nil, err
	}
	c["arms"] = engine.Arms(domain.Obj(c["plan"]))
	c["live"] = engine.Live(domain.Str(c["status"]))
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT data->>'status',data->>'outcome',count(*) FROM arl.documents WHERE project_id=$1 AND kind='trials' AND data->>'campaign_id'=$2 GROUP BY 1,2", r.DB.Project, id)
	if err != nil {
		return nil, err
	}
	counts := []M{}
	statuses, outcomes := M{}, M{}
	for rows.Next() {
		var status, outcome string
		var n int
		if err = rows.Scan(&status, &outcome, &n); err != nil {
			rows.Close()
			return nil, err
		}
		counts = append(counts, M{"status": status, "outcome": outcome, "n": n})
		statuses[status] = domain.Int(statuses[status]) + n
		outcomes[outcome] = domain.Int(outcomes[outcome]) + n
	}
	rows.Close()
	c["counts"] = counts
	state := engine.State(c)
	state["counts"], state["outcomes"] = statuses, outcomes
	c["state"] = state
	return c, nil
}

var statusCodes = M{"pending": 0, "running": 1, "completed": 2, "error": 3, "cancelled": 4, "not_applicable": 5, "interrupted": 6}
var outcomeCodes = M{"not_assessed": 0, "pass": 1, "fail": 2, "inconclusive": 3, "not_applicable": 4}

func campaignGrid(r *Request) (any, error) {
	c, err := r.DB.Get(r.R.Context(), "campaigns", r.R.PathValue("id"))
	if err != nil {
		return nil, err
	}
	keys := []string{}
	indices := map[string]int{}
	for i, a := range engine.Arms(domain.Obj(c["plan"])) {
		key := domain.Str(a["key"])
		keys = append(keys, key)
		indices[key] = i
	}
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT id,data->>'arm_key',data->>'status',data->>'outcome' FROM arl.documents WHERE project_id=$1 AND kind='trials' AND data->>'campaign_id'=$2 ORDER BY (data->>'order_index')::integer", r.DB.Project, c["id"])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	arms, statuses, outcomes := []int{}, []int{}, []int{}
	for rows.Next() {
		var id, arm, status, outcome string
		if err = rows.Scan(&id, &arm, &status, &outcome); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		arms = append(arms, indices[arm])
		statuses = append(statuses, domain.Int(statusCodes[status]))
		outcomes = append(outcomes, domain.Int(outcomeCodes[outcome]))
	}
	return M{"arms": keys, "ids": ids, "arm": arms, "status": statuses, "outcome": outcomes, "status_codes": statusCodes, "outcome_codes": outcomeCodes}, rows.Err()
}
func campaignTrials(r *Request) (any, error) {
	id := r.R.PathValue("id")
	if _, err := r.DB.Get(r.R.Context(), "campaigns", id); err != nil {
		return nil, err
	}
	q := r.R.URL.Query()
	where := "t.project_id=$1 AND t.kind='trials' AND t.data->>'campaign_id'=$2 AND ($3='' OR t.data->>'arm_key'=$3) AND ($4='' OR t.data->>'outcome'=$4) AND ($5='' OR t.data->>'status'=$5) AND ($6='' OR t.data->>'output_text' ILIKE '%'||$6||'%' OR s.data->>'key' ILIKE '%'||$6||'%')"
	join := " FROM arl.documents t JOIN arl.documents s ON s.project_id=t.project_id AND s.kind='samples' AND s.id=t.data->>'sample_id' WHERE " + where
	args := []any{r.DB.Project, id, q.Get("arm"), q.Get("outcome"), q.Get("status"), q.Get("q")}
	var total int
	if err := r.DB.Pool.QueryRow(r.R.Context(), "SELECT count(*)"+join, args...).Scan(&total); err != nil {
		return nil, err
	}
	offset, limit := queryInt(r.R, "offset", 0, 0, 50000), queryInt(r.R, "limit", 50, 1, 200)
	order := "(t.data->>'order_index')::integer"
	switch q.Get("sort") {
	case "latency":
		order = "(t.data->>'latency_ms')::double precision DESC NULLS LAST"
	case "cost":
		order = "(t.data->>'cost_usd')::double precision DESC NULLS LAST"
	case "sample":
		order = "(s.data->>'ordinal')::integer,t.data->>'arm_key'"
	}
	args = append(args, limit, offset)
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT (t.data-'output_text')||jsonb_build_object('output_preview',left(t.data->>'output_text',240),'sample_key',s.data->>'key')"+join+" ORDER BY "+order+" LIMIT $7 OFFSET $8", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var m M
		if err = rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return M{"items": out, "total": total, "offset": offset, "limit": limit}, rows.Err()
}
func trialDetail(r *Request) (any, error) {
	t, err := r.DB.Get(r.R.Context(), "trials", r.R.PathValue("id"))
	if err != nil {
		return nil, err
	}
	sample, err := r.DB.Get(r.R.Context(), "samples", domain.Str(t["sample_id"]))
	if err != nil {
		return nil, err
	}
	c, err := r.DB.Get(r.R.Context(), "campaigns", domain.Str(t["campaign_id"]))
	if err != nil {
		return nil, err
	}
	p := domain.Obj(c["plan"])
	ds, err := r.DB.Get(r.R.Context(), "datasets", domain.Str(p["dataset_id"]))
	if err != nil {
		return nil, err
	}
	t["sample"] = sample
	t["arm"] = nil
	for _, arm := range engine.Arms(p) {
		if arm["key"] == t["arm_key"] {
			t["arm"] = arm
		}
	}
	attempts, err := r.DB.List(r.R.Context(), "attempts", M{"trial_id": t["id"]}, 1000)
	if err != nil {
		return nil, err
	}
	store.Order(attempts, "n", false)
	assess, err := r.DB.List(r.R.Context(), "assessments", M{"trial_id": t["id"]}, 1000)
	if err != nil {
		return nil, err
	}
	store.Order(assess, "revision", true)
	t["attempts"], t["assessments"], t["scoring"], t["instructions"] = attempts, assess, c["scoring"], ds["instructions"]
	return t, nil
}
func campaignResults(r *Request) (any, error) {
	id := r.R.PathValue("id")
	c, err := r.DB.Get(r.R.Context(), "campaigns", id)
	if err != nil {
		return nil, err
	}
	cached, err := r.DB.Get(r.R.Context(), "results", id)
	if err == nil && cached["snapshot_status"] == c["status"] && domain.Num(cached["snapshot_done"]) == domain.Num(c["done"]) && domain.Num(cached["scoring_revision"]) == domain.Num(domain.Obj(c["scoring"])["revision"]) {
		return cached, nil
	}
	key := "analysis:" + id + ":" + domain.Hash([]any{c["status"], c["done"], domain.Obj(c["scoring"])["revision"]})
	return r.Async("campaign.analyze", M{"campaign_id": id}, key)
}
func cancelCampaign(r *Request) (any, error) {
	id := r.R.PathValue("id")
	_, err := r.Q.MutateAndEnqueue(r.R.Context(), "campaigns", id, func(c M) (queue.Item, error) {
		if !engine.Live(domain.Str(c["status"])) && !domain.Has([]string{"paused", "requires_reconciliation"}, domain.Str(c["status"])) {
			return queue.Item{}, conflict("nothing to cancel")
		}
		c["status"] = "cancelling"
		c["stop_reason"] = "user_cancelled"
		return queue.Item{Kind: "campaign.cancel", Payload: M{"campaign_id": id}, Key: fmt.Sprintf("cancel:%s:%v", id, c["generation"])}, nil
	})
	if err != nil {
		return nil, err
	}
	return M{"ok": true, "status": "cancelling"}, err
}
func resumeCampaign(r *Request) (any, error) {
	body := M{}
	if r.R.ContentLength > 0 {
		var err error
		body, err = r.Body(8192)
		if err != nil {
			return nil, err
		}
	}
	if body["budget_max_usd"] != nil && domain.Num(body["budget_max_usd"]) < 0 {
		return nil, invalid("negative budget")
	}
	return r.Async("campaign.resume", M{"campaign_id": r.R.PathValue("id"), "budget_max_usd": body["budget_max_usd"]}, r.Key("resume"))
}
func rerunCampaign(r *Request) (any, error) {
	return r.Async("campaign.rerun", M{"campaign_id": r.R.PathValue("id")}, r.Key("rerun"))
}
func rescoreCampaign(r *Request) (any, error) {
	id := r.R.PathValue("id")
	body, err := r.Body(1 << 20)
	if err != nil {
		return nil, err
	}
	specs := domain.Maps(body["scorers"])
	if len(specs) < 1 || len(specs) > 12 {
		return nil, invalid("scorers must have 1-12 entries")
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		sid := domain.Str(spec["id"])
		if sid == "" || seen[sid] {
			return nil, invalid("scorer IDs must be unique")
		}
		seen[sid] = true
		if err = scoring.Validate(spec); err != nil {
			return nil, invalid(err.Error())
		}
	}
	primary := domain.Str(body["primary_scorer"])
	if primary == "" {
		primary = domain.Str(specs[0]["id"])
	}
	if !seen[primary] {
		return nil, invalid("invalid primary_scorer")
	}
	c, err := r.Q.MutateAndEnqueue(r.R.Context(), "campaigns", id, func(c M) (queue.Item, error) {
		if engine.Live(domain.Str(c["status"])) {
			return queue.Item{}, conflict("campaign is running")
		}
		revision := domain.Int(domain.Obj(c["scoring"])["revision"]) + 1
		c["scoring"] = M{"scorers": specs, "primary": primary, "revision": revision}
		c["rescore_execution_status"] = c["status"]
		c["status"] = "grading"
		return queue.Item{Kind: "campaign.rescore", Payload: M{"campaign_id": id}, Key: fmt.Sprintf("rescore:%s:%d", id, revision)}, nil
	})
	if err != nil {
		return nil, err
	}
	revision := domain.Obj(c["scoring"])["revision"]
	return M{"ok": true, "revision": revision}, err
}
func report(r *Request) (any, error) {
	id := r.R.PathValue("id")
	ext := "json"
	if strings.HasSuffix(r.R.URL.Path, ".md") {
		ext = "md"
	} else if strings.HasSuffix(r.R.URL.Path, ".html") {
		ext = "html"
	}
	c, err := r.DB.Get(r.R.Context(), "campaigns", id)
	if err != nil {
		return nil, err
	}
	key := "report:" + id + ":" + domain.Hash([]any{c["status"], c["done"], domain.Obj(c["scoring"])["revision"]})
	result, err := r.Async("campaign.report", M{"campaign_id": id}, key)
	if err != nil {
		return nil, err
	}
	content := domain.Str(result[ext])
	mime := map[string]string{"json": "application/json", "html": "text/html; charset=utf-8", "md": "text/markdown; charset=utf-8"}[ext]
	r.W.Header().Set("Content-Type", mime)
	r.W.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", id+"."+ext))
	_, err = r.W.Write([]byte(content))
	return nil, err
}
func streamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
}
func event(w http.ResponseWriter, ev M) {
	if ord := ev["ordinal"]; ord != nil {
		_, _ = fmt.Fprintf(w, "id: %v\n", ord)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", domain.JSON(ev))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
func campaignEvents(r *Request) (any, error) {
	id := r.R.PathValue("id")
	c, err := r.DB.Get(r.R.Context(), "campaigns", id)
	if err != nil {
		return nil, err
	}
	streamHeaders(r.W)
	_, _ = fmt.Fprint(r.W, "retry: 2000\n\n")
	event(r.W, M{"type": "state", "state": engine.State(c)})
	after, _ := strconv.ParseInt(r.R.Header.Get("Last-Event-ID"), 10, 64)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		events, next, err := r.DB.Events(r.R.Context(), "campaign:"+id, after, 200)
		if err != nil {
			return nil, nil
		}
		after = next
		for _, ev := range events {
			event(r.W, ev)
		}
		select {
		case <-r.R.Context().Done():
			return nil, nil
		case <-ticker.C:
			if len(events) == 0 {
				c, err = r.DB.Get(r.R.Context(), "campaigns", id)
				if err != nil {
					return nil, nil
				}
				if !engine.Live(domain.Str(c["status"])) {
					event(r.W, M{"type": "idle", "status": c["status"]})
				} else {
					event(r.W, M{"type": "state", "state": engine.State(c)})
				}
			}
		}
	}
}
func playground(r *Request) (any, error) {
	body, err := r.Body(2 << 20)
	if err != nil {
		return nil, err
	}
	targets := domain.Maps(body["targets"])
	if len(targets) < 1 || len(targets) > 8 || len(domain.Str(body["prompt"])) > 100000 || len(domain.Arr(body["images"])) > 4 {
		return nil, invalid("invalid playground request")
	}
	if domain.Str(body["prompt"]) == "" && len(domain.Arr(body["images"])) == 0 {
		return nil, invalid("prompt or image required")
	}
	for _, t := range targets {
		if _, ok := r.Engine.Reg.Spec(r.R.Context(), domain.Str(t["provider"])); !ok || t["provider"] == "imported" {
			return nil, invalid("invalid playground provider")
		}
	}
	id := domain.ID("play")
	channel := "playground:" + id
	if err = r.DB.Put(r.R.Context(), "playgrounds", id, M{"status": "running", "request": body}); err != nil {
		return nil, err
	}
	for i, t := range targets {
		if _, err = r.Q.Enqueue(r.R.Context(), "playground.target", M{"channel": channel, "playground_id": id, "i": i, "target": t}, "play:"+id+fmt.Sprint(i)); err != nil {
			return nil, err
		}
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = r.DB.Update(ctx, "playgrounds", id, func(m M) error { m["status"] = "cancelled"; return nil })
	}()
	streamHeaders(r.W)
	after := int64(0)
	finished := map[int]bool{}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		evs, next, err := r.DB.Events(r.R.Context(), channel, after, 200)
		if err != nil {
			return nil, nil
		}
		after = next
		for _, ev := range evs {
			event(r.W, ev)
			if ev["type"] == "done" || ev["type"] == "error" {
				finished[domain.Int(ev["i"])] = true
			}
		}
		if len(finished) == len(targets) {
			event(r.W, M{"type": "end"})
			return nil, nil
		}
		select {
		case <-r.R.Context().Done():
			return nil, nil
		case <-ticker.C:
		}
	}
}
