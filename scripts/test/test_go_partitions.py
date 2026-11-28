"""go_partitions: internal/node's and internal/assistant's tests split into exact, disjoint -run patterns (CI shards,
release build, test-server.sh)."""
from pathlib import Path
import re
import unittest
from unittest import mock

import go_partitions as gp

SERVER = Path(__file__).resolve().parents[2] / "server"

LISTING = """TestAlpha
TestBeta
BenchmarkGamma
ExampleDelta
FuzzEpsilon
TestZeta
ok  \texample/server/internal/node\t0.123s
"""


class PartitionTests(unittest.TestCase):
    def test_names_keep_runnable_tests_and_drop_benchmarks_and_the_summary(self):
        self.assertEqual(gp.names(LISTING), ["TestAlpha", "TestBeta", "ExampleDelta", "FuzzEpsilon", "TestZeta"])

    def test_every_test_runs_in_exactly_one_partition(self):
        names = ["Test%03d" % i for i in range(101)]
        for partitions in (1, 2, 3):
            patterns = gp.patterns(names, partitions)
            self.assertEqual(len(patterns), partitions)
            matched = [[n for n in names + ["Test0010x", "XTest001"] if re.fullmatch(p, n)] for p in patterns]
            self.assertEqual(sorted(sum(matched, [])), names)  # disjoint, complete, nothing else
            self.assertLessEqual(max(map(len, matched)) - min(map(len, matched)), 1)  # round-robin

    def test_a_pattern_never_matches_a_longer_name_or_a_prefix(self):
        (pattern,) = gp.patterns(["TestA"], 1)
        self.assertEqual(pattern, "^(TestA)$")
        self.assertFalse(re.search(pattern, "TestAB"))
        self.assertFalse(re.search(pattern, "Test"))

    def test_an_empty_listing_never_becomes_an_empty_run(self):
        with self.assertRaisesRegex(ValueError, "no tests"):
            gp.patterns(gp.names("ok  \texample/server/internal/node\t0.1s\n"))

    def test_repeated_names_and_oversized_patterns_fail(self):
        with self.assertRaisesRegex(ValueError, "repeated"):
            gp.patterns(["TestA", "TestA"])
        with self.assertRaisesRegex(ValueError, "too long"):
            gp.patterns(["Test%06d%s" % (i, "x" * 40) for i in range(6000)], 2)

    def test_only_the_packages_themselves_are_partitioned(self):
        for name in ("node", "assistant", "devices", "db"):
            self.assertEqual(gp.partitioned("./internal/" + name), "./internal/" + name)
            self.assertEqual(gp.partitioned("./internal/%s/" % name), "./internal/" + name)
            self.assertEqual(gp.partitioned("github.com/example/persona/server/internal/" + name), "./internal/" + name)
        self.assertFalse(gp.is_partitioned("github.com/example/persona/server/internal/node/hostinfo"))
        self.assertFalse(gp.is_partitioned("github.com/example/persona/server/internal/nodeupdate"))
        self.assertFalse(gp.is_partitioned("github.com/example/persona/server/internal/assistantwire"))
        self.assertFalse(gp.is_partitioned("github.com/example/persona/server/internal/assistant/fleet"))
        self.assertFalse(gp.is_partitioned("github.com/example/persona/server/internal/db/dbtest"))

    def test_split_keeps_the_rest_in_order_and_names_each_partitioned_package_once(self):
        module = "github.com/example/persona/server"
        rest, parts = gp.split([module + "/internal/assistant", module + "/cmd/personad", "./internal/node/",
                                module + "/internal/node", module + "/internal/node/sub", "./internal/assistant"])
        self.assertEqual(rest, [module + "/cmd/personad", module + "/internal/node/sub"])
        self.assertEqual(parts, ["./internal/assistant", "./internal/node"])
        self.assertEqual(gp.split(["./internal/kb"]), (["./internal/kb"], []))

    def test_each_package_is_split_into_its_own_number_of_partitions(self):
        listing = "\n".join("Test%03d" % i for i in range(10)) + "\nok  \texample\t0.1s\n"
        for package, partitions in gp.PACKAGES.items():
            with mock.patch.object(gp.subprocess, "check_output", return_value=listing) as listed:
                self.assertEqual(len(gp.list_patterns("/server", package)), partitions)
            self.assertEqual(listed.call_args.args[0], ["go", "test", "-list", ".*", package])
            self.assertEqual(listed.call_args.kwargs["cwd"], "/server")

    def test_list_patterns_forwards_wrapper_prefix_and_build_flags(self):
        listing = "TestA\nTestB\nok  \texample\t0.1s\n"
        prefix = ["python3", "server_test_env.py", "--database-file", "/tmp/db.json", "--"]
        flags = ["-tags", "persona_core_only", "-race", "-trimpath"]
        env = {"GOFLAGS": "-trimpath"}
        with mock.patch.object(gp.subprocess, "check_output", return_value=listing) as listed:
            patterns = gp.list_patterns("/server", "./internal/assistant", env=env, prefix=prefix, flags=flags)
        self.assertEqual(patterns, ["^(TestA)$", "^(TestB)$"])
        self.assertEqual(listed.call_args.args[0], [*prefix, "go", "test", *flags, "-list", ".*", "./internal/assistant"])
        self.assertEqual(listed.call_args.kwargs["cwd"], "/server")
        self.assertIs(listed.call_args.kwargs["env"], env)

    def test_a_package_that_splits_itself_is_partitioned_and_still_has_its_own_split(self):
        # go_test_partitioned.py leaves these to their own TestMain under a <dir>/... argument: a package that lost its
        # split would then run serially there, so the claim is checked against the package's tests.
        self.assertTrue(gp.SPLITS_ITSELF)
        for package in gp.SPLITS_ITSELF:
            with self.subTest(package=package):
                self.assertIn(package, gp.PACKAGES)
                sources = [path.read_text() for path in sorted((SERVER / package).glob("*_test.go"))]
                self.assertTrue(any("func runPartitioned(" in source for source in sources))
                self.assertTrue(calls_from_test_main(sources, "runPartitioned"), package)

    def test_only_a_call_inside_test_main_counts_as_its_split(self):
        # The helper's own definition, a later function's call and a comment are not TestMain's call (#12765): a match
        # that ran on past TestMain's end once let a TestMain without the split pass.
        test_main = "func TestMain(m *testing.M) {\n%s\tos.Exit(m.Run())\n}\n"
        helper = "func runPartitioned() (code int, ran bool) {\n\treturn 0, false\n}\n"
        split = test_main % "\tif code, ran := runPartitioned(); ran {\n\t\tos.Exit(code)\n\t}\n"
        for sources, calls in (([split, helper], True), ([helper, split], True), ([test_main % "", helper], False),
                               ([test_main % "\t// runPartitioned() splits the run\n", helper], False),
                               ([test_main % "", "func later() {\n\trunPartitioned()\n}\n"], False),
                               ([helper], False)):
            with self.subTest(sources=sources):
                self.assertEqual(calls_from_test_main(sources, "runPartitioned"), calls)


def calls_from_test_main(sources, function):
    """Whether the gofmt-formatted TestMain among sources (one string per file) calls function(): its body ends at the
    first closing brace in column one, and line comments do not count."""
    for source in sources:
        match = re.search(r"(?ms)^func TestMain\(.*?^}$", source)
        if match:
            body = re.sub(r"//.*", "", match.group(0))
            return re.search(r"\b%s\(\)" % re.escape(function), body) is not None
    return False

if __name__ == "__main__":
    unittest.main()
