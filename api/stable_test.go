package api

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "accept additive changes to the stable contract")

// lockFile records the stable REST surface of v1. Breaking it is a test
// failure; additive changes need "go test ./api -run TestStableContract -update"
// so that they show up for review.
const lockFile = "testdata/stable-v1.json"

type stableOp struct {
	OperationID string   `json:"operation_id"`
	Request     []string `json:"request"`
	Response    []string `json:"response"`
}

func TestStableContractHasNoBreakingChanges(t *testing.T) {
	current := stableSurface(t, spec(t))
	raw, err := os.ReadFile(lockFile)
	if err != nil && !*update {
		t.Fatalf("%v; create it with -update", err)
	}
	locked := map[string]stableOp{}
	if raw != nil {
		if err = json.Unmarshal(raw, &locked); err != nil {
			t.Fatal(err)
		}
	}
	breaks := breakingChanges(locked, current)
	if len(breaks) > 0 {
		t.Fatalf("breaking changes to the stable v1 contract (these need /api/v2, see api/README.md):\n  %s", strings.Join(breaks, "\n  "))
	}
	out, _ := json.MarshalIndent(current, "", "  ")
	out = append(out, '\n')
	if string(out) == string(raw) {
		return
	}
	if *update {
		if err = os.MkdirAll("testdata", 0o755); err == nil {
			err = os.WriteFile(lockFile, out, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("the stable contract grew (additive, allowed); record it with: go test ./api -run TestStableContract -update")
}

// breakingChanges lists what in current would break a client of locked.
func breakingChanges(locked, current map[string]stableOp) []string {
	breaks := []string{}
	for key, old := range locked {
		now, ok := current[key]
		if !ok {
			breaks = append(breaks, key+": stable operation removed or no longer stable")
			continue
		}
		if now.OperationID != old.OperationID {
			breaks = append(breaks, fmt.Sprintf("%s: operationId %s renamed to %s", key, old.OperationID, now.OperationID))
		}
		// Clients rely on every response field keeping its name, type and presence.
		for _, fact := range old.Response {
			if !slices.Contains(now.Response, fact) {
				breaks = append(breaks, key+": response changed: "+fact)
			}
		}
		// Requests may gain optional inputs, never new required ones, and an
		// existing input keeps its type.
		types := map[string]string{}
		for _, fact := range old.Request {
			path, typ, _ := strings.Cut(fact, " ")
			types[path] = typ
		}
		for _, fact := range now.Request {
			path, typ, _ := strings.Cut(fact, " ")
			was, existed := types[path]
			switch {
			case existed && strings.TrimSuffix(was, " required") != strings.TrimSuffix(typ, " required"):
				breaks = append(breaks, key+": request type changed: "+fact)
			case strings.HasSuffix(typ, "required") && (!existed || !strings.HasSuffix(was, "required")):
				breaks = append(breaks, key+": request input became required: "+fact)
			}
		}
	}
	sort.Strings(breaks)
	return breaks
}

// stableSurface flattens every stable operation into comparable facts such as
// "200 $.items[].id string required".
func stableSurface(t *testing.T, doc map[string]any) map[string]stableOp {
	out := map[string]stableOp{}
	for path, item := range doc["paths"].(map[string]any) {
		item := item.(map[string]any)
		for _, method := range methods {
			op, ok := item[method].(map[string]any)
			if !ok || op["x-stability-level"] != "stable" {
				continue
			}
			s := stableOp{OperationID: op["operationId"].(string), Request: []string{}, Response: []string{}}
			for _, list := range [][]any{asList(item["parameters"]), asList(op["parameters"])} {
				for _, p := range list {
					p := deref(t, doc, p).(map[string]any)
					req := ""
					if p["required"] == true {
						req = " required"
					}
					s.Request = append(s.Request, fmt.Sprintf("%s:%s %s%s", p["in"], p["name"], typeOf(t, doc, p["schema"]), req))
				}
			}
			if body, ok := op["requestBody"].(map[string]any); ok {
				body = deref(t, doc, body).(map[string]any)
				for media, c := range body["content"].(map[string]any) {
					s.Request = append(s.Request, shape(t, doc, "body:"+media+":$", c.(map[string]any)["schema"], true, 0)...)
				}
			}
			for status, r := range op["responses"].(map[string]any) {
				if status == "default" {
					continue
				}
				r := deref(t, doc, r).(map[string]any)
				content, _ := r["content"].(map[string]any)
				for media, c := range content {
					s.Response = append(s.Response, shape(t, doc, status+":"+media+":$", c.(map[string]any)["schema"], true, 0)...)
				}
			}
			sort.Strings(s.Request)
			sort.Strings(s.Response)
			out[strings.ToUpper(method)+" "+path] = s
		}
	}
	return out
}

func deref(t *testing.T, doc map[string]any, v any) any {
	for {
		m, ok := v.(map[string]any)
		if !ok || m["$ref"] == nil {
			return v
		}
		var err error
		if v, err = resolve(doc, m["$ref"].(string)); err != nil {
			t.Fatal(err)
		}
	}
}

// merged resolves $ref and folds allOf into one schema.
func merged(t *testing.T, doc map[string]any, v any) map[string]any {
	s, _ := deref(t, doc, v).(map[string]any)
	if s == nil {
		return map[string]any{}
	}
	all, ok := s["allOf"].([]any)
	if !ok {
		return s
	}
	out := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
	own := map[string]any{"properties": s["properties"], "required": s["required"]}
	for _, part := range append(slices.Clone(all), own) {
		p := merged(t, doc, part)
		for k, v := range asMap(p["properties"]) {
			out["properties"].(map[string]any)[k] = v
		}
		out["required"] = append(out["required"].([]any), asList(p["required"])...)
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func typeOf(t *testing.T, doc map[string]any, v any) string {
	s := merged(t, doc, v)
	switch {
	case s["const"] != nil:
		return fmt.Sprintf("const:%v", s["const"])
	case s["enum"] != nil:
		return fmt.Sprintf("enum:%v", s["enum"])
	}
	switch typ := s["type"].(type) {
	case string:
		return typ
	case []any:
		parts := []string{}
		for _, p := range typ {
			parts = append(parts, p.(string))
		}
		sort.Strings(parts)
		return strings.Join(parts, "|")
	}
	if s["properties"] != nil {
		return "object"
	}
	return "any"
}

func shape(t *testing.T, doc map[string]any, at string, v any, required bool, depth int) []string {
	req := ""
	if required {
		req = " required"
	}
	facts := []string{at + " " + typeOf(t, doc, v) + req}
	if depth > 6 {
		return facts
	}
	s := merged(t, doc, v)
	need := map[string]bool{}
	for _, r := range asList(s["required"]) {
		need[r.(string)] = true
	}
	for name, p := range asMap(s["properties"]) {
		facts = append(facts, shape(t, doc, at+"."+name, p, need[name], depth+1)...)
	}
	if items, ok := s["items"]; ok && len(asMap(items)) > 0 {
		facts = append(facts, shape(t, doc, at+"[]", items, true, depth+1)...)
	}
	if extra, ok := s["additionalProperties"].(map[string]any); ok {
		facts = append(facts, shape(t, doc, at+"{}", extra, true, depth+1)...)
	}
	return facts
}

// The detector must flag each kind of break, or the lock proves nothing.
func TestBreakingChangesAreDetected(t *testing.T) {
	base := stableSurface(t, spec(t))
	clone := func() map[string]stableOp {
		out := map[string]stableOp{}
		for k, v := range base {
			v.Request, v.Response = slices.Clone(v.Request), slices.Clone(v.Response)
			out[k] = v
		}
		return out
	}
	if b := breakingChanges(base, base); len(b) != 0 {
		t.Fatal(b)
	}
	const key = "GET /agent-lab/runs/{id}/events"
	mutations := map[string]func(map[string]stableOp){
		"operation removed":   func(m map[string]stableOp) { delete(m, key) },
		"operationId renamed": func(m map[string]stableOp) { op := m[key]; op.OperationID = "events"; m[key] = op },
		"response field removed": func(m map[string]stableOp) {
			op := m[key]
			op.Response = slices.DeleteFunc(op.Response, func(f string) bool { return strings.Contains(f, "$.has_more ") })
			m[key] = op
		},
		"response field optional": func(m map[string]stableOp) {
			op := m[key]
			for i, f := range op.Response {
				op.Response[i] = strings.Replace(f, "$.has_more boolean required", "$.has_more boolean", 1)
			}
			m[key] = op
		},
		"request input required": func(m map[string]stableOp) {
			op := m[key]
			op.Request = append(op.Request, "query:cursor string required")
			m[key] = op
		},
		"request type changed": func(m map[string]stableOp) {
			op := m[key]
			for i, f := range op.Request {
				op.Request[i] = strings.Replace(f, "query:limit integer", "query:limit string", 1)
			}
			m[key] = op
		},
	}
	for name, mutate := range mutations {
		next := clone()
		mutate(next)
		if len(breakingChanges(base, next)) == 0 {
			t.Errorf("%s was not detected", name)
		}
	}
	additive := clone()
	op := additive[key]
	op.Response = append(op.Response, "200:application/json:$.next_after integer")
	op.Request = append(op.Request, "query:kind string")
	additive[key] = op
	if b := breakingChanges(base, additive); len(b) != 0 {
		t.Fatal("additive change flagged:", b)
	}
}
