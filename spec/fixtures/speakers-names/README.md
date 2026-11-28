# Speaker display names and roster edits

Shared rules for displaying disagreement position speakers (including `also` supporters) and applying speaker roster
edits (`name`, `merge`, `unmerge`, `clear`) with automatic merging, chain flattening and server-computed `undo`
(#17674). The normative contract is in
[docs/assistant/meeting-minutes.md](../../../docs/assistant/meeting-minutes.md). All data is made up.

## Rules

| Rule | Cases |
| --- | --- |
| Display row 1: a sure `speaker` the owner named shows the owner-chosen name (taking precedence over `who`). | `display_row1_sure_named_speaker` |
| Display row 2: a sure `speaker` not named shows its speaker number `N` (localized by callers as `Speaker N` / `发言人 N` / `話者 N`, taking precedence over `who`). | `display_row2_sure_unnamed_speaker` |
| Display row 3: when `speaker` is absent or unsure, a model-written `who` shows `who`. | `display_row3_unsure_speaker_falls_back_to_who`, `display_row3_no_speaker_shows_who` |
| Display row 4: otherwise no name and no hint are shown. | `display_row4_unsure_without_who_shows_none`, `display_row4_neither_speaker_nor_who_shows_none` |
| Positions with `also` list sure speakers in order, deduplicated by canonical merged id (skipping unsure `also` entries); when main has no sure speaker, `who` is kept unless an `also` sure speaker already carries that exact name. | `display_also_lists_multiple_sure_speakers_and_deduplicates`, `display_also_with_unsure_main_and_who` |
| Naming trims surrounding whitespace, accepts up to 100 Unicode code points on one line, clears name on empty string, and rejects >100 code points or multi-line names. | `edit_name_trims_and_sets_undo`, `edit_clear_name_empty_string`, `edit_name_over_100_runes_rejected`, `edit_name_exactly_100_runes_accepted`, `edit_name_multiline_rejected` |
| Giving a speaker an existing name automatically merges the speaker who spoke less into the one who spoke more (exact match after trimming); `undo` unmerges and restores the old name. | `edit_same_name_auto_merges_less_into_more_and_undo`, `edit_same_name_auto_merges_more_absorbs_existing_holder_and_undo` |
| Manual `merge` merges `from` into `into`, clears `minor` on merged entries, and flattens children or merged targets so `merged_into` never forms chains; `unmerge` returns the speaker unnamed and recomputes `minor`. | `edit_manual_merge_clears_minor_and_unmerge_returns_unnamed`, `edit_unmerge_returns_unnamed_and_restores_minor`, `edit_merge_flattens_children_no_chains`, `edit_merge_into_merged_target_flattens_no_chain` |
| `clear` empties `people` and returns an empty `undo` list (cannot be undone). | `edit_clear_empties_list_with_no_undo` |

## Format

`{"version": 1, "cases": [...]}`. Each case has `name`, `kind` (`"display"` or `"edit"`), `description`, `source`,
`input` and `expected`.

- For `kind: "display"`:
  - `input.people`: roster `[{"id", "name"?, "me"?, "merged_into"?, "minor"?}]`.
  - `input.position`: `{"speaker"?, "speaker_unsure"?, "who"?, "also"?: [{"speaker"?, "speaker_unsure"?}]}`.
  - `expected.displays`: ordered list of `{"kind": "name", "value": "..."}`, `{"kind": "numbered", "number": N}`, or
    `{"kind": "who", "value": "..."}` (`[]` when nothing is shown).
- For `kind: "edit"`:
  - `input`: `{"next", "people", "stats": {id: [seconds, lines]}, "edits"?: {"name"?, "merge"?, "unmerge"?, "clear"?}, "ops": [...]}`.
  - `expected`: either `{"people", "edits", "undo", "verify_undo_roundtrip"?: bool}` on success, or
    `{"error": "invalid"|"refused"|"missing", "error_code": "..."}` on failure.

## Checks

- Hub (Go): `cd server && go test -v ./internal/recordingspeakers/...` (`server/internal/recordingspeakers/recordingspeakersadapt/fixtures_test.go`).
- Expected values are worked out by hand from the rules above; never copy expectations from implementation output.
