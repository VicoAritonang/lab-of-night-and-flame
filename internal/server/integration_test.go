package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"go.vicoworks.com/agent-reliability-lab/internal/dataset"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/engine"
	"go.vicoworks.com/agent-reliability-lab/internal/queue"
	"go.vicoworks.com/agent-reliability-lab/internal/server"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/testutil"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type M = domain.M
type fixture struct {
	t      *testing.T
	DB     *store.Store
	HTTP   *httptest.Server
	Engine *engine.Engine
	Server *server.Server
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := testutil.DB(t)
	cfg := testutil.Config()
	s, err := server.New(context.Background(), cfg, db, "LAB_WS_URL · arl/1 · graph.visit")
	if err != nil {
		t.Fatal(err)
	}
	q, err := queue.New(context.Background(), db, "", cfg.Region, cfg.Concurrency)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(db, q, cfg)
	if _, err = dataset.Seed(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = q.RunLocal(ctx, eng.Handle) }()
	httpServer := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Hub.Close(); httpServer.Close(); cancel(); <-done })
	return &fixture{t, db, httpServer, eng, s}
}
func (f *fixture) request(method, path string, body any) M {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(domain.JSON(body))
	}
	req, _ := http.NewRequest(method, f.HTTP.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	res, err := f.HTTP.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		f.t.Fatalf("%s %s HTTP %d: %s", method, path, res.StatusCode, data)
	}
	var out M
	if json.Unmarshal(data, &out) != nil {
		f.t.Fatalf("not a JSON object: %s", data)
	}
	return out
}
func (f *fixture) dataset(name string) string {
	f.t.Helper()
	rows, err := f.DB.List(context.Background(), "datasets", M{"name": name}, 1)
	if err != nil || len(rows) != 1 {
		f.t.Fatal("missing fixture", name, err)
	}
	return domain.Str(rows[0]["id"])
}
func plan(ds string, models ...string) M {
	targets := []M{}
	for _, m := range models {
		targets = append(targets, M{"provider": "fake", "model": m})
	}
	return M{"dataset_id": ds, "targets": targets, "scorers": []M{{"id": "s1", "key": "auto"}}, "primary_scorer": "s1", "protocol": M{"concurrency": 4, "max_retries": 3}, "budget": M{"max_usd": 1.}, "acceptance": M{"min_assessed_per_arm": 4, "min_pass_rate": 0.4}}
}
func (f *fixture) launch(p M) string {
	f.t.Helper()
	result := f.request("POST", "/api/experiments?launch=true", M{"name": "Go integration", "plan": p})
	id := domain.Str(result["campaign_id"])
	if id == "" {
		f.t.Fatal(result)
	}
	return id
}
func (f *fixture) wait(id string) M {
	f.t.Helper()
	end := time.Now().Add(25 * time.Second)
	for time.Now().Before(end) {
		c := f.request("GET", "/api/campaigns/"+id, nil)
		if !domain.Bool(c["live"]) {
			return c
		}
		time.Sleep(40 * time.Millisecond)
	}
	f.t.Fatal("campaign timed out", id)
	return nil
}
func TestFakeCampaignEndToEnd(t *testing.T) {
	f := setup(t)
	p := plan(f.dataset("general-knowledge-mcq"), "fake/oracle-90", "fake/oracle-50")
	p["scorers"] = []M{{"id": "s1", "key": "choice"}}
	cid := f.launch(p)
	c := f.wait(cid)
	if c["status"] != "completed" {
		t.Fatal(c)
	}
	r := f.request("GET", "/api/campaigns/"+cid+"/results", nil)
	if !domain.Bool(domain.Obj(r["gate"])["execution_complete"]) || len(domain.Arr(r["comparisons"])) != 1 {
		t.Fatal(r["gate"])
	}
	for _, v := range domain.Obj(r["summaries"]) {
		s := domain.Obj(v)
		if domain.Int(s["planned"]) != 24 || domain.Int(s["completed"]) != 24 || domain.Num(domain.Obj(s["cost"])["total_known_usd"]) != 0 {
			t.Fatal(s)
		}
	}
	trials := f.request("GET", "/api/campaigns/"+cid+"/trials?limit=5", nil)
	if domain.Int(trials["total"]) != 48 || len(domain.Arr(trials["items"])) != 5 {
		t.Fatal(trials)
	}
	detail := f.request("GET", "/api/trials/"+domain.Str(domain.Maps(trials["items"])[0]["id"]), nil)
	if len(domain.Arr(detail["attempts"])) == 0 || len(domain.Arr(detail["assessments"])) == 0 {
		t.Fatal(detail)
	}
	report := f.request("GET", "/api/campaigns/"+cid+"/report.json", nil)
	if len(domain.Arr(report["trials"])) != 48 || domain.Obj(report["checksum"])["algorithm"] != "sha256" {
		t.Fatal("report missing evidence")
	}
	checksum := domain.Str(domain.Obj(report["checksum"])["value"])
	delete(report, "checksum")
	if domain.Hash(report) != checksum {
		t.Fatal("report checksum does not verify")
	}
	grid := f.request("GET", "/api/campaigns/"+cid+"/grid", nil)
	if len(domain.Arr(grid["ids"])) != 48 {
		t.Fatal(grid)
	}
}
func TestRetryEvidenceAndRescore(t *testing.T) {
	f := setup(t)
	p := plan(f.dataset("arithmetic-word-problems"), "fake/flaky-30")
	p["scorers"] = []M{{"id": "s1", "key": "numeric"}}
	cid := f.launch(p)
	if c := f.wait(cid); c["status"] != "completed" {
		t.Fatal(c)
	}
	attempts, err := f.DB.List(context.Background(), "attempts", M{"campaign_id": cid}, 1000)
	if err != nil || len(attempts) <= 40 {
		t.Fatal("retry attempts missing", len(attempts), err)
	}
	before, _ := f.DB.List(context.Background(), "trials", M{"campaign_id": cid}, 1000)
	res := f.request("POST", "/api/campaigns/"+cid+"/rescore", M{"scorers": []M{{"id": "norm", "key": "normalized_match"}}, "primary_scorer": "norm"})
	if domain.Int(res["revision"]) != 2 {
		t.Fatal(res)
	}
	f.wait(cid)
	after, _ := f.DB.List(context.Background(), "trials", M{"campaign_id": cid}, 1000)
	byID := map[string]string{}
	for _, row := range before {
		byID[domain.Str(row["id"])] = domain.Str(row["output_text"])
	}
	for _, row := range after {
		if byID[domain.Str(row["id"])] != domain.Str(row["output_text"]) {
			t.Fatal("rescore changed generation")
		}
	}
	r := f.request("GET", "/api/campaigns/"+cid+"/results", nil)
	if r["primary_scorer"] != "norm" || !domain.Has(domain.Strings(r["labels"]), "re_scored") {
		t.Fatal(r["scoring_revision"])
	}
}
func TestBudgetAndPaidConfirmation(t *testing.T) {
	f := setup(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"mock\",\"choices\":[{\"delta\":{\"content\":\"4\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"cost\":0.1}}\n\ndata: [DONE]\n\n")
	}))
	defer mock.Close()
	f.request("POST", "/api/connections", M{"name": "paid", "label": "Mock paid provider", "base_url": mock.URL, "rpm": 10000})
	// Direct fixture setup bypasses endpoint rpm bound while preserving production rate logic.
	_ = f.DB.Put(context.Background(), "catalogs", "paid", M{"items": []M{{"id": "mock", "price_in": 1000., "price_out": 1000., "modalities": []string{"text"}}}})
	p := plan(f.dataset("arithmetic-word-problems"), "fake/oracle-90")
	p["targets"] = []M{{"provider": "paid", "model": "mock", "params": M{"max_tokens": 32}}}
	p["scorers"] = []M{{"id": "s1", "key": "numeric"}}
	domain.Obj(p["protocol"])["sample_limit"] = 4
	p["budget"] = M{"max_usd": 0., "paid_run_confirmed": true}
	cid := f.launch(p)
	c := f.wait(cid)
	if c["status"] != "paused" || domain.Num(c["spent_usd"])+domain.Num(c["reserved_usd"]) > 0 {
		t.Fatal(c)
	}
	f.request("POST", "/api/campaigns/"+cid+"/resume", M{"budget_max_usd": 100.})
	if c = f.wait(cid); c["status"] != "completed" {
		t.Fatal(c)
	}
	p["budget"] = M{"max_usd": 100.}
	body := bytes.NewReader(domain.JSON(M{"name": "unconfirmed", "plan": p}))
	res, err := f.HTTP.Client().Post(f.HTTP.URL+"/api/experiments?launch=true", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(res.Body)
	if res.StatusCode != 422 || !strings.Contains(string(text), "paid_run_not_confirmed") {
		t.Fatal(res.StatusCode, string(text))
	}
}
func TestCancellationStopsAdmission(t *testing.T) {
	f := setup(t)
	p := plan(f.dataset("arithmetic-word-problems"), "fake/slow")
	domain.Obj(p["protocol"])["concurrency"] = 2
	p["scorers"] = []M{{"id": "s1", "key": "numeric"}}
	cid := f.launch(p)
	time.Sleep(300 * time.Millisecond)
	f.request("POST", "/api/campaigns/"+cid+"/cancel", M{})
	cutoff := domain.Now() + 0.05
	c := f.wait(cid)
	if c["status"] != "cancelled" {
		t.Fatal(c)
	}
	rows, _ := f.DB.List(context.Background(), "attempts", M{"campaign_id": cid}, 1000)
	for _, row := range rows {
		if domain.Num(row["started_at"]) > cutoff {
			t.Fatal("provider dispatched after cancellation")
		}
	}
}
func pngBytes(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for i := range im.Pix {
		im.Pix[i] = 255
	}
	im.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func (f *fixture) multipart(path string, fields map[string]string, files map[string][]byte) (int, []byte) {
	f.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, value := range fields {
		_ = mw.WriteField(key, value)
	}
	for field, b := range files {
		name := "data.jsonl"
		if field == "images" || field == "image" {
			name = "red.png"
		}
		part, err := mw.CreateFormFile(field, name)
		if err != nil {
			f.t.Fatal(err)
		}
		_, _ = part.Write(b)
	}
	_ = mw.Close()
	res, err := f.HTTP.Client().Post(f.HTTP.URL+path, mw.FormDataContentType(), &body)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}
func TestDatasetImportPredictionsAndImages(t *testing.T) {
	f := setup(t)
	records := "{\"id\":\"a\",\"input\":\"What color?\",\"image\":\"red.png\",\"expected\":\"red\"}\n{\"id\":\"b\",\"input\":\"2+2?\",\"expected\":4}\n{\"id\":\"c\"}"
	files := map[string][]byte{"file": []byte(records), "images": pngBytes(t)}
	code, b := f.multipart("/api/datasets/import", map[string]string{"name": "mine"}, files)
	if code != 422 {
		t.Fatal(code, string(b))
	}
	code, b = f.multipart("/api/datasets/import", map[string]string{"name": "mine", "allow_partial": "true"}, files)
	if code != 200 {
		t.Fatal(code, string(b))
	}
	var ds M
	_ = json.Unmarshal(b, &ds)
	if domain.Int(ds["sample_count"]) != 2 || ds["modality"] != "image" {
		t.Fatal(ds)
	}
	code, b = f.multipart("/api/datasets/import", map[string]string{"name": "mine", "allow_partial": "true"}, files)
	var repeat M
	_ = json.Unmarshal(b, &repeat)
	if repeat["id"] != ds["id"] {
		t.Fatal("snapshot not idempotent", repeat)
	}
	id := domain.Str(ds["id"])
	code, b = f.multipart("/api/datasets/"+id+"/predictions", map[string]string{"name": "legacy"}, map[string][]byte{"file": []byte("{\"id\":\"a\",\"output\":\"red\"}\n{\"id\":\"b\",\"output\":\"5\"}")})
	if code != 200 {
		t.Fatal(code, string(b))
	}
	p := plan(id, "fake/echo")
	p["targets"] = []M{{"provider": "imported", "model": "legacy"}}
	p["scorers"] = []M{{"id": "s1", "key": "normalized_match"}}
	cid := f.launch(p)
	f.wait(cid)
	r := f.request("GET", "/api/campaigns/"+cid+"/results", nil)
	for _, v := range domain.Obj(r["summaries"]) {
		s := domain.Obj(v)
		if domain.Int(domain.Obj(s["primary"])["passes"]) != 1 || domain.Obj(s["latency_ms"])["p50"] != nil {
			t.Fatal(s)
		}
	}
}
func TestAgentWebSocketTraceResumeAndExport(t *testing.T) {
	f := setup(t)
	f.request("POST", "/api/agent-lab/listener/start", M{})
	ctx := context.Background()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.HTTP.URL, "http")+"/api/agent-lab/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	exchange := func(c *websocket.Conn, m M) M {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Write(ctx, websocket.MessageText, domain.JSON(m)); err != nil {
			t.Fatal(err)
		}
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out M
		_ = json.Unmarshal(b, &out)
		return out
	}
	ws := dial()
	hello := M{"type": "hello", "protocol": "arl/1", "service": "test-agent", "stage": "dev"}
	ack := exchange(ws, hello)
	if ack["type"] != "hello_ack" {
		t.Fatal(ack)
	}
	emit := func(kind, id string, data M) M {
		return exchange(ws, M{"type": "event", "event_id": id, "run_id": "job", "event": kind, "phase": "agent", "data": data})
	}
	start := emit("run.start", "start", M{"checks": M{"max_cost_usd": 1., "required_nodes": []string{"n1"}, "output_contains": []string{"ok"}}})
	rid := domain.Str(start["trace_id"])
	if rid == "" {
		t.Fatal(start)
	}
	emit("graph.node", "node", M{"node_id": "n1"})
	emit("graph.visit", "visit", M{"node_id": "n1", "attempt": 1})
	if dup := emit("graph.visit", "visit", M{"node_id": "n1", "attempt": 1}); dup["duplicate"] != true {
		t.Fatal(dup)
	}
	emit("tool.call", "call", M{"tool": "search", "call_id": "call1"})
	emit("cost.record", "cost", M{"billing_id": "call1", "amount_usd": 0.})
	emit("output", "out", M{"content": []M{{"type": "text", "text": "ok"}}})
	emit("run.end", "end", M{"status": "completed", "duration_ms": 20})
	_ = ws.Close(websocket.StatusNormalClosure, "done")
	ws = dial()
	hello["session_id"], hello["resume_token"] = ack["session_id"], ack["resume_token"]
	if resumed := exchange(ws, hello); resumed["type"] != "hello_ack" {
		t.Fatal(resumed)
	}
	if duplicate := emit("run.start", "start", M{"checks": M{"max_cost_usd": 1., "required_nodes": []string{"n1"}, "output_contains": []string{"ok"}}}); duplicate["duplicate"] != true {
		t.Fatal(duplicate)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")
	detail := f.request("GET", "/api/agent-lab/runs/"+rid, nil)
	summary := domain.Obj(detail["summary"])
	if summary["verdict"] != "pass" || domain.Int(summary["visits"]) != 1 || domain.Int(summary["events"]) != 7 {
		t.Fatal(summary)
	}
	export := f.request("GET", "/api/agent-lab/runs/"+rid+"/export", nil)
	if export["protocol"] != "arl/1" || len(domain.Arr(export["events"])) != 7 {
		t.Fatal(export)
	}
	pages := f.request("GET", "/api/agent-lab/runs/"+rid+"/events?limit=2", nil)
	if pages["has_more"] != true || len(domain.Arr(pages["items"])) != 2 {
		t.Fatal(pages)
	}
}
func TestAuthAndWebSocketTicket(t *testing.T) {
	f := setup(t)
	f.Server.Config.AuthToken = "owner-test-secret"
	res, err := f.HTTP.Client().Get(f.HTTP.URL + "/api/agent-lab/status")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("API unprotected")
	}
	req, _ := http.NewRequest("POST", f.HTTP.URL+"/api/agent-lab/ticket", nil)
	req.Header.Set("Authorization", "Bearer owner-test-secret")
	res, err = f.HTTP.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var ticket M
	_ = json.NewDecoder(res.Body).Decode(&ticket)
	res.Body.Close()
	key := domain.Str(ticket["ticket"])
	if key == "" {
		t.Fatal(ticket)
	}
	wsURL := "ws" + strings.TrimPrefix(f.HTTP.URL, "http") + "/api/agent-lab/ws?ticket=" + key
	ws, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.CloseNow()
	if reused, _, err := websocket.Dial(context.Background(), wsURL, nil); err == nil {
		reused.CloseNow()
		t.Fatal("ticket was reusable")
	}
}
