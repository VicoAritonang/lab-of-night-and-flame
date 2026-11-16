/**
 * TypeScript SDK for Agent Reliability Lab (protocol arl/1).
 *
 * Streams the functional evidence of agent runs to a Lab over one WebSocket and
 * lets the Lab Workbench replay saved scenarios with different models, prompts
 * or state. Enabled only when STAGE=dev and LAB_WS_URL are set; fail-open by
 * default. Uses the global WebSocket (Node.js >= 22, Bun, Deno, browsers).
 */

export const PROTOCOL = "arl/1";
export const DEFAULT_PHASES = ["input", "retrieval", "agent", "output"];

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
export type Obj = Record<string, any>;
export type Part = { type: "text"; text: string } | { type: "json"; value: unknown } | { type: "image"; artifact: string; alt?: string } | Obj;

export const text = (value: string): Part => ({ type: "text", text: value });
export const jsonPart = (value: unknown): Part => ({ type: "json", value });
export const newId = (): string => (globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(16)}${Math.random().toString(16).slice(2)}`).replace(/-/g, "");

export interface Ack { type: string; event_id?: string; trace_id?: string; ordinal?: number; duplicate?: boolean }

export class ProtocolError extends Error {
  constructor(public code: string, message = "") { super(`lab rejected telemetry: ${code} ${message}`.trim()); }
}

export interface LabOptions {
  url?: string; stage?: string; token?: string; project?: string; profile?: "agent" | "router" | "generic";
  phases?: string[]; capabilities?: string[]; metadata?: Obj;
  /** Surface connection and acknowledgement failures (strict tests). Default fail-open. */
  required?: boolean;
  timeoutMs?: number;
  /** Custom WebSocket constructor, e.g. from the "ws" package on Node < 22. */
  WebSocket?: any;
  /** Custom fetch for the REST calls (feedback, prompts); defaults to globalThis.fetch. */
  fetch?: typeof fetch;
  logger?: Pick<Console, "warn">;
}

/** A Workbench request to run the agent again from a saved scenario. */
export class Replay {
  replay_id: string; scenario_id: string; source_trace_id: string; name: string;
  input: unknown; state: Obj; config: Obj; checks: Obj;
  constructor(m: Obj) {
    this.replay_id = m.replay_id ?? ""; this.scenario_id = m.scenario_id ?? ""; this.source_trace_id = m.source_trace_id ?? "";
    this.name = m.name ?? ""; this.input = m.input; this.state = m.state ?? {}; this.config = m.config ?? {}; this.checks = m.checks ?? {};
  }
  get<T = unknown>(key: string, fallback: T): T { const v = this.config[key]; return v === undefined || v === null || v === "" ? fallback : v; }
  model(fallback: string): string { return this.get("model", fallback); }
  systemPrompt(fallback: string): string { return this.get("system_prompt", fallback); }
  param<T = unknown>(key: string, fallback: T): T { const v = (this.config.params ?? {})[key]; return v === undefined || v === null ? fallback : v; }
  inputText(fallback = ""): string { return typeof this.input === "string" && this.input ? this.input : fallback; }
}

export type ReplayHandler = (replay: Replay, lab: Lab) => unknown | Promise<unknown>;

const env = (key: string): string => {
  const g = globalThis as any;
  return g.process?.env?.[key] ?? g.Deno?.env?.get?.(key) ?? "";
};

interface Waiter { resolve: (m: Obj) => void; reject: (e: Error) => void; timer: any }

/** Connection to an Agent Reliability Lab. */
export class Lab {
  readonly service: string;
  readonly phases: string[];
  enabled: boolean;
  private o: Required<Pick<LabOptions, "url" | "stage" | "token" | "project" | "profile" | "capabilities" | "metadata" | "required" | "timeoutMs">> & LabOptions;
  private ws: any = null;
  private waiters: Waiter[] = [];
  private chain: Promise<unknown> = Promise.resolve();
  private session = "";
  private resume = "";
  private retryAt = 0;
  private backoff = 0;
  private handler: ReplayHandler | null = null;
  private timer: any = null;
  private closed = false;
  private warned = false;

  constructor(service: string, options: LabOptions = {}) {
    this.service = service;
    this.o = {
      ...options,
      url: options.url ?? env("LAB_WS_URL"), stage: options.stage ?? env("STAGE"), token: options.token ?? env("LAB_TOKEN"),
      project: options.project ?? env("LAB_PROJECT_ID"), profile: options.profile ?? "agent",
      capabilities: options.capabilities ?? [], metadata: { environment: environment(), ...(options.metadata ?? {}) },
      required: options.required ?? env("LAB_REQUIRED") === "true", timeoutMs: options.timeoutMs ?? 5000,
    };
    this.phases = options.phases?.length ? options.phases : DEFAULT_PHASES;
    this.enabled = this.o.stage.toLowerCase() === "dev" && !!this.o.url;
  }

  /** Serializes all frames: one outstanding request, replies in order. */
  private locked<T>(fn: () => Promise<T>): Promise<T> {
    const next = this.chain.then(fn, fn);
    this.chain = next.catch(() => undefined);
    return next;
  }

  private hello(): Obj {
    const caps = [...this.o.capabilities];
    if (this.handler && !caps.includes("replay")) caps.push("replay");
    const h: Obj = { type: "hello", protocol: PROTOCOL, service: this.service, stage: "dev", lab: this.o.profile, phases: this.phases, capabilities: caps, metadata: this.o.metadata };
    if (this.o.project) h.project_id = this.o.project;
    if (this.session) { h.session_id = this.session; h.resume_token = this.resume; }
    return h;
  }

  private fail(err: Error) { const ws = this.waiters.splice(0); ws.forEach((w) => { clearTimeout(w.timer); w.reject(err); }); }

  private drop() {
    const ws = this.ws;
    this.ws = null;
    if (ws) { try { ws.close(); } catch { /* ignore */ } }
    this.fail(new Error("lab connection closed"));
  }

  private exchange(payload: Obj): Promise<Obj> {
    return new Promise<Obj>((resolve, reject) => {
      const ws = this.ws;
      if (!ws) return reject(new Error("lab connection closed"));
      const timer = setTimeout(() => { this.waiters = this.waiters.filter((w) => w.timer !== timer); reject(new Error("lab acknowledgement timed out")); }, this.o.timeoutMs);
      this.waiters.push({ resolve, reject, timer });
      try { ws.send(JSON.stringify(payload)); } catch (e) { clearTimeout(timer); this.waiters.pop(); reject(e as Error); }
    }).then((m: Obj) => { if (m.type === "error") throw new ProtocolError(String(m.code), String(m.message ?? "")); return m; });
  }

  private async open(): Promise<void> {
    this.drop();
    const WS = this.o.WebSocket ?? (globalThis as any).WebSocket;
    if (!WS) throw new Error("No WebSocket implementation; use Node.js >= 22 or pass options.WebSocket");
    // Browsers cannot set headers; Node (undici), Bun, Deno and "ws" accept an init object.
    const ws = this.o.token ? new WS(this.o.url, { headers: { Authorization: `Bearer ${this.o.token}` } }) : new WS(this.o.url);
    await new Promise<void>((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("lab connection timed out")), 3000);
      ws.onopen = () => { clearTimeout(t); resolve(); };
      ws.onerror = () => { clearTimeout(t); reject(new Error("cannot connect to lab")); };
    });
    this.ws = ws;
    ws.onmessage = (ev: { data: unknown }) => {
      let m: Obj;
      try { m = JSON.parse(typeof ev.data === "string" ? ev.data : String(ev.data)); } catch { return; }
      if (m.type === "replay") { void this.dispatch(new Replay(m)); return; }
      const w = this.waiters.shift();
      if (w) { clearTimeout(w.timer); w.resolve(m); }
    };
    ws.onclose = () => { if (this.ws === ws) { this.ws = null; this.fail(new Error("lab connection closed")); } };
    let reply: Obj;
    try {
      reply = await this.exchange(this.hello());
    } catch (e) {
      if (!(e instanceof ProtocolError && e.code === "invalid_resume" && this.session)) { this.drop(); throw e; }
      this.session = this.resume = "";
      reply = await this.exchange(this.hello());
    }
    if (reply.type !== "hello_ack") { this.drop(); throw new Error("expected hello_ack"); }
    this.session = reply.session_id;
    this.resume = reply.resume_token ?? this.resume;
    this.backoff = 0; this.retryAt = 0;
  }

  private failure(err: Error): void {
    this.drop();
    this.backoff = this.backoff ? Math.min(this.backoff * 2, 30000) : 1000;
    this.retryAt = Date.now() + this.backoff;
    if (this.o.required) throw err;
    if (!this.warned) { (this.o.logger ?? console).warn(`[arl] lab telemetry unavailable (${err.message}); the job continues without a complete trace`); this.warned = true; }
  }

  /** Opens the connection and starts the heartbeat. With a replay handler the client keeps reconnecting. */
  connect(): Promise<this> {
    return this.locked(async () => {
      if (!this.enabled) return this;
      if (!this.timer) {
        this.timer = setInterval(() => void this.maintain(), 20000);
        this.timer.unref?.();
      }
      try { await this.open(); } catch (e) { this.failure(e as Error); }
      return this;
    });
  }

  private maintain(): Promise<void> {
    return this.locked(async () => {
      if (!this.enabled || this.closed) return;
      if (this.ws) { try { await this.exchange({ type: "ping" }); } catch { this.drop(); } return; }
      if (this.handler && Date.now() >= this.retryAt) { try { await this.open(); } catch (e) { try { this.failure(e as Error); } catch { /* strict mode: keep trying */ } } }
    });
  }

  close(): void {
    this.closed = true; this.enabled = false;
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    this.drop();
  }

  private async send(payload: Obj): Promise<Obj> {
    for (let attempt = 0; ; attempt++) {
      try {
        if (!this.ws) await this.open();
        return await this.exchange(payload);
      } catch (e) {
        if (e instanceof ProtocolError || attempt === 1) throw e;
        this.drop();
        await new Promise((r) => setTimeout(r, 250));
      }
    }
  }

  /** Sends one event and resolves once the Lab committed it (empty ack when disabled/unavailable). */
  emit(runId: string, event: string, phase: string, data: Obj = {}, opts: { eventId?: string; spanId?: string; parentSpanId?: string } = {}): Promise<Ack> {
    return this.locked(async () => {
      if (!this.enabled) return { type: "disabled" };
      if (!this.ws && Date.now() < this.retryAt) {
        if (this.o.required) throw new Error("lab unavailable");
        return { type: "skipped" };
      }
      const payload: Obj = { type: "event", event_id: opts.eventId ?? newId(), run_id: runId, event, phase, timestamp: Date.now() / 1000, data };
      if (opts.spanId) payload.span_id = opts.spanId;
      if (opts.parentSpanId) payload.parent_span_id = opts.parentSpanId;
      try {
        return (await this.send(payload)) as Ack;
      } catch (e) {
        if (e instanceof ProtocolError) {
          if (this.o.required) throw e;
          (this.o.logger ?? console).warn(`[arl] lab rejected event ${event}: ${e.message}`);
          return { type: "rejected" };
        }
        this.failure(e as Error);
        return { type: "skipped" };
      }
    });
  }

  /** Starts a run; pass the replay when running inside a replay handler. */
  async startRun(name: string, opts: { runId?: string; checks?: Obj; data?: Obj; replay?: Replay | null; threadId?: string; userId?: string; tags?: string[] } = {}): Promise<Run> {
    const run = new Run(this, opts.runId ?? newId());
    const data: Obj = { name, ...(opts.data ?? {}) };
    if (opts.threadId) data.thread_id = opts.threadId;
    if (opts.userId) data.user_id = opts.userId;
    if (opts.tags?.length) data.tags = opts.tags;
    const checks: Obj = { ...(opts.checks ?? {}) };
    if (opts.replay) {
      data.replay = { replay_id: opts.replay.replay_id, scenario_id: opts.replay.scenario_id, source_trace_id: opts.replay.source_trace_id };
      Object.assign(checks, opts.replay.checks);
    }
    if (Object.keys(checks).length) data.checks = checks;
    const ack = await this.emit(run.id, "run.start", run.phase("input"), data);
    run.traceId = ack.trace_id ?? null;
    return run;
  }

  /** Registers the function the Workbench calls to re-run this agent. Register before connect(). */
  onReplay(handler: ReplayHandler): void {
    const reconnect = !this.handler && !!this.ws;
    this.handler = handler;
    if (reconnect) void this.locked(async () => { this.drop(); try { await this.open(); } catch (e) { try { this.failure(e as Error); } catch { /* retried later */ } } });
  }

  private async status(replayId: string, status: string, error = ""): Promise<void> {
    await this.locked(async () => {
      if (!this.enabled) return;
      try { await this.send({ type: "replay.status", replay_id: replayId, status, ...(error ? { error } : {}) }); }
      catch { (this.o.logger ?? console).warn(`[arl] cannot report replay ${replayId} status`); }
    });
  }

  private async dispatch(r: Replay): Promise<void> {
    if (!r.replay_id) return;
    if (!this.handler) return this.status(r.replay_id, "rejected", "no replay handler registered");
    await this.status(r.replay_id, "started");
    try { await this.handler(r, this); }
    catch (e) { return this.status(r.replay_id, "failed", e instanceof Error ? `${e.name}: ${e.message}` : String(e)); }
    await this.status(r.replay_id, "completed");
  }

  private async http(method: string, path: string, body?: Obj): Promise<any> {
    const doFetch = this.o.fetch ?? globalThis.fetch;
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (this.o.token) headers.Authorization = `Bearer ${this.o.token}`;
    if (this.o.project) headers["X-Lab-Project"] = this.o.project;
    const res = await doFetch(this.httpBase() + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(this.o.timeoutMs * 2) });
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(`lab HTTP ${res.status}: ${typeof data?.detail === "string" ? data.detail : "request failed"}`);
    return data;
  }

  /** Attaches feedback (e.g. an end user's thumbs-up) to a run identified by traceId or by your own runId. */
  async feedback(name: string, value?: number | boolean | null, opts: { traceId?: string; runId?: string; label?: string; comment?: string; source?: "user" | "human" | "sdk"; author?: string; metadata?: Obj } = {}): Promise<Obj | null> {
    if (!this.enabled) return null;
    const body: Obj = { name, value: value ?? null, label: opts.label, comment: opts.comment, source: opts.source ?? "user", metadata: opts.metadata ?? {} };
    if (opts.author) body.author = opts.author;
    if (opts.traceId) body.trace_id = opts.traceId; else { body.run_id = opts.runId; body.service = this.service; }
    try { return await this.http("POST", "/api/feedback", body); }
    catch (e) {
      if (this.o.required) throw e;
      (this.o.logger ?? console).warn(`[arl] feedback not recorded: ${(e as Error).message}`);
      return null;
    }
  }

  private prompts = new Map<string, { at: number; prompt: Prompt }>();

  /**
   * Fetches a prompt version from the lab registry: the "production" label by default, or a label/version.
   * `fallback` (system prompt string or {system, messages}) is used when the lab is disabled or unreachable.
   */
  async prompt(name: string, opts: { label?: string; version?: number; fallback?: string | { system?: string; messages?: Obj[]; config?: Obj }; cacheMs?: number } = {}): Promise<Prompt> {
    const key = JSON.stringify([name, opts.label ?? "", opts.version ?? 0]);
    const hit = this.prompts.get(key);
    if (hit && Date.now() - hit.at < (opts.cacheMs ?? 60000)) return hit.prompt;
    if (this.enabled) {
      const query = opts.version ? `?version=${opts.version}` : opts.label ? `?label=${encodeURIComponent(opts.label)}` : "";
      try {
        const prompt = Prompt.fromVersion(await this.http("GET", `/api/prompts/${encodeURIComponent(name)}${query}`));
        this.prompts.set(key, { at: Date.now(), prompt });
        return prompt;
      } catch (e) {
        if (this.o.required && opts.fallback === undefined) throw e;
        (this.o.logger ?? console).warn(`[arl] prompt ${name} unavailable, using fallback: ${(e as Error).message}`);
      }
    }
    if (opts.fallback === undefined) throw new Error(`prompt ${name} is unavailable and no fallback was given`);
    const t = typeof opts.fallback === "string" ? { system: opts.fallback } : opts.fallback;
    return new Prompt(name, null, t.system ?? "", t.messages ?? [], t.config ?? {});
  }

  /** Derives the Lab's HTTP origin from the WebSocket URL. */
  httpBase(): string { return this.o.url.replace(/^wss:/, "https:").replace(/^ws:/, "http:").replace(/\/api\/agent-lab\/ws$/, ""); }
}

const parts = (content: unknown): Part[] => typeof content === "string" ? [text(content)] : Array.isArray(content) ? content.map((p) => (p && typeof p === "object" && "type" in p ? p : jsonPart(p))) : content && typeof content === "object" && "type" in content ? [content as Part] : [jsonPart(content)];

export interface ModelCall {
  /** OpenAI-compatible chat payload (model, messages, temperature, tools, ...). */
  request: Obj; response?: string; provider?: string; model?: string;
  tokensIn?: number; tokensOut?: number; costUsd?: number; durationMs?: number; toolCalls?: unknown; error?: unknown;
  spanId?: string; parentSpanId?: string;
}

/** One agent job. Helpers pick phases from the handshake: first phase for input/state, "agent" (or the middle) for model calls, last phase for output. */
export class Run {
  traceId: string | null = null;
  private started = Date.now();
  private ended = false;
  constructor(readonly lab: Lab, readonly id: string) {}

  phase(role: string): string {
    const p = this.lab.phases;
    if (role === "input") return p[0];
    if (role === "output") return p[p.length - 1];
    return p.includes(role) ? role : p[Math.floor(p.length / 2)];
  }
  emit(event: string, phase: string, data: Obj = {}, opts?: { eventId?: string; spanId?: string; parentSpanId?: string }) { return this.lab.emit(this.id, event, phase, data, opts); }
  input(content: unknown) { return this.emit("input", this.phase("input"), { content: parts(content) }); }
  /** Records the functional state needed to replay: state = variables/memory/context, config = model, system_prompt, params. */
  snapshot(state: Obj = {}, config: Obj = {}) { return this.emit("state.snapshot", this.phase("input"), { state, config }); }
  async modelCall(c: ModelCall): Promise<void> {
    const span = c.spanId ?? `model-${newId().slice(0, 8)}`;
    const model = c.model ?? String(c.request.model ?? "");
    const opts = { spanId: span, parentSpanId: c.parentSpanId };
    const phase = this.phase("agent");
    const req: Obj = { model, request: c.request };
    if (c.provider) req.provider = c.provider;
    const last = Array.isArray(c.request.messages) ? c.request.messages[c.request.messages.length - 1] : undefined;
    if (typeof last?.content === "string") req.content = [text(last.content)];
    await this.emit("model.request", phase, req, opts);
    const res: Obj = { model, content: [text(c.response ?? "")] };
    const cost: Obj = { billing_id: span, model };
    if (c.toolCalls !== undefined) res.tool_calls = c.toolCalls;
    if (c.durationMs !== undefined) res.duration_ms = c.durationMs;
    if (c.error !== undefined) { res.status = "error"; res.error = String(c.error); }
    if (c.tokensIn !== undefined && c.tokensOut !== undefined) { res.tokens_in = cost.tokens_in = Math.round(c.tokensIn); res.tokens_out = cost.tokens_out = Math.round(c.tokensOut); }
    await this.emit("model.response", phase, res, opts);
    if (c.costUsd !== undefined) { cost.amount_usd = c.costUsd; cost.source = "reported"; }
    await this.emit("cost.record", phase, cost, opts);
  }
  async tool(name: string, args: unknown, result: unknown, error?: unknown): Promise<void> {
    const call = `call-${newId().slice(0, 8)}`;
    await this.emit("tool.call", this.phase("agent"), { tool: name, call_id: call, arguments: args }, { spanId: call });
    await this.emit("tool.result", this.phase("agent"), { call_id: call, status: error ? "error" : "ok", result, ...(error ? { error: String(error) } : {}) }, { spanId: call });
  }
  output(content: unknown) { return this.emit("output", this.phase("output"), { content: parts(content) }); }
  /** Attaches feedback (e.g. an automated self-check) to this run. */
  feedback(name: string, value?: number | boolean | null, opts: Parameters<Lab["feedback"]>[2] = {}) {
    return this.lab.feedback(name, value, { source: "sdk", ...opts, ...(this.traceId ? { traceId: this.traceId } : { runId: this.id }) });
  }
  async end(status: "completed" | "failed" | "cancelled" = "completed"): Promise<void> {
    if (this.ended) return;
    this.ended = true;
    await this.emit("run.end", this.phase("output"), { status, duration_ms: Date.now() - this.started });
  }
  /** Ends as failed with the error, or completed. */
  async finish(error?: unknown): Promise<void> {
    if (error) { await this.emit("error", this.phase("output"), { reason: error instanceof Error ? `${error.name}: ${error.message}` : String(error) }); return this.end("failed"); }
    return this.end("completed");
  }
}

const VARIABLE = /\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}/g;

/** Runtime facts for comparing runs across machines (no host names). */
function environment(): Obj {
  const g = globalThis as any;
  const p = g.process;
  if (p?.versions?.node) return { runtime: `node ${p.versions.node}`, os: p.platform, arch: p.arch };
  if (g.Deno) return { runtime: `deno ${g.Deno.version?.deno}`, os: g.Deno.build?.os, arch: g.Deno.build?.arch };
  if (g.Bun) return { runtime: `bun ${g.Bun.version}` };
  return { runtime: "js" };
}

/** A prompt version from the lab registry (or a local fallback). */
export class Prompt {
  constructor(readonly name: string, readonly version: number | null, readonly system: string, readonly messages: Obj[], readonly config: Obj) {}
  static fromVersion(v: Obj): Prompt {
    const t = v.template ?? {};
    return new Prompt(v.prompt ?? "", v.version ?? null, t.system ?? "", t.messages ?? [], v.config ?? {});
  }
  /** Record this in run.snapshot(state, { prompt: p.ref }) to know which version ran. */
  get ref(): Obj { return { name: this.name, version: this.version }; }
  private static fill(text: string, vars: Obj): string {
    return text.replace(VARIABLE, (token, path: string) => {
      let v: any = vars;
      for (const part of path.split(".")) {
        if (v === null || typeof v !== "object" || !(part in v)) return token;
        v = v[part];
      }
      return typeof v === "string" ? v : JSON.stringify(v);
    });
  }
  renderSystem(vars: Obj = {}): string { return Prompt.fill(this.system, vars); }
  /** Chat messages with {{variables}} filled in. */
  render(vars: Obj = {}): Obj[] {
    const out: Obj[] = this.system ? [{ role: "system", content: Prompt.fill(this.system, vars) }] : [];
    return out.concat(this.messages.map((m) => ({ role: m.role, content: Prompt.fill(String(m.content ?? ""), vars) })));
  }
}

/** Runs fn inside a run and ends it as completed or failed. */
export async function withRun<T>(lab: Lab, name: string, fn: (run: Run) => Promise<T>, opts: Parameters<Lab["startRun"]>[1] = {}): Promise<T> {
  const run = await lab.startRun(name, opts);
  try { const out = await fn(run); await run.finish(); return out; }
  catch (e) { await run.finish(e); throw e; }
}
