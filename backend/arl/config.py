"""Runtime configuration. Everything comes from the environment; secrets are never persisted."""
from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path


def _load_dotenv(path: Path) -> None:
    """Minimal .env loader (no dependency). Existing env vars win."""
    if not path.is_file():
        return
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        key, value = key.strip(), value.strip().strip('"').strip("'")
        if key and key not in os.environ:
            os.environ[key] = value


@dataclass(frozen=True)
class Settings:
    data_dir: Path
    static_dir: Path | None
    auth_token: str | None
    local_base_url: str
    provider_keys: dict[str, str | None] = field(repr=False)
    default_concurrency: int = 8
    max_upload_bytes: int = 20 * 1024 * 1024
    version: str = "0.1.0"

    @property
    def db_path(self) -> Path:
        return self.data_dir / "arl.db"

    @property
    def artifact_dir(self) -> Path:
        return self.data_dir / "artifacts"

    def key_for(self, env_name: str | None) -> str | None:
        if not env_name:
            return None
        return os.environ.get(env_name) or None


def load_settings() -> Settings:
    root = Path(__file__).resolve().parents[2]
    _load_dotenv(root / ".env")
    data_dir = Path(os.environ.get("ARL_DATA_DIR", root / "data")).resolve()
    data_dir.mkdir(parents=True, exist_ok=True)
    (data_dir / "artifacts").mkdir(exist_ok=True)
    static = os.environ.get("ARL_STATIC_DIR")
    static_dir = Path(static) if static else (root / "frontend" / "dist")
    return Settings(
        data_dir=data_dir,
        static_dir=static_dir if static_dir.is_dir() else None,
        auth_token=os.environ.get("ARL_AUTH_TOKEN") or None,
        local_base_url=os.environ.get("ARL_LOCAL_BASE_URL", "http://localhost:11434/v1"),
        provider_keys={
            "openrouter": os.environ.get("OPENROUTER_API_KEY") or None,
            "openai": os.environ.get("OPENAI_API_KEY") or None,
            "groq": os.environ.get("GROQ_API_KEY") or None,
        },
        default_concurrency=int(os.environ.get("ARL_CONCURRENCY", "8")),
    )
