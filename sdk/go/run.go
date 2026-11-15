package arl

import (
	"context"
	"sync"
	"time"
)

// Text and JSON build content parts for input, output and model events.
func Text(s string) Object { return Object{"type": "text", "text": s} }
func JSON(v any) Object    { return Object{"type": "json", "value": v} }

type RunOptions struct {
	// ID is the run_id; a random one is generated when empty.
	ID     string
	Checks Object
	Data   Object
}

// Run is one agent job. Helpers pick phases from the handshake: the first phase
// for input and state, "agent" (or the middle phase) for model calls and the
// last phase for output.
type Run struct {
	c       *Client
	ctx     context.Context
	ID      string
	TraceID string
	started time.Time
	mu      sync.Mutex
	ended   bool
	Err     error
}

// StartRun emits run.start. Inside a replay handler's context, the run is
// linked to that replay and the replay checks are applied.
func (c *Client) StartRun(ctx context.Context, name string, opts ...RunOptions) *Run {
	opt := RunOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.ID == "" {
		opt.ID = NewID()
	}
	data := Object{"name": name}
	for k, v := range opt.Data {
		data[k] = v
	}
	checks := Object{}
	for k, v := range opt.Checks {
		checks[k] = v
	}
	if r := ReplayFrom(ctx); r != nil {
		data["replay"] = Object{"replay_id": r.ID, "scenario_id": r.ScenarioID, "source_trace_id": r.SourceTraceID}
		for k, v := range r.Checks {
			checks[k] = v
		}
	}
	if len(checks) > 0 {
		data["checks"] = checks
	}
	run := &Run{c: c, ctx: ctx, ID: opt.ID, started: time.Now()}
	ack, err := c.Emit(ctx, run.ID, "run.start", run.phase("input"), data)
	run.TraceID, run.Err = ack.TraceID, err
	return run
}

func (r *Run) phase(role string) string {
	phases := r.c.opts.Phases
	switch role {
	case "input":
		return phases[0]
	case "output":
		return phases[len(phases)-1]
	}
	if contains(phases, role) {
		return role
	}
	return phases[len(phases)/2]
}

// Emit sends a custom event in this run. The first error is kept in r.Err.
func (r *Run) Emit(event, phase string, data Object, opts ...EventOptions) Ack {
	ack, err := r.c.Emit(r.ctx, r.ID, event, phase, data, opts...)
	if err != nil && r.Err == nil {
		r.Err = err
	}
	return ack
}

// Input records what the agent received.
func (r *Run) Input(parts ...Object) { r.Emit("input", r.phase("input"), Object{"content": parts}) }

// Snapshot records the functional state needed to replay this run: state holds
// variables, memory or retrieved context; config holds the model, system
// prompt and parameters. The Workbench edits both before a replay.
func (r *Run) Snapshot(state, config Object) {
	data := Object{"state": state, "config": config}
	if state == nil {
		data["state"] = Object{}
	}
	if config == nil {
		data["config"] = Object{}
	}
	r.Emit("state.snapshot", r.phase("input"), data)
}

// ModelCall describes one LLM request/response. Request should be the
// OpenAI-compatible chat payload (model, messages, temperature, tools, ...);
// the Workbench can re-send it directly to any configured provider.
type ModelCall struct {
	SpanID, ParentSpanID, Provider, Model string
	Request                               Object
	Response                              string
	ToolCalls                             any
	TokensIn, TokensOut                   *int
	CostUSD                               *float64
	Duration                              time.Duration
	Error                                 error
}

func Int(v int) *int           { return &v }
func Float(v float64) *float64 { return &v }

// ModelCall emits model.request, model.response and cost.record.
func (r *Run) ModelCall(call ModelCall) {
	if call.SpanID == "" {
		call.SpanID = "model-" + NewID()[:8]
	}
	if call.Model == "" && call.Request != nil {
		call.Model, _ = call.Request["model"].(string)
	}
	opts := EventOptions{SpanID: call.SpanID, ParentSpanID: call.ParentSpanID}
	phase := r.phase("agent")
	req := Object{"model": call.Model}
	if call.Provider != "" {
		req["provider"] = call.Provider
	}
	if call.Request != nil {
		req["request"] = call.Request
		var last Object
		switch msgs := call.Request["messages"].(type) {
		case []any:
			if len(msgs) > 0 {
				last, _ = msgs[len(msgs)-1].(map[string]any)
			}
		case []Object:
			if len(msgs) > 0 {
				last = msgs[len(msgs)-1]
			}
		}
		if s, ok := last["content"].(string); ok {
			req["content"] = []Object{Text(s)}
		}
	}
	r.Emit("model.request", phase, req, opts)
	res := Object{"model": call.Model, "content": []Object{Text(call.Response)}}
	if call.ToolCalls != nil {
		res["tool_calls"] = call.ToolCalls
	}
	if call.Duration > 0 {
		res["duration_ms"] = float64(call.Duration.Microseconds()) / 1000
	}
	if call.Error != nil {
		res["status"], res["error"] = "error", call.Error.Error()
	}
	cost := Object{"billing_id": call.SpanID, "model": call.Model}
	if call.TokensIn != nil && call.TokensOut != nil {
		res["tokens_in"], res["tokens_out"] = *call.TokensIn, *call.TokensOut
		cost["tokens_in"], cost["tokens_out"] = *call.TokensIn, *call.TokensOut
	}
	r.Emit("model.response", phase, res, opts)
	if call.CostUSD != nil {
		cost["amount_usd"], cost["source"] = *call.CostUSD, "reported"
	}
	r.Emit("cost.record", phase, cost, opts)
}

// Tool records a tool call and its result.
func (r *Run) Tool(name string, arguments, result any, err error) {
	id := "call-" + NewID()[:8]
	opts := EventOptions{SpanID: id}
	phase := r.phase("agent")
	r.Emit("tool.call", phase, Object{"tool": name, "call_id": id, "arguments": arguments}, opts)
	data := Object{"call_id": id, "status": "ok", "result": result}
	if err != nil {
		data["status"], data["error"] = "error", err.Error()
	}
	r.Emit("tool.result", phase, data, opts)
}

// Output records the final answer.
func (r *Run) Output(parts ...Object) { r.Emit("output", r.phase("output"), Object{"content": parts}) }

// End emits run.end once; status is completed, failed or cancelled.
func (r *Run) End(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	r.Emit("run.end", r.phase("output"), Object{"status": status, "duration_ms": float64(time.Since(r.started).Microseconds()) / 1000})
}

// Finish ends the run as failed when err is non-nil and completed otherwise.
func (r *Run) Finish(err error) {
	if err != nil {
		r.Emit("error", r.phase("output"), Object{"reason": err.Error()})
		r.End("failed")
		return
	}
	r.End("completed")
}
