#!/usr/bin/env python3
"""Run Android's production hub client against real core-only and full hubs (#16786).

    python3 scripts/ci/android-core-only-hub.py [--keep] [--evidence FILE]

Each phase builds the server programs with the requested edition's tags, starts a disposable Postgres 18 container and
a loopback-only persona-hub in its own temporary HOME and PERSONA_HOME, then runs CoreOnlyHubLiveTest in the default
Android app build. The test pairs through HubPairingClient, receives a device credential, opens the production WebSocket
connection, reads core APIs and applies the real hello to ModuleRegistry. The core-only phase expects every carried
module and Recording/Lark/Mail off; the full control expects them on. No emulator, private hub or private configuration
is used. Prints one PASS / FAIL / TIMEOUT verdict. Needs Go, Docker (DOCKER names another client), JDK 21 and the
Android SDK used by Gradle.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("core_only_smoke", ROOT / "scripts/ci/core-only-smoke.py")
smoke = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(smoke)
Failed, TimedOut, check = smoke.Failed, smoke.TimedOut, smoke.check


def start_hub(bindir, home, work, log):
    """Start only the sandbox hub, with PERSONA_HOME explicit even in the clean environment."""
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(home), "TMPDIR": str(work),
           "PERSONA_HOME": str(home / ".persona"), "LANG": "C.UTF-8", "PERSONA_LOCAL_NODE": "0",
           "PERSONA_HUB_EDGE": "0"}
    with open(log, "a") as out:
        return subprocess.Popen(["env", "-i"] + [f"{key}={value}" for key, value in env.items()] +
                                [str(bindir / "persona-hub"), "run"], cwd=work, env={"PATH": env["PATH"]},
                                stdout=out, stderr=subprocess.STDOUT, start_new_session=True)


def run_gradle(android, fixture, home, timeout):
    """Run only the opt-in live test, against this phase's explicit sandbox and never the caller's hub."""
    workers = os.environ.get("ANDROID_CI_MAX_WORKERS", "2")
    command = [str(android / "gradlew"), "--no-daemon", "--no-watch-fs", "--console=plain", "--stacktrace",
               f"--max-workers={workers}", "-PpersonaApplicationId=com.example.persona",
               "-PpersonaGoogleOAuthScheme=persona.oauth.unset", f"-PpersonaRepoRoot={ROOT}",
               "-PpersonaBuildCommit=unknown", ":app:testDebugUnitTest", "--tests",
               "persona.android.CoreOnlyHubLiveTest", "--rerun"]
    env = dict(os.environ, PERSONA_HOME=str(home / ".persona"), PERSONA_ANDROID_CORE_HUB_FIXTURE=str(fixture))
    try:
        result = subprocess.run(command, cwd=android, env=env, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        raise TimedOut(f"Android live test exceeded {timeout}s") from error
    check(result.returncode == 0, f"Android CoreOnlyHubLiveTest exited {result.returncode}")


def validate_proof(proof, edition, run_id):
    """Bind the Android result to this run and make both the off and on assertions non-vacuous."""
    full = edition == "full"
    check(proof.get("schema") == 1 and proof.get("run_id") == run_id and proof.get("edition") == edition,
          "the Android proof does not belong to this hub phase")
    check(proof.get("device_credential_issued") is True and proof.get("current_device_confirmed") is True,
          f"{edition}: Android did not confirm an issued device credential")
    check(proof.get("modules_marker") is True, f"{edition}: the real hello lacked the modules marker")
    reads = proof.get("core_reads") or {}
    check(reads.get("installed_managers", 0) >= 1 and reads.get("sessions", -1) >= 0 and
          reads.get("paired_devices", 0) >= 1, f"{edition}: authenticated core reads were not exercised: {reads}")
    check(reads.get("inbox_items", -1) == 0 and reads.get("todos", -1) == 0,
          f"{edition}: Inbox and To-dos must both be well-formed empty lists: {reads}")
    carried = proof.get("carried_modules") or []
    active = proof.get("active_modules") or []
    check(carried, f"{edition}: the app carried no modules, so the check would be vacuous")
    check(active == (carried if full else []), f"{edition}: carried {carried}, active {active}")
    expected_hub = {module: full for module in ("recording", "lark", "mail")}
    check(proof.get("hub_modules") == expected_hub,
          f"{edition}: hub modules {proof.get('hub_modules')}, want {expected_hub}")
    pages = proof.get("unregistered_module_debug_pages", -1)
    check(pages > 0 if not full else pages == 0, f"{edition}: unregistered module debug pages {pages}")
    flags = set(proof.get("module_flags") or [])
    check("modules" in flags, f"{edition}: the proof lacks the modules marker")
    for module in set(carried) | set(expected_hub):
        check((f"module.{module}" in flags) == full, f"{edition}: module.{module} has the wrong live flag")


def run_phase(parent, android, edition, run_id, keep, gradle_timeout):
    """Build, start and check one edition; always stop only this phase's hub and Postgres container."""
    work = parent / edition
    work.mkdir()
    bindir, home, log = work / "bin", work / "home", work / "hub.log"
    container = f"persona-android-{run_id}-{edition}"
    hub = None
    try:
        tags = "" if edition == "full" else "persona_core_only"
        smoke.build(tags, bindir)
        print(f"{edition}: built server programs ({tags or 'default tags'})", flush=True)
        database = smoke.postgres(container)
        port, pairing_token = smoke.free_port(), secrets.token_hex(24)
        smoke.write_hub_json(home, port, pairing_token, database)
        base = f"http://127.0.0.1:{port}"
        listen = f"127.0.0.1:{port}"
        print(f"{edition}: PERSONA_HOME={home / '.persona'}; sandbox hub will listen on {base}", flush=True)
        hub = start_hub(bindir, home, work, log)
        loaded = smoke.wait_ready(hub, base, log, 0)
        check((len(loaded) > 0) == (edition == "full"), f"{edition}: manager modules loaded: {loaded}")
        module_ids = smoke.module_ids()
        check((set(loaded) == set(module_ids)) if edition == "full" else not loaded,
              f"{edition}: manager modules differ from the build manifest: {loaded}")

        # A read-only health check proves the generated fixture names this sandbox before Android makes its auth write.
        status, body = smoke.http(base + "/api/v1/health")
        check(status == 200, f"{edition}: health returned {status}")
        health = json.loads(body)
        hub_id = health.get("hub_id")
        check(health.get("ok") is True and isinstance(hub_id, str) and hub_id,
              f"{edition}: the sandbox health response has no hub id")
        config = json.loads((home / ".persona/hub.json").read_text())
        check(config["listen"] == listen and config["public_url"] == base,
              f"{edition}: the sandbox configuration points somewhere else")
        features = set(health.get("features") or [])
        check("modules" in features, f"{edition}: health lacks the modules marker")
        for module in module_ids:
            check((f"module.{module}" in features) == (edition == "full"),
                  f"{edition}: health has the wrong flag for module.{module}")
        print(f"{edition}: confirmed disposable loopback hub {base}, hub_id={hub_id}, "
              f"modules loaded: {', '.join(loaded) or 'none'}; Android pairing may proceed", flush=True)

        fixture = work / "fixture.json"
        fixture.write_text(json.dumps({"schema": 1, "run_id": run_id, "edition": edition, "base_url": base,
                                      "hub_id": hub_id, "pairing_token": pairing_token, "confirmed_loopback": True}))
        fixture.chmod(0o600)
        run_gradle(android, fixture, home, gradle_timeout)
        proof_path = work / "android-proof.json"
        check(proof_path.is_file(), f"{edition}: Android wrote no proof")
        proof = json.loads(proof_path.read_text())
        validate_proof(proof, edition, run_id)
        smoke.stop_hub(hub, bindir)
        hub = None
        print(f"{edition}: Android proof {json.dumps(proof, sort_keys=True)}", flush=True)
        if keep:
            print(f"{edition}: kept sandbox {work}", flush=True)
        return proof
    except Exception:
        if log.exists():
            sys.stderr.write(log.read_text()[-6000:])
        raise
    finally:
        if hub is not None and hub.poll() is None:
            os.killpg(hub.pid, signal.SIGKILL)
            hub.wait(10)
        # The name contains this run's random id; never enumerate or remove any other container.
        subprocess.run([smoke.DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--android-dir", type=Path, default=ROOT / "android", help="Android checkout to test")
    parser.add_argument("--evidence", type=Path, help="write the two sanitized Android proofs as JSON")
    parser.add_argument("--keep", action="store_true", help="keep temporary homes and logs (their credentials are synthetic)")
    parser.add_argument("--gradle-timeout", type=int, default=900, metavar="SECONDS",
                        help="timeout for each Android live-test invocation (default 900)")
    args = parser.parse_args()
    if args.gradle_timeout < 1:
        parser.error("--gradle-timeout must be positive")
    android = args.android_dir.resolve()
    work = Path(tempfile.mkdtemp(prefix="persona-android-hub-editions."))
    run_id = secrets.token_hex(6)
    started = time.monotonic()
    verdict = "FAIL"
    detail = "not run"
    try:
        check((android / "gradlew").is_file(), f"no Android Gradle wrapper at {android}")
        evidence = {}
        for edition in ("core-only", "full"):
            evidence[edition] = run_phase(work, android, edition, run_id, args.keep, args.gradle_timeout)
        if args.evidence:
            args.evidence.parent.mkdir(parents=True, exist_ok=True)
            args.evidence.write_text(json.dumps({"schema": 1, "run_id": run_id, "phases": evidence}, indent=2) + "\n")
        verdict = "PASS"
        detail = (f"Android paired with issued device credentials on both hubs; core APIs read; real hello applied; "
                  f"core-only modules off and debug pages unregistered, full-hub control on ({time.monotonic() - started:.0f}s)")
        return_code = 0
    except (Failed, TimedOut) as error:
        verdict = "TIMEOUT" if isinstance(error, TimedOut) else "FAIL"
        detail = str(error)
        return_code = 1
    except Exception as error:
        verdict = "FAIL"
        detail = f"{type(error).__name__}: {error}"
        return_code = 1
    finally:
        if not args.keep:
            shutil.rmtree(work, ignore_errors=True)
    print(f"{verdict} Android hub editions: {detail}")
    return return_code


if __name__ == "__main__":
    sys.exit(main())
