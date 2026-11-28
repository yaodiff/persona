// An HTML file previewed in the file viewer (docs/web/web.md "HTML preview", #1413; iOS / Mac: #1297): the page goes into a
// opaque-origin srcdoc frame with scripts and the network off until the user turns them on for this file.
// Pure; html-preview.test.ts.

import { decodeURIComponentSafe } from "./uri";

export interface PreviewPolicy {
  scripts: boolean;
  network: boolean;
}

/** The frame's sandbox: popups for links always (they open a new tab, outside the sandbox), scripts only when asked. */
export function sandboxFor(p: PreviewPolicy): string {
  const flags = ["allow-popups", "allow-popups-to-escape-sandbox"];
  if (p.scripts) flags.push("allow-scripts", "allow-forms", "allow-modals");
  return flags.join(" ");
}

/** The Content-Security-Policy put in front of the page: without the network only what the file itself holds loads. */
export function cspFor(p: PreviewPolicy): string {
  if (p.network) return "";
  const script = p.scripts ? "'unsafe-inline' 'unsafe-eval'" : "'none'";
  return `default-src 'none'; img-src data: blob:; media-src data: blob:; font-src data:; style-src 'unsafe-inline' data:; script-src ${script}; form-action 'none'`;
}

/**
 * The page with the policy and `<base target="_blank">` put first, after the doctype (before it would put the page in
 * quirks mode; the parser moves a leading <meta> into <head> anyway, ahead of anything the page loads).
 */
export function previewDocument(html: string, p: PreviewPolicy): string {
  const csp = cspFor(p);
  const inject = `${csp ? `<meta http-equiv="Content-Security-Policy" content="${csp}">` : ""}<base target="_blank">`;
  const m = /^\s*(?:<\?xml[^>]*>\s*)?(<!doctype[^>]*>)?/i.exec(html)!;
  return html.slice(0, m[0].length) + inject + html.slice(m[0].length);
}

/** How many references the page makes to files beside it (they cannot load in the frame, which has no address). */
export function relativeRefs(html: string): number {
  let n = 0;
  for (const m of html.matchAll(/\s(?:src|href)\s*=\s*["']?([^"'\s>]+)/gi)) {
    const url = m[1];
    if (!/^(?:[a-z][a-z0-9+.-]*:|#|\/\/)/i.test(url)) n++;
  }
  return n;
}

/** A reference to a file beside the page (not a URL, an anchor or a protocol-relative address). */
export function isRelativeRef(url: string): boolean {
  const u = url.trim();
  return u !== "" && !/^(?:[a-z][a-z0-9+.-]*:|#|\/)/i.test(u);
}

/** Where a relative reference points, from the folder of the file that makes it ("a/b/../c.png?v=1" → "dir/a/c.png"). */
export function resolveRelative(dir: string, ref: string): string {
  const clean = decodeURIComponentSafe(ref.trim().replace(/[?#].*$/, ""));
  const parts = dir.replace(/\/+$/, "").split("/");
  for (const seg of clean.split("/")) {
    if (seg === "..") {
      if (parts.length > 1) parts.pop();
    } else if (seg !== "." && seg !== "") parts.push(seg);
  }
  return parts.join("/") || "/";
}

/** Every url(...) in a style sheet, rewritten by `to` (which returns the replacement, or null to leave it). */
export function rewriteCssUrls(css: string, to: (url: string) => string | null): string {
  return css.replace(/url\(\s*(['"]?)([^'")]+)\1\s*\)/gi, (m, _q: string, url: string) => {
    const r = to(url);
    return r === null ? m : `url("${r}")`;
  });
}

/** `@import "x.css"` / `@import url(x.css)` targets in a style sheet. */
export function cssImports(css: string): string[] {
  return [...css.matchAll(/@import\s+(?:url\(\s*)?['"]?([^'")\s;]+)/gi)].map((m) => m[1]);
}
