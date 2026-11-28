#!/usr/bin/env python3
"""Start a Persona Core hub and manager built without any module, and check the core alone runs (#13978).

Builds personad, persona-hub, persona-manager and persona-node with -tags persona_core_only into a temporary directory, starts a
disposable Postgres 18 container on a private port, writes a hub.json for a temporary home (a token of its own, no
computers) and runs `persona-hub run` with a clean environment, so nothing of this machine's own hub (~/.persona,
PERSONA_HOME) is read. The hub starts the persona-manager beside it, as an installed hub does. Then it checks:

- /api/v1/health answers ok, and an authenticated request (/api/v1/managers) is served;
- the manager connects and says `modules: loaded none` (internal/assistant/modules.go), and the hub's hello still
  carries the `modules` marker with no built-in module's `module.<id>` flag (a client-only flag such as
  `module.glasses` may stay), which is the wire fact every client's hide-decision reads (#16243,
  docs/architecture/core-and-modules.md "Hub flags");
- every extension program a module provides (builtinext.AddProgram in server/internal/modules/all) is an unknown
  subcommand of personad and persona-manager (usage, exit 2), its built-in extension is not in /api/v1/extensions, and
  that extension's route (/api/v1/extensions/<id>/-/assistant-data) answers 404;
- every device command a module's node half provides (commandport.AddCommand in server/internal/modules/all/node) is
  an unknown subcommand of personad and persona-node, and their usage does not list it;
- every connector a module registers on every hub (its port provided in server/internal/modules/all, ALWAYS_CONNECTORS)
  is not in /api/v1/connectors?all=1, and its route (/api/v1/connectors/<id>/auth) answers 404;
- every built-in extension a connector module adds (MODULE_EXTENSIONS) is not in /api/v1/extensions, and the routes
  a module serves through its seam (MODULE_ROUTES: mail, recording) answer 404;
- a module that is off keeps what the user has (#13430, docs/architecture/core-and-modules.md): two recordings a hub
  with the recording module wrote (a meeting with its transcript and minutes, and one whose minutes were still to be
  written) are in the knowledge base before the hub starts; without the module they are still listed, opened and
  found by search through the knowledge base's own routes, the one in flight says `paused: module_off` in the
  response only, and once the hub has stopped every file under recordings/ is byte for byte what it was;
- SIGTERM stops the hub and the manager within the timeout, exit 0.

--full runs the same against a default build and expects the opposite (every module loaded, the programs, extensions
and routes there), which shows the checks are not vacuous.

--switched-off <ids> (comma-separated module ids) is the run-time switch (#14405): a default build starts with every
module, then the `modules` settings row (`{"disabled": [ids]}`) is written into the manager's database and the hub
restarts. The switched-off modules must then be absent from the hub as in the core-only run (not in the manager's
`modules: loaded`, no `module.<id>` flag in the hub's features, no extension, connector or route), and every other module
still present. The row is cleared and the hub restarted once more: the modules come back.

Prints one verdict line: PASS, FAIL or TIMEOUT.

    python3 scripts/ci/core-only-smoke.py [--full | --switched-off <ids>] [--keep]

Needs Go, Docker (DOCKER names another client, e.g. a wrapper) and Python 3.
"""
import argparse
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
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVER = ROOT / "server"
PROGRAMS = ("personad", "persona-hub", "persona-manager", "persona-node")
DOCKER = os.environ.get("DOCKER", "docker")


class Failed(Exception):
    pass


class TimedOut(Exception):
    pass


def root_files(root):
    """The composition root's non-test files, each with the module id of its build constraint (!persona_without_<tag>,
    the tag's _ an id's -), "" for a file of no module."""
    out = []
    for path in sorted((SERVER / root).glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text()
        m = re.search(r"^//go:build .*?!persona_without_(\w+)", text, re.M)
        out.append((m.group(1).replace("_", "-") if m else "", text))
    return out


def by_module(root, pattern, mapping=None):
    """{module id: [names]} of the root's calls matching pattern; mapping turns a match into a name (None: skip)."""
    found = {}
    for module, text in root_files(root):
        for hit in re.findall(pattern, text):
            name = mapping.get(hit) if mapping is not None else hit
            if name and name not in found.setdefault(module, []):
                found[module].append(name)
    return found


def calls_found(root, call, found):
    """Every call of call in the root's files was read (a changed call shape must not make the checks vacuous)."""
    calls = sum(text.count(call + "(") for _, text in root_files(root))
    names = sum(len(n) for n in found.values())
    if calls != names:
        raise Failed(f"{root}: {calls} calls of {call}, {names} read: update the smoke's pattern")
    return found


def flat(found, ids=None):
    return sorted({n for m, names in found.items() if ids is None or m in ids for n in names})


def module_programs():
    """The extension programs the built-in modules provide: builtinext.AddProgram(<module>, "<name>", …) in modules/all."""
    return calls_found("internal/modules/all", "builtinext.AddProgram",
                       by_module("internal/modules/all", r'builtinext\.AddProgram\([^"]*"([^"]+)"'))


def node_commands():
    """The device commands the modules' node halves provide: commandport.AddCommand("<name>", …) in modules/all/node."""
    return calls_found("internal/modules/all/node", "commandport.AddCommand",
                       by_module("internal/modules/all/node", r'commandport\.AddCommand\([^"]*"([^"]+)"'))


# The connector modules whose connector the manager registers on every hub, unconfigured too: the port the composition
# root provides (server/internal/modules/all) and the connector's id.
ALWAYS_CONNECTORS = {"githubport": "github"}
# The connector modules that add a built-in extension of the extension manager's (its settings form), by port.
MODULE_EXTENSIONS = {"imessageport": "imessage"}


# A route a module serves through its provider seam (not the kernel's hub_routes), by module id: answered (not 404)
# while it is there, 404 once it is gone.
MODULE_ROUTES = {"mail": "/api/v1/mail/accounts", "recording": "/api/v1/recordings"}


# The recordings the knowledge base holds before the hub starts
# (server/internal/modules/recording/writers/recording_fixture_test.go writes them with the knowledge base and the
# recording module's writers, as a hub with the recording module leaves them): a meeting with its transcript
# and minutes, and one whose minutes were still to be written. KEPT_WORDS are found by search: a word of the transcript
# and one of the minutes.
KEPT_RECORDING, KEPT_PENDING = "6f1c2a3b-0000-4000-8000-00000000c0de", "6f1c2a3b-0000-4000-8000-00000000beef"
# The first holds one photo (attachments/p1.jpg); the third is a hub-transcribed meeting in flight with two seconds of
# audio held (attachments/audio/0.pcm), which no route serves: it is the module's own input, kept untouched.
KEPT_AUDIO = "6f1c2a3b-0000-4000-8000-00000000a0d1"
# A hub-transcribed meeting with timing "turn", speaker timeline, and synthetic names (Ada, Grace).
KEPT_SPEAKERS = "6f1c2a3b-0000-4000-8000-000000005bea"
KEPT_WORDS = ("quokka", "wombat")


def seed_recordings(home, server=SERVER):
    """Write the kept recordings into the knowledge base of the hub whose home is `home` (before it starts)."""
    data = home / ".persona/data"
    data.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, PERSONA_RECORDING_FIXTURE_DIR=str(data))
    r = run(["go", "test", "-count=1", "-run", "^TestWriteRecordingFixture$", "./internal/modules/recording/writers/"], 600, cwd=server, env=env)
    if r.returncode != 0:
        raise Failed("writing the kept recordings: " + (r.stdout + r.stderr).strip()[-2000:])
    check(recording_files(home), f"the fixture wrote no recording under {data}")


def recording_files(home):
    """Every file under the knowledge base's recordings/, by its path there: its bytes."""
    root = home / ".persona/data/recordings"
    return {str(p.relative_to(root)): p.read_bytes() for p in sorted(root.rglob("*")) if p.is_file()}


def files_changed(before, after):
    """The paths whose bytes differ, appeared or went between two recording_files."""
    return sorted(k for k in set(before) | set(after) if before.get(k) != after.get(k))


def http_raw(url, token=None):
    """http for a body that is not text: (status, headers, bytes)."""
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"} if token else {})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.headers, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


def http_method(method, url, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if data is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def kept_recordings(base, token, module_on, home=None):
    """What the knowledge base's own routes answer for the kept recordings, as (check, ok, detail) rows: listed,
    opened and found, with or without the recording module; `paused` only without it, on the one in flight."""
    rows = []
    state = "with" if module_on else "without"

    def get(path):
        status, body = http(base + path, token)
        return status, (json.loads(body) if status == 200 else {})

    status, body = get("/api/v1/kb/recordings")
    paused = {i["id"]: i.get("paused", "") for i in body.get("items") or []}
    want = {KEPT_RECORDING: "", KEPT_PENDING: "" if module_on else "module_off"}
    rows.append((f"the stored recordings are listed {state} the module (GET /api/v1/kb/recordings)",
                 status == 200 and all(i in paused for i in want), f"status {status}, ids {sorted(paused)}"))
    rows.append((f"the list marks only the recording in flight as paused {state} the module",
                 {i: paused.get(i) for i in want} == want, str(paused)))
    status, body = get(f"/api/v1/kb/recordings/{KEPT_RECORDING}")
    d = body.get("detail") or {}
    rows.append((f"a stored recording opens with its transcript and minutes {state} the module",
                 status == 200 and len(d.get("segments") or []) == 3 and KEPT_WORDS[1] in json.dumps(d.get("minutes"))
                 and KEPT_WORDS[0] in d.get("recent", "") and "paused" not in d,
                 f"status {status}, keys {sorted(d)}"))
    status, body = get(f"/api/v1/kb/recordings/{KEPT_PENDING}")
    d2 = body.get("detail") or {}
    rows.append((f"the recording in flight opens and says paused={'(absent)' if module_on else 'module_off'} {state} the module",
                 status == 200 and d2.get("paused", "") == want[KEPT_PENDING], f"status {status}, paused {d2.get('paused')!r}"))
    for word in KEPT_WORDS:
        status, body = get(f"/api/v1/kb-search?results=1&q={word}")
        rows.append((f"search finds the stored recording by {word!r} {state} the module",
                     status == 200 and KEPT_RECORDING in json.dumps(body), f"status {status}, {json.dumps(body)[:300]}"))
    status, body = get(f"/api/v1/kb/recordings/{KEPT_SPEAKERS}")
    ds = body.get("detail") or {}
    sp = ds.get("speakers") or {}
    sp_names = [p.get("name") for p in (sp.get("people") or []) if p.get("name")]
    sp_segs = [sg.get("speaker") for sg in (ds.get("segments") or [])]
    rows.append((f"a hub meeting with speakers keeps detail.speakers, names and segment speakers {state} the module",
                 status == 200 and sp.get("state") == "ready" and sp_names == ["Ada", "Grace"]
                 and sp_segs == ["s1", "s2", "s1"] and len(sp.get("spans") or []) == 3,
                 f"status {status}, state {sp.get('state')!r}, names {sp_names}, segs {sp_segs}"))
    if not module_on:
        st_edit, _ = http_method("POST", f"{base}/api/v1/recordings/{KEPT_SPEAKERS}/speakers", token, {"rev": 0, "ops": []})
        rows.append(("POST /api/v1/recordings/{id}/speakers answers 404 without the module", st_edit == 404, f"status {st_edit}"))
        st_imp, _ = http_method("PUT", f"{base}/api/v1/recordings/{KEPT_SPEAKERS}/speakers/turns", token, {})
        rows.append(("PUT /api/v1/recordings/{id}/speakers/turns answers 404 without the module", st_imp == 404, f"status {st_imp}"))
    else:
        st_imp, _ = http_method("PUT", f"{base}/api/v1/recordings/{KEPT_SPEAKERS}/speakers/turns", token, {})
        rows.append(("PUT /api/v1/recordings/{id}/speakers/turns answers 404 with debug switch off", st_imp == 404, f"status {st_imp}"))
    if home is not None:  # the stored photo, read back through the route: the file's bytes, with or without the module
        files = recording_files(home)
        stored = files.get(f"{KEPT_RECORDING}/attachments/p1.jpg")
        status, headers, raw = http_raw(f"{base}/api/v1/recordings/{KEPT_RECORDING}/photos/1", token)
        rows.append((f"a stored photo is served {state} the module, its bytes those of attachments/p1.jpg (GET /api/v1/recordings/{{id}}/photos/1)",
                     stored is not None and status == 200 and raw == stored and headers.get("Content-Type") == "image/jpeg"
                     and headers.get("X-Content-Type-Options") == "nosniff" and headers.get("Cache-Control") == "private, no-store",
                     f"status {status}, {len(raw)} bytes of {len(stored or b'')}, {headers.get('Content-Type')}"))
        status, _, _ = http_raw(f"{base}/api/v1/recordings/{KEPT_RECORDING}/photos/2", token)
        rows.append((f"a photo that is not listed answers 404 {state} the module", status == 404, f"status {status}"))
        chunk = files.get(f"{KEPT_AUDIO}/attachments/audio/0.pcm")
        rows.append((f"the audio held for the recording in flight is on disk {state} the module (2 s of PCM, attachments/audio/0.pcm)",
                     chunk is not None and len(chunk) == 4 * 16000, f"{len(chunk or b'')} bytes"))
    return rows, d


def check_kept(base, token, module_on, home=None):
    """kept_recordings as the smoke's own checks; the opened recording's detail."""
    deadline = time.monotonic() + 90  # the knowledge base opens with the manager's first use of it
    while True:
        rows, detail = kept_recordings(base, token, module_on, home)
        if all(ok for _, ok, _ in rows) or time.monotonic() > deadline:
            break
        time.sleep(2)
    for what, ok, detail_text in rows:
        check(ok, f"{what}: {detail_text}")
    return detail


def module_connectors():
    """The always-registered connectors the built-in modules provide: <port>.Provide(…) in modules/all."""
    return by_module("internal/modules/all", r"\b(\w+port)\.Provide\(", ALWAYS_CONNECTORS)


def module_extensions():
    """The built-in extensions the connector modules add: <port>.Provide(…) in modules/all, MODULE_EXTENSIONS."""
    return by_module("internal/modules/all", r"\b(\w+port)\.Provide\(", MODULE_EXTENSIONS)


def module_ids():
    """Every built-in module's id: the directories of server/internal/modules with a module.json."""
    return sorted(p.parent.name for p in (SERVER / "internal/modules").glob("*/module.json"))


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def run(args, timeout, **kw):
    try:
        return subprocess.run(args, timeout=timeout, capture_output=True, text=True, **kw)
    except subprocess.TimeoutExpired:
        raise TimedOut(" ".join(map(str, args[:3])))


def build(tags, out):
    env = dict(os.environ, GOFLAGS=(os.environ.get("GOFLAGS", "") + " -trimpath").strip())
    args = ["go", "build"] + (["-tags", tags] if tags else []) + ["-o", str(out) + "/"] + [f"./cmd/{p}" for p in PROGRAMS]
    r = run(args, 900, cwd=SERVER, env=env)
    if r.returncode != 0:
        raise Failed("build: " + (r.stderr or r.stdout).strip()[-2000:])


def postgres(name):
    r = run([DOCKER, "run", "--detach", "--name", name, "-e", "POSTGRES_USER=persona", "-e", "POSTGRES_PASSWORD=smoke-only",
             "-e", "POSTGRES_DB=persona", "-p", "127.0.0.1::5432", "postgres:18-alpine",
             "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"], 180)
    if r.returncode != 0:
        raise Failed("postgres container: " + r.stderr.strip())
    deadline = time.monotonic() + 90
    while run([DOCKER, "exec", name, "pg_isready", "-U", "persona", "-d", "persona"], 10).returncode != 0:
        if time.monotonic() > deadline:
            raise TimedOut("Postgres readiness")
        time.sleep(1)
    # pg_isready answers during the image's first-start init too: wait until the final server takes TCP connections.
    port = run([DOCKER, "port", name, "5432/tcp"], 10).stdout.strip().splitlines()[0].rsplit(":", 1)[1]
    while run([DOCKER, "exec", name, "psql", "-h", "127.0.0.1", "-U", "persona", "-d", "persona", "-c", "select 1"], 10).returncode != 0:
        if time.monotonic() > deadline:
            raise TimedOut("Postgres readiness")
        time.sleep(1)
    return f"postgres://persona:smoke-only@127.0.0.1:{port}/persona?sslmode=disable"


def http(url, token=None):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"} if token else {})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def wait_for(what, seconds, probe):
    deadline = time.monotonic() + seconds
    while True:
        try:
            if probe():
                return
        except (OSError, urllib.error.URLError):
            pass
        if time.monotonic() > deadline:
            raise TimedOut(what)
        time.sleep(1)


def check(cond, why):
    if not cond:
        raise Failed(why)


def subcommands(bindir, full, programs):
    """A module's program is a subcommand of personad and persona-manager only when the module is built in."""
    for prog in ("personad", "persona-manager"):
        for name in programs:
            if full:
                # Running a module's program would start it: personad's usage line for it says it is there.
                if prog == "personad":
                    r = run([str(bindir / prog), "no-such-command"], 30, stdin=subprocess.DEVNULL)
                    check(re.search(rf"personad {re.escape(name)}\b", r.stdout + r.stderr), f"personad's usage lacks {name}")
                continue
            r = run([str(bindir / prog), name], 30, stdin=subprocess.DEVNULL)
            check(r.returncode == 2, f"{prog} {name}: exit {r.returncode}, want 2 (unknown subcommand)")
            check(not re.search(rf"\b{re.escape(prog)} {re.escape(name)}\b", r.stdout + r.stderr),
                  f"{prog} {name}: the usage still lists the module's program")


def device_commands(bindir, full, commands):
    """A node half's device command is a subcommand of personad and persona-node only when the module is built in."""
    for prog in ("personad", "persona-node"):
        usage = run([str(bindir / prog), "no-such-command"], 30, stdin=subprocess.DEVNULL)
        for name in commands:
            listed = re.search(rf"\b(personad|persona-node) {re.escape(name)}\b", usage.stdout + usage.stderr)
            if full:
                check(listed, f"{prog}'s usage lacks {name}")
                continue
            check(not listed, f"{prog}'s usage still lists the module's command {name}")
            r = run([str(bindir / prog), name], 30, stdin=subprocess.DEVNULL)
            check(r.returncode == 2, f"{prog} {name}: exit {r.returncode}, want 2 (unknown subcommand)")


def write_hub_json(home, port, token, database):
    (home / ".persona").mkdir(parents=True, exist_ok=True)
    cfg = home / ".persona/hub.json"
    cfg.write_text(json.dumps({"listen": f"127.0.0.1:{port}", "token": token, "public_url": f"http://127.0.0.1:{port}",
                               "database_url": database, "machines": []}))
    cfg.chmod(0o600)


def start_hub(bindir, home, work, log):
    """Start `persona-hub run` for the temporary home with a clean environment (no PERSONA_HOME or other settings of
    this machine's own hub), its output appended to log. The hub starts the persona-manager beside it."""
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(home), "TMPDIR": str(work), "LANG": "C.UTF-8",
           "PERSONA_LOCAL_NODE": "0"}
    with open(log, "a") as out:
        return subprocess.Popen([str(bindir / "persona-hub"), "run"], cwd=work, env=env, stdout=out, stderr=subprocess.STDOUT,
                                start_new_session=True)


def stop_hub(hub, bindir):
    """SIGTERM the hub's process group: it and its manager stop within the timeout, exit 0, nothing left."""
    os.killpg(hub.pid, signal.SIGTERM)
    try:
        code = hub.wait(60)
    except subprocess.TimeoutExpired:
        raise TimedOut("hub shutdown")
    check(code == 0, f"the hub exited {code} on SIGTERM")
    deadline = time.monotonic() + 15
    while True:
        left = run(["pgrep", "-f", str(bindir)], 10).stdout.split()
        if not left:
            return
        if time.monotonic() > deadline:
            raise Failed(f"processes left after shutdown: {left}")
        time.sleep(0.5)


MODULES_LINE = re.compile(r"modules: loaded (.+)$", re.M)


def wait_ready(hub, base, log, after):
    """Wait for health and for the manager's module list logged after offset `after` of log; the loaded ids."""
    def healthy():
        check(hub.poll() is None, f"the hub exited ({hub.returncode}) before answering")
        status, body = http(base + "/api/v1/health")
        return status == 200 and json.loads(body).get("ok") is True

    wait_for("hub health", 90, healthy)
    wait_for("the manager's module list", 120, lambda: MODULES_LINE.search(log.read_text()[after:]) or hub.poll() is not None)
    check(hub.poll() is None, f"the hub exited ({hub.returncode})")
    loaded = MODULES_LINE.search(log.read_text()[after:]).group(1).strip()
    return [] if loaded == "none" else [m.strip() for m in loaded.split(",")]


def observe(base, token):
    """What the hub serves now: its features, extensions and connectors."""
    status, _ = http(base + "/api/v1/managers", token)
    check(status == 200, f"/api/v1/managers: {status}")
    status, body = http(base + "/api/v1/health")  # loopback: the features are in it
    check(status == 200, f"/api/v1/health: {status}")
    features = set(json.loads(body).get("features") or [])
    status, body = http(base + "/api/v1/extensions", token)
    check(status == 200, f"/api/v1/extensions: {status}")
    extensions = {e["id"] for e in json.loads(body).get("extensions", [])}
    status, body = http(base + "/api/v1/connectors?all=1", token)
    check(status == 200, f"/api/v1/connectors: {status}")
    connectors = {c["id"] for c in json.loads(body).get("connectors") or []}
    return features, extensions, connectors


def expect(base, token, loaded, present, absent, programs, connectors, module_exts, flags=True):
    """Every module in present is there (loaded, its flag, programs' extensions and routes, connectors) and every one in
    absent is not. flags: check every built-in module's module.<id> flag (a core-only hub announces none of them, but
    still announces the `modules` marker every client reads; docs/architecture/core-and-modules.md "Hub flags")."""
    features, extensions, listed = observe(base, token)
    for want, ids in ((True, present), (False, absent)):
        what = "missing" if want else "present"
        for id in ids:
            check((id in loaded) == want, f"module {id} {what} in the manager's modules: loaded {loaded}")
            check(("module." + id in features) == want, f"flag module.{id} {what} in the hub's features")
        for name in flat(programs, ids):
            check((name in extensions) == want, f"extension {name} {what}: {sorted(extensions)}")
            status, _ = http(f"{base}/api/v1/extensions/{name}/-/assistant-data", token)
            check(status == (200 if want else 404), f"/api/v1/extensions/{name}/-/assistant-data: {status}")
        for name in flat(module_exts, ids):
            check((name in extensions) == want, f"extension {name} {what}: {sorted(extensions)}")
        for name in flat(connectors, ids):
            check((name in listed) == want, f"connector {name} {what}: {sorted(listed)}")
            status, _ = http(f"{base}/api/v1/connectors/{name}/auth", token)
            check(status == (200 if want else 404), f"/api/v1/connectors/{name}/auth: {status}")
        for id in ids:
            if id in MODULE_ROUTES:
                status, _ = http(base + MODULE_ROUTES[id], token)
                check((status != 404) == want, f"{MODULE_ROUTES[id]} ({id}): {status}, want {'not ' if want else ''}404")
    if flags and (present or absent):
        check("modules" in features, "the hub's features lack the modules marker")
    if not flags:
        # A core-only hub has no built-in module, but it still publishes the marker every client's hide-decision needs
        # (#16243; docs/architecture/core-and-modules.md "Hub flags"). A client-only flag (modapi.ClientModules, glasses)
        # may stay; any built-in module's flag would mean the core loaded it.
        check("modules" in features, "a core-only hub's features lack the modules marker, so clients cannot tell it serves no module")
        stray = sorted(f for f in features if f.startswith("module.") and f.removeprefix("module.") in set(absent))
        check(not stray, f"a core-only hub announces built-in module flags it loaded no module for: {stray}")


def set_modules_off(container, ids):
    """Write the `modules` settings row ({"disabled": ids}; [] clears it) into every database of the container that
    has the manager's settings table (the manager's own, or the hub's when it shares it)."""
    value = json.dumps({"disabled": ids})
    dbs = run([DOCKER, "exec", container, "psql", "-U", "persona", "-d", "persona", "-Atc",
               "select datname from pg_database where not datistemplate"], 30).stdout.split()
    wrote = []
    for name in dbs:
        has = run([DOCKER, "exec", container, "psql", "-U", "persona", "-d", name, "-Atc",
                   "select to_regclass('public.settings') is not null"], 30).stdout.strip()
        if has != "t":
            continue
        # Module ids are [a-z0-9-], so the JSON has no $: a dollar-quoted literal needs no escaping.
        r = run([DOCKER, "exec", container, "psql", "-U", "persona", "-d", name, "-v", "ON_ERROR_STOP=1",
                 "-c", f"insert into settings (key, value) values ('modules', $v${value}$v$::jsonb) "
                       "on conflict (key) do update set value = excluded.value, updated_at = now()"], 30)
        check(r.returncode == 0, f"writing the modules row in {name}: {r.stderr.strip()}")
        wrote.append(name)
    check(wrote, "no database has the manager's settings table")
    return wrote


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--full", action="store_true", help="a default build, expecting every module (the control)")
    mode.add_argument("--switched-off", metavar="IDS", help="a default build with these modules (comma-separated) "
                      "switched off in the modules setting, then on again")
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory (logs, home)")
    a = ap.parse_args()
    off = sorted({i.strip() for i in (a.switched_off or "").split(",") if i.strip()})
    if a.switched_off is not None:
        unknown = [i for i in off if i not in module_ids()]
        if not off or unknown:
            ap.error(f"--switched-off needs module ids of server/internal/modules: {unknown or 'none given'}")
    default = a.full or bool(off)
    kind = f"switched-off ({','.join(off)})" if off else "default build" if a.full else "core-only"
    work = Path(tempfile.mkdtemp(prefix="persona-core-smoke."))
    bindir, home = work / "bin", work / "home"
    container = "persona-core-smoke-" + secrets.token_hex(4)
    hub = None
    log = work / "hub.log"
    started = time.monotonic()
    try:
        programs = module_programs()
        connectors = module_connectors()
        module_exts = module_extensions()
        commands = node_commands()
        build("" if default else "persona_core_only", bindir)
        print(f"built {', '.join(PROGRAMS)} ({'default' if default else 'core-only'}) in {time.monotonic() - started:.0f}s", flush=True)
        subcommands(bindir, default, flat(programs))
        device_commands(bindir, default, flat(commands))
        database = postgres(container)
        port, token = free_port(), secrets.token_hex(24)
        write_hub_json(home, port, token, database)
        seed_recordings(home)
        stored = recording_files(home)
        base = f"http://127.0.0.1:{port}"
        hub = start_hub(bindir, home, work, log)
        loaded = wait_ready(hub, base, log, 0)
        print(f"hub healthy on 127.0.0.1:{port}, modules loaded: {', '.join(loaded) or 'none'}", flush=True)
        ids = module_ids()
        if default:
            check(loaded, "a default build's manager loaded no module")
            expect(base, token, loaded, ids, [], programs, connectors, module_exts)
        else:
            check(not loaded, f"a core-only manager loaded modules: {loaded}")
            expect(base, token, loaded, [], ids, programs, connectors, module_exts, flags=False)
        opened = check_kept(base, token, "recording" in loaded, home)
        if off:
            rest = [i for i in ids if i not in off]
            for phase, disabled, present, absent in (("off", off, rest, off), ("on again", [], ids, [])):
                dbs = set_modules_off(container, disabled)
                stop_hub(hub, bindir)
                hub = None
                mark = len(log.read_text())
                hub = start_hub(bindir, home, work, log)
                if phase == "off":
                    stored = recording_files(home)  # as the hub with every module left them
                elif "recording" in off:
                    changed = files_changed(stored, recording_files(home))
                    check(not changed, f"files under recordings/ changed while the recording module was off: {changed}")
                loaded = wait_ready(hub, base, log, mark)
                expect(base, token, loaded, present, absent, programs, connectors, module_exts)
                again = check_kept(base, token, "recording" in loaded, home)
                if "recording" in off:
                    check(again == opened, f"the stored recording's detail differs ({phase}): {json.dumps(again)[:400]} / {json.dumps(opened)[:400]}")
                print(f"restarted with modules {json.dumps({'disabled': disabled})} (in {', '.join(dbs)}): "
                      f"modules loaded: {', '.join(loaded) or 'none'}", flush=True)
        stop_hub(hub, bindir)
        hub = None
        if not default:  # no module at all: nothing may have written under recordings/
            changed = files_changed(stored, recording_files(home))
            check(not changed, f"files under recordings/ changed on a hub without the recording module: {changed}")
        gone = off if off else ([] if a.full else ids)
        print(f"PASS {kind} smoke: built, hub healthy, modules loaded: {', '.join(loaded) or 'none'}, "
              f"absent{' then back' if off else ''}: {', '.join(gone) or 'none'} (module programs "
              f"{', '.join(flat(programs, gone)) or 'none'}, connectors {', '.join(flat(connectors, gone)) or 'none'}, "
              f"extensions {', '.join(flat(module_exts, gone)) or 'none'}, routes "
              f"{', '.join(MODULE_ROUTES[i] for i in gone if i in MODULE_ROUTES) or 'none'}), node commands "
              f"{'present' if default else 'absent'} ({', '.join(flat(commands)) or 'none'}), stored recordings listed, "
              f"opened and found{'' if a.full else ', files unchanged without the recording module'}, clean shutdown "
              f"({time.monotonic() - started:.0f}s)")
        return 0
    except (Failed, TimedOut) as e:
        verdict = "TIMEOUT" if isinstance(e, TimedOut) else "FAIL"
        if log.exists():
            sys.stderr.write(log.read_text()[-6000:])
        print(f"{verdict} {kind} smoke: {e}")
        return 1
    finally:
        if hub is not None and hub.poll() is None:
            os.killpg(hub.pid, signal.SIGKILL)
            hub.wait(10)
        subprocess.run([DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)
        if a.keep:
            print(f"kept {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
