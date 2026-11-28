"""Isolated Git, process and history fixtures for Web development updates."""
import contextlib
import errno
import fcntl
import gzip
import hashlib
import importlib.util
import io
import json
import os
import signal
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("webdev", ROOT / "server/internal/update/webdev.py")
webdev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(webdev)


class DevelopmentFixtures(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="persona-web-dev-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "tools"
        self.bin.mkdir()
        self.env = patch.dict(os.environ, {"PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                                          "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        self.env.start()
        self.addCleanup(self.env.stop)

    def tool(self, name, source):
        path = self.bin / name
        path.write_text("#!" + sys.executable + "\n" + source)
        path.chmod(0o755)
        return str(path)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True, stderr=subprocess.DEVNULL).strip()

    def test_project_main_and_fixed_compile_candidate(self):
        self.repo = self.root / "remote"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.com")
        for relative, content in {"server/build-tags.txt":"public", "web/package.json":"{}"}.items():
            file = self.repo / relative
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(content)
        self.git("add", ".")
        self.git("commit", "-m", "First fixture")
        (self.repo / "earlier").write_text("previous history")
        self.git("add", ".")
        self.git("commit", "-m", "Selected fixture")
        repository = patch.object(webdev, "REPOSITORY", self.repo.as_uri())
        repository.start()
        self.addCleanup(repository.stop)
        resolve_work = self.root/"resolve"
        resolve_work.mkdir()
        selected = webdev.resolve({"work": str(resolve_work)})
        self.assertEqual(selected["branch"], "main")
        self.assertEqual(selected["updated_at"], self.git("show", "-s", "--format=%cI", selected["commit"]))
        (self.repo / "newer").write_text("remote advanced after checking")
        self.git("add", ".")
        self.git("commit", "-m", "Second fixture")
        trace = self.root / "compile.jsonl"
        os.environ["FIXTURE_TRACE"] = str(trace)
        self.addCleanup(os.environ.pop, "FIXTURE_TRACE", None)
        fixture = '''import json, os, pathlib, subprocess, sys
args = sys.argv[1:]
with open(os.environ['FIXTURE_TRACE'], 'a') as log: log.write(json.dumps([pathlib.Path(sys.argv[0]).name, args])+'\\n')
if pathlib.Path(sys.argv[0]).name == 'npm' and args == ['run', 'build:development'] and pathlib.Path.cwd().name == 'web':
    pathlib.Path('dist').mkdir(); pathlib.Path('dist/index.html').write_text('fixture web')
if pathlib.Path(sys.argv[0]).name == 'go' and args[0] == 'build':
    pathlib.Path(args[args.index('-o')+1]).write_text(subprocess.check_output(['git','rev-parse','HEAD'], text=True))
if args == ['tool', 'dist', 'list']:
    print('linux/amd64', 'linux/riscv64', 'darwin/arm64', 'windows/amd64', sep=chr(10))
'''
        self.tool("go", fixture)
        self.tool("npm", fixture)
        work = self.root / "build"
        work.mkdir()
        events = []
        with patch.object(webdev, "HISTORY_LIMIT", 1):
            built = webdev.build({"platforms":["linux-amd64", "linux-riscv64", "darwin-arm64", "windows-amd64"], "work":str(work), "candidate":selected,
                                  "version":"web-dev-fixture", "build":202611130001, "platform":"linux-amd64"},
                                 webdev.Progress("building", events.append))
        self.assertEqual(list(dict.fromkeys(event["step"] for event in events)),
                         ["fetch", "npm_install", "web_build", "runtime_packages", "go_build", "manifest"])
        targets = [event for event in events if event.get("item_label")]
        self.assertEqual(targets[0]["item_label"], "personad (darwin-arm64)")
        self.assertEqual((targets[0]["item_done"], targets[0]["item_total"]), (0, 13))
        self.assertNotIn(str(self.root), json.dumps(events))
        self.assertEqual(built["ancestors"], [selected["commit"]])
        self.assertEqual(subprocess.check_output(["git", "-C", str(work/"source"), "rev-list", "--count", "HEAD"], text=True).strip(), "1")
        self.assertEqual((work / "personad-linux-amd64").read_text().strip(), selected["commit"])
        self.assertNotEqual(selected["commit"], self.git("rev-parse", "HEAD"))
        self.assertEqual({p.name for p in Path(built["artifacts"]).glob("*.gz")}, {
            "personad-linux-amd64.gz", "personad-darwin-arm64.gz", "personad-windows-amd64.exe.gz",
            "persona-node-linux-amd64.gz", "persona-node-darwin-arm64.gz", "persona-node-windows-amd64.exe.gz",
            "persona-manager-linux-amd64.gz", "persona-manager-darwin-arm64.gz", "persona-manager-windows-amd64.exe.gz",
            "personad-linux-riscv64.gz", "persona-node-linux-riscv64.gz", "persona-manager-linux-riscv64.gz",
            "persona-hub-linux-amd64.gz"})
        artifacts = Path(built["artifacts"])
        manifest = json.loads((artifacts / "manifest.json").read_text())
        self.assertEqual((manifest["commit"], manifest["version"], manifest["build"]),
                         (selected["commit"], "web-dev-fixture", 202611130001))
        for files in (manifest["files"], *manifest["nodes"].values(), *manifest["programs"].values()):
            for entry in files.values():
                archive = (artifacts / entry["name"]).read_bytes()
                self.assertEqual(entry["size"], len(archive))
                self.assertEqual(entry["sha256"], hashlib.sha256(archive).hexdigest())
                self.assertEqual(entry["binary_sha256"], hashlib.sha256(gzip.decompress(archive)).hexdigest())
        commands = [json.loads(line) for line in trace.read_text().splitlines()]
        self.assertFalse(any("test" in args or "vet" in args for _, args in commands))
        self.git("branch", "-m", "other")
        with self.assertRaisesRegex(ValueError, "project main branch"):
            webdev.resolve({"work": str(resolve_work)})

    def history_fixture(self):
        runtime = self.root / "runtime"
        (runtime / "data").mkdir(parents=True)
        (runtime / "hub.json").write_text(json.dumps({"database_url":"fixture-only"}))
        (runtime / "secret.key").write_bytes(b"fixture-key")
        (runtime / "data/history.txt").write_text("synthetic history")
        (runtime / "web").mkdir()
        (runtime / "web/index.html").write_bytes(b"saved web override")
        binary = self.root / "personad"
        binary.write_bytes(b"fixture installed program")
        database = self.root / "database.json"
        database.write_text('{"messages":["synthetic history"]}')
        self.env_db = patch.dict(os.environ, {"FIXTURE_DB":str(database)})
        self.env_db.start()
        self.addCleanup(self.env_db.stop)
        self.tool("pg_dump", "import os,pathlib,sys\np=pathlib.Path(sys.argv[sys.argv.index('--file')+1]);p.write_bytes(b'PGDMP'+pathlib.Path(os.environ['FIXTURE_DB']).read_bytes())\n")
        self.tool("pg_restore", "import pathlib,sys\nassert pathlib.Path(sys.argv[-1]).read_bytes().startswith(b'PGDMP')\nprint('fixture archive list')\n")
        events = self.root / "events"
        hook = self.tool("writer-hook", "import pathlib,sys\np=pathlib.Path(sys.argv[1]);p.write_text((p.read_text() if p.exists() else '')+sys.argv[2]+'\\n')\n")
        return {"runtime": str(runtime), "hub_database":"effective-fixture-database", "executables":{"personad":str(binary)}, "candidate":{"commit":"a"*40}, "config":{"backup":{
            "quiesce":[hook,str(events),"pause"], "resume":[hook,str(events),"resume"]}}}

    def test_history_progress_counts_regular_files_and_skips_excluded_trees(self):
        root = self.root / "history"
        root.mkdir()
        (root / "keep").write_bytes(b"a" * (2 * 1024 * 1024))
        os.link(root / "keep", root / "same")
        (root / "empty").touch()
        (root / "link").symlink_to("keep")
        (root / "skip").mkdir()
        (root / "skip/excluded").write_bytes(b"discard")
        os.mkfifo(root / "fifo")
        elapsed = iter(range(100))
        events = []
        with patch.object(webdev.time, "monotonic", side_effect=lambda: next(elapsed)):
            progress = webdev.Progress("backing_up", events.append)
            progress.step("archive_files")
            archive = self.root / "files.tar.gz"
            webdev.archive_history([root], {root / "skip"}, archive, progress)
        first = next(event for event in events if "bytes_total" in event)
        self.assertEqual((first["bytes_done"], first["bytes_total"], first["item_total"]), (0, 2 * 1024 * 1024, 2))
        self.assertTrue(any(0 < event.get("percent", 0) < 100 and event["bytes_per_second"] > 0 for event in events))
        self.assertEqual((events[-1]["item_done"], events[-1]["percent"]), (2, 100))
        self.assertNotIn(str(root), json.dumps(events))
        with tarfile.open(archive) as stream:
            self.assertEqual(stream.getnames(), ["root-0", "root-0/empty", "root-0/keep", "root-0/same", "root-0/link"])

    def test_progress_throttles_counters_and_resets_metrics_on_step_change(self):
        elapsed = 0
        events = []
        with patch.object(webdev.time, "monotonic", side_effect=lambda: elapsed):
            progress = webdev.Progress("backing_up", events.append)
            progress.step("database_dump", item_done=0, item_total=2, bytes_done=0)
            elapsed = 0.5
            progress.add_bytes(200)
            self.assertEqual(len(events), 1)
            elapsed = 0.75
            progress.add_bytes(100)
            self.assertEqual(events[-1]["bytes_per_second"], 400)
            self.assertNotIn("percent", events[-1])
            elapsed = 1.5
            progress.emit()
            self.assertEqual(events[-1]["bytes_per_second"], 260)
            progress.step("archive_files")
        self.assertEqual(events[-1]["elapsed_seconds"], 1)
        self.assertNotIn("bytes_done", events[-1])
        self.assertNotIn("item_total", events[-1])

    def wait_file(self, path, process):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if path.exists():
                return
            if process.poll() is not None:
                self.fail("helper exited before " + path.name)
            time.sleep(.01)
        self.fail("timed out waiting for " + path.name)

    def test_cancellation_resumes_writers_and_reaps_child_group(self):
        for phase, cancel_signal in (("quiesce", signal.SIGINT), ("dump", signal.SIGTERM),
                                     ("resume", signal.SIGINT), ("failed-resume", signal.SIGINT),
                                     ("stubborn-dump", signal.SIGINT)):
            with self.subTest(phase=phase), tempfile.TemporaryDirectory(dir=self.root) as directory:
                # Each cancellation gets an independent synthetic installation.
                previous_root = self.root
                self.root = Path(directory)
                child_group = None
                try:
                    request = self.history_fixture()
                    paused, entered, resumed, interrupted = [self.root / n for n in ("paused", "entered", "resumed", "interrupted")]
                    blocker = self.tool("blocker", f'''import os, pathlib, signal, sys, time
def interrupt(*_):
    pathlib.Path({str(interrupted)!r}).write_text('SIGINT')
    raise SystemExit(9)
signal.signal(signal.SIGINT, interrupt)
if {phase == 'stubborn-dump'!r}:
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
pathlib.Path({str(entered)!r}).write_text(str(os.getpid()))
while True: time.sleep(.01)
''')
                    pause = self.tool("pause", f"import pathlib\npathlib.Path({str(paused)!r}).touch()\n")
                    resume = self.tool("resume", f'''import pathlib, time
if {phase == 'resume'!r}: pathlib.Path({str(entered)!r}).touch()
time.sleep(.4)
if {phase == 'failed-resume'!r}: raise SystemExit(7)
pathlib.Path({str(paused)!r}).unlink(missing_ok=True)
pathlib.Path({str(resumed)!r}).touch()
''')
                    request["config"]["backup"].update(quiesce=[pause], resume=[resume])
                    if phase == "quiesce":
                        request["config"]["backup"]["quiesce"] = [self.tool("pause-block", f"import pathlib,os\npathlib.Path({str(paused)!r}).touch()\nos.execv({blocker!r},[{blocker!r}])\n")]
                    elif phase != "resume":
                        self.tool("pg_dump", Path(blocker).read_text().split("\n", 1)[1])
                    request.update(operation="backup", result=str(self.root/"result.json"))
                    with subprocess.Popen([sys.executable, str(ROOT/"server/internal/update/webdev.py")],
                                          stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                          text=True, start_new_session=True) as process:
                        try:
                            process.stdin.write(json.dumps(request))
                            process.stdin.close()
                            self.wait_file(entered, process)
                            if phase != "resume":
                                child_group = int(entered.read_text())
                            process.send_signal(cancel_signal)
                            if phase == "resume":
                                time.sleep(.05)
                                process.send_signal(signal.SIGTERM)
                            self.assertNotEqual(process.wait(timeout=10), 0)
                        finally:
                            if process.poll() is None:
                                process.kill()
                            process.wait(timeout=3)
                    store = Path(request["runtime"])/"update/development-backups"
                    self.assertFalse((self.root/"result.json").exists())
                    self.assertFalse(any(store.glob(".incomplete-*")))
                    if phase == "failed-resume":
                        self.assertTrue(paused.exists())
                        self.assertTrue((store/"coordination-pending.json").exists())
                    else:
                        self.assertTrue(resumed.exists(), "cancellation interrupted resume")
                        self.assertFalse(paused.exists())
                        self.assertFalse((store/"coordination-pending.json").exists())
                    if phase != "resume":
                        if phase != "stubborn-dump":
                            self.assertTrue(interrupted.exists(), "child did not get graceful SIGINT")
                        with self.assertRaises(ProcessLookupError):
                            os.killpg(int(entered.read_text()), 0)
                    if phase == "failed-resume":
                        with self.assertRaisesRegex(ValueError, "coordination"):
                            webdev.preflight(request)
                    else:
                        webdev.preflight(request)
                finally:
                    if child_group is not None:
                        try:
                            os.killpg(child_group, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    self.root = previous_root

    def test_three_verified_snapshots_and_failure_retention(self):
        request = self.history_fixture()
        store = Path(request["runtime"]) / "update/development-backups"
        receipts = []
        for _ in range(5):
            receipts.append(webdev.snapshot(request))
        snapshots = sorted(p for p in store.iterdir() if webdev.NAME.fullmatch(p.name))
        self.assertEqual(len(snapshots), 3)
        self.assertEqual([p.name for p in snapshots], [r["id"] for r in receipts[-3:]])
        self.assertEqual(receipts[-1]["retained"], 3)
        manifest = webdev.verify(snapshots[-1])
        self.assertEqual(manifest["databases"], 1)
        self.assertEqual(json.loads((snapshots[-1]/"effective-databases.json").read_text()),["effective-fixture-database"])
        self.assertIn("personad",manifest["programs"])
        with tarfile.open(snapshots[-1]/"files.tar.gz") as archive:
            self.assertEqual(archive.extractfile("root-0/data/history.txt").read(), b"synthetic history")
            self.assertEqual(archive.extractfile("root-0/secret.key").read(), b"fixture-key")
            self.assertEqual(archive.extractfile("root-0/web/index.html").read(), b"saved web override")
            self.assertFalse(any("update/development-backups" in m.name for m in archive))
        unrelated = store / "manual-backup"
        unrelated.mkdir()
        (unrelated/"keep").write_text("untouched")
        self.tool("pg_dump", "raise SystemExit(7)\n")
        with self.assertRaises(subprocess.CalledProcessError):
            webdev.snapshot(request)
        self.assertEqual(sorted(p.name for p in snapshots), sorted(p.name for p in store.iterdir() if webdev.NAME.fullmatch(p.name)))
        self.assertTrue((unrelated/"keep").exists())
        self.assertTrue((self.root/"events").read_text().endswith("pause\nresume\n"))
        self.assertFalse((store/"coordination-pending.json").exists())

    def test_slim_history_restores_live_state_and_respects_explicit_roots(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        previous = runtime / "web.prev"
        previous.mkdir()
        (previous / "index.html").write_text("stale")
        kept = ["managers/assistant/manager.json", "profiles/default/model", "personas/custom/look",
                "skills/custom/SKILL.md", "uploads/image", "data/web.prev/note", "unknown/state"]
        for name in kept:
            path = runtime / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("{}" if name.endswith(".json") else name)
        backup = request["config"]["backup"]
        original_quiesce = backup["quiesce"]
        for layout in ("default", "extra-root", "knowledge", "active-web"):
            with self.subTest(layout=layout):
                manager = runtime / kept[0]
                backup["quiesce"] = original_quiesce
                backup.pop("extra_roots", None)
                manager.write_text("{}")
                if layout == "extra-root":
                    backup["extra_roots"] = [str(previous)]
                elif layout == "knowledge":
                    kb = previous / "notes"
                    kb.mkdir()
                    # Capture the final write made while the external pause drains.
                    backup["quiesce"] = [sys.executable, "-c",
                        "import pathlib,sys; pathlib.Path(sys.argv[1]).write_text(sys.argv[2])",
                        str(manager), json.dumps({"kb_dir": str(kb)})]
                elif layout == "active-web":
                    (runtime / "web").rename(runtime / "unused-web")
                    (runtime / "web").symlink_to(previous)
                receipt = webdev.snapshot(request)
                snapshot = runtime / "update/development-backups" / receipt["id"]
                destination = self.root / layout
                webdev.extract_history(snapshot, destination)
                restored = destination / "root-0"
                self.assertEqual((restored / "web.prev").exists(), layout != "default")
                for name in [*kept, "hub.json", "data/history.txt", "secret.key", "web/index.html"]:
                    self.assertEqual((restored / name).read_bytes(), (runtime / name).read_bytes())
                self.assertEqual((snapshot / "database-0.dump").read_bytes()[5:], (self.root / "database.json").read_bytes())

    def test_archive_programs_after_external_resume(self):
        request = self.history_fixture()
        events = self.root / "events"
        steps = []
        def observe(event):
            step = event["step"]
            if steps and steps[-1] == step:
                return
            steps.append(step)
            if step in ("database_dump", "archive_files", "resume_writers"):
                self.assertEqual(events.read_text().splitlines(), ["pause"])
            if step in ("archive_programs", "verify_snapshot"):
                self.assertEqual(events.read_text().splitlines(), ["pause", "resume"])
        webdev.snapshot(request, webdev.Progress("backing_up", observe))
        self.assertEqual(steps, ["preflight", "quiesce", "database_dump", "archive_files", "resume_writers",
                                 "archive_programs", "verify_snapshot"])

    def test_packaging_interrupt_releases_lock_and_keeps_last_recovery_point(self):
        request = self.history_fixture()
        store = Path(request["runtime"]) / "update/development-backups"
        first = webdev.snapshot(request)
        def interrupt(*args):
            self.assertEqual((self.root / "events").read_text().splitlines(), ["pause", "resume"] * 2)
            self.assertFalse((store / "coordination-pending.json").exists())
            raise InterruptedError()
        with patch.object(webdev, "archive_programs", interrupt), self.assertRaises(InterruptedError):
            webdev.snapshot(request)
        self.assertEqual(list(store.glob("snapshot-*")), [store / first["id"]])
        self.assertFalse(list(store.glob(".incomplete-*")))
        self.assertEqual(webdev.snapshot(request)["retained"], 2)

    def test_program_preimages_survive_atomic_replacement_and_reject_in_place_changes(self):
        request = self.history_fixture()
        program = Path(request["executables"]["personad"])
        original = program.read_bytes()
        replacement = self.root / "replacement"
        replacement.write_bytes(b"later install")
        request["config"]["backup"]["resume"] = ["mv", str(replacement), str(program)]
        receipt = webdev.snapshot(request)
        store = Path(request["runtime"]) / "update/development-backups"
        snapshot = store / receipt["id"]
        self.assertEqual(gzip.decompress((snapshot / "program-personad.gz").read_bytes()), original)
        self.assertEqual(program.read_bytes(), b"later install")

        request["config"]["backup"]["resume"] = [sys.executable, "-c",
            "import pathlib, sys; pathlib.Path(sys.argv[1]).write_bytes(b'changed in place')", str(program)]
        with self.assertRaisesRegex(ValueError, "installed program changed"):
            webdev.snapshot(request)
        self.assertEqual(list(store.glob("snapshot-*")), [snapshot])
        self.assertFalse(list(store.glob(".incomplete-*")))
        self.assertFalse((store / "coordination-pending.json").exists())

    def test_host_node_storage_and_recovery_with_both_v1_layouts(self):
        request = self.history_fixture()
        platform = "linux-amd64"
        nodes = self.root / "nodes"
        nodes.mkdir()
        request.update(platform=platform, from_commit="b" * 40, from_version="saved", from_build=123,
                       node_directory=str(nodes))
        original = Path(request["executables"]["personad"]).read_bytes()
        for layout in ("linked", "legacy", "different", "no-hard-links"):
            node = nodes / ("personad-" + platform)
            node.write_bytes(b"different node" if layout == "different" else original)
            unsupported = OSError(errno.EOPNOTSUPP, "fixture store has no hard links")
            with (patch.object(os, "link", side_effect=unsupported) if layout == "no-hard-links" else contextlib.nullcontext()):
                receipt = webdev.snapshot(request)
            path = Path(request["runtime"]) / "update/development-backups" / receipt["id"]
            package = path / ("node-" + platform + ".gz")
            program = path / "program-personad.gz"
            if layout == "legacy":
                # Older writers compressed the node separately, with its own gzip header and hash.
                package.unlink()
                with gzip.open(package, "wb") as stream:
                    stream.write(node.read_bytes())
                manifest = json.loads((path / "manifest.json").read_text())
                manifest["files"][package.name] = webdev.digest(package)
                (path / "manifest.json").write_text(json.dumps(manifest))
            self.assertEqual(package.samefile(program), layout == "linked")
            manifest = webdev.verify(path)
            # Follow the offline operator procedure using the recorded files and native hashes.
            for entry, expected in [(manifest["programs"]["personad"], original),
                                    (manifest["nodes"][platform], node.read_bytes())]:
                restored = gzip.decompress((path / entry["file"]).read_bytes())
                self.assertEqual(restored, expected)
                self.assertEqual(hashlib.sha256(restored).hexdigest(), entry["sha256"])
            work = self.root / layout
            work.mkdir()
            out = webdev.prepare_backup(dict(request, backup_id=receipt["id"], work=str(work), candidate=None))
            self.assertEqual(gzip.decompress((Path(out["artifacts"]) / "personad-linux-amd64.gz").read_bytes()), original)

    def test_builtin_quiesce_custom_directory_and_retention(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        # The Hub paused the writers itself: no hooks, no coordination marker.
        directory = runtime / "backups/web-dev"
        request["config"]["backup"] = {"builtin": True, "directory": str(directory), "retain": 2}
        receipts = [webdev.snapshot(request) for _ in range(3)]
        snapshots = sorted(p for p in directory.iterdir() if webdev.NAME.fullmatch(p.name))
        self.assertEqual(len(snapshots), 2)
        self.assertEqual([p.name for p in snapshots], [r["id"] for r in receipts[-2:]])
        self.assertEqual(receipts[-1]["retained"], 2)
        self.assertFalse((runtime / "update/development-backups").exists())
        self.assertFalse((directory / "coordination-pending.json").exists())
        # A folder inside the runtime is never archived into the next snapshot.
        with tarfile.open(snapshots[-1] / "files.tar.gz") as archive:
            self.assertFalse(any("backups/web-dev" in member.name for member in archive))
        # The folder must be private.
        public = runtime / "public-snapshots"
        public.mkdir()
        public.chmod(0o755)
        request["config"]["backup"]["directory"] = str(public)
        with self.assertRaisesRegex(ValueError, "private"):
            webdev.snapshot(request)
        # Relative paths and an out-of-range retention are refused.
        for broken in ({"directory": "relative/dir"}, {"directory": str(directory), "retain": 51}):
            request["config"]["backup"] = dict(broken, builtin=True)
            with self.assertRaises(ValueError):
                webdev.snapshot(request)
        # Without the built-in pause, hooks are still required.
        request["config"]["backup"] = {}
        with self.assertRaisesRegex(ValueError, "quiesce"):
            webdev.snapshot(request)

    def test_existing_backslash_runtime_and_backup_directory(self):
        request = self.history_fixture()
        original_runtime = Path(request["runtime"])
        runtime = self.root / r"persona\prod"
        original_runtime.rename(runtime)
        request["runtime"] = str(runtime)
        recommended = runtime / "update" / "development-backups"
        request["config"]["backup"] = {"builtin": True, "directory": str(recommended)}
        receipt = webdev.snapshot(request)
        manifest = webdev.verify(recommended / receipt["id"])
        self.assertEqual(manifest["roots"], [str(runtime.resolve())])

        existing_store = self.root / r"existing\store"
        existing_store.mkdir(mode=0o700)
        request["config"]["backup"] = {"builtin": True, "directory": str(existing_store)}
        receipt2 = webdev.snapshot(request)
        webdev.verify(existing_store / receipt2["id"])

        for bad in (
            str(runtime / r"new\dir"),
            str(runtime / r"new\dir" / "snapshots"),
            str(runtime) + "//update/development-backups",
            str(runtime / "update" / ".." / "escape"),
            str(runtime / "bad\x00dir"),
        ):
            request["config"]["backup"] = {"builtin": True, "directory": bad}
            with self.assertRaisesRegex(ValueError, "invalid backup directory"):
                webdev.backup_store(request)

    def test_builtin_quiesce_refuses_extra_sources_and_other_local_managers(self):
        request = self.history_fixture()
        external = dict(request["config"]["backup"])
        runtime = Path(request["runtime"])
        extra_root = self.root / "extra-root"
        extra_root.mkdir()
        (extra_root / "note.txt").write_text("extra")

        # Built-in pause refuses extra_databases and extra_roots.
        for extra in ({"extra_databases": ["extra-db"]}, {"extra_roots": [str(extra_root)]}):
            request["config"]["backup"] = dict(extra, builtin=True)
            with self.assertRaisesRegex(ValueError, "built-in writer pause"):
                webdev.snapshot(request)

        # Built-in pause refuses another local manager even with no owned machines.
        other = runtime / "managers" / "m-other"
        other.mkdir(parents=True)
        (other / "manager.json").write_text(json.dumps({"database_url": "other-manager-db"}))
        request["config"]["backup"] = {"builtin": True}
        with self.assertRaisesRegex(ValueError, "other local managers"):
            webdev.snapshot(request)

        # External quiesce/resume hooks still work with extra sources and another local manager.
        request["config"]["backup"] = dict(external, extra_databases=["extra-db"], extra_roots=[str(extra_root)])
        receipt = webdev.snapshot(request)
        manifest = webdev.verify(runtime / "update/development-backups" / receipt["id"])
        self.assertEqual(manifest["databases"], 3)
        self.assertIn(str(extra_root.resolve()), manifest["roots"])

    def test_store_may_not_be_or_contain_a_collected_root(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        # The store and everything below it leave the archive: a store that is
        # the runtime, or holds it, would drop what the snapshot is for.
        for store in (runtime, runtime.parent):
            request["config"]["backup"] = {"builtin": True, "directory": str(store)}
            with self.assertRaisesRegex(ValueError, "may not be the runtime or contain"):
                webdev.snapshot(request)
        self.assertFalse((runtime / "update" / "development-backups").exists())

    def test_apply_backup_stages_only_saved_programs_and_preserves_live_data(self):
        request = self.history_fixture()
        request.update(from_version="web-dev-old", from_commit="b"*40, from_build=202611120001,
                       from_updated_at="2026-11-12T10:00:00Z", platform="linux-amd64")
        node_dir = self.root / "nodes"
        node_dir.mkdir()
        node = node_dir / "personad-linux-arm64"
        node.write_bytes(b"old arm64 node")
        (node_dir / "personad-linux-arm64.version").write_text("web-dev-old\n")
        (node_dir / "personad-windows-amd64").write_bytes(b"another version")
        (node_dir / "personad-windows-amd64.version").write_text("other-version")
        (node_dir / "personad-darwin-arm64").symlink_to(node)
        request["node_directory"] = str(node_dir)
        receipt = webdev.snapshot(request)
        manifest = webdev.verify(Path(request["runtime"])/"update/development-backups"/receipt["id"])
        self.assertEqual(set(manifest["nodes"]), {"linux-arm64"})
        for _ in range(2):
            webdev.snapshot(request)
        runtime = Path(request["runtime"])
        current = {runtime/"data/history.txt": b"new messages", runtime/"secret.key": b"current key",
                   self.root/"database.json": b"current database", self.root/"personad": b"current program",
                   runtime/"web/index.html": b"current web override"}
        for file, content in current.items():
            file.write_bytes(content)
        versions = webdev.backups(request)["backups"]
        self.assertEqual(len(versions), 3)
        self.assertEqual(versions[0]["commit"], "b"*40)  # Saved version, not the attempted target.
        self.assertEqual(versions[0]["updated_at"], request["from_updated_at"])
        self.assertNotIn("effective-fixture-database", json.dumps(versions))
        work = self.root/"prepare"
        work.mkdir()
        # Preparation must neither restore a database nor pause current writers.
        self.tool("pg_restore", "raise SystemExit('must not restore data')\n")
        events = (self.root/"events").read_bytes()
        progress = []
        clock = iter(range(100))
        with patch.object(webdev.time, "monotonic", side_effect=lambda: next(clock)):
            built = webdev.prepare_backup(dict(request, work=str(work), backup_id=receipt["id"],
                                               candidate={"commit":"b"*40}, version="web-dev-old", build=202611120001,
                                               platforms=["linux-amd64", "linux-arm64"]),
                                          webdev.Progress("applying_backup", progress.append))
        artifacts = Path(built["artifacts"])
        self.assertEqual({p["step"] for p in progress}, {"verify_snapshot", "prepare_artifacts"})
        self.assertGreater(max(p.get("bytes_per_second", 0) for p in progress if p["step"] == "prepare_artifacts"), 0)
        self.assertEqual(progress[-1]["item_total"], 2)
        self.assertEqual(progress[-1]["item_done"], 2)
        self.assertEqual(progress[-1]["bytes_done"], sum(p.stat().st_size for p in artifacts.iterdir()))
        self.assertEqual(progress[-1]["bytes_done"], progress[-1]["bytes_total"])
        self.assertNotIn(str(self.root), json.dumps(progress))
        self.assertEqual(gzip.decompress((artifacts/"personad-linux-amd64.gz").read_bytes()), b"fixture installed program")
        self.assertEqual(gzip.decompress((artifacts/"personad-linux-arm64.gz").read_bytes()), b"old arm64 node")
        self.assertEqual((self.root/"events").read_bytes(), events)
        for file, content in current.items():
            self.assertEqual(file.read_bytes(), content)
        with self.assertRaisesRegex(ValueError, "required Node platform"):
            webdev.prepare_backup(dict(request, work=str(work), backup_id=receipt["id"],
                                       platforms=["darwin-arm64"], candidate=None))
        self.tool("pg_restore", "import pathlib,sys\nassert pathlib.Path(sys.argv[-1]).read_bytes().startswith(b'PGDMP')\n")
        webdev.snapshot(request)  # Rotating the selected oldest snapshot keeps staged programs usable.
        self.assertFalse((runtime/"update/development-backups"/receipt["id"]).exists())
        self.assertEqual(gzip.decompress((artifacts/"personad-linux-amd64.gz").read_bytes()), b"fixture installed program")
        with self.assertRaises(ValueError):
            webdev.prepare_backup(dict(request, work=str(work), backup_id="../outside"))
        snapshot = runtime/"update/development-backups"/versions[0]["id"]
        (snapshot/"program-personad.gz").write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "checksum"):
            webdev.prepare_backup(dict(request, work=str(work), backup_id=snapshot.name))

    def test_stale_staging_is_removed_only_under_lock_without_recovery_marker(self):
        request = self.history_fixture()
        store = Path(request["runtime"]) / "update/development-backups"
        store.mkdir(parents=True, mode=0o700)
        stale = store / ".incomplete-earlier"
        stale.mkdir()
        outside = self.root / "keep"
        outside.mkdir()
        (stale / "link").symlink_to(outside)
        (store / ".incomplete-link").symlink_to(outside)
        marker = store / "coordination-pending.json"
        with (store / ".lock").open("a") as lock:
            webdev.lock_backup(lock)
            with self.assertRaisesRegex(ValueError, "another backup"):
                webdev.preflight(request)
            self.assertTrue(stale.is_dir())
        marker.touch()
        events = []
        with self.assertRaisesRegex(ValueError, "coordination"):
            webdev.snapshot(request, webdev.Progress("backing_up", events.append))
        self.assertEqual([e["step"] for e in events], ["preflight"])
        self.assertTrue(stale.is_dir())
        marker.unlink()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            receipt = webdev.snapshot(request)
        self.assertIn("removed an incomplete snapshot", output.getvalue())
        self.assertFalse(stale.exists())
        self.assertTrue(outside.is_dir())
        self.assertTrue((store / ".incomplete-link").is_symlink())
        self.assertEqual(receipt["retained"], 1)

    def test_failed_verification_cleans_staging_and_preserves_published_snapshots(self):
        request = self.history_fixture()
        first = webdev.snapshot(request)
        store = Path(request["runtime"]) / "update/development-backups"
        with patch.object(webdev, "verify", side_effect=ValueError("invalid snapshot")), self.assertRaises(ValueError):
            webdev.snapshot(request)
        self.assertEqual(list(store.glob(".incomplete-*")), [])
        self.assertEqual([p.name for p in store.glob("snapshot-*")], [first["id"]])
        self.assertEqual(webdev.snapshot(request)["retained"], 2)

    def test_resume_failure_and_unknown_coordination_block_retry(self):
        request = self.history_fixture()
        request["config"]["backup"]["resume"] = [self.tool("resume-fails", "raise SystemExit(2)\n")]
        with self.assertRaises(subprocess.CalledProcessError):
            webdev.snapshot(request)
        with self.assertRaisesRegex(ValueError,"coordination needs recovery"):
            webdev.snapshot(request)

    def test_backup_and_resume_failures_both_reach_private_log(self):
        request = self.history_fixture()
        self.tool("pg_dump", "raise SystemExit(7)\n")
        request["config"]["backup"]["resume"] = [self.tool("resume-fails", "raise SystemExit(2)\n")]
        log = io.StringIO()
        with contextlib.redirect_stderr(log), self.assertRaises(subprocess.CalledProcessError) as caught:
            webdev.snapshot(request)
        self.assertEqual(caught.exception.returncode, 2)
        self.assertEqual(caught.exception.__cause__.returncode, 7)
        self.assertIn("snapshot failed: CalledProcessError (exit 7)", log.getvalue())
        self.assertIn("resume failed: CalledProcessError (exit 2)", log.getvalue())
        self.assertNotIn("effective-fixture-database", log.getvalue())
        store = Path(request["runtime"])/"update/development-backups"
        self.assertTrue((store/"coordination-pending.json").exists())
        self.assertEqual(list(store.glob("snapshot-*")), [])

    def test_snapshot_and_prepare_report_lock_contention(self):
        request = self.history_fixture()
        receipt = webdev.snapshot(request)
        store = Path(request["runtime"])/"update/development-backups"
        with (store/".lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            for operation, payload in [(webdev.snapshot, request), (webdev.prepare_backup, dict(request, backup_id=receipt["id"]))]:
                with self.subTest(operation=operation.__name__), self.assertRaisesRegex(ValueError, "another backup or prepare is in progress"):
                    operation(payload)

    def test_real_writer_is_quiesced_across_the_database_and_history_capture(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        pause, paused, ready = self.root/"pause", self.root/"paused", self.root/"writer-ready"
        writer = self.root/"writer.py"
        writer.write_text('''import pathlib,sys,time
root,pause,paused,db,ready=map(pathlib.Path,sys.argv[1:])
counter=0
while True:
    if pause.exists():
        paused.touch()
        while pause.exists(): time.sleep(.002)
        paused.unlink(missing_ok=True)
    counter+=1
    (root/'data/history.txt').write_text(str(counter))
    db.write_text(str(counter))
    if counter == 1: ready.touch()
    time.sleep(.002)
''')
        process = subprocess.Popen([sys.executable,str(writer),str(runtime),str(pause),str(paused),str(self.root/"database.json"),str(ready)])
        self.addCleanup(lambda: (process.terminate(),process.wait(timeout=3)))
        # The byte comparison requires the writer's first completed paired write.
        self.wait_file(ready, process)
        hook = self.tool("coordinate", '''import pathlib,sys,time
pause,paused=map(pathlib.Path,sys.argv[1:3])
if sys.argv[3]=='pause': pause.touch()
else: pause.unlink(missing_ok=True)
for _ in range(1000):
    if paused.exists()==(sys.argv[3]=='pause'): break
    time.sleep(.002)
else: raise SystemExit(3)
''')
        request["config"]["backup"].update(quiesce=[hook,str(pause),str(paused),"pause"],resume=[hook,str(pause),str(paused),"resume"])
        receipt = webdev.snapshot(request)
        path = runtime / "update/development-backups" / receipt["id"]
        with tarfile.open(path/"files.tar.gz") as archive:
            history = archive.extractfile("root-0/data/history.txt").read()
        self.assertEqual((path/"database-0.dump").read_bytes()[5:], history)
        self.assertFalse(pause.exists())
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                self.fail("writer exited before resuming")
            live_history = (runtime/"data/history.txt").read_bytes()
            live_database = (self.root/"database.json").read_bytes()
            if live_history not in (b"", history) and live_history == live_database:
                break
            time.sleep(.01)
        else:
            self.fail("writer did not resume paired writes")

    def test_snapshot_excludes_hub_meeting_audio_from_knowledge_bases(self):
        rec_id = "0f8fad5b-d9cb-469f-a165-70867728950e"
        for layout in ("default", "external-kb", "extra-manager"):
            with self.subTest(layout=layout), tempfile.TemporaryDirectory(dir=self.root) as directory:
                previous_root = self.root
                self.root = Path(directory)
                try:
                    request = self.history_fixture()
                    runtime = Path(request["runtime"])
                    if layout == "default":
                        kb = runtime / "data"
                        root_prefix = "root-0/data"
                    elif layout == "external-kb":
                        kb = self.root / "external-kb"
                        kb.mkdir()
                        manager_dir = runtime / "managers/assistant"
                        manager_dir.mkdir(parents=True)
                        (manager_dir / "manager.json").write_text(json.dumps({"kb_dir": str(kb)}))
                        root_prefix = None
                    else:
                        extra_mgr = runtime / "managers/m-extra"
                        extra_mgr.mkdir(parents=True)
                        (extra_mgr / "manager.json").write_text(json.dumps({"database_url": "extra-db"}))
                        kb = extra_mgr / "data"
                        root_prefix = "root-0/managers/m-extra/data"

                    audio_files = [
                        kb / f"recordings/{rec_id}/attachments/audio/0.pcm",
                        kb / f"recordings/{rec_id}/attachments/audio/index.json",
                        kb / "recordings/sample-meeting/attachments/audio/marker.bin",
                    ]
                    for path in audio_files:
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_bytes(b"ephemeral-audio-" + path.name.encode())

                    kept_files = {
                        f"recordings/{rec_id}/meta.json": b'{"id":"' + rec_id.encode() + b'"}',
                        f"recordings/{rec_id}/hub_segments.json": b'{"rev":1}',
                        f"recordings/{rec_id}/attachments/p1.jpg": b"meeting-photo",
                        f"recordings/{rec_id}/attachments/audio.jpg": b"photo-named-audio",
                        "chats/c1/attachments/audio/note.ogg": b"chat-audio-attachment",
                    }
                    for rel, body in kept_files.items():
                        target = kb / rel
                        target.parent.mkdir(parents=True, exist_ok=True)
                        target.write_bytes(body)

                    receipt = webdev.snapshot(request)
                    snapshot = runtime / "update/development-backups" / receipt["id"]
                    with tarfile.open(snapshot / "files.tar.gz") as archive:
                        names = archive.getnames()
                    if root_prefix is None:
                        manifest = json.loads((snapshot / "manifest.json").read_text())
                        root_prefix = f"root-{manifest['roots'].index(str(kb.resolve()))}"
                    for name in names:
                        if f"{root_prefix}/recordings/" in name and ("/attachments/audio/" in name or name.endswith("/attachments/audio")):
                            self.fail(f"development snapshot contains hub meeting audio member {name}")
                    for rel in kept_files:
                        self.assertIn(f"{root_prefix}/{rel}", names)

                    destination = self.root / "extracted"
                    webdev.extract_history(snapshot, destination)
                    restored_kb = destination / root_prefix
                    for rel, body in kept_files.items():
                        self.assertEqual((restored_kb / rel).read_bytes(), body)
                    self.assertFalse((restored_kb / f"recordings/{rec_id}/attachments/audio").exists())
                    self.assertFalse((restored_kb / "recordings/sample-meeting/attachments/audio").exists())
                finally:
                    self.root = previous_root


if __name__ == "__main__":
    unittest.main()
