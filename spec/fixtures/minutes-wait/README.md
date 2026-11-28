# What the minutes line says while the minutes wait

A hub-mode meeting's minutes wait for the final pass, which replays the whole recording once it is complete (#13461).
While they wait, the minutes line on the iPhone, Android and the web says the hub is checking the whole recording, with
minutes done of the total once the pass has started, instead of "Writing the minutes…" for the whole wait. The rules
are in [docs/assistant/meeting-minutes.md "Gaps and the final pass"](../../../docs/assistant/meeting-minutes.md). These
cases hold the hub (what it keeps → what it sends) and the clients (what they get → which line, which numbers) to
the same rule. All data is made up.

## Rules

1. The hub sends `transcription.final_done` and `transcription.end` (recording seconds) only while the state is
   `finishing`, the pass has started (`final_from` is set; it may be waiting for its next try) and the end is known
   (`end` above 0). `final_done` is at most `end`. Cases: `pass_midway`, `pass_just_started`, `pass_not_started_yet`,
   `last_window_past_the_end`, `end_unknown`, `final_writes_the_minutes`, `failed_writes_the_minutes`,
   `live_before_the_complete_upload`.
2. A client shows the line only while the minutes are `pending` and none are shown (`has_minutes: false`); otherwise
   `none`, the page as before (the minutes, Updating, or their failure). Cases: `minutes_shown_while_newer_ones_wait`,
   `minutes_ready`, `minutes_failed`.
3. `checking` while `transcription.mode` is `hub` and `state` is `finishing`; `writing` otherwise (final, failed,
   live, device mode, no transcription, a state the client does not know). Cases: `final_writes_the_minutes`,
   `failed_writes_the_minutes`, `live_before_the_complete_upload`, `device_mode`, `no_transcription`, `unknown_state`.
4. Numbers come with `checking` only when both `final_done` and `end` are JSON numbers, `final_done` is 0 or more and
   `end` above 0: `total_minutes` is `end / 60` rounded to the nearest (halves up), at least 1; `done_minutes` is
   `final_done / 60` rounded down, at most `total_minutes`. Cases: `pass_midway`, `done_rounds_down_total_rounds`,
   `short_meeting_counts_one_minute`, `last_window_past_the_end`, `numbers_that_are_not_numbers`.

## Structure

- `{"version": 1, "cases": [...]}`. `hub` (optional) is what `meeting.json` keeps: `state`, `pass_started`
  (whether `final_from` is set), `final_done`, `end`; `transcription` is `detail.meta.transcription` as the apps
  read it (`null`: none); `minutes_state` is `detail.meta.minutes_state`; `has_minutes` whether `detail.minutes` is
  there. `expected.line` is `checking`, `writing` or `none`; `done_minutes` and `total_minutes` are present only when
  the line carries them.
- The hub checks the cases with `hub`: the view it builds has `final_done` and `end` exactly when `transcription`
  has them, with the same values. The clients check every case.

## Verification

- hub: `cd server && go test ./internal/recordingwire -run MinutesWaitFixture` (`server/internal/recordingwire/transcription_test.go`).
- iPhone: `apple/PersonaTests/MinutesWaitFixtureTests.swift` (the rule in `MinutesWaitLine`,
  `apple/Persona/Recording/MeetingAudioWire.swift`).
- web: `cd web && npx vitest run src/modules/recording/minutes-wait.test.ts` (the rule in `web/src/modules/recording/minutes-wait.ts`).
- Android: KnowledgeMinutesTest in the feature:knowledge module (the rule in KnowledgeMinutes.kt).
- HarmonyOS: `cd harmony && node --test tests/minutes-wait.test.mjs` (the rule in `harmony/entry/src/main/ets/core/MinutesWait.ts`).
- A new case: work out the expected line and numbers by hand from the rules above, then run the hub and client checks.
