package otel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
)

// Result reports what a batch did. Finished lists traces whose root span has
// ended (summaries and online evaluators run for them).
type Result struct {
	Spans, Rejected, Events int
	Traces, Finished        []string
	Error                   string
}

// Session returns the synthetic session that owns a service's OTLP traces.
func Session(ctx context.Context, s *store.Store, service string) (string, error) {
	id := "otlp_" + domain.Hash(s.Project + "\x00" + service)[:24]
	config := M{"type": "hello", "protocol": "otlp", "service": service, "stage": "dev", "lab": "agent", "phases": tracelab.DefaultPhases, "capabilities": []string{}, "metadata": M{"source": "otlp"}}
	sum := sha256.Sum256([]byte(domain.ID("otlp") + domain.ID("secret")))
	_, err := s.Pool.Exec(ctx, "INSERT INTO arl.sessions(id,project_id,service,resume_hash,config) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING", id, s.Project, service, hex.EncodeToString(sum[:]), domain.JSONB(config))
	return id, err
}

// Ingest stores a decoded batch. Spans of one trace may arrive over several
// batches and in any order; every event id derives from the span, so a
// re-exported batch is acknowledged as duplicates instead of stored twice.
func Ingest(ctx context.Context, s *store.Store, spans []Span) (Result, error) {
	res := Result{Spans: len(spans)}
	sessions := map[string]string{}
	opts := tracelab.IngestOptions{Source: "otlp", Late: true, ClientTime: true, Passive: true}
	for _, t := range Group(spans) {
		sid := sessions[t.Service]
		if sid == "" {
			var err error
			if sid, err = Session(ctx, s, t.Service); err != nil {
				return res, err
			}
			sessions[t.Service] = sid
		}
		var traceID, name string
		err := s.Pool.QueryRow(ctx, "SELECT id,name FROM arl.traces WHERE session_id=$1 AND external_id=$2", sid, t.ID).Scan(&traceID, &name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return res, err
		}
		if traceID == "" {
			head := t.Spans[0]
			if t.Root != nil {
				head = *t.Root
			}
			data := Context(t)
			data["name"] = head.Name
			start := event(Span{TraceID: t.ID, SpanID: "run"}, "start", "run.start", "input", head.Start, data)
			delete(start, "span_id")
			ack, err := tracelab.IngestWith(ctx, s, sid, start, opts)
			if err != nil && !isProtocol(err) {
				return res, err
			}
			traceID = domain.Str(ack["trace_id"])
			if traceID == "" {
				// Another batch created the run concurrently.
				_ = s.Pool.QueryRow(ctx, "SELECT id FROM arl.traces WHERE session_id=$1 AND external_id=$2", sid, t.ID).Scan(&traceID)
			}
		} else {
			// The run was created from spans that arrived first: adopt the
			// root's name and start, and metadata first seen in this batch.
			meta := Context(t)
			rootName, rootStart := name, 0.
			if t.Root != nil {
				rootName, rootStart = t.Root.Name, t.Root.Start
			}
			if _, err = s.Pool.Exec(ctx, `UPDATE arl.traces SET name=$2,started_at=CASE WHEN $3::float8>0 THEN LEAST(started_at,$3::float8) ELSE started_at END,
 thread_id=COALESCE(thread_id,$4),user_id=COALESCE(user_id,$5) WHERE id=$1`, traceID, rootName, rootStart, meta["thread_id"], meta["user_id"]); err != nil {
				return res, err
			}
		}
		if traceID == "" {
			res.Rejected += len(t.Spans)
			continue
		}
		res.Traces = append(res.Traces, traceID)
		agents := AgentNames(t.Spans)
		events := []M{}
		if t.Root != nil {
			if input, _ := RootIO(*t.Root); input != "" {
				events = append(events, event(Span{TraceID: t.ID, SpanID: t.Root.SpanID}, "input", "input", "input", t.Root.Start, M{"content": []any{M{"type": "text", "text": input}}}))
			}
		}
		for _, span := range t.Spans {
			events = append(events, Events(span, agents)...)
		}
		if t.Root != nil {
			root := *t.Root
			if _, output := RootIO(root); output != "" {
				events = append(events, event(Span{TraceID: t.ID, SpanID: root.SpanID}, "output", "output", "output", root.End, M{"content": []any{M{"type": "text", "text": output}}}))
			}
			end := M{"status": "completed"}
			if root.Error {
				end["status"] = "failed"
			}
			if d := durationMS(root); d != nil {
				end["duration_ms"] = d
			}
			events = append(events, event(Span{TraceID: t.ID, SpanID: "run"}, "end", "run.end", "output", root.End, end))
		}
		rejected := map[string]bool{}
		for _, ev := range events {
			if _, err := tracelab.IngestWith(ctx, s, sid, ev, opts); err != nil {
				if !isProtocol(err) {
					return res, err
				}
				rejected[domain.Str(ev["span_id"])] = true
				res.Error = err.Error()
				continue
			}
			res.Events++
		}
		res.Rejected += len(rejected)
		if t.Root != nil {
			res.Finished = append(res.Finished, traceID)
		}
	}
	return res, nil
}

func isProtocol(err error) bool {
	var p *tracelab.ProtocolError
	return errors.As(err, &p)
}
