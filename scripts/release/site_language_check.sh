#!/bin/bash
# Verify language selection through the real entry proxy, including caches that ignore Vary or Cookie.
set -euo pipefail

fail() {
  printf 'site language check: FAIL (%s): %s\n' "$1" "$2" >&2
  exit 1
}

[ "$#" -eq 1 ] || fail "usage" "expected: scripts/release/site_language_check.sh <base url>"
BASE="${1%/}"
[ -n "$BASE" ] || fail "usage" "the base URL is empty"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/persona-site-language.XXXXXX")" || fail "setup" "could not create a temporary directory"
trap 'rm -rf "$TMP"' EXIT
HEADERS="$TMP/headers"
BODY="$TMP/body"
STATUS=""

fetch() {
  local path="$1" language="${2:-}" cookie="${3:-}"
  local args=(-q -sS --connect-timeout 5 --max-time 20 -D "$HEADERS" -o "$BODY" -w '%{http_code}')
  [ -z "$language" ] || args+=(-H "Accept-Language: $language")
  [ -z "$cookie" ] || args+=(-H "Cookie: $cookie")
  STATUS="$(curl "${args[@]}" "$BASE$path")"
}

header_value() {
  tr -d '\r' <"$HEADERS" | awk -v wanted="$1" '
    tolower($1) == tolower(wanted) ":" {
      $1 = ""
      sub(/^[ \t]+/, "")
      print
      exit
    }'
}

expect_root() {
  local step="$1" language="$2"
  fetch "/" "$language" || fail "$step" "GET / failed for Accept-Language: $language"
  if [ "$language" = "zh-CN" ]; then
    [ "$STATUS" = "200" ] || fail "$step" "Chinese GET / returned $STATUS, expected 200"
    grep -qi '<html lang="zh-CN"' "$BODY" || fail "$step" "Chinese GET / did not return the zh-CN document"
  else
    [ "$STATUS" = "302" ] || fail "$step" "English GET / returned $STATUS, expected 302"
    [ "$(header_value Location)" = "/en/" ] || fail "$step" "English GET / Location was '$(header_value Location)', expected /en/"
  fi
}

# Three rounds on one URL expose a proxy that reuses the previous visitor's response instead of varying by language.
expect_root "round 1 Chinese" "zh-CN"
expect_root "round 1 English" "en-US"
expect_root "round 2 English" "en-US"
expect_root "round 2 Chinese" "zh-CN"
expect_root "round 3 Chinese" "zh-CN"
expect_root "round 3 English" "en-US"

fetch "/" "zh-CN" "persona_lang=en" || fail "explicit English cookie" "GET / failed"
[ "$STATUS" = "302" ] || fail "explicit English cookie" "returned $STATUS, expected 302"
[ "$(header_value Location)" = "/en/" ] || fail "explicit English cookie" "Location was '$(header_value Location)', expected /en/"

fetch "/en/" || fail "canonical English page" "GET /en/ failed"
[ "$STATUS" = "200" ] || fail "canonical English page" "returned $STATUS, expected 200"
grep -qi '<html lang="en"' "$BODY" || fail "canonical English page" "did not return the English document"

fetch "/install.sh" || fail "installer" "GET /install.sh failed"
INSTALLER="present"
case "$STATUS" in
  404)
    INSTALLER="not present; skipped"
    ;;
  200)
    if header_value Content-Type | grep -qi 'text/html'; then
      fail "installer" "returned an HTML Content-Type"
    fi
    if grep -Eiq '^[[:space:]]*(<!doctype[[:space:]]+html|<html([[:space:]>]))' "$BODY"; then
      fail "installer" "returned an HTML document"
    fi
    ;;
  3??)
    fail "installer" "returned redirect $STATUS; the installer must be served directly"
    ;;
  *)
    fail "installer" "returned $STATUS, expected 200 or 404 when not published"
    ;;
esac

printf 'site language check: PASS (three negotiation rounds, cookie override, English page; installer %s)\n' "$INSTALLER"
