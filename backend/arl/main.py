"""ASGI entrypoint: `uvicorn arl.main:app`."""
from __future__ import annotations

import asyncio
import contextlib
import logging
from pathlib import Path

from fastapi import FastAPI, HTTPException, Request
from fastapi.exceptions import RequestValidationError
from fastapi.middleware.gzip import GZipMiddleware
from fastapi.responses import FileResponse, ORJSONResponse, Response

from .api import datasets, experiments, system
from .api.deps import check_token
from .config import Settings, load_settings
from .context import Lab
from .datasets.builtin import ensure_builtin
from .datasets.store import ArtifactStore
from .db import Database
from .engine.events import EventBus
from .providers.registry import Registry

log = logging.getLogger("arl")


class SelectiveGZip:
    """GZip everything except streaming endpoints: compressing SSE buffers events and breaks live updates."""

    def __init__(self, app, minimum_size: int = 1024):
        self.app = app
        self.gzip = GZipMiddleware(app, minimum_size=minimum_size)

    async def __call__(self, scope, receive, send):
        if scope["type"] == "http" and (scope["path"].endswith("/events") or scope["path"] == "/api/playground"):
            await self.app(scope, receive, send)
        else:
            await self.gzip(scope, receive, send)
PUBLIC_API = ("/api/health", "/api/auth/")


def create_app(settings: Settings | None = None) -> FastAPI:
    settings = settings or load_settings()

    @contextlib.asynccontextmanager
    async def lifespan(app: FastAPI):
        db = Database(settings.db_path)
        await db.connect()
        registry = Registry(settings)
        lab = Lab(settings=settings, db=db, registry=registry,
                  artifacts=ArtifactStore(db, settings.artifact_dir), bus=EventBus())
        for row in await db.fetchall("SELECT * FROM connections"):
            registry.add_connection(row)
        recovered = await lab.recover()
        if recovered:
            log.warning("marked %d interrupted campaign(s) as requires_reconciliation", recovered)
        await ensure_builtin(db, lab.artifacts)
        lab.bus.start()
        app.state.lab = lab
        lab.spawn(_warm_catalog(registry))
        try:
            yield
        finally:
            for runner in list(lab.runners.values()):
                runner.cancel()
            if lab.tasks:
                await asyncio.wait(list(lab.tasks), timeout=5)
            await lab.bus.stop()
            await registry.close()
            await db.close()

    app = FastAPI(title="Lab of Night and Flame", version=settings.version, lifespan=lifespan,
                  default_response_class=ORJSONResponse, docs_url="/api/docs", openapi_url="/api/openapi.json")
    app.add_middleware(SelectiveGZip, minimum_size=1024)

    @app.middleware("http")
    async def guard(request: Request, call_next):
        path = request.url.path
        if path.startswith("/api/") and not path.startswith(PUBLIC_API) and path not in ("/api/docs", "/api/openapi.json"):
            if not check_token(request.app.state.lab, request):
                return ORJSONResponse({"detail": "authentication required"}, status_code=401)
        response: Response = await call_next(request)
        response.headers.setdefault("X-Content-Type-Options", "nosniff")
        response.headers.setdefault("Referrer-Policy", "no-referrer")
        if not path.startswith("/api/campaigns/") or not path.endswith("report.html"):
            response.headers.setdefault("X-Frame-Options", "DENY")
        return response

    @app.exception_handler(RequestValidationError)
    async def validation_error(_: Request, exc: RequestValidationError):
        errors = [f"{'.'.join(str(p) for p in e['loc'] if p != 'body')}: {e['msg']}" for e in exc.errors()]
        return ORJSONResponse({"detail": {"message": "validation failed", "errors": errors}}, status_code=422)

    app.include_router(system.router)
    app.include_router(datasets.router)
    app.include_router(experiments.router)

    static = settings.static_dir
    if static is not None:
        index = static / "index.html"

        @app.get("/{full_path:path}", include_in_schema=False)
        async def spa(full_path: str):
            if full_path.startswith("api/"):
                raise HTTPException(404)
            candidate = (static / full_path).resolve()
            if full_path and candidate.is_file() and Path(static.resolve()) in candidate.parents:
                cache = "public, max-age=31536000, immutable" if "/assets/" in f"/{full_path}" else "no-cache"
                return FileResponse(candidate, headers={"Cache-Control": cache})
            return FileResponse(index, headers={"Cache-Control": "no-cache"})

    return app


async def _warm_catalog(registry: Registry) -> None:
    with contextlib.suppress(Exception):
        await registry.full_catalog()


app = create_app()
