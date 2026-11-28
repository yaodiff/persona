# Minutes disagreements

A meeting's minutes may list real disagreements (`detail.minutes.debates`, #16728): the question in dispute, two to four
positions citing the transcript line where each side's holder says it, and whether the meeting settled it (`open` or
`settled`, with an `outcome` linked by the hub to the kept decision when settled). The rules are in
[docs/assistant/meeting-minutes.md "Disagreements (#16728)"](../../../docs/assistant/meeting-minutes.md). These cases
hold the hub, the mock hub, the iPhone and the web to the same cleaning rules, fallback when no disagreement survives,
and hub Markdown in both layouts across `en`, `zh-Hans` and `ja`. All data is made up.

## Rules

| Rule | Cases |
| --- | --- |
| Summary-card layout with disagreements renders `## Disagreements` after Core conclusions and before the first group or topic, with status words and optional speaker and outcome lines. | `overview_with_debates` |
| Legacy layout with disagreements renders `## Disagreements` between Action items and Open questions, in `en`, `zh-Hans` and `ja`. | `legacy_with_debates_en`, `legacy_with_debates_zh`, `legacy_with_debates_ja` |
| Legacy minutes in `zh-Hans` and `ja` without disagreements keep their Markdown byte for byte unchanged. | `legacy_no_debates_zh`, `legacy_no_debates_ja` |
| `"debates": []` in either layout cleans to `[]` and renders byte for byte identically to minutes without `debates`. | `empty_debates_overview`, `empty_debates_legacy` |
| A `debates` block that cleans down to nothing, or a `debates` value that is `null`, a string or an object, cleans to `[]`, leaves the rest of the minutes intact, and renders byte for byte identically to minutes without `debates`. | `cleaned_to_empty_overview`, `debates_not_array`, `debates_null`, `debates_object` |
| An unknown `status` cleans to `null` and renders with no status word; unknown keys at every level (`debates[]`, `positions[]`, `outcome`) are ignored. | `unknown_status_and_extra_keys` |
| Non-object debate or position entries, blank `question` or `stance`, non-array `positions`, and debates left with fewer than two valid positions are dropped; non-string or blank `topic` / `who` become `null`; invalid or negative `at` becomes `null`; `outcome` is kept only for `settled` with a non-blank string `text`; `outcome.decision` is kept only when it is a non-negative integer within `decisions` bounds (never a boolean, fraction, negative or out of bounds). | `malformed_members` |
| In the summary-card layout, literal `*` in debate fields is escaped as `\*` in Markdown, matching the rest of the summary-card Markdown. | `overview_star_escape` |

## Format

`{"version": 1, "cases": [...]}`. Each case has `name`, `description`, `source`, `input` and `expected`.

- `input.minutes` is `detail.minutes` as the hub sends it, without `markdown` (`expected.markdown` is what the hub
  writes there). The rest of the minutes always decodes, whatever `debates` holds.
- `expected.debates` is the cleaned list of disagreements (`[]` when none survives):
  `{"question", "topic", "status", "at", "positions", "outcome"}`.
  - `topic` is a non-blank string or `null`.
  - `status` is `"open"`, `"settled"`, or `null` (for any unknown, missing or non-string value: clients show no status word).
  - `at` is a non-negative finite number or `null`.
  - Each position is `{"stance", "at", "who"}` (`at` a non-negative finite number or `null`; `who` a non-blank string or `null`).
  - `outcome` is `{"text", "at", "decision"}` (only when `status` is `"settled"` and `outcome` has a non-blank `text`; `decision` a non-negative integer `< len(decisions)` or `null`), else `null`.
- `expected.markdown` is the hub's Markdown of these minutes (`renderMinutes`) for cases whose fields are valid for hub
  rendering (or read as none); `null` when a malformed member mixes invalid timestamps that the hub would never write.

Numbers are compared as numbers (`312` and `312.0` are the same); keys as named.

## Checks

- Hub: `cd server && go test ./internal/modules/recording/writers -run 'MinutesDebatesFixture'`
  (`server/internal/modules/recording/writers/minutes_debates_test.go`; the rules in `server/internal/modules/recording/writers/minutes_debates.go` and `server/internal/recordingwire/minutes_debates.go`).
- Mock hub: `python3 scripts/mock/test_recording_minutes_debates_mock.py`.
- A new case: work the expected values out by hand from the rules above, then run every consumer; never copy expectations
  from the output of the implementation it checks.
