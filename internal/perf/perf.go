// Package perf load-tests a model endpoint: the same request at increasing
// concurrency, measuring time to first token, end-to-end latency, decode
// speed, throughput and errors per level. It answers "how does this model (or
// this local server on this hardware) behave under my agent's load?".
package perf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/provider"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/stats"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
)

type M = domain.M

const Runs = "perf_runs"

// Validate normalizes a perf run request and estimates its worst-case cost.
func Validate(ctx context.Context, reg *provider.Registry, raw M) (M, error) {
	prov, model := domain.Str(raw["provider"]), domain.Str(raw["model"])
	spec, ok := reg.Spec(ctx, prov)
	if !ok || prov == "imported" {
		return nil, errors.New("unknown provider")
	}
	if model == "" {
		return nil, errors.New("model is required")
	}
	prompt := domain.Str(raw["prompt"])
	if strings.TrimSpace(prompt) == "" {
		prompt = "Write a short paragraph explaining what a load test measures."
	}
	if len(prompt) > 200000 {
		return nil, errors.New("prompt exceeds 200000 characters")
	}
	maxTokens := domain.Int(raw["max_tokens"])
	if maxTokens == 0 {
		maxTokens = 256
	}
	if maxTokens < 1 || maxTokens > 8192 {
		return nil, errors.New("max_tokens must be 1-8192")
	}
	levels := []int{}
	for _, v := range domain.Arr(raw["concurrency"]) {
		n := domain.Int(v)
		if n < 1 || n > 64 {
			return nil, errors.New("concurrency levels must be 1-64")
		}
		levels = append(levels, n)
	}
	if len(levels) == 0 {
		levels = []int{1, 2, 4, 8}
	}
	if len(levels) > 8 {
		return nil, errors.New("at most 8 concurrency levels")
	}
	sort.Ints(levels)
	requests := domain.Int(raw["requests"])
	if requests == 0 {
		requests = 8
	}
	if requests < 1 || requests > 200 {
		return nil, errors.New("requests per level must be 1-200")
	}
	stream := true
	if v, ok := raw["stream"].(bool); ok {
		stream = v
	}
	total := requests * len(levels)
	pin, pout, known := reg.Price(ctx, prov, model)
	estimate := any(nil)
	if known {
		estimate = float64(total) * (float64(len(prompt)/3+20)*pin + float64(maxTokens)*pout)
	}
	if spec.Kind == "external" || spec.Kind == "custom" {
		if (!known || domain.Num(estimate) > 0) && !domain.Bool(raw["confirm_paid"]) {
			return nil, fmt.Errorf("this sends %d requests to a paid provider (estimated %s); set confirm_paid", total, money(estimate))
		}
	}
	return M{"provider": prov, "model": model, "prompt": prompt, "max_tokens": maxTokens, "temperature": raw["temperature"], "concurrency": levels, "requests": requests, "stream": stream, "estimated_cost_usd": estimate, "base_url": spec.BaseURL, "label": domain.Str(raw["label"])}, nil
}

func money(v any) string {
	if v == nil {
		return "unknown cost"
	}
	return fmt.Sprintf("$%.4f", domain.Num(v))
}

// Environment describes the machine running the lab (the load generator).
func Environment() M {
	host, _ := os.Hostname()
	return M{"os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(), "go": runtime.Version(), "host": host}
}

type sample struct {
	latency, ttft, tokens float64
	cost                  any
	err                   string
}

// Level runs n identical requests with at most c in flight.
func Level(ctx context.Context, reg *provider.Registry, run M, c, n int, key string) M {
	samples := make([]sample, n)
	sem := make(chan struct{}, c)
	var wg sync.WaitGroup
	start := time.Now()
	params := M{"max_tokens": run["max_tokens"]}
	if run["temperature"] != nil {
		params["temperature"] = run["temperature"]
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			req := provider.Request{Provider: domain.Str(run["provider"]), Model: domain.Str(run["model"]), Messages: []M{{"role": "user", "content": run["prompt"]}}, Params: params, Stream: domain.Bool(run["stream"]), SampleKey: fmt.Sprintf("%s:%d:%d", key, c, i), Attempt: 2}
			callCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
			defer cancel()
			t0 := time.Now()
			res, err := reg.Generate(callCtx, req)
			if err != nil {
				category := "provider_error"
				var pe *provider.Error
				if errors.As(err, &pe) {
					category = pe.Category
				}
				samples[i] = sample{latency: float64(time.Since(t0).Microseconds()) / 1000, err: category}
				return
			}
			s := sample{latency: domain.Num(res["latency_ms"]), ttft: domain.Num(res["ttft_ms"]), tokens: domain.Num(res["tokens_out"]), cost: res["cost_usd"]}
			if res["ttft_ms"] == nil {
				s.ttft = s.latency
			}
			samples[i] = s
		}(i)
	}
	wg.Wait()
	wall := time.Since(start).Seconds()
	lat, ttft, decode := []float64{}, []float64{}, []float64{}
	tokens, cost := 0., 0.
	errs := map[string]int{}
	ok := 0
	for _, s := range samples {
		if s.err != "" {
			errs[s.err]++
			continue
		}
		ok++
		lat, ttft = append(lat, s.latency), append(ttft, s.ttft)
		tokens += s.tokens
		cost += domain.Num(s.cost)
		if gen := (s.latency - s.ttft) / 1000; gen > 0 && s.tokens > 1 {
			decode = append(decode, (s.tokens-1)/gen)
		}
	}
	out := M{"concurrency": c, "requests": n, "ok": ok, "errors": errs, "error_rate": float64(n-ok) / float64(n), "wall_s": wall, "latency_ms": stats.Summary(lat), "ttft_ms": stats.Summary(ttft), "decode_tokens_per_s": stats.Summary(decode), "output_tokens": tokens, "cost_usd": cost}
	if wall > 0 {
		out["requests_per_s"], out["output_tokens_per_s"] = float64(ok)/wall, tokens/wall
	}
	return out
}

// Execute runs a stored perf run level by level (job perf.run), saving
// progress after each level so the UI can chart it live.
func Execute(ctx context.Context, db *store.Store, reg *provider.Registry, id string) (M, error) {
	run, err := db.Get(ctx, Runs, id)
	if err != nil {
		return nil, err
	}
	if run["status"] == "completed" || run["status"] == "cancelled" {
		return M{"result_kind": Runs, "result_id": id}, nil
	}
	_, _ = db.Update(ctx, Runs, id, func(m M) error { m["status"], m["started_at"] = "running", domain.Now(); return nil })
	levels := []M{}
	for _, c := range domain.Arr(run["concurrency"]) {
		current, err := db.Get(ctx, Runs, id)
		if err != nil {
			return nil, err
		}
		if current["status"] == "cancelled" {
			return M{"result_kind": Runs, "result_id": id}, nil
		}
		levels = append(levels, Level(ctx, reg, run, domain.Int(c), domain.Int(run["requests"]), id))
		if _, err = db.Update(ctx, Runs, id, func(m M) error { m["levels"] = levels; return nil }); err != nil {
			return nil, err
		}
	}
	_, err = db.Update(ctx, Runs, id, func(m M) error {
		m["status"], m["finished_at"] = "completed", domain.Now()
		m["saturation"] = Saturation(levels)
		return nil
	})
	return M{"result_kind": Runs, "result_id": id}, err
}

// Saturation picks the concurrency with the best throughput whose p95 latency
// stays within 1.5x of the single-request p95 and errors below 5%.
func Saturation(levels []M) M {
	if len(levels) == 0 {
		return nil
	}
	base := domain.Num(domain.Obj(levels[0]["latency_ms"])["p95"])
	var best M
	for _, l := range levels {
		p95 := domain.Num(domain.Obj(l["latency_ms"])["p95"])
		if domain.Num(l["error_rate"]) > 0.05 || (base > 0 && p95 > base*1.5) {
			continue
		}
		if best == nil || domain.Num(l["requests_per_s"]) > domain.Num(best["requests_per_s"]) {
			best = l
		}
	}
	if best == nil {
		return M{"concurrency": nil, "note": "latency or errors degrade from the first level"}
	}
	return M{"concurrency": best["concurrency"], "requests_per_s": best["requests_per_s"], "output_tokens_per_s": best["output_tokens_per_s"], "note": "highest throughput with p95 latency within 1.5x of sequential and <5% errors"}
}
