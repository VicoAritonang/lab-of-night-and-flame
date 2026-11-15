# Go SDK for Agent Reliability Lab

```bash
go get go.vicoworks.com/arl@latest
```

```go
import "go.vicoworks.com/arl"

lab := arl.New("support-agent", arl.Options{}) // reads LAB_WS_URL, STAGE, LAB_TOKEN
lab.OnReplay(func(ctx context.Context, r *arl.Replay) error {
	return answer(ctx, r.InputText(""), Config{Model: r.Model(defaultModel), SystemPrompt: r.SystemPrompt(defaultPrompt)})
})
_ = lab.Connect(ctx)
defer lab.Close()

func answer(ctx context.Context, q string, cfg Config) error {
	run := lab.StartRun(ctx, "Support question") // linked automatically inside a replay ctx
	run.Input(arl.Text(q))
	run.Snapshot(arl.Object{"memory": memory}, arl.Object{"model": cfg.Model, "system_prompt": cfg.SystemPrompt})
	req := arl.Object{"model": cfg.Model, "messages": []arl.Object{{"role": "system", "content": cfg.SystemPrompt}, {"role": "user", "content": q}}}
	text, err := callLLM(ctx, req)
	run.ModelCall(arl.ModelCall{Request: req, Response: text, Error: err})
	run.Output(arl.Text(text))
	run.Finish(err)
	return err
}
```

Run the agent with `STAGE=dev LAB_WS_URL=ws://localhost:8000/api/agent-lab/ws`
(inside docker compose: `ws://arl:8000/api/agent-lab/ws`). Without `STAGE=dev`
every call is a no-op. Fail-open by default; `Options.Required` or
`LAB_REQUIRED=true` makes failures visible for strict tests.

- Requires Go 1.23+. Only dependency: `github.com/coder/websocket`.
- `go.vicoworks.com/arl` is a vanity path for
  [github.com/VicoAritonang/lab-of-night-and-flame/sdk/go](https://github.com/VicoAritonang/lab-of-night-and-flame/tree/main/sdk/go).
  With the default `GOPROXY` it works on any supported Go version; with
  `GOPROXY=direct` it needs Go 1.25+.
- A complete agent is in [examples/go-agent](../../examples/go-agent/main.go);
  the protocol is in [connection.md](../../connection.md).
