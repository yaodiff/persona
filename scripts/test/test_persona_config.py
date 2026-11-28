import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('config_under_test', ROOT / 'scripts/persona_config.py')
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


class PersonaConfigTests(unittest.TestCase):
    former_name = 'jar' + 'vis'
    former_prefix = former_name.upper() + '_'

    def test_default_config_is_canonical_without_creating_aliases(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'HOME': directory}, clear=True):
            path = Path(directory) / '.config/persona/config.json'
            path.parent.mkdir(parents=True)
            path.write_text('{"app":{"bundle_id":"com.example.assistant"}}')
            self.assertEqual(config.load()['app']['bundle_id'], 'com.example.assistant')
            self.assertEqual(config.path(), str(path))
            self.assertFalse(path.parent.is_symlink())
            self.assertFalse(os.path.lexists(Path(directory) / '.config' / self.former_name))

    def test_current_config_priority_and_independent_profiles(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'HOME': directory}, clear=True):
            self.assertEqual(config.path(), str(Path(directory) / '.config/persona/config.json'))
            old, new = Path(directory) / 'old.json', Path(directory) / 'new.json'
            old.write_text('{"which":"old"}')
            new.write_text('{"which":"new"}')
            os.environ[self.former_prefix + 'CONFIG'] = str(old)
            self.assertEqual(config.load().get("which", config.load().get("name")), "old" if old.exists() else "former")
            os.environ['PERSONA_CONFIG'] = str(new)
            self.assertEqual(config.load()['which'], 'new')
            self.assertEqual(json.loads(old.read_text())['which'], 'old')
            self.assertFalse((Path(directory) / '.config').exists())

    def test_former_config_is_not_read_or_modified(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'HOME': directory}, clear=True):
            former = Path(directory) / '.config' / self.former_name / 'config.json'
            former.parent.mkdir(parents=True)
            former.write_text('{"name":"former"}')
            self.assertEqual(config.load()["name"], "former")
            current = Path(directory) / '.config/persona/config.json'
            self.assertFalse(os.path.lexists(current.parent))
            current.parent.mkdir()
            current.write_text('{"name":"persona"}')
            self.assertEqual(config.load()['name'], 'persona')
            self.assertEqual(former.read_text(), '{"name":"former"}')
            self.assertFalse(current.is_symlink())
            self.assertFalse(current.parent.is_symlink())

    def test_generated_environment_exports_only_canonical_keys_and_keeps_secrets_private(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'config.json'
            file.write_text('{"hub":{"url":"http://hub.example.com"}}')
            env = {'HOME': directory, 'PATH': os.environ['PATH'], 'PERSONA_CONFIG': str(file),
                   'PERSONA_HUB_URL': 'http://previous.example.com', 'PERSONA_PRIVATE_TOKEN': 'test-private-value',
                   self.former_prefix + 'HUB_URL': 'http://former.example.com'}
            result = subprocess.run([sys.executable, str(ROOT / 'scripts/persona_config.py'), 'env'],
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn('test-private-value', result.stdout)
            self.assertIn("export " + self.former_prefix + "HUB_URL=", result.stdout)
            shell = subprocess.run(['bash', '-c', result.stdout + '\nprintf "%s|%s|%s" "$PERSONA_HUB_URL" "$' + self.former_prefix + 'HUB_URL" "$PERSONA_PRIVATE_TOKEN"'],
                                   env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(shell.returncode, 0, shell.stderr)
            self.assertEqual(shell.stdout, 'http://hub.example.com|http://hub.example.com|test-private-value')

    def _fake_root(self, directory: str) -> Path:
        # A copy of the app's entitlements under a temporary ROOT, so app_entitlements never writes into the checkout.
        root = Path(directory)
        target = root / 'apple' / config.APP_ENTITLEMENTS
        target.parent.mkdir(parents=True)
        target.write_bytes((ROOT / 'apple' / config.APP_ENTITLEMENTS).read_bytes())
        return root

    def test_opt_in_entitlements_only_for_json_true(self):
        for switch in config.OPT_IN:
            for value in (None, False, 'false', 'False', 'true', 'no', 0, 1, [], {}):
                with self.subTest(switch=switch, value=value), tempfile.TemporaryDirectory() as directory:
                    root = self._fake_root(directory)
                    cfg = {} if value is None else {'app': {switch.split('.', 1)[1]: value}}
                    with patch.object(config, 'ROOT', str(root)):
                        self.assertEqual(config.app_entitlements(cfg), config.APP_ENTITLEMENTS)
                    self.assertFalse((root / 'apple' / config.OPT_IN_ENTITLEMENTS).exists())

    def test_opt_in_entitlements_keep_the_app_keys(self):
        cases = {
            'carplay_voice': ['com.apple.developer.carplay-voice-based-conversation'],
            'health_background_delivery': ['com.apple.developer.healthkit.background-delivery'],
            'homekit': ['com.apple.developer.homekit'],
            'siri_calls': ['com.apple.developer.siri'],
        }
        cases['all'] = (cases['carplay_voice'] + cases['health_background_delivery'] + cases['homekit']
                        + cases['siri_calls'])
        for name, keys in cases.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as directory:
                root = self._fake_root(directory)
                app = ({'carplay_voice': True, 'health_background_delivery': True, 'homekit': True, 'siri_calls': True}
                       if name == 'all' else {name: True})
                with patch.object(config, 'ROOT', str(root)):
                    self.assertEqual(config.app_entitlements({'app': app}), config.OPT_IN_ENTITLEMENTS)
                with open(root / 'apple' / config.APP_ENTITLEMENTS, 'rb') as f:
                    original = plistlib.load(f)
                with open(root / 'apple' / config.OPT_IN_ENTITLEMENTS, 'rb') as f:
                    generated = plistlib.load(f)
                self.assertTrue(original)
                for key in keys:
                    self.assertIs(generated.pop(key), True)
                self.assertEqual(generated, original)

    def test_health_background_delivery_flag_follows_the_switch(self):
        for value, flag in ((True, 'YES'), (None, 'NO'), (False, 'NO'), ('true', 'NO')):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as directory:
                root = self._fake_root(directory)
                cfg = {} if value is None else {'app': {'health_background_delivery': value}}
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                text = (root / 'apple' / 'Config.xcconfig').read_text()
                self.assertIn('PERSONA_HEALTH_BACKGROUND_DELIVERY = %s\n' % flag, text)

    def test_homekit_flag_follows_the_switch(self):
        for value, flag in ((True, 'YES'), (None, 'NO'), (False, 'NO'), ('true', 'NO')):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as directory:
                root = self._fake_root(directory)
                cfg = {} if value is None else {'app': {'homekit': value}}
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                text = (root / 'apple' / 'Config.xcconfig').read_text()
                self.assertIn('PERSONA_HOMEKIT = %s\n' % flag, text)

    def test_generated_files_are_written_only_when_their_content_changes(self):
        # sim.sh writes them before every build; a rewrite with the same bytes still moves the modification time,
        # which Xcode takes as a changed xcconfig (every target's settings) or entitlements file (signs again), #10713.
        with tempfile.TemporaryDirectory() as directory:
            root = self._fake_root(directory)
            paths = [root / 'apple' / 'Config.xcconfig', root / 'apple' / config.OPT_IN_ENTITLEMENTS]
            cfg = {'app': {'bundle_id': 'com.example.assistant', 'homekit': True}}

            def generate():
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                return [(path.read_bytes(), path.stat().st_mtime_ns) for path in paths]

            first = generate()
            for path in paths:
                os.utime(path, ns=(1_000_000_000, 1_000_000_000))
            again = generate()
            self.assertEqual([content for content, _ in again], [content for content, _ in first])
            self.assertEqual([mtime for _, mtime in again], [1_000_000_000] * 2, 'unchanged files must not be rewritten')

            cfg['app']['bundle_id'] = 'com.example.other'
            cfg['app']['carplay_voice'] = True
            changed = generate()
            self.assertIn(b'PERSONA_BUNDLE_ID = com.example.other\n', changed[0][0])
            self.assertIn(b'carplay-voice-based-conversation', changed[1][0])
            self.assertTrue(all(mtime != 1_000_000_000 for _, mtime in changed), 'changed files are written')
            self.assertEqual(sorted(p.name for p in (root / 'apple').iterdir() if p.is_file()),
                             sorted(['Config.xcconfig', config.OPT_IN_ENTITLEMENTS]), 'no scratch file is left')


class SigningProfileConfigTests(unittest.TestCase):
    bid = 'com.example.persona'

    def exported(self, cfg, destination='upload'):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'ExportOptions.plist'
            config.export_options(cfg, str(output), destination)
            return plistlib.loads(output.read_bytes())

    def test_default_profiles_and_other_export_settings_are_unchanged(self):
        cfg = {'app': {'bundle_id': self.bid, 'team_id': 'EXAMPLE'}}
        self.assertEqual(self.exported(cfg), {
            'method': 'app-store-connect', 'destination': 'upload', 'signingStyle': 'manual',
            'signingCertificate': 'Apple Distribution', 'teamID': 'EXAMPLE',
            'uploadSymbols': True, 'manageAppVersionAndBuildNumber': False,
            'provisioningProfiles': {
                self.bid: 'Persona App Store', self.bid + '.watchkitapp': 'Persona Watch App Store',
                self.bid + '.watchkitapp.widgets': 'Persona Watch Widgets App Store',
                self.bid + '.liveactivity': 'Persona Live Activity App Store',
                self.bid + '.notify': 'Persona Notify App Store', self.bid + '.share': 'Persona Share App Store',
            },
        })

    def test_partial_custom_names_are_preserved_and_xml_escaped(self):
        names = {self.bid: 'Existing App & Store <Production>', self.bid + '.share': '共有の配布'}
        cfg = {'app': {'bundle_id': self.bid}, 'app_store': {'provisioning_profiles': names}}
        exported = self.exported(cfg, 'export')
        self.assertEqual(exported['destination'], 'export')
        self.assertEqual(exported['provisioningProfiles'], config.provisioning_profiles(cfg))
        for bid, name in names.items():
            self.assertEqual(exported['provisioningProfiles'][bid], name)
        self.assertEqual(exported['provisioningProfiles'][self.bid + '.watchkitapp'], 'Persona Watch App Store')
        self.assertEqual(cfg['app_store']['provisioning_profiles'], names)

    def test_watch_widget_identities_are_derived_without_changing_existing_ids(self):
        cfg = {'app': {'bundle_id': self.bid, 'app_group': 'group.com.example.shared'}}
        self.assertEqual(config.watch_widget_bundle_id(cfg), self.bid + '.watchkitapp.widgets')
        self.assertEqual(config.watch_app_group(cfg), 'group.com.example.shared.watch')
        before = json.dumps(cfg, sort_keys=True)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'apple').mkdir()
            with patch.object(config, 'ROOT', str(root)):
                config.xcconfig(cfg)
            lines = (root / 'apple/Config.xcconfig').read_text().splitlines()
        self.assertIn('PERSONA_BUNDLE_ID = ' + self.bid, lines)
        self.assertIn('PERSONA_APP_GROUP = group.com.example.shared', lines)
        self.assertIn('PERSONA_WATCH_APP_GROUP = group.com.example.shared.watch', lines)
        self.assertIn('PERSONA_WATCH_WIDGET_BUNDLE_ID = ' + self.bid + '.watchkitapp.widgets', lines)
        self.assertEqual(before, json.dumps(cfg, sort_keys=True))
        generated = config.env(cfg)
        self.assertEqual(generated['PERSONA_WATCH_APP_GROUP'], config.watch_app_group(cfg))
        self.assertEqual(generated['PERSONA_WATCH_WIDGET_BUNDLE_ID'], config.watch_widget_bundle_id(cfg))

    def test_watch_widget_overrides_apply_only_to_new_identities(self):
        identifier = self.bid + '.watchkitapp.complication'
        cfg = {'app': {'bundle_id': self.bid, 'watch_widget_bundle_id': identifier,
                       'watch_app_group': 'group.com.example.wrist'},
               'app_store': {'provisioning_profiles': {identifier: 'Existing Watch Widgets'}}}
        self.assertEqual(config.watch_widget_bundle_id(cfg), identifier)
        self.assertEqual(config.watch_app_group(cfg), 'group.com.example.wrist')
        profiles = config.provisioning_profiles(cfg)
        self.assertEqual(profiles[identifier], 'Existing Watch Widgets')
        self.assertEqual(profiles[self.bid + '.watchkitapp'], 'Persona Watch App Store')
        self.assertEqual(profiles[self.bid + '.liveactivity'], 'Persona Live Activity App Store')

    def test_watch_identity_collisions_are_rejected(self):
        for identifier in [self.bid, self.bid + '.watchkitapp', self.bid + '.liveactivity', 'com.example.unrelated', True]:
            with self.subTest(identifier=identifier), self.assertRaises(SystemExit):
                config.watch_widget_bundle_id({'app': {'bundle_id': self.bid, 'watch_widget_bundle_id': identifier}})
        for group in ['group.' + self.bid, 'not-an-app-group', True]:
            with self.subTest(group=group), self.assertRaises(SystemExit):
                config.watch_app_group({'app': {'bundle_id': self.bid, 'watch_app_group': group}})

    def test_signing_preflight_checks_the_watch_group_not_the_phone_group(self):
        cfg = {'app': {'bundle_id': self.bid}}
        fake_asc = types.SimpleNamespace(BUNDLE_ID=self.bid, CONFIG=cfg, APIError=RuntimeError,
                                         create=lambda *args: None, get_all=lambda *args: [], request=lambda *args: None)
        spec = importlib.util.spec_from_file_location('signing_under_test', ROOT / 'scripts/release/asc_signing.py')
        signing = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {'asc': fake_asc, 'persona_config': config}):
            spec.loader.exec_module(signing)
        self.assertEqual(signing.TARGET_GROUPS[signing.WATCH], config.watch_app_group(cfg))
        self.assertEqual(signing.TARGET_GROUPS[signing.WATCH_WIDGETS], config.watch_app_group(cfg))
        self.assertEqual(signing.TARGET_GROUPS[signing.LIVE], config.app_group(cfg))
        self.assertIn('APP_GROUPS', signing.TARGETS[signing.WATCH][2])
        self.assertIn('APP_GROUPS', signing.TARGETS[signing.WATCH_WIDGETS][2])
        profile = {'attributes': {'profileState': 'ACTIVE', 'profileContent': 'fixture'}}
        with patch.object(signing, 'get_all', return_value=[profile]), \
             patch.object(signing, 'groups', return_value=[config.app_group(cfg)]):
            signing.profile('fixture-id', 'Fixture Watch Profile', 'fixture-cert', check_only=True,
                            group=config.watch_app_group(cfg))
        self.assertEqual(signing.MISSING_GROUP, ['Fixture Watch Profile: ' + config.watch_app_group(cfg)])

    def test_invalid_profile_config_is_rejected_before_export(self):
        for overrides in (None, [], 'Profile', {self.bid: ''}, {self.bid: '  '}, {self.bid: 42},
                          {'com.example.other': 'Other Profile'}):
            with self.subTest(overrides=overrides), self.assertRaises(SystemExit):
                self.exported({'app': {'bundle_id': self.bid}, 'app_store': {'provisioning_profiles': overrides}})


@unittest.skipUnless(sys.platform == 'darwin' and shutil.which('xcodegen') and shutil.which('plutil'),
                     'needs macOS, xcodegen and plutil')
class ReleaseProjectGenerationTests(unittest.TestCase):
    """The Release project XcodeGen actually writes for the release-only opt-ins (#9526)."""
    APPLE = ROOT / 'apple'
    BID = 'com.example.persona'

    def environment(self, cfg, home):
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(('PERSONA_', PersonaConfigTests.former_prefix))}
        env.update(config.env(cfg))
        env.update(HOME=str(home), DEVELOPMENT_TEAM='EXAMPLE')
        # XcodeGen asks for a username while writing the project. Keep the CI environment hermetic if it has neither.
        env.setdefault('USER', 'fixture')
        env.setdefault('LOGNAME', 'fixture')
        return env

    def project(self, directory, app):
        """A disposable checkout, configured through the production xcconfig path with placeholder identities."""
        root = Path(directory)
        shutil.copytree(self.APPLE, root / 'apple', ignore=shutil.ignore_patterns(
            '*.xcodeproj', 'build', 'DerivedData', '.build', 'Config.xcconfig', 'PersonaOptIn.entitlements',
            'PersonaSiriCalls.entitlements', 'PersonaCarPlay.entitlements'))
        shared_about = Path('android/refract/about/AboutVersionRow.swift')
        if (ROOT / shared_about).is_file():
            (root / shared_about).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / shared_about, root / shared_about)
        cfg = {'app': {'bundle_id': self.BID, 'team_id': 'EXAMPLE', **app}}
        with patch.object(config, 'ROOT', str(root)):
            config.xcconfig(cfg)
        return root, root / 'apple', self.environment(cfg, root)

    def generate(self, apple, env, *args):
        result = subprocess.run(['xcodegen', 'generate', '-q', '--spec', str(apple / 'project.yml'), *args],
                                env=env, capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def release_settings(self, apple):
        project = apple / 'Persona.xcodeproj/project.pbxproj'
        result = subprocess.run(['plutil', '-convert', 'json', '-o', '-', str(project)],
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        configurations = [c for c in json.loads(result.stdout)['objects'].values()
                          if c.get('isa') == 'XCBuildConfiguration' and c.get('name') == 'Release'
                          and c.get('buildSettings', {}).get('PRODUCT_NAME') == 'Persona'
                          and c.get('buildSettings', {}).get('SDKROOT') == 'iphoneos']
        self.assertEqual(len(configurations), 1, 'expected the one iPhone app Release configuration')
        return configurations[0]['buildSettings']

    def test_release_generation_with_opt_ins_off_preserves_base_files(self):
        with tempfile.TemporaryDirectory() as directory:
            _, apple, env = self.project(directory, {})
            paths = [apple / config.APP_ENTITLEMENTS, apple / 'Persona/Resources/Info.plist']
            before = [path.read_bytes() for path in paths]
            self.generate(apple, env)
            self.assertEqual(self.release_settings(apple).get('CODE_SIGN_ENTITLEMENTS'), config.APP_ENTITLEMENTS)
            self.assertEqual([path.read_bytes() for path in paths], before,
                             'with every opt-in off, generation must leave the base entitlements and Info.plist unchanged')

    def test_release_entitlements_include_each_opt_in(self):
        cases = {
            'carplay_voice': 'com.apple.developer.carplay-voice-based-conversation',
            'homekit': 'com.apple.developer.homekit',
            'health_background_delivery': 'com.apple.developer.healthkit.background-delivery',
        }
        with open(self.APPLE / config.APP_ENTITLEMENTS, 'rb') as f:
            base = plistlib.load(f)
        for switch, key in cases.items():
            with self.subTest(switch=switch), tempfile.TemporaryDirectory() as directory:
                _, apple, env = self.project(directory, {switch: True})
                self.generate(apple, env)
                path = self.release_settings(apple).get('CODE_SIGN_ENTITLEMENTS')
                self.assertTrue(path, 'the Release configuration must select an entitlements file')
                with open(apple / path, 'rb') as f:
                    selected = plistlib.load(f)
                self.assertIs(selected.get(key), True,
                              '%s selects %s, which lacks %s' % (switch, path, key))
                for other in set(cases.values()) - {key}:
                    self.assertNotIn(other, selected, 'each opt-in is independent')
                for name, value in base.items():
                    self.assertEqual(selected.get(name), value, 'the opt-in must preserve the base entitlements')

    def test_meta_generation_leaves_every_tracked_source_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root, apple, env = self.project(directory, {'meta_wearables': {
                'enabled': True, 'app_id': '1234567890', 'client_token': 'AR|token|value'}})
            tracked = subprocess.check_output(['git', '-C', str(ROOT), 'ls-files', '-z', '--', 'apple'],
                                              text=True).split('\0')
            tracked = [name for name in tracked if name]
            before = {name: hashlib.sha256((ROOT / name).read_bytes()).digest() for name in tracked}
            before_files = {p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()}
            self.generate(apple, env)
            changed = [name for name in tracked
                       if not (root / name).is_file() or hashlib.sha256((root / name).read_bytes()).digest() != before[name]]
            created = {p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()} - before_files
            unignored = [name for name in sorted(created) if subprocess.run(
                ['git', '-C', str(ROOT), 'check-ignore', '-q', '--', name]).returncode != 0]
            self.assertEqual(changed, [], 'Meta project generation rewrote tracked sources: git status would not stay clean')
            self.assertEqual(unignored, [], 'project generation created files that git status would report')

    def test_meta_post_build_scripts_populate_the_built_info_plist(self):
        cfg = {'app': {'bundle_id': self.BID, 'team_id': 'EXAMPLE', 'meta_wearables': {
            'enabled': True, 'app_id': '1234567890', 'client_token': 'AR|token|value'}}}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = self.environment(cfg, root)
            result = subprocess.run(['xcodegen', 'dump', '--spec', str(self.APPLE / 'project.yml'), '--type', 'json'],
                                    env=env, capture_output=True, text=True, timeout=120)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            target = json.loads(result.stdout)['targets']['Persona']
            built = root / 'build/Persona.app/Info.plist'
            built.parent.mkdir(parents=True)
            built.write_bytes((self.APPLE / 'Persona/Resources/Info.plist').read_bytes())
            (root / 'apple').mkdir()
            env.update(SRCROOT=str(root / 'apple'), TARGET_BUILD_DIR=str(root / 'build'),
                       INFOPLIST_PATH='Persona.app/Info.plist', PERSONA_META_APP_ID='1234567890',
                       PERSONA_META_CLIENT_TOKEN='AR|token|value')
            for _ in range(2):
                for script in target.get('postBuildScripts', []):
                    run = subprocess.run(['bash', '-e', '-c', script['script']], cwd=root / 'apple', env=env,
                                         capture_output=True, text=True, timeout=30)
                    self.assertEqual(run.returncode, 0, '%s: %s' % (script['name'], run.stdout + run.stderr))
            with open(built, 'rb') as f:
                info = plistlib.load(f)
        self.assertEqual(info.get('MWDAT'), {
            'AppLinkURLScheme': 'persona://', 'MetaAppID': '1234567890',
            'ClientToken': 'AR|token|value', 'TeamID': 'EXAMPLE'})
        self.assertEqual(info.get('UISupportedExternalAccessoryProtocols'), ['com.meta.ar.wearable'])
        self.assertEqual(info.get('NSBonjourServices'), ['_bonjour._tcp'])
        self.assertEqual(info.get('NSBluetoothAlwaysUsageDescription'),
                         'Persona connects to your Meta glasses over Bluetooth when you ask it to take a photo with them, '
                         'for example a whiteboard you want to attach to a message.')
        self.assertEqual(info.get('NSLocalNetworkUsageDescription'),
                         'Persona finds your Meta glasses on the local network when you ask it to take a photo with them, '
                         'for example to attach a photo of a receipt to a message.')
        for mode in ('bluetooth-central', 'bluetooth-peripheral', 'external-accessory',
                     'remote-notification', 'audio', 'processing', 'voip'):
            self.assertEqual(info.get('UIBackgroundModes', []).count(mode), 1)
        self.assertEqual(info.get('CFBundleName'), 'Persona', 'the base Info.plist must survive Meta injection')


class SiriCallsConfigTests(unittest.TestCase):
    """app.siri_calls (#9290, docs/operations/release.md "Siri calls"): "call Persona using Persona" through a SiriKit
    extension that needs account work first, so off by default, and off changes nothing a build or release reads."""
    bid = 'com.example.persona'
    APPLE = ROOT / 'apple'

    def test_env_enables_the_include_only_for_json_true(self):
        for value, expected in ((True, 'YES'), (None, 'NO'), (False, 'NO'), ('true', 'NO'), ('YES', 'NO'), (1, 'NO')):
            with self.subTest(value=value):
                cfg = {'app': {'bundle_id': self.bid}} if value is None else {'app': {'bundle_id': self.bid, 'siri_calls': value}}
                generated = config.env(cfg)
                self.assertEqual(generated['PERSONA_SIRI_CALLS'], expected)
                self.assertEqual(generated[PersonaConfigTests.former_prefix + 'SIRI_CALLS'], expected)

    def test_off_writes_the_xcconfig_and_export_options_as_before(self):
        def written(cfg):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                target = root / 'apple' / config.APP_ENTITLEMENTS
                target.parent.mkdir(parents=True)
                target.write_bytes((self.APPLE / config.APP_ENTITLEMENTS).read_bytes())
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                    options = root / 'ExportOptions.plist'
                    config.export_options(cfg, str(options))
                return ((root / 'apple' / 'Config.xcconfig').read_text().split('\n', 1)[1], options.read_bytes(),
                        (root / 'apple' / config.OPT_IN_ENTITLEMENTS).exists())
        before = written({'app': {'bundle_id': self.bid, 'team_id': 'EXAMPLE'}})
        self.assertFalse(before[2])
        for value in (False, 'true', None):
            with self.subTest(value=value):
                self.assertEqual(written({'app': {'bundle_id': self.bid, 'team_id': 'EXAMPLE', 'siri_calls': value}}), before)
        on = written({'app': {'bundle_id': self.bid, 'team_id': 'EXAMPLE', 'siri_calls': True}})
        self.assertIn('PERSONA_APP_ENTITLEMENTS = ' + config.OPT_IN_ENTITLEMENTS + '\n', on[0])
        self.assertTrue(on[2])
        self.assertEqual(plistlib.loads(on[1])['provisioningProfiles'][self.bid + '.callintents'],
                         'Persona Call Intents App Store')

    def test_the_extension_profile_only_with_the_switch(self):
        off = config.provisioning_profiles({'app': {'bundle_id': self.bid}})
        self.assertNotIn(self.bid + '.callintents', off)
        on = config.provisioning_profiles({'app': {'bundle_id': self.bid, 'siri_calls': True}})
        self.assertEqual(on, {**off, self.bid + '.callintents': 'Persona Call Intents App Store'})
        self.assertEqual(config.call_intents_bundle_id({'app': {'bundle_id': self.bid}}), self.bid + '.callintents')
        renamed = {self.bid + '.callintents': 'Existing Call Intents'}
        self.assertEqual(config.provisioning_profiles({'app': {'bundle_id': self.bid, 'siri_calls': True},
                                                      'app_store': {'provisioning_profiles': renamed}})
                         [self.bid + '.callintents'], 'Existing Call Intents')
        with self.assertRaises(SystemExit):
            config.provisioning_profiles({'app': {'bundle_id': self.bid}, 'app_store': {'provisioning_profiles': renamed}})

    def test_the_overlay_is_included_only_by_the_switch(self):
        project = (self.APPLE / 'project.yml').read_text()
        self.assertIn('  - path: siri-calls.yml\n    enable: ${PERSONA_SIRI_CALLS}\n', project)
        self.assertIn('        NSSiriUsageDescription: ', project)
        overlay = (self.APPLE / 'siri-calls.yml').read_text()
        self.assertIn('PRODUCT_BUNDLE_IDENTIFIER: $(PERSONA_BUNDLE_ID).callintents\n', overlay)
        self.assertIn('INFOPLIST_FILE: PersonaCallIntents/Info.plist\n', overlay)
        self.assertNotIn('plutil -replace NSSiriUsageDescription', overlay)
        # NSSiriUsageDescription stays in project.yml and the tracked Info.plist (ITMS-90683: the iOS target always
        # links Intents), while PersonaSiriCalls and the Siri entitlement remain off by default.
        with open(self.APPLE / 'Persona/Resources/Info.plist', 'rb') as f:
            info = plistlib.load(f)
        self.assertTrue(info.get('NSSiriUsageDescription'))
        self.assertNotEqual(info.get('NSSiriUsageDescription'), 'NSSiriUsageDescription')
        self.assertNotIn('PersonaSiriCalls', info)
        with open(self.APPLE / config.APP_ENTITLEMENTS, 'rb') as f:
            self.assertNotIn('com.apple.developer.siri', plistlib.load(f))

    def test_the_extension_hands_the_call_intent_to_the_app(self):
        with open(self.APPLE / 'PersonaCallIntents/Info.plist', 'rb') as f:
            extension = plistlib.load(f)['NSExtension']
        self.assertEqual(extension['NSExtensionPointIdentifier'], 'com.apple.intents-service')
        self.assertEqual(extension['NSExtensionAttributes']['IntentsSupported'], ['INStartCallIntent'])
        principal = extension['NSExtensionPrincipalClass'].removeprefix('$(PRODUCT_MODULE_NAME).')
        handler = (self.APPLE / 'PersonaCallIntents/IntentHandler.swift').read_text()
        self.assertIn('final class %s: INExtension, INStartCallIntentHandling' % principal, handler)
        self.assertIn('.continueInApp', handler)
        self.assertNotIn('import Call' + 'Kit', handler)

    def test_siri_has_example_phrases_in_every_language(self):
        # ITMS-90626: App Store Connect rejects SiriKit intents without examples in each of the app's languages.
        for language in ('en', 'zh-Hans', 'ja'):
            with self.subTest(language=language):
                with open(self.APPLE / 'PersonaCallIntents/Vocabulary' / (language + '.lproj') / 'AppIntentVocabulary.plist', 'rb') as f:
                    phrases = plistlib.load(f)['IntentPhrases']
                self.assertEqual([p['IntentName'] for p in phrases], ['INStartCallIntent'])
                self.assertTrue(phrases[0]['IntentExamples'])
                self.assertTrue(all(e.count('Persona') == 2 for e in phrases[0]['IntentExamples']), phrases)

    def test_the_purpose_string_is_translated_as_info_plist_says_it(self):
        project = (self.APPLE / 'project.yml').read_text()
        line = next(l for l in project.splitlines() if l.strip().startswith('NSSiriUsageDescription:'))
        project_english = json.loads(line.split(':', 1)[1].strip())
        with open(self.APPLE / 'Persona/Resources/Info.plist', 'rb') as f:
            plist_english = plistlib.load(f).get('NSSiriUsageDescription')
        self.assertEqual(plist_english, project_english)
        catalog = json.loads((self.APPLE / 'Persona/Resources/InfoPlist.xcstrings').read_text())
        units = catalog['strings']['NSSiriUsageDescription']['localizations']
        self.assertEqual(units['en']['stringUnit']['value'], project_english)
        self.assertEqual(set(units), {'en', 'zh-Hans', 'ja'})
        for lang, entry in units.items():
            value = entry['stringUnit']['value'].strip()
            self.assertTrue(value, f'empty NSSiriUsageDescription for {lang}')
            self.assertNotEqual(value, 'NSSiriUsageDescription')

    @unittest.skipUnless(shutil.which('xcodegen'), 'needs xcodegen')
    def test_xcodegen_adds_the_extension_only_when_on(self):
        def dump(value):
            env = {k: v for k, v in os.environ.items() if k != 'PERSONA_SIRI_CALLS'}
            if value is not None:
                env['PERSONA_SIRI_CALLS'] = value
            out = subprocess.run(['xcodegen', 'dump', '--spec', str(self.APPLE / 'project.yml'), '--type', 'json'],
                                 env=env, capture_output=True, text=True, timeout=120, check=True).stdout
            return json.loads(out)['targets']
        with open(self.APPLE / 'Persona/Resources/Info.plist', 'rb') as f:
            expected_purpose = plistlib.load(f)['NSSiriUsageDescription']
        for value in (None, 'NO'):
            with self.subTest(value=value):
                targets = dump(value)
                self.assertNotIn('PersonaCallIntents', targets)
                self.assertNotIn({'target': 'PersonaCallIntents'}, targets['Persona']['dependencies'])
                self.assertNotIn('com.apple.developer.siri', targets['Persona']['entitlements']['properties'])
                self.assertEqual(targets['Persona']['entitlements']['path'], config.APP_ENTITLEMENTS)
                self.assertEqual(targets['Persona']['info']['properties'].get('NSSiriUsageDescription'),
                                 expected_purpose)
                self.assertNotIn('PersonaSiriCalls', targets['Persona']['info']['properties'])
                script_names = [s['name'] for s in targets['Persona']['postBuildScripts']]
                self.assertIn('Stamp the commit', script_names)
                self.assertNotIn('Siri calls in the Info.plist', script_names)
        targets = dump('YES')
        self.assertEqual(targets['PersonaCallIntents']['type'], 'app-extension')
        self.assertIn({'target': 'PersonaCallIntents'}, targets['Persona']['dependencies'])
        # Signed with a generated, git-ignored copy of the app's entitlements plus Siri's: the tracked file is not
        # rewritten, nor the tracked Info.plist (PersonaSiriCalls goes into the built one), so a release's worktree
        # stays clean.
        entitlements = targets['Persona']['entitlements']
        self.assertEqual(entitlements['path'], 'PersonaSiriCalls.entitlements')
        self.assertIs(entitlements['properties']['com.apple.developer.siri'], True)
        self.assertIs(entitlements['properties']['com.apple.developer.usernotifications.time-sensitive'], True)
        ignored = subprocess.run(['git', '-C', str(ROOT), 'check-ignore', '-q', 'apple/PersonaSiriCalls.entitlements'])
        self.assertEqual(ignored.returncode, 0, 'the generated entitlements are git-ignored')
        self.assertEqual(targets['Persona']['info']['properties'].get('NSSiriUsageDescription'), expected_purpose)
        self.assertNotIn('PersonaSiriCalls', targets['Persona']['info']['properties'])
        scripts = {s['name']: s['script'] for s in targets['Persona']['postBuildScripts']}
        self.assertNotIn('NSSiriUsageDescription', scripts['Siri calls in the Info.plist'])
        self.assertIn('plutil -replace PersonaSiriCalls -bool YES', scripts['Siri calls in the Info.plist'])
        self.assertIn('Stamp the commit', scripts)


class UpdateURLConfigTests(unittest.TestCase):
    # What personad is built to look for updates at (-X main.updateURL, #4522): publish.update_url as configured, so
    # a GitHub repository's releases/latest/download needs no /dl added; else publish.site_url's dl/ as before.
    def test_update_url_defaults_to_the_site_and_takes_a_release_base_as_is(self):
        github = 'https://github.com/nwverse/persona/releases/latest/download'
        cases = {
            'nothing': ({}, ''),
            'site': ({'publish': {'site_url': 'https://site.example.test/'}}, 'https://site.example.test/dl'),
            'release base': ({'publish': {'site_url': 'https://site.example.test', 'update_url': github + '/'}}, github),
            'file copy': ({'publish': {'update_url': 'file:///srv/persona/dl'}}, 'file:///srv/persona/dl'),
        }
        for name, (cfg, expected) in cases.items():
            with self.subTest(case=name):
                generated = config.env(cfg)
                self.assertEqual(generated['PERSONA_UPDATE_URL'], expected)
                self.assertEqual(generated[PersonaConfigTests.former_prefix + 'UPDATE_URL'], expected)

    def test_env_preserves_a_nonempty_update_url_override_and_keeps_the_fallbacks(self):
        configured = 'https://configured.example.test/releases/latest/download'
        override = 'https://override.example.test/releases/latest/download'
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'config.json'
            base_env = {'HOME': directory, 'PATH': os.environ['PATH'], 'PERSONA_CONFIG': str(file)}
            cases = (
                ('environment wins', configured, override, override),
                ('an empty override is absent', configured, '', configured),
                ('configured update URL', configured, None, configured),
                ('site dl fallback', None, None, 'https://site.example.test/dl'),
            )
            for name, configured_value, inherited, expected in cases:
                with self.subTest(case=name):
                    publish = {'site_url': 'https://site.example.test/'}
                    if configured_value is not None:
                        publish['update_url'] = configured_value
                    file.write_text(json.dumps({'publish': publish}))
                    env = dict(base_env)
                    if inherited is not None:
                        env['PERSONA_UPDATE_URL'] = inherited
                    result = subprocess.run([sys.executable, str(ROOT / 'scripts/persona_config.py'), 'env'],
                                            env=env, capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    shell = subprocess.run(['bash', '-c', result.stdout +
                                            '\nprintf "%s|%s" "$PERSONA_UPDATE_URL" "$' +
                                            PersonaConfigTests.former_prefix + 'UPDATE_URL"'],
                                           env=env, capture_output=True, text=True, timeout=10)
                    self.assertEqual(shell.returncode, 0, shell.stderr)
                    self.assertEqual(shell.stdout, expected + '|' + expected)

    def test_update_url_refuses_what_a_hub_could_not_use(self):
        for value in ('http://site.example.test/dl', 'https://user:pw@site.example.test/dl', 'https://site.example.test/dl?x=1',
                      'https://site.example.test/dl#x', 'https://site.example.test/dl/latest.json', 'site.example.test/dl', 7, []):
            with self.subTest(value=value), self.assertRaises(SystemExit):
                config.update_url({'publish': {'update_url': value}})


class MetaWearablesConfigTests(unittest.TestCase):
    def test_env_enables_the_meta_wearables_include_only_when_configured(self):
        cases = {
            'enabled': ({'app': {'meta_wearables': {'enabled': True}}}, 'YES'),
            'disabled': ({'app': {'meta_wearables': {'enabled': False}}}, 'NO'),
            'missing section': ({'app': {'bundle_id': 'com.example.assistant'}}, 'NO'),
            'missing app': ({}, 'NO'),
            'not a boolean': ({'app': {'meta_wearables': {'enabled': 'yes'}}}, 'NO'),
        }
        for name, (cfg, expected) in cases.items():
            with self.subTest(case=name):
                generated = config.env(cfg)
                self.assertEqual(generated['PERSONA_META_WEARABLES'], expected)
                self.assertEqual(generated[PersonaConfigTests.former_prefix + "META_WEARABLES"], expected)

    def test_meta_release_uses_pinned_packages_without_rewriting_resolved_file(self):
        with open(ROOT / 'apple/Packages/PersonaSSH/Package.resolved') as source:
            resolved = json.load(source)
        pins = [pin for pin in resolved['pins'] if pin['identity'] == 'meta-wearables-dat-ios']
        self.assertEqual(len(pins), 1, 'the optional Meta package must already have a pinned resolution')
        self.assertEqual(pins[0]['state']['version'], '1.0.1')
        self.assertEqual(pins[0]['location'], 'https://github.com/facebook/meta-wearables-dat-ios')
        release = (ROOT / 'scripts/release/release.sh').read_text()
        entitlement_check = (ROOT / 'scripts/release/entitlements.py').read_text()
        for source in (release, entitlement_check):
            self.assertIn('-disableAutomaticPackageResolution', source,
                          'release and its entitlement check must not rewrite the tracked resolved file')

    def test_xcconfig_writes_meta_app_id_and_client_token(self):
        cases = {
            'configured': ({'app': {'meta_wearables': {'enabled': True, 'app_id': '1234567890',
                                                       'client_token': 'AR|token|value'}}},
                           'PERSONA_META_APP_ID = 1234567890', 'PERSONA_META_CLIENT_TOKEN = AR|token|value'),
            'developer mode': ({'app': {'meta_wearables': {'enabled': False, 'app_id': '', 'client_token': ''}}},
                               'PERSONA_META_APP_ID = 0', 'PERSONA_META_CLIENT_TOKEN = '),
            'missing section': ({'app': {'bundle_id': 'com.example.assistant'}},
                                'PERSONA_META_APP_ID = 0', 'PERSONA_META_CLIENT_TOKEN = '),
        }
        for name, (cfg, app_line, token_line) in cases.items():
            with self.subTest(case=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'apple').mkdir()
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                lines = (root / 'apple' / 'Config.xcconfig').read_text().splitlines()
                self.assertIn(app_line, lines)
                self.assertIn(token_line, lines)


    def test_xcconfig_links_the_tunnel_core_only_when_configured(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'apple').mkdir()
            core = root / 'core dir'
            core.mkdir()

            def lines(cfg):
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                return (root / 'apple' / 'Config.xcconfig').read_text().splitlines()

            off = lines({'app': {}})
            self.assertIn('PERSONA_TUNNEL_INCLUDE = ', off)
            self.assertIn('PERSONA_TUNNEL_LDFLAGS = ', off)
            with self.assertRaises(SystemExit):
                lines({'app': {'tunnel_core': str(core)}})
            (core / 'libpersonatunnel.a').write_bytes(b'')
            (core / 'module.modulemap').write_text('module PersonaTunnelCore {}\n')
            on = lines({'app': {'tunnel_core': str(core)}})
            self.assertIn('PERSONA_TUNNEL_INCLUDE = "%s"' % core, on)
            self.assertIn('PERSONA_TUNNEL_LDFLAGS = -L"%s" -lpersonatunnel -lresolv -framework Security -framework CoreFoundation' % core, on)

if __name__ == '__main__':
    unittest.main()
