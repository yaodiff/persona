# Minutes summary card

After a meeting the minutes read as one summary card (#6563): a headline, the core conclusions, the points and
decisions grouped by topic with their key figure or status in bold, then the action items, the open questions and
an AI note. The hub writes it as `detail.minutes.overview` and renders it as the minutes' Markdown; the clients decode
it on their own, apart from the legacy fields, and show the card only when a bullet is left after cleaning. The rules
are in [docs/assistant/meeting-minutes.md "Summary card (#6563)"](../../../docs/assistant/meeting-minutes.md). These
cases hold the hub, the mock hub, the iPhone and the web to the same bold rule, the same cleaning and the same
fallback. All data is made up.

## Rules

| Rule | Cases |
| --- | --- |
| Minutes without an overview keep the layout and the Markdown they had before #6563, byte for byte. | `legacy_only` |
| The contract's example: two titled groups, the Markdown with `##` groups and `###` topics, each decision after "Decided:", the AI note last. | `rich` |
| One group titled `""` shows no heading; in the Markdown each topic is a `##` heading. Without a headline there is no headline line (a client shows the title). A literal `*` is `\*` in the Markdown. The partial note comes before the AI note. The Markdown's words are the minutes' language's. | `one_group` |
| Bold: from the start of `text`, each `strong` entry's first exact occurrence at or after the end of the one before is bold; one that is not found there is skipped and leaves the cursor where it was. | `repeated_figure`, `strong_not_found` |
| Entries are compared by code units: no case folding, no Unicode normalization (`é` precomposed is not `e` plus U+0301). An empty entry bolds nothing. | `strong_not_found` |
| An entry is bolded as it is, with the signs a figure or a name holds (`92%`, `-5%`, `#42`, `@Ada`): the hub has already moved spaces and other punctuation outside it, and a client trims nothing. | `figure_signs` |
| An `icon` that is not exactly one of the list (`progress`, `schedule`, `release`, `performance`, `metrics`, `tooling`, `quality`, `design`, `people`, `customer`, `budget`, `risk`, `general`) is `general`, a missing one too. | `unknown_icon`, `malformed_members` |
| A topic without a bullet left, then a group without a topic, is dropped; a client shows the heading of a titled group that is left (it does not apply the hub's group rule). | `empty_topic` |
| A missing, `null` or non-object `overview`, a missing or non-array `groups`, or nothing left after cleaning is the layout as before. | `overview_empty_object`, `overview_string`, `overview_null`, `groups_not_array`, `nothing_left` |
| Members that are not objects are dropped; a group without an array `topics`, a topic without an array `bullets` or without a non-blank string `title`, a bullet or conclusion without a non-blank string `text` are dropped; a group `title` that is not a string is `""`; a wrong-typed `headline`, `conclusions`, `who` or `strong` is absent, and invalid `strong` entries are dropped; an `at` that is not a non-negative number shows no time; a `photo` that is not a positive whole number is absent; only `true` marks a decision. | `malformed_members` |
| A `photo` is a positive whole number however the JSON writes it (`2.0` is photo 2, like `2`); a fraction, `true`, `false`, zero or a negative number is absent, in bullets and conclusions alike. | `photo_numbers` |

## Format

`{"version": 1, "cases": [...]}`. Each case has `name`, `description`, `source`, `input` and `expected`.

- `input.minutes` is `detail.minutes` as the hub sends it, without `markdown` (`expected.markdown` is what the hub
  writes there). Its legacy fields always decode, whatever the overview is.
- `expected.layout` is `"overview"` (the summary card) or `"legacy"` (the minutes as before).
- `expected.overview` is what is left of the overview after cleaning, `null` for the legacy layout:
  `{"headline", "conclusions", "groups"}`. `headline` is `null` when absent (a client shows the minutes' title
  instead). Each conclusion is `{"segments", "at", "photo"}` and each bullet `{"segments", "at", "photo", "who",
  "decision"}`, in the order they come; `at`, `photo` and `who` are `null` when there is no time, photo or speaker.
  `segments` is the text cut into its plain and bold runs, `[{"text", "bold"}]`, in order: no segment is empty, and
  the cases have no two bold places side by side, so plain and bold runs alternate. Joined, the segments are the text.
  Groups are `{"title", "topics"}`, topics `{"title", "icon", "bullets"}`.
- `expected.markdown` is the hub's Markdown of these minutes (`renderMinutes`), for the cases whose overview is one
  the hub writes, or none; `null` otherwise. The hub writes only overviews that already meet the contract's
  guarantees, and reads one it cannot decode as none.

Numbers are compared as numbers (`312` and `312.0` are the same); keys as named, `null` and absent alike only where
this README says so.

## Checks

- Hub: `cd server && go test ./internal/kb -run 'MinutesOverviewFixture|MinutesOverview$'`
  (`server/internal/kb/minutes_overview_test.go`; the rules in `server/internal/kb/minutes_overview.go`).
  `TestMinutesOverviewFixture` cleans each overview as the clients do and compares the segments and the layout, and
  renders the Markdown of the cases that have one; `TestMinutesOverview` has a model's answer in the summary card's
  shape written as minutes and compares them with `rich`, overview, legacy lists and Markdown.
- The mock hub, the iPhone and the web read the same file in their own tests.
- A new case: work the expected values out by hand from the rules above, then run every consumer; never write an
  expectation from the output of the implementation it checks.
