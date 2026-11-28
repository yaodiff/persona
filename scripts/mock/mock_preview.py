"""MOCK_PREVIEW for dev server previews (docs/web/preview.md, #150): mock_hub.py loads it when MOCK_PREVIEW is set and hands it
its own module (`install`); everything here is made up.

What it adds:
  feature "preview"
  GET  /api/v1/machines/{mid}/preview/servers   a project's .claude/launch.json as the node reads it (by mode, below)
  POST /api/v1/machines/{mid}/preview/start     starts an entry: its port answers 3 s later (the panel waits for it)
  POST /api/v1/machines/{mid}/ports/{port}/open the preview's own ports answer, the others do not
  GET  …/ports/{port}/connect                   a running preview port forwards to a real page server with the same
                                                made-up app (`forward`), as the iPhone opens it
  GET  …/preview/connect                        a preview socket: pictures of a made-up dev app ("Acme Notes", pages
                                                /, /settings, /notes) that the browser this computer has draws at the
                                                size the panel asks, and the messages the node sends (page, external,
                                                picked, a screenshot, dialogs)
  GET  /mock/preview                            what the panels sent (opens, sizes, navigations, picks, shots)
  GET  /mock/preview?mode=…                     switches the mode on the running mock

MOCK_PREVIEW modes: 1 (web running, api stopped, docs attaching, an agent's Storybook port, one broken entry),
none (no launch.json, no ports), stopped (nothing running), nobrowser (the computer has no browser), old (a node from
before previews), slow (pictures take long: the panel's "Opening the page" state).
The pictures need a Chrome / Chromium / Edge (PERSONA_CHROME, else the one Playwright downloaded, else the usual
places); without one each picture is a plain grey PNG.
"""
import base64
import glob
import hashlib
import json
import os
import shutil
import struct
import subprocess
import tempfile
import threading
import time
import zlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODE = {"mode": os.environ.get("MOCK_PREVIEW") or "1"}
LOG = []
STARTED = {}  # port -> when it was started from the panel
SHOTS = {}  # (path, w, h, dpr, mobile, hl, sel) -> PNG bytes
SELECT_STATE = {"value": 0, "label": "Editor"}
ACKS = {"count": 0}

SELECT_OPTIONS = [
    {"index": 0, "label": "Editor", "group": "Workspace"},
    {"index": 1, "label": "Viewer", "group": "Workspace"},
    {"index": 2, "label": "Owner (managed)", "disabled": True, "group": "Workspace"},
    {"index": 3, "label": "Reviewer", "group": "Automation"},
    {"index": 4, "label": "Observer", "group": "Automation"},
]

PAGES = {
    "/": ("Acme Notes", "<h1>Good morning</h1><p class=muted>3 notes changed since yesterday.</p>"
          "<div class=grid><div class=card><b>Roadmap</b><p>Ship the editor toolbar</p></div>"
          "<div class=card><b>Groceries</b><p>Oat milk, lemons, rice</p></div>"
          "<div class=card><b>Trip</b><p>Train at 9:40, platform 3</p></div></div>"),
    "/settings": ("Settings · Acme Notes", "<h1>Settings</h1><form>"
                  "<label>Role<select id='role' class='role-select'>{select_options}</select></label>"
                  "<p class=role-note>Current role: <b>{role_label}</b></p>"
                  "<label>Display name<input value='Sam Lee'></label>"
                  "<label>Email<input value='sam@example.com'></label><label class=row><input type=checkbox checked> Weekly summary</label>"
                  "<div class=actions><button type=button class=ghost>Cancel</button><button type=button class='save {hl}'>Save changes</button></div></form>"),
    "/notes": ("Notes · Acme Notes", "<h1>Notes</h1><ul class=list><li>Roadmap<span>today</span></li><li>Groceries<span>yesterday</span></li>"
               "<li>Trip<span>Mon</span></li><li>Reading list<span>Oct 30</span></li></ul>"),
}
STYLE = """*{box-sizing:border-box}body{margin:0;font:15px/1.5 -apple-system,system-ui,sans-serif;background:#f6f7f9;color:#17181c}
nav{display:flex;align-items:center;gap:18px;height:54px;padding:0 22px;background:#fff;border-bottom:1px solid #e6e8ec}
nav b{font-size:16px;margin-right:auto}nav a{color:#5b606b;text-decoration:none}nav a.on{color:#2a62d6;font-weight:600}
main{max-width:760px;margin:0 auto;padding:24px 22px}h1{font-size:26px;line-height:32px;height:32px;margin:0 0 18px}.muted{color:#6b707b;margin:0 0 18px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:12px}.card{background:#fff;border:1px solid #e6e8ec;border-radius:12px;padding:14px}
.card p{margin:4px 0 0;color:#6b707b}form{display:flex;flex-direction:column;gap:14px;max-width:420px;margin-top:0}
label{display:flex;flex-direction:column;gap:6px;font-weight:600;font-size:13px;line-height:20px}label.row{flex-direction:row;align-items:center;font-weight:500}
input,select{font:inherit;padding:9px 11px;border:1px solid #d5d8de;border-radius:8px;background:#fff;color:#17181c}
select.role-select{height:38px;padding:0 11px}.role-note{margin:-6px 0 0;font-size:13px;color:#5b606b}
.actions{display:flex;gap:10px;justify-content:flex-end}
button{font:inherit;font-weight:600;padding:9px 16px;border-radius:8px;border:1px solid #d5d8de;background:#fff}
button.save{background:#2a62d6;border-color:#2a62d6;color:#fff}.hl{outline:3px solid rgba(57,135,229,.9);box-shadow:0 0 0 9999px rgba(57,135,229,.08)}
.list{list-style:none;padding:0;margin:18px 0 0;background:#fff;border:1px solid #e6e8ec;border-radius:12px}
.list li{display:flex;justify-content:space-between;padding:12px 14px;border-top:1px solid #eef0f3}.list li:first-child{border-top:0}.list span{color:#6b707b}"""


def select_rect(w):
    """Border box of #role on /settings in CSS pixels, matching STYLE above."""
    content_x = max(0.0, (w - 760.0) / 2.0) + 22.0
    field_w = min(420.0, max(120.0, w - 44.0))
    return {"x": round(content_x), "y": 154, "w": round(field_w), "h": 38}


def _select_html(selected):
    parts, cur_group = [], None
    for opt in SELECT_OPTIONS:
        g = opt.get("group")
        if g != cur_group:
            if cur_group is not None:
                parts.append("</optgroup>")
            if g is not None:
                parts.append(f"<optgroup label='{g}'>")
            cur_group = g
        sel = " selected" if opt["index"] == selected else ""
        dis = " disabled" if opt.get("disabled") else ""
        parts.append(f"<option value='{opt['index']}'{sel}{dis}>{opt['label']}</option>")
    if cur_group is not None:
        parts.append("</optgroup>")
    return "".join(parts)


def page_html(path, hl="", width=0, selected=0):
    title, body = PAGES.get(path, PAGES["/"])
    # Chrome's --screenshot lays out no narrower than 500 px whatever the window: a phone-wide page is pinned to its width
    pin = f"html{{width:{width}px;overflow:hidden}}" if width else ""
    links = "".join(f"<a href='{p}' class='{'on' if p == path else ''}'>{n}</a>" for p, n in (("/", "Home"), ("/notes", "Notes"), ("/settings", "Settings")))
    opt = next((o for o in SELECT_OPTIONS if o["index"] == selected), SELECT_OPTIONS[0])
    rendered = body.replace("{hl}", hl).replace("{select_options}", _select_html(selected)).replace("{role_label}", opt["label"])
    return (f"<!doctype html><html><head><meta charset=utf-8><meta name=viewport content='width=device-width,initial-scale=1'>"
            f"<title>{title}</title><style>{STYLE}{pin}</style></head><body><nav><b>Acme Notes</b>{links}</nav><main>{rendered}</main></body></html>")


class _Page(BaseHTTPRequestHandler):
    def do_GET(self):
        path, _, query = self.path.partition("?")
        args = dict(x.split("=", 1) for x in query.split("&") if "=" in x)
        b = page_html(path, "hl" if args.get("hl") == "1" else "", int(args.get("w", "0") or 0), int(args.get("sel", "0") or 0)).encode()
        self.send_response(200); self.send_header("Content-Type", "text/html; charset=utf-8"); self.send_header("Content-Length", str(len(b)))
        self.end_headers(); self.wfile.write(b)

    def log_message(self, *a): pass


_server = None


def page_server():
    global _server
    if _server is None:
        _server = ThreadingHTTPServer(("127.0.0.1", 0), _Page)
        threading.Thread(target=_server.serve_forever, daemon=True).start()
    return _server.server_address[1]


def browser():
    env = os.environ.get("PERSONA_CHROME")
    if env and os.access(env, os.X_OK): return env
    for pat in ("~/Library/Caches/ms-playwright/chromium-*/chrome-mac*/*.app/Contents/MacOS/*", "~/.cache/ms-playwright/chromium-*/chrome-linux*/chrome"):
        found = sorted(glob.glob(os.path.expanduser(pat)))
        if found: return found[-1]
    for p in ("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
              shutil.which("google-chrome") or "", shutil.which("chromium") or ""):
        if p and os.access(p, os.X_OK): return p
    return None


def grey_png(w, h):
    raw = b"".join(b"\0" + b"\xee\xef\xf2" * w for _ in range(h))
    chunk = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


def render(path, w, h, dpr, mobile, hl=False, sel=0):
    """A picture of the made-up app at a viewport, cached."""
    key = (path, w, h, dpr, mobile, hl, sel)
    if key in SHOTS: return SHOTS[key]
    exe = browser()
    png = None
    if exe:
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "shot.png")
            args = [exe, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run", "--use-mock-keychain",
                    f"--user-data-dir={d}/profile", f"--window-size={w},{h}", f"--force-device-scale-factor={dpr}", f"--screenshot={out}"]
            if mobile: args.append("--user-agent=Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36")
            url = f"http://127.0.0.1:{page_server()}{path}?hl={1 if hl else 0}&w={w if w < 500 else 0}&sel={sel}"
            try:
                subprocess.run(args + [url], timeout=30, capture_output=True)
                with open(out, "rb") as f: png = f.read()
            except (OSError, subprocess.SubprocessError):
                png = None
    SHOTS[key] = png or grey_png(max(1, int(w * dpr) // 4), max(1, int(h * dpr) // 4))
    return SHOTS[key]


def servers():
    m = MODE["mode"]
    if m == "none": return {"servers": [], "browser": "Google Chrome 131"}
    started = lambda port: port in STARTED and time.time() - STARTED[port] > 3
    running = m not in ("stopped",)
    return {
        "file": "/Users/me/src/acme-notes/.claude/launch.json",
        "browser": None if m == "nobrowser" else "Google Chrome 131",
        "servers": [
            {"name": "web", "port": 5173, "command": ["npm", "run", "dev"], "cwd": "/Users/me/src/acme-notes", "listening": running or started(5173), "title": "Acme Notes"},
            {"name": "api", "port": 8080, "command": ["go", "run", "./cmd/api"], "env": {"PORT": "8080"}, "cwd": "/Users/me/src/acme-notes/server", "listening": started(8080)},
            {"name": "docs", "port": 4000, "url": "http://localhost:4000/guide", "listening": False},
            {"name": "staging", "listening": False, "problem": "not_local"},
        ],
    }


def listening(port):
    s = servers()
    if any(x.get("port") == port and x["listening"] for x in s["servers"]): return True
    return MODE["mode"] == "1" and port == 6006


HUB = {}


def agent_ports():
    """The agent's Storybook port, in the mode that has one (the session lists it, as the node's scan would): the widget
    session's, the one the previews open from, so a MOCK_PORTS session first in the list keeps its own ports."""
    hub = HUB.get("hub")
    sid = getattr(hub, "WIDGET", None)
    s = next((x for x in getattr(hub, "SESSIONS", None) or [] if x.get("session_id") == sid), None)
    if s is not None:
        s["ports"] = [{"port": 6006, "title": "Storybook", "process": "node"}] if MODE["mode"] == "1" else []


def forward(port):
    """Where …/ports/{port}/connect reaches a preview's port on this computer (the Apple clients open the page through the
    port forwarding, docs/web/preview.md "iPhone"): the made-up app's page server while the port is running, else None."""
    return page_server() if MODE["mode"] != "old" and listening(port) else None


def install(hub):
    """Adds the preview to the mock: its feature, an agent's Storybook port, and the routes."""
    HUB["hub"] = hub
    agent_ports()
    return route


def route(h, method, p, q):
    if p == "/mock/preview":
        if q.get("mode"):
            MODE["mode"] = q["mode"][0]; STARTED.clear(); agent_ports()
            SELECT_STATE.update(value=0, label=SELECT_OPTIONS[0]["label"])
        return h.send_json({"mode": MODE["mode"], "log": LOG, "select": dict(SELECT_STATE), "acks": ACKS["count"]}) or True
    if not p.startswith("/api/v1/machines/"): return False
    parts = p.split("/")  # ['', 'api', 'v1', 'machines', mid, 'preview' | 'ports', …]
    if len(parts) < 6: return False
    if parts[5] == "ports" and method == "POST" and len(parts) == 8 and parts[7] == "open":
        port = int(parts[6]) if parts[6].isdigit() else 0
        if not listening(port): return h.send_json({"error": "nothing is listening on port %d" % port}, 404) or True
        return h.send_json({"port": port, "listening": True, "http": True}) or True
    if parts[5] != "preview": return False
    if MODE["mode"] == "old":
        return h.send_json({"error": "update Persona on this computer to preview its dev servers", "code": "node_outdated"}, 409) or True
    what = parts[6] if len(parts) > 6 else ""
    if what == "servers" and method == "GET":
        return h.send_json(servers()) or True
    if what == "start" and method == "POST":
        body = json.loads(h.last_body or b"{}")
        s = next((x for x in servers()["servers"] if x["name"] == body.get("name")), None)
        if not s or not s.get("command"): return h.send_json({"error": "no such entry"}, 409) or True
        STARTED[s["port"]] = time.time()
        LOG.append({"start": body})
        return h.send_json({"pane": "w1:p9", "terminal": "pane:w1:p9", "port": s["port"]}) or True
    if what == "connect" and method == "GET":
        connect(h, {k: v[0] for k, v in q.items()})
        return True
    return False


def connect(h, q):
    key = h.headers.get("Sec-WebSocket-Key", "")
    accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
    h.send_response(101, "Switching Protocols")
    h.send_header("Upgrade", "websocket"); h.send_header("Connection", "Upgrade")
    h.send_header("Sec-WebSocket-Accept", accept); h.end_headers(); h.wfile.flush()
    lock = threading.Lock()
    state = {"open": True, "seq": 0, "path": q.get("path") or "/", "w": int(float(q.get("w") or 800)), "h": int(float(q.get("h") or 600)),
             "dpr": float(q.get("dpr") or 1), "mobile": q.get("mobile") == "1", "picking": False, "history": [],
             "features": {}, "select_id": 0, "select_open_id": 0, "select_focused": False, "select_value": SELECT_STATE["value"]}
    port = int(q.get("port") or 0)
    LOG.append({"open": {"port": port, "path": state["path"], "w": state["w"], "h": state["h"], "mobile": state["mobile"]}})

    def frame(op, data):
        n = len(data)
        hdr = bytes([0x80 | op]) + (bytes([n]) if n < 126 else bytes([126]) + struct.pack(">H", n) if n < 65536 else bytes([127]) + struct.pack(">Q", n))
        with lock:
            if not state["open"]: return
            h.wfile.write(hdr + data); h.wfile.flush()

    def say(obj): frame(0x1, json.dumps(obj, ensure_ascii=False).encode())

    def url(): return f"http://localhost:{port}{state['path']}"

    def page(loading=False):
        title = PAGES.get(state["path"], PAGES["/"])[0]
        say({"t": "page", "url": url(), "title": title, "loading": loading, "back": len(state["history"]) > 0, "forward": False})

    def picture():
        w, hh = (390, 844) if state["mobile"] else (state["w"], state["h"])
        if MODE["mode"] == "slow": time.sleep(30)
        png = render(state["path"], w, hh, state["dpr"], state["mobile"], hl=state["picking"] and state["path"] == "/settings", sel=state["select_value"])
        state["seq"] += 1
        frame(0x2, b"f" + struct.pack(">IHH", state["seq"], w, hh) + png)

    def open_select():
        w = 390 if state["mobile"] else state["w"]
        state["select_id"] += 1
        state["select_open_id"] = state["select_id"]
        state["select_focused"] = True
        say({"t": "select_open", "id": state["select_id"], "rect": select_rect(w), "selected": state["select_value"], "options": SELECT_OPTIONS})

    def withdraw_select():
        if state["select_open_id"]:
            old = state["select_open_id"]
            state["select_open_id"] = 0
            say({"t": "select_close", "id": old})

    if MODE["mode"] == "nobrowser":
        say({"t": "error", "code": "no_browser", "error": "no Chrome, Chromium, Edge or Brave on this computer"})
        frame(0x8, struct.pack(">H", 1000))
        h.close_connection = True
        return
    if not listening(port):
        say({"t": "error", "code": "port_gone", "error": f"nothing is listening on port {port}"})
        frame(0x8, struct.pack(">H", 1000))
        h.close_connection = True
        return

    def go(path):
        withdraw_select()
        state["select_focused"] = False
        state["history"].append(state["path"])
        state["path"] = path if path in PAGES else "/"
        page(True)
        picture()
        page()

    say({"t": "ready", "browser": "Google Chrome 131", "w": state["w"], "h": state["h"]})
    threading.Thread(target=lambda: (page(True), picture(), page()), daemon=True).start()
    r = h.rfile
    try:
        while True:
            hd = r.read(2)
            if len(hd) < 2: break
            op = hd[0] & 0x0F; n = hd[1] & 0x7F
            if n == 126: n = struct.unpack(">H", r.read(2))[0]
            elif n == 127: n = struct.unpack(">Q", r.read(8))[0]
            mask = r.read(4) if hd[1] & 0x80 else b"\0\0\0\0"
            payload = bytes(b ^ mask[i % 4] for i, b in enumerate(r.read(n)))
            if op == 8: break
            if op == 9: frame(0xA, payload); continue
            if op != 1: continue
            m = json.loads(payload)
            t = m.get("t")
            if t == "ack":
                ACKS["count"] += 1
                continue
            if t == "mouse" and m.get("type") != "down": continue
            LOG.append(m)
            if t == "features":
                state["features"] = m
            elif t == "size":
                withdraw_select()
                state.update(w=int(m["w"]), h=int(m["h"]), dpr=float(m.get("dpr") or 1), mobile=bool(m.get("mobile")))
                threading.Thread(target=picture, daemon=True).start()
            elif t == "nav":
                u = m.get("url", "")
                if u.startswith("/"): threading.Thread(target=go, args=(u.split("?")[0],), daemon=True).start()
                else: say({"t": "external", "url": u})
            elif t == "back" and state["history"]:
                withdraw_select()
                state["select_focused"] = False
                state["path"] = state["history"].pop()
                page(); threading.Thread(target=picture, daemon=True).start()
            elif t == "reload":
                withdraw_select()
                page(True); threading.Thread(target=lambda: (picture(), page()), daemon=True).start()
            elif t == "pick":
                state["picking"] = bool(m.get("on"))
                threading.Thread(target=picture, daemon=True).start()
            elif t == "mouse" and state["picking"]:
                state["picking"] = False
                say({"t": "pick", "on": False})
                say({"t": "picked", "selector": "main > form > div.actions > button.save", "tag": "button", "text": "Save changes",
                     "html": "<button type=\"button\" class=\"save\">Save changes</button>", "role": "", "label": "",
                     "component": "SaveButton", "source": {"file": "/Users/me/src/acme-notes/src/settings/SaveButton.tsx", "line": 14},
                     "rect": {"x": 600, "y": 300, "w": 130, "h": 40}, "url": url(), "title": PAGES.get(state["path"], PAGES["/"])[0]})
                threading.Thread(target=picture, daemon=True).start()
            elif t == "mouse" and state["path"] == "/settings" and state["features"].get("select"):
                rect = select_rect(390 if state["mobile"] else state["w"])
                mx, my = m.get("x", -1), m.get("y", -1)
                if rect["x"] - 4 <= mx <= rect["x"] + rect["w"] + 4 and rect["y"] - 4 <= my <= rect["y"] + rect["h"] + 4:
                    open_select()
                else:
                    state["select_focused"] = False
            elif t == "key" and m.get("type") == "down" and state["select_focused"] and state["path"] == "/settings" and state["features"].get("select"):
                k = m.get("key", "")
                mods = m.get("mods", 0) or 0
                if k in (" ", "F4") or (k in ("ArrowDown", "ArrowUp") and (mods & 1)):
                    open_select()
            elif t == "select_choose":
                if m.get("id") == state["select_open_id"]:
                    opt = next((o for o in SELECT_OPTIONS if o["index"] == m.get("index") and not o.get("disabled")), None)
                    state["select_open_id"] = 0
                    state["select_focused"] = True
                    if opt is not None:
                        state["select_value"] = opt["index"]
                        SELECT_STATE.update(value=opt["index"], label=opt["label"])
                        threading.Thread(target=picture, daemon=True).start()
            elif t == "select_close":
                if m.get("id") == state["select_open_id"]:
                    state["select_open_id"] = 0
            elif t == "shot":
                png = render(state["path"], *((390, 844) if state["mobile"] else (state["w"], state["h"])), state["dpr"], state["mobile"], sel=state["select_value"])
                frame(0x2, b"s" + struct.pack(">I", int(m.get("id") or 0)) + png)
            elif t == "mock_external":  # what a click on a link elsewhere does
                say({"t": "external", "url": "https://docs.example.com/notes/api"})
    except OSError:
        pass
    finally:
        state["open"] = False
        h.close_connection = True
