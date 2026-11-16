package arl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// environment describes the runtime for comparing runs across machines (no
// host names).
func environment() Object {
	return Object{"runtime": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU()}
}

func (c *Client) rest(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*c.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.HTTPBase()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.Token)
	}
	if c.opts.Project != "" {
		req.Header.Set("X-Lab-Project", c.opts.Project)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 400 {
		return fmt.Errorf("lab HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Feedback is a score attached to a run. Identify the run by TraceID or by the
// RunID the agent used (RunOptions.ID). Value is a number (booleans: 1/0).
type Feedback struct {
	TraceID, RunID string
	Name           string
	Value          *float64
	Label, Comment string
	Source, Author string
	Metadata       Object
}

// Feedback records feedback, e.g. an end user's thumbs-up sent from your API
// after the agent answered. It is a no-op when telemetry is disabled and
// fail-open unless Options.Required.
func (c *Client) Feedback(ctx context.Context, f Feedback) (Object, error) {
	if !c.Enabled() {
		return nil, nil
	}
	if f.Source == "" {
		f.Source = "user"
	}
	body := Object{"name": f.Name, "value": f.Value, "source": f.Source, "metadata": f.Metadata}
	if f.Label != "" {
		body["label"] = f.Label
	}
	if f.Comment != "" {
		body["comment"] = f.Comment
	}
	if f.Author != "" {
		body["author"] = f.Author
	}
	if f.TraceID != "" {
		body["trace_id"] = f.TraceID
	} else {
		body["run_id"], body["service"] = f.RunID, c.service
	}
	var out Object
	if err := c.rest(ctx, "POST", "/api/feedback", body, &out); err != nil {
		if c.opts.Required {
			return nil, err
		}
		slog.Warn("lab feedback not recorded", "error", err.Error())
		return nil, nil
	}
	return out, nil
}

// Feedback attaches feedback to this run (source "sdk" unless set).
func (r *Run) Feedback(f Feedback) (Object, error) {
	if f.Source == "" {
		f.Source = "sdk"
	}
	f.TraceID, f.RunID = r.TraceID, r.ID
	return r.c.Feedback(r.ctx, f)
}

// Message is one chat message of a prompt template.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Prompt is a version from the lab's prompt registry (or a local fallback,
// with Version 0).
type Prompt struct {
	Name     string    `json:"prompt"`
	Version  int       `json:"version"`
	System   string    `json:"-"`
	Messages []Message `json:"-"`
	Config   Object    `json:"config"`
}

var variable = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}`)

func fill(text string, vars Object) string {
	return variable.ReplaceAllStringFunc(text, func(token string) string {
		var cur any = vars
		for _, part := range strings.Split(variable.FindStringSubmatch(token)[1], ".") {
			m, ok := cur.(Object)
			if !ok {
				return token
			}
			if cur, ok = m[part]; !ok {
				return token
			}
		}
		if s, ok := cur.(string); ok {
			return s
		}
		b, _ := json.Marshal(cur)
		return string(b)
	})
}

// Render returns chat messages with {{variables}} filled in; unknown
// variables stay as written.
func (p Prompt) Render(vars Object) []Message {
	out := []Message{}
	if p.System != "" {
		out = append(out, Message{"system", fill(p.System, vars)})
	}
	for _, m := range p.Messages {
		out = append(out, Message{m.Role, fill(m.Content, vars)})
	}
	return out
}

// RenderSystem fills the system prompt only.
func (p Prompt) RenderSystem(vars Object) string { return fill(p.System, vars) }

// Ref identifies the version; record it in Run.Snapshot config.
func (p Prompt) Ref() Object { return Object{"name": p.Name, "version": p.Version} }

// PromptOptions selects a version: Label ("production" by default when set,
// else the latest), or an explicit Version. Fallback is a system prompt used
// when the lab is disabled or unreachable.
type PromptOptions struct {
	Label    string
	Version  int
	Fallback *Prompt
	CacheFor time.Duration
}

type promptCache struct {
	mu    sync.Mutex
	items map[string]cached
}
type cached struct {
	at time.Time
	p  Prompt
}

var prompts = promptCache{items: map[string]cached{}}

// Prompt fetches a prompt version from the lab registry.
func (c *Client) Prompt(ctx context.Context, name string, opts PromptOptions) (Prompt, error) {
	if opts.CacheFor == 0 {
		opts.CacheFor = time.Minute
	}
	key := fmt.Sprintf("%p\x00%s\x00%s\x00%d", c, name, opts.Label, opts.Version)
	prompts.mu.Lock()
	hit, ok := prompts.items[key]
	prompts.mu.Unlock()
	if ok && time.Since(hit.at) < opts.CacheFor {
		return hit.p, nil
	}
	var err error = errors.New("lab telemetry disabled")
	if c.Enabled() {
		query := ""
		if opts.Version > 0 {
			query = fmt.Sprintf("?version=%d", opts.Version)
		} else if opts.Label != "" {
			query = "?label=" + url.QueryEscape(opts.Label)
		}
		var v struct {
			Prompt
			Template struct {
				System   string    `json:"system"`
				Messages []Message `json:"messages"`
			} `json:"template"`
		}
		if err = c.rest(ctx, "GET", "/api/prompts/"+url.PathEscape(name)+query, nil, &v); err == nil {
			p := v.Prompt
			p.System, p.Messages = v.Template.System, v.Template.Messages
			prompts.mu.Lock()
			prompts.items[key] = cached{time.Now(), p}
			prompts.mu.Unlock()
			return p, nil
		}
		slog.Warn("lab prompt unavailable", "prompt", name, "error", err.Error())
	}
	if opts.Fallback != nil {
		p := *opts.Fallback
		p.Name, p.Version = name, 0
		return p, nil
	}
	return Prompt{}, fmt.Errorf("prompt %q unavailable and no fallback: %w", name, err)
}
