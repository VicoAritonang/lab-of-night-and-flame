package arl

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLostAckReconnectUsesIdenticalEvent(t *testing.T) {
	var mu sync.Mutex
	stored := map[string]string{}
	var connections atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := context.Background()
		for {
			_, body, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var m Object
			_ = json.Unmarshal(body, &m)
			reply := Object{"type": "hello_ack", "session_id": "session-1", "resume_token": "private-resume"}
			if m["type"] == "event" {
				key := m["event_id"].(string)
				mu.Lock()
				old, exists := stored[key]
				if !exists {
					stored[key] = string(body)
				}
				mu.Unlock()
				if !exists {
					return
				}
				if old != string(body) {
					t.Error("reconnect changed event payload")
				}
				reply = Object{"type": "ack", "event_id": key, "trace_id": "trace-1", "ordinal": 1, "duplicate": true}
			}
			encoded, _ := json.Marshal(reply)
			if ws.Write(ctx, websocket.MessageText, encoded) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	c := New("agent", Options{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Stage: "dev", Required: true})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ack, err := c.Emit(context.Background(), "job", "run.start", "input", Object{"name": "test"})
	if err != nil || !ack.Duplicate || ack.TraceID != "trace-1" {
		t.Fatal(ack, err)
	}
	if connections.Load() != 2 || len(stored) != 1 {
		t.Fatal("duplicate persisted event", connections.Load(), len(stored))
	}
}
func TestConcurrentEmitIsSerialized(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, body, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var m Object
			_ = json.Unmarshal(body, &m)
			reply := Object{"type": "hello_ack", "session_id": "s", "resume_token": "r"}
			if m["type"] == "event" {
				calls.Add(1)
				reply = Object{"type": "ack", "event_id": m["event_id"], "trace_id": "t"}
			}
			b, _ := json.Marshal(reply)
			if ws.Write(context.Background(), websocket.MessageText, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	c := New("agent", Options{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Stage: "dev", Required: true})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Emit(context.Background(), "j", "custom.event", "agent", Object{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 16 {
		t.Fatal(calls.Load())
	}
}
func TestProductionNoopAndDevFailOpen(t *testing.T) {
	c := New("agent", Options{URL: "ws://127.0.0.1:1/ws", Stage: "production", Required: true})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Emit(context.Background(), "j", "run.start", "input", nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c = New("agent", Options{URL: "ws://127.0.0.1:1/ws", Stage: "dev"})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal("fail-open blocked job", err)
	}
	c.Close()
}
func TestReplayCommandRunsHandlerAndLinksRun(t *testing.T) {
	statuses := make(chan string, 8)
	starts := make(chan Object, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := context.Background()
		for {
			_, body, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var m Object
			_ = json.Unmarshal(body, &m)
			var reply Object
			switch m["type"] {
			case "hello":
				caps, _ := json.Marshal(m["capabilities"])
				if !strings.Contains(string(caps), "replay") {
					t.Error("replay capability missing from handshake")
				}
				reply = Object{"type": "hello_ack", "session_id": "s", "resume_token": "r"}
				b, _ := json.Marshal(reply)
				_ = ws.Write(ctx, websocket.MessageText, b)
				push, _ := json.Marshal(Object{"type": "replay", "replay_id": "rpl-1", "source_trace_id": "trace-0", "input": "hi", "config": Object{"model": "m-2", "system_prompt": "be brief"}, "checks": Object{"max_retries": 0}})
				_ = ws.Write(ctx, websocket.MessageText, push)
				continue
			case "replay.status":
				statuses <- m["status"].(string)
				reply = Object{"type": "ack", "replay_id": m["replay_id"]}
			case "event":
				if m["event"] == "run.start" {
					starts <- m["data"].(map[string]any)
				}
				reply = Object{"type": "ack", "event_id": m["event_id"], "trace_id": "trace-1"}
			default:
				reply = Object{"type": "pong"}
			}
			b, _ := json.Marshal(reply)
			if ws.Write(ctx, websocket.MessageText, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	c := New("agent", Options{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Stage: "dev", Required: true})
	got := make(chan *Replay, 1)
	c.OnReplay(func(ctx context.Context, r *Replay) error {
		run := c.StartRun(ctx, "replayed")
		run.Input(Text(r.InputText("")))
		run.Output(Text("ok"))
		run.End("completed")
		got <- r
		return run.Err
	})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := <-got
	if r.Model("default") != "m-2" || r.SystemPrompt("") != "be brief" || r.InputText("") != "hi" {
		t.Fatal("replay overrides not decoded", r)
	}
	start := <-starts
	link, _ := start["replay"].(map[string]any)
	if link["replay_id"] != "rpl-1" || link["source_trace_id"] != "trace-0" {
		t.Fatal("run not linked to replay", start)
	}
	if checks, _ := start["checks"].(map[string]any); checks["max_retries"] == nil {
		t.Fatal("replay checks not applied", start)
	}
	if a, b := <-statuses, <-statuses; a != "started" || b != "completed" {
		t.Fatal("unexpected replay statuses", a, b)
	}
}
