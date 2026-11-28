"""native/app-core/scripts/build-apple.sh: the shared XCFramework cache and the simulator-only build (#10713), and the
Linux path for Linux Apple toolchains (#16944).

Runs the real script in disposable checkouts with fake cargo, rustup, rustc, xcrun, xcodebuild and uname on PATH, so
it needs no Rust toolchain or Xcode and runs on Linux too. The fake uname makes every host an Apple silicon Mac unless
a test says otherwise: the script refuses any other machine name on a Mac, and Linux on arm64 says aarch64 where a Mac
says arm64. The Linux cases make it Linux and leave every Xcode tool off PATH, the host's own copies included.
"""
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'native/app-core/scripts/build-apple.sh'
SIM_TARGET = 'aarch64-apple-ios-sim'
SHASUM = shutil.which('shasum')
HOST = 'aarch64-apple-darwin'
TOOLCHAIN = re.search(r'^channel = "(.*)"$', (ROOT / 'native/app-core/rust-toolchain.toml').read_text(), re.M).group(1)
SDKS = {'iphoneos': 'iPhoneOS', 'iphonesimulator': 'iPhoneSimulator', 'macosx': 'MacOSX'}
MODULEMAP = 'module PersonaCoreC {\n    header "persona_core.h"\n    export *\n}\n'
SIMULATOR_NOTE = 'simulator slice only; for a phone'
LINUX_NOTE = 'persona-core-apple: no macOS slice on Linux; macOS swift test needs a Mac'


def bashes():
    """Every bash on the machine, once each: macOS's 3.2 drops a NUL in a command substitution silently, 4.4 and later
    warn about it on stderr."""
    found = {}
    for path in (shutil.which('bash'), '/bin/bash', '/usr/bin/bash', '/opt/homebrew/bin/bash', '/usr/local/bin/bash'):
        if path and os.access(path, os.X_OK):
            found.setdefault(os.path.realpath(path), path)
    return list(found.values())


FAKES = {
    # FAKE_OS (default Darwin, a Mac) on FAKE_ARCH (default arm64).
    'uname': '''#!/bin/sh
case "$1" in
  -m) echo "${FAKE_ARCH:-arm64}" ;;
  *) echo "${FAKE_OS:-Darwin}" ;;
esac
''',
    # Records each build (its target, and its SDKROOT, RUSTFLAGS and arguments) and leaves a static library named after
    # its target where the script looks for it.
    'cargo': '''#!/bin/sh
printf '%s|%s|%s\\n' "${SDKROOT-unset}" "${RUSTFLAGS-}" "$*" >> "$FAKE_LOG/cargo-calls"
target=
while [ $# -gt 0 ]; do [ "$1" = --target ] && target=$2; shift; done
echo "$target" >> "$FAKE_LOG/cargo"
mkdir -p "$CARGO_TARGET_DIR/$target/release"
echo "lib $target" > "$CARGO_TARGET_DIR/$target/release/libpersona_core.a"
''',
    'rustup': '''#!/bin/sh
echo "$*" >> "$FAKE_LOG/rustup"
''',
    'rustc': '''#!/bin/sh
echo "host: %s"
''' % HOST,
    'xcrun': '''#!/bin/sh
case "$*" in
  *--show-sdk-build-version*) echo "${FAKE_SDK_BUILD:-23A100}" ;;
  *--show-sdk-path*) echo /fake/sdk ;;
esac
''',
    # -create-xcframework: one folder per library, named after the target, and an Info.plist.
    'xcodebuild': '''#!/bin/sh
[ "$1" = -create-xcframework ] || exit 0
shift
output=
libraries=
while [ $# -gt 0 ]; do
  case "$1" in
    -library) libraries="$libraries $2"; shift ;;
    -headers) shift ;;
    -output) output=$2; shift ;;
  esac
  shift
done
[ ! -e "$output" ] || { echo "exists: $output" >&2; exit 1; }
mkdir -p "$output"
for library in $libraries; do
  slice=$(basename "$(dirname "$(dirname "$library")")")
  mkdir -p "$output/$slice"
  cp "$library" "$output/$slice/"
done
echo plist > "$output/Info.plist"
echo "$output" >> "$FAKE_LOG/xcframework"
''',
}


class Harness(unittest.TestCase):
    def setUp(self):
        self.temp = Path(tempfile.mkdtemp(prefix='persona-core-apple-test-'))
        self.addCleanup(shutil.rmtree, self.temp)
        self.cache = self.temp / 'cache'
        self.logs = self.temp / 'logs'
        self.logs.mkdir()
        bin_dir = self.temp / 'bin'
        bin_dir.mkdir()
        for name, body in FAKES.items():
            (bin_dir / name).write_text(body)
            (bin_dir / name).chmod(0o755)
        self.bin = bin_dir
        self.env = {
            'PATH': '%s:%s:/usr/bin:/bin' % (bin_dir, os.path.dirname(SHASUM)),
            'HOME': str(self.temp / 'home'),
            'TMPDIR': str(self.temp),
            'FAKE_LOG': str(self.logs),
            'PERSONA_APP_CORE_CACHE': str(self.cache),
        }

    def checkout(self, name, source='pub fn core() {}\n'):
        """A checkout with the files the script hashes, the script itself among them."""
        root = self.temp / name
        core = root / 'native/app-core'
        (core / 'scripts').mkdir(parents=True)
        shutil.copy2(SCRIPT, core / 'scripts/build-apple.sh')
        shutil.copy2(ROOT / 'native/app-core/rust-toolchain.toml', core / 'rust-toolchain.toml')
        (core / 'Cargo.toml').write_text('[workspace]\n')
        (core / 'Cargo.lock').write_text('# lock\n')
        for crate in ('persona-core', 'persona-core-runtime', 'persona-core-ffi'):
            (core / 'crates' / crate / 'src').mkdir(parents=True)
            (core / 'crates' / crate / 'src/lib.rs').write_text(source)
        (core / 'crates/persona-core-ffi/include').mkdir()
        (core / 'crates/persona-core-ffi/include/persona_core.h').write_text('int persona_core(void);\n')
        (root / 'apple/Packages/PersonaCore').mkdir(parents=True)
        return root

    def run_script(self, root, *args, bash='bash', **env):
        result = subprocess.run([bash, str(root / 'native/app-core/scripts/build-apple.sh'), *args],
                                env={**self.env, **env}, capture_output=True, text=True, timeout=120)
        return result

    def build(self, root, *args, **env):
        result = self.run_script(root, *args, **env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def built(self):
        """The targets cargo built since the last call."""
        log = self.logs / 'cargo'
        targets = log.read_text().split() if log.exists() else []
        log.unlink(missing_ok=True)
        return targets

    def cargo_calls(self):
        """Each cargo run since the last call: its SDKROOT, its RUSTFLAGS and its arguments."""
        log = self.logs / 'cargo-calls'
        lines = log.read_text().splitlines() if log.exists() else []
        log.unlink(missing_ok=True)
        calls = [line.split('|', 2) for line in lines]
        return [(sdkroot, rustflags, args.split()) for sdkroot, rustflags, args in calls]

    def framework(self, root):
        dest = root / 'apple/Packages/PersonaCore/build/PersonaCoreC.xcframework'
        slices = sorted(p.name for p in dest.iterdir() if p.is_dir())
        stamp = (root / 'apple/Packages/PersonaCore/build/Inputs.stamp').read_text().split()
        return slices, stamp

    def entries(self):
        return sorted(p.name for p in self.cache.iterdir()) if self.cache.exists() else []


@unittest.skipUnless(SHASUM, 'needs shasum')
class AppleCoreBuildTests(Harness):
    def test_a_second_worktree_at_the_same_inputs_copies_the_cached_framework(self):
        first, second = self.checkout('first'), self.checkout('second')
        out = self.build(first, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET], 'a simulator request builds only its slice')
        self.assertIn('XCFramework (simulator) ready', out)
        self.assertEqual(self.framework(first), ([SIM_TARGET], [self.entries()[0], 'simulator']))

        out = self.build(first, '--simulator')
        self.assertIn('XCFramework is current', out)
        self.assertEqual(self.built(), [])

        # Another checkout path: the key holds paths relative to the checkout, so it is the same entry.
        out = self.build(second, '--simulator')
        self.assertEqual(self.built(), [], 'the second worktree must reuse the cached framework')
        self.assertIn('from the shared cache', out)
        self.assertEqual(self.framework(second), self.framework(first))
        self.assertEqual(len(self.entries()), 1)

    def test_a_full_request_builds_every_slice_and_serves_later_simulator_requests(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator')
        self.built()
        # The worktree has only the simulator slice: a full request (release, CI, swift test) builds all three.
        self.build(first)
        self.assertEqual(sorted(self.built()), sorted(['aarch64-apple-ios', SIM_TARGET, HOST]))
        slices, stamp = self.framework(first)
        self.assertEqual(slices, sorted(['aarch64-apple-ios', SIM_TARGET, HOST]))
        self.assertEqual(stamp[1], 'all')
        # A full worktree copy satisfies a simulator request as it is.
        self.assertIn('XCFramework is current', self.build(first, '--simulator'))
        # Another worktree asking for everything gets the full entry without building.
        self.build(second)
        self.assertEqual(self.built(), [])
        self.assertEqual(self.framework(second), self.framework(first))
        key = self.entries()[0]
        self.assertEqual(sorted(p.name for p in (self.cache / key).iterdir()), ['all', 'simulator'])

    def test_a_simulator_request_takes_a_full_entry_when_it_is_the_only_one(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first)
        self.built()
        out = self.build(second, '--simulator')
        self.assertEqual(self.built(), [])
        self.assertIn('XCFramework (all) from the shared cache', out)
        self.assertNotIn('simulator slice only', out)
        self.assertEqual(self.framework(second)[1][1], 'all')

    def test_a_simulator_only_framework_says_how_to_build_for_a_phone(self):
        # Xcode stops with "no library for this platform" on a phone or My Mac (Designed for iPad) when the worktree
        # has the simulator slice only, so every way of leaving one says how to add the rest; a full one says nothing.
        first, second = self.checkout('first'), self.checkout('second')
        note = 'simulator slice only; for a phone'
        self.assertIn(note, self.build(first, '--simulator'))        # built
        self.assertIn(note, self.build(first, '--simulator'))        # current
        self.assertIn(note, self.build(second, '--simulator'))       # from the cache
        out = self.build(first)
        self.assertIn('XCFramework (all) ready', out)
        self.assertNotIn(note, out)
        self.assertNotIn(note, self.build(first, '--simulator'))     # a full copy serves it as it is

    def test_changed_inputs_build_again_and_only_the_newest_keys_stay(self):
        first = self.checkout('first')
        # Something else in the cache folder is neither counted nor removed.
        (self.cache / 'notes').mkdir(parents=True)
        self.build(first, '--simulator', PERSONA_APP_CORE_CACHE_KEEP='2')
        old = [name for name in self.entries() if name != 'notes']
        for n in range(2):
            (first / 'native/app-core/crates/persona-core/src/lib.rs').write_text('pub fn core() { %d }\n' % n)
            self.build(first, '--simulator', PERSONA_APP_CORE_CACHE_KEEP='2')
            self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET] if n == 0 else [SIM_TARGET])
        self.assertEqual(len(self.entries()), 3)
        self.assertIn('notes', self.entries())
        self.assertNotIn(old[0], self.entries(), 'the oldest key is pruned')
        self.assertIn(self.framework(first)[1][0], self.entries())

    def test_the_environment_that_changes_the_output_is_part_of_the_key(self):
        first = self.checkout('first')
        self.build(first, '--simulator')
        self.build(first, '--simulator', FAKE_SDK_BUILD='24A200')
        self.build(first, '--simulator', RUSTFLAGS='-C opt-level=1')
        self.assertEqual(self.built(), [SIM_TARGET] * 3)
        self.assertEqual(len(self.entries()), 3)

    def test_the_simulator_slice_follows_the_macs_architecture(self):
        first = self.checkout('first')
        self.build(first, '--simulator', FAKE_ARCH='x86_64')
        self.assertEqual(self.built(), ['x86_64-apple-ios'])
        self.build(first, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertEqual(len(self.entries()), 2, 'the architecture is part of the key')
        result = self.run_script(first, '--simulator', FAKE_ARCH='aarch64')
        self.assertEqual(result.returncode, 1)
        self.assertIn('unsupported Mac architecture aarch64', result.stderr)

    def test_paths_are_part_of_the_key(self):
        first, second = self.checkout('first'), self.checkout('second')
        (first / 'native/app-core/crates/persona-core/src/a.rs').write_text('// same\n')
        (second / 'native/app-core/crates/persona-core/src/b.rs').write_text('// same\n')
        self.build(first, '--simulator')
        self.build(second, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET])
        self.assertEqual(len(self.entries()), 2)

    def test_every_bash_builds_without_a_word_on_stderr_and_to_the_same_key(self):
        # The key's stream separates each path from its content with a NUL; captured in a command substitution, bash
        # 4.4+ warned "ignored null byte" on every run and dropped it.
        keys = set()
        for n, bash in enumerate(bashes()):
            with self.subTest(bash=bash):
                root = self.checkout('bash%d' % n)
                for _ in range(2):  # built (or from the cache), then current
                    result = self.run_script(root, '--simulator', bash=bash)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(result.stderr, '')
                self.assertIn('XCFramework is current', result.stdout)
                keys.add(self.framework(root)[1][0])
        self.assertEqual(len(keys), 1, keys)

    def test_a_cache_that_cannot_be_created_costs_only_the_cache(self):
        # A file where the cache's folder would go stops even root.
        first = self.checkout('first')
        blocker = self.temp / 'a-file'
        blocker.write_text('')
        result = self.run_script(first, '--simulator', PERSONA_APP_CORE_CACHE=str(blocker / 'cache'))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertIn('XCFramework (simulator) ready', result.stdout)
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
        self.assertIn('could not write the shared cache', result.stderr)
        self.assertEqual(self.framework(first)[0], [SIM_TARGET])
        self.assertEqual(sorted(p.name for p in (first / 'apple/Packages/PersonaCore/build').iterdir()),
                         ['Inputs.stamp', 'PersonaCoreC.xcframework'])

    @unittest.skipIf(hasattr(os, 'geteuid') and os.geteuid() == 0, 'root reads and writes past the permissions')
    def test_a_read_only_cache_is_read_and_never_stops_a_build(self):
        first, second, third = self.checkout('first'), self.checkout('second'), self.checkout('third')
        self.build(first, '--simulator')
        self.built()
        entry = self.cache / self.entries()[0]
        locked = [self.cache, entry, entry / 'simulator']
        for path in locked:
            path.chmod(0o555)
        self.addCleanup(lambda: [path.chmod(0o755) for path in locked])
        # Read: copied as before, with nothing to say about the touch that failed.
        result = self.run_script(second, '--simulator')
        self.assertEqual((result.returncode, result.stderr), (0, ''))
        self.assertIn('from the shared cache', result.stdout)
        self.assertEqual(self.built(), [])
        # Write: every slice is built and kept in the worktree, with one line about the cache.
        result = self.run_script(third)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
        self.assertIn('could not write the shared cache', result.stderr)
        self.assertEqual(self.framework(third)[1][1], 'all')
        self.assertEqual(self.entries(), [entry.name])

    @unittest.skipIf(hasattr(os, 'geteuid') and os.geteuid() == 0, 'root reads past the permissions')
    def test_an_entry_that_cannot_be_read_is_built_again(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator')
        self.built()
        library = self.cache / self.entries()[0] / 'simulator/PersonaCoreC.xcframework' / SIM_TARGET / 'libpersona_core.a'
        library.chmod(0)
        self.addCleanup(library.chmod, 0o644)
        result = self.run_script(second, '--simulator')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
        self.assertIn('could not copy', result.stderr)
        self.assertEqual(self.framework(second)[0], [SIM_TARGET])
        self.assertEqual(sorted(p.name for p in (second / 'apple/Packages/PersonaCore/build').iterdir()),
                         ['Inputs.stamp', 'PersonaCoreC.xcframework'])

    def test_the_cache_can_be_turned_off(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator', PERSONA_APP_CORE_CACHE='0')
        self.build(second, '--simulator', PERSONA_APP_CORE_CACHE='0')
        self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET])
        self.assertFalse(self.cache.exists())
        self.assertFalse((self.temp / 'home').exists(), 'nothing is written to the default cache either')

    def test_a_home_or_checkout_behind_a_link_is_remapped_as_it_really_is_too(self):
        # Real paths, as on a Mac: the same two remaps as ever.
        home = Path(os.path.realpath(self.temp)) / 'home'
        home.mkdir()
        root = Path(os.path.realpath(self.checkout('real')))
        self.build(root, '--simulator', PERSONA_APP_CORE_CACHE='0', HOME=str(home))
        self.assertEqual([rustflags for _, rustflags, _ in self.cargo_calls()],
                         ['--remap-path-prefix=%s=/build/home --remap-path-prefix=%s=/build/persona' % (home, root)])
        # Through links (a home on another disk, often): rustup and cargo hand rustc the real paths, so those are
        # remapped too, the checkout's last so that they win for its files; the caller's flags stay first.
        (self.temp / 'home-link').symlink_to(home)
        (self.temp / 'checkout-link').symlink_to(root)
        self.build(self.temp / 'checkout-link', '--simulator', PERSONA_APP_CORE_CACHE='0',
                   HOME=str(self.temp / 'home-link'), RUSTFLAGS='-C debug-assertions')
        self.assertEqual([rustflags for _, rustflags, _ in self.cargo_calls()], [' '.join([
            '-C debug-assertions',
            '--remap-path-prefix=%s=/build/home' % (self.temp / 'home-link'),
            '--remap-path-prefix=%s=/build/home' % home,
            '--remap-path-prefix=%s=/build/persona' % (self.temp / 'checkout-link'),
            '--remap-path-prefix=%s=/build/persona' % root])])

    def test_unknown_arguments_and_a_bad_keep_count_are_refused(self):
        first = self.checkout('first')
        self.assertEqual(self.run_script(first, '--device').returncode, 2)
        result = self.run_script(first, PERSONA_APP_CORE_CACHE_KEEP='0')
        self.assertEqual(result.returncode, 1)
        self.assertIn('PERSONA_APP_CORE_CACHE_KEEP', result.stderr)
        self.assertEqual(self.built(), [])

    def test_no_scratch_is_left_behind(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator')
        self.build(second)
        for root in (first, second):
            self.assertEqual(sorted(p.name for p in (root / 'apple/Packages/PersonaCore/build').iterdir()),
                             ['Inputs.stamp', 'PersonaCoreC.xcframework'])
        for entry in self.cache.iterdir():
            self.assertEqual(sorted(p.name for p in entry.iterdir()), ['all', 'simulator'])
        self.assertEqual([p.name for p in self.temp.iterdir() if p.name.startswith('persona-core-apple-headers')], [])


@unittest.skipUnless(SHASUM, 'needs shasum')
class LinuxAppleCoreBuildTests(Harness):
    """The Linux path (#16944): a Linux Apple toolchain, with no Xcode tool on PATH."""

    def setUp(self):
        super().setUp()
        self.mac_path = self.env['PATH']
        self.linux_bin = self.temp / 'linux-bin'
        self.linux_bin.mkdir()
        for name in FAKES:
            if name not in ('xcrun', 'xcodebuild'):
                (self.linux_bin / name).symlink_to(self.bin / name)
        self.env.update(PATH=self.linux_path(), FAKE_OS='Linux', FAKE_ARCH='x86_64')
        # The toolchain's copy of the Xcode SDKs, as apple-build's sdk-import leaves it and its config names it.
        self.sdk = self.temp / 'sdk'
        self.set_sdks(self.sdk, iphoneos='24B8118', iphonesimulator='24B8118', macosx='26B118')
        self.configure({'sdk_root': str(self.sdk)})

    def linux_path(self, *leave_out):
        """The fakes, then the host's own tools less Xcode's (a Mac's /usr/bin has xcrun and xcodebuild) and the named
        ones."""
        tools = Path(tempfile.mkdtemp(prefix='tools-', dir=self.temp))
        for directory in ('/usr/bin', '/bin', '/usr/sbin', '/sbin'):
            names = os.listdir(directory) if os.path.isdir(directory) else []
            for name in names:
                if name not in ('xcrun', 'xcodebuild') + leave_out and not os.path.lexists(tools / name):
                    (tools / name).symlink_to(os.path.join(directory, name))
        return '%s:%s' % (self.linux_bin, tools)

    def set_sdks(self, root, **builds):
        """Writes each named SDK's SystemVersion.plist with its ProductBuildVersion."""
        for sdk, build in builds.items():
            name = SDKS[sdk]
            folder = root / ('Developer/Platforms/%s.platform/Developer/SDKs/%s.sdk' % (name, name))
            (folder / 'System/Library/CoreServices').mkdir(parents=True, exist_ok=True)
            (folder / 'System/Library/CoreServices/SystemVersion.plist').write_bytes(
                plistlib.dumps({'ProductBuildVersion': build, 'ProductName': name}))

    def configure(self, config):
        path = self.temp / 'home/.config/apple-build/config.json'
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(config if isinstance(config, str) else json.dumps(config))

    def build(self, root, *args, **env):
        # Nothing on the Linux path has anything to say on stderr.
        result = self.run_script(root, *args, **env)
        self.assertEqual((result.returncode, result.stderr), (0, ''), result.stdout + result.stderr)
        return result.stdout

    def key(self, root):
        return self.framework(root)[1][0]

    def test_linux_builds_the_device_and_arm64_simulator_static_libraries_on_any_machine(self):
        for arch in ('x86_64', 'aarch64'):
            with self.subTest(arch=arch):
                root = self.checkout('linux-' + arch)
                out = self.build(root, FAKE_ARCH=arch)
                manifest = str(root / 'native/app-core/Cargo.toml')
                # The crate's crate types less the cdylib: the rlib keeps cargo from LTO-merging the static library.
                self.assertEqual([(sdkroot, args) for sdkroot, _, args in self.cargo_calls()], [
                    ('unset', ['+' + TOOLCHAIN, 'rustc', '--manifest-path', manifest, '--locked', '--release', '-p',
                               'persona-core-ffi', '--lib', '--target', target, '--crate-type', 'staticlib,rlib'])
                    for target in ('aarch64-apple-ios', SIM_TARGET)], 'static libraries, and no SDKROOT')
                self.assertEqual(self.built(), ['aarch64-apple-ios', SIM_TARGET])
                rustup = (self.logs / 'rustup').read_text().splitlines()
                self.assertEqual(rustup[-1], 'target add --toolchain %s aarch64-apple-ios %s' % (TOOLCHAIN, SIM_TARGET))
                self.assertIn('XCFramework (all) ready', out)
                self.assertEqual(self.framework(root)[0], ['ios-arm64', 'ios-arm64-simulator'])
                self.assertEqual(self.framework(root)[1][1], 'all')
                self.assertEqual(out.splitlines()[-1], LINUX_NOTE)
                self.build(root, '--simulator', FAKE_ARCH=arch)
                self.assertEqual(self.built(), [], 'the full framework serves a simulator request')
                self.build(self.checkout('simulator-' + arch), '--simulator', PERSONA_APP_CORE_CACHE='0',
                           FAKE_ARCH=arch)
                self.assertEqual(self.built(), [SIM_TARGET], 'the arm64 simulator whatever the machine')
                self.cargo_calls()
        self.assertEqual(len(self.entries()), 2, 'the machine is still part of the key')

    def test_linux_writes_the_xcframework_xcodebuild_would(self):
        def library(identifier, variant=None):
            # What xcodebuild -create-xcframework writes for each of these static libraries, BinaryPath included.
            entry = {'BinaryPath': 'libpersona_core.a', 'HeadersPath': 'Headers', 'LibraryIdentifier': identifier,
                     'LibraryPath': 'libpersona_core.a', 'SupportedArchitectures': ['arm64'],
                     'SupportedPlatform': 'ios'}
            return {**entry, 'SupportedPlatformVariant': variant} if variant else entry

        def framework(*libraries):
            return {'AvailableLibraries': list(libraries), 'CFBundlePackageType': 'XFWK',
                    'XCFrameworkFormatVersion': '1.0'}

        first, second = self.checkout('first'), self.checkout('second')
        self.build(first)
        self.build(second, '--simulator', PERSONA_APP_CORE_CACHE='0')
        for root, slices in ((first, ('ios-arm64', 'ios-arm64-simulator')), (second, ('ios-arm64-simulator',))):
            dest = root / 'apple/Packages/PersonaCore/build/PersonaCoreC.xcframework'
            raw = (dest / 'Info.plist').read_bytes()
            self.assertTrue(raw.startswith(b'<?xml version="1.0" encoding="UTF-8"?>'), 'an XML property list, as Xcode')
            plist = plistlib.loads(raw)
            self.assertEqual(plist, framework(*(library(s, 'simulator' if s.endswith('-simulator') else None)
                                                for s in slices)))
            for entry in plist['AvailableLibraries']:
                self.assertEqual(list(entry), sorted(entry), 'keys in the order Xcode writes them')
            self.assertEqual(sorted(str(p.relative_to(dest)) for p in dest.rglob('*')), sorted(
                ['Info.plist'] + [s + suffix for s in slices for suffix in (
                    '', '/libpersona_core.a', '/Headers', '/Headers/persona_core.h', '/Headers/module.modulemap')]))
            for identifier in slices:
                target = SIM_TARGET if identifier.endswith('-simulator') else 'aarch64-apple-ios'
                self.assertEqual((dest / identifier / 'libpersona_core.a').read_text(), 'lib %s\n' % target)
                self.assertEqual((dest / identifier / 'Headers/persona_core.h').read_text(),
                                 'int persona_core(void);\n')
                self.assertEqual((dest / identifier / 'Headers/module.modulemap').read_text(), MODULEMAP)

    def test_linux_keys_the_cache_by_the_toolchains_sdk_builds(self):
        root = self.checkout('first')
        self.env['PERSONA_APP_CORE_CACHE_KEEP'] = '10'
        self.build(root, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertIn('XCFramework is current', self.build(root, '--simulator'))
        keys = [self.key(root)]
        for sdk in SDKS:
            with self.subTest(sdk=sdk):
                self.set_sdks(self.sdk, **{sdk: '99Z999'})
                self.build(root, '--simulator')
                self.assertEqual(self.built(), [SIM_TARGET], 'a new %s SDK builds again' % sdk)
                self.assertNotIn(self.key(root), keys)
                keys.append(self.key(root))
        # APPLE_BUILD_SDK_ROOT comes before the config, as for apple-build; the builds count, not where the SDK is.
        other = self.temp / 'other-sdk'
        self.set_sdks(other, iphoneos='24B8118', iphonesimulator='24B8118', macosx='26B118')
        self.assertIn('from the shared cache', self.build(root, '--simulator', APPLE_BUILD_SDK_ROOT=str(other)))
        self.assertEqual((self.built(), self.key(root)), ([], keys[0]))
        # Nothing to read gives `none` for each SDK, never a failure: no config, a folder with no SDKs, a broken config.
        (self.temp / 'home/.config/apple-build/config.json').unlink()
        self.build(root, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertNotIn(self.key(root), keys)
        for config in ({'sdk_root': str(self.temp / 'nowhere')}, '{"sdk_root": ', {'sdk_root': None}, []):
            with self.subTest(config=config):
                self.configure(config)
                self.assertIn('XCFramework is current', self.build(root, '--simulator'))
        # XDG_CONFIG_HOME moves apple-build's config, as it does for apple-build.
        xdg = self.temp / 'xdg'
        (xdg / 'apple-build').mkdir(parents=True)
        (xdg / 'apple-build/config.json').write_text(json.dumps({'sdk_root': str(other)}))
        self.build(root, '--simulator', XDG_CONFIG_HOME=str(xdg))
        self.assertEqual((self.built(), self.key(root)), ([], keys[0]))

    def test_linux_cache_stamp_and_simulator_requests_work_as_on_a_mac(self):
        first, second = self.checkout('first'), self.checkout('second')
        out = self.build(first, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertIn('XCFramework (simulator) ready', out)
        self.assertIn(SIMULATOR_NOTE, out)
        self.assertNotIn(LINUX_NOTE, out)
        self.assertIn('XCFramework is current (simulator)', self.build(first, '--simulator'))
        out = self.build(second, '--simulator')
        self.assertEqual(self.built(), [])
        self.assertIn('XCFramework (simulator) from the shared cache', out)
        # A full request adds the device slice; its note says once that no macOS slice comes with it.
        out = self.build(first)
        self.assertEqual(self.built(), ['aarch64-apple-ios', SIM_TARGET])
        self.assertIn('XCFramework (all) ready', out)
        self.assertNotIn(SIMULATOR_NOTE, out)
        self.assertEqual([line for line in out.splitlines() if 'macOS' in line], [LINUX_NOTE])
        self.assertEqual(self.framework(first)[1][1], 'all')
        for args, said in (((), 'XCFramework is current (all)'), (('--simulator',), 'XCFramework is current (all)')):
            out = self.build(first, *args)
            self.assertIn(said, out)
            self.assertEqual(out.splitlines()[-1], LINUX_NOTE)
        out = self.build(second)
        self.assertEqual(self.built(), [])
        self.assertIn('XCFramework (all) from the shared cache', out)
        self.assertEqual(out.splitlines()[-1], LINUX_NOTE)
        self.assertEqual(self.framework(second), self.framework(first))
        self.assertEqual(sorted(p.name for p in (self.cache / self.entries()[0]).iterdir()), ['all', 'simulator'])
        for root in (first, second):
            self.assertEqual(sorted(p.name for p in (root / 'apple/Packages/PersonaCore/build').iterdir()),
                             ['Inputs.stamp', 'PersonaCoreC.xcframework'])
        self.assertEqual([p.name for p in self.temp.iterdir() if p.name.startswith('persona-core-apple-headers')], [])

    def test_linux_keeps_the_default_cache_under_the_xdg_cache_folder(self):
        first, second = self.checkout('first'), self.checkout('second')
        del self.env['PERSONA_APP_CORE_CACHE']
        self.build(first, '--simulator')
        self.assertEqual(len(list((self.temp / 'home/.cache/persona/app-core').iterdir())), 1)
        xdg = self.temp / 'xdg-cache'
        self.build(second, '--simulator', XDG_CACHE_HOME=str(xdg))
        self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET])
        self.assertEqual(len(list((xdg / 'persona/app-core').iterdir())), 1)
        self.assertFalse((self.temp / 'home/Library').exists())

    def test_a_linux_framework_never_serves_a_mac(self):
        # The same machine name and SDK builds on both: only the system tells them apart, and a Mac needs its macOS
        # slice.
        self.set_sdks(self.sdk, iphoneos='23A100', iphonesimulator='23A100', macosx='23A100')
        self.build(self.checkout('linux'))
        self.built()
        out = self.build(self.checkout('mac'), PATH=self.mac_path, FAKE_OS='Darwin')
        self.assertEqual(self.built(), ['aarch64-apple-ios', 'x86_64-apple-ios', HOST])
        self.assertNotIn(LINUX_NOTE, out)
        self.assertEqual(len(self.entries()), 2)

    def test_other_systems_and_missing_tools_are_refused_in_one_line(self):
        root = self.checkout('first')
        for env, said in (({'FAKE_OS': 'FreeBSD'}, 'FAIL unsupported system FreeBSD: build on macOS with Xcode, or on '
                                                   'Linux with a Linux Apple toolchain'),
                          ({'PATH': self.linux_path('python3')}, 'FAIL python3 is required'),
                          ({'PATH': self.linux_path('shasum', 'sha256sum')}, 'FAIL shasum or sha256sum is required')):
            with self.subTest(env=env):
                result = self.run_script(root, **env)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertEqual(result.stderr.splitlines(), ['persona-core-apple: ' + said])
        self.assertEqual(self.built(), [])

    def test_without_shasum_sha256sum_makes_the_same_key(self):
        path = self.linux_path('shasum')
        if not os.path.exists(path.split(':')[1] + '/sha256sum'):
            self.skipTest('needs sha256sum')
        root = self.checkout('first')
        self.build(root, '--simulator')
        self.assertIn('XCFramework is current', self.build(root, '--simulator', PATH=path))
        self.assertEqual(self.built(), [SIM_TARGET])


if __name__ == '__main__':
    unittest.main()
