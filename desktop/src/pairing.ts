import type { HubAddress } from "./urls.js";

/** The hub-hosted login exchanges a temporary app-pairing token for a device credential in its own encrypted session. */
export function appPairingURL(hub: HubAddress, raw: string): string | undefined {
  let link: URL;
  try { link = new URL(raw); } catch { return undefined; }
  if (link.protocol !== "persona:" && link.protocol !== "jarvis:") return undefined;
  if (link.host !== "pair" || link.username || link.password || link.port || link.hash) return undefined;
  const address = link.searchParams.get("url");
  const token = link.searchParams.get("token");
  if (!address || !token || address.length > 2048 || token.length > 2048 || /[\u0000- \u007f]/.test(token)) return undefined;
  let supplied: URL;
  try { supplied = new URL(address); } catch { return undefined; }
  if ((supplied.protocol !== "https:" && supplied.protocol !== "http:") || supplied.origin !== hub.url.origin || supplied.username || supplied.password || supplied.hash) return undefined;
  const target = new URL(hub.url);
  target.searchParams.set("token", token);
  // Never log or expose this temporary token to the main process. The web takes it out of its address bar on startup.
  return target.href;
}
