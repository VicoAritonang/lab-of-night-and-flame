"""Agent Reliability Lab client (protocol arl/1).

Streams the functional evidence of agent runs to a Lab over one WebSocket and
lets the Lab Workbench replay saved scenarios with different models, prompts
or state. Telemetry is enabled only when STAGE=dev and LAB_WS_URL is set; by
default the client is fail-open so an unavailable Lab never breaks the job.
"""

from __future__ import annotations

import contextvars
import json
import logging
import os
import queue
import re
import threading
import time
import traceback
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional


PROTOCOL = "arl/1"
DEFAULT_PHASES = ["input", "retrieval", "agent", "output"]
log = logging.getLogger("arl_lab")
_replay: contextvars.ContextVar[Optional["Replay"]] = contextvars.ContextVar("arl_replay", default=None)


class ProtocolError(Exception):
    """The Lab rejected a frame, for example an undeclared phase."""

    def __init__(self, code: str, message: str = ""):
        super().__init__(f"lab rejected telemetry: {code} {message}".strip())
        self.code = code


class _Closed(Exception):
    pass


def new_id() -> str:
    return uuid.uuid4().hex


def text(value: str) -> Dict[str, Any]:
    return {"type": "text", "text": value}


def json_part(value: Any) -> Dict[str, Any]:
    return {"type": "json", "value": value}


@dataclass
class Replay:
    """A Workbench request to run the agent again from a saved scenario."""

    replay_id: str
    scenario_id: str = ""
    source_trace_id: str = ""
    name: str = ""
    input: Any = None
    state: Dict[str, Any] = field(default_factory=dict)
    config: Dict[str, Any] = field(default_factory=dict)
    checks: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_message(cls, m: Dict[str, Any]) -> "Replay":
        return cls(
            replay_id=m.get("replay_id", ""),
            scenario_id=m.get("scenario_id") or "",
            source_trace_id=m.get("source_trace_id") or "",
            name=m.get("name") or "",
            input=m.get("input"),
            state=m.get("state") or {},
            config=m.get("config") or {},
            checks=m.get("checks") or {},
        )

    def get(self, key: str, default: Any = None) -> Any:
        value = self.config.get(key)
        return default if value in (None, "") else value

    def model(self, default: str) -> str:
        return self.get("model", default)

    def system_prompt(self, default: str) -> str:
        return self.get("system_prompt", default)

    def param(self, key: str, default: Any = None) -> Any:
        value = (self.config.get("params") or {}).get(key)
        return default if value is None else value

    def input_text(self, default: str = "") -> str:
        return self.input if isinstance(self.input, str) and self.input else default


def current_replay() -> Optional[Replay]:
    """The replay the current code is running for, if any."""
    return _replay.get()


ReplayHandler = Callable[[Replay], Any]


class Lab:
    """Connection to an Agent Reliability Lab.

    >>> lab = Lab("my-agent")            # reads LAB_WS_URL, STAGE, LAB_TOKEN
    >>> @lab.on_replay
    ... def replay(r): answer(r.input_text(), model=r.model("gpt-4o-mini"))
    >>> lab.connect()
    >>> with lab.run("Answer question") as run:
    ...     run.input("What is the refund policy?")
    """

    def __init__(
        self,
        service: str,
        *,
        url: Optional[str] = None,
        stage: Optional[str] = None,
        token: Optional[str] = None,
        project: Optional[str] = None,
        profile: str = "agent",
        phases: Optional[List[str]] = None,
        capabilities: Optional[List[str]] = None,
        metadata: Optional[Dict[str, Any]] = None,
        required: Optional[bool] = None,
        timeout: float = 5.0,
    ):
        self.service = service
        self.url = url or os.getenv("LAB_WS_URL", "")
        self.stage = stage or os.getenv("STAGE", "")
        self.token = token or os.getenv("LAB_TOKEN", "")
        self.project = project or os.getenv("LAB_PROJECT_ID", "")
        self.profile = profile
        self.phases = list(phases or DEFAULT_PHASES)
        self.capabilities = list(capabilities or [])
        self.metadata = dict(metadata or {})
        self.metadata.setdefault("environment", _environment())
        self._prompts: Dict[Any, Any] = {}
        self.required = os.getenv("LAB_REQUIRED") == "true" if required is None else required
        self.timeout = timeout
        self.enabled = self.stage.lower() == "dev" and bool(self.url)
        self._lock = threading.RLock()
        self._ws = None
        self._replies: "queue.Queue[Any]" = queue.Queue()
        self._session = ""
        self._resume = ""
        self._retry_at = 0.0
        self._backoff = 0.0
        self._handler: Optional[ReplayHandler] = None
        self._stop = threading.Event()
        self._wake = threading.Event()
        self._maintainer: Optional[threading.Thread] = None

    # -- connection -----------------------------------------------------
    def _hello(self) -> Dict[str, Any]:
        caps = list(self.capabilities)
        if self._handler and "replay" not in caps:
            caps.append("replay")
        hello = {"type": "hello", "protocol": PROTOCOL, "service": self.service, "stage": "dev", "lab": self.profile, "phases": self.phases, "capabilities": caps, "metadata": self.metadata}
        if self.project:
            hello["project_id"] = self.project
        if self._session:
            hello["session_id"], hello["resume_token"] = self._session, self._resume
        return hello

    def _reader(self, ws, replies: "queue.Queue[Any]") -> None:
        try:
            for raw in ws:
                try:
                    msg = json.loads(raw)
                except ValueError:
                    continue
                if msg.get("type") == "replay":
                    threading.Thread(target=self._dispatch, args=(msg,), daemon=True, name="arl-replay").start()
                else:
                    replies.put(msg)
        except Exception:
            pass
        finally:
            replies.put(_Closed())
            self._wake.set()

    def _drop(self) -> None:
        if self._ws is not None:
            try:
                self._ws.close()
            except Exception:
                pass
            self._ws = None

    def _exchange(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        self._ws.send(json.dumps(payload, default=str))
        try:
            reply = self._replies.get(timeout=self.timeout)
        except queue.Empty:
            raise TimeoutError("lab acknowledgement timed out")
        if isinstance(reply, _Closed):
            raise ConnectionError("lab connection closed")
        if reply.get("type") == "error":
            raise ProtocolError(str(reply.get("code")), str(reply.get("message", "")))
        return reply

    def _connect(self) -> None:
        self._drop()
        headers = {"Authorization": f"Bearer {self.token}"} if self.token else None
        # Imported on first connect: a disabled client (production, no LAB_WS_URL)
        # never loads the WebSocket stack.
        from websockets.sync.client import connect as ws_connect

        ws = ws_connect(self.url, additional_headers=headers, open_timeout=3, max_size=2 << 20)
        ws.__enter__()  # long-lived connection; closed explicitly in _drop()
        self._ws, self._replies = ws, queue.Queue()
        threading.Thread(target=self._reader, args=(ws, self._replies), daemon=True, name="arl-reader").start()
        try:
            reply = self._exchange(self._hello())
        except ProtocolError as e:
            if e.code != "invalid_resume" or not self._session:
                self._drop()
                raise
            # The Lab database was reset: start a fresh session.
            self._session = self._resume = ""
            reply = self._exchange(self._hello())
        except Exception:
            self._drop()
            raise
        if reply.get("type") != "hello_ack":
            self._drop()
            raise ConnectionError("expected hello_ack")
        self._session = reply["session_id"]
        self._resume = reply.get("resume_token") or self._resume
        self._backoff, self._retry_at = 0.0, 0.0

    def _failure(self, err: Exception) -> None:
        self._drop()
        self._backoff = 1.0 if not self._backoff else min(self._backoff * 2, 30.0)
        self._retry_at = time.monotonic() + self._backoff
        if self.required:
            raise err
        log.warning("lab telemetry unavailable (%s); the job continues without a complete trace", err)

    @property
    def connected(self) -> bool:
        return self._ws is not None

    def connect(self, wait: float = 0) -> "Lab":
        """Open the connection and start the heartbeat. With a replay handler the
        client keeps reconnecting so the Lab can always reach it. ``wait`` retries
        for that many seconds first (useful when the Lab starts alongside)."""
        deadline = time.monotonic() + wait
        with self._lock:
            if not self.enabled:
                return self
            if self._maintainer is None:
                self._maintainer = threading.Thread(target=self._maintain, daemon=True, name="arl-heartbeat")
                self._maintainer.start()
            while True:
                try:
                    self._connect()
                    break
                except Exception as e:
                    if time.monotonic() + 1 < deadline:
                        time.sleep(1)
                        continue
                    self._failure(e)
                    break
        return self

    def _maintain(self) -> None:
        while not self._stop.is_set():
            woke = self._wake.wait(20)
            if self._stop.is_set():
                return
            if woke:
                self._wake.clear()
                time.sleep(1)
            with self._lock:
                if not self.enabled:
                    continue
                if self._ws is not None:
                    try:
                        self._exchange({"type": "ping"})
                    except Exception:
                        self._drop()
                elif self._handler and time.monotonic() >= self._retry_at:
                    try:
                        self._connect()
                    except Exception as e:
                        try:
                            self._failure(e)
                        except Exception:
                            pass

    def close(self) -> None:
        self._stop.set()
        self._wake.set()
        with self._lock:
            self.enabled = False
            self._drop()

    def __enter__(self) -> "Lab":
        return self.connect()

    def __exit__(self, *exc) -> None:
        self.close()

    def _send(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        for attempt in range(2):
            try:
                if self._ws is None:
                    self._connect()
                return self._exchange(payload)
            except ProtocolError:
                raise
            except Exception:
                self._drop()
                if attempt == 1:
                    raise
                time.sleep(0.25)
        raise ConnectionError("lab connection closed")

    # -- events ---------------------------------------------------------
    def emit(self, run_id: str, event: str, phase: str, data: Optional[Dict[str, Any]] = None, *, event_id: Optional[str] = None, span_id: Optional[str] = None, parent_span_id: Optional[str] = None) -> Dict[str, Any]:
        """Send one event and wait until the Lab committed it. Returns the ack
        (empty when telemetry is disabled or unavailable)."""
        with self._lock:
            if not self.enabled:
                return {}
            if self._ws is None and time.monotonic() < self._retry_at:
                if self.required:
                    raise ConnectionError("lab unavailable")
                return {}
            payload: Dict[str, Any] = {"type": "event", "event_id": event_id or new_id(), "run_id": run_id, "event": event, "phase": phase, "timestamp": time.time(), "data": data or {}}
            if span_id:
                payload["span_id"] = span_id
            if parent_span_id:
                payload["parent_span_id"] = parent_span_id
            try:
                return self._send(payload)
            except ProtocolError as e:
                if self.required:
                    raise
                log.warning("lab rejected event %s: %s", event, e)
                return {}
            except Exception as e:
                self._failure(e)
                return {}

    def run(self, name: str, *, run_id: Optional[str] = None, checks: Optional[Dict[str, Any]] = None, data: Optional[Dict[str, Any]] = None, thread_id: Optional[str] = None, user_id: Optional[str] = None, tags: Optional[List[str]] = None) -> "Run":
        """Start a run. Use as a context manager to end it automatically.

        ``thread_id`` groups the turns of one conversation, ``user_id`` the
        runs of one end user, and ``tags`` label runs for filtering."""
        extra = dict(data or {})
        for key, value in (("thread_id", thread_id), ("user_id", user_id), ("tags", list(tags) if tags else None)):
            if value:
                extra[key] = value
        return Run(self, name, run_id=run_id, checks=checks, data=extra)

    def _http(self, method: str, path: str, body: Optional[Dict[str, Any]] = None) -> Any:
        import urllib.request

        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.http_base() + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("Authorization", f"Bearer {self.token}")
        if self.project:
            req.add_header("X-Lab-Project", self.project)
        with urllib.request.urlopen(req, timeout=self.timeout * 2) as res:
            return json.loads(res.read() or b"null")

    def feedback(self, name: str, value: Any = None, *, trace_id: Optional[str] = None, run_id: Optional[str] = None, label: Optional[str] = None, comment: Optional[str] = None, source: str = "user", author: Optional[str] = None, metadata: Optional[Dict[str, Any]] = None) -> Optional[Dict[str, Any]]:
        """Attach feedback to a run, e.g. an end user's thumbs-up.

        Identify the run by ``trace_id`` or by your own ``run_id`` (the id
        passed to :meth:`run`). ``value`` is a number or boolean."""
        if not self.enabled:
            return None
        body: Dict[str, Any] = {"name": name, "value": value, "label": label, "comment": comment, "source": source, "metadata": metadata or {}}
        if author:
            body["author"] = author
        if trace_id:
            body["trace_id"] = trace_id
        else:
            body["run_id"], body["service"] = run_id, self.service
        try:
            return self._http("POST", "/api/feedback", body)
        except Exception as e:
            if self.required:
                raise
            log.warning("lab feedback not recorded: %s", e)
            return None

    def prompt(self, name: str, *, label: Optional[str] = None, version: Optional[int] = None, fallback: Any = None, cache_seconds: float = 60.0) -> "Prompt":
        """Fetch a prompt version from the lab's registry.

        Without ``label``/``version`` the "production" label is used (else the
        latest version). ``fallback`` (a system prompt string or a
        ``{"system": ..., "messages": [...]}`` template) is returned when the lab
        is disabled or unreachable, so production code keeps working."""
        key = (name, label, version)
        cached = self._prompts.get(key)
        if cached and time.monotonic() - cached[0] < cache_seconds:
            return cached[1]
        if self.enabled:
            query = f"?version={int(version)}" if version else (f"?label={label}" if label else "")
            try:
                found = Prompt.from_version(self._http("GET", f"/api/prompts/{name}{query}"))
                self._prompts[key] = (time.monotonic(), found)
                return found
            except Exception as e:
                if self.required and fallback is None:
                    raise
                log.warning("lab prompt %s unavailable, using fallback: %s", name, e)
        if fallback is None:
            raise LookupError(f"prompt {name!r} is unavailable and no fallback was given")
        template = {"system": fallback} if isinstance(fallback, str) else dict(fallback)
        return Prompt(name=name, version=None, system=template.get("system", ""), messages=list(template.get("messages", [])), config=dict(template.get("config", {})))

    def upload_image(self, name: str, content: bytes) -> Optional[Dict[str, Any]]:
        """Store an image so events can reference {"type": "image", "artifact": digest}."""
        if not self.enabled:
            return None
        import urllib.request

        boundary = new_id()
        body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\nContent-Type: application/octet-stream\r\n\r\n").encode() + content + f"\r\n--{boundary}--\r\n".encode()
        req = urllib.request.Request(self.http_base() + "/api/artifacts", data=body, method="POST")
        req.add_header("Content-Type", f"multipart/form-data; boundary={boundary}")
        if self.token:
            req.add_header("Authorization", f"Bearer {self.token}")
        if self.project:
            req.add_header("X-Lab-Project", self.project)
        with urllib.request.urlopen(req, timeout=110) as res:
            return json.loads(res.read())

    def http_base(self) -> str:
        base = self.url.replace("wss://", "https://", 1).replace("ws://", "http://", 1)
        return base[: -len("/api/agent-lab/ws")] if base.endswith("/api/agent-lab/ws") else base

    # -- replay ---------------------------------------------------------
    def on_replay(self, handler: ReplayHandler) -> ReplayHandler:
        """Register the function the Workbench calls to re-run this agent.
        Usable as a decorator; register before connect()."""
        with self._lock:
            reconnect = self._handler is None and self._ws is not None
            self._handler = handler
            if reconnect:
                self._drop()
                self._wake.set()
        return handler

    def _status(self, replay_id: str, status: str, error: str = "") -> None:
        with self._lock:
            if not self.enabled:
                return
            msg = {"type": "replay.status", "replay_id": replay_id, "status": status}
            if error:
                msg["error"] = error
            try:
                self._send(msg)
            except Exception:
                log.warning("cannot report replay %s status to the lab", replay_id)

    def _dispatch(self, msg: Dict[str, Any]) -> None:
        replay = Replay.from_message(msg)
        if not replay.replay_id:
            return
        handler = self._handler
        if handler is None:
            self._status(replay.replay_id, "rejected", "no replay handler registered")
            return
        self._status(replay.replay_id, "started")
        token = _replay.set(replay)
        try:
            result = handler(replay)
            if hasattr(result, "__await__"):
                import asyncio

                asyncio.run(_await(result))
        except Exception as e:
            log.debug("replay handler failed: %s", traceback.format_exc())
            self._status(replay.replay_id, "failed", f"{type(e).__name__}: {e}")
            return
        finally:
            _replay.reset(token)
        self._status(replay.replay_id, "completed")


async def _await(result: Any) -> Any:
    return await result


_VARIABLE = re.compile(r"\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}")


def _environment() -> Dict[str, Any]:
    """Runtime facts for comparing runs across machines (no host names)."""
    import platform

    env: Dict[str, Any] = {"runtime": "python " + platform.python_version(), "os": platform.system().lower(), "arch": platform.machine(), "cpus": os.cpu_count()}
    for var in ("CUDA_VISIBLE_DEVICES", "NVIDIA_VISIBLE_DEVICES"):
        if os.getenv(var):
            env["gpus"] = os.getenv(var)
    return env


@dataclass
class Prompt:
    """A prompt version from the lab registry (or a local fallback)."""

    name: str
    version: Optional[int]
    system: str = ""
    messages: List[Dict[str, Any]] = field(default_factory=list)
    config: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_version(cls, v: Dict[str, Any]) -> "Prompt":
        t = v.get("template") or {}
        return cls(name=v.get("prompt", ""), version=v.get("version"), system=t.get("system", ""), messages=list(t.get("messages") or []), config=dict(v.get("config") or {}))

    @property
    def ref(self) -> Dict[str, Any]:
        """Record this in ``run.snapshot(config=...)`` to know which version ran."""
        return {"name": self.name, "version": self.version}

    @staticmethod
    def _fill(text_value: str, variables: Dict[str, Any]) -> str:
        def sub(m: "re.Match[str]") -> str:
            value: Any = variables
            for part in m.group(1).split("."):
                if not isinstance(value, dict) or part not in value:
                    return m.group(0)
                value = value[part]
            return value if isinstance(value, str) else json.dumps(value)

        return _VARIABLE.sub(sub, text_value)

    def render_system(self, **variables: Any) -> str:
        return self._fill(self.system, variables)

    def render(self, **variables: Any) -> List[Dict[str, Any]]:
        """Chat messages with ``{{variables}}`` filled in."""
        out = [{"role": "system", "content": self._fill(self.system, variables)}] if self.system else []
        out += [{"role": m["role"], "content": self._fill(m["content"], variables)} for m in self.messages]
        return out


class Run:
    """One agent job. Helpers pick phases from the handshake: the first phase
    for input and state, "agent" (or the middle phase) for model calls and the
    last phase for output."""

    def __init__(self, lab: Lab, name: str, *, run_id: Optional[str] = None, checks: Optional[Dict[str, Any]] = None, data: Optional[Dict[str, Any]] = None):
        self.lab = lab
        self.id = run_id or new_id()
        self.started = time.monotonic()
        self.ended = False
        payload: Dict[str, Any] = {"name": name, **(data or {})}
        merged = dict(checks or {})
        replay = current_replay()
        if replay:
            payload["replay"] = {"replay_id": replay.replay_id, "scenario_id": replay.scenario_id, "source_trace_id": replay.source_trace_id}
            merged.update(replay.checks)
        if merged:
            payload["checks"] = merged
        ack = self.lab.emit(self.id, "run.start", self.phase("input"), payload)
        self.trace_id: Optional[str] = ack.get("trace_id")

    def phase(self, role: str) -> str:
        phases = self.lab.phases
        if role == "input":
            return phases[0]
        if role == "output":
            return phases[-1]
        return role if role in phases else phases[len(phases) // 2]

    def emit(self, event: str, phase: str, data: Optional[Dict[str, Any]] = None, **kw: Any) -> Dict[str, Any]:
        return self.lab.emit(self.id, event, phase, data, **kw)

    @staticmethod
    def _parts(content: Any) -> List[Dict[str, Any]]:
        if isinstance(content, str):
            return [text(content)]
        if isinstance(content, dict) and "type" in content:
            return [content]
        if isinstance(content, list):
            return [p if isinstance(p, dict) and "type" in p else json_part(p) for p in content]
        return [json_part(content)]

    def input(self, content: Any) -> None:
        self.emit("input", self.phase("input"), {"content": self._parts(content)})

    def snapshot(self, state: Optional[Dict[str, Any]] = None, config: Optional[Dict[str, Any]] = None) -> None:
        """Record the functional state needed to replay this run: ``state`` holds
        variables, memory or retrieved context; ``config`` holds model,
        system_prompt and params. The Workbench edits both before a replay."""
        self.emit("state.snapshot", self.phase("input"), {"state": state or {}, "config": config or {}})

    def model_call(self, request: Dict[str, Any], response: str = "", *, provider: str = "", model: str = "", tokens_in: Optional[int] = None, tokens_out: Optional[int] = None, cost_usd: Optional[float] = None, duration_ms: Optional[float] = None, tool_calls: Any = None, error: Optional[BaseException] = None, span_id: Optional[str] = None, parent_span_id: Optional[str] = None) -> None:
        """Record one LLM call. ``request`` is the OpenAI-compatible chat payload
        (model, messages, temperature, tools...) so the Workbench can re-send it."""
        span = span_id or f"model-{new_id()[:8]}"
        model = model or str(request.get("model", ""))
        phase = self.phase("agent")
        req: Dict[str, Any] = {"model": model, "request": request}
        if provider:
            req["provider"] = provider
        messages = request.get("messages") or []
        if messages and isinstance(messages[-1].get("content"), str):
            req["content"] = [text(messages[-1]["content"])]
        self.emit("model.request", phase, req, span_id=span, parent_span_id=parent_span_id)
        res: Dict[str, Any] = {"model": model, "content": [text(response or "")]}
        if tool_calls is not None:
            res["tool_calls"] = tool_calls
        if duration_ms is not None:
            res["duration_ms"] = duration_ms
        if error is not None:
            res["status"], res["error"] = "error", str(error)
        cost: Dict[str, Any] = {"billing_id": span, "model": model}
        if tokens_in is not None and tokens_out is not None:
            res["tokens_in"] = cost["tokens_in"] = int(tokens_in)
            res["tokens_out"] = cost["tokens_out"] = int(tokens_out)
        self.emit("model.response", phase, res, span_id=span, parent_span_id=parent_span_id)
        if cost_usd is not None:
            cost["amount_usd"], cost["source"] = float(cost_usd), "reported"
        self.emit("cost.record", phase, cost, span_id=span, parent_span_id=parent_span_id)

    def tool(self, name: str, arguments: Any = None, result: Any = None, error: Optional[BaseException] = None) -> None:
        call = f"call-{new_id()[:8]}"
        phase = self.phase("agent")
        self.emit("tool.call", phase, {"tool": name, "call_id": call, "arguments": arguments}, span_id=call)
        data: Dict[str, Any] = {"call_id": call, "status": "ok", "result": result}
        if error is not None:
            data["status"], data["error"] = "error", str(error)
        self.emit("tool.result", phase, data, span_id=call)

    def output(self, content: Any) -> None:
        self.emit("output", self.phase("output"), {"content": self._parts(content)})

    def feedback(self, name: str, value: Any = None, **kw: Any) -> Optional[Dict[str, Any]]:
        """Attach feedback (e.g. an automated self-check) to this run."""
        kw.setdefault("source", "sdk")
        if self.trace_id:
            return self.lab.feedback(name, value, trace_id=self.trace_id, **kw)
        return self.lab.feedback(name, value, run_id=self.id, **kw)

    def end(self, status: str = "completed") -> None:
        if self.ended:
            return
        self.ended = True
        self.emit("run.end", self.phase("output"), {"status": status, "duration_ms": (time.monotonic() - self.started) * 1000})

    def __enter__(self) -> "Run":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        if exc is not None:
            self.emit("error", self.phase("output"), {"reason": f"{exc_type.__name__}: {exc}"})
            self.end("failed")
        else:
            self.end("completed")
