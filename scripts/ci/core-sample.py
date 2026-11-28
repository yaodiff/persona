#!/usr/bin/env python3
"""The core-only sample gate (#16244): a feature is added to Persona Core without changing Core, and switched off.

    python3 scripts/ci/core-sample.py [--keep] [--evidence <file>]

It follows docs/extensions/core-only-features.md literally with the sample extension sdk/examples/quicknotes, on a
hub built with persona_core_only, in a temporary home on a disposable Postgres container (the helpers of
scripts/ci/core-only-smoke.py: nothing of this machine's own hub is read or reached):

  absent     the core-only hub is healthy and loads no module; the sample's flag, entry and route are not there
  refused    an install with a deliberately wrong --expect-home is refused before any request or write: non-zero exit,
             the mismatch names both homes, no flag, no entry, the package still there; `extension list` unchanged
  installed  `personad extension test`, `pack` and `install` with the core-only personad; the hub announces the
             feature flag `quicknotes`, lists the page in the main navigation (titles in en, zh-Hans and ja), draws
             it, and the route stores and lists a note
  off        `personad extension disable`: the flag and the entry are gone, the route answers 404 for a read and
             for a write, Core still answers, and the stored file is byte for byte what it was
  on again   `personad extension enable`: the same note is read back, the file still unchanged
  uninstalled `personad extension uninstall`: the flag, entry and route are gone, Core answers, and the data directory
             is deleted (the explicit alternative to switching off; --keep-data is documented but not used here)

Every personad command runs with `env -i` and an explicit HOME and PERSONA_HOME of the temporary home: an inherited
PERSONA_HOME would send the command to this machine's own hub (docs/lessons/workflow.md). Before every hub write the
gate runs the read-only `extension list` in that same environment, prints the resolved home and listen address, and
checks they are the sandbox's; the write itself also carries `--expect-home <sandbox PERSONA_HOME>` and
`--expect-hub <sandbox listen>`, so the CLI refuses anything but this sandbox even if the check above were skipped
(#16302). Prints one PASS / FAIL / TIMEOUT line. Needs Go and Docker (DOCKER names another client).
"""
import argparse
import hashlib
import importlib.util
import json
import os
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SAMPLE = "sdk/examples/quicknotes"
ID = FLAG = "quicknotes"
ROUTE = "/api/v1/quicknotes/notes"
TITLES = {"en": "Quick notes", "zh-Hans": "速记", "ja": "クイックメモ"}  # i18n-ignore: the sample's l10n files

_spec = importlib.util.spec_from_file_location("core_only_smoke", ROOT / "scripts/ci/core-only-smoke.py")
smoke = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(smoke)
Failed, TimedOut, check = smoke.Failed, smoke.TimedOut, smoke.check


def call(base, token, method, path, body=None, lang="en"):
    """(status, decoded JSON or text) of one request to the test hub."""
    headers = {"Authorization": f"Bearer {token}", "Accept-Language": lang}
    data = None
    if body is not None:
        data, headers["Content-Type"] = json.dumps(body).encode(), "application/json"
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            status, raw = resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        status, raw = e.code, e.read().decode()
    try:
        return status, json.loads(raw)
    except ValueError:
        return status, raw


def features(base):
    status, body = smoke.http(base + "/api/v1/health")
    check(status == 200, f"/api/v1/health: {status}")
    return set(json.loads(body).get("features") or [])


def entries(base, token, lang="en"):
    """The sample's places among what the apps draw (GET /api/v1/extensions/-/ui)."""
    status, body = call(base, token, "GET", "/api/v1/extensions/-/ui", lang=lang)
    check(status == 200, f"/api/v1/extensions/-/ui: {status}")
    return [p for p in body.get("ui") or [] if p.get("extension") == ID]


def core_answers(base, token):
    for path in ("/api/v1/managers", "/api/v1/extensions"):
        status, _ = call(base, token, "GET", path)
        check(status == 200, f"Core's {path}: {status}")


def cli_env(home):
    """An explicit, minimal environment for personad: this home and nothing of the caller's hub."""
    env = {"HOME": str(home), "PERSONA_HOME": str(home / ".persona"), "LANG": "C.UTF-8",
           "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
    go = smoke.run(["go", "env", "GOCACHE", "GOMODCACHE", "GOPATH"], 30).stdout.split("\n")
    env.update(GOCACHE=go[0], GOMODCACHE=go[1], GOPATH=go[2], GOFLAGS="-mod=mod", GOTOOLCHAIN=os.environ.get("GOTOOLCHAIN", "local"))
    return env


def run_personad(bindir, env, cwd, *args, timeout=600):
    """(exit code, output) of one personad call in the sandbox environment; the caller decides what a failure means."""
    r = smoke.run(["env", "-i"] + [f"{k}={v}" for k, v in env.items()] + [str(bindir / "personad")] + list(args), timeout,
                  cwd=cwd, stdin=subprocess.DEVNULL)
    return r.returncode, (r.stdout + r.stderr).strip()


def personad(bindir, env, cwd, *args, timeout=600):
    code, out = run_personad(bindir, env, cwd, *args, timeout=timeout)
    check(code == 0, f"personad {' '.join(args[:3])}: exit {code}: {out[-1500:]}")
    return out


def target_flags(env, listen):
    """The CLI's own target guard (#16302): the write itself refuses any home or hub but this sandbox's."""
    return ["--expect-home", env["PERSONA_HOME"], "--expect-hub", listen]


def write(bindir, env, cwd, listen, *args, timeout=600):
    """One `personad extension` write (`install`, `disable`, `enable`, `uninstall`), bound by the CLI to this sandbox."""
    return personad(bindir, env, cwd, *args, *target_flags(env, listen), timeout=timeout)


def start_hub(bindir, home, work, log):
    """Start only the sandbox hub, with PERSONA_HOME explicit even in the clean environment."""
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(home), "TMPDIR": str(work),
           "PERSONA_HOME": str(home / ".persona"), "LANG": "C.UTF-8", "PERSONA_LOCAL_NODE": "0"}
    with open(log, "a") as out:
        return subprocess.Popen(["env", "-i"] + [f"{k}={v}" for k, v in env.items()] +
                                [str(bindir / "persona-hub"), "run"], cwd=work, env={"PATH": env["PATH"]},
                                stdout=out, stderr=subprocess.STDOUT, start_new_session=True)


def confirm_target(bindir, env, cwd, listen):
    """A read-only CLI call before a write; never print the sandbox token. The write's own flags bind it (target_flags)."""
    resolved = json.loads((Path(env["PERSONA_HOME"]) / "hub.json").read_text())["listen"]
    check(resolved == listen, f"personad would reach {resolved}, not the test hub {listen}")
    out = personad(bindir, env, cwd, "extension", "list")
    print(f"target confirmed by read-only extension list: PERSONA_HOME={env['PERSONA_HOME']}, hub={resolved}; "
          f"{out or 'no extensions listed'}", flush=True)
    return out


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory (logs, home)")
    ap.add_argument("--evidence", metavar="FILE", help="write what each phase observed as JSON")
    a = ap.parse_args()
    work = Path(tempfile.mkdtemp(prefix="persona-core-sample."))
    bindir, home, log = work / "bin", work / "home", work / "hub.log"
    container = "persona-core-sample-" + secrets.token_hex(4)
    hub, seen, started = None, {}, time.monotonic()
    try:
        smoke.build("persona_core_only", bindir)
        print(f"built the core-only programs in {time.monotonic() - started:.0f}s", flush=True)
        database = smoke.postgres(container)
        port, token = smoke.free_port(), secrets.token_hex(24)
        smoke.write_hub_json(home, port, token, database)
        base = f"http://127.0.0.1:{port}"
        hub = start_hub(bindir, home, work, log)
        loaded = smoke.wait_ready(hub, base, log, 0)
        check(not loaded, f"a core-only manager loaded modules: {loaded}")
        env = cli_env(home)
        resolved = json.loads((Path(env["PERSONA_HOME"]) / "hub.json").read_text())["listen"]
        check(resolved == f"127.0.0.1:{port}", f"personad would reach {resolved}, not the test hub")
        print(f"core-only hub on {resolved} (PERSONA_HOME={env['PERSONA_HOME']}), modules loaded: none", flush=True)

        # absent: Core is whole without the sample.
        core_answers(base, token)
        check(FLAG not in features(base), "the flag is announced before the sample is installed")
        check(not entries(base, token), "the entry is listed before the sample is installed")
        status, _ = call(base, token, "GET", ROUTE)
        check(status == 404, f"{ROUTE} before the install: {status}, want 404")
        seen["absent"] = {"flag": False, "entries": 0, "route": status}
        print("absent: Core answers; no flag, no entry, route 404", flush=True)

        # installed: the document's steps, on a copy so the checkout gets no build output.
        src = work / "src"
        shutil.copytree(ROOT / "sdk/go", src / "sdk/go")
        shutil.copytree(ROOT / SAMPLE, src / SAMPLE, ignore=shutil.ignore_patterns("bin", "dist"))
        sample = src / SAMPLE
        r = smoke.run(["go", "test", "-count=1", "./..."], 600, cwd=sample, env=dict(os.environ, GOFLAGS="-mod=mod"))
        check(r.returncode == 0, f"the sample's go test: {(r.stdout + r.stderr)[-1500:]}")
        personad(bindir, env, sample, "extension", "test")
        platform = "-".join(smoke.run(["go", "env", "GOOS", "GOARCH"], 30).stdout.split())
        personad(bindir, env, sample, "extension", "pack", "--platforms", platform)
        package = sample / "dist" / f"{ID}-0.1.0.tar.gz"
        check(package.exists(), f"pack wrote no {package.name}")
        before_list = confirm_target(bindir, env, sample, resolved)
        # The guard is the write's own: a deliberately wrong home must be refused before any request or write.
        wrong_home = str(work / "not-the-sandbox" / ".persona")
        code, refused = run_personad(bindir, env, sample, "extension", "install", str(package), "--yes",
                                     "--expect-home", wrong_home, "--expect-hub", resolved)
        check(code != 0, f"install with --expect-home {wrong_home} exited 0; the write was not refused")
        check(wrong_home in refused and env["PERSONA_HOME"] in refused,
              f"install with a wrong --expect-home: no target mismatch naming both homes in {refused[-800:]}")
        check(FLAG not in features(base) and not entries(base, token),
              "the refused install changed the hub: the flag or the entry exists")
        check(package.exists(), "the refused install consumed the package")
        same = confirm_target(bindir, env, sample, resolved)
        check(same == before_list, f"the refused install changed extension list: {before_list!r} -> {same!r}")
        seen["refused"] = {"expect_home": wrong_home, "exit": code, "list": same}
        print(f"refused: install with --expect-home {wrong_home} exited {code} with the mismatch printed; "
              f"extension list unchanged ({same or 'no extensions listed'}), no flag, no entry", flush=True)
        write(bindir, env, sample, resolved, "extension", "install", str(package), "--yes")
        smoke.wait_for("the sample's route", 60, lambda: call(base, token, "GET", ROUTE)[0] == 200)
        check(FLAG in features(base), "installed: the hub does not announce the flag")
        titles = {}
        for lang, title in TITLES.items():
            got = entries(base, token, lang)
            check(len(got) == 1 and got[0]["placement"] == "nav_page", f"installed: the navigation entry ({lang}): {got}")
            titles[lang] = got[0]["title"]
            check(titles[lang] == title, f"the entry's title in {lang}: {titles[lang]!r}, want {title!r}")
        status, note = call(base, token, "POST", ROUTE, {"text": "kept when off", "mark": "important"})
        check(status == 201 and note.get("id") == "n1" and note.get("mark") == "important", f"storing a note: {status} {note}")
        status, bad = call(base, token, "POST", ROUTE, {"text": "x", "mark": "urgent"})
        check(status == 400, f"a mark outside the set: {status} {bad}, want 400")
        status, view = call(base, token, "POST", f"/api/v1/extensions/{ID}/-/ui/{ID}", {"ui_version": 12})
        check(status == 200 and "kept when off" in json.dumps(view), f"drawing the page: {status} {str(view)[:300]}")
        stored = home / ".persona/extensions/data" / ID / "notes.json"
        check(stored.exists(), f"no stored value at {stored}")
        before = sha(stored)
        seen["installed"] = {"flag": True, "titles": titles, "note": note, "stored_sha256": before}
        print(f"installed: flag announced, entry listed ({', '.join(titles.values())}), note n1 stored and drawn", flush=True)

        # off: entry points and new activity go; what is stored stays.
        confirm_target(bindir, env, work, resolved)
        write(bindir, env, work, resolved, "extension", "disable", ID)
        smoke.wait_for("the flag to go", 30, lambda: FLAG not in features(base))
        check(not entries(base, token), "off: the navigation entry is still listed")
        read, _ = call(base, token, "GET", ROUTE)
        denied, _ = call(base, token, "POST", ROUTE, {"text": "written while off"})
        check(read == 404 and denied == 404, f"off: the route answers {read} (read) and {denied} (write), want 404")
        core_answers(base, token)
        check(sha(stored) == before, "off: the stored file changed")
        kept = json.loads(stored.read_text())["notes"]
        check(len(kept) == 1 and kept[0]["text"] == "kept when off", f"off: the stored notes read {kept}")
        seen["off"] = {"flag": False, "entries": 0, "read": read, "write": denied, "stored_sha256": sha(stored)}
        print("off: flag and entry gone, route 404 for read and write, Core answers, stored file unchanged", flush=True)

        # on again: the same data, nothing rewritten.
        confirm_target(bindir, env, work, resolved)
        write(bindir, env, work, resolved, "extension", "enable", ID)
        smoke.wait_for("the sample's route again", 60, lambda: call(base, token, "GET", ROUTE)[0] == 200)
        status, body = call(base, token, "GET", ROUTE)
        check(body == {"notes": [note]}, f"on again: the route lists {body}, want the note stored before")
        check(FLAG in features(base) and len(entries(base, token)) == 1, "on again: the flag or the entry is missing")
        check(sha(stored) == before, "on again: the stored file changed")
        seen["on_again"] = {"flag": True, "entries": 1, "notes": body["notes"], "stored_sha256": sha(stored)}
        print("on again: flag and entry back, the stored note read back, file unchanged", flush=True)

        # uninstalled: a separate, explicit deletion. Off above kept the file; the default uninstall removes it.
        confirm_target(bindir, env, work, resolved)
        write(bindir, env, work, resolved, "extension", "uninstall", ID)
        smoke.wait_for("the flag to go after uninstall", 30, lambda: FLAG not in features(base))
        check(not entries(base, token), "uninstalled: the navigation entry is still listed")
        status, _ = call(base, token, "GET", ROUTE)
        check(status == 404, f"uninstalled: the route answers {status}, want 404")
        core_answers(base, token)
        check(not stored.parent.exists(), f"uninstalled: the data directory remains at {stored.parent}")
        seen["uninstalled"] = {"flag": False, "entries": 0, "route": status, "data_dir_exists": False}
        print("uninstalled: flag and entry gone, route 404, Core answers, data directory deleted", flush=True)

        smoke.stop_hub(hub, bindir)
        hub = None
        if a.evidence:
            Path(a.evidence).write_text(json.dumps(seen, ensure_ascii=False, indent=2) + "\n")
        print(f"PASS core-only sample: core-only hub, {ID} absent -> wrong --expect-home refused -> installed -> off -> "
              f"on again -> uninstalled; every write bound to the sandbox by --expect-home / --expect-hub, flag, "
              f"navigation entry and route follow the switch, off leaves the stored value unchanged (sha256 {before[:12]}), "
              f"uninstall deletes it, clean shutdown ({time.monotonic() - started:.0f}s)")
        return 0
    except (Failed, TimedOut) as e:
        verdict = "TIMEOUT" if isinstance(e, TimedOut) else "FAIL"
        if log.exists():
            sys.stderr.write(log.read_text()[-6000:])
        print(f"{verdict} core-only sample: {e}")
        return 1
    finally:
        if hub is not None and hub.poll() is None:
            os.killpg(hub.pid, signal.SIGKILL)
            hub.wait(10)
        subprocess.run([smoke.DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)
        if a.keep:
            print(f"kept {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
