#!/bin/bash
# check-fast.sh — run only the local fast checks that a change needs (#2849).
#
#   scripts/check-fast.sh                 # diff merge-base(HEAD, default branch)..working tree, pick checks by path;
#                                         # the default branch is origin/HEAD, else origin/main, else origin/master
#                                         # (local refs only, no network)
#   scripts/check-fast.sh --base main     # diff against another ref
#   scripts/check-fast.sh --all           # every local fast check except iOS/macOS builds and the Postgres suite,
#                                         # including every scripts/**/test_*.py suite CI's Scripts tests job runs
#                                         # (a test_*.py that never mentions unittest is a SKIP line, as in
#                                         # scripts/ci/python-tests.py; diff mode skips it too)
#   scripts/check-fast.sh --dry-run       # print the plan (the queued checks and the SKIP / INFO notes), run nothing
#   scripts/check-fast.sh --dry-run --changed-from <file|->
#                                         # the plan for the paths listed in the file (one per line), not the diff
#   scripts/check-fast.sh --dry-run --changed-from <file|-> --each
#                                         # the plan of each listed path on its own, after a "== <path>" line;
#                                         # scripts/ci/affected_drift.py compares them with the CI lanes (#11026)
#   scripts/check-fast.sh --compare-main  # after the run, rerun only what failed at the merge base and say, for each
#                                         # failure, whether the base fails it too (scripts/ci/check_fast_compare.py)
#   scripts/check-fast.sh --compare-tip   # --compare-main, and the failures the base has too also at the base ref's tip
#   scripts/check-fast.sh --no-server-go  # plan as usual, but leave the server module's go test, go build, repo-wide guard
#                                         # and go vet steps to the caller (scripts/ci/affected.sh tests the affected
#                                         # packages against Postgres itself); gofmt, sdk/go and the rest still run
#   scripts/check-fast.sh -h|--help
#
# Path mapping (diff mode): the table in scripts/ci/check_fast_map.py maps each changed path to its checks, and a new
# mapping is a rule there (python3 scripts/ci/check_fast_map.py < paths prints the checks of each path). What a change
# selects that depends on the tree or on a rename is found here at run time instead:
#   web/**, and files outside web/ that web/src and its tests name (spec/fixtures/<case>/..., schema/extension/...,
#   scripts/mock/...)          the tests that python3 scripts/ci/paths.py --web-plan picks (#10619), after npm ci
#                              (only with no node_modules or a changed package.json/package-lock.json), node_modules
#                              against package-lock.json and the typecheck. paths.py derives the files outside web/
#                              from the paths web/src and its tests name, CI's web lane inputs too (--web-inputs):
#     web/src/** only          npx vitest related --run: the tests whose imports reach a changed file, plus every
#                              vitest file that reads files from disk (fs, node:fs/promises, readFileSync,
#                              import.meta.glob, ?raw) or builds through a Vite config (imports vite): the import
#                              graph does not see those reads. They are found by scanning the tests at run time
#     web/src/lib/store.ts, web/src/lib/api.ts, web/src/ui/atoms.tsx, web/src/i18n/**, a web/src module the Vite
#     configs import, anything the vitest setup files reach (setupFiles, globalSetup: their imports and reads, followed
#     to the end, such as web/src/admin/catalog.ts), anything in web/ outside web/src (configs, package.json,
#     package-lock.json, scripts/, e2e/)
#                              npm test, the whole suite (as --all does)
#   server/internal/db/migrations/**, scripts/ci/migration_freeze.py
#                              python3 scripts/ci/migration_freeze.py (a migration already on the base must not be
#                              edited, deleted or renamed; comment- and whitespace-only edits pass; a new file may be
#                              added, named YYYYMMDDHHMMSS_<name>.sql by the UTC second it was written). A rename out of
#                              the directory counts too. --all runs it when the script is present.
#   server/**/*_test.go, sdk/go/**/*_test.go, scripts/ci/sleep_ratchet.py, scripts/ci/sleep_baseline.json
#                              python3 scripts/ci/sleep_ratchet.py (each Go test file's time.Sleep count may stay or
#                              fall, never rise; comments, strings and a // sleep-ok: reason line do not count; a new
#                              file starts at zero; --update only lowers). A rename out of those trees counts too.
#                              --all runs it when the script is present.
#   spec/fixtures/**, schema/<dir>/**, integrations/**, docs/<file>, scripts/test/server_test_env.py
#                              also go test on each server package whose _test.go names that input (server-test.yml's
#                              path filter), found at run time with git grep (comment lines are ignored)
#   apple/**, android/**       also go test on each server package whose _test.go names the file whole, in quotes
#                              (internal/proto's Swift and Kotlin contract checks, #11026)
#   spec/**, schema/**         also the tests of each integrations/<pkg> and of sdk/ts whose files name the path or a
#                              directory it is in (CI's runtimes lane runs for all of them), found at run time
#   scripts/**                 each changed scripts/<dir>/test_*.py and the test_<name>.py of each changed script
#                              <name>.py / <name>.sh (- read as _; db.sh's is test_db_sh.py), run from its
#                              directory as CI does (#3968), and every scripts/**/test_*.py that imports a changed
#                              <name>.py from its own directory or from one it names, found at run time (a suite a
#                              dedicated check runs schedules that check; #11026); then the table's suites
# A change under server/ or sdk/go/ also runs the repo-wide guard step (job_go_guards: GO_GUARD_PKGS, GO_GUARD_TEST_PKGS
# and GO_GUARD_TESTS below), the guards and cross-package contract tests that fail on code outside the touched group:
# the architecture and link guards, the i18n and prompts packages, and the hub's and the assistant's goldens, wire
# samples, route scopes and size budgets (#12842).
# It also runs go vet, in a job of its own (job_go_vet, #12894): go vet -ldflags= ./... over the whole server module,
# then GOOS=windows GOARCH=amd64 go vet -ldflags= on each changed package group, or on ./... for a change outside every
# group (go.mod, a file directly in server/plugins/, sdk/go), as CI's server lane vets for Linux and Windows. A group
# with no package that builds for Windows (scripts/fleetsoak, cmd/password-wasm) is left out of the Windows step, as
# CI's ./... passes over it, and the PASS line names it. Being its own job, a vet that runs past its timeout on a cold
# build cache is that job's TIMEOUT and cannot hide a FAIL of the guard job's contract tests. A changed .go file under server/ or sdk/go/ runs scripts/ci/gofmt.py on the changed .go
# files (job_go_fmt; --all checks both trees whole, as CI's Go formatting step does), a job that takes no go slot: it
# compiles nothing, so it reports in seconds even while every go slot is busy.
# scripts/privacy_check.py runs on every invocation that has changes (it is a seconds-long local scan, no network).
# The plan ends with an INFO line naming the CI lanes the change selects (python3 scripts/ci/paths.py --lanes-for, the
# selection CI and the PR watcher read) that it runs no part of, such as android, core or desktop (#11026).
#
# Independent checks run in parallel; each command gets a GNU timeout (override with the CHECK_FAST_TIMEOUT_*
# variables below). The go test / go build / go vet and vitest commands first take a machine-wide slot of their kind
# (scripts/heavy_slot.py; docs/operations/testing.md "Heavy build slots"), so parallel worktrees queue instead of
# thrashing; the wait comes before the timeout, and a wait past PERSONA_HEAVY_SLOT_TIMEOUT is a TIMEOUT.
# While a step waits for a slot, the helper's "waiting for a <kind> slot" line, and the one when it gets the slot,
# appear on stderr as they happen ("check-fast: <step>: waiting for a go slot (3 busy, up to 45m)").
# Every check prints one PASS / FAIL / TIMEOUT / SKIP line with its duration, followed by a summary; a check that
# waited for a slot names the wait apart from its own time: "PASS     server go test ./... (95s, plus 140s waiting for a
# go slot)". The temp log directory is kept when a check fails or times out and removed on a fully clean run.
# Each failed or timed-out check ends with a "rerun:" line per failure, the command that reruns just that: the failing
# Go tests of each package (go test -count=1 -run '^(A|B)$' ./pkg), the failing unittest ids, or the step's own
# command. step() records each failed step for them (record_step); scripts/ci/check_fast_compare.py narrows it.
#
# --compare-main (#12733): when a check failed, rerun only the failures, in a detached worktree of the merge base
# (<log dir>/compare/base, removed afterwards by that path; a signal removes it too), and print one line for each:
# ALREADY (also fails at the base), NEW (passes there and fails again rerun here on its own, so likely this change),
# FLAKY (passes there and rerun here on its own), EDITED (not attributable: the change edits that test's files) or
# UNKNOWN (not compared, and why). The rules are the PR watcher's: named tests only, no attribution when the change
# edits the test. --compare-tip also reruns the ALREADY ones at the base ref's tip
# (TIP-PASS: main fixed it since; TIP-FAIL: it still fails; TIP-?: no answer there). The exit code stays the run's: a failure main already has
# is still a failure here. With --all the merge base and the change's files are found as in diff mode.
#
# Exit codes: 0 all checks passed, skipped or not needed; 1 one or more checks FAILED; 124 no failure but at least
# one check TIMEOUT (a timeout is reported, not silently treated as success; run the full suite when that happens).
# Ctrl-C, SIGTERM and SIGHUP, to the script or to its whole process group, stop every running check first, keep the
# log directory, and exit 130, 143 or 129.
# A missing optional toolchain (node < 22, go, bun) skips only its checks in diff mode; in --all mode a missing
# core tool (node >= 22, go, python3) is a failure. A diff cannot be planned without python3, which reads the table:
# check-fast stops with exit 2, as it does when a search the plan depends on fails.
#
# This script never runs iOS/macOS checks (use a Mac with scripts/app/sim.sh; PersonaKit: swift test, docs/operations/testing.md),
# the Android gradle build (scripts/app/android.sh), or the full server suite against real Postgres
# (scripts/test/test-server.sh).
set -uo pipefail
# A caller that closed stderr (2>&-) would make every redirection that copies it fail: each job subshell's 4>&2, so no
# check could report and every one would end "no status" (#11250). Open it on /dev/null instead: the checks run, only
# the live slot lines are lost, and no pipe or file opened later can take the free fd 2 (bash leaves a process
# substitution's pipe there). Copying fd 2 fails only when it is closed, and the error message then goes nowhere.
: 9>&2 || exec 2>/dev/null

CALLER_DIR=$PWD  # a relative --changed-from file is read from here
cd "$(dirname "$0")/../.."
ROOT=$PWD  # the directory each failed step ran in is recorded relative to it (record_step)

# Per-command timeouts in seconds (CLAUDE.md: every background step carries a timeout). Override for slow machines.
TIMEOUT_NPM_CI="${CHECK_FAST_TIMEOUT_NPM_CI:-600}"
TIMEOUT_WEB_TEST="${CHECK_FAST_TIMEOUT_WEB_TEST:-300}"
TIMEOUT_GO_TEST="${CHECK_FAST_TIMEOUT_GO_TEST:-300}"
TIMEOUT_OTHER="${CHECK_FAST_TIMEOUT_OTHER:-120}"

usage() {
  awk 'NR==1 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "$0"
}

ALL=0
BASE_REF=""
COMPARE=0  # 1: --compare-main, 2: --compare-tip
DRY_RUN=0
CHANGED_FROM=""
EACH=0
NO_SERVER_GO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --all) ALL=1 ;;
    # An empty value is refused: BASE_REF="" means "no --base" below.
    --base)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "check-fast: --base needs a git ref" >&2; exit 2; }
      BASE_REF=$2; shift ;;
    --base=*)
      BASE_REF=${1#*=}
      [ -n "$BASE_REF" ] || { echo "check-fast: --base needs a git ref" >&2; exit 2; } ;;
    --dry-run) DRY_RUN=1 ;;
    --changed-from)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "check-fast: --changed-from needs a file (- for stdin)" >&2; exit 2; }
      CHANGED_FROM=$2; shift ;;
    --changed-from=*)
      CHANGED_FROM=${1#*=}
      [ -n "$CHANGED_FROM" ] || { echo "check-fast: --changed-from needs a file (- for stdin)" >&2; exit 2; } ;;
    --each) EACH=1 ;;
    --no-server-go) NO_SERVER_GO=1 ;;
    --compare-main) [ "$COMPARE" -eq 2 ] || COMPARE=1 ;;
    --compare-tip) COMPARE=2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "check-fast: unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done
# A listed set of paths has no merge-base for the checks that compare with one (frozen migrations, fixed sleeps), so
# it only plans; --each plans one listed path at a time.
if [ -n "$CHANGED_FROM" ] && { [ "$DRY_RUN" -eq 0 ] || [ "$ALL" -eq 1 ] || [ -n "$BASE_REF" ]; }; then
  echo "check-fast: --changed-from goes with --dry-run, without --all or --base" >&2; exit 2
fi
if [ "$EACH" -eq 1 ] && [ -z "$CHANGED_FROM" ]; then
  echo "check-fast: --each needs --dry-run --changed-from <file>" >&2; exit 2
fi
if [ "$COMPARE" -gt 0 ] && [ "$DRY_RUN" -eq 1 ]; then
  echo "check-fast: --compare-main and --compare-tip rerun failures, which a dry run has none of" >&2; exit 2
fi
case "$CHANGED_FROM" in ""|-|/*) ;; *) CHANGED_FROM="$CALLER_DIR/$CHANGED_FROM" ;; esac
if [ -n "$CHANGED_FROM" ] && [ "$CHANGED_FROM" != "-" ] && [ ! -f "$CHANGED_FROM" ]; then
  echo "check-fast: --changed-from: no such file: $CHANGED_FROM" >&2; exit 2
fi

if command -v timeout >/dev/null 2>&1; then
  TIMEOUT_BIN=timeout
elif command -v gtimeout >/dev/null 2>&1; then
  TIMEOUT_BIN=gtimeout
else
  echo "check-fast: GNU timeout is required (macOS: brew install coreutils)" >&2
  exit 2
fi

have() { command -v "$1" >/dev/null 2>&1; }

# Coarse interpreter gate: only the Node major version is checked here (22+). Each package's engines field
# (web needs 22.13, some integrations 22.19) is npm's own check; without engine-strict npm only warns on it.
NODE_MAJOR=0
if have node; then
  NODE_MAJOR=$(node -e 'console.log(process.versions.node.split(".")[0])' 2>/dev/null || echo 0)
fi
node_ok() { [ "$NODE_MAJOR" -ge 22 ] 2>/dev/null; }
HAVE_GO=0; have go && HAVE_GO=1
HAVE_PY=0; have python3 && HAVE_PY=1
HAVE_BUN=0; have bun && HAVE_BUN=1
# Go's build cache key holds each package's directory unless -trimpath is set: add it, so worktrees share compiled
# packages (#10614). The helper keeps a user-set GOFLAGS; without the helper GOFLAGS stays as it was.
if [ "$HAVE_GO" -eq 1 ] && GO_TRIMPATH_FLAGS=$(sh scripts/test/go_trimpath.sh 2>/dev/null); then
  export GOFLAGS="$GO_TRIMPATH_FLAGS"
fi
# A job that sets JOB_SLOT (go, web) runs its steps in a machine-wide slot of that kind (#10714); with the slots off
# (PERSONA_HEAVY_SLOTS=0), or without the helper or python3, they run directly, as before.
HEAVY_SLOT="$PWD/scripts/heavy_slot.py"
# The per-path table: the checks each changed path selects (plan_changes reads it once a run).
CHECK_FAST_MAP=scripts/ci/check_fast_map.py
# What a failed step's rerun line is, and --compare-main's reruns at the merge base (#12733).
COMPARE_HELPER=scripts/ci/check_fast_compare.py

WORK=$(mktemp -d "${TMPDIR:-/tmp}/check-fast.XXXXXX")
# Keep WORK after a failure or timeout (its logs are the trail to read); a clean run removes it on exit.
KEEP_WORK=0
cleanup_work() { [ "$KEEP_WORK" -eq 0 ] && rm -rf "$WORK"; }
trap cleanup_work EXIT

# descendants prints every process below pid $1, parents before their children (POSIX ps, so it runs on macOS too).
descendants() {
  ps -A -o pid= -o ppid= 2>/dev/null | awk -v root="$1" '
    { kids[$2] = kids[$2] " " $1 }
    END {
      n = split(kids[root], queue, " ")
      for (i = 1; i <= n; i++) {
        print queue[i]
        m = split(kids[queue[i]], c, " ")
        for (j = 1; j <= m; j++) queue[++n] = c[j]
      }
    }'
}

# on_signal stops every check on Ctrl-C, TERM or HUP. A signal to this script alone never reaches them: the job
# subshells run in the background (a non-interactive shell starts them ignoring SIGINT) and GNU timeout moves each
# command into its own process group, so without this they keep running, reparented to init, writing into $WORK.
# A signal to the whole process group (a closed terminal or tmux pane, kill -- -PGID) reaches the job subshells and
# tee as well, so they ignore TERM and HUP too (launch_all); otherwise they would die first and orphan the checks.
# The whole tree is frozen first (SIGSTOP, repeated until no new process appears) so nothing forks the next step or
# escapes to init while it is being collected. The processes in this script's own process group (subshells, tee,
# short helpers) then get KILL while still frozen; every other one is a check in the group timeout made for it and
# gets TERM and CONT (timeout forwards the TERM to its group), and whatever is still alive after timeout's own -k
# grace gets KILL.
on_signal() {  # on_signal <signal name> <exit code>
  local _sig=$1 _code=$2 _all=" " _new _p _g _pass _list _left _own _groups
  trap '' INT TERM HUP
  for _pass in 1 2 3 4 5 6 7 8 9 10; do
    _new=0
    for _p in $(descendants $$); do
      case "$_all" in *" $_p "*) continue ;; esac
      # The ps and awk of the snapshot have exited by now, so kill fails for them and they are not collected.
      kill -STOP "$_p" 2>/dev/null || continue
      _all="$_all$_p "; _new=1
    done
    [ "$_new" -eq 0 ] && break
  done
  # One ps for the process group of each frozen process; if it fails, every process takes the TERM path below.
  _own=$(ps -o pgid= -p $$ 2>/dev/null | tr -d ' ')
  _list=$(echo $_all | tr ' ' ',')
  _groups=$(ps -o pid= -o pgid= -p "$_list" 2>/dev/null)
  # bash reports each job subshell it reaps after a KILL ("line N: <pid> Killed ( ... )"); keep that noise off stderr.
  exec 3>&2 2>/dev/null
  if [ -n "$_own" ] && [ -n "$_groups" ]; then
    _all=" "
    while read -r _p _g; do
      [ -n "$_p" ] || continue
      if [ "$_g" = "$_own" ]; then kill -KILL "$_p" 2>/dev/null; else _all="$_all$_p "; fi
    done < <(printf '%s\n' "$_groups")
  fi
  for _p in $_all; do kill -TERM "$_p" 2>/dev/null; kill -CONT "$_p" 2>/dev/null; done
  _list=$(echo $_all | tr ' ' ',')
  if [ -n "$_list" ]; then
    _pass=0
    while [ "$_pass" -lt 70 ]; do  # up to ~7s: timeout's -k 5s grace plus a margin
      # A process that has exited stays a zombie (Z) until its parent reaps it; count only live processes.
      _left=$(ps -o stat= -p "$_list" 2>/dev/null | grep -vc '^Z')
      [ "${_left:-0}" -eq 0 ] && break
      sleep 0.1
      _pass=$((_pass + 1))
    done
    for _p in $_all; do kill -KILL "$_p" 2>/dev/null; done
  fi
  wait
  exec 2>&3 3>&-
  # --compare-main's checkouts: the helper removes them itself, but it was just killed with the rest. By their paths.
  for _p in "$WORK/compare/base" "$WORK/compare/tip"; do
    [ -d "$_p" ] && "$TIMEOUT_BIN" -k 5s 120 git worktree remove --force "$_p" >/dev/null 2>&1
  done
  KEEP_WORK=1
  echo >&2
  echo "check-fast: interrupted (SIG$_sig); stopped the running checks; logs in $WORK" >&2
  exit "$_code"
}
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

# Job bookkeeping (plain indexed arrays for macOS's bash 3.2).
JOB_NAME=(); JOB_FN=(); JOB_ARG=(); JOB_LOG=(); JOB_STATUS=(); JOB_PID=()
INFO=()                 # informational SKIP lines for the summary

# arr_has reports whether a token equals one of the remaining arguments (arr_has "$t" "${arr[@]:-}"), so the
# package and go-group lists can be indexed arrays that keep spaces and glob characters in directory names.
arr_has() {
  local _token=$1 _x
  shift
  for _x in "$@"; do [ "$_x" = "$_token" ] && return 0; done
  return 1
}

# step runs one command under a timeout; a timeout classifies the whole job and stops it.
# JOB_HINT (set by the job body) is appended to the TIMEOUT detail; each job appends its steps to one log. JOB_NOTE (set
# by the job body, outside in_dir's subshell) is shown on the job's PASS line: what a passing job left out, and why.
step() {
  local _seconds=$1 _desc=$2
  shift 2
  echo "+ $_desc (timeout ${_seconds}s)"
  # tee the output so the job log stays live and a whole-suite self-timeout can still be classified as TIMEOUT
  # instead of FAIL. One appended file per job is not reset by in_dir's subshell, so a multi-step job (web:
  # npm ci, typecheck, vitest) keeps every step. The grep anchors on the runners' own whole-suite markers, never
  # on the words "timed out" (a failing assertion that merely prints them must stay a FAIL):
  #   go test panics "panic: test timed out after …" when ITS suite deadline kills the run;
  #   node's TAP reporter emits "failureType: 'testTimeoutFailure'". A node per-test timeout under the default
  #   reporter is a clean exit 1 naming the test, so it rightly stays a FAIL, not a whole-job TIMEOUT.
  local _out="$WORK/job.${JOB_I:-x}.steps.log" _rc _from=0
  [ -f "$_out" ] && _from=$(wc -c < "$_out")
  # The slot comes before the timeout, so waiting in its queue is not counted as a hung step. The helper's lines come
  # before the command's first output: slot_lines shows them live on fd 4 (launch_all's stderr) and records the wait.
  # fd 4 is closed for the command and tee, so nothing a check leaves running holds the caller's stderr open.
  if [ -n "${JOB_SLOT:-}" ] && [ "${PERSONA_HEAVY_SLOTS:-}" != 0 ] && [ "$HAVE_PY" -eq 1 ] && [ -f "$HEAVY_SLOT" ]; then
    python3 "$HEAVY_SLOT" run --kind "$JOB_SLOT" -- "$TIMEOUT_BIN" -k 5s "$_seconds" "$@" 4>&- 2>&1 |
      tee -a "$_out" 4>&- | slot_lines "$_desc"
    _rc=${PIPESTATUS[0]}
  else
    "$TIMEOUT_BIN" -k 5s "$_seconds" "$@" 4>&- 2>&1 | tee -a "$_out" 4>&-
    _rc=${PIPESTATUS[0]}
  fi
  local _hint="${JOB_HINT:+ ($JOB_HINT)}"
  if [ "$_rc" -eq 124 ]; then
    # The helper's own 124: no slot came free in time, so the command never ran (not a hung check).
    [ -n "${JOB_SLOT:-}" ] && grep -q '^heavy_slot: timed out' "$_out" 2>/dev/null &&
      _hint=" (no ${JOB_SLOT:-} slot came free; it did not run)$_hint"
    record_step TIMEOUT "$_rc" "$_from" "$_desc" "$@"
    printf 'TIMEOUT\t%s\t%s\t%s%s\n' "$_rc" "$SECONDS" "$_desc" "$_hint" > "$JOB_STATUS"
    exit 0
  fi
  if [ "$_rc" -eq 137 ]; then
    # SIGKILL: timeout -k's kill after a command that ignored TERM, or the OOM killer — both mean "did not finish".
    record_step TIMEOUT "$_rc" "$_from" "$_desc" "$@"
    printf 'TIMEOUT\t%s\t%s\t%s (killed: timeout SIGKILL or OOM)%s\n' "$_rc" "$SECONDS" "$_desc" "$_hint" > "$JOB_STATUS"
    exit 0
  fi
  if [ "$_rc" -ne 0 ] && grep -qE "panic: test timed out after [0-9]|failureType: '?testTimeoutFailure'?" "$_out" 2>/dev/null; then
    record_step TIMEOUT "$_rc" "$_from" "$_desc" "$@"
    printf 'TIMEOUT\t%s\t%s\t%s (test suite self-timed out)%s\n' "$_rc" "$SECONDS" "$_desc" "$_hint" > "$JOB_STATUS"
    exit 0
  fi
  [ "$_rc" -eq 0 ] || record_step FAIL "$_rc" "$_from" "$_desc" "$@"
  return "$_rc"
}

# record_step <FAIL|TIMEOUT> <exit code> <offset> <description> <command...> records a step that did not pass, for the
# summary's rerun lines and --compare-main (scripts/ci/check_fast_compare.py reads it): job.<n>.step.<m> in the log
# directory, NUL-separated fields: the status, the exit code, the directory it ran in (relative to the repository), the
# byte range of its output in job.<n>.steps.log (from <offset>, the log's size when the step started, to its size now),
# the description, then the command. A job's steps run one after another, so a counter file numbers them; in_dir's
# subshell would lose a variable.
record_step() {
  local _status=$1 _rc=$2 _from=$3 _desc=$4 _to=0 _n=0 _dir
  local _out="$WORK/job.${JOB_I:-x}.steps.log" _count="$WORK/job.${JOB_I:-x}.nsteps"
  shift 4
  [ -f "$_out" ] && _to=$(wc -c < "$_out")
  [ -f "$_count" ] && read -r _n < "$_count"
  _n=$((_n + 1))
  echo "$_n" > "$_count"
  _dir=${PWD#"$ROOT"}; _dir=${_dir#/}
  printf '%s\0' "$_status" "$_rc" "${_dir:-.}" "$((_from))" "$((_to))" "$_desc" "$@" > "$WORK/job.${JOB_I:-x}.step.$_n"
}

# slot_lines <step> passes a slot step's output through. The helper's lines about a wait come before the command's
# first output: "waiting for a go slot (…)", then "got a go slot after 1m5s" or "timed out after 45m waiting for a go
# slot (…)" (that one alone when no wait was allowed). It also copies those live to fd 4 as "check-fast: <step>:
# <line>", so a run waiting for a slot is not silent until the step ends, and appends each wait to the job's wait file
# as "<kind> <seconds>" for the summary. A "got" line without a "waiting" line before it, or anything after the first
# other line, is the command's and only passes through: a free slot runs the command without a word.
# bash's read takes a pipe a byte at a time, so the waiting line is seen the moment it arrives; awk is no substitute,
# since mawk (Debian and Ubuntu's awk) fills a 4 KiB buffer or reaches EOF before it handles a line read from a pipe,
# which here is the whole wait. After the helper's lines cat copies the rest, with fd 4 closed.
SLOT_WAITING='^heavy_slot: waiting for an? [a-z]+ slot \([0-9]+ busy, up to [0-9ms]+\)$'
SLOT_GOT='^heavy_slot: got an? ([a-z]+) slot after ([0-9ms]+)$'
SLOT_TIMED_OUT='^heavy_slot: timed out after ([0-9ms]+) waiting for an? ([a-z]+) slot '
slot_lines() (
  _step=$1
  # slot_read reads one line into _line (a last line without a newline too); false at the end of the output.
  slot_read() { _line=""; IFS= read -r _line || [ -n "$_line" ]; }
  # slot_live copies the line to fd 4 and records the wait a "got" or "timed out" line reports (45s, 2m or 1m5s).
  slot_live() {
    local _kind="" _wait="" _minutes=0
    printf 'check-fast: %s: %s\n' "$_step" "${_line#heavy_slot: }" >&4
    if [[ $_line =~ $SLOT_GOT ]]; then
      _kind=${BASH_REMATCH[1]}; _wait=${BASH_REMATCH[2]}
    elif [[ $_line =~ $SLOT_TIMED_OUT ]]; then
      _kind=${BASH_REMATCH[2]}; _wait=${BASH_REMATCH[1]}
    fi
    [ -n "$_wait" ] || return 0
    case $_wait in *m*) _minutes=${_wait%%m*}; _wait=${_wait#*m} ;; esac
    _wait=${_wait%s}
    printf '%s %s\n' "$_kind" "$((_minutes * 60 + ${_wait:-0}))" >> "$WORK/job.${JOB_I:-x}.wait"
  }
  slot_read || exit 0
  if [[ $_line =~ $SLOT_WAITING ]]; then
    slot_live
    printf '%s\n' "$_line"
    slot_read || exit 0
    if [[ $_line =~ $SLOT_GOT || $_line =~ $SLOT_TIMED_OUT ]]; then slot_live; fi
  elif [[ $_line =~ $SLOT_TIMED_OUT ]]; then
    slot_live
  fi
  printf '%s\n' "$_line"
  exec cat 4>&-
)

skip() { printf 'SKIP\t0\t%s\t%s\n' "$SECONDS" "$1" > "$JOB_STATUS"; exit 0; }

# in_dir runs the rest of the arguments in a directory; if a step there classified the job (a timeout), the whole
# job body stops, instead of continuing with the next step.
in_dir() {
  local _dir=$1
  shift
  ( cd "$_dir" && "$@" )
  local _rc=$?
  [ -s "$JOB_STATUS" ] && exit 0
  return "$_rc"
}

schedule() {  # schedule <name> <function> <arg>
  local _i=${#JOB_NAME[@]}
  JOB_NAME+=("$1"); JOB_FN+=("$2"); JOB_ARG+=("$3")
  JOB_LOG+=("$WORK/job.$_i.log"); JOB_STATUS+=("$WORK/job.$_i.status")
  echo "  queued: $1"
}

launch_all() {
  local _i
  for _i in "${!JOB_NAME[@]}"; do
    (
      # A TERM or HUP to the whole process group must not end the subshell (or the tee its steps start) before
      # on_signal has frozen the checks under it; on_signal KILLs them itself (see there).
      trap '' TERM HUP
      JOB_STATUS=${JOB_STATUS[$_i]}
      JOB_I=$_i
      SECONDS=0
      set +e
      "${JOB_FN[$_i]}" "${JOB_ARG[$_i]}"
      _rc=$?
      if [ ! -s "$JOB_STATUS" ]; then
        if [ "$_rc" -eq 0 ]; then
          printf 'PASS\t0\t%s\t%s\n' "$SECONDS" "${JOB_NOTE:-}" > "$JOB_STATUS"
        else
          printf 'FAIL\t%s\t%s\texit %s\n' "$_rc" "$SECONDS" "$_rc" > "$JOB_STATUS"
        fi
      fi
    ) 4>&2 > "${JOB_LOG[$_i]}" 2>&1 &  # fd 4: this script's stderr, for slot_lines' live lines
    JOB_PID+=($!)
  done
  for _i in "${!JOB_PID[@]}"; do wait "${JOB_PID[$_i]}" 2>/dev/null || true; done
}

# --- check bodies ---------------------------------------------------------

job_privacy() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/privacy_check.py" python3 scripts/privacy_check.py || return $?
}

job_web() {  # $1 = ci when npm ci must run first; WEB_MODE (full or related) and WEB_PLAN pick the tests
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  if [ "$1" = ci ]; then
    JOB_HINT="a stale web/node_modules can cause this; remove web/node_modules and rerun"
    in_dir web step "$TIMEOUT_NPM_CI" "web: npm ci" npm ci || return $?
    JOB_HINT=""
  fi
  # A node_modules from before the last lock bump runs old packages: stop with the list instead (#4776).
  in_dir web step "$TIMEOUT_OTHER" "web: node_modules matches package-lock.json" npm run check:deps || return $?
  in_dir web step "$TIMEOUT_OTHER" "web: tsc -b (typecheck)" npm run typecheck || return $?
  JOB_SLOT=web
  if [ "$WEB_MODE" = related ]; then
    # The plan's files after its first line, relative to web/: the changed ones and those that read files from disk.
    local _files=() _f
    while IFS= read -r _f; do [ -n "$_f" ] && _files+=("$_f"); done < <(tail -n +2 "$WEB_PLAN")
    in_dir web step "$TIMEOUT_WEB_TEST" "web: vitest related ($WEB_REASON)" \
      node scripts/process-group.mjs --timeout-ms "$((TIMEOUT_WEB_TEST * 1000))" -- \
      npx --no -- vitest related --run --passWithNoTests ${_files[@]+"${_files[@]}"}
  else
    in_dir web step "$TIMEOUT_WEB_TEST" "web: vitest run" npm test
  fi
}

job_integration() {  # $1 = package directory name under integrations/
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  local _dir="integrations/$1"
  [ -f "$_dir/package.json" ] || skip "integrations/$1 has no package.json"
  # Build the list in the package dir so unmatched globs never reach node as literal paths (bash 3.2 has no nullglob).
  local _tests=() _t
  while IFS= read -r _t; do _tests+=("$_t"); done \
    < <(cd "$_dir" 2>/dev/null && find test -maxdepth 1 \( -name '*.test.js' -o -name '*.test.mjs' \) -print 2>/dev/null)
  [ "${#_tests[@]}" -gt 0 ] || skip "integrations/$1 has no test/*.test.{js,mjs}"
  in_dir "$_dir" step "$TIMEOUT_OTHER" "integrations/$1: node --test" node --test "${_tests[@]}"
}

job_site() {
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  [ -f site/worker.test.mjs ] || skip "site/worker.test.mjs not found"
  step "$TIMEOUT_OTHER" "site: node --test worker.test.mjs" node --test site/worker.test.mjs
}

job_sdk_ts() {
  [ "$HAVE_BUN" -eq 1 ] || skip "bun not found in PATH (sdk/ts tests run on bun)"
  in_dir sdk/ts step "$TIMEOUT_OTHER" "sdk/ts: bun test" bun test
}

# Every Go group runs through the partitioned runner, inside the same timeout (#12765). For a ./<dir>/... that covers
# internal/assistant, whose serial run alone outgrew the budget, it runs that package's tests in go_partitions.py's
# partitions beside the other packages, and for ./... both partitioned packages' as test-server.sh does; any other
# group it execs exactly as given (internal/node/... too: its TestMain splits it). Without python3 (an --all run,
# already failing for it) go test runs directly.
GO_RUNNER=../scripts/test/go_test_partitioned.py  # from server/
job_go() {  # $1 = a package dir (internal/foo, cmd/bar, plugins/baz) or ALL for ./...
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  JOB_SLOT=go
  local _pattern _label _inner _go=(go)
  if [ "$1" = ALL ]; then _pattern=./...; _label="go test ./..."; else _pattern="./$1/..."; _label="go test ./$1/..."; fi
  _inner=$((TIMEOUT_GO_TEST - 10)); [ "$_inner" -lt 5 ] && _inner=5
  [ "$HAVE_PY" -eq 1 ] && _go=(python3 -B "$GO_RUNNER" go)
  in_dir server step "$TIMEOUT_GO_TEST" "$_label" "${_go[@]}" test -count=1 -timeout "${_inner}s" "$_pattern"
}

# The server's repo-wide guards: tests that read other packages' source, go list output, the migrations or sdk/go, or
# that pin what other packages render, so a change anywhere under server/ or sdk/go/ can fail them while go test on the
# touched packages passes (#1936 broke the lazyre guard on main that way). A server change tests only its own group's
# subtree (./internal/<x>/...), so these run for every one of them (#12842, #10615):
#   - lazyre, archguard, tzguard and plugins, and the hub's and the assistant's architecture tests, which read source,
#     go list output, the migrations or sdk/go;
#   - internal/i18n (its tests walk the whole module for hard-coded text and for keys its call sites use) and
#     internal/prompts (its tests walk the other packages for the code that offers each MCP tool, and read
#     docs/assistant/prompts.md);
#   - TestOneBuild (internal/update: no file builds differently with the release tags) and each program's link guard
#     (cmd/persona-hub, cmd/persona-manager, cmd/persona-node: go list -deps on every platform they ship for);
#   - the hub's and the assistant's cross-package contract tests: the prompt and MCP tool goldens (TestGolden.*), the
#     wire samples, the route scopes and extension route bounds, the prompt and instruction size budgets, and the
#     contracts of the model purposes, repository guidance and start paths. Their packages render, route or start what
#     other packages define, so a change there fails them.
# The list is curated, not every *guard*_test.go: internal/node's pathguard and writeguard tests and internal/hub's
# ownership_guard_test.go test their own package's behaviour. GO_GUARD_PKGS run whole; GO_GUARD_TESTS names the tests of
# GO_GUARD_TEST_PKGS that run, since their other tests are slow or need Postgres. go test -run silently matches nothing
# once a listed test is renamed, so scripts/test/test_check_fast_guards.py fails when an entry names no func Test... of
# those packages, or a listed package directory is gone. Every test runs with -count=1, as the other go steps do.
GO_GUARD_PKGS=(./internal/lazyre ./internal/archguard ./internal/tzguard ./plugins ./internal/i18n ./internal/prompts)
GO_GUARD_TEST_PKGS=(./internal/hub ./internal/assistant ./internal/update ./cmd/persona-hub ./cmd/persona-manager
  ./cmd/persona-node)
GO_GUARD_TESTS='^(TestHubAnswersOnlyItsOwnRoutes|TestHubDoesNotImportTheManager|TestHubCallsNoModel|TestHubStoreStaysSmall'\
'|TestTheHubOpensNoneOfTheManagersFiles|TestStoresTouchTheirOwnTables|TestSettingsKeysAreOneSidesOwn'\
'|TestTheLaunchersSealedFieldsAreReSealed|TestPlacesThatSealAreKnown'\
'|TestOneBuild|TestPersonaHubDoesNotLinkTheManager|TestPersonaManagerDoesNotLinkTheHub'\
'|TestPersonaNodeDoesNotLinkTheHubOrTheManager'\
'|TestGolden.*|TestNoWholeListMCPToolGoldens|TestWireSamples|TestRouteScopes|TestEveryExtensionRouteIsBounded'\
'|TestPromptSizeRatchet|TestSupervisionPolicySizeBudget|TestModelPurposesDescribeThemselves'\
'|TestRepositoryRetrievalGuidanceContract|TestEveryStartPathIsSupervisedOrOptedOut)$'

# job_go_guards runs both guard commands, the second one also when the first failed, so one run names every failure.
# In diff mode the first runs with CI_BASE_SHA set to the merge-base check-fast resolved, so archguard's
# TestDomainLayers holds each domain count to the lower of its baseline and its count there, as the sleep ratchet's
# --base does; --all is a full-tree run, held to the baseline only.
job_go_guards() {
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  JOB_SLOT=go
  local _inner=$((TIMEOUT_GO_TEST - 10)) _rc=0 _base=""
  [ "$_inner" -lt 5 ] && _inner=5
  [ "$ALL" -eq 1 ] || _base=$MB
  in_dir server step "$TIMEOUT_GO_TEST" "CI_BASE_SHA=$_base go test ${GO_GUARD_PKGS[*]}" \
    env "CI_BASE_SHA=$_base" go test -count=1 -timeout "${_inner}s" "${GO_GUARD_PKGS[@]}" || _rc=$?
  in_dir server step "$TIMEOUT_GO_TEST" "go test -run <guard tests> ${GO_GUARD_TEST_PKGS[*]}" \
    go test -count=1 -timeout "${_inner}s" -run "$GO_GUARD_TESTS" "${GO_GUARD_TEST_PKGS[@]}" || _rc=$?
  return "$_rc"
}

# job_go_fmt runs scripts/ci/gofmt.py on the changed .go files under server/ and sdk/go/ (GO_FMT_FILES), the trees CI's
# Go formatting step covers; --all checks both trees whole, as CI does (#12894). gofmt parses files and compiles nothing,
# so the job takes no go slot. A deleted file is in the diff too; only the files there now are checked.
job_go_fmt() {  # $1 = ALL for the whole of server/ and sdk/go/, else GO_FMT_FILES
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  have gofmt || skip "gofmt not found (it comes with Go)"
  local _files=() _f
  if [ "$1" = ALL ]; then
    for _f in server sdk/go; do [ -d "$_f" ] && _files+=("$_f"); done
    [ "${#_files[@]}" -gt 0 ] || skip "no server/ or sdk/go/ here"
  else
    for _f in ${GO_FMT_FILES[@]+"${GO_FMT_FILES[@]}"}; do [ -f "$_f" ] && _files+=("$_f"); done
    [ "${#_files[@]}" -gt 0 ] || skip "every changed .go file is deleted"
  fi
  step "$TIMEOUT_OTHER" "python3 scripts/ci/gofmt.py (${#_files[@]} path(s))" python3 -B scripts/ci/gofmt.py "${_files[@]}"
}

# job_go_vet vets the whole server module, then each changed package group for Windows (GO_VET_GROUPS: <top>/<name>, or
# . for the whole module; --all: the whole module), the second step also when the first failed, so one run names every
# finding (#12894). -ldflags= clears a link flag GOFLAGS may carry and keeps the rest, as scripts/ci/server.sh does. The
# Windows step names its environment through env, so its rerun line and --compare-main's rerun at the base keep it.
# A group with no package that builds for Windows (every file of scripts/fleetsoak is //go:build unix, of
# cmd/password-wasm js && wasm), or whose directory is gone, is left out of the Windows step: given only such groups,
# go vet says "no packages to vet" (or that the directory does not exist) and exits 1, where CI's ./... passes over
# them. With no group left, the step does not run. The PASS line names each group left out (JOB_NOTE).
job_go_vet() {  # $1 = ALL for the whole module on both platforms, else GO_VET_GROUPS
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  JOB_SLOT=go
  local _rc=0 _g _windows=() _none=()
  in_dir server step "$TIMEOUT_GO_TEST" "go vet ./..." go vet -ldflags= ./... || _rc=$?
  if [ "$1" = ALL ] || arr_has . ${GO_VET_GROUPS[@]+"${GO_VET_GROUPS[@]}"}; then
    _windows=(./...)
  else
    for _g in ${GO_VET_GROUPS[@]+"${GO_VET_GROUPS[@]}"}; do
      if [ ! -d "server/$_g" ] || no_windows_package "./$_g/..."; then
        _none+=("./$_g/...")
      else
        _windows+=("./$_g/...")
      fi
    done
  fi
  if [ "${#_none[@]}" -gt 0 ]; then
    JOB_NOTE="windows vet left out ${_none[*]}: no package there builds for windows"
    echo "$JOB_NOTE"
  fi
  if [ "${#_windows[@]}" -gt 0 ]; then
    in_dir server step "$TIMEOUT_GO_TEST" "GOOS=windows go vet ${_windows[*]}" \
      env GOOS=windows GOARCH=amd64 go vet -ldflags= "${_windows[@]}" || _rc=$?
  fi
  return "$_rc"
}

# no_windows_package <pattern> is true when go list, run in server/ for GOOS=windows, lists nothing for the pattern and
# says it matched no packages. Any other answer, an error or a timeout included, is false: the group is vetted, and vet
# reports what is wrong. go list compiles nothing, so it runs outside the go slot.
no_windows_package() {
  local _out
  _out=$(cd server && "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" env GOOS=windows GOARCH=amd64 go list "$1" 2>&1) || return 1
  [ "$_out" = "go: warning: \"$1\" matched no packages" ]
}

job_go_build() {
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  JOB_SLOT=go
  in_dir server step "$TIMEOUT_GO_TEST" "go build ./..." go build ./...
}

job_sdk_go() {  # sdk/go is its own Go module (its own go.mod), not part of server/.
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  JOB_SLOT=go
  local _inner=$((TIMEOUT_GO_TEST - 10)); [ "$_inner" -lt 5 ] && _inner=5
  in_dir sdk/go step "$TIMEOUT_GO_TEST" "sdk/go: go test ./..." go test -count=1 -timeout "${_inner}s" ./...
}

job_i18n() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/app/android_i18n_check.py" python3 scripts/app/android_i18n_check.py || return $?
  # The checkers' own unittests, run from scripts/app the way CI does (their fixture paths are relative).
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: test_android_i18n_check.py self-test" \
    python3 -B test_android_i18n_check.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: test_android_token_baseline.py self-test" \
    python3 -B test_android_token_baseline.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: test_android_motion_baseline.py self-test" \
    python3 -B test_android_motion_baseline.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: test_android_slot_order.py self-test" \
    python3 -B test_android_slot_order.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: android_slot_order.py" python3 -B android_slot_order.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: android_token_baseline.py" python3 -B android_token_baseline.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: android_motion_baseline.py" python3 -B android_motion_baseline.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: android_compose_budget.py" python3 -B android_compose_budget.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: android_nested_throwables.py" python3 -B android_nested_throwables.py
}

job_external_refs() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/test/test_external_service_refs.py" \
    python3 -B scripts/test/test_external_service_refs.py || return $?
}

job_sdk_bundle() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/test/test_sdk_bundle.py" \
    python3 -B scripts/test/test_sdk_bundle.py || return $?
}

job_scripts_test() {  # $1 = scripts/<dir>/test_<name>.py, run from its directory the way CI's Scripts tests job does
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  local _dir _module
  _dir=$(dirname "$1"); _module=$(basename "$1" .py)
  in_dir "$_dir" step "$TIMEOUT_OTHER" "$1" python3 -B -m unittest -q "$_module"
}

# job_scripts_all runs every SCRIPT_TESTS suite (--all) one after another in a single job, the way CI's Scripts tests
# job loops over them, instead of starting ~60 python processes at once. Each suite runs from its own directory under
# its own TIMEOUT_OTHER; one failing or slow suite does not stop the rest. The failed and timed-out files are listed
# last so the summary's log tail names them. Any failure makes the job a FAIL; only timeouts make it a TIMEOUT.
job_scripts_all() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  local _t _rc _failed=() _timed_out=() _out="$WORK/job.${JOB_I:-x}.steps.log" _from
  for _t in "${SCRIPT_TESTS[@]}"; do
    echo "+ $_t (timeout ${TIMEOUT_OTHER}s)"
    # Into the job's steps log too, so a failed suite is recorded like a step (record_step) and gets its rerun line.
    _from=0; [ -f "$_out" ] && _from=$(wc -c < "$_out")
    ( cd "$(dirname "$_t")" && "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" python3 -B -m unittest -q "$(basename "$_t" .py)" ) \
      4>&- 2>&1 | tee -a "$_out" 4>&-
    _rc=${PIPESTATUS[0]}
    case "$_rc" in
      0) ;;
      124|137) _timed_out+=("$_t")
        ( cd "$(dirname "$_t")" && record_step TIMEOUT "$_rc" "$_from" "$_t" python3 -B -m unittest -q "$(basename "$_t" .py)" ) ;;
      *) _failed+=("$_t")
        ( cd "$(dirname "$_t")" && record_step FAIL "$_rc" "$_from" "$_t" python3 -B -m unittest -q "$(basename "$_t" .py)" ) ;;
    esac
  done
  echo "${#SCRIPT_TESTS[@]} suite(s): ${#_failed[@]} failed, ${#_timed_out[@]} timed out"
  for _t in "${_timed_out[@]:-}"; do [ -n "$_t" ] && echo "timed out: $_t"; done
  for _t in "${_failed[@]:-}"; do [ -n "$_t" ] && echo "failed: $_t"; done
  [ "${#_failed[@]}" -eq 0 ] || return 1
  if [ "${#_timed_out[@]}" -gt 0 ]; then
    printf 'TIMEOUT\t124\t%s\t%s suite(s) timed out: %s\n' "$SECONDS" "${#_timed_out[@]}" "${_timed_out[*]}" > "$JOB_STATUS"
  fi
  return 0
}

job_pr_needs_issue() {
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  step "$TIMEOUT_OTHER" "node scripts/test/pr_needs_issue_test.mjs" node scripts/test/pr_needs_issue_test.mjs || return $?
}

# job_migration_freeze compares migrations already on the base with the index and the working tree. In diff mode the
# base is the merge-base check-fast already resolved (so --base agrees); --all resolves the default branch itself.
job_migration_freeze() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  if [ "$ALL" -eq 1 ]; then
    step "$TIMEOUT_OTHER" "python3 scripts/ci/migration_freeze.py" python3 scripts/ci/migration_freeze.py || return $?
  else
    step "$TIMEOUT_OTHER" "python3 scripts/ci/migration_freeze.py --base $MB" \
      python3 scripts/ci/migration_freeze.py --base "$MB" || return $?
  fi
}

# job_sleep_ratchet counts fixed time.Sleep calls in Go tests. In diff mode the base is the merge-base check-fast
# already resolved (so --base agrees), which also holds each changed file to its count there. --all is a full-tree run:
# counts are held to the baseline only, and the ratchet resolves the default branch itself for its never-raise check.
job_sleep_ratchet() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  if [ "$ALL" -eq 1 ]; then
    step "$TIMEOUT_OTHER" "python3 scripts/ci/sleep_ratchet.py" python3 scripts/ci/sleep_ratchet.py || return $?
  else
    step "$TIMEOUT_OTHER" "python3 scripts/ci/sleep_ratchet.py --base $MB" \
      python3 scripts/ci/sleep_ratchet.py --base "$MB" || return $?
  fi
}

# migration_freeze_wanted is true when a migration already on the base might have changed, or when the checker itself
# changed. --name-only lists a rename's destination only, so a file moved out of the directory shows up in the
# name-status scan (status, then the old path, then the new path).
migration_freeze_wanted() {
  [ "$ALL" -eq 1 ] && return 0
  local f status path newpath
  while IFS= read -r f; do
    case "$f" in
      server/internal/db/migrations/*|scripts/ci/migration_freeze.py) return 0 ;;
    esac
  done <<< "$CHANGED"
  [ -n "$CHANGED_FROM" ] && return 1  # a listed set of paths is the whole change: no renames to look for
  while IFS= read -r -d '' status; do
    IFS= read -r -d '' path || return 1
    case "$status" in
      R*|C*)
        IFS= read -r -d '' newpath || return 1
        case "$path" in server/internal/db/migrations/*) return 0 ;; esac
        path=$newpath
        ;;
    esac
    case "$path" in
      server/internal/db/migrations/*|scripts/ci/migration_freeze.py) return 0 ;;
    esac
  done < <(git diff --name-status --find-renames -z "$MB" HEAD
           git diff --name-status --find-renames -z
           git diff --cached --name-status --find-renames -z)
  return 1
}

# sleep_ratchet_wanted is true when a Go test's fixed sleeps might have changed, or when the checker or its baseline
# changed. --name-only lists a rename's destination only, so a test file moved out of server/ or sdk/go/ shows up in
# the name-status scan (status, then the old path, then the new path).
sleep_ratchet_wanted() {
  [ "$ALL" -eq 1 ] && return 0
  local f status path newpath
  while IFS= read -r f; do
    case "$f" in
      server/*_test.go|sdk/go/*_test.go|scripts/ci/sleep_ratchet.py|scripts/ci/sleep_baseline.json) return 0 ;;
    esac
  done <<< "$CHANGED"
  [ -n "$CHANGED_FROM" ] && return 1  # a listed set of paths is the whole change: no renames to look for
  while IFS= read -r -d '' status; do
    IFS= read -r -d '' path || return 1
    case "$status" in
      R*|C*)
        IFS= read -r -d '' newpath || return 1
        case "$path" in
          server/*_test.go|sdk/go/*_test.go) return 0 ;;
        esac
        path=$newpath
        ;;
    esac
    case "$path" in
      server/*_test.go|sdk/go/*_test.go|scripts/ci/sleep_ratchet.py|scripts/ci/sleep_baseline.json) return 0 ;;
    esac
  done < <(git diff --name-status --find-renames -z "$MB" HEAD
           git diff --name-status --find-renames -z
           git diff --cached --name-status --find-renames -z)
  return 1
}

job_extension_schema() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  # The codegen-current check reformats generated Go and needs gofmt.
  have gofmt || skip "gofmt not found (the schema codegen check needs Go installed)"
  step "$TIMEOUT_OTHER" "python3 scripts/test/test_extension_schema.py" python3 scripts/test/test_extension_schema.py || return $?
}

# --- plan -----------------------------------------------------------------

# plan_reset empties the plan, so --each can plan one listed path after another.
plan_reset() {
  CORE_FAIL=0
  WEB=0; SDKTS=0; ANDROID=0; PRNEEDS=0; EXTSCHEMA=0; SERVER_BUILD=0; APPLE=0; UNMAPPED=0; GO_GUARDS=0
  SITE=0; SDKGO=0; EXTREFS=0; SDKBUNDLE=0; WEB_INSTALL=0; DENO_ADAPTER=0
  INTEG=(); GO_GROUPS=(); UNMAPPED_FILES=(); SCRIPT_TESTS=(); TABLE_SUITES=(); GO_FMT_FILES=(); GO_VET_GROUPS=()
  CORE_MISSING=(); INFO=()
  JOB_NAME=(); JOB_FN=(); JOB_ARG=(); JOB_LOG=(); JOB_STATUS=()
}
plan_reset
CHANGED=""

core_missing() {  # --all treats a missing core toolchain as a failure; diff mode records a skip.
  if [ "$ALL" -eq 1 ]; then
    CORE_FAIL=1
    CORE_MISSING+=("$1")
  else
    INFO+=("SKIP  $1")
  fi
}

# resolve_base sets BASE_REF and MB, the merge-base the diff starts from (exit 2 when there is none).
# Without --base: the clone's default branch, from local refs only so the script works offline. origin/HEAD is set at
# clone time and can name a branch that is gone since, so it counts only if it resolves to a commit. Its symbolic
# target names the base in the output; an origin/HEAD that is a plain ref is used as it is. Full ref names: a local
# branch called origin/main would otherwise shadow the remote-tracking one.
resolve_base() {
  local _head _ref
  if [ -z "$BASE_REF" ]; then
    _head=$(git symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null) || _head=refs/remotes/origin/HEAD
    for _ref in "$_head" refs/remotes/origin/main refs/remotes/origin/master; do
      if git rev-parse --verify --quiet "${_ref}^{commit}" >/dev/null 2>&1; then
        BASE_REF=$_ref
        break
      fi
    done
    if [ -z "$BASE_REF" ]; then
      echo "check-fast: no default base: none of origin/HEAD, origin/main, origin/master is a commit here" >&2
      echo "  (git fetch origin, then git remote set-head origin --auto; or pass --base <ref>)" >&2
      exit 2
    fi
  elif ! git rev-parse --verify "${BASE_REF}^{commit}" >/dev/null 2>&1; then
    echo "check-fast: base ref '$BASE_REF' not found locally (fetch it, or pass --base <ref>)" >&2
    exit 2
  fi
  MB=$(git merge-base HEAD "$BASE_REF" 2>/dev/null) || { echo "check-fast: no merge-base with $BASE_REF" >&2; exit 2; }
}

# changed_files prints the paths changed since the merge-base: committed since then, plus staged/unstaged/untracked
# work, so the script works before a commit too.
changed_files() {
  {
    git diff --name-only --no-renames -z "$MB" HEAD
    git diff --name-only --no-renames -z HEAD
    git diff --cached --name-only --no-renames -z
    git ls-files --others --exclude-standard -z
  } | tr '\0' '\n' | sed '/^$/d' | sort -u
}

COMPARE_CHANGED=""  # the change's paths, for --compare-main's EDITED verdicts
if [ "$ALL" -eq 1 ]; then
  echo "check-fast: --all, running every local fast check (iOS/macOS and Postgres suites excluded)"
  # --all plans no diff, but --compare-main needs the merge base to rerun at and the change's files to attribute by.
  if [ "$COMPARE" -gt 0 ]; then
    resolve_base
    COMPARE_CHANGED=$(changed_files)
  fi
elif [ -n "$CHANGED_FROM" ]; then
  if [ "$CHANGED_FROM" = - ]; then CHANGED=$(sed '/^$/d' | sort -u); else CHANGED=$(sed '/^$/d' "$CHANGED_FROM" | sort -u); fi
  if [ -z "$CHANGED" ]; then
    echo "check-fast: no paths listed in $CHANGED_FROM; nothing to check."
    exit 0
  fi
  [ "$EACH" -eq 1 ] || echo "check-fast: $(printf '%s\n' "$CHANGED" | wc -l | tr -d ' ') listed file(s) from $CHANGED_FROM"
else
  resolve_base
  CHANGED=$(changed_files)
  COMPARE_CHANGED=$CHANGED
  if [ -z "$CHANGED" ]; then
    echo "check-fast: no changes since $(git rev-parse --short "$MB") ($BASE_REF); nothing to check."
    exit 0
  fi
  echo "check-fast: $(printf '%s\n' "$CHANGED" | wc -l | tr -d ' ') changed file(s) against $(git rev-parse --short "$MB") (merge-base with $BASE_REF)"
fi

# web_plan prints the web check's plan for $CHANGED (paths.py --web-plan, below). --each reads the next line of the
# plans paths.py --web-plan-each made for every listed path at once (fd 6): one scan of web/ takes about a second.
web_plan() {
  if [ "$EACH" -eq 1 ]; then
    local _line
    IFS= read -r _line <&6 || return 1
    printf '%s\n' "$_line"
    return 0
  fi
  printf '%s\n' "$CHANGED" | "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" python3 scripts/ci/paths.py --web-plan
}

# readers_of points READERS_FILE at server_readers' lines for the regex $1 (server_readers is below), kept per regex:
# under --each many listed paths share one (spec/fixtures/*). runtime_named's lines are read once per run too.
READER_CACHE=()
RUNTIME_NAMED=""; RUNTIME_NAMED_READ=0
SUITE_CACHE=(); SUITE_IS_SUITE=()
readers_of() {
  local _i=0
  while [ "$_i" -lt "${#READER_CACHE[@]}" ]; do
    if [ "${READER_CACHE[$_i]}" = "$1" ]; then READERS_FILE="$WORK/readers.$_i"; return 0; fi
    _i=$((_i + 1))
  done
  READERS_FILE="$WORK/readers.$_i"
  server_readers "$1" > "$READERS_FILE"
  READER_CACHE+=("$1")
}

# plan_notes prints what a plan leaves for the summary: the missing core toolchains and the SKIP / INFO lines.
plan_notes() {
  local _msg
  for _msg in "${CORE_MISSING[@]:-}"; do [ -n "$_msg" ] && echo "FAIL     core toolchain: $_msg"; done
  for _msg in "${INFO[@]:-}"; do [ -n "$_msg" ] && echo "$_msg"; done
  return 0
}

# plan_changes picks the checks for $CHANGED (every check with --all) and schedules them. Its body keeps the
# indentation it had at the top level.
plan_changes() {
# The CI lanes the change selects (paths.py --lanes-for, the selection CI and the PR watcher read), asked in the
# background while the plan is made; the lanes check-fast runs no part of are named at the end. --each reads the next
# line of the lanes paths.py --lanes-for-each listed for every path at once (fd 7).
LANES_PID=""; LANES_READ=0
if [ "$ALL" -eq 0 ] && [ "$HAVE_PY" -eq 1 ] && [ -f scripts/ci/paths.py ]; then
  if [ "$EACH" -eq 1 ]; then
    IFS= read -r _lanes <&7 && printf '%s\n' "$_lanes" > "$WORK/lanes.txt" && LANES_READ=1
  else
    printf '%s\n' "$CHANGED" | "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" python3 scripts/ci/paths.py --lanes-for \
      > "$WORK/lanes.txt" 2> /dev/null &
    LANES_PID=$!
  fi
fi
if [ "$ALL" -eq 1 ]; then
  WEB=1; SDKTS=1; ANDROID=1; PRNEEDS=1; EXTSCHEMA=1; SITE=1; SDKGO=1; EXTREFS=1; SDKBUNDLE=1
  for _d in integrations/*/; do
    if [ -f "$_d/package.json" ]; then
      _p=$(basename "$_d")
      arr_has "$_p" "${INTEG[@]:-}" || INTEG+=("$_p")
    fi
  done
else
  # The table's checks for each path (scripts/ci/check_fast_map.py), one line per path in $CHANGED's order on fd 8,
  # their tab-separated checks (check_fast_map.CHECKS). The suites among them are added after the ones found below
  # from the path's own name and its importers, in the table's order (TABLE_SUITES, one entry per path).
  while IFS= read -r f; do
    if ! IFS= read -r _line <&8; then
      echo "check-fast: python3 $CHECK_FAST_MAP listed fewer lines than paths; no plan made" >&2
      exit 2
    fi
    _listed=""
    _ifs=$IFS; IFS=$'\t'; set -f; _checks=($_line); set +f; IFS=$_ifs
    for _c in ${_checks[@]+"${_checks[@]}"}; do
      case "$_c" in
        web) WEB=1 ;;
        web-install) WEB_INSTALL=1 ;;
        site) SITE=1 ;;
        integration:*) _p=${_c#integration:}; arr_has "$_p" "${INTEG[@]:-}" || INTEG+=("$_p") ;;
        sdk-ts) SDKTS=1 ;;
        deno-adapter) DENO_ADAPTER=1 ;;
        go-package:*)
          _g=${_c#go-package:}
          # A diff includes the old path of a moved or deleted package. --changed-from also plans hypothetical paths.
          if [ -d "server/$_g" ] || [ -n "$CHANGED_FROM" ]; then
            arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g")
          fi ;;
        server-build) SERVER_BUILD=1 ;;
        go-guards) GO_GUARDS=1 ;;
        go-vet:*) _g=${_c#go-vet:}; arr_has "$_g" "${GO_VET_GROUPS[@]:-}" || GO_VET_GROUPS+=("$_g") ;;
        go-fmt) GO_FMT_FILES+=("$f") ;;
        sdk-go) SDKGO=1 ;;
        android) ANDROID=1 ;;
        pr-needs-issue) PRNEEDS=1 ;;
        extension-schema) EXTSCHEMA=1 ;;
        sdk-bundle) SDKBUNDLE=1 ;;
        external-refs) EXTREFS=1 ;;
        apple) APPLE=1 ;;
        unmapped) UNMAPPED=1; UNMAPPED_FILES+=("$f") ;;
        suite:*) _listed="$_listed${_c#suite:}"$'\t' ;;
        *) echo "check-fast: python3 $CHECK_FAST_MAP named a check this script does not know: $_c; no plan made" >&2
           exit 2 ;;
      esac
    done
    TABLE_SUITES+=("$_listed")
  done <<< "$CHANGED"
fi

# The web check's plan (#10619): scripts/ci/paths.py scans web/src and the web tests for the files they read, and
# prints "none", "full" or "related" with a reason, then the files vitest related starts from. Without it (no
# scripts/ci/paths.py, or the scan failed), a change under web/ runs the whole suite and files outside web/ do not run
# the web check.
WEB_MODE=full; WEB_REASON=""; WEB_PLAN="$WORK/web-plan.txt"
if [ "$ALL" -eq 0 ]; then
  if [ "$HAVE_PY" -eq 1 ] && [ -f scripts/ci/paths.py ]; then
    if web_plan > "$WEB_PLAN" 2> "$WORK/web-plan.err"; then
      read -r WEB_MODE WEB_REASON < "$WEB_PLAN"
      case "$WEB_MODE" in
        full|related) WEB=1 ;;
        *) WEB_MODE=full ;;  # none: nothing the web check reads changed
      esac
    else
      WEB_MODE=full
      INFO+=("INFO  web plan: python3 scripts/ci/paths.py --web-plan failed ($(tail -n 1 "$WORK/web-plan.err" 2>/dev/null)): a web/ change runs the whole suite, and files outside web/ do not run the web check")
    fi
  elif [ "$WEB" -eq 1 ]; then
    INFO+=("INFO  web plan: python3 or scripts/ci/paths.py not found: the whole web suite runs, and files outside web/ do not run the web check")
  fi
  if [ "$WEB" -eq 1 ]; then
    if [ "$WEB_MODE" = related ]; then
      echo "check-fast: web tests: vitest related, $WEB_REASON"
    else
      echo "check-fast: web tests: the whole suite${WEB_REASON:+, $WEB_REASON}"
    fi
  fi
fi

# search <what> <output file> <git grep arguments...> runs git grep into the file; no match leaves it empty. Any other
# failure stops the plan (exit 2): the checks the search would have found are not run otherwise, and nothing would
# say so. One alternation of every changed client path once grew past what git grep compiles on a large apple/ diff,
# and every Go reader of those files was dropped without a word (#11250).
search() {
  local _what=$1 _out=$2 _rc
  shift 2
  git grep "$@" > "$_out" 2> "$WORK/search.err"
  _rc=$?
  [ "$_rc" -le 1 ] && return 0
  echo "check-fast: searching $_what failed (git grep exit $_rc): $(head -c 300 "$WORK/search.err" | tr '\n' ' ')" >&2
  echo "check-fast: no plan made; the checks that read the changed files cannot be picked" >&2
  exit 2
}

# server_readers prints the package directory (relative to server/) of each server _test.go that matches the extended regex $1
# outside a // comment line. Finding them at run time keeps a new reader mapped without an edit here.
server_readers() {
  search "the server tests for $1" "$WORK/search.out" -nE "$1" -- 'server/*_test.go'
  LC_ALL=C grep -vE '^[^:]*:[0-9]+:[[:space:]]*//' "$WORK/search.out" | cut -d: -f1 | sort -u \
    | while IFS= read -r _file; do _dir=$(dirname "$_file"); printf '%s\n' "${_dir#server/}"; done | sort -u
}

# client_named writes CLIENT_NAMED once a run: "<path><tab><package dir>" for each whole "apple/…" or "android/…"
# string in a server _test.go outside a // comment line, the text between its quotes, so a longer string that only
# mentions a file ("apple/Shared/Models.swift: Fold") names that longer string. A plan matches the changed client paths
# against it, so a diff of thousands of client files still costs one search.
CLIENT_NAMED=""; CLIENT_NAMED_READ=0
client_named() {
  [ "$CLIENT_NAMED_READ" -eq 1 ] && return 0
  CLIENT_NAMED="$WORK/client-named.txt"
  search "the server tests for the client sources they name" "$WORK/client-grep.txt" \
    -nE '"(apple|android)/[^"]*"' -- 'server/*_test.go'
  LC_ALL=C grep -vE '^[^:]*:[0-9]+:[[:space:]]*//' "$WORK/client-grep.txt" | LC_ALL=C awk '
    {
      file = $0; sub(/:.*/, "", file)
      dir = file; sub(/\/[^\/]*$/, "", dir); sub(/^server\//, "", dir)
      text = $0; sub(/^[^:]*:[0-9]+:/, "", text)
      while (match(text, /"(apple|android)\/[^"]*"/)) {
        print substr(text, RSTART + 1, RLENGTH - 2) "\t" dir
        text = substr(text, RSTART + RLENGTH)
      }
    }' | LC_ALL=C sort -u > "$CLIENT_NAMED"
  CLIENT_NAMED_READ=1
}

# Repository inputs that server tests read (the extra paths in server-test.yml's filter): a change to one also runs go
# test on the packages that read it. Each regex matches the input as a path string or as filepath.Join elements.
if [ "$ALL" -eq 0 ]; then
  if [ "$EACH" -eq 1 ] && [ -f "$WORK/reader-plans.bin" ]; then
    # The --each reader index was built from this frozen path list before any
    # plan was made. Each record still belongs to exactly one path.
    IFS= read -r -d '' _reader_path <&9 &&
      IFS= read -r -d '' _server_named <&9 &&
      IFS= read -r -d '' _runtime_named <&9 || {
      echo "check-fast: the per-path reader index ended before $_each; no plan made" >&2
      exit 2
    }
    if [ "$_reader_path" != "$CHANGED" ]; then
      echo "check-fast: the per-path reader index names $_reader_path instead of $CHANGED; no plan made" >&2
      exit 2
    fi
    while IFS= read -r _g; do
      [ -n "$_g" ] && { arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g"); }
    done <<< "$_server_named"
    while IFS= read -r _pkg; do
      [ -n "$_pkg" ] || continue
      case "$_pkg" in
        sdk/ts) SDKTS=1 ;;
        *) _pkg=${_pkg#integrations/}; arr_has "$_pkg" "${INTEG[@]:-}" || INTEG+=("$_pkg") ;;
      esac
    done <<< "$_runtime_named"
  else
  READER_RES=(); _client=0
  while IFS= read -r f; do
    _re=""
    case "$f" in
      spec/fixtures/*) _re='spec/fixtures|"spec", *"fixtures"' ;;
      # Each schema family (extension, wire, configuration, ...): internal/configuration reads schema/configuration.
      schema/*/*) _s=${f#schema/}; _s=${_s%%/*}; _re="schema/$_s|\"schema\", *\"$_s\"" ;;
      integrations/*/*) _re='integrations/|"integrations"' ;;
      scripts/test/server_test_env.py) _re='server_test_env\.py' ;;
      docs/*) _re=$(printf '%s' "$f" | sed 's/[][\.^$*+?(){}|]/\\&/g') ;;  # docs/example-guide.md -> docs/example-guide\.md
      apple/*|android/*) _client=1 ;;  # below
    esac
    [ -n "$_re" ] && { arr_has "$_re" "${READER_RES[@]:-}" || READER_RES+=("$_re"); }
  done <<< "$CHANGED"
  for _re in "${READER_RES[@]:-}"; do
    [ -n "$_re" ] || continue
    readers_of "$_re"
    while IFS= read -r _g; do
      [ -n "$_g" ] && { arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g"); }
    done < "$READERS_FILE"
  done
  # Client sources the Go contract tests read (internal/proto reads "apple/Shared/Models.swift"): only a test that names
  # the whole path in quotes counts, not a longer string that mentions it (client_named).
  if [ "$_client" -eq 1 ]; then
    client_named
    printf '%s\n' "$CHANGED" | LC_ALL=C grep -E '^(apple|android)/' > "$WORK/client-changed.txt"
    while IFS= read -r _g; do
      [ -n "$_g" ] && { arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g"); }
    done < <(LC_ALL=C awk -F '\t' 'NR == FNR { changed[$0] = 1; next } $1 in changed { print $2 }' \
               "$WORK/client-changed.txt" "$CLIENT_NAMED" | LC_ALL=C sort -u)
  fi
  fi
fi

# runtime_named prints "<package> <path>" for each shared fixture or schema path (spec/..., schema/...) a file of an
# integrations/<pkg> package or of sdk/ts names outside a comment line, as a path relative to itself
# (../../../spec/fixtures/input-clip/cases.json, ../../../schema/extension/examples/). CI's runtimes lane runs for all
# of spec/ and schema/; check-fast runs the packages that read the changed path.
runtime_named() {
  search "the runtime packages for the fixtures and schemas they name" "$WORK/runtime-grep.txt" \
    -nE '(spec|schema)/[A-Za-z0-9_.-]' -- 'integrations/*' 'sdk/ts/*'
  LC_ALL=C grep -vE '^[^:]*:[0-9]+:[[:space:]]*(//|\*|#)' "$WORK/runtime-grep.txt" \
    | while IFS= read -r _line; do
        case "$_line" in
          integrations/*/*) _pkg=${_line#integrations/}; _pkg="integrations/${_pkg%%/*}" ;;
          sdk/ts/*) _pkg=sdk/ts ;;
          *) continue ;;
        esac
        printf '%s\n' "${_line#*:*:}" | grep -oE '(spec|schema)/[A-Za-z0-9_./-]*' | sed "s|^|$_pkg |"
      done | sort -u
}

# A changed spec/ or schema/ path also runs the runtime packages that read it: the path itself, or a directory it is in.
if [ "$ALL" -eq 0 ] && [ "$EACH" -eq 0 ] && printf '%s\n' "$CHANGED" | grep -qE '^(spec|schema)/'; then
  # Not in $( ): a failed search must stop this shell, not a subshell.
  if [ "$RUNTIME_NAMED_READ" -eq 0 ]; then
    runtime_named > "$WORK/runtime-named.txt"
    RUNTIME_NAMED=$(cat "$WORK/runtime-named.txt"); RUNTIME_NAMED_READ=1
  fi
  while IFS= read -r f; do
    case "$f" in spec/*|schema/*) ;; *) continue ;; esac
    while read -r _pkg _path; do
      [ -n "$_path" ] || continue
      case "$f" in "$_path"|"${_path%/}"/*) ;; *) continue ;; esac
      case "$_pkg" in
        sdk/ts) SDKTS=1 ;;
        *) _pkg=${_pkg#integrations/}; arr_has "$_pkg" "${INTEG[@]:-}" || INTEG+=("$_pkg") ;;
      esac
    done <<< "$RUNTIME_NAMED"
  done <<< "$CHANGED"
fi

# The trusted Deno adapter (server/internal/extension/denohost/adapter.mjs) is generated from adapter.ts, which bundles
# sdk/ts/src: a change there (the table's deno-adapter) also runs denohost's tests, whose
# TestTrustedAdapterMatchesOfficialSDK fails while the committed adapter is stale (#4221; scripts/gen/deno_adapter.sh
# regenerates it). A few seconds; it needs bun.
if [ "$ALL" -eq 0 ] && [ "$DENO_ADAPTER" -eq 1 ]; then
  if [ "$HAVE_BUN" -eq 0 ]; then
    INFO+=("SKIP  trusted Deno adapter (go test ./internal/extension/denohost/...): bun not found in PATH")
  elif ! arr_has internal/extension "${GO_GROUPS[@]:-}"; then  # ./internal/extension/... already covers it
    arr_has internal/extension/denohost "${GO_GROUPS[@]:-}" || GO_GROUPS+=(internal/extension/denohost)
  fi
fi

# script_test_add adds a scripts/<dir>/test_*.py that exists (a deleted one is not run); for one a dedicated job runs
# (refs, bundle, extension schema, the android checkers) it schedules that job instead. A file that never mentions
# unittest is a SKIP line instead, the rule of is_suite in scripts/ci/python-tests.py: python -m unittest runs no tests
# in such a verification script and exits 5 ("NO TESTS RAN") on Python 3.12 and later.
script_test_add() {
  # job_i18n runs four of the scripts/app/test_android_* suites; the others run here (#11026).
  case "$1" in
    scripts/test/test_external_service_refs.py) EXTREFS=1; return 0 ;;
    scripts/test/test_sdk_bundle.py) SDKBUNDLE=1; return 0 ;;
    scripts/test/test_extension_schema.py) EXTSCHEMA=1; return 0 ;;
    scripts/app/test_android_i18n_check.py|scripts/app/test_android_token_baseline.py|\
    scripts/app/test_android_motion_baseline.py|scripts/app/test_android_slot_order.py) ANDROID=1; return 0 ;;
  esac
  # Already added: every file of a large diff adds the same few suites again, and the grep below is a process each.
  arr_has "$1" "${SCRIPT_TESTS[@]:-}" && return 0
  [ -f "$1" ] || return 0
  # A path's suites are reset between --each probes, but its unittest
  # classification is not. --each is a read-only plan, so retain that observation
  # rather than spawning grep hundreds of times for the same tracked files.
  if [ "$EACH" -eq 1 ]; then
    local _i=0
    while [ "$_i" -lt "${#SUITE_CACHE[@]}" ]; do
      if [ "${SUITE_CACHE[$_i]}" = "$1" ]; then
        if [ "${SUITE_IS_SUITE[$_i]}" -eq 1 ]; then
          SCRIPT_TESTS+=("$1")
        else
          arr_has "SKIP  $1 (not a unittest suite)" "${INFO[@]:-}" || INFO+=("SKIP  $1 (not a unittest suite)")
        fi
        return 0
      fi
      _i=$((_i + 1))
    done
    local _is_suite=0
    if grep -q unittest "$1"; then _is_suite=1; fi
    SUITE_CACHE+=("$1")
    SUITE_IS_SUITE+=("$_is_suite")
    if [ "$_is_suite" -eq 0 ]; then
      arr_has "SKIP  $1 (not a unittest suite)" "${INFO[@]:-}" || INFO+=("SKIP  $1 (not a unittest suite)")
      return 0
    fi
  elif ! grep -q unittest "$1"; then
    arr_has "SKIP  $1 (not a unittest suite)" "${INFO[@]:-}" || INFO+=("SKIP  $1 (not a unittest suite)")
    return 0
  fi
  SCRIPT_TESTS+=("$1")
}

# The scripts/ unittests a change needs (#3968): each changed scripts/<dir>/test_*.py, and for each changed script
# <name>.py / <name>.sh anywhere under scripts/, every scripts/<dir>/test_<name>.py, and test_<name>_sh.py for a .sh
# (CI's Scripts tests job runs scripts/**/test_*.py; sim-boot.sh is tested by test_sim_boot.py, db.sh by test_db_sh.py).
# Then each path adds the suites the table maps it to (scripts/ci/check_fast_map.py): the ones guarding docs, markdown,
# apple/, deploy/docker/ and the root Dockerfile, and the ones that run or read a script by its path.
# --all runs every scripts/**/test_*.py suite, the same find as CI's Scripts tests job, minus the few script_test_add
# skips because a dedicated check above runs them; renaming or dropping one of those checks must update that skip list.
# test_check_fast.py compares the --all selection with python-tests.py's.

# script_importers prints the scripts/**/test_*.py suites that import the module scripts/<dir>/<name>.py ($1) with
# "import <name>" or "from <name> import" at the start of a line: from their own directory, or from one they name (a
# suite puts scripts/<dir> on sys.path to import from it). Found at run time, so a new importer needs no edit here;
# scripts/ci/affected_drift.py works the same suites out on its own and compares (#11026).
script_importers() {
  local _dir _mod _t
  _dir=${1%/*}; _mod=${1##*/}; _mod=${_mod%.py}
  case "$_mod" in ""|*[!A-Za-z0-9_]*) return 0 ;; esac  # android-shards.py cannot be imported by name
  search "the scripts suites that import $1" "$WORK/importers-grep.txt" \
    -lE "^[[:space:]]*(from[[:space:]]+$_mod[[:space:]]+import|import[[:space:]]+([A-Za-z0-9_]+[[:space:]]*,[[:space:]]*)*$_mod([[:space:],]|\$))" \
    -- 'scripts/*test_*.py'
  while IFS= read -r _t; do
    case "${_t##*/}" in test_*.py) ;; *) continue ;; esac
    [ "$_t" != "$1" ] || continue
    if [ "${_t%/*}" = "$_dir" ] || grep -qF "$_dir" "$_t" || grep -qE "/[[:space:]]*\"${_dir##*/}\"" "$_t"; then
      printf '%s\n' "$_t"
    fi
  done < "$WORK/importers-grep.txt"
}

if [ "$ALL" -eq 1 ]; then
  while IFS= read -r f; do
    script_test_add "$f"
  done < <(find scripts -name 'test_*.py' -not -path '*/node_modules/*' | sort)
else
  _n=0
  while IFS= read -r f; do
    case "$f" in
      scripts/*/test_*.py) script_test_add "$f" ;;
      scripts/*.py|scripts/*.sh)
        _name=${f##*/}; _name=${_name%.*}
        _name=$(printf '%s' "$_name" | tr '-' '_')
        for _t in scripts/*/"test_$_name.py"; do script_test_add "$_t"; done
        case "$f" in *.sh) for _t in scripts/*/"test_${_name}_sh.py"; do script_test_add "$_t"; done ;; esac ;;
    esac
    # The suites that import a changed module, wherever they are (script_importers).
    # Through a file, not < <( ): a failed search must stop this shell, not a subshell.
    case "$f" in
      scripts/*.py)
        script_importers "$f" > "$WORK/importers.txt"
        while IFS= read -r _t; do script_test_add "$_t"; done < "$WORK/importers.txt" ;;
    esac
    # The suites the table maps the path to, in its order (TABLE_SUITES from the first loop): the ones that guard
    # files outside scripts/, and the ones that run or read a script by its path rather than importing it.
    _ifs=$IFS; IFS=$'\t'; set -f; _suites=(${TABLE_SUITES[$_n]}); set +f; IFS=$_ifs
    for _t in ${_suites[@]+"${_suites[@]}"}; do script_test_add "$_t"; done
    _n=$((_n + 1))
  done <<< "$CHANGED"
fi

[ "$DRY_RUN" -eq 1 ] || echo "check-fast: logs in $WORK"

# All jobs, privacy included, launch together in launch_all below; privacy is scheduled first so it is the first
# line in the summary.
if [ "$HAVE_PY" -eq 1 ]; then
  schedule "privacy scan" job_privacy ""
else
  core_missing "python3 not found; privacy scan skipped"
fi

if [ "$WEB" -eq 1 ]; then
  if node_ok || [ "$ALL" -eq 0 ]; then
    WEB_CI=noci
    if [ ! -d web/node_modules ]; then
      WEB_CI=ci
    elif [ "$ALL" -eq 0 ] && [ "$WEB_INSTALL" -eq 1 ]; then  # a changed manifest (the table's web-install)
      WEB_CI=ci
    fi
    if node_ok; then schedule "web" job_web "$WEB_CI"; else INFO+=("SKIP  web: node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"); fi
  fi
  if [ "$ALL" -eq 1 ] && ! node_ok; then core_missing "node >= 22 not found (found: $(node -v 2>/dev/null || echo none)); web and integration checks skipped"; fi
fi

if [ "${#INTEG[@]}" -gt 0 ]; then
  if node_ok; then
    for _p in "${INTEG[@]}"; do schedule "integrations/$_p" job_integration "$_p"; done
  else
    core_missing "node >= 22 not found (found: $(node -v 2>/dev/null || echo none)); integration tests (${INTEG[*]}) skipped"
  fi
fi

[ "$SDKTS" -eq 1 ] && schedule "sdk/ts (bun)" job_sdk_ts ""
if [ "$SITE" -eq 1 ]; then
  if node_ok; then
    schedule "site (node --test)" job_site ""
  elif [ "$ALL" -eq 1 ]; then
    core_missing "node >= 22 not found; site check skipped"
  else
    INFO+=("SKIP  site: node >= 22 not found (found: $(node -v 2>/dev/null || echo none))")
  fi
fi
[ "$EXTREFS" -eq 1 ] && schedule "external service refs" job_external_refs ""
if [ "${#SCRIPT_TESTS[@]}" -gt 0 ]; then
  if [ "$HAVE_PY" -eq 1 ]; then
    if [ "$ALL" -eq 1 ]; then
      schedule "scripts unittest suites (${#SCRIPT_TESTS[@]})" job_scripts_all ""
    else
      for _t in "${SCRIPT_TESTS[@]}"; do schedule "$_t" job_scripts_test "$_t"; done
    fi
  elif [ "$ALL" -eq 1 ]; then
    core_missing "python3 not found; scripts unittest suites skipped"
  else
    INFO+=("SKIP  scripts unittests (${SCRIPT_TESTS[*]}): python3 not found")
  fi
fi
[ "$SDKBUNDLE" -eq 1 ] && schedule "sdk bundle snapshot" job_sdk_bundle ""
if [ "$SDKGO" -eq 1 ]; then
  if [ "$HAVE_GO" -eq 1 ]; then
    schedule "sdk/go go test" job_sdk_go ""
  elif [ "$ALL" -eq 0 ]; then
    # In --all mode the missing-go failure is already reported by the server core_missing line below.
    INFO+=("SKIP  sdk/go tests: go not found")
  fi
fi

if [ "$ALL" -eq 1 ]; then
  if [ "$HAVE_GO" -eq 1 ]; then
    schedule "server go test" job_go ALL
    schedule "server go vet" job_go_vet ALL
  else core_missing "go not found; server and sdk/go checks skipped"; fi
  # gofmt does not depend on go being found: without gofmt (or python3) the job is a SKIP line naming it.
  schedule "gofmt (server, sdk/go)" job_go_fmt ALL
elif [ "$NO_SERVER_GO" -eq 1 ]; then
  if [ "${#GO_GROUPS[@]}" -gt 0 ] || [ "$SERVER_BUILD" -eq 1 ] || [ "$GO_GUARDS" -eq 1 ] || [ "${#GO_VET_GROUPS[@]}" -gt 0 ]; then
    INFO+=("INFO  server go test, build, guards and vet: left to the caller (--no-server-go)")
  fi
  if [ "${#GO_FMT_FILES[@]}" -gt 0 ]; then schedule "gofmt (changed files)" job_go_fmt ""; fi
else
  if [ "${#GO_GROUPS[@]}" -gt 0 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then
      for _g in "${GO_GROUPS[@]}"; do schedule "server go test ./$_g/..." job_go "$_g"; done
    else
      INFO+=("SKIP  server go tests (${GO_GROUPS[*]}): go not found")
    fi
  fi
  if [ "$SERVER_BUILD" -eq 1 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then schedule "server go build ./..." job_go_build ""; else INFO+=("SKIP  server go build: go not found"); fi
  fi
  if [ "$GO_GUARDS" -eq 1 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then schedule "server repo-wide guards" job_go_guards ""; else INFO+=("SKIP  server repo-wide guards: go not found"); fi
  fi
  if [ "${#GO_VET_GROUPS[@]}" -gt 0 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then schedule "server go vet" job_go_vet ""; else INFO+=("SKIP  server go vet: go not found"); fi
  fi
  # As under --all, gofmt does not depend on go being found; the job itself skips without gofmt.
  if [ "${#GO_FMT_FILES[@]}" -gt 0 ]; then schedule "gofmt (changed files)" job_go_fmt ""; fi
fi

if [ "$ANDROID" -eq 1 ]; then
  if [ "$HAVE_PY" -eq 1 ]; then schedule "android i18n" job_i18n ""; else core_missing "python3 not found; android i18n check skipped"; fi
fi
if [ "$PRNEEDS" -eq 1 ]; then
  if node_ok; then schedule "pr-needs-issue workflow test" job_pr_needs_issue ""; else core_missing "node >= 22 not found; pr-needs-issue test skipped"; fi
fi
if [ "$EXTSCHEMA" -eq 1 ]; then
  if [ "$HAVE_PY" -eq 1 ]; then schedule "extension schema conformance" job_extension_schema ""; else core_missing "python3 not found; extension schema test skipped"; fi
fi

if [ -f scripts/ci/migration_freeze.py ] && migration_freeze_wanted; then
  if [ "$HAVE_PY" -eq 1 ]; then
    schedule "frozen migrations" job_migration_freeze ""
  elif [ "$ALL" -eq 1 ]; then
    core_missing "python3 not found; frozen migration check skipped"
  else
    INFO+=("SKIP  frozen migrations: python3 not found")
  fi
fi

if [ -f scripts/ci/sleep_ratchet.py ] && sleep_ratchet_wanted; then
  if [ "$HAVE_PY" -eq 1 ]; then
    schedule "fixed sleeps" job_sleep_ratchet ""
  elif [ "$ALL" -eq 1 ]; then
    core_missing "python3 not found; fixed sleep check skipped"
  else
    INFO+=("SKIP  fixed sleeps: python3 not found")
  fi
fi

if [ "$APPLE" -eq 1 ]; then
  INFO+=("SKIP  apple/** builds and tests: iOS/macOS checks need a Mac — scripts/app/sim.sh, PersonaKit swift test (docs/operations/testing.md)")
fi
if [ "$UNMAPPED" -eq 1 ]; then
  INFO+=("INFO  no fast check maps to: ${UNMAPPED_FILES[*]} (privacy scan still ran; full suites in docs/operations/testing.md)")
fi
if [ "$LANES_READ" -eq 1 ] || { [ -n "$LANES_PID" ] && wait "$LANES_PID"; }; then
  _not_run=""
  for _lane in $(cat "$WORK/lanes.txt"); do
    case "$_lane" in
      scripts) continue ;;  # CI's "**": the privacy scan and the scripts suites above are its fast part
      web) [ "$WEB" -eq 1 ] && continue ;;
      server)
        { [ "${#GO_GROUPS[@]}" -gt 0 ] || [ "$SERVER_BUILD" -eq 1 ] || [ "$GO_GUARDS" -eq 1 ] || [ "$SDKGO" -eq 1 ] ||
          [ "${#GO_VET_GROUPS[@]}" -gt 0 ] || [ "${#GO_FMT_FILES[@]}" -gt 0 ]; } && continue ;;
      runtimes) { [ "${#INTEG[@]}" -gt 0 ] || [ "$SDKTS" -eq 1 ]; } && continue ;;
      apple) [ "$APPLE" -eq 1 ] && continue ;;  # its own SKIP line names it
    esac
    _not_run="${_not_run:+$_not_run, }$_lane"
  done
  [ -n "$_not_run" ] && INFO+=("INFO  CI lanes this change selects that check-fast does not run: $_not_run (bash scripts/ci/<lane>.sh)")
fi
}

# The table lists the checks of every path at once (one line per path, in $CHANGED's order), and plan_changes reads
# them in step with $CHANGED on fd 8; --each reads one line per listed path. --all does not read it. Without python3 or
# the table no plan can be made, so the run stops, as it does when a search fails (search, above).
if [ "$ALL" -eq 0 ]; then
  if [ "$HAVE_PY" -eq 0 ] || [ ! -f "$CHECK_FAST_MAP" ]; then
    echo "check-fast: picking the checks for a change needs python3 and $CHECK_FAST_MAP; no plan made" >&2
    exit 2
  fi
  if ! printf '%s\n' "$CHANGED" | "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" python3 "$CHECK_FAST_MAP" \
      > "$WORK/table.txt" 2> "$WORK/table.err"; then
    echo "check-fast: python3 $CHECK_FAST_MAP failed ($(tail -n 1 "$WORK/table.err" 2>/dev/null)); no plan made" >&2
    exit 2
  fi
  exec 8< "$WORK/table.txt"
fi

# --each: plan every listed path on its own. paths.py plans the web check of all of them in one scan (web_plan),
# lists the CI lanes of each in one run (plan_changes), and affected_drift.py's --reader-plan-each indexes the
# tracked Go/runtime reader matches once. The per-path records below still feed the original plan_changes for each
# path separately; ordinary diff execution retains the dynamic git-grep searches above.
if [ "$EACH" -eq 1 ]; then
  printf '%s\n' "$CHANGED" > "$WORK/each.txt"
  if [ "$HAVE_PY" -eq 1 ] && [ -f scripts/ci/paths.py ]; then
    if [ ! -f scripts/ci/affected_drift.py ]; then
      echo "check-fast: --each needs scripts/ci/affected_drift.py for its indexed readers" >&2
      exit 2
    fi
    "$TIMEOUT_BIN" -k 5s 900 python3 scripts/ci/paths.py --web-plan-each < "$WORK/each.txt" > "$WORK/web-plans.txt" \
      || { echo "check-fast: python3 scripts/ci/paths.py --web-plan-each failed" >&2; exit 2; }
    "$TIMEOUT_BIN" -k 5s 900 python3 scripts/ci/paths.py --lanes-for-each < "$WORK/each.txt" > "$WORK/lanes-each.txt" \
      || { echo "check-fast: python3 scripts/ci/paths.py --lanes-for-each failed" >&2; exit 2; }
    "$TIMEOUT_BIN" -k 5s 900 python3 scripts/ci/affected_drift.py --reader-plan-each < "$WORK/each.txt" \
      > "$WORK/reader-plans.bin" || { echo "check-fast: the per-path reader index failed" >&2; exit 2; }
    exec 6< "$WORK/web-plans.txt" 7< "$WORK/lanes-each.txt" 9< "$WORK/reader-plans.bin"
  fi
  while IFS= read -r _each <&5; do
    plan_reset
    CHANGED=$_each
    echo "== $_each"
    plan_changes
    plan_notes
  done 5< "$WORK/each.txt"
  exit 0
fi

plan_changes
[ "$ALL" -eq 1 ] || exec 8<&-  # the table's lines are read: the checks do not need the file open
if [ "$DRY_RUN" -eq 1 ]; then
  echo "check-fast: dry run, ${#JOB_NAME[@]} check(s) planned; nothing ran."
  plan_notes
  exit 0
fi

if [ ${#JOB_NAME[@]} -eq 0 ] && [ "$CORE_FAIL" -eq 0 ]; then
  echo "check-fast: no related checks to run."
  for _line in "${INFO[@]:-}"; do [ -n "$_line" ] && echo "$_line"; done
  exit 0
fi

echo "check-fast: ${#JOB_NAME[@]} check(s) running in parallel..."
launch_all

# --- summary --------------------------------------------------------------

# rerun_lines <job> prints, for each step of the job that failed or timed out, the command that reruns just what
# failed in it: the failing Go tests of each package, the failing unittest ids, or the step's own command
# (scripts/ci/check_fast_compare.py rerun, from the steps record_step wrote). Nothing without python3 or the helper.
rerun_lines() {
  [ "$HAVE_PY" -eq 1 ] && [ -f "$COMPARE_HELPER" ] || return 0
  "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" python3 "$COMPARE_HELPER" rerun --work "$WORK" --job "$1" --root "$ROOT" \
    2>/dev/null | sed 's/^/         rerun: /'
}

# compare_with_base reruns the failures at the merge base (and with --compare-tip at the base ref's tip) and prints
# a verdict line for each, then one that counts them (scripts/ci/check_fast_compare.py compare). Each rerun has its
# check's timeout; the checkouts are removed by their paths, by the helper or by on_signal.
compare_with_base() {
  echo
  echo "== check-fast --compare-main =="
  if [ "$HAVE_PY" -eq 0 ] || [ ! -f "$COMPARE_HELPER" ]; then
    echo "check-fast --compare-main: needs python3 and $COMPARE_HELPER; nothing compared"
    return 0
  fi
  printf '%s\n' "$COMPARE_CHANGED" > "$WORK/changed.txt"
  local _tip=()
  [ "$COMPARE" -eq 2 ] && _tip=(--tip "$(git rev-parse "${BASE_REF}^{commit}")")
  # In the background and waited for: bash runs a trap only after the foreground command ends, so a signal during a
  # long rerun would wait for it; wait returns at once and on_signal stops the reruns and removes the checkouts.
  python3 "$COMPARE_HELPER" compare --work "$WORK" --root "$ROOT" --base "$MB" --base-ref "$BASE_REF" \
    ${_tip[@]+"${_tip[@]}"} --changed "$WORK/changed.txt" --timeout-go "$TIMEOUT_GO_TEST" \
    --timeout-other "$TIMEOUT_OTHER" --timeout-bin "$TIMEOUT_BIN" &
  wait $!
}

echo
echo "== check-fast summary =="
N_PASS=0; N_FAIL=0; N_TIMEOUT=0; N_SKIP=0; N_JOB_FAIL=0
for _i in "${!JOB_NAME[@]}"; do
  _kind=""; _rc=""; _seconds=""; _detail=""  # don't inherit a previous job's values if this status file is missing
  IFS=$'\t' read -r _kind _rc _seconds _detail < "${JOB_STATUS[$_i]}"
  if [ -z "$_kind" ]; then
    # The job subshell was killed (SIGKILL/OOM) before it could write a status; never let that count as nothing.
    _kind=FAIL; _seconds="?"; _detail="no status: the job was killed before it could report"
  fi
  # Time in a slot's queue is not the check's own: the duration leaves it out, and the line names it after.
  _waited=""
  if [ -s "$WORK/job.$_i.wait" ]; then
    read -r _wait_kind _wait_seconds < <(awk '{ total += $2; kind = $1 } END { print kind, total }' "$WORK/job.$_i.wait")
    if [ "${_wait_seconds:-0}" -gt 0 ] 2>/dev/null; then
      _waited=", plus ${_wait_seconds}s waiting for a $_wait_kind slot"
      if [ "$_seconds" -ge 0 ] 2>/dev/null; then
        _seconds=$((_seconds - _wait_seconds)); [ "$_seconds" -lt 0 ] && _seconds=0
      fi
    fi
  fi
  # The log's tail is a command's output, which need not be UTF-8: macOS sed stops at the first byte that is not
  # ("illegal byte sequence") in a UTF-8 locale, so it runs with LC_ALL=C.
  case "$_kind" in
    PASS) echo "PASS     ${JOB_NAME[$_i]} (${_seconds}s$_waited${_detail:+; $_detail})"; N_PASS=$((N_PASS+1)) ;;
    SKIP) echo "SKIP     ${JOB_NAME[$_i]}: $_detail"; N_SKIP=$((N_SKIP+1)) ;;
    FAIL)
      echo "FAIL     ${JOB_NAME[$_i]} (${_seconds}s$_waited, $_detail); log: ${JOB_LOG[$_i]}"
      LC_ALL=C sed 's/^/         /' "${JOB_LOG[$_i]}" | tail -n 25
      rerun_lines "$_i"
      N_FAIL=$((N_FAIL+1)); N_JOB_FAIL=$((N_JOB_FAIL+1)) ;;
    TIMEOUT)
      echo "TIMEOUT  ${JOB_NAME[$_i]} (${_seconds}s$_waited, $_detail); log: ${JOB_LOG[$_i]}"
      LC_ALL=C sed 's/^/         /' "${JOB_LOG[$_i]}" | tail -n 25
      rerun_lines "$_i"
      N_TIMEOUT=$((N_TIMEOUT+1)) ;;
  esac
done
for _msg in "${CORE_MISSING[@]:-}"; do
  [ -n "$_msg" ] && { echo "FAIL     core toolchain: $_msg"; N_FAIL=$((N_FAIL+1)); }
done
for _line in "${INFO[@]:-}"; do [ -n "$_line" ] && echo "$_line"; done
if [ "$COMPARE" -gt 0 ]; then
  if [ "$N_JOB_FAIL" -gt 0 ]; then
    compare_with_base
  else
    echo
    echo "check-fast --compare-main: no check failed; nothing to compare"
  fi
fi

echo
if [ "$N_FAIL" -gt 0 ] || [ "$CORE_FAIL" -eq 1 ]; then
  echo "check-fast: $N_PASS passed, $N_SKIP skipped, $N_FAIL failed, $N_TIMEOUT timed out (FAILED); logs in $WORK"
  KEEP_WORK=1
  exit 1
fi
if [ "$N_TIMEOUT" -gt 0 ]; then
  echo "check-fast: $N_PASS passed, $N_SKIP skipped, 0 failed, $N_TIMEOUT timed out (TIMEOUT); logs in $WORK"
  echo "check-fast: a timeout is not a pass. Rerun the timed-out job directly, or run the full suite for it"
  echo "check-fast: (docs/operations/testing.md: web 'npm test', server scripts/test/test-server.sh, android scripts/app/android.sh)."
  KEEP_WORK=1
  exit 124
fi
echo "check-fast: $N_PASS passed, $N_SKIP skipped, 0 failed, 0 timed out (OK)"
