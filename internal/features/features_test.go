package features

import (
	"slices"
	"strings"
	"testing"
)

func TestZeroSetEnablesEverythingCompiled(t *testing.T) {
	var s Set
	for _, i := range All {
		if s.On(i.Name) != i.Compiled {
			t.Fatal(i.Name)
		}
	}
	if s.On("unknown") {
		t.Fatal("unknown feature enabled")
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		spec string
		want []Feature
	}{
		{"", Set{}.Enabled()},
		{"all", Set{}.Enabled()},
		{"none", []Feature{}},
		{"suites", []Feature{Traces, Workbench, Suites}},
		{"tracing", []Feature{Traces, OTel, Monitor}},
		{"traces, prompts", []Feature{Traces, Prompts}},
		{"experiments", []Feature{Datasets, Experiments}},
		{"eval,-perf", []Feature{Playground, Datasets, Experiments}},
	}
	for _, c := range cases {
		s, _, err := Parse(c.spec)
		if err != nil {
			t.Fatal(c.spec, err)
		}
		// Presets skip modules left out by build tags (arl_nootel drops otel).
		want := slices.DeleteFunc(slices.Clone(c.want), func(f Feature) bool { return !Compiled(f) })
		if got := s.Enabled(); !slices.Equal(got, want) {
			t.Errorf("%q: got %v want %v", c.spec, got, want)
		}
	}
}

func TestRemovalCascadesToDependents(t *testing.T) {
	s, notes, err := Parse("-traces")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Feature{Traces, OTel, Monitor, Quality, Workbench, Suites} {
		if s.On(f) {
			t.Fatal(f, "still on")
		}
	}
	if !s.On(Experiments) || !s.On(Prompts) {
		t.Fatal("unrelated modules disabled")
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, ";"), "suites disabled") {
		t.Fatal(notes)
	}
}

func TestUnknownFeatureIsAnError(t *testing.T) {
	if _, _, err := Parse("traces,telemetry"); err == nil || !strings.Contains(err.Error(), "telemetry") {
		t.Fatal(err)
	}
}

func TestEveryJobKindHasAnOwner(t *testing.T) {
	for kind, want := range map[string]Feature{"trial.run": Experiments, "campaign.report": Experiments, "experiment.create": Experiments, "rescore.trial": Experiments, "trace.evaluate": Quality, "trace.summarize": Traces, "catalog.refresh": "", "perf.run": Perf, "workbench.llm": Workbench} {
		if got := ForJob(kind); got != want {
			t.Errorf("%s: %q", kind, got)
		}
	}
}
