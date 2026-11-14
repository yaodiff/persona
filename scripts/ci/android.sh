#!/usr/bin/env bash
# Portable Android verification; no persona_config.py, signing keys, hub or emulator.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
usage() {
  printf '%s\n' \
    'Usage: scripts/ci/android.sh [--lane full|lint-build-test-0|test-1] [--profile]' \
    '       scripts/ci/android.sh --help' \
    'Requires Bash, Python 3, timeout (or gtimeout), JAVA_HOME pointing to JDK 21,' \
    'and ANDROID_HOME (or ANDROID_SDK_ROOT) pointing to an installed Android SDK.' \
    'Also requires rustup and NDK 29.0.14206865: core:rustcore tests build the host JNI library;' \
    'core:rustcore-android packages arm64-v8a and x86_64 for the debug APK.' \
    'SDK setup: sdkmanager "platforms;android-37.0" "build-tools;37.0.0" "platform-tools" "ndk;29.0.14206865"' \
    'Install the Android Rust targets: rustup target add --toolchain 1.98.0 aarch64-linux-android x86_64-linux-android' \
    'Accept SDK licenses using sdkmanager --licenses if required by your SDK tools.' \
    'Toolchain versions are read from android/gradle/libs.versions.toml.' \
    'No arguments runs the original complete verification. lint-build-test-0 owns checker tests,' \
    'translation/token checks, all-module lint, the debug APK and feature test shard 0; test-1' \
    'runs core, feature shard 1, platform and app tests. Together the two lanes run every' \
    'included module once using its all-variant test lifecycle task.' \
    '--profile writes Gradle timing reports to android/build/reports/profile/.' \
    'No instrumentation/device tests or release signing.' \
    'ANDROID_CI_TIMEOUT_SECONDS: timeout per check (default 3600; kill grace 30s).' \
    'ANDROID_CI_MAX_WORKERS: Gradle worker limit (default 2).' \
    'Cache GRADLE_USER_HOME/caches and GRADLE_USER_HOME/wrapper (default ~/.gradle),' \
    'plus ~/.m2/repository for Robolectric runtime downloads; keep the SDK installed.' \
    'Reports: android/**/build/reports/{lint-results*,tests/**}; test XML under' \
    'android/**/build/test-results/**; APK: android/app/build/outputs/apk/debug/app-debug.apk.'
}
LANE=full
PROFILE=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --help|-h) usage; exit 0 ;;
    --lane)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      LANE=$2
      shift 2
      ;;
    --profile) PROFILE=(--profile); shift ;;
    *) usage >&2; exit 2 ;;
  esac
done
case $LANE in
  full|lint-build-test-0|test-1) ;;
  *) usage >&2; exit 2 ;;
esac

fail() { printf 'Android CI: FAILED (%s)\n' "$*" >&2; exit 1; }
command -v python3 >/dev/null || fail 'Python 3 is required'
if command -v timeout >/dev/null; then
  TIMEOUT=timeout
elif command -v gtimeout >/dev/null; then
  TIMEOUT=gtimeout
else
  fail 'Install GNU timeout (coreutils on Linux; gtimeout on macOS)'
fi
TIME_LIMIT=${ANDROID_CI_TIMEOUT_SECONDS:-3600}
WORKERS=${ANDROID_CI_MAX_WORKERS:-2}
[[ $TIME_LIMIT =~ ^[1-9][0-9]*$ ]] || fail 'ANDROID_CI_TIMEOUT_SECONDS must be a positive integer'
[[ $WORKERS =~ ^[1-9][0-9]*$ ]] || fail 'ANDROID_CI_MAX_WORKERS must be a positive integer'

# Avoid a Python TOML dependency so the entry point works with Python 3.9 too.
VERSIONS=$(python3 -B - "$ROOT/android/gradle/libs.versions.toml" <<'PY'
import re
import sys
from pathlib import Path

source = Path(sys.argv[1]).read_text().split('[versions]', 1)[1].split('\n[', 1)[0]
values = []
for key in ('jdk', 'compileSdk', 'buildTools'):
    match = re.search(r'^' + key + r'\s*=\s*"([0-9.]+)"\s*$', source, re.M)
    if not match:
        sys.exit('Android CI: invalid or missing toolchain version: ' + key)
    values.append(match.group(1))
print('\t'.join(values))
PY
) || fail 'Cannot read Android toolchain versions'
IFS=$'\t' read -r JDK COMPILE_SDK BUILD_TOOLS <<< "$VERSIONS"
[[ -n ${JAVA_HOME:-} && -x $JAVA_HOME/bin/java && -x $JAVA_HOME/bin/javac ]] || fail "Set JAVA_HOME to JDK $JDK"
JAVA_HOME=$(cd "$JAVA_HOME" && pwd)
export JAVA_HOME
JAVA_VERSION=$("$JAVA_HOME/bin/java" -version 2>&1) || fail 'Cannot run Java'
[[ $JAVA_VERSION =~ version[[:space:]]+\"([0-9]+) ]] || fail 'Cannot determine Java version'
[[ ${BASH_REMATCH[1]} == "$JDK" ]] || fail "JAVA_HOME must select JDK $JDK (including the build-logic toolchain)"

SDK=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
[[ -n $SDK && -d $SDK ]] || fail 'Set ANDROID_HOME or ANDROID_SDK_ROOT to an installed Android SDK'
SDK=$(cd "$SDK" && pwd)
if [[ -n ${ANDROID_HOME:-} && -n ${ANDROID_SDK_ROOT:-} ]]; then
  [[ -d $ANDROID_SDK_ROOT && $(cd "$ANDROID_SDK_ROOT" && pwd) == "$SDK" ]] || fail 'ANDROID_HOME and ANDROID_SDK_ROOT must agree'
fi
export ANDROID_HOME="$SDK" ANDROID_SDK_ROOT="$SDK"
# API 37 is distributed as android-37.0, unlike older platforms' android-<API>.
[[ -f $SDK/platforms/android-$COMPILE_SDK.0/android.jar || -f $SDK/platforms/android-$COMPILE_SDK/android.jar ]] || fail "Install SDK platform android-$COMPILE_SDK.0"
[[ -d $SDK/build-tools/$BUILD_TOOLS ]] || fail "Install SDK build-tools $BUILD_TOOLS"
[[ -d $SDK/ndk/29.0.14206865 ]] || fail 'Install NDK 29.0.14206865 for the Android shared core'
rustup target list --installed --toolchain 1.98.0 | grep -Fx aarch64-linux-android >/dev/null ||
  rustup target add --toolchain 1.98.0 aarch64-linux-android || fail 'Cannot install the arm64 Android Rust target'
rustup target list --installed --toolchain 1.98.0 | grep -Fx x86_64-linux-android >/dev/null ||
  rustup target add --toolchain 1.98.0 x86_64-linux-android || fail 'Cannot install the x86_64 Android Rust target'
[[ -f $ROOT/android/gradlew && -f $ROOT/android/gradle/wrapper/gradle-wrapper.jar ]] || fail 'The Gradle wrapper is missing'
# :core:rustcore's tests load a host build of the shared Rust core (native/app-core).
source "$ROOT/scripts/ci/common.sh"
ci_rust_toolchain || fail 'Install rustup (https://rustup.rs) for the shared Rust core'
cd "$ROOT"

# Capture the generator's status before reading its output; process substitution
# would hide parse failures and could accidentally invoke Gradle's default tasks.
TASK_LINES=$(python3 -B "$ROOT/scripts/ci/android-shards.py" --lane "$LANE") || fail 'Cannot enumerate Android CI tasks'
TASKS=()
while IFS= read -r task; do
  [[ -z $task ]] || TASKS+=("$task")
done <<< "$TASK_LINES"

FAILED=()
run_check() {
  local name=$1 status
  shift
  printf '\nAndroid CI: running %s\n' "$name"
  if "$TIMEOUT" --kill-after=30s "${TIME_LIMIT}s" "$@"; then
    printf 'Android CI: PASSED (%s)\n' "$name"
  else
    status=$?
    FAILED+=("$name")
    if [[ $status == 124 ]]; then
      printf 'Android CI: TIMEOUT (%s; exit %s)\n' "$name" "$status" >&2
    elif [[ $status == 137 ]]; then
      printf 'Android CI: KILLED (%s; timeout kill grace or external SIGKILL; exit %s)\n' "$name" "$status" >&2
    else
      printf 'Android CI: FAILED (%s; exit %s)\n' "$name" "$status" >&2
    fi
  fi
}

# The checker tests and resource checks run in the lane that runs lint.
if [[ $LANE == full || $LANE == lint-build-test-0 ]]; then
  cd "$ROOT/scripts/ci"
  run_check 'Android shard coverage tests' python3 -B test_android_shards.py
  cd "$ROOT/scripts/app"
  run_check 'translation checker tests' python3 -B test_android_i18n_check.py
  run_check 'token checker tests' python3 -B test_android_token_baseline.py
  run_check 'slot order checker tests' python3 -B test_android_slot_order.py
  run_check 'translation resources and literal baseline' python3 -B android_i18n_check.py
  run_check 'design token baseline' python3 -B android_token_baseline.py
  run_check 'design-system slot order' python3 -B android_slot_order.py
  run_check 'generated design tokens' python3 -B "$ROOT/scripts/assets/gen_tokens.py" --check \
    --kotlin "$ROOT/android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt"
  run_check 'icon meanings' python3 -B "$ROOT/scripts/assets/gen_icon_meanings.py" --check
fi

cd "$ROOT/android"
# Unqualified lint still covers every module; full preserves the original selectors.
# --continue collects independent failures, never turns them into success.
# Do not stop shared daemons, update baselines, exclude tests or depend on local.properties.
if [[ ${#TASKS[@]} -gt 0 ]]; then
  printf 'Android CI: lane %s; tasks: %s\n' "$LANE" "${TASKS[*]}"
  run_check "Gradle $LANE" bash ./gradlew \
    --no-daemon --no-watch-fs --console=plain --stacktrace --continue \
    --max-workers="$WORKERS" \
    -PpersonaApplicationId=com.example.persona \
    -PpersonaGoogleOAuthScheme=persona.oauth.unset \
    -PpersonaRepoRoot="$ROOT" \
    ${PROFILE[@]+"${PROFILE[@]}"} "${TASKS[@]}"
else
  printf 'Android CI: no modules in lane %s; no Gradle default tasks invoked\n' "$LANE"
fi

if [[ ${#FAILED[@]} -ne 0 ]]; then
  fail "${FAILED[*]}"
fi
printf '\nAndroid CI: PASSED (lane %s)\n' "$LANE"
