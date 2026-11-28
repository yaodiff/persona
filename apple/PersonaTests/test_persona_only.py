"""Compiler-free Persona-only Apple guards (#277).

Run with Python and PyYAML: python3 -B apple/PersonaTests/test_persona_only.py
These checks complement, and never replace, Apple builds and Swift tests.
"""

import collections
import json
from pathlib import Path
import plistlib
import re
import shlex
import subprocess
import unittest
import xml.etree.ElementTree as ET

import yaml


APPLE = Path(__file__).resolve().parents[1]
_APPLE_FILES = None

TEST_ONLY_TARGET_EXCEPTIONS = {
    "AboutVersionAccessibilityHost":
        "Standalone test process for the shared About row accessibility tree; never embedded in Persona (#11150).",
    "AboutVersionAccessibilityUITests":
        "UI tests for the standalone About row accessibility host, not the production app (#11150).",
}
TEST_ONLY_SCHEME_EXCEPTIONS = {
    "AboutVersionAccessibilityHost": "Runs only the standalone About row accessibility UI tests (#11150).",
    "AboutVersionVisualTests": "Builds PersonaTests without PersonaUITests for About row visual comparisons (#11150).",
    "PersonaOfflineUnitTests": "Builds and runs offline PersonaTests without PersonaUITests (#12474).",
}


def is_allowed_product_name(name, exceptions):
    return name.startswith("Persona") or name in exceptions


def apple_files():
    """The repository's files under apple/: tracked, or new and not ignored (#7026).

    Build output (DerivedData under apple/build/), local generated projects and other files git ignores are not the
    repository's, so they are never checked, wherever a checkout puts them. Without git (an unpacked archive) every file
    is checked, as before.
    """
    global _APPLE_FILES
    if _APPLE_FILES is None:
        try:
            listed = subprocess.run(
                ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."],
                cwd=APPLE, capture_output=True, check=True).stdout.decode("utf-8").split("\0")
            paths = [APPLE / name for name in dict.fromkeys(listed) if name]
            _APPLE_FILES = sorted(path for path in paths if path.is_file() or path.is_symlink())
        except (OSError, subprocess.CalledProcessError):
            _APPLE_FILES = sorted(path for path in APPLE.rglob("*") if path.is_file() or path.is_symlink())
    return _APPLE_FILES


def unique_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate key: {key}")
        result[key] = value
    return result


def check_yaml_keys(node):
    """Inspect the syntax tree: safe_load otherwise silently drops duplicate keys."""
    if isinstance(node, yaml.MappingNode):
        keys = set()
        for key, value in node.value:
            if key.value in keys:
                raise ValueError(f"duplicate YAML key {key.value} at line {key.start_mark.line + 1}")
            keys.add(key.value)
            check_yaml_keys(value)
    elif isinstance(node, yaml.SequenceNode):
        for value in node.value:
            check_yaml_keys(value)


def source(path):
    return (APPLE / path).read_text(encoding="utf-8")


def widget_source_for_platform(platform):
    output = []
    branches = []
    active = True
    for line in source("Shared/ExtensionSystemWidget.swift").splitlines():
        directive = line.strip()
        if directive.startswith(("#if ", "#elseif ")):
            expression = directive.split(" ", 1)[1]
            predicates = re.findall(r"(!?)os\((\w+)\)", expression)
            if not predicates:
                raise ValueError(f"unsupported widget condition: {expression}")
            matches = any((platform == name) != bool(negated) for negated, name in predicates)
            if directive.startswith("#if "):
                branches.append([active, matches])
            else:
                parent, matched = branches[-1]
                matches = matches and not matched
                branches[-1] = [parent, matched or matches]
            active = branches[-1][0] and matches
        elif directive == "#else":
            active = branches[-1][0] and not branches[-1][1]
            branches[-1][1] = True
        elif directive == "#endif":
            active = branches.pop()[0]
        elif active:
            output.append(line)
    if branches:
        raise ValueError("unclosed widget condition")
    return "\n".join(output)


class PersonaOnlyTests(unittest.TestCase):
    def test_vision_widgets_embed_read_only_bundle_with_shared_storage(self):
        targets = yaml.safe_load(source("project.yml"))["targets"]
        self.assertIn("PersonaVisionWidgets", targets)
        app = targets["PersonaVision"]
        widget = targets["PersonaVisionWidgets"]
        self.assertIn({"target": "PersonaVisionWidgets"}, app["dependencies"])
        self.assertEqual(widget["platform"], "visionOS")
        self.assertEqual(widget["type"], "app-extension")
        settings = widget["settings"]["base"]
        self.assertEqual(settings["PRODUCT_BUNDLE_IDENTIFIER"], "$(PERSONA_BUNDLE_ID).liveactivity")
        self.assertEqual(settings["PRODUCT_MODULE_NAME"], "PersonaWidgets")
        self.assertEqual(settings["PRODUCT_NAME"], "PersonaWidgets")
        self.assertIn("WIDGET_EXTENSION", settings["SWIFT_ACTIVE_COMPILATION_CONDITIONS"])
        self.assertNotIn("TARGETED_DEVICE_FAMILY", settings)
        # The watch widgets' shared sources, less the watch face's own (the Milk Frog, All-Day Recording), which visionOS does not draw.
        shared = {item["path"] for item in targets["PersonaWatchWidgets"]["sources"] if item["path"].startswith("Shared/")}
        watch_pet = {"Shared/WatchPetSnapshot.swift", "Shared/Recording/AllDaySnapshot.swift", "Shared/Design/WatchFaceTokens.swift",
                     # The phone's Record complication allowance belongs to the watch face too, not visionOS widgets.
                     "Shared/Recording/PhoneRecordingAllowance.swift"}
        self.assertTrue(watch_pet.issubset(shared))
        # The water and protein complications (#13275) are the watch face's too: visionOS has no complications.
        watch_intake = {"Shared/WatchIntake.swift", "Shared/WatchIntakeConfiguration.swift", "Shared/WatchIntakeComplicationView.swift",
                        "Shared/StatusPalette.swift", "Shared/Design/Palette.swift", "Shared/Design/HealthIntakePalette.swift"}
        self.assertTrue(watch_intake.issubset(shared))
        vision_sources = {item["path"] for item in widget["sources"]}
        self.assertTrue(watch_pet.isdisjoint(vision_sources))
        self.assertTrue(watch_intake.isdisjoint(vision_sources))
        self.assertEqual(vision_sources, (shared - watch_pet - watch_intake) | {"PersonaVisionWidgets"})
        for target in (app, widget):
            self.assertEqual(target["entitlements"]["properties"]["com.apple.security.application-groups"], ["$(PERSONA_APP_GROUP)"])
            self.assertEqual(target["info"]["properties"]["PersonaAppGroup"], "$(PERSONA_APP_GROUP)")
            entitlements = plistlib.loads((APPLE / target["entitlements"]["path"]).read_bytes())
            self.assertEqual(entitlements["com.apple.security.application-groups"], ["$(PERSONA_APP_GROUP)"])
            info = plistlib.loads((APPLE / target["info"]["path"]).read_bytes())
            self.assertEqual(info["PersonaAppGroup"], "$(PERSONA_APP_GROUP)")
        info = widget["info"]["properties"]
        self.assertEqual(info["NSExtension"]["NSExtensionPointIdentifier"], "com.apple.widgetkit-extension")
        bundle = source("PersonaVisionWidgets/PersonaVisionWidgetsBundle.swift")
        self.assertIn("LanguageStore.shared.follow(LanguageStore.shared.groupResolved)", bundle)
        self.assertIn("var body: some Widget { ExtensionSystemWidget() }", bundle)
        self.assertNotIn("ActivityConfiguration", bundle)
        self.assertNotIn("ControlWidget", bundle)

    def test_vision_widgets_use_only_visionos_26_families(self):
        vision = widget_source_for_platform("visionOS")
        self.assertIn("struct ExtensionSystemWidget: Widget", vision)
        self.assertIn("[.systemSmall, .systemMedium, .systemLarge]", vision)
        self.assertNotRegex(vision, r"\.accessory(?:Circular|Inline|Rectangular)")
        for platform in ("iOS", "watchOS"):
            supported = widget_source_for_platform(platform)
            self.assertIn(".accessoryCircular", supported)
            self.assertIn(".accessoryInline", supported)
        self.assertNotIn(".systemSmall", widget_source_for_platform("watchOS"))

    def test_vision_widget_sync_and_invalidation_are_enabled(self):
        # visionOS syncs and invalidates extension widgets as iOS and watchOS do. Shared/ builds for all three and
        # Persona/ for iOS and visionOS, so this code takes no platform branch at all (#13399): none may leave visionOS out.
        sync = source("Shared/ExtensionSurfaceSync.swift")
        self.assertNotRegex(sync, r"#(?:if|elseif)[^\n]*\bos\(")
        self.assertEqual(sync.count("WidgetCenter.shared.reloadTimelines(ofKind: ExtensionSystemWidget.kind)"), 2)
        self.assertIn("ExtensionWidgetReconciliation.configurations()", sync)
        app = source("Persona/App/PersonaApp.swift")
        self.assertIn('if let widget = info["extension_widget"]', app)
        self.assertNotRegex(app, r'#if os\(iOS\)\s+if let widget = info\["extension_widget"\]')
        model = source("Persona/App/AppModel.swift")
        self.assertIn("ExtensionWidgetReconciliation.invalidate(", model)
        self.assertNotRegex(model, r"#if os\(iOS\)\s+ExtensionWidgetReconciliation.invalidate")
        vision = widget_source_for_platform("visionOS")
        self.assertIn('Open Persona to sync extension capabilities.', vision)
        self.assertNotIn('Open Persona on iPhone to sync extension capabilities.', vision)
        catalog = json.loads(source("Shared/Localization/Localizable.xcstrings"))
        localizations = catalog["strings"]["Open Persona to sync extension capabilities."]["localizations"]
        self.assertTrue({"en", "zh-Hans", "ja"}.issubset(localizations))

    def test_retired_native_identifiers_are_confined_to_one_compatibility_source(self):
        # The user-approved exception is one physical compatibility source, never a global relaxation.
        migration = APPLE / "Shared/NativeStateMigration.swift"
        former = "jar" + "vis"
        allowed = {migration.resolve()}
        for path in (path for path in apple_files() if path.suffix == ".swift"):
            if former in path.read_text(encoding="utf-8").lower():
                self.assertIn(path.resolve(), allowed, path.relative_to(APPLE))
        text = migration.read_text(encoding="utf-8")
        self.assertIn("Compatibility never deletes a previous store", text)

    def test_native_controls_keep_stable_kinds_and_register_interim_kinds_once(self):
        controls = source("PersonaWidgets/PersonaControls.swift")
        for stable in ["controlAskKind", "controlAgentsKind"]:
            self.assertEqual(controls.count(f"Self.configuration(kind: LegacyNames.{stable})"), 1)
        for interim in ["persona.control.ask", "persona.control.agents"]:
            self.assertEqual(controls.count(f'configuration(kind: "{interim}")'), 1)
        self.assertEqual(controls.count('Self.configuration(kind: "persona.control.dictate")'), 1)
        self.assertEqual(controls.count('Self.configuration(kind: "persona.control.message")'), 1)
        self.assertEqual(controls.count('Self.configuration(kind: "persona.control.call")'), 1)
        bundle = source("PersonaWidgets/PersonaWidgetsBundle.swift")
        for widget in ["AskPersonaControl", "PersonaAgentsControl", "DictatePersonaControl", "CallPersonaControl",
                       "MessagePersonaControl", "CompatibleAssistantControl", "CompatibleAgentsControl", "FleetLiveActivity",
                       "CompatibleFleetLiveActivity"]:
            self.assertEqual(len(re.findall(rf"\b{widget}\(\)", bundle)), 1)

    def test_duplicate_key_guards_reject_silent_overwrites(self):
        with self.assertRaises(ValueError):
            json.loads('{"Persona": 1, "Persona": 2}', object_pairs_hook=unique_pairs)
        with self.assertRaises(ValueError):
            check_yaml_keys(yaml.compose("schemes:\n  Persona: {}\n  Persona: {}\n"))

    def test_yaml_has_unique_keys_and_build_conditions(self):
        for path in apple_files():
            if path.suffix not in {".yml", ".yaml"}:
                continue
            with self.subTest(path=path.relative_to(APPLE)):
                text = path.read_text(encoding="utf-8")
                check_yaml_keys(yaml.compose(text))
                for line in text.splitlines():
                    if "SWIFT_ACTIVE_COMPILATION_CONDITIONS:" in line:
                        tokens = shlex.split(line.split(":", 1)[1].strip().strip('"'))
                        self.assertEqual(len(tokens), len(set(tokens)), line)
                    if "OTHER_SWIFT_FLAGS:" in line:
                        flags = re.findall(r"-D\s*([A-Za-z_][A-Za-z0-9_]*)", line)
                        self.assertEqual(len(flags), len(set(flags)), line)

    def test_project_uses_only_persona_targets_and_schemes(self):
        project = yaml.safe_load(source("project.yml"))
        self.assertEqual(project["name"], "Persona")
        canonical_schemes = {"Persona", "PersonaWatch", "PersonaVision"}
        self.assertEqual(set(project["schemes"]), {*canonical_schemes, *TEST_ONLY_SCHEME_EXCEPTIONS})
        targets = project["targets"]
        for name, target in targets.items():
            with self.subTest(target=name):
                self.assertTrue(is_allowed_product_name(name, TEST_ONLY_TARGET_EXCEPTIONS))
                for dependency in target.get("dependencies", []):
                    if "target" in dependency:
                        self.assertIn(dependency["target"], targets)
                for item in target["sources"]:
                    self.assertTrue((APPLE / item["path"]).exists(), item["path"])
        for name, scheme in project["schemes"].items():
            with self.subTest(scheme=name):
                self.assertTrue(is_allowed_product_name(name, TEST_ONLY_SCHEME_EXCEPTIONS))
                self.assertTrue(set(scheme["build"]["targets"]).issubset(targets))
        modules = {
            "Persona": "Persona", "PersonaVision": "Persona", "PersonaWatch": "Persona",
            "PersonaWidgets": "PersonaWidgets", "PersonaNotify": "PersonaNotify", "PersonaVisionNotify": "PersonaNotify",
            "PersonaShare": "PersonaShare", "PersonaVisionShare": "PersonaShare",
            "PersonaWatchWidgets": "PersonaWatchWidgets", "PersonaVisionWidgets": "PersonaWidgets",
        }
        for target, module in modules.items():
            self.assertEqual(targets[target]["settings"]["base"]["PRODUCT_MODULE_NAME"], module)
        self.assertIn("*.py", targets["PersonaTests"]["sources"][0]["excludes"])
        projects = {APPLE / path.relative_to(APPLE).parts[0] for path in apple_files()
                    if path.relative_to(APPLE).parts[0].endswith(".xcodeproj")}
        for path in sorted(projects):
            self.assertEqual(path.name, "Persona.xcodeproj")
            self.assertFalse(path.is_symlink(), "project aliases must not return")

    def test_about_version_test_only_exceptions_are_narrow_and_reasoned(self):
        expected_targets = {"AboutVersionAccessibilityHost", "AboutVersionAccessibilityUITests"}
        expected_schemes = {"AboutVersionAccessibilityHost", "AboutVersionVisualTests", "PersonaOfflineUnitTests"}
        for exceptions, expected in ((TEST_ONLY_TARGET_EXCEPTIONS, expected_targets),
                                     (TEST_ONLY_SCHEME_EXCEPTIONS, expected_schemes)):
            self.assertEqual(set(exceptions), expected)
            for name, reason in exceptions.items():
                with self.subTest(name=name):
                    self.assertIsInstance(reason, str)
                    self.assertTrue(reason.strip(), "a test-only exception needs a deliberate reason")
        for exceptions, expected in ((TEST_ONLY_TARGET_EXCEPTIONS, expected_targets),
                                     (TEST_ONLY_SCHEME_EXCEPTIONS, expected_schemes)):
            for name in expected:
                with self.subTest(registered=name):
                    self.assertTrue(is_allowed_product_name(name, exceptions))
            with self.subTest(unregistered="AboutVersionUnregisteredTests"):
                self.assertFalse(is_allowed_product_name("AboutVersionUnregisteredTests", exceptions))
        self.assertTrue(is_allowed_product_name("Persona", {}))

    def test_about_version_test_targets_and_schemes_keep_their_wiring(self):
        project = yaml.safe_load(source("project.yml"))
        targets = project["targets"]
        schemes = project["schemes"]
        ui_tests = targets["AboutVersionAccessibilityUITests"]
        self.assertIn({"target": "AboutVersionAccessibilityHost"}, ui_tests["dependencies"])
        self.assertEqual(ui_tests["settings"]["base"]["TEST_TARGET_NAME"], "AboutVersionAccessibilityHost")
        accessibility = schemes["AboutVersionAccessibilityHost"]
        self.assertEqual(accessibility["build"]["targets"], {
            "AboutVersionAccessibilityHost": "all",
            "AboutVersionAccessibilityUITests": ["test"],
        })
        self.assertEqual(accessibility["test"]["targets"], ["AboutVersionAccessibilityUITests"])
        visual = schemes["AboutVersionVisualTests"]
        self.assertEqual(visual["build"]["targets"], {"Persona": "all", "PersonaTests": ["test"]})
        self.assertEqual(visual["test"]["targets"], ["PersonaTests"])

    def test_plists_have_unique_keys_and_exact_compatible_url_schemes(self):
        registered = set()
        expected = {"Persona/Resources/Info.plist", "Vision/Resources/Info.plist", "PersonaWatch/Info.plist",
                    "PersonaLink/App/Info.plist"}  # Persona Link opens pairing links (persona://link?…)
        for path in apple_files():
            if path.suffix not in {".plist", ".entitlements"}:
                continue
            relative = path.relative_to(APPLE).as_posix()
            with self.subTest(path=relative):
                tree = ET.parse(path)
                for dictionary in tree.iter("dict"):
                    keys = [key.text for key in dictionary if key.tag == "key"]
                    self.assertEqual(len(keys), len(set(keys)), keys)
                values = plistlib.loads(path.read_bytes())
                if "CFBundleURLTypes" in values:
                    self.assertIn(relative, expected)
                    schemes = [scheme for entry in values["CFBundleURLTypes"]
                               for scheme in entry["CFBundleURLSchemes"]]
                    self.assertEqual(schemes, ["persona"] if relative == "PersonaWatch/Info.plist" else ["persona", "jarvis"])
                    registered.add(relative)
        self.assertEqual(registered, expected)
        project = yaml.safe_load(source("project.yml"))
        for target in ["Persona", "PersonaVision"]:
            settings = project["targets"][target]
            properties = settings["info"]["properties"]
            self.assertEqual(len(properties["CFBundleURLTypes"]), 1)
            self.assertEqual(properties["CFBundleURLTypes"][0]["CFBundleURLSchemes"], ["persona", "jarvis"])
            self.assertEqual(properties["PersonaPushKeychainGroup"], "$(DEVELOPMENT_TEAM).$(PERSONA_BUNDLE_ID).push")
            plist = plistlib.loads((APPLE / settings["info"]["path"]).read_bytes())
            for key in ["PersonaPushRelay", "PersonaGettingStartedURL", "PersonaPushKeychainGroup"]:
                self.assertEqual(plist[key], properties[key])
        for target in ["Persona", "PersonaWidgets"]:
            settings = project["targets"][target]
            self.assertEqual(settings["info"]["properties"]["PersonaAppGroup"], "$(PERSONA_APP_GROUP)")
            self.assertEqual(settings["entitlements"]["properties"]["com.apple.security.application-groups"], ["$(PERSONA_APP_GROUP)"])
        for path, principal in [("PersonaNotify/Info.plist", "NotificationService"), ("PersonaShare/Info.plist", "ShareViewController")]:
            values = plistlib.loads((APPLE / path).read_bytes())
            self.assertEqual(values["NSExtension"]["NSExtensionPrincipalClass"], f"$(PRODUCT_MODULE_NAME).{principal}")

    def test_extension_system_targets_keep_credentials_and_renderers_separate(self):
        project = yaml.safe_load(source("project.yml"))
        targets = project["targets"]
        self.assertEqual(project["options"]["deploymentTarget"]["watchOS"], "10.0")
        self.assertEqual(targets["PersonaWatchWidgets"]["settings"]["base"]["PRODUCT_BUNDLE_IDENTIFIER"],
                         "$(PERSONA_WATCH_WIDGET_BUNDLE_ID)")
        self.assertIn({"target": "PersonaWatchWidgets"}, targets["PersonaWatch"]["dependencies"])
        for target in ["PersonaWatch", "PersonaWatchWidgets"]:
            settings = targets[target]
            self.assertEqual(settings["info"]["properties"]["PersonaAppGroup"], "$(PERSONA_WATCH_APP_GROUP)")
            self.assertEqual(settings["entitlements"]["properties"]["com.apple.security.application-groups"],
                             ["$(PERSONA_WATCH_APP_GROUP)"])
            plist = plistlib.loads((APPLE / settings["info"]["path"]).read_bytes())
            self.assertEqual(plist["PersonaAppGroup"], "$(PERSONA_WATCH_APP_GROUP)")
        for target in ["PersonaWidgets", "PersonaWatchWidgets"]:
            paths = [item["path"] for item in targets[target]["sources"]]
            for path in ["Shared/ExtensionSurfaceStore.swift", "Shared/ExtensionSurfaceView.swift", "Shared/ExtensionSystemWidget.swift"]:
                self.assertIn(path, paths)
            self.assertNotIn("Shared/APIClient.swift", paths)
            self.assertNotIn("Persona/Views/Settings/ExtensionUIRenderer.swift", paths)
            self.assertIn("WIDGET_EXTENSION", targets[target]["settings"]["base"]["SWIFT_ACTIVE_COMPILATION_CONDITIONS"])
        store = source("Shared/ExtensionSurfaceStore.swift")
        self.assertIn("completeFileProtectionUntilFirstUserAuthentication", store)
        self.assertIn("isExcludedFromBackup = true", store)
        self.assertIn("flock(descriptor, LOCK_EX)", store)
        self.assertNotIn("FleetLookStore", store)
        watch = source("PersonaWatch/WatchExtensionSurfaces.swift")
        self.assertIn("ExtensionSurfaceSync.credentials(configs", watch)
        self.assertNotIn("HubStore", watch)
        self.assertNotIn("registry", watch.replace("the phone's registry", ""))
        self.assertIn("answerOnly: true", watch)
        self.assertNotIn("/api/v1/extensions/-/ui", watch)
        self.assertNotIn("api.request(\"POST\"", watch)
        self.assertIn(".projection(link)", watch)
        self.assertNotIn(".deviceTokens()", source("Shared/ExtensionSurfaceSync.swift"))
        self.assertIn("credential.isAnswerOnly", source("Shared/ExtensionSurfaceClient.swift"))
        self.assertIn("ExtensionSurfaceCapabilitiesUnknown()", source("Shared/ExtensionSurfaceClient.swift"))
        intent = source("Shared/RunExtensionActionIntent.swift")
        self.assertIn("intent?.action.id", intent)
        self.assertIn("compiler(>=6.4)", intent)
        self.assertNotIn("intent?.action?.id", intent)
        self.assertNotIn("try IntentSupport.client", intent)

    def test_json_and_string_catalogs_have_unique_keys(self):
        for path in apple_files():
            if path.suffix in {".json", ".xcstrings"}:
                with self.subTest(path=path.relative_to(APPLE)):
                    json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_pairs)
        catalog = json.loads(source("Shared/Localization/Localizable.xcstrings"))
        localized = catalog["strings"]["Persona"]["localizations"]
        for language in ["en", "zh-Hans", "ja"]:
            self.assertEqual(localized[language]["stringUnit"]["value"], "Persona")

    def test_agent_preset_strings_have_all_native_languages(self):
        # #2225: New Agent's Preset row and the plan card's "Preset <name>" chip.
        catalog = json.loads(source("Shared/Localization/Localizable.xcstrings"))
        for key in ["Preset", "Preset %@"]:
            with self.subTest(key=key):
                localizations = catalog["strings"].get(key, {}).get("localizations", {})
                for language in ["en", "zh-Hans", "ja"]:
                    with self.subTest(language=language):
                        value = localizations.get(language, {}).get("stringUnit", {}).get("value", "")
                        self.assertTrue(value.strip(), f"{key} needs {language}")

    def test_agent_preset_native_docs_keep_the_start_contract_unchanged(self):
        profiles = (APPLE.parent / "docs/assistant/model-profiles.md").read_text(encoding="utf-8")
        section = profiles.split("## Agent presets (tiers)", 1)[1].split("## Edit and add profiles", 1)[0]
        self.assertIn("iOS/Mac", section, "document the native New Agent and plan-card behavior")
        self.assertNotIn("iOS, Mac and Android are open in #2225", section)
        protocol = (APPLE.parent / "docs/protocol/protocol.md").read_text(encoding="utf-8")
        start = next(line for line in protocol.splitlines() if line.startswith("| POST | `/api/v1/sessions` |"))
        request = start.split("→", 1)[0]
        self.assertNotIn('"tier"', request, "Agent presets resolve to launcher/model/effort before POST /sessions")

    def test_brand_and_avatar_symbols_do_not_collide(self):
        icons = source("Shared/Design/Icons.swift")
        for mac in [False, True]:
            # The device icon has one declaration per platform, not two in the same compilation.
            platform = re.sub(r"#if os\(macOS\)\s*(.*?)#else\s*(.*?)#endif",
                              lambda match: match.group(1 if mac else 2), icons, flags=re.DOTALL)
            names = re.findall(r"\bstatic let (\w+)\s*=", platform)
            self.assertEqual(len(names), len(set(names)), [name for name, n in collections.Counter(names).items() if n > 1])
        self.assertIn('static let avatar = "person.crop.artframe"', icons)
        self.assertIn('static let persona = "sparkle"', icons)
        # Settings' Avatar row (its catalog entry, which Settings draws), not a brand or persona symbol.
        self.assertIn("symbol: Theme.Icon.avatar", source("Persona/Views/Settings/SettingsNavigation.swift"))
        launch = source("PersonaUITests/UITestStyle.swift")
        self.assertEqual(len(re.findall(r"\bfunc launchPersona\(", launch)), 1)
        self.assertNotRegex(launch, r"func launchPersona\([^)]*\)\s*\{\s*launchPersona\(")
        wire = source("Packages/PersonaWire/Sources/PersonaWire/ExtensionWire.swift")
        look = re.search(r"enum ExtensionBuiltinLook\s*\{([^}]+)\}", wire).group(1)
        names = re.findall(r"static let (\w+)\s*=", look)
        self.assertEqual(len(names), len(set(names)))
        self.assertIn('static let all = ["persona"]', look)
        self.assertNotIn('personaVisor', look)
        self.assertNotIn('personaCircuit', look)

    def test_only_mochi_is_drawn_and_milk_frog_is_a_package(self):
        model = source("Persona/Persona/Persona.swift")
        self.assertIn('static let builtins = [builtin]', model)
        self.assertIn('static let builtinID = PersonaSelection.builtinID', model)
        self.assertRegex(model, r'static let builtin = Persona\(id: builtinID,[\s\S]*?design: "mochi"')
        policy = source("Shared/PersonaSelection.swift")
        self.assertIn('static let builtinID = "persona"', policy)
        self.assertIn('static let packageID = "nwverse"', policy)
        design = re.search(r'enum Design[^\{]*\{([^}]+)\}', source("Shared/Design/PersonaFace.swift")).group(1)
        self.assertEqual(re.findall(r'\bcase\s+(\w+)', design), ["mochi"])
        for name in ["Visor", "Circuit", "Pixel", "Facet", "Blob"]:
            self.assertFalse((APPLE / f"Persona/Resources/Assets.xcassets/AppIcon-{name}.appiconset").exists())
            self.assertFalse((APPLE / f"Persona/Resources/Assets.xcassets/AppIcons/app-icon-{name.lower()}.imageset").exists())
        self.assertNotIn("FACES", (APPLE.parent / "scripts/assets/make_alt_icons.py").read_text())
        self.assertFalse((APPLE.parent / "scripts/assets/persona_face").exists())

    def test_context_parsers_share_the_exact_dual_contract(self):
        naming = source("Shared/HubNaming.swift")
        self.assertIn('NSRegularExpression.escapedPattern(for: LegacyNames.product)', naming)
        self.assertIn('static var contextPattern: String { productPattern + "-context" }', naming)
        self.assertIn('static var filePattern: String { productPattern + "-file" }', naming)
        for path in ["Shared/ContextText.swift", "Shared/Injected.swift", "Shared/WatchText.swift",
                     "Persona/Voice/VoiceWire.swift", "Persona/Views/Link/LinkSheet.swift"]:
            with self.subTest(path=path):
                text = source(path)
                self.assertIn("HubNaming.contextPattern", text)
                self.assertNotIn("persona|", text)
                self.assertNotIn("|persona", text)
        closing_source = source("Shared/ContextText.swift")
        pieces = re.search(r'pattern: #"([^"\n]+)"# \+ HubNaming.contextPattern \+ #"([^"\n]+)"#', closing_source)
        self.assertIsNotNone(pieces, "the sanitizer must use the shared context pattern")
        previous = re.search(r'static let product = "([^"\n]+)"', source("Shared/NativeStateMigration.swift")).group(1)
        closing = pieces.group(1) + "(?:persona|" + re.escape(previous) + ")-context" + pieces.group(2)
        regex = re.compile(closing, re.IGNORECASE)
        for product in ["persona", previous]:
            for tag in [f"</{product}-context>", f"</{product.upper()}-CONTEXT>", f"< / {product}-context\t>"]:
                self.assertEqual(regex.sub("", f"a{tag}b"), "ab")
        self.assertIsNone(regex.search("</other-context>"))
        self.assertIn('let removed = replace(text, with: "")', closing_source)
        self.assertIn('return replace(removed, with: "&lt;$1")', closing_source)
        for path in ["Shared/WatchText.swift", "Persona/Views/Chat/VideoAttach.swift"]:
            self.assertIn("HubNaming.filePattern", source(path))

    def test_link_package_and_clients_share_compatible_names_without_duplicates(self):
        # The menu bar App (apple/PersonaLink/App) reads the connector's state from the same data directory the launchd
        # service gives it (PERSONA_HOME), else the canonical one, else the previous name's when that is the one there.
        package = source("PersonaLink/Package.swift")
        self.assertIn('name: "PersonaScreen"', package)
        self.assertIn('path: "Sources/PersonaScreen"', package)
        self.assertNotIn('path: "Sources/PersonaLink"', package)
        service = APPLE.parent / "server/cmd/persona-link/service_mac.go"
        self.assertIn('<key>PERSONA_HOME</key>', service.read_text())
        paths = source("PersonaLink/App/Sources/PersonaLink/LinkStatePaths.swift")
        self.assertIn('environment["PERSONA_HOME"] ?? environment[previousHomeKey] ?? ""', paths)
        self.assertIn('LegacyNames.product.uppercased() + "_HOME"', paths)
        self.assertIn('home.appendingPathComponent(".persona", isDirectory: true)', paths)
        self.assertIn('home.appendingPathComponent("." + LegacyNames.product, isDirectory: true)', paths)
        self.assertIn("lstat(previous.path, &old)", paths)
        self.assertIn("a.st_dev == b.st_dev, a.st_ino == b.st_ino", paths)
        self.assertNotIn("moveItem(", paths)
        self.assertNotIn("removeItem(", paths)
        model = source("PersonaLink/App/Sources/PersonaLink/LinkModel.swift")
        self.assertEqual(model.count('environment["PERSONA_HOME"] = stateURL.path'), 1)
        self.assertIn('LegacyNames.product.uppercased() + "_HOME"', model)
        self.assertNotIn('"--parent"', model, "launchd runs the connector; the App never hosts it")
        self.assertIn('static let schemes: Set<String> = ["persona", LegacyNames.product]',
                      source("PersonaLink/App/Sources/PersonaLink/PairLink.swift"))
        route = source("Shared/HubPageLink.swift")
        self.assertIn('case "persona", LegacyNames.conversationPath:', route)
        for path in ["Shared/APIClient.swift", "Shared/ExtensionSystemLink.swift"]:
            self.assertIn("HubNaming.accepts(scheme:", source(path))
        self.assertIn('["persona", LegacyNames.urlScheme].contains(url.scheme?.lowercased() ?? "")',
                      source("LiveActivity/PersonaActivityAttributes.swift"))
        self.assertIn('case "persona", LegacyNames.urlScheme:', source("Persona/Views/Chat/AssistantHTMLCard.swift"))
        self.assertFalse((APPLE / "PersonaLink/Sources/PersonaLink").exists())
        self.assertFalse((APPLE / "PersonaLink/Tests/PersonaLinkTests").exists())
        compatibility = APPLE / "PersonaLink/App/Sources/PersonaLink/NativeCompatibility.swift"
        self.assertTrue(compatibility.is_symlink())
        self.assertEqual(compatibility.resolve(), (APPLE / "Shared/NativeStateMigration.swift").resolve())
        for path in (path for path in apple_files() if path.suffix == ".swift"):
            with self.subTest(path=path.relative_to(APPLE)):
                text = path.read_text(encoding="utf-8")
                self.assertNotRegex(text, r'"persona"\s*,\s*"persona"')
                self.assertNotIn("persona|persona", text)
                self.assertNotRegex(text, r'replacingOccurrences\(of: "([^"]+)", with: "\1"\)')
                self.assertNotRegex(text, r'\["PERSONA_HOME"\][^\n]*(?:\|\||\?\?)[^\n]*\["PERSONA_HOME"\]')



if __name__ == "__main__":
    unittest.main()
