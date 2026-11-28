/** An RFC3339 time in milliseconds; NaN for none or one that does not parse. */
export function timeOrNaN(at?: string | null): number {
  return at ? Date.parse(at) : NaN;
}

/** The same time with 0 for none or one that does not parse. The hub's times are not fixed-width (Go
 * drops a fraction's trailing zeros: `…:05.12Z` and `…:05.123Z`) and may carry an offset (`+08:00`), so they are
 * compared as times, never as strings. */
export function timeMs(at?: string | null): number {
  const ms = timeOrNaN(at);
  return Number.isNaN(ms) ? 0 : ms;
}

/** An RFC3339 instant at the wire's full nanosecond precision; null when it is absent or malformed. */
export function timeNs(at?: string | null): bigint | null {
  const match = at ? /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(at) : null;
  if (!match) return null;
  const seconds = Date.parse(match[1] + match[3]) / 1000;
  if (!Number.isInteger(seconds)) return null;
  return BigInt(seconds) * 1_000_000_000n + BigInt((match[2] ?? "0").padEnd(9, "0"));
}

/** Compares two times for a sort: the earlier first (swap the arguments for the latest first); none is the earliest. */
export function byTime(a?: string | null, b?: string | null): number {
  return timeMs(a) - timeMs(b);
}
