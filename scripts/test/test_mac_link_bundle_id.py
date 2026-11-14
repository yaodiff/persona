"""mac-link.sh names Persona Link and its Persona Screen and Persona Input helpers (#7569, #7775).

macOS keeps Persona Screen's Screen Recording grant per bundle id, so a build must never fall back to a placeholder id:
without mac.link_bundle_id or PERSONA_LINK_BUNDLE_ID it takes the app's registered identity plus ".link", and with no
identity at all it stops and says what to set. Persona Input's Accessibility grant is tied to its code, so its build is
kept by a hash of its sources and reused until they change. The script runs for real in a temporary copy of the files it
reads, with xcodebuild, xcrun, go, lipo, codesign and git replaced by stubs, so nothing is built, signed or installed.
"""
import json
import os
from pathlib import Path
import plistlib
import shutil
import stat
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
COPIED = ('scripts/app/mac-link.sh', 'scripts/persona_config.py', 'scripts/persona_identity.py',
          'apple/PersonaLink/PersonaScreen-Info.plist', 'server/build-tags.txt',
          'apple/PersonaLink/Input/Package.swift', 'apple/PersonaLink/Input/PersonaInput-Info.plist',
          'apple/PersonaLink/Input/Sources/PersonaInput/main.swift', 'apple/PersonaLink/Input/Sources/PersonaInput/UnixSocket.swift',
          'apple/Packages/PersonaInputKit/Package.swift', 'apple/Packages/PersonaInputKit/Sources/PersonaInputKit/ScrollRecord.swift')
STUBS = {
    # The PersonaScreen scheme's product, where mac-link.sh copies it from (run in apple/PersonaLink).
    'xcodebuild': 'mkdir -p build/dd/Build/Products/Release && : > build/dd/Build/Products/Release/PersonaScreen',
    'go': 'while [ $# -gt 0 ]; do [ "$1" = -o ] && : > "$2"; shift; done',
    'lipo': 'while [ $# -gt 0 ]; do [ "$1" = -output ] && : > "$2"; shift; done',
    'codesign': 'echo "$*" >> "$CODESIGN_LOG"',
    'git': 'echo v0.0.0-test',
    # `swift build` of Persona Input: an empty executable in its scratch path, each real build logged; --show-bin-path
    # says where.
    'xcrun': 'while [ $# -gt 0 ]; do case $1 in --scratch-path) s=$2;; --show-bin-path) show=1;; esac; shift; done\n'
             'if [ -n "${show:-}" ]; then echo "$s/release"; else mkdir -p "$s/release" && : > "$s/release/PersonaInput" && chmod +x "$s/release/PersonaInput" && echo build >> "$SWIFT_LOG"; fi',
}


class MacLinkBundleIdTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.root = self.tmp / 'repo'
        for name in COPIED:
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / name, target)
        bin_dir = self.tmp / 'bin'
        bin_dir.mkdir()
        for name, body in STUBS.items():
            stub = bin_dir / name
            stub.write_text('#!/bin/bash\n' + body + '\n')
            stub.chmod(stub.stat().st_mode | stat.S_IXUSR)
        self.home = self.tmp / 'home'
        self.home.mkdir()
        self.config = self.tmp / 'config.json'
        self.log = self.tmp / 'codesign.log'
        self.swift = self.tmp / 'swift.log'
        self.env = {'PATH': f'{bin_dir}:{os.environ["PATH"]}', 'HOME': str(self.home), 'PERSONA_CONFIG': str(self.config),
                    'CODESIGN_LOG': str(self.log), 'SWIFT_LOG': str(self.swift),
                    'PERSONA_INPUT_CACHE': str(self.tmp / 'input-cache')}

    def build(self, config=None, **env):
        if config is not None:
            self.config.write_text(json.dumps(config))
        return subprocess.run(['bash', str(self.root / 'scripts/app/mac-link.sh'), 'build'],
                              env={**self.env, **env}, capture_output=True, text=True, timeout=60)

    def built_ids(self, result):
        """The Persona Screen bundle id and the Link helpers' signing identifiers of a finished build."""
        self.assertEqual(result.returncode, 0, result.stderr)
        app = self.root / 'apple/PersonaLink/build/Persona Link.app/Contents/Helpers'
        with open(app / 'Persona Screen.app/Contents/Info.plist', 'rb') as f:
            screen = plistlib.load(f)['CFBundleIdentifier']
        identifiers = sorted(line.split('--identifier ')[1].split()[0]
                             for line in self.log.read_text().splitlines() if '--identifier ' in line)
        return screen, identifiers

    def input_id(self):
        app = self.root / 'apple/PersonaLink/build/Persona Link.app/Contents/Helpers'
        with open(app / 'Persona Input.app/Contents/Info.plist', 'rb') as f:
            return plistlib.load(f)['CFBundleIdentifier']

    def swift_builds(self):
        return len(self.swift.read_text().splitlines()) if self.swift.exists() else 0

    def test_without_a_link_id_the_app_identity_names_link_and_screen(self):
        screen, signed = self.built_ids(self.build({'app': {'bundle_id': 'org.sample.assistant'}}))
        self.assertEqual(screen, 'org.sample.assistant.link.screen')
        self.assertEqual(self.input_id(), 'org.sample.assistant.link.input')
        self.assertEqual(signed, ['org.sample.assistant.link.helper', 'org.sample.assistant.link.persona-node'])

    def test_persona_input_is_built_once_until_its_sources_change(self):
        config = {'app': {'bundle_id': 'org.sample.assistant'}}
        self.built_ids(self.build(config))
        self.assertEqual(self.swift_builds(), 1)
        self.built_ids(self.build(config))
        self.assertEqual(self.swift_builds(), 1, 'the same sources: the kept helper, not a new build')
        source = self.root / 'apple/PersonaLink/Input/Sources/PersonaInput/main.swift'
        source.write_text(source.read_text() + '// changed\n')
        self.built_ids(self.build(config))
        self.assertEqual(self.swift_builds(), 2, 'changed sources: built again')
        kit = self.root / 'apple/Packages/PersonaInputKit/Sources/PersonaInputKit/ScrollRecord.swift'
        kit.write_text(kit.read_text() + '// changed\n')
        self.built_ids(self.build(config))
        self.assertEqual(self.swift_builds(), 3, "the record's kit changed: built again")
        self.built_ids(self.build(config, UNIVERSAL='1'))
        self.assertEqual(self.swift_builds(), 4, 'other architectures: a build of their own')

    def test_the_exported_app_identity_names_it_without_a_config(self):
        screen, signed = self.built_ids(self.build(PERSONA_BUNDLE_ID='org.sample.exported'))
        self.assertEqual(screen, 'org.sample.exported.link.screen')
        self.assertEqual(signed, ['org.sample.exported.link.helper', 'org.sample.exported.link.persona-node'])

    def test_mac_link_bundle_id_wins_over_the_app_identity(self):
        config = {'app': {'bundle_id': 'org.sample.assistant'}, 'mac': {'link_bundle_id': 'org.sample.link'}}
        screen, signed = self.built_ids(self.build(config, PERSONA_BUNDLE_ID='org.sample.exported'))
        self.assertEqual(screen, 'org.sample.link.screen')
        self.assertEqual(signed, ['org.sample.link.helper', 'org.sample.link.persona-node'])

    def test_the_environment_wins_over_the_config(self):
        config = {'app': {'bundle_id': 'org.sample.assistant'}, 'mac': {'link_bundle_id': 'org.sample.link'}}
        screen, _ = self.built_ids(self.build(config, PERSONA_LINK_BUNDLE_ID='org.sample.env'))
        self.assertEqual(screen, 'org.sample.env.screen')

    def test_without_any_identity_it_stops_and_says_what_to_set(self):
        for name, config in (('no config file', None), ('no app.bundle_id', {'hub': {'url': 'https://hub.example.com'}})):
            with self.subTest(name):
                self.config.unlink(missing_ok=True)
                result = self.build(config)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('mac.link_bundle_id', result.stderr)
                self.assertIn('app.bundle_id', result.stderr)
                self.assertFalse(self.log.exists(), 'nothing may be signed without an identity')
                self.assertFalse((self.root / 'apple/PersonaLink/build').exists())

    def test_no_placeholder_id_is_left_in_the_script(self):
        self.assertNotIn('com.example', (ROOT / 'scripts/app/mac-link.sh').read_text())


if __name__ == '__main__':
    unittest.main()
