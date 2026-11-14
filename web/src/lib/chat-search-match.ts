// Search in chat's matching (docs/web/web.md "Search in chat", Android ChatSearch.kt, parity N26): literal,
// case-insensitive matching over what is loaded of a chat (words, quoted contexts, thoughts, tool names, tool input and
// output, system events), newest first, at most SEARCH_LIMIT hits. What of an item is searched is the shared rule of
// spec/fixtures/chat-search: what the chat can show, never a hidden tag (#6281). Loaded with the search panel only.
import { assistantBlocks, cardFallback, cardReply } from "./assistant-blocks";
import type { ChatSearchHit, ChatSearchResults, SearchCategory } from "./chat-search";
import { injectedOf } from "./injected";
import type { Item } from "./types";
import { parseUserText, sortItems } from "./turns";

export const SEARCH_LIMIT = 100;
/** How much of the text shows before and after the first match. */
const BEFORE = 45;
const AFTER = 110;

/** Every string in a tool's input, in order (keys are not searched). */
function strings(v: unknown): string[] {
  if (typeof v === "string") return [v];
  if (Array.isArray(v)) return v.flatMap(strings);
  if (v && typeof v === "object") return Object.values(v).flatMap(strings);
  return [];
}

/** A reply's text with each card as the words it falls back to (sdk/go/wire AssistantCardText): its fallback, an old
 *  persona-card's title, never its JSON. */
export function assistantCardText(markdown: string): string {
  if (!markdown.includes("persona-")) return markdown;
  const blocks = assistantBlocks(markdown);
  if (!blocks.length) return markdown;
  let out = "";
  let at = 0;
  for (const b of blocks) {
    let raw: unknown;
    try {
      raw = JSON.parse(b.raw);
    } catch {
      raw = undefined;
    }
    const title = b.language === "persona-card" && raw && typeof raw === "object" && typeof (raw as { title?: unknown }).title === "string" ? (raw as { title: string }).title.trim() : "";
    out += markdown.slice(at, b.start) + (title || cardFallback(raw)) + "\n";
    at = b.end;
  }
  return out + markdown.slice(at);
}

/** What of an item is searched, and as what (spec/fixtures/chat-search): what the chat can show of it. */
export function searchFields(item: Item): [SearchCategory, string][] {
  const out: [SearchCategory, string][] = [];
  const add = (c: SearchCategory, text?: string) => text?.trim() && out.push([c, text]);
  switch (item.kind) {
    case "text": {
      if (item.role !== "user") {
        add("text", assistantCardText(item.text ?? ""));
        break;
      }
      // a message Persona wrote for the model: the line the chat shows of it (and what a scheduled task asks), never
      // its words for the model
      const injected = injectedOf(item);
      if (injected) {
        add("event", [injected.line, injected.detail].filter(Boolean).join("\n"));
        break;
      }
      // your messages: their words without the tags contexts, files, photos and a card's answer travel in; a quoted
      // context (its chip opens to it) and a file's name as shown
      const p = parseUserText(item.text ?? "");
      add("text", cardReply(p.text)?.text ?? p.text);
      for (const c of p.contexts) add("quoted", [c.title, c.text].filter(Boolean).join("\n"));
      for (const f of p.files) add("text", f.name);
      break;
    }
    case "thinking":
      add("thinking", item.text);
      break;
    case "tool_use":
      add("tool_input", item.text);
      add("tool_name", item.tool_name);
      add("tool_input", strings(item.tool_input).join("\n"));
      break;
    case "tool_result":
      add("tool_output", item.text);
      break;
    case "notice":
    case "recap":
      add("event", item.text);
      break;
    default:
      return out;
  }
  if (item.task) add("event", [item.task.title, item.task.detail, item.task.status].filter(Boolean).join("\n"));
  return out;
}

/** Case-insensitive indexOf that keeps the text's own offsets (a lowercase form of another length matches exactly). */
function finder(needle: string) {
  const low = needle.toLowerCase();
  return (text: string, from = 0): number => {
    const t = text.toLowerCase();
    return t.length === text.length && low.length === needle.length ? t.indexOf(low, from) : text.indexOf(needle, from);
  };
}

const isLow = (s: string, i: number) => {
  const c = s.charCodeAt(i);
  return c >= 0xdc00 && c <= 0xdfff;
};

export function searchChat(items: Item[], query: string): ChatSearchResults {
  const needle = query.trim();
  if (!needle) return { hits: [], truncated: false };
  const find = finder(needle);
  const seen = new Set<string>();
  const hits: ChatSearchHit[] = [];
  const unique = new Map<string, Item>();
  for (const item of items) {
    // ChatView puts live items before cached fold steps: an exact duplicate keeps that first copy; a newer seq wins below.
    const key = `${item.id}\0${item.seq}`;
    if (!unique.has(key)) unique.set(key, item);
  }
  for (const item of sortItems([...unique.values()]).reverse()) {
    if (seen.has(item.id)) continue;
    seen.add(item.id);
    for (const [category, text] of searchFields(item)) {
      const first = find(text);
      if (first < 0) continue;
      if (hits.length === SEARCH_LIMIT) return { hits, truncated: true };
      let start = Math.max(0, first - BEFORE);
      let end = Math.min(text.length, first + needle.length + AFTER);
      if (start > 0 && isLow(text, start)) start--;
      if (end < text.length && isLow(text, end)) end++;
      const snippet = (start > 0 ? "…" : "") + text.slice(start, end).replace(/\s*\n\s*/g, " ") + (end < text.length ? "…" : "");
      const ranges: [number, number][] = [];
      for (let at = find(snippet); at >= 0; at = find(snippet, at + needle.length)) ranges.push([at, at + needle.length]);
      hits.push({ itemId: item.id, category, snippet, ranges });
    }
  }
  return { hits, truncated: false };
}

/** The hits by category, the categories in the order they first come. */
export function groupHits(hits: ChatSearchHit[]): [SearchCategory, ChatSearchHit[]][] {
  const groups = new Map<SearchCategory, ChatSearchHit[]>();
  for (const h of hits) groups.set(h.category, [...(groups.get(h.category) ?? []), h]);
  return [...groups];
}

/** The snippet cut at its matches: [text, matched] pieces. */
export function snippetParts(hit: ChatSearchHit): [string, boolean][] {
  const out: [string, boolean][] = [];
  let at = 0;
  for (const [a, b] of hit.ranges) {
    if (a > at) out.push([hit.snippet.slice(at, a), false]);
    out.push([hit.snippet.slice(a, b), true]);
    at = b;
  }
  if (at < hit.snippet.length) out.push([hit.snippet.slice(at), false]);
  return out;
}
