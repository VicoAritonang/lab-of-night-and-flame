// Run after npm run build: verify the deployable HTML rather than only source templates.
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";

const read = (path) => readFileSync(new URL(`../dist/${path}`, import.meta.url), "utf8");
const home = read("index.html"), about = read("about/index.html");
assert.match(home, /Lab Vico Aritonang/);
assert.match(home, /href="\/favicon.ico"/);
assert.doesNotMatch(home, /href="\/favicon.svg"/);
assert.match(about, /<h1[^>]*>Lab Vico Aritonang<\/h1>/);
assert.match(about, /node dan edge/);
assert.doesNotMatch(about, /<noscript>/);

for (const path of ["settings", "models", "datasets", "experiments", "experiments/new", "router-lab", "agent-lab", "playground"]) {
  const html = read(`${path}/index.html`);
  assert.match(html, /content="noindex, follow"/);
  assert.ok(html.includes(`rel="canonical" href="https://lab.vicoworks.com/${path}"`));
}
assert.match(read("workspace.html"), /content="noindex, follow"/);

const sitemap = read("sitemap.xml");
assert.deepEqual([...sitemap.matchAll(/<loc>(.*?)<\/loc>/g)].map((match) => match[1]), ["https://lab.vicoworks.com/", "https://lab.vicoworks.com/about"]);
for (const file of ["robots.txt", "llms.txt", "llms-full.txt"]) assert.match(read(file), /lab.vicoworks.com/);
assert.deepEqual(readFileSync(new URL("../dist/favicon.ico", import.meta.url)), readFileSync(new URL("../assets/favicon.ico", import.meta.url)));

for (const [html, path] of [[home, "/"], [about, "/about"]]) {
  assert.match(html, /content="index, follow, max-image-preview:large"/);
  assert.ok(html.includes(`rel="canonical" href="https://lab.vicoworks.com${path}"`));
  const schema = html.match(/<script id="site-structured-data" type="application\/ld\+json">(.*?)<\/script>/s);
  assert.ok(schema);
  assert.equal(JSON.parse(schema[1])["@graph"].length, 4);
  assert.equal((html.match(/rel="canonical"/g) || []).length, 1);
  for (const [, asset] of html.matchAll(/(?:src|href)="(\/assets\/[^"#?]+)"/g)) {
    assert.ok(existsSync(new URL(`../dist${asset}`, import.meta.url)), `Missing built asset: ${asset}`);
  }
}
console.log("SEO checks passed: static public content, metadata, JSON-LD, noindex, sitemap, crawler files, favicon, and asset links.");
