package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.vicoworks.com/agent-reliability-lab/api/internal/openapi"
)

func spec(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(OpenAPIJSON, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestJSONIsGeneratedFromYAML(t *testing.T) {
	want, err := openapi.JSON(OpenAPIYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, OpenAPIJSON) {
		t.Fatal("api/openapi.json is out of date; run: go generate ./api")
	}
}

var methods = []string{"get", "put", "post", "delete", "patch", "head", "options"}
var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// Every operation is identified, tagged, classified and documents its errors,
// and every path parameter is declared, so generated clients are usable.
func TestOperationsAreComplete(t *testing.T) {
	doc := spec(t)
	tags := map[string]bool{}
	for _, tag := range doc["tags"].([]any) {
		tags[tag.(map[string]any)["name"].(string)] = true
	}
	params := doc["components"].(map[string]any)["parameters"].(map[string]any)
	ids := map[string]string{}
	for path, item := range doc["paths"].(map[string]any) {
		item := item.(map[string]any)
		for _, method := range methods {
			op, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			where := strings.ToUpper(method) + " " + path
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s: no operationId", where)
			} else if other, dup := ids[id]; dup {
				t.Errorf("%s: operationId %s also used by %s", where, id, other)
			}
			ids[id] = where
			for _, tag := range op["tags"].([]any) {
				if !tags[tag.(string)] {
					t.Errorf("%s: undeclared tag %v", where, tag)
				}
			}
			if s := op["x-stability-level"]; s != "stable" && s != "beta" {
				t.Errorf("%s: x-stability-level must be stable or beta, got %v", where, s)
			}
			responses := op["responses"].(map[string]any)
			if responses["200"] == nil {
				t.Errorf("%s: no 200 response", where)
			}
			if responses["default"] == nil && !strings.HasPrefix(path, "/openapi") && path != "/docs" {
				t.Errorf("%s: errors are not documented (default response)", where)
			}
			declared := map[string]bool{}
			for _, list := range [][]any{asList(item["parameters"]), asList(op["parameters"])} {
				for _, p := range list {
					p := p.(map[string]any)
					if ref, ok := p["$ref"].(string); ok {
						p = params[strings.TrimPrefix(ref, "#/components/parameters/")].(map[string]any)
					}
					if p["in"] == "path" {
						declared[p["name"].(string)] = true
					}
				}
			}
			for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
				if !declared[m[1]] {
					t.Errorf("%s: path parameter %s is not declared", where, m[1])
				}
			}
		}
	}
	if len(ids) < 100 {
		t.Fatalf("only %d operations documented", len(ids))
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// Every $ref points at something that exists.
func TestReferencesResolve(t *testing.T) {
	doc := spec(t)
	var walk func(v any, at string)
	walk = func(v any, at string) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				if _, err := resolve(doc, ref); err != nil {
					t.Errorf("%s: %v", at, err)
				}
			}
			for k, c := range v {
				walk(c, at+"/"+k)
			}
		case []any:
			for _, c := range v {
				walk(c, at)
			}
		}
	}
	walk(doc, "#")
}

// resolve follows a local JSON pointer ("#/components/schemas/Health").
func resolve(doc map[string]any, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("only local references are allowed: %s", ref)
	}
	var cur any = doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok || m[part] == nil {
			return nil, fmt.Errorf("unresolved reference %s", ref)
		}
		cur = m[part]
	}
	return cur, nil
}

// asyncapi.yaml takes its payloads from openapi.yaml; every one must exist.
func TestAsyncAPIPayloadsResolve(t *testing.T) {
	src, err := os.ReadFile("asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := spec(t)
	refs := regexp.MustCompile(`openapi\.yaml(#[^"\s}]+)`).FindAllSubmatch(src, -1)
	if len(refs) < 9 {
		t.Fatalf("expected the nine arl/1 frame payloads, found %d", len(refs))
	}
	for _, m := range refs {
		if _, err := resolve(doc, string(m[1])); err != nil {
			t.Error(err)
		}
	}
	if _, err := openapi.JSON(src); err != nil {
		t.Fatal("asyncapi.yaml is not valid YAML:", err)
	}
}
