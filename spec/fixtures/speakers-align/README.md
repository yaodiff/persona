# Speaker timeline numbering, alignment, stats and quality

Pure rules for turning a meeting's speaker timeline into stable speaker ids, aligning transcript lines across both
layers, computing speaking statistics and minor flags, and judging meeting-level separation quality (#17674). The
normative contract is in [docs/assistant/meeting-minutes.md](../../../docs/assistant/meeting-minutes.md). All data is
made up.

## Rules

| Rule | Cases |
| --- | --- |
| First-time numbering maps engine cluster labels (`c1`, …) to stable ids (`s1`, `s2`, …) in order of first speech and sets `next` past the last id. | `number_first_speech_order` |
| Pairing a later timeline matches clusters 1-to-1 to previous original speaker ids by largest overlap first, requiring overlap of at least 50% of either the new cluster's or the old id's speaking time; unpaired clusters get new ids from `next` in first-speech order and existing names follow their ids. | `pair_overlap_preserves_names_and_allocates_new`, `pair_one_to_one_largest_overlap_first`, `pair_half_threshold_either_side_qualifies` |
| Shortening a recording cuts turns past its new length (dropping turns starting at or after the new length and clamping crossing turns). | `trim_cuts_turns_past_length` |
| Alignment layer one computes per-original-id overlap ignoring merges; layer two sums merged ids and marks a line sure only when the largest merged person has strictly over 70% of all overlap, at least 1.0 second of overlap, is not `minor`, and meeting `quality` is `ok`. | `align_overlap_70_percent_boundary`, `align_merged_sum_flips_winner_and_certainty`, `align_short_overlap_under_one_second_is_unsure`, `align_minor_speaker_always_unsure`, `align_low_quality_makes_every_line_unsure` |
| Eligibility: `echo: true` lines (`echo`), `source: "hub"` lines when `timing` is `"spread"` (`unreliable_timing`, while `source: "device"` lines stay labelled), lines with under half their duration inside `covered` (`hole`), and eligible lines with zero turn overlap (`no_overlap`) receive no speaker label. | `align_eligibility_spread_device_hole_echo_no_overlap` |
| Stats compute speaking seconds (union of turns) and eligible layer-one attributed line counts per original id, sum them per merged person on read, and mark unmerged speakers under 20.0 seconds as `minor` (merged speakers are never `minor`). | `stats_original_merged_and_minor` |
| Quality marks a meeting `low` if: calendar attendees > 7 (`too_many_attendees`); total speech < 60.0 s (`too_little_speech`); covered duration within `[0, duration]` < 50% of recording duration (`low_coverage`); unlabelled eligible substantive lines (duration >= 2.0 s or >= 8 CJK unified ideographs; English by duration only; excluding echo, unreliable timing and holes) > 20% (`too_many_unlabelled`); or one merged speaker has > 95% of speaking time with >= 3 calendar attendees (`single_speaker_dominant`). | `quality_ok_baseline`, `quality_low_unlabelled_over_fifth`, `quality_ok_unlabelled_holes_and_short_english_excluded`, `quality_low_dominant_speaker_with_three_attendees`, `quality_ok_dominant_95_percent_exact`, `quality_ok_dominant_skipped_with_two_attendees`, `quality_low_too_little_speech`, `quality_low_under_half_covered`, `quality_ok_exact_60s_and_half_covered`, `quality_low_over_seven_attendees`, `quality_ok_exactly_seven_attendees` |

## Format

`{"version": 1, "cases": [...]}`. Each case has `name`, `kind`, `description`, `source`, `input` and `expected`.

Wire shapes (independent of Go internal types):

- `turns` / `raw_turns`: array of `[start, end, id]` triples in recording seconds.
- `covered`: array of `[start, end]` pairs in recording seconds.
- `people`: array of `{"id", "name"?, "me"?, "merged_into"?, "minor"?}`.
- `segments`: array of `{"start", "end", "source": "hub"|"device", "echo"?: bool, "text"?: string}`.
- `stats`: map of `id -> [seconds, lines]`.
- Alignment result per segment: `{"over": {id: seconds}, "speaker"?: id, "speaker_unsure"?: bool, "skip"?: "unreliable_timing"|"hole"|"echo"|"no_overlap"}`.
- Quality result: `{"quality": "ok"|"low", "reason": ""|"too_many_unlabelled"|"single_speaker_dominant"|"too_little_speech"|"low_coverage"|"too_many_attendees"}`.

Numbers are compared numerically (`10` and `10.0` are equal, within `1e-6` for floating-point sums).

## Checks

- Hub (Go): `cd server && go test -v ./internal/recordingspeakers/...` (`server/internal/recordingspeakers/recordingspeakersadapt/fixtures_test.go`).
- Expected values are worked out by hand from the rules above; never copy expectations from implementation output.
