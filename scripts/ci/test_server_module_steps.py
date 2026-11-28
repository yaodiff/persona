from collections import Counter
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ServerModuleSteps(unittest.TestCase):
    def test_portable_jobs_and_hosted_jobs_cover_the_same_checks_once(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            for name in ("scripts/ci/server.sh", "scripts/ci/full.sh", "scripts/test/go_trimpath.sh"):
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(ROOT / name, root / name)
            (root / "scripts/ci/common.sh").write_text('''CI_IMPLEMENTATION_ROOT=$PWD
ci_run() { ci_run_step "$@"; }
ci_run_step() {
    printf '%s\\t%s\\t%s\\t%s\\t%s\\n' "$lane" "$1" "$2" "$GOFLAGS" "${*:3}" >> "$TEST_STEPS"
}
''')
            # The scheduled steps are captured above. Only the empty fixture's cleanup invokes Docker directly.
            bindir = root / "bin"
            bindir.mkdir()
            docker = bindir / "docker"
            docker.write_text('#!/bin/sh\ntest "$1" = rm\n')
            docker.chmod(0o700)
            modules = root / "server/internal/modules"
            for module in ("briefing", "mail", "voice", "volc"):
                (modules / module).mkdir(parents=True)
            steps = root / "scheduled"
            env = {**os.environ, "PATH": str(bindir) + os.pathsep + os.environ["PATH"],
                   "TEST_STEPS": str(steps), "PERSONA_CI_FULL_JOBS": "1"}
            for name in ("PERSONA_CI_PAIRED_SERVER_SHARDS", "PERSONA_CI_FULL_INCLUDE", "PERSONA_CI_FULL_EXCLUDE"):
                env.pop(name, None)

            def trace(script, *args):
                steps.write_text("")
                subprocess.run(["bash", str(root / "scripts/ci" / script), *args], cwd=root, env=env,
                               check=True, capture_output=True, text=True, timeout=30)
                return [line.split("\t", 4) for line in steps.read_text().splitlines()]

            def actual_checks(rows):
                setup = {"Postgres 18 fixture", "Postgres readiness", "Browser runner resources"}
                return Counter((label, seconds, flags, re.sub(r"(--database-file )\S+", r"\1<fixture>", cmd))
                               for _, label, seconds, flags, cmd in rows if label not in setup)

            normal = ("build", "node", "managers", "other")
            hosted = (*normal, "core-node", "core-managers", "core-other")
            for expanded in (False, True):
                if expanded:
                    for module in ("office", "screen-share", "phone-extra", "recording", "lark", "github", "imessage"):
                        (modules / module).mkdir()
                with self.subTest(expanded=expanded):
                    together = actual_checks(trace("server.sh", "all"))
                    remote = [row for lane in hosted for row in trace("server.sh", lane)]
                    local = trace("full.sh", *("server-" + lane for lane in normal))
                    self.assertEqual(actual_checks(remote), together)
                    self.assertEqual(actual_checks(local), together)
                    self.assertTrue(all(n == 1 for n in together.values()))
                    where = {label: lane for lane, label, *_ in remote}
                    self.assertEqual(where["Core-only and module-less builds"], "node")
                    self.assertEqual(where["Builds without the mail module"], "node")
                    self.assertEqual(where["Builds without the speech modules"], "core-node")
                    self.assertEqual(where["Core-only hub smoke"], "build")
                    self.assertEqual(where["Core-only install from packages"], "core-other")
                    self.assertEqual(where["Builds without each module"], "build")
                    self.assertEqual(where["Non-root extension container isolation"], "other")
                    for label, lane in (("Builds without the node modules", "node"),
                                        ("Builds without the recording module", "node"),
                                        ("Builds without the Lark module", "core-node"),
                                        ("Builds without the connector modules", "core-node")):
                        self.assertEqual(where.get(label), lane if expanded else None)
                    if not expanded:
                        (modules / "office").mkdir()
                        labels = [row[1] for row in trace("server.sh", "node")]
                        self.assertIn("Builds without the node modules", labels)
                        (modules / "office").rmdir()
                    for rows in (remote, local):
                        self.assertEqual(sum(label == "Postgres readiness" for _, label, *_ in rows), 6)
                        self.assertEqual(sum(label == "Browser runner resources" for _, label, *_ in rows), 2)


if __name__ == "__main__":
    unittest.main()
