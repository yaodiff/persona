/**
 * Right-edge conversation scrubber geometry matching iOS `ChatEdgeScrub.swift` (#1666).
 * Hold along the right edge of a transcript to scrub across user prompts with haptic ticks.
 */

export interface EdgeScrubMark {
  readonly id: string;
  readonly lineIndex: number;
  readonly ordinal: number;
  readonly text: string;
  readonly preview: string;
}

export interface EdgeScrubColumn {
  readonly first: number;
  readonly count: number;
  readonly gap: number;
  readonly top: number;
}

export const EDGE_TICK_GAP_MIN = 10;
export const EDGE_TICK_GAP_MAX = 22;
export const EDGE_SCRUB_BAND_VP = 28;
export const EDGE_SCRUB_SLOP_VP = 8;

export function fitEdgeScrubColumn(
  totalMarks: number,
  height: number,
  topMargin: number,
  bottomMargin: number
): EdgeScrubColumn | undefined {
  const room = height - topMargin - bottomMargin;
  if (totalMarks <= 0 || room <= 0) return undefined;
  const fits = Math.floor(room / EDGE_TICK_GAP_MIN) + 1;
  const count = Math.min(totalMarks, Math.max(1, fits));
  const gap = count > 1 ? Math.min(EDGE_TICK_GAP_MAX, room / (count - 1)) : 0;
  const span = gap * (count - 1);
  return {
    first: totalMarks - count,
    count: count,
    gap: gap,
    top: topMargin + (room - span) / 2
  };
}

export function edgeScrubIndexAt(column: EdgeScrubColumn, y: number): number {
  if (column.count <= 1 || column.gap <= 0) return column.first;
  const relative = Math.round((y - column.top) / column.gap);
  const clamped = Math.max(0, Math.min(column.count - 1, relative));
  return column.first + clamped;
}

export function edgeScrubYOf(column: EdgeScrubColumn, index: number): number {
  return column.top + column.gap * (index - column.first);
}

export interface EdgeScrubSourceLine {
  readonly id: string;
  readonly role: string;
  readonly text: string;
  readonly hasImages: boolean;
}

export function buildEdgeScrubMarks(lines: readonly EdgeScrubSourceLine[]): EdgeScrubMark[] {
  const marks: EdgeScrubMark[] = [];
  let ordinal = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.role !== 'user') continue;
    const cleaned = (line.text || '').replace(/\s+/g, ' ').trim();
    if (cleaned.startsWith('/loop')) continue;
    if (!cleaned && !line.hasImages) continue;
    ordinal++;
    const label = cleaned ? (cleaned.length > 72 ? cleaned.slice(0, 71) + '…' : cleaned) : 'Photo';
    marks.push({
      id: line.id,
      lineIndex: i,
      ordinal: ordinal,
      text: label,
      preview: `${ordinal}. ${label}`
    });
  }
  return marks;
}
