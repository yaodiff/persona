#!/usr/bin/env python3
"""End-to-end check of archiving Agent sessions on the web (docs/web/web.md "Archived agents"): a row's menu offers
Archive and Close… (#7125). While the agent works, Archive is disabled, saying why, and Close… asks with what closing
interrupts and "Close anyway", as Close pane on its pane does; on an idle agent Close… archives and closes, then the
row leaves and the toast says it was closed (or why it is still open); a plain archive offers Undo, which brings the
row back; Archived agents lists the archived ones, searches them, restores one (it is back in the list), and an
archived chat says it is archived with Restore.

Needs a running web page against the fake hub with archives on, and Playwright for Python:

    MOCK_AGENT_ARCHIVES=1 MOCK_MANAGERS=1 PORT=18893 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18893 npx vite --port 5893 --strictPort --host 127.0.0.1 &)
    python3 scripts/test/web_agent_archives_e2e.py http://127.0.0.1:5893 --hub http://127.0.0.1:18893 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs light and dark, at desktop and phone
widths, in Chinese (the labels are read from the web catalog); --shots saves each state there. --channel msedge where
there is no Chrome.
"""
import argparse
import glob
import json
import os
import sys
import urllib.request

from playwright.sync_api import expect, sync_playwright

SIZES = {"wide": {"width": 1280, "height": 860}, "narrow": {"width": 390, "height": 844}}
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# the mock hub's agents (scripts/mock/mock_hub.py)
IDLE = "a1f3c2d4-0000-4000-8000-000000000006"
UNREAD = "a1f3c2d4-0000-4000-8000-000000000008"
WORKING = "a1f3c2d4-0000-4000-8000-000000000001"
MOCK_REFUSAL = "Its engine or another tool keeps this session, so it was left open on the computer"  # the mock's close reason


def catalog(lang):
    out = {}
    for f in glob.glob(os.path.join(ROOT, "web", "src", "i18n", "locales", lang, "*.json")):
        ns = os.path.basename(f)[:-5]
        if ns.startswith("_"):
            continue
        with open(f, encoding="utf-8") as fh:
            for k, v in json.load(fh).items():
                out[f"{ns}.{k}"] = v
    return out


T = catalog("zh-Hans")


def t(key, **args):
    s = T[key]
    for k, v in args.items():
        s = s.replace("{" + k + "}", v)
    return s


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def get(hub, path):
    req = urllib.request.Request(hub + path, headers={"Authorization": "Bearer mock"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read() or b"{}")


def titles(hub):
    rows = get(hub, "/api/v1/sessions")
    rows = rows.get("sessions") or [s for m in rows.get("machines", []) for s in m.get("sessions", [])]
    return {s["session_id"]: s["title"] for s in rows}


def run(browser, base, hub, token, size, theme, shots, names):
    tag = f"{size}/{theme}"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=size == "narrow", locale="zh-CN")
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}'); localStorage.setItem('persona.lang', 'zh-Hans')")

    def shot(name):
        if shots:
            page.wait_for_timeout(350)
            page.screenshot(path=os.path.join(shots, f"archive-{size}-{theme}-{name}.png"))

    def open_list():
        page.goto(base + ("/agents" if size == "narrow" else "/overview"))
        page.get_by_role("link", name=names[IDLE]).first.wait_for()

    def row(sid):
        return page.get_by_role("link", name=names[sid]).first

    def menu(sid):
        r = row(sid)
        r.scroll_into_view_if_needed()
        if size == "wide":
            r.click(button="right")
        else:
            r.hover()
            r.locator("xpath=..").get_by_role("button", name=t("sidebar.agent_actions")).first.click()
        page.get_by_role("menu").first.wait_for()

    def item(label):
        return page.get_by_role("menuitem", name=label)

    get(hub, "/mock/reset")
    open_list()

    # a working agent: Archive waits, saying why; Close… and Close pane both ask, saying what closing interrupts
    menu(WORKING)
    expect(item(t("agentmenus.archive")).first).to_be_disabled()
    expect(page.get_by_text(t("agentmenus.archive_busy")).first).to_be_visible()
    expect(item(t("agentmenus.close")).first).to_be_enabled()
    shot("menu-busy")
    for label in (t("agentmenus.close"), t("lib-node-actions.close_pane")):
        if label != t("agentmenus.close"):
            menu(WORKING)
        item(label).first.click()
        dialog = page.get_by_role("dialog")
        expect(dialog.get_by_text(t("close-agent.busy_working")).first).to_be_visible()
        expect(dialog.get_by_role("button", name=t("close-agent.close_anyway"))).to_be_visible()
        shot("dialog-busy")
        dialog.get_by_role("button", name=t("common.cancel")).click()
        expect(dialog).to_have_count(0)
    expect(row(WORKING)).to_be_visible()
    ok(f"{tag}: a working agent's Archive waits with the reason; Close… and Close pane ask what closing interrupts")

    # an idle one: Close… asks, archives and closes, and the row goes
    menu(IDLE)
    expect(item(t("agentmenus.archive")).first).to_be_enabled()
    expect(item(t("agentmenus.delete_chat")).first).to_have_count(0)
    shot("menu")
    item(t("agentmenus.close")).first.click()
    dialog = page.get_by_role("dialog")
    expect(dialog.get_by_text(t("chat-chatview.close_agent_title")).first).to_be_visible()
    shot("dialog")
    dialog.get_by_role("button", name=t("chat-sessiondialogs.archive_close")).click()
    expect(page.get_by_text(t("lib-agent-archives.archived_closed", title=names[IDLE])).first).to_be_visible()
    expect(row(IDLE)).to_have_count(0)
    shot("closed")
    ok(f"{tag}: Close… asked, archived and closed the agent and took the row out")

    # a plain archive, then Undo
    menu(UNREAD)
    item(t("agentmenus.archive")).first.click()
    toast = page.get_by_text(t("lib-agent-archives.archived", title=names[UNREAD])).first
    expect(toast).to_be_visible()
    expect(row(UNREAD)).to_have_count(0)
    toast.locator("xpath=..").get_by_role("button", name=t("lib-agent-archives.undo")).click()  # this toast's Undo, not an earlier one's
    expect(row(UNREAD)).to_be_visible()
    ok(f"{tag}: Archive took the row out and Undo brought it back")

    # the computer leaves it open: archived, and the toast says why
    get(hub, "/mock/agent_archives?close=refuse")
    menu(UNREAD)
    item(t("agentmenus.close")).first.click()
    page.get_by_role("dialog").get_by_role("button", name=t("chat-sessiondialogs.archive_close")).click()
    expect(page.get_by_text(t("lib-agent-archives.archived_still_open", title=names[UNREAD], reason=MOCK_REFUSAL)).first).to_be_visible()
    expect(row(UNREAD)).to_have_count(0)
    shot("still-open")
    ok(f"{tag}: a refused close leaves it archived and says it is still open")

    # Archived agents: the list, search, restore
    page.goto(base + "/agents/archived")
    rows = page.locator("[data-agent-archives] > div")
    expect(rows).to_have_count(4)
    shot("archived")
    page.get_by_placeholder(t("agents-archives.search_placeholder")).fill("CI")
    expect(rows).to_have_count(1)
    page.get_by_placeholder(t("agents-archives.search_placeholder")).fill("")
    page.get_by_title(t("agents-archives.restore_label", title=names[IDLE])).click()
    expect(page.get_by_text(t("lib-agent-archives.restored", title=names[IDLE])).first).to_be_visible()
    expect(rows).to_have_count(3)
    if get(hub, "/mock/agent_archives").get("archived", []).count(IDLE):
        fail(f"{tag}: the hub still has the restored agent archived")
    ok(f"{tag}: Archived agents lists, searches and restores")

    # an archived chat says so and offers Restore
    page.locator("[data-agent-archives] a").first.click()
    notice = page.locator("[data-archived-notice]")
    expect(notice).to_be_visible()
    shot("chat")
    ok(f"{tag}: an archived chat shows the archived notice with Restore")

    open_list()
    expect(row(IDLE)).to_be_visible()
    ok(f"{tag}: the restored agent is back in the Agents list")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    ap.add_argument("--channel", default="chrome", help="the installed browser Playwright drives (chrome, msedge)")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    get(a.hub, "/mock/reset")
    names = titles(a.hub)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel=a.channel)
        try:
            for size in SIZES:
                for theme in ("light", "dark"):
                    run(browser, a.base, a.hub, a.token, size, theme, a.shots, names)
        finally:
            browser.close()
    print("verdict: ok", flush=True)


if __name__ == "__main__":
    main()
