import asyncio
import os
import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
# Canary secret: tests assert it never leaks into exports/logs.
os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-CANARYSECRET0123456789"
os.environ["OPENAI_API_KEY"] = ""
os.environ["GROQ_API_KEY"] = ""
os.environ.pop("ARL_AUTH_TOKEN", None)

from arl.config import load_settings  # noqa: E402
from arl.main import create_app  # noqa: E402

CANARY = "CANARYSECRET0123456789"


@pytest.fixture
async def client(tmp_path, monkeypatch):
    monkeypatch.setenv("ARL_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("ARL_STATIC_DIR", str(tmp_path / "nostatic"))
    app = create_app(load_settings())
    async with app.router.lifespan_context(app):
        transport = httpx.ASGITransport(app=app)
        async with httpx.AsyncClient(transport=transport, base_url="http://test", timeout=60) as c:
            c.app = app
            yield c


async def wait_campaign(client, cid, timeout=60, until=("completed", "cancelled", "paused", "failed")):
    loop = asyncio.get_running_loop()
    end = loop.time() + timeout
    c = {}
    while loop.time() < end:
        c = (await client.get(f"/api/campaigns/{cid}")).json()
        if c["status"] in until and not c["live"]:
            return c
        await asyncio.sleep(0.05)
    raise AssertionError(f"campaign {cid} did not finish: {c.get('status')}")


async def dataset_id(client, name):
    ds = (await client.get("/api/datasets")).json()
    return next(d["id"] for d in ds if d["name"] == name)
