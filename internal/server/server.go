package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"go.vicoworks.com/agent-reliability-lab/internal/config"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/engine"
	"go.vicoworks.com/agent-reliability-lab/internal/queue"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type M = domain.M

// Version is overridden at build time with -ldflags "-X .../internal/server.Version=v1.2.3".
var Version = "0.4.0-dev"

type Server struct {
	Config                      config.Config
	DB                          *store.Store
	Q                           *queue.Queue
	Engine                      *engine.Engine
	Hub                         *tracelab.Hub
	Mux                         *http.ServeMux
	Doc                         string
	UI                          fs.FS
	Started                     time.Time
	slots, wsSlots, uploadSlots chan struct{}
	ticketMu                    sync.Mutex
	tickets                     map[string]ticket
}
type ticket struct {
	Project string
	Expiry  time.Time
}
type Request struct {
	W      http.ResponseWriter
	R      *http.Request
	DB     *store.Store
	Q      *queue.Queue
	Engine *engine.Engine
	S      *Server
}
type HTTPError struct {
	Status int
	Detail any
}

func (e *HTTPError) Error() string  { return fmt.Sprint(e.Detail) }
func invalid(message string) error  { return &HTTPError{422, message} }
func conflict(message string) error { return &HTTPError{409, message} }
func New(ctx context.Context, c config.Config, db *store.Store, doc string) (*Server, error) {
	q, err := queue.New(ctx, db, c.QueueURL, c.Region, c.Concurrency)
	if err != nil {
		return nil, err
	}
	s := &Server{Config: c, DB: db, Q: q, Engine: engine.New(db, q, c), Hub: tracelab.NewHub(), Mux: http.NewServeMux(), Doc: doc, Started: time.Now(), slots: make(chan struct{}, domain.Max(1, c.MaxHTTP)), wsSlots: make(chan struct{}, domain.Max(1, c.MaxWebSockets)), uploadSlots: make(chan struct{}, 2), tickets: map[string]ticket{}}
	s.routes()
	return s, nil
}
func (s *Server) Authorized(r *http.Request) bool {
	if s.Config.AuthToken == "" {
		return true
	}
	supplied := ""
	if c, e := r.Cookie("arl_token"); e == nil {
		supplied = c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		supplied = h[7:]
	}
	return subtle.ConstantTimeCompare([]byte(s.Config.AuthToken), []byte(supplied)) == 1
}
func (s *Server) OriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	if e != nil || u.Scheme == "" {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	for _, allowed := range s.Config.Origins {
		if strings.TrimRight(allowed, "/") == origin {
			return true
		}
	}
	return false
}
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !strings.HasSuffix(r.URL.Path, "report.html") {
			w.Header().Set("X-Frame-Options", "DENY")
		}
		if !s.OriginOK(r) {
			writeError(w, &HTTPError{403, "origin not allowed"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Lab-Project, Last-Event-ID, Idempotency-Key")
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/v1/traces" {
			// OTLP exporters configured with the lab root as endpoint.
			r.URL.Path = "/api/otel/v1/traces"
		}
		if s.UI != nil && !strings.HasPrefix(r.URL.Path, "/api/") {
			s.static(w, r)
			return
		}
		if r.URL.Path == "/api/agent-lab/ws" {
			s.websocket(w, r)
			return
		}
		public := r.URL.Path == "/api/health" || strings.HasPrefix(r.URL.Path, "/api/auth/") || r.URL.Path == "/api/docs" || r.URL.Path == "/api/openapi.json"
		if !public && !s.Authorized(r) {
			writeError(w, &HTTPError{401, "authentication required"})
			return
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			writeError(w, &HTTPError{429, "gateway is at capacity; retry shortly"})
			return
		}
		defer func() {
			if recover() != nil {
				slog.Error("HTTP handler panic")
				writeError(w, &HTTPError{500, "internal server error"})
			}
		}()
		s.Mux.ServeHTTP(w, r)
	})
}
func (s *Server) route(pattern string, handler func(*Request) (any, error)) {
	s.Mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		project := r.Header.Get("X-Lab-Project")
		db := s.DB.Scope(project)
		req := &Request{w, r, db, s.Q.Scope(db.Project), s.Engine.Scope(db.Project), s}
		value, err := handler(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if value != nil {
			writeJSON(w, 200, tracelab.Redact(value))
		}
	})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	status, detail := 500, any("internal operation failed; inspect server logs and job state")
	var api *HTTPError
	if errors.As(err, &api) {
		status, detail = api.Status, api.Detail
	} else if errors.Is(err, store.ErrNotFound) {
		status, detail = 404, "resource not found"
	} else if errors.Is(err, store.ErrConflict) {
		status, detail = 409, "resource conflict"
	} else if errors.Is(err, context.DeadlineExceeded) {
		status, detail = 503, "async operation is still pending; retry with the same request or inspect /api/jobs"
	}
	if status >= 500 {
		slog.Warn("HTTP operation unavailable", "category", fmt.Sprintf("%T", err))
	}
	writeJSON(w, status, M{"detail": detail})
}
func (r *Request) Body(limit int64) (M, error) {
	r.R.Body = http.MaxBytesReader(r.W, r.R.Body, limit)
	dec := json.NewDecoder(r.R.Body)
	var m M
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, invalid("invalid JSON object")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, invalid("only one JSON object allowed")
	}
	return m, nil
}
func (r *Request) Async(kind string, payload M, key string, docs ...queue.Doc) (M, error) {
	id, err := r.Q.Enqueue(r.R.Context(), kind, payload, key, docs...)
	if err != nil {
		return nil, err
	}
	r.W.Header().Set("X-Lab-Job-ID", id)
	ctx, cancel := context.WithTimeout(r.R.Context(), r.S.Config.JobWait)
	defer cancel()
	result, err := r.Q.Wait(ctx, id)
	if err != nil {
		return nil, err
	}
	if domain.Bool(result["validation_error"]) {
		return nil, &HTTPError{422, M{"message": result["message"], "errors": result["errors"]}}
	}
	if result["result_kind"] != nil {
		return r.DB.Get(ctx, domain.Str(result["result_kind"]), domain.Str(result["result_id"]))
	}
	return result, nil
}
func queryInt(r *http.Request, key string, def, min, max int) int {
	n, e := strconv.Atoi(r.URL.Query().Get(key))
	if e != nil {
		n = def
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}
func (r *Request) Key(prefix string) string {
	if key := r.R.Header.Get("Idempotency-Key"); key != "" {
		return prefix + ":" + domain.Hash(key)
	}
	return prefix + ":" + domain.ID("request")
}
func page(rows []M, offset, limit int) M {
	n := len(rows)
	offset = domain.Min(offset, n)
	end := domain.Min(offset+limit, n)
	return M{"items": rows[offset:end], "total": n, "offset": offset, "limit": limit}
}
func (s *Server) routes() {
	s.route("GET /api/health", func(r *Request) (any, error) {
		if err := s.DB.Ready(r.R.Context()); err != nil {
			return nil, err
		}
		n, _ := r.DB.Count(r.R.Context(), "campaigns", M{"status": "running"})
		return M{"status": "ok", "version": Version, "uptime_s": time.Since(s.Started).Seconds(), "active_campaigns": n, "auth_required": s.Config.AuthToken != "", "mode": s.Config.Mode, "async_transport": map[bool]string{true: "sqs", false: "local-worker"}[s.Config.QueueURL != ""]}, nil
	})
	s.route("GET /api/auth/status", func(r *Request) (any, error) {
		return M{"auth_required": s.Config.AuthToken != "", "authenticated": s.Authorized(r.R)}, nil
	})
	s.route("POST /api/auth/login", func(r *Request) (any, error) {
		body, err := r.Body(4096)
		if err != nil {
			return nil, err
		}
		if s.Config.AuthToken != "" && subtle.ConstantTimeCompare([]byte(domain.Str(body["token"])), []byte(s.Config.AuthToken)) != 1 {
			_ = r.DB.Audit(r.R.Context(), "auth.login_failed", "owner", M{})
			return nil, &HTTPError{401, "invalid token"}
		}
		if s.Config.AuthToken != "" {
			secure := r.R.TLS != nil || r.R.Header.Get("X-Forwarded-Proto") == "https"
			http.SetCookie(r.W, &http.Cookie{Name: "arl_token", Value: s.Config.AuthToken, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 14 * 24 * 3600})
		}
		return M{"authenticated": true}, nil
	})
	s.route("POST /api/auth/logout", func(r *Request) (any, error) {
		http.SetCookie(r.W, &http.Cookie{Name: "arl_token", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		return M{"ok": true}, nil
	})
	s.route("GET /api/providers", func(r *Request) (any, error) { return r.Engine.Reg.Status(r.R.Context()), nil })
	s.route("GET /api/scorers", scorers)
	s.route("GET /api/catalog", catalog)
	s.route("POST /api/connections", connection)
	s.route("GET /api/overview", overview)
	s.route("GET /api/audit", audit)
	s.route("GET /api/projects", projects)
	s.route("POST /api/projects", createProject)
	s.route("GET /api/jobs/{id}", jobStatus)
	s.route("GET /api/openapi.json", openAPI)
	s.Mux.HandleFunc("GET /api/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>ARL Go API</title><h1>Agent Reliability Lab · Go API</h1><p><a href='/api/openapi.json'>OpenAPI document</a> · <a href='/api/agent-lab/connection.md'>WebSocket and backend integration</a></p><p>SYNC HTTP facade and persistent ASYNC jobs. X-Lab-Job-ID identifies durable jobs.</p>")
	})
	s.Mux.HandleFunc("POST /api/otel/v1/traces", s.otlpTraces)
	s.route("GET /api/otel/v1/traces", otlpInfo)
	s.datasetRoutes()
	s.campaignRoutes()
	s.traceRoutes()
	s.workbenchRoutes()
	s.monitorRoutes()
	s.qualityRoutes()
	s.promptRoutes()
	s.suiteRoutes()
	s.perfRoutes()
}

// static serves the embedded single-page UI; unknown paths fall back to index.html.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		writeError(w, &HTTPError{405, "method not allowed"})
		return
	}
	name := strings.Trim(path.Clean("/"+r.URL.Path), "/")
	candidates := []string{name, name + "/index.html", "index.html", "placeholder.html"}
	if name == "" {
		candidates = []string{"index.html", "placeholder.html"}
	}
	for _, c := range candidates {
		c = strings.TrimPrefix(c, "/")
		info, err := fs.Stat(s.UI, c)
		if err != nil || info.IsDir() {
			continue
		}
		if strings.HasPrefix(c, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, s.UI, c)
		return
	}
	http.NotFound(w, r)
}
