#!/usr/bin/env python3
"""End-to-end check of the web's dev server preview panel (docs/web/preview.md, #150): the session's ⋯ › Preview opens the
running server's page; the address bar goes to another page; Pick element outlines, a click opens the comment box and
the comment lands above the message box with the page and the element; a screenshot lands in the message box; a link
elsewhere is offered in a new tab; Phone size; the servers menu; a server that is not running is started after its
command is confirmed (the node is asked with the session's workspace) and shown once it answers; and the states with
nothing to show: no launch.json, no browser on the computer, a node from before previews. On a phone with only a finger
(#11511): a scroll, Pick and Go leave the keyboard down and the page its size; a tap focuses the keys field once it lifts.

Needs a running web page against the fake hub with MOCK_PREVIEW=1 (scripts/mock/mock_preview.py: its GET
/mock/preview?mode= switches the state, and lists what the panel sent) and Playwright for Python:

    PORT=18871 MOCK_PREVIEW=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18871 npx vite --port 5871 --strictPort &)
    python3 scripts/test/web_preview_e2e.py http://127.0.0.1:5871 --hub http://127.0.0.1:18871 [--shots <dir>]

The pictures of the made-up page are drawn by the browser this computer has (PERSONA_CHROME, else Playwright's). Prints
one line per check and a verdict; exits 1 on the first failure. Runs light and dark at desktop width, and light at phone
width, then the touch phone at 390; --shots saves each state there. --channel msedge, or --executable <path>, where
there is no Chrome.
"""
import argparse
import json
import math
import os
import sys
import urllib.request

from playwright.sync_api import TimeoutError as PlaywrightTimeout, expect, sync_playwright

SIZES = {"wide": {"width": 1440, "height": 900}, "narrow": {"width": 390, "height": 844}}
MID = "macbook"
SID = "a1f3c2d4-0000-4000-8000-000000000001"
UA = ("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 "
      "(KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
KEYS = "在预览中输入"  # the panel's keys field (preview.keyboard_label)

# iOS: a focused text field raises the keyboard, which shrinks only the visual viewport. A stand-in the page follows
# (as web_mobile_input_e2e.py's), up while a text field has focus; __raised counts how often it came up.
IOS_KEYBOARD = """(() => {
  const et = new EventTarget();
  let up = false;  // innerHeight is read late: before the viewport tag is parsed it is the 980-pixel layout's
  const vv = { get height() { return innerHeight - (up ? 336 : 0); }, get width() { return innerWidth; }, offsetTop: 0, offsetLeft: 0,
    scale: 1, pageTop: 0, addEventListener: et.addEventListener.bind(et), removeEventListener: et.removeEventListener.bind(et) };
  Object.defineProperty(window, 'visualViewport', { get: () => vv });
  window.__raised = 0;
  const typing = (e) => !!e && (e.tagName === 'TEXTAREA' || e.isContentEditable ||
    (e.tagName === 'INPUT' && !['checkbox', 'radio', 'button', 'submit', 'range'].includes(e.type)));
  const follow = () => {
    if (up === typing(document.activeElement)) return;
    up = !up;
    if (up) window.__raised++;
    et.dispatchEvent(new Event('resize'));
  };
  addEventListener('focusin', () => setTimeout(follow));
  addEventListener('focusout', () => setTimeout(follow));
})();"""


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def mock(hub, token, query=""):
    req = urllib.request.Request(f"{hub}/mock/preview{query}", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def run(browser, base, hub, token, size, theme, shots, full):
    tag = f"{size}/{theme}"
    narrow = size == "narrow"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=narrow, locale="zh-CN")
    ctx.grant_permissions(["clipboard-read", "clipboard-write"])
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    panel = page.locator("[data-preview-panel]")
    canvas = page.locator("[data-preview-canvas]")

    def shot(name):
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=os.path.join(shots, f"preview-{size}-{theme}-{name}.png"))

    def open_panel(mode):
        mock(hub, token, "?mode=" + mode + "&origin=0")
        page.evaluate(f"sessionStorage.clear()")
        page.goto(f"{base}/s/{MID}/{SID}")
        # ⋯ › Preview: the item is there once the hub's hello said preview, a moment after the page loads
        for _ in range(20):
            page.get_by_role("button", name="更多").first.click()
            item = page.get_by_role("menuitem", name="预览")
            try:
                item.wait_for(timeout=1000)
            except PlaywrightTimeout:
                page.keyboard.press("Escape")
                page.wait_for_timeout(150)
                continue
            item.click()
            try:
                panel.wait_for(timeout=3000)
                break
            except PlaywrightTimeout:
                continue
        else:
            fail(f"{tag}: the ⋯ menu has no Preview")
        expect(panel).to_be_visible()

    def drawn():
        page.wait_for_function("(() => { const c = document.querySelector('[data-preview-canvas]'); return c && c.width > 1 && getComputedStyle(c).opacity !== '0'; })()", timeout=40000)

    # the running server opens at once
    open_panel("1")
    drawn()
    expect(panel).to_contain_text("web")
    expect(panel).to_contain_text("localhost:5173")
    expect(page.locator("[data-preview-address]")).to_have_value("/")
    ok(f"{tag}: the running server's page is shown")
    shot("live")

    # the address bar
    addr = page.locator("[data-preview-address]")
    addr.click()
    addr.fill("/settings")
    addr.press("Enter")
    expect(addr).to_have_value("/settings")
    page.wait_for_timeout(1500)
    snap = mock(hub, token)
    if not any(m.get("t") == "nav" and m.get("url") == "/settings" for m in snap["log"]):
        fail(f"{tag}: the address went to the node as {snap['log'][-3:]}")
    if not any(m.get("t") == "features" and m.get("select") and m.get("png") for m in snap["log"]):
        fail(f"{tag}: ready did not advertise features {{select: true, png: true}}")
    ok(f"{tag}: the address bar navigates and features advertised")
    shot("settings")

    # native <select> popup: real mouse pick, real keyboard pick (skipping disabled), and Esc dismiss
    cbox = canvas.bounding_box()
    last_w = next((m["w"] for m in reversed(snap["log"]) if m.get("t") == "size"), None) or next(m["open"]["w"] for m in reversed(snap["log"]) if "open" in m)
    last_h = next((m["h"] for m in reversed(snap["log"]) if m.get("t") == "size"), None) or next(m["open"]["h"] for m in reversed(snap["log"]) if "open" in m)
    sel_x = max(0.0, (last_w - 760.0) / 2.0) + 22.0 + 80.0
    sel_y = 154.0 + 19.0
    click_x = cbox["x"] + sel_x * (cbox["width"] / last_w)
    click_y = cbox["y"] + sel_y * (cbox["height"] / last_h)
    page.mouse.click(click_x, click_y)
    listbox = page.locator("[data-preview-select][role='listbox']")
    expect(listbox).to_be_visible()
    expect(listbox.locator("[data-preview-select-group]")).to_have_text(["Workspace", "Automation"])
    expect(listbox.get_by_role("option", name="Owner (managed)", exact=True)).to_have_attribute("aria-disabled", "true")
    expect(listbox.get_by_role("option", name="Editor", exact=True)).to_have_attribute("aria-selected", "true")
    shot("select-open")

    acks_before = mock(hub, token)["acks"]
    listbox.get_by_role("option", name="Viewer", exact=True).click()
    expect(listbox).to_have_count(0)
    page.wait_for_timeout(1200)
    after_mouse = mock(hub, token)
    if after_mouse["select"]["value"] != 1 or not any(m.get("t") == "select_choose" and m.get("index") == 1 for m in after_mouse["log"]):
        fail(f"{tag}: mouse select did not choose Viewer: {after_mouse['select']}")
    if after_mouse["acks"] <= acks_before:
        fail(f"{tag}: updated PNG frame was not acknowledged after mouse select")
    ok(f"{tag}: native <select> mouse pick updates value to Viewer and acks frame")

    # Keyboard reopen (Space while select keeps focus), ArrowDown skips disabled Owner -> Reviewer, Enter chooses
    page.keyboard.press("Space")
    expect(listbox).to_be_visible()
    expect(listbox.get_by_role("option", name="Viewer", exact=True)).to_have_attribute("aria-selected", "true")
    page.keyboard.press("ArrowDown")
    page.keyboard.press("Enter")
    expect(listbox).to_have_count(0)
    page.wait_for_timeout(1200)
    after_key = mock(hub, token)
    if after_key["select"]["value"] != 3 or not any(m.get("t") == "select_choose" and m.get("index") == 3 for m in after_key["log"]):
        fail(f"{tag}: keyboard select did not skip disabled and choose Reviewer: {after_key['select']}")
    ok(f"{tag}: native <select> keyboard pick skips disabled and updates value to Reviewer")

    # Esc closes popup, sends select_close, preserves value and keeps panel open
    n_esc = len(after_key["log"])
    page.keyboard.press("Space")
    expect(listbox).to_be_visible()
    page.keyboard.press("Escape")
    expect(listbox).to_have_count(0)
    expect(panel).to_be_visible()
    after_esc = mock(hub, token)
    if not any(m.get("t") == "select_close" for m in after_esc["log"][n_esc:]) or after_esc["select"]["value"] != 3:
        fail(f"{tag}: Esc did not dismiss select cleanly: {after_esc['log'][n_esc:]}")
    ok(f"{tag}: native <select> Esc dismisses cleanly without closing panel")

    if not narrow or full:
        # picking an element and commenting on it
        panel.get_by_role("button", name="选择页面元素进行评论").click()
        expect(page.locator("[data-preview-picking]")).to_be_visible()
        page.wait_for_timeout(1500)
        shot("picking")
        box = canvas.bounding_box()
        page.mouse.click(box["x"] + box["width"] * 0.8, box["y"] + box["height"] * 0.5)
        line = page.get_by_role("dialog", name="你的评论")
        expect(line).to_be_visible()
        expect(line).to_contain_text("button.save")
        line.locator("textarea").fill("把按钮改成主色，和别处一致")
        shot("comment-box")
        line.get_by_role("button", name="保存评论").click()
        chip = page.get_by_role("button", name="1 条评论")
        expect(chip).to_be_visible()
        chip.click()
        popover = page.locator("[data-comment-popover]")
        expect(popover).to_contain_text("button.save")
        expect(popover).to_contain_text("/settings")
        ok(f"{tag}: a picked element becomes a comment above the message box")
        shot("commented")
        page.keyboard.press("Escape")

        # a screenshot into the message box
        panel.get_by_role("button", name="截图").click()
        page.get_by_role("menuitem", name="可见区域").click()
        expect(page.get_by_text("截图已添加到消息中")).to_be_visible()
        ok(f"{tag}: a screenshot lands in the message box")
        shot("screenshot")

        # a link elsewhere
        addr.click()
        addr.fill("https://docs.example.com/notes/api")
        addr.press("Enter")
        bar = page.locator("[data-preview-bar=external]")
        expect(bar).to_be_visible()
        expect(bar.get_by_role("link", name="在新标签页打开")).to_have_attribute("href", "https://docs.example.com/notes/api")
        ok(f"{tag}: an address elsewhere is offered in a new tab, not opened in the preview")
        shot("external")
        bar.get_by_role("button", name="关闭").click()

        # phone size
        panel.get_by_role("button", name="桌面尺寸（切换到手机）").click()
        page.wait_for_timeout(2500)
        if not any(m.get("t") == "size" and m.get("mobile") for m in mock(hub, token)["log"]):
            fail(f"{tag}: phone size was not asked")
        ok(f"{tag}: phone size")
        shot("phone")

        # the servers menu
        panel.get_by_role("button", name="服务器").click()
        menu = page.get_by_role("menu")
        expect(menu).to_contain_text("api")
        expect(menu).to_contain_text("Storybook")
        ok(f"{tag}: the servers menu lists launch.json's entries and the agent's port")
        shot("servers")
        page.keyboard.press("Escape")

    # nothing running: the list, Start after confirming its command, then the page
    open_panel("stopped")
    row = page.locator("[data-preview-server=web]")
    expect(row).to_be_visible()
    expect(row).to_contain_text("npm run dev")
    expect(page.locator("[data-preview-problems]")).to_contain_text("staging")
    ok(f"{tag}: servers not running are listed with their commands, the broken entry says why")
    shot("stopped")
    row.get_by_role("button", name="启动…").click()
    dialog = page.get_by_role("dialog")
    expect(dialog.locator("[data-preview-command]")).to_have_text("npm run dev")
    shot("confirm")
    dialog.get_by_role("button", name="启动").click()
    expect(panel).to_contain_text("正在启动 web")
    shot("starting")
    drawn()
    start = [m["start"] for m in mock(hub, token)["log"] if "start" in m]
    if not start or start[-1].get("workspace") != "w1" or start[-1].get("name") != "web":
        fail(f"{tag}: start asked {start}")
    ok(f"{tag}: Start runs the confirmed entry in the session's workspace, and the page shows once it answers")

    for mode, text, name in (("none", "暂无开发服务器", "empty"), ("old", "更新这台电脑后才能预览", "old")):
        open_panel(mode)
        expect(panel).to_contain_text(text)
        ok(f"{tag}: {name}")
        shot(name)
    open_panel("nobrowser")
    expect(panel).to_contain_text("MacBook 上未找到可用于预览的浏览器")
    ok(f"{tag}: no browser on the computer")
    shot("nobrowser")
    mock(hub, token, "?mode=1")
    ctx.close()


def phone_touch(browser, base, hub, token, shots, w=390, h=844, theme="light"):
    """A phone with only a finger (#11511): scrolling the page, Pick and Go leave the panel's keys field alone, so the
    keyboard stays down and the page keeps its size; a tap clicks the page and focuses the field once the finger lifts.
    Only taps and CDP touches here: one mouse event would make the page treat this as a computer with a mouse."""
    tag = f"phone {w}/{theme}"
    mock(hub, token, "?mode=1&origin=0")
    ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=3, is_mobile=True, has_touch=True,
                              user_agent=UA, locale="zh-CN")
    ctx.add_init_script(IOS_KEYBOARD)
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}'); sessionStorage.clear()")
    page.goto(f"{base}/s/{MID}/{SID}")
    if page.evaluate("matchMedia('(any-pointer: fine)').matches"):
        fail(f"{tag}: the browser reports a fine pointer, so this proves nothing")
    for _ in range(20):
        page.get_by_role("button", name="更多").first.tap()
        item = page.get_by_role("menuitem", name="预览")
        try:
            item.wait_for(timeout=1000)
        except PlaywrightTimeout:
            page.keyboard.press("Escape")
            continue
        item.tap()
        break
    else:
        fail(f"{tag}: the ⋯ menu has no Preview")
    panel = page.locator("[data-preview-panel]")
    expect(panel).to_be_visible()
    page.wait_for_function("(() => { const c = document.querySelector('[data-preview-canvas]'); return c && c.width > 1 && getComputedStyle(c).opacity !== '0'; })()", timeout=40000)
    page.wait_for_timeout(800)
    cdp = ctx.new_cdp_session(page)

    def touch(kind, x=0, y=0):
        cdp.send("Input.dispatchTouchEvent", {"type": kind, "touchPoints": [] if kind == "touchEnd" else [{"x": x, "y": y}]})

    def focused():
        return page.evaluate("document.activeElement ? (document.activeElement.getAttribute('aria-label') || document.activeElement.tagName) : ''")

    def page_height():
        # the page's height as the node was last told: the socket's open, then each size
        hh = 0
        for m in mock(hub, token)["log"]:
            if "open" in m:
                hh = m["open"]["h"]
            elif m.get("t") == "size":
                hh = m["h"]
        return hh

    def kept(what, n0, least):
        """Neither the keys field nor the keyboard: no size under `least` went to the node."""
        page.wait_for_timeout(800)
        if focused() == KEYS:
            fail(f"{tag}: {what} focused the keys field")
        if page.evaluate("window.__raised"):
            fail(f"{tag}: {what} raised the keyboard")
        smaller = [m for m in mock(hub, token)["log"][n0:] if m.get("t") == "size" and m["h"] < least]
        if smaller:
            fail(f"{tag}: {what} told the node the page is smaller: {smaller}")

    def shot(name):
        if shots:
            page.screenshot(path=os.path.join(shots, f"preview-phone-{w}-{theme}-{name}.png"))

    h0 = page_height()
    n0 = len(mock(hub, token)["log"])
    box = page.locator("[data-preview-canvas]").bounding_box()
    x, y = box["x"] + box["width"] / 2, box["y"] + box["height"] * 0.7
    touch("touchStart", x, y)
    for i in range(1, 9):
        touch("touchMove", x, y - 25 * i)
        page.wait_for_timeout(16)
    touch("touchEnd")
    kept("a finger's scroll", n0, h0)
    if not any(m.get("t") == "wheel" for m in mock(hub, token)["log"][n0:]):
        fail(f"{tag}: the scroll did not reach the page")
    ok(f"{tag}: a finger's scroll scrolls the page; the keyboard stays down and the page keeps its height ({h0})")
    shot("scrolled")

    pick = panel.get_by_role("button", name="选择页面元素进行评论")
    pick.tap()
    banner = page.locator("[data-preview-picking]")
    expect(banner).to_be_visible()
    kept("Pick", n0, h0 - math.ceil(banner.bounding_box()["height"]))  # the picking line takes its own room
    ok(f"{tag}: Pick leaves the keyboard down")
    shot("picking")
    banner.get_by_role("button", name="取消").tap()
    expect(banner).to_have_count(0)

    # the address field raises its own keyboard; Go puts it away and the page is its height again
    addr = page.locator("[data-preview-address]")
    addr.tap()
    addr.fill("/settings")
    addr.press("Enter")
    page.wait_for_timeout(1200)
    if focused() == KEYS or page.evaluate("!!document.activeElement?.matches('[data-preview-address]')"):
        fail(f"{tag}: after Go the focus is on {focused()}")
    if page.evaluate("visualViewport.height") != h:
        fail(f"{tag}: the keyboard is still up after Go")
    if page_height() != h0:
        fail(f"{tag}: after Go the page is {page_height()} high, not {h0}")
    if not any(m.get("t") == "nav" and m.get("url") == "/settings" for m in mock(hub, token)["log"][n0:]):
        fail(f"{tag}: Go did not navigate")
    ok(f"{tag}: Go navigates and puts the keyboard away")
    shot("after-go")

    # a tap: nothing on the press, the click and the keys field once the finger lifts
    n1 = len(mock(hub, token)["log"])
    touch("touchStart", x, y)
    page.wait_for_timeout(200)
    if focused() == KEYS:
        fail(f"{tag}: a finger's press focused the keys field before it lifted")
    touch("touchEnd")
    page.wait_for_timeout(500)
    if focused() != KEYS:
        fail(f"{tag}: a tap left the focus on {focused()}, not the keys field")
    if not any(m.get("t") == "mouse" and m.get("type") == "down" for m in mock(hub, token)["log"][n1:]):
        fail(f"{tag}: the tap did not click the page")
    ok(f"{tag}: a tap clicks the page and focuses its keys field once the finger lifts")
    ctx.close()


def run_direct(browser, base, hub, token, size, theme, shots, full):
    """Isolated preview origin mode (docs/web/preview.md): direct iframe by default, native <select> inside iframe,
    same-port navigation & reload without replaying the one-time code, manual switch in the secondary menu, automatic
    fallback on verified unreachable postMessage / grant failure, auth_failed retry once, and Pick switching to stream."""
    tag = f"direct {size}/{theme}"
    narrow = size == "narrow"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=narrow, locale="zh-CN")
    ctx.grant_permissions(["clipboard-read", "clipboard-write"])
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    panel = page.locator("[data-preview-panel]")
    iframe = page.locator("iframe[data-preview-iframe]")
    canvas = page.locator("[data-preview-canvas]")

    def shot(name):
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=os.path.join(shots, f"preview-{size}-{theme}-{name}.png"))

    def open_direct(origin_mode):
        mock(hub, token, f"?mode=1&origin=1&origin_mode={origin_mode}&clear=1")
        page.evaluate("sessionStorage.clear()")
        page.goto(f"{base}/s/{MID}/{SID}")
        for _ in range(20):
            page.get_by_role("button", name="更多").first.click()
            item = page.get_by_role("menuitem", name="预览")
            try:
                item.wait_for(timeout=1000)
            except PlaywrightTimeout:
                page.keyboard.press("Escape")
                page.wait_for_timeout(150)
                continue
            item.click()
            try:
                panel.wait_for(timeout=3000)
                break
            except PlaywrightTimeout:
                continue
        else:
            fail(f"{tag}: the ⋯ menu has no Preview")
        expect(panel).to_be_visible()

    def drawn():
        page.wait_for_function("(() => { const c = document.querySelector('[data-preview-canvas]'); return c && c.width > 1 && getComputedStyle(c).opacity !== '0'; })()", timeout=40000)

    # 1. Direct view by default when hub advertises preview_origin
    open_direct("ok")
    expect(iframe).to_be_visible()
    expect(canvas).to_have_count(0)
    frame = page.frame_locator("iframe[data-preview-iframe]")
    expect(frame.locator("h1")).to_have_text("Good morning")

    # Verify cross-origin isolation & safe iframe attributes
    attrs = page.evaluate("""(() => {
      const f = document.querySelector('iframe[data-preview-iframe]');
      return { src: f.getAttribute('src') || '', sandbox: f.getAttribute('sandbox') || '', ref: f.getAttribute('referrerpolicy') || '', hubOrigin: location.origin };
    })()""")
    if not attrs["src"] or attrs["src"].startswith(attrs["hubOrigin"]):
        fail(f"{tag}: direct iframe was not hosted on a separate origin: {attrs}")
    sb = attrs["sandbox"].split()
    if "allow-scripts" not in sb or "allow-same-origin" not in sb or any(t.startswith("allow-top-navigation") for t in sb):
        fail(f"{tag}: unexpected iframe sandbox: {attrs['sandbox']}")
    if attrs["ref"] != "no-referrer":
        fail(f"{tag}: expected referrerpolicy=no-referrer, got {attrs['ref']}")
    if panel.get_by_role("button", name="后退").count() != 0 or panel.get_by_role("button", name="前进").count() != 0:
        fail(f"{tag}: Back/Forward buttons should be hidden in direct mode")
    ok(f"{tag}: direct iframe renders on isolated origin with safe sandbox and no-referrer")
    shot("direct")

    # 2. Address bar navigation, native <select> inside direct iframe, and reload without code replay
    addr = page.locator("[data-preview-address]")
    addr.click()
    addr.fill("/settings")
    addr.press("Enter")
    expect(frame.locator("h1")).to_have_text("Settings")
    frame.locator("#role").select_option("1")
    expect(frame.locator(".role-note b")).to_have_text("Viewer")
    snap = mock(hub, token)
    grants = [m for m in snap["log"] if "grant" in m]
    if len(grants) != 1 or any("open" in m for m in snap["log"]):
        fail(f"{tag}: expected 1 grant and 0 stream opens after same-port navigation, got {snap['log']}")
    panel.get_by_role("button", name="刷新").click()
    expect(frame.locator("h1")).to_have_text("Settings")
    expect(canvas).to_have_count(0)
    ok(f"{tag}: direct address navigation, native <select>, and reload work without replaying grant code")

    # 3. Manual mode switching in the preview bar secondary menu
    more = panel.get_by_role("button", name="更多预览选项")
    more.click()
    to_stream = page.get_by_role("menuitem", name="通过电脑查看")
    expect(to_stream).to_be_visible()
    if full and theme == "light":
        shot("direct-menu")
    to_stream.click()
    expect(iframe).to_have_count(0)
    drawn()
    expect(page.locator("[data-preview-bar='notice']")).to_have_count(0)
    expect(panel.get_by_role("button", name="后退")).to_be_visible()
    ok(f"{tag}: manual switch to computer stream works without fallback notice")

    more.click()
    to_direct = page.get_by_role("menuitem", name="直接查看")
    expect(to_direct).to_be_visible()
    to_direct.click()
    expect(iframe).to_be_visible()
    expect(frame.locator("h1")).to_have_text("Settings")
    expect(canvas).to_have_count(0)
    ok(f"{tag}: manual switch back to direct iframe works")

    if full and theme == "light":
        # Pick element in direct mode switches to stream with explanation and enters picking mode
        panel.get_by_role("button", name="选择页面元素进行评论").click()
        expect(page.locator("[data-preview-bar='notice']")).to_contain_text("已切换到通过电脑查看，以便选择页面元素。")
        expect(page.locator("[data-preview-picking]")).to_be_visible()
        drawn()
        ok(f"{tag}: Pick element in direct mode switches to stream with notice and activates picking")

        # auth_failed retries grant once and stays direct
        open_direct("auth_retry")
        expect(iframe).to_be_visible()
        expect(frame.locator("h1")).to_have_text("Good morning")
        expect(canvas).to_have_count(0)
        retry_grants = [m for m in mock(hub, token)["log"] if "grant" in m]
        if len(retry_grants) != 2:
            fail(f"{tag}: expected 2 grants on auth_failed retry, got {retry_grants}")
        ok(f"{tag}: auth_failed retries grant once and stays in direct mode")

        # Grant failure falls back automatically with notice
        open_direct("grant_fail")
        expect(iframe).to_have_count(0)
        drawn()
        expect(page.locator("[data-preview-bar='notice']")).to_contain_text("无法直接显示这个页面，已改为通过电脑显示。")
        ok(f"{tag}: grant failure automatically falls back to stream with notice")

    # 4. Verified unreachable postMessage automatically falls back to stream with notice
    open_direct("unreachable")
    expect(iframe).to_have_count(0)
    drawn()
    notice = page.locator("[data-preview-bar='notice']")
    expect(notice).to_be_visible()
    expect(notice).to_contain_text("无法直接显示这个页面，已改为通过电脑显示。")
    ok(f"{tag}: unreachable postMessage automatically falls back to stream with notice")
    shot("direct-fallback")

    mock(hub, token, "?mode=1&origin=0&origin_mode=ok")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    ap.add_argument("--channel", default="chrome", help="the installed browser Playwright drives (chrome, msedge)")
    ap.add_argument("--executable", help="a browser to drive instead of --channel")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=a.executable) if a.executable else p.chromium.launch(channel=a.channel)
        for size, theme in (("wide", "light"), ("wide", "dark"), ("narrow", "light"), ("narrow", "dark")):
            run(browser, a.base, a.hub, a.token, size, theme, a.shots, full=size == "wide")
        for size, theme in (("wide", "light"), ("wide", "dark"), ("narrow", "light"), ("narrow", "dark")):
            run_direct(browser, a.base, a.hub, a.token, size, theme, a.shots, full=size == "wide")
        phone_touch(browser, a.base, a.hub, a.token, a.shots)
        browser.close()
    print("PASS web preview", flush=True)


if __name__ == "__main__":
    main()
