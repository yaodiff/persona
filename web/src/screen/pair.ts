// The pair one RFB connection carries for the live screen's video channel (#3954, #6558): 16 random bytes in hex, a new
// one each connection, as the iPhone makes them. A module of its own so the screen page's chunk doesn't pull in the
// video chunk's code (./video-stream) to make it.
export function newPair(): string {
  return [...crypto.getRandomValues(new Uint8Array(16))].map((b) => b.toString(16).padStart(2, "0")).join("");
}
