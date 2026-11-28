import importlib.util
import io
from pathlib import Path
import sys
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts/ci" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


server = load("server-shards")
python = load("python-tests")


class ServerShardCommandTests(unittest.TestCase):
    node = ["example/server/internal/node", "example/server/internal/node/sub"]

    def test_the_node_shard_runs_internal_node_in_its_partitions_beside_its_other_packages(self):
        runs = server.commands(self.node, "node", "/tmp/db.json",
                               {"./internal/node": ["^(TestA|TestD)$", "^(TestB|TestE)$", "^(TestC|TestF)$"]})
        self.assertEqual(len(runs), 4)
        for run in runs:
            self.assertEqual(run[run.index("go"):run.index("go") + 7],
                             ["go", "test", "-json", "-p", "1", "-count=1", "-timeout=10m"])
            self.assertIn("/tmp/db.json", run)
        self.assertEqual(runs[0][-1], "example/server/internal/node/sub")
        self.assertEqual([run[-3:] for run in runs[1:]], [["-run", "^(TestA|TestD)$", "./internal/node"],
                                                          ["-run", "^(TestB|TestE)$", "./internal/node"],
                                                          ["-run", "^(TestC|TestF)$", "./internal/node"]])

    def test_the_managers_shard_runs_internal_assistant_hub_and_voice_in_their_partitions(self):
        managers = ["example/server/internal/assistant", "example/server/internal/assistant/fleet",
                    "example/server/internal/hub", "example/server/internal/modules/voice"]
        self.assertEqual(server.select_packages(managers + self.node, "managers"), managers)
        runs = server.commands(managers, "managers", "/tmp/db.json",
                               {"./internal/assistant": ["^(TestX)$", "^(TestY)$"], "./internal/hub": ["^(TestH)$"],
                                "./internal/modules/voice": ["^(TestV)$"]})
        self.assertEqual(len(runs), 5)
        for run in runs:
            self.assertEqual(run[run.index("go"):run.index("go") + 7],
                             ["go", "test", "-json", "-p", "3", "-count=1", "-timeout=10m"])
        self.assertEqual(runs[0][-2:], ["-timeout=10m", "example/server/internal/assistant/fleet"])
        self.assertEqual([run[-3:] for run in runs[1:]], [["-run", "^(TestX)$", "./internal/assistant"],
                                                          ["-run", "^(TestY)$", "./internal/assistant"],
                                                          ["-run", "^(TestH)$", "./internal/hub"],
                                                          ["-run", "^(TestV)$", "./internal/modules/voice"]])

    def test_shards_without_a_partitioned_package_stay_one_process(self):
        self.assertEqual(len(server.commands(["example/server/internal/assistant/fleet"], "managers", "/tmp/db.json")), 1)
        self.assertEqual(len(server.commands(["example/server/internal/node/sub"], "node", "/tmp/db.json")), 1)
        self.assertEqual(len(server.commands(["example/server/internal/assistantwire"], "other", "/tmp/db.json")), 1)

    def test_large_rest_lists_are_batched_round_robin_while_small_lists_stay_one_process(self):
        small_rest = [f"example/server/internal/pkg{i:02d}" for i in range(server.REST_BATCH_MIN - 1)]
        self.assertEqual(len(server.commands(small_rest, "other", "/tmp/db.json")), 1)
        self.assertEqual(len(server.commands(small_rest + ["example/server/internal/db"], "other", "/tmp/db.json",
                                             {"./internal/db": ["^(TestDB)$"]})), 2)

        large_rest = [f"example/server/internal/pkg{i:02d}" for i in range(server.REST_BATCH_MIN)]
        plain_runs = server.commands(large_rest, "other", "/tmp/db.json")
        self.assertEqual(len(plain_runs), server.REST_BATCHES)
        prefix = server.go_test("other", "/tmp/db.json")
        batched = []
        for index, run in enumerate(plain_runs):
            self.assertEqual(run[:len(prefix)], prefix)
            batch = run[len(prefix):]
            self.assertEqual(batch, large_rest[index::server.REST_BATCHES])
            batched.extend(batch)
        self.assertCountEqual(batched, large_rest)
        self.assertEqual(len(set(batched)), len(large_rest))

        mixed = large_rest + ["example/server/internal/db", "example/server/internal/devices"]
        partitions = {
            "./internal/db": ["^(TestDB1)$", "^(TestDB2)$"],
            "./internal/devices": ["^(TestDev1)$", "^(TestDev2)$"],
        }
        mixed_runs = server.commands(mixed, "other", "/tmp/db.json", partitions)
        self.assertEqual(len(mixed_runs), server.REST_BATCHES + 4)
        self.assertEqual(mixed_runs[:server.REST_BATCHES], plain_runs)
        self.assertEqual([run[-3:] for run in mixed_runs[server.REST_BATCHES:]], [
            ["-run", "^(TestDB1)$", "./internal/db"],
            ["-run", "^(TestDB2)$", "./internal/db"],
            ["-run", "^(TestDev1)$", "./internal/devices"],
            ["-run", "^(TestDev2)$", "./internal/devices"],
        ])
        self.assertEqual(server.rest_batches([]), [])

    def test_a_partitioned_package_never_runs_without_its_partitions(self):
        for packages, shard in ((self.node, "node"), (["example/server/internal/assistant"], "managers")):
            with self.assertRaises(ValueError):
                server.commands(packages, shard, "/tmp/db.json", {})
            with self.assertRaises(ValueError):
                server.commands(packages, shard, "/tmp/db.json")

    def test_partition_listing_uses_the_same_server_test_env_prefix_as_partition_runs(self):
        db = "/tmp/db.json"
        prefix = server.server_test_env(db)
        for shard in (*server.SHARDS, server.AFFECTED):
            self.assertEqual(server.go_test(shard, db)[:len(prefix)], prefix)
        listing = "TestA\nTestB\nok  \texample/server/internal/assistant\t0.1s\n"
        with mock.patch.object(server.go_partitions.subprocess, "check_output", return_value=listing) as listed:
            patterns = server.list_patterns("./internal/assistant", db)
        self.assertEqual(patterns, ["^(TestA)$", "^(TestB)$"])
        self.assertEqual(listed.call_args.args[0], [*prefix, "go", "test", "-list", ".*", "./internal/assistant"])
        self.assertEqual(listed.call_args.kwargs["cwd"], ROOT / "server")

    def test_main_lists_partitions_through_server_test_env_before_spawning_workers(self):
        db = "/tmp/db.json"
        events = []

        def fake_check_output(command, **kwargs):
            if command[:2] == ["go", "list"]:
                return "example/server/internal/assistant\nexample/server/internal/assistant/fleet\n"
            events.append(("list", command))
            return "TestOne\nTestTwo\nok  \texample/server/internal/assistant\t0.1s\n"

        class FakeProcess:
            def __init__(self, command, **kwargs):
                events.append(("run", command))
                self.stdout = io.StringIO("")
                self.returncode = 0

            def poll(self):
                return self.returncode

            def wait(self, timeout=None):
                return self.returncode

        with mock.patch.object(sys, "argv", ["server-shards.py", "managers", "--database-file", db]), \
                mock.patch.object(server.subprocess, "check_output", side_effect=fake_check_output), \
                mock.patch.object(server.go_partitions.subprocess, "check_output", side_effect=fake_check_output), \
                mock.patch.object(server.subprocess, "Popen", side_effect=FakeProcess), \
                mock.patch.object(sys, "stdout", io.StringIO()):
            self.assertEqual(server.main(), 0)
        list_calls = [cmd for kind, cmd in events if kind == "list"]
        run_calls = [cmd for kind, cmd in events if kind == "run"]
        self.assertEqual(len(list_calls), 1)
        self.assertEqual(len(run_calls), 3)  # fleet + 2 assistant partitions
        self.assertEqual(events[0][0], "list")
        prefix = server.server_test_env(db)
        self.assertEqual(list_calls[0], [*prefix, "go", "test", "-list", ".*", "./internal/assistant"])
        for command in run_calls:
            self.assertEqual(command[:len(prefix)], prefix)

    def test_main_warms_large_rest_lists_before_spawning_workers(self):
        db = "/tmp/db.json"
        rest_pkgs = [f"example/server/internal/pkg{i:02d}" for i in range(server.REST_BATCH_MIN)]
        events = []

        def fake_check_output(command, **kwargs):
            if command[:2] == ["go", "list"]:
                return "\n".join(rest_pkgs) + "\n"
            raise AssertionError(f"unexpected check_output: {command}")

        def fake_run(command, **kwargs):
            events.append(("warm", command))
            return mock.Mock(returncode=0)

        class FakeProcess:
            def __init__(self, command, **kwargs):
                events.append(("run", command))
                self.stdout = io.StringIO("")
                self.returncode = 0

            def poll(self):
                return self.returncode

            def wait(self, timeout=None):
                return self.returncode

        with mock.patch.object(sys, "argv", ["server-shards.py", "other", "--database-file", db]), \
                mock.patch.object(server.subprocess, "check_output", side_effect=fake_check_output), \
                mock.patch.object(server.subprocess, "run", side_effect=fake_run), \
                mock.patch.object(server.subprocess, "Popen", side_effect=FakeProcess), \
                mock.patch.object(sys, "stdout", io.StringIO()):
            self.assertEqual(server.main(), 0)
        self.assertEqual([kind for kind, _ in events], ["warm"] + ["run"] * server.REST_BATCHES)
        prefix = server.server_test_env(db)
        self.assertEqual(events[0][1], [*prefix, "go", "test", "-p", "4", "-list", "^$", *rest_pkgs])


def output(test, *lines, package="example/server/internal/node"):
    return [{"Action": "output", "Package": package, "Test": test, "Output": line} for line in lines]


def ended(action, test, elapsed=0.01, package="example/server/internal/node"):
    return {"Action": action, "Package": package, "Test": test, "Elapsed": elapsed}


class ServerShardResultsTests(unittest.TestCase):
    def take(self, events):
        results = server.Results()
        printed = "".join(text for text in map(results.take, events) if text is not None)
        return results, printed

    def test_a_failed_subtest_among_passing_tests_is_repeated_with_its_first_lines(self):
        events = [*output("TestA", "=== RUN   TestA\n", "    a_test.go:3: fine\n", "--- PASS: TestA (0.00s)\n"), ended("pass", "TestA"),
                  *output("TestC", "=== RUN   TestC\n", "--- SKIP: TestC (0.00s)\n"), ended("skip", "TestC"),
                  *output("TestB", "=== RUN   TestB\n"),
                  *output("TestB/case", "=== RUN   TestB/case\n", "    b_test.go:12: got 1, want 2\n", "--- FAIL: TestB/case (0.01s)\n"),
                  ended("fail", "TestB/case"), *output("TestB", "--- FAIL: TestB (0.02s)\n"), ended("fail", "TestB", 0.02),
                  *output(None, "FAIL\n", "FAIL\texample/server/internal/node\t1.000s\n"),
                  {"Action": "fail", "Package": "example/server/internal/node", "Elapsed": 1.0}]
        results, printed = self.take(events)
        self.assertEqual(printed, "".join(e["Output"] for e in events if e["Action"] == "output"))  # the stream is unchanged
        # The parent failed only by its subtest, and the package by its tests: neither is repeated.
        self.assertEqual(results.failures(), ["--- FAIL: TestB/case (example/server/internal/node, 0.01s)",
                                              "    b_test.go:12: got 1, want 2"])
        self.assertEqual([test for _, _, test in results.slow], ["TestA", "TestB/case", "TestB"])

    def test_a_package_that_fails_outside_its_tests_shows_its_last_output(self):
        events = [*output("TestFast", "=== RUN   TestFast\n", "--- PASS: TestFast (0.00s)\n"), ended("pass", "TestFast"),
                  *output(None, *["noise %d\n" % i for i in range(30)], "panic: test timed out after 10m0s\n",
                          "\trunning tests:\n", "\t\tTestSlow (10m0s)\n", "FAIL\texample/server/internal/node\t600.100s\n"),
                  {"Action": "fail", "Package": "example/server/internal/node", "Elapsed": 600.1}]
        results, _ = self.take(events)
        self.assertEqual(results.failures(), [
            "--- FAIL: example/server/internal/node (600.10s), outside any test; its last output:",
            "noise 29", "panic: test timed out after 10m0s", "\trunning tests:", "\t\tTestSlow (10m0s)",
            "FAIL\texample/server/internal/node\t600.100s"])
        self.assertLessEqual(len(results.package_output[0, "example/server/internal/node"]), server.Results.PACKAGE_LINES)

    def test_a_timeout_or_panic_names_the_tests_still_running_with_the_panic(self):
        # go test -json files a timeout's panic under the test running then, which never gets an end event of its own.
        events = [*output("TestFast", "=== RUN   TestFast\n", "--- PASS: TestFast (0.00s)\n"), ended("pass", "TestFast"),
                  *output("TestSleepy", "=== RUN   TestSleepy\n", "    s_test.go:4: waiting\n",
                          "panic: test timed out after 3s\n", "\trunning tests:\n", "\t\tTestSleepy (3s)\n", "\n",
                          "goroutine 1 [chan receive]:\n", "testing.(*T).Run(...)\n"),
                  *output(None, "FAIL\texample/server/internal/node\t3.433s\n"),
                  {"Action": "fail", "Package": "example/server/internal/node", "Elapsed": 3.433}]
        results, _ = self.take(events)
        self.assertEqual(results.failures(), [
            "--- FAIL: TestSleepy (example/server/internal/node, did not finish)",
            "panic: test timed out after 3s", "\trunning tests:", "\t\tTestSleepy (3s)", "goroutine 1 [chan receive]:",
            "testing.(*T).Run(...)"])
        self.assertEqual(results.output, {})

    def test_a_package_that_does_not_compile_prints_and_repeats_its_build_errors(self):
        # Go 1.24+ reports compile and vet errors only as build-output events; a shard that dropped them exited in
        # seconds with no error text (internal/node's redeclared test fake, #12890).
        build = "example/server/internal/node [example/server/internal/node.test]"
        events = [{"Action": "build-output", "ImportPath": build, "Output": "# " + build + "\n"},
                  {"Action": "build-output", "ImportPath": build,
                   "Output": "internal/node/a_test.go:24:6: fakeDirEntry redeclared in this block\n"},
                  {"Action": "build-fail", "ImportPath": build},
                  {"Action": "start", "Package": "example/server/internal/node"},
                  *output(None, "FAIL\texample/server/internal/node [build failed]\n"),
                  {"Action": "fail", "Package": "example/server/internal/node", "Elapsed": 0, "FailedBuild": build}]
        results, printed = self.take(events)
        self.assertIn("fakeDirEntry redeclared in this block", printed)
        self.assertEqual(results.failures(), [
            "--- FAIL: example/server/internal/node (0.00s), outside any test; its last output:",
            "# " + build, "internal/node/a_test.go:24:6: fakeDirEntry redeclared in this block",
            "FAIL\texample/server/internal/node [build failed]"])
        self.assertEqual(results.build_output, {})

    def test_one_partition_ending_leaves_the_tests_another_is_running(self):
        # The node shard runs internal/node in several processes: the first to fail ends only its own tests.
        results = server.Results()
        for process, event in [(1, output("TestElsewhere", "=== RUN   TestElsewhere\n")[0]),
                               *[(0, e) for e in output("TestBroken", "=== RUN   TestBroken\n", "    d_test.go:2: no\n")],
                               (0, ended("fail", "TestBroken")),
                               (0, {"Action": "fail", "Package": "example/server/internal/node", "Elapsed": 1.0}),
                               (1, ended("pass", "TestElsewhere"))]:
            results.take(event, process)
        self.assertEqual(results.failures(), ["--- FAIL: TestBroken (example/server/internal/node, 0.01s)", "    d_test.go:2: no"])
        self.assertEqual(results.output, {})

    def test_failures_are_bounded_in_number_lines_and_width(self):
        events = []
        for i in range(7):
            name = "TestFail%d" % i
            events += [*output(name, "=== RUN   %s\n" % name, *["    x_test.go:%d: line %d\n" % (i, j) for j in range(8)],
                              "    x_test.go:99: " + "y" * 500 + "\n"),
                       ended("fail", name)]
        results, _ = self.take(events)
        summary = results.failures(lines=9)
        self.assertEqual(summary[0], "--- FAIL: TestFail0 (example/server/internal/node, 0.01s)")
        self.assertEqual(len(summary), 5 * 10 + 1)
        self.assertEqual(summary[-1], "... and 2 more failed")
        self.assertEqual(len(summary[9]), 300)
        self.assertEqual(len(results.failures()), 5 * 6 + 1)

    def test_the_report_ends_with_the_failures_where_a_head_and_tail_excerpt_keeps_them(self):
        events = [*output("TestSlow", "=== RUN   TestSlow\n"), ended("pass", "TestSlow", 9.5),
                  *output("TestBroken", "=== RUN   TestBroken\n", "    c_test.go:7: profile retained? no\n"),
                  ended("fail", "TestBroken", 0.2)]
        results, _ = self.take(events)
        report = results.report("node", 1, 2, 1)
        self.assertEqual(report[:3], ["Server shard node: 1 packages in 2 go test processes; exit 1",
                                      "Slowest test events (parent/subtest durations may overlap):",
                                      "SLOW 9.500s example/server/internal/node TestSlow"])
        self.assertEqual(report[-3:], ["Failed tests (repeated from above with their first lines):",
                                       "--- FAIL: TestBroken (example/server/internal/node, 0.20s)",
                                       "    c_test.go:7: profile retained? no"])
        passing, _ = self.take(events[:2])
        self.assertFalse(any(line.startswith("Failed tests") for line in passing.report("node", 1, 2, 0)))


class ShardTests(unittest.TestCase):
    def test_every_server_package_is_selected_once(self):
        packages = ["example/server/internal/" + name for name in
                    ("node", "hub", "voice", "assistant", "devices", "extmgr", "newpackage", "nodecmd", "hubedge")]
        packages += ["example/server/internal/node/newpackage", "example/server/internal/assistant/newpackage", "example/server/cmd/personad"]
        selected = [package for shard in server.SHARDS for package in server.select_packages(packages, shard)]
        self.assertCountEqual(selected, packages)
        self.assertEqual(len(selected), len(set(selected)))
        self.assertEqual(server.select_packages(packages, "node"), [packages[0], packages[-3]])
        self.assertIn("example/server/internal/hubedge", server.select_packages(packages, "other"))
        self.assertIn("example/server/internal/modules/voice", server.select_packages(packages + ["example/server/internal/modules/voice"], "managers"))
        with self.assertRaises(ValueError):
            server.select_packages(packages, "typo")

    def test_python_shards_cover_current_and_new_suites_once(self):
        suites = list(ROOT.joinpath("scripts").rglob("test_*.py"))
        suites = [suite for suite in suites if "node_modules" not in suite.parts]
        suites.append(ROOT / "scripts/test/test_new_suite.py")
        for count in (1, 2, 3):
            selected = [suite for index in range(count) for suite in python.select_suites(suites, index, count)]
            self.assertCountEqual(selected, suites)
            self.assertEqual(len(selected), len(set(selected)))
        for index, count in ((-1, 2), (2, 2), (0, 0)):
            with self.assertRaises(ValueError):
                python.select_suites(suites, index, count)

    def test_only_unittest_suites_run(self):
        # Verification scripts named test_*.py take arguments and have no tests: unittest exits 5 on them from Python 3.12.
        found = [suite for suite in ROOT.joinpath("scripts").rglob("test_*.py") if "node_modules" not in suite.parts]
        skipped = {suite.relative_to(ROOT).as_posix() for suite in found if not python.is_suite(suite)}
        self.assertTrue({"scripts/test/test_rolling_compatibility.py", "scripts/test/test_source_development_resources.py"} <= skipped)
        self.assertTrue(python.is_suite(Path(__file__)))
        self.assertTrue(python.is_suite(ROOT / "scripts/test/test_doc_paths.py"))

    def test_source_server_shard_installs_the_pinned_bundler(self):
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        setup = """      - uses: oven-sh/setup-bun@v2
        if: matrix.shard == 'other' || matrix.shard == 'core-other'
        with:
          bun-version: '1.4.2'
"""
        self.assertIn(setup, workflow)
        self.assertLess(workflow.index(setup), workflow.index("      - name: Server checks"))
        workflow = (ROOT / ".github/workflows/runtimes-test.yml").read_text()
        setup = """      - uses: oven-sh/setup-bun@v2
        with:
          bun-version: '1.4.2'
"""
        self.assertIn(setup, workflow)
        self.assertLess(workflow.index(setup), workflow.index("      - name: Runtime and SDK checks"))
        source_packages = ["example/server/internal/extension/denohost", "example/server/internal/extmgr"]
        self.assertEqual(server.select_packages(source_packages, "other"), source_packages)

    def test_server_workflow_covers_build_and_every_test_shard(self):
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        self.assertIn("shard: [node, managers, other, build, core-node, core-managers, core-other]", workflow)
        self.assertIn("fail-fast: false", workflow)
        self.assertIn('scripts/ci/server.sh "$CI_SERVER_SHARD"', workflow)
        script = (ROOT / "scripts/ci/server.sh").read_text()
        for label in ("Go formatting", "Server vet", "Server build", "Windows node build and vet", "Go SDK tests",
                      "Server programs with build-tags.txt", "Tunnel tests with build-tags.txt", "Core-only hub smoke",
                      "Core-only install from packages", "Builds without each module", "Server $shard tests with Postgres, core only"):
            self.assertIn(label, script)
        self.assertIn('lane="${1:-all}"', script)

    def test_managers_outer_budget_lets_the_inner_go_timeout_report(self):
        script = (ROOT / "scripts/ci/server.sh").read_text()
        self.assertIn("shard_budget=600", script)
        self.assertIn('if [ "$shard" = managers ]; then shard_budget=900; fi', script)
        self.assertIn('ci_run "Server $shard tests with Postgres" "$shard_budget"', script)
        self.assertIn('GOFLAGS="$GOFLAGS -tags=persona_core_only" ci_run "Server $shard tests with Postgres, core only" "$shard_budget"', script)
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        self.assertIn("timeout-minutes: 20", workflow)
        # The inner Go timeout, test selection and assertions stay unchanged: a genuinely stuck test still gets 10m.
        shards = (ROOT / "scripts/ci/server-shards.py").read_text()
        self.assertIn('"--", "go", "test", "-json", "-p", str(workers), "-count=1", "-timeout=10m"]', shards)

    def test_server_go_cache_is_saved_only_by_main_dispatches(self):
        # actions/cache saves on every ref; branch entries pushed main's out of the 10 GB repository cache.
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        self.assertNotIn("actions/cache@", workflow)
        self.assertEqual(workflow.count("actions/cache/save@v4"), 1)
        save = workflow.split("- name: Save shard Go caches", 1)[1]
        self.assertIn("if: success() && github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'", save)
        self.assertIn("key: ${{ steps.go-cache.outputs.cache-primary-key }}", save)


if __name__ == "__main__":
    unittest.main()
