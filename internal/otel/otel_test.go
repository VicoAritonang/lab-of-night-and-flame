package otel

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func kv(k string, v any) *commonpb.KeyValue {
	switch x := v.(type) {
	case string:
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: x}}}
	case int:
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(x)}}}
	case float64:
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: x}}}
	}
	panic("unsupported")
}

// AgentRequest is an OpenAI-Agents-style trace: an agent span with a chat
// span (which asks for a tool) and the tool execution.
func AgentRequest(t testing.TB) []byte {
	t.Helper()
	trace := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	id := func(b byte) []byte { return []byte{0, 0, 0, 0, 0, 0, 0, b} }
	at := func(ms int) uint64 { return uint64(1_700_000_000_000+ms) * 1_000_000 }
	rs := &tracepb.ResourceSpans{Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("service.name", "otel-agent")}}, ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
		{TraceId: trace, SpanId: id(1), Name: "invoke_agent Support", StartTimeUnixNano: at(0), EndTimeUnixNano: at(900), Attributes: []*commonpb.KeyValue{kv("gen_ai.operation.name", "invoke_agent"), kv("gen_ai.agent.name", "Support"), kv("gen_ai.conversation.id", "conv-7"), kv("input.value", "Where is my refund?"), kv("output.value", "Refund issued: 14 days")}},
		{TraceId: trace, SpanId: id(2), ParentSpanId: id(1), Name: "chat gpt-4.1-mini", StartTimeUnixNano: at(10), EndTimeUnixNano: at(400), Attributes: []*commonpb.KeyValue{kv("gen_ai.operation.name", "chat"), kv("gen_ai.provider.name", "openai"), kv("gen_ai.request.model", "gpt-4.1-mini"), kv("gen_ai.request.temperature", 0.2), kv("gen_ai.usage.input_tokens", 120), kv("gen_ai.usage.output_tokens", 30),
			kv("gen_ai.system_instructions", `[{"type":"text","content":"You are a support agent."}]`),
			kv("gen_ai.input.messages", `[{"role":"user","parts":[{"type":"text","content":"Where is my refund?"}]}]`),
			kv("gen_ai.output.messages", `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"lookup_order","arguments":{"id":7}}]}]`)}},
		{TraceId: trace, SpanId: id(3), ParentSpanId: id(1), Name: "execute_tool lookup_order", StartTimeUnixNano: at(410), EndTimeUnixNano: at(500), Attributes: []*commonpb.KeyValue{kv("gen_ai.operation.name", "execute_tool"), kv("gen_ai.tool.name", "lookup_order"), kv("gen_ai.tool.call.id", "call_1"), kv("gen_ai.tool.call.arguments", `{"id":7}`), kv("gen_ai.tool.call.result", `{"status":"refunded"}`)}},
	}}}}
	inner, err := proto.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	return protowire.AppendBytes(out, inner)
}

func TestDecodeAndConvertGenAISpans(t *testing.T) {
	spans, err := DecodeProto(AgentRequest(t))
	if err != nil || len(spans) != 3 {
		t.Fatal(spans, err)
	}
	traces := Group(spans)
	if len(traces) != 1 || traces[0].Root == nil || traces[0].Service != "otel-agent" {
		t.Fatal(traces)
	}
	if Classify(spans[0]) != KindAgent || Classify(spans[1]) != KindLLM || Classify(spans[2]) != KindTool {
		t.Fatal("classification")
	}
	agents := AgentNames(spans)
	events := []M{}
	for _, s := range traces[0].Spans {
		events = append(events, Events(s, agents)...)
	}
	kinds := []string{}
	for _, ev := range events {
		if err := tracelab.Validate(domain.Copy(ev)); err != nil {
			t.Fatal(ev["event"], err)
		}
		kinds = append(kinds, domain.Str(ev["event"]))
	}
	want := []string{"graph.visit", "span.start", "span.end", "model.request", "model.response", "cost.record", "tool.call", "tool.result"}
	if len(kinds) != len(want) {
		t.Fatal(kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatal(kinds)
		}
	}
	req := domain.Obj(domain.Obj(events[3]["data"])["request"])
	msgs := domain.Maps(req["messages"])
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["content"] != "Where is my refund?" || req["temperature"] != 0.2 {
		t.Fatal(req)
	}
	res := domain.Obj(events[4]["data"])
	if domain.Int(res["tokens_in"]) != 120 || res["tool_calls"] == nil {
		t.Fatal(res)
	}
	if ctx := Context(traces[0]); ctx["thread_id"] != "conv-7" {
		t.Fatal(ctx)
	}
	in, out := RootIO(*traces[0].Root)
	if in != "Where is my refund?" || out != "Refund issued: 14 days" {
		t.Fatal(in, out)
	}
}

func TestDecodeJSONAndLegacyAttributes(t *testing.T) {
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"py"}}]},"scopeSpans":[{"spans":[
	 {"traceId":"0102030405060708090a0b0c0d0e0f10","spanId":"0000000000000002","parentSpanId":"0000000000000001","name":"openai.chat","kind":3,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000500000000",
	  "attributes":[{"key":"gen_ai.request.model","value":{"stringValue":"gpt-4o"}},{"key":"gen_ai.usage.prompt_tokens","value":{"intValue":"11"}},{"key":"gen_ai.usage.completion_tokens","value":{"intValue":4}},
	   {"key":"gen_ai.prompt.0.role","value":{"stringValue":"user"}},{"key":"gen_ai.prompt.0.content","value":{"stringValue":"hi"}},{"key":"gen_ai.completion.0.content","value":{"stringValue":"hello"}}],
	  "status":{"code":2,"message":"boom"},"events":[{"name":"exception","timeUnixNano":"1700000000400000000","attributes":[{"key":"exception.type","value":{"stringValue":"RateLimit"}},{"key":"exception.message","value":{"stringValue":"slow down"}}]}]}]}]}]}`
	spans, err := DecodeJSON([]byte(body))
	if err != nil || len(spans) != 1 {
		t.Fatal(spans, err)
	}
	s := spans[0]
	if Classify(s) != KindLLM || !s.Error || s.Start != 1_700_000_000 {
		t.Fatal(s)
	}
	events := Events(s, nil)
	if len(events) != 4 || events[3]["event"] != "error" || domain.Obj(events[3]["data"])["reason"] != "RateLimit: slow down" {
		t.Fatal(events)
	}
	if text, _ := Completion(s); text != "hello" {
		t.Fatal(text)
	}
	if domain.Int(domain.Obj(events[1]["data"])["tokens_out"]) != 4 || domain.Obj(events[1]["data"])["status"] != "error" {
		t.Fatal(events[1])
	}
}

func TestPartialSuccessEncoding(t *testing.T) {
	if len(PartialSuccess(0, "")) != 0 {
		t.Fatal("full success must be an empty message")
	}
	b := PartialSuccess(2, "bad span")
	num, typ, n := protowire.ConsumeTag(b)
	if num != 1 || typ != protowire.BytesType || n < 0 {
		t.Fatal(b)
	}
}
