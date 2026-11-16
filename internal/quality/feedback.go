// Package quality holds the production-quality loop around traces: feedback
// (end users, humans, automated evaluators), human annotation queues with
// inter-annotator and judge agreement, online evaluators that score sampled
// traces as they finish, and alert rules on rolling metrics.
package quality

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
)

type M = domain.M

const (
	Queues      = "annotation_queues"
	Items       = "annotation_items"
	Evaluators  = "evaluators"
	Alerts      = "alert_rules"
	AlertEvents = "alert_events"
)

var Sources = []string{"human", "user", "auto", "sdk"}

func finite(v any) bool {
	switch v.(type) {
	case int, int64, float64:
	default:
		return false
	}
	n := domain.Num(v)
	return !math.IsNaN(n) && !math.IsInf(n, 0)
}

// ValidateFeedback checks a feedback submission: a name plus a numeric value,
// a categorical label or a comment.
func ValidateFeedback(body M) (M, error) {
	name := strings.TrimSpace(domain.Str(body["name"]))
	if name == "" || len(name) > 100 {
		return nil, errors.New("name must be 1-100 characters")
	}
	out := M{"name": name, "value": nil, "label": nil, "comment": nil, "source": "user", "author": "anonymous", "metadata": domain.Obj(body["metadata"])}
	if v := body["value"]; v != nil {
		if b, ok := v.(bool); ok {
			v = map[bool]float64{true: 1, false: 0}[b]
		}
		if !finite(v) {
			return nil, errors.New("value must be a finite number or boolean")
		}
		out["value"] = domain.Num(v)
	}
	if l := domain.Str(body["label"]); l != "" {
		if len(l) > 200 {
			return nil, errors.New("label exceeds 200 characters")
		}
		out["label"] = l
	}
	if c := domain.Str(body["comment"]); c != "" {
		if len(c) > 5000 {
			return nil, errors.New("comment exceeds 5000 characters")
		}
		out["comment"] = c
	}
	if out["value"] == nil && out["label"] == nil && out["comment"] == nil {
		return nil, errors.New("provide value, label or comment")
	}
	if s := domain.Str(body["source"]); s != "" {
		if !domain.Has(Sources, s) || s == "auto" {
			return nil, errors.New("source must be user, human or sdk")
		}
		out["source"] = s
	}
	if a := strings.TrimSpace(domain.Str(body["author"])); a != "" {
		if len(a) > 100 {
			return nil, errors.New("author exceeds 100 characters")
		}
		out["author"] = a
	}
	if len(domain.JSON(out["metadata"])) > 16<<10 {
		return nil, errors.New("metadata exceeds 16 KiB")
	}
	return out, nil
}

// PutFeedback inserts or replaces feedback id on a trace of the project.
func PutFeedback(ctx context.Context, db *store.Store, id, traceID string, f M) error {
	tag, err := db.Pool.Exec(ctx, `INSERT INTO arl.feedback(id,project_id,trace_id,name,value,label,comment,source,author,queue_id,evaluator_id,metadata)
 SELECT $1,$2,t.id,$4,$5,$6,$7,$8,$9,$10,$11,$12 FROM arl.traces t WHERE t.id=$3 AND t.project_id=$2
 ON CONFLICT(id) DO UPDATE SET value=EXCLUDED.value,label=EXCLUDED.label,comment=EXCLUDED.comment,metadata=EXCLUDED.metadata,updated_at=now()`,
		id, db.Project, traceID, f["name"], f["value"], f["label"], f["comment"], f["source"], f["author"], nullable(f["queue_id"]), nullable(f["evaluator_id"]), domain.JSONB(domain.Obj(f["metadata"])))
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return err
}

func nullable(v any) any {
	if domain.Str(v) == "" {
		return nil
	}
	return domain.Str(v)
}

// ListFeedback returns a trace's feedback, newest first.
func ListFeedback(ctx context.Context, db *store.Store, traceID string) ([]M, error) {
	rows, err := db.Pool.Query(ctx, `SELECT id,name,value,label,comment,source,author,queue_id,evaluator_id,metadata,extract(epoch from created_at)::float8
 FROM arl.feedback WHERE project_id=$1 AND trace_id=$2 ORDER BY created_at DESC`, db.Project, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, name, source, author string
		var value *float64
		var label, comment, queue, evaluator *string
		var meta M
		var created float64
		if err = rows.Scan(&id, &name, &value, &label, &comment, &source, &author, &queue, &evaluator, &meta, &created); err != nil {
			return nil, err
		}
		out = append(out, M{"id": id, "trace_id": traceID, "name": name, "value": value, "label": label, "comment": comment, "source": source, "author": author, "queue_id": queue, "evaluator_id": evaluator, "metadata": meta, "created_at": created})
	}
	return out, rows.Err()
}

// ResolveTrace finds the trace a feedback request refers to: trace_id, or the
// agent's own run_id (optionally narrowed by service) for feedback sent later
// by the application, e.g. a thumbs-up in its UI.
func ResolveTrace(ctx context.Context, db *store.Store, body M) (string, error) {
	if id := domain.Str(body["trace_id"]); id != "" {
		return id, nil
	}
	run := domain.Str(body["run_id"])
	if run == "" {
		return "", errors.New("trace_id or run_id is required")
	}
	var id string
	err := db.Pool.QueryRow(ctx, `SELECT t.id FROM arl.traces t JOIN arl.sessions s ON s.id=t.session_id
 WHERE t.project_id=$1 AND t.external_id=$2 AND ($3='' OR s.service=$3) ORDER BY t.started_at DESC LIMIT 1`, db.Project, run, domain.Str(body["service"])).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return id, err
}
