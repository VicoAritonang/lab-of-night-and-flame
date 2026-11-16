package quality

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/stats"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"github.com/jackc/pgx/v5"
)

var criterionKinds = []string{"binary", "scale", "label", "text"}

// ErrInvalid reports a request that asks for more items than one call allows.
var ErrInvalid = errors.New("at most 1000 traces per request")

// ValidateQueue normalizes an annotation queue: a rubric of named criteria
// and how many independent annotators each item needs.
func ValidateQueue(raw M) (M, error) {
	name := strings.TrimSpace(domain.Str(raw["name"]))
	if name == "" || len(name) > 200 {
		return nil, errors.New("name must be 1-200 characters")
	}
	rubric := []M{}
	seen := map[string]bool{}
	for _, c := range domain.Maps(raw["rubric"]) {
		cname := strings.TrimSpace(domain.Str(c["name"]))
		kind := domain.Str(c["kind"])
		if cname == "" || len(cname) > 100 || seen[cname] {
			return nil, errors.New("rubric criteria need unique names of 1-100 characters")
		}
		if !domain.Has(criterionKinds, kind) {
			return nil, errors.New("criterion kind must be binary, scale, label or text")
		}
		seen[cname] = true
		item := M{"name": cname, "kind": kind, "description": domain.Str(c["description"])}
		switch kind {
		case "scale":
			lo, hi := 1., 5.
			if c["min"] != nil {
				lo = domain.Num(c["min"])
			}
			if c["max"] != nil {
				hi = domain.Num(c["max"])
			}
			if !(hi > lo) || hi-lo > 100 {
				return nil, errors.New("scale needs min < max (range at most 100)")
			}
			item["min"], item["max"] = lo, hi
		case "label":
			options := domain.Strings(c["options"])
			if len(options) < 2 || len(options) > 20 {
				return nil, errors.New("label criteria need 2-20 options")
			}
			item["options"] = options
		}
		rubric = append(rubric, item)
	}
	if len(rubric) == 0 || len(rubric) > 20 {
		return nil, errors.New("rubric needs 1-20 criteria")
	}
	required := domain.Int(raw["required"])
	if required == 0 {
		required = 1
	}
	if required < 1 || required > 5 {
		return nil, errors.New("required annotators must be 1-5")
	}
	return M{"name": name, "description": domain.Str(raw["description"]), "instructions": domain.Str(raw["instructions"]), "rubric": rubric, "required": required}, nil
}

func itemID(queue, trace string) string { return "ann_" + domain.Hash([]string{queue, trace})[:24] }

// AddItems enqueues traces for review: explicit trace_ids, or a random sample
// of traces matching filter {service, verdict, errors, hours, source, limit}.
func AddItems(ctx context.Context, db *store.Store, queueID string, body M) (int, error) {
	ids := domain.Strings(body["trace_ids"])
	if f := domain.Obj(body["filter"]); len(ids) == 0 && len(f) > 0 {
		limit := domain.Int(f["limit"])
		if limit <= 0 || limit > 1000 {
			limit = 50
		}
		hours := domain.Num(f["hours"])
		since := 0.
		if hours > 0 {
			since = domain.Now() - hours*3600
		}
		rows, err := db.Pool.Query(ctx, `SELECT t.id FROM arl.traces t JOIN arl.sessions s ON s.id=t.session_id
 WHERE t.project_id=$1 AND t.status<>'running' AND t.started_at>=$2 AND ($3='' OR s.service=$3) AND ($4='' OR t.verdict=$4) AND (NOT $5 OR t.errors>0 OR t.status='failed') AND ($6='' OR t.source=$6)
 ORDER BY random() LIMIT $7`, db.Project, since, domain.Str(f["service"]), domain.Str(f["verdict"]), domain.Bool(f["errors"]), domain.Str(f["source"]), limit)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			ids = append(ids, id)
		}
		rows.Close()
	}
	if len(ids) > 1000 {
		return 0, ErrInvalid
	}
	docs := []M{}
	for _, id := range ids {
		var exists bool
		if err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM arl.traces WHERE id=$1 AND project_id=$2)", id, db.Project).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			docs = append(docs, M{"id": itemID(queueID, id), "queue_id": queueID, "trace_id": id, "status": "pending", "annotators": []string{}, "created_at": domain.Now()})
		}
	}
	before, _ := db.Count(ctx, Items, M{"queue_id": queueID})
	if err := db.Batch(ctx, Items, docs); err != nil {
		return 0, err
	}
	after, err := db.Count(ctx, Items, M{"queue_id": queueID})
	return after - before, err
}

// Next returns the oldest pending item the author has not annotated yet.
func Next(ctx context.Context, db *store.Store, queueID, author string) (M, error) {
	var item M
	err := db.Pool.QueryRow(ctx, `SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND data->>'queue_id'=$3 AND data->>'status'='pending'
 AND NOT (data->'annotators' @> to_jsonb($4::text)) AND NOT COALESCE(data->'skipped' @> to_jsonb($4::text),false) ORDER BY created_at,id LIMIT 1`, db.Project, Items, queueID, author).Scan(&item)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

// Submit records one annotator's scores for an item as human feedback.
func Submit(ctx context.Context, db *store.Store, queue M, itemID, author string, scores M, comment string) (M, error) {
	author = strings.TrimSpace(author)
	if author == "" || len(author) > 100 {
		return nil, errors.New("author must be 1-100 characters")
	}
	item, err := db.Get(ctx, Items, itemID)
	if err != nil || item["queue_id"] != queue["id"] {
		return nil, store.ErrNotFound
	}
	rows := []M{}
	for _, c := range domain.Maps(queue["rubric"]) {
		name := domain.Str(c["name"])
		v, given := scores[name]
		if !given || v == nil || v == "" {
			continue
		}
		f := M{"name": name, "source": "human", "author": author, "queue_id": queue["id"], "metadata": M{"criterion": c["kind"]}}
		switch c["kind"] {
		case "binary":
			b, ok := v.(bool)
			if !ok {
				return nil, errors.New(name + " expects true or false")
			}
			f["value"], f["label"] = map[bool]float64{true: 1, false: 0}[b], map[bool]string{true: "pass", false: "fail"}[b]
		case "scale":
			lo, hi := domain.Num(c["min"]), domain.Num(c["max"])
			if !finite(v) || domain.Num(v) < lo || domain.Num(v) > hi {
				return nil, errors.New(name + " is outside the scale")
			}
			f["value"], f["label"] = (domain.Num(v)-lo)/(hi-lo), nil
			domain.Obj(f["metadata"])["raw"] = domain.Num(v)
		case "label":
			s := domain.Str(v)
			if !domain.Has(domain.Strings(c["options"]), s) {
				return nil, errors.New(name + " must be one of the options")
			}
			f["label"] = s
		case "text":
			s := domain.Str(v)
			if len(s) > 5000 {
				return nil, errors.New(name + " exceeds 5000 characters")
			}
			f["comment"] = s
		}
		if comment != "" && f["comment"] == nil {
			f["comment"] = comment
		}
		rows = append(rows, f)
	}
	if len(rows) == 0 {
		return nil, errors.New("score at least one criterion")
	}
	for _, f := range rows {
		id := "fb_" + domain.Hash([]any{queue["id"], itemID, author, f["name"]})[:24]
		if err = PutFeedback(ctx, db, id, domain.Str(item["trace_id"]), f); err != nil {
			return nil, err
		}
	}
	return db.Update(ctx, Items, itemID, func(m M) error {
		annotators := domain.Strings(m["annotators"])
		if !domain.Has(annotators, author) {
			annotators = append(annotators, author)
		}
		m["annotators"] = annotators
		if len(annotators) >= domain.Int(queue["required"]) {
			m["status"], m["completed_at"] = "done", domain.Now()
		}
		return nil
	})
}

// Skip hides an item from one annotator without scoring it.
func Skip(ctx context.Context, db *store.Store, itemID, author string) (M, error) {
	return db.Update(ctx, Items, itemID, func(m M) error {
		skipped := domain.Strings(m["skipped"])
		if !domain.Has(skipped, author) {
			skipped = append(skipped, author)
		}
		m["skipped"] = skipped
		return nil
	})
}

// Progress counts a queue's items by status.
func Progress(ctx context.Context, db *store.Store) (map[string]M, error) {
	rows, err := db.Pool.Query(ctx, `SELECT data->>'queue_id', count(*) FILTER (WHERE data->>'status'='pending'), count(*) FILTER (WHERE data->>'status'='done')
 FROM arl.documents WHERE project_id=$1 AND kind=$2 GROUP BY 1`, db.Project, Items)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]M{}
	for rows.Next() {
		var id string
		var pending, done int
		if err = rows.Scan(&id, &pending, &done); err != nil {
			return nil, err
		}
		out[id] = M{"pending": pending, "done": done, "total": pending + done}
	}
	return out, rows.Err()
}

type rating struct {
	trace, author, source string
	value                 *float64
	label                 string
}

// Agreement reports, per rubric criterion, how consistently humans annotate
// (Krippendorff's alpha, plus Cohen's kappa for exactly two annotators) and
// how well automated evaluators that write feedback under the same name agree
// with the human consensus (accuracy, kappa, confusion counts for binary
// criteria; Pearson/Spearman for scales). This is how an LLM judge is
// calibrated before it is trusted for online evaluation.
func Agreement(ctx context.Context, db *store.Store, queue M) (M, error) {
	rows, err := db.Pool.Query(ctx, `SELECT f.trace_id,f.author,f.source,f.name,f.value,COALESCE(f.label,'') FROM arl.feedback f
 WHERE f.project_id=$1 AND ((f.source='human' AND f.queue_id=$2) OR (f.source='auto' AND f.trace_id IN (SELECT trace_id FROM arl.feedback WHERE project_id=$1 AND queue_id=$2)))`, db.Project, queue["id"])
	if err != nil {
		return nil, err
	}
	byName := map[string][]rating{}
	for rows.Next() {
		var r rating
		var name string
		if err = rows.Scan(&r.trace, &r.author, &r.source, &name, &r.value, &r.label); err != nil {
			rows.Close()
			return nil, err
		}
		byName[name] = append(byName[name], r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	criteria := []M{}
	for _, c := range domain.Maps(queue["rubric"]) {
		if c["kind"] == "text" {
			continue
		}
		criteria = append(criteria, criterionAgreement(c, byName[domain.Str(c["name"])]))
	}
	return M{"queue_id": queue["id"], "criteria": criteria, "methods": M{"inter_annotator": "Krippendorff's alpha (nominal for binary/label, interval for scale); Cohen's kappa when exactly two annotators", "judge": "automated feedback with the same name versus human consensus (majority label, or mean scale value)", "interpretation": "alpha ≥ 0.8 reliable, 0.667-0.8 tentative, below 0.667 the rubric or guidelines need work"}}, nil
}

func criterionAgreement(c M, ratings []rating) M {
	kind := domain.Str(c["kind"])
	nominal := kind != "scale"
	code := func(r rating) (float64, bool) {
		if kind == "label" {
			for i, o := range domain.Strings(c["options"]) {
				if o == r.label {
					return float64(i), true
				}
			}
			return 0, false
		}
		if r.value == nil {
			return 0, false
		}
		if kind == "binary" {
			if *r.value >= 0.5 {
				return 1, true
			}
			return 0, true
		}
		return *r.value, true
	}
	human := map[string][]float64{}
	pairs := map[string]map[string]string{}
	authors := map[string]bool{}
	auto := map[string]float64{}
	for _, r := range ratings {
		v, ok := code(r)
		if !ok {
			continue
		}
		if r.source == "auto" {
			auto[r.trace] = v
			continue
		}
		human[r.trace] = append(human[r.trace], v)
		authors[r.author] = true
		if pairs[r.author] == nil {
			pairs[r.author] = map[string]string{}
		}
		pairs[r.author][r.trace] = domain.Str(v)
	}
	units := [][]float64{}
	traces := []string{}
	for t, vs := range human {
		units = append(units, vs)
		traces = append(traces, t)
	}
	sort.Strings(traces)
	out := M{"name": c["name"], "kind": kind, "items": len(human), "annotators": len(authors), "ratings": 0, "alpha": stats.KrippendorffAlpha(units, !nominal), "kappa": nil}
	for _, u := range units {
		out["ratings"] = domain.Int(out["ratings"]) + len(u)
	}
	if len(authors) == 2 {
		names := []string{}
		for a := range authors {
			names = append(names, a)
		}
		a, b := []string{}, []string{}
		for _, t := range traces {
			a, b = append(a, pairs[names[0]][t]), append(b, pairs[names[1]][t])
		}
		out["kappa"] = stats.CohenKappa(a, b)
	}
	// Consensus per trace, then compare with the automated evaluator.
	consensus := map[string]float64{}
	for t, vs := range human {
		if nominal {
			counts := map[float64]int{}
			best, bestN := vs[0], 0
			for _, v := range vs {
				counts[v]++
				if counts[v] > bestN || (counts[v] == bestN && v < best) {
					best, bestN = v, counts[v]
				}
			}
			consensus[t] = best
		} else {
			consensus[t] = domain.Num(stats.Mean(vs))
		}
	}
	h, j := []float64{}, []float64{}
	hs, js := []string{}, []string{}
	agree := 0
	confusion := M{"tp": 0, "fp": 0, "fn": 0, "tn": 0}
	for _, t := range traces {
		jv, ok := auto[t]
		if !ok {
			continue
		}
		hv := consensus[t]
		h, j = append(h, hv), append(j, jv)
		hs, js = append(hs, domain.Str(hv)), append(js, domain.Str(jv))
		if hv == jv {
			agree++
		}
		if kind == "binary" {
			// Positive = the human marked the run as failing; how well does
			// the judge catch failures?
			switch {
			case hv == 0 && jv == 0:
				confusion["tp"] = domain.Int(confusion["tp"]) + 1
			case hv == 1 && jv == 0:
				confusion["fp"] = domain.Int(confusion["fp"]) + 1
			case hv == 0 && jv == 1:
				confusion["fn"] = domain.Int(confusion["fn"]) + 1
			default:
				confusion["tn"] = domain.Int(confusion["tn"]) + 1
			}
		}
	}
	judge := M{"paired_items": len(h)}
	if len(h) > 0 {
		if nominal {
			judge["accuracy"] = float64(agree) / float64(len(h))
			judge["kappa"] = stats.CohenKappa(hs, js)
			if kind == "binary" {
				judge["confusion"] = confusion
				judge["accuracy_interval"] = stats.Wilson(agree, len(h))
			}
		} else {
			judge["pearson"], judge["spearman"] = stats.Pearson(h, j), stats.Spearman(h, j)
			mae := 0.
			for i := range h {
				d := h[i] - j[i]
				if d < 0 {
					d = -d
				}
				mae += d
			}
			judge["mean_abs_error"] = mae / float64(len(h))
		}
	}
	out["judge"] = judge
	return out
}
