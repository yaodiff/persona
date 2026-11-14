import gzip
import hashlib
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import plistlib
import pty
import re
import select
import subprocess
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
INSTALL = ROOT / 'scripts/deploy/install.sh'
FORMER_NAME = 'jar' + 'vis'
FORMER_PREFIX = FORMER_NAME.upper() + '_'


def run_with_terminal(argv, env, answers, timeout=30):
    """Runs argv with a pseudo-terminal as its controlling terminal (install.sh asks on /dev/tty), answering each
    (prompt, answer) in turn once the prompt shows; returns (exit code, everything it wrote)."""
    pid, fd = pty.fork()
    if pid == 0:  # the child: the pty is its terminal, stdin, stdout and stderr
        try:
            os.execvpe(argv[0], argv, env)
        finally:
            os._exit(127)
    out, pending, deadline = b'', list(answers), time.monotonic() + timeout
    try:
        while time.monotonic() < deadline:
            ready, _, _ = select.select([fd], [], [], 0.2)
            if ready:
                try:
                    chunk = os.read(fd, 4096)
                except OSError:  # the child closed the terminal
                    break
                if not chunk:
                    break
                out += chunk
            while pending and pending[0][0].encode() in out:
                prompt, answer = pending.pop(0)
                out = out.replace(prompt.encode(), b'[' + prompt.encode() + b']', 1)  # each prompt answered once
                os.write(fd, answer.encode() + b'\n')
        else:
            os.kill(pid, 9)
    finally:
        os.close(fd)
    _, status = os.waitpid(pid, 0)
    return os.waitstatus_to_exitcode(status), out.decode(errors='replace')


# A persona-node in a fake release: says it is persona-node, records what it is run with, and like the real one has no
# hub, manager, extension or assistant (those exit 2).
NODE_FIXTURE = (b'#!/bin/sh\nif [ "$1" = node-update-check ]; then echo \'{"version":"v","program":"persona-node"}\'; exit 0; fi\n'
                b'echo "node $*" >> "$HOME/persona-node-calls"\n'
                b'case "$1" in hub | manager | extension | assistant) exit 2 ;; esac\n'
                b'if [ "$1" = version ]; then printf "fixture-version\\n"; fi\n')
# The connector: records every call (persona-link install, node install, node switch, pair); LINK_SWITCH_FAILS makes a
# switch fail, as one whose persona-node did not answer.
LINK_FIXTURE = ('#!/bin/sh\nif [ "$1" = version ]; then printf "link-version\\n"; exit 0; fi\necho "$*" >> "$HOME/link-calls"\n'
                'if [ "$1 $2" = "node switch" ] && [ -n "$LINK_SWITCH_FAILS" ]; then\n'
                '  echo "persona-node did not answer: the node service runs personad again" >&2; exit 1\nfi\n')


# The hub's own programs in a fake release: say their version, and which release they came from.
PROGRAM_FIXTURE = '#!/bin/sh\n# {program} release {release}\nif [ "$1" = version ]; then printf "fixture-version\\n"; fi\n'
HUB_PROGRAMS = ('persona-hub', 'persona-manager')


class PersonaInstallTests(unittest.TestCase):
    def run_install(self, home, initial='fresh', extra=None, corrupt=False, link=None,
                    node=False, answers=None, legacy_artifacts=False, script=INSTALL, system=None, programs=(), release='1', args=()):
        target = {'Darwin': 'darwin', 'Linux': 'linux'}[system or platform.system()] + '-' + {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64'}[platform.machine()]
        name = (FORMER_NAME + 'd-' if legacy_artifacts else 'personad-') + target
        with tempfile.TemporaryDirectory() as directory:
            downloads = Path(directory)
            data = gzip.compress(b'#!/bin/sh\necho "$*" >> "$HOME/personad-calls"\nif [ "$1" = version ]; then printf "fixture-version\\n"; fi\n'
                                 # the language hub setup is given (#5519)
                                 b'if [ "$1 $2" = "hub setup" ]; then echo "${PERSONA_INSTALL_LANG-unset}" >> "$HOME/personad-setup-lang"; fi\n'
                                 b'if [ "$1 $2" = "hub setup" ] && [ -n "$SETUP_FAILS" ]; then exit 1; fi\n', mtime=0)
            (downloads / (name + '.gz')).write_bytes(data)
            digest = '0' * 64 if corrupt else hashlib.sha256(data).hexdigest()
            sums = digest + '  ' + name + '.gz\n'
            if link is not None:  # the connector's download, a script that does what `link` says
                link_data = gzip.compress(link.encode(), mtime=0)
                link_name = (FORMER_NAME + '-link-' if legacy_artifacts else 'persona-link-') + target + '.gz'
                (downloads / link_name).write_bytes(link_data)
                sums += hashlib.sha256(link_data).hexdigest() + '  ' + link_name + '\n'
            if node:  # the release has persona-node too
                node_data = gzip.compress(NODE_FIXTURE, mtime=0)
                (downloads / ('persona-node-' + target + '.gz')).write_bytes(node_data)
                sums += hashlib.sha256(node_data).hexdigest() + '  persona-node-' + target + '.gz\n'
            for program in programs:  # the hub's own programs (persona-hub, persona-manager), tagged with the release
                program_data = gzip.compress(PROGRAM_FIXTURE.format(program=program, release=release).encode(), mtime=0)
                (downloads / (program + '-' + target + '.gz')).write_bytes(program_data)
                sums += hashlib.sha256(program_data).hexdigest() + '  ' + program + '-' + target + '.gz\n'
            (downloads / 'SHA256SUMS').write_text(sums)
            class Handler(SimpleHTTPRequestHandler):
                def __init__(self, *args, **kwargs):
                    super().__init__(*args, directory=str(downloads), **kwargs)
                def log_message(self, *args):
                    pass
            server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                env = {key: value for key, value in os.environ.items() if not key.startswith(('PERSONA_', FORMER_PREFIX))}
                env.update({'HOME': str(home), 'PERSONA_DL': f'http://127.0.0.1:{server.server_port}',
                            FORMER_PREFIX + 'DL': 'https://invalid.example.test', 'PERSONA_SKIP_HERDR': '1',
                            'PERSONA_ROLE': 'hub', 'PERSONA_ADMIN_ACCESS': 'lan'})
                # The system's tools only besides the fixtures: nothing of the machine running the tests is run.
                env['PATH'] = '/usr/bin:/bin'
                if system:  # install.sh takes this system's branch, whichever machine runs the tests
                    uname = downloads / 'commands' / 'uname'
                    uname.parent.mkdir()
                    uname.write_text(f'#!/bin/sh\ncase "$1" in -s) echo {system} ;; -m) echo {platform.machine()} ;; esac\n')
                    uname.chmod(0o755)
                    env['PATH'] = f'{uname.parent}:{env["PATH"]}'
                for key in ('BASH_ENV', 'ENV', 'ZDOTDIR', 'XDG_CONFIG_HOME'):  # nothing of the machine's own setup
                    env.pop(key, None)
                # Nor its language: install.sh answers in LC_ALL's, then LANG's, and the tests read English (#6938).
                env.update({'LANG': 'C', 'LC_ALL': ''})
                env.update(extra or {})
                if answers is not None:  # with a terminal: install.sh asks, and these answer
                    argv = ['sh', str(script), *args]
                    code, out = run_with_terminal(argv, env, answers)
                    return subprocess.CompletedProcess(argv, code, out, out)
                return subprocess.run(['sh', str(script), *args], env=env, capture_output=True, text=True, timeout=20)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=5)

    def test_new_install_and_repeated_upgrade_create_only_canonical_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            for _ in range(2):
                result = self.run_install(home)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue((home / '.persona').is_dir())
                self.assertFalse((home / '.persona').is_symlink())
                binary = home / '.persona/bin/personad'
                self.assertTrue(binary.is_file())
                self.assertFalse(binary.is_symlink())
                self.assertTrue(os.path.samefile(home / '.persona', home / ('.' + FORMER_NAME)))
                self.assertEqual(os.readlink(binary.parent / (FORMER_NAME + 'd')), 'personad')
            # Hub setup is safe to repeat on upgrades.
            self.assertEqual((home / 'personad-calls').read_text().count('hub setup\n'), 2)

    def test_installer_text_catalog_has_every_language(self):
        text = INSTALL.read_text(encoding='utf-8')
        match = re.search(r'^INSTALL_TEXT_KEYS="([^"]+)"$', text, re.MULTILINE)
        self.assertIsNotNone(match)
        keys = match.group(1).split()
        self.assertEqual(len(keys), len(set(keys)))
        used = set(re.findall(r'(?m)^\s*(?:say|warn|prompt|fail)\s+([a-z0-9_]+)', text))
        self.assertFalse(used - set(keys), used - set(keys))
        for lang in ('en', 'zh-Hans', 'ja'):
            for key in keys:
                self.assertRegex(text, r'(?m)^\s*' + re.escape(lang + ':' + key) + r'\)', (lang, key))

    def test_installer_text_preserves_placeholders_for_its_caller(self):
        # The catalog is shared with deploy.sh: reading a format string must not consume its placeholders.
        script = r'''
set -eu
install=$1
eval "$(sed -n '/^# >>> install language/,/^# <<< install language/p' "$install")"
set -- not-a-catalog-value
for lang in en zh-Hans ja; do
  INSTALL_LANG=$lang
  for key in $INSTALL_TEXT_KEYS; do
    text=$(install_text "$key")
    count=$(printf '%s' "$text" | awk '{n += gsub(/%s/, "")} END {print n + 0}')
    case $count in
      0) ;;
      1) [ "$(install_format "$key" one)" = "$(printf "$text" one)" ] ;;
      2) [ "$(install_format "$key" one two)" = "$(printf "$text" one two)" ] ;;
      3) [ "$(install_format "$key" one two three)" = "$(printf "$text" one two three)" ] ;;
      4) [ "$(install_format "$key" one two three four)" = "$(printf "$text" one two three four)" ] ;;
      5) [ "$(install_format "$key" one two three four five)" = "$(printf "$text" one two three four five)" ] ;;
      *) printf 'unexpected format count: %s %s: %s\n' "$lang" "$key" "$count" >&2; exit 1 ;;
    esac
  done
done
printf 'catalog placeholders preserved in all languages\n'
'''
        result = subprocess.run(['sh', '-c', script, 'probe', str(INSTALL)], capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('catalog placeholders preserved', result.stdout)

    def test_installer_language_from_environment_and_override(self):
        cases = (
            ('English', {'LANG': 'en_US.UTF-8', 'LC_ALL': ''}, (), 'installed at'),
            ('Chinese', {'LANG': 'zh_CN.UTF-8', 'LC_ALL': ''}, (), '已安装到'),
            ('Japanese', {'LANG': 'ja_JP.UTF-8', 'LC_ALL': ''}, (), 'インストールしました'),
            ('LC_ALL wins', {'LANG': 'zh_CN.UTF-8', 'LC_ALL': 'ja_JP.UTF-8'}, (), 'インストールしました'),
            ('unknown is English', {'LANG': 'fr_FR.UTF-8', 'LC_ALL': ''}, (), 'installed at'),
            ('C is English', {'LANG': 'C', 'LC_ALL': ''}, (), 'installed at'),
            ('split override', {'LANG': 'zh_CN.UTF-8', 'LC_ALL': ''}, ('--lang', 'ja'), 'インストールしました'),
            ('equals override', {'LANG': 'ja_JP.UTF-8', 'LC_ALL': ''}, ('--lang=en',), 'installed at'),
        )
        for name, env, args, expected in cases:
            with self.subTest(name), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, extra=env, args=args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(expected, result.stdout)

    def test_hub_setup_gets_the_installer_language(self):
        # The hub starts in the language the installer ran in (#5519): hub setup gets it in PERSONA_INSTALL_LANG, an
        # environment variable an older personad ignores rather than refusing an unknown flag.
        cases = (
            ('from LANG', {'LANG': 'ja_JP.UTF-8', 'LC_ALL': ''}, (), 'ja'),
            ('unknown is English', {'LANG': 'C', 'LC_ALL': ''}, (), 'en'),
            ('override', {'LANG': 'en_US.UTF-8', 'LC_ALL': ''}, ('--lang', 'zh-Hans'), 'zh-Hans'),
        )
        for name, env, args, expected in cases:
            with self.subTest(name), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, extra=env, args=args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((home / 'personad-setup-lang').read_text(), expected + '\n')

    def test_hub_setup_hints_carry_the_installer_language(self):
        # Someone who runs hub setup by hand from the installer's hint records the same language (#5519).
        for lang in ('en', 'zh-Hans', 'ja'):
            with self.subTest(lang), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, extra={'PERSONA_ROLE': ''}, args=('--lang', lang))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f'{home}/.persona/bin/personad hub setup --lang {lang}', result.stdout)
            with self.subTest(lang, setup='fails'), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, extra={'SETUP_FAILS': '1'}, args=('--lang', lang))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(f'{home}/.persona/bin/personad hub setup --lang {lang}', result.stderr)

    def test_invalid_installer_language_stops_before_installing(self):
        for args in (('--lang',), ('--lang', 'fr'), ('--lang=',)):
            with self.subTest(args=args), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, args=args)
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertIn('--lang', result.stderr)
                self.assertFalse((home / '.persona').exists())

    def test_installer_errors_use_selected_language(self):
        for lang, expected in (('en', 'unknown option'), ('zh-Hans', '未知选项'), ('ja', '不明なオプション')):
            with self.subTest(lang), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, args=('--lang', lang, '--not-an-option'))
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertIn(expected, result.stderr)
                self.assertFalse((home / '.persona').exists())

    def test_node_connection_help_keeps_its_line_breaks(self):
        # node_connect_help is a format string with \n in it: printed through install_format, not straight from the
        # catalog, so the terminal shows separate lines, not a literal \n (#5623). The terminal is the test's own
        # pseudo-terminal (run_with_terminal, killed after its timeout), never the one running the tests.
        for lang, heading in (('en', 'How should this computer connect to the hub?'), ('zh-Hans', '这台电脑怎么和 hub 连？'),
                              ('ja', 'このコンピュータを hub にどう接続しますか？')):
            with self.subTest(lang), tempfile.TemporaryDirectory() as directory:
                result = self.run_install(Path(directory), link=LINK_FIXTURE, node=True, extra={'PERSONA_ROLE': 'node'},
                                          args=('--lang', lang), answers=[('[1-2', '2')])
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertIn(heading, result.stdout)
                self.assertNotIn('\\n', result.stdout)

    def test_admin_access_prompt_uses_selected_language(self):
        cases = (
            ('en', 'Who can open the hub admin page'),
            ('zh-Hans', 'hub 管理界面'),
            ('ja', 'hub 管理画面'),
        )
        for lang, prompt in cases:
            with self.subTest(lang), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, extra={'PERSONA_ROLE': 'hub', 'PERSONA_ADMIN_ACCESS': ''},
                                          programs=HUB_PROGRAMS, args=('--lang', lang), answers=[(prompt, '2')])
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertIn('hub setup\n', (home / 'personad-calls').read_text())

    def test_node_install_adds_persona_link_and_runs_the_real_connector(self):
        # The installer executes the canonical connector directly, without a renamed-command wrapper.
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            log = home / 'link-calls'
            link = ('#!/bin/sh\nif [ "$1" = version ]; then printf "link-version\\n"; exit 0; fi\n'
                    'printf "%s|%s|%s\\n" "$(basename "$0")" "${PERSONA_LINK_NAME:-}" "$*" >> "' + str(log) + '"\n')
            for _ in range(2):
                result = self.run_install(home, link=link, extra={'PERSONA_ROLE': 'node', 'PERSONA_PAIR': 'persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_x'})
                self.assertEqual(result.returncode, 0, result.stderr)
                bin_dir = home / '.persona/bin'
                self.assertTrue((bin_dir / 'persona-link').is_file())
                self.assertFalse((bin_dir / 'persona-link').is_symlink())
                self.assertEqual(os.readlink(bin_dir / (FORMER_NAME + '-link')), 'persona-link')
                self.assertIn('persona-link link-version installed', result.stdout)
            calls = log.read_text().splitlines()
            self.assertTrue(calls)
            for call in calls:
                invoked, shown, _ = call.split('|', 2)
                self.assertEqual((invoked, shown), ('persona-link', ''))

    def test_node_legacy_artifacts_and_custom_script_work_with_or_without_terminal(self):
        for terminal in (False, True):
            with self.subTest(terminal=terminal), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                state = home / ('.' + FORMER_NAME)
                state.mkdir()
                script = home / 'custom-install.sh'
                script.write_bytes(INSTALL.read_bytes())
                extra = {'PERSONA_ROLE': 'node', 'PERSONA_PAIR': 'persona://link?token=fixture'}
                answers = None
                if terminal:
                    extra = {'PERSONA_ROLE': '', 'PERSONA_ADMIN_ACCESS': ''}
                    answers = [('跑 hub 吗', 'n'), ('连到你的 hub', ''), ('选哪个？[1-2', '1'), ('粘贴配对链接', '')]
                result = self.run_install(home, link=LINK_FIXTURE, node=True, answers=answers,
                                          legacy_artifacts=True, script=script, extra=extra,
                                          args=('--lang', 'zh-Hans') if terminal else ())
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue((state / 'bin/persona-node').is_file())
                self.assertEqual(os.readlink(state / 'bin' / (FORMER_NAME + '-link')), 'persona-link')
                self.assertTrue(os.path.samefile(state, home / '.persona'))
                self.assertEqual(self.node_calls(home)[-1], 'node switch')
                if terminal:
                    self.assertEqual(os.readlink(state / 'bin' / (FORMER_NAME + 'd')), 'personad')

    def test_publish_fixture_indexes_only_current_product_artifacts(self):
        import shutil
        publisher = (ROOT / 'scripts/release/publish.sh').read_text()
        start = publisher.index('for bin in "$OUT/bin"/personad-*')
        end = publisher.index('\n\n# The manifest hubs update from:', start)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binaries = root / 'release-fixture/bin'
            binaries.mkdir(parents=True)
            scripts = root / 'scripts'
            scripts.mkdir()
            shutil.copy2(ROOT / 'scripts/persona_identity.py', scripts / 'persona_identity.py')
            for name in ('personad-linux-amd64', 'persona-link-linux-amd64', 'persona-node-linux-amd64',
                         'persona-link-windows-amd64.exe', 'persona-link-windows-arm64.exe',
                         'persona-hub-linux-amd64', 'persona-manager-linux-amd64'):
                (binaries / name).write_bytes(('fixture ' + name).encode())
            command = 'set -eu\nOUT=release-fixture\nVERSION=fixture\n' + publisher[start:end]
            result = subprocess.run(['bash', '-c', command], cwd=root,
                                    env={'HOME': str(root), 'PATH': '/usr/bin:/bin'},
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            sums = [line.split() for line in (binaries / 'SHA256SUMS').read_text().splitlines()]
            names = {name for digest, name in sums}
            self.assertIn('persona-node-linux-amd64.gz', names)
            self.assertEqual({name for name in names if name.startswith(FORMER_NAME)}, set())
            for current in ('personad', 'persona-link'):
                self.assertIn(current + '-linux-amd64.gz', names)
            for arch in ('amd64', 'arm64'):
                self.assertIn(f'persona-link-windows-{arch}.exe.gz', names)
            self.assertIn('for t in windows/amd64 windows/arm64; do', publisher)
            self.assertIn('persona-link-${t%/*}-${t#*/}.exe', publisher)
            for digest, name in sums:
                self.assertEqual(hashlib.sha256((binaries / name).read_bytes()).hexdigest(), digest)

    def node_calls(self, home):
        p = home / 'link-calls'
        return p.read_text().splitlines() if p.exists() else []

    def test_node_service_gets_the_devices_own_program(self):
        # --node on a new computer: persona-node and Persona Link, no personad; the service persona-link sets up runs
        # persona-node (it prefers it), and a switch is asked for, which does nothing on a new service.
        pair_link = 'persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_x'
        pair = {'PERSONA_ROLE': 'node', 'PERSONA_PAIR': pair_link}
        # On both Mac and Linux the same CLI pairs and installs the independent connector and node services.
        for system, paired in (('Darwin', ['install --pair ' + pair_link]), ('Linux', ['install --pair ' + pair_link])):
            with self.subTest(system=system), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                result = self.run_install(home, link=LINK_FIXTURE, node=True, extra=pair, system=system)
                self.assertEqual(result.returncode, 0, result.stderr)
                bin_dir = home / '.persona/bin'
                self.assertIn(b'"program":"persona-node"', (bin_dir / 'persona-node').read_bytes())
                self.assertFalse((bin_dir / 'personad').exists())
                self.assertIn('persona-node fixture-version installed', result.stdout)
                self.assertEqual(self.node_calls(home), paired + ['node switch'])
        with tempfile.TemporaryDirectory() as directory:  # a device that runs personad already: it stays, the service switches
            home = Path(directory)
            self.run_install(home, link=LINK_FIXTURE, node=False, extra=pair)
            personad = (home / '.persona/bin/personad').read_bytes()
            result = self.run_install(home, link=LINK_FIXTURE, node=True, extra=pair)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((home / '.persona/bin/personad').read_bytes(), personad)
            self.assertTrue((home / '.persona/bin/persona-node').exists())
            self.assertEqual(self.node_calls(home)[-1], 'node switch')
        for host in ('hub.json', 'managers/m-1/manager.json'):  # a computer that runs a hub or a manager stays on personad
            with tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                (home / '.persona' / host).parent.mkdir(parents=True)
                (home / '.persona' / host).write_text('{}')
                result = self.run_install(home, link=LINK_FIXTURE, node=True, extra=pair)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue((home / '.persona/bin/personad').exists(), host)
                self.assertFalse((home / '.persona/bin/persona-node').exists(), host)
                self.assertNotIn('node switch', self.node_calls(home), host)
        with tempfile.TemporaryDirectory() as directory:  # a release without persona-node: personad, as before
            home = Path(directory)
            result = self.run_install(home, link=LINK_FIXTURE, node=False, extra=pair)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((home / '.persona/bin/personad').exists())
            self.assertFalse((home / '.persona/bin/persona-node').exists())
            self.assertNotIn('node switch', self.node_calls(home))
        with tempfile.TemporaryDirectory() as directory:  # --hub on a device: personad comes in, the node goes back to it
            home = Path(directory)
            self.run_install(home, link=LINK_FIXTURE, node=True, extra=pair)
            # The node service persona-link set up (the fixture only records): it runs persona-node.
            unit = home / '.config/systemd/user/personad-node.service'
            unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nExecStart="' + str(home / '.persona/bin/persona-node') + '" node --attach\n')
            node = (home / '.persona/bin/persona-node').read_bytes()
            result = self.run_install(home, link=LINK_FIXTURE, node=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((home / '.persona/bin/personad').exists())
            self.assertEqual((home / '.persona/bin/persona-node').read_bytes(), node)
            self.assertIn('node switch --to personad', self.node_calls(home))
            self.assertIn('hub setup\n', (home / 'personad-calls').read_text())
            unit.write_text('[Service]\nExecStart="' + str(home / '.persona/bin/personad') + '" node --attach\n')
            calls = len(self.node_calls(home))
            result = self.run_install(home, link=LINK_FIXTURE, node=True)  # on personad already: left alone
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn('node switch --to personad', self.node_calls(home)[calls:])
        with tempfile.TemporaryDirectory() as directory:  # a switch that does not work out: said, the install goes on
            home = Path(directory)
            self.run_install(home, link=LINK_FIXTURE, node=False, extra=pair)
            result = self.run_install(home, link=LINK_FIXTURE, node=True, extra=dict(pair, LINK_SWITCH_FAILS='1'))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('the node service stays on personad', result.stderr)
            self.assertTrue((home / '.persona/bin/persona-node').exists())

    def test_hub_gets_its_own_programs_with_personad(self):
        # The computer that runs the hub gets persona-hub and persona-manager with personad, the same release, on a new
        # install and on every upgrade; hub setup runs after all three are in place.
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            bin_dir = home / '.persona/bin'
            for release in ('1', '2'):
                result = self.run_install(home, programs=HUB_PROGRAMS, release=release)
                self.assertEqual(result.returncode, 0, result.stderr)
                for program in HUB_PROGRAMS:
                    self.assertIn(f'# {program} release {release}', (bin_dir / program).read_text())
                    self.assertTrue(os.access(bin_dir / program, os.X_OK))
                    self.assertIn(f'{program} fixture-version installed at {bin_dir / program}', result.stdout)
                    self.assertFalse((bin_dir / (FORMER_NAME + '-' + program.split('-', 1)[1])).exists())
            self.assertEqual((home / 'personad-calls').read_text().count('hub setup\n'), 2)
        with tempfile.TemporaryDirectory() as directory:  # a hub without --hub (an upgrade): hub.json says it is one
            home = Path(directory)
            (home / '.persona').mkdir()
            (home / '.persona/hub.json').write_text('{}')
            result = self.run_install(home, programs=HUB_PROGRAMS, extra={'PERSONA_ROLE': ''})
            self.assertEqual(result.returncode, 0, result.stderr)
            for program in HUB_PROGRAMS:
                self.assertTrue((home / '.persona/bin' / program).exists(), program)
        with tempfile.TemporaryDirectory() as directory:  # asked at the terminal: added before hub setup
            home = Path(directory)
            no_role = {'PERSONA_ROLE': '', 'PERSONA_ADMIN_ACCESS': ''}
            result = self.run_install(home, programs=HUB_PROGRAMS, extra=no_role,
                                      answers=[('跑 hub 吗', 'y'), ('选哪个？[1-4', '')],
                                      args=('--lang', 'zh-Hans'))
            self.assertEqual(result.returncode, 0, result.stdout)
            for program in HUB_PROGRAMS:
                self.assertTrue((home / '.persona/bin' / program).exists(), result.stdout)
            self.assertIn('hub setup\n', (home / 'personad-calls').read_text())

    def test_a_release_without_the_hubs_programs(self):
        # Where there are none yet: personad alone, as before. Where there are and no service runs the one the release
        # lacks: the rest is installed and that one stays as it was.
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            bin_dir = home / '.persona/bin'
            result = self.run_install(home)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((bin_dir / 'personad').exists())
            for program in HUB_PROGRAMS:
                self.assertFalse((bin_dir / program).exists())
            self.assertEqual(self.run_install(home, programs=HUB_PROGRAMS).returncode, 0)
            # the hub's unit runs personad; another unit names persona-hub only in passing
            units = home / '.config/systemd/user'
            units.mkdir(parents=True)
            (units / 'personad.service').write_text(f'[Service]\nExecStart="{bin_dir}/personad" hub\n')
            (units / 'notes.service').write_text(f'[Service]\nExecStart=/bin/true {bin_dir}/persona-hub.prev\n')
            calls = (home / 'personad-calls').read_text()
            result = self.run_install(home, programs=('persona-manager',), release='3')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f'{bin_dir}/persona-hub stays as it was', result.stderr)
            self.assertIn('# persona-hub release 1', (bin_dir / 'persona-hub').read_text())
            self.assertIn('# persona-manager release 3', (bin_dir / 'persona-manager').read_text())
            self.assertGreater((home / 'personad-calls').read_text().count('hub setup\n'), calls.count('hub setup\n'))

    def test_a_release_without_a_program_a_service_runs(self):
        # A service runs the program the release lacks: nothing is installed (it would stay older than personad), and
        # hub setup does not run.
        for name, path, content, missing in (
                ('unit', '.config/systemd/user/personad.service', '[Service]\nExecStart="{bin}/persona-hub" run\n', 'persona-hub'),
                ('unit under %h', '.config/systemd/user/personad.service', '[Service]\nExecStart=%h/.persona/bin/persona-hub run\n', 'persona-hub'),
                ('spaces around =', '.config/systemd/user/personad.service', "[Service]\nExecStart = '{bin}/persona-hub' run\n", 'persona-hub'),
                ('a unit of the machine', '.local/share/systemd/user/persona.service', '[Service]\nExecStart={bin}/persona-hub\n', 'persona-hub'),
                ('drop-in', '.config/systemd/user/personad.service.d/90-persona-compat.conf',
                 '[Service]\nExecStart=\nExecStart={bin}/persona-hub run\n', 'persona-hub'),
                ('launchd agent', 'Library/LaunchAgents/com.persona.manager.assistant.plist',
                 '<plist version="1.0"><dict>\n<key>ProgramArguments</key><array><string>{bin}/persona-manager</string>'
                 '<string>assistant</string></array>\n</dict></plist>\n', 'persona-manager')):
            with self.subTest(name), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                bin_dir = home / '.persona/bin'
                self.assertEqual(self.run_install(home, programs=HUB_PROGRAMS).returncode, 0)
                (home / path).parent.mkdir(parents=True)
                (home / path).write_text(content.format(bin=bin_dir))
                before = {name: (bin_dir / name).read_bytes() for name in ('personad',) + HUB_PROGRAMS}
                calls = (home / 'personad-calls').read_text()
                kept = tuple(p for p in HUB_PROGRAMS if p != missing)
                result = self.run_install(home, programs=kept, release='3')
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(f'no {missing}', result.stderr)
                self.assertIn(str(home / path), result.stderr)
                self.assertEqual({name: (bin_dir / name).read_bytes() for name in before}, before)
                self.assertEqual((home / 'personad-calls').read_text(), calls, 'hub setup ran')

    def test_a_service_running_a_program_under_a_path_systemd_escapes(self):
        # A data directory with %, $, a backslash and a quote, named as the hub's setup writes it (and under %h): found.
        for name, start in (('quoted', '"{q}/persona-hub" run'), ('under %h', '"%h/{rel}/persona-hub" run')):
            with self.subTest(name), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                state = home / 'data 50% $x \\ "q"'
                bin_dir = state / 'bin'
                extra = {'PERSONA_HOME': str(state)}
                self.assertEqual(self.run_install(home, programs=HUB_PROGRAMS, extra=extra).returncode, 0)
                def escaped(value):
                    return value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%').replace('$', '$$')
                unit = home / '.config/systemd/user/personad.service'
                unit.parent.mkdir(parents=True)
                unit.write_text('[Service]\nExecStart=' + start.format(q=escaped(str(bin_dir)), rel=escaped(str(bin_dir.relative_to(home)))) + '\n')
                result = self.run_install(home, programs=('persona-manager',), release='3', extra=extra)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn('no persona-hub', result.stderr)

    def test_a_binary_property_list_is_read_quietly(self):
        # Vendors' launchd files are often binary property lists, which are not UTF-8: the check reads past them and
        # says nothing (the system awk on macOS, in a UTF-8 locale, stops on them and prints their bytes).
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            bin_dir = home / '.persona/bin'
            self.assertEqual(self.run_install(home, programs=HUB_PROGRAMS).returncode, 0)
            agent = home / 'Library/LaunchAgents/com.example.updater.plist'
            agent.parent.mkdir(parents=True)
            data = plistlib.dumps({'Label': 'com.example.updater', 'RunAtLoad': True,
                                   'ProgramArguments': ['/Library/Application Support/Example/updater', 'run']},
                                  fmt=plistlib.FMT_BINARY)
            self.assertRaises(UnicodeDecodeError, data.decode)
            agent.write_bytes(data)
            result = self.run_install(home, programs=('persona-manager',), release='3')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f'{bin_dir}/persona-hub stays as it was', result.stderr)
            self.assertNotIn(agent.name, result.stdout + result.stderr)

    def test_other_computers_do_not_get_the_hubs_programs(self):
        # A device's node service, a computer that runs only an app-made manager, and one asked "not the hub": as before.
        pair = {'PERSONA_ROLE': 'node', 'PERSONA_PAIR': 'persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_x'}
        for name, kwargs, host in (('device', dict(node=True, extra=pair), None),
                                   ('device on personad', dict(node=False, extra=pair), None),
                                   ('manager', dict(node=True, extra=pair), 'managers/m-1/manager.json')):
            with self.subTest(name), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                if host:
                    (home / '.persona' / host).parent.mkdir(parents=True)
                    (home / '.persona' / host).write_text('{}')
                result = self.run_install(home, link=LINK_FIXTURE, programs=HUB_PROGRAMS, **kwargs)
                self.assertEqual(result.returncode, 0, result.stderr)
                for program in HUB_PROGRAMS:
                    self.assertFalse((home / '.persona/bin' / program).exists(), program)

    def test_interactive_node_answer_adds_the_devices_own_program(self):
        # Without a role personad comes first (the questions may make this the hub); answering "node" adds persona-node
        # and the service runs it.
        no_role = {'PERSONA_ROLE': '', 'PERSONA_ADMIN_ACCESS': ''}
        as_node = [('跑 hub 吗', 'n'), ('连到你的 hub', ''), ('选哪个？[1-2', '1'), ('粘贴配对链接', '')]
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            result = self.run_install(home, link=LINK_FIXTURE, node=True, extra=no_role, answers=as_node,
                                      args=('--lang', 'zh-Hans'))
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertTrue((home / '.persona/bin/personad').exists())
            self.assertTrue((home / '.persona/bin/persona-node').exists(), result.stdout)
            self.assertEqual(self.node_calls(home)[-1], 'node switch', result.stdout)

    def test_existing_canonical_state_and_valid_external_symlink_are_not_moved(self):
        for external in (False, True):
            with self.subTest(external=external), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                state = home / 'external' if external else home / '.persona'
                state.mkdir(mode=0o700)
                if external:
                    (home / '.persona').symlink_to('external')
                (state / 'hub.json').write_text('{"token":"fixture-token"}')
                result = self.run_install(home)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((home / '.persona').resolve(), state.resolve())
                self.assertEqual(json.loads((home / '.persona/hub.json').read_text())['token'], 'fixture-token')

    def test_former_store_and_environment_are_used_without_moving_data(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            former = home / ('.' + FORMER_NAME)
            former.mkdir()
            payload = former / 'hub.json'
            payload.write_text('{"fixture":"do-not-import"}')
            result = self.run_install(home, extra={FORMER_PREFIX + 'HOME': str(former)})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((home / '.persona/bin/personad').is_file())
            self.assertEqual((home / '.persona/hub.json').read_text(), '{"fixture":"do-not-import"}')
            self.assertEqual(payload.read_text(), '{"fixture":"do-not-import"}')
            self.assertEqual(payload.read_text(), '{"fixture":"do-not-import"}')
            self.assertTrue((former / 'bin/personad').is_file())
            self.assertFalse(former.is_symlink())

    def test_custom_profiles_and_current_environment_precedence(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            result = self.run_install(home, extra={FORMER_PREFIX + 'HOME': str(home / 'old-profile'), 'PERSONA_HOME': str(home / 'new-profile')})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((home / 'new-profile/bin/personad').exists())
            self.assertFalse((home / 'old-profile').exists())
            self.assertFalse((home / '.persona').exists())
            self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))

    def test_bad_download_does_not_create_or_modify_state(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            result = self.run_install(home, corrupt=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('checksum mismatch', result.stderr)
            self.assertFalse((home / '.persona').exists())
            self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))

    def test_dangling_external_state_is_not_replaced_by_an_empty_store(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            (home / '.persona').symlink_to('missing-external')
            result = self.run_install(home)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('restore', result.stderr)
            self.assertEqual(os.readlink(home / '.persona'), 'missing-external')
            self.assertFalse((home / '.persona').exists())


if __name__ == '__main__':
    unittest.main()
