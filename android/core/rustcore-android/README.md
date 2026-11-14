# Rust core Android packaging (#6871)

`:core:rustcore` stays a pure JVM module with the generated Kotlin codecs and the original real host-JNI fixture replay.
`:core:rustcore-android` is a thin Android library: it packages the same `libpersona_core.so` for `arm64-v8a` and `x86_64`,
plus the locked Cargo dependency notices. The App depends on it in phase 1, but production continuity and all screens are
unchanged. Nothing constructs `RustCore` or loads the native library on the App's launch path.

## Toolchain and cached build

Install rustup, the Rust 1.98.0 targets and NDK **29.0.14206865**. The build uses the NDK's LLVM clang/ar directly;
`cargo-ndk` is not required. It supports an x86_64 Linux build host and the App's `minSdk 30`.

```sh
rustup target add --toolchain 1.98.0 aarch64-linux-android x86_64-linux-android
sdkmanager 'ndk;29.0.14206865'
cd android && ./gradlew :core:rustcore:test :app:assembleDebug :core:rustcore-android:assembleDebugAndroidTest \
  :core:rustcore-android:assembleReleaseAndroidTest
```

`buildAndroidCore` declares Cargo.lock, the pinned toolchain, Rust sources/manifests/headers and target rlibs, the
build script, and the NDK's clang/lld/ar/strip/readelf and sysroot as inputs. Standalone JVM tests do not require the NDK.
A second invocation with unchanged inputs must report it `UP-TO-DATE` (or `FROM-CACHE`), with no Cargo compilation.
Both the symbol-retaining `build/native/symbols/<abi>/libpersona_core.so` and the stripped packaged `.so` under
`build/native/jni/<abi>/` are build outputs; no compiled library is committed. The unstripped symbols and ELF reports
are separate output roots, so neither is packaged as another ABI.

Every `LOAD` segment must report alignment `0x4000`. The App packages both libraries uncompressed and `zipalign -P 16`
checks their 16 KB APK alignment. Kernel page size is a separate device property: ELF/APK alignment does not establish
that a test ran on a 16 KB kernel. Produce a size report (raw, stripped, gzip and actual APK bytes per ABI) with:

```sh
python3 -I native/app-core/scripts/android-package-report.py \
  --native android/core/rustcore-android/build/native/jni \
  --symbols android/core/rustcore-android/build/native/symbols \
  --apk android/app/build/outputs/apk/debug/app-debug.apk \
  --build-tools "$ANDROID_HOME/build-tools/37.0.0" \
  --output android/core/rustcore-android/build/reports/package
```

## Real packaged-JNI replay and benchmark

Use an explicitly selected, owned emulator or an available hardware device; never boot or stop someone else's emulator.
The tests have no UI and require no hub, pairing or network. `assembleDebugAndroidTest` copies the exact host replay
assertions, replacing only repository-file fixture reads with assets from `spec/fixtures/continuity`. This is the real
packaged JNI library, not a host JVM library or a Kotlin continuity implementation. Run it with the bounded runner below
(it installs the APK and requires the real JUnit count; AGP's connected-test success alone is not evidence):

```sh
export ANDROID_ADB_SERVER_PORT=<your isolated adb server port>  # omit for your own default adb server
python3 -I native/app-core/scripts/android-device-test.py --serial <owned serial> --suite replay \
  --apk android/core/rustcore-android/build/outputs/apk/androidTest/debug/rustcore-android-debug-androidTest.apk \
  --output android/core/rustcore-android/build/reports/device-replay
```

The optimized native core is exercised by `RustCoreDeviceBenchmark` in the R8-minified, non-debuggable release test APK.
Enable `release.androidTest` in `beforeVariants` (AGP 9.5 does not create library release tests by default); release's
`androidTest.enableMinification` and `androidTest.proguardFiles` preserve the test entry points and the JNI methods. The
production app's debug and release settings are unchanged. The same debug test APK can replay all fixtures without R8.
It measures 1,000 samples after 100 warm-ups, separately for `dispatch` including bincode encoding
and `drain + decode + apply`; worker synchronization (`flush`) is outside both timed spans. The JSON report names the
model, ABI, API, build ID and kernel page size and records p50/p99/max in microseconds. The benchmark retains the
50 µs / 1 ms p99 assertions. Its report status and raw nanosecond arrays are emitted before those assertions, so a
failed budget leaves evidence rather than hiding it; the runner still exits nonzero and never treats the report as a
pass. An unavailable external report falls back to that same status JSON.

Two additional, explicitly diagnostic suites are available: `attribution` records separate encoding/JNI/queue/lock/
decode/apply spans, real thread IDs, GC and scheduler counters; `control` measures the original public boundary without
per-iteration `/proc`, GC, atomics or JSON work. Neither replaces the original benchmark or its budgets. The former's
spans include instrumentation overhead and are not independent wall-clock samples. Successful diagnostic tests do not
call `sendStatus`: AndroidJUnitRunner owns the final completion status. Their runner reads the nonce-fenced report
file after completion. Only the original failing benchmark publishes a report-only status before its budget failure.

```sh
python3 -I native/app-core/scripts/android-device-test.py --serial <owned serial> --suite attribution \
  --apk android/core/rustcore-android/build/outputs/apk/androidTest/release/rustcore-android-release-androidTest.apk \
  --output android/core/rustcore-android/build/reports/device-attribution
python3 -I native/app-core/scripts/android-device-test.py --serial <owned serial> --suite control \
  --apk android/core/rustcore-android/build/outputs/apk/androidTest/release/rustcore-android-release-androidTest.apk \
  --output android/core/rustcore-android/build/reports/device-control
```

Emulator measurements are diagnostics, not physical-device performance evidence.

```sh
python3 -I native/app-core/scripts/android-device-test.py --serial <owned serial> --suite benchmark \
  --apk android/core/rustcore-android/build/outputs/apk/androidTest/release/rustcore-android-release-androidTest.apk \
  --output android/core/rustcore-android/build/reports/device-benchmark
```

An arm64 hardware run uses the same tasks with the device selected. There is no separate arm64-only test
implementation. The release test APK is non-debuggable and R8-minified, with its mapping under
`build/outputs/mapping/releaseAndroidTest/`; the native library is also an optimized release build. These are not
production-app screen measurements. The phase-1 production adapter and iOS-baseline comparative measurements remain separate work.
