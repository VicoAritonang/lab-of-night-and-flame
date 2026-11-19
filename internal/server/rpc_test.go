//go:build !arl_nogrpc

package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	arlv1 "go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1"
	"go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1/arlv1connect"
	"google.golang.org/protobuf/types/known/structpb"
)

// h2c serves the lab over cleartext HTTP/2, as main.go does, and returns a
// client that speaks it: native gRPC streaming needs HTTP/2.
func (f *fixture) h2c() (*http.Client, string) {
	srv := httptest.NewUnstartedServer(conforming(f.t, f.Server.Handler()))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	f.t.Cleanup(srv.Close)
	transport := &http.Transport{Protocols: new(http.Protocols)}
	transport.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: transport}, srv.URL
}

func TestTelemetryStreamAndTypedReadsOverGRPC(t *testing.T) {
	f := setup(t)
	f.request("POST", "/api/v1/agent-lab/listener/start", nil)
	client, base := f.h2c()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream := arlv1connect.NewTelemetryServiceClient(client, base, connect.WithGRPC()).Connect(ctx)
	send := func(req *arlv1.ConnectRequest) *arlv1.ConnectResponse {
		t.Helper()
		if err := stream.Send(req); err != nil {
			t.Fatal(err)
		}
		res, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	hello := send(&arlv1.ConnectRequest{Frame: &arlv1.ConnectRequest_Hello{Hello: &arlv1.Hello{Protocol: "arl/1", Service: "grpc-agent", Stage: "dev", Capabilities: []string{"tools"}}}})
	if hello.GetHelloAck().GetSessionId() == "" || hello.GetHelloAck().GetResumeToken() == "" || len(hello.GetHelloAck().GetPhases()) == 0 {
		t.Fatal(hello)
	}
	event := func(id, kind string, data map[string]any) *arlv1.ConnectResponse {
		s, err := structpb.NewStruct(data)
		if err != nil {
			t.Fatal(err)
		}
		return send(&arlv1.ConnectRequest{Frame: &arlv1.ConnectRequest_Event{Event: &arlv1.Event{EventId: id, RunId: "grpc-run", Event: kind, Data: s}}})
	}
	start := event("e1", "run.start", map[string]any{"name": "gRPC run"})
	traceID := start.GetAck().GetTraceId()
	if traceID == "" || start.GetAck().GetOrdinal() == 0 {
		t.Fatal(start)
	}
	event("e2", "tool.call", map[string]any{"tool": "search", "call_id": "c1"})
	if dup := event("e2", "tool.call", map[string]any{"tool": "search", "call_id": "c1"}); !dup.GetAck().GetDuplicate() {
		t.Fatal(dup)
	}
	// A rejected frame is answered in order and the stream stays usable.
	if bad := event("e3", "Not An Event", nil); bad.GetError().GetCode() != "validation_error" || bad.GetError().GetEventId() != "e3" {
		t.Fatal(bad)
	}
	if pong := send(&arlv1.ConnectRequest{Frame: &arlv1.ConnectRequest_Ping{Ping: &arlv1.Ping{}}}); !pong.GetPong().GetListening() {
		t.Fatal(pong)
	}
	event("e4", "run.end", map[string]any{"status": "completed"})
	if err := stream.CloseRequest(); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseResponse()

	traces := arlv1connect.NewTraceServiceClient(client, base, connect.WithGRPC())
	runs, err := traces.ListRuns(ctx, connect.NewRequest(&arlv1.ListRunsRequest{ServiceName: "grpc-agent", Limit: 10}))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs.Msg.Runs) != 1 || runs.Msg.Runs[0].Id != traceID || runs.Msg.Runs[0].ToolCalls != 1 || runs.Msg.Runs[0].Source != "sdk" {
		t.Fatal(runs.Msg)
	}
	run, err := traces.GetRun(ctx, connect.NewRequest(&arlv1.GetRunRequest{Id: traceID}))
	if err != nil {
		t.Fatal(err)
	}
	if run.Msg.Run.Status != "completed" || run.Msg.Run.Summary == nil || run.Msg.Run.ExternalId != "grpc-run" {
		t.Fatal(run.Msg)
	}
	events, err := traces.ListRunEvents(ctx, connect.NewRequest(&arlv1.ListRunEventsRequest{Id: traceID, Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Msg.Items) != 2 || !events.Msg.HasMore || events.Msg.Items[1].Data.Fields["tool"].GetStringValue() != "search" {
		t.Fatal(events.Msg)
	}
	if _, err = traces.GetRun(ctx, connect.NewRequest(&arlv1.GetRunRequest{Id: "trace_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("missing run:", err)
	}

	feedback := arlv1connect.NewFeedbackServiceClient(client, base)
	value := 1.0
	add := func() string {
		req := connect.NewRequest(&arlv1.CreateFeedbackRequest{RunId: "grpc-run", Service: "grpc-agent", Name: "helpful", Value: &value})
		req.Header().Set("Idempotency-Key", "thumbs-1")
		res, err := feedback.CreateFeedback(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.Msg.Feedback.TraceId != traceID {
			t.Fatal(res.Msg)
		}
		return res.Msg.Feedback.Id
	}
	if first, second := add(), add(); first != second {
		t.Fatal("idempotency key ignored", first, second)
	}
	listed, err := feedback.ListFeedback(ctx, connect.NewRequest(&arlv1.ListFeedbackRequest{TraceId: traceID}))
	if err != nil || len(listed.Msg.Feedback) != 1 || listed.Msg.Feedback[0].GetValue() != 1 {
		t.Fatal(listed, err)
	}
	if _, err = feedback.CreateFeedback(ctx, connect.NewRequest(&arlv1.CreateFeedbackRequest{TraceId: traceID})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal("invalid feedback:", err)
	}

	f.request("POST", "/api/v1/prompts", M{"name": "support", "template": M{"system": "You help {{customer}}.", "messages": []M{{"role": "user", "content": "{{question}}"}}}, "config": M{"model": "fake/echo"}, "labels": []string{"production"}})
	prompts := arlv1connect.NewPromptServiceClient(client, base)
	prompt, err := prompts.GetPrompt(ctx, connect.NewRequest(&arlv1.GetPromptRequest{Name: "support", Label: "production"}))
	if err != nil {
		t.Fatal(err)
	}
	if p := prompt.Msg.Prompt; p.Version != 1 || p.Labels["production"] != 1 || p.Template.System != "You help {{customer}}." || len(p.Variables) != 2 {
		t.Fatal(prompt.Msg)
	}
	vars, _ := structpb.NewStruct(map[string]any{"customer": "Ana"})
	rendered, err := prompts.RenderPrompt(ctx, connect.NewRequest(&arlv1.RenderPromptRequest{Name: "support", Label: "production", Variables: vars}))
	if err != nil {
		t.Fatal(err)
	}
	if r := rendered.Msg; r.Messages[0].Content != "You help Ana." || len(r.Missing) != 1 || r.Missing[0] != "question" {
		t.Fatal(rendered.Msg)
	}

	projects := arlv1connect.NewProjectServiceClient(client, base)
	list, err := projects.ListProjects(ctx, connect.NewRequest(&arlv1.ListProjectsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list.Msg.Projects {
		found = found || (p.Id == f.DB.Project && p.CreatedAt > 0)
	}
	if !found {
		t.Fatal(list.Msg)
	}
	if _, err = projects.CreateProject(ctx, connect.NewRequest(&arlv1.CreateProjectRequest{Id: f.DB.Project, Name: "again"})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatal("duplicate project:", err)
	}
	var jobID string
	if err = f.DB.Pool.QueryRow(ctx, "SELECT id FROM arl.jobs WHERE project_id=$1 AND kind='trace.summarize' LIMIT 1", f.DB.Project).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	job, err := arlv1connect.NewJobServiceClient(client, base).GetJob(ctx, connect.NewRequest(&arlv1.GetJobRequest{Id: jobID}))
	if err != nil {
		t.Fatal(err)
	}
	if j := job.Msg.Job; j.Kind != "trace.summarize" || j.CreatedAt.AsTime().IsZero() || j.UpdatedAt.AsTime().Before(j.CreatedAt.AsTime()) {
		t.Fatal(job.Msg)
	}
	health, err := arlv1connect.NewLabServiceClient(client, base, connect.WithGRPC()).GetHealth(ctx, connect.NewRequest(&arlv1.GetHealthRequest{}))
	if err != nil || health.Msg.Status != "ok" {
		t.Fatal(health, err)
	}
}
