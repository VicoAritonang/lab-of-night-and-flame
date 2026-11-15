// A small support agent instrumented with the Go SDK.
//
//	LAB_WS_URL=ws://localhost:8000/api/agent-lab/ws STAGE=dev go run ./examples/go-agent
//
// It answers one question on start, then serves POST /ask on :8081 and accepts
// Workbench replays. Without LLM_API_KEY it uses a deterministic mock model.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	arl "github.com/VicoAritonang/lab-of-night-and-flame/sdk/go"
)

var knowledge = map[string]string{
	"refund":   "Refunds are issued within 14 days to the original payment method.",
	"shipping": "Standard shipping takes 3-5 business days; express takes 1-2.",
	"warranty": "Hardware has a 2-year limited warranty covering manufacturing defects.",
}

type config struct {
	Model, SystemPrompt string
	Temperature         float64
}

func defaults() config {
	return config{Model: env("LLM_MODEL", "mock/support-v1"), SystemPrompt: "You are a concise support agent. Answer only from the context.", Temperature: 0.2}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func retrieve(question string) []string {
	found := []string{}
	for topic, fact := range knowledge {
		if strings.Contains(strings.ToLower(question), topic) {
			found = append(found, fact)
		}
	}
	return found
}

// complete calls an OpenAI-compatible endpoint, or the mock model.
func complete(ctx context.Context, req map[string]any) (string, int, int, error) {
	key := os.Getenv("LLM_API_KEY")
	if key == "" {
		msgs := req["messages"].([]map[string]any)
		system, user := msgs[0]["content"].(string), msgs[len(msgs)-1]["content"].(string)
		answer := fmt.Sprintf("[%v] %s", req["model"], strings.TrimPrefix(strings.SplitN(user, "\n\nQuestion:", 2)[0], "Context:\n"))
		if strings.Contains(strings.ToLower(system), "friendly") {
			answer = "Happy to help! " + answer
		}
		return answer, len(system+user) / 4, len(answer) / 4, nil
	}
	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequestWithContext(ctx, "POST", env("LLM_BASE_URL", "https://openrouter.ai/api/v1")+"/chat/completions", bytes.NewReader(body))
	hreq.Header.Set("Authorization", "Bearer "+key)
	hreq.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return "", 0, 0, err
	}
	defer res.Body.Close()
	var out struct {
		Choices []struct{ Message struct{ Content string } }
		Usage   struct{ PromptTokens, CompletionTokens int } `json:"usage"`
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return "", 0, 0, fmt.Errorf("llm HTTP %d", res.StatusCode)
	}
	return out.Choices[0].Message.Content, out.Usage.PromptTokens, out.Usage.CompletionTokens, nil
}

func answer(ctx context.Context, lab *arl.Client, question string, cfg config) (string, error) {
	run := lab.StartRun(ctx, "Support question", arl.RunOptions{Checks: arl.Object{"max_latency_ms": 10000}})
	run.Input(arl.Text(question))
	// Everything a replay needs to reproduce this run.
	run.Snapshot(arl.Object{"knowledge_topics": []string{"refund", "shipping", "warranty"}}, arl.Object{"model": cfg.Model, "system_prompt": cfg.SystemPrompt, "params": arl.Object{"temperature": cfg.Temperature}})
	facts := retrieve(question)
	run.Emit("retrieval.result", "retrieval", arl.Object{"content": []arl.Object{arl.Text(strings.Join(facts, "\n"))}, "matches": len(facts)})
	req := map[string]any{"model": cfg.Model, "temperature": cfg.Temperature, "messages": []map[string]any{
		{"role": "system", "content": cfg.SystemPrompt},
		{"role": "user", "content": "Context:\n" + strings.Join(facts, "\n") + "\n\nQuestion: " + question},
	}}
	start := time.Now()
	text, tin, tout, err := complete(ctx, req)
	run.ModelCall(arl.ModelCall{Request: req, Response: text, TokensIn: arl.Int(tin), TokensOut: arl.Int(tout), Duration: time.Since(start), Error: err})
	if err == nil {
		run.Output(arl.Text(text))
	}
	run.Finish(err)
	return text, err
}

func main() {
	ctx := context.Background()
	lab := arl.New("example/go-support-agent", arl.Options{Capabilities: []string{"tools", "costs"}})
	lab.OnReplay(func(ctx context.Context, r *arl.Replay) error {
		cfg := defaults()
		cfg.Model, cfg.SystemPrompt = r.Model(cfg.Model), r.SystemPrompt(cfg.SystemPrompt)
		if t, ok := r.Param("temperature").(float64); ok {
			cfg.Temperature = t
		}
		_, err := answer(ctx, lab, r.InputText("What is your refund policy?"), cfg)
		return err
	})
	if err := lab.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer lab.Close()
	if text, err := answer(ctx, lab, "What is your refund policy?", defaults()); err == nil {
		log.Println("answer:", text)
	}
	http.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Question string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		text, err := answer(r.Context(), lab, body.Question, defaults())
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"answer": text})
	})
	log.Println("go support agent on :8081 (POST /ask); waiting for Workbench replays")
	log.Fatal(http.ListenAndServe(env("AGENT_ADDR", ":8081"), nil))
}
