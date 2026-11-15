package arl

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Replay is a Lab request to run the agent again from a saved scenario.
// Config carries the effective configuration after the Workbench overrides,
// for example {"model": "...", "system_prompt": "...", "params": {...}}.
type Replay struct {
	ID            string `json:"replay_id"`
	ScenarioID    string `json:"scenario_id"`
	SourceTraceID string `json:"source_trace_id"`
	Name          string `json:"name"`
	Input         any    `json:"input"`
	State         Object `json:"state"`
	Config        Object `json:"config"`
	Checks        Object `json:"checks"`
}

// ReplayHandler runs the agent for a replay. Runs started with the handler's
// context are linked to the replay automatically.
type ReplayHandler func(ctx context.Context, r *Replay) error

type replayKey struct{}

func WithReplay(ctx context.Context, r *Replay) context.Context {
	return context.WithValue(ctx, replayKey{}, r)
}

// ReplayFrom returns the replay that ctx belongs to, or nil.
func ReplayFrom(ctx context.Context) *Replay {
	r, _ := ctx.Value(replayKey{}).(*Replay)
	return r
}

// String returns a string value from Config, or def.
func (r *Replay) String(key, def string) string {
	if r == nil {
		return def
	}
	if v, ok := r.Config[key].(string); ok && v != "" {
		return v
	}
	return def
}

// Model returns the overridden model, or def.
func (r *Replay) Model(def string) string { return r.String("model", def) }

// SystemPrompt returns the overridden system prompt, or def.
func (r *Replay) SystemPrompt(def string) string { return r.String("system_prompt", def) }

// Param returns a generation parameter such as "temperature" from Config.params.
func (r *Replay) Param(key string) any {
	if r == nil {
		return nil
	}
	params, _ := r.Config["params"].(map[string]any)
	return params[key]
}

// InputText returns the replay input when it is a string.
func (r *Replay) InputText(def string) string {
	if r == nil {
		return def
	}
	if s, ok := r.Input.(string); ok && s != "" {
		return s
	}
	return def
}

// OnReplay registers the handler that lets the Lab Workbench re-run this agent.
// Register it before Connect so the handshake advertises the "replay" capability.
func (c *Client) OnReplay(h ReplayHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconnect := c.handler == nil && c.conn != nil
	c.handler = h
	if reconnect {
		// Re-handshake so the Lab learns about the new capability.
		c.drop()
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

func (c *Client) replayStatus(id, status, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.Timeout)
	defer cancel()
	msg := Object{"type": "replay.status", "replay_id": id, "status": status}
	if detail != "" {
		msg["error"] = detail
	}
	if _, err := c.send(ctx, msg); err != nil {
		slog.Warn("cannot report replay status to the lab", "replay_id", id)
	}
}

func (c *Client) dispatch(m Object) {
	body, _ := json.Marshal(m)
	r := &Replay{}
	if json.Unmarshal(body, r) != nil || r.ID == "" {
		return
	}
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	if h == nil {
		c.replayStatus(r.ID, "rejected", "no replay handler registered")
		return
	}
	c.replayStatus(r.ID, "started", "")
	ctx, cancel := context.WithTimeout(WithReplay(context.Background(), r), 15*time.Minute)
	defer cancel()
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("replay handler panic: %v", p)
			}
		}()
		return h(ctx, r)
	}()
	if err != nil {
		c.replayStatus(r.ID, "failed", err.Error())
		return
	}
	c.replayStatus(r.ID, "completed", "")
}
