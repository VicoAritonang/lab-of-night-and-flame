package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	arl "go.vicoworks.com/agent-reliability-lab"
)

func (f *fixture) waitRun(id string) M {
	f.t.Helper()
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		run := f.request("GET", "/api/workbench/runs/"+id, nil)
		switch run["status"] {
		case "completed", "failed", "rejected", "cancelled":
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatal("workbench run timed out", id)
	return nil
}

// An SDK agent is captured, saved as a scenario, then replayed through the
// Workbench with a different model and prompt; the LLM step is re-sent directly.
func TestWorkbenchCaptureReplayAndLLMStep(t *testing.T) {
	f := setup(t)
	f.Server.Hub.SetAlwaysOn(true)
	ctx := context.Background()
	lab := arl.New("wb-agent", arl.Options{URL: "ws" + strings.TrimPrefix(f.HTTP.URL, "http") + "/api/agent-lab/ws", Stage: "dev", Required: true})
	agent := func(ctx context.Context, question, model, system string) string {
		run := lab.StartRun(ctx, "Support", arl.RunOptions{Checks: arl.Object{"output_contains": []string{"refund"}}})
		run.Input(arl.Text(question))
		run.Snapshot(arl.Object{"topic": "refund"}, arl.Object{"model": model, "system_prompt": system})
		req := arl.Object{"model": model, "messages": []arl.Object{{"role": "system", "content": system}, {"role": "user", "content": question}}}
		answer := "[" + model + "] " + system + " refund in 14 days"
		run.ModelCall(arl.ModelCall{Request: req, Response: answer, TokensIn: arl.Int(10), TokensOut: arl.Int(5), CostUSD: arl.Float(0)})
		run.Output(arl.Text(answer))
		run.End("completed")
		if run.Err != nil {
			t.Error(run.Err)
		}
		return run.TraceID
	}
	lab.OnReplay(func(ctx context.Context, r *arl.Replay) error {
		agent(ctx, r.InputText(""), r.Model("m-1"), r.SystemPrompt(""))
		return nil
	})
	if err := lab.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	traceID := agent(ctx, "How do refunds work?", "m-1", "Be brief.")

	agents := f.request("GET", "/api/agent-lab/status", nil)
	if domain.Int(agents["active_connections"]) != 1 {
		t.Fatal(agents)
	}
	scenario := f.request("POST", "/api/workbench/from-trace", M{"trace_id": traceID, "save": true})
	id := domain.Str(scenario["id"])
	if id == "" || scenario["input"] != "How do refunds work?" || scenario["mode"] != "agent" {
		t.Fatal(scenario)
	}
	config := domain.Obj(scenario["config"])
	config["model"], config["system_prompt"] = "m-2", "Be friendly."
	saved := f.request("POST", "/api/workbench/scenarios/"+id, scenario)
	if domain.Obj(saved["config"])["model"] != "m-2" || len(domain.Arr(saved["steps"])) != 1 {
		t.Fatal(saved)
	}

	run := f.request("POST", "/api/workbench/run", M{"scenario_id": id, "mode": "agent"})
	done := f.waitRun(domain.Str(run["id"]))
	if done["status"] != "completed" || done["verdict"] != "pass" || !strings.Contains(domain.Str(done["output"]), "[m-2] Be friendly.") {
		t.Fatal(done)
	}
	if done["trace_id"] == nil || done["trace_id"] == traceID {
		t.Fatal("replay must produce its own trace", done)
	}

	config["provider"], config["model"] = "fake", "fake/echo"
	llm := f.request("POST", "/api/workbench/run", M{"scenario_id": id, "scenario": scenario, "mode": "llm"})
	result := f.waitRun(domain.Str(llm["id"]))
	if result["status"] != "completed" || result["output"] != "How do refunds work?" || result["verdict"] != "pass" {
		t.Fatal(result)
	}
	f.request("DELETE", "/api/workbench/scenarios/"+id, nil)
}
