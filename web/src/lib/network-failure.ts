// Telling "the request never reached the hub" apart from a bug in the page. fetch rejects a failed connection with a
// TypeError, and so does JavaScript for a bad property access: checking `instanceof TypeError` alone called a page's
// own crash (a row with a field it did not expect) "Can't reach the hub", and Retry could never help.

/** fetch rejected, or the answer's body broke off before its end: nothing usable came back from the hub. A TypeError,
 *  as fetch's own is, so code that already counts a TypeError as a gap keeps working; `cause` is the browser's error. */
export class HubUnreachableError extends TypeError {
  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause), { cause });
    this.name = "HubUnreachableError";
  }
}

// What browsers and runtimes say when a fetch fails for want of a connection: Chromium, Firefox, Safari, Node / undici,
// React Native's polyfill. Only for a TypeError that did not come through api() (lib/api.ts wraps its own).
const FETCH_FAILURE = /^(?:failed to fetch|load failed|networkerror when attempting to fetch resource\.?|network ?error|network request failed|the network connection was lost\.?|fetch failed)\b/i;

/** The request failed to reach the hub or to bring its answer back, as opposed to a program error in this page. */
export function isNetworkFailure(e: unknown): boolean {
  return e instanceof HubUnreachableError || (e instanceof TypeError && FETCH_FAILURE.test(e.message.trim()));
}

/** A mistake in this page's own code (a property of undefined, a value of the wrong kind), not anything the hub said. */
export function isProgramError(e: unknown): e is Error {
  return (e instanceof TypeError || e instanceof ReferenceError || e instanceof RangeError) && !isNetworkFailure(e);
}
