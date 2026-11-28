import { t } from "../../i18n";
import { transcriptLineId, type RecordingView } from "./recording-view";

export type TranscriptRow = RecordingView["lines"][number] & { id: string };

/** The first segment and first bookmark at a time retain their existing minutes/recap anchors.
 *  Additional lines at the same time get their own targets for search and keyboard navigation. */
export function transcriptRows(rec: Pick<RecordingView, "lines" | "trimmedAt">): TranscriptRow[] {
  const counts = new Map<string, number>();
  const rows: TranscriptRow[] = [];
  for (const line of rec.lines) {
    if (rec.trimmedAt && !line.bookmark) continue;
    const anchor = transcriptLineId(line.seconds, line.bookmark);
    const occurrence = (counts.get(anchor) ?? 0) + 1;
    counts.set(anchor, occurrence);
    rows.push({ ...line, id: occurrence === 1 ? anchor : `${anchor}-row-${occurrence}` });
  }
  return rows;
}

export function transcriptRowText(row: TranscriptRow): string {
  if (!row.bookmark) return row.text;
  const label = t("recording.bookmark");
  return row.text ? `${label} · ${row.text}` : label;
}

const fold = (value: string) => value.normalize("NFC").toLowerCase();

/** Search the visible wording literally, including across spaces in a phrase. */
export function transcriptMatches(rows: readonly TranscriptRow[], query: string): TranscriptRow[] {
  const needle = fold(query.trim());
  if (!needle) return [];
  return rows.filter((row) => fold(transcriptRowText(row)).includes(needle));
}

/** Split highlights in the displayed text, preserving the original spelling even when normalization
 *  or case conversion changes a character's UTF-16 length (for example e + combining acute or İ). */
export function transcriptMatchParts(text: string, query: string): { text: string; hit: boolean }[] {
  const needle = fold(query.trim());
  const haystack = fold(text);
  let next = needle ? haystack.indexOf(needle) : -1;
  if (next < 0) return [{ text, hit: false }];

  const starts: number[] = [], ends: number[] = [];
  if (text !== text.normalize("NFC") || text.length !== haystack.length) {
    for (const { segment, index } of new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(text)) {
      const foldedLength = fold(segment).length;
      for (let i = 0; i < foldedLength; i++) {
        starts.push(index);
        ends.push(index + segment.length);
      }
    }
  }
  const parts: { text: string; hit: boolean }[] = [];
  let previousEnd = 0;
  while (next >= 0) {
    const start = starts[next] ?? next;
    const end = ends[next + needle.length - 1] ?? next + needle.length;
    if (start > previousEnd) parts.push({ text: text.slice(previousEnd, start), hit: false });
    // Two matches can map to the same source grapheme.
    if (end > previousEnd) parts.push({ text: text.slice(Math.max(previousEnd, start), end), hit: true });
    previousEnd = end;
    next = haystack.indexOf(needle, next + needle.length);
  }
  if (previousEnd < text.length) parts.push({ text: text.slice(previousEnd), hit: false });
  return parts;
}

export function stepTranscriptMatch(matches: readonly TranscriptRow[], selected: string | null, direction: 1 | -1): string | null {
  if (!matches.length) return null;
  const current = matches.findIndex((row) => row.id === selected);
  if (current < 0) return matches[direction === 1 ? 0 : matches.length - 1].id;
  return matches[(current + direction + matches.length) % matches.length].id;
}

/** Export retained rows in their original order with the exact clocks shown on screen. */
export function exportTranscriptText(rows: readonly TranscriptRow[]): string {
  return rows.map((row) => `${row.clock}  ${transcriptRowText(row)}`).join("\n");
}
