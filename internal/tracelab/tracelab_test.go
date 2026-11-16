package tracelab

import (
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"strings"
	"testing"
)

func ev(kind string, data M) M {
	return M{"type": "event", "event_id": "id", "run_id": "run", "event": kind, "phase": "agent", "data": data}
}
func TestValidationRejectsMalformedEvents(t *testing.T) {
	for _, m := range []M{ev("graph.visit", M{"node_id": 1}), ev("span.start", M{}), ev("cost.record", M{"billing_id": "b", "amount_usd": -1}), ev("evaluation", M{"name": "n", "passed": "yes"}), ev("input", M{"content": []any{M{"type": "image", "artifact": "not-a-digest"}}}), ev("tool.call", M{"tool": "x"})} {
		if Validate(m) == nil {
			t.Fatal("malformed event accepted", m)
		}
	}
}
func TestUnknownOperationCostCannotPassBudget(t *testing.T) {
	run := M{"status": "completed", "started_at": 1., "finished_at": 2., "check_config": M{"max_cost_usd": 1.}}
	request := ev("model.request", M{"model": "m"})
	request["span_id"] = "model1"
	events := []M{request, ev("cost.record", M{"billing_id": "unrelated", "amount_usd": 0.})}
	summary := Summary(run, events)
	if summary["verdict"] != "insufficient_evidence" || domain.Int(domain.Obj(summary["cost"])["unknown_entries"]) != 1 {
		t.Fatal(summary)
	}
}
func TestSummaryRetryAndExplicitChecks(t *testing.T) {
	run := M{"status": "completed", "started_at": 1., "finished_at": 2., "check_config": M{"max_retries": 0, "required_nodes": []string{"n"}}}
	s := Summary(run, []M{ev("graph.visit", M{"node_id": "n"}), ev("retrieval.retry", M{"attempt": 2})})
	if s["verdict"] != "fail" || domain.Int(s["visits"]) != 1 {
		t.Fatal(s)
	}
}
func TestRedactionAndFiniteJSON(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "CANARY_SECRET_123")
	raw := M{"authorization": "Bearer raw", "nested": M{"password": "private"}, "text": "CANARY_SECRET_123"}
	redacted := string(domain.JSON(Redact(raw)))
	if strings.Contains(redacted, "private") || strings.Contains(redacted, "CANARY_SECRET") {
		t.Fatal("secret leaked")
	}
	if _, err := AssertJSON([]byte("{\"data\":{\"x\":NaN}}")); err == nil {
		t.Fatal("NaN accepted")
	}
}
func TestFrozenCheckShapeAndPricing(t *testing.T) {
	cfg, err := CheckConfig(M{"pricing": M{"m": M{"input_per_million": 1., "output_per_million": 2.}}, "checks": M{"max_cost_usd": 1.}})
	if err != nil {
		t.Fatal(err)
	}
	cost, err := price(M{"billing_id": "b", "model": "m", "tokens_in": 1000000., "tokens_out": 500000.}, cfg)
	if err != nil || cost["source"] != "estimated" || domain.Num(cost["amount_usd"]) != 2 {
		t.Fatal(cost, err)
	}
}
func TestTrajectoryAndOutputChecks(t *testing.T) {
	cfg, err := Checks(M{"required_tools": []any{"search"}, "tool_sequence": []any{"search", "answer"}, "max_tool_calls": 3., "output_regex": "^Refund", "output_not_contains": []any{"sorry"}, "output_json_schema": nil})
	if err != nil {
		t.Fatal(err)
	}
	run := M{"status": "completed", "started_at": 1., "finished_at": 2., "check_config": cfg}
	good := []M{ev("tool.call", M{"tool": "search", "call_id": "a"}), ev("tool.call", M{"tool": "answer", "call_id": "b"}), ev("output", M{"content": []any{M{"type": "text", "text": "Refund in 14 days"}}})}
	if s := Summary(run, good); s["verdict"] != "pass" || domain.Int(s["tool_calls"]) != 2 {
		t.Fatal(s)
	}
	wrongOrder := []M{good[1], good[0], good[2]}
	if s := Summary(run, wrongOrder); s["verdict"] != "fail" {
		t.Fatal("tool order violation passed", s)
	}
	running := M{"status": "running", "started_at": 1., "check_config": cfg}
	if s := Summary(running, good[:1]); s["verdict"] != "insufficient_evidence" {
		t.Fatal("running trace must not pass or fail on missing tools", s)
	}
	if _, err := Checks(M{"output_regex": "("}); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if _, err := Checks(M{"output_json_schema": M{"type": 5}}); err == nil {
		t.Fatal("invalid schema accepted")
	}
	schema := OutputChecks(M{"output_json_schema": M{"type": "object", "required": []any{"answer"}}}, `{"answer": 1}`, true)
	if len(schema) != 1 || schema[0]["passed"] != true {
		t.Fatal(schema)
	}
}
