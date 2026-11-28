"""go_test_partitioned: test-server.sh's and check-fast's go test runs internal/node, internal/assistant and the other
partitioned packages in go_partitions' partitions (#5594, #7644, #12765)."""
import io
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import go_partitions
import go_test_partitioned as gtp

MODULE = ["example/server/cmd/personad", "example/server/internal/assistant", "example/server/internal/assistantwire",
          "example/server/internal/node", "example/server/internal/node/sub"]
NODE, ASSISTANT = "./internal/node", "./internal/assistant"
# The fixture module has no internal/hub, internal/devices, internal/extmgr, internal/modules/voice or internal/db:
# they split the way internal/assistant does.
PATTERNS = {NODE: ["^(TestA|TestC)$", "^(TestB)$"], ASSISTANT: ["^(TestX|TestZ)$", "^(TestY)$"],
            "./internal/hub": ["^(TestH)$"], "./internal/devices": ["^(TestD)$"], "./internal/extmgr": ["^(TestM)$"],
            "./internal/modules/voice": ["^(TestV)$"], "./internal/db": ["^(TestDB)$"]}
PREFIX = "example/server/"
# MODULE with subpackages of internal/assistant, for the ./<dir>/... arguments check-fast passes (#12765).
TREE = MODULE + [PREFIX + "internal/assistant/inbox", PREFIX + "internal/assistant/inbox/item"]
ASSISTANT_TREE = "./internal/assistant/..."


def lister(module):
    """go list over a fixture module: ./... is all of it, ./<dir>/... the packages at and under <dir>."""
    def go_list(patterns):
        found = []
        for pattern in patterns:
            directory = "" if pattern == "./..." else PREFIX + pattern[len("./"):-len("/...")]
            found += [p for p in module if not directory or p == directory or p.startswith(directory + "/")]
        return list(dict.fromkeys(found))
    return go_list


def plan(argv, module=MODULE):
    return gtp.plan(argv, lister(module), lambda package: list(PATTERNS[package]))


def partitions(flags, package, trailing=()):
    return [["go", "test", *flags, "-run", pattern, package, *trailing] for pattern in PATTERNS[package]]


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
        self.assertEqual(plan(["go", "test", "./internal/kb/", "./internal/node/", "./internal/assistant"]),
                         [["go", "test", "./internal/kb/"], *partitions([], NODE), *partitions([], ASSISTANT)])

    def test_run_or_other_packages_or_another_command_run_as_given(self):
        for argv in (["go", "test", "-run", "TestX", "./..."], ["go", "test", "-run=TestX", "./internal/node/"],
                     ["go", "test", "-run", "TestX", "./internal/assistant"], ["go", "test", "-run", "TestX", ASSISTANT_TREE],
                     ["go", "test", "-count=1", "./internal/kb/"],
                     ["go", "test", "./internal/node/sub"], ["go", "test", "./internal/assistantwire"],
                     ["go", "vet", "./..."]):
            self.assertEqual(plan(argv), [argv], argv)

    def test_flag_values_are_not_packages(self):
        flags, packages = gtp.split(["-coverprofile", "/tmp/cover.out", "-skip", "A|B", "-timeout=20m", "./..."])
        self.assertEqual((flags, packages), (["-coverprofile", "/tmp/cover.out", "-skip", "A|B", "-timeout=20m"],
                                             ["./..."]))

    def test_coverprofile_runs_directly_before_package_discovery(self):
        for alias in ("-coverprofile", "--coverprofile", "-test.coverprofile", "--test.coverprofile"):
            for form in ((alias, "/tmp/c"), (alias + "=/tmp/c",)):
                for package in ("./...", NODE, ASSISTANT_TREE):
                    with self.subTest(form=form, package=package):
                        argv = ["go", "test", *form, package]
                        list_packages = mock.Mock(return_value=list(MODULE))
                        list_patterns = mock.Mock(side_effect=lambda name: list(PATTERNS[name]))
                        self.assertEqual(gtp.plan(argv, list_packages, list_patterns), [argv])
                        list_packages.assert_not_called()
                        list_patterns.assert_not_called()

    def test_coverprofile_tokens_in_values_and_double_dash_args_are_not_coverage(self):
        for flag in ("-skip", "--skip", "-run", "--run", "-exec", "--exec", "-outputdir", "-test.list"):
            for value in ("-coverprofile", "--test.coverprofile"):
                with self.subTest(flag=flag, value=value):
                    self.assertFalse(gtp.has_coverage_profile([flag, value, "./..."]))
        for args in (["./...", "--", "-coverprofile"], [NODE, "--", "--test.coverprofile=/tmp/c"]):
            with self.subTest(args=args):
                self.assertFalse(gtp.has_coverage_profile(args))

    def test_args_test_coverprofile_is_coverage(self):
        for terminator in ("-args", "--args"):
            for form in (("-test.coverprofile", "/tmp/c"), ("-test.coverprofile=/tmp/c",)):
                with self.subTest(terminator=terminator, form=form):
                    self.assertTrue(gtp.has_coverage_profile([NODE, terminator, *form]))

    def test_skip_and_exec_values_that_look_like_coverprofile_keep_the_partition_plan(self):
        for flag in ("-skip", "--skip", "-exec", "--exec", "-outputdir", "-test.list"):
            for value in ("-coverprofile", "--test.coverprofile"):
                flags = [flag, value]
                with self.subTest(flags=flags):
                    self.assertEqual(plan(["go", "test", *flags, NODE]), partitions(flags, NODE))

    def test_run_value_that_looks_like_coverprofile_still_narrows_without_coverage(self):
        argv = ["go", "test", "-run", "-coverprofile", "./..."]
        self.assertFalse(gtp.has_coverage_profile(argv[2:]))
        self.assertEqual(plan(argv), [argv])

    def test_double_dash_args_stay_after_generated_partition_flags_and_packages(self):
        trailing = ["--", "-coverprofile"]
        self.assertEqual(plan(["go", "test", NODE, *trailing]), partitions([], NODE, trailing))
        self.assertEqual(plan(["go", "test", "./...", *trailing]), [
            ["go", "test", "example/server/cmd/personad", "example/server/internal/assistantwire",
             "example/server/internal/node/sub", *trailing],
            *partitions([], ASSISTANT, trailing), *partitions([], NODE, trailing),
        ])

    def test_args_passthrough_runs_directly_before_package_discovery(self):
        trailing_forms = ((),
                          ("-test.coverprofile", "/tmp/c"),
                          ("-test.coverprofile=/tmp/c",),
                          ("-test.run", "TestAlpha"),
                          ("-test.run=TestAlpha",),
                          ("-test.v",))
        for terminator in ("-args", "--args"):
            for trailing in trailing_forms:
                for package in ("./...", NODE, ASSISTANT_TREE):
                    with self.subTest(terminator=terminator, trailing=trailing, package=package):
                        argv = ["go", "test", package, terminator, *trailing]
                        list_packages = mock.Mock(return_value=list(MODULE))
                        list_patterns = mock.Mock(side_effect=lambda name: list(PATTERNS[name]))
                        self.assertEqual(gtp.plan(argv, list_packages, list_patterns), [argv])
                        list_packages.assert_not_called()
                        list_patterns.assert_not_called()

    def test_empty_coverprofile_values_keep_the_partition_plan(self):
        for alias in ("-coverprofile", "--coverprofile", "-test.coverprofile", "--test.coverprofile"):
            for flags in ([alias + "="], [alias, ""]):
                with self.subTest(flags=flags):
                    self.assertEqual(plan(["go", "test", *flags, NODE]), partitions(flags, NODE))

    def test_last_coverprofile_value_wins_across_aliases(self):
        for empty, nonempty in (("-coverprofile=", "--test.coverprofile=/tmp/c"),
                                ("--test.coverprofile=", "-coverprofile=/tmp/c")):
            with self.subTest(empty=empty, nonempty=nonempty):
                self.assertEqual(plan(["go", "test", empty, NODE]), partitions([empty], NODE))
                argv = ["go", "test", nonempty, empty, NODE]
                self.assertEqual(plan(argv), partitions([nonempty, empty], NODE))
                argv = ["go", "test", empty, nonempty, NODE]
                self.assertEqual(plan(argv), [argv])

    def test_goflags_coverprofile_runs_directly_before_package_discovery(self):
        for alias in ("-coverprofile", "--coverprofile", "-test.coverprofile", "--test.coverprofile"):
            for goflags in (alias + "=/tmp/c", "'" + alias + "=/tmp/a b/c.out'",
                            '"' + alias + '=/tmp/a b/c.out"'):
                for package in ("./...", NODE, ASSISTANT_TREE):
                    with self.subTest(goflags=goflags, package=package):
                        argv = ["go", "test", package]
                        list_packages = mock.Mock(return_value=list(MODULE))
                        list_patterns = mock.Mock(side_effect=lambda name: list(PATTERNS[name]))
                        self.assertEqual(gtp.plan(argv, list_packages, list_patterns, goflags=goflags), [argv])
                        list_packages.assert_not_called()
                        list_patterns.assert_not_called()

    def test_goflags_empty_or_overridden_coverprofile_keeps_the_partition_plan(self):
        for alias in ("-coverprofile", "--coverprofile", "-test.coverprofile", "--test.coverprofile"):
            for goflags, flags in ((alias + "=", []),
                                   (alias + "=/tmp/from-env", [alias + "="]),
                                   (alias + "=/tmp/from-env", ["-coverprofile", ""])):
                with self.subTest(goflags=goflags, flags=flags):
                    argv = ["go", "test", *flags, NODE]
                    self.assertEqual(gtp.plan(argv, lister(MODULE), lambda package: list(PATTERNS[package]),
                                              goflags=goflags), partitions(flags, NODE))

    def test_goflags_explicit_nonempty_profile_override_runs_directly(self):
        argv = ["go", "test", "-test.coverprofile=/tmp/from-cli", NODE]
        list_packages = mock.Mock(return_value=list(MODULE))
        list_patterns = mock.Mock(return_value=list(PATTERNS[NODE]))
        self.assertEqual(gtp.plan(argv, list_packages, list_patterns, goflags="-coverprofile=/tmp/from-env"), [argv])
        list_packages.assert_not_called()
        list_patterns.assert_not_called()

    def test_goflags_values_and_ordinary_noncoverage_flags_do_not_false_positive(self):
        for goflags in ("-skip=-coverprofile", "-run=-test.coverprofile", "-exec=-coverprofile",
                        "-mod=readonly -count=1", "-ldflags=-w"):
            with self.subTest(goflags=goflags):
                argv = ["go", "test", NODE]
                commands = gtp.plan(argv, lister(MODULE), lambda package: list(PATTERNS[package]),
                                    goflags=goflags)
                if goflags.startswith("-run="):
                    self.assertEqual(commands, [argv])
                else:
                    self.assertEqual(commands, partitions([], NODE))

    def test_invalid_goflags_and_unknown_command_flags_run_directly(self):
        argv = ["go", "test", NODE]
        for goflags in ("-coverprofile /tmp/c", "'-coverprofile=/tmp/unterminated", "not-a-flag"):
            with self.subTest(goflags=goflags):
                list_packages = mock.Mock(return_value=list(MODULE))
                list_patterns = mock.Mock(return_value=list(PATTERNS[NODE]))
                self.assertEqual(gtp.plan(argv, list_packages, list_patterns, goflags=goflags), [argv])
                list_packages.assert_not_called()
                list_patterns.assert_not_called()
        for argv in (["go", "test", "-custom", "value", NODE], ["go", "test", NODE, "-custom=value"],
                     ["go", "test", "-coverprofile"]):
            with self.subTest(argv=argv):
                list_packages = mock.Mock(return_value=list(MODULE))
                list_patterns = mock.Mock(return_value=list(PATTERNS[NODE]))
                self.assertEqual(gtp.plan(argv, list_packages, list_patterns), [argv])
                list_packages.assert_not_called()
                list_patterns.assert_not_called()

    def test_covermode_without_coverprofile_keeps_the_partition_plan(self):
        flags = ["-covermode=atomic"]
        self.assertEqual(plan(["go", "test", *flags, "./..."]), [
            ["go", "test", *flags, "example/server/cmd/personad", "example/server/internal/assistantwire",
             "example/server/internal/node/sub"],
            *partitions(flags, ASSISTANT), *partitions(flags, NODE),
        ])

    def test_a_partition_is_described_by_its_package_and_size_not_its_names(self):
        self.assertEqual(gtp.describe(["go", "test", "-run", "^(TestA|TestB|TestC)$", NODE]),
                         "./internal/node partition (3 tests)")
        self.assertEqual(gtp.describe(["go", "test", "-run", "^(TestX)$", ASSISTANT]),
                         "./internal/assistant partition (1 tests)")
        self.assertEqual(gtp.describe(["go", "test", "-count=1", "./internal/hub", "./internal/kb"]), "2 package(s)")

    def test_build_flags_forwards_only_build_affecting_flags_to_discovery(self):
        flags = [
            "-count=1", "-p", "1", "-timeout", "290s", "-v", "-skip", "-race",
            "-tags", "persona_core_only", "--tags=extra", "-race", "-trimpath=false",
            "-covermode=atomic", "-gcflags=all=-N", "-ldflags", "-w",
            "-a", "-x", "-json", "-o", "/tmp/pkg.test", "-coverprofile", "",
        ]
        self.assertEqual(
            gtp.build_flags(flags),
            ["-tags", "persona_core_only", "--tags=extra", "-race", "-trimpath=false",
             "-covermode=atomic", "-gcflags=all=-N", "-ldflags", "-w"],
        )

    def test_main_forwards_build_flags_to_partition_listing(self):
        argv = ["go", "test", "-count=1", "-tags", "persona_core_only", "-race", "-trimpath", ASSISTANT]
        with mock.patch.dict(os.environ, {}, clear=False), \
                mock.patch.object(go_partitions, "list_patterns", return_value=["^(TestX)$", "^(TestY)$"]) as listed, \
                mock.patch.object(gtp, "run", return_value=0) as runner:
            os.environ.pop("GOFLAGS", None)
            self.assertEqual(gtp.main(argv), 0)
        listed.assert_called_once_with(
            os.getcwd(), ASSISTANT, env=None, flags=["-tags", "persona_core_only", "-race", "-trimpath"],
        )
        self.assertEqual(runner.call_args.args[0], [
            ["go", "test", "-count=1", "-tags", "persona_core_only", "-race", "-trimpath", "-run", "^(TestX)$", ASSISTANT],
            ["go", "test", "-count=1", "-tags", "persona_core_only", "-race", "-trimpath", "-run", "^(TestY)$", ASSISTANT],
        ])


class SubtreePlanTests(unittest.TestCase):
    """A ./<dir>/... argument that covers a partitioned package, as check-fast's Go checks pass it (#12765)."""

    def test_the_assistant_subtree_runs_the_assistant_in_partitions_beside_its_subpackages(self):
        flags = ["-count=1", "-timeout", "290s"]
        self.assertEqual(plan(["go", "test", *flags, ASSISTANT_TREE], TREE), [
            ["go", "test", *flags, PREFIX + "internal/assistant/inbox", PREFIX + "internal/assistant/inbox/item"],
            *partitions(flags, ASSISTANT),
        ])

    def test_a_subtree_with_no_other_package_runs_only_the_partitions(self):
        self.assertEqual(plan(["go", "test", ASSISTANT_TREE]), partitions([], ASSISTANT))

    def test_a_wider_subtree_leaves_a_package_that_splits_itself_to_its_own_split(self):
        # internal/node's TestMain splits a plain run of it into up to six processes; three -run partitions would be
        # slower on a machine with eight CPUs or more. It stays with the other packages, after them.
        self.assertEqual(go_partitions.SPLITS_ITSELF, {NODE})
        self.assertEqual(plan(["go", "test", "-count=1", "./internal/..."], TREE), [
            ["go", "test", "-count=1", PREFIX + "internal/assistantwire", PREFIX + "internal/node/sub",
             PREFIX + "internal/assistant/inbox", PREFIX + "internal/assistant/inbox/item", PREFIX + "internal/node"],
            *partitions(["-count=1"], ASSISTANT),
        ])

    def test_subtrees_beside_named_packages(self):
        self.assertEqual(plan(["go", "test", "./internal/kb/", ASSISTANT_TREE, "./internal/node"], TREE), [
            ["go", "test", PREFIX + "internal/assistant/inbox", PREFIX + "internal/assistant/inbox/item",
             "./internal/kb/"],
            *partitions([], ASSISTANT), *partitions([], NODE),
        ])
        # internal/node named on its own is partitioned as before, and does not run a second time from the subtree.
        self.assertEqual(plan(["go", "test", "./internal/...", "./internal/node/"], TREE), [
            ["go", "test", PREFIX + "internal/assistantwire", PREFIX + "internal/node/sub",
             PREFIX + "internal/assistant/inbox", PREFIX + "internal/assistant/inbox/item"],
            *partitions([], ASSISTANT), *partitions([], NODE),
        ])
        # Another subtree stays a pattern in the other packages' command; go test expands it.
        self.assertEqual(plan(["go", "test", "./internal/kb/...", ASSISTANT_TREE], TREE), [
            ["go", "test", PREFIX + "internal/assistant/inbox", PREFIX + "internal/assistant/inbox/item",
             "./internal/kb/..."],
            *partitions([], ASSISTANT),
        ])

    def test_a_subtree_beside_the_whole_module_does_not_run_twice(self):
        self.assertEqual(plan(["go", "test", "./...", ASSISTANT_TREE], TREE), plan(["go", "test", "./..."], TREE))

    def test_subtrees_that_cover_no_package_the_runner_splits_run_as_given_without_go_list(self):
        for package in ("./internal/kb/...", "./cmd/...", "./internal/assistantwire/...",
                        "./internal/assistant/inbox/...", "./internal/node/...", "./internal/node/sub/...",
                        "internal/assistant/...", "github.com/example/server/internal/assistant/...",
                        "./internal/.../assistant/...", "../server/internal/assistant/..."):
            with self.subTest(package=package):
                argv = ["go", "test", "-count=1", package]
                list_packages = mock.Mock(side_effect=lister(TREE))
                self.assertEqual(gtp.plan(argv, list_packages, lambda name: list(PATTERNS[name])), [argv])
                list_packages.assert_not_called()

    def test_a_subtree_where_go_list_finds_no_partitioned_package_runs_as_given(self):
        argv = ["go", "test", "./internal/..."]
        self.assertEqual(plan(argv, [PREFIX + "internal/kb", PREFIX + "internal/node"]), [argv])

    def test_explicit_packages_and_the_whole_module_list_what_they_did(self):
        # The plans above them are pinned by PlanTests; the go list each one makes is pinned here.
        for argv, listed in ((["go", "test", "./internal/kb/", "./internal/node/", "./internal/assistant"], None),
                             (["go", "test", "-count=1", "-p", "1", "./..."], ["./..."]),
                             (["go", "test", "./...", ASSISTANT_TREE], ["./..."]),
                             (["go", "test", ASSISTANT_TREE, "./internal/..."], [ASSISTANT_TREE, "./internal/..."])):
            with self.subTest(argv=argv):
                list_packages = mock.Mock(side_effect=lister(TREE))
                gtp.plan(argv, list_packages, lambda name: list(PATTERNS[name]))
                if listed is None:
                    list_packages.assert_not_called()
                else:
                    list_packages.assert_called_once_with(listed)

    def test_a_subtree_names_its_directory(self):
        for token, directory in (("./internal/assistant/...", "internal/assistant"), ("./internal/...", "internal"),
                                 ("./internal/assistant//...", "internal/assistant"), ("./...", None),
                                 ("./internal/assistant", None), ("internal/assistant/...", None),
                                 ("./internal/.../x/...", None), ("./../server/...", None)):
            with self.subTest(token=token):
                self.assertEqual(gtp.subtree(token), directory)


class CoverageIntegrationTests(unittest.TestCase):
    """A real go test comparison in a temporary module outside the repository."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="go-test-partitioned-cover-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = os.environ.copy()
        self.env.pop("GOFLAGS", None)
        # Go chooses the fixture's declared minimum on older hosts, as it does for the repository itself.
        self.env.update(GOTOOLCHAIN="auto", GOMAXPROCS="2")
        (self.root / "go.mod").write_text("module example.com/coverfixture\n\ngo 1.27.1\n")
        self.write("internal/node/node.go", """package node

func Alpha() int {
	return 1
}

func Beta() int {
	return 2
}
""")
        self.write("internal/node/node_test.go", """package node

import "testing"

func TestAlpha(t *testing.T) {
	if Alpha() != 1 {
		t.Fatal("Alpha")
	}
}

func TestBeta(t *testing.T) {
	if Beta() != 2 {
		t.Fatal("Beta")
	}
}
""")
        self.write("internal/other/other.go", """package other

func Value() int {
	return 3
}
""")
        self.write("internal/other/other_test.go", """package other

import "testing"

func TestValue(t *testing.T) {
	if Value() != 3 {
		t.Fatal("Value")
	}
}
""")

    def test_fixture_uses_the_module_minimum_instead_of_an_older_local_go(self):
        self.assertEqual(self.env["GOTOOLCHAIN"], "auto")

    def write(self, relative, contents):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(contents)

    def run_command(self, command, env=None):
        return subprocess.run(command, cwd=self.root, env=env or self.env, stdout=subprocess.PIPE,
                              stderr=subprocess.STDOUT, text=True, timeout=120)

    def check_profile(self, path, mode, packages):
        lines = path.read_text().splitlines()
        self.assertTrue(lines)
        self.assertEqual(lines[0], "mode: " + mode)
        self.assertEqual(sum(line.startswith("mode:") for line in lines), 1)
        covered = {line.split(":", 1)[0].rsplit("/", 1)[0] for line in lines[1:] if line}
        self.assertEqual(covered, set(packages))
        result = self.run_command(["go", "tool", "cover", "-func=" + str(path)])
        self.assertEqual(result.returncode, 0, result.stdout)

    def compare_direct_and_wrapper(self, form, mode, package, passing=True, alias="-coverprofile", goflags=False):
        direct_profile = self.root / "direct.out"
        wrapper_profile = self.root / "wrapper.out"
        for profile in (direct_profile, wrapper_profile):
            profile.unlink(missing_ok=True)
        direct_flag = ([] if goflags else
                       [alias, str(direct_profile)] if form == "separate" else [alias + "=" + str(direct_profile)])
        wrapper_flag = ([] if goflags else
                        [alias, str(wrapper_profile)] if form == "separate" else [alias + "=" + str(wrapper_profile)])
        direct_env = self.env.copy()
        wrapper_env = self.env.copy()
        if goflags:
            direct_env["GOFLAGS"] = alias + "=" + str(direct_profile)
            wrapper_env["GOFLAGS"] = alias + "=" + str(wrapper_profile)
        base = ["go", "test", "-count=1", "-p", "1", "-covermode=" + mode]
        direct = self.run_command([*base, *direct_flag, package], env=direct_env)
        wrapper = self.run_command([sys.executable, "-I", str(Path(gtp.__file__).resolve()),
                                    *base, *wrapper_flag, package], env=wrapper_env)
        if passing:
            self.assertEqual(direct.returncode, 0, direct.stdout)
        else:
            self.assertNotEqual(direct.returncode, 0, direct.stdout)
        self.assertEqual(wrapper.returncode, direct.returncode, direct.stdout + wrapper.stdout)
        self.assertEqual(wrapper_profile.read_bytes(), direct_profile.read_bytes())
        packages = (["example.com/coverfixture/internal/node"] if package == NODE else
                    ["example.com/coverfixture/internal/node", "example.com/coverfixture/internal/other"])
        for profile in (direct_profile, wrapper_profile):
            self.check_profile(profile, mode, packages)

    def test_profiles_match_direct_go_for_every_flag_form_mode_and_package_selection(self):
        for form in ("separate", "equals"):
            for mode in ("set", "count", "atomic"):
                for package in ("./...", NODE):
                    with self.subTest(form=form, mode=mode, package=package):
                        self.compare_direct_and_wrapper(form, mode, package)

    def test_all_coverprofile_aliases_and_forms_match_direct_go(self):
        for alias in ("-coverprofile", "--coverprofile", "-test.coverprofile", "--test.coverprofile"):
            for form in ("separate", "equals"):
                with self.subTest(alias=alias, form=form):
                    self.compare_direct_and_wrapper(form, "atomic", NODE, alias=alias)

    def test_goflags_coverprofile_aliases_match_direct_go(self):
        for alias in ("-coverprofile", "--coverprofile", "-test.coverprofile", "--test.coverprofile"):
            for mode in ("set", "count", "atomic"):
                with self.subTest(alias=alias, mode=mode):
                    self.compare_direct_and_wrapper("equals", mode, NODE, alias=alias, goflags=True)

    def test_empty_goflags_profile_can_be_overridden_without_creating_a_file(self):
        self.env["GOFLAGS"] = "-coverprofile=" + str(self.root / "unused.out")
        direct = self.run_command(["go", "test", "-count=1", "-p", "1", "-test.coverprofile=", NODE])
        wrapper = self.run_command([sys.executable, "-I", str(Path(gtp.__file__).resolve()),
                                    "go", "test", "-count=1", "-p", "1", "-test.coverprofile=", NODE])
        self.assertEqual(direct.returncode, 0, direct.stdout)
        self.assertEqual(wrapper.returncode, direct.returncode, direct.stdout + wrapper.stdout)
        self.assertFalse((self.root / "unused.out").exists())

    def test_a_failing_test_preserves_the_exit_status_and_profile(self):
        self.write("internal/node/failure_test.go", """package node

import "testing"

func TestFailure(t *testing.T) {
	t.Fatal("boom")
}
""")
        self.compare_direct_and_wrapper("equals", "atomic", NODE, passing=False)
        self.compare_direct_and_wrapper("separate", "count", NODE, passing=False, alias="--test.coverprofile")
        self.compare_direct_and_wrapper("equals", "set", NODE, passing=False, alias="--coverprofile", goflags=True)

    def test_args_coverprofile_matches_direct_go(self):
        for terminator in ("-args", "--args"):
            for form in ("separate", "equals"):
                with self.subTest(terminator=terminator, form=form):
                    direct_profile = self.root / "direct.out"
                    wrapper_profile = self.root / "wrapper.out"
                    for profile in (direct_profile, wrapper_profile):
                        profile.unlink(missing_ok=True)
                    direct_trailing = ([terminator, "-test.coverprofile", str(direct_profile)] if form == "separate"
                                       else [terminator, "-test.coverprofile=" + str(direct_profile)])
                    wrapper_trailing = ([terminator, "-test.coverprofile", str(wrapper_profile)] if form == "separate"
                                        else [terminator, "-test.coverprofile=" + str(wrapper_profile)])
                    base = ["go", "test", "-count=1", "-p", "1", "-cover", NODE]
                    direct = self.run_command([*base, *direct_trailing])
                    wrapper = self.run_command([sys.executable, "-I", str(Path(gtp.__file__).resolve()),
                                                *base, *wrapper_trailing])
                    self.assertEqual(direct.returncode, 0, direct.stdout)
                    self.assertEqual(wrapper.returncode, direct.returncode, direct.stdout + wrapper.stdout)
                    direct_lines = direct_profile.read_text().splitlines()
                    self.assertEqual(direct_lines[0], "mode: set")
                    self.assertEqual(len(direct_lines[1:]), 2, direct_profile.read_text())
                    self.assertTrue(all(line.endswith(" 1") for line in direct_lines[1:]), direct_profile.read_text())
                    self.assertEqual(wrapper_profile.read_text(), direct_profile.read_text())

    def test_args_test_run_matches_direct_go(self):
        for terminator in ("-args", "--args"):
            for form in ("separate", "equals"):
                with self.subTest(terminator=terminator, form=form):
                    trailing = ([terminator, "-test.run", "TestAlpha"] if form == "separate"
                                else [terminator, "-test.run=TestAlpha"])
                    base = ["go", "test", "-count=1", "-p", "1", "-v", NODE]
                    direct = self.run_command([*base, *trailing])
                    wrapper = self.run_command([sys.executable, "-I", str(Path(gtp.__file__).resolve()),
                                                *base, *trailing])
                    self.assertEqual(direct.returncode, 0, direct.stdout)
                    self.assertEqual(wrapper.returncode, direct.returncode, direct.stdout + wrapper.stdout)
                    self.assertEqual(direct.stdout.count("=== RUN   TestAlpha"), 1, direct.stdout)
                    self.assertNotIn("=== RUN   TestBeta", direct.stdout)
                    self.assertEqual(wrapper.stdout.count("=== RUN   TestAlpha"), 1, wrapper.stdout)
                    self.assertNotIn("=== RUN   TestBeta", wrapper.stdout)

    def test_noncoverage_coverprofile_tokens_keep_real_partitioning(self):
        for args in (["-skip", "-coverprofile", "./..."], ["./...", "--", "-coverprofile"]):
            with self.subTest(args=args):
                result = self.run_command([sys.executable, "-I", str(Path(gtp.__file__).resolve()),
                                           "go", "test", "-count=1", "-p", "1", *args])
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertIn("== go test:", result.stdout)
                self.assertFalse(list(self.root.glob("*.out")))


class SubtreeIntegrationTests(unittest.TestCase):
    """A real go list and go test of a ./<dir>/... argument in a temporary module outside the repository (#12765)."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="go-test-partitioned-subtree-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = os.environ.copy()
        self.env.pop("GOFLAGS", None)
        # Go chooses the fixture's declared minimum on older hosts, as it does for the repository itself.
        self.env.update(GOTOOLCHAIN="auto", GOMAXPROCS="2")
        (self.root / "go.mod").write_text("module example.com/subtreefixture\n\ngo 1.27.1\n")
        tests = {"internal/assistant/assistant_test.go": ("assistant", ["TestA1", "TestA2", "TestA3", "TestA4"]),
                 "internal/assistant/inbox/inbox_test.go": ("inbox", ["TestInbox"]),
                 "internal/node/node_test.go": ("node", ["TestNode"])}
        for relative, (package, names) in tests.items():
            path = self.root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("package %s\n\nimport \"testing\"\n\n" % package +
                            "".join("func %s(t *testing.T) {}\n\n" % name for name in names))

    def run_wrapper(self, *packages):
        return subprocess.run([sys.executable, "-I", str(Path(gtp.__file__).resolve()), "go", "test", "-count=1", "-v",
                               *packages], cwd=self.root, env=self.env, stdout=subprocess.PIPE,
                              stderr=subprocess.STDOUT, text=True, timeout=180)

    def test_the_subtree_runs_each_test_once_with_the_partitioned_package_in_partitions(self):
        result = self.run_wrapper("./internal/...")
        self.assertEqual(result.returncode, 0, result.stdout)
        # go_partitions.PACKAGES' partitions, round-robin, one line as each starts and one as it ends: at most one per
        # test of the fixture's four.
        partitions = len(go_partitions.patterns(["TestA1", "TestA2", "TestA3", "TestA4"],
                                                go_partitions.PACKAGES["./internal/assistant"]))
        self.assertEqual(result.stdout.count("== go test: ./internal/assistant partition ("), 2 * partitions, result.stdout)
        for name in ("TestA1", "TestA2", "TestA3", "TestA4", "TestInbox", "TestNode"):
            self.assertEqual(result.stdout.count("=== RUN   %s\n" % name), 1, result.stdout)
        # internal/node splits itself (go_partitions.SPLITS_ITSELF): it runs with the other packages, not in partitions.
        self.assertNotIn("./internal/node partition", result.stdout)

    def test_a_failing_partition_fails_the_run(self):
        path = self.root / "internal/assistant/failure_test.go"
        path.write_text('package assistant\n\nimport "testing"\n\nfunc TestBroken(t *testing.T) { t.Fatal("boom") }\n')
        result = self.run_wrapper("./internal/assistant/...")
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("--- FAIL: TestBroken", result.stdout)
        self.assertRegex(result.stdout, r"(?m)^FAIL\texample\.com/subtreefixture/internal/assistant\t")
        self.assertRegex(result.stdout, r"(?m)^ok  \texample\.com/subtreefixture/internal/assistant/inbox\t")


@unittest.skipIf(os.name == "nt", "the runner's exec and a shell go that kills itself need POSIX")
class ExecTests(unittest.TestCase):
    """A command the runner does not split replaces the runner (os.execvp), so check-fast's timeout and heavy slot wrap
    go itself: a go killed by SIGKILL (the OOM killer) still reads as killed, which check-fast's TIMEOUT rule for 137
    relies on. Run as a child instead, go's death would become python's exit status 247 and that rule a FAIL."""

    def test_a_command_run_as_given_is_the_callers_child_and_keeps_its_signal_status(self):
        temporary = tempfile.TemporaryDirectory(prefix="go-test-partitioned-exec-")
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        pid_file = root / "go.pid"
        go = root / "bin/go"
        go.parent.mkdir()
        go.write_text('#!/bin/sh\nprintf "%%s\\n" "$$" > "%s"\nkill -KILL "$$"\n' % pid_file)
        go.chmod(0o755)
        env = dict(os.environ, PATH="%s%s%s" % (go.parent, os.pathsep, os.environ.get("PATH", "")))
        env.pop("GOFLAGS", None)
        # ./internal/foo is no package the runner splits: plan() returns it as given without running go list.
        process = subprocess.Popen([sys.executable, "-I", str(Path(gtp.__file__).resolve()), "go", "test", "-count=1",
                                    "./internal/foo"], cwd=root, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True)
        out, _ = process.communicate(timeout=60)
        self.assertEqual(-signal.SIGKILL, process.returncode, out)
        self.assertEqual(str(process.pid), pid_file.read_text().strip(), out)  # go is the process the caller started


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
