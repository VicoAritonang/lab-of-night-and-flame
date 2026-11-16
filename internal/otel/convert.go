package otel

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.vicoworks.com/agent-reliability-lab/internal/domain"
)

// Span categories, derived from gen_ai.operation.name or openinference.span.kind.
const (
	KindLLM       = "llm"
	KindTool      = "tool"
	KindAgent     = "agent"
	KindRetriever = "retriever"
	KindOther     = "span"
)

func first(attrs M, keys ...string) any {
	for _, k := range keys {
		if v, ok := attrs[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

func str(attrs M, keys ...string) string { return domain.Str(first(attrs, keys...)) }

// Classify names the role a span plays in an agent run.
func Classify(s Span) string {
	op := strings.ToLower(str(s.Attrs, "gen_ai.operation.name"))
	oi := strings.ToUpper(str(s.Attrs, "openinference.span.kind"))
	switch {
	case op == "execute_tool" || oi == "TOOL":
		return KindTool
	case op == "invoke_agent" || op == "create_agent" || op == "invoke_workflow" || oi == "AGENT":
		return KindAgent
	case op == "embeddings" || op == "retrieval" || oi == "RETRIEVER" || oi == "EMBEDDING" || oi == "RERANKER":
		return KindRetriever
	case op == "chat" || op == "text_completion" || op == "generate_content" || op == "completion" || oi == "LLM":
		return KindLLM
	case first(s.Attrs, "gen_ai.tool.name", "tool.name") != nil:
		return KindTool
	case first(s.Attrs, "gen_ai.request.model", "llm.model_name", "gen_ai.prompt.0.content") != nil:
		return KindLLM
	}
	return KindOther
}

// decodeJSONish parses attribute values that carry JSON documents as strings.
func decodeJSONish(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var out any
		if json.Unmarshal([]byte(t), &out) == nil {
			return out
		}
	}
	return s
}

// partsText flattens GenAI message parts ([{type:"text",content:"…"}]) or a
// plain content value to text.
func partsText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		texts := []string{}
		for _, p := range x {
			part := domain.Obj(p)
			if t := domain.Str(first(part, "content", "text")); t != "" && (part["type"] == nil || part["type"] == "text") {
				texts = append(texts, t)
			} else if s, ok := p.(string); ok {
				texts = append(texts, s)
			}
		}
		return strings.Join(texts, "\n")
	case map[string]any:
		return domain.Str(first(x, "content", "text"))
	}
	return ""
}

// message converts one GenAI semconv or OpenAI-style message to chat format.
func message(raw any) M {
	m := domain.Obj(raw)
	role := domain.Str(m["role"])
	if role == "" {
		role = "user"
	}
	out := M{"role": role}
	if parts, ok := m["parts"]; ok {
		out["content"] = partsText(parts)
		for _, p := range domain.Maps(parts) {
			if p["type"] == "tool_call" {
				calls, _ := out["tool_calls"].([]any)
				out["tool_calls"] = append(calls, M{"id": p["id"], "type": "function", "function": M{"name": p["name"], "arguments": domain.JSONB(p["arguments"])}})
			}
		}
	} else {
		out["content"] = partsText(m["content"])
		if m["tool_calls"] != nil {
			out["tool_calls"] = m["tool_calls"]
		}
	}
	if id := domain.Str(first(m, "tool_call_id", "id")); id != "" && role == "tool" {
		out["tool_call_id"] = id
	}
	return out
}

// indexed reads attributes like prefix.0.role / prefix.0.content.
func indexed(attrs M, prefix, roleKey, contentKey string) []M {
	out := []M{}
	for i := 0; i < 500; i++ {
		base := fmt.Sprintf("%s.%d.", prefix, i)
		role, content := attrs[base+roleKey], attrs[base+contentKey]
		if role == nil && content == nil {
			break
		}
		r := domain.Str(role)
		if r == "" {
			r = "assistant"
		}
		out = append(out, M{"role": r, "content": partsText(decodeJSONish(content))})
	}
	return out
}

// Messages returns the chat messages sent to a model span.
func Messages(s Span) []M {
	out := []M{}
	if sys := first(s.Attrs, "gen_ai.system_instructions"); sys != nil {
		if text := partsText(decodeJSONish(sys)); text != "" {
			out = append(out, M{"role": "system", "content": text})
		}
	}
	if raw := first(s.Attrs, "gen_ai.input.messages"); raw != nil {
		for _, m := range domain.Arr(decodeJSONish(raw)) {
			out = append(out, message(m))
		}
		return out
	}
	if msgs := indexed(s.Attrs, "gen_ai.prompt", "role", "content"); len(msgs) > 0 {
		return append(out, msgs...)
	}
	if msgs := indexed(s.Attrs, "llm.input_messages", "message.role", "message.content"); len(msgs) > 0 {
		return append(out, msgs...)
	}
	// Deprecated event-based capture: gen_ai.system.message, gen_ai.user.message, …
	for _, e := range s.Events {
		if strings.HasPrefix(e.Name, "gen_ai.") && strings.HasSuffix(e.Name, ".message") {
			role := strings.TrimSuffix(strings.TrimPrefix(e.Name, "gen_ai."), ".message")
			out = append(out, M{"role": role, "content": partsText(decodeJSONish(first(e.Attrs, "content", "gen_ai.event.content")))})
		}
	}
	return out
}

// Completion returns the model's text output and tool calls.
func Completion(s Span) (string, any) {
	if raw := first(s.Attrs, "gen_ai.output.messages"); raw != nil {
		texts := []string{}
		var calls []any
		for _, m := range domain.Arr(decodeJSONish(raw)) {
			msg := message(m)
			if t := domain.Str(msg["content"]); t != "" {
				texts = append(texts, t)
			}
			if c, ok := msg["tool_calls"].([]any); ok {
				calls = append(calls, c...)
			}
		}
		if calls == nil {
			return strings.Join(texts, "\n"), nil
		}
		return strings.Join(texts, "\n"), calls
	}
	if msgs := indexed(s.Attrs, "gen_ai.completion", "role", "content"); len(msgs) > 0 {
		return domain.Str(msgs[0]["content"]), nil
	}
	if msgs := indexed(s.Attrs, "llm.output_messages", "message.role", "message.content"); len(msgs) > 0 {
		return domain.Str(msgs[0]["content"]), nil
	}
	for _, e := range s.Events {
		if e.Name == "gen_ai.choice" {
			return partsText(decodeJSONish(first(e.Attrs, "message", "content"))), nil
		}
	}
	return valueText(first(s.Attrs, "output.value")), nil
}

func valueText(v any) string {
	v = decodeJSONish(v)
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		// A list of chat messages: keep the last one's text.
		if len(x) > 0 {
			if t := partsText(domain.Obj(x[len(x)-1])["content"]); t != "" {
				return t
			}
			if t := partsText(domain.Obj(x[len(x)-1])["parts"]); t != "" {
				return t
			}
		}
	case map[string]any:
		for _, k := range []string{"output", "answer", "content", "text", "result", "input", "query", "question"} {
			if t, ok := x[k].(string); ok && t != "" {
				return t
			}
		}
	}
	return string(domain.JSON(v))
}

func intAttr(attrs M, keys ...string) any {
	v := first(attrs, keys...)
	if v == nil {
		return nil
	}
	n := domain.Num(v)
	if n < 0 {
		return nil
	}
	return int(n)
}

// event builds one arl/1 event envelope.
func event(s Span, suffix, kind, phase string, at float64, data M) M {
	ev := M{"type": "event", "event_id": "otel:" + s.TraceID + ":" + s.SpanID + ":" + suffix, "run_id": s.TraceID, "event": kind, "phase": phase, "data": data}
	if s.SpanID != "" {
		ev["span_id"] = s.SpanID
	}
	if s.ParentID != "" {
		ev["parent_span_id"] = s.ParentID
	}
	if at > 0 {
		ev["timestamp"] = at
	}
	return ev
}

// compact keeps span attributes readable in the trace view: message payloads
// already become model events and are dropped here.
func compact(attrs M) M {
	out := M{}
	for k, v := range attrs {
		if strings.HasPrefix(k, "gen_ai.input.messages") || strings.HasPrefix(k, "gen_ai.output.messages") || strings.HasPrefix(k, "gen_ai.prompt.") || strings.HasPrefix(k, "gen_ai.completion.") || strings.HasPrefix(k, "llm.input_messages.") || strings.HasPrefix(k, "llm.output_messages.") {
			continue
		}
		out[k] = v
	}
	return out
}

func durationMS(s Span) any {
	if s.End <= 0 || s.End < s.Start {
		return nil
	}
	return (s.End - s.Start) * 1000
}

func status(s Span) string {
	if s.Error {
		return "error"
	}
	return "ok"
}

var generation = map[string]string{"gen_ai.request.temperature": "temperature", "gen_ai.request.top_p": "top_p", "gen_ai.request.max_tokens": "max_tokens", "gen_ai.request.seed": "seed", "llm.invocation_parameters": ""}

// Events maps one span to arl/1 events (without run.start/run.end).
func Events(s Span, agents map[string]string) []M {
	kind := Classify(s)
	out := []M{}
	switch kind {
	case KindLLM:
		model := str(s.Attrs, "gen_ai.request.model", "gen_ai.response.model", "llm.model_name")
		messages := Messages(s)
		req := M{"model": model, "messages": messages}
		for attr, param := range generation {
			if v := s.Attrs[attr]; v != nil && param != "" {
				req[param] = v
			}
		}
		if params := domain.Obj(decodeJSONish(s.Attrs["llm.invocation_parameters"])); len(params) > 0 {
			for _, k := range []string{"temperature", "top_p", "max_tokens", "seed"} {
				domain.Default(req, k, params[k])
				if req[k] == nil {
					delete(req, k)
				}
			}
		}
		if tools := decodeJSONish(first(s.Attrs, "gen_ai.tool.definitions")); tools != nil {
			req["tools"] = tools
		}
		data := M{"model": model, "operation": str(s.Attrs, "gen_ai.operation.name"), "provider": str(s.Attrs, "gen_ai.provider.name", "gen_ai.system", "llm.provider", "llm.system")}
		if len(messages) > 0 {
			data["request"] = req
			if text := domain.Str(messages[len(messages)-1]["content"]); text != "" {
				data["content"] = []any{M{"type": "text", "text": text}}
			}
		}
		out = append(out, event(s, "req", "model.request", "agent", s.Start, data))
		text, calls := Completion(s)
		tokensIn := intAttr(s.Attrs, "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.token_count.prompt")
		tokensOut := intAttr(s.Attrs, "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.token_count.completion")
		res := M{"model": str(s.Attrs, "gen_ai.response.model", "gen_ai.request.model", "llm.model_name"), "content": []any{M{"type": "text", "text": text}}, "status": status(s)}
		if d := durationMS(s); d != nil {
			res["duration_ms"] = d
		}
		if calls != nil {
			res["tool_calls"] = calls
		}
		if tokensIn != nil && tokensOut != nil {
			res["tokens_in"], res["tokens_out"] = tokensIn, tokensOut
		}
		if r := first(s.Attrs, "gen_ai.response.finish_reasons"); r != nil {
			res["finish_reasons"] = r
		}
		if s.Error {
			res["error"] = s.StatusMessage
		}
		out = append(out, event(s, "res", "model.response", "agent", s.End, res))
		cost := M{"billing_id": s.SpanID, "model": model}
		if tokensIn != nil && tokensOut != nil {
			cost["tokens_in"], cost["tokens_out"] = tokensIn, tokensOut
		}
		if amount := first(s.Attrs, "gen_ai.usage.cost", "llm.cost.total", "gen_ai.usage.cost_usd"); amount != nil && domain.Num(amount) >= 0 {
			cost["amount_usd"], cost["source"] = domain.Num(amount), "reported"
		}
		out = append(out, event(s, "cost", "cost.record", "agent", s.End, cost))
	case KindTool:
		name := str(s.Attrs, "gen_ai.tool.name", "tool.name")
		if name == "" {
			name = strings.TrimPrefix(s.Name, "execute_tool ")
		}
		if len(name) > 200 {
			name = name[:200]
		}
		call := str(s.Attrs, "gen_ai.tool.call.id", "tool_call.id")
		if call == "" || len(call) > 200 {
			call = s.SpanID
		}
		out = append(out, event(s, "call", "tool.call", "agent", s.Start, M{"tool": name, "call_id": call, "arguments": decodeJSONish(first(s.Attrs, "gen_ai.tool.call.arguments", "tool.parameters", "input.value"))}))
		result := M{"call_id": call, "status": status(s), "result": decodeJSONish(first(s.Attrs, "gen_ai.tool.call.result", "output.value"))}
		if d := durationMS(s); d != nil {
			result["duration_ms"] = d
		}
		if s.Error {
			result["error"] = s.StatusMessage
		}
		out = append(out, event(s, "result", "tool.result", "agent", s.End, result))
	default:
		phase := "agent"
		if kind == KindRetriever {
			phase = "retrieval"
		}
		start := M{"name": s.Name, "kind": kind, "attributes": compact(s.Attrs)}
		if q := valueText(first(s.Attrs, "input.value", "gen_ai.retrieval.query.text")); q != "" && kind == KindRetriever {
			start["query"] = q
		}
		if kind == KindAgent {
			node := agents[s.SpanID]
			visit := M{"node_id": node, "label": node}
			if parent := agents[s.ParentID]; parent != "" {
				visit["from_node"] = parent
			}
			out = append(out, event(s, "visit", "graph.visit", phase, s.Start, visit))
		}
		out = append(out, event(s, "start", "span.start", phase, s.Start, start))
		end := M{"status": status(s)}
		if d := durationMS(s); d != nil {
			end["duration_ms"] = d
		}
		if s.Error && s.StatusMessage != "" {
			end["error"] = s.StatusMessage
		}
		out = append(out, event(s, "end", "span.end", phase, s.End, end))
	}
	for i, e := range s.Events {
		if e.Name == "exception" {
			reason := strings.TrimSpace(str(e.Attrs, "exception.type") + ": " + str(e.Attrs, "exception.message"))
			out = append(out, event(s, fmt.Sprintf("exc%d", i), "error", "agent", e.Time, M{"reason": strings.TrimPrefix(reason, ": "), "stacktrace": e.Attrs["exception.stacktrace"]}))
		}
	}
	return out
}

// AgentNames labels agent spans for the graph view; nested agents with the
// same name get distinct nodes only when their names differ.
func AgentNames(spans []Span) map[string]string {
	out := map[string]string{}
	for _, s := range spans {
		if Classify(s) != KindAgent {
			continue
		}
		name := str(s.Attrs, "gen_ai.agent.name", "agent.name")
		if name == "" {
			name = strings.TrimPrefix(strings.TrimPrefix(s.Name, "invoke_agent "), "create_agent ")
		}
		if len(name) > 200 {
			name = name[:200]
		}
		out[s.SpanID] = name
	}
	return out
}

// Trace collects the spans of one trace from a batch.
type Trace struct {
	ID, Service string
	Spans       []Span
	Root        *Span
}

// Group splits a batch by service and trace and orders spans by start time.
func Group(spans []Span) []*Trace {
	index := map[string]*Trace{}
	order := []*Trace{}
	for _, s := range spans {
		if s.TraceID == "" || s.SpanID == "" {
			continue
		}
		service := s.Service
		if service == "" {
			service = "unknown_service"
		}
		key := service + "\x00" + s.TraceID
		t := index[key]
		if t == nil {
			t = &Trace{ID: s.TraceID, Service: service}
			index[key] = t
			order = append(order, t)
		}
		t.Spans = append(t.Spans, s)
	}
	for _, t := range order {
		sort.SliceStable(t.Spans, func(i, j int) bool { return t.Spans[i].Start < t.Spans[j].Start })
		for i := range t.Spans {
			if t.Spans[i].ParentID == "" || strings.Trim(t.Spans[i].ParentID, "0") == "" {
				t.Spans[i].ParentID = ""
				t.Root = &t.Spans[i]
			}
		}
	}
	return order
}

// Context reads run-level metadata from any span of the batch.
func Context(t *Trace) M {
	out := M{}
	for _, s := range t.Spans {
		if v := str(s.Attrs, "gen_ai.conversation.id", "session.id"); v != "" && out["thread_id"] == nil && len(v) <= 200 {
			out["thread_id"] = v
		}
		if v := str(s.Attrs, "user.id", "enduser.id"); v != "" && out["user_id"] == nil && len(v) <= 200 {
			out["user_id"] = v
		}
	}
	return out
}

// RootIO extracts the run's input and final output from the root span.
func RootIO(root Span) (string, string) {
	input := valueText(first(root.Attrs, "input.value", "traceloop.entity.input", "gen_ai.prompt"))
	if input == "" {
		if msgs := Messages(root); len(msgs) > 0 {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i]["role"] == "user" {
					input = domain.Str(msgs[i]["content"])
					break
				}
			}
		}
	}
	output := valueText(first(root.Attrs, "output.value", "traceloop.entity.output", "gen_ai.completion"))
	if output == "" && first(root.Attrs, "gen_ai.output.messages") != nil {
		output, _ = Completion(root)
	}
	return input, output
}
