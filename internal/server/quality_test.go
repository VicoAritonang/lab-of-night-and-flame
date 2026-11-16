package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	arl "go.vicoworks.com/agent-reliability-lab"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func (f *fixture) raw(method, path, contentType string, body []byte) (int, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.HTTP.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	res, err := f.HTTP.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

func (f *fixture) eventually(what string, check func() bool) {
	f.t.Helper()
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		if check() {
			return
		}
		time.Sleep(60 * time.Millisecond)
	}
	f.t.Fatal("timed out waiting for", what)
}

func (f *fixture) list(path string) []M {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.HTTP.URL+path, nil)
	res, err := f.HTTP.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out []M
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 || json.Unmarshal(data, &out) != nil {
		f.t.Fatalf("GET %s: %d %s", path, res.StatusCode, data)
	}
	return out
}

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
func integer(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func otlpBatch(t *testing.T, spans ...*tracepb.Span) []byte {
	rs := &tracepb.ResourceSpans{Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", "otel-support")}}, ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}}
	inner, err := proto.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	return protowire.AppendBytes(out, inner)
}

// OpenTelemetry spans arrive child-first over two batches; the lab builds one
// trace with model/tool events, rollups and a summary, then monitoring,
// feedback, annotation, online evaluation and alerting work on top of it.
func TestOTLPMonitoringFeedbackAnnotationEvaluatorsAlerts(t *testing.T) {
	f := setup(t)
	f.Server.Hub.SetAlwaysOn(true)
	ctx := context.Background()

	hook := make(chan M, 4)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m M
		_ = json.NewDecoder(r.Body).Decode(&m)
		hook <- m
	}))
	defer webhook.Close()

	evaluator := f.request("POST", "/api/evaluators", M{"name": "Tool policy", "type": "checks", "feedback_name": "policy", "config": M{"checks": M{"required_tools": []any{"lookup_order"}, "output_contains": []any{"refund"}}}, "filter": M{"service": "otel-support"}})
	if evaluator["revision"] != 1. || evaluator["enabled"] != true {
		t.Fatal(evaluator)
	}

	traceID := []byte{9, 9, 9, 9, 9, 9, 9, 9, 1, 2, 3, 4, 5, 6, 7, 8}
	id := func(b byte) []byte { return []byte{0, 0, 0, 0, 0, 0, 1, b} }
	now := uint64(time.Now().UnixNano())
	at := func(ms uint64) uint64 { return now + ms*1_000_000 }
	chat := &tracepb.Span{TraceId: traceID, SpanId: id(2), ParentSpanId: id(1), Name: "chat gpt-4.1-mini", StartTimeUnixNano: at(10), EndTimeUnixNano: at(300), Attributes: []*commonpb.KeyValue{str("gen_ai.operation.name", "chat"), str("gen_ai.request.model", "gpt-4.1-mini"), integer("gen_ai.usage.input_tokens", 100), integer("gen_ai.usage.output_tokens", 20), str("gen_ai.input.messages", `[{"role":"system","parts":[{"type":"text","content":"Be brief."}]},{"role":"user","parts":[{"type":"text","content":"Refund status?"}]}]`), str("gen_ai.output.messages", `[{"role":"assistant","parts":[{"type":"text","content":"Checking."}]}]`)}}
	tool := &tracepb.Span{TraceId: traceID, SpanId: id(3), ParentSpanId: id(1), Name: "execute_tool lookup_order", StartTimeUnixNano: at(310), EndTimeUnixNano: at(350), Attributes: []*commonpb.KeyValue{str("gen_ai.operation.name", "execute_tool"), str("gen_ai.tool.name", "lookup_order"), str("gen_ai.tool.call.arguments", `{"order":7}`)}}
	root := &tracepb.Span{TraceId: traceID, SpanId: id(1), Name: "invoke_agent Support", StartTimeUnixNano: at(0), EndTimeUnixNano: at(400), Attributes: []*commonpb.KeyValue{str("gen_ai.operation.name", "invoke_agent"), str("gen_ai.agent.name", "Support"), str("session.id", "thread-42"), str("input.value", "Refund status?"), str("output.value", "Your refund arrives in 3 days.")}}

	code, body := f.raw("POST", "/api/otel/v1/traces", "application/x-protobuf", otlpBatch(t, chat, tool))
	if code != 200 || len(body) != 0 {
		t.Fatal(code, string(body))
	}
	// Root last, through the root-path alias; a repeated batch is idempotent.
	for i := 0; i < 2; i++ {
		if code, body = f.raw("POST", "/v1/traces", "application/x-protobuf", otlpBatch(t, root)); code != 200 {
			t.Fatal(code, string(body))
		}
	}
	runs := f.list("/api/agent-lab/runs?source=otlp")
	if len(runs) != 1 {
		t.Fatal(runs)
	}
	run := runs[0]
	if run["name"] != "invoke_agent Support" || run["status"] != "completed" || run["thread_id"] != "thread-42" || domain.Int(run["tokens_in"]) != 100 || domain.Int(run["model_calls"]) != 1 || domain.Int(run["tool_calls"]) != 1 || domain.Num(run["duration_ms"]) < 399 || domain.Num(run["duration_ms"]) > 401 {
		t.Fatal(run)
	}
	tid := domain.Str(run["id"])
	detail := f.request("GET", "/api/agent-lab/runs/"+tid, nil)
	if domain.Int(domain.Obj(detail["summary"])["tool_calls"]) != 1 || detail["source"] != "otlp" {
		t.Fatal(detail)
	}
	// The captured chat span is re-sendable from the Workbench.
	scenario := f.request("POST", "/api/workbench/from-trace", M{"trace_id": tid})
	if domain.Obj(scenario["config"])["system_prompt"] != "Be brief." || scenario["mode"] != "llm" {
		t.Fatal(scenario)
	}

	// Online evaluator ran on the finished trace.
	var auto M
	f.eventually("online evaluator feedback", func() bool {
		for _, fb := range f.list("/api/agent-lab/runs/" + tid + "/feedback") {
			if fb["source"] == "auto" {
				auto = fb
				return true
			}
		}
		return false
	})
	if auto["name"] != "policy" || auto["value"] != 1. || auto["label"] != "pass" {
		t.Fatal(auto)
	}

	// SDK trace with thread/tags metadata, plus end-user feedback by run id.
	lab := arl.New("sdk-support", arl.Options{URL: "ws" + strings.TrimPrefix(f.HTTP.URL, "http") + "/api/agent-lab/ws", Stage: "dev", Required: true})
	if err := lab.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	sdkRun := lab.StartRun(ctx, "Answer", arl.RunOptions{ID: "app-run-1", Data: arl.Object{"thread_id": "thread-42", "user_id": "u-1", "tags": []string{"beta"}}})
	sdkRun.ModelCall(arl.ModelCall{Request: arl.Object{"model": "m-sdk", "messages": []arl.Object{{"role": "user", "content": "hi"}}}, Response: "hello", TokensIn: arl.Int(5), TokensOut: arl.Int(2), CostUSD: arl.Float(0.25)})
	sdkRun.Output(arl.Text("hello"))
	sdkRun.End("completed")
	if sdkRun.Err != nil {
		t.Fatal(sdkRun.Err)
	}
	fb := f.request("POST", "/api/feedback", M{"run_id": "app-run-1", "service": "sdk-support", "name": "thumbs", "value": true, "comment": "helpful"})
	if fb["trace_id"] != sdkRun.TraceID || fb["value"] != 1. {
		t.Fatal(fb)
	}
	thread := f.list("/api/agent-lab/runs?thread_id=thread-42")
	if len(thread) != 2 {
		t.Fatal(thread)
	}
	if tagged := f.list("/api/agent-lab/runs?tag=beta&model=m-sdk"); len(tagged) != 1 || domain.Num(tagged[0]["cost_usd"]) != 0.25 {
		t.Fatal(tagged)
	}

	metrics := f.request("GET", "/api/monitor/metrics?hours=1", nil)
	totals := domain.Obj(metrics["totals"])
	if domain.Int(totals["traces"]) != 2 || domain.Num(totals["cost_usd"]) != 0.25 || domain.Int(totals["threads"]) != 1 || len(domain.Arr(metrics["models"])) != 2 || len(domain.Arr(metrics["tools"])) != 1 {
		t.Fatal(metrics)
	}
	facets := f.request("GET", "/api/monitor/facets", nil)
	if !domain.Has(domain.Strings(facets["tags"]), "beta") {
		t.Fatal(facets)
	}

	// Two annotators review the OTLP trace; agreement compares them and the evaluator.
	queue := f.request("POST", "/api/annotation/queues", M{"name": "Support QA", "required": 2, "rubric": []M{{"name": "policy", "kind": "binary"}, {"name": "tone", "kind": "scale", "min": 1, "max": 5}}})
	qid := domain.Str(queue["id"])
	added := f.request("POST", "/api/annotation/queues/"+qid+"/items", M{"filter": M{"source": "otlp", "hours": 1}})
	if added["added"] != 1. {
		t.Fatal(added)
	}
	for _, author := range []string{"ana", "budi"} {
		next := domain.Obj(f.request("GET", "/api/annotation/queues/"+qid+"/next?author="+author, nil)["item"])
		if next["trace_id"] != tid {
			t.Fatal(next)
		}
		f.request("POST", "/api/annotation/items/"+domain.Str(next["id"])+"/submit", M{"author": author, "scores": M{"policy": true, "tone": 4}})
	}
	if next := f.request("GET", "/api/annotation/queues/"+qid+"/next?author=ana", nil); next["item"] != nil {
		t.Fatal("annotated item offered again", next)
	}
	queues := f.list("/api/annotation/queues")
	if domain.Int(domain.Obj(queues[0]["progress"])["done"]) != 1 {
		t.Fatal(queues)
	}
	agreement := f.request("GET", "/api/annotation/queues/"+qid+"/agreement", nil)
	policy := domain.Maps(agreement["criteria"])[0]
	if policy["annotators"] != 2. || domain.Num(domain.Obj(policy["judge"])["accuracy"]) != 1 || domain.Int(domain.Obj(policy["judge"])["paired_items"]) != 1 {
		t.Fatal(agreement)
	}

	// An alert on the error rate stays ok; a cost alert fires and calls the webhook.
	f.request("POST", "/api/alerts", M{"name": "Spend", "metric": "cost_usd", "op": ">", "threshold": 0.1, "window_minutes": 60, "webhook_url": webhook.URL})
	f.request("POST", "/api/alerts", M{"name": "Errors", "metric": "error_rate", "threshold": 0.5, "window_minutes": 60})
	check := f.request("POST", "/api/alerts/check", M{})
	if check["fired"] != 1. || check["rules"] != 2. {
		t.Fatal(check)
	}
	select {
	case m := <-hook:
		if !strings.Contains(domain.Str(m["text"]), "Spend firing") {
			t.Fatal(m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook not called")
	}
	events := f.list("/api/alerts/events")
	if len(events) != 1 || events[0]["delivered"] != true {
		t.Fatal(events)
	}
}
