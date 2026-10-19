import { useRef, useState } from "react";
import { api, postStream } from "../api";
import { ModelPicker } from "../components/ModelPicker";
import { Badge, Card, CopyButton, Icon, Modal, useToast } from "../components/ui";
import { ms, num, seriesColor, usd } from "../format";
import { useLocalState } from "../hooks";

interface Target { provider: string; model: string; image?: boolean }
interface Out { text: string; status: "idle" | "running" | "done" | "error"; result?: any; error?: string; started?: number }

const DEFAULT_TARGETS: Target[] = [{ provider: "fake", model: "fake/echo" }];

export default function Playground() {
  const [targets, setTargets] = useLocalState<Target[]>("arl-pg-targets", DEFAULT_TARGETS);
  const [system, setSystem] = useLocalState("arl-pg-system", "You are a precise assistant.");
  const [prompt, setPrompt] = useLocalState("arl-pg-prompt", "Explain the difference between pass@k and all-k consistency in two sentences.");
  const [temperature, setTemperature] = useLocalState("arl-pg-temp", 0.2);
  const [maxTokens, setMaxTokens] = useLocalState("arl-pg-max", 512);
  const [images, setImages] = useState<{ digest: string; url: string; w: number; h: number }[]>([]);
  const [outs, setOuts] = useState<Out[]>([]);
  const [picker, setPicker] = useState(false);
  const [running, setRunning] = useState(false);
  const [over, setOver] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const toast = useToast();

  const upload = async (files: FileList | File[]) => {
    for (const f of Array.from(files).slice(0, 4 - images.length)) {
      const fd = new FormData();
      fd.append("file", f);
      try {
        const r = await api.form<{ digest: string; width: number; height: number }>("/api/artifacts", fd);
        setImages((xs) => [...xs, { digest: r.digest, url: `/api/artifacts/${r.digest}`, w: r.width, h: r.height }]);
      } catch (e: any) { toast(e.message, "bad"); }
    }
  };

  const run = async () => {
    if (!targets.length) return toast("Add at least one model", "bad");
    abort.current?.abort();
    const ac = new AbortController();
    abort.current = ac;
    setRunning(true);
    const now = performance.now();
    setOuts(targets.map(() => ({ text: "", status: "running", started: now })));
    try {
      await postStream("/api/playground", {
        system, prompt, images: images.map((i) => i.digest),
        targets: targets.map((t) => ({ provider: t.provider, model: t.model, params: { temperature, max_tokens: maxTokens } })),
      }, (ev) => {
        if (ev.type === "delta") setOuts((xs) => xs.map((o, i) => (i === ev.i ? { ...o, text: o.text + ev.text } : o)));
        else if (ev.type === "done") setOuts((xs) => xs.map((o, i) => (i === ev.i ? { ...o, text: ev.result.text, status: "done", result: ev.result } : o)));
        else if (ev.type === "error") setOuts((xs) => xs.map((o, i) => (i === ev.i ? { ...o, status: "error", error: `${ev.category}: ${ev.message}` } : o)));
      }, ac.signal);
    } catch (e: any) {
      if (e.name !== "AbortError") toast(e.message, "bad");
    } finally { setRunning(false); }
  };

  const done = outs.filter((o) => o.status === "done" && o.result);
  const maxLat = Math.max(1, ...done.map((o) => o.result.latency_ms ?? 0));
  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">Playground</h1>
          <div className="page-sub">One prompt, many models, side by side. Streaming output with TTFT, latency, tokens/s and cost per call. Not a substitute for a frozen experiment.</div>
        </div>
      </div>
      <div className="grid" style={{ gridTemplateColumns: "minmax(300px, 380px) minmax(0, 1fr)", alignItems: "start" }}>
        <Card title="Request">
          <div className="col" style={{ gap: 12 }}>
            <div className="field"><label>Models ({targets.length}/8)</label>
              <div className="row wrap" style={{ gap: 6 }}>
                {targets.map((t, i) => (
                  <span key={`${t.provider}:${t.model}`} className="badge" style={{ height: 24, paddingRight: 3 }}>
                    <span className="swatch" style={{ background: seriesColor(i) }} />
                    <span className="trunc" style={{ maxWidth: 190 }}>{t.model}</span>
                    <button className="btn sm ghost icon" style={{ width: 18, height: 18 }} aria-label="remove" onClick={() => setTargets(targets.filter((_, j) => j !== i))}><Icon name="x" size={11} /></button>
                  </span>
                ))}
                <button className="btn sm" onClick={() => setPicker(true)}><Icon name="plus" size={13} />Add</button>
              </div>
            </div>
            <div className="field"><label>System</label><textarea className="textarea" rows={2} value={system} onChange={(e) => setSystem(e.target.value)} /></div>
            <div className="field"><label>Prompt <span className="faint">· Ctrl+Enter to run</span></label>
              <textarea className="textarea" rows={7} value={prompt} onChange={(e) => setPrompt(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) run(); }} />
            </div>
            <div className="field"><label>Images (optional, max 4 · stored by digest, never resized)</label>
              <div className={`dropzone ${over ? "over" : ""}`} onDragOver={(e) => { e.preventDefault(); setOver(true); }} onDragLeave={() => setOver(false)}
                onDrop={(e) => { e.preventDefault(); setOver(false); upload(e.dataTransfer.files); }}
                onClick={() => { const i = document.createElement("input"); i.type = "file"; i.accept = "image/*"; i.multiple = true; i.onchange = () => i.files && upload(i.files); i.click(); }}>
                <Icon name="image" /> Drop or click to add images
              </div>
              {images.length > 0 && <div className="row wrap">{images.map((im) => (
                <div key={im.digest} style={{ position: "relative" }}>
                  <img className="thumb" src={im.url} alt="" title={`${im.w}×${im.h}`} />
                  <button className="btn sm icon" style={{ position: "absolute", top: -6, right: -6, width: 20, height: 20, borderRadius: 99 }} onClick={() => setImages(images.filter((x) => x.digest !== im.digest))}><Icon name="x" size={10} /></button>
                </div>
              ))}</div>}
            </div>
            <div className="grid g2">
              <div className="field"><label>Temperature · {temperature}</label><input type="range" min={0} max={1.5} step={0.1} value={temperature} onChange={(e) => setTemperature(+e.target.value)} /></div>
              <div className="field"><label>Max tokens</label><input className="input" type="number" min={1} max={8192} value={maxTokens} onChange={(e) => setMaxTokens(+e.target.value)} /></div>
            </div>
            <div className="row">
              {running ? <button className="btn danger" onClick={() => abort.current?.abort()}><Icon name="stop" />Stop</button>
                : <button className="btn primary grow" onClick={run}><Icon name="play" />Run on {targets.length} model{targets.length === 1 ? "" : "s"}</button>}
            </div>
          </div>
        </Card>

        <div className="col" style={{ gap: 12 }}>
          {done.length > 1 && (
            <Card title="Latency comparison" sub="End-to-end (bar) and time-to-first-token (marker), this request only — n=1, not a benchmark">
              <div className="col" style={{ gap: 8 }}>
                {outs.map((o, i) => o.result ? (
                  <div key={i} className="row" style={{ gap: 10 }}>
                    <span className="trunc" style={{ width: 200 }}>{targets[i]?.model}</span>
                    <div style={{ flex: 1, position: "relative", height: 12, background: "var(--plate-3)", borderRadius: 2 }}>
                      <div className="bar-grow" style={{ position: "absolute", inset: 0, width: `${(o.result.latency_ms / maxLat) * 100}%`, background: seriesColor(i), borderRadius: 2 }} />
                      {o.result.ttft_ms != null && <div style={{ position: "absolute", top: -3, bottom: -3, width: 2, left: `${(o.result.ttft_ms / maxLat) * 100}%`, background: "var(--ink)" }} />}
                    </div>
                    <span className="num" style={{ width: 70, textAlign: "right" }}>{ms(o.result.latency_ms)}</span>
                  </div>
                ) : null)}
              </div>
            </Card>
          )}
          <div className="pg-grid">
            {(outs.length ? outs : targets.map(() => ({ text: "", status: "idle" } as Out))).map((o, i) => {
              const t = targets[i];
              if (!t) return null;
              const r = o.result;
              const tps = r && r.tokens_out && r.latency_ms ? r.tokens_out / Math.max(0.001, (r.latency_ms - (r.ttft_ms ?? 0)) / 1000) : null;
              return (
                <Card key={i} title={<span className="row" style={{ gap: 8 }}><span className="swatch" style={{ background: seriesColor(i) }} /><span className="trunc">{t.model}</span></span>}
                  sub={<span className="mono">{t.provider}{r?.resolved_model && r.resolved_model !== t.model ? ` → ${r.resolved_model}` : ""}</span>}
                  actions={<>{o.status === "running" && <Badge tone="live" dot>streaming</Badge>}{o.status === "error" && <Badge tone="bad">error</Badge>}{o.status === "done" && <CopyButton text={o.text} />}</>}>
                  {o.status === "error" ? <div className="callout bad mono" style={{ fontSize: 12 }}>{o.error}</div> : (
                    <div className={`pg-out ${o.status === "running" ? "caret" : ""}`}>{o.text || (o.status === "idle" ? <span className="faint">Output will stream here.</span> : "")}</div>
                  )}
                  {r && (
                    <>
                      <div className="divider" />
                      <dl className="kv" style={{ gridTemplateColumns: "repeat(3, max-content 1fr)", fontSize: 12 }}>
                        <dt>TTFT</dt><dd className="num">{ms(r.ttft_ms)}</dd>
                        <dt>Latency</dt><dd className="num">{ms(r.latency_ms)}</dd>
                        <dt>Tok/s</dt><dd className="num">{tps ? num(tps, 0) : "—"}</dd>
                        <dt>In</dt><dd className="num">{num(r.tokens_in)}</dd>
                        <dt>Out</dt><dd className="num">{num(r.tokens_out)}{r.reasoning_tokens ? <span className="faint"> ({r.reasoning_tokens} reasoning)</span> : null}</dd>
                        <dt>Cost</dt><dd className="num" title={r.cost_source ?? "unknown"}>{usd(r.cost_usd)}</dd>
                      </dl>
                    </>
                  )}
                </Card>
              );
            })}
          </div>
        </div>
      </div>

      <Modal open={picker} onClose={() => setPicker(false)} title="Add models to compare" footer={<button className="btn primary" onClick={() => setPicker(false)}>Done</button>}>
        <ModelPicker selected={targets} requireImage={images.length > 0}
          onToggle={(m) => {
            const exists = targets.some((t) => t.provider === m.provider && t.model === m.id);
            setTargets(exists ? targets.filter((t) => !(t.provider === m.provider && t.model === m.id)) : [...targets, { provider: m.provider, model: m.id, image: m.modalities.includes("image") }]);
          }} />
      </Modal>
    </div>
  );
}
