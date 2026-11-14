"""go_test_partitioned: test-server.sh's go test runs internal/node and internal/assistant in go_partitions'
partitions (#5594, #7644)."""
import io
import sys
import unittest
from unittest import mock

import go_partitions
import go_test_partitioned as gtp

MODULE = ["example/server/cmd/personad", "example/server/internal/assistant", "example/server/internal/assistantwire",
          "example/server/internal/node", "example/server/internal/node/sub"]
NODE, ASSISTANT = "./internal/node", "./internal/assistant"
PATTERNS = {NODE: ["^(TestA|TestC)$", "^(TestB)$"], ASSISTANT: ["^(TestX|TestZ)$", "^(TestY)$"]}


def plan(argv):
    return gtp.plan(argv, lambda: list(MODULE), lambda package: list(PATTERNS[package]))


def partitions(flags, package):
    return [["go", "test", *flags, "-run", pattern, package] for pattern in PATTERNS[package]]


class PlanTests(unittest.TestCase):
    def test_the_whole_module_runs_the_partitioned_packages_in_partitions_beside_the_rest(self):
        flags = ["-count=1", "-p", "1"]
        self.assertEqual(plan(["go", "test", *flags, "./..."]), [
            ["go", "test", *flags, "example/server/cmd/personad", "example/server/internal/assistantwire",
             "example/server/internal/node/sub"],
            *partitions(flags, ASSISTANT), *partitions(flags, NODE),
        ])
        self.assertEqual(list(PATTERNS), list(go_partitions.PACKAGES))  # the fixture covers every partitioned package

    def test_every_command_keeps_the_callers_flags(self):
        # A deploy's skip list (scripts/deploy): every partition leaves the same tests out.
        commands = plan(["go", "test", "-count=1", "-p", "1", "-skip", "TestX|TestY", "-v", "./..."])
        for command in commands:
            self.assertEqual(command[2:8], ["-count=1", "-p", "1", "-skip", "TestX|TestY", "-v"])

    def test_a_partitioned_package_named_alone_runs_only_its_partitions(self):
        for package, named in ((NODE, "./internal/node"), (NODE, "./internal/node/"), (ASSISTANT, "./internal/assistant/")):
            self.assertEqual(plan(["go", "test", "-count=1", named]), partitions(["-count=1"], package))

    def test_partitioned_packages_beside_other_packages(self):
        self.assertEqual(plan(["go", "test", "./internal/hub/", "./internal/node/", "./internal/assistant"]),
                         [["go", "test", "./internal/hub/"], *partitions([], NODE), *partitions([], ASSISTANT)])

    def test_run_or_other_packages_or_another_command_run_as_given(self):
        for argv in (["go", "test", "-run", "TestX", "./..."], ["go", "test", "-run=TestX", "./internal/node/"],
                     ["go", "test", "-run", "TestX", "./internal/assistant"], ["go", "test", "-count=1", "./internal/hub/"],
                     ["go", "test", "./internal/node/sub"], ["go", "test", "./internal/assistantwire"],
                     ["go", "vet", "./..."]):
            self.assertEqual(plan(argv), [argv], argv)

    def test_flag_values_are_not_packages(self):
        flags, packages = gtp.split(["-coverprofile", "/tmp/cover.out", "-skip", "A|B", "-timeout=20m", "./..."])
        self.assertEqual((flags, packages), (["-coverprofile", "/tmp/cover.out", "-skip", "A|B", "-timeout=20m"],
                                             ["./..."]))

    def test_a_partition_is_described_by_its_package_and_size_not_its_names(self):
        self.assertEqual(gtp.describe(["go", "test", "-run", "^(TestA|TestB|TestC)$", NODE]),
                         "./internal/node partition (3 tests)")
        self.assertEqual(gtp.describe(["go", "test", "-run", "^(TestX)$", ASSISTANT]),
                         "./internal/assistant partition (1 tests)")
        self.assertEqual(gtp.describe(["go", "test", "-count=1", "./internal/hub", "./internal/kb"]), "2 package(s)")


class RunTests(unittest.TestCase):
    def run_commands(self, commands):
        out = io.StringIO()
        with mock.patch.object(sys, "stdout", out):
            status = gtp.run(commands)
        return status, out.getvalue()

    def test_outputs_never_interleave_and_any_failure_fails(self):
        py = sys.executable
        status, text = self.run_commands([
            [py, "-c", "import time\nfor i in range(3): print('rest', i, flush=True); time.sleep(0.05)"],
            [py, "-c", "print('FAIL\\tpartition one'); raise SystemExit(1)"],
            [py, "-c", "print('ok\\tpartition two')"],
        ])
        self.assertEqual(status, 1)
        lines = text.splitlines()
        for line in ("rest 0", "rest 1", "rest 2", "FAIL\tpartition one", "ok\tpartition two"):
            self.assertIn(line, lines)
        self.assertEqual(lines.index("FAIL\tpartition one") - 1, next(
            i for i, line in enumerate(lines) if line.endswith("exit 1")))

    def test_all_passing_passes(self):
        py = sys.executable
        self.assertEqual(self.run_commands([[py, "-c", "pass"], [py, "-c", "pass"]])[0], 0)


if __name__ == "__main__":
    unittest.main()
