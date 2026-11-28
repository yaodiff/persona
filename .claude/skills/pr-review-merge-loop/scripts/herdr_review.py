#!/usr/bin/python3
"""Run one read-only structured reviewer in an isolated Herdr tab.

Usage: herdr_review.py CONFIG -- [CLI_ARG ...]

The watcher owns semantic validation. This helper only transports the fixed
prompt and interactive terminal result, allocates bounded slots, and reclaims
its own tab and scope. It does not start, stop or supervise Herdr itself.
"""
import atexit
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid

_cleanup_tab = None
_cleanup_scope = None
_terminated = False
_config = None


class MalformedTranscriptResult(RuntimeError):
    """A final transcript result that cannot become valid by appending records."""


def socket_request(request, timeout=90, socket_factory=socket.socket):
    """Keep long prompts in Herdr's JSON body, not an operating-system argv."""
    payload = json.dumps(request, separators=(",", ":"), ensure_ascii=False).encode("utf-8") + b"\n"
    if len(payload) > 2_100_000:
        raise RuntimeError("interactive reviewer request exceeds its 2 MiB transport bound")
    connection = socket_factory(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        connection.settimeout(timeout)
        connection.connect(_config["socket_path"])
        connection.sendall(payload)
        response = bytearray()
        while not response.endswith(b"\n"):
            chunk = connection.recv(65536)
            if not chunk:
                raise RuntimeError("Herdr closed its socket before returning a response")
            response.extend(chunk)
            if len(response) > 4_000_000:
                raise RuntimeError("Herdr response exceeds its 4 MB transport bound")
        value = json.loads(response)
    finally:
        connection.close()
    if not isinstance(value, dict):
        raise RuntimeError("Herdr returned an unexpected response")
    if "error" in value:
        detail = value["error"]
        raise RuntimeError(str(detail.get("message") or detail.get("code") or detail))
    return value.get("result", value)


def command(*args, timeout=90):
    """Translate the existing CLI vocabulary to the equivalent socket API."""
    request_id = "pr-review-" + secrets.token_hex(8)
    if args[0] == "agent" and args[1] == "prompt":
        pane, text = args[2:4]
        wait = {"timeout_ms": int(args[args.index("--timeout") + 1]),
                "until": [args[index + 1] for index, arg in enumerate(args) if arg == "--until"]}
        request = {"method": "agent.prompt", "params": {"target": pane, "text": text, "wait": wait}}
    elif args[0] == "pane" and args[1] == "run":
        # pane.run accepts no input and returns no JSON on successful shell launch.
        subprocess.run(
            [_config["herdr_command"], "--session", _config["session"], *args],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, timeout=timeout, check=True)
        return {}
    else:
        params = {}
        index = 2
        while index < len(args):
            arg = args[index]
            if arg.startswith("--"):
                if arg == "--no-focus":
                    params["focus"] = False
                else:
                    value = args[index + 1]
                    if arg == "--source":
                        value = value.replace("-", "_")
                    params[arg[2:].replace("-", "_").replace("workspace", "workspace_id")] = (
                        True if arg == "--raw" else int(value) if arg == "--lines" else value)
                    index += 1
            elif not params:
                params["workspace_id" if args[:2] == ("tab", "create") else
                       "tab_id" if args[:2] == ("tab", "close") else
                       "pane_id" if args[0] == "pane" else "target"] = arg
            else:
                params.setdefault("keys", []).append(arg)
            index += 1
        request = {"method": args[0] + "." + args[1].replace("-", "_"), "params": params}
    return socket_request({"id": request_id, **request}, timeout=timeout)


def interactive_command(launcher, flags):
    """Validate the watcher request; the private launcher owns CLI policy.

    Herdr should show the same simple interactive command as other scoped
    agents. The configured launcher is trusted maintainer configuration and
    applies the maintainer's interactive permission policy, Read-only tools and
    resource fallback. A new session is created per invocation.
    """
    options_with_values = {"--json-schema", "--tools", "--setting-sources", "--permission-mode"}
    schema = None
    index = 0
    while index < len(flags):
        flag = flags[index]
        if flag == "-p":
            index += 1
            continue
        if flag == "--output-format":
            if index + 1 >= len(flags) or flags[index + 1] != "json":
                raise RuntimeError("interactive reviewer requires JSON output")
            index += 2
            continue
        if flag in options_with_values:
            if index + 1 >= len(flags):
                raise RuntimeError("missing reviewer option argument")
            if flag == "--json-schema":
                schema = flags[index + 1]
            index += 2
            continue
        if flag == "--add-dir":
            if index + 1 >= len(flags):
                raise RuntimeError("missing reviewer add-dir argument")
            index += 2
            continue
        if flag in {"--no-session-persistence", "--safe-mode", "--strict-mcp-config",
                    "--disable-slash-commands"}:
            index += 1
            continue
        raise RuntimeError("unsupported interactive reviewer flag: " + flag)
    if schema is None:
        raise RuntimeError("interactive reviewer requires the watcher JSON schema")
    return launcher, schema


def scoped_reviewer(slot, unit, properties):
    """Show only systemd's resource options and the interactive launcher.

    On cgroup v1 the configured launcher must retain affinity and rlimits;
    scope properties alone do not bound the memory and CPU controllers.
    """
    if not re.fullmatch(r"pr-review-r[0-9]{2}-[0-9]+-[a-f0-9]{8}\.scope", unit):
        raise RuntimeError("reviewer scope name is not owned by this helper")
    command = ["/usr/bin/systemd-run", "--user", "--scope", "--unit=" + unit[:-6], "--collect"]
    for key, value in sorted(properties.items()):
        if key not in {"CPUQuota", "CPUWeight", "MemoryHigh", "MemoryMax", "TasksMax"}:
            raise RuntimeError("unsupported reviewer scope property")
        if key == "CPUQuota":
            if not isinstance(value, str) or not re.fullmatch(r"[0-9]+%", value) or int(value[:-1]) <= 0:
                raise RuntimeError("CPUQuota must be a positive percentage")
        elif key in {"CPUWeight", "TasksMax"}:
            if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
                raise RuntimeError("reviewer CPU weight/task count must be positive integers")
            value = str(value)
        elif not isinstance(value, str) or not re.fullmatch(r"[0-9]+[KMG]", value) or int(value[:-1]) <= 0:
            raise RuntimeError("reviewer scope memory values must use K, M or G units")
        command.extend(["-p", key + "=" + value])
    return [*command, "zsh", "-lic"]


def reviewer_command(slot, unit, properties, launcher):
    return scoped_reviewer(slot, unit, properties) + [launcher]


def wait_session_id(path, pane, unit, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = path.read_text(encoding="utf-8").strip()
            return str(uuid.UUID(value))
        except (OSError, ValueError):
            pass
        if not scope_running(unit):
            screen = terminal_text(pane, "recent-unwrapped", 120)
            raise RuntimeError("interactive reviewer exited before creating its session: "
                               + json.dumps(screen, ensure_ascii=False)[-1200:])
        time.sleep(.2)
    raise RuntimeError("interactive reviewer did not publish its session ID")


def scope_command(action, unit, timeout=20):
    if not unit:
        return
    subprocess.run(["/usr/bin/systemctl", "--user", action, unit],
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, timeout=timeout, check=False)


def cleanup_scope():
    # --collect reaps a normally completed scope. stop is used only to abort
    # a live scope owned by this helper on error or termination.
    scope_command("stop", _cleanup_scope, timeout=10)


def cleanup_tab():
    global _cleanup_tab
    tab = _cleanup_tab
    _cleanup_tab = None
    if tab:
        try:
            command("tab", "close", tab, timeout=10)
        except (RuntimeError, subprocess.TimeoutExpired):
            pass


def terminate_signal(signum, frame):
    global _terminated
    _terminated = True
    cleanup_tab()
    cleanup_scope()
    raise SystemExit(128 + signum)


def scope_state(unit):
    result = subprocess.run(
        ["/usr/bin/systemctl", "--user", "show", unit, "--property=LoadState,ActiveState"],
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, timeout=10, check=False)
    values = dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
    return values.get("LoadState"), values.get("ActiveState")


def wait_scope_start(unit, deadline):
    """Scope registration races pane.run; absence is not yet a process exit."""
    while time.monotonic() < deadline:
        load, active = scope_state(unit)
        if active == "active":
            return True
        if active == "failed" or load == "loaded" and active == "inactive":
            raise RuntimeError("interactive reviewer scope exited before startup")
        time.sleep(.2)
    raise TimeoutError("interactive reviewer scope did not start")


def scope_running(unit):
    load, active = scope_state(unit)
    return load != "not-found" and active not in ("inactive", "failed")


def wait_agent(pane, unit, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not scope_running(unit):
            screen = terminal_text(pane, "visible", 45)
            raise RuntimeError("interactive reviewer exited before becoming ready: "
                               + json.dumps(screen, ensure_ascii=False)[-1200:])
        agents = command("agent", "list", timeout=20).get("agents", [])
        agent = next((item for item in agents if item.get("pane_id") == pane), None)
        if agent is not None:
            if agent.get("agent") != "claude":
                raise RuntimeError("unexpected reviewer executable")
            screen = terminal_text(pane, "visible", 45)
            warning = _config.get("startup_warning", "")
            acceptance = _config.get("startup_acceptance", "")
            if warning and acceptance and warning in screen and acceptance in screen:
                command("pane", "send-keys", pane, "down", "enter", timeout=20)
                time.sleep(2)
                continue
            if "Yes, I trust this folder" in screen and "Quick safety check" in screen:
                command("pane", "send-keys", pane, "down", "enter", timeout=20)
                time.sleep(2)
                continue
            if agent.get("agent_status") == "blocked":
                screen = terminal_text(pane, "visible", 45)
                if not warning or not acceptance or warning not in screen or acceptance not in screen:
                    raise RuntimeError("reviewer requested an unexpected interaction")
                command("pane", "send-keys", pane, "down", "enter", timeout=20)
                time.sleep(2)
                continue
            if agent.get("agent_status") in ("idle", "working"):
                return
            if agent.get("agent_status") == "done":
                time.sleep(1)
                continue
            raise RuntimeError("reviewer startup state is not usable")
        time.sleep(1)
    raise RuntimeError("reviewer did not become ready")


def forwarded_flags(arguments, forbidden):
    output = []
    options_with_values = {"--output-format", "--json-schema", "--tools", "--setting-sources", "--permission-mode"}
    index = 0
    while index < len(arguments):
        flag = arguments[index]
        if flag in forbidden:
            raise RuntimeError("reviewer cannot override " + flag)
        if flag in options_with_values:
            if index + 1 >= len(arguments):
                raise RuntimeError("missing reviewer option argument")
            output.extend(arguments[index:index + 2])
            index += 2
            continue
        if flag == "--add-dir":
            if index + 1 >= len(arguments):
                raise RuntimeError("missing reviewer add-dir argument")
            output.extend(arguments[index:index + 2])
            index += 2
            continue
        if flag not in output:
            output.append(flag)
        index += 1
    return output


def interactive_prompt(prompt, schema, marker):
    return (prompt + "\n\nComplete this review now. Your final response must contain no prose, "
            "markdown, tool calls or further turns. Return exactly one JSON object matching this "
            "schema, with a unique transport marker on the line before and after it:\n"
            + marker + "\n<JSON object matching the schema below>\n" + marker
            + "\nSchema:\n" + schema)


def transcript_path(worktree, session_id, home=None):
    """Resolve one launcher's session, never another conversation's history."""
    try:
        session_id = str(uuid.UUID(session_id))
    except (ValueError, TypeError, AttributeError) as error:
        raise RuntimeError("reviewer session ID is invalid") from error
    cwd = Path(worktree).resolve()
    if not cwd.is_absolute() or not cwd.is_dir():
        raise RuntimeError("reviewer worktree is not an absolute directory")
    config = Path(home or os.environ.get("CLAUDE_CONFIG_DIR") or (Path.home() / ".claude")).expanduser()
    if not config.is_absolute() or not config.is_dir():
        raise RuntimeError("reviewer transcript configuration is unavailable")
    project = re.sub(r"[^A-Za-z0-9]", "-", str(cwd))
    project = "-" + project.lstrip("-")
    return config / "projects" / project / (session_id + ".jsonl")


def transcript_result(path, marker, schema):
    """Read the current session's exact final message, not terminal scrollback."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise RuntimeError("interactive reviewer transcript is unavailable") from error
    marked = []
    for line in lines:
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("type") != "assistant" or record.get("sessionId") != path.stem:
            continue
        message = record.get("message")
        if not isinstance(message, dict):
            continue
        content = message.get("content")
        if not isinstance(content, list):
            continue
        text = "".join(block.get("text", "") for block in content
                       if isinstance(block, dict) and block.get("type") == "text")
        if marker not in text:
            continue
        marked.append((text, message.get("stop_reason")))
    if len(marked) > 1:
        raise MalformedTranscriptResult("interactive reviewer transcript result is not unique")
    if not marked:
        raise RuntimeError("interactive reviewer transcript has no final result yet")
    text, stop_reason = marked[0]
    if stop_reason != "end_turn":
        # A tool-use message may be split across transcript writes. Wait for its
        # final assistant message instead of rejecting a partial marked text.
        raise RuntimeError("interactive reviewer transcript result is not final")
    matches = list(re.finditer(re.escape(marker), text))
    if len(matches) != 2:
        raise MalformedTranscriptResult("interactive reviewer transcript markers are not unique")
    body = text[matches[0].end():matches[1].start()].strip()
    if not body or text[:matches[0].start()].strip() or text[matches[1].end():].strip():
        raise MalformedTranscriptResult("interactive reviewer result must contain only its marked JSON")
    try:
        value = json.loads(body)
    except json.JSONDecodeError as error:
        raise MalformedTranscriptResult("interactive reviewer returned invalid marked JSON") from error
    try:
        import jsonschema
        jsonschema.validate(value, json.loads(schema))
    except ImportError as error:
        raise RuntimeError("interactive reviewer requires the jsonschema validator") from error
    except (ValueError, jsonschema.ValidationError) as error:
        raise MalformedTranscriptResult("interactive reviewer result does not match the watcher schema") from error
    return value


def wait_transcript(path, marker, schema, pane, unit, timeout, accepted_stall=False):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if path.is_file():
            try:
                return transcript_result(path, marker, schema)
            except MalformedTranscriptResult:
                # A final malformed answer occupies no useful review work. Fail
                # now so the owned scope closes and another PR can use the slot.
                raise
            except RuntimeError:
                # Missing or unfinished transcript records are not review evidence;
                # keep waiting for this same invocation's final assistant message.
                pass
        agents = command("agent", "list", timeout=20).get("agents", [])
        agent = next((item for item in agents if item.get("pane_id") == pane), None)
        blocked = agent and agent.get("agent_status") == "blocked"
        if blocked and not accepted_stall:
            raise RuntimeError("interactive reviewer requested an unexpected interaction")
        accepted_stall = False
        if not scope_running(unit):
            if path.is_file():
                return transcript_result(path, marker, schema)
            raise RuntimeError("interactive reviewer exited before returning its result")
        time.sleep(1)
    raise TimeoutError("interactive reviewer did not return a complete marked result")


def terminal_text(pane, source="recent-unwrapped", lines=None):
    result = command("pane", "read", pane, "--source", source,
                     "--lines", str(lines or int(_config.get("result_lines", 20000))),
                     "--format", "text", timeout=60)
    if result.get("type") != "pane_read" or not isinstance(result.get("read"), dict) \
            or not isinstance(result["read"].get("text"), str):
        raise RuntimeError("Herdr returned an unexpected terminal response")
    return result["read"]["text"]


def wait_result(pane, unit, marker, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        screen = terminal_text(pane)
        if screen.rsplit("●", 1)[-1].count(marker) >= 2:
            agents = command("agent", "list", timeout=20).get("agents", [])
            agent = next((item for item in agents if item.get("pane_id") == pane), None)
            if agent and agent.get("agent_status") in ("idle", "done"):
                return screen
            if agent and agent.get("agent_status") == "blocked":
                raise RuntimeError("interactive reviewer requested an unexpected interaction")
        if not scope_running(unit):
            raise RuntimeError("interactive reviewer exited before returning its result")
        time.sleep(1)
    raise TimeoutError("interactive reviewer did not return a complete marked result")


def terminal_result(screen, marker, schema):
    """Accept only a marked assistant response, not the prompt or a diff's JSON."""
    if marker not in screen:
        raise RuntimeError("interactive reviewer returned no marked result")
    tail = screen.rsplit("●", 1)[-1]
    matches = list(re.finditer(re.escape(marker), tail))
    if len(matches) != 2:
        raise RuntimeError("interactive reviewer result markers are not unique: "
                           + screen[-1500:])
    text = tail[matches[0].end():matches[1].start()]
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    text = "\n".join(lines)
    try:
        value = json.loads(text)
    except json.JSONDecodeError as error:
        raise RuntimeError("interactive reviewer returned invalid marked JSON") from error
    try:
        import jsonschema
        jsonschema.validate(value, json.loads(schema))
    except ImportError as error:
        raise RuntimeError("interactive reviewer requires the jsonschema validator") from error
    except (ValueError, jsonschema.ValidationError) as error:
        raise RuntimeError("interactive reviewer result does not match the watcher schema") from error
    return value


def agent_state(pane):
    agents = command("agent", "list", timeout=20).get("agents", [])
    return next((item for item in agents if item.get("pane_id") == pane), None)


def prompt_started(pane, transcript, marker, schema, unit):
    """Allow a delayed lifecycle update, never a resubmission or an idle pass."""
    grace = int(_config.get("startup_grace_seconds", 30))
    if not 1 <= grace <= 120:
        raise RuntimeError("reviewer startup grace must be between 1 and 120 seconds")
    deadline = time.monotonic() + grace
    while True:
        if not scope_running(unit):
            raise RuntimeError("interactive reviewer scope exited during prompt startup")
        agent = agent_state(pane)
        status = agent and agent.get("agent_status")
        if status in ("working", "blocked"):
            return True
        if status in ("idle", "done"):
            try:
                transcript_result(transcript, marker, schema)
            except RuntimeError:
                pass
            else:
                return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(min(1, max(0, deadline - time.monotonic())))


def submit_prompt(pane, prompt, transcript, marker, schema, unit):
    """Keep an accepted prompt on its original pane across observation stalls."""
    try:
        command("agent", "prompt", pane, prompt, "--wait", "--until", "working",
                "--until", "blocked",
                "--timeout", str(int(_config.get("lifetime_seconds", 7200)) * 1000),
                timeout=int(_config.get("prompt_timeout_seconds", 120)))
    except RuntimeError as error:
        if "agent prompt produced no observed state change" not in str(error):
            raise
        if not prompt_started(pane, transcript, marker, schema, unit):
            raise RuntimeError("interactive reviewer prompt did not start: " + str(error)) from error
        return True
    return False


def run(slot, flags):
    global _cleanup_tab, _cleanup_scope
    root = Path(_config["runtime_directory"]).expanduser()
    lease_path = root / ("slot-%02d.json" % slot)
    if lease_path.exists():
        try:
            old = json.loads(lease_path.read_text(encoding="utf-8"))
            pid = old.get("pid")
            if isinstance(pid, int) and pid > 0:
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    tab = old.get("tab")
                    if isinstance(tab, str) and tab.startswith("w") and ":t" in tab:
                        command("tab", "close", tab, timeout=20)
        except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired):
            pass
    name = "pr-r%02d" % slot
    unit = "pr-review-r%02d-%d-%s.scope" % (slot, os.getpid(), secrets.token_hex(4))
    marker = "PR_REVIEW_RESULT_" + secrets.token_hex(12)
    launch, schema = interactive_command(_config["reviewer_launcher"], flags)
    prompt = interactive_prompt(sys.stdin.read(), schema, marker)
    slot_id = "%02d" % slot
    session_path = root / ("slot-%02d-session" % slot)
    transcript = None
    with tempfile.TemporaryDirectory(prefix="slot-%02d-" % slot, dir=root):
        with lease_path.open("w", encoding="utf-8") as lease:
            json.dump({"pid": os.getpid(), "slot": slot}, lease)
            lease.flush()
            os.fsync(lease.fileno())
            try:
                created = command("tab", "create", "--workspace", _config.get("workspace", "w1"),
                                  "--cwd", os.getcwd(), "--label", name, "--no-focus", timeout=30)
                tab = created["tab"]["tab_id"]
                pane = created["root_pane"]["pane_id"]
                _cleanup_tab = tab
                lease.seek(0)
                lease.truncate()
                json.dump({"pid": os.getpid(), "slot": slot, "tab": tab, "pane": pane,
                           "scope": unit}, lease)
                lease.flush()
                os.fsync(lease.fileno())
                bounded = reviewer_command(slot, unit, _config.get("scope_properties", {}), launch)
                environment = _config.get("launcher_environment", {})
                if not isinstance(environment, dict) or any(
                        not isinstance(key, str) or not isinstance(value, str)
                        for key, value in environment.items()):
                    raise RuntimeError("launcher_environment must contain string values")
                environment["PR_REVIEW_SLOT"] = slot_id
                environment["PR_REVIEW_SESSION_PATH"] = str(session_path)
                environment["PR_REVIEW_SCOPE"] = unit
                for key, value in environment.items():
                    bounded[2:2] = ["--setenv=" + key + "=" + value]
                _cleanup_scope = unit
                session_path.unlink(missing_ok=True)
                command("pane", "run", pane, " ".join(shlex.quote(arg) for arg in bounded), timeout=30)
                if not wait_scope_start(unit, time.monotonic() + 180):
                    screen = terminal_text(pane, "visible", 45)
                    raise RuntimeError("interactive reviewer exited before becoming ready: "
                                       + json.dumps(screen, ensure_ascii=False)[-1200:])
                session_id = wait_session_id(session_path, pane, unit)
                transcript = transcript_path(os.getcwd(), session_id)
                wait_agent(pane, unit)
                accepted_stall = submit_prompt(pane, prompt, transcript, marker, schema, unit)
                value = wait_transcript(transcript, marker, schema, pane, unit,
                                        int(_config.get("lifetime_seconds", 7200)), accepted_stall)
                result = {"is_error": False, "subtype": "success", "result": "",
                          "structured_output": value, "permission_denials": []}
                sys.stdout.write(json.dumps(result, separators=(",", ":")))
            finally:
                cleanup_tab()
                cleanup_scope()


def acquire_slot(root, slots, flags):
    """Wait for a real free slot rather than failing a queued review.

    The lock itself is the bound: no extra interactive agent is created while
    waiting. Each request still has the watcher's overall review timeout.
    """
    while True:
        for slot in range(slots):
            lock = (root / ("slot-%02d.lock" % slot)).open("a+")
            try:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError:
                    lock.close()
                    continue
                try:
                    return run(slot, flags)
                finally:
                    fcntl.flock(lock, fcntl.LOCK_UN)
                    lock.close()
            except BaseException:
                lock.close()
                raise
        time.sleep(0.1)


def main():
    global _config
    signal.signal(signal.SIGTERM, terminate_signal)
    signal.signal(signal.SIGINT, terminate_signal)
    atexit.register(cleanup_tab)
    if len(sys.argv) < 4 or sys.argv[2] != "--":
        raise SystemExit("usage: herdr_review.py CONFIG -- [CLI_ARG ...]")
    _config = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
    for key in ("herdr_command", "session", "runtime_directory", "reviewer_launcher"):
        if not isinstance(_config.get(key), str) or not _config[key]:
            raise RuntimeError("missing reviewer configuration: " + key)
    socket_path = _config.get("socket_path")
    if not isinstance(socket_path, str) or not socket_path:
        raise RuntimeError("missing reviewer configuration: socket_path")
    if not Path(socket_path).is_absolute() or not Path(socket_path).is_socket():
        raise RuntimeError("reviewer socket_path must name an existing Herdr socket")
    root = Path(_config["runtime_directory"]).expanduser()
    if not root.is_absolute():
        raise RuntimeError("runtime_directory must be absolute")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    flags = forwarded_flags(sys.argv[3:], set(_config.get("forbidden_flags", ["--model"])))
    required_flags = {
        "-p": None, "--output-format": "json", "--tools": "Read",
        "--permission-mode": "plan", "--json-schema": None,
    }
    for flag, value in required_flags.items():
        if flag not in flags:
            raise RuntimeError("reviewer requires " + flag)
        if value is not None:
            index = flags.index(flag)
            if index + 1 >= len(flags) or flags[index + 1] != value:
                raise RuntimeError("reviewer requires %s %s" % (flag, value))
    slots = int(_config.get("slots", 1))
    if not 1 <= slots <= 64:
        raise RuntimeError("reviewer slots must be between 1 and 64")
    try:
        acquire_slot(root, slots, flags)
    finally:
        cleanup_tab()
        cleanup_scope()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
