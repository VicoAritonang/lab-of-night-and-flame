import { lazy, Suspense, useEffect, useState } from "react";
import { api, onAuthRequired } from "./api";
import { Sky } from "./components/Sky";
import { A, Icon, Loading, Logo, Modal, ThemeSwitch, ToastProvider } from "./components/ui";
import { useApi, useFeatures, useLocalState } from "./hooks";
import { match, navigate, useLocation } from "./router";
import About from "./pages/About";
import { updateMetadata } from "./seo";

const Overview = lazy(() => import("./pages/Overview"));
const Playground = lazy(() => import("./pages/Playground"));
const Models = lazy(() => import("./pages/Models"));
const Datasets = lazy(() => import("./pages/Datasets"));
const DatasetDetail = lazy(() => import("./pages/DatasetDetail"));
const Experiments = lazy(() => import("./pages/Experiments"));
const Builder = lazy(() => import("./pages/Builder"));
const CampaignPage = lazy(() => import("./pages/Campaign"));
const RouterLab = lazy(() => import("./pages/RouterLab"));
const AgentLab = lazy(() => import("./pages/AgentLab"));
const Workbench = lazy(() => import("./pages/Workbench"));
const Settings = lazy(() => import("./pages/Settings"));
const Monitor = lazy(() => import("./pages/Monitor"));
const Quality = lazy(() => import("./pages/Quality"));
const Prompts = lazy(() => import("./pages/Prompts"));
const Suites = lazy(() => import("./pages/Suites"));
const Perf = lazy(() => import("./pages/Perf"));

/** `feature` names the server module (ARL_FEATURES) a page needs; pages without one are core. */
type Route = { pattern: string; render: (p: Record<string, string>) => JSX.Element; section: string; feature?: string };
const ROUTES: Route[] = [
  { pattern: "/about", render: () => <About />, section: "/about" },
  { pattern: "/", render: () => <Overview />, section: "/", feature: "experiments" },
  { pattern: "/playground", render: () => <Playground />, section: "/playground", feature: "playground" },
  { pattern: "/models", render: () => <Models />, section: "/models" },
  { pattern: "/datasets", render: () => <Datasets />, section: "/datasets", feature: "datasets" },
  { pattern: "/datasets/:id", render: (p) => <DatasetDetail id={p.id} />, section: "/datasets", feature: "datasets" },
  { pattern: "/experiments", render: () => <Experiments />, section: "/experiments", feature: "experiments" },
  { pattern: "/experiments/new", render: () => <Builder />, section: "/experiments", feature: "experiments" },
  { pattern: "/campaigns/:id", render: (p) => <CampaignPage id={p.id} />, section: "/experiments", feature: "experiments" },
  { pattern: "/router-lab", render: () => <RouterLab />, section: "/router-lab", feature: "experiments" },
  { pattern: "/agent-lab", render: () => <AgentLab />, section: "/agent-lab", feature: "traces" },
  { pattern: "/workbench", render: () => <Workbench />, section: "/workbench", feature: "workbench" },
  { pattern: "/settings", render: () => <Settings />, section: "/settings" },
  { pattern: "/monitor", render: () => <Monitor />, section: "/monitor", feature: "monitor" },
  { pattern: "/quality", render: () => <Quality />, section: "/quality", feature: "quality" },
  { pattern: "/prompts", render: () => <Prompts />, section: "/prompts", feature: "prompts" },
  { pattern: "/suites", render: () => <Suites />, section: "/suites", feature: "suites" },
  { pattern: "/perf", render: () => <Perf />, section: "/perf", feature: "perf" },
];

const KEYS = [
  { to: "/", label: "Bench", feature: "experiments" },
  { to: "/experiments", label: "Experiments", feature: "experiments" },
  { to: "/router-lab", label: "Router lab", feature: "experiments" },
  { to: "/agent-lab", label: "Agent lab", feature: "traces" },
  { to: "/monitor", label: "Monitor", feature: "monitor" },
  { to: "/quality", label: "Quality", feature: "quality" },
  { to: "/workbench", label: "Workbench", feature: "workbench" },
  { to: "/suites", label: "Suites", feature: "suites" },
  { to: "/prompts", label: "Prompts", feature: "prompts" },
  { to: "/playground", label: "Playground", feature: "playground" },
  { to: "/perf", label: "Perf", feature: "perf" },
  { to: "/models", label: "Models" },
  { to: "/datasets", label: "Datasets", feature: "datasets" },
  { to: "/settings", label: "Settings" },
];

function Disabled({ feature }: { feature: string }) {
  return (
    <div className="page">
      <h1 className="page-title">Module switched off</h1>
      <p className="muted">This lab runs without the <span className="mono">{feature}</span> module. Add it to <span className="mono">ARL_FEATURES</span> and restart the lab to use this page.</p>
      <A href="/settings">See enabled modules</A>
    </div>
  );
}

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
  useEffect(() => updateMetadata(path), [path]);
  const [needAuth, setNeedAuth] = useState(false);
  useEffect(() => onAuthRequired(() => setNeedAuth(true)), []);
  const { data: health } = useApi<{ version: string; active_campaigns: number; auth_required: boolean }>("/api/health", { refreshMs: 5000, staleMs: 2000 });
  const on = useFeatures();
  const keys = KEYS.filter((k) => on(k.feature));
  let route: Route | undefined, params: Record<string, string> = {};
  for (const r of ROUTES) { const m = match(r.pattern, path); if (m) { route = r; params = m; break; } }
  const section = route?.section ?? "";
  // The bench belongs to experiments; without it "/" opens the first enabled page.
  const home = route?.pattern === "/" && !on(route.feature);
  useEffect(() => { if (home && keys[0]) navigate(keys[0].to, true); }, [home, keys[0]?.to]);
  return (
    <ToastProvider>
      <Sky />
      <div className="shell">
        <header className="masthead">
          <div className="masthead-row">
            <A href="/" className="brand"><Logo /><div><div className="brand-name"><span className="brand-of">Lab of</span> Night <span className="brand-amp">and</span> Flame</div><div className="brand-sub">v{health?.version ?? ""} · {health ? (health.auth_required ? "owner auth" : "local") : "connecting"}</div></div></A>
            <nav className="fkeys" aria-label="Primary">
              {keys.map((k) => (
                <A key={k.to} href={k.to} className={`fkey ${section === k.to ? "active" : ""}`}>
                  <span className="led" />{k.label}
                  {k.to === "/experiments" && !!health?.active_campaigns && <span className="count">{health.active_campaigns} live</span>}
                </A>
              ))}
            </nav>
            <div className="masthead-tools">
              <ThemeToggle />
              {on("experiments") && <A href="/experiments/new" className="btn sm primary"><Icon name="plus" size={13} />New experiment</A>}
            </div>
          </div>
        </header>
        <main className="main">
          <Suspense fallback={<div className="page"><Loading rows={5} /></div>}>
            {route && !on(route.feature) ? (home ? <Loading rows={3} /> : <Disabled feature={route.feature!} />) : route ? <div key={path}>{route.render(params)}</div> : <div className="page"><h1 className="page-title">No such page</h1><A href="/">Back to the bench</A></div>}
          </Suspense>
        </main>
        <footer className="site-footer"><span>Lab Vico Aritonang · Vicoworks</span><A href="/about">About the lab</A></footer>
      </div>
      {needAuth && <Login onDone={() => { setNeedAuth(false); location.reload(); }} />}
    </ToastProvider>
  );
}
