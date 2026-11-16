package provider

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"go.vicoworks.com/agent-reliability-lab/internal/config"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type M = domain.M
type Spec struct {
	Name, Label, BaseURL, KeyEnv, Kind string
	RPM                                int
	RequiresKey                        bool
}
type Registry struct {
	DB     *store.Store
	Config config.Config
	Client *http.Client
}
type Request struct {
	Provider, Model, SampleKey string
	Messages                   []M
	Params                     M
	Stream                     bool
	OnDelta                    func(string)
	Reference                  any
	Attempt                    int
}
type Error struct {
	Category                  string
	Retryable, MaybeProcessed bool
	Status                    int
	RetryAfter                time.Duration
}

func (e *Error) Error() string { return "provider operation failed: " + e.Category }
func New(db *store.Store, c config.Config) *Registry {
	return &Registry{db, c, &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxIdleConns: 32, MaxIdleConnsPerHost: 8, MaxConnsPerHost: 16, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second}}}
}
func (r *Registry) Specs(ctx context.Context) []Spec {
	out := []Spec{{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "external", config.Integer("ARL_OPENROUTER_RPM", 120), true}, {"openai", "OpenAI", "https://api.openai.com/v1", "OPENAI_API_KEY", "external", config.Integer("ARL_OPENAI_RPM", 300), true}, {"groq", "Groq", "https://api.groq.com/openai/v1", "GROQ_API_KEY", "external", config.Integer("ARL_GROQ_RPM", 30), true}, {"local", "Local (OpenAI-compatible)", r.Config.LocalURL, "", "local", 600, false}, {"fake", "Fake (runtime tests)", "", "", "fake", 100000, false}, {"imported", "Imported outputs", "", "", "imported", 100000, false}}
	rows, _ := r.DB.List(ctx, "connections", nil, 100)
	for _, m := range rows {
		out = append(out, Spec{domain.Str(m["name"]), domain.Str(m["label"]), domain.Str(m["base_url"]), domain.Str(m["key_env"]), "custom", domain.Max(1, domain.Int(m["rpm"])), domain.Str(m["key_env"]) != ""})
	}
	return out
}
func (r *Registry) Spec(ctx context.Context, name string) (Spec, bool) {
	for _, s := range r.Specs(ctx) {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}
func (r *Registry) Status(ctx context.Context) []M {
	if r.Config.Mode != "sync" {
		return r.localStatus(ctx)
	}
	snapshot, _ := r.DB.Get(ctx, "settings", "worker_providers")
	availability := map[string]bool{}
	for _, item := range domain.Maps(snapshot["items"]) {
		availability[domain.Str(item["name"])] = domain.Bool(item["configured"])
	}
	out := r.localStatus(ctx)
	for _, item := range out {
		if domain.Bool(item["requires_key"]) {
			item["configured"] = availability[domain.Str(item["name"])]
		}
		item["worker_reported_at"] = snapshot["updated_at"]
	}
	return out
}
func (r *Registry) PublishStatus(ctx context.Context) error {
	return r.DB.Put(ctx, "settings", "worker_providers", M{"items": r.localStatus(ctx), "updated_at": domain.Now()})
}
func (r *Registry) localStatus(ctx context.Context) []M {
	out := []M{}
	for _, s := range r.Specs(ctx) {
		var base any = s.BaseURL
		if base == "" {
			base = nil
		}
		out = append(out, M{"name": s.Name, "label": s.Label, "kind": s.Kind, "base_url": base, "configured": !s.RequiresKey || os.Getenv(s.KeyEnv) != "", "requires_key": s.RequiresKey, "rpm": s.RPM, "reports_cost": s.Name == "openrouter" || s.Name == "fake"})
	}
	return out
}
func (r *Registry) Models(ctx context.Context) []M {
	out := FakeCatalog()
	rows, _ := r.DB.List(ctx, "catalogs", nil, 100)
	for _, row := range rows {
		out = append(out, domain.Maps(row["items"])...)
	}
	return out
}
func FakeCatalog() []M {
	out := []M{}
	for _, id := range []string{"fake/oracle-90", "fake/oracle-70", "fake/oracle-50", "fake/flaky-30", "fake/slow", "fake/router", "fake/echo"} {
		out = append(out, M{"provider": "fake", "id": id, "name": id, "description": "Deterministic simulated target; does not measure model intelligence", "modalities": []string{"text", "image"}, "context_length": 32768, "price_in": 0, "price_out": 0, "price_source": "fake", "structured_output": true, "seed": true, "free": true, "fake": true})
	}
	return out
}
func (r *Registry) Price(ctx context.Context, prov, model string) (float64, float64, bool) {
	if prov == "fake" || prov == "imported" || prov == "local" || strings.HasSuffix(model, ":free") {
		return 0, 0, true
	}
	row, e := r.DB.Get(ctx, "catalogs", prov)
	if e == nil {
		for _, m := range domain.Maps(row["items"]) {
			if m["id"] == model && m["price_in"] != nil && m["price_out"] != nil {
				return domain.Num(m["price_in"]) / 1e6, domain.Num(m["price_out"]) / 1e6, true
			}
		}
	}
	settings, e := r.DB.Get(ctx, "settings", "agent_lab")
	if e == nil {
		rate := domain.Obj(domain.Obj(settings["pricing"])[model])
		if rate["input_per_million"] != nil && rate["output_per_million"] != nil {
			return domain.Num(rate["input_per_million"]) / 1e6, domain.Num(rate["output_per_million"]) / 1e6, true
		}
	}
	return 0, 0, false
}
func (r *Registry) Vision(ctx context.Context, prov, model string) bool {
	if prov == "fake" || prov == "imported" || prov == "local" {
		return true
	}
	row, e := r.DB.Get(ctx, "catalogs", prov)
	if e == nil {
		for _, m := range domain.Maps(row["items"]) {
			if m["id"] == model {
				return domain.Has(domain.Strings(m["modalities"]), "image")
			}
		}
	}
	return prov == "openai" && (strings.HasPrefix(model, "gpt-4o") || strings.HasPrefix(model, "gpt-4.1") || strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4"))
}
func (r *Registry) Refresh(ctx context.Context) error {
	if err := r.PublishStatus(ctx); err != nil {
		return err
	}
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, s := range r.Specs(ctx) {
		if s.BaseURL == "" || (s.RequiresKey && os.Getenv(s.KeyEnv) == "") {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(s Spec) {
			defer wg.Done()
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			req, e := http.NewRequestWithContext(c, "GET", strings.TrimRight(s.BaseURL, "/")+"/models", nil)
			if e != nil {
				return
			}
			if k := os.Getenv(s.KeyEnv); k != "" {
				req.Header.Set("Authorization", "Bearer "+k)
			}
			res, e := r.Client.Do(req)
			if e != nil {
				return
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				return
			}
			var body M
			if json.NewDecoder(io.LimitReader(res.Body, 10<<20)).Decode(&body) != nil {
				return
			}
			models := []M{}
			for _, raw := range domain.Maps(body["data"]) {
				id := domain.Str(raw["id"])
				nonchat := false
				for _, hint := range []string{"whisper", "tts", "embedding", "dall-e", "moderation", "realtime", "transcribe", "sora", "babbage", "davinci"} {
					if strings.Contains(strings.ToLower(id), hint) {
						nonchat = true
					}
				}
				if nonchat {
					continue
				}
				p := domain.Obj(raw["pricing"])
				modalities := []string{"text"}
				if strings.Contains(domain.Str(domain.Obj(raw["architecture"])["modality"]), "image") || domain.Has(domain.Strings(domain.Obj(raw["architecture"])["input_modalities"]), "image") {
					modalities = append(modalities, "image")
				}
				if s.Name == "openai" && strings.HasPrefix(id, "gpt-") {
					modalities = append(modalities, "image")
				}
				var pin, pout any
				if p["prompt"] != nil {
					pin = domain.Num(p["prompt"]) * 1e6
				}
				if p["completion"] != nil {
					pout = domain.Num(p["completion"]) * 1e6
				}
				name := raw["name"]
				if name == nil {
					name = id
				}
				models = append(models, M{"provider": s.Name, "id": id, "name": name, "description": domain.Str(raw["description"]), "modalities": modalities, "context_length": raw["context_length"], "price_in": pin, "price_out": pout, "price_source": "provider_catalog", "structured_output": nil, "seed": nil, "free": pin != nil && pout != nil && domain.Num(pin) == 0 && domain.Num(pout) == 0, "created": raw["created"]})
			}
			_ = r.DB.Put(c, "catalogs", s.Name, M{"items": models, "updated_at": domain.Now()})
		}(s)
	}
	wg.Wait()
	return ctx.Err()
}
func (r *Registry) Rate(ctx context.Context, s Spec) error {
	if s.RPM >= 100000 {
		return nil
	}
	var next time.Time
	err := r.DB.Pool.QueryRow(ctx, `INSERT INTO arl.provider_limits(project_id,provider,next_at) VALUES($1,$2,now()+$3::interval)
 ON CONFLICT(project_id,provider) DO UPDATE SET next_at=GREATEST(arl.provider_limits.next_at,now())+$3::interval RETURNING next_at`, r.DB.Project, s.Name, fmt.Sprintf("%f seconds", 60/float64(s.RPM))).Scan(&next)
	if err != nil {
		return err
	}
	wait := time.Until(next) - time.Duration(float64(time.Minute)/float64(s.RPM))
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func Payload(req Request) M {
	p := M{"model": req.Model, "messages": req.Messages, "stream": req.Stream}
	params := req.Params
	field := "max_tokens"
	if req.Provider == "openai" {
		field = "max_completion_tokens"
	}
	p[field] = params["max_tokens"]
	for _, k := range []string{"temperature", "top_p", "seed"} {
		if params[k] != nil && !(req.Provider == "openai" && (strings.HasPrefix(req.Model, "o") || strings.HasPrefix(req.Model, "gpt-5")) && k != "seed") {
			p[k] = params[k]
		}
	}
	for _, k := range []string{"tools", "tool_choice"} {
		if params[k] != nil {
			p[k] = params[k]
		}
	}
	if domain.Bool(params["json_mode"]) {
		p["response_format"] = M{"type": "json_object"}
	}
	if params["reasoning_effort"] != nil {
		if req.Provider == "openrouter" {
			p["reasoning"] = M{"effort": params["reasoning_effort"]}
		} else {
			p["reasoning_effort"] = params["reasoning_effort"]
		}
	}
	if req.Stream && req.Provider != "openrouter" {
		p["stream_options"] = M{"include_usage": true}
	}
	if req.Provider == "openrouter" {
		p["usage"] = M{"include": true}
	}
	return p
}
func (r *Registry) Generate(ctx context.Context, req Request) (M, error) {
	if req.Provider == "fake" {
		return Fake(ctx, req)
	}
	s, ok := r.Spec(ctx, req.Provider)
	if !ok || s.Name == "imported" {
		return nil, &Error{Category: "provider_not_found"}
	}
	if s.RequiresKey && os.Getenv(s.KeyEnv) == "" {
		return nil, &Error{Category: "auth"}
	}
	if e := r.Rate(ctx, s); e != nil {
		return nil, e
	}
	start := time.Now()
	body := domain.JSON(Payload(req))
	httpReq, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(s.BaseURL, "/")+"/chat/completions", strings.NewReader(string(body)))
	if e != nil {
		return nil, &Error{Category: "configuration"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := os.Getenv(s.KeyEnv); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	res, e := r.Client.Do(httpReq)
	if e != nil {
		category := "network"
		if errors.Is(e, context.DeadlineExceeded) {
			category = "timeout"
		}
		return nil, &Error{Category: category, Retryable: true, MaybeProcessed: true}
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		cat := "provider_error"
		switch res.StatusCode {
		case 401, 403:
			cat = "auth"
		case 404:
			cat = "model_not_found"
		case 429:
			cat = "rate_limited"
		case 400, 422:
			cat = "invalid_request"
		}
		retry := res.StatusCode == 429 || res.StatusCode >= 500
		ra, _ := strconv.ParseFloat(res.Header.Get("Retry-After"), 64)
		return nil, &Error{Category: cat, Retryable: retry, MaybeProcessed: res.StatusCode >= 500, Status: res.StatusCode, RetryAfter: time.Duration(ra * float64(time.Second))}
	}
	out := M{"text": "", "provider": req.Provider, "requested_model": req.Model, "resolved_model": req.Model, "upstream_provider": nil, "request_id": res.Header.Get("x-request-id"), "tokens_in": nil, "tokens_out": nil, "cost_usd": nil, "cost_source": nil, "ttft_ms": nil}
	var content strings.Builder
	done := !req.Stream
	fill := func(d M) error {
		if d["error"] != nil {
			return &Error{Category: "provider_error", Retryable: true, MaybeProcessed: true}
		}
		for _, key := range []string{"model", "provider", "id"} {
			if d[key] != nil {
				target := map[string]string{"model": "resolved_model", "provider": "upstream_provider", "id": "request_id"}[key]
				out[target] = d[key]
			}
		}
		usage := domain.Obj(d["usage"])
		if usage["prompt_tokens"] != nil {
			out["tokens_in"] = usage["prompt_tokens"]
		}
		if usage["completion_tokens"] != nil {
			out["tokens_out"] = usage["completion_tokens"]
		}
		if usage["cost"] != nil {
			out["cost_usd"], out["cost_source"] = usage["cost"], "provider"
		}
		if v := domain.Obj(usage["completion_tokens_details"])["reasoning_tokens"]; v != nil {
			out["reasoning_tokens"] = v
		}
		for _, choice := range domain.Maps(d["choices"]) {
			message := domain.Obj(choice["message"])
			if req.Stream {
				message = domain.Obj(choice["delta"])
			}
			if calls := message["tool_calls"]; calls != nil && !req.Stream {
				out["tool_calls"] = calls
			}
			piece := domain.Str(message["content"])
			if piece != "" {
				if out["ttft_ms"] == nil {
					out["ttft_ms"] = float64(time.Since(start).Microseconds()) / 1000
				}
				if content.Len()+len(piece) > 2<<20 {
					return &Error{Category: "response_too_large", MaybeProcessed: true}
				}
				content.WriteString(piece)
				if req.OnDelta != nil {
					req.OnDelta(piece)
				}
			}
			if choice["finish_reason"] != nil {
				out["finish_reason"] = choice["finish_reason"]
				done = true
			}
		}
		return nil
	}
	if req.Stream {
		scanner := bufio.NewScanner(io.LimitReader(res.Body, 16<<20))
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if raw == "[DONE]" {
				done = true
				break
			}
			var d M
			if json.Unmarshal([]byte(raw), &d) != nil {
				continue
			}
			if e = fill(d); e != nil {
				return nil, e
			}
		}
		if scanner.Err() != nil || !done {
			return nil, &Error{Category: "incomplete_stream", Retryable: true, MaybeProcessed: true}
		}
	} else {
		var d M
		if e = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&d); e != nil {
			return nil, &Error{Category: "invalid_response", MaybeProcessed: true}
		}
		if e = fill(d); e != nil {
			return nil, e
		}
	}
	out["text"] = content.String()
	out["latency_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if out["cost_usd"] == nil && out["tokens_in"] != nil && out["tokens_out"] != nil {
		pin, pout, known := r.Price(ctx, req.Provider, req.Model)
		if known {
			out["cost_usd"] = domain.Num(out["tokens_in"])*pin + domain.Num(out["tokens_out"])*pout
			out["cost_source"] = "price_table"
		}
	}
	return out, nil
}
func unit(parts ...any) float64 {
	h := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return float64(binary.BigEndian.Uint64(h[:8])) / 18446744073709551616.
}
func Fake(ctx context.Context, req Request) (M, error) {
	start := time.Now()
	delay := time.Duration(15+int(unit(req.SampleKey, req.Model)*90)) * time.Millisecond
	if req.Model == "fake/slow" {
		delay = 500 * time.Millisecond
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(delay):
	}
	if strings.Contains(req.Model, "flaky") && req.Attempt <= 1 && unit("flaky", req.SampleKey) < 0.3 {
		return nil, &Error{Category: "rate_limited", Retryable: true, RetryAfter: 25 * time.Millisecond}
	}
	text := ""
	for _, m := range req.Messages {
		if m["role"] == "user" {
			if s, ok := m["content"].(string); ok {
				text += s
			}
			for _, p := range domain.Maps(m["content"]) {
				if p["type"] == "text" {
					text += domain.Str(p["text"])
				}
			}
		}
	}
	model := req.Model
	accuracy := 0.
	_, _ = fmt.Sscanf(req.Model, "fake/oracle-%f", &accuracy)
	accuracy /= 100
	if strings.Contains(req.Model, "flaky") {
		accuracy = 0.8
	}
	if req.Model == "fake/slow" {
		accuracy = 0.7
	}
	if req.Model == "fake/router" {
		accuracy = 0.65
		model = "fake/small"
		if strings.Contains(strings.ToLower(text), "think carefully") || strings.Contains(strings.ToLower(text), "json") || unit("route", req.SampleKey) < 0.15 {
			accuracy = 0.92
			model = "fake/large"
		}
	}
	if req.Reference != nil && req.Model != "fake/echo" {
		v := req.Reference
		if unit("acc", model, req.SampleKey, req.Params["seed"]) >= accuracy {
			switch x := v.(type) {
			case float64:
				v = x + 1
			case int:
				v = x + 1
			case string:
				if len(x) == 1 && strings.Contains("ABCDE", x) {
					v = string(rune('A') + (rune(x[0]-'A')+1)%4)
				} else {
					v = "I am not sure."
				}
			default:
				v = M{"incorrect": true}
			}
		}
		if s, ok := v.(string); ok {
			text = s
		} else {
			text = string(domain.JSON(v))
		}
	}
	if req.OnDelta != nil {
		req.OnDelta(text)
	}
	ms := float64(time.Since(start).Microseconds()) / 1000
	return M{"text": text, "provider": "fake", "requested_model": req.Model, "resolved_model": model, "upstream_provider": "fake-lab", "request_id": domain.ID("fake"), "finish_reason": "stop", "tokens_in": domain.Max(1, len(text)/4), "tokens_out": domain.Max(1, len(text)/4), "cost_usd": 0., "cost_source": "free", "latency_ms": ms, "ttft_ms": ms * 0.35}, nil
}
