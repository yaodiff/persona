export interface HubAddress {
  readonly url: URL;
  /** Path prefix, including a trailing slash; empty at the root. */
  readonly prefix: string;
}

/** The hub address a person entered. Never accepts a credential, fragment, non-network scheme or opaque URL. */
export function parseHubAddress(input: string): HubAddress {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 2048 || /[\u0000- \u007f]/.test(trimmed)) throw new Error("Enter the hub's web address.");
  const withScheme = /^[a-z][a-z\d+.-]*:/i.test(trimmed) ? trimmed : `https://${trimmed}`;
  const url = new URL(withScheme);
  if (url.protocol !== "https:" && url.protocol !== "http:") throw new Error("The hub address must use HTTP or HTTPS.");
  if (!url.hostname || url.username || url.password || url.hash) throw new Error("The hub address cannot contain credentials or a fragment.");
  url.pathname = `${url.pathname.replace(/\/+$/, "")}/`;
  url.search = "";
  return { url, prefix: url.pathname === "/" ? "" : url.pathname };
}

/** A page of the configured hub, including when a reverse proxy mounts it below the origin root. */
export function inHub(hub: HubAddress, candidate: string | URL): boolean {
  let url: URL;
  try { url = typeof candidate === "string" ? new URL(candidate) : candidate; } catch { return false; }
  return url.origin === hub.url.origin && url.pathname.startsWith(hub.url.pathname);
}

/** An external link is opened only by the system browser, never given a new Electron window. */
export function externalBrowserURL(candidate: string): string | undefined {
  try {
    const url = new URL(candidate);
    return (url.protocol === "https:" || url.protocol === "http:") && !url.username && !url.password ? url.href : undefined;
  } catch { return undefined; }
}

export interface DesktopDeepLink {
  readonly kind: "open" | "pair-link" | "pair-app";
  readonly url: string;
  readonly route?: string;
}

/** Deep links are data, never automatic pairing or navigation to an arbitrary web origin. */
export function parseDeepLink(raw: string): DesktopDeepLink | undefined {
  let url: URL;
  try { url = new URL(raw); } catch { return undefined; }
  if (url.protocol !== "persona:" && url.protocol !== "jarvis:") return undefined;
  if (url.username || url.password || url.port) return undefined;
  if (url.host === "link") return { kind: "pair-link", url: raw };
  if (url.host === "pair") return { kind: "pair-app", url: raw };
  if (url.host !== "open" || url.hash || url.searchParams.size !== 1) return undefined;
  const route = url.searchParams.get("route");
  if (!route || !route.startsWith("/") || route.startsWith("//") || route.includes("\\") || /[\u0000-\u001f\u007f]/.test(route)) return undefined;
  try {
    const target = new URL(route, "https://desktop.invalid/");
    if (target.origin !== "https://desktop.invalid") return undefined;
  } catch { return undefined; }
  return { kind: "open", url: raw, route };
}

export function hubRoute(hub: HubAddress, route: string): string {
  const target = new URL(hub.url);
  const clean = route.replace(/^\/+/, "");
  target.pathname = `${target.pathname}${clean}`;
  return target.href;
}
