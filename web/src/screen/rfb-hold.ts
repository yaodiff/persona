// Holding noVNC's framebuffer update requests while the live screen's video shows (#6558), as the iPhone holds its RFB
// client's: Screen Sharing then encodes nothing for a picture nobody sees. noVNC has no API for it; every request goes
// through its static `RFB.messages.fbUpdateRequest(sock, …)`, so a wrapper drops the requests of a held connection and,
// when the hold ends, sends the last one it dropped, which starts noVNC's request loop again and lets the picture catch
// up. A noVNC without that function (another version) can't be held: video still shows, and RFB keeps updating
// underneath, more bandwidth for the same picture. The class is the connection's own (`rfb.constructor`), so this chunk
// doesn't import noVNC (it stays in the screen page's chunk).

type Request = (sock: unknown, incremental: boolean, x?: number, y?: number, w?: number, h?: number) => void;
type Messages = { fbUpdateRequest?: Request };

const holds = new WeakMap<object, { last: unknown[] | null }>();
let original: Request | null = null;

function messages(rfb: object): Messages | null {
  const m = (rfb.constructor as { messages?: Messages } | undefined)?.messages;
  return m && typeof m.fbUpdateRequest === "function" ? m : null;
}

function install(m: Messages): boolean {
  if (original) return true;
  const send = m.fbUpdateRequest;
  if (!send) return false;
  original = send;
  m.fbUpdateRequest = function (this: unknown, sock: unknown, ...rest: unknown[]) {
    const held = sock && typeof sock === "object" ? holds.get(sock) : undefined;
    if (held) {
      held.last = [sock, ...rest];
      return;
    }
    return send.call(this, sock as never, ...(rest as [boolean, number?, number?, number?, number?]));
  };
  return true;
}

/** Holds (or releases) `rfb`'s update requests. False when this noVNC can't be held, and nothing is changed. */
export function holdUpdates(rfb: object, on: boolean): boolean {
  const sock = (rfb as { _sock?: unknown })._sock;
  const m = messages(rfb);
  if (!m || !sock || typeof sock !== "object" || !install(m)) return false;
  if (on) {
    if (!holds.has(sock)) holds.set(sock, { last: null });
    return true;
  }
  const held = holds.get(sock);
  holds.delete(sock);
  if (held?.last && original) original.apply(m, held.last as never);
  return true;
}
