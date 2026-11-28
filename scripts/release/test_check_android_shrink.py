import importlib.util
from pathlib import Path
import re
import struct
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("check_android_shrink", Path(__file__).with_name("check_android_shrink.py"))
shrink = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(shrink)
DexClass = shrink.DexClass

# The shape of `aapt2 dump xmltree --file AndroidManifest.xml` for a merged manifest.
MANIFEST = '''N: android=http://schemas.android.com/apk/res/android (line=2)
  E: manifest (line=2)
    A: package="com.example.persona" (Raw: "com.example.persona")
    E: application (line=10)
      A: http://schemas.android.com/apk/res/android:name(0x01010003)="persona.android.PersonaApp" (Raw: "persona.android.PersonaApp")
      A: http://schemas.android.com/apk/res/android:appComponentFactory(0x0101057a)="androidx.core.app.CoreComponentFactory" (Raw: "androidx.core.app.CoreComponentFactory")
      E: activity (line=12)
        A: http://schemas.android.com/apk/res/android:name(0x01010003)="persona.android.MainActivity" (Raw: "persona.android.MainActivity")
      E: provider (line=20)
        A: http://schemas.android.com/apk/res/android:name(0x01010003)="androidx.startup.InitializationProvider" (Raw: "androidx.startup.InitializationProvider")
        E: meta-data (line=21)
          A: http://schemas.android.com/apk/res/android:name(0x01010003)="androidx.work.WorkManagerInitializer" (Raw: "androidx.work.WorkManagerInitializer")
          A: http://schemas.android.com/apk/res/android:value(0x01010024)="androidx.startup" (Raw: "androidx.startup")
        E: meta-data (line=22)
          A: http://schemas.android.com/apk/res/android:name(0x01010003)="persona.example.flag" (Raw: "persona.example.flag")
          A: http://schemas.android.com/apk/res/android:value(0x01010024)="on" (Raw: "on")
'''


BRIDGE = "persona.core.designsystem.MarkdownRenderer$Bridge"
LOOKAHEAD = "androidx.compose.ui.node.LookaheadDelegate"
LOOKAHEAD_IMPL = "androidx.compose.ui.node.InnerNodeCoordinator$LookaheadDelegateImpl"
BASELINE_OWNER = ("getAlignmentLinesOwner", "()Landroidx/compose/ui/node/AlignmentLinesOwner;")
BASELINE_CALCULATION = ("calculateAlignmentLine", "(Landroidx/compose/ui/layout/AlignmentLine;)I")
SSH_ALGORITHMS = ("persona.core.hub.tunnel.TinkX25519", "persona.core.hub.tunnel.TinkEd25519", "com.jcraft.jsch.DH25519",
                  "com.jcraft.jsch.UserAuthPublicKey", "com.jcraft.jsch.jce.AES128GCM")

# The shapes of R8's seeds.txt (classes, then `Class: member`) and usage.txt (`Class` removed whole, `Class:` then the
# members removed from a kept one).
SEEDS = """persona.core.hub.HubFailure
persona.core.hub.HubClient$Refused
persona.core.contracts.api.ModelPurposeNotFound
coil3.network.HttpException
persona.core.hub.HubFailure: java.lang.String getCode()
androidx.work.ListenableWorker
"""
USAGE = """persona.core.contracts.api.ModelPurposeNotFound
persona.core.hub.HubClient:
    public void unused()
"""


def kept_release():
    init = {shrink.NO_ARGS}
    return {
        "persona.android.PersonaApp": DexClass("android.app.Application", methods=set(init)),
        "androidx.core.app.CoreComponentFactory": DexClass("android.app.AppComponentFactory", methods=set(init)),
        "persona.android.MainActivity": DexClass("android.app.Activity", methods=set(init)),
        "androidx.startup.InitializationProvider": DexClass("android.content.ContentProvider", methods=set(init)),
        LOOKAHEAD: DexClass("java.lang.Object", methods={BASELINE_OWNER}),
        LOOKAHEAD_IMPL: DexClass("java.lang.Object", methods={BASELINE_CALCULATION}),
        "androidx.work.WorkManagerInitializer": DexClass("java.lang.Object", methods=set(init)),
        BRIDGE: DexClass("java.lang.Object", methods=set(shrink.REQUIRED_MEMBERS[BRIDGE])),
        "persona.core.hub.HubFailure": DexClass(
            "java.io.IOException", methods=set(shrink.REQUIRED_MEMBERS["persona.core.hub.HubFailure"])),
        **{name: DexClass("java.lang.Object", methods=set(init)) for name in SSH_ALGORITHMS},
        **{name: DexClass("java.lang.Object", methods=set(shrink.REQUIRED_MEMBERS[name]))
           for name in ("persona.core.rustcore.NativeBridge", "persona.core.rustcore.HostBridge")},
        "androidx.room.RoomDatabase": DexClass("java.lang.Object", access=shrink.ACC_ABSTRACT),
        "persona.core.data.ItemsDatabase": DexClass("androidx.room.RoomDatabase", access=shrink.ACC_ABSTRACT),
        "persona.core.data.ItemsDatabase_Impl": DexClass("persona.core.data.ItemsDatabase", methods=set(init)),
        "androidx.work.ListenableWorker": DexClass("java.lang.Object", access=shrink.ACC_ABSTRACT),
        "androidx.work.CoroutineWorker": DexClass("androidx.work.ListenableWorker", access=shrink.ACC_ABSTRACT),
        "persona.platform.notify.ConnectionWatchdog": DexClass("androidx.work.CoroutineWorker",
                                                              methods={shrink.WORKER_INIT}),
        "persona.core.navigation.SettingsPageRoute$Storage": DexClass(
            "java.lang.Object", (shrink.NAV_KEY,), annotations={shrink.INNER_CLASS}),
        "persona.core.hub.HubClient$Refused": DexClass("java.io.IOException", annotations={shrink.INNER_CLASS}),
        "coil3.network.HttpException": DexClass("java.lang.RuntimeException"),
        # R8's stub of a platform exception newer than minSdk: no keep rule can list it.
        "android.app.ServiceStartNotAllowedException": DexClass("java.lang.IllegalStateException"),
        # Lost its InnerClass annotation, but neither an exception nor a route: its name is never reported.
        "persona.core.designsystem.Tokens$Space": DexClass("java.lang.Object"),
    }


class ShrinkGuardTests(unittest.TestCase):
    def test_a_release_that_keeps_everything_reached_by_name_passes(self):
        manifest = shrink.manifest_classes(MANIFEST)
        self.assertEqual(manifest, [
            ("application", "persona.android.PersonaApp"),
            ("appComponentFactory", "androidx.core.app.CoreComponentFactory"),
            ("activity", "persona.android.MainActivity"),
            ("provider", "androidx.startup.InitializationProvider"),
            ("startup initializer", "androidx.work.WorkManagerInitializer"),
        ])
        self.assertEqual(shrink.problems(kept_release(), manifest), [])
        self.assertEqual(shrink.problems(kept_release(), manifest, seeds=SEEDS, usage=USAGE), [])

    def test_each_removed_keep_stops_the_release(self):
        manifest = shrink.manifest_classes(MANIFEST)
        cases = {
            "baseline owner": (lambda c: c[LOOKAHEAD].methods.clear(),
                               "LookaheadDelegate.getAlignmentLinesOwner()Landroidx/compose/ui/node/AlignmentLinesOwner;: removed"),
            "baseline calculation": (lambda c: c[LOOKAHEAD_IMPL].methods.clear(),
                                     "LookaheadDelegateImpl.calculateAlignmentLine(Landroidx/compose/ui/layout/AlignmentLine;)I: removed"),
            "Bridge.done": (lambda c: c[BRIDGE].methods.discard(("done", "(ILjava/lang/String;)V")),
                            f"{BRIDGE}.done(ILjava/lang/String;)V: removed"),
            "hub failure status": (lambda c: c["persona.core.hub.HubFailure"].methods.discard(
                ("getStatus", "()Ljava/lang/Integer;")), "HubFailure.getStatus()Ljava/lang/Integer;: removed"),
            "ssh algorithm": (lambda c: c.pop("persona.core.hub.tunnel.TinkX25519"),
                              "persona.core.hub.tunnel.TinkX25519.<init>()V: removed"),
            "activity": (lambda c: c.pop("persona.android.MainActivity"), "activity persona.android.MainActivity"),
            "initializer": (lambda c: c["androidx.work.WorkManagerInitializer"].methods.clear(),
                            "startup initializer androidx.work.WorkManagerInitializer: no-argument constructor"),
            "worker": (lambda c: c["persona.platform.notify.ConnectionWatchdog"].methods.clear(),
                       "worker persona.platform.notify.ConnectionWatchdog"),
            "database": (lambda c: c.pop("persona.core.data.ItemsDatabase_Impl"),
                         "database persona.core.data.ItemsDatabase: generated"),
            "route name": (lambda c: c["persona.core.navigation.SettingsPageRoute$Storage"].annotations.clear(),
                           "SettingsPageRoute$Storage: InnerClass annotation removed"),
            "exception name": (lambda c: c["persona.core.hub.HubClient$Refused"].annotations.clear(),
                               "HubClient$Refused: InnerClass annotation removed"),
        }
        for label, (remove, expected) in cases.items():
            with self.subTest(label):
                classes = kept_release()
                remove(classes)
                found = shrink.problems(classes, manifest)
                self.assertEqual(len(found), 1, found)
                self.assertIn(expected, found[0])
        self.assertEqual(shrink.problems(kept_release(), manifest, "-dontwarn a.B\n"),
                         ["R8 reported missing classes (missing_rules.txt is not empty)"])

    def test_a_throwable_merged_into_another_class_stops_the_release(self):
        # A seed neither in the dex nor removed was merged away: its reports would carry another class's name. A library
        # exception counts as much as persona's (a lateinit crash reported as coil3.network.HttpException).
        manifest = shrink.manifest_classes(MANIFEST)
        merged = SEEDS + "persona.core.contracts.api.ChannelRejected\nkotlin.UninitializedPropertyAccessException\n"
        self.assertEqual(shrink.problems(kept_release(), manifest, seeds=merged, usage=USAGE), [
            f"{name}: merged into another class, so reports name that class (keep its name in proguard-rules.pro)"
            for name in ("kotlin.UninitializedPropertyAccessException", "persona.core.contracts.api.ChannelRejected")])
        # A rule that lists only persona Throwables as seeds leaves the merged library ones unseen.
        persona_only = SEEDS.replace("coil3.network.HttpException\n", "")
        self.assertEqual(shrink.problems(kept_release(), manifest, seeds=persona_only, usage=USAGE), [
            "Throwables in the dex missing from seeds.txt (1: coil3.network.HttpException): proguard-rules.pro lost "
            "the rule that keeps every Throwable's name"])


    def test_release_is_shrunk_without_obfuscation_and_no_module_pulls_the_extended_icons(self):
        # Shrinking and optimizing take the release APK from about 93 MB to 19.5 MB, and check_android_shrink.py passes an
        # unshrunk APK (the rollback), so only this line notices a merge or an edit that turns it off: a rollback changes
        # it too. Obfuscated or merged classes would break in_app and the names in error reports, and the merge check
        # reads the Throwables the keepnames rule lists as seeds; the extended icons were 21.9 MB of dex in debug builds
        # for one unused button.
        android = Path(__file__).resolve().parents[2] / "android"
        release = re.search(r"\brelease\s*\{([^{}]*)\}", (android / "app/build.gradle.kts").read_text())
        self.assertIsNotNone(release, "no buildTypes release block in android/app/build.gradle.kts")
        self.assertEqual(re.findall(r"\bisMinifyEnabled\s*=\s*(\w+)", release[1]), ["true"])
        self.assertIn('"proguard-rules.pro"', release[1])
        rules = [line.strip() for line in (android / "app/proguard-rules.pro").read_text().splitlines()]
        for rule in ("-dontobfuscate", "-keepattributes SourceFile,LineNumberTable",
                     "-keepattributes InnerClasses,EnclosingMethod",
                     "-keepnames class * extends java.lang.Throwable",
                     "-keepnames class persona.core.hub.HttpChatComposerService",
                     # DocumentText and Xlsx hold the SAX stop signals of the document readers (#17474, #17454).
                     "-keepnames class persona.feature.files.DocumentText",
                     "-keepnames class persona.feature.files.Xlsx"):
            self.assertIn(rule, rules)
        # Optimizing saves 13.7 MB: with -dontoptimize the APK is 33.2 MB again (#2840).
        self.assertNotIn("-dontoptimize", rules)
        builds = [android / "gradle/libs.versions.toml", *android.glob("*/build.gradle.kts"),
                  *android.glob("*/*/build.gradle.kts")]
        self.assertEqual([p.name for p in builds if "material-icons-extended" in p.read_text()], [])


def build_dex(methods, class_name="Lpersona/SizeTest;"):
    """A dex holding one class with `methods`: (name, code units or None for a method without code). Only what
    read_dex reads is written: string, type, proto and method ids, one class_def, its class_data and its code_items."""
    descriptors = [class_name, "V"] + [name for name, _ in methods]
    string_ids_off = 0x70
    type_ids_off = string_ids_off + 4 * len(descriptors)
    proto_off = type_ids_off + 8
    method_ids_off = proto_off + 12
    class_def_off = method_ids_off + 8 * len(methods)
    string_data_off = class_def_off + 32
    string_offsets, string_data = [], b""
    for text in descriptors:
        string_offsets.append(string_data_off + len(string_data))
        string_data += uleb_bytes(len(text)) + text.encode() + b"\0"
    class_data_off = string_data_off + len(string_data)
    head = bytes([0, 0, len(methods), 0])
    base = class_data_off + len(head)  # then as long as uleb(code offset) grows with the code before it
    for _ in range(4):
        code_offsets, codes, entries = [], b"", b""
        for index, (_, size) in enumerate(methods):
            code_offsets.append(0 if size is None else base + len(codes))
            if size is not None:
                codes += struct.pack("<4HII", 1, size + 1, 0, 0, 0, size) + b"\0\0" * size
        entries = b"".join(uleb_bytes(index - previous) + b"\x01" + uleb_bytes(offset)
                           for index, (previous, offset) in
                           enumerate(zip([0] + list(range(len(code_offsets) - 1)), code_offsets)))
        base = class_data_off + len(head) + len(entries)
    class_data = head + entries
    header = bytearray(0x70)
    header[:8] = b"dex\n035\0"
    struct.pack_into("<12I", header, 56, len(descriptors), string_ids_off, 2, type_ids_off, 1, proto_off,
                     0, 0, len(methods), method_ids_off, 1, class_def_off)
    body = b"".join(struct.pack("<I", off) for off in string_offsets)
    body += struct.pack("<2I", 0, 1)              # type ids: the class descriptor, then "V"
    body += struct.pack("<3I", 1, 1, 0)           # proto: shorty "V", returns "V", no parameters
    body += b"".join(struct.pack("<HHI", 0, 0, 2 + index) for index in range(len(methods)))
    body += struct.pack("<8I", 0, 1, shrink.NO_INDEX, 0, shrink.NO_INDEX, 0, class_data_off, 0)
    return bytes(header) + body + string_data + class_data + codes


def uleb_bytes(value):
    out = b""
    while True:
        byte, value = value & 0x7F, value >> 7
        out += bytes([byte | 0x80]) if value else bytes([byte])
        if not value:
            return out


class MethodSizeTests(unittest.TestCase):
    def test_the_sizes_come_from_the_code_items(self):
        classes = shrink.read_dex(build_dex([("big", 12345), ("small", 7), ("abstract", None)]))
        item = classes["persona.SizeTest"]
        self.assertEqual(item.sizes, {("big", "()V"): 12345, ("small", "()V"): 7})
        self.assertEqual(item.methods, {("big", "()V"), ("small", "()V"), ("abstract", "()V")})

    def test_a_method_over_the_limit_that_is_not_listed_stops_the_release(self):
        classes = shrink.read_dex(build_dex([("big", 10001)]))
        failures, warnings = shrink.method_size_problems(classes, {})
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("persona.SizeTest.big()V: 10001 code units", failures[0])
        self.assertEqual(warnings, [])

    def test_a_method_just_under_the_limit_only_warns(self):
        for size in (9000, 10000):
            with self.subTest(size):
                failures, warnings = shrink.method_size_problems(
                    shrink.read_dex(build_dex([("near", size)])), {})
                self.assertEqual(failures, [])
                self.assertEqual(len(warnings), 1, warnings)
                self.assertIn(f"{size} code units, close to the 10000", warnings[0])
        # Below 9,000 nothing is printed at all.
        self.assertEqual(shrink.method_size_problems(shrink.read_dex(build_dex([("fine", 8999)])), {}), ([], []))

    def test_a_listed_method_warns_until_it_grows_past_its_recorded_size(self):
        key = "persona.feature.settings.QuotaText.SettingsContent(Lpersona/feature/settings/SettingsServices;"\
              "Landroidx/compose/runtime/GapComposer;I)V"
        known = {key: 10642}
        listed = shrink.DexClass(sizes={("SettingsContent",
                                         "(Lpersona/feature/settings/SettingsServices;"
                                         "Landroidx/compose/runtime/GapComposer;I)V"): 10642})
        classes = {"persona.feature.settings.QuotaText": listed}
        method = ("SettingsContent", "(Lpersona/feature/settings/SettingsServices;"
                                     "Landroidx/compose/runtime/GapComposer;I)V")
        failures, warnings = shrink.method_size_problems(classes, known)
        self.assertEqual(failures, [])
        self.assertEqual(len(warnings), 1, warnings)
        self.assertIn("listed in known_oversized_methods.txt (may only shrink)", warnings[0])
        # Still over the limit but not past the recorded size: still only the may-only-shrink warning.
        listed.sizes[method] = 10001
        failures, warnings = shrink.method_size_problems(classes, known)
        self.assertEqual(failures, [])
        self.assertEqual(len(warnings), 1, warnings)
        self.assertIn("may only shrink", warnings[0])
        # Under the limit now: the line can go.
        listed.sizes[method] = 9000
        self.assertEqual(shrink.method_size_problems(classes, known), ([], [
            f"{key}: now 9000 code units, at or under the 10000 ART compiles, so it can leave "
            "known_oversized_methods.txt (nothing fails)"]))
        # Growing past the recorded size fails.
        listed.sizes[method] = 10643
        failures, warnings = shrink.method_size_problems(classes, known)
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("10643 code units, grown past the 10642 recorded", failures[0])
        # A method gone from the dex (the split landed) is only noted.
        self.assertEqual(shrink.method_size_problems({}, known), ([], [
            f"{key}: gone from the dex, so it can leave known_oversized_methods.txt (nothing fails)"]))

    def test_an_unlisted_overload_of_a_listed_method_still_fails(self):
        # The name alone is not a signature: the listed no-argument one is small and an unlisted overload passes the
        # limit, which must fail the release rather than hide behind the list.
        listed = ("SettingsContent", "()V")
        other = ("SettingsContent", "(I)V")
        classes = {"persona.feature.settings.QuotaText": shrink.DexClass(sizes={listed: 9000, other: 10500})}
        known = {"persona.feature.settings.QuotaText.SettingsContent()V": 10642}
        failures, warnings = shrink.method_size_problems(classes, known)
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("persona.feature.settings.QuotaText.SettingsContent(I)V: 10500 code units, over the 10000",
                      failures[0])
        # The listed overload is the one at 9,000, so its line can go.
        self.assertIn("SettingsContent()V: now 9000 code units", warnings[0])

    def test_a_library_method_over_the_limit_fails_unless_it_is_listed(self):
        # R8 merges classes across packages (a release moved persona methods into
        # androidx.compose.animation.TransformScopeImpl), so the class holding a method says nothing about who wrote
        # it: every class is judged, and a library method may stay big only by being in the list.
        library = {"androidx.compose.runtime.SlotTable": shrink.DexClass(sizes={("big", "()V"): 20000})}
        failures, warnings = shrink.method_size_problems(library, {})
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("androidx.compose.runtime.SlotTable.big()V: 20000 code units", failures[0])
        self.assertEqual(warnings, [])
        listed = shrink.method_size_problems(library, {"androidx.compose.runtime.SlotTable.big()V": 20001})
        self.assertEqual(listed, ([], ["androidx.compose.runtime.SlotTable.big()V: 20000 code units, over the 10000 "
                                       "ART compiles, listed in known_oversized_methods.txt (may only shrink)"]))

    def test_a_shrunk_release_that_keeps_everything_still_passes(self):
        self.assertEqual(shrink.method_size_problems(kept_release(), {}), ([], []))

    def test_the_shipped_list_holds_the_zxing_table(self):
        known, notice = shrink.read_known(shrink.KNOWN_OVERSIZED)
        self.assertEqual(notice, "")
        self.assertEqual([(key.split("(")[0], size) for key, size in known.items()], [
            ("com.google.zxing.pdf417.PDF417Common.<clinit>", 11178)])
        # The whole signature, so the class initializer keeps its name and "()V" descriptor.
        self.assertEqual(next(iter(known)), "com.google.zxing.pdf417.PDF417Common.<clinit>()V")
        self.assertTrue((shrink.read_known("/nonexistent")[1]))

    def test_a_malformed_list_line_says_which_line(self):
        directory = tempfile.mkdtemp()
        for line, number in (("persona.A.b()V", 2), ("persona.A.c(I 10001", 2), ("persona.A.c( 10001", 3),
                             ("persona.A.c()V 10a01", 2), ("A.c()V 10001", 2), ("persona.A.c()V", 2)):
            with self.subTest(line):
                path = Path(directory) / "list.txt"
                path.write_text("persona.A.b()V 10001\n" + line + "\n" if number == 2
                                else "persona.A.b()V 10001\n\n" + line + "\n")
                with self.assertRaises(ValueError) as raised:
                    shrink.read_known(path)
                self.assertIn(f"list.txt:{number}: expected `Class.method(descriptor) size`", str(raised.exception))

    def test_a_list_line_without_the_descriptor_is_rejected(self):
        # `persona.A.c 10001` names no method the dex could hold: it must fail with the line, not sit in the list.
        path = Path(tempfile.mkdtemp()) / "list.txt"
        path.write_text("persona.A.b()V 10001\npersona.A.c 10001\n")
        with self.assertRaises(ValueError) as raised:
            shrink.read_known(path)
        self.assertIn("list.txt:2: expected `Class.method(descriptor) size`, got 'persona.A.c 10001'",
                      str(raised.exception))

    def test_a_list_line_that_is_not_a_dex_descriptor_is_rejected(self):
        # `([^\s()]*)` used to take any text between the brackets, so a signature no dex holds sat in the list and
        # never matched. Each part is now a run of complete field descriptors and the return is exactly one.
        directory = tempfile.mkdtemp()
        for line, expected in (("persona.A.c(not-a-descriptor)V 10001", "not a dex descriptor"),
                               ("persona.A.c(Lfoo)V 10001", "not a dex descriptor"),
                               ("persona.A.c(X)V 10001", "not a dex descriptor"),
                               ("persona.A.c(V)V 10001", "not a dex descriptor"),
                               ("persona.A.c([)V 10001", "not a dex descriptor"),
                               ("persona.A.c()Lfoo 10001", "not a dex descriptor"),
                               ("persona.A.c(I)Void 10001", "not a dex descriptor"),
                               # A class name stops at the characters that close or nest a descriptor.
                               ("persona.A.c(Lfoo(bar;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(Lfoo)bar;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(Lfoo<Bar>;)V 10001", "not a dex descriptor"),
                               ("persona.A.c()Lfoo(bar; 10001", "not a dex descriptor"),
                               ("persona.A.c(L[foo;)V 10001", "not a dex descriptor"),
                               # Class descriptors use slash-separated, non-empty SimpleNames, not Java source dots
                               # or other punctuation; leading, trailing and doubled slashes are not dex names.
                               ("persona.A.c(Lfoo.bar;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(Lfoo:bar;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(Lfoo//bar;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(L;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(L/foo;)V 10001", "not a dex descriptor"),
                               ("persona.A.c(Lfoo/;)V 10001", "not a dex descriptor"),
                               # The list is whitespace-delimited, so a DEX 040 class-name space is rejected by
                               # KNOWN_LINE before descriptor validation.
                               ("persona.A.c(Lfoo bar;)V 10001", "expected `Class.method(descriptor) size`"),
                               # void is a return type of its own, never an array.
                               ("persona.A.c()[V 10001", "not a dex descriptor")):
            with self.subTest(line):
                path = Path(directory) / "list.txt"
                path.write_text("persona.A.b()V 10001\n" + line + "\n")
                with self.assertRaises(ValueError) as raised:
                    shrink.read_known(path)
                message = str(raised.exception)
                self.assertIn("list.txt:2:", message)
                self.assertIn(expected, message)
        # Every real shape still parses: primitives, objects, both kinds of array, an object return, and the `$` and
        # `/` a nested class or a package puts in a name.
        path = Path(directory) / "list.txt"
        path.write_text("persona.A.a()V 10001\npersona.A.b(Lfoo;[I[[Ljava/lang/String;)J 10001\n"
                        "persona.A.c()Ljava/lang/String; 10001\npersona.A.d(Lfoo$Bar;)V 10001\n"
                        "persona.A.e(I)Lfoo/Bar$Baz; 10001\npersona.A.f([[I)V 10001\n"
                        "persona.A.g(Lfoo/bar$Baz;[Lfoo/bar;Lfoo/bar-baz_1;Lfoo/$lambda$2;)V 10001\n")
        self.assertEqual(shrink.read_known(path)[0], {
            "persona.A.a()V": 10001,
            "persona.A.b(Lfoo;[I[[Ljava/lang/String;)J": 10001,
            "persona.A.c()Ljava/lang/String;": 10001,
            "persona.A.d(Lfoo$Bar;)V": 10001,
            "persona.A.e(I)Lfoo/Bar$Baz;": 10001,
            "persona.A.f([[I)V": 10001,
            "persona.A.g(Lfoo/bar$Baz;[Lfoo/bar;Lfoo/bar-baz_1;Lfoo/$lambda$2;)V": 10001,
        })

    def test_a_list_line_at_or_under_the_limit_is_rejected(self):
        # Such a line records a method that was never oversized, and being "known" hides its near-limit warning, so it
        # would stay forever; the list may only shrink.
        directory = tempfile.mkdtemp()
        for size in (9000, 10000):
            with self.subTest(size):
                path = Path(directory) / "list.txt"
                path.write_text("persona.A.b()V 10001\npersona.A.c()V " + str(size) + "\n")
                with self.assertRaises(ValueError) as raised:
                    shrink.read_known(path)
                message = str(raised.exception)
                self.assertIn(f"list.txt:2: {size} is not over the 10000 ART compiles", message)
                self.assertIn("remove it", message)
        path = Path(directory) / "list.txt"
        path.write_text("persona.A.c()V 10001\n")
        self.assertEqual(shrink.read_known(path)[0], {"persona.A.c()V": 10001})


if __name__ == "__main__":
    unittest.main()
