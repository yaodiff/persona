# Persona for HarmonyOS

Native ArkTS / ArkUI client for HarmonyOS 7 phones and foldables (API 26). See the [product, interaction and architecture guide](../docs/harmony/harmony.md).

Open this directory in DevEco Studio with the HarmonyOS 7 SDK installed, resolve the pinned OHPM dependencies, and select a phone, foldable or wide-foldable emulator. Build the pinned ARM64 SSH core first using [the native build instructions](native/ssh/README.md). With that library and DevEco CLI available:

```sh
devecocli build
devecocli device list
devecocli run --skip-build --device '<device serial>'
```

The checked-in bundle identity is a development example. Production signing and the registered application identity come from the maintainer's private configuration; no credentials or signing material belong in this directory. Local toolchain configuration, signing files, OHPM installations and build outputs are ignored.

Portable model and protocol tests require Node.js 24 or newer:

```sh
node --test tests/*.test.mjs
node tests/run-domain.mjs
```

Native SDK tests use the `entry@ohosTest` target and `OpenHarmonyTestRunner`; they exercise actual platform storage, credentials, URL handling, TLS/WebSocket transport and image preview. The transport fixture is separate from a real Hub; see [verification](../docs/harmony/harmony.md#verification). The [native extension fixture](tests/native-extension-fixture/README.md) reproduces legacy and streamed component updates, focus retention and the Hub confirmation gate on a disposable Hub.

The shared Rust core uses one ohos-rs NAPI library, not a C++ shim. With the API 26 native SDK and Rust 1.98.0 installed:

```sh
OHOS_SDK_NATIVE=/path/to/api26/native ../native/app-core/scripts/build-ohos.sh
devecocli build --modules entry@ohosTest
hdc -t '<device serial>' install entry/build/default/outputs/ohosTest/entry-ohosTest-unsigned.hap
hdc -t '<device serial>' shell aa test -b '<development bundle>' -m entry_test -s unittest OpenHarmonyTestRunner -s coreReplay 1 -w 60
```

The builder packages the original `spec/fixtures/continuity` corpus and its exact Cargo license closure into ignored
`build/` resources. `coreReplay` replays all 38 cases / 94 steps through the real NAPI handle, callbacks and generated
ArkTS codec, and separately asserts one `Update` for a repeated full item. Host tests use a fake NAPI only for adapter
behavior; they do not prove a HAP or device run. Native core notices are packaged with the core test resources.

One host replay does check the generated ArkTS codec against the real core without a device: `scripts/ci/core.sh`
builds `native/app-core`'s `host_replay` example and runs `tests/core-host/replay.test.mjs`, which replays the same
38 cases / 94 steps through `persona-core-ohos`'s registry with ArkTS encoding and decoding, and compares ArkTS and
Rust bytes for independently built values. It found the `u32` width defect (#7485). It exercises neither the NAPI glue
(argument conversion, thread-safe callbacks) nor a HAP, so `coreReplay` above remains the only device test of those.

The application icon reuses Persona's approved artwork. Run `python3 scripts/export-icons.py` from this directory to regenerate its layers, startup image and provenance manifest. The system applies its own launcher mask.
