import { createHash } from "node:crypto";
import type { HubAddress } from "./urls.js";

/** Web localStorage is origin-scoped: two reverse-proxy prefixes on one origin need separate Electron sessions. */
export function hubPartition(hub: HubAddress): string {
  return `persist:hub-${createHash("sha256").update(hub.url.href).digest("hex").slice(0, 32)}`;
}
