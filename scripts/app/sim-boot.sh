#!/bin/bash
# The one entry for getting a simulator, for scripts and agents alike: reuse or create it, boot it slim with
# simslim (https://github.com/mobai-app/simslim) using the profile for the job, check it, and print its udid.
# Without simslim it falls back to a plain `simctl boot` and says how to install it. docs/operations/testing.md "Simulators".
#
#   SIM=$(scripts/app/sim-boot.sh "Persona Shots <issue>")          # reuse the simulator of that name, or create it
#   scripts/app/sim-boot.sh <udid>                                   # an existing one
#   scripts/app/sim-boot.sh --profile watch --device-type "Apple Watch Series 11 (46mm)" "Persona Watch <issue>"
#
# Options (or env): --profile P (SIM_PROFILE), --device-type T (SIM_DEVICE_TYPE, default iPhone 17 Pro; a name or
# an identifier), --runtime R (SIM_RUNTIME, default the newest runtime that has the device type), --reslim.
# --create-only requires stock and a new name: create a shut-down device for a caller that verifies readiness itself.
# --start-only requires stock and an existing UDID: initiate boot if shut down; the caller must bound the command
# and verify readiness. Its subprocesses stay in the caller's process group so that shared deadline can stop them.
# Profiles (apple/simslim/<P>.json, or a path to a simslim profile JSON):
#   default        UI tests, screenshots, everyday runs (widgets / Live Activities, photos, push, App Intents kept)
#   watch          a paired iPhone + Apple Watch: also Watch connectivity, messaging, HealthKit
#   live-activity  Live Activity / widget / push-to-start work: default plus App Store & push (store)
#   stock          no slimming (system prompts, keyboard, Siri, Spotlight; or to rule slimming out)
#
# Never reboots a simulator that is already booted: it may be another agent's. A booted one that isn't slim to the
# profile is used as it is, with a warning; --reslim reboots it slim (only for your own simulator).
#
# At most SIM_MAX_BOOTED (default 3) simulators are Booted or Booting on this Mac. A boot that would exceed it waits up
# to SIM_SLOT_WAIT seconds (default 1200) for a slot, then exits 75 listing the booted ones: shut down one you own
# (xcrun simctl shutdown <udid>). Using an already booted simulator is never blocked. Boots take a machine-wide lock
# (SIM_BOOT_LOCK_DIR, default $TMPDIR/persona-sim-boot.lock), so two agents cannot both pass the check.
# A new name first adopts a shut-down "Persona …" simulator of the same device type and runtime (renamed) instead of
# creating another one; SIM_REUSE=0 turns that off. --create-only and --start-only (CI, which owns the deadline) skip
# the limit, the lock and the adoption. Shut it down when you are done; delete it when the task ends.
# stdout is only the udid (last line); everything else goes to stderr. The line before it on stderr is the verdict:
#   sim-boot: slim|stock|stock-no-simslim|in-use-not-slim|failed→stock <profile> <udid>
# Exit 0 after boot readiness (creation only with --create-only; boot initiation only with --start-only).
# Exit 2 on usage errors (nothing touched); nonzero when a requested operation fails.
set -uo pipefail
CALLER=$PWD
cd "$(dirname "$0")/../.." || exit 2
ROOT=$PWD

PROFILE="${SIM_PROFILE:-default}"
DEVICE_TYPE="${SIM_DEVICE_TYPE:-iPhone 17 Pro}"
RUNTIME="${SIM_RUNTIME:-}"
MAX_BOOTED="${SIM_MAX_BOOTED:-3}"
SLOT_WAIT="${SIM_SLOT_WAIT:-1200}"
REUSE="${SIM_REUSE:-1}"
LOCKDIR="${SIM_BOOT_LOCK_DIR:-${TMPDIR:-/tmp}/persona-sim-boot.lock}"
case "$MAX_BOOTED$SLOT_WAIT" in *[!0-9]*|"") echo "sim-boot: SIM_MAX_BOOTED and SIM_SLOT_WAIT must be whole numbers" >&2; exit 2 ;; esac
[ "$MAX_BOOTED" -ge 1 ] || { echo "sim-boot: SIM_MAX_BOOTED must be at least 1" >&2; exit 2; }
RESLIM=0
CREATE_ONLY=0
START_ONLY=0
TARGET=""
usage() { sed -n '5,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:-}"; shift 2 || usage ;;
    --device-type) DEVICE_TYPE="${2:-}"; shift 2 || usage ;;
    --runtime) RUNTIME="${2:-}"; shift 2 || usage ;;
    --reslim) RESLIM=1; shift ;;
    --create-only) CREATE_ONLY=1; shift ;;
    --start-only) START_ONLY=1; shift ;;
    -h|--help) usage ;;
    -*) echo "sim-boot: unknown option $1" >&2; usage ;;
    *) [ -z "$TARGET" ] || usage; TARGET="$1"; shift ;;
  esac
done
[ -n "$TARGET" ] || { echo "sim-boot: give a udid or a name (a new name creates the simulator)" >&2; usage; }

log() { echo "sim-boot: $*" >&2; }
# features `simslim doctor` checks after a slim boot (simslim doctor --list); a profile's except/keep must keep them
REQUIRES=""
case "$PROFILE" in
  stock) PROFILE_FILE="" ;;
  default) PROFILE_FILE="$ROOT/apple/simslim/default.json"; REQUIRES="push,photos,widgets" ;;
  watch) PROFILE_FILE="$ROOT/apple/simslim/watch.json"; REQUIRES="push,photos,widgets,health,imessage" ;;
  live-activity) PROFILE_FILE="$ROOT/apple/simslim/live-activity.json"; REQUIRES="push,photos,widgets,app-store" ;;
  */*|*.json) PROFILE_FILE="$PROFILE"; [ "${PROFILE_FILE#/}" != "$PROFILE_FILE" ] || PROFILE_FILE="$CALLER/$PROFILE_FILE" ;;
  *) log "unknown profile $PROFILE (default, watch, live-activity, stock, or a JSON path)"; exit 2 ;;
esac
if [ "$CREATE_ONLY" = 1 ] && { [ "$PROFILE" != stock ] || [ "$RESLIM" = 1 ]; }; then
  log "--create-only requires --profile stock and cannot reslim a device"; exit 2
fi
if [ "$START_ONLY" = 1 ]; then
  if [ "$PROFILE" != stock ] || [ "$RESLIM" = 1 ] || [ "$CREATE_ONLY" = 1 ] ||
     ! [[ "$TARGET" =~ ^[0-9A-Fa-f]{8}-([0-9A-Fa-f]{4}-){3}[0-9A-Fa-f]{12}$ ]]; then
    log "--start-only requires --profile stock and an existing UDID, without reslim or create-only"; exit 2
  fi
fi
if [ -n "$PROFILE_FILE" ] && ! python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$PROFILE_FILE" 2>/dev/null; then
  log "profile $PROFILE_FILE is missing or not JSON; nothing changed"; exit 2
fi

eval "$(python3 scripts/persona_config.py env)" >/dev/null
SIMSLIM="${SIMSLIM:-simslim}"
command -v "$SIMSLIM" >/dev/null 2>&1 || SIMSLIM=""

# timed <seconds> <cmd...>: like slim-sim.sh (kills the whole process group; exit 124 on timeout)
timed() {
  perl -e '
    my $t = shift; my $p = fork // exit 125;
    if (!$p) { setpgrp(0, 0); exec @ARGV or exit 127 }
    $SIG{ALRM} = sub { kill TERM => -$p; select undef, undef, undef, 5; kill KILL => -$p; waitpid $p, 0; exit 124 };
    alarm $t; waitpid $p, 0;
    exit($? & 127 ? 128 + ($? & 127) : $? >> 8);' "$@"
}

# "<udid> <state> <platform>" for a udid or an exact name; several with the name is an error (give the udid)
lookup() {
  local -a runner=(timed 120 xcrun)
  [ "$START_ONLY" = 0 ] || runner=(xcrun)
  "${runner[@]}" simctl list devices -j | python3 -c '
import json, re, sys
want = sys.argv[1]
hits = []
for runtime, devices in json.load(sys.stdin)["devices"].items():
    for d in devices:
        if d["udid"] == want or d["name"] == want:
            hits.append((d["udid"], d["state"].replace(" ", "_"), runtime.rsplit(".", 1)[-1].split("-")[0], d.get("isAvailable", True)))
if sys.argv[2] == "1" and (len(hits) != 1 or not hits[0][3] or hits[0][0] != want):
    sys.exit("sim-boot: --start-only requires one available simulator with UDID " + want)
hits = [h for h in hits if h[3]] or hits
if len(hits) > 1:
    sys.exit("sim-boot: %d simulators are called %r; pass the udid: %s" % (len(hits), want, " ".join(h[0] for h in hits)))
for h in hits: print(*h[:3])
' "$1" "$START_ONLY"
}

# Machine-wide boot lock (a directory: mkdir is atomic). Held until this script exits; a dead holder's lock is taken over.
HAVE_LOCK=0
acquire_lock() {
  [ "$HAVE_LOCK" = 0 ] || return 0
  mkdir -p "$(dirname "$LOCKDIR")"
  local deadline holder
  deadline=$(( $(date +%s) + SLOT_WAIT ))
  until mkdir "$LOCKDIR" 2>/dev/null; do
    holder=$(cat "$LOCKDIR/pid" 2>/dev/null || true)
    if { [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; } ||
       { [ -z "$holder" ] && [ -n "$(find "$LOCKDIR" -maxdepth 0 -mmin +2 2>/dev/null)" ]; }; then
      log "taking over the boot lock of a finished sim-boot.sh (${holder:-no pid})"
      rm -rf "$LOCKDIR"; continue
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      log "waited ${SLOT_WAIT}s for another sim-boot.sh (pid ${holder:-?}) to finish booting; nothing was booted"; exit 75
    fi
    sleep 1
  done
  echo $$ > "$LOCKDIR/pid"
  HAVE_LOCK=1
  trap 'rm -rf "$LOCKDIR"' EXIT
}

# First line: how many simulators are Booted or Booting; then one "  name (udid) state" line each.
booted_report() {
  timed 120 xcrun simctl list devices -j | python3 -c '
import json, sys
rows = [(d["name"], d["udid"], d["state"]) for r in json.load(sys.stdin)["devices"].values() for d in r
        if d["state"] in ("Booted", "Booting") and d.get("isAvailable", True)]
print(len(rows))
for n, u, s in sorted(rows): print("  %s (%s) %s" % (n, u, s))'
}

# Called before a boot or a create+boot of a simulator that is not running yet: takes the lock, then waits for a slot.
slot_gate() {
  acquire_lock
  local deadline report n
  deadline=$(( $(date +%s) + SLOT_WAIT ))
  while :; do
    if ! report=$(booted_report); then
      log "warning: cannot count booted simulators (simctl list failed); not enforcing the limit of $MAX_BOOTED"; return 0
    fi
    n=${report%%$'\n'*}
    [ "$n" -lt "$MAX_BOOTED" ] && return 0
    if [ "$(date +%s)" -ge "$deadline" ]; then
      log "$n simulators are already booted (limit $MAX_BOOTED, SIM_MAX_BOOTED); waited ${SLOT_WAIT}s, nothing was booted. Shut down one you own:"
      log "  xcrun simctl shutdown <udid>"
      printf '%s\n' "${report#*$'\n'}" >&2
      exit 75
    fi
    log "$n simulators are booted (limit $MAX_BOOTED); waiting for a slot"
    sleep $(( SLOT_WAIT < 5 ? 1 : 5 ))
  done
}

# udid of a shut-down "Persona …" simulator with this device type and runtime (adopted instead of creating one)
adopt_candidate() {
  timed 120 xcrun simctl list devices -j | python3 -c '
import json, sys
t, r = sys.argv[1], sys.argv[2]
hits = sorted((d["name"], d["udid"]) for d in json.load(sys.stdin)["devices"].get(r, [])
              if d["state"] == "Shutdown" and d.get("isAvailable", True)
              and d.get("deviceTypeIdentifier") == t and d["name"].startswith("Persona "))
if hits: print(hits[0][1])' "$1" "$2" 2>/dev/null || true
}

create() {
  local spec
  spec=$(timed 120 xcrun simctl list -j devicetypes runtimes | python3 -c '
import json, sys
want, runtime = sys.argv[1], sys.argv[2]
d = json.load(sys.stdin)
types = [t for t in d["devicetypes"] if want in (t["name"], t["identifier"])]
if not types: sys.exit("sim-boot: no device type %r (xcrun simctl list devicetypes)" % want)
t = types[0]["identifier"]
rts = [r for r in d["runtimes"] if r.get("isAvailable") and any(s["identifier"] == t for s in r.get("supportedDeviceTypes", []))]
if runtime:
    rts = [r for r in rts if runtime in (r["identifier"], r["name"], r["version"])]
    if not rts: sys.exit("sim-boot: runtime %r does not run %r" % (runtime, want))
if not rts: sys.exit("sim-boot: no installed runtime runs %r" % want)
key = lambda r: tuple(int(x) for x in r["version"].split("."))
print(t, max(rts, key=key)["identifier"])
' "$DEVICE_TYPE" "$RUNTIME") || exit 2
  if [ "$REUSE" != 0 ] && [ "$CREATE_ONLY" = 0 ]; then
    local old
    old=$(adopt_candidate "${spec% *}" "${spec#* }")
    if [ -n "$old" ]; then
      log "reusing the shut-down simulator $old as \"$TARGET\" instead of creating another one"
      if timed 60 xcrun simctl rename "$old" "$TARGET" >/dev/null; then return 0; fi
      log "could not rename $old; creating a new simulator"
    fi
  fi
  log "creating \"$TARGET\" (${spec% *}, ${spec#* })"
  timed 300 xcrun simctl create "$TARGET" $spec >/dev/null || { log "simctl create failed"; exit 1; }
}

INFO=$(lookup "$TARGET") || exit 2
if [ "$CREATE_ONLY" = 1 ] && [ -n "$INFO" ]; then
  log "--create-only requires a new name; refusing existing simulator $TARGET"; exit 2
fi
if [ -z "$INFO" ]; then
  if [[ "$TARGET" =~ ^[0-9A-Fa-f]{8}-([0-9A-Fa-f]{4}-){3}[0-9A-Fa-f]{12}$ ]]; then log "no simulator $TARGET"; exit 2; fi
  # a new device is booted below, so it needs a slot (CI's --create-only boots nothing and owns its deadline)
  [ "$CREATE_ONLY" = 1 ] || slot_gate
  create
  INFO=$(lookup "$TARGET") || exit 2
  [ -n "$INFO" ] || { log "created \"$TARGET\" but cannot find it"; exit 1; }
elif [ "$CREATE_ONLY" = 0 ] && [ "$START_ONLY" = 0 ]; then
  # booted or booting simulators are never blocked; a shut-down one needs a slot to boot
  case "$INFO" in *" Shutdown "*) slot_gate ;; esac
fi
read -r UDID STATE PLATFORM <<<"$INFO"

finish() { log "$1 $PROFILE $UDID"; echo "$UDID"; exit "${2:-0}"; }
[ "$CREATE_ONLY" = 0 ] || finish created
if [ "$START_ONLY" = 1 ]; then
  case "$STATE" in
    Booted) finish already-started ;;
    Shutdown)
      # No bootstatus wait: the caller owns its readiness check and overall deadline.
      xcrun simctl boot "$UDID" >&2
      code=$?
      [ "$code" = 0 ] || { log "$UDID could not start (boot exit $code)"; exit "$code"; }
      finish started ;;
    *) log "$UDID is $STATE; refusing to start it"; exit 1 ;;
  esac
fi
# Keep boot progress and errors visible without mixing them with the UDID on stdout.
wait_booted() {
  python3 "$ROOT/scripts/app/sim_boot_wait.py" --timeout "${SIM_BOOT_TIMEOUT:-900}" --udid "$UDID" >&2
  local code=$?
  if [ "$code" != 0 ]; then log "$UDID did not finish booting (bootstatus exit $code)"; fi
  return "$code"
}
plain_boot() {
  timed 120 xcrun simctl boot "$UDID" >&2
  local code=$?
  if [ "$code" != 0 ]; then log "$UDID could not start (boot exit $code)"; exit "$code"; fi
  wait_booted || exit $?
}
# number of services simslim has turned off on this device (0 for stock or unknown)
disabled_count() {
  timed 120 "$SIMSLIM" list --json </dev/null 2>/dev/null | python3 -c '
import json, sys
print(next((d.get("managedDisabled", 0) for d in json.load(sys.stdin) if d["udid"] == sys.argv[1]), 0))' "$UDID" 2>/dev/null || echo 0
}
doctor() {
  [ -n "$REQUIRES" ] || return 0
  local rc
  timed 300 "$SIMSLIM" doctor "$UDID" --requires "$REQUIRES" </dev/null >&2; rc=$?
  case $rc in
    0) ;;
    124) log "warning: simslim doctor timed out (the Mac is busy); not checked: $REQUIRES" ;;
    *) log "warning: simslim doctor says a feature this profile needs is broken (exit $rc, above); compare on stock: simslim off $UDID" ;;
  esac
}

case "$STATE" in Creating|Booting|Shutting_Down) log "$UDID is $STATE, waiting"; wait_booted || exit $?; INFO=$(lookup "$UDID"); read -r UDID STATE PLATFORM <<<"$INFO" ;; esac
case "$PLATFORM" in
  iOS|watchOS) ;;
  *) log "$PLATFORM simulator: no profile for it, booting stock"; [ "$STATE" = Booted ] || plain_boot; finish stock ;;
esac

if [ -z "$SIMSLIM" ]; then
  [ "$STATE" = Booted ] || plain_boot
  [ "$PROFILE" = stock ] && finish stock
  log "simslim is not installed, so this simulator runs stock (~4-6 GB instead of ~1-2 GB). Install it:"
  log "  brew install mobai-app/tap/simslim   (or: go install github.com/mobai-app/simslim/cmd/simslim@v0.14.0)"
  finish stock-no-simslim
fi

if [ "$PROFILE" = stock ]; then
  if [ "$STATE" = Booted ]; then
    [ "$(disabled_count)" = 0 ] || log "warning: $UDID is booted slim; left as it is (simslim off $UDID reboots it stock)"
    finish stock
  fi
  if [ "$(disabled_count)" != 0 ]; then
    log "putting $UDID back to stock"
    timed 1200 "$SIMSLIM" off "$UDID" --preserve-boot-state </dev/null >&2 || { log "simslim off failed"; exit 1; }
  fi
  plain_boot
  finish stock
fi

if [ "$STATE" = Booted ] && [ "$RESLIM" = 0 ]; then
  if timed 120 "$SIMSLIM" verify "$UDID" --profile "$PROFILE_FILE" </dev/null >/dev/null 2>&1; then
    doctor; finish slim
  fi
  log "$UDID is already booted and not slim to $PROFILE; using it as it is (it may be another agent's)."
  log "  If it is yours: scripts/app/sim-boot.sh --reslim --profile $PROFILE $UDID   (reboots it)"
  finish in-use-not-slim
fi

# shut down (or --reslim): slim-sim.sh boots it slim, verifies, and on any error puts it back to stock
VERDICT=$(scripts/app/slim-sim.sh "$UDID" "$PROFILE_FILE" 2>&1 | tee /dev/stderr | sed -n 's/^slim-sim: \([^ ]*\) .*/\1/p' | tail -1)
case "$VERDICT" in
  slim) doctor; finish slim ;;
  failed→stock) wait_booted || plain_boot; finish failed→stock ;;
  skipped) [ "$(lookup "$UDID" | cut -d' ' -f2)" = Booted ] || plain_boot; finish stock ;;
  *) log "slim-sim.sh ended with \"$VERDICT\""; exit 1 ;;
esac
