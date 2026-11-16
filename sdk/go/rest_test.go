package arl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPromptRegistryAndFeedback(t *testing.T) {
	var mu sync.Mutex
	var bodies []Object
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/prompts/support":
			gets++
			version := 3
			if r.URL.Query().Get("label") == "staging" {
				version = 4
			}
			_ = json.NewEncoder(w).Encode(Object{"prompt": "support", "version": version, "config": Object{"model": "m-1"}, "template": Object{"system": "You help {{company}}.", "messages": []Object{{"role": "user", "content": "{{ q }} ({{meta.lang}})"}}}})
		case r.Method == "POST" && r.URL.Path == "/api/feedback":
			var b Object
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			_ = json.NewEncoder(w).Encode(Object{"id": "fb_1"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c := New("svc", Options{URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent-lab/ws", Stage: "dev", Token: "tok"})
	p, err := c.Prompt(ctx, "support", PromptOptions{})
	if err != nil || p.Version != 3 || p.Config["model"] != "m-1" {
		t.Fatal(p, err)
	}
	msgs := p.Render(Object{"company": "Acme", "q": "Hi", "meta": Object{"lang": "id"}})
	if len(msgs) != 2 || msgs[0].Content != "You help Acme." || msgs[1].Content != "Hi (id)" {
		t.Fatal(msgs)
	}
	if p.Render(nil)[1].Content != "{{ q }} ({{meta.lang}})" {
		t.Fatal("missing variables must stay as written")
	}
	if _, err = c.Prompt(ctx, "support", PromptOptions{}); err != nil || gets != 1 {
		t.Fatal("prompt not cached", gets, err)
	}
	if staging, _ := c.Prompt(ctx, "support", PromptOptions{Label: "staging"}); staging.Version != 4 {
		t.Fatal(staging)
	}
	fb, err := c.Prompt(ctx, "other", PromptOptions{Fallback: &Prompt{System: "Be kind."}})
	if err != nil || fb.Version != 0 || fb.Render(nil)[0].Content != "Be kind." {
		t.Fatal(fb, err)
	}
	if _, err = c.Prompt(ctx, "other", PromptOptions{}); err == nil {
		t.Fatal("missing prompt without fallback must fail")
	}
	one := 1.
	if out, err := c.Feedback(ctx, Feedback{RunID: "r-9", Name: "thumbs", Value: &one, Comment: "great"}); err != nil || out["id"] != "fb_1" {
		t.Fatal(out, err)
	}
	if bodies[0]["run_id"] != "r-9" || bodies[0]["service"] != "svc" || bodies[0]["source"] != "user" || bodies[0]["value"] != 1. {
		t.Fatal(bodies[0])
	}
	off := New("svc", Options{Stage: "prod"})
	if out, err := off.Feedback(ctx, Feedback{RunID: "r", Name: "x"}); out != nil || err != nil {
		t.Fatal("disabled client must be a no-op")
	}
	if env := c.hello()["metadata"].(Object)["environment"].(Object); env["os"] == "" || env["cpus"] == nil {
		t.Fatal(env)
	}
}
