// Cloudflare Worker serving Go vanity import paths for go.vicoworks.com.
//
// `go get go.vicoworks.com/arl` requests https://go.vicoworks.com/arl?go-get=1
// and reads the go-import meta tag, which points at the GitHub repository and
// the module's subdirectory (supported by the go command since Go 1.25; older
// toolchains still work through proxy.golang.org). Browsers are redirected to
// the documentation on pkg.go.dev.

/** Modules served under this host. `path` is relative to the host. */
export const MODULES = [
  {
    path: "arl",
    repo: "https://github.com/VicoAritonang/lab-of-night-and-flame",
    subdir: "sdk/go",
    branch: "main",
    description: "Go SDK for Agent Reliability Lab: live agent traces, state snapshots and Workbench replays.",
  },
];

const escape = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

/** Finds the module whose path is the longest prefix of the requested package. */
export function resolve(pathname, modules = MODULES) {
  const pkg = pathname.replace(/^\/+|\/+$/g, "");
  let best = null;
  for (const m of modules) {
    if ((pkg === m.path || pkg.startsWith(m.path + "/")) && (!best || m.path.length > best.path.length)) best = m;
  }
  return best && { module: best, pkg };
}

export function goImport(host, m) {
  const root = `${host}/${m.path}`;
  const tree = `${m.repo}/tree/${m.branch}${m.subdir ? "/" + m.subdir : ""}`;
  const blob = `${m.repo}/blob/${m.branch}${m.subdir ? "/" + m.subdir : ""}`;
  return [
    `<meta name="go-import" content="${escape(`${root} git ${m.repo}${m.subdir ? " " + m.subdir : ""}`)}">`,
    `<meta name="go-source" content="${escape(`${root} ${tree} ${tree}{/dir} ${blob}{/dir}/{file}#L{line}`)}">`,
  ].join("\n");
}

const page = (title, head, body) => `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
${head}
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${escape(title)}</title>
<style>
:root{color-scheme:light dark;--fg:#1b1d22;--muted:#5d6370;--bg:#fbfaf7;--line:#e2ded5;--code:#f1eee7;--accent:#b4501f}
@media (prefers-color-scheme:dark){:root{--fg:#e6e8ee;--muted:#9aa1b2;--bg:#0c1020;--line:#252b40;--code:#151b30;--accent:#8fb4ff}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:720px;margin:0 auto;padding:12vh 16px 64px}
h1{font-size:22px;margin:0 0 4px}p{color:var(--muted);margin:0 0 20px}
a{color:var(--accent)}code,pre{font:13px/1.6 ui-monospace,SFMono-Regular,Consolas,monospace}
pre{background:var(--code);border:1px solid var(--line);padding:10px 12px;overflow-x:auto;margin:8px 0 0}
section{border-top:1px solid var(--line);padding:18px 0}
</style>
</head>
<body><main>${body}</main></body>
</html>
`;

/** Handles one request; `env.HOST` overrides the import host (default: request host). */
export function handle(request, env = {}) {
  const url = new URL(request.url);
  const host = env.HOST || url.host;
  const headers = { "content-type": "text/html; charset=utf-8", "cache-control": "public, max-age=300", "x-content-type-options": "nosniff" };
  if (request.method !== "GET" && request.method !== "HEAD") return new Response("method not allowed\n", { status: 405, headers: { allow: "GET, HEAD" } });
  if (url.pathname === "/healthz") return new Response("ok\n", { headers: { "content-type": "text/plain" } });

  if (url.pathname === "/" || url.pathname === "") {
    const list = MODULES.map((m) => `<section><h2><code>${escape(`${host}/${m.path}`)}</code></h2><p>${escape(m.description)}</p>
<pre>go get ${escape(`${host}/${m.path}`)}@latest</pre>
<p><a href="https://pkg.go.dev/${escape(`${host}/${m.path}`)}">Documentation</a> · <a href="${escape(m.repo)}">Source</a></p></section>`).join("");
    return new Response(page(`${host} — Go modules`, "", `<h1>${escape(host)}</h1><p>Go modules by Vicoworks.</p>${list}`), { headers });
  }

  const hit = resolve(url.pathname);
  if (!hit) return new Response(page("Not found", `<meta name="robots" content="noindex">`, `<h1>Not found</h1><p>No Go module at <code>${escape(host + url.pathname)}</code>. <a href="/">Available modules</a>.</p>`), { status: 404, headers });

  const meta = goImport(host, hit.module);
  const docs = `https://pkg.go.dev/${host}/${hit.pkg}`;
  if (url.searchParams.get("go-get") === "1") {
    // The go command reads only the head; keep the response tiny.
    return new Response(`<!doctype html>\n<html><head>\n${meta}\n</head><body>go get ${escape(`${host}/${hit.pkg}`)}</body></html>\n`, { headers });
  }
  return new Response(page(`${host}/${hit.pkg}`, `${meta}\n<meta http-equiv="refresh" content="0; url=${escape(docs)}">`, `<h1><code>${escape(`${host}/${hit.pkg}`)}</code></h1><p>Redirecting to <a href="${escape(docs)}">the documentation</a>…</p><pre>go get ${escape(`${host}/${hit.module.path}`)}@latest</pre>`), { headers });
}

export default {
  fetch(request, env) {
    return handle(request, env);
  },
};
