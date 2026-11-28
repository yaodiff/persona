"""Compiler-free checks for canonical Apple product identities and script entry points."""
import importlib.util
import json
from pathlib import Path
import plistlib
import re
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
APPLE = ROOT / 'apple'
PROJECT = (APPLE / 'project.yml').read_text()
TARGETS = PROJECT.split('targets:\n', 1)[1].split('schemes:\n', 1)[0]
SCHEMES = PROJECT.split('schemes:\n', 1)[1]
FORMER_NAME = ('jar' + 'vis').capitalize()

TEST_ONLY_TARGET_EXCEPTIONS = {
    'AboutVersionAccessibilityHost':
        'Standalone test process for the shared About row accessibility tree; never embedded in Persona (#11150).',
    'AboutVersionAccessibilityUITests':
        'UI tests for the standalone About row accessibility host, not the production app (#11150).',
}
TEST_ONLY_SCHEME_EXCEPTIONS = {
    'AboutVersionAccessibilityHost': 'Runs only the standalone About row accessibility UI tests (#11150).',
    'AboutVersionVisualTests': 'Builds PersonaTests without PersonaUITests for About row visual comparisons (#11150).',
    'PersonaOfflineUnitTests': 'Builds and runs offline PersonaTests without PersonaUITests (#12474).',
}


def is_allowed_product_name(name, exceptions):
    return name.startswith('Persona') or name in exceptions


def blocks(text):
    matches = list(re.finditer(r'^  (\w+):\n', text, re.MULTILINE))
    return {m.group(1): text[m.end():matches[i + 1].start() if i + 1 < len(matches) else len(text)]
            for i, m in enumerate(matches)}


def load_tests(loader, tests, pattern):
    # Keep Apple-owned source guards in the full scripts discovery without duplicating their assertions here.
    spec = importlib.util.spec_from_file_location('apple_persona_only', APPLE / 'PersonaTests/test_persona_only.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    tests.addTests(loader.loadTestsFromModule(module))
    return tests


class AppleRenameTests(unittest.TestCase):
    def test_sources_use_only_canonical_directories_and_project(self):
        for suffix in ('', 'Watch', 'Widgets', 'Notify', 'Tests', 'UITests'):
            with self.subTest(directory=suffix):
                self.assertFalse((APPLE / (FORMER_NAME + suffix)).exists())
                self.assertTrue((APPLE / ('Persona' + suffix)).is_dir())
        self.assertFalse((APPLE / 'PersonaLink/Sources' / (FORMER_NAME + 'Link')).exists())
        self.assertFalse((APPLE / 'PersonaLink/Sources/PersonaLink').exists())
        self.assertTrue((APPLE / 'PersonaLink/Sources/PersonaScreen').is_dir())
        self.assertTrue((APPLE / 'PersonaLink/App/Sources/PersonaLink').is_dir())
        tracked = subprocess.run(['git', 'ls-files', '--', 'mac'], cwd=ROOT, capture_output=True, text=True, check=True).stdout
        self.assertEqual(tracked, '', 'the independent Link helper package is apple/PersonaLink/')
        self.assertIn('name: Persona\n', PROJECT)
        if (APPLE / 'Persona.xcodeproj').exists():
            self.assertTrue((APPLE / 'Persona.xcodeproj').is_dir())
        self.assertFalse((APPLE / 'Persona.xcodeproj').is_symlink())
        former_project = APPLE / (FORMER_NAME + '.xcodeproj')
        self.assertFalse(former_project.exists() or former_project.is_symlink())

    def test_targets_use_canonical_product_modules(self):
        targets = blocks(TARGETS)
        expected = {
            'Persona': 'Persona', 'PersonaWatch': 'Persona', 'PersonaVision': 'Persona',
            'PersonaWidgets': 'PersonaWidgets', 'PersonaNotify': 'PersonaNotify', 'PersonaVisionNotify': 'PersonaNotify',
            'PersonaShare': 'PersonaShare', 'PersonaVisionShare': 'PersonaShare',
            'PersonaWatchWidgets': 'PersonaWatchWidgets', 'PersonaVisionWidgets': 'PersonaWidgets',
        }
        self.assertEqual(set(targets), {*expected, 'PersonaTests', 'PersonaUITests', *TEST_ONLY_TARGET_EXCEPTIONS})
        for target, module in expected.items():
            with self.subTest(target=target):
                self.assertIn('PRODUCT_MODULE_NAME: ' + module + '\n', targets[target])
                self.assertIn('PRODUCT_NAME: Persona', targets[target])
        self.assertIn('TEST_TARGET_NAME: Persona\n', targets['PersonaUITests'])
        self.assertIn('Persona.app/Persona"', targets['PersonaTests'])

    def test_vision_target_uses_canonical_source_and_scheme(self):
        target = blocks(TARGETS)['PersonaVision']
        self.assertIn('platform: visionOS\n', target)
        self.assertIn('- path: Vision\n', target)
        self.assertIn('PRODUCT_NAME: Persona\n', target)
        self.assertTrue((APPLE / 'Vision').is_dir())
        self.assertIn('PersonaVision: all\n', blocks(SCHEMES)['PersonaVision'])

    def test_only_canonical_schemes_build_existing_targets(self):
        schemes = blocks(SCHEMES)
        canonical = {'Persona', 'PersonaWatch', 'PersonaVision'}
        self.assertEqual(set(schemes), {*canonical, *TEST_ONLY_SCHEME_EXCEPTIONS})
        for name in canonical:
            self.assertIn(name + ': all\n', schemes[name])
        self.assertNotIn('PersonaMac', schemes)
        self.assertFalse((APPLE / (FORMER_NAME + '.xcodeproj')).is_symlink())

    def test_about_version_test_only_exceptions_are_narrow_and_reasoned(self):
        expected_targets = {'AboutVersionAccessibilityHost', 'AboutVersionAccessibilityUITests'}
        expected_schemes = {'AboutVersionAccessibilityHost', 'AboutVersionVisualTests', 'PersonaOfflineUnitTests'}
        for exceptions, expected in ((TEST_ONLY_TARGET_EXCEPTIONS, expected_targets),
                                     (TEST_ONLY_SCHEME_EXCEPTIONS, expected_schemes)):
            self.assertEqual(set(exceptions), expected)
            for name, reason in exceptions.items():
                with self.subTest(name=name):
                    self.assertIsInstance(reason, str)
                    self.assertTrue(reason.strip(), 'a test-only exception needs a deliberate reason')
        for exceptions, expected in ((TEST_ONLY_TARGET_EXCEPTIONS, expected_targets),
                                     (TEST_ONLY_SCHEME_EXCEPTIONS, expected_schemes)):
            for name in expected:
                with self.subTest(registered=name):
                    self.assertTrue(is_allowed_product_name(name, exceptions))
            with self.subTest(unregistered='AboutVersionUnregisteredTests'):
                self.assertFalse(is_allowed_product_name('AboutVersionUnregisteredTests', exceptions))
        self.assertTrue(is_allowed_product_name('Persona', {}))

    def test_intent_control_activity_and_info_keys_use_canonical_identity(self):
        expected = {
            'Persona/Intents/AskPersonaIntent.swift': ['AskPersonaIntent'],
            'Persona/Intents/PersonaStatusIntent.swift': ['PersonaStatusIntent'],
            'Persona/Intents/PersonaShortcuts.swift': ['PersonaShortcuts'],
            'PersonaWidgets/PersonaControls.swift': ['AskPersonaControl', 'PersonaAgentsControl', 'DictatePersonaControl',
                                                     'CallPersonaControl', 'MessagePersonaControl'],
            'LiveActivity/PersonaControlIntents.swift': ['OpenPersonaAssistantIntent', 'OpenPersonaAgentsIntent',
                                                         'OpenPersonaDictationIntent', 'OpenPersonaCallIntent',
                                                         'OpenPersonaTypingIntent'],
            'LiveActivity/PersonaActivityAttributes.swift': ['PersonaActivityAttributes'],
        }
        for path, names in expected.items():
            source = (APPLE / path).read_text()
            for name in names:
                with self.subTest(path=path, name=name):
                    self.assertRegex(source, r'\bstruct ' + name + r'\s*:')
        controls = (APPLE / 'PersonaWidgets/PersonaControls.swift').read_text()
        self.assertIn('kind: "persona.control.ask"', controls)
        self.assertIn('kind: "persona.control.agents"', controls)
        self.assertIn('kind: "persona.control.dictate"', controls)
        self.assertIn('kind: "persona.control.call"', controls)
        info = plistlib.loads((APPLE / 'PersonaNotify/Info.plist').read_bytes())
        self.assertEqual(info['NSExtension']['NSExtensionPrincipalClass'], '$(PRODUCT_MODULE_NAME).NotificationService')
        self.assertIn('PersonaPushKeychainGroup', info)
        for key in ('PushKeychainGroup', 'PushRelay', 'AppGroup'):
            self.assertIn('Persona' + key + ':', PROJECT)
            self.assertNotIn(FORMER_NAME + key + ':', PROJECT)
        for relative in ('Persona/Resources/Info.plist', 'Vision/Resources/Info.plist'):
            info = plistlib.loads((APPLE / relative).read_bytes())
            self.assertEqual(info['CFBundleURLTypes'][0]['CFBundleURLSchemes'], ['persona', FORMER_NAME.lower()])

    def test_avatar_names_do_not_collide_with_persona_model(self):
        source = (APPLE / 'Persona/Views/Assistant/AssistantAvatar.swift').read_text()
        self.assertIn('struct AssistantAvatar: View', source)
        self.assertIn('enum AssistantAvatarGallery', source)
        self.assertNotIn('struct PersonaAvatar', source)
        self.assertTrue((APPLE / 'Persona/Persona/Persona.swift').is_file())

    def test_baseline_paths_resolve_without_changing_thresholds(self):
        baseline = json.loads((APPLE / 'PersonaTests/TokenBaseline.json').read_text())
        self.assertTrue(baseline)
        for path, thresholds in baseline.items():
            with self.subTest(path=path):
                self.assertFalse(path.startswith(FORMER_NAME))
                self.assertTrue((APPLE / path).is_file())
                self.assertIsInstance(thresholds, dict)
                self.assertTrue(all(isinstance(value, int) and value >= 0 for value in thresholds.values()))

    def test_launch_and_ctl_have_one_canonical_entry(self):
        launch = (APPLE / 'PersonaUITests/UITestStyle.swift').read_text()
        self.assertEqual(len(re.findall(r'func launchPersona\(', launch)), 1)
        self.assertNotRegex(launch, r'func launchPersona\([^)]*\)\s*\{\s*launchPersona\(')
        self.assertNotIn('func launch' + FORMER_NAME + '()', launch)
        self.assertEqual(len(re.findall(r'func launchPinned\(', launch)), 1)
        command = ROOT / 'scripts/app/personactl.py'
        self.assertTrue(command.is_file())
        self.assertFalse(command.is_symlink())
        alias = command.parent / (FORMER_NAME.lower() + 'ctl.py')
        self.assertTrue(alias.is_symlink())
        self.assertEqual(alias.readlink().as_posix(), command.name)
        self.assertEqual(alias.resolve(), command.resolve())

    def test_compact_shell_and_regular_sidebar_are_platform_defaults(self):
        root = (APPLE / 'Persona/Views/RootView.swift').read_text()
        selection = root.split('private var usesShell: Bool {', 1)[1].split('\n    }', 1)[0]
        self.assertRegex(selection, r'#if os\(visionOS\)[\s\S]*?return false')
        # Active compact width still selects the shell; #2836 holds that width while iOS snapshots the app away.
        # No other platform since the Mac app went (#13611): iOS is the last branch.
        self.assertRegex(selection, r'#elseif os\(iOS\)\s+return \(heldWidthClass \?\? widthClass\) == \.compact \|\| flipped\s+#endif')
        # The Sidebar layout when it is chosen, at every width (#6332: the drawer on a phone, the column at regular width);
        # otherwise the shell at compact width, else the tabs. Nothing picked is Merged where wide and the shell at compact
        # width (#4206, NavigationLayout.effective).
        self.assertIn('@StoredValue(NavigationLayout.key) private var navigation = ""', root)
        self.assertIn('NavigationLayout.effective(picked: navigation, regular:', root)
        self.assertRegex(root, r'if usesSidebar \{ SidebarRoot\(\)[^\n]*\n\s+else if usesShell \{ ShellView\(\)[^\n]*\n'
                               r'\s+else \{ RootTabs\(\)')
        self.assertIn('RootTabs().visionShell()', root)
        tabs = root.split('private struct RootTabs: View {', 1)[1].split('\n#if os(iOS)', 1)[0]
        self.assertIn('TabView(selection: Binding(get: { model.selectedTab }, set: { model.selectHomeTab($0) }))', tabs)
        self.assertIn('AssistantRootTab()', tabs)
        self.assertIn('AgentsRootTab()', tabs)
        self.assertRegex(tabs, r'#if !os\(iOS\)\s+InboxRootTab\(\)\s+SettingsRootTab\(\)\s+#endif')
        # Inside the iOS-only block; more iOS-only modifiers may follow it there (#8367 added the heads' line-up).
        self.assertRegex(tabs, r'#if os\(iOS\)(?:(?!#endif)[\s\S])*?\.modifier\(PadRoutes\(router: pad\)\)'
                               r'(?:(?!#if|#endif)[\s\S])*?#endif')
        for name in ('Assistant', 'Agents', 'Inbox', 'Settings'):
            self.assertRegex(root, r'private struct ' + name + r'RootTab: TabContent')
        routes = root.split('final class PadRouter: TabRouter {', 1)[1].split('private struct PadRoutes:', 1)[0]
        # While the router sets the tab itself it only notes which is in front (#6939).
        self.assertRegex(routes, r'func route\(_ t: AppTab, in model: AppModel\)\s*\{\s*'
                                 r'if settingTab \{ front = t; return \}\s*switch t')
        self.assertIn('show(.inbox)', routes)
        self.assertIn('model.shellSettings = true', routes)

    def test_ui_launch_helpers_preserve_default_navigation(self):
        source = (APPLE / 'PersonaUITests/UITestStyle.swift').read_text()
        launch = source.split('func launchPersona(navigation: Bool = true) {', 1)[1].split('\n    }', 1)[0]
        # Only the clean Demo Mode reset and the Separate layout (#4206: with nothing picked an iPad starts Merged, and the
        # tests were written for the tabs and the shell) may add arguments; a test that names a layout, or passes
        # navigation: false to test the setting itself, keeps it.
        self.assertEqual(re.findall(r'launchArguments\s*\+=\s*(\[[^\]]*\])', launch),
                         ['["-app.navigation", "separate"]', '["-openWelcome", "YES"]'])
        self.assertIn('if navigation, !launchArguments.contains("-app.navigation")', launch)
        self.assertIn('Self.wantsDemo(launchArguments)', launch)
        self.assertRegex(launch, r'\blaunch\(\)')
        self.assertNotIn('ProcessInfo', launch)
        harness = (ROOT / 'scripts/app/cli-tests/clitest.py').read_text()
        self.assertRegex(harness, r'(?m)^PINNED = \["-ui\.style", os\.environ\.get\("PERSONA_UI_STYLE", "glass"\)\]$')
        assets = (ROOT / 'scripts/assets/make_readme_assets.py').read_text()
        fresh = assets.split('def fresh(', 1)[1].split('\ndef ', 1)[0]
        # Capture controls can select style/color, but cannot choose a different navigation tree.
        self.assertEqual(set(re.findall(r'"(-ui\.[^"]+)"', fresh)), {'-ui.style', '-ui.color'})

    def test_independent_link_package_contains_only_persona_screen(self):
        package = (APPLE / 'PersonaLink/Package.swift').read_text()
        self.assertIn('name: "PersonaScreen"', package)
        self.assertIn('path: "Sources/PersonaScreen"', package)
        self.assertNotIn('path: "Sources/PersonaLink"', package)
        self.assertFalse((APPLE / 'PersonaLink/Info.plist').exists())
        script = (ROOT / 'scripts/app/mac-link.sh').read_text()
        # Persona Screen is the package's one product, built by name (#7927: swift build, kept by its sources).
        self.assertIn('PersonaScreen "$@"', script)
        self.assertNotIn('--product PersonaLink', script)
        self.assertIn('persona-link" install --node-bin', script)

    def test_menu_bar_app_is_a_package_of_its_own_with_the_canonical_process_name(self):
        # Apart from Persona Screen's package, so building the App never changes Screen's bits (#7927).
        package = (APPLE / 'PersonaLink/App/Package.swift').read_text()
        self.assertIn('name: "PersonaLink"', package)
        self.assertIn('path: "Sources/PersonaLink"', package)
        info = plistlib.loads((APPLE / 'PersonaLink/App/Info.plist').read_bytes())
        self.assertEqual(info['CFBundleExecutable'], 'PersonaLink')
        self.assertTrue(info['LSUIElement'])
        script = (ROOT / 'scripts/app/mac-link.sh').read_text()
        self.assertIn('-scheme PersonaLink', script)
        self.assertIn('cp "$products/PersonaLink" "$APP/Contents/MacOS/PersonaLink"', script)
        self.assertIn('pkill -x PersonaLink', script)
        self.assertIn('open "/Applications/Persona Link.app"', script)

    def test_entitlement_checker_accepts_only_the_canonical_product(self):
        spec = importlib.util.spec_from_file_location('entitlements', ROOT / 'scripts/release/entitlements.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(Path(module.PROJECT), APPLE / 'Persona.xcodeproj')
        with tempfile.TemporaryDirectory() as directory:
            former = Path(directory) / (FORMER_NAME + '.app')
            former.mkdir()
            with self.assertRaises(SystemExit):
                module.app_bundle(directory)
            app = Path(directory) / 'Persona.app'
            app.mkdir()
            self.assertEqual(Path(module.app_bundle(directory)), app)
            self.assertTrue(former.is_dir())
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(SystemExit):
                module.app_bundle(directory)


if __name__ == '__main__':
    unittest.main()
