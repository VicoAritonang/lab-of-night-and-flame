package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.vicoworks.com/agent-reliability-lab/internal/config"
	"go.vicoworks.com/agent-reliability-lab/internal/features"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
)

// Disabled modules answer before touching the database, so no PostgreSQL is needed.
func TestDisabledModulesAnswerWithReason(t *testing.T) {
	set, _, err := features.Parse("tracing")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), config.Config{MaxHTTP: 4, MaxWebSockets: 4, Features: set}, &store.Store{}, "")
	if err != nil {
		t.Fatal(err)
	}
	for path, feature := range map[string]string{"/api/perf": "perf", "/api/experiments": "experiments", "/api/datasets": "datasets", "/api/prompts": "prompts", "/api/workbench/scenarios": "workbench", "/api/workbench/suites": "suites", "/api/alerts": "quality"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var body struct{ Detail, Feature, Code string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 404 || body.Feature != feature || body.Code != "feature_disabled" || !strings.Contains(body.Detail, "ARL_FEATURES") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/features", nil))
	var got struct {
		Enabled []string
		Modules []map[string]any
	}
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	want := "traces,monitor"
	if features.Compiled(features.OTel) {
		want = "traces,otel,monitor"
	}
	if strings.Join(got.Enabled, ",") != want || len(got.Modules) != len(features.All) {
		t.Fatal(got)
	}
}
