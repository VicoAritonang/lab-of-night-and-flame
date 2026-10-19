import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

/** Stale-while-revalidate cache shared across components: instant back-navigation, background refresh. */
const cache = new Map<string, { data: any; ts: number }>();
const inflight = new Map<string, Promise<any>>();

export function invalidate(prefix: string) {
  for (const k of cache.keys()) if (k.startsWith(prefix)) cache.delete(k);
}

export function useApi<T>(path: string | null, opts: { refreshMs?: number; staleMs?: number } = {}) {
  const { refreshMs, staleMs = 15000 } = opts;
  const cached = path ? cache.get(path) : undefined;
  const [data, setData] = useState<T | undefined>(cached?.data);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(!cached);
  const pathRef = useRef(path);
  pathRef.current = path;

  const load = useCallback(async (force = false) => {
    if (!path) return;
    const hit = cache.get(path);
    if (hit && !force && Date.now() - hit.ts < staleMs) { setData(hit.data); setLoading(false); return; }
    try {
      let p = inflight.get(path);
      if (!p) { p = api.get<T>(path); inflight.set(path, p); }
      const d = await p;
      cache.set(path, { data: d, ts: Date.now() });
      if (pathRef.current === path) { setData(d); setError(null); }
    } catch (e) {
      if (pathRef.current === path) setError(e as Error);
    } finally {
      inflight.delete(path);
      if (pathRef.current === path) setLoading(false);
    }
  }, [path, staleMs]);

  useEffect(() => {
    if (!path) return;
    const hit = cache.get(path);
    setData(hit?.data);
    setLoading(!hit);
    load(!!hit);
  }, [path, load]);

  useEffect(() => {
    if (!refreshMs || !path) return;
    const id = window.setInterval(() => { if (!document.hidden) load(true); }, refreshMs);
    return () => window.clearInterval(id);
  }, [refreshMs, path, load]);

  return { data, error, loading, reload: () => load(true), setData };
}

/** Subscribe to a Server-Sent Events stream; auto-reconnects (EventSource default). */
export function useEventSource(url: string | null, onMessage: (ev: any) => void) {
  const handler = useRef(onMessage);
  handler.current = onMessage;
  useEffect(() => {
    if (!url) return;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (m) => { try { handler.current(JSON.parse(m.data)); } catch { /* ignore */ } };
    return () => es.close();
  }, [url]);
}

/** Animated number (rAF tween, honours reduced motion). */
export function useTween(target: number | null | undefined, durationMs = 700): number | null {
  const [v, setV] = useState<number | null>(target ?? null);
  const from = useRef<number>(target ?? 0);
  useEffect(() => {
    if (target == null) { setV(null); return; }
    const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    const start = from.current ?? 0;
    if (reduce || start === target) { setV(target); from.current = target; return; }
    const t0 = performance.now();
    let raf = 0;
    const step = (t: number) => {
      const k = Math.min(1, (t - t0) / durationMs);
      const e = 1 - Math.pow(1 - k, 3);
      const cur = start + (target - start) * e;
      setV(cur);
      from.current = cur;
      if (k < 1) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [target, durationMs]);
  return v;
}

export function useDebounced<T>(value: T, ms = 250): T {
  const [v, setV] = useState(value);
  useEffect(() => { const id = setTimeout(() => setV(value), ms); return () => clearTimeout(id); }, [value, ms]);
  return v;
}

export function useLocalState<T>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try { const s = localStorage.getItem(key); return s ? (JSON.parse(s) as T) : initial; } catch { return initial; }
  });
  const set = useCallback((nv: T) => { setV(nv); try { localStorage.setItem(key, JSON.stringify(nv)); } catch { /* ignore */ } }, [key]);
  return [v, set];
}
