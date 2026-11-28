// Shared non-first-screen URL helpers for hub `/api/` media paths and same-origin `blob:` previews.
import { BASE } from "./base";

export const MEDIA_CTRL_CHARS = /[\u0000-\u001f\u007f]/;

function hasPathTraversal(rawPath: string): boolean {
  if (rawPath.includes("//")) return true;
  let cur = rawPath;
  for (let pass = 0; pass < 4; pass++) {
    if (cur.includes("\\") || /%5c/i.test(cur)) return true;
    // Split on literal '/', encoded '%2f', backslash, and '%5c' so '..%2f', '%2e%2e%2f', and
    // multi-encoded '%252e%252e' are rejected while non-dot '%2F' IDs (e.g. 'a%2Fb') stay valid.
    const parts = cur.split(/\/|%2f|\\|%5c/i);
    if (parts.some((seg) => seg === "." || seg === "..")) return true;
    if (pass === 3) break;
    let next: string;
    if (pass === 0) {
      try {
        next = decodeURIComponent(cur);
      } catch {
        return true;
      }
    } else {
      next = cur.replace(/%([0-9a-f]{2})/gi, (_, h: string) => String.fromCharCode(parseInt(h, 16)));
    }
    if (next === cur) break;
    cur = next;
  }
  return false;
}

/** Validates a relative hub `/api/` path (optionally under `base`) against control chars, fragments,
 *  backslashes, double slashes, and literal/encoded dot-segment traversal before appending auth tokens. */
export function safeApiPath(raw: string, prefix = "/api/", base = BASE): string | undefined {
  const s = raw.trim();
  if (!s || !s.startsWith("/") || MEDIA_CTRL_CHARS.test(s) || s.includes("\\") || s.includes("#")) return undefined;
  const q = s.indexOf("?");
  const rawPath = q >= 0 ? s.slice(0, q) : s;
  const prefixed = rawPath.startsWith(prefix) || (!!base && rawPath.startsWith(`${base}${prefix}`));
  if (!prefixed || hasPathTraversal(rawPath)) return undefined;
  try {
    const u = new URL(s, "https://hub.invalid");
    if (u.origin !== "https://hub.invalid") return undefined;
    if (!u.pathname.startsWith(prefix) && !(base && u.pathname.startsWith(`${base}${prefix}`))) return undefined;
    return s;
  } catch {
    return undefined;
  }
}

/** Whether `raw` is a control-char-free `blob:` URL belonging strictly to this page's origin. */
export function isSameOriginBlobURL(raw: string): boolean {
  if (!raw || MEDIA_CTRL_CHARS.test(raw) || typeof location === "undefined" || location.origin === "null") return false;
  return raw.startsWith(`blob:${location.origin}/`);
}
