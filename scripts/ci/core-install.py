#!/usr/bin/env python3
"""Install the core edition from its packages on a clean machine, as docs/operations/core-install.md says (#16239).

Builds this machine's packages of both editions the way a release does (scripts/release/core_edition.sh for the core
edition, the same go build line as scripts/release/publish.sh for the full one), serves them from one flat directory
as a publisher's site does (install.sh, dl/latest.json, dl/core-latest.json, ...), starts a disposable Postgres 18
container and a clean container (Alpine with Python 3) that holds no checkout, and runs in it the commands of the document's
"Install the hub by hand" and "A computer's node" sections, read from the document itself: only the three settings
lines (SITE, fetch, DATABASE_URL), the node id and its pairing link are given this run's values. Then it checks:

- the document's own check commands pass (health, the manager's `modules: loaded none`, the edition file);
- the hello keeps the `modules` marker and has no built-in module's `module.<id>` flag (a client-only flag may stay);
- the document's node commands install and pair a core node in an isolated home, and the hub lists it online;
- the browser sign-in flow issues a web credential that can read the machines API;
- every installed program is the core edition's build, byte for byte, and its build info says persona_core_only;
- the installer asked the site for core- names only;
- the hub's update check reads core-latest.json only: it reports the core edition's newer release, never the newer
  full release beside it, and refuses a full manifest served as core-latest.json;
- running the installer again without --core keeps the install core-only;
- the full edition installs from the same directory in a second clean container with the same commands without
  --core, asks for no core- name, and its manager loads modules: the full install is what it was.

Nothing of this machine's own hub is used: every program runs in a container or with a clean environment and a
temporary home. Prints one verdict line: PASS, FAIL or TIMEOUT.

    python3 scripts/ci/core-install.py [--keep] [--timeout seconds]

Needs Go, Docker (DOCKER names another client, e.g. a wrapper), git and Python 3; no private configuration.
"""
import argparse
import datetime
import hashlib
import http.server
import json
import os
import platform
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVER = ROOT / "server"
DOC = ROOT / "docs/operations/core-install.md"
SECTION = "Install the hub by hand"
DOCKER = os.environ.get("DOCKER", "docker")
MACHINE = "python:3.13-alpine"  # Alpine with Python 3, which install.sh needs; nothing else
POSTGRES = "postgres:18-alpine"
PROGRAMS = ("personad", "persona-link", "persona-node", "persona-hub", "persona-manager")
HUB_PROGRAMS = ("personad", "persona-hub", "persona-manager")
CORE_TAG = "persona_core_only"
ARCH = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
PLAT = f"linux-{ARCH}"


class Failed(Exception):
    pass


class TimedOut(Exception):
    pass


def check(ok, what):
    if not ok:
        raise Failed(what)


def clean_env(home, **extra):
    """An environment with nothing of this machine's own hub in it: a temporary home, PATH and Go's own settings."""
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(home), "PERSONA_HOME": str(home / ".persona"),
           "TMPDIR": str(home / "tmp"), "LANG": "C.UTF-8"}
    for key, value in os.environ.items():
        if key.startswith("GO") or key in ("CGO_ENABLED",):
            env[key] = value
    go = lambda name: subprocess.run(["go", "env", name], capture_output=True, text=True).stdout.strip()  # noqa: E731
    for name in ("GOCACHE", "GOMODCACHE", "GOPATH"):
        env.setdefault(name, go(name))
    env.update(extra)
    return env


def run(args, timeout, **kw):
    try:
        return subprocess.run(args, timeout=timeout, capture_output=True, text=True, **kw)
    except subprocess.TimeoutExpired:
        raise TimedOut(" ".join(map(str, args[:4])))


def must(args, timeout, what, **kw):
    r = run(args, timeout, **kw)
    if r.returncode != 0:
        raise Failed(f"{what}: {(r.stderr or r.stdout).strip()[-2000:]}")
    return r.stdout


def docker(*args, timeout=120):
    return run([DOCKER, *args], timeout)


def wait_for(what, seconds, probe):
    deadline = time.monotonic() + seconds
    while True:
        got = probe()
        if got:
            return got
        if time.monotonic() > deadline:
            raise TimedOut(what)
        time.sleep(1)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


# --- the document ----------------------------------------------------------------------------------------------------

def section_blocks(section):
    """The sh blocks of one section of the document, in order."""
    text = DOC.read_text()
    m = re.search(rf"^## {re.escape(section)}\n(.*?)(?=^## )", text, re.M | re.S)
    check(m, f"{DOC.name} has no section '{section}'")
    return re.findall(r"^```sh\n(.*?)^```", m.group(1), re.M | re.S)


def doc_blocks():
    """The sh blocks of the document's install section, in order."""
    blocks = section_blocks(SECTION)
    check(len(blocks) == 5, f"'{SECTION}' has {len(blocks)} sh blocks, this check knows 5 (settings, hub.json, install, start, check)")
    return blocks


def node_doc_blocks():
    """The node's pairing, install and no-service-manager start commands, exactly as the document gives them."""
    blocks = section_blocks("A computer's node")
    check(len(blocks) == 3, f"'A computer's node' has {len(blocks)} sh blocks, this check knows 3")
    return blocks


def settings(block, site, database):
    """The settings block with this run's values in its three lines; any other line is the document's."""
    values = {"SITE": site, "fetch": "wget -qO-", "DATABASE_URL": database}
    out, seen = [], set()
    for line in block.splitlines():
        name = line.split("=", 1)[0]
        if name in values:
            seen.add(name)
            line = f"{name}='{values[name]}'"
        out.append(line)
    check(seen == set(values), f"the settings block of '{SECTION}' does not set {sorted(set(values) - seen)}")
    return "\n".join(out) + "\n"


# --- packages --------------------------------------------------------------------------------------------------------

def build_packages(work, env, update_url):
    """Both editions' packages for this machine, laid out as a publisher serves them; the release's facts."""
    build = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d%H%M")
    version = f"v0.0.0-ci-{build}"
    commit = must(["git", "rev-parse", "HEAD"], 30, "git rev-parse", cwd=ROOT).strip()
    ldflags = (f"-s -w -X main.version={version} -X main.channel=release -X main.build={build} -X main.commit={commit} "
               f"-X main.updateURL={update_url}")
    tags = (SERVER / "build-tags.txt").read_text().strip()
    full, core = work / "full/bin", work / "core"
    full.mkdir(parents=True)
    # The full edition: publish.sh's go build line.
    for program in PROGRAMS:
        must(["go", "build", "-tags", tags, "-trimpath", "-ldflags", ldflags, "-o", str(full / f"{program}-{PLAT}"), f"./cmd/{program}"],
             900, f"build {program} (full)", cwd=SERVER, env=dict(env, CGO_ENABLED="0", GOOS="linux", GOARCH=ARCH))
    for binary in sorted(full.iterdir()):
        must(["gzip", "-n", "-9", "-k", str(binary)], 300, "gzip")
    host = str(full / f"personad-{PLAT}")
    must([host, "release", "manifest", "--dir", str(full), "--version", version, "--build", build, "--commit", commit,
          "--out", str(full / "latest.json")], 300, "the full manifest", cwd=ROOT, env=env)
    # The core edition: the release's own script, which also checks the two editions against each other.
    must([str(ROOT / "scripts/release/core_edition.sh"), "build", str(core), f"linux/{ARCH}"], 900, "build the core edition",
         cwd=ROOT, env=dict(env, LDFLAGS=ldflags))
    must([str(ROOT / "scripts/release/core_edition.sh"), "pack", str(core), version, build, commit, "release", str(full)],
         600, "pack the core edition", cwd=ROOT, env=env)
    # One flat directory, as the site serves it.
    site = work / "site"
    (site / "dl").mkdir(parents=True)
    sums = []
    for gz in sorted(full.glob("*.gz")):
        shutil.copy2(gz, site / "dl" / gz.name)
        sums.append(f"{sha256(gz)}  {gz.name}\n")
    (site / "dl/SHA256SUMS").write_text("".join(sums))
    (site / "dl/VERSION").write_text(version + "\n")
    shutil.copy2(full / "latest.json", site / "dl/latest.json")
    for f in sorted((core / "dl").iterdir()):
        shutil.copy2(f, site / "dl" / f.name)
    must([sys.executable, str(ROOT / "scripts/persona_identity.py"), "--render-installer", str(ROOT / "scripts/deploy/install.sh"),
          str(site / "install.sh"), update_url], 60, "render install.sh", cwd=ROOT, env=env)
    return {"version": version, "build": int(build), "commit": commit, "site": site, "full": full, "core": core / "bin"}


def has_core_tag(binary, env):
    out = must(["go", "version", "-m", str(binary)], 60, "go version -m", env=env)
    tags = re.search(r"^\s*build\s+-tags=(\S+)", out, re.M)
    return bool(tags) and CORE_TAG in tags.group(1).split(",")


# --- the site --------------------------------------------------------------------------------------------------------

class Site:
    """The publisher's site: the flat directory over HTTP on the containers' gateway, with every path asked for."""

    def __init__(self, directory, address):
        asked, lock = [], threading.Lock()
        self.asked, self.lock = asked, lock

        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *a, **kw):
                super().__init__(*a, directory=str(directory), **kw)

            def log_message(self, *a):
                pass

            def do_GET(self):
                with lock:
                    asked.append(self.path)
                super().do_GET()

        self.server = http.server.ThreadingHTTPServer((address, 0), Handler)
        # A client that stops a download (a hub refusing a file half way) is not this check's failure.
        self.server.handle_error = lambda *a: None
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def mark(self):
        with self.lock:
            return len(self.asked)

    def since(self, mark):
        with self.lock:
            return list(self.asked[mark:])

    def close(self):
        self.server.shutdown()


def core_names(paths):
    return [p for p in paths if p.startswith("/dl/") and p.rsplit("/", 1)[-1].startswith("core-")]


def full_names(paths):
    return [p for p in paths if p.startswith("/dl/") and not p.rsplit("/", 1)[-1].startswith("core-")]


# --- machines --------------------------------------------------------------------------------------------------------

class Machine:
    """A clean container: nothing in it but the base image, on the test network."""

    def __init__(self, name, network):
        self.name = name
        r = docker("run", "--detach", "--name", name, "--network", network, MACHINE, "sleep", "3600", timeout=300)
        check(r.returncode == 0, f"start {name}: {r.stderr.strip()}")

    def sh(self, script, timeout=300):
        try:
            return subprocess.run([DOCKER, "exec", "-i", self.name, "sh", "-e"], input=script, timeout=timeout,
                                  capture_output=True, text=True)
        except subprocess.TimeoutExpired:
            raise TimedOut(f"a step in {self.name}")

    def must(self, script, what, timeout=300):
        r = self.sh(script, timeout)
        check(r.returncode == 0, f"{what}: exit {r.returncode}\n{(r.stdout + r.stderr).strip()[-3000:]}")
        return r.stdout + r.stderr

    def copy_out(self, path, to):
        r = docker("cp", f"{self.name}:{path}", str(to))
        check(r.returncode == 0, f"copy {path} out of {self.name}: {r.stderr.strip()}")
        return to

    def log(self):
        return self.sh("cat ~/.persona/hub.log 2>/dev/null || true").stdout[-4000:]


def install(machine, blocks, site_url, database, what, edit=lambda s: s):
    """Follow the document on machine: settings, hub.json, install, start, then its checks until they pass."""
    head = settings(blocks[0], site_url, database)
    out = machine.must(edit(head + blocks[1] + blocks[2]), f"{what}: write hub.json and install", timeout=600)
    machine.must(head + blocks[3], f"{what}: start the hub")
    return out, head


def api(machine, method, path, timeout=60):
    """An authenticated request to the hub from inside its machine, with the token of the hub.json the test wrote."""
    post = "--post-data=''" if method == "POST" else ""
    script = ("TOKEN=\"$(sed -n 's/.*\"token\": *\"\\([^\"]*\\)\".*/\\1/p' ~/.persona/hub.json)\"\n"
              f"wget -qO- {post} --header \"Authorization: Bearer $TOKEN\" 'http://127.0.0.1:18660{path}'\n")
    r = machine.sh(script, timeout)
    return r.returncode, r.stdout, r.stderr


def browser_sign_in(machine):
    """Run the real browser sign-in handshake, then use its web credential; print no credential."""
    script = r"""python3 - <<'PY'
import json
import os
import urllib.parse
import urllib.request

home = os.path.expanduser("~")
assert home == "/root", home
with open(os.path.join(home, ".persona", "hub.json")) as f:
    config = json.load(f)
base = "http://127.0.0.1:18660"
assert config["public_url"] == base, config["public_url"]

def call(method, path, body=None, token=None):
    headers = {}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=20) as response:
        assert response.status == 200, response.status
        return json.load(response)

# The read-only check comes first and proves this is the disposable hub and home before any credential write.
assert call("GET", "/api/v1/health")["ok"] is True
phone = call("POST", "/api/v1/auth/tokens", {"name": "CI phone", "kind": "phone"}, config["token"])
started = call("POST", "/api/v1/auth/browser-login/start", {"name": "CI browser"})
hub = urllib.parse.parse_qs(urllib.parse.urlparse(started["url"]).query)["hub"][0]
approval = call("POST", "/api/v1/auth/browser-login/approve",
                {"id": started["id"], "hub": hub, "allow": True, "show_number": True}, phone["token"])
signed_in = call("POST", "/api/v1/auth/browser-login/confirm",
                 {"id": started["id"], "secret": started["secret"], "number": approval["number"]})
assert signed_in["token"].startswith("jd_")
machines = call("GET", "/api/v1/machines", token=signed_in["token"])
assert isinstance(machines.get("machines"), list), sorted(machines)
print("browser sign-in: full web credential issued; GET /api/v1/machines 200")
PY
"""
    return machine.must(script, "browser sign-in", timeout=90).strip()


def pair_node(machine, head, blocks, release, work, env):
    """Follow the node section in an isolated home on the clean machine; no checkout or host configuration."""
    # Read-only status in the same environment immediately before the pairing command writes the new computer.
    code, body, err = api(machine, "GET", "/api/v1/health")
    check(code == 0 and json.loads(body).get("ok") is True, f"node pairing's status check: {err or body}")
    pair = machine.sh(blocks[0].replace("<machine id>", "ci-node"), 60)
    check(pair.returncode == 0, f"make the node pairing link: {(pair.stdout + pair.stderr)[-1000:]}")
    pair_url = pair.stdout.strip()
    check(pair_url.startswith("persona://link?"), "the pairing command did not print only a computer pairing link")

    # One clean container, two isolated homes: the node cannot read the hub's home or its master token. Its pairing
    # link's loopback address therefore reaches the disposable hub, exactly as it would on one computer.
    node_home = "export HOME=/root/core-install-node\nexport PERSONA_HOME=$HOME/.persona\n"
    install_block = blocks[1].replace("'<the printed pairing link>'", json.dumps(pair_url))
    r = machine.sh(node_home + head + install_block, 600)
    check(r.returncode == 1, f"the node install without a service manager should install, pair and exit 1: "
                             f"exit {r.returncode}\n{(r.stdout + r.stderr)[-2000:]}")
    machine.must(node_home + "test -x ~/.persona/bin/persona-node && test -x ~/.persona/bin/persona-link && "
                             "test -s ~/.persona/link.json && test ! -e ~/.persona/hub.json", "the paired node files")
    machine.must(node_home + blocks[2], "start the node as the document's no-service-manager block says")

    def online():
        code, body, _ = api(machine, "GET", "/api/v1/machines")
        if code != 0:
            return None
        machines = json.loads(body).get("machines") or []
        return next((m for m in machines if m.get("id") == "ci-node" and m.get("online") is True
                     and m.get("node_version") == release["version"]), None)

    found = wait_for("the paired core node in the hub's machine list", 150, online)
    check(found.get("kind") == "link" and found.get("transport") == "link", f"the node's transport: {found}")
    for program in ("persona-node", "persona-link"):
        got = machine.copy_out(f"/root/core-install-node/.persona/bin/{program}", work / f"installed-core-{program}")
        check(sha256(got) == sha256(release["core"] / f"{program}-{PLAT}"), f"the installed {program} is not the core build")
        check(has_core_tag(got, env), f"the installed {program} lacks {CORE_TAG}")
    return found


def manifest(release, version, build, edition):
    """A release manifest as a hub's update check reads it; its downloads are never fetched by the checks below."""
    m = {"version": version, "build": build, "commit": release["commit"], "released": "2026-01-01T00:00:00Z", "protocol": 1,
         "files": {PLAT: {"name": f"personad-{PLAT}.gz", "size": 1, "sha256": "0" * 64, "binary_sha256": "0" * 64}}}
    if edition == "core":
        m["edition"] = "core"
        m["files"][PLAT]["served"] = f"core-personad-{PLAT}.gz"
    return json.dumps(m)


def later(build, days):
    t = datetime.datetime.strptime(str(build), "%Y%m%d%H%M") + datetime.timedelta(days=days)
    return int(t.strftime("%Y%m%d%H%M"))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--keep", action="store_true", help="leave the containers, the network and the work directory")
    parser.add_argument("--timeout", type=int, default=1500, help="seconds for the whole check")
    args = parser.parse_args()

    def alarm(*_):
        raise TimedOut(f"the whole check ({args.timeout}s)")

    signal.signal(signal.SIGALRM, alarm)
    signal.alarm(args.timeout)

    run_id = secrets.token_hex(4)
    prefix = f"persona-core-install-{run_id}"
    network, pg = f"{prefix}-net", f"{prefix}-pg"
    work = Path(tempfile.mkdtemp(prefix="persona-core-install-"))
    home = work / "home"
    (home / "tmp").mkdir(parents=True)
    site, machines, verdict, started = None, [], "FAIL", time.monotonic()
    try:
        blocks = doc_blocks()
        node_blocks = node_doc_blocks()
        env = clean_env(home)
        r = docker("network", "create", network)
        check(r.returncode == 0, f"docker network: {r.stderr.strip()}")
        gateway = must([DOCKER, "network", "inspect", network, "--format", "{{(index .IPAM.Config 0).Gateway}}"], 30,
                       "the network's gateway").strip()
        # The site's address is in the binaries (their update URL) and in install.sh: listen first, then build.
        (work / "site").mkdir()
        site = Site(work / "site", gateway)
        site_url = f"http://{gateway}:{site.port}"
        (work / "site").rmdir()
        release = build_packages(work, env, site_url + "/dl")
        for program in PROGRAMS:
            check(has_core_tag(release["core"] / f"{program}-{PLAT}", env), f"the core edition's {program} lacks {CORE_TAG}")
            check(not has_core_tag(release["full"] / f"{program}-{PLAT}", env), f"the full edition's {program} has {CORE_TAG}")
        print(f"packages: {release['version']} for {PLAT}, both editions, served at {site_url}", flush=True)

        password = secrets.token_hex(8)
        r = docker("run", "--detach", "--name", pg, "--network", network, "-e", "POSTGRES_USER=persona",
                   "-e", f"POSTGRES_PASSWORD={password}", "-e", "POSTGRES_DB=persona", POSTGRES,
                   "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off", timeout=300)
        check(r.returncode == 0, f"postgres container: {r.stderr.strip()}")
        wait_for("Postgres readiness", 90, lambda: docker(
            "exec", pg, "psql", "-h", "127.0.0.1", "-U", "persona", "-d", "persona", "-c", "select 1", timeout=10).returncode == 0)
        must([DOCKER, "exec", pg, "createdb", "-h", "127.0.0.1", "-U", "persona", "persona_full"], 30, "the full edition's database")
        database = lambda name: f"postgres://persona:{password}@{pg}:5432/{name}?sslmode=disable"  # noqa: E731

        # 1. The core edition, by the document.
        core = Machine(f"{prefix}-core", network)
        machines.append(core)
        mark = site.mark()
        out, head = install(core, blocks, site_url, database("persona"), "core edition")
        asked = site.since(mark)
        check(not full_names(asked), f"the --core install asked for a full edition file: {full_names(asked)}")
        check(f"/dl/core-personad-{PLAT}.gz" in asked and "/dl/core-SHA256SUMS" in asked, f"the --core install asked for {asked}")

        def documented_checks(machine, expect_core):
            checks = blocks[4] if expect_core else blocks[4].replace("grep 'modules: loaded none'", "grep 'modules: loaded'") \
                .replace("cat ~/.persona/bin/.edition", "test ! -e ~/.persona/bin/.edition")
            return machine.sh(head + checks, 30)

        def passes(machine, expect_core):
            r = documented_checks(machine, expect_core)
            return r if r.returncode == 0 else None

        try:
            r = wait_for("the document's checks on the core edition", 180, lambda: passes(core, True))
        except TimedOut:
            raise Failed("the document's checks never passed on the core edition:\n" + documented_checks(core, True).stderr[-800:]
                         + "\n" + core.log())
        check('"ok":true' in r.stdout.replace(" ", ""), f"health: {r.stdout[:200]}")
        check(r.stdout.strip().splitlines()[-1] == "core", f"the edition file says {r.stdout.strip().splitlines()[-1]!r}")
        check("modules: loaded none" in r.stdout, "the manager's module list")
        for program in HUB_PROGRAMS:
            got = core.copy_out(f"/root/.persona/bin/{program}", work / f"installed-core-{program}")
            check(sha256(got) == sha256(release["core"] / f"{program}-{PLAT}"), f"the installed {program} is not the core edition's build")
            check(has_core_tag(got, env), f"the installed {program} lacks {CORE_TAG}")
        version = core.must("~/.persona/bin/personad version -v", "personad version")
        check(version.splitlines()[0] == release["version"] and f"channel release, build {release['build']}" in version,
              f"personad version -v: {version!r}")
        health = json.loads(r.stdout.splitlines()[0])
        features = set(health.get("features") or [])
        check("modules" in features, "the hello lacks the modules marker")
        built_in = {p.parent.name for p in (SERVER / "internal/modules").glob("*/module.json")}
        module_flags = sorted(f for f in features if f.startswith("module.") and f.removeprefix("module.") in built_in)
        check(not module_flags, f"the core-only hello announces built-in module flags: {module_flags}")
        client_flags = sorted(f for f in features if f.startswith("module.") and f.removeprefix("module.") not in built_in)
        print(f"hello: modules marker present, no built-in module.<id> flag (client-only: {', '.join(client_flags) or 'none'})",
              flush=True)
        print(browser_sign_in(core), flush=True)
        mark = site.mark()
        node = pair_node(core, head, node_blocks, release, work, env)
        asked = site.since(mark)
        check(not full_names(asked), f"the core node install asked for a full edition file: {full_names(asked)}")
        check(f"/dl/core-persona-node-{PLAT}.gz" in asked and f"/dl/core-persona-link-{PLAT}.gz" in asked
              and f"/dl/{release['version']}/core-manifest.json" in asked,
              f"the core node install asked for {asked}")
        print(f"node: {node['id']} paired and online over Persona Link; core persona-node and persona-link", flush=True)
        print("core edition: installed by the document, hub and manager run, modules: loaded none", flush=True)

        # 2. Updates stay core-only. A newer release of each edition appears; the hub sees only its own.
        newer_core, newer_full = "v0.0.1-ci-core", "v9.9.9-ci-full"
        dl = release["site"] / "dl"
        real_core_manifest = (dl / "core-latest.json").read_text()
        (dl / "core-latest.json").write_text(manifest(release, newer_core, later(release["build"], 1), "core"))
        (dl / "latest.json").write_text(manifest(release, newer_full, later(release["build"], 2), "full"))
        mark = site.mark()
        code, body, err = api(core, "POST", "/api/v1/hub/update/check")
        check(code == 0, f"the update check failed: {err.strip()[-400:]}")
        view = json.loads(body)
        latest = view.get("latest") or {}
        check(view.get("can_update") is True, f"a release build with an update URL does not update itself: {body[:400]}")
        check(latest.get("version") == newer_core and latest.get("newer") is True,
              f"the hub's newest release is {latest.get('version')!r}, want the core edition's {newer_core}")
        asked = site.since(mark)
        check("/dl/core-latest.json" in asked and "/dl/latest.json" not in asked, f"the update check asked for {asked}")
        # A full manifest under the core's name is refused: the hub does not offer it.
        (dl / "core-latest.json").write_text(manifest(release, newer_full, later(release["build"], 3), "full"))
        code, body, err = api(core, "POST", "/api/v1/hub/update/check")
        offered = ""
        if code == 0:
            offered = (json.loads(body).get("latest") or {}).get("version", "")
        check(offered != newer_full, f"a full release served as core-latest.json was offered: {body[:400]}")
        (dl / "core-latest.json").write_text(real_core_manifest)
        print(f"updates: the hub read core-latest.json only, saw {newer_core}, never {newer_full}", flush=True)

        # 3. The installer again, without --core: still the core edition.
        mark = site.mark()
        again = core.must(head + blocks[2].replace(" --core", ""), "the installer again without --core", timeout=600)
        check("core edition" in again, f"the installer did not say it keeps the core edition:\n{again[-600:]}")
        asked = site.since(mark)
        check(not full_names(asked) and core_names(asked), f"the second install asked for {asked}")
        got = core.copy_out("/root/.persona/bin/personad", work / "installed-again-personad")
        check(sha256(got) == sha256(release["core"] / f"personad-{PLAT}"), "the second install replaced the core edition's personad")
        check(core.must("cat ~/.persona/bin/.edition", "the edition file").strip() == "core", "the edition file changed")
        print("installer: a second run without --core kept the core edition", flush=True)

        # 4. The full edition from the same directory: the same commands without --core.
        shutil.copy2(release["full"] / "latest.json", dl / "latest.json")
        full = Machine(f"{prefix}-full", network)
        machines.append(full)
        mark = site.mark()
        install(full, blocks, site_url, database("persona_full"), "full edition", edit=lambda s: s.replace(" --core", ""))
        asked = site.since(mark)
        check(not core_names(asked), f"the full install asked for a core file: {core_names(asked)}")
        check(f"/dl/personad-{PLAT}.gz" in asked and "/dl/SHA256SUMS" in asked, f"the full install asked for {asked}")
        try:
            r = wait_for("the checks on the full edition", 180, lambda: passes(full, False))
        except TimedOut:
            raise Failed("the checks never passed on the full edition:\n" + documented_checks(full, False).stderr[-800:] + "\n" + full.log())
        loaded = re.search(r"modules: loaded (.+)", r.stdout)
        check(loaded and loaded.group(1).strip() != "none", f"the full edition's manager loaded {loaded.group(1) if loaded else '?'}")
        for program in HUB_PROGRAMS:
            got = full.copy_out(f"/root/.persona/bin/{program}", work / f"installed-full-{program}")
            check(sha256(got) == sha256(release["full"] / f"{program}-{PLAT}"), f"the installed {program} is not the full edition's build")
        mark = site.mark()
        code, body, err = api(full, "POST", "/api/v1/hub/update/check")
        check(code == 0, f"the full hub's update check failed: {err.strip()[-400:]}")
        asked = site.since(mark)
        check("/dl/latest.json" in asked and not core_names(asked), f"the full hub's update check asked for {asked}")
        print(f"full edition: installed from the same directory, modules: loaded {loaded.group(1).strip()}", flush=True)
        verdict = "PASS"
    except Failed as e:
        print(f"core-install: {e}", file=sys.stderr)
    except TimedOut as e:
        verdict = "TIMEOUT"
        print(f"core-install: timed out waiting for {e}", file=sys.stderr)
    finally:
        signal.alarm(0)
        if site:
            site.close()
        if args.keep:
            print(f"kept: {work}, containers and network {prefix}-*", file=sys.stderr)
        else:
            for name in [m.name for m in machines] + [pg]:
                docker("rm", "--force", "--volumes", name, timeout=60)
            docker("network", "rm", network, timeout=60)
            shutil.rmtree(work, ignore_errors=True)
    print(f"{verdict} core-install: the core edition installs from its packages by docs/operations/core-install.md, stays core-only, "
          f"and the full edition is unchanged ({int(time.monotonic() - started)}s)")
    return 0 if verdict == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
