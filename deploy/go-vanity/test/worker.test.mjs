import assert from "node:assert/strict";
import { test } from "node:test";
import worker, { resolve } from "../src/index.js";

const env = { HOST: "go.vicoworks.com" };
const get = (path, init) => worker.fetch(new Request(`https://go.vicoworks.com${path}`, init), env);
const goImport = 'content="go.vicoworks.com/arl git https://github.com/VicoAritonang/lab-of-night-and-flame sdk/go"';

test("go get receives go-import with module root and subdirectory", async () => {
  for (const path of ["/arl?go-get=1", "/arl/?go-get=1", "/arl/internal/x?go-get=1"]) {
    const res = await get(path);
    assert.equal(res.status, 200, path);
    const body = await res.text();
    assert.ok(body.includes(`<meta name="go-import" ${goImport}>`), body);
    assert.ok(body.includes('name="go-source"'));
    assert.ok(body.indexOf("go-import") < 200, "meta must appear early for the go command parser");
  }
});

test("browsers are redirected to pkg.go.dev", async () => {
  const body = await (await get("/arl")).text();
  assert.ok(body.includes('url=https://pkg.go.dev/go.vicoworks.com/arl"'));
  assert.ok(body.includes(goImport));
});

test("index lists modules; unknown paths 404; only GET/HEAD", async () => {
  assert.ok((await (await get("/")).text()).includes("go get go.vicoworks.com/arl@latest"));
  assert.equal((await get("/arlx?go-get=1")).status, 404);
  assert.equal((await get("/other?go-get=1")).status, 404);
  assert.equal((await get("/arl", { method: "POST" })).status, 405);
  assert.equal((await get("/healthz")).status, 200);
});

test("longest prefix wins and escaping is applied", () => {
  const mods = [{ path: "a" }, { path: "a/b" }];
  assert.equal(resolve("/a/b/c", mods).module.path, "a/b");
  assert.equal(resolve("/a/bc", mods).module.path, "a");
  assert.equal(resolve("/ab", mods), null);
});
