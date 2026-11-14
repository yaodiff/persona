#!/bin/sh
set -eu
: "${OHOS_SDK_NATIVE:?Set OHOS_SDK_NATIVE}"
exec "$OHOS_SDK_NATIVE/llvm/bin/clang" --target=aarch64-linux-ohos --sysroot="$OHOS_SDK_NATIVE/sysroot" -D__MUSL__ "$@"
