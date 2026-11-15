# Go SDK for Agent Reliability Lab

```bash
go get github.com/VicoAritonang/lab-of-night-and-flame/sdk/go
```

```go
import arl "github.com/VicoAritonang/lab-of-night-and-flame/sdk/go"

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

Only dependency: `github.com/coder/websocket`. Fail-open by default
(`Options.Required` or `LAB_REQUIRED=true` for strict tests). A complete agent is
in [examples/go-agent](../../examples/go-agent/main.go).
