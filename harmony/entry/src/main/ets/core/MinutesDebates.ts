/**
 * Pure TypeScript lenient decoder for meeting minutes disagreements (`debates`),
 * matching `server/internal/recordingwire/minutes_debates.go` (#16732 / #16735).
 */

export type DebateStatus = 'open' | 'settled' | string;

export interface MinutesDebatePosition {
  readonly stance: string;
  readonly at: number;
  readonly who?: string;
}

export interface MinutesDebateOutcome {
  readonly text: string;
  readonly at: number;
  readonly decision?: number;
}

export interface MinutesDebateItem {
  readonly question: string;
  readonly topic?: string;
  readonly status: DebateStatus;
  readonly at: number;
  readonly positions: MinutesDebatePosition[];
  readonly outcome?: MinutesDebateOutcome;
}

function nonBlankString(v: Object | null | undefined): string | undefined {
  if (typeof v !== 'string') return undefined;
  return v.trim().length > 0 ? v : undefined;
}

function nonNegNumber(v: Object | null | undefined): number | undefined {
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 0) return undefined;
  return v;
}

function plainObject(v: Object | null | undefined): Record<string, Object> | null {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null;
  return v as Record<string, Object>;
}

/**
 * Decodes `detail.minutes.debates` leniently:
 * - Missing, null, non-array, or unreadable block returns `[]`.
 * - Malformed entries or debates with `< 2` valid positions are silently dropped.
 * - Outcome is retained only when `status === 'settled'` and `outcome.text` is non-blank.
 */
export function parseMinutesDebates(
  raw: Object | null | undefined,
  decisionsCount?: number
): MinutesDebateItem[] {
  if (!Array.isArray(raw)) return [];
  const out: MinutesDebateItem[] = [];

  for (const entry of raw as Object[]) {
    const obj = plainObject(entry);
    if (!obj) continue;
    const question = nonBlankString(obj['question']);
    if (!question) continue;
    const rawPos = obj['positions'];
    if (!Array.isArray(rawPos)) continue;

    const positions: MinutesDebatePosition[] = [];
    for (const pe of rawPos as Object[]) {
      const po = plainObject(pe);
      if (!po) continue;
      const stance = nonBlankString(po['stance']);
      if (!stance) continue;
      const at = nonNegNumber(po['at']) ?? 0;
      const who = nonBlankString(po['who']);
      positions.push(who !== undefined ? { stance, at, who } : { stance, at });
    }
    if (positions.length < 2) continue;

    const topic = nonBlankString(obj['topic']);
    const status = nonBlankString(obj['status']) ?? '';
    const at = nonNegNumber(obj['at']) ?? 0;

    let outcome: MinutesDebateOutcome | undefined;
    if (status === 'settled') {
      const oo = plainObject(obj['outcome']);
      const text = oo ? nonBlankString(oo['text']) : undefined;
      if (oo && text) {
        const outAt = nonNegNumber(oo['at']) ?? 0;
        const decRaw = nonNegNumber(oo['decision']);
        const decision =
          decRaw !== undefined &&
          Number.isInteger(decRaw) &&
          (decisionsCount === undefined || decRaw < decisionsCount)
            ? decRaw
            : undefined;
        outcome = decision !== undefined ? { text, at: outAt, decision } : { text, at: outAt };
      }
    }

    out.push({
      question,
      topic,
      status,
      at,
      positions,
      outcome
    });
  }

  return out;
}

export function isKnownDebateStatus(status: string): boolean {
  return status === 'open' || status === 'settled';
}
