/**
 * Meeting minutes wait-line derivation matching `spec/fixtures/minutes-wait` (#13507 / #13461).
 *
 * Pure TypeScript so shared cross-platform fixture cases run directly on the host.
 */

export interface MinutesWaitResult {
  readonly line: 'checking' | 'writing' | 'none';
  readonly doneMinutes?: number;
  readonly totalMinutes?: number;
}

/**
 * Computes what the recording detail minutes section displays while minutes are pending
 * and none are shown yet (`hasMinutes === false`).
 */
export function resolveMinutesWait(
  transcription: Record<string, Object> | null | undefined,
  minutesState: string,
  hasMinutes: boolean
): MinutesWaitResult {
  if (minutesState !== 'pending' || hasMinutes) {
    return { line: 'none' };
  }
  if (!transcription || typeof transcription !== 'object' || Array.isArray(transcription)) {
    return { line: 'writing' };
  }
  const mode = transcription['mode'];
  const state = transcription['state'];
  if (mode !== 'hub' || state !== 'finishing') {
    return { line: 'writing' };
  }
  const finalDone = transcription['final_done'];
  const end = transcription['end'];
  if (
    typeof finalDone !== 'number' ||
    typeof end !== 'number' ||
    !Number.isFinite(finalDone) ||
    !Number.isFinite(end) ||
    finalDone < 0 ||
    end <= 0
  ) {
    return { line: 'checking' };
  }
  const totalMinutes = Math.max(1, Math.round(end / 60));
  const doneMinutes = Math.min(totalMinutes, Math.floor(finalDone / 60));
  return { line: 'checking', doneMinutes: doneMinutes, totalMinutes: totalMinutes };
}
