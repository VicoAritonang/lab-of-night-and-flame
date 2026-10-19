export class ApiError extends Error {
  status: number;
  errors: string[];
  constructor(status: number, message: string, errors: string[] = []) {
    super(message);
    this.status = status;
    this.errors = errors;
  }
}

type Listener = () => void;
const authListeners = new Set<Listener>();
export const onAuthRequired = (fn: Listener) => { authListeners.add(fn); return () => { authListeners.delete(fn); }; };

async function parse(res: Response): Promise<any> {
  const text = await res.text();
  let body: any = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = text; }
  if (!res.ok) {
    if (res.status === 401) authListeners.forEach((f) => f());
    const d = body?.detail ?? body;
    const msg = typeof d === "string" ? d : d?.message ?? `HTTP ${res.status}`;
    const errs: string[] = Array.isArray(d?.errors) ? d.errors.map((e: any) => (typeof e === "string" ? e : e.error ? `#${e.record ?? e.line}: ${e.error}` : JSON.stringify(e))) : [];
    throw new ApiError(res.status, msg, errs);
  }
  return body;
}

export const api = {
  get: <T = any>(path: string, signal?: AbortSignal): Promise<T> => fetch(path, { signal, credentials: "same-origin" }).then(parse),
  post: <T = any>(path: string, body?: unknown): Promise<T> =>
    fetch(path, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) }).then(parse),
  del: <T = any>(path: string): Promise<T> => fetch(path, { method: "DELETE", credentials: "same-origin" }).then(parse),
  form: <T = any>(path: string, form: FormData): Promise<T> => fetch(path, { method: "POST", credentials: "same-origin", body: form }).then(parse),
};

/** POST that streams Server-Sent Events back (used by the playground). */
export async function postStream(path: string, body: unknown, onEvent: (e: any) => void, signal?: AbortSignal): Promise<void> {
  const res = await fetch(path, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal });
  if (!res.ok || !res.body) { await parse(res); return; }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buf.indexOf("\n\n")) >= 0) {
      const chunk = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      for (const line of chunk.split("\n")) {
        if (line.startsWith("data:")) {
          try { onEvent(JSON.parse(line.slice(5))); } catch { /* ignore malformed */ }
        }
      }
    }
  }
}
