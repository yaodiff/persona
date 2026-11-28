#!/usr/bin/env python3
"""The module e2e gate: switch modules off and on against a test hub and the real web app in headless Chrome (#14417).

A module move (#13430) merges only after this passes on its branch. From a checkout (default: this one) it builds
personad, persona-hub, persona-manager and persona-node (a default build) and the web app (`npm run build`), starts a
disposable postgres:18-alpine container on a private port and `persona-hub run` in a temporary home with a clean
environment, its own hub.json and token, and the web app served by that hub, the start-up of
scripts/ci/core-only-smoke.py. Nothing of this machine's own hub (~/.persona, PERSONA_HOME, its ports, its database)
is read or touched. Then, for every module id in --modules:

- on: the hello has `modules` and module.<id>; the manager's `modules: loaded …` line has a server module; its
  server-side parts answer (SERVER below); in Chrome, light and dark, its entry (WEB below) is there and opens;
- off: the `modules` settings row {"disabled": [...]} is written into the disposable database and the hub restarted:
  the flag is gone from the hello while the marker stays, the manager leaves the module out, its extension, connector
  and routes are absent (404), its entry points and new activity are gone (its entry, an address of its page, which
  answers like a removed page), and none of its chunks is requested. The exception is data the user already has
  (#13430), which stays listed and readable: a WEB entry with an `off` block is such data, and off it asserts the
  opposite of "gone" (below);
- back on: the row is deleted and the hub restarted: everything returns;
- what the user already has stays (#13430: a module that is off must not hide or change it), for a module with a
  `kept` row in SERVER. Recording: two recordings a hub with the module wrote (one with its transcript and minutes,
  one whose minutes were still to be written; scripts/ci/core-only-smoke.py seed_recordings) are in the knowledge base
  before the hub first starts. In every phase they are listed, opened and found by search through the knowledge
  base's own routes; off, the opened recording's detail equals the on phase's, the one in flight says
  `paused: module_off` in the response only, and every file under recordings/ is byte for byte what it was when the
  off phase began. In the web app (the `off` block of the recording row in WEB, #15530), off: the recording uploaded
  while on and a stored meeting with minutes are still listed under Knowledge › Meetings, each opens in the default
  panel's read-only detail (the transcript, and the minutes of the one that has them), none of the module's controls
  is there (rename, make or regenerate minutes, hand to an Agent, the private mark, the questions asked in the meeting), and none of
  the module's chunks is requested, the detail opened inside the app included;
- older hub: against the mock hub (scripts/mock/mock_hub.py) without the `modules` marker every module is on, the
  composer's voice button included; with the marker and without the module's flags they are off.

In every phase the core works: health ok, an authenticated request answers, the app loads, the main chat's composer
and the Agents page render, and the browser logs no page or console error. Prints one PASS / FAIL line per module and
check, writes the evidence (JSON) and the light and dark screenshots to local paths (never commit them), and ends with
one verdict line: PASS, FAIL or TIMEOUT; exits non-zero on anything but PASS.

    python3 scripts/ci/module-e2e.py --modules voice,health,recording [--checkout <dir>] [--shots <dir>]
        [--evidence <file>] [--no-build-web] [--no-mock] [--keep] [--timeout <seconds>]

Needs Go, Node with web/node_modules (npm ci), Chrome for playwright-core (the ms-playwright cache), Docker (DOCKER
names another client, e.g. a wrapper that runs `sg docker -c …`) and Python 3.

Adding a module: give it a row in SERVER (what the hub has of it) and in WEB (what the web app shows of it). A module
without a row is still checked for its hello flag and the manager's module list, and the run says it has no row.
"""
import argparse
import importlib.util
import json
import os
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HERE = Path(__file__).resolve()
ROOT = HERE.parents[2]
_spec = importlib.util.spec_from_file_location("core_only_smoke", HERE.parent / "core-only-smoke.py")
smoke = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(smoke)
Failed, TimedOut, http, run = smoke.Failed, smoke.TimedOut, smoke.http, smoke.run

# What the hub has of a module, checked against the test hub. Every path is fetched with the hub's token.
#   extensions: ids in /api/v1/extensions while on, absent off
#   connectors: ids in /api/v1/connectors?all=1 while on, absent off
#   routes: paths that answer (anything but 404) while on and 404 off; a [method, path, body] entry is sent so
#   (the hub registers every relayed route with its method, so another method answers 405 whether or not it is served)
#   features: flags of the hub's hello (/api/v1/health) the module's server half makes the hub announce, absent off
#   (the module's own module.<id> flag is always checked). The assistant tools a module adds reach the computers'
#   MCP only through a node, which this hub has none of: their absence is checked by the module's Go tests.
#   seed: requests made once while the module is first on, so the web app has something of it to show: [method, path, body]
#   kept: "recordings": the module's stored data stays listed, opened, found and unchanged while it is off (kept_checks)
# A client-only module (modapi.ClientModules) has an empty row: only its hello flag.
RECORDING_ID = "6f1c2a3b-0000-4000-8000-00000000e2e1"
SERVER = {
    "voice": {"extensions": ["voice"], "routes": ["/api/v1/extensions/voice/-/assistant-data"]},
    "volc": {"extensions": ["volc"], "routes": ["/api/v1/extensions/volc/-/assistant-data"]},
    "health": {
        "features": ["health_intake"],
        # The POSTs change nothing: the page's first draw, and a reminder action the manager refuses (400).
        "routes": ["/api/v1/assistant/health/intake", "/api/v1/assistant/health/intake/changes",
                   ["POST", "/api/v1/assistant/health/intake/ui", {}],
                   ["POST", "/api/v1/assistant/health/water/reminder", {}]],
    },
    "glasses": {},
    # Its server half (the history import, the sent marks) shows nothing over HTTP on a hub with no connector: its tools
    # reach the MCP through a node, and the selection page's chat list answers 404 for a source without history either
    # way. Its absence is TestKnowledgeUIFollowsItsModule's; here, its flag and the manager's line.
    "knowledge-ui": {},
    "recording": {
        "features": ["meeting_speakers"],
        "routes": ["/api/v1/recordings",
                   ["POST", f"/api/v1/recordings/{smoke.KEPT_SPEAKERS}/speakers", {"rev": 999, "ops": []}]],
        "kept": "recordings",
        # started an hour ago: an upload older than recording_days (14) is refused as expired
        "seed": [["PUT", f"/api/v1/recordings/{RECORDING_ID}", {
            "revision": 1, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 3600)), "duration": 60, "language": "en", "device": "phone",
            "title": "Module gate sample recording", "complete": True, "kind": "note",
            "segments": [{"start": 0, "end": 5, "text": "A made-up sample line for the module gate."}]}]],
    },
    "github": {"connectors": ["github"], "routes": ["/api/v1/connectors/github/auth"]},
    "imessage": {"extensions": ["imessage"]},
    # Lark: its parts are the bot's client (internal/botport), the lark-cli adapter (internal/cliport) and the assistant's
    # Feishu layer (internal/larkhost), none with a route or list entry of its own. The bot's settings
    # (/api/v1/bots/feishu) are the extension host's and answer in every build (binding and starting are what is refused
    # without the module), and the lark-cli connector exists only where lark.cli is configured, which the test hub is not.
    # So what the gate sees of it is its hello flag and the manager's module list. The web app gates nothing on it.
    "lark": {},
    "mail": {"routes": ["/api/v1/mail/accounts"]},
    # its sessions run on the computers (internal/modules/all/node): nothing on the manager but its flag
    "screen-share": {},
    # Briefing, Office and iPhone control have no HTTP surface of their own on a hub with no computer: the briefing's
    # setting (`assistant.briefing_waits`) is hidden from persona_settings while the module is off and omitted while it is
    # on until it is set, and its other parts are a scheduled job and the brief feed item; Office and iPhone control are
    # device commands on the computers (internal/modules/all/node, checked as node commands by core-only-smoke) and
    # Register adds nothing on the manager. So what the gate sees of each is its hello flag and the manager's module line,
    # as for lark and screen-share; an empty row is the honest one, not a missing one (#16243).
    "briefing": {},
    "office": {},
    "phone-extra": {},
}

# What the web app shows of a module (web/e2e/module-gate.mjs). An entry:
#   name; hubs: where it is checked ("test": the hub this gate starts, no model or computer; "mock": the mock hub, which
#   has conversations); page: the address it lives on; ready: what shows that page rendered, on or off; selector (CSS)
#   or text (a regular expression): the entry itself, visible while on, absent off; opens: what clicking it shows while
#   on (selector, text or url); off_page: an address of the module's own that answers like a removed page while off.
#   off: for an entry that is data the user already has, which a module that is off must not hide (#13430). It replaces
#   "absent off": kept is the items still listed on the page while off (name; selector or text; shows: what its opened
#   page must show, each with a name), hides is the module's controls none of those opened pages may show.
# chunks: regular expressions for the module's own files as the production build names them (web/dist/assets) and as
#   the dev server serves them; none may be requested while off. chunk_when_on: the hubs where one must load while on.
NOT_FOUND = r"no page|not found|doesn.t exist|isn.t here|no longer"
WEB = {
    "voice": {
        "entries": [
            {"name": "Settings › Extensions row", "hubs": ["test"], "page": "/settings?tab=extensions",
             "ready": {"selector": "[data-extension-row]"}, "selector": "[data-extension-row='voice']",
             "opens": {"url": r"voice"}},
            {"name": "composer voice button", "hubs": ["mock"], "page": "/", "ready": {"selector": "textarea"},
             "selector": "[data-testid='composer-voice']"},
        ],
        "chunks": [r"/assets/(slots|VoiceCall|VoiceBar|VoiceParts)-[\w-]+\.js", r"/src/modules/voice/"],
        "chunk_when_on": ["mock"],  # its composer slot mounts in a conversation, which only the mock hub has
    },
    "health": {
        "entries": [
            # Off, /health is not a removed page: #15704 (2026-11-26) makes it the default panel's read-only Health
            # archive (#13430), so existing conversations, reports and memory stay readable and none of the module's
            # write entries is on it. The same page a core-only build shows.
            {"name": "sidebar entry", "hubs": ["test", "mock"], "page": "/", "ready": {"selector": "a[href='/inbox']"},
             "selector": "a[href='/health']", "opens": {"url": r"/health"},
             "off_stays": {"path": "/health",
                           "shows": [{"name": "the read-only line", "selector": "[data-testid='health-readonly-note']"},
                                     {"name": "reports", "selector": "[data-testid='health-readonly-reports']"},
                                     {"name": "memory", "selector": "[data-testid='health-readonly-memory']"}],
                           "hides": [{"name": "a composer", "selector": "textarea"}]}},
        ],
        "chunks": [r"/assets/(Health|HealthReports|HealthIntakePage)-[\w-]+\.js", r"/src/modules/health/"],
        "chunk_when_on": ["test", "mock"],
    },
    "github": {
        "entries": [
            {"name": "Settings › Connectors row", "hubs": ["test"], "page": "/settings?tab=connectors",
             "ready": {"text": r"^Connectors$"}, "selector": "[data-connector-row='github']", "opens": {"url": r"github"}},
        ],
        "chunks": [],
    },
    "imessage": {
        "entries": [
            {"name": "Settings › Extensions row", "hubs": ["test"], "page": "/settings?tab=extensions",
             "ready": {"selector": "[data-extension-row]"}, "selector": "[data-extension-row='imessage']",
             "opens": {"url": r"imessage"}},
        ],
        "chunks": [],
    },
    "mail": {
        "entries": [
            {"name": "Settings › Connectors Mail group", "hubs": ["test"], "page": "/settings?tab=connectors",
             "ready": {"text": r"^Connectors$"}, "selector": "[data-settings-group='mail']"},
            # A mail the hub already delivered (the mock hub's inbox). On, it opens the whole message with its replies.
            # Off, it stays (#13430, #15995): the Inbox's own route still answers with the text the hub stored, so the
            # page shows that and one line saying mail is off, and none of the controls that need the mail routes
            # (StoredMailBody.tsx; the mock answers 404 on /api/v1/mail/ then, as the hub does).
            {"name": "Inbox mail item", "hubs": ["mock"], "page": "/inbox", "ready": {"selector": "[data-item]"},
             "selector": "[data-item='mail:acc1:t1:501']", "opens": {"selector": "button:has-text('Reply all')"},
             "off": {
                 "kept": [
                     {"name": "a mail the hub already delivered", "selector": "[data-item='mail:acc1:t1:501']",
                      "shows": [{"name": "its stored text", "selector": "[data-mail-stored-text]:has-text('Lucy')"},
                                {"name": "the line saying mail is off", "selector": "[data-mail-off-notice]"}]},
                 ],
                 "hides": [{"name": "reply", "selector": "button:has-text('Reply')"},
                           {"name": "AI draft reply", "selector": "button:has-text('AI draft reply')"},
                           {"name": "attachment", "selector": "a[download]"},
                           {"name": "error where the mail should be", "text": r"HTTP 404|not set up"}],
             }},
        ],
        "chunks": [],
    },
    "screen-share": {
        "entries": [
            # the machine page's View screen (MOCK_COMPUTER=ready: a Mac with Screen Sharing on); the live screen itself
            # is a page of its own (standalonePages), which an off hub answers as a removed page
            {"name": "machine page View screen", "hubs": ["mock"], "page": "/machines/macbook",
             "ready": {"text": r"^MacBook$"}, "selector": "a[href$='/screen/macbook']",
             "off_page": {"path": "/screen/macbook", "text": NOT_FOUND}},
        ],
        "chunks": [r"/assets/(standalone|ScreenView|ScreenMenu|DisplayMenu|StatsOverlay|VideoPicture)-[\w-]+\.js",
                   r"/src/modules/screen-share/"],
        "chunk_when_on": [],  # the button is a link that opens a new tab: nothing of it loads on the machine page
    },
    "lark": {
        "entries": [
            # the "Show messages on home" row in the Lark section (a hub with feature `lark`: the mock hub); the Messages
            # page's own address answers like a removed page while off
            {"name": "Settings › Connectors › Lark row", "hubs": ["mock"], "page": "/settings?tab=connectors",
             "ready": {"selector": "main"}, "text": r"^Show messages on home$",
             "off_page": {"path": "/messages", "text": NOT_FOUND}},
        ],
        "chunks": [r"/assets/(lark-code|Messages|LarkHomeSwitch|lark-chats)-[\w-]+\.js", r"/src/modules/lark/"],
        "chunk_when_on": ["mock"],  # the row is the module's code, loaded when the section draws
    },
    "recording": {
        "entries": [
            {"name": "Knowledge › Meetings recording", "hubs": ["test"], "page": "/kb/meetings",
             "ready": {"text": r"^Meetings$"}, "text": r"Module gate sample recording",
             "opens": {"selector": "main :text('A made-up sample line')"},
             # Off, all stored recordings stay (#13430, #15530, #15214): the default panel's read-only detail
             # (RecordingReadOnlyDetail.tsx) shows what is stored and no control of the module's. The seeded meetings
             # are core-only-smoke's KEPT_RECORDING ("Kept recording", with minutes and a photo) and KEPT_PENDING
             # ("Kept recording, minutes pending", paused with module_off). The controls are the module's English labels
             # (web/src/i18n/locales/en: recording.rename, recording.minutes_make, recording.minutes_regenerate,
             # recording-handoff.title, recording.private_label, recording.asks_title).
             "off": {
                 "kept": [
                     # uploaded in the `on` phase, so it exists only when there is one: core-only has no `on` phase.
                     {"name": "the recording uploaded while on", "text": r"Module gate sample recording", "needs_on": True,
                      "shows": [{"name": "its transcript", "selector": "main :text('A made-up sample line')"}]},
                     {"name": "a stored meeting with minutes", "text": r"^Kept recording$",
                      "shows": [{"name": "its minutes", "selector": "main :text('wombat checklist')"},
                                {"name": "its transcript", "selector": "main :text('quokka transcript')"},
                                {"name": "its photo caption", "selector": "main :text('Plan')"}]},
                     {"name": "a stored meeting paused while off", "text": r"^Kept recording, minutes pending$",
                      "shows": [{"name": "its transcript", "selector": "main :text('Its minutes were never written')"},
                                {"name": "the paused note", "selector": "main [data-recording-paused='module_off']"}]},
                 ],
                 "hides": [{"name": "rename", "selector": "main button:has-text('Rename')"},
                           {"name": "make minutes", "selector": "main button:has-text('Make minutes')"},
                           {"name": "regenerate", "selector": "main button:has-text('Regenerate')"},
                           {"name": "hand to an Agent", "selector": "main button:has-text('Hand to Agent')"},
                           {"name": "private mark", "selector": "main :text('Private meeting')"},
                           {"name": "questions asked in the meeting", "selector": "main :text('Questions in the meeting')"},
                           {"name": "error retry", "selector": "main button:has-text('Retry')"}],
             }},
        ],
        "chunks": [r"/assets/RecordingDetail-[\w-]+\.js", r"/src/modules/recording/"],
        "chunk_when_on": ["test"],
    },
    # briefing, phone-extra, volc and knowledge-ui have no WEB row on purpose: the web app gates nothing on their flags.
    # briefing and knowledge-ui change what the hub returns (a scheduled job, the knowledge history import); phone-extra
    # runs on the computers; volc is a hub-side speech extension. The gate prints a NOTE for each (a missing WEB row is
    # not a failure). A browser assertion would have nothing of the module to look at (#16243).
    #
    # office has a web module (`web/src/modules/office`, a FilePreview the composer opens from a file, #16289) but no
    # address of its own: its chunk loads only when a session has an Office file, which this hub has none of. So it gets
    # a chunks-only row (no entry): while off, none of its chunks may be requested, which is checked like any module's.
    "office": {
        "entries": [],
        "chunks": [r"/assets/(OfficePreview|docx-preview|workbook\.worker)-[\w-]+\.js", r"/src/modules/office/"],
        "chunk_when_on": [],  # nothing of it loads without an Office file, so it is not required while on
    },
}
# Console lines that are not the app's errors. The test hub has no computer, so the assistant's memory (kept on the
# assistant's computer) answers 409; the mock hub answers 404 for what it does not script. Chrome cancels loads in
# flight with ERR_NETWORK_CHANGED when the machine's interfaces change (another Docker container starting or stopping);
# the page checks around it still have to pass.
NETWORK_CHANGED = r"net::ERR_NETWORK_CHANGED"
IGNORE_CONSOLE = {"test": [r"status of 409 .*/api/v1/assistant/memory", NETWORK_CHANGED],
                  "mock": [r"status of 404", r"status of 501", NETWORK_CHANGED]}

PROGRAMS = smoke.PROGRAMS
DEADLINE = [0.0]
# Whether a chunk an off module's page fetches before that page's hello fails the run (--strict-before-hello) or is a
# WARN: the web draws every module as on until the hello comes (docs/web/web.md "When a module is there").
STRICT_BEFORE_HELLO = [False]


def remaining(limit):
    left = DEADLINE[0] - time.monotonic()
    if left <= 0:
        raise TimedOut("the gate's --timeout")
    return min(limit, left)


class Report:
    def __init__(self):
        self.lines, self.failed, self.warned = [], 0, 0

    def add(self, phase, module, check, ok, detail="", scheme="", warn=False):
        line = {"phase": phase, "module": module, "check": check, "scheme": scheme, "ok": bool(ok), "detail": detail}
        if warn and not ok:
            line["warn"] = True
        self.lines.append(line)
        word = "PASS" if ok else "WARN" if warn else "FAIL"
        if word == "FAIL":
            self.failed += 1
        elif word == "WARN":
            self.warned += 1
        where = f" [{scheme}]" if scheme else ""
        print(f"{word} {phase} {module}: {check}{where}" + (f" ({detail})" if detail and not ok else ""), flush=True)


class Hub:
    """persona-hub run in the temporary home; restarted between phases with the same home, port and database."""

    def __init__(self, bindir, work, home, port, token):
        self.bindir, self.work, self.home, self.port, self.token = bindir, work, home, port, token
        self.base = f"http://127.0.0.1:{port}"
        self.proc, self.starts = None, 0

    def start(self):
        self.starts += 1
        self.log = self.work / f"hub-{self.starts}.log"
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(self.home), "TMPDIR": str(self.work),
               "LANG": "C.UTF-8", "PERSONA_LOCAL_NODE": "0"}
        with open(self.log, "w") as out:
            self.proc = subprocess.Popen([str(self.bindir / "persona-hub"), "run"], cwd=self.work, env=env, stdout=out,
                                         stderr=subprocess.STDOUT, start_new_session=True)

        def healthy():
            smoke.check(self.proc.poll() is None, f"the hub exited ({self.proc.returncode}) before answering")
            status, body = http(self.base + "/api/v1/health")
            return status == 200 and json.loads(body).get("ok") is True

        smoke.wait_for("hub health", remaining(90), healthy)
        line = re.compile(r"modules: loaded (.+)$", re.M)
        smoke.wait_for("the manager's module list", remaining(120), lambda: line.search(self.log.read_text()) or self.proc.poll() is not None)
        smoke.check(self.proc.poll() is None, f"the hub exited ({self.proc.returncode})")
        loaded = line.search(self.log.read_text()).group(1).strip()
        # the manager's flags reach the hub's hello once it is connected
        smoke.wait_for("the manager's flags in the hello", remaining(60), lambda: "modules" in self.features())
        return loaded

    def features(self):
        status, body = http(self.base + "/api/v1/health")
        return json.loads(body).get("features", []) if status == 200 else []

    def stop(self):
        if self.proc is None or self.proc.poll() is not None:
            return
        os.killpg(self.proc.pid, signal.SIGTERM)
        try:
            code = self.proc.wait(remaining(60))
        except subprocess.TimeoutExpired:
            os.killpg(self.proc.pid, signal.SIGKILL)
            self.proc.wait(10)
            raise TimedOut("hub shutdown")
        smoke.check(code == 0, f"the hub exited {code} on SIGTERM")

    def request(self, method, path, body=None):
        import urllib.request, urllib.error
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Authorization": f"Bearer {self.token}", "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()


def psql(container, sql):
    r = run([smoke.DOCKER, "exec", container, "psql", "-v", "ON_ERROR_STOP=1", "-U", "persona", "-d", "persona_manager",
             "-Atc", sql], remaining(30))
    if r.returncode != 0:
        raise Failed("psql: " + r.stderr.strip())
    return r.stdout.strip()


def build_web(checkout, npm_ci):
    web = checkout / "web"
    if npm_ci or not (web / "node_modules").is_dir():
        r = run(["npm", "ci", "--no-audit", "--no-fund"], remaining(900), cwd=web)
        if r.returncode != 0:
            raise Failed("npm ci: " + (r.stderr or r.stdout).strip()[-2000:])
    r = run(["npm", "run", "build"], remaining(900), cwd=web)
    if r.returncode != 0:
        raise Failed("web build: " + (r.stderr or r.stdout).strip()[-2000:])
    return web / "dist"


def build_go(checkout, out, tags=""):
    env = dict(os.environ, GOFLAGS=(os.environ.get("GOFLAGS", "") + " -trimpath").strip())
    args = ["go", "build"] + ([f"-tags={tags}"] if tags else []) + ["-o", str(out) + "/"] + [f"./cmd/{p}" for p in PROGRAMS]
    r = run(args, remaining(900), cwd=checkout / "server", env=env)
    if r.returncode != 0:
        raise Failed("build: " + (r.stderr or r.stdout).strip()[-2000:])


def core_only_phase(rep, ids, dist, shots, work, schemes):
    """A browser against a hub built with `-tags persona_core_only` (#16243): the client against the *core-only binary*,
    not a default build with modules switched off. The hello still carries the `modules` marker and no built-in
    `module.<id>` flag (a client-only flag such as module.glasses may stay), so every module is off by the clients' own
    rule (docs/architecture/core-and-modules.md "Hub flags"), and it is checked as such: the core's pages render with no
    page or console error, and no module's entry, address or chunk is there."""
    phase = "core-only"
    container = "persona-module-e2e-core-" + secrets.token_hex(4)
    db = smoke.postgres(container)
    home = work / "core-home"
    (home / ".persona").mkdir(parents=True)
    port, token = smoke.free_port(), secrets.token_hex(24)
    cfg = home / ".persona/hub.json"
    cfg.write_text(json.dumps({"listen": f"127.0.0.1:{port}", "token": token, "public_url": f"http://127.0.0.1:{port}",
                               "name": "Core-only gate hub", "database_url": db, "machines": []}))
    cfg.chmod(0o600)
    shutil.copytree(dist, home / ".persona/web")  # newer than the build's embedded copy, so the hub serves it
    smoke.seed_recordings(home)  # what the user already has, before the hub first starts
    bindir = work / "core-bin"
    bindir.mkdir()
    build_go(ROOT, bindir, tags="persona_core_only")
    hub = Hub(bindir, work, home, port, token)
    hub.checkout = ROOT
    try:
        loaded = hub.start()
        on = {id: False for id in ids}
        server_checks(rep, phase, hub, ids, on, loaded)
        rep.add(phase, "core", "a core-only manager loaded no module", loaded in ("none", ""), f"modules: loaded {loaded}")
        flags = [f for f in hub.features() if f == "modules" or f.startswith("module.")]
        rep.add(phase, "core", "the hello has the modules marker", "modules" in flags, ",".join(flags))
        stray = sorted(f for f in flags if f.startswith("module.") and f.removeprefix("module.") in on)
        rep.add(phase, "core", "the hello announces no built-in module flag", not stray, ",".join(stray))
        rows, _detail = smoke.kept_recordings(hub.base, hub.token, False, home)
        for what, ok, text in rows:
            rep.add(phase, "recording", what, ok, text)
        seen = browser(rep, phase, hub.base, token, "test", ids, on, shots, work, schemes, phases=(phase,))
    finally:
        if hub.proc is not None and hub.proc.poll() is None:
            try:
                hub.stop()
            except TimedOut:
                pass
        subprocess.run([smoke.DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)
    return {"manager_line": f"modules: loaded {loaded}", "hello_module_flags": flags, "browser_hello": seen["hello"],
            "requested": seen["requested"]}


def server_checks(rep, phase, hub, ids, on, loaded):
    features = hub.features()
    rep.add(phase, "core", "hub health ok", True)
    status, _ = hub.request("GET", "/api/v1/managers")
    rep.add(phase, "core", "an authenticated request answers (/api/v1/managers)", status == 200, f"status {status}")
    rep.add(phase, "core", "the hello has the modules marker", "modules" in features)
    status, body = hub.request("GET", "/api/v1/extensions")
    extensions = {e["id"] for e in json.loads(body).get("extensions", [])} if status == 200 else set()
    status, body = hub.request("GET", "/api/v1/connectors?all=1")
    connectors = {c["id"] for c in (json.loads(body).get("connectors") or [])} if status == 200 else set()
    for id in ids:
        want = on[id]
        rep.add(phase, id, f"hello {'has' if want else 'lacks'} module.{id}", (f"module.{id}" in features) == want)
        if (hub.checkout / "server/internal/modules" / id / "module.json").exists():
            rep.add(phase, id, f"the manager {'loads' if want else 'leaves out'} the module (modules: loaded {loaded})",
                    (id in [m.strip() for m in loaded.split(",")]) == want)
        row = SERVER.get(id)
        if row is None:
            rep.add(phase, id, "has a SERVER row in scripts/ci/module-e2e.py", False,
                    "add one: what the hub has of the module (`{}` when it is only its flag and the manager line)")
            continue
        for ext in row.get("extensions", []):
            rep.add(phase, id, f"extension {ext} {'listed' if want else 'absent'}", (ext in extensions) == want, sorted(extensions))
        for con in row.get("connectors", []):
            rep.add(phase, id, f"connector {con} {'registered' if want else 'absent'}", (con in connectors) == want, sorted(connectors))
        for flag in row.get("features", []):
            rep.add(phase, id, f"hello {'has' if want else 'lacks'} {flag}", (flag in features) == want)
        for route in row.get("routes", []):
            method, path, body = route if isinstance(route, list) else ("GET", route, None)
            status, _ = hub.request(method, path, body)
            rep.add(phase, id, f"{method} {path} {'answers' if want else 'is 404'}", (status != 404) == want, f"status {status}")


def kept_checks(rep, phase, hub, ids, on, kept, home):
    """What a module's users already have stays listed, opened and found in every phase; off, it is unchanged."""
    if "recording" not in ids or SERVER["recording"].get("kept") != "recordings":
        return
    want = on["recording"]
    deadline = time.monotonic() + remaining(90)  # the knowledge base opens with the manager's first use of it
    while True:
        rows, detail = smoke.kept_recordings(hub.base, hub.token, want, home)
        if all(ok for _, ok, _ in rows) or time.monotonic() > deadline:
            break
        time.sleep(2)
    for what, ok, text in rows:
        rep.add(phase, "recording", what, ok, text)
    if phase == "on":
        kept["detail"] = detail
    else:
        rep.add(phase, "recording", "the stored recording's detail equals the on phase's", detail == kept.get("detail"),
                json.dumps(detail)[:300])


def browser(rep, phase, base, token, hub_kind, ids, on, shots, work, schemes, phases=("on", "off", "back-on")):
    """phases: which phases this run has, so a kept item only the `on` phase makes (needs_on) is skipped in a run
    without one (the core-only gate has no `on` phase)."""
    modules = {id: WEB[id] for id in ids if id in WEB}
    for id in ids:
        if id not in WEB and phase == "on":
            print(f"NOTE {id}: no WEB row in scripts/ci/module-e2e.py, so no browser checks for it", flush=True)
    spec = {"base": base, "token": token, "phase": phase, "hub": hub_kind, "expect": {id: on[id] for id in modules},
            "modules": modules, "shots": str(shots), "out": str(work / f"browser-{phase}.json"), "schemes": schemes,
            "ignore_console": IGNORE_CONSOLE[hub_kind], "strict_before_hello": STRICT_BEFORE_HELLO[0],
            "mask_text": socket.gethostname(), "phases": list(phases)}
    path = work / f"spec-{phase}.json"
    path.write_text(json.dumps(spec))
    r = run(["node", str(ROOT / "web/e2e/module-gate.mjs"), str(path)], remaining(600), cwd=ROOT / "web")
    if not Path(spec["out"]).exists():
        raise Failed(f"the browser run of {phase} wrote nothing: " + (r.stderr or r.stdout).strip()[-2000:])
    seen = json.loads(Path(spec["out"]).read_text())
    for c in seen["checks"]:
        rep.add(phase, c["module"], c["check"], c["ok"], c["detail"], c["scheme"], c.get("warn", False))
    if r.returncode != 0:
        rep.add(phase, "core", "the browser run finished", False, (r.stderr or r.stdout).strip()[-600:])
    return seen


def mock_phase(rep, phase, ids, flags, dist, shots, work, schemes, procs):
    """The web app (vite preview of the same build) against the mock hub, as an older hub or with module flags."""
    hub_port, web_port = smoke.free_port(), smoke.free_port()
    # MOCK_COMPUTER: a Mac with Screen Sharing on, for the screen-share module's View screen
    env = dict(os.environ, PORT=str(hub_port), MOCK_VOICE="1", MOCK_COMPUTER="ready")
    env.pop("MOCK_HUB_MODULES", None)
    if flags is not None:
        env["MOCK_HUB_MODULES"] = ",".join(flags) or "none"
    log = open(work / f"mock-{phase}.log", "w")
    mock = subprocess.Popen([sys.executable, str(ROOT / "scripts/mock/mock_hub.py")], cwd=work, env=env, stdout=log,
                            stderr=subprocess.STDOUT, start_new_session=True)
    procs.append(mock)
    smoke.wait_for("the mock hub", remaining(60), lambda: http(f"http://127.0.0.1:{hub_port}/api/v1/health")[0] == 200)
    wenv = dict(os.environ, PERSONA_HUB_URL=f"http://127.0.0.1:{hub_port}")
    preview = subprocess.Popen(["npx", "vite", "preview", "--outDir", str(dist), "--host", "127.0.0.1", "--port", str(web_port),
                                "--strictPort"], cwd=ROOT / "web", env=wenv, stdout=log, stderr=subprocess.STDOUT,
                               start_new_session=True)
    procs.append(preview)
    smoke.wait_for("vite preview", remaining(60), lambda: http(f"http://127.0.0.1:{web_port}/")[0] == 200)
    on = {id: flags is None for id in ids}
    try:
        browser(rep, phase, f"http://127.0.0.1:{web_port}", "mock", "mock", ids, on, shots, work, schemes)
    finally:
        for p in (preview, mock):
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGTERM)
                try:
                    p.wait(15)
                except subprocess.TimeoutExpired:
                    os.killpg(p.pid, signal.SIGKILL)
                    p.wait(5)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--modules", help="comma-separated module ids, e.g. voice,health,recording (not needed with --core-only)")
    ap.add_argument("--checkout", type=Path, default=ROOT, help="the checkout to build (default: this one)")
    ap.add_argument("--shots", type=Path, default=Path.home() / "persona-pr-shots/module-e2e", help="screenshots go here")
    ap.add_argument("--evidence", type=Path, help="the JSON evidence file (default: <shots>/evidence.json)")
    ap.add_argument("--no-build-web", action="store_true", help="serve the checkout's existing web/dist")
    ap.add_argument("--npm-ci", action="store_true", help="run npm ci before building the web app")
    ap.add_argument("--no-mock", action="store_true", help="leave out the mock hub phases (older hub, flags off)")
    ap.add_argument("--core-only", action="store_true",
                    help="only the core-only browser phase: build with -tags persona_core_only and check that every "
                         "module is off against the core-only binary (no on/off/back-on, no mock phases)")

    ap.add_argument("--schemes", default="light,dark")
    ap.add_argument("--strict-before-hello", action="store_true",
                    help="fail on an off module's chunk fetched before the page's hello (a WARN otherwise)")
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory (logs, home)")
    ap.add_argument("--timeout", type=int, default=2400, help="the whole run, in seconds")
    a = ap.parse_args()
    DEADLINE[0] = time.monotonic() + a.timeout
    STRICT_BEFORE_HELLO[0] = a.strict_before_hello
    if a.core_only:
        # The core-only phase checks every built-in module is off; --modules may narrow nothing, so it defaults to all.
        ids = [m.strip() for m in (a.modules or ",".join(smoke.module_ids())).split(",") if m.strip()]
    else:
        if not a.modules:
            ap.error("--modules is required without --core-only")
        ids = [m.strip() for m in a.modules.split(",") if m.strip()]
    checkout = a.checkout.resolve()
    shots = a.shots.expanduser().resolve()
    shots.mkdir(parents=True, exist_ok=True)
    evidence = (a.evidence or shots / "evidence.json").expanduser().resolve()
    schemes = [s for s in a.schemes.split(",") if s]
    work = Path(tempfile.mkdtemp(prefix="persona-module-e2e."))
    bindir, home = work / "bin", work / "home"
    container = "persona-module-e2e-" + secrets.token_hex(4)
    rep, hub, procs, verdict = Report(), None, [], "FAIL"
    commit = run(["git", "-C", str(checkout), "rev-parse", "HEAD"], 10).stdout.strip()
    dirty = bool(run(["git", "-C", str(checkout), "status", "--porcelain", "--untracked-files=no"], 10).stdout.strip())
    out = {"commit": commit + ("-dirty" if dirty else ""), "checkout": str(checkout), "modules": ids, "phases": {}}
    started = time.monotonic()
    if a.core_only:
        # Only the core-only browser phase (#16243): a core-only-built hub against the real web app. Its own Postgres
        # container is made and removed inside core_only_phase.
        try:
            dist = checkout / "web/dist" if a.no_build_web else build_web(checkout, a.npm_ci)
            smoke.check((dist / "index.html").exists(), f"no web build at {dist}")
            print(f"the web app from {checkout} at {out['commit'][:12]} in {time.monotonic() - started:.0f}s", flush=True)
            out["phases"]["core-only"] = core_only_phase(rep, ids, dist, shots, work, schemes)
        except (Failed, TimedOut) as e:
            verdict = "TIMEOUT" if isinstance(e, TimedOut) else "FAIL"
            summary = f"{verdict} core-only browser gate for {', '.join(ids)} at {out['commit'][:12]}: {e}"
        else:
            verdict = "PASS" if rep.failed == 0 else "FAIL"
            summary = f"{verdict} core-only browser gate for {', '.join(ids)} at {out['commit'][:12]}: " \
                      f"{len(rep.lines) - rep.failed - rep.warned} checks passed, {rep.failed} failed, {rep.warned} warnings " \
                      f"({time.monotonic() - started:.0f}s)"
        out.update(verdict=verdict, summary=summary, checks=rep.lines, shots=str(shots))
        evidence.parent.mkdir(parents=True, exist_ok=True)
        evidence.write_text(json.dumps(out, indent=2, ensure_ascii=False))
        print(f"evidence: {evidence}; screenshots: {shots}")
        print(summary)
        if not a.keep:
            shutil.rmtree(work, ignore_errors=True)
        else:
            print(f"kept {work}")
        return 0 if verdict == "PASS" else 1

    try:
        build_go(checkout, bindir)
        dist = checkout / "web/dist" if a.no_build_web else build_web(checkout, a.npm_ci)
        smoke.check((dist / "index.html").exists(), f"no web build at {dist}")
        print(f"built {', '.join(PROGRAMS)} and the web app from {checkout} at {out['commit'][:12]} in {time.monotonic() - started:.0f}s", flush=True)
        database = smoke.postgres(container)
        port, token = smoke.free_port(), secrets.token_hex(24)
        (home / ".persona").mkdir(parents=True)
        cfg = home / ".persona/hub.json"
        cfg.write_text(json.dumps({"listen": f"127.0.0.1:{port}", "token": token, "public_url": f"http://127.0.0.1:{port}",
                                   "name": "Module gate hub", "database_url": database, "machines": []}))
        cfg.chmod(0o600)
        shutil.copytree(dist, home / ".persona/web")  # newer than the build's embedded copy, so the hub serves it
        hub = Hub(bindir, work, home, port, token)
        hub.checkout = checkout
        kept = {}
        if "recording" in ids:  # what the user already has, before the hub first starts
            smoke.seed_recordings(home, checkout / "server")

        phases = [("on", None), ("off", ids), ("back-on", [])]
        for phase, disabled in phases:
            if disabled is not None:
                hub.stop()
                if "recording" in ids:
                    files = smoke.recording_files(home)
                    if phase == "off":
                        kept["files"] = files  # as the hub with the module left them
                    else:
                        changed = smoke.files_changed(kept["files"], files)
                        rep.add("off", "recording", f"every file under recordings/ is unchanged after the off phase ({len(files)} files)",
                                not changed, str(changed))
                if disabled:
                    psql(container, "INSERT INTO settings (key, value) VALUES ('modules', '%s'::jsonb) ON CONFLICT (key) DO UPDATE "
                                    "SET value = EXCLUDED.value, updated_at = now()" % json.dumps({"disabled": disabled}))
                else:
                    psql(container, "DELETE FROM settings WHERE key = 'modules'")
            loaded = hub.start()
            row = psql(container, "SELECT value::text FROM settings WHERE key = 'modules'")
            print(f"{phase}: settings row modules = {row or '(none)'}; manager: modules: loaded {loaded}", flush=True)
            on = {id: not disabled for id in ids}
            if phase == "on":
                for id in ids:
                    for method, path, body in SERVER.get(id, {}).get("seed", []):
                        status, text = hub.request(method, path, body)
                        smoke.check(status < 300, f"seeding {id}: {method} {path}: {status} {text[:200]}")
            server_checks(rep, phase, hub, ids, on, loaded)
            kept_checks(rep, phase, hub, ids, on, kept, home)
            seen = browser(rep, phase, hub.base, token, "test", ids, on, shots, work, schemes)
            out["phases"][phase] = {"settings_row": row or None, "manager_line": f"modules: loaded {loaded}",
                                    "hello_module_flags": [f for f in hub.features() if f == "modules" or f.startswith("module.")],
                                    "browser_hello": seen["hello"], "requested": seen["requested"]}
            if phase == "on":
                all_flags = out["phases"]["on"]["hello_module_flags"]
        hub.stop()
        hub = None
        if not a.no_mock:
            mock_phase(rep, "older-hub", ids, None, dist, shots, work, schemes, procs)
            mock_phase(rep, "mock-flags-off", ids, [f for f in all_flags if f != "modules" and f.removeprefix("module.") not in ids],
                       dist, shots, work, schemes, procs)
        verdict = "PASS" if rep.failed == 0 else "FAIL"
        summary = f"{verdict} module e2e gate for {', '.join(ids)} at {out['commit'][:12]}: {len(rep.lines) - rep.failed - rep.warned} checks passed, {rep.failed} failed, {rep.warned} warnings ({time.monotonic() - started:.0f}s)"
    except (Failed, TimedOut) as e:
        verdict = "TIMEOUT" if isinstance(e, TimedOut) else "FAIL"
        logs = sorted(work.glob("hub-*.log"))
        if logs:
            sys.stderr.write(logs[-1].read_text()[-6000:])
        summary = f"{verdict} module e2e gate for {', '.join(ids)} at {out['commit'][:12]}: {e}"
    finally:
        if hub is not None and hub.proc is not None and hub.proc.poll() is None:
            os.killpg(hub.proc.pid, signal.SIGKILL)
            hub.proc.wait(10)
        for p in procs:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGKILL)
                p.wait(10)
        subprocess.run([smoke.DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)
        if a.keep:
            print(f"kept {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)
    out.update(verdict=verdict, summary=summary, checks=rep.lines, shots=str(shots))
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(out, indent=2, ensure_ascii=False))
    print(f"evidence: {evidence}; screenshots: {shots}")
    print(summary)
    return 0 if verdict == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
