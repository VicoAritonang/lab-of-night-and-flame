"""A small support agent instrumented with the arl-lab Python SDK.

    LAB_WS_URL=ws://localhost:8000/api/agent-lab/ws STAGE=dev python agent.py

It answers one question on start, serves POST /ask on :8082 and accepts
Workbench replays. Without LLM_API_KEY it uses a deterministic mock model, so
the whole flow works offline; set LLM_API_KEY (and optionally LLM_BASE_URL,
LLM_MODEL) to call any OpenAI-compatible endpoint such as OpenRouter.
"""

import json
import os
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from arl_lab import Lab

KNOWLEDGE = {
    "refund": "Refunds are issued within 14 days to the original payment method.",
    "shipping": "Standard shipping takes 3-5 business days; express takes 1-2.",
    "warranty": "Hardware has a 2-year limited warranty covering manufacturing defects.",
}
DEFAULTS = {
    "model": os.getenv("LLM_MODEL", "mock/support-v1"),
    "system_prompt": "You are a concise support agent. Answer only from the context.",
    "params": {"temperature": 0.2},
}

lab = Lab("example/python-support-agent", capabilities=["tools", "costs"])


def complete(request: dict) -> tuple[str, int, int]:
    key = os.getenv("LLM_API_KEY")
    if not key:
        system, user = request["messages"][0]["content"], request["messages"][-1]["content"]
        context = user.split("\n\nQuestion:")[0].removeprefix("Context:\n") or "I don't know."
        answer = f"[{request['model']}] {context}"
        if "friendly" in system.lower():
            answer = "Happy to help! " + answer
        return answer, len(system + user) // 4, len(answer) // 4
    req = urllib.request.Request(
        os.getenv("LLM_BASE_URL", "https://openrouter.ai/api/v1") + "/chat/completions",
        data=json.dumps(request).encode(),
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=120) as res:
        body = json.loads(res.read())
    usage = body.get("usage", {})
    return body["choices"][0]["message"]["content"], usage.get("prompt_tokens", 0), usage.get("completion_tokens", 0)


def answer(question: str, config: dict) -> str:
    with lab.run("Support question", checks={"max_latency_ms": 15000}) as run:
        run.input(question)
        # Everything a replay needs to reproduce this run.
        run.snapshot({"knowledge_topics": sorted(KNOWLEDGE)}, config)
        facts = [fact for topic, fact in KNOWLEDGE.items() if topic in question.lower()]
        run.tool("knowledge.search", {"query": question}, facts)
        request = {
            "model": config["model"],
            "temperature": config.get("params", {}).get("temperature", 0.2),
            "messages": [
                {"role": "system", "content": config["system_prompt"]},
                {"role": "user", "content": "Context:\n" + "\n".join(facts) + "\n\nQuestion: " + question},
            ],
        }
        started = time.monotonic()
        text, tokens_in, tokens_out = complete(request)
        run.model_call(request, text, tokens_in=tokens_in, tokens_out=tokens_out, duration_ms=(time.monotonic() - started) * 1000)
        run.output(text)
        return text


@lab.on_replay
def replay(r):
    """Called by the Workbench with the edited scenario."""
    config = {
        "model": r.model(DEFAULTS["model"]),
        "system_prompt": r.system_prompt(DEFAULTS["system_prompt"]),
        "params": {**DEFAULTS["params"], **(r.config.get("params") or {})},
    }
    answer(r.input_text("What is your refund policy?"), config)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/ask":
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        text = answer(body.get("question", ""), DEFAULTS)
        payload = json.dumps({"answer": text}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(payload)


if __name__ == "__main__":
    lab.connect(wait=60)  # in docker compose the Lab may still be starting
    print("answer:", answer("What is your refund policy?", DEFAULTS), flush=True)
    port = int(os.getenv("AGENT_PORT", "8082"))
    print(f"python support agent on :{port} (POST /ask); waiting for Workbench replays", flush=True)
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
