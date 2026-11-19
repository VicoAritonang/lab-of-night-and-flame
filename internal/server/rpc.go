//go:build !arl_nogrpc

package server

// The gRPC API (api/proto/arl/v1) is a typed facade over the REST contract.
// Every unary call runs the matching REST operation in-process and converts
// its JSON with protojson, so both transports share validation, project
// scoping, modules and errors. TelemetryService shares the agent connection
// with the WebSocket endpoint. Connect serves gRPC, gRPC-Web and the Connect
// protocol (JSON over HTTP/1.1) on the same port as REST.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	arlv1 "go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1"
	"go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1/arlv1connect"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/features"
	"go.vicoworks.com/agent-reliability-lab/internal/tracelab"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const rpcCompiled = true

// rpcServices maps each gRPC service to the module it needs ("" is core).
var rpcServices = map[string]features.Feature{
	arlv1connect.LabServiceName:       "",
	arlv1connect.ProjectServiceName:   "",
	arlv1connect.JobServiceName:       "",
	arlv1connect.TraceServiceName:     features.Traces,
	arlv1connect.TelemetryServiceName: features.Traces,
	arlv1connect.FeedbackServiceName:  features.Quality,
	arlv1connect.PromptServiceName:    features.Prompts,
}

func isRPC(path string) bool {
	return strings.HasPrefix(path, "/arl.v1.") || strings.HasPrefix(path, "/grpc.health.v1.") || strings.HasPrefix(path, "/grpc.reflection.")
}

func (s *Server) rpcHandler() http.Handler {
	mux := http.NewServeMux()
	opts := connect.WithReadMaxBytes(2 * tracelab.MaxMessage)
	r := rpc{s}
	mux.Handle(arlv1connect.NewLabServiceHandler(r, opts))
	mux.Handle(arlv1connect.NewProjectServiceHandler(r, opts))
	mux.Handle(arlv1connect.NewJobServiceHandler(r, opts))
	mux.Handle(arlv1connect.NewTraceServiceHandler(r, opts))
	mux.Handle(arlv1connect.NewFeedbackServiceHandler(r, opts))
	mux.Handle(arlv1connect.NewPromptServiceHandler(r, opts))
	mux.Handle(arlv1connect.NewTelemetryServiceHandler(r, opts))
	mux.Handle(grpchealth.NewHandler(r))
	names := []string{}
	for name := range rpcServices {
		names = append(names, name)
	}
	reflector := grpcreflect.NewStaticReflector(names...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	return mux
}

// serveRPC authenticates and admits RPCs the way Handler does REST calls.
func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	errs := connect.NewErrorWriter()
	public := r.URL.Path == arlv1connect.LabServiceGetHealthProcedure || !strings.HasPrefix(r.URL.Path, "/arl.v1.")
	if !public && !s.Authorized(r) {
		_ = errs.Write(w, r, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required; send authorization: Bearer <ARL_AUTH_TOKEN>")))
		return
	}
	slots := s.slots
	if r.URL.Path == arlv1connect.TelemetryServiceConnectProcedure {
		slots = s.wsSlots
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		_ = errs.Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("gateway is at capacity; retry shortly")))
		return
	}
	defer func() {
		if recover() != nil {
			slog.Error("RPC handler panic")
		}
	}()
	s.rpc.ServeHTTP(w, r)
}

type rpc struct{ s *Server }

// recorder captures an in-process REST answer.
type recorder struct {
	header http.Header
	body   bytes.Buffer
	status int
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(200)
	return r.body.Write(b)
}
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

// forwarded are the request headers a REST operation may depend on.
var forwarded = []string{"Authorization", "Cookie", "X-Lab-Project", "Idempotency-Key", "X-Request-ID"}

// invoke runs a REST operation for an RPC and decodes its JSON answer into
// out, first wrapping it as {"<wrap>": answer} when wrap is set.
func (s *Server) invoke(ctx context.Context, h http.Header, method, target string, body proto.Message, wrap string, out proto.Message) error {
	reader := io.Reader(http.NoBody)
	if body != nil {
		b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(body)
		if err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	for _, k := range forwarded {
		if v := h.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	rec := &recorder{header: http.Header{}, status: 200}
	func() {
		defer func() {
			if recover() != nil {
				slog.Error("RPC operation panic", "target", req.URL.Path)
				rec.status, rec.wrote = 500, true
			}
		}()
		s.Mux.ServeHTTP(rec, req)
	}()
	if rec.status >= 400 {
		return rpcError(rec.status, rec.body.Bytes())
	}
	data := rec.body.Bytes()
	if wrap != "" {
		data = append(append([]byte(`{"`+wrap+`":`), data...), '}')
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, out); err != nil {
		slog.Warn("REST answer does not fit the RPC message", "target", req.URL.Path, "error", err.Error())
		return connect.NewError(connect.CodeInternal, errors.New("response does not match the API contract"))
	}
	return nil
}

func unary[T any, P interface {
	*T
	proto.Message
}](r rpc, ctx context.Context, h http.Header, method, target string, body proto.Message, wrap string) (*connect.Response[T], error) {
	out := P(new(T))
	if err := r.s.invoke(ctx, h, method, target, body, wrap, out); err != nil {
		return nil, err
	}
	return connect.NewResponse((*T)(out)), nil
}

// rpcError converts a REST problem document into a gRPC status with an
// arl.v1.ErrorDetail carrying the same machine-readable fields.
func rpcError(status int, body []byte) error {
	var p struct {
		Detail, Code, Feature string
		RequestID             string `json:"request_id"`
		Errors                []any
	}
	_ = json.Unmarshal(body, &p)
	if p.Detail == "" {
		p.Detail = strings.ToLower(http.StatusText(status))
	}
	code := connect.CodeUnknown
	switch {
	case p.Code == "feature_disabled":
		code = connect.CodeUnimplemented
	case status == 400 || status == 422:
		code = connect.CodeInvalidArgument
	case status == 401:
		code = connect.CodeUnauthenticated
	case status == 403:
		code = connect.CodePermissionDenied
	case status == 404 || status == 405:
		code = connect.CodeNotFound
	case status == 409:
		code = connect.CodeAlreadyExists
	case status == 413 || status == 429:
		code = connect.CodeResourceExhausted
	case status == 503:
		code = connect.CodeUnavailable
	case status >= 500:
		code = connect.CodeInternal
	}
	err := connect.NewError(code, errors.New(p.Detail))
	detail := &arlv1.ErrorDetail{Code: p.Code, HttpStatus: int32(status), Feature: p.Feature, RequestId: p.RequestID}
	if len(p.Errors) > 0 {
		detail.Errors, _ = structpb.NewList(p.Errors)
	}
	if d, e := connect.NewErrorDetail(detail); e == nil {
		err.AddDetail(d)
	}
	return err
}

// at builds an operation path, rejecting empty identifiers.
func at(format string, ids ...string) (string, error) {
	args := []any{}
	for _, id := range ids {
		if id == "" {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("identifier is required"))
		}
		args = append(args, url.PathEscape(id))
	}
	return fmt.Sprintf(format, args...), nil
}

func query(target string, q url.Values) string {
	if len(q) == 0 {
		return target
	}
	return target + "?" + q.Encode()
}

func set(q url.Values, key, value string) {
	if value != "" && value != "0" && value != "false" {
		q.Set(key, value)
	}
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// LabService

func (r rpc) GetHealth(ctx context.Context, req *connect.Request[arlv1.GetHealthRequest]) (*connect.Response[arlv1.GetHealthResponse], error) {
	return unary[arlv1.GetHealthResponse](r, ctx, req.Header(), "GET", "/api/health", nil, "")
}
func (r rpc) GetFeatures(ctx context.Context, req *connect.Request[arlv1.GetFeaturesRequest]) (*connect.Response[arlv1.GetFeaturesResponse], error) {
	return unary[arlv1.GetFeaturesResponse](r, ctx, req.Header(), "GET", "/api/features", nil, "")
}

// ProjectService

func (r rpc) ListProjects(ctx context.Context, req *connect.Request[arlv1.ListProjectsRequest]) (*connect.Response[arlv1.ListProjectsResponse], error) {
	return unary[arlv1.ListProjectsResponse](r, ctx, req.Header(), "GET", "/api/projects", nil, "projects")
}
func (r rpc) CreateProject(ctx context.Context, req *connect.Request[arlv1.CreateProjectRequest]) (*connect.Response[arlv1.CreateProjectResponse], error) {
	return unary[arlv1.CreateProjectResponse](r, ctx, req.Header(), "POST", "/api/projects", req.Msg, "project")
}

// JobService

func (r rpc) GetJob(ctx context.Context, req *connect.Request[arlv1.GetJobRequest]) (*connect.Response[arlv1.GetJobResponse], error) {
	target, err := at("/api/jobs/%s", req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return unary[arlv1.GetJobResponse](r, ctx, req.Header(), "GET", target, nil, "job")
}

// TraceService

func (r rpc) ListSessions(ctx context.Context, req *connect.Request[arlv1.ListSessionsRequest]) (*connect.Response[arlv1.ListSessionsResponse], error) {
	return unary[arlv1.ListSessionsResponse](r, ctx, req.Header(), "GET", "/api/agent-lab/sessions", nil, "sessions")
}
func (r rpc) ListRuns(ctx context.Context, req *connect.Request[arlv1.ListRunsRequest]) (*connect.Response[arlv1.ListRunsResponse], error) {
	m, q := req.Msg, url.Values{}
	for key, value := range map[string]string{"service_name": m.ServiceName, "status": m.Status, "verdict": m.Verdict, "model": m.Model, "tag": m.Tag, "thread_id": m.ThreadId, "user_id": m.UserId, "source": m.Source, "q": m.Q, "min_ms": num(m.MinMs), "errors": strconv.FormatBool(m.Errors), "before": num(m.Before), "hours": num(m.Hours), "limit": strconv.Itoa(int(m.Limit))} {
		set(q, key, value)
	}
	return unary[arlv1.ListRunsResponse](r, ctx, req.Header(), "GET", query("/api/agent-lab/runs", q), nil, "runs")
}
func (r rpc) GetRun(ctx context.Context, req *connect.Request[arlv1.GetRunRequest]) (*connect.Response[arlv1.GetRunResponse], error) {
	target, err := at("/api/agent-lab/runs/%s", req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return unary[arlv1.GetRunResponse](r, ctx, req.Header(), "GET", target, nil, "run")
}
func (r rpc) ListRunEvents(ctx context.Context, req *connect.Request[arlv1.ListRunEventsRequest]) (*connect.Response[arlv1.ListRunEventsResponse], error) {
	target, err := at("/api/agent-lab/runs/%s/events", req.Msg.Id)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	set(q, "after", strconv.FormatInt(req.Msg.After, 10))
	set(q, "limit", strconv.Itoa(int(req.Msg.Limit)))
	return unary[arlv1.ListRunEventsResponse](r, ctx, req.Header(), "GET", query(target, q), nil, "")
}

// FeedbackService

func (r rpc) ListFeedback(ctx context.Context, req *connect.Request[arlv1.ListFeedbackRequest]) (*connect.Response[arlv1.ListFeedbackResponse], error) {
	target, err := at("/api/agent-lab/runs/%s/feedback", req.Msg.TraceId)
	if err != nil {
		return nil, err
	}
	return unary[arlv1.ListFeedbackResponse](r, ctx, req.Header(), "GET", target, nil, "feedback")
}
func (r rpc) CreateFeedback(ctx context.Context, req *connect.Request[arlv1.CreateFeedbackRequest]) (*connect.Response[arlv1.CreateFeedbackResponse], error) {
	return unary[arlv1.CreateFeedbackResponse](r, ctx, req.Header(), "POST", "/api/feedback", req.Msg, "feedback")
}
func (r rpc) DeleteFeedback(ctx context.Context, req *connect.Request[arlv1.DeleteFeedbackRequest]) (*connect.Response[arlv1.DeleteFeedbackResponse], error) {
	target, err := at("/api/feedback/%s", req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return unary[arlv1.DeleteFeedbackResponse](r, ctx, req.Header(), "DELETE", target, nil, "")
}

// PromptService

func (r rpc) GetPrompt(ctx context.Context, req *connect.Request[arlv1.GetPromptRequest]) (*connect.Response[arlv1.GetPromptResponse], error) {
	target, err := at("/api/prompts/%s", req.Msg.Name)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	set(q, "label", req.Msg.Label)
	set(q, "version", strconv.Itoa(int(req.Msg.Version)))
	return unary[arlv1.GetPromptResponse](r, ctx, req.Header(), "GET", query(target, q), nil, "prompt")
}
func (r rpc) RenderPrompt(ctx context.Context, req *connect.Request[arlv1.RenderPromptRequest]) (*connect.Response[arlv1.RenderPromptResponse], error) {
	target, err := at("/api/prompts/%s/render", req.Msg.Name)
	if err != nil {
		return nil, err
	}
	return unary[arlv1.RenderPromptResponse](r, ctx, req.Header(), "POST", target, req.Msg, "")
}

// grpc.health.v1: NOT_SERVING while the database is unreachable or the
// service's module is disabled.

func (r rpc) Check(ctx context.Context, req *grpchealth.CheckRequest) (*grpchealth.CheckResponse, error) {
	feature, known := rpcServices[req.Service]
	if req.Service != "" && !known {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown service %q", req.Service))
	}
	if r.s.DB.Ready(ctx) != nil || (feature != "" && !r.s.Config.Features.On(feature)) {
		return &grpchealth.CheckResponse{Status: grpchealth.StatusNotServing}, nil
	}
	return &grpchealth.CheckResponse{Status: grpchealth.StatusServing}, nil
}

// TelemetryService

func (r rpc) Connect(ctx context.Context, stream *connect.BidiStream[arlv1.ConnectRequest, arlv1.ConnectResponse]) error {
	s := r.s
	if !s.Config.Features.On(features.Traces) {
		return connect.NewError(connect.CodeUnimplemented, errors.New(s.Config.Features.Reason(features.Traces)))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stopped, idled atomic.Value
	a := s.newAgentConn(ctx, stream.RequestHeader().Get("X-Lab-Project"), func(_ context.Context, msg M) error {
		frame, err := labFrame(msg)
		if err != nil {
			return err
		}
		return stream.Send(frame)
	}, func(reason string) { stopped.Store(reason); cancel() })
	defer a.close()
	idle := time.AfterFunc(a.idle(), func() { idled.Store(true); cancel() })
	defer idle.Stop()
	for {
		req, err := stream.Receive()
		if err != nil {
			switch {
			case stopped.Load() != nil:
				return connect.NewError(connect.CodeUnavailable, errors.New(stopped.Load().(string)))
			case idled.Load() != nil:
				return connect.NewError(connect.CodeDeadlineExceeded, errors.New("no frame within the idle timeout; send ping frames"))
			case errors.Is(err, io.EOF):
				return nil
			}
			return err
		}
		idle.Stop()
		var reply M
		fatal := false
		if body, e := agentFrame(req); e != nil {
			reply, fatal = tracelab.ErrorMessage(e), a.sid == ""
		} else {
			reply, fatal = a.handle(body)
		}
		if err = a.write(reply); err != nil {
			return err
		}
		if fatal {
			return connect.NewError(connect.CodeInvalidArgument, errors.New(domain.Str(reply["message"])))
		}
		idle.Reset(a.idle())
	}
}

// agentFrame turns a typed frame into the arl/1 JSON the WebSocket carries.
func agentFrame(req *arlv1.ConnectRequest) ([]byte, error) {
	var kind string
	var msg proto.Message
	switch f := req.GetFrame().(type) {
	case *arlv1.ConnectRequest_Hello:
		kind, msg = "hello", f.Hello
	case *arlv1.ConnectRequest_Event:
		kind, msg = "event", f.Event
	case *arlv1.ConnectRequest_Ping:
		kind, msg = "ping", f.Ping
	case *arlv1.ConnectRequest_ReplayStatus:
		kind, msg = "replay.status", f.ReplayStatus
	default:
		return nil, tracelab.Errorf("validation_error", "Empty frame")
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return nil, tracelab.Errorf("validation_error", "Invalid frame")
	}
	var m M
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, tracelab.Errorf("validation_error", "Invalid frame")
	}
	m["type"] = kind
	return domain.JSON(m), nil
}

// labFrame turns an arl/1 JSON frame from the lab into its typed message.
func labFrame(msg M) (*arlv1.ConnectResponse, error) {
	body := domain.Copy(msg)
	delete(body, "type")
	data := domain.JSON(body)
	decode := protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal
	out := &arlv1.ConnectResponse{}
	var err error
	switch msg["type"] {
	case "hello_ack":
		f := &arlv1.HelloAck{}
		err, out.Frame = decode(data, f), &arlv1.ConnectResponse_HelloAck{HelloAck: f}
	case "ack":
		f := &arlv1.Ack{}
		err, out.Frame = decode(data, f), &arlv1.ConnectResponse_Ack{Ack: f}
	case "pong":
		f := &arlv1.Pong{}
		err, out.Frame = decode(data, f), &arlv1.ConnectResponse_Pong{Pong: f}
	case "error":
		f := &arlv1.ProtocolError{}
		err, out.Frame = decode(data, f), &arlv1.ConnectResponse_Error{Error: f}
	case "replay":
		f := &arlv1.Replay{}
		err, out.Frame = decode(data, f), &arlv1.ConnectResponse_Replay{Replay: f}
	default:
		err = fmt.Errorf("no RPC frame for %q", msg["type"])
	}
	return out, err
}
