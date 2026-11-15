package workbench

import "testing"

func trace() (M, []M) {
	detail := M{"id": "trace_1", "name": "Support", "service": "svc", "config": M{"capabilities": []any{"replay"}}, "check_config": M{"max_retries": 2., "output_contains": []any{}}}
	request := M{"model": "openai/gpt-x", "temperature": 0.3, "messages": []any{M{"role": "system", "content": "Be brief."}, M{"role": "user", "content": "Hi"}}}
	events := []M{
		{"event": "input", "data": M{"content": []any{M{"type": "text", "text": "Hi"}}}},
		{"event": "state.snapshot", "data": M{"state": M{"memory": "x"}, "config": M{"model": "openai/gpt-x"}}},
		{"event": "model.request", "span_id": "m1", "data": M{"model": "openai/gpt-x", "provider": "openai", "request": request}},
		{"event": "model.response", "span_id": "m1", "data": M{"content": []any{M{"type": "text", "text": "Hello"}}, "tokens_in": 5., "tokens_out": 1.}},
	}
	return detail, events
}

func TestFromTraceExtractsReplayableScenario(t *testing.T) {
	detail, events := trace()
	s := FromTrace(detail, events, "")
	config := s["config"].(M)
	if s["input"] != "Hi" || s["mode"] != "agent" || s["state"].(M)["memory"] != "x" {
		t.Fatal(s)
	}
	if config["system_prompt"] != "Be brief." || config["provider"] != "openai" || config["params"].(M)["temperature"] != 0.3 {
		t.Fatal(config)
	}
	if msgs := s["llm"].(M)["messages"].([]M); len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatal(msgs)
	}
	if checks := s["checks"].(M); checks["max_retries"] != 2. || checks["output_contains"] != nil {
		t.Fatal(checks)
	}
	if steps := s["steps"].([]M); len(steps) != 1 || steps[0]["response"] != "Hello" {
		t.Fatal(steps)
	}
}

func TestRequestAppliesPromptAndModelOverrides(t *testing.T) {
	detail, events := trace()
	s := FromTrace(detail, events, "m1")
	s["config"].(M)["model"] = "fake/echo"
	s["config"].(M)["provider"] = "fake"
	s["config"].(M)["system_prompt"] = "Be friendly."
	req, err := Request(s)
	if err != nil {
		t.Fatal(err)
	}
	if req.Provider != "fake" || req.Model != "fake/echo" || req.Messages[0]["content"] != "Be friendly." || len(req.Messages) != 2 || req.Params["max_tokens"] != 1024 {
		t.Fatal(req)
	}
	if _, err := Request(M{"config": M{}}); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestDeriveNeverHidesFailures(t *testing.T) {
	cases := []struct {
		run  M
		want string
	}{
		{M{"status": "dispatched"}, "dispatched"},
		{M{"handler_status": "started"}, "started"},
		{M{"handler_status": "started", "trace_id": "t"}, "running"},
		{M{"handler_status": "completed", "trace_id": "t", "trace_status": "failed"}, "failed"},
		{M{"handler_status": "failed", "trace_id": "t", "trace_status": "completed"}, "failed"},
		{M{"handler_status": "completed"}, "completed"},
	}
	for _, c := range cases {
		if got := Derive(c.run); got != c.want {
			t.Fatal(c.run, got)
		}
	}
}

func TestNormalizeRejectsInvalidScenario(t *testing.T) {
	if _, err := Normalize(M{"name": ""}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := Normalize(M{"name": "x", "mode": "batch"}); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := Normalize(M{"name": "x", "checks": M{"bogus": 1}}); err == nil {
		t.Fatal("bad check accepted")
	}
	s, err := Normalize(M{"name": "x", "checks": M{"max_retries": 1.}})
	if err != nil || s["collection"] != "Unsorted" || len(s["checks"].(M)) != 1 {
		t.Fatal(s, err)
	}
}
