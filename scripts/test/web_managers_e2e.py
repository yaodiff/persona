#!/usr/bin/env python3
"""Check the web's pages with several assistants (docs/assistant/managers.md "Clients") against the mock hub's three managers.

Run with a dedicated fake hub and the web dev server pointing to it (the default roster: 助手 default, 工作 online,
家里 offline; MOCK_EXTENSIONS=1 for the extensions page, MOCK_MANAGER_NAS=1 gives 家里 the NAS, offline with it, which the
machines checks need; MOCK_MANAGER_OUTDATED=1 marks 工作 needs_update, for --outdated):
    PORT=<port> MOCK_EXTENSIONS=1 MOCK_MANAGER_NAS=1 MOCK_MANAGER_OUTDATED=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
    python3 scripts/test/web_managers_e2e.py http://127.0.0.1:<web port> --outdated [--shots <directory>]
and for one manager (MOCK_MANAGERS=1 MOCK_EXTENSIONS=1 on another mock): web_managers_e2e.py <its web address> --single.
Another assistant's conversation reopened by its address (a reload, a new page) on a hub with the paged sessions list:
a mock with MOCK_SESSIONS_LIST=1 MOCK_MANAGER_NAS=1 and web_managers_e2e.py <web address> --reload.
Adding a computer to the current assistant: a mock with MOCK_ADD_COMPUTER=1 MOCK_ADD_COMPUTER_JOIN=2 and
web_managers_e2e.py <web address> --add-computer; with MOCK_ADD_COMPUTER_NO_MANAGER=1 too (a hub from before add.manager),
--add-computer --old-hub.
A mock with MOCK_MANAGER_NEEDS_YOU=0 as well (a hub whose roster has no inbox counts) runs the same: the counts are then
the agents waiting only.
It moves the NAS to 工作 and back to 家里, so every round (390 / 1200, light / dark) starts alike.

Checks the sidebar's assistant switcher (the current assistant in the sidebar's head; its list with New assistant and
Manage assistants…; the pick names the assistant's requests and survives a reload; an offline one greyed with the
composer off, its inbox and to-dos saying it is offline rather than empty; an installing one opens its install code),
that the machines and inbox pages list only the current assistant's (no manager headings), another assistant's inbox
item opened by its address (its requests name its manager, the Trail's too, and the current assistant stays, also after
Hand to AI on it), the Muted dialog, and Settings › Assistants' Notifications switch. Each round also checks Settings'
three groups ("<name>'s settings" with Switch, This device, Hub), that Switch opens the sidebar's list, and that the
first group then follows the new assistant (its pages' requests name it, Connectors and Extensions list its copies
only); with --single, the title and Switch with one assistant. Once after the rounds: the count beside 工作 (its agents
waiting plus the roster's inbox_needs_you) moving without a reload when the hub pushes `managers`, and every row of the
list reachable and clickable at 200% zoom and at the largest text size. --outdated its Needs update badge; --single
checks the current assistant's name, avatar and status in the sidebar head, its one-row roster with New assistant and
Manage assistants…, and the appearance and settings menu, while the chat title stays product-branded. It also checks
that nothing is filtered and that no request names a manager (no X-Persona-Manager header, no manager= in the address).
Screenshots (390 and 1200 wide, light and dark) contain only mock data.
"""
import argparse
import os
import re
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import Error as PlaywrightError, expect, sync_playwright

# Shared development machines may take longer than Playwright's five-second assertion default.
expect.set_options(timeout=15000)


def go(page, url):
    """Navigate after a closed overlay's pending history entry has been popped."""
    page.wait_for_function("!history.state?.personaOverlay")
    try:
        page.goto(url)
    except PlaywrightError as e:
        if "ERR_ABORTED" not in str(e):
            raise
        page.wait_for_timeout(500)
        page.goto(url)


# What this browser keeps of the pick (lib/manager-choice.ts): the pick and its name, each per hub, both only while the
# pick is another assistant than the default.
KEPT = "Object.fromEntries(Object.entries(localStorage).filter(([k]) => k.startsWith('persona.currentManager') || k.startsWith('persona.pickedAssistantName')))"

# 工作's inbox item opened by its address (the mock hub's MANAGED_INBOX), from under another assistant.
WORK_ITEM = "/inbox/lark%3Af3%3A300?manager=m-3fa21c"

# The mock hub's installed managers (scripts/mock/mock_hub.py), by the name the switcher shows.
MANAGERS = {"助手": "assistant", "工作": "m-3fa21c", "家里": "m-8b04d2"}


def check(browser, base, width, theme, shots, single, outdated):
    context = browser.new_context(viewport={"width": width, "height": 900}, color_scheme=theme, locale="zh-CN")
    context.add_init_script("localStorage.setItem('persona.theme', " + repr(theme) + ")")
    page = context.new_page()
    page.set_default_timeout(15000)
    errors, assistant_headers, api_requests = [], [], []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.on("request", lambda r: "/api/v1/assistant" in r.url and assistant_headers.append(r.headers.get("x-persona-manager")))
    # every request to the hub, with the manager it names (header or query), for the checks below
    page.on("request", lambda r: "/api/" in r.url and api_requests.append((r.method, r.url, r.headers.get("x-persona-manager"), (parse_qs(urlparse(r.url).query).get("manager") or [None])[0])))
    name = lambda what: os.path.join(shots, f"managers-{what}-{width}-{theme}.png")
    try:
        with page.expect_response(lambda r: "/api/v1/managers" in r.url):
            page.goto(base + "/?token=mock")
        main = page.locator("main")
        head = page.locator("[data-assistant-switcher]")

        # a phone's drawer is off screen and inert until opened
        shut = lambda: head.evaluate("(e) => Boolean(e.closest('[inert]'))")

        def open_drawer():
            if width < 1024:
                # once the page is drawn (its sidebar button), and settled after a navigation
                expect(main.get_by_role("button", name="侧边栏").first).to_be_visible()
                page.wait_for_timeout(300)
                if shut():
                    main.get_by_role("button", name="侧边栏").first.click()

        def open_list():
            """The head's menu: the hub's roster, or product settings on a hub without one."""
            open_drawer()
            head.click()
            return page.get_by_role("menu")

        def pick(assistant):
            menu = open_list()
            overlay = page.evaluate("history.state?.personaOverlay")
            menu.get_by_role("menuitemradio").filter(has_text=assistant).click()
            expect(head).to_have_attribute("data-assistant-switcher", MANAGERS[assistant])
            expect(menu).to_have_count(0)
            if width < 1024:
                # Menu and drawer close pop separate history entries asynchronously. Let both settle before page.goto.
                if overlay:
                    page.wait_for_function("(id) => history.state?.personaOverlay !== id", arg=overlay)
                if not shut():
                    page.keyboard.press("Escape")
                page.wait_for_function("!history.state?.personaOverlay")

        if single:
            # signed in and the assistant page drawn (its box)
            page.wait_for_function("localStorage.getItem('persona.token')")
            expect(main.get_by_role("textbox").last).to_be_visible()
            # The installed roster has loaded: the head names even the only assistant; the chat title stays Persona.
            expect(head).to_have_attribute("data-assistant-switcher", MANAGERS["助手"])
            expect(head).to_contain_text("助手")
            expect(head).to_contain_text("在线 · server")
            avatar = head.locator("[data-assistant-avatar]")
            expect(avatar).to_have_attribute("data-assistant-avatar", MANAGERS["助手"])
            expect(avatar).to_be_visible()
            expect(avatar).to_contain_text("助")
            expect(avatar.locator('[data-avatar-status="green"]')).to_be_visible()
            open_drawer()
            if shots:
                page.wait_for_timeout(400)
                page.screenshot(path=name("single"))
            menu = open_list()
            radio = menu.get_by_role("menuitemradio")
            expect(radio).to_have_count(1)
            expect(radio).to_have_attribute("aria-checked", "true")
            expect(radio).to_contain_text("助手")
            expect(radio).to_contain_text("在线 · server")
            expect(radio.get_by_text("默认", exact=True)).to_have_count(0)
            expect(menu.get_by_role("menuitem", name="新建助手")).to_be_visible()
            expect(menu.get_by_role("menuitem", name="管理助手…")).to_be_visible()
            expect(menu.get_by_text("外观", exact=True)).to_be_visible()
            for item in ("跟随系统", "浅色", "深色", "设置", "断开这个浏览器…"):
                expect(menu.get_by_role("menuitem", name=item, exact=True)).to_be_visible()
            if shots:
                page.screenshot(path=name("single-menu"))
            page.goto(base + "/machines")
            expect(main.get_by_text("MacBook", exact=True).first).to_be_visible()
            # the pages that name a manager with several: with one, none of their requests may name it
            page.goto(base + "/inbox")
            row = main.locator("[data-item]").first
            expect(row).to_be_visible()
            row.locator("a").first.click()
            page.wait_for_url(re.compile(r"/inbox/[^/?]+"))
            assert "manager=" not in page.url, page.url
            page.wait_for_timeout(1200)
            page.goto(base + "/inbox")  # a phone shows the list again only without an item open
            main.get_by_role("button", name="更多操作", exact=True).first.click()
            page.get_by_role("menuitem", name="已静音").click()
            expect(page.get_by_role("dialog")).to_be_visible()
            page.wait_for_timeout(400)
            page.keyboard.press("Escape")
            expect(page.get_by_role("dialog")).to_have_count(0)  # closing it goes back in history: let it land
            go(page, base + "/settings?tab=extensions")
            main.locator("[data-extension-row]").first.click()  # an extension's page
            page.wait_for_url(re.compile(r"extension="))
            page.wait_for_timeout(1200)
            page.goto(base + "/health")
            page.wait_for_timeout(1000)
            page.goto(base + "/machines/macbook")
            page.wait_for_timeout(1000)
            named = [r for r in api_requests if r[2] or r[3]]
            assert not named, named[:5]
            assert len(api_requests) > 20, len(api_requests)
            assert page.evaluate(KEPT) == {}, page.evaluate(KEPT)
            assert not errors, errors
            return

        # the list: the three installed ones (the default first, tagged), the one installing, then the two actions
        expect(main.get_by_role("textbox").last).to_be_visible()
        menu = open_list()
        radios = menu.get_by_role("menuitemradio")
        expect(radios).to_have_count(3)
        expect(radios.nth(0)).to_have_attribute("aria-checked", "true")
        expect(radios.nth(0)).to_contain_text("默认")
        expect(radios.nth(0)).to_contain_text("在线 · server")
        expect(radios.nth(2)).to_contain_text("离线 · 最后同步于")
        expect(menu.get_by_role("menuitem").filter(has_text="安装中")).to_have_count(1)
        expect(menu.get_by_role("menuitem", name="新建助手")).to_be_visible()
        expect(menu.get_by_role("menuitem", name="管理助手…")).to_be_visible()
        # the segmented switcher at the assistant page's top is gone
        expect(page.get_by_role("radiogroup", name="管理者")).to_have_count(0)
        expect(page.get_by_role("radiogroup", name="助手")).to_have_count(0)
        if shots:
            page.wait_for_timeout(500)
            page.screenshot(path=name("switcher"))
        assert all(h is None for h in assistant_headers), assistant_headers
        # the one still installing: its install code, not a switch
        menu.get_by_role("menuitem").filter(has_text="安装中").click()
        dialog = page.get_by_role("dialog").filter(has_text="安装码")
        expect(dialog).to_be_visible()
        expect(head).to_have_attribute("data-assistant-switcher", "assistant")
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("install-code"))
        dialog.get_by_role("button", name="完成", exact=True).click()
        expect(dialog).to_have_count(0)

        # 工作: the whole app is its (the assistant's requests name it), and it is still the pick after a reload
        pick("工作")
        expect(page.get_by_text("已切换到 工作", exact=True)).to_be_visible()
        expect(main.get_by_role("textbox").last).to_be_enabled()
        page.wait_for_timeout(500)
        assert "m-3fa21c" in assistant_headers, assistant_headers
        if shots:
            page.wait_for_timeout(1700)  # the toast has gone
            page.screenshot(path=name("assistant"))
        page.reload()
        expect(main.get_by_role("textbox").last).to_be_enabled()
        assert page.evaluate(KEPT) == {"persona.currentManager": "m-3fa21c", "persona.pickedAssistantName": "工作"}, page.evaluate(KEPT)
        del assistant_headers[:]
        page.wait_for_timeout(800)

        # its computers and its inbox only, with no manager's name over them
        page.goto(base + "/machines")
        expect(main.get_by_text("MacBook", exact=True).first).to_be_visible()
        expect(main.get_by_text("Mac Studio", exact=True)).to_have_count(0)
        assert all(h == "m-3fa21c" for h in assistant_headers) and assistant_headers, assistant_headers
        if shots:
            page.wait_for_timeout(600)
            page.screenshot(path=name("machines"))
        page.goto(base + "/inbox")
        expect(main.locator('[data-item="lark:f3:300"]')).to_be_visible()
        assert main.locator("[data-item]").evaluate_all("(rows) => rows.every((r) => r.dataset.manager === 'm-3fa21c')")
        if shots:
            page.wait_for_timeout(600)
            page.screenshot(path=name("inbox"))
        # its item: its address names 工作, and so does every request about it (the item, its Trail, its Lark chat)
        before = len(api_requests)
        main.locator('[data-item="lark:f3:300"] a').first.click()
        expect(page).to_have_url(re.compile(r"/inbox/lark(:|%3A)f3(:|%3A)300\?manager=m-3fa21c$"))
        page.wait_for_timeout(1500)
        about = [r for r in api_requests[before:] if "/inbox/tree/inbox/lark:f3:300" in r[1] or "/lark/conversations/f3" in r[1]]
        assert about, api_requests[before:]
        assert all("m-3fa21c" in (r[2], r[3]) for r in about), about
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("inbox-item"))
        # the Muted dialog: the current assistant's, asked of it by name
        page.goto(base + "/inbox")
        main.get_by_role("button", name="更多操作", exact=True).first.click()
        page.get_by_role("menuitem", name="已静音").click()
        dialog = page.get_by_role("dialog")
        expect(dialog).to_be_visible()
        page.wait_for_timeout(600)
        asked = [r for r in api_requests if r[1].split("?")[0].endswith("/inbox/mutes")]
        assert asked and all(r[2] == "m-3fa21c" for r in asked), asked
        expect(dialog.locator("[data-mutes-manager]:not([data-mutes-manager='m-3fa21c'])")).to_have_count(0)
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("muted"))
        page.keyboard.press("Escape")
        expect(page.get_by_role("dialog")).to_have_count(0)  # closing it goes back in history: let it land

        # 家里, offline: greyed in the list, its page says so and nothing can be sent
        go(page, base + "/")
        pick("家里")
        box = main.get_by_role("textbox").last
        expect(box).to_be_disabled()
        assert "最后同步于" in (box.get_attribute("placeholder") or ""), box.get_attribute("placeholder")
        expect(main.get_by_text("家里离线，能看历史，暂时不能发消息。", exact=True)).to_be_visible()
        if shots:
            page.wait_for_timeout(1700)
            page.screenshot(path=name("assistant-offline"))
        # its NAS: offline with its assistant, said in our words (not the hub's), and no Reconnect through it
        page.goto(base + "/machines")
        nas_card = main.locator("section:not([data-testid])").filter(has=page.get_by_text("NAS", exact=True))
        expect(nas_card.locator('[data-error-code="manager_offline"]')).to_have_text("它的助手「家里」离线，助手回来后这台电脑会恢复在线。")
        expect(nas_card.get_by_role("button", name="重新连接")).to_have_count(0)
        assert "助手暂时连不上" not in (nas_card.inner_text() or ""), nas_card.inner_text()
        expect(main.get_by_text("MacBook", exact=True)).to_have_count(0)
        if shots:
            page.wait_for_timeout(600)
            page.screenshot(path=name("machines-offline"))
        # its inbox and to-dos: the hub's merged lists leave it out, so they say it is offline, not empty
        page.goto(base + "/inbox")
        offline = main.locator('[data-assistant-offline="m-8b04d2"]')
        expect(offline).to_contain_text("家里 离线")
        expect(offline).to_contain_text("最后同步于")
        expect(offline).to_contain_text("它上线后这里会更新。")
        expect(main.locator("[data-inbox-no-sources]")).to_have_count(0)
        expect(main.get_by_text("都处理完了", exact=True)).to_have_count(0)
        if shots:
            page.wait_for_timeout(600)
            page.screenshot(path=name("inbox-offline"))
        page.goto(base + "/todos")
        expect(main.locator('[data-assistant-offline="m-8b04d2"]')).to_contain_text("家里 离线")
        expect(main.get_by_text("没有待办", exact=True)).to_have_count(0)
        if shots:
            page.wait_for_timeout(600)
            page.screenshot(path=name("todos-offline"))

        # back on the default: nothing is named again, and the pick is forgotten
        go(page, base + "/")
        pick("助手")
        expect(main.get_by_role("textbox").last).to_be_enabled()
        assert page.evaluate(KEPT) == {}, page.evaluate(KEPT)
        # another assistant's item opened by its address: the item opens, the current assistant stays
        go(page, base + WORK_ITEM)
        expect(main.get_by_text("周末徒步群", exact=False).first).to_be_visible()
        if width >= 1024:
            expect(head).to_have_attribute("data-assistant-switcher", "assistant")
        assert page.evaluate(KEPT) == {}, page.evaluate(KEPT)
        # Hand to AI on it: handed off through 工作, its conversation opens, and the current assistant is still the default
        before = len(api_requests)
        main.get_by_role("button", name="交给 AI", exact=True).first.click()
        dialog = page.get_by_role("dialog").filter(has_text="交给 AI")
        expect(dialog).to_be_visible()
        dialog.get_by_role("button", name="发送", exact=True).click()
        expect(dialog).to_have_count(0)
        # its address names its computer and manager (lib/conversation-page.ts): 工作's assistant runs on the MacBook
        page.wait_for_url(re.compile(r"/persona/[^/?]+\?machine=macbook&manager=m-3fa21c$"))
        handed = [r for r in api_requests[before:] if r[0] == "POST" and re.search(r"(:|%3A)f3(:|%3A)300/handoff", r[1])]
        assert handed and handed[0][2] == "m-3fa21c", api_requests[before:]
        expect(head).to_have_attribute("data-assistant-switcher", "assistant")
        expect(page.get_by_text("已切换到", exact=False)).to_have_count(0)
        assert page.evaluate(KEPT) == {}, page.evaluate(KEPT)
        page.wait_for_timeout(500)
        if width >= 1024:
            expect(head).to_contain_text("助手")

        # Settings › Extensions is the current assistant's (the default's here): its copies only, no assistant's name over them
        page.goto(base + "/settings?tab=extensions")
        rows = main.locator("[data-extension-row]")
        expect(rows.first).to_be_visible()
        assert set(rows.evaluate_all("(rs) => rs.map((r) => r.dataset.manager)")) == {"assistant"}
        expect(main.get_by_text("工作", exact=True)).to_have_count(0)

        go(page, base + "/settings?tab=managers")
        work = main.get_by_role("button").filter(has=page.get_by_text("工作", exact=True)).first
        expect(work).to_be_visible()
        if outdated:
            expect(work.get_by_text("需要更新", exact=True)).to_be_visible()
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("list"))
        work.click()
        dialog = page.get_by_role("dialog")
        switch = dialog.get_by_role("switch", name="推送通知")
        expect(switch).to_have_attribute("aria-checked", "true")
        if outdated:
            expect(dialog.get_by_text("请在它所在的电脑上更新 personad", exact=False)).to_be_visible()
            if shots:
                page.wait_for_timeout(400)
                page.screenshot(path=name("detail"))
        switch.click()
        expect(switch).to_have_attribute("aria-checked", "false")
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("push"))
        switch.click()
        expect(switch).to_have_attribute("aria-checked", "true")
        # 删除 says what goes with it: its computers come back, its sources don't
        dialog.get_by_role("button", name="删除", exact=True).click()
        expect(dialog.get_by_text("它的 1 台电脑会回到 hub 自己的助手。", exact=True)).to_be_visible()
        expect(dialog.get_by_text("不先移到别的助手", exact=False)).to_be_visible()
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("delete"))
        dialog.get_by_role("button", name="保留", exact=True).click()
        dialog.get_by_role("button", name="完成", exact=True).click()
        expect(dialog).to_have_count(0)

        # 移到… on a computer's page: the NAS (家里's, offline with it) goes to 工作, then back
        page.goto(base + "/machines/nas")
        expect(main.locator('[data-error-code="manager_offline"]')).to_be_visible()
        expect(main.get_by_role("button", name="重新连接")).to_have_count(0)
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("machine-offline"))
        main.get_by_role("button", name="移到…", exact=True).click()
        menu = page.get_by_role("menu")
        expect(menu.get_by_role("menuitem").filter(has_text="家里")).to_contain_text("离线")
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("move"))
        menu.get_by_role("menuitem").filter(has_text="工作").click()
        expect(page.get_by_text("已移到「工作」", exact=True)).to_be_visible()
        main.get_by_role("button", name="移到…", exact=True).click()
        page.get_by_role("menu").get_by_role("menuitem").filter(has_text="家里").click()
        expect(page.get_by_text("已移到「家里」", exact=True)).to_be_visible()
        assert not errors, errors
    except Exception:
        # This driver is for mock data only; retain the actual page when a selector or browser fails.
        print("failed mock page:", page.url, "errors:", errors)
        if shots:
            try:
                page.screenshot(path=name("failure"), timeout=5000)
            except PlaywrightError:
                pass
        raise
    finally:
        context.close()


def agents_waiting(request, base, auth, manager):
    """The agents waiting at manager, counted here from the hub's lists by the rule every client shares
    (docs/assistant/managers.md "The current assistant"): its sessions that are not a quiet task's and not muted, with
    a pending card or in Needs you, once each; a pending card on no session, each."""
    sessions = request.get(base + "/api/v1/sessions", headers=auth).json()["sessions"]
    cards = request.get(base + "/api/v1/interactions", headers=auth).json()["interactions"]
    mutes = request.get(base + "/api/v1/mutes", headers=auth).json()
    key = lambda mid, sid: f"{mid}/{sid}"

    def muted(mid, sid, space):
        k = key(mid, sid)
        if k in (mutes.get("sessions") or []):
            return True
        if k in (mutes.get("allowed") or []):
            return False
        return bool(space) and key(mid, space) in (mutes.get("spaces") or [])

    by_key = {key(s["machine_id"], s["session_id"]): s for s in sessions}
    counted = lambda s: not s.get("quiet") and not muted(s["machine_id"], s["session_id"], s.get("space_name"))
    waiting = set()
    for s in sessions:
        if s.get("manager") == manager and not s.get("assistant") and counted(s) and (s.get("state") or s.get("status")) == "needs_you":
            waiting.add(key(s["machine_id"], s["session_id"]))
    for c in cards:
        if c.get("manager") != manager:
            continue
        if not c.get("session_id"):
            waiting.add("card:" + c["id"])
            continue
        s = by_key.get(key(c["machine_id"], c["session_id"]))
        if s is not None and not counted(s) or s is None and muted(c["machine_id"], c["session_id"], None):
            continue
        waiting.add(key(c["machine_id"], c["session_id"]))
    return len(waiting)


def check_settings(browser, base, width, theme, shots, single):
    """Settings' three groups (docs/assistant/managers.md "The current assistant"): "<name>'s settings" with the current
    assistant's avatar and Switch, This device and Hub, in that order with their lines; Switch opens the sidebar's list
    and the first group then follows the new assistant (its pages' requests name it, Connectors and Extensions list its
    copies only), and with an offline one Connectors says it is offline; one assistant shows the title and Switch too."""
    context = browser.new_context(viewport={"width": width, "height": 900}, color_scheme=theme, locale="zh-CN")
    context.add_init_script("localStorage.setItem('persona.theme', " + repr(theme) + ")")
    page = context.new_page()
    page.set_default_timeout(15000)
    errors, named = [], []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.on("request", lambda r: "/api/" in r.url and named.append((urlparse(r.url).path, r.headers.get("x-persona-manager"))))
    name = lambda what: os.path.join(shots, f"settings-{what}-{width}-{theme}.png")
    phone = width < 640
    groups_of = "[data-settings-group^='nav-']" if phone else "[data-settings-nav-group]"
    key = "settingsGroup" if phone else "settingsNavGroup"
    try:
        page.goto(base + "/?token=mock")
        page.wait_for_function("localStorage.getItem('persona.token')")
        page.goto(base + "/settings")
        groups = page.locator(groups_of)
        expect(groups).to_have_count(3)
        assert [g.replace("nav-", "") for g in groups.evaluate_all(f"(gs) => gs.map((g) => g.dataset.{key})")] == ["assistant", "device", "hub"]
        first, last = groups.nth(0), groups.nth(2)
        expect(first).to_contain_text("助手 的设置")
        expect(first.locator("[data-assistant-avatar]")).to_have_attribute("data-assistant-avatar", "assistant")
        expect(first.locator("[data-settings-switch]")).to_have_text("切换")
        expect(first).to_contain_text("只显示「助手」自己的内容。要改别的助手，先切换过去。")
        expect(groups.nth(1)).to_contain_text("这台设备")
        expect(last).to_contain_text("Hub")
        expect(last).to_contain_text("这两组和当前是哪个助手无关。")
        if shots:
            page.wait_for_timeout(500)
            page.screenshot(path=name("home"))
        switch = first.locator("[data-settings-switch]")
        switch.click()
        menu = page.get_by_role("menu")
        expect(menu.get_by_role("menuitem", name="新建助手")).to_be_visible()
        expect(menu.get_by_role("menuitem", name="管理助手…")).to_be_visible()
        if single:
            expect(menu.get_by_role("menuitemradio")).to_have_count(1)
            if shots:
                page.wait_for_timeout(400)
                page.screenshot(path=name("switch-single"))
            page.keyboard.press("Escape")
            assert all(m is None for _, m in named), [x for x in named if x[1]][:5]
            assert not errors, errors
            return
        expect(menu.get_by_role("menuitemradio")).to_have_count(3)
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("switch"))
        menu.get_by_role("menuitemradio").filter(has_text="工作").click()
        expect(first).to_contain_text("工作 的设置")
        expect(first.locator("[data-assistant-avatar]")).to_have_attribute("data-assistant-avatar", MANAGERS["工作"])
        expect(first).to_contain_text("只显示「工作」自己的内容。要改别的助手，先切换过去。")

        # the first group's pages are 工作's: Connectors and Extensions list its copies only, and its pages ask it
        def opened(tab):
            del named[:]
            go(page, base + f"/settings?tab={tab}")
            page.wait_for_timeout(1200)

        opened("connectors")
        rows = page.locator("main [data-connector-row]")
        expect(rows.first).to_be_visible()
        assert set(rows.evaluate_all("(rs) => rs.map((r) => r.dataset.manager)")) == {MANAGERS["工作"]}
        mail = [m for p, m in named if p.startswith("/api/v1/mail/accounts")]
        assert mail and all(m == MANAGERS["工作"] for m in mail), mail
        if shots:
            page.screenshot(path=name("connectors"))
        opened("extensions")
        rows = page.locator("main [data-extension-row]")
        expect(rows.first).to_be_visible()
        assert set(rows.evaluate_all("(rs) => rs.map((r) => r.dataset.manager)")) == {MANAGERS["工作"]}
        if shots:
            page.screenshot(path=name("extensions"))
        opened("persona")
        for path in ("/api/v1/assistant", "/api/v1/agents/triage"):
            asked = [m for p, m in named if p == path]
            assert asked and all(m == MANAGERS["工作"] for m in asked), (path, asked)
        opened("models")
        asked = [m for p, m in named if p == "/api/v1/launchers"]
        assert asked and all(m == MANAGERS["工作"] for m in asked), asked
        # a page of this device's names nobody
        opened("general")
        assert all(m is None for p, m in named if not p.startswith("/api/v1/assistant")), [x for x in named if x[1]][:5]
        # 家里, offline: the hub's merged list leaves its connectors out, so Connectors says it is offline instead of none
        go(page, base + "/settings")
        page.locator(groups_of).nth(0).locator("[data-settings-switch]").click()
        page.get_by_role("menu").get_by_role("menuitemradio").filter(has_text="家里").click()
        expect(page.locator(groups_of).nth(0)).to_contain_text("家里 的设置")
        opened("connectors")
        expect(page.locator(f'main [data-assistant-offline="{MANAGERS["家里"]}"]')).to_be_visible()
        expect(page.locator("main [data-connector-row]")).to_have_count(0)
        if shots:
            page.screenshot(path=name("connectors-offline"))
        # back to 助手, from the same Switch (the list the sidebar shows)
        go(page, base + "/settings")
        page.locator(groups_of).nth(0).locator("[data-settings-switch]").click()
        page.get_by_role("menu").get_by_role("menuitemradio").filter(has_text="助手").click()
        expect(page.locator(groups_of).nth(0)).to_contain_text("助手 的设置")
        assert page.evaluate(KEPT) == {}, page.evaluate(KEPT)
        assert not errors, errors
    finally:
        context.close()


def check_counts(browser, base):
    """The count beside 工作 is its agents waiting (counted here from the hub's lists) plus the roster's inbox_needs_you,
    0 when the roster has none (a hub from before it, MOCK_MANAGER_NEEDS_YOU=0). Where the roster has it, the count moves
    without a reload when the hub pushes `managers`: one of its inbox items that need the user handled elsewhere (as
    another device would), then brought back."""
    context = browser.new_context(viewport={"width": 1200, "height": 900}, locale="zh-CN")
    page = context.new_page()
    page.set_default_timeout(15000)
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    auth = {"Authorization": "Bearer mock"}
    try:
        page.goto(base + "/?token=mock")
        page.wait_for_function("localStorage.getItem('persona.token')")
        head = page.locator("[data-assistant-switcher]")
        expect(head).to_have_attribute("data-assistant-switcher", "assistant")

        def work():
            roster = page.request.get(base + "/api/v1/managers", headers=auth).json()
            return next(m for m in roster["managers"] if m["id"] == MANAGERS["工作"])

        def needs_you():
            return work().get("inbox_needs_you", 0)

        agents = agents_waiting(page.request, base, auth, MANAGERS["工作"])
        has_field = "inbox_needs_you" in work()
        before_needs = needs_you()
        head.click()
        badge = page.get_by_role("menu").get_by_role("menuitemradio").filter(has_text="工作").locator("[data-menu-badge]")
        expect(badge).to_have_text(str(agents + before_needs))
        before = int(badge.inner_text())
        assert agents > 0, agents
        if not has_field:
            # a hub without the field: the agents only, and nothing an inbox change pushes moves it
            assert before == agents, (before, agents)
            assert not errors, errors
            return
        assert before_needs > 0, before_needs

        def done(item_id, on):
            r = page.request.post(f"{base}/api/v1/inbox/{item_id}/done", headers={**auth, "X-Persona-Manager": MANAGERS["工作"]}, data={"done": on})
            assert r.ok, r.status

        # 工作's open items: the first whose handling moves its count
        items = page.request.get(base + "/api/v1/inbox?sources=all", headers={**auth, "X-Persona-Manager": MANAGERS["工作"]}).json()["items"]
        moved = None
        for item in [x for x in items if x.get("manager") == MANAGERS["工作"] and not x.get("done_at")]:
            done(item["id"], True)
            if needs_you() != before_needs:
                moved = item["id"]
                break
            done(item["id"], False)
        assert moved, [x["id"] for x in items]
        after = agents + needs_you()
        # the list left open shows it once the push has been read: no reload, no reopening
        expect(badge).to_have_text(str(after), timeout=8000)
        done(moved, False)
        expect(badge).to_have_text(str(before), timeout=8000)
        assert not errors, errors
    finally:
        context.close()


def check_reach(browser, base, shots):
    """Every row of the list can be scrolled to and clicked at 200% zoom (a 1200 x 900 window is 600 x 450 CSS pixels)
    and at the largest text size: New assistant, Manage assistants…, the three looks under Appearance, Settings and
    Disconnect."""
    for what, viewport, text_size in (("zoom200", {"width": 600, "height": 450}, None), ("large", {"width": 390, "height": 700}, "large")):
        context = browser.new_context(viewport=viewport, device_scale_factor=2, locale="zh-CN", color_scheme="light")
        if text_size:
            context.add_init_script("localStorage.setItem('persona.text-size', " + repr(text_size) + ")")
        page = context.new_page()
        page.set_default_timeout(15000)
        errors = []
        page.on("pageerror", lambda error: errors.append(str(error)))
        try:
            page.goto(base + "/?token=mock")
            main = page.locator("main")
            head = page.locator("[data-assistant-switcher]")

            def open_list():
                expect(main.get_by_role("button", name="侧边栏").first).to_be_visible()
                page.wait_for_timeout(300)
                if head.evaluate("(e) => Boolean(e.closest('[inert]'))"):
                    main.get_by_role("button", name="侧边栏").first.click()
                head.click()
                return page.get_by_role("menu")

            menu = open_list()
            if shots:
                page.wait_for_timeout(400)
                page.screenshot(path=os.path.join(shots, f"managers-reach-{what}-top.png"))
            for name in ("新建助手", "管理助手…", "跟随系统", "浅色", "深色", "设置", "断开这个浏览器…"):
                row = menu.get_by_role("menuitem", name=name, exact=True)
                row.scroll_into_view_if_needed()
                box = row.bounding_box()
                assert box and box["y"] >= 0 and box["y"] + box["height"] <= viewport["height"] + 0.5, (what, name, box)
                # actionable where it is: visible, still, and nothing over it takes the click
                row.click(trial=True)
            if shots:
                page.wait_for_timeout(300)
                page.screenshot(path=os.path.join(shots, f"managers-reach-{what}-end.png"))
            # a row reached by scrolling does what it says (not Disconnect, which signs this browser out at once):
            # Settings opens Settings
            menu.get_by_role("menuitem", name="设置", exact=True).click()
            expect(page).to_have_url(re.compile(r"/settings"))
            # and Manage assistants… opens Settings › Assistants
            menu = open_list()
            row = menu.get_by_role("menuitem", name="管理助手…", exact=True)
            row.scroll_into_view_if_needed()
            row.click()
            expect(page).to_have_url(re.compile(r"/settings\?tab=managers"))
            assert not errors, errors
        finally:
            context.close()


def check_reload(browser, base, width, theme):
    """Another assistant's conversation opened by its address alone, on a hub with the paged sessions list
    (MOCK_SESSIONS_LIST=1: no `sessions` pushes to find it in): Hand to AI on 工作's item under the default opens 工作's
    conversation on the MacBook; after a reload, and in a new page at the same address, every request about it still
    goes to the MacBook (the exact lookup there brings its row) and the composer takes a message, the default staying
    current."""
    context = browser.new_context(viewport={"width": width, "height": 900}, color_scheme=theme, locale="zh-CN")
    context.add_init_script("localStorage.setItem('persona.theme', " + repr(theme) + ")")
    errors, about = [], []
    sid = {"id": ""}

    def watch(page):
        page.on("pageerror", lambda error: errors.append(str(error)))
        # the conversation's session routes and its exact lookup, with the computer each names
        page.on("request", lambda r: sid["id"] and sid["id"] in r.url and "/api/v1/sessions" in r.url and about.append(r.url))

    try:
        page = context.new_page()
        page.set_default_timeout(20000)
        watch(page)
        page.goto(base + "/?token=mock")
        main = page.locator("main")
        expect(main.get_by_role("textbox").last).to_be_visible()
        go(page, base + WORK_ITEM)
        main.get_by_role("button", name="交给 AI", exact=True).first.click()
        dialog = page.get_by_role("dialog").filter(has_text="交给 AI")
        dialog.get_by_role("button", name="发送", exact=True).click()
        page.wait_for_url(re.compile(r"/persona/[^/?]+\?machine=macbook&manager=m-3fa21c$"))
        address = page.url
        sid["id"] = urlparse(address).path.rsplit("/", 1)[-1]

        def opened(p):
            del about[:]
            box = p.locator("main").get_by_role("textbox").last
            expect(box).to_be_enabled()
            p.wait_for_timeout(1500)
            assert about, "no request about the conversation"
            wrong = [u for u in about if "/sessions/macbook/" not in u and "machine_id=macbook" not in u]
            assert not wrong, wrong
            assert any("machine_id=macbook" in u and "session_id=" in u for u in about) or any("/sessions/macbook/" in u for u in about), about
            expect(p.locator("[data-assistant-switcher]")).to_have_attribute("data-assistant-switcher", "assistant")
            assert p.evaluate(KEPT) == {}, p.evaluate(KEPT)

        page.reload()
        opened(page)
        other = context.new_page()
        other.set_default_timeout(20000)
        watch(other)
        other.goto(address)
        opened(other)
        assert not errors, errors
    finally:
        context.close()


def check_add_computer(browser, base, width, theme, shots, old):
    """Add computer with 工作 current: the code names it (add.manager), and the computer joins 工作's list with nothing
    said on the waiting screen; a hub from before add.manager (old) puts it under its own assistant, which the waiting
    screen says. With 家里 (offline) current nothing can be added and the dialog says why."""
    context = browser.new_context(viewport={"width": width, "height": 900}, color_scheme=theme, locale="zh-CN")
    context.add_init_script("localStorage.setItem('persona.theme', " + repr(theme) + ")")
    # the pick as the switcher keeps it (lib/manager-choice.ts)
    context.add_init_script("if (!localStorage.getItem('persona.currentManager')) localStorage.setItem('persona.currentManager', 'm-3fa21c')")
    page = context.new_page()
    page.set_default_timeout(20000)
    errors, asked = [], []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.on("request", lambda r: r.method == "POST" and r.url.split("?")[0].endswith("/api/v1/auth/pair-codes") and asked.append(r.post_data_json))
    name = lambda what: os.path.join(shots, f"managers-{what}-{width}-{theme}.png")
    try:
        page.goto(base + "/machines?token=mock")
        main = page.locator("main")
        expect(page.locator("[data-assistant-switcher]")).to_have_attribute("data-assistant-switcher", "m-3fa21c")
        main.get_by_role("button", name="添加电脑", exact=True).first.click()
        dialog = page.get_by_role("dialog").filter(has_text="添加电脑")
        computer = f"e2e 电脑 {width} {theme}"
        dialog.locator("input").fill(computer)
        dialog.get_by_role("button", name="下一步", exact=True).click()
        expect(dialog.locator('[data-stage="waiting"]')).to_be_visible()
        assert asked and asked[-1]["add"].get("manager") == "m-3fa21c", asked
        under = dialog.locator("[data-added-under]")
        if old:
            expect(under).to_have_text("这台电脑加到了「助手」名下。")
        else:
            expect(under).to_have_count(0)
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=name("add-under" if old else "add-waiting"))
        # it joins (MOCK_ADD_COMPUTER_JOIN): It's mine, then the list
        dialog.get_by_role("button", name="是我的电脑", exact=True).click(timeout=30000)
        dialog.get_by_role("button", name="完成", exact=True).click()
        expect(dialog).to_have_count(0)
        listed = main.get_by_text(computer, exact=True)
        if old:
            expect(listed).to_have_count(0)  # the hub's own assistant's, not 工作's
        else:
            expect(listed.first).to_be_visible()
        # 家里, offline: nothing is added to it
        page.evaluate("localStorage.setItem('persona.currentManager', 'm-8b04d2')")
        page.reload()
        expect(page.locator("[data-assistant-switcher]")).to_have_attribute("data-assistant-switcher", "m-8b04d2")
        before = len(asked)
        main.get_by_role("button", name="添加电脑", exact=True).first.click()
        dialog = page.get_by_role("dialog").filter(has_text="添加电脑")
        expect(dialog.locator('[data-stage="assistant-offline"]')).to_have_text("「家里」离线，暂时不能给它添加电脑。")
        expect(dialog.get_by_role("button", name="下一步", exact=True)).to_have_count(0)
        if shots and not old:
            page.wait_for_timeout(400)
            page.screenshot(path=name("add-offline"))
        assert len(asked) == before, asked
        assert not errors, errors
    finally:
        context.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots")
    ap.add_argument("--single", action="store_true")
    ap.add_argument("--outdated", action="store_true")
    ap.add_argument("--reload", action="store_true")
    ap.add_argument("--add-computer", action="store_true")
    ap.add_argument("--old-hub", action="store_true")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            if a.reload:
                for width in (390, 1200):
                    check_reload(browser, a.base.rstrip("/"), width, "light")
                print("web managers e2e (reload): ok")
                return
            if a.add_computer:
                for width in (390, 1200):
                    for theme in ("light", "dark"):
                        check_add_computer(browser, a.base.rstrip("/"), width, theme, a.shots, a.old_hub)
                print("web managers e2e (add computer%s): ok" % (", old hub" if a.old_hub else ""))
                return
            for width in (390, 1200):
                for theme in ("light", "dark"):
                    check(browser, a.base.rstrip("/"), width, theme, a.shots, a.single, a.outdated)
                    check_settings(browser, a.base.rstrip("/"), width, theme, a.shots, a.single)
            if not a.single:
                check_counts(browser, a.base.rstrip("/"))
                check_reach(browser, a.base.rstrip("/"), a.shots)
        finally:
            browser.close()
    print("web managers e2e: ok")


if __name__ == "__main__":
    main()
