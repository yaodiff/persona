package runtimepkg

import (
	"os"
	"testing"
)

// TestDistBudget bounds what personad, persona-node and persona-manager embed of the runtime packages: every file
// Generate writes from integrations/. The complete Antigravity package, including the image guard (#11790),
// measured 192,060 B; preserving delivered replies through a background wake (#11756) added 1,822 B. Claude Code
// plugin 0.2.0 session settings overrides brought the total to 196,211 B; busy owner input queueing + turn interrupts
// add 2,129 B on their branch. Main measured 196,937 B on 2026-11-21, and keeping an Antigravity send from rewriting
// the conversation's history (#12014) adds 738 B (199,188 B ceiling). Embedding persona-opencode-runtime
// (opencode/package.json, index.js, companion.mjs, lib/*.js including #12504/#12545; shared runtime-sdk blob reused)
// measured 248,704 B on 2026-11-21. Session-bound A2A tool bridges bring the generated total to about 251.3 kB.
// Minifying the remaining non-compat Antigravity lib sources (acp.js, file-links.js, models.js; #12781, #13123)
// brings the generated total from 254,085 B back to 251,311 B under the unchanged 252,000 B ceiling.
// Antigravity's empty-response retry and interrupted-turn notices (#13957) use the remaining room and add 850 B.
// Runtime fixes merged on 2026-11-24 add 872 B (253,709 B): the runtime-sdk's shell gates reading quoted $(…) and
// $'…' words (#14040, 574 B), opencode's JSONC comma-brace strings and provider:org/model split (#14091, 189 B), pi's
// top-level oldText/newText (#14092, 11 B) and paseo's WebSocket RSV-bit and Close-payload checks (#14095, 98 B).
// OpenCode live ACP tool-call normalization (#14130) adds 383 B on main (254,092 B).
// The dedicated-Assistant environment handoff (#14068) adds 114 B (254,206 B).
// Claude Code plugin 0.3.0 permission / question / plan approval bridging (#14214) adds 6,363 B (260,569 B).
// Antigravity multi-instance companion flags and --agy-dir env propagation (#14483) add 373 B (260,942 B).
// An Antigravity CLI without session/set_mode: checks skip the switch and starts say to update agy (#14533), 149 B (261,091 B).
// Antigravity Gemini API-key gateway model discovery routes through HTTP(S)_PROXY and honors NO_PROXY (#14552), 1,497 B (262,588 B).
// Keeping agy's live text ahead of later thoughts (#14745) measured 262,673 B on main, 85 B over the ceiling.
// Keeping Antigravity steps at a reused lower index when the export goes on past them, dropping them again once the run re-covers them (#14626), adds 230 B (262,818 B).
// Antigravity native error backfill at every unexported index, loading conversations another agy process created, with read failures in the history token (#14627), 427 B (263,245 B).
// Antigravity subagent reports and background task results as task notices, invoke_subagent Role / Prompt (#14625), 749 B (263,994 B).
// Untitled Antigravity conversations take a short title from their first prompt line (#14629), 218 B (264,212 B).
// An API error agy streams as message text becomes the turn's error, kept in history and settled idle (#14628), 231 B (264,443 B).
// Antigravity GENERIC timestamp header stripping (#13662) adds 176 B (264,619 B).
// An agy main chat on Windows finding personad.exe and a cmd.exe media-guard hook that also checks Node.js (#14511) add 182 B (264,801 B).
// Subsequent runtime fixes on main (#15269, #15275, #13312, #12952, #15475) bring the total to 265,362 B.
// Antigravity shared ACP exit cleanup (#15756) and always-allow permission choices (#15785) bring the total to 265,686 B.
// Antigravity permission mode persistence across companion restarts and resume re-application (#15807) add 273 B (265,959 B).
// Antigravity agent-mode MCP proxy budgeting for non-assistant leader and worker catalogs (#15822) adds 253 B (266,212 B).
// Antigravity launchers.check honoring explicit params.model (#15905) adds 1 B (266,213 B).
// Antigravity withoutStaleWorking coverage for send policy, fork, rewind, modes, and native error reads (#15924) adds 48 B (266,261 B).
// Runtime package lifecycle support for preserving tools across upgrades (#16016) adds 4,368 B (270,629 B).
// An agy main chat on Windows finding personad.exe and a cmd.exe media-guard hook that also checks Node.js (#14511) add 163 B (270,792 B).
// ACP instruction fitting to the mounted device tools and supervision role (#16052) adds 244 B (270,873 B).
// Antigravity settled-turn recovery across delayed shifted-index exports and structured agy_turn_end turn_duration items (#15619) add 1,169 B (272,042 B).
// Antigravity non-rewind negative cache for summary_behind GetCascadeTrajectory checks + summarizeTranscript prefix summary reuse (#13086) add 872 B (272,914 B).
// Antigravity Desktop session.start (ConversationService/CreateConversation), model discovery, and lifecycle controls (#15994) add 4,922 B (277,836 B).
// Antigravity Assistant run_command command-gate routing, unclipped command & per-call Cwd forwarding, fail-closed reconnect, tracks Assistant detection, and Assistant allow_always bypass guard (#16202) add 511 B (278,347 B).
// After preserving Antigravity modes (#16300), the packages measured 278,334 B. Filtering botmux marker
// lines before shortening runtime previews adds 549 B (#16310).
// Antigravity detached pending approval turn cancellation (including sibling-ask withdrawal and stale-turn/racing-ask/refusal guards) and explicit deny-with-note refusal follow-up add 436 B (279,319 B).
// Antigravity native usage account name and subscription plan enrichment via GetUserStatus (#16277) adds 286 B (279,605 B).
// Main then measured 280,046 B (#16595): #16490 (+21 B), #15678 (+406 B) and #16027 (+22 B) grew the packages without raising
// the budget, and #16285 adds 278 B on main (its 286 B was measured on its own branch).
// Later runtime changes on main (#16591, #16122) bring the total to 280,892 B.
// Following an Antigravity conversation past a rewind's abandoned export tail and rewinding a cut that hides ranges at
// its matching visible step (#14297) add 304 B (281,196 B).
// Applying the configured Antigravity model before the first prompt after ACP resume (#15537) adds 67 B (281,263 B).
// Claude Code plugin 0.3.1 reports classic hook pass-over and disables permission bridging under managed settings
// (#15546), adding 1,397 B (282,660 B).
// Antigravity form elicitation (#16645) and background task and schedule tracking (#13594) bring the total to 286,998 B,
// including the 182 B Windows media-guard change above.
// The Antigravity companion running the node's program from --personad-bin, else persona-node beside personad (#14582), 204 B (287,202 B).
// Antigravity incremental transcript append (tail anchor + rolling SHA-256 + deferred/periodic verification),
// sub-second fileMeta fast hit, watched-poll idle backoff, and bounded summary/cache weights (with decoupled
// raw-byte and object-weight transcriptCache caps) add 1,969 B (289,171 B).
// Merging #16001 onto that main keeps its settled list-summary / .persona-turn-completion keying and static summary_behind readListedTranscript cache, restores dev/ino/ctime wake-receipt detection, and adds a fixed
// 4 KiB head-sample integrity check inside the racy window alongside the tail anchor. The union measures
// 290,316 B exactly. The merge also restores one missing ASI semicolon in main's runtime.js.
// With the compat.js declaration fix (#16771), main measured 290,321 B.
// Keeping legacy effort-only requests in the selected model family adds 694 B (291,015 B).
// Antigravity keep_always declaration, Assistant allow_once-absent fail-closed guard, and MultiEdit/NotebookEdit tool mapping add 101 B (291,116 B).
// Treating epoch-zero resetTime on untouched Antigravity quota buckets as unset and emitting a SHA-256 account_key for
// signed-in Antigravity accounts add 180 B (291_296 B).
// Antigravity unknown-status jitter fixes (CANCELING/BUSY native working, empty 0-step idle, fresh empty-transcript
// start, settled-turn recovery across transient empty/UNSPECIFIED summary_not_idle and tool-only turns with
// autonomous-wake cleanup, cached GetCascadeTrajectory idle verification for summary_behind, last_modified_time
// listed cache invalidation, and sanitized unrecognized-status logging) add 1,066 B (292,362 B).
// Antigravity Run-prefixed Persona MCP permission normalization (with shell/file/a2a guards), fork mode/model/effort preservation with RPC-gated implicit-option recovery, and assistant capabilities.rewind=false advertisement add 1,976 B (294,338 B).
// Antigravity idle auto-compaction `/compact` divider mapping and empty-prompt exemption (#16604) add 174 B (294,512 B).
// OpenCode unconditional interruptVersion queued-turn cancellation and status refresh add 71 B (294,583 B).
// Keeping Antigravity media-guard fail-closed when the optional partial JPEG copy fails, races, or receives a non-string state adds 55 B (294,638 B).
// PR #16425 (P0-1) reuses one read-only conversation_summaries connection with a stat-validated parsed snapshot,
// prepared primary-key lookup and the watch-poll short-circuit (the nativeWorking gate stays open so the active
// interval keeps evaluating). It uses main's object-shaped transcriptVersion key, keeps main's #16604 compact,
// CHECKPOINT and availability-model edits through the merge, and ships summary-db.js through the antigravity/lib/*.js
// glob. Re-aligned onto the main carrying #16604, the union measures 296,417 B exactly (+1,779 B over 294,638 B).
// Main carrying #17140 (Antigravity background-task restart/list reconciliation, +2,241 B) measured 298,658 B.
// Deterministic runtime compaction settlement (`session.compaction.settled` in runtime-sdk and antigravity runtime) adds 211 B (298,869 B).
// Main carrying #17145 (Antigravity ACP pool isolation) plus hardening runtime-sdk shell-gates against POSIX/zsh/ksh -c/operand option bypasses, standalone +, unquoted command substitutions, and line continuations measures 301,379 B (+2,510 B over 298,869 B).
// Checking conversation existence before serving Antigravity session.items (fail-closed on stat/metadata read errors) adds 272 B (301,651 B, #16410).
// Antigravity session model persistence and reload restoration after idle ACP recycle or companion restart (#16393) adds 313 B over main (301,651 B -> 301,964 B).
// Making runtime-sdk light() idempotent and avoiding double light() in dsh-files adds 58 B (302,022 B).
// Registering integer and number A2A tool parameters with numeric TypeBox schemas in pi adds 172 B (302,194 B).
// Hardening opencode pre-prompt interrupt/close guards and post-close snapshot/streamEnd suppression adds 644 B (302,838 B).
// Pausing the Antigravity prompt inactivity watchdog once a matched foreground reply is settled and the only open
// work is a same-turn live background task (six fail-closed predicates, wake re-arm and T1-T5 tests; #17214) adds
// 1,424 B of gzipped-package budget (302,838 B -> 304,262 B).
// Preserving delivered replies across live Antigravity background wakes, in-place RUNNING->DONE updates, and runtime-sdk catch-up/batch delta ordering (#14920) adds 1,015 B over main (304,262 B -> 305,277 B).
// Keeping Antigravity summary-db connection() atomic and closing on probe() error adds 48 B (305,277 B -> 305,325 B).
// Persisting the model after the first turn and restoring older Antigravity sessions from their transcript adds 100 B (#16458).
// Targeted Antigravity stepsToItems lookup (targetItemId), per-session sentMsgVersions, and lazy transcriptOf projection (#16152) add 567 B (305,425 B -> 305,992 B).
// Antigravity unavailable-Auto permission-mode fallback, restricted permission_modes advertisement, and runtime-sdk session.modes permission_mode forwarding (#17239) add 703 B (305,992 B -> 306,695 B).
// Hardening dsh assistant memoryWrite against relative-path, clipInput truncation, and symlink escapes (#17339) adds 739 B (306,695 B -> 307,434 B).
// Isolating managed Antigravity channel model catalogs, stripping ambient/extension provider env across channels, preserving Assistant channel and extension env snapshots across respawns, and rotating warm idle Assistant channels add 181 B (307,434 B -> 307,615 B).
// Routing dsh assistant shell gates through runtime-sdk shell-gates with fail-closed hubReady/disconnect/phoneTools=false guards adds 794 B (307,615 B -> 308,409 B).
// Guarding Antigravity ACP stdout/nativeRequest against JSON null/non-object payloads and rejecting stale _start() after stop/process replacement add 107 B (308,409 B -> 308,516 B).
// Hardening opencode companion & plugin assistant isMemoryWrite with symlink/namei/multi-resource/allowlisted-rawInput/diff/metadata containment (opencode/lib/memory-write.js) adds 2,064 B (308,516 B -> 310,580 B).
// Persona-owned prepared identity persistence, scoped marker validation, lazy restoration and fail-closed
// supervision-policy recovery (#17348) measured 310,620 B against the 308,516 B ACP main: +2,104 B.
// Its Antigravity part is unchanged by the separately packed OpenCode change above, giving a 312,684 B union.
// Fixing Antigravity supervision policy >24KB early-newline split fallback and hardening policyRoot/session/file symlink + O_NOFOLLOW checks add 337 B (312,684 B -> 313,021 B).
// Hardening Paseo read-only (plan) start and takeover (withholding initialPrompt until verified, active-receiver resolution, pre-mutation pendingReadonly eviction, 0..callWait op/send coordination, and zombie inFlight cleanup) adds 2,516 B (313,021 B -> 315,537 B).
// Refreshing expired native Antigravity OAuth through bounded ACP authentication (#17537) brought main to 317,692 B (+2,155 B); Antigravity readNativeError reload-on-not_found, fail-fast permanent loss, and non-API local notice / timeout formatting (#17577) add 271 B (317,692 B -> 317,963 B).
// Hardening Antigravity Desktop hub discovery/RPC/context wrapping, workspacePathOf UNC/NUL/host checks, and custom-channel fork/rewind isolation with rollback adds 1,012 B (317,963 B -> 318,975 B).
// Anchoring Antigravity one-shot timer heartbeat detection and returning turn_over_background when interrupting background-only sessions (#17619) add 222 B (318,975 B -> 319,197 B).
// Preserving active native Antigravity interrupts alongside running background tasks (#17652) adds 15 B (319,197 B -> 319,212 B).
// Runtime command input (#14573) adds 2,657 B to the runtime SDK package
// on top of the 319,212 B upstream total (321,869 B combined).
// Reaping idle assistant ACP workers independently of coding agents and storing compact background summaries instead of full exported.steps in listedTranscriptCache add 8 B (321,869 B -> 321,877 B).
// Replacing resident Antigravity ACP processes after a new agy sign-in (metadata-only sign-in stamp, lib/sign-in.js) or a
// quota or sign-in error (#17860) adds 1,809 B (321,877 B -> 323,686 B).
// Reconciling Antigravity silent background task terminations (TimerCondition cancellation, JSON-array manage_task(list), manage_task(status), one-shot timer expiry with cache invalidation/refresh, schedule MaxIterations, and manage_subagents; #17891) adds 2,393 B (323,686 B -> 326,079 B).
// Raise the budget only for code a runtime needs, never to embed the packages raw again.
func TestDistBudget(t *testing.T) {
	const budget = 326_079
	_, out := generateIntegrations(t)
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(0)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	if size > budget {
		t.Fatalf("generated runtime packages are %d bytes, over the budget of %d", size, budget)
	}
	t.Logf("generated runtime packages: %d bytes (budget %d)", size, budget)
}
