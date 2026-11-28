/**
 * Pure TypeScript retained recording transcript review & search logic (#16509),
 * ported from `web/src/modules/recording/transcript-review.ts` & `recording-view.ts`.
 */

export interface TranscriptRow {
  readonly id: string;
  readonly seconds: number;
  readonly clock: string;
  readonly text: string;
  readonly bookmark: boolean;
  readonly speaker: string;
}

export interface TranscriptMatchPart {
  readonly text: string;
  readonly hit: boolean;
}

export interface TranscriptViewData {
  readonly rows: TranscriptRow[];
  readonly trimmedAt: string;
  readonly hasBookmarks: boolean;
  readonly segmentCount: number;
}

function asObj(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

function asStr(v: Object | undefined): string {
  return typeof v === 'string' ? v : '';
}

function pad2(n: number): string {
  return String(Math.max(0, Math.trunc(n))).padStart(2, '0');
}

export function formatOffsetClock(seconds: number): string {
  const whole = Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0;
  const h = Math.floor(whole / 3600);
  const m = Math.floor((whole % 3600) / 60);
  const s = whole % 60;
  return h > 0 ? String(h) + ':' + pad2(m) + ':' + pad2(s) : String(m) + ':' + pad2(s);
}

export function formatRecordingClock(startedAt: string, seconds: number): string {
  const startMs = startedAt ? Date.parse(startedAt) : Number.NaN;
  if (!Number.isFinite(startMs)) return formatOffsetClock(seconds);
  const d = new Date(startMs + Math.max(0, seconds) * 1000);
  return pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
}

export function transcriptLineId(seconds: number, bookmark: boolean = false): string {
  return 'recording-' + (bookmark ? 'bookmark' : 'line') + '-' + String(seconds);
}

/**
 * Builds retained transcript rows from recording detail (`meta`, `segments`).
 * When `meta.trimmed_at` is set (speech expired), spoken segments are filtered out
 * while retained bookmarks stay available for review and export.
 */
export function buildTranscriptView(detail: Object | null | undefined): TranscriptViewData {
  const obj = asObj(detail);
  if (!obj) return { rows: [], trimmedAt: '', hasBookmarks: false, segmentCount: 0 };

  const meta = asObj(obj['meta']);
  const startedAt = meta ? asStr(meta['started_at']).trim() : '';
  const trimmedRaw = meta ? asStr(meta['trimmed_at']).trim() : '';
  const trimmedAt = trimmedRaw && Number.isFinite(Date.parse(trimmedRaw)) ? trimmedRaw : '';

  const at: Array<{ readonly sec: number; readonly row: TranscriptRow }> = [];
  const rawSegments = trimmedAt ? [] : (Array.isArray(obj['segments']) ? (obj['segments'] as Object[]) : []);
  for (const item of rawSegments) {
    const seg = asObj(item);
    if (!seg || seg['echo'] === true) continue;
    const text = asStr(seg['text']).trim();
    if (!text) continue;
    const secRaw = seg['start'];
    const sec = typeof secRaw === 'number' && Number.isFinite(secRaw) ? Math.max(0, secRaw) : 0;
    const speaker = asStr(seg['speaker']).trim();
    at.push({
      sec,
      row: {
        id: '',
        seconds: sec,
        clock: formatRecordingClock(startedAt, sec),
        text,
        bookmark: false,
        speaker
      }
    });
  }

  const rawBookmarks = meta && Array.isArray(meta['bookmarks']) ? (meta['bookmarks'] as Object[]) : [];
  let hasBookmarks = false;
  for (const item of rawBookmarks) {
    const mark = asObj(item);
    if (!mark) continue;
    const secRaw = mark['at'];
    const sec = typeof secRaw === 'number' && Number.isFinite(secRaw) ? Math.max(0, secRaw) : 0;
    const label = asStr(mark['label']).trim();
    hasBookmarks = true;
    at.push({
      sec,
      row: {
        id: '',
        seconds: sec,
        clock: formatRecordingClock(startedAt, sec),
        text: label,
        bookmark: true,
        speaker: ''
      }
    });
  }

  // Stable sort by timestamp: bookmarks at the same second stay after the segment at that second.
  const indexed = at.map((entry, idx) => ({ sec: entry.sec, idx, row: entry.row }));
  indexed.sort((a, b) => (a.sec !== b.sec ? a.sec - b.sec : a.idx - b.idx));

  const counts = new Map<string, number>();
  const rows: TranscriptRow[] = [];
  let segmentCount = 0;
  for (const entry of indexed) {
    const anchor = transcriptLineId(entry.row.seconds, entry.row.bookmark);
    const occurrence = (counts.get(anchor) ?? 0) + 1;
    counts.set(anchor, occurrence);
    const id = occurrence === 1 ? anchor : anchor + '-row-' + String(occurrence);
    if (!entry.row.bookmark) segmentCount++;
    rows.push({
      id,
      seconds: entry.row.seconds,
      clock: entry.row.clock,
      text: entry.row.text,
      bookmark: entry.row.bookmark,
      speaker: entry.row.speaker
    });
  }

  return { rows, trimmedAt, hasBookmarks, segmentCount };
}

export function transcriptRowDisplayText(row: TranscriptRow, bookmarkLabel: string = 'Bookmark'): string {
  if (row.bookmark) {
    return row.text ? bookmarkLabel + ' · ' + row.text : bookmarkLabel;
  }
  return row.speaker ? row.speaker + ': ' + row.text : row.text;
}

function foldSearch(value: string): string {
  return value.normalize('NFC').toLowerCase();
}

export function filterTranscriptBookmarks(rows: readonly TranscriptRow[]): TranscriptRow[] {
  return rows.filter((row: TranscriptRow) => row.bookmark);
}

/**
 * Case-insensitive literal search across retained rows (matches display text or timestamp clock).
 */
export function findTranscriptMatches(
  rows: readonly TranscriptRow[],
  query: string,
  bookmarkLabel: string = 'Bookmark',
  bookmarksOnly: boolean = false
): TranscriptRow[] {
  const pool = bookmarksOnly ? filterTranscriptBookmarks(rows) : rows;
  const needle = foldSearch(query.trim());
  if (!needle) return [];
  return pool.filter((row: TranscriptRow) => {
    const body = foldSearch(transcriptRowDisplayText(row, bookmarkLabel));
    const clock = foldSearch(row.clock);
    return body.includes(needle) || clock.includes(needle);
  });
}

export function stepTranscriptMatch(
  matches: readonly TranscriptRow[],
  selectedId: string | null,
  direction: 1 | -1
): string | null {
  if (!matches.length) return null;
  const current = selectedId ? matches.findIndex((row: TranscriptRow) => row.id === selectedId) : -1;
  if (current < 0) return matches[direction === 1 ? 0 : matches.length - 1].id;
  return matches[(current + direction + matches.length) % matches.length].id;
}

export function splitTranscriptMatchParts(text: string, query: string): TranscriptMatchPart[] {
  const needle = foldSearch(query.trim());
  const haystack = foldSearch(text);
  let next = needle ? haystack.indexOf(needle) : -1;
  if (next < 0) return [{ text, hit: false }];

  const parts: TranscriptMatchPart[] = [];
  let prevEnd = 0;
  // Fall back to direct index mapping when lengths match (NFC-safe for common scripts)
  if (text.length === haystack.length) {
    while (next >= 0) {
      if (next > prevEnd) parts.push({ text: text.slice(prevEnd, next), hit: false });
      const end = next + needle.length;
      if (end > prevEnd) parts.push({ text: text.slice(Math.max(prevEnd, next), end), hit: true });
      prevEnd = end;
      next = haystack.indexOf(needle, next + needle.length);
    }
    if (prevEnd < text.length) parts.push({ text: text.slice(prevEnd), hit: false });
    return parts;
  }

  // Character-by-character folded length alignment when NFC/case changes UTF-16 length
  const starts: number[] = [];
  const ends: number[] = [];
  for (let i = 0; i < text.length; ) {
    const cp = text.codePointAt(i) ?? 0;
    const step = cp > 0xffff ? 2 : 1;
    const slice = text.slice(i, i + step);
    const flen = foldSearch(slice).length;
    for (let k = 0; k < flen; k++) {
      starts.push(i);
      ends.push(i + step);
    }
    i += step;
  }
  while (next >= 0) {
    const start = starts[next] ?? next;
    const end = ends[next + needle.length - 1] ?? next + needle.length;
    if (start > prevEnd) parts.push({ text: text.slice(prevEnd, start), hit: false });
    if (end > prevEnd) parts.push({ text: text.slice(Math.max(prevEnd, start), end), hit: true });
    prevEnd = end;
    next = haystack.indexOf(needle, next + needle.length);
  }
  if (prevEnd < text.length) parts.push({ text: text.slice(prevEnd), hit: false });
  return parts;
}

export function exportTranscriptText(rows: readonly TranscriptRow[], bookmarkLabel: string = 'Bookmark'): string {
  if (!rows.length) return '';
  return rows.map((row: TranscriptRow) => row.clock + '  ' + transcriptRowDisplayText(row, bookmarkLabel)).join('\n');
}
