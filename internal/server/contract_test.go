package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.vicoworks.com/agent-reliability-lab/api"
	arlv1 "go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1"
	"go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1/arlv1connect"
	"go.vicoworks.com/agent-reliability-lab/internal/config"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/features"
	"go.vicoworks.com/agent-reliability-lab/internal/server"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
)

const specURL = "https://contract.arl.test/openapi.json"

// contract routes requests to the documented operations of api/openapi.yaml
// and validates answers against their schemas.
type contract struct {
	doc      map[string]any
	ops      *http.ServeMux
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	schemas  map[string]*jsonschema.Schema
}

var loadContract = sync.OnceValues(func() (*contract, error) {
	c := &contract{ops: http.NewServeMux(), compiler: jsonschema.NewCompiler(), schemas: map[string]*jsonschema.Schema{}}
	if err := json.Unmarshal(api.OpenAPIJSON, &c.doc); err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(api.OpenAPIJSON))
	if err != nil {
		return nil, err
	}
	if err = c.compiler.AddResource(specURL, doc); err != nil {
		return nil, err
	}
	for path, item := range c.doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			if method == "parameters" {
				continue
			}
			pattern := strings.ToUpper(method) + " /api" + path
			c.ops.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
		}
	}
	return c, nil
})

func (c *contract) operation(method, path string) (M, string) {
	req := httptest.NewRequest(method, path, nil)
	_, pattern := c.ops.Handler(req)
	if pattern == "" {
		return nil, ""
	}
	verb, p, _ := strings.Cut(pattern, " ")
	item := c.doc["paths"].(map[string]any)[strings.TrimPrefix(p, "/api")].(map[string]any)
	return item[strings.ToLower(verb)].(map[string]any), pattern
}

// schema returns the compiled schema at a JSON pointer, following a response $ref.
func (c *contract) schema(pointer string) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.schemas[pointer]; ok {
		return s, nil
	}
	s, err := c.compiler.Compile(specURL + "#" + pointer)
	if err == nil {
		c.schemas[pointer] = s
	}
	return s, err
}

func escape(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }

// check validates one answer: problem documents for errors, the documented
// JSON schema for successes.
func (c *contract) check(method, path string, status int, contentType string, body []byte) error {
	op, pattern := c.operation(method, path)
	if op == nil {
		return errors.New("undocumented operation")
	}
	media := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if status >= 400 {
		if media != "application/problem+json" {
			return errors.New("error answered without application/problem+json: " + contentType)
		}
		return c.validate("/components/schemas/Problem", body)
	}
	responses := op["responses"].(map[string]any)
	response, ok := responses[strconv.Itoa(status)].(map[string]any)
	if !ok {
		return errors.New("undocumented status " + strconv.Itoa(status))
	}
	verb, p, _ := strings.Cut(pattern, " ")
	pointer := "/paths/" + escape(strings.TrimPrefix(p, "/api")) + "/" + strings.ToLower(verb) + "/responses/" + strconv.Itoa(status)
	if ref, ok := response["$ref"].(string); ok {
		pointer = strings.TrimPrefix(ref, "#")
		response = c.doc["components"].(map[string]any)["responses"].(map[string]any)[strings.TrimPrefix(ref, "#/components/responses/")].(map[string]any)
	}
	content, _ := response["content"].(map[string]any)
	if content[media] == nil {
		return errors.New("undocumented media type " + media)
	}
	if media != "application/json" {
		return nil
	}
	return c.validate(pointer+"/content/application~1json/schema", body)
}

func (c *contract) validate(pointer string, body []byte) error {
	s, err := c.schema(pointer)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	return s.Validate(value)
}

type capture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
	c.ResponseWriter.WriteHeader(status)
}
func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}
func (c *capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// conforming wraps the lab so every REST answer a test provokes is checked
// against api/openapi.yaml.
func conforming(t *testing.T, h http.Handler) http.Handler {
	c, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Replace(r.URL.Path, "/api/v1/", "/api/", 1)
		if !strings.HasPrefix(path, "/api/") || path == "/api/agent-lab/ws" {
			h.ServeHTTP(w, r)
			return
		}
		cw := &capture{ResponseWriter: w}
		h.ServeHTTP(cw, r)
		if err := c.check(r.Method, path, cw.status, w.Header().Get("Content-Type"), cw.body.Bytes()); err != nil {
			t.Errorf("contract: %s %s -> %d: %v\n%.400s", r.Method, path, cw.status, err, cw.body.String())
		}
	})
}

// newLab builds a lab without a database: enough for routing, modules,
// errors and the RPC facade.
func newLab(t *testing.T, spec, token string) *server.Server {
	t.Helper()
	set, _, err := features.Parse(spec)
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.New(context.Background(), config.Config{MaxHTTP: 8, MaxWebSockets: 4, Features: set, AuthToken: token}, &store.Store{}, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEveryRouteIsInTheContract(t *testing.T) {
	c, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	routes := newLab(t, "", "").Routes()
	documented := map[string]bool{}
	for path, item := range c.doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" /api"+path] = true
			}
		}
	}
	for _, r := range routes {
		if !documented[r] {
			t.Errorf("route %s is not in api/openapi.yaml", r)
		}
		delete(documented, r)
	}
	for op := range documented {
		t.Errorf("api/openapi.yaml documents %s, which the lab does not serve", op)
	}
}

func TestErrorsAreProblemDocuments(t *testing.T) {
	h := conforming(t, newLab(t, "tracing", "secret").Handler())
	for _, tc := range []struct {
		path, auth, code string
		status           int
	}{
		{"/api/v1/projects", "", "unauthenticated", 401},
		{"/api/v1/prompts", "Bearer secret", "feature_disabled", 404},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.Header.Set("Authorization", tc.auth)
		req.Header.Set("X-Request-ID", "trace-me-1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var raw M
		_ = json.Unmarshal(w.Body.Bytes(), &raw)
		if w.Code != tc.status || raw["code"] != tc.code || raw["type"] != "urn:arl:problem:"+tc.code || raw["request_id"] != "trace-me-1" || w.Header().Get("X-Request-ID") != "trace-me-1" {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestVersionedPrefixIsTheSameAPI(t *testing.T) {
	h := newLab(t, "tracing", "").Handler()
	var bodies []string
	for _, path := range []string{"/api/features", "/api/v1/features"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		bodies = append(bodies, w.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Fatal(bodies)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/openapi.json", nil))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), api.OpenAPIJSON) {
		t.Fatal("OpenAPI document not served", w.Code)
	}
}

func TestRPCSharesRESTSemantics(t *testing.T) {
	s := newLab(t, "tracing", "secret")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/features", nil)
	r.Header.Set("Authorization", "Bearer secret")
	s.Handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"grpc":true`) {
		t.Skip("built with arl_nogrpc")
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	auth := func(req connect.AnyRequest) { req.Header().Set("Authorization", "Bearer secret") }

	lab := arlv1connect.NewLabServiceClient(srv.Client(), srv.URL)
	req := connect.NewRequest(&arlv1.GetFeaturesRequest{})
	auth(req)
	res, err := lab.GetFeatures(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Msg.Enabled, "traces") || slices.Contains(res.Msg.Enabled, "prompts") || len(res.Msg.Modules) != len(features.All) || !res.Msg.Build.Grpc {
		t.Fatal(res.Msg)
	}

	if _, err = lab.GetFeatures(context.Background(), connect.NewRequest(&arlv1.GetFeaturesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("missing token:", err)
	}

	prompts := arlv1connect.NewPromptServiceClient(srv.Client(), srv.URL, connect.WithGRPCWeb())
	preq := connect.NewRequest(&arlv1.GetPromptRequest{Name: "support"})
	auth(preq)
	_, err = prompts.GetPrompt(context.Background(), preq)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnimplemented {
		t.Fatal("disabled module:", err)
	}
	found := false
	for _, d := range ce.Details() {
		if v, e := d.Value(); e == nil {
			if detail, ok := v.(*arlv1.ErrorDetail); ok && detail.Code == "feature_disabled" && detail.Feature == "prompts" && detail.HttpStatus == 404 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no arl.v1.ErrorDetail", ce.Details())
	}
}

// The checker itself must catch drift, or the integration tests prove nothing.
func TestContractCheckerRejectsDrift(t *testing.T) {
	c, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	good := `{"status":"ok","version":"1","uptime_s":1,"auth_required":false,"mode":"all","async_transport":"local-worker","features":[]}`
	for _, tc := range []struct {
		method, path, media, body string
		status                    int
		ok                        bool
	}{
		{"GET", "/api/health", "application/json", good, 200, true},
		{"GET", "/api/health", "application/json", `{"status":"degraded"}`, 200, false},
		{"GET", "/api/agent-lab/runs", "application/json", `[{"id":1}]`, 200, false},
		{"GET", "/api/jobs/j1", "application/json", `{"id":"j1","kind":"k","state":"lost","attempts":0,"created_at":"x","updated_at":"x"}`, 200, false},
		{"GET", "/api/health", "application/json", `{"detail":"x"}`, 500, false},
		{"GET", "/api/health", "application/problem+json", `{"type":"urn:arl:problem:internal","title":"Internal Server Error","status":500,"detail":"x","code":"internal"}`, 500, true},
		{"GET", "/api/not-documented", "application/json", `{}`, 200, false},
		{"GET", "/api/campaigns/c1", "application/json", `{"anything":true}`, 200, true},
	} {
		err := c.check(tc.method, tc.path, tc.status, tc.media, []byte(tc.body))
		if (err == nil) != tc.ok {
			t.Errorf("%s %s %d %s: err=%v", tc.method, tc.path, tc.status, tc.body, err)
		}
	}
}

var frameSchemas = map[string]string{"hello": "AgentHello", "event": "AgentEvent", "ping": "AgentPing", "replay.status": "AgentReplayStatus", "hello_ack": "LabHelloAck", "ack": "LabAck", "pong": "LabPong", "error": "LabError", "replay": "LabReplay"}

// checkFrame validates an arl/1 frame against api/asyncapi.yaml (whose
// payloads live in openapi.yaml).
func checkFrame(t *testing.T, frame M) {
	t.Helper()
	c, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	name := frameSchemas[domain.Str(frame["type"])]
	if name == "" {
		t.Errorf("undocumented arl/1 frame type %v", frame["type"])
		return
	}
	if err = c.validate("/components/schemas/"+name, domain.JSON(frame)); err != nil {
		t.Errorf("arl/1 %s frame does not match the contract: %v\n%s", name, err, domain.JSON(frame))
	}
}
