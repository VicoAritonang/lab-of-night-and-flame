// Package workbench stores agent scenarios captured from traces and runs them
// again with edited input, state, model or prompt (agent replay or a direct
// LLM step), similar to saving and re-sending requests in an API client.
package workbench

import (
	"context"
	"errors"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/provider"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/tracelab"
	"strings"
	"time"
)

type M = domain.M

const (
	Scenarios = "scenarios"
	Runs      = "scenario_runs"
)

var Modes = []string{"agent", "llm"}
var terminal = []string{"completed", "failed", "cancelled", "rejected"}

// Terminal reports whether a scenario run will not change any more.
func Terminal(status string) bool { return domain.Has(terminal, status) }

func text(parts any) (string, bool) {
	var b strings.Builder
	all := true
	for _, part := range domain.Maps(parts) {
		if part["type"] != "text" {
			all = false
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(domain.Str(part["text"]))
	}
	return b.String(), all
}

// Steps lists the LLM calls of a trace: each model.request with the response
// that shares its span.
func Steps(events []M) []M {
	responses := map[string]M{}
	for _, ev := range events {
		if ev["event"] == "model.response" && domain.Str(ev["span_id"]) != "" {
			responses[domain.Str(ev["span_id"])] = domain.Obj(ev["data"])
		}
	}
	out := []M{}
	for _, ev := range events {
		if ev["event"] != "model.request" {
			continue
		}
		d := domain.Obj(ev["data"])
		res := responses[domain.Str(ev["span_id"])]
		output, _ := text(res["content"])
		out = append(out, M{"span_id": ev["span_id"], "ordinal": ev["ordinal"], "phase": ev["phase"], "model": d["model"], "provider": d["provider"], "request": d["request"], "response": output, "tokens_in": res["tokens_in"], "tokens_out": res["tokens_out"], "replayable": d["request"] != nil})
	}
	return out
}

// split separates the system prompt from the remaining chat messages.
func split(messages []M) (string, []M) {
	system := []string{}
	rest := []M{}
	for _, m := range messages {
		if m["role"] == "system" {
			if s, ok := m["content"].(string); ok {
				system = append(system, s)
				continue
			}
		}
		rest = append(rest, m)
	}
	return strings.Join(system, "\n\n"), rest
}

var generation = []string{"temperature", "top_p", "max_tokens", "max_completion_tokens", "seed", "reasoning_effort"}

// FromTrace builds a scenario from a captured trace. The first state.snapshot
// is the replay starting point; the first replayable model.request seeds the
// LLM step.
func FromTrace(detail M, events []M, step string) M {
	config := M{}
	state := M{}
	var input any
	snapshots := 0
	for _, ev := range events {
		d := domain.Obj(ev["data"])
		switch ev["event"] {
		case "input":
			if input == nil {
				if s, all := text(d["content"]); all && s != "" {
					input = s
				} else if d["content"] != nil {
					input = d["content"]
				} else {
					input = d
				}
			}
		case "state.snapshot":
			if snapshots == 0 {
				state, config = domain.Obj(d["state"]), domain.Obj(d["config"])
			}
			snapshots++
		}
	}
	steps := Steps(events)
	var chosen M
	for _, s := range steps {
		if s["request"] != nil && (step == "" || s["span_id"] == step) {
			chosen = s
			break
		}
	}
	llm := M{"messages": []M{}}
	if chosen != nil {
		req := domain.Obj(chosen["request"])
		system, rest := split(domain.Maps(req["messages"]))
		llm["messages"] = rest
		if req["tools"] != nil {
			llm["tools"] = req["tools"]
		}
		if req["tool_choice"] != nil {
			llm["tool_choice"] = req["tool_choice"]
		}
		llm["span_id"] = chosen["span_id"]
		llm["baseline"] = M{"response": chosen["response"], "tokens_in": chosen["tokens_in"], "tokens_out": chosen["tokens_out"]}
		domain.Default(config, "model", domain.Str(req["model"]))
		if system != "" {
			domain.Default(config, "system_prompt", system)
		}
		if p := domain.Str(chosen["provider"]); p != "" {
			domain.Default(config, "provider", p)
		}
		params := domain.Obj(config["params"])
		for _, k := range generation {
			if req[k] != nil {
				domain.Default(params, k, req[k])
			}
		}
		if len(params) > 0 {
			config["params"] = params
		}
	}
	profile := domain.Obj(detail["config"])
	mode := "agent"
	if !domain.Has(domain.Strings(profile["capabilities"]), "replay") && chosen != nil {
		mode = "llm"
	}
	checks := M{}
	for k, v := range domain.Obj(detail["check_config"]) {
		if v != nil && !(isList(v) && len(domain.Arr(v)) == 0) {
			checks[k] = v
		}
	}
	return M{"name": domain.Str(detail["name"]), "collection": domain.Str(detail["service"]), "service": domain.Str(detail["service"]), "source_trace_id": detail["id"], "mode": mode, "input": input, "state": state, "config": config, "checks": checks, "llm": llm, "steps": steps, "snapshots": snapshots, "notes": ""}
}

func isList(v any) bool {
	switch v.(type) {
	case []any, []string, []M:
		return true
	}
	return false
}

// Normalize validates an edited scenario and keeps only known fields.
func Normalize(raw M) (M, error) {
	name := strings.TrimSpace(domain.Str(raw["name"]))
	if name == "" || len(name) > 200 {
		return nil, errors.New("name must be 1-200 characters")
	}
	mode := domain.Str(raw["mode"])
	if mode == "" {
		mode = "agent"
	}
	if !domain.Has(Modes, mode) {
		return nil, errors.New("mode must be agent or llm")
	}
	for _, k := range []string{"state", "config", "checks", "llm"} {
		if v, ok := raw[k]; ok && v != nil {
			if _, isObj := v.(map[string]any); !isObj {
				return nil, errors.New(k + " must be an object")
			}
		}
	}
	checks, err := tracelab.Checks(domain.Obj(raw["checks"]))
	if err != nil {
		return nil, err
	}
	for k, v := range checks {
		if v == nil || (isList(v) && len(domain.Arr(v)) == 0) {
			delete(checks, k)
		}
	}
	llm := domain.Obj(raw["llm"])
	if v, ok := llm["messages"]; ok && v != nil && !isList(v) {
		return nil, errors.New("llm.messages must be an array")
	}
	if len(domain.JSON(raw)) > 2<<20 {
		return nil, errors.New("scenario exceeds 2 MiB")
	}
	collection := strings.TrimSpace(domain.Str(raw["collection"]))
	if collection == "" {
		collection = "Unsorted"
	}
	out := M{"name": name, "collection": collection, "service": strings.TrimSpace(domain.Str(raw["service"])), "source_trace_id": raw["source_trace_id"], "mode": mode, "input": raw["input"], "state": domain.Obj(raw["state"]), "config": domain.Obj(raw["config"]), "checks": checks, "llm": llm, "steps": domain.Arr(raw["steps"]), "snapshots": domain.Int(raw["snapshots"]), "notes": domain.Str(raw["notes"])}
	return out, nil
}

// Command is the replay frame pushed to the agent's SDK.
func Command(runID, scenarioID string, s M) M {
	return M{"type": "replay", "replay_id": runID, "scenario_id": scenarioID, "source_trace_id": s["source_trace_id"], "name": s["name"], "input": s["input"], "state": domain.Obj(s["state"]), "config": domain.Obj(s["config"]), "checks": domain.Obj(s["checks"])}
}

// Request turns a scenario into a direct provider call: the system prompt and
// generation parameters come from config, the conversation from llm.messages.
func Request(s M) (provider.Request, error) {
	config := domain.Obj(s["config"])
	llm := domain.Obj(s["llm"])
	model := domain.Str(config["model"])
	if model == "" {
		return provider.Request{}, errors.New("config.model is required for an LLM step")
	}
	prov := domain.Str(config["provider"])
	if prov == "" {
		prov = "openrouter"
	}
	messages := []M{}
	if system := domain.Str(config["system_prompt"]); system != "" {
		messages = append(messages, M{"role": "system", "content": system})
	}
	messages = append(messages, domain.Maps(llm["messages"])...)
	if len(messages) == 0 || (len(messages) == 1 && messages[0]["role"] == "system") {
		if input := domain.Str(s["input"]); input != "" {
			messages = append(messages, M{"role": "user", "content": input})
		}
	}
	if len(messages) == 0 {
		return provider.Request{}, errors.New("add llm.messages or a text input")
	}
	params := domain.Copy(domain.Obj(config["params"]))
	if params["max_tokens"] == nil && params["max_completion_tokens"] != nil {
		params["max_tokens"] = params["max_completion_tokens"]
	}
	domain.Default(params, "max_tokens", 1024)
	for _, k := range []string{"tools", "tool_choice"} {
		if llm[k] != nil {
			params[k] = llm[k]
		}
	}
	return provider.Request{Provider: prov, Model: model, Messages: messages, Params: params, SampleKey: domain.Hash(messages)}, nil
}

// Assert evaluates scenario checks against a direct LLM result.
func Assert(checks M, result M) []M {
	out := []M{}
	output := domain.Str(result["text"])
	for _, term := range domain.Strings(checks["output_contains"]) {
		out = append(out, M{"name": "Output contains: " + term, "passed": strings.Contains(output, term), "evidence": M{"expected": term}})
	}
	if checks["max_latency_ms"] != nil {
		out = append(out, M{"name": "Latency", "passed": domain.Num(result["latency_ms"]) <= domain.Num(checks["max_latency_ms"]), "evidence": M{"duration_ms": result["latency_ms"], "limit": checks["max_latency_ms"]}})
	}
	if checks["max_cost_usd"] != nil {
		var passed any
		if result["cost_usd"] != nil {
			passed = domain.Num(result["cost_usd"]) <= domain.Num(checks["max_cost_usd"])
		}
		out = append(out, M{"name": "Cost budget", "passed": passed, "evidence": M{"known_usd": result["cost_usd"], "limit": checks["max_cost_usd"]}})
	}
	return out
}

func verdict(checks []M, failed bool) string {
	if failed {
		return "fail"
	}
	if len(checks) == 0 {
		return "no_checks"
	}
	all := true
	for _, c := range checks {
		if c["passed"] == false {
			return "fail"
		}
		if c["passed"] != true {
			all = false
		}
	}
	if all {
		return "pass"
	}
	return "insufficient_evidence"
}

// Execute runs a queued LLM-step scenario run (worker job workbench.llm).
func Execute(ctx context.Context, db *store.Store, reg *provider.Registry, runID string) (M, error) {
	run, err := db.Get(ctx, Runs, runID)
	if err != nil {
		return nil, err
	}
	if Terminal(domain.Str(run["status"])) {
		return M{"result_kind": Runs, "result_id": runID}, nil
	}
	req, err := Request(domain.Obj(run["request"]))
	finish := func(m M) error {
		m["finished_at"] = domain.Now()
		_, e := db.Update(ctx, Runs, runID, func(r M) error {
			for k, v := range m {
				r[k] = v
			}
			return nil
		})
		return e
	}
	if err != nil {
		return M{"result_kind": Runs, "result_id": runID}, finish(M{"status": "failed", "error": err.Error(), "verdict": "fail"})
	}
	_, _ = db.Update(ctx, Runs, runID, func(r M) error { r["status"] = "running"; r["started_at"] = domain.Now(); return nil })
	callCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	result, err := reg.Generate(callCtx, req)
	if err != nil {
		category := "provider_error"
		var pe *provider.Error
		if errors.As(err, &pe) {
			category = pe.Category
		}
		return M{"result_kind": Runs, "result_id": runID}, finish(M{"status": "failed", "error": "Provider call failed: " + category, "verdict": "fail", "result": M{"provider": req.Provider, "requested_model": req.Model}})
	}
	checks := Assert(domain.Obj(domain.Obj(run["request"])["checks"]), result)
	return M{"result_kind": Runs, "result_id": runID}, finish(M{"status": "completed", "result": result, "output": result["text"], "checks": checks, "verdict": verdict(checks, false)})
}

// Derive computes the public status of an agent replay from the SDK's
// handler status and the linked trace status.
func Derive(run M) string {
	handler := domain.Str(run["handler_status"])
	trace := domain.Str(run["trace_status"])
	switch {
	case handler == "rejected" || handler == "failed":
		return handler
	case domain.Has([]string{"completed", "failed", "cancelled"}, trace):
		return trace
	case handler == "completed" && run["trace_id"] == nil:
		return "completed"
	case run["trace_id"] != nil:
		return "running"
	case handler == "started":
		return "started"
	}
	return domain.Str(run["status"])
}

// Link connects an ingested trace event to its replay run.
func Link(ctx context.Context, db *store.Store, msg, ack M) error {
	d := domain.Obj(msg["data"])
	switch msg["event"] {
	case "run.start":
		id := domain.Str(domain.Obj(d["replay"])["replay_id"])
		if id == "" || domain.Bool(ack["duplicate"]) {
			return nil
		}
		_, err := db.Update(ctx, Runs, id, func(r M) error {
			r["trace_id"], r["trace_status"] = ack["trace_id"], "running"
			r["status"] = Derive(r)
			return nil
		})
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	case "run.end":
		rows, err := db.List(ctx, Runs, M{"trace_id": ack["trace_id"]}, 1)
		if err != nil || len(rows) == 0 {
			return err
		}
		status := domain.Str(d["status"])
		if status == "" {
			status = "completed"
		}
		_, err = db.Update(ctx, Runs, domain.Str(rows[0]["id"]), func(r M) error {
			r["trace_status"] = status
			r["status"] = Derive(r)
			if Terminal(domain.Str(r["status"])) {
				r["finished_at"] = domain.Now()
			}
			return nil
		})
		return err
	}
	return nil
}

// Status records a replay.status frame sent by the SDK.
func Status(ctx context.Context, db *store.Store, msg M) error {
	status := domain.Str(msg["status"])
	if !domain.Has([]string{"started", "completed", "failed", "rejected"}, status) {
		return errors.New("invalid replay status")
	}
	_, err := db.Update(ctx, Runs, domain.Str(msg["replay_id"]), func(r M) error {
		r["handler_status"] = status
		if e := domain.Str(msg["error"]); e != "" {
			if len(e) > 2000 {
				e = e[:2000]
			}
			r["error"] = e
		}
		r["status"] = Derive(r)
		if Terminal(domain.Str(r["status"])) && r["finished_at"] == nil {
			r["finished_at"] = domain.Now()
		}
		return nil
	})
	return err
}

// Enrich attaches the linked trace's output, verdict and cost to an agent replay.
func Enrich(ctx context.Context, db *store.Store, run M) M {
	rid := domain.Str(run["trace_id"])
	if rid == "" {
		return run
	}
	detail, events, err := tracelab.Snapshot(ctx, db, rid)
	if err != nil {
		return run
	}
	summary := tracelab.Summary(detail, events)
	outputs := []string{}
	for _, ev := range events {
		if ev["event"] == "output" {
			if s, _ := text(domain.Obj(ev["data"])["content"]); s != "" {
				outputs = append(outputs, s)
			}
		}
	}
	run = domain.Copy(run)
	run["output"] = strings.Join(outputs, "\n")
	run["checks"] = summary["checks"]
	run["verdict"] = summary["verdict"]
	cost := domain.Obj(summary["cost"])
	run["result"] = M{"latency_ms": summary["duration_ms"], "tokens_in": cost["tokens_in"], "tokens_out": cost["tokens_out"], "cost_usd": cost["known_usd"], "events": summary["events"], "tool_calls": summary["tool_calls"], "retries": summary["retries"]}
	return run
}
