package tracelab

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/scoring"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
)

type M = domain.M

const MaxMessage = 1 << 20
const MaxEvents = 20000
const MaxTraceBytes = 32 << 20

var DefaultPhases = []string{"input", "retrieval", "agent", "output"}
var eventPattern = regexp.MustCompile("^[a-z][a-z0-9_.-]*$")
var digestPattern = regexp.MustCompile("^[a-f0-9]{64}$")
var sensitive = regexp.MustCompile("(?i)authorization|api[_-]?key|password|secret|(^|_)token$|cookie|resume_hash")

type ProtocolError struct{ Code, Message string }

func (e *ProtocolError) Error() string { return e.Message }
func bad(code, msg string) error       { return &ProtocolError{code, msg} }
func Redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := M{}
		for k, v := range x {
			if sensitive.MatchString(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = Redact(v)
			}
		}
		return out
	case []M:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = Redact(v)
		}
		return a
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = Redact(v)
		}
		return a
	case string:
		for _, key := range []string{"ARL_AUTH_TOKEN", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "NEON_DB_URL"} {
			if secret := os.Getenv(key); secret != "" {
				x = strings.ReplaceAll(x, secret, "[REDACTED]")
			}
		}
		return x
	default:
		return v
	}
}
func CheckConfig(raw M) (M, error) {
	for k := range raw {
		if k != "pricing" && k != "checks" {
			return nil, errors.New("unknown listener configuration field")
		}
	}
	pricing := domain.Obj(raw["pricing"])
	if len(pricing) > 100 {
		return nil, errors.New("maximum 100 model rates")
	}
	for _, v := range pricing {
		rate := domain.Obj(v)
		for k := range rate {
			if k != "input_per_million" && k != "output_per_million" {
				return nil, errors.New("unknown pricing field")
			}
		}
		for _, k := range []string{"input_per_million", "output_per_million"} {
			if rate[k] == nil || !finite(rate[k], true) {
				return nil, errors.New("pricing requires finite non-negative input/output rates")
			}
		}
	}
	checks, err := Checks(domain.Obj(raw["checks"]))
	if err != nil {
		return nil, err
	}
	return M{"pricing": pricing, "checks": checks}, nil
}
func Checks(raw M) (M, error) {
	out := M{"max_cost_usd": nil, "max_latency_ms": nil, "max_retries": nil, "output_contains": []string{}, "required_nodes": []string{}, "forbidden_tools": []string{}}
	for k, v := range raw {
		switch k {
		case "max_cost_usd", "max_latency_ms", "max_retries", "max_tool_calls", "max_model_calls":
			if v != nil && (!finite(v, true) || (k != "max_cost_usd" && k != "max_latency_ms" && domain.Num(v) != math.Trunc(domain.Num(v)))) {
				return nil, errors.New("invalid numeric check")
			}
		case "output_regex":
			pattern, ok := v.(string)
			if v != nil && (!ok || len(pattern) > 2000) {
				return nil, errors.New("output_regex must be a string")
			}
			if _, e := regexp.Compile(pattern); e != nil {
				return nil, errors.New("output_regex is not a valid regular expression")
			}
		case "output_json_schema":
			if v != nil {
				schema, ok := v.(map[string]any)
				if !ok {
					return nil, errors.New("output_json_schema must be an object")
				}
				if e := scoring.Validate(M{"key": "json_schema", "params": M{"schema": schema}}); e != nil {
					return nil, errors.New("output_json_schema is not a valid JSON schema")
				}
			}
		case "output_contains", "output_not_contains", "required_nodes", "forbidden_tools", "required_tools", "tool_sequence":
			a, ok := v.([]any)
			if !ok {
				if _, valid := v.([]string); !valid {
					return nil, errors.New("check lists must be arrays")
				}
				a = domain.Arr(v)
			}
			if len(a) > 100 {
				return nil, errors.New("maximum 100 items per check")
			}
			for _, s := range a {
				if _, ok := s.(string); !ok {
					return nil, errors.New("check list must contain strings")
				}
			}
		default:
			return nil, errors.New("unknown check: " + k)
		}
		out[k] = v
	}
	return out, nil
}

// OutputChecks evaluates the checks that only need the final output text.
// has=false means the run produced no output yet, so text checks are unknown.
func OutputChecks(config M, text string, has bool) []M {
	out := []M{}
	add := func(name string, passed any, evidence M) {
		if !has {
			passed = nil
		}
		out = append(out, M{"name": name, "passed": passed, "evidence": evidence})
	}
	for _, term := range domain.Strings(config["output_contains"]) {
		add("Output contains: "+term, strings.Contains(text, term), M{"expected": term})
	}
	for _, term := range domain.Strings(config["output_not_contains"]) {
		add("Output excludes: "+term, !strings.Contains(text, term), M{"forbidden": term})
	}
	if pattern := domain.Str(config["output_regex"]); pattern != "" {
		rx, e := regexp.Compile(pattern)
		add("Output matches /"+pattern+"/", e == nil && rx.MatchString(text), M{"pattern": pattern})
	}
	if schema := domain.Obj(config["output_json_schema"]); len(schema) > 0 {
		v := scoring.Run(M{"key": "json_schema", "params": M{"schema": schema}}, text, nil, nil)
		add("Output matches JSON schema", v.Verdict == "pass", v.Evidence)
	}
	return out
}

// ToolChecks evaluates the agent trajectory: which tools ran, in what order and
// how often. tools is the ordered list of called tool names.
func ToolChecks(config M, tools []string, modelCalls int, terminal bool) []M {
	out := []M{}
	add := func(name string, passed bool, evidence M) {
		var p any = passed
		if !terminal && !passed {
			// A running agent can still call the missing tool.
			p = nil
		}
		out = append(out, M{"name": name, "passed": p, "evidence": evidence})
	}
	called := map[string]int{}
	for _, t := range tools {
		called[t]++
	}
	for _, tool := range domain.Strings(config["required_tools"]) {
		add("Called tool: "+tool, called[tool] > 0, M{"calls": called[tool]})
	}
	if seq := domain.Strings(config["tool_sequence"]); len(seq) > 0 {
		next := 0
		for _, t := range tools {
			if next < len(seq) && t == seq[next] {
				next++
			}
		}
		add("Tool order: "+strings.Join(seq, " → "), next == len(seq), M{"expected": seq, "matched": next, "actual": tools})
	}
	if config["max_tool_calls"] != nil {
		out = append(out, M{"name": "Tool calls", "passed": len(tools) <= domain.Int(config["max_tool_calls"]), "evidence": M{"actual": len(tools), "limit": config["max_tool_calls"]}})
	}
	if config["max_model_calls"] != nil {
		out = append(out, M{"name": "Model calls", "passed": modelCalls <= domain.Int(config["max_model_calls"]), "evidence": M{"actual": modelCalls, "limit": config["max_model_calls"]}})
	}
	return out
}
func finite(v any, nonnegative bool) bool {
	switch v.(type) {
	case int, int64, float64, json.Number:
	default:
		return false
	}
	n := domain.Num(v)
	return !math.IsInf(n, 0) && !math.IsNaN(n) && (!nonnegative || n >= 0)
}
func Config(ctx context.Context, s *store.Store) M {
	m, e := s.Get(ctx, "settings", "agent_lab")
	if e != nil {
		m = M{}
	}
	delete(m, "id")
	out, e := CheckConfig(m)
	if e != nil {
		out, _ = CheckConfig(M{})
	}
	return out
}

type Hub struct {
	mu        sync.Mutex
	listening map[string]bool
	active    map[string]*Peer
	Owner     string
	// alwaysOn makes every project accept connections without pressing
	// "Start listening" (always-on development deployments).
	alwaysOn bool
}

// Peer is one connected backend session. Writes are serialized so the Lab can
// push replay commands while the socket loop replies to events.
type Peer struct {
	Conn         *websocket.Conn
	Session      string
	Project      string
	Service      string
	Capabilities []string
	Connected    float64
	mu           sync.Mutex
}

func (p *Peer) Write(ctx context.Context, msg M) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Conn.Write(ctx, websocket.MessageText, domain.JSON(msg))
}
func (p *Peer) Info() M {
	return M{"session_id": p.Session, "service": p.Service, "capabilities": p.Capabilities, "replay": domain.Has(p.Capabilities, "replay"), "connected_at": p.Connected}
}

var ErrNoPeer = errors.New("no connected agent accepts replays")

func NewHub() *Hub {
	return &Hub{listening: map[string]bool{}, active: map[string]*Peer{}, Owner: domain.ID("sync")}
}
func (h *Hub) Listening(project string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if on, set := h.listening[project]; set {
		return on
	}
	return h.alwaysOn
}
func (h *Hub) SetAlwaysOn(on bool)  { h.mu.Lock(); defer h.mu.Unlock(); h.alwaysOn = on }
func (h *Hub) AlwaysOn() bool       { h.mu.Lock(); defer h.mu.Unlock(); return h.alwaysOn }
func (h *Hub) Start(project string) { h.mu.Lock(); defer h.mu.Unlock(); h.listening[project] = true }
func (h *Hub) Count(project string) int {
	return len(h.Peers(project))
}
func (h *Hub) Peers(project string) []*Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []*Peer{}
	for _, p := range h.active {
		if p.Project == project {
			out = append(out, p)
		}
	}
	return out
}

// Push sends msg to the session, or to the most recent replay-capable session
// of service when session is empty.
func (h *Hub) Push(ctx context.Context, project, session, service string, msg M) (*Peer, error) {
	var target *Peer
	for _, p := range h.Peers(project) {
		if !domain.Has(p.Capabilities, "replay") || (session != "" && p.Session != session) || (service != "" && p.Service != service) {
			continue
		}
		if target == nil || p.Connected > target.Connected {
			target = p
		}
	}
	if target == nil {
		return nil, ErrNoPeer
	}
	return target, target.Write(ctx, msg)
}
func (h *Hub) Stop(project string) {
	h.mu.Lock()
	h.listening[project] = false
	sockets := []*websocket.Conn{}
	for sid, p := range h.active {
		if p.Project == project {
			sockets = append(sockets, p.Conn)
			delete(h.active, sid)
		}
	}
	h.mu.Unlock()
	for _, c := range sockets {
		_ = c.Close(websocket.StatusServiceRestart, "listener stopped")
	}
}
func (h *Hub) Close() {
	h.mu.Lock()
	ps := map[string]bool{}
	for p := range h.listening {
		ps[p] = true
	}
	for _, p := range h.active {
		ps[p.Project] = true
	}
	h.mu.Unlock()
	for p := range ps {
		h.Stop(p)
	}
}
func (h *Hub) Attach(p *Peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p.Connected = domain.Now()
	h.active[p.Session] = p
}
func (h *Hub) Detach(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, sid)
}
func Hello(ctx context.Context, s *store.Store, h *Hub, msg M) (M, error) {
	for k := range msg {
		if !domain.Has([]string{"type", "protocol", "service", "stage", "lab", "phases", "capabilities", "metadata", "session_id", "resume_token", "project_id"}, k) {
			return nil, bad("validation_error", "Unknown hello field")
		}
	}
	if msg["type"] != "hello" || msg["protocol"] != "arl/1" || msg["stage"] != "dev" {
		return nil, bad("validation_error", "Expected hello with protocol arl/1 and stage dev")
	}
	service, ok := msg["service"].(string)
	if !ok || service == "" || len(service) > 200 {
		return nil, bad("validation_error", "Invalid service")
	}
	domain.Default(msg, "lab", "agent")
	if !domain.Has([]string{"agent", "router", "generic"}, domain.Str(msg["lab"])) {
		return nil, bad("validation_error", "Invalid lab profile")
	}
	domain.Default(msg, "phases", DefaultPhases)
	phases := domain.Strings(msg["phases"])
	if len(phases) < 1 || len(phases) > 32 {
		return nil, bad("invalid_phases", "Declare 1-32 phases")
	}
	seen := map[string]bool{}
	for _, p := range phases {
		if p == "" || len(p) > 80 || seen[p] {
			return nil, bad("invalid_phases", "Phase names must be unique and 1-80 characters")
		}
		seen[p] = true
	}
	if !h.Listening(s.Project) {
		return nil, bad("not_listening", "Start listening in Agent lab first")
	}
	sid := domain.Str(msg["session_id"])
	token := ""
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if sid != "" {
		var hash, storedService string
		var stored M
		var owned bool
		e = tx.QueryRow(ctx, "SELECT resume_hash,service,config,COALESCE(connected_until>now(),false) FROM arl.sessions WHERE id=$1 AND project_id=$2 FOR UPDATE", sid, s.Project).Scan(&hash, &storedService, &stored, &owned)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, bad("invalid_resume", "Invalid session or resume token")
		}
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256([]byte(domain.Str(msg["resume_token"])))
		supplied := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(hash), []byte(supplied)) != 1 {
			return nil, bad("invalid_resume", "Invalid session or resume token")
		}
		if storedService != service || stored["lab"] != msg["lab"] || string(domain.JSONB(stored["phases"])) != string(domain.JSONB(phases)) {
			return nil, bad("session_mismatch", "Resume with the original service, lab and phases")
		}
		if owned {
			return nil, bad("session_busy", "Session already has an active connection")
		}
	} else {
		if msg["resume_token"] != nil {
			return nil, bad("invalid_resume", "resume_token requires session_id")
		}
		sid, token = domain.ID("session"), domain.ID("resume")+domain.ID("secret")
		sum := sha256.Sum256([]byte(token))
		config := M{"type": "hello", "protocol": "arl/1", "service": service, "stage": "dev", "lab": msg["lab"], "phases": phases, "capabilities": domain.Arr(msg["capabilities"]), "metadata": domain.Obj(msg["metadata"])}
		_, e = tx.Exec(ctx, "INSERT INTO arl.sessions(id,project_id,service,resume_hash,config) VALUES($1,$2,$3,$4,$5)", sid, s.Project, service, hex.EncodeToString(sum[:]), domain.JSONB(Redact(config)))
		if e != nil {
			return nil, e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE arl.sessions SET connected_owner=$2,connected_until=now()+interval '50 seconds',last_seen=now() WHERE id=$1", sid, h.Owner)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	out := M{"type": "hello_ack", "protocol": "arl/1", "session_id": sid, "lab": msg["lab"], "phases": phases, "max_message_bytes": MaxMessage, "max_events_per_run": MaxEvents, "max_trace_bytes": MaxTraceBytes, "heartbeat_seconds": 20, "features": []string{"graph", "timeline", "spans", "media", "costs", "checks", "resume", "export", "snapshots", "replay"}}
	if token != "" {
		out["resume_token"] = token
	}
	return out, nil
}
func Validate(msg M) error {
	for k := range msg {
		if !domain.Has([]string{"type", "event_id", "run_id", "event", "phase", "span_id", "parent_span_id", "timestamp", "data"}, k) {
			return bad("validation_error", "Unknown event envelope field")
		}
	}
	if msg["type"] != "event" {
		return bad("validation_error", "Expected event envelope")
	}
	for _, k := range []string{"event_id", "run_id"} {
		v, ok := msg[k].(string)
		if !ok || v == "" || len(v) > 128 {
			return bad("validation_error", "Invalid event_id or run_id")
		}
	}
	event, ok := msg["event"].(string)
	if !ok || len(event) > 100 || !eventPattern.MatchString(event) {
		return bad("validation_error", "Invalid event name")
	}
	domain.Default(msg, "phase", "agent")
	phase, ok := msg["phase"].(string)
	if !ok || phase == "" || len(phase) > 80 {
		return bad("validation_error", "Invalid phase")
	}
	for _, k := range []string{"span_id", "parent_span_id"} {
		domain.Default(msg, k, nil)
		if msg[k] != nil {
			v, ok := msg[k].(string)
			if !ok || len(v) > 128 {
				return bad("validation_error", "Invalid span ID")
			}
		}
	}
	domain.Default(msg, "timestamp", nil)
	if msg["timestamp"] != nil && !finite(msg["timestamp"], true) {
		return bad("validation_error", "Invalid timestamp")
	}
	domain.Default(msg, "data", M{})
	d, ok := msg["data"].(map[string]any)
	if !ok {
		return bad("validation_error", "data must be an object")
	}
	for _, k := range []string{"name", "label", "model", "category", "reason", "status", "from_node", "query", "tool", "call_id", "thread_id", "user_id"} {
		if v, exists := d[k]; exists {
			if _, ok := v.(string); !ok {
				return bad("invalid_data", k+" must be a string")
			}
		}
	}
	for _, k := range []string{"duration_ms", "attempt", "iteration", "tokens_in", "tokens_out", "depth", "amount_usd"} {
		if v, exists := d[k]; exists && v != nil {
			if !finite(v, true) || (k != "duration_ms" && k != "amount_usd" && domain.Num(v) != math.Trunc(domain.Num(v))) {
				return bad("invalid_data", k+" must be a finite non-negative number")
			}
		}
	}
	if d["score"] != nil && !finite(d["score"], false) {
		return bad("invalid_data", "score must be finite")
	}
	required := map[string][]string{"graph.node": {"node_id"}, "graph.visit": {"node_id"}, "graph.edge": {"source", "target"}, "tool.call": {"tool", "call_id"}, "tool.result": {"call_id"}, "cost.record": {"billing_id"}, "evaluation": {"name"}}
	for _, key := range required[event] {
		v, ok := d[key].(string)
		if !ok || v == "" || len(v) > 200 {
			return bad("invalid_data", key+" must be a nonempty string")
		}
	}
	if event == "span.start" || event == "span.end" {
		if domain.Str(msg["span_id"]) == "" {
			return bad("invalid_data", "span_id required")
		}
	}
	if msg["span_id"] != nil && msg["span_id"] == msg["parent_span_id"] {
		return bad("invalid_data", "span cannot be its own parent")
	}
	if event == "evaluation" {
		if _, ok := d["passed"].(bool); !ok {
			return bad("invalid_data", "evaluation requires boolean passed")
		}
	}
	if event == "run.start" {
		if _, e := Checks(domain.Obj(d["checks"])); e != nil {
			return bad("invalid_data", e.Error())
		}
		for _, k := range []string{"thread_id", "user_id"} {
			if len(domain.Str(d[k])) > 200 {
				return bad("invalid_data", k+" exceeds 200 characters")
			}
		}
		if tags, exists := d["tags"]; exists && tags != nil {
			list, ok := tags.([]any)
			if !ok || len(list) > 32 {
				return bad("invalid_data", "tags must be an array of at most 32 strings")
			}
			for _, t := range list {
				if s, ok := t.(string); !ok || s == "" || len(s) > 100 {
					return bad("invalid_data", "tags must be nonempty strings of at most 100 characters")
				}
			}
		}
	}
	if event == "state.snapshot" {
		for _, k := range []string{"state", "config"} {
			if v, exists := d[k]; exists && v != nil {
				if _, ok := v.(map[string]any); !ok {
					return bad("invalid_data", k+" must be an object")
				}
			}
		}
	}
	if event == "model.request" && d["request"] != nil {
		if _, ok := d["request"].(map[string]any); !ok {
			return bad("invalid_data", "request must be an object")
		}
	}
	if r, exists := d["replay"]; exists && event == "run.start" {
		if _, ok := r.(map[string]any); !ok {
			return bad("invalid_data", "replay must be an object")
		}
	}
	if event == "run.end" && d["status"] != nil && !domain.Has([]string{"completed", "failed", "cancelled"}, domain.Str(d["status"])) {
		return bad("invalid_data", "Invalid run.end status")
	}
	if event == "cost.record" {
		if d["source"] != nil && !domain.Has([]string{"reported", "estimated", "unknown"}, domain.Str(d["source"])) {
			return bad("invalid_data", "Invalid cost source")
		}
		if rate := d["pricing"]; rate != nil {
			_, e := CheckConfig(M{"pricing": M{"model": rate}})
			if e != nil {
				return bad("invalid_data", "Invalid pricing")
			}
		}
	}
	if parts, exists := d["content"]; exists {
		if _, ok := parts.([]any); !ok {
			return bad("invalid_data", "content must be an array")
		}
		for _, raw := range domain.Arr(parts) {
			part, ok := raw.(map[string]any)
			if !ok || !domain.Has([]string{"text", "json", "image", "audio", "video", "file"}, domain.Str(part["type"])) {
				return bad("invalid_data", "Invalid content part")
			}
			if part["type"] == "text" {
				if _, ok := part["text"].(string); !ok {
					return bad("invalid_data", "text content requires string text")
				}
			}
			if part["type"] == "image" && !digestPattern.MatchString(domain.Str(part["artifact"])) {
				return bad("invalid_data", "Upload image artifact via HTTP first")
			}
		}
	}
	return nil
}
func price(d, config M) (M, error) {
	out := domain.Copy(d)
	if out["amount_usd"] != nil {
		domain.Default(out, "source", "reported")
		if out["source"] == "unknown" {
			return nil, bad("invalid_data", "Unknown source cannot have an amount")
		}
		return out, nil
	}
	rate := domain.Obj(out["pricing"])
	if len(rate) == 0 {
		rate = domain.Obj(domain.Obj(config["pricing"])[domain.Str(out["model"])])
	}
	if len(rate) > 0 && out["tokens_in"] != nil && out["tokens_out"] != nil {
		amount := (domain.Num(out["tokens_in"])*domain.Num(rate["input_per_million"]) + domain.Num(out["tokens_out"])*domain.Num(rate["output_per_million"])) / 1e6
		if math.IsInf(amount, 0) || math.IsNaN(amount) {
			return nil, bad("invalid_data", "Cost overflow")
		}
		out["amount_usd"], out["source"], out["pricing"] = amount, "estimated", rate
	} else {
		out["amount_usd"], out["source"] = nil, "unknown"
	}
	return out, nil
}

// IngestOptions adapts ingestion for telemetry that does not come from a live
// arl/1 socket (OpenTelemetry export batches).
type IngestOptions struct {
	// Source is recorded on new traces ("sdk" or "otlp").
	Source string
	// Late accepts events for a run that already ended (spans exported after
	// their root) without reopening it.
	Late bool
	// ClientTime uses the event timestamp as the trace start instead of the
	// server clock.
	ClientTime bool
	// Passive leaves the session's connection lease untouched.
	Passive bool
}

func Ingest(ctx context.Context, s *store.Store, sid string, msg M) (M, error) {
	return IngestWith(ctx, s, sid, msg, IngestOptions{Source: "sdk"})
}

// rollup is the per-event increment of a trace's monitoring columns.
type rollup struct {
	cost                         float64
	tokensIn, tokensOut          int
	modelCalls, toolCalls, fails int
	model                        string
}

func rollupOf(event string, d M) rollup {
	r := rollup{}
	switch event {
	case "cost.record":
		r.cost = domain.Num(d["amount_usd"])
		r.tokensIn, r.tokensOut = domain.Int(d["tokens_in"]), domain.Int(d["tokens_out"])
	case "model.request":
		r.modelCalls, r.model = 1, domain.Str(d["model"])
		if len(r.model) > 200 {
			r.model = r.model[:200]
		}
	case "tool.call":
		r.toolCalls = 1
	}
	if event == "error" || d["status"] == "error" {
		r.fails = 1
	}
	return r
}

func IngestWith(ctx context.Context, s *store.Store, sid string, msg M, opts IngestOptions) (M, error) {
	if opts.Source == "" {
		opts.Source = "sdk"
	}
	if e := Validate(msg); e != nil {
		return nil, e
	}
	config := Config(ctx, s)
	clean := domain.Obj(Redact(msg))
	digest := domain.Hash(clean)
	d := domain.Obj(clean["data"])
	event := domain.Str(clean["event"])
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var profile M
	if e = tx.QueryRow(ctx, "SELECT config FROM arl.sessions WHERE id=$1 AND project_id=$2 FOR UPDATE", sid, s.Project).Scan(&profile); e != nil {
		return nil, e
	}
	var previousID, previousTrace string
	var previousOrd int64
	e = tx.QueryRow(ctx, "SELECT client_digest,trace_id,ordinal FROM arl.trace_events WHERE session_id=$1 AND event_id=$2", sid, clean["event_id"]).Scan(&previousID, &previousTrace, &previousOrd)
	if e == nil {
		if previousID != digest {
			return nil, bad("event_conflict", "event_id already used for another payload")
		}
		return M{"type": "ack", "event_id": clean["event_id"], "run_id": clean["run_id"], "trace_id": previousTrace, "ordinal": previousOrd, "duplicate": true}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if !domain.Has(domain.Strings(profile["phases"]), domain.Str(clean["phase"])) {
		return nil, bad("unknown_phase", "phase was not declared in handshake")
	}
	var rid, status string
	var count int
	var total int64
	e = tx.QueryRow(ctx, "SELECT id,status,event_count,total_bytes FROM arl.traces WHERE session_id=$1 AND external_id=$2 FOR UPDATE", sid, clean["run_id"]).Scan(&rid, &status, &count, &total)
	if errors.Is(e, pgx.ErrNoRows) {
		if event != "run.start" {
			return nil, bad("run_not_started", "Send run.start first")
		}
		rid = domain.ID("trace")
		checks := domain.Merge(domain.Obj(config["checks"]), domain.Obj(d["checks"]))
		name := domain.Str(d["name"])
		if name == "" {
			name = domain.Str(clean["run_id"])
		}
		started := domain.Now()
		if opts.ClientTime && clean["timestamp"] != nil {
			started = domain.Num(clean["timestamp"])
		}
		var thread, user any
		if v := domain.Str(d["thread_id"]); v != "" {
			thread = v
		}
		if v := domain.Str(d["user_id"]); v != "" {
			user = v
		}
		tags := domain.Strings(d["tags"])
		if tags == nil {
			tags = []string{}
		}
		status = "running"
		_, e = tx.Exec(ctx, "INSERT INTO arl.traces(id,project_id,session_id,external_id,name,checks,started_at,thread_id,user_id,tags,source) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", rid, s.Project, sid, clean["run_id"], name, domain.JSONB(checks), started, thread, user, tags, opts.Source)
		if e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	} else {
		if event == "run.start" {
			return nil, bad("run_exists", "Use a new run_id")
		}
		if status != "running" && (!opts.Late || event == "run.end") {
			return nil, bad("run_closed", "run.end is final; use another run_id")
		}
	}
	size := len(domain.JSONB(clean))
	if (count >= MaxEvents || total+int64(size) > MaxTraceBytes) && event != "run.end" {
		return nil, bad("trace_limit", "Trace limit reached; run.end is still allowed")
	}
	var billing any
	if event == "cost.record" {
		d, e = price(d, config)
		if e != nil {
			return nil, e
		}
		billing = d["billing_id"]
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM arl.trace_events WHERE trace_id=$1 AND billing_id=$2)", rid, billing).Scan(&exists); e != nil {
			return nil, e
		}
		if exists {
			return nil, bad("billing_conflict", "billing_id must be unique; resend original event_id for retry")
		}
		clean["data"] = d
	}
	for _, part := range domain.Maps(d["content"]) {
		if part["type"] == "image" {
			var exists bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM arl.artifacts WHERE project_id=$1 AND digest=$2)", s.Project, part["artifact"]).Scan(&exists); e != nil {
				return nil, e
			}
			if !exists {
				return nil, bad("missing_artifact", "Upload image using POST /api/artifacts first")
			}
		}
	}
	now := domain.Now()
	var ordinal int64
	e = tx.QueryRow(ctx, "INSERT INTO arl.trace_events(session_id,trace_id,event_id,client_digest,kind,phase,billing_id,received_at,event) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING ordinal", sid, rid, clean["event_id"], digest, event, clean["phase"], billing, now, domain.JSONB(clean)).Scan(&ordinal)
	if e != nil {
		return nil, e
	}
	// A late event (status already final) leaves status and finish time alone.
	keep := status != "running"
	var finished, duration any
	if event == "run.end" {
		status = domain.Str(d["status"])
		if status == "" {
			status = "completed"
		}
		finished = now
		if opts.ClientTime && clean["timestamp"] != nil {
			finished = domain.Num(clean["timestamp"])
		}
		if d["duration_ms"] != nil {
			duration = domain.Num(d["duration_ms"])
		}
	}
	r := rollupOf(event, d)
	_, e = tx.Exec(ctx, `UPDATE arl.traces SET status=CASE WHEN $5 THEN status ELSE $2 END,finished_at=CASE WHEN $5 THEN finished_at ELSE $3::float8 END,
 duration_ms=CASE WHEN $5 THEN duration_ms WHEN $6::float8 IS NOT NULL THEN $6::float8 WHEN $3::float8 IS NOT NULL THEN GREATEST(0,($3::float8-started_at)*1000) ELSE duration_ms END,
 event_count=event_count+1,total_bytes=total_bytes+$4,cost_usd=cost_usd+$7,tokens_in=tokens_in+$8,tokens_out=tokens_out+$9,model_calls=model_calls+$10,tool_calls=tool_calls+$11,errors=errors+$12,
 models=CASE WHEN $13='' OR $13=ANY(models) OR cardinality(models)>=20 THEN models ELSE array_append(models,$13::text) END WHERE id=$1`, rid, status, finished, size, keep, duration, r.cost, r.tokensIn, r.tokensOut, r.modelCalls, r.toolCalls, r.fails, r.model)
	if e != nil {
		return nil, e
	}
	if opts.Passive {
		_, e = tx.Exec(ctx, "UPDATE arl.sessions SET last_seen=now() WHERE id=$1", sid)
	} else {
		_, e = tx.Exec(ctx, "UPDATE arl.sessions SET last_seen=now(),connected_until=now()+interval '50 seconds' WHERE id=$1", sid)
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return M{"type": "ack", "event_id": clean["event_id"], "run_id": clean["run_id"], "trace_id": rid, "ordinal": ordinal, "duplicate": false}, nil
}
func Detail(ctx context.Context, s *store.Store, rid string) (M, error) {
	return detailQuery(ctx, s.Pool, s.Project, rid)
}

type reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func detailQuery(ctx context.Context, db reader, project, rid string) (M, error) {
	var id, sid, external, name, status, service, source string
	var checks, profile M
	var started float64
	var finished *float64
	var thread, user *string
	var tags []string
	var count int
	e := db.QueryRow(ctx, "SELECT r.id,r.session_id,r.external_id,r.name,r.status,r.checks,r.started_at,r.finished_at,r.event_count,s.service,s.config,r.thread_id,r.user_id,r.tags,r.source FROM arl.traces r JOIN arl.sessions s ON s.id=r.session_id WHERE r.id=$1 AND r.project_id=$2", rid, project).Scan(&id, &sid, &external, &name, &status, &checks, &started, &finished, &count, &service, &profile, &thread, &user, &tags, &source)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	return M{"id": id, "session_id": sid, "external_id": external, "name": name, "status": status, "check_config": checks, "started_at": started, "finished_at": finished, "event_count": count, "service": service, "config": profile, "lab": profile["lab"], "thread_id": thread, "user_id": user, "tags": tags, "source": source}, nil
}
func Events(ctx context.Context, s *store.Store, rid string, after int64, limit int) ([]M, bool, error) {
	return eventsQuery(ctx, s.Pool, s.Project, rid, after, limit)
}
func eventsQuery(ctx context.Context, db reader, project, rid string, after int64, limit int) ([]M, bool, error) {
	rows, e := db.Query(ctx, "SELECT e.ordinal,e.received_at,e.event FROM arl.trace_events e JOIN arl.traces t ON t.id=e.trace_id WHERE t.project_id=$1 AND e.trace_id=$2 AND e.ordinal>$3 ORDER BY e.ordinal LIMIT $4", project, rid, after, limit+1)
	if e != nil {
		return nil, false, e
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var ord int64
		var received float64
		var m M
		if e = rows.Scan(&ord, &received, &m); e != nil {
			return nil, false, e
		}
		m["ordinal"], m["received_at"] = ord, received
		out = append(out, m)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, rows.Err()
}
func Snapshot(ctx context.Context, s *store.Store, rid string) (M, []M, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, nil, e
	}
	defer tx.Rollback(ctx)
	detail, e := detailQuery(ctx, tx, s.Project, rid)
	if e != nil {
		return nil, nil, e
	}
	events, _, e := eventsQuery(ctx, tx, s.Project, rid, 0, MaxEvents+1)
	if e != nil {
		return nil, nil, e
	}
	return detail, events, tx.Commit(ctx)
}
func Summarize(ctx context.Context, s *store.Store, rid string) (M, error) {
	detail, events, e := Snapshot(ctx, s, rid)
	if e != nil {
		return nil, e
	}
	summary := Summary(detail, events)
	doc := M{"summary": summary, "event_count": len(events), "status": detail["status"]}
	if e = s.Put(ctx, "trace_summaries", rid, doc); e != nil {
		return nil, e
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE arl.traces SET verdict=$3 WHERE id=$1 AND project_id=$2", rid, s.Project, summary["verdict"]); e != nil {
		return nil, e
	}
	return M{"result_kind": "trace_summaries", "result_id": rid}, nil
}
func Summary(run M, events []M) M {
	visits, phases := map[string]int{}, M{}
	costs := []M{}
	retry, tools := 0, 0
	expected := map[string]bool{}
	toolNames := map[string]bool{}
	toolOrder := []string{}
	modelCalls := 0
	outputs := []string{}
	hasOutput := false
	elapsed := (domain.Now() - domain.Num(run["started_at"])) * 1000
	if run["finished_at"] != nil {
		elapsed = (domain.Num(run["finished_at"]) - domain.Num(run["started_at"])) * 1000
	}
	evaluations := []M{}
	for _, ev := range events {
		d := domain.Obj(ev["data"])
		kind := domain.Str(ev["event"])
		phase := domain.Str(ev["phase"])
		phases[phase] = domain.Int(phases[phase]) + 1
		switch kind {
		case "graph.visit":
			visits[domain.Str(d["node_id"])]++
		case "retrieval.retry", "agent.retry", "retry":
			retry++
		case "model.request":
			id := domain.Str(ev["span_id"])
			if id == "" {
				id = domain.Str(ev["event_id"])
			}
			expected[id] = true
			modelCalls++
		case "tool.call":
			tools++
			toolNames[domain.Str(d["tool"])] = true
			toolOrder = append(toolOrder, domain.Str(d["tool"]))
			expected[domain.Str(d["call_id"])] = true
		case "cost.record":
			costs = append(costs, d)
		case "output":
			hasOutput = true
			for _, part := range domain.Maps(d["content"]) {
				if part["type"] == "text" {
					outputs = append(outputs, domain.Str(part["text"]))
				}
			}
		case "run.end":
			if d["duration_ms"] != nil {
				elapsed = domain.Num(d["duration_ms"])
			}
		case "evaluation":
			evaluations = append(evaluations, M{"name": d["name"], "passed": d["passed"], "evidence": d["evidence"]})
		}
	}
	known, unknown, estimated, tin, tout := 0., 0, 0, 0, 0
	for _, c := range costs {
		if c["amount_usd"] == nil {
			unknown++
		} else {
			known += domain.Num(c["amount_usd"])
		}
		if c["source"] == "estimated" {
			estimated++
		}
		tin += domain.Int(c["tokens_in"])
		tout += domain.Int(c["tokens_out"])
		delete(expected, domain.Str(c["billing_id"]))
	}
	unreported := []string{}
	for id := range expected {
		unreported = append(unreported, id)
		unknown++
	}
	checks := []M{}
	terminal := run["status"] != "running"
	add := func(name string, passed any, evidence M) {
		if !terminal {
			passed = nil
		}
		checks = append(checks, M{"name": name, "passed": passed, "evidence": evidence})
	}
	config := domain.Obj(run["check_config"])
	if config["max_cost_usd"] != nil {
		var passed any = true
		if known > domain.Num(config["max_cost_usd"]) {
			passed = false
		} else if unknown > 0 || len(costs) == 0 {
			passed = nil
		}
		add("Cost budget", passed, M{"known_usd": known, "unknown_entries": unknown, "limit": config["max_cost_usd"]})
	}
	if config["max_latency_ms"] != nil {
		add("Latency", elapsed <= domain.Num(config["max_latency_ms"]), M{"duration_ms": elapsed, "limit": config["max_latency_ms"]})
	}
	if config["max_retries"] != nil {
		add("Retries", retry <= domain.Int(config["max_retries"]), M{"actual": retry, "limit": config["max_retries"]})
	}
	text := strings.Join(outputs, "\n")
	checks = append(checks, OutputChecks(config, text, hasOutput && terminal)...)
	checks = append(checks, ToolChecks(config, toolOrder, modelCalls, terminal)...)
	for _, node := range domain.Strings(config["required_nodes"]) {
		add("Visited: "+node, visits[node] > 0, M{"visits": visits[node]})
	}
	for _, tool := range domain.Strings(config["forbidden_tools"]) {
		add("Forbidden tool: "+tool, !toolNames[tool], M{"called": toolNames[tool]})
	}
	checks = append(checks, evaluations...)
	failed := run["status"] == "failed" || run["status"] == "cancelled"
	all := len(checks) > 0
	for _, check := range checks {
		if check["passed"] == false {
			failed = true
		}
		if check["passed"] != true {
			all = false
		}
	}
	verdict := "insufficient_evidence"
	if failed {
		verdict = "fail"
	} else if terminal && run["status"] == "completed" && all {
		verdict = "pass"
	}
	nvisits := 0
	for _, n := range visits {
		nvisits += n
	}
	return M{"events": len(events), "visits": nvisits, "unique_nodes": len(visits), "retries": retry, "tool_calls": tools, "model_calls": modelCalls, "tools": toolOrder, "duration_ms": elapsed, "phases": phases, "checks": checks, "verdict": verdict, "cost": M{"known_usd": known, "unknown_entries": unknown, "entries": len(costs), "estimated_entries": estimated, "tokens_in": tin, "tokens_out": tout, "unreported_operations": unreported}}
}
func ErrorMessage(err error) M {
	var p *ProtocolError
	if errors.As(err, &p) {
		return M{"type": "error", "code": p.Code, "message": p.Message}
	}
	return M{"type": "error", "code": "internal_error", "message": "Telemetry could not be persisted; retry the identical event after reconnect"}
}
func AssertJSON(body []byte) (M, error) {
	if len(body) > MaxMessage {
		return nil, bad("message_too_large", "JSON frame exceeds 1 MiB")
	}
	var m M
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, bad("validation_error", "Expected finite JSON object")
	}
	return m, nil
}
func Errorf(code, format string, v ...any) error { return bad(code, fmt.Sprintf(format, v...)) }
