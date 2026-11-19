// Package features lists the optional modules of the lab and resolves which of
// them a process runs (ARL_FEATURES).
//
// Everything compiled in is enabled by default. A disabled module registers no
// working routes (they answer 404 with a hint), accepts no jobs, starts no
// background work and is hidden in the UI. The core (health, auth, projects,
// jobs, providers/catalog, artifacts, the UI shell) is always on.
//
// Heavy dependencies are removed at build time instead, with Go build tags:
// arl_nootel (OTLP/protobuf), arl_nosqs (AWS SDK), arl_noui (embedded UI) and
// arl_nogrpc (the gRPC/Connect API; REST and WebSocket remain).
package features

import (
	"fmt"
	"sort"
	"strings"
)

type Feature string

const (
	Traces      Feature = "traces"
	OTel        Feature = "otel"
	Monitor     Feature = "monitor"
	Quality     Feature = "quality"
	Workbench   Feature = "workbench"
	Suites      Feature = "suites"
	Prompts     Feature = "prompts"
	Playground  Feature = "playground"
	Datasets    Feature = "datasets"
	Experiments Feature = "experiments"
	Perf        Feature = "perf"
)

type Info struct {
	Name     Feature   `json:"name"`
	Title    string    `json:"title"`
	Requires []Feature `json:"requires"`
	// Compiled is false when the binary was built with the module's exclusion tag.
	Compiled bool   `json:"compiled"`
	Tag      string `json:"build_tag,omitempty"`
}

// All is every module in dependency order.
var All = []Info{
	{Traces, "Agent lab: live arl/1 traces over WebSocket", nil, true, ""},
	{OTel, "OTLP/HTTP trace ingest", []Feature{Traces}, otelCompiled, "arl_nootel"},
	{Monitor, "Monitor dashboards over traces", []Feature{Traces}, true, ""},
	{Quality, "Feedback, annotation queues, online evaluators and alerts", []Feature{Traces}, true, ""},
	{Workbench, "Scenario replay and LLM steps", []Feature{Traces}, true, ""},
	{Suites, "Workbench regression suites (arl suite in CI)", []Feature{Workbench}, true, ""},
	{Prompts, "Prompt registry", nil, true, ""},
	{Playground, "Model playground", nil, true, ""},
	{Datasets, "Datasets and the built-in benchmark fixtures", nil, true, ""},
	{Experiments, "Experiments, campaigns, reports and Router lab", []Feature{Datasets}, true, ""},
	{Perf, "Load tests of model endpoints", nil, true, ""},
}

// Presets are shorthands usable in ARL_FEATURES next to module names.
var Presets = map[string][]Feature{
	"tracing": {Traces, OTel, Monitor},
	"agent":   {Traces, OTel, Monitor, Quality, Workbench, Suites, Prompts, Playground},
	"eval":    {Datasets, Experiments, Playground, Perf},
}

func lookup(f Feature) (Info, bool) {
	for _, i := range All {
		if i.Name == f {
			return i, true
		}
	}
	return Info{}, false
}

// Compiled reports whether f is part of this binary.
func Compiled(f Feature) bool {
	i, ok := lookup(f)
	return ok && i.Compiled
}

// Set is the resolved selection. The zero value enables every compiled module.
type Set struct{ off map[Feature]bool }

func (s Set) On(f Feature) bool { return Compiled(f) && !s.off[f] }

// Enabled lists the enabled modules in dependency order.
func (s Set) Enabled() []Feature {
	out := []Feature{}
	for _, i := range All {
		if s.On(i.Name) {
			out = append(out, i.Name)
		}
	}
	return out
}

// Describe reports every module with its state, for /api/features.
func (s Set) Describe() []map[string]any {
	out := []map[string]any{}
	for _, i := range All {
		req := i.Requires
		if req == nil {
			req = []Feature{}
		}
		out = append(out, map[string]any{"name": i.Name, "title": i.Title, "requires": req, "compiled": i.Compiled, "build_tag": i.Tag, "enabled": s.On(i.Name)})
	}
	return out
}

// Reason explains why f is unavailable, for API errors and logs.
func (s Set) Reason(f Feature) string {
	if i, ok := lookup(f); ok && !i.Compiled {
		return fmt.Sprintf("feature %q is not compiled into this binary (built with -tags %s)", f, i.Tag)
	}
	return fmt.Sprintf("feature %q is disabled on this lab; add it to ARL_FEATURES", f)
}

// Parse resolves an ARL_FEATURES value. Tokens are separated by commas or
// spaces and applied left to right:
//
//	all | none          every compiled module | only the core
//	traces, perf, ...   enable a module and the modules it requires
//	tracing, agent, ... enable a preset
//	-perf, -eval        disable a module/preset and the modules that require it
//
// A list that starts with a removal starts from "all", so "-perf" means
// everything except perf. Empty means "all". The notes describe implicit
// changes (requirements pulled in, dependents dropped) for the startup log.
func Parse(spec string) (Set, []string, error) {
	tokens := strings.FieldsFunc(strings.ToLower(spec), func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\t' || r == '\n' })
	on := map[Feature]bool{}
	if len(tokens) == 0 || strings.HasPrefix(tokens[0], "-") {
		tokens = append([]string{"all"}, tokens...)
	}
	notes := []string{}
	var enable, disable func(f Feature, why Feature)
	enable = func(f Feature, why Feature) {
		if on[f] {
			return
		}
		i, _ := lookup(f)
		on[f] = true
		if why != "" {
			notes = append(notes, fmt.Sprintf("%s enabled because %s requires it", f, why))
		}
		for _, r := range i.Requires {
			enable(r, f)
		}
	}
	disable = func(f Feature, why Feature) {
		if !on[f] {
			return
		}
		delete(on, f)
		if why != "" {
			notes = append(notes, fmt.Sprintf("%s disabled because it requires %s", f, why))
		}
		for _, i := range All {
			for _, r := range i.Requires {
				if r == f {
					disable(i.Name, f)
				}
			}
		}
	}
	for _, t := range tokens {
		remove := strings.HasPrefix(t, "-")
		name := strings.TrimPrefix(strings.TrimPrefix(t, "-"), "+")
		var group []Feature
		switch {
		case name == "all":
			for _, i := range All {
				if i.Compiled || remove {
					group = append(group, i.Name)
				}
			}
		case name == "none" || name == "core":
			if !remove {
				on = map[Feature]bool{}
			}
			continue
		case Presets[name] != nil:
			group = Presets[name]
		default:
			i, ok := lookup(Feature(name))
			if !ok {
				return Set{}, nil, fmt.Errorf("ARL_FEATURES: unknown feature %q (known: %s)", name, Known())
			}
			if !i.Compiled && !remove {
				return Set{}, nil, fmt.Errorf("ARL_FEATURES: %s", Set{}.Reason(i.Name))
			}
			group = []Feature{i.Name}
		}
		for _, f := range group {
			if remove {
				disable(f, "")
			} else if i, _ := lookup(f); i.Compiled {
				enable(f, "")
			}
		}
	}
	for _, i := range All {
		for _, r := range i.Requires {
			if on[i.Name] {
				if ri, _ := lookup(r); !ri.Compiled {
					return Set{}, nil, fmt.Errorf("ARL_FEATURES: %s requires %s, which is not compiled into this binary", i.Name, r)
				}
			}
		}
	}
	off := map[Feature]bool{}
	for _, i := range All {
		if !on[i.Name] {
			off[i.Name] = true
		}
	}
	return Set{off}, notes, nil
}

// Known lists module and preset names for error messages.
func Known() string {
	names := []string{}
	for _, i := range All {
		names = append(names, string(i.Name))
	}
	presets := []string{}
	for p := range Presets {
		presets = append(presets, p)
	}
	sort.Strings(presets)
	return strings.Join(names, ", ") + "; presets: all, none, " + strings.Join(presets, ", ")
}

// ForJob maps a job kind to the module that owns it. Core jobs return "".
func ForJob(kind string) Feature {
	switch kind {
	case "catalog.refresh", "artifact.validate":
		return ""
	case "trace.summarize", "trace.export":
		return Traces
	case "trace.evaluate", "alerts.check":
		return Quality
	case "workbench.llm":
		return Workbench
	case "perf.run":
		return Perf
	case "playground.target":
		return Playground
	case "datasets.seed", "dataset.import", "dataset.delete", "predictions.import":
		return Datasets
	}
	if strings.HasPrefix(kind, "experiment.") || strings.HasPrefix(kind, "campaign.") || kind == "trial.run" || kind == "rescore.trial" {
		return Experiments
	}
	return ""
}
