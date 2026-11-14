"""The version every build script writes (#7201): the nearest vX.Y.Z tag, a lightweight one too.

Plain `git describe` counts annotated tags only, so after the lightweight v0.4.0 every build still read v0.3.1-<n>-g<sha>.
Each script's own `$(git describe …)` is run on a scratch repository with an older annotated v tag, a newer lightweight
one and other tags on the head, so a script that drifts from the one command fails here.
"""
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
BASH = shutil.which('bash') or '/bin/bash'
# a `$(git describe …)` on a line that is not a comment
DESCRIBE = re.compile(r"^(?!\s*#).*?\$\((git describe[^)]*)\)", re.M)


def describe_commands():
    """Every script under scripts/ that names a build, with the describe command it runs."""
    found = {}
    for path in sorted(ROOT.joinpath('scripts').rglob('*.sh')):
        if 'node_modules' in path.parts:
            continue
        for command in DESCRIBE.findall(path.read_text(encoding='utf-8', errors='replace')):
            found.setdefault(str(path.relative_to(ROOT)), []).append(command)
    return found


def git(repo, *args):
    env = {'GIT_AUTHOR_NAME': 'Test', 'GIT_AUTHOR_EMAIL': 'test@example.com', 'GIT_COMMITTER_NAME': 'Test',
           'GIT_COMMITTER_EMAIL': 'test@example.com', 'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_CONFIG_NOSYSTEM': '1',
           'PATH': '/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin'}
    return subprocess.run(['git', *args], cwd=repo, env=env, check=True, capture_output=True, text=True).stdout.strip()


def commit(repo, name):
    Path(repo, 'file.txt').write_text(name)
    git(repo, 'add', 'file.txt')
    git(repo, 'commit', '-q', '-m', name)
    return git(repo, 'rev-parse', '--short=9', 'HEAD')


class VersionDescribeTests(unittest.TestCase):
    def setUp(self):
        self.commands = describe_commands()

    def run_in(self, repo, command):
        return subprocess.run([BASH, '-c', command], cwd=repo, check=True, capture_output=True, text=True).stdout.strip()

    def test_the_build_scripts_all_name_a_version(self):
        for script in ('scripts/deploy/deploy.sh', 'scripts/release/publish.sh', 'scripts/release/release.sh',
                       'scripts/ci/hub-image.sh', 'scripts/app/mac-link.sh'):
            self.assertIn(script, self.commands, f'{script} no longer runs git describe: update this test')

    def test_a_lightweight_release_tag_names_the_build_and_other_tags_never_do(self):
        with tempfile.TemporaryDirectory() as repo:
            git(repo, 'init', '-q')
            commit(repo, 'one')
            git(repo, 'tag', '-a', 'v0.3.1', '-m', 'v0.3.1')
            commit(repo, 'two')
            git(repo, 'tag', 'v0.4.0')  # lightweight, as a GitHub release's tag
            head = commit(repo, 'three')
            git(repo, 'tag', 'testflight/202611140000')
            git(repo, 'tag', 'promo-v1')
            for script, commands in self.commands.items():
                for command in commands:
                    with self.subTest(script=script, command=command):
                        self.assertRegex(self.run_in(repo, command), rf'^v0\.4\.0-1-g{head[:7]}[0-9a-f]*$')
            # --dirty marks uncommitted changes where a script asks for it
            Path(repo, 'file.txt').write_text('changed')
            for script, commands in self.commands.items():
                for command in commands:
                    if '--dirty' in command:
                        with self.subTest(script=script, dirty=True):
                            self.assertRegex(self.run_in(repo, command), rf'^v0\.4\.0-1-g{head[:7]}[0-9a-f]*-dirty$')

    def test_with_no_version_tag_a_build_is_its_commit(self):
        with tempfile.TemporaryDirectory() as repo:
            git(repo, 'init', '-q')
            head = commit(repo, 'one')
            git(repo, 'tag', 'testflight/202611140000')
            for script, commands in self.commands.items():
                for command in commands:
                    with self.subTest(script=script, command=command):
                        self.assertRegex(self.run_in(repo, command), rf'^{head[:7]}[0-9a-f]*$')


if __name__ == '__main__':
    unittest.main()
