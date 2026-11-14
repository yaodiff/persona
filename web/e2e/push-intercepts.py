#!/usr/bin/env python3
"""Chinese held-push UI before/after evidence, with synthetic browser-side routes only.

Build and preview first, then run:
  python3 web/e2e/push-intercepts.py http://127.0.0.1:51783 --phase after --shots /tmp/ln-after
Use --ln off to verify that the entry is absent. No live hub, real message, or write request is used.
"""
import argparse
import json
import re
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright

AT = "2026-11-14T04:00:00Z"
MAIN = "demo-main"
MACHINE = {"id": "demo", "name": "Computer A", "online": True, "os": "linux"}
SESSION = {"machine_id": "demo", "session_id": MAIN, "title": "Persona", "state": "idle", "status": "idle", "cwd": "/demo", "updated_at": AT, "started_by_persona": True, "capabilities": {"send": True, "interrupt": True}}
TODO = {"id": "demo-task", "title": "Demo task", "origin": MAIN, "created_at": AT, "updated_at": AT}
HELD = [
    {"at": "2026-11-14T03:30:00Z", "level": "P1", "reason": "ln_p1_held", "title": "发布结果等你核对", "body": "网页和手机的构建都完成了", "related_key": "demo-subject", "manager": "assistant", "also": ["请检查最后的发布说明"]},
    {"at": "2026-11-14T02:00:00Z", "level": "P1", "reason": "ln_dedup_12h", "title": "同一任务还有一次补充汇报"},
    {"at": "2026-11-14T01:00:00Z", "level": "P2", "reason": "ln_p2_silent", "title": "后台检查已完成", "body": "没有需要你处理的事项"},
    {"at": "2026-11-13T03:00:00Z", "level": "P1", "reason": "ln_p1_held", "title": "昨天的记录不应出现"},
    {"at": "invalid", "level": "P1", "reason": "ln_p1_held", "title": "坏时间不应出现"},
]


def run(args):
    shots = Path(args.shots)
    shots.mkdir(parents=True, exist_ok=True)
    origin = args.url.rstrip("/")
    results = {"phase": args.phase, "ln": args.ln, "shots": []}
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=args.chromium, args=["--no-sandbox"])
        try:
            for width in (1280, 390):
                for theme in ("light", "dark"):
                    writes = []
                    page_errors = []
                    context = browser.new_context(viewport={"width": width, "height": 900}, locale="zh-CN", color_scheme=theme,
                                                        reduced_motion="reduce", timezone_id="Asia/Shanghai")
                    context.add_init_script(f"localStorage.setItem('persona.token', 'demo'); localStorage.setItem('persona.language', 'zh-Hans'); localStorage.setItem('persona.theme', '{theme}');")
                    context.add_init_script(f"const RealDate = Date; globalThis.Date = class extends RealDate {{ constructor(...args) {{ super(...(args.length ? args : ['{AT}'])); }} static now() {{ return new RealDate('{AT}').getTime(); }} }};")

                    def api(route):
                        if route.request.method != "GET":
                            writes.append((route.request.method, route.request.url))
                        raw_path = urlparse(route.request.url).path
                        path = raw_path[len("/api/v1"):] if raw_path.startswith("/api/v1") else raw_path
                        data = {}
                        if path == "/machines":
                            data = {"machines": [MACHINE]}
                        elif path == "/sessions":
                            data = {"sessions": [SESSION]}
                        elif path == "/interactions":
                            data = {"interactions": []}
                        elif path == "/assistant":
                            data = {"machine_id": "demo", "main": MAIN, "conversations": [SESSION], "tasks": [], "memory": []}
                        elif path == f"/assistant/conversations/{MAIN}/feed":
                            data = {"items": [], "plans": {}, "task_cards": []}
                        elif path == "/todos":
                            data = {"todos": [TODO]}
                        elif path.endswith("/items"):
                            data = {"items": [{"id": "demo-answer", "seq": 1, "role": "assistant", "kind": "text", "text": "今天的推送记录都在这里。", "ts": AT}], "has_more": False}
                        elif path == "/push/ln-mode":
                            data = {"enabled": args.ln == "on"}
                        elif path == "/push/intercepts":
                            data = {"intercepts": HELD}
                        elif path in ("/extensions", "/managers"):
                            data = {"extensions": [], "managers": []}
                        route.fulfill(status=200, content_type="application/json", body=json.dumps(data))

                    def socket(ws):
                        ws.send(json.dumps({"type": "hello", "server_version": "demo", "hub_name": "Demo hub", "features": []}))
                        ws.on_message(lambda message: ws.send(json.dumps({"type": "pong"})) if "app_ping" in message else None)

                    context.route("**/api/v1/**", api)
                    context.route_web_socket("**/api/v1/ws**", socket)
                    page = context.new_page()
                    page.on("pageerror", lambda error: page_errors.append(str(error)))
                    page.set_default_timeout(15000)
                    page.goto(origin + "/")
                    bar = page.locator("[data-assistant-actionbar]")
                    expect(bar).to_be_visible()
                    expect(bar.locator("[data-tray-entry]")).to_have_count(2)
                    entry = page.locator("[data-held-pushes-entry]")
                    if args.phase == "after" and args.ln == "on":
                        expect(entry).to_be_visible(timeout=20000)
                        expect(entry).to_contain_text("被拦推送")
                        expect(entry).to_contain_text("3")
                    else:
                        expect(entry).to_have_count(0)

                    def capture(name):
                        expect(page.locator("vite-error-overlay")).to_have_count(0)
                        assert not page_errors, page_errors
                        page.evaluate("() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")
                        path = shots / f"{width}-{theme}-{args.phase}-{name}.png"
                        page.screenshot(path=str(path))
                        results["shots"].append(str(path))

                    capture("entry")
                    if args.phase == "after" and args.ln == "on":
                        entry.click()
                        dialog = page.get_by_role("dialog")
                        expect(dialog).to_be_visible()
                        expect(dialog).to_contain_text("发布结果等你核对")
                        expect(dialog).to_contain_text("assistant")
                        expect(dialog).to_contain_text("请检查最后的发布说明")
                        expect(dialog).to_contain_text("同一任务还有一次补充汇报")
                        expect(dialog).not_to_contain_text("后台检查已完成")
                        expect(dialog).not_to_contain_text("昨天的记录不应出现")
                        expect(dialog).not_to_contain_text("坏时间不应出现")
                        capture("p2-collapsed")
                        p2 = dialog.locator("button[aria-expanded='false']").filter(has_text=re.compile("P2"))
                        expect(p2).to_have_count(1)
                        p2.click()
                        expect(dialog).to_contain_text("后台检查已完成")
                        capture("p2-expanded")
                        page.keyboard.press("Escape")
                        expect(dialog).to_have_count(0)
                    assert all("/today" in url and method == "POST" for method, url in writes), writes
                    assert not any("push/" in url for _, url in writes), writes
                    print(f"PASS {width} {theme} {args.phase} ln={args.ln}: synthetic Chinese UI, no writes", flush=True)
                    context.close()
        finally:
            browser.close()
    (shots / "results.json").write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
    print("PASS held-push browser verification", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("--phase", choices=["before", "after"], required=True)
    parser.add_argument("--ln", choices=["on", "off"], default="on")
    parser.add_argument("--shots", required=True)
    parser.add_argument("--chromium", default="/usr/bin/google-chrome")
    run(parser.parse_args())
