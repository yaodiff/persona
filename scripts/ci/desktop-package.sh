#!/bin/bash
# Builds the desktop packages for one platform.
#
#   scripts/ci/desktop-package.sh mac|win
#
#   CSC_LINK (+ CSC_KEY_PASSWORD)     sign with this certificate; without it the package is an explicitly unsigned
#                                     development build with a placeholder app ID.
#   PERSONA_DESKTOP_VERSION           the package version (a release passes its tag without the leading "v").
#   PERSONA_DESKTOP_REQUIRE_SIGNING=1 refuse to build unless a certificate and the real app ID are supplied, so a
#                                     release can never fall back to the unsigned build.
#   APPLE_API_KEY_P8                  the App Store Connect API key's text (macOS notarization). electron-builder wants
#                                     a file, so it is written to a private temporary file for the build only.
set -euo pipefail
source "$(dirname "$0")/common.sh"
target="${1:?usage: scripts/ci/desktop-package.sh mac|win}"
case "$target" in
  mac) platform=mac ;;
  win) platform=win ;;
  *) printf 'usage: scripts/ci/desktop-package.sh mac|win\n' >&2; exit 2 ;;
esac
args=(--"$platform" --publish never)
if [[ -n "${PERSONA_DESKTOP_VERSION:-}" ]]; then
  if [[ ! "$PERSONA_DESKTOP_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    printf 'desktop-package: FAIL PERSONA_DESKTOP_VERSION is not a semantic version: %s\n' "$PERSONA_DESKTOP_VERSION" >&2
    exit 2
  fi
  args+=("-c.extraMetadata.version=$PERSONA_DESKTOP_VERSION")
fi
if [[ "${PERSONA_DESKTOP_REQUIRE_SIGNING:-}" == 1 ]]; then
  if [[ -z "${CSC_LINK:-}" ]]; then
    printf 'desktop-package: FAIL signing is required but no certificate was supplied\n' >&2
    exit 2
  fi
  if [[ -z "${PERSONA_DESKTOP_APP_ID:-}" || "$PERSONA_DESKTOP_APP_ID" == com.example.* ]]; then
    printf 'desktop-package: FAIL signing is required but PERSONA_DESKTOP_APP_ID is not the registered app ID\n' >&2
    exit 2
  fi
fi
if [[ -n "${CSC_LINK:-}" ]]; then
  if [[ -n "${APPLE_API_KEY_P8:-}" ]]; then
    key_directory="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/persona-notary-XXXXXX")"
    trap 'rm -rf "${key_directory:?}"' EXIT
    (umask 077 && printf '%s\n' "$APPLE_API_KEY_P8" > "$key_directory/AuthKey.p8")
    export APPLE_API_KEY="$key_directory/AuthKey.p8"
    unset APPLE_API_KEY_P8
  fi
  ci_run 'Desktop signed package' 2400 npm --prefix desktop run dist -- "${args[@]}"
else
  printf 'desktop-package: no signing certificate supplied; producing an explicitly unsigned development package\n'
  ci_run 'Desktop unsigned package' 2400 npm --prefix desktop run dist:unsigned -- "${args[@]}"
fi
