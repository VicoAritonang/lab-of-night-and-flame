package engine

import (
	"context"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/stats"
	"sort"
)

func (e *Engine) Analyze(ctx context.Context, cid string) (M, error) {
	c, err := e.DB.Get(ctx, "campaigns", cid)
	if err != nil {
		return nil, err
	}
	p := domain.Obj(c["plan"])
	ds, err := e.DB.Get(ctx, "datasets", domain.Str(p["dataset_id"]))
	if err != nil {
		return nil, err
	}
	samples, err := Samples(ctx, e.DB, p)
	if err != nil {
		return nil, err
	}
	trials, err := e.DB.List(ctx, "trials", M{"campaign_id": cid}, 50000)
	if err != nil {
		return nil, err
	}
	attempts, err := e.DB.List(ctx, "attempts", M{"campaign_id": cid}, 350000)
	if err != nil {
		return nil, err
	}
	assessments, err := e.DB.List(ctx, "assessments", M{"campaign_id": cid, "revision": domain.Obj(c["scoring"])["revision"]}, 100000)
	if err != nil {
		return nil, err
	}
	scoring := domain.Obj(c["scoring"])
	primary := domain.Str(scoring["primary"])
	revision := domain.Int(scoring["revision"])
	arms := Arms(p)
	seed := int64(domain.Num(domain.Obj(p["protocol"])["seed"]))
	scorerSpecs := domain.Maps(scoring["scorers"])
	acc := domain.Obj(p["acceptance"])
	sampleByID := map[string]M{}
	for _, s := range samples {
		sampleByID[domain.Str(s["id"])] = s
	}
	assessByTrial := map[string]map[string]M{}
	for _, a := range assessments {
		if domain.Int(a["revision"]) != revision {
			continue
		}
		id := domain.Str(a["trial_id"])
		if assessByTrial[id] == nil {
			assessByTrial[id] = map[string]M{}
		}
		assessByTrial[id][domain.Str(a["scorer_key"])] = a
	}
	byArm := map[string][]M{}
	for _, t := range trials {
		key := domain.Str(t["arm_key"])
		byArm[key] = append(byArm[key], t)
	}
	attemptByTrial := map[string][]M{}
	for _, a := range attempts {
		id := domain.Str(a["trial_id"])
		attemptByTrial[id] = append(attemptByTrial[id], a)
	}
	summaries := M{}
	perSample := map[string]map[string][]float64{}
	matrixSamples := []M{}
	matrix := [][]any{}
	armKeys := []string{}
	for _, a := range arms {
		armKeys = append(armKeys, domain.Str(a["key"]))
	}
	for _, arm := range arms {
		key := domain.Str(arm["key"])
		rows := byArm[key]
		planned, na, completed, errorsN, cancelled, pending := len(rows), 0, 0, 0, 0, 0
		errcats := M{}
		latency, ttft, tps, tin, tout := []float64{}, []float64{}, []float64{}, []float64{}, []float64{}
		targetCost, judgeCost := 0., 0.
		unknown, nAttempts := 0, 0
		scorerSummaries := M{}
		perSample[key] = map[string][]float64{}
		for _, t := range rows {
			switch t["status"] {
			case "not_applicable":
				na++
			case "completed":
				completed++
			case "error":
				errorsN++
				errcats[domain.Str(t["error_category"])] = domain.Int(errcats[domain.Str(t["error_category"])]) + 1
			case "cancelled":
				cancelled++
			default:
				pending++
			}
			if t["latency_ms"] != nil {
				latency = append(latency, domain.Num(t["latency_ms"]))
			}
			if t["ttft_ms"] != nil {
				ttft = append(ttft, domain.Num(t["ttft_ms"]))
			}
			if t["tokens_in"] != nil {
				tin = append(tin, domain.Num(t["tokens_in"]))
			}
			if t["tokens_out"] != nil {
				tout = append(tout, domain.Num(t["tokens_out"]))
				if domain.Num(t["latency_ms"]) > 0 {
					tps = append(tps, domain.Num(t["tokens_out"])/(domain.Num(t["latency_ms"])/1000))
				}
			}
			for _, attempt := range attemptByTrial[domain.Str(t["id"])] {
				if attempt["kind"] == "judge" {
					continue
				}
				nAttempts++
				if attempt["cost_usd"] != nil {
					targetCost += domain.Num(attempt["cost_usd"])
				} else if attempt["status"] == "unknown" || attempt["status"] == "completed" || attempt["status"] == "running" {
					unknown++
				}
			}
			if a := assessByTrial[domain.Str(t["id"])][primary]; a != nil {
				if a["verdict"] == "pass" || a["verdict"] == "fail" {
					score := 0.
					if a["verdict"] == "pass" {
						score = 1
					}
					sid := domain.Str(t["sample_id"])
					perSample[key][sid] = append(perSample[key][sid], score)
				}
			}
		}
		for _, spec := range scorerSpecs {
			sid := domain.Str(spec["id"])
			n, passes, fails, inc, notApp, missing := 0, 0, 0, 0, 0, 0
			values := []float64{}
			versions := []string{}
			cost := 0.
			for _, t := range rows {
				a := assessByTrial[domain.Str(t["id"])][sid]
				if a == nil {
					missing++
					continue
				}
				ver := domain.Str(a["scorer_version"])
				if !domain.Has(versions, ver) {
					versions = append(versions, ver)
				}
				switch a["verdict"] {
				case "pass":
					n++
					passes++
				case "fail":
					n++
					fails++
				case "inconclusive":
					inc++
				case "not_applicable":
					notApp++
				}
				if a["score"] != nil {
					values = append(values, domain.Num(a["score"]))
				}
				if a["cost_usd"] != nil {
					cost += domain.Num(a["cost_usd"])
				}
			}
			judgeCost += cost
			scorerSummaries[sid] = M{"assessed": n, "passes": passes, "fails": fails, "inconclusive": inc, "not_applicable": notApp, "missing": missing, "pass_rate": stats.Wilson(passes, n), "mean_score": stats.Bootstrap(values, seed), "versions": versions, "cost_usd": cost}
		}
		prim := domain.Obj(scorerSummaries[primary])
		passes, n := domain.Int(prim["passes"]), domain.Int(prim["assessed"])
		taskRates := []float64{}
		passAt, allK, passOne, full := 0., 0., 0., 0
		k := domain.Int(domain.Obj(p["protocol"])["repetitions"])
		for _, values := range perSample[key] {
			taskRates = append(taskRates, domain.Num(stats.Mean(values)))
			if len(values) == k {
				count := 0
				for _, v := range values {
					if v == 1 {
						count++
					}
				}
				passAt += domain.Num(stats.PassK(len(values), count, k))
				if count == k {
					allK++
				}
				passOne += float64(count) / float64(k)
				full++
			}
		}
		var passK any
		if k > 1 {
			pk := M{"k": k, "pass_at_k": nil, "all_k": nil, "pass_at_1": nil, "samples_complete": full}
			if full > 0 {
				pk["pass_at_k"] = passAt / float64(full)
				pk["all_k"] = allK / float64(full)
				pk["pass_at_1"] = passOne / float64(full)
			}
			passK = pk
		}
		complete := unknown == 0
		var perTrial, perPass any
		if complete && completed > 0 {
			perTrial = (targetCost + judgeCost) / float64(completed)
		}
		if complete && passes > 0 {
			perPass = (targetCost + judgeCost) / float64(passes)
		}
		var errorRate any
		if planned-na > 0 {
			errorRate = float64(errorsN) / float64(planned-na)
		}
		summaries[key] = M{"arm": arm, "planned": planned, "not_applicable": na, "completed": completed, "errors": errorsN, "error_categories": errcats, "cancelled": cancelled, "pending": pending, "error_rate": errorRate, "attempts": nAttempts, "retries": domain.Max(0, nAttempts-completed-errorsN), "scorers": scorerSummaries, "primary": M{"assessed": n, "passes": passes, "pass_rate": stats.Wilson(passes, n), "all_admitted_rate": stats.Wilson(passes, planned-na), "task_balanced": stats.Bootstrap(taskRates, seed), "pass_k": passK}, "latency_ms": stats.Summary(latency), "ttft_ms": stats.Summary(ttft), "latency_hist": stats.Histogram(latency), "latency_values": latency, "output_tokens_per_s": stats.Summary(tps), "tokens_in_mean": stats.Mean(tin), "tokens_out_mean": stats.Mean(tout), "cost": M{"target_known_usd": targetCost, "judge_known_usd": judgeCost, "total_known_usd": targetCost + judgeCost, "unknown_cost_attempts": unknown, "complete": complete, "per_trial_usd": perTrial, "per_pass_usd": perPass}, "routing": routing(arm, rows, sampleByID, assessByTrial, primary)}
	}
	for _, sample := range samples {
		sid := domain.Str(sample["id"])
		matrixSamples = append(matrixSamples, M{"id": sid, "key": sample["key"], "meta": sample["meta"]})
		values := []any{}
		for _, key := range armKeys {
			values = append(values, stats.Mean(perSample[key][sid]))
		}
		matrix = append(matrix, values)
	}
	baseline := ""
	if len(armKeys) > 0 {
		baseline = armKeys[0]
	}
	ref := domain.Str(acc["baseline_arm"])
	for _, arm := range arms {
		if ref != "" && (arm["key"] == ref || arm["label"] == ref || domain.Str(arm["provider"])+"/"+domain.Str(arm["model"]) == ref) {
			baseline = domain.Str(arm["key"])
		}
	}
	comparisons := []M{}
	for _, key := range armKeys {
		if key == baseline {
			continue
		}
		a, b := []float64{}, []float64{}
		wins, losses, ties := 0, 0, 0
		for _, sample := range samples {
			sid := domain.Str(sample["id"])
			av, bv := stats.Mean(perSample[baseline][sid]), stats.Mean(perSample[key][sid])
			if av == nil || bv == nil {
				continue
			}
			af, bf := domain.Num(av), domain.Num(bv)
			a = append(a, af)
			b = append(b, bf)
			if bf > af {
				wins++
			} else if bf < af {
				losses++
			} else {
				ties++
			}
		}
		var mcnemar any
		method := "paired sample bootstrap of repetition-averaged outcomes"
		if domain.Int(domain.Obj(p["protocol"])["repetitions"]) == 1 {
			mcnemar = stats.McNemar(wins, losses)
			method = "paired sample bootstrap + exact McNemar on discordant binary outcomes"
		}
		var effect any
		if len(a) > 0 {
			effect = stats.CohenH(domain.Num(stats.Mean(a)), domain.Num(stats.Mean(b)))
		}
		comparisons = append(comparisons, M{"baseline": baseline, "candidate": key, "paired_samples": len(a), "missing_pairs": len(samples) - len(a), "wins": wins, "losses": losses, "ties": ties, "diff": stats.Paired(a, b, seed), "mcnemar_p": mcnemar, "test": method, "effect_size_h": effect})
	}
	// Several candidates against one baseline form a family of tests: report
	// family-wise (Holm) and false-discovery-rate (Benjamini–Hochberg) adjusted p-values.
	raw := []any{}
	for _, cmp := range comparisons {
		raw = append(raw, cmp["mcnemar_p"])
	}
	holm, bh := stats.AdjustHolm(raw), stats.AdjustBH(raw)
	for i, cmp := range comparisons {
		cmp["mcnemar_p_holm"], cmp["mcnemar_p_bh"] = holm[i], bh[i]
	}
	gateArms := M{}
	executionComplete := c["status"] == "completed"
	for _, key := range armKeys {
		s := domain.Obj(summaries[key])
		prim := domain.Obj(s["primary"])
		iv := domain.Obj(prim["pass_rate"])
		checks := []M{}
		verdict := "pass"
		add := func(name, result, detail string) {
			checks = append(checks, M{"check": name, "result": result, "detail": detail})
			if result == "fail" {
				verdict = "fail"
			} else if result == "insufficient_evidence" && verdict != "fail" {
				verdict = "insufficient_evidence"
			}
		}
		if !executionComplete {
			add("execution", "insufficient_evidence", "Campaign execution is incomplete")
		}
		if domain.Int(prim["assessed"]) < domain.Int(acc["min_assessed_per_arm"]) {
			add("minimum_evidence", "insufficient_evidence", "Below minimum assessed trials")
		}
		if acc["min_pass_rate"] != nil {
			res := "insufficient_evidence"
			if iv["low"] != nil {
				if domain.Num(iv["low"]) >= domain.Num(acc["min_pass_rate"]) {
					res = "pass"
				} else if domain.Num(iv["high"]) < domain.Num(acc["min_pass_rate"]) {
					res = "fail"
				}
			}
			add("min_pass_rate", res, "95% Wilson interval compared with threshold")
		}
		if s["error_rate"] != nil {
			res := "pass"
			if domain.Num(s["error_rate"]) > domain.Num(acc["max_error_rate"]) {
				res = "fail"
			}
			add("max_error_rate", res, "Observed infrastructure errors per applicable trial")
		}
		if acc["max_p95_latency_ms"] != nil {
			v := domain.Obj(s["latency_ms"])["p95"]
			res := "insufficient_evidence"
			if v != nil {
				res = "pass"
				if domain.Num(v) > domain.Num(acc["max_p95_latency_ms"]) {
					res = "fail"
				}
			}
			add("max_p95_latency_ms", res, "Observed p95 latency")
		}
		if acc["max_cost_per_pass_usd"] != nil {
			v := domain.Obj(s["cost"])["per_pass_usd"]
			res := "insufficient_evidence"
			if v != nil {
				res = "pass"
				if domain.Num(v) > domain.Num(acc["max_cost_per_pass_usd"]) {
					res = "fail"
				}
			}
			add("max_cost_per_pass_usd", res, "Requires complete cost evidence")
		}
		if key != baseline && acc["non_inferiority_margin"] != nil {
			res := "insufficient_evidence"
			for _, cmp := range comparisons {
				if cmp["candidate"] == key {
					iv := domain.Obj(cmp["diff"])
					if iv["low"] != nil {
						if domain.Num(iv["low"]) >= -domain.Num(acc["non_inferiority_margin"]) {
							res = "pass"
						} else if domain.Num(iv["high"]) < -domain.Num(acc["non_inferiority_margin"]) {
							res = "fail"
						}
					}
				}
			}
			add("non_inferiority", res, "Paired sample difference with confidence interval")
		}
		gateArms[key] = M{"verdict": verdict, "checks": checks, "is_baseline": key == baseline}
	}
	slices := []M{}
	sliceKeys := map[string]bool{}
	for _, s := range samples {
		for k, v := range domain.Obj(s["meta"]) {
			switch v.(type) {
			case string, float64, bool:
				sliceKeys[k] = true
			}
		}
	}
	for key := range sliceKeys {
		byValue := map[string]map[string][]float64{}
		for _, s := range samples {
			val := domain.Str(domain.Obj(s["meta"])[key])
			if val == "" {
				continue
			}
			if byValue[val] == nil {
				byValue[val] = map[string][]float64{}
			}
			for _, a := range armKeys {
				byValue[val][a] = append(byValue[val][a], perSample[a][domain.Str(s["id"])]...)
			}
		}
		if len(byValue) < 2 || len(byValue) > 40 {
			continue
		}
		rows := []M{}
		vals := []string{}
		for val := range byValue {
			vals = append(vals, val)
		}
		sort.Strings(vals)
		for _, val := range vals {
			values := M{}
			for _, a := range armKeys {
				xs := byValue[val][a]
				passes := 0
				for _, x := range xs {
					if x == 1 {
						passes++
					}
				}
				values[a] = stats.Wilson(passes, len(xs))
			}
			rows = append(rows, M{"value": val, "arms": values})
		}
		slices = append(slices, M{"key": key, "exploratory": true, "rows": rows})
	}
	sort.Slice(slices, func(i, j int) bool { return domain.Str(slices[i]["key"]) < domain.Str(slices[j]["key"]) })
	ranking := append([]string{}, armKeys...)
	sort.SliceStable(ranking, func(i, j int) bool {
		return domain.Num(domain.Obj(domain.Obj(domain.Obj(summaries[ranking[i]])["primary"])["pass_rate"])["estimate"]) > domain.Num(domain.Obj(domain.Obj(domain.Obj(summaries[ranking[j]])["primary"])["pass_rate"])["estimate"])
	})
	var best any
	if len(ranking) > 0 {
		best = ranking[0]
	}
	tied := []string{}
	if best != nil {
		bestIV := domain.Obj(domain.Obj(domain.Obj(summaries[domain.Str(best)])["primary"])["pass_rate"])
		for _, k := range ranking {
			iv := domain.Obj(domain.Obj(domain.Obj(summaries[k])["primary"])["pass_rate"])
			if iv["high"] != nil && bestIV["low"] != nil && domain.Num(iv["high"]) >= domain.Num(bestIV["low"]) {
				tied = append(tied, k)
			}
		}
	}
	costPts, latPts := []M{}, []M{}
	for _, key := range armKeys {
		s := domain.Obj(summaries[key])
		quality := domain.Obj(domain.Obj(s["primary"])["pass_rate"])["estimate"]
		if quality == nil {
			continue
		}
		if cost := domain.Obj(s["cost"])["per_trial_usd"]; cost != nil {
			costPts = append(costPts, M{"id": key, "x": cost, "y": quality})
		}
		if lat := domain.Obj(s["latency_ms"])["p50"]; lat != nil {
			latPts = append(latPts, M{"id": key, "x": lat, "y": quality})
		}
	}
	result := M{"campaign_id": cid, "status": c["status"], "primary_scorer": primary, "scorers": scorerSpecs, "scoring_revision": revision, "dataset": M{"id": ds["id"], "name": ds["name"], "version": ds["version"], "digest": ds["digest"], "modality": ds["modality"]}, "labels": c["labels"], "arms": arms, "baseline": baseline, "summaries": summaries, "comparisons": comparisons, "matrix": M{"samples": matrixSamples, "arms": armKeys, "values": matrix}, "slices": slices, "pareto": M{"cost": stats.Pareto(costPts), "latency": stats.Pareto(latPts)}, "gate": M{"arms": gateArms, "execution_status": c["status"], "execution_complete": executionComplete, "note": nil, "policy": acc}, "suspect_labels": suspectLabels(trials, sampleByID), "ranking": ranking, "best": best, "statistically_tied_with_best": tied, "methods": M{"pass_rate": "95% Wilson interval", "task_balanced": "sample-level bootstrap, 2000 resamples", "comparison": "paired sample bootstrap; exact McNemar for discordant outcomes", "pass_k": "unbiased estimator; complete repetitions only", "multiplicity": "Holm-Bonferroni (family-wise) and Benjamini-Hochberg (false discovery rate) adjusted McNemar p-values", "effect_size": "Cohen's h between mean pass rates"}, "snapshot_done": c["done"], "snapshot_status": c["status"]}
	if err = e.DB.Put(ctx, "results", cid, result); err != nil {
		return nil, err
	}
	return M{"result_kind": "results", "result_id": cid}, nil
}
func routing(arm M, trials []M, samples map[string]M, assess map[string]map[string]M, primary string) M {
	routes := map[string][]M{}
	for _, t := range trials {
		if t["resolved_model"] != nil {
			m := domain.Str(t["resolved_model"])
			routes[m] = append(routes[m], t)
		}
	}
	total := 0
	for _, r := range routes {
		total += len(r)
	}
	list := []M{}
	isRouter := stringsContains(domain.Str(arm["model"]), "router")
	sampleRoutes := map[string]map[string]bool{}
	sampleCalls := map[string]int{}
	for model, rows := range routes {
		if model != arm["model"] {
			isRouter = true
		}
		passes, n := 0, 0
		cost, lat := 0., []float64{}
		known := true
		providers := []string{}
		for _, t := range rows {
			a := assess[domain.Str(t["id"])][primary]
			if a["verdict"] == "pass" {
				passes++
				n++
			} else if a["verdict"] == "fail" {
				n++
			}
			if t["cost_usd"] == nil {
				known = false
			} else {
				cost += domain.Num(t["cost_usd"])
			}
			if t["latency_ms"] != nil {
				lat = append(lat, domain.Num(t["latency_ms"]))
			}
			prov := domain.Str(t["upstream_provider"])
			if prov != "" && !domain.Has(providers, prov) {
				providers = append(providers, prov)
			}
			sid := domain.Str(t["sample_id"])
			if sampleRoutes[sid] == nil {
				sampleRoutes[sid] = map[string]bool{}
			}
			sampleRoutes[sid][model] = true
			sampleCalls[sid]++
		}
		var c, per any
		if known {
			c = cost
			per = cost / float64(len(rows))
		}
		list = append(list, M{"model": model, "n": len(rows), "share": float64(len(rows)) / float64(domain.Max(1, total)), "pass_rate": stats.Wilson(passes, n), "cost_usd": c, "cost_per_call_usd": per, "latency_p50_ms": stats.Percentile(lat, 0.5), "providers": providers})
	}
	sort.Slice(list, func(i, j int) bool { return domain.Int(list[i]["n"]) > domain.Int(list[j]["n"]) })
	multi, stable := 0, 0
	for sid, n := range sampleCalls {
		if n > 1 {
			multi++
			if len(sampleRoutes[sid]) == 1 {
				stable++
			}
		}
	}
	var stability any
	if multi > 0 {
		stability = float64(stable) / float64(multi)
	}
	slices := []M{}
	keys := map[string]bool{}
	for _, s := range samples {
		for k := range domain.Obj(s["meta"]) {
			keys[k] = true
		}
	}
	for key := range keys {
		cells := map[string]M{}
		for model, rows := range routes {
			for _, t := range rows {
				value := domain.Str(domain.Obj(samples[domain.Str(t["sample_id"])]["meta"])[key])
				if value == "" {
					continue
				}
				if cells[value] == nil {
					cells[value] = M{}
				}
				cell := domain.Obj(cells[value][model])
				cell["n"] = domain.Int(cell["n"]) + 1
				cell["passes"] = domain.Int(cell["passes"])
				cell["assessed"] = domain.Int(cell["assessed"])
				a := assess[domain.Str(t["id"])][primary]
				if a["verdict"] == "pass" || a["verdict"] == "fail" {
					cell["assessed"] = domain.Int(cell["assessed"]) + 1
					if a["verdict"] == "pass" {
						cell["passes"] = domain.Int(cell["passes"]) + 1
					}
				}
				cells[value][model] = cell
			}
		}
		if len(cells) > 1 && len(cells) < 40 {
			rows := []M{}
			for val, routes := range cells {
				rows = append(rows, M{"value": val, "routes": routes})
			}
			sort.Slice(rows, func(i, j int) bool { return domain.Str(rows[i]["value"]) < domain.Str(rows[j]["value"]) })
			slices = append(slices, M{"key": key, "rows": rows})
		}
	}
	return M{"is_router": isRouter, "routes": list, "distinct_models": len(routes), "calls": total, "stability": stability, "stability_samples": multi, "by_slice": slices}
}
func stringsContains(a, b string) bool {
	for i := 0; i+len(b) <= len(a); i++ {
		if a[i:i+len(b)] == b {
			return true
		}
	}
	return false
}
func suspectLabels(trials []M, samples map[string]M) []M {
	by := map[string][]M{}
	for _, t := range trials {
		if t["status"] == "completed" {
			sid := domain.Str(t["sample_id"])
			by[sid] = append(by[sid], t)
		}
	}
	out := []M{}
	for sid, rows := range by {
		answers := map[string]int{}
		arms := map[string]bool{}
		for _, t := range rows {
			answers[domain.Str(t["output_text"])]++
			arms[domain.Str(t["arm_key"])] = true
		}
		if len(arms) < 2 {
			continue
		}
		best := ""
		count := 0
		for ans, n := range answers {
			if n > count {
				best, count = ans, n
			}
		}
		if float64(count)/float64(len(rows)) >= 0.8 && best != domain.Str(samples[sid]["expected"]) {
			out = append(out, M{"sample_id": sid, "key": samples[sid]["key"], "arms": len(arms), "trials": len(rows), "consensus_answer": best, "agreement": float64(count) / float64(len(rows)), "expected": samples[sid]["expected"]})
		}
	}
	return out
}
