// The web pages a finished exchange used (#9132; drawn by views/chat/TurnSources.tsx): the pages its web calls opened,
// then the results its searches returned. The node reads a folded run the same way (server/internal/transcript/
// fold_sources.go), and both read spec/fixtures/fold-sources, so the row is the same before and after a reload.
import type { Step } from "./turns";
import type { FoldSource, Item } from "./types";

/** As proto.FoldSourcesMax. */
export const MAX_SOURCES = 10;
const TITLE_MAX = 200;
const CLIP_NOTE = "\n…（已截断"; // i18n-ignore: the hub's note ending a cut result (transcript/clip.go)

/** An http(s) URL with something after the scheme and no white space. */
export function webURL(s: string): boolean {
  return /^https?:\/\/\S+$/i.test(s);
}

/** A title trimmed, at most 200 characters (an ellipsis ending a cut one). */
function sourceTitle(s: string | undefined): string {
  const t = (s ?? "").trim();
  const chars = Array.from(t);
  return chars.length <= TITLE_MAX ? t : `${chars.slice(0, TITLE_MAX - 1).join("")}…`;
}

/** The complete {"title","url"} entries of the JSON array `s` starts with, read until one is cut or broken. */
function linksList(s: string): FoldSource[] {
  const out: FoldSource[] = [];
  let i = s.indexOf("[");
  if (i !== 0) return out;
  i++;
  for (;;) {
    while (i < s.length && /[\s,]/.test(s[i])) i++;
    if (s[i] !== "{") return out;
    // the entry's end: its closing brace outside strings
    let depth = 0, inString = false, end = -1;
    for (let j = i; j < s.length; j++) {
      const c = s[j];
      if (inString) {
        if (c === "\\") j++;
        else if (c === '"') inString = false;
      } else if (c === '"') inString = true;
      else if (c === "{") depth++;
      else if (c === "}" && --depth === 0) {
        end = j;
        break;
      }
    }
    if (end < 0) return out;
    try {
      const e = JSON.parse(s.slice(i, end + 1)) as { title?: unknown; url?: unknown };
      out.push({ url: typeof e.url === "string" ? e.url : "", title: typeof e.title === "string" ? e.title : "" });
    } catch {
      return out;
    }
    i = end + 1;
  }
}

/** The pages a web search's result names. In a result the hub cut (`full_size`, its note at the end) a "Links:" list
 *  keeps its entries before the cut one, and a Codex list loses its last line, which may be cut. */
function searchResults(res: Item): FoldSource[] {
  let text = res.text ?? "";
  const note = text.lastIndexOf(CLIP_NOTE);
  const cut = Boolean(res.full_size) && note >= 0;
  if (cut) text = text.slice(0, note);
  let lines = text.split("\n");
  const linked = lines.filter((l) => l.startsWith("Links: "));
  if (linked.length) return linked.flatMap((l) => linksList(l.slice("Links: ".length)));
  // Codex: one "title  url" line each (server/internal/transcript/codex.go codexWebSearch)
  if (cut) lines = lines.slice(0, -1);
  const out: FoldSource[] = [];
  for (const raw of lines) {
    const line = raw.trim();
    if (webURL(line)) out.push({ url: line });
    else {
      const i = line.lastIndexOf("  ");
      if (i >= 0) out.push({ url: line.slice(i + 2).trim(), title: line.slice(0, i) });
    }
  }
  return out;
}

/** The pages an exchange's own web calls used, or null when there are none. Work the hub folded brings its fold's
 *  `sources` (first, in order); calls on the page add the pages they opened (WebFetch), then their search results
 *  (WebSearch). Each URL once (a later mention gives one without a title its title), at most MAX_SOURCES. */
export function turnSources(steps: readonly Step[]): FoldSource[] | null {
  const folded: FoldSource[] = [];
  const opened: FoldSource[] = [];
  const searched: FoldSource[] = [];
  for (const s of steps) {
    if (s.kind === "fold") folded.push(...(s.item.fold?.sources ?? []));
    if (s.kind !== "tool" || !s.result || s.result.is_error) continue;
    const name = s.use.tool_name;
    const input = s.use.tool_input as { url?: unknown } | undefined;
    if (name === "WebFetch" && typeof input?.url === "string") opened.push({ url: input.url });
    else if (name === "WebSearch") searched.push(...searchResults(s.result));
  }
  const out: FoldSource[] = [];
  const at = new Map<string, number>();
  for (const e of [...folded, ...opened, ...searched]) {
    const url = e.url.trim();
    const title = sourceTitle(e.title);
    if (!webURL(url)) continue;
    const i = at.get(url);
    if (i !== undefined) {
      if (!out[i].title && title) out[i].title = title;
      continue;
    }
    at.set(url, out.length);
    out.push(title ? { url, title } : { url });
  }
  return out.length ? out.slice(0, MAX_SOURCES) : null;
}
