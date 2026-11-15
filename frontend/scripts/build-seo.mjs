import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

// Use the same component and metadata as the browser: no separate crawler-only content.
const server = await createServer({ server: { middlewareMode: true, watch: null }, appType: "custom" });
try {
  const { default: About } = await server.ssrLoadModule("/src/pages/About.tsx");
  const { renderMetadata, SITE_URL, WORKSPACE_PATHS } = await server.ssrLoadModule("/src/site.ts");
  const template = await readFile(resolve("dist/index.html"), "utf8");
  const page = (path, body = "") => template
    .replace(/<!-- site:head:start -->[\s\S]*?<!-- site:head:end -->/, `<!-- site:head:start -->\n${renderMetadata(path)}\n<!-- site:head:end -->`)
    .replace('<html lang="en">', `<html lang="${path === "/about" ? "id" : "en"}">`)
    .replace('<div id="root"></div>', `<div id="root">${body}</div>`)
    // The About HTML is already usable without JavaScript.
    .replace(path === "/about" ? /\s*<noscript>[\s\S]*?<\/noscript>/ : /$^/, "");

  for (const path of ["/about", ...WORKSPACE_PATHS]) {
    const directory = resolve(`dist${path}`);
    await mkdir(directory, { recursive: true });
    const body = path === "/about" ? `<main>${renderToStaticMarkup(createElement(About))}</main>` : "";
    await writeFile(resolve(directory, "index.html"), page(path, body));
  }
  // Hosting fallback for dynamic IDs and unknown paths must start with noindex.
  await writeFile(resolve("dist/workspace.html"), page("/workspace"));
  const sitemap = `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n  <url><loc>${SITE_URL}/</loc></url>\n  <url><loc>${SITE_URL}/about</loc></url>\n</urlset>\n`;
  await writeFile(resolve("dist/sitemap.xml"), sitemap);
  console.log("SEO: static About page, workspace metadata, and sitemap generated.");
} finally {
  await server.close();
}
