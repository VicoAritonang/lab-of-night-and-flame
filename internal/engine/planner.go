package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/provider"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/scoring"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/stats"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"math"
	"math/rand"
	"sort"
	"strings"
)

type M = domain.M

func Plan(raw M) (M, error) {
	p := domain.Copy(raw)
	for k := range p {
		if !domain.Has([]string{"schema_version", "dataset_id", "targets", "prompts", "params_grid", "scorers", "primary_scorer", "protocol", "budget", "acceptance"}, k) {
			return nil, fmt.Errorf("unknown plan field: %s", k)
		}
	}
	domain.Default(p, "schema_version", "arl-experiment-v1")
	if p["schema_version"] != "arl-experiment-v1" {
		return nil, errors.New("unsupported experiment schema")
	}
	if domain.Str(p["dataset_id"]) == "" {
		return nil, errors.New("dataset_id required")
	}
	targets := domain.Maps(p["targets"])
	if len(targets) < 1 || len(targets) > 24 {
		return nil, errors.New("targets must contain 1-24 entries")
	}
	for _, t := range targets {
		if domain.Str(t["provider"]) == "" || domain.Str(t["model"]) == "" {
			return nil, errors.New("each target requires provider and model")
		}
		params := domain.Obj(t["params"])
		domain.Default(params, "temperature", 0.)
		domain.Default(params, "max_tokens", 512)
		domain.Default(params, "json_mode", false)
		if domain.Int(params["max_tokens"]) < 1 || domain.Int(params["max_tokens"]) > 32768 {
			return nil, errors.New("max_tokens must be 1-32768")
		}
		if params["temperature"] != nil && (domain.Num(params["temperature"]) < 0 || domain.Num(params["temperature"]) > 2) {
			return nil, errors.New("temperature must be 0-2")
		}
		if params["top_p"] != nil && (domain.Num(params["top_p"]) <= 0 || domain.Num(params["top_p"]) > 1) {
			return nil, errors.New("top_p must be >0 and <=1")
		}
		t["params"] = params
	}
	p["targets"] = targets
	prompts := domain.Maps(p["prompts"])
	if p["prompts"] == nil {
		prompts = []M{{"id": "default", "system": "{instructions}", "user": "{input}"}}
	}
	if len(prompts) < 1 || len(prompts) > 8 {
		return nil, errors.New("prompts must have 1-8 variants")
	}
	seen := map[string]bool{}
	for _, pr := range prompts {
		domain.Default(pr, "id", "default")
		domain.Default(pr, "system", "{instructions}")
		domain.Default(pr, "user", "{input}")
		id := domain.Str(pr["id"])
		if seen[id] || id == "" || len(id) > 40 {
			return nil, errors.New("prompt IDs must be unique, 1-40 characters")
		}
		seen[id] = true
	}
	p["prompts"] = prompts
	grid := domain.Obj(p["params_grid"])
	for key, v := range grid {
		if !domain.Has([]string{"temperature", "top_p", "max_tokens", "reasoning_effort"}, key) || len(domain.Arr(v)) < 1 || len(domain.Arr(v)) > 8 {
			return nil, errors.New("invalid parameter grid")
		}
	}
	p["params_grid"] = grid
	specs := domain.Maps(p["scorers"])
	if len(specs) < 1 || len(specs) > 12 {
		return nil, errors.New("scorers must have 1-12 entries")
	}
	seen = map[string]bool{}
	for _, spec := range specs {
		id := domain.Str(spec["id"])
		if id == "" || seen[id] {
			return nil, errors.New("scorer IDs must be unique")
		}
		seen[id] = true
		if e := scoring.Validate(spec); e != nil {
			return nil, e
		}
		domain.Default(spec, "params", M{})
	}
	if !seen[domain.Str(p["primary_scorer"])] {
		return nil, errors.New("primary_scorer must reference a scorer ID")
	}
	p["scorers"] = specs
	proto := domain.Obj(p["protocol"])
	for k, v := range (M{"repetitions": 1, "order": "interleaved", "seed": 20261011, "concurrency": 6, "timeout_s": 120., "max_retries": 2, "splits": []string{"test"}, "sample_limit": nil, "stream": true}) {
		domain.Default(proto, k, v)
	}
	if domain.Int(proto["repetitions"]) < 1 || domain.Int(proto["repetitions"]) > 20 || domain.Int(proto["concurrency"]) < 1 || domain.Int(proto["concurrency"]) > 64 || domain.Num(proto["timeout_s"]) < 5 || domain.Num(proto["timeout_s"]) > 900 || domain.Int(proto["max_retries"]) < 0 || domain.Int(proto["max_retries"]) > 6 {
		return nil, errors.New("invalid repetitions, concurrency, timeout or retry limit")
	}
	if proto["order"] != "interleaved" && proto["order"] != "blocked" {
		return nil, errors.New("invalid trial order")
	}
	if proto["sample_limit"] != nil && domain.Int(proto["sample_limit"]) < 1 {
		return nil, errors.New("sample_limit must be positive")
	}
	p["protocol"] = proto
	budget := domain.Obj(p["budget"])
	domain.Default(budget, "max_usd", 1.)
	domain.Default(budget, "paid_run_confirmed", false)
	if budget["max_usd"] != nil && domain.Num(budget["max_usd"]) < 0 {
		return nil, errors.New("negative budget")
	}
	p["budget"] = budget
	acc := domain.Obj(p["acceptance"])
	for k, v := range (M{"min_pass_rate": nil, "baseline_arm": nil, "non_inferiority_margin": nil, "min_assessed_per_arm": 20, "max_p95_latency_ms": nil, "max_cost_per_pass_usd": nil, "max_error_rate": 0.1}) {
		domain.Default(acc, k, v)
	}
	for _, k := range []string{"min_pass_rate", "non_inferiority_margin", "max_error_rate"} {
		if acc[k] != nil && (domain.Num(acc[k]) < 0 || domain.Num(acc[k]) > 1) {
			return nil, errors.New("acceptance probability outside 0-1")
		}
	}
	if domain.Int(acc["min_assessed_per_arm"]) < 1 {
		return nil, errors.New("min_assessed_per_arm must be positive")
	}
	p["acceptance"] = acc
	if len(Arms(p)) > 48 {
		return nil, errors.New("maximum 48 arms; reduce grid")
	}
	return p, nil
}
func Arms(p M) []M {
	grid := domain.Obj(p["params_grid"])
	keys := []string{}
	for k := range grid {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	combos := []M{{}}
	for _, key := range keys {
		next := []M{}
		for _, c := range combos {
			for _, v := range domain.Arr(grid[key]) {
				n := domain.Copy(c)
				n[key] = v
				next = append(next, n)
			}
		}
		combos = next
	}
	out := []M{}
	seen := map[string]bool{}
	prompts := domain.Maps(p["prompts"])
	for i, t := range domain.Maps(p["targets"]) {
		for _, pr := range prompts {
			for _, combo := range combos {
				params := domain.Merge(domain.Obj(t["params"]), combo)
				ident := M{"provider": t["provider"], "model": t["model"], "params": params, "system": pr["system"], "user": pr["user"]}
				key := domain.Hash(ident)[:12]
				if seen[key] {
					continue
				}
				seen[key] = true
				label := domain.Str(t["label"])
				if label == "" {
					label = domain.Str(t["model"])
					if !strings.HasPrefix(label, domain.Str(t["provider"])+"/") {
						label = domain.Str(t["provider"]) + "/" + label
					}
				}
				if len(prompts) > 1 {
					label += " · prompt:" + domain.Str(pr["id"])
				}
				for _, k := range keys {
					label += fmt.Sprintf(" · %s=%v", k, combo[k])
				}
				out = append(out, M{"key": key, "label": label, "provider": t["provider"], "model": t["model"], "params": params, "prompt_id": pr["id"], "system": pr["system"], "user": pr["user"], "target_index": i})
			}
		}
	}
	return out
}
func Samples(ctx context.Context, s *store.Store, p M) ([]M, error) {
	rows, e := s.List(ctx, "samples", M{"dataset_id": p["dataset_id"]}, 50000)
	if e != nil {
		return nil, e
	}
	store.Order(rows, "ordinal", false)
	proto := domain.Obj(p["protocol"])
	splits := domain.Strings(proto["splits"])
	out := []M{}
	for _, row := range rows {
		if domain.Has(splits, "all") || domain.Has(splits, domain.Str(row["split"])) {
			out = append(out, row)
		}
	}
	if proto["sample_limit"] != nil && len(out) > domain.Int(proto["sample_limit"]) {
		out = out[:domain.Int(proto["sample_limit"])]
	}
	return out, nil
}
func Render(template, instructions, text string, meta M) string {
	s := strings.ReplaceAll(strings.ReplaceAll(template, "{instructions}", instructions), "{input}", text)
	for k, v := range meta {
		s = strings.ReplaceAll(s, "{meta."+k+"}", domain.Str(v))
	}
	return s
}
func Preflight(ctx context.Context, s *store.Store, r *provider.Registry, p M) (M, error) {
	ds, e := s.Get(ctx, "datasets", domain.Str(p["dataset_id"]))
	if e != nil {
		return nil, e
	}
	samples, e := Samples(ctx, s, p)
	if e != nil {
		return nil, e
	}
	errs, warns, labels, unknown := []string{}, []string{}, []string{}, []string{}
	disclosure := []M{}
	if len(samples) == 0 {
		errs = append(errs, "no samples in requested splits")
	}
	if domain.Bool(ds["synthetic"]) {
		labels = append(labels, "synthetic_data")
	}
	low, high := 0., 0.
	arms := Arms(p)
	proto := domain.Obj(p["protocol"])
	reps := domain.Int(proto["repetitions"])
	for _, a := range arms {
		prov, model := domain.Str(a["provider"]), domain.Str(a["model"])
		spec, exists := r.Spec(ctx, prov)
		if !exists {
			errs = append(errs, "unknown provider: "+prov)
		} else if spec.RequiresKey {
			for _, st := range r.Status(ctx) {
				if st["name"] == prov && !domain.Bool(st["configured"]) {
					errs = append(errs, "provider has no API key: "+prov)
				}
			}
		}
		if prov == "fake" {
			labels = append(labels, "fake_runtime_test")
		}
		if prov == "imported" {
			labels = append(labels, "imported_outputs")
			n, e := s.Count(ctx, "predictions", M{"dataset_id": p["dataset_id"], "import_name": model})
			if e != nil {
				return nil, e
			}
			if n == 0 {
				errs = append(errs, "no imported predictions: "+model)
			}
		}
		pin, pout, known := r.Price(ctx, prov, model)
		a["price_known"] = known
		applicable := 0
		armLow, armHigh := 0., 0.
		vision := r.Vision(ctx, prov, model)
		for _, sample := range samples {
			input := domain.Obj(sample["input"])
			nimages := len(domain.Arr(input["images"]))
			if nimages > 0 && !vision {
				continue
			}
			applicable++
			tokens := float64(len(domain.Str(input["text"])+domain.Str(ds["instructions"])))/3.5 + 8 + 800*float64(nimages)
			maxOut := float64(domain.Int(domain.Obj(a["params"])["max_tokens"]))
			armLow += (tokens*pin + math.Min(32, maxOut)*pout) * float64(reps)
			armHigh += (tokens*pin + maxOut*pout) * float64(reps)
		}
		a["applicable"] = applicable > 0
		a["partial"] = applicable > 0 && applicable < len(samples)
		a["trials"] = applicable * reps
		if !known {
			unknown = append(unknown, domain.Str(a["label"]))
			a["cost_low_usd"], a["cost_high_usd"] = nil, nil
		} else {
			a["cost_low_usd"], a["cost_high_usd"] = armLow, armHigh
		}
		low += armLow
		high += armHigh
		if applicable < len(samples) {
			warns = append(warns, domain.Str(a["label"])+": image samples not supported; marked not_applicable")
		}
		kind := "external"
		if domain.Has([]string{"fake", "imported", "local"}, prov) {
			kind = "local"
		}
		disclosure = append(disclosure, M{"recipient": prov, "kind": kind, "models": []string{model}, "data": "sample inputs and optional images (expected answers are never sent)"})
	}
	judgeCalls := 0
	for _, spec := range domain.Maps(p["scorers"]) {
		if spec["key"] == "llm_judge" {
			n := len(samples) * reps * len(arms)
			judgeCalls += n
			params := domain.Obj(spec["params"])
			prov := domain.Str(params["provider"])
			if prov == "" {
				prov = "openai"
			}
			model := domain.Str(params["model"])
			if model == "" {
				model = "gpt-4.1-nano"
			}
			pin, pout, known := r.Price(ctx, prov, model)
			if !known {
				unknown = append(unknown, "judge "+prov+"/"+model)
			} else {
				low += float64(n) * (750*pin + 40*pout)
				high += float64(n) * (1500*pin + 400*pout)
			}
			disclosure = append(disclosure, M{"recipient": "judge " + prov + "/" + model, "kind": "judge", "data": "task, candidate output, rubric, optional reference answer"})
		}
	}
	unique := func(a []string) []string {
		seen := map[string]bool{}
		b := []string{}
		for _, s := range a {
			if !seen[s] {
				b = append(b, s)
				seen[s] = true
			}
		}
		sort.Strings(b)
		return b
	}
	labels = unique(labels)
	if !domain.Has(labels, "fake_runtime_test") && !domain.Has(labels, "imported_outputs") {
		labels = append(labels, "model_quality_test")
	}
	trials := len(samples) * reps * len(arms)
	if trials > 50000 {
		errs = append(errs, "maximum 50000 trials per campaign")
	}
	if trials*len(domain.Maps(p["scorers"])) > 100000 {
		errs = append(errs, "maximum 100000 assessments per scoring revision; reduce samples, arms or scorers")
	}
	if len(unknown) > 0 {
		warns = append(warns, "Some prices unknown; conservative reservation required")
	}
	if len(samples)*reps < domain.Int(domain.Obj(p["acceptance"])["min_assessed_per_arm"]) {
		warns = append(warns, "Too few trials for acceptance minimum")
	}
	budget := domain.Obj(p["budget"])
	if budget["max_usd"] != nil && high > domain.Num(budget["max_usd"]) {
		warns = append(warns, "Worst-case cost exceeds budget cap")
	}
	// Statistical power of the paired comparison this plan can support: the
	// smallest pass-rate difference (from a 50% or 80% baseline) detectable at
	// alpha=0.05 with 80% power, and the samples needed for a 5-point change.
	power := M{"alpha": 0.05, "power": 0.8, "samples_per_arm": len(samples), "mde_at_50": stats.MinimumDetectable(0.5, len(samples), 0.05, 0.8), "mde_at_80": stats.MinimumDetectable(0.8, len(samples), 0.05, 0.8), "samples_for_5pt_at_80": stats.SampleSize(0.8, 0.85, 0.05, 0.8), "method": "two-proportion normal approximation with Cohen's h (conservative for paired designs)"}
	if len(arms) > 1 && len(samples) > 0 && domain.Num(power["mde_at_80"]) > 0.15 {
		warns = append(warns, fmt.Sprintf("Underpowered: with %d samples only differences of about %.0f points are reliably detectable", len(samples), domain.Num(power["mde_at_80"])*100))
	}
	return M{"ok": len(errs) == 0, "errors": errs, "warnings": warns, "arms": arms, "samples": len(samples), "trials": trials, "calls_estimate": trials, "judge_calls_estimate": judgeCalls, "cost_low_usd": low, "cost_high_usd": high, "unknown_price_arms": unique(unknown), "disclosure": disclosure, "plan_digest": domain.Hash(p), "labels": labels, "power": power}, nil
}
func Schedule(arms, samples []M, proto M) []M {
	rng := rand.New(rand.NewSource(int64(domain.Num(proto["seed"]))))
	out := []M{}
	sampleOrder := append([]M{}, samples...)
	if proto["order"] == "blocked" {
		rng.Shuffle(len(sampleOrder), func(i, j int) { sampleOrder[i], sampleOrder[j] = sampleOrder[j], sampleOrder[i] })
	}
	for rep := 0; rep < domain.Int(proto["repetitions"]); rep++ {
		for _, s := range sampleOrder {
			aorder := append([]M{}, arms...)
			if proto["order"] == "blocked" {
				rng.Shuffle(len(aorder), func(i, j int) { aorder[i], aorder[j] = aorder[j], aorder[i] })
			}
			for _, a := range aorder {
				out = append(out, M{"arm_key": a["key"], "sample_id": s["id"], "repetition": rep})
			}
		}
	}
	if proto["order"] == "interleaved" {
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	}
	return out
}
