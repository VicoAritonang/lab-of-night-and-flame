// Package arl is the Go SDK for Agent Reliability Lab (protocol arl/1).
//
// It streams the functional evidence of an agent run (input, state snapshots,
// model calls, tools, output) to a Lab instance over one WebSocket, and lets
// the Lab ask the agent to replay a captured scenario with different models,
// prompts or state ("Postman for agents").
//
// Telemetry is only enabled when STAGE=dev and LAB_WS_URL is set. By default
// the client is fail-open: an unavailable Lab never breaks the business job.
package arl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const Protocol = "arl/1"

type Object = map[string]any
type Options struct {
	URL, Stage, Token, Project, Profile string
	Phases, Capabilities                []string
	Metadata                            Object
	// Required makes connection and acknowledgement failures visible to the caller
	// (strict integration tests). The default is fail-open.
	Required bool
	Timeout  time.Duration
}
type EventOptions struct{ EventID, SpanID, ParentSpanID string }
type Ack struct {
	Type      string `json:"type"`
	EventID   string `json:"event_id"`
	TraceID   string `json:"trace_id"`
	Ordinal   int64  `json:"ordinal"`
	Duplicate bool   `json:"duplicate"`
}

// ProtocolError is a rejection sent by the Lab, for example an undeclared phase.
type ProtocolError struct{ Code, Message string }

func (e *ProtocolError) Error() string { return "lab rejected telemetry: " + e.Code }

var errClosed = errors.New("lab connection closed")

type Client struct {
	mu             sync.Mutex
	opts           Options
	service        string
	conn           *websocket.Conn
	replies        chan Object
	enabled        bool
	session, token string
	retryAt        time.Time
	backoff        time.Duration
	handler        ReplayHandler
	wake           chan struct{}
	stop           context.CancelFunc
}

func NewID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

// New reads LAB_WS_URL, STAGE, LAB_TOKEN, LAB_PROJECT_ID and LAB_REQUIRED for
// any option left empty.
func New(service string, opts Options) *Client {
	if opts.URL == "" {
		opts.URL = os.Getenv("LAB_WS_URL")
	}
	if opts.Stage == "" {
		opts.Stage = os.Getenv("STAGE")
	}
	if opts.Token == "" {
		opts.Token = os.Getenv("LAB_TOKEN")
	}
	if opts.Project == "" {
		opts.Project = os.Getenv("LAB_PROJECT_ID")
	}
	if os.Getenv("LAB_REQUIRED") == "true" {
		opts.Required = true
	}
	if opts.Profile == "" {
		opts.Profile = "agent"
	}
	if len(opts.Phases) == 0 {
		opts.Phases = []string{"input", "retrieval", "agent", "output"}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	metadata := Object{"environment": environment()}
	for k, v := range opts.Metadata {
		metadata[k] = v
	}
	opts.Metadata = metadata
	return &Client{opts: opts, service: service, enabled: strings.EqualFold(opts.Stage, "dev") && opts.URL != "", wake: make(chan struct{}, 1)}
}

// Enabled reports whether telemetry is configured for this process.
func (c *Client) Enabled() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.enabled }

func (c *Client) hello() Object {
	caps := append([]string{}, c.opts.Capabilities...)
	if c.handler != nil && !contains(caps, "replay") {
		caps = append(caps, "replay")
	}
	h := Object{"type": "hello", "protocol": Protocol, "service": c.service, "stage": "dev", "lab": c.opts.Profile, "phases": c.opts.Phases, "capabilities": caps, "metadata": c.opts.Metadata}
	if c.opts.Project != "" {
		h["project_id"] = c.opts.Project
	}
	if c.session != "" {
		h["session_id"], h["resume_token"] = c.session, c.token
	}
	return h
}

// exchange sends one frame and waits for the Lab's reply. Callers hold c.mu, so
// at most one request is outstanding and replies arrive in order.
func (c *Client) exchange(ctx context.Context, payload Object) (Object, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	if err = c.conn.Write(bounded, websocket.MessageText, body); err != nil {
		return nil, err
	}
	select {
	case out, ok := <-c.replies:
		if !ok {
			return nil, errClosed
		}
		if out["type"] == "error" {
			return nil, &ProtocolError{fmt.Sprint(out["code"]), fmt.Sprint(out["message"])}
		}
		return out, nil
	case <-bounded.Done():
		return nil, bounded.Err()
	}
}

// read demultiplexes one connection: replay commands are dispatched, everything
// else is the reply to the outstanding request.
func (c *Client) read(conn *websocket.Conn, replies chan Object) {
	defer close(replies)
	defer func() {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}()
	for {
		_, body, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var m Object
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		if m["type"] == "replay" {
			go c.dispatch(m)
			continue
		}
		select {
		case replies <- m:
		default:
		}
	}
}

func (c *Client) drop() {
	if c.conn != nil {
		_ = c.conn.CloseNow()
		c.conn = nil
	}
}

func (c *Client) connect(ctx context.Context) error {
	c.drop()
	headers := http.Header{}
	if c.opts.Token != "" {
		headers.Set("Authorization", "Bearer "+c.opts.Token)
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(bounded, c.opts.URL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	conn.SetReadLimit(2 << 20)
	c.conn, c.replies = conn, make(chan Object, 4)
	go c.read(conn, c.replies)
	reply, err := c.exchange(ctx, c.hello())
	var protocol *ProtocolError
	if errors.As(err, &protocol) && protocol.Code == "invalid_resume" && c.session != "" {
		// The Lab database was reset: start a fresh session.
		c.session, c.token = "", ""
		reply, err = c.exchange(ctx, c.hello())
	}
	if err != nil {
		c.drop()
		return err
	}
	if reply["type"] != "hello_ack" {
		c.drop()
		return errors.New("expected hello_ack")
	}
	c.session = fmt.Sprint(reply["session_id"])
	if token, ok := reply["resume_token"].(string); ok {
		c.token = token
	}
	c.backoff, c.retryAt = 0, time.Time{}
	return nil
}

// failure applies the fail-open policy: log once, back off, and keep the job running.
func (c *Client) failure(err error) error {
	c.drop()
	if c.backoff == 0 {
		c.backoff = time.Second
	} else if c.backoff < 30*time.Second {
		c.backoff *= 2
	}
	c.retryAt = time.Now().Add(c.backoff)
	if c.opts.Required {
		return err
	}
	slog.Warn("lab telemetry unavailable; the job continues without a complete trace", "retry_in", c.backoff.String())
	return nil
}

// Connect opens the connection and starts the heartbeat. With a replay handler
// registered, the client also keeps reconnecting so the Lab can reach it.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return nil
	}
	err := c.connect(ctx)
	if c.stop == nil {
		background, cancel := context.WithCancel(context.Background())
		c.stop = cancel
		go c.maintain(background)
	}
	if err != nil {
		return c.failure(err)
	}
	return nil
}

func (c *Client) maintain(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.wake:
			// Connection dropped; give the Lab a moment before reconnecting.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		c.mu.Lock()
		if c.enabled {
			if c.conn != nil {
				if _, err := c.exchange(ctx, Object{"type": "ping"}); err != nil {
					c.drop()
				}
			} else if c.handler != nil && time.Now().After(c.retryAt) {
				if err := c.connect(ctx); err != nil {
					_ = c.failure(err)
				}
			}
		}
		c.mu.Unlock()
	}
}

func (c *Client) send(ctx context.Context, payload Object) (Object, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		if c.conn == nil {
			err = c.connect(ctx)
		}
		var reply Object
		if err == nil {
			reply, err = c.exchange(ctx, payload)
		}
		if err == nil {
			return reply, nil
		}
		var protocol *ProtocolError
		if errors.As(err, &protocol) {
			// The connection is healthy; only this frame was rejected.
			return nil, err
		}
		c.drop()
		if attempt == 1 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, errClosed
}

// Emit sends one event and waits for the Lab's acknowledgement (committed evidence).
// A lost acknowledgement is retried after reconnecting with the identical event_id.
func (c *Client) Emit(ctx context.Context, runID, event, phase string, data Object, options ...EventOptions) (Ack, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return Ack{}, nil
	}
	if c.conn == nil && time.Now().Before(c.retryAt) {
		if c.opts.Required {
			return Ack{}, errClosed
		}
		return Ack{}, nil
	}
	opt := EventOptions{}
	if len(options) > 0 {
		opt = options[0]
	}
	if opt.EventID == "" {
		opt.EventID = NewID()
	}
	if data == nil {
		data = Object{}
	}
	payload := Object{"type": "event", "event_id": opt.EventID, "run_id": runID, "event": event, "phase": phase, "timestamp": float64(time.Now().UnixMilli()) / 1000, "data": data}
	if opt.SpanID != "" {
		payload["span_id"] = opt.SpanID
	}
	if opt.ParentSpanID != "" {
		payload["parent_span_id"] = opt.ParentSpanID
	}
	reply, err := c.send(ctx, payload)
	if err != nil {
		var protocol *ProtocolError
		if errors.As(err, &protocol) {
			if c.opts.Required {
				return Ack{}, err
			}
			slog.Warn("lab rejected an event", "code", protocol.Code, "event", event, "detail", protocol.Message)
			return Ack{}, nil
		}
		return Ack{}, c.failure(err)
	}
	body, _ := json.Marshal(reply)
	var ack Ack
	err = json.Unmarshal(body, &ack)
	return ack, err
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
	c.enabled = false
	if c.conn != nil {
		err := c.conn.Close(websocket.StatusNormalClosure, "client closed")
		c.conn = nil
		return err
	}
	return nil
}

// UploadImage stores an image in the Lab so events can reference it as
// {"type":"image","artifact":"<digest>"}.
func (c *Client) UploadImage(ctx context.Context, name string, body []byte) (Object, error) {
	if len(body) > 20<<20 {
		return nil, errors.New("image exceeds 20 MiB")
	}
	if !c.Enabled() {
		return nil, nil
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	_, _ = part.Write(body)
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", c.HTTPBase()+"/api/artifacts", &form)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.Token)
	}
	if c.opts.Project != "" {
		req.Header.Set("X-Lab-Project", c.opts.Project)
	}
	res, err := (&http.Client{Timeout: 110 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("image upload HTTP %d", res.StatusCode)
	}
	var result Object
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result)
	return result, err
}

// HTTPBase derives the Lab's HTTP origin from the WebSocket URL.
func (c *Client) HTTPBase() string {
	base := strings.Replace(strings.Replace(c.opts.URL, "wss://", "https://", 1), "ws://", "http://", 1)
	return strings.TrimSuffix(base, "/api/agent-lab/ws")
}

func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
