import { useSyncExternalStore } from "react";

/** Tiny History-API router (no dependency). Routes are matched in order; `:param` segments are captured. */
const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());
window.addEventListener("popstate", notify);

export function navigate(to: string, replace = false) {
  if (to === location.pathname + location.search) return;
  history[replace ? "replaceState" : "pushState"](null, "", to);
  window.scrollTo({ top: 0 });
  notify();
}

const subscribe = (l: () => void) => { listeners.add(l); return () => listeners.delete(l); };
export function useLocation(): { path: string; query: URLSearchParams } {
  const href = useSyncExternalStore(subscribe, () => location.pathname + location.search);
  const u = new URL(href, location.origin);
  return { path: u.pathname, query: u.searchParams };
}

export function match(pattern: string, path: string): Record<string, string> | null {
  const p = pattern.split("/").filter(Boolean);
  const s = path.split("/").filter(Boolean);
  if (p.length !== s.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < p.length; i++) {
    if (p[i].startsWith(":")) params[p[i].slice(1)] = decodeURIComponent(s[i]);
    else if (p[i] !== s[i]) return null;
  }
  return params;
}

/** Intercept plain left-clicks on internal <a href="/..."> so links stay real links (middle-click works). */
export function linkHandler(e: React.MouseEvent<HTMLAnchorElement>) {
  const a = e.currentTarget;
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || a.target === "_blank") return;
  const href = a.getAttribute("href");
  if (!href || !href.startsWith("/") || href.startsWith("/api/")) return;
  e.preventDefault();
  navigate(href);
}
