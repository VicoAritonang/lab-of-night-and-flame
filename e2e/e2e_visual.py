"""Visual + flow E2E test against a running Lab (Playwright, Chromium).

Usage (from repo root, with the compose stack running):
  docker run --rm --network agent-reliability-lab_default -v "$PWD/e2e:/e2e" \
    mcr.microsoft.com/playwright/python:v1.58.0-noble python /e2e/e2e_visual.py http://arl:8000

Fails (exit 1) on any console error, failed request, or broken flow. Screenshots land in e2e/shots/.
"""
from __future__ import annotations

import json
import sys
import time
from pathlib import Path

from playwright.sync_api import Page, expect, sync_playwright

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8000"
SHOTS = Path(__file__).parent / "shots"
SHOTS.mkdir(exist_ok=True)
errors: list[str] = []
checks: list[str] = []


def ok(msg: str) -> None:
    checks.append(msg)
    print(f"  ✓ {msg}", flush=True)


def shot(page: Page, name: str, full: bool = True) -> None:
    page.wait_for_timeout(900)  # let entrance animations settle
    page.screenshot(path=str(SHOTS / f"{name}.png"), full_page=full, animations="disabled")


def attach(page: Page) -> None:
    page.on("console", lambda m: errors.append(f"console.{m.type}: {m.text}") if m.type == "error" else None)
    page.on("pageerror", lambda e: errors.append(f"pageerror: {e}"))
    page.on("requestfailed", lambda r: errors.append(f"requestfailed: {r.url} {r.failure}")
            if "/events" not in r.url and "/playground" not in r.url else None)
    page.on("response", lambda r: errors.append(f"HTTP {r.status}: {r.url}") if r.status >= 500 else None)


def run() -> None:
    with sync_playwright() as p:
        browser = p.chromium.launch()
        ctx = browser.new_context(viewport={"width": 1440, "height": 900}, color_scheme="dark", device_scale_factor=1)
        page = ctx.new_page()
        attach(page)

        print("Overview")
        page.goto(BASE + "/")
        expect(page.get_by_role("heading", name="Every number here has a denominator.")).to_be_visible()
        expect(page.get_by_text("Providers", exact=True)).to_be_visible()
        ok("overview renders with provider panel")
        shot(page, "01-overview-empty")

        print("Demo campaign via UI")
        page.get_by_role("button", name="Run free demo").click()
        page.wait_for_url("**/campaigns/*", timeout=15000)
        expect(page.get_by_text("Trial map", exact=True)).to_be_visible()
        first = page.locator(".stat-value").first.text_content()
        page.wait_for_timeout(1800)
        second = page.locator(".stat-value").first.text_content()
        assert first != second, f"live progress did not advance over SSE ({first} -> {second})"
        ok(f"live progress advances over SSE ({first} → {second})")
        shot(page, "02-campaign-live")
        ok("demo launched and live trial map visible")
        # wait for completion
        cid = page.url.rsplit("/", 1)[-1]
        for _ in range(120):
            st = page.evaluate(f"fetch('/api/campaigns/{cid}').then(r => r.json()).then(j => j.status)")
            if st in ("completed", "failed", "cancelled", "paused"):
                break
            time.sleep(0.5)
        assert st == "completed", f"demo campaign ended {st}"
        ok("demo campaign completed")
        page.wait_for_timeout(1200)
        shot(page, "03-campaign-live-done")

        page.get_by_role("tab", name="Results & gate").click()
        expect(page.get_by_text("Leaderboard", exact=True)).to_be_visible()
        expect(page.get_by_text("Paired comparison vs baseline")).to_be_visible()
        page.wait_for_timeout(2200)  # let the evidence scope finish its sequence
        verdicts = page.locator(".stamp").count()
        assert verdicts == 3, f"expected 3 verdict stamps, got {verdicts}"
        ok("results: 3 gate verdicts, leaderboard, forest plot")
        shot(page, "04-results")

        # heatmap cell → trial drawer
        canvas = page.locator("canvas").last
        canvas.scroll_into_view_if_needed()
        box = canvas.bounding_box()
        page.mouse.click(box["x"] + 130, box["y"] + 14)
        expect(page.locator(".drawer")).to_be_visible(timeout=5000)
        expect(page.locator(".drawer").get_by_text("Attempts (every provider call")).to_be_visible()
        ok("heatmap cell opens trial drawer with attempts")
        shot(page, "05-trial-drawer", full=False)
        page.keyboard.press("Escape")

        page.get_by_role("tab", name="Trials explorer").click()
        expect(page.locator("table.t tbody tr").first).to_be_visible()
        page.locator("select[aria-label=Outcome]").select_option("fail")
        page.wait_for_timeout(600)
        rows = page.locator("table.t tbody tr").count()
        assert rows > 0, "no failed trials shown for 50% oracle"
        ok(f"trials explorer filters by outcome ({rows} failing rows on page)")
        shot(page, "06-trials")

        # report exports
        for fmt in ("html", "md", "json"):
            r = page.request.get(f"{BASE}/api/campaigns/{cid}/report.{fmt}")
            assert r.ok, f"report.{fmt} → {r.status}"
        rep = ctx.new_page()
        attach(rep)
        rep.goto(f"{BASE}/api/campaigns/{cid}/report.html")
        expect(rep.get_by_text("Qualification gate", exact=True)).to_be_visible()
        shot(rep, "07-report-html")
        rep.close()
        ok("HTML/MD/JSON reports export")

        print("Re-score flow")
        page.get_by_role("button", name="Re-score").click()
        expect(page.get_by_text("Re-score stored outputs")).to_be_visible()
        page.locator(".modal textarea").fill(json.dumps([{"id": "choice", "key": "choice", "params": {}},
                                                         {"id": "contains", "key": "contains", "params": {"mode": "any"}}]))
        page.get_by_role("button", name="Create revision 2").click()
        page.wait_for_timeout(2500)
        expect(page.get_by_text("assessment rev 2").first).to_be_visible(timeout=10000)
        ok("re-score created assessment revision 2")

        print("Builder")
        page.goto(BASE + "/experiments/new")
        expect(page.get_by_role("heading", name="New experiment")).to_be_visible()
        page.get_by_role("button", name="vision-shape-counting").first.click()
        shot(page, "08-builder-data")
        page.get_by_role("button", name="Next").click()
        page.get_by_role("button", name="Add models").click()
        expect(page.locator(".model-list")).to_be_visible()
        page.locator(".modal select[aria-label=Provider]").select_option("fake")
        page.wait_for_timeout(700)
        page.locator(".model-row").filter(has_text="fake/oracle-90").click()
        page.locator(".model-row").filter(has_text="fake/oracle-70").click()
        shot(page, "09-builder-picker", full=False)
        page.get_by_role("button", name="Done (2)").click()
        page.get_by_role("button", name="Next").click()
        expect(page.get_by_text("Numeric tolerance").first).to_be_visible()
        page.get_by_role("button", name="Next").click()
        page.get_by_role("button", name="Next").click()
        expect(page.get_by_text("Frozen manifest (arl-experiment-v1)", exact=True)).to_be_visible()
        expect(page.get_by_text("Data disclosure", exact=True)).to_be_visible()
        shot(page, "10-builder-review")
        page.get_by_role("button", name="Freeze & launch").click()
        page.wait_for_url("**/campaigns/*", timeout=15000)
        ok("builder wizard → vision experiment launched")
        vcid = page.url.rsplit("/", 1)[-1]
        for _ in range(120):
            st = page.evaluate(f"fetch('/api/campaigns/{vcid}').then(r => r.json()).then(j => j.status)")
            if st in ("completed", "failed", "cancelled", "paused"):
                break
            time.sleep(0.5)
        assert st == "completed", st
        page.get_by_role("tab", name="Results & gate").click()
        expect(page.get_by_text("Slices", exact=True)).to_be_visible()
        shot(page, "11-vision-results")
        ok("vision campaign completed with slices")

        print("Router lab (fake router, no cost)")
        page.goto(BASE + "/router-lab")
        expect(page.get_by_role("heading", name="Where does the router send your request?")).to_be_visible()
        shot(page, "22-router-lab-empty")
        for m in ("typesafe/jev-router", "openai/gpt-6-luna", "openai/gpt-6-sol"):
            page.locator("label.check").filter(has_text=m).locator("input").uncheck()
        page.locator("label.check").filter(has_text="fake/router").locator("input").check()
        page.get_by_role("button", name="Freeze plan and run").click()
        page.wait_for_url("**/campaigns/*", timeout=15000)
        rcid = page.url.rsplit("/", 1)[-1]
        page.wait_for_timeout(1500)
        shot(page, "23-router-live")
        for _ in range(120):
            st = page.evaluate(f"fetch('/api/campaigns/{rcid}').then(r => r.json()).then(j => j.status)")
            if st in ("completed", "failed", "cancelled", "paused"):
                break
            time.sleep(0.5)
        assert st == "completed", st
        page.get_by_role("tab", name="Results & gate").click()
        expect(page.get_by_text("Routing — fake/router")).to_be_visible(timeout=10000)
        shot(page, "24-router-results")
        ok("router lab → fake router campaign → routing flow rendered")

        print("Playground")
        page.goto(BASE + "/playground")
        page.get_by_role("button", name="Run on").click()
        expect(page.get_by_text("TTFT").first).to_be_visible(timeout=10000)
        ok("playground streams a fake target and shows metrics")
        shot(page, "12-playground")

        print("Registry pages")
        page.goto(BASE + "/models")
        expect(page.get_by_role("heading", name="Model registry")).to_be_visible()
        expect(page.locator("table.t tbody tr").first).to_be_visible(timeout=10000)
        shot(page, "13-models", full=False)
        page.goto(BASE + "/datasets")
        expect(page.get_by_text("invoice-extraction", exact=True)).to_be_visible()
        shot(page, "14-datasets")
        page.get_by_text("vision-chart-reading").click()
        expect(page.get_by_text("Integrity checks", exact=True)).to_be_visible()
        shot(page, "15-dataset-detail")
        page.goto(BASE + "/settings")
        expect(page.get_by_text("Audit trail", exact=True)).to_be_visible()
        shot(page, "16-settings")
        page.goto(BASE + "/")
        shot(page, "17-overview-populated")
        ok("models, datasets, dataset detail, settings render")

        print("Light theme + mobile")
        light = browser.new_context(viewport={"width": 1440, "height": 900}, color_scheme="light")
        lp = light.new_page()
        attach(lp)
        lp.goto(f"{BASE}/campaigns/{cid}")
        lp.get_by_role("tab", name="Results & gate").click()
        expect(lp.get_by_text("Leaderboard", exact=True)).to_be_visible()
        shot(lp, "18-results-light")
        mobile = browser.new_context(viewport={"width": 390, "height": 844}, color_scheme="dark")
        mp = mobile.new_page()
        attach(mp)
        mp.goto(BASE + "/")
        shot(mp, "19-mobile-overview")
        overflow = mp.evaluate("document.documentElement.scrollWidth > window.innerWidth + 1")
        if overflow:
            errors.append("mobile: horizontal page overflow")
        ok("light theme and mobile viewport render")

        # performance: navigation timing on a fresh context
        perf_ctx = browser.new_context()
        pp = perf_ctx.new_page()
        pp.goto(BASE + "/")
        pp.wait_for_load_state("networkidle")
        t = pp.evaluate("JSON.stringify(performance.getEntriesByType('navigation')[0].toJSON())")
        nav = json.loads(t)
        res = pp.evaluate("performance.getEntriesByType('resource').reduce((a, r) => a + (r.transferSize || 0), 0)")
        print(f"  perf: DOMContentLoaded {nav['domContentLoadedEventEnd']:.0f} ms, load {nav['loadEventEnd']:.0f} ms, transferred {res / 1024:.0f} KB")
        browser.close()


if __name__ == "__main__":
    try:
        run()
    except Exception as e:  # noqa: BLE001
        errors.append(f"FLOW FAILURE: {type(e).__name__}: {e}")
    print(f"\n{len(checks)} checks passed")
    if errors:
        print("ERRORS:")
        for e in errors:
            print("  ✗", e)
        sys.exit(1)
    print("no console errors, no failed requests")
