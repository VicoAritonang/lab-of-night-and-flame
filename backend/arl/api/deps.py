from __future__ import annotations

import hmac
from typing import Any

from fastapi import HTTPException, Request
from fastapi.responses import ORJSONResponse

from ..context import Lab

COOKIE = "arl_token"


def lab(request: Request) -> Lab:
    return request.app.state.lab


def check_token(lab_: Lab, request: Request) -> bool:
    token = lab_.settings.auth_token
    if not token:
        return True
    supplied = request.cookies.get(COOKIE)
    auth = request.headers.get("authorization", "")
    if auth.lower().startswith("bearer "):
        supplied = auth[7:]
    return bool(supplied) and hmac.compare_digest(supplied, token)


def not_found(what: str) -> HTTPException:
    return HTTPException(status_code=404, detail=f"{what} not found")


def page(items: list[Any], total: int, offset: int, limit: int) -> ORJSONResponse:
    return ORJSONResponse({"items": items, "total": total, "offset": offset, "limit": limit})
