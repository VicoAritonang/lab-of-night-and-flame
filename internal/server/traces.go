package server

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
	"go.vicoworks.com/agent-reliability-lab/internal/workbench"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) traceRoutes() {
	s.route("GET /api/agent-lab/status", traceStatus)
	s.route("POST /api/agent-lab/listener/start", func(r *Request) (any, error) { s.Hub.Start(r.DB.Project); return traceStatus(r) })
	s.route("POST /api/agent-lab/listener/stop", func(r *Request) (any, error) { s.Hub.Stop(r.DB.Project); return traceStatus(r) })
	s.route("POST /api/agent-lab/config", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		cfg, err := tracelab.CheckConfig(body)
		if err != nil {
			return nil, invalid(err.Error())
		}
		if err = r.DB.Put(r.R.Context(), "settings", "agent_lab", cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	})
	s.route("GET /api/agent-lab/connection.md", func(r *Request) (any, error) {
		r.W.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err := r.W.Write([]byte(s.Doc))
		return nil, err
	})
	s.route("POST /api/agent-lab/ticket", wsTicket)
	s.route("GET /api/agent-lab/sessions", traceSessions)
	s.route("GET /api/agent-lab/runs", traceRuns)
	s.route("GET /api/agent-lab/runs/{id}", traceDetail)
	s.route("GET /api/agent-lab/runs/{id}/events", traceEvents)
	s.route("GET /api/agent-lab/runs/{id}/export", traceExport)
}
func traceStatus(r *Request) (any, error) {
	peers := []M{}
	for _, p := range r.S.Hub.Peers(r.DB.Project) {
		peers = append(peers, p.Info())
	}
	return M{"listening": r.S.Hub.Listening(r.DB.Project), "always_on": r.S.Hub.AlwaysOn(), "active_connections": len(peers), "peers": peers, "protocol": "arl/1", "ws_path": "/api/agent-lab/ws", "config": tracelab.Config(r.R.Context(), r.DB)}, nil
}
func wsTicket(r *Request) (any, error) {
	s := r.S
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	now := time.Now()
	for key, t := range s.tickets {
		if t.Expiry.Before(now) {
			delete(s.tickets, key)
		}
	}
	if len(s.tickets) > 500 {
		return nil, &HTTPError{429, "too many outstanding WebSocket tickets"}
	}
	id := domain.ID("wst")
	s.tickets[id] = ticket{r.DB.Project, now.Add(30 * time.Second)}
	return M{"ticket": id, "expires_in": 30}, nil
}
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	project := r.Header.Get("X-Lab-Project")
	authenticated := s.Authorized(r)
	if key := r.URL.Query().Get("ticket"); key != "" {
		s.ticketMu.Lock()
		t, ok := s.tickets[key]
		delete(s.tickets, key)
		s.ticketMu.Unlock()
		authenticated = ok && t.Expiry.After(time.Now())
		if authenticated {
			project = t.Project
		}
	}
	if !authenticated {
		writeError(w, &HTTPError{401, "authentication required; use Authorization: Bearer"})
		return
	}
	select {
	case s.wsSlots <- struct{}{}:
		defer func() { <-s.wsSlots }()
	default:
		writeError(w, &HTTPError{429, "WebSocket capacity reached"})
		return
	}
	// Origin is checked centrally against exact configured origins before this handler.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(2 * tracelab.MaxMessage)
	db := s.DB.Scope(project)
	sid := ""
	done := make(chan struct{})
	defer close(done)
	defer func() {
		if sid != "" {
			s.Hub.Detach(sid)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.DB.Pool.Exec(ctx, "UPDATE arl.sessions SET connected_owner=NULL,connected_until=NULL,last_seen=now() WHERE id=$1 AND connected_owner=$2", sid, s.Hub.Owner)
		}
	}()
	peer := &tracelab.Peer{Conn: conn}
	write := func(msg M) error {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		return peer.Write(ctx, msg)
	}
	for {
		timeout := 90 * time.Second
		if sid == "" {
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		kind, body, readErr := conn.Read(ctx)
		cancel()
		if readErr != nil {
			return
		}
		if kind != websocket.MessageText {
			_ = write(M{"type": "error", "code": "invalid_message", "message": "Send JSON text frames; upload images over HTTP"})
			continue
		}
		msg, err := tracelab.AssertJSON(body)
		if err != nil {
			_ = write(tracelab.ErrorMessage(err))
			if sid == "" {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid handshake")
				return
			}
			continue
		}
		var reply M
		if sid == "" {
			if p := domain.Str(msg["project_id"]); p != "" {
				db = s.DB.Scope(p)
			}
			reply, err = tracelab.Hello(r.Context(), db, s.Hub, msg)
			if err == nil {
				sid = domain.Str(reply["session_id"])
				peer.Session, peer.Project, peer.Service, peer.Capabilities = sid, db.Project, domain.Str(msg["service"]), domain.Strings(msg["capabilities"])
				s.Hub.Attach(peer)
				sessionID := sid
				go func() {
					ticker := time.NewTicker(20 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-done:
							return
						case <-r.Context().Done():
							return
						case <-ticker.C:
							ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
							_, _ = db.Pool.Exec(ctx, "UPDATE arl.sessions SET connected_until=now()+interval '50 seconds' WHERE id=$1 AND connected_owner=$2", sessionID, s.Hub.Owner)
							cancel()
						}
					}
				}()
			}
		} else if msg["type"] == "ping" {
			reply = M{"type": "pong", "timestamp": domain.Now(), "listening": s.Hub.Listening(db.Project)}
		} else if msg["type"] == "replay.status" {
			if err = workbench.Status(r.Context(), db, msg); err != nil {
				err = tracelab.Errorf("invalid_replay", "Unknown replay or status")
			} else {
				reply = M{"type": "ack", "replay_id": msg["replay_id"]}
			}
		} else if !s.Hub.Listening(db.Project) {
			err = tracelab.Errorf("not_listening", "Listener stopped")
		} else {
			reply, err = tracelab.Ingest(r.Context(), db, sid, msg)
			if err == nil {
				if e := workbench.Link(r.Context(), db, msg, reply); e != nil {
					slog.Warn("cannot link trace to replay", "trace_id", reply["trace_id"])
				}
			}
			if err == nil && msg["event"] == "run.end" {
				_, _ = s.Q.Scope(db.Project).Enqueue(r.Context(), "trace.summarize", M{"trace_id": reply["trace_id"]}, fmt.Sprintf("trace:final:%v:%v", reply["trace_id"], reply["ordinal"]))
			}
		}
		if err != nil {
			reply = tracelab.ErrorMessage(err)
			reply["event_id"] = msg["event_id"]
			if write(reply) != nil {
				return
			}
			if sid == "" {
				_ = conn.Close(websocket.StatusPolicyViolation, domain.Str(reply["code"]))
				return
			}
			continue
		}
		if write(reply) != nil {
			return
		}
	}
}
func traceSessions(r *Request) (any, error) {
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT id,service,config,extract(epoch from created_at)::double precision,extract(epoch from last_seen)::double precision,COALESCE(connected_until>now(),false) FROM arl.sessions WHERE project_id=$1 ORDER BY last_seen DESC LIMIT 200", r.DB.Project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, service string
		var cfg M
		var created, last float64
		var connected bool
		if err = rows.Scan(&id, &service, &cfg, &created, &last, &connected); err != nil {
			return nil, err
		}
		out = append(out, M{"id": id, "service": service, "config": cfg, "created_at": created, "last_seen": last, "connected": connected})
	}
	return out, rows.Err()
}

// traceRuns lists traces, newest first. Filters: service_name, status,
// verdict, model, tag, thread_id, user_id, source, q (name), min_ms,
// errors=true, before (started_at cursor) and hours.
func traceRuns(r *Request) (any, error) {
	q := r.R.URL.Query()
	minMS, _ := strconv.ParseFloat(q.Get("min_ms"), 64)
	before, _ := strconv.ParseFloat(q.Get("before"), 64)
	hours, _ := strconv.ParseFloat(q.Get("hours"), 64)
	since := 0.
	if hours > 0 {
		since = domain.Now() - hours*3600
	}
	list, err := rows(r.R.Context(), r.DB, `SELECT r.id,r.session_id,r.external_id,r.name,r.status,r.started_at,r.finished_at,r.event_count,s.service,s.config->>'lab' AS lab,
 r.duration_ms,r.cost_usd,r.tokens_in,r.tokens_out,r.model_calls,r.tool_calls,r.errors,r.models,r.tags,r.thread_id,r.user_id,r.source,r.verdict,
 (SELECT count(*) FROM arl.feedback f WHERE f.trace_id=r.id)::int AS feedback_count
 FROM arl.traces r JOIN arl.sessions s ON s.id=r.session_id
 WHERE r.project_id=$1 AND ($2='' OR s.service=$2) AND ($3='' OR r.status=$3) AND ($5='' OR r.verdict=$5) AND ($6='' OR $6=ANY(r.models)) AND ($7='' OR $7=ANY(r.tags))
 AND ($8='' OR r.thread_id=$8) AND ($9='' OR r.user_id=$9) AND ($10='' OR r.source=$10) AND ($11='' OR r.name ILIKE '%'||$11||'%' OR r.external_id=$11 OR r.id=$11)
 AND ($12::float8<=0 OR r.duration_ms>=$12) AND (NOT $13 OR r.errors>0 OR r.status='failed') AND ($14::float8<=0 OR r.started_at<$14) AND r.started_at>=$15
 ORDER BY r.started_at DESC LIMIT $4`, r.DB.Project, q.Get("service_name"), q.Get("status"), queryInt(r.R, "limit", 100, 1, 500), q.Get("verdict"), q.Get("model"), q.Get("tag"), q.Get("thread_id"), q.Get("user_id"), q.Get("source"), q.Get("q"), minMS, q.Get("errors") == "true", before, since)
	if err != nil {
		return nil, err
	}
	return list, nil
}
func traceDetail(r *Request) (any, error) {
	id := r.R.PathValue("id")
	detail, err := tracelab.Detail(r.R.Context(), r.DB, id)
	if err != nil {
		return nil, err
	}
	summary, err := r.DB.Get(r.R.Context(), "trace_summaries", id)
	if err != nil || domain.Int(summary["event_count"]) != domain.Int(detail["event_count"]) || summary["status"] != detail["status"] {
		summary, err = r.Async("trace.summarize", M{"trace_id": id}, fmt.Sprintf("trace:summary:%s:%v:%v", id, detail["event_count"], detail["status"]))
		if err != nil {
			return nil, err
		}
	}
	detail["summary"] = summary["summary"]
	return detail, nil
}
func traceEvents(r *Request) (any, error) {
	id := r.R.PathValue("id")
	if _, err := tracelab.Detail(r.R.Context(), r.DB, id); err != nil {
		return nil, err
	}
	after, _ := strconv.ParseInt(r.R.URL.Query().Get("after"), 10, 64)
	if after < 0 {
		return nil, invalid("after must be non-negative")
	}
	items, more, err := tracelab.Events(r.R.Context(), r.DB, id, after, queryInt(r.R, "limit", 500, 1, 1000))
	return M{"items": items, "has_more": more}, err
}
func traceExport(r *Request) (any, error) {
	id := r.R.PathValue("id")
	detail, err := tracelab.Detail(r.R.Context(), r.DB, id)
	if err != nil {
		return nil, err
	}
	result, err := r.Async("trace.export", M{"trace_id": id}, fmt.Sprintf("trace:export:%s:%v:%v", id, detail["event_count"], detail["status"]))
	if err != nil {
		return nil, err
	}
	r.W.Header().Set("Content-Type", "application/json")
	r.W.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".json"))
	_, err = r.W.Write([]byte(domain.Str(result["json"])))
	return nil, err
}
