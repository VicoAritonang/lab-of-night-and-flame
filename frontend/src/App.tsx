import { lazy, Suspense, useEffect, useState } from "react";
import { api, onAuthRequired } from "./api";
import { Sky } from "./components/Sky";
import { A, Icon, Loading, Logo, Modal, ThemeSwitch, ToastProvider } from "./components/ui";
import { useApi, useLocalState } from "./hooks";
import { match, useLocation } from "./router";

const Overview = lazy(() => import("./pages/Overview"));
const Playground = lazy(() => import("./pages/Playground"));
const Models = lazy(() => import("./pages/Models"));
const Datasets = lazy(() => import("./pages/Datasets"));
const DatasetDetail = lazy(() => import("./pages/DatasetDetail"));
const Experiments = lazy(() => import("./pages/Experiments"));
const Builder = lazy(() => import("./pages/Builder"));
const CampaignPage = lazy(() => import("./pages/Campaign"));
const RouterLab = lazy(() => import("./pages/RouterLab"));
const Settings = lazy(() => import("./pages/Settings"));

type Route = { pattern: string; render: (p: Record<string, string>) => JSX.Element; section: string };
const ROUTES: Route[] = [
  { pattern: "/", render: () => <Overview />, section: "/" },
  { pattern: "/playground", render: () => <Playground />, section: "/playground" },
  { pattern: "/models", render: () => <Models />, section: "/models" },
  { pattern: "/datasets", render: () => <Datasets />, section: "/datasets" },
  { pattern: "/datasets/:id", render: (p) => <DatasetDetail id={p.id} />, section: "/datasets" },
  { pattern: "/experiments", render: () => <Experiments />, section: "/experiments" },
  { pattern: "/experiments/new", render: () => <Builder />, section: "/experiments" },
  { pattern: "/campaigns/:id", render: (p) => <CampaignPage id={p.id} />, section: "/experiments" },
  { pattern: "/router-lab", render: () => <RouterLab />, section: "/router-lab" },
  { pattern: "/settings", render: () => <Settings />, section: "/settings" },
];

const KEYS = [
  { to: "/", label: "Bench" },
  { to: "/experiments", label: "Experiments" },
  { to: "/router-lab", label: "Router lab" },
  { to: "/playground", label: "Playground" },
  { to: "/models", label: "Models" },
  { to: "/datasets", label: "Datasets" },
  { to: "/settings", label: "Settings" },
];

/** Night = the dark theme, Flame = the light theme. Stored values stay "dark" / "light" so earlier preferences carry over. */
function ThemeToggle() {
  const [theme, setTheme] = useLocalState<string | null>("arl-theme-pref", null);
  useEffect(() => {
    if (theme) document.documentElement.dataset.theme = theme; else delete document.documentElement.dataset.theme;
    try { if (theme) localStorage.setItem("arl-theme", theme); else localStorage.removeItem("arl-theme"); } catch { /* ignore */ }
  }, [theme]);
  const dark = theme ? theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
  return <ThemeSwitch night={dark} onToggle={() => setTheme(dark ? "light" : "dark")} />;
}

function Login({ onDone }: { onDone: () => void }) {
  const [token, setToken] = useState("");
  const [err, setErr] = useState("");
  const submit = () => api.post("/api/auth/login", { token }).then(onDone).catch((e) => setErr(e.message));
  return (
    <Modal open onClose={() => {}} title="Owner token required" footer={<button className="btn primary" onClick={submit}>Sign in</button>}>
      <p className="muted" style={{ margin: 0 }}>This Lab is protected by <span className="mono">ARL_AUTH_TOKEN</span>.</p>
      <input className="input mono" type="password" autoFocus value={token} onChange={(e) => setToken(e.target.value)} onKeyDown={(e) => e.key === "Enter" && submit()} />
      {err && <div className="bad">{err}</div>}
    </Modal>
  );
}

export default function App() {
  const { path } = useLocation();
  const [needAuth, setNeedAuth] = useState(false);
  useEffect(() => onAuthRequired(() => setNeedAuth(true)), []);
  const { data: health } = useApi<{ version: string; active_campaigns: number; auth_required: boolean }>("/api/health", { refreshMs: 5000, staleMs: 2000 });
  let route: Route | undefined, params: Record<string, string> = {};
  for (const r of ROUTES) { const m = match(r.pattern, path); if (m) { route = r; params = m; break; } }
  const section = route?.section ?? "";
  return (
    <ToastProvider>
      <Sky />
      <div className="shell">
        <header className="masthead">
          <div className="masthead-row">
            <A href="/" className="brand"><Logo /><div><div className="brand-name"><span className="brand-of">Lab of</span> Night <span className="brand-amp">and</span> Flame</div><div className="brand-sub">v{health?.version ?? ""} · {health ? (health.auth_required ? "owner auth" : "local") : "connecting"}</div></div></A>
            <nav className="fkeys" aria-label="Primary">
              {KEYS.map((k) => (
                <A key={k.to} href={k.to} className={`fkey ${section === k.to ? "active" : ""}`}>
                  <span className="led" />{k.label}
                  {k.to === "/experiments" && !!health?.active_campaigns && <span className="count">{health.active_campaigns} live</span>}
                </A>
              ))}
            </nav>
            <div className="masthead-tools">
              <ThemeToggle />
              <A href="/experiments/new" className="btn sm primary"><Icon name="plus" size={13} />New experiment</A>
            </div>
          </div>
        </header>
        <main className="main">
          <Suspense fallback={<div className="page"><Loading rows={5} /></div>}>
            {route ? <div key={path}>{route.render(params)}</div> : <div className="page"><h1 className="page-title">No such page</h1><A href="/">Back to the bench</A></div>}
          </Suspense>
        </main>
      </div>
      {needAuth && <Login onDone={() => { setNeedAuth(false); location.reload(); }} />}
    </ToastProvider>
  );
}
