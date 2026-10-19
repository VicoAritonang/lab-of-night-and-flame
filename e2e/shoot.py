"""Screenshot helper: python shoot.py <base> <path> <name> [tab-name] [light|dark] [live]

`live` keeps animations running and captures only the viewport (to show pulses in flight).
"""
import sys

from playwright.sync_api import sync_playwright

base, path, name = sys.argv[1:4]
tab = sys.argv[4] if len(sys.argv) > 4 and sys.argv[4] != "-" else None
scheme = sys.argv[5] if len(sys.argv) > 5 else "dark"
live = len(sys.argv) > 6 and sys.argv[6] == "live"
with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={"width": 1440, "height": 900}, color_scheme=scheme)
    pg.goto(base + path)
    pg.wait_for_timeout(1200)
    if tab:
        pg.get_by_role("tab", name=tab).click()
    pg.wait_for_timeout(2500)
    if live:
        pg.screenshot(path=f"/e2e/shots/{name}.png")
    else:
        pg.screenshot(path=f"/e2e/shots/{name}.png", full_page=True, animations="disabled")
    b.close()
