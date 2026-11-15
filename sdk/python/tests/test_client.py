import json
import threading

import pytest
from websockets.sync.server import serve

from arl_lab import Lab, ProtocolError, text


class FakeLab:
    """Minimal arl/1 server: acks events, optionally pushes one replay."""

    def __init__(self, push_replay=False, reject=None):
        self.events, self.statuses, self.hellos = [], [], []
        self.push_replay, self.reject = push_replay, reject
        self.done = threading.Event()
        self.server = serve(self.handle, "127.0.0.1", 0)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.url = f"ws://127.0.0.1:{self.server.socket.getsockname()[1]}/api/agent-lab/ws"

    def handle(self, ws):
        for raw in ws:
            m = json.loads(raw)
            if m["type"] == "hello":
                self.hellos.append(m)
                ws.send(json.dumps({"type": "hello_ack", "session_id": "s1", "resume_token": "r1"}))
                if self.push_replay:
                    ws.send(json.dumps({"type": "replay", "replay_id": "rpl-1", "source_trace_id": "t0", "input": "hi", "config": {"model": "m-2", "params": {"temperature": 0.9}}, "checks": {"max_retries": 0}}))
                continue
            if m["type"] == "replay.status":
                self.statuses.append(m["status"])
                ws.send(json.dumps({"type": "ack", "replay_id": m["replay_id"]}))
                if m["status"] in ("completed", "failed"):
                    self.done.set()
                continue
            if m["type"] == "ping":
                ws.send(json.dumps({"type": "pong"}))
                continue
            if self.reject and m["event"] == self.reject:
                ws.send(json.dumps({"type": "error", "code": "unknown_phase", "message": "nope"}))
                continue
            self.events.append(m)
            ws.send(json.dumps({"type": "ack", "event_id": m["event_id"], "trace_id": "trace-1", "ordinal": len(self.events)}))

    def close(self):
        self.server.shutdown()


@pytest.fixture
def lab_server():
    servers = []

    def make(**kw):
        s = FakeLab(**kw)
        servers.append(s)
        return s

    yield make
    for s in servers:
        s.close()


def test_run_helpers_emit_protocol_events(lab_server):
    server = lab_server()
    lab = Lab("py-agent", url=server.url, stage="dev", required=True).connect()
    with lab.run("demo", checks={"output_contains": ["ok"]}) as run:
        run.input("question")
        run.snapshot({"memory": 1}, {"model": "m-1", "system_prompt": "s"})
        run.model_call({"model": "m-1", "messages": [{"role": "user", "content": "q"}]}, "ok", tokens_in=3, tokens_out=1)
        run.tool("search", {"q": "x"}, ["r"])
        run.output("ok")
    lab.close()
    kinds = [e["event"] for e in server.events]
    assert kinds == ["run.start", "input", "state.snapshot", "model.request", "model.response", "cost.record", "tool.call", "tool.result", "output", "run.end"]
    assert run.trace_id == "trace-1"
    assert server.events[3]["data"]["request"]["messages"][0]["content"] == "q"
    assert server.events[-1]["data"]["status"] == "completed"
    assert {e["phase"] for e in server.events} <= {"input", "agent", "output"}


def test_exception_marks_run_failed(lab_server):
    server = lab_server()
    lab = Lab("py-agent", url=server.url, stage="dev", required=True).connect()
    with pytest.raises(ValueError):
        with lab.run("boom"):
            raise ValueError("bad")
    lab.close()
    assert server.events[-1]["data"]["status"] == "failed"
    assert server.events[-2]["event"] == "error"


def test_replay_runs_handler_and_links_run(lab_server):
    server = lab_server(push_replay=True)
    lab = Lab("py-agent", url=server.url, stage="dev", required=True)
    seen = {}

    @lab.on_replay
    def replay(r):
        seen["model"], seen["temperature"] = r.model("m-1"), r.param("temperature")
        with lab.run("replayed") as run:
            run.input(r.input_text())
            run.output(text("ok"))

    lab.connect()
    assert server.done.wait(5)
    lab.close()
    assert "replay" in server.hellos[0]["capabilities"]
    assert seen == {"model": "m-2", "temperature": 0.9}
    start = server.events[0]["data"]
    assert start["replay"]["replay_id"] == "rpl-1" and start["checks"]["max_retries"] == 0
    assert server.statuses == ["started", "completed"]


def test_rejected_event_is_fail_open_unless_required(lab_server):
    server = lab_server(reject="input")
    lab = Lab("py-agent", url=server.url, stage="dev").connect()
    run = lab.run("x")
    run.input("dropped")
    run.end()
    assert [e["event"] for e in server.events] == ["run.start", "run.end"]
    lab.close()
    strict = Lab("py-agent", url=server.url, stage="dev", required=True).connect()
    with pytest.raises(ProtocolError):
        strict.run("y").input("rejected")
    strict.close()


def test_disabled_outside_dev_and_unreachable_lab_is_fail_open():
    assert Lab("x", url="ws://127.0.0.1:1/ws", stage="production").connect().run("r").trace_id is None
    lab = Lab("x", url="ws://127.0.0.1:1/ws", stage="dev").connect()
    with lab.run("still works") as run:
        run.output("fine")
    lab.close()
