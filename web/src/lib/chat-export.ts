// Exporting a conversation as Markdown (#7375, docs/web/web.md "Exporting a conversation"): the whole chat read from the
// hub page by page, whatever the page holds, and written as one document people can keep, paste or file. The rules of
// the document are here, pure, with the words passed in, so spec/fixtures/chat-export (and later the apps) can check
// the same file; the dialog (views/chat/ExportDialog.tsx) reads, shows the progress and saves.
import type { Item, ItemsPage } from "./types";

/** The words the document uses, in the reader's language (the dialog passes them from chat-export). */
export interface ExportWords {
  you: string;
  persona: string;
  /** A message another session sent (cross-session messaging, Item.from). */
  from: (name: string) => string;
  computer: string;
  folder: string;
  agent: string;
  exported: string;
  thinking: string;
  input: string;
  result: string;
  error: string;
  image: string;
  /** Text the hub keeps cut and could not give whole: how much is left out. */
  cut: (characters: number) => string;
  /** The document stops before the conversation's start: where it begins. */
  partial: (from: string) => string;
}

/** What the document says before the messages. `agent`: who writes the replies (Claude Code, the assistant's name). */
export interface ExportMeta {
  title: string;
  agent: string;
  computer?: string;
  folder?: string;
  /** When it was exported, as shown. */
  exported: string;
}

export interface MarkdownOptions {
  /** Tool calls with their input and result, thinking, and what the agent said between replies. */
  work: boolean;
  words: ExportWords;
  /** A message's time as shown (the item's ts); local "YYYY-MM-DD HH:MM" when not given. */
  time?: (ts: string) => string;
  /** The document starts after the conversation's own start (the reader stopped at its limit). */
  partial?: boolean;
}

/** At most this much of a tool's result is kept in the document; the rest is said to be cut. */
export const RESULT_MAX = 4000;
/** At most this much of a tool's input, as JSON. */
export const INPUT_MAX = 4000;

const pad = (n: number) => String(n).padStart(2, "0");

/** "YYYY-MM-DD HH:MM" in this browser's time zone; "" for a time it cannot read. */
export function localTime(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A fenced block of `text` whose fence no run of backticks inside can close. */
export function fenced(text: string, lang = ""): string {
  const longest = Math.max(0, ...[...text.matchAll(/`+/g)].map((m) => m[0].length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  return `${fence}${lang}\n${text.replace(/\n+$/, "")}\n${fence}`;
}

/** One line for a `<summary>`: no markup that would end it, at most `max` characters. */
function summaryText(s: string, max = 80): string {
  const line = s.replace(/\s+/g, " ").trim().replace(/[<>&]/g, (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;" })[c]!);
  return [...line].length > max ? `${[...line].slice(0, max - 1).join("")}…` : line;
}

// The field of a tool's input that says what it did: Bash's command, Grep's and Glob's pattern before the folder they
// search, Read's and Edit's file, a fetch's URL…
const SUMMARY_FIELDS = ["command", "pattern", "file_path", "path", "url", "query", "description", "prompt"];

/** A tool call's one line: its name and what it was given. */
export function toolSummary(it: Pick<Item, "tool_name" | "tool_input">): string {
  const input = it.tool_input ?? {};
  const field = SUMMARY_FIELDS.map((k) => input[k]).find((v) => typeof v === "string" && v.trim());
  const name = summaryText(it.tool_name || "tool", 40);
  return field ? `${name}: ${summaryText(field as string)}` : name;
}

/** At most `max` characters of `text`, and how many more there were (counting what the hub had cut: `full`). */
function clip(text: string, max: number, full?: number): { text: string; left: number } {
  const chars = [...text];
  const total = Math.max(full ?? 0, chars.length);
  if (chars.length <= max) return { text, left: total - chars.length };
  return { text: chars.slice(0, max).join(""), left: total - max };
}

/** The conversation as Markdown: a heading with what it is, then each message under who wrote it and when. Replies in a
 *  row share one heading; a message of yours is one heading with its pictures. Work steps (`work`) go inside the
 *  agent's part as collapsible blocks, each tool call with its result; without them only the conversation is written. */
export function transcriptMarkdown(items: readonly Item[], meta: ExportMeta, o: MarkdownOptions): string {
  const w = o.words;
  const time = o.time ?? localTime;
  const out: string[] = [`# ${meta.title.replace(/\s+/g, " ").trim()}`, ""];
  const facts: [string, string | undefined][] = [[w.agent, meta.agent], [w.computer, meta.computer], [w.folder, meta.folder], [w.exported, meta.exported]];
  for (const [k, v] of facts) if (v?.trim()) out.push(`- ${k}: ${v.trim()}`);
  out.push("");
  const first = items.find((i) => i.ts && time(i.ts));
  if (o.partial && first?.ts) out.push(`> ${w.partial(time(first.ts))}`, "");

  // each tool call's result, so the call is written with it
  const results = new Map<string, Item>();
  if (o.work) for (const i of items) if (i.kind === "tool_result" && i.tool_use_id) results.set(i.tool_use_id, i);
  const written = new Set<string>();
  let speaker = ""; // the heading the last block went under: a new one only when it changes
  const block = (text: string) => out.push(text, "");
  const heading = (key: string, label: string, ts?: string) => {
    if (key === speaker) return;
    speaker = key;
    const at = ts ? time(ts) : "";
    out.push(`## ${label}${at ? ` · ${at}` : ""}`, "");
  };
  const agentHeading = (it: Item) => heading("agent", meta.agent, it.ts);
  const cutNote = (left: number) => (left > 0 ? `\n\n_${w.cut(left)}_` : "");

  for (const it of items) {
    if (it.kind === "fold" || it.kind === "recap") continue; // the folded work (off), the while-you-were-away recap
    if (it.role === "user" && (it.kind === "text" || it.kind === "image")) {
      const label = it.injected ? w.persona : it.from ? w.from(it.from) : w.you;
      // one message's items (its words and pictures) share the id the hub tied them by
      heading(`user:${label}:${it.client_msg_id ?? it.id}`, label, it.ts);
      if (it.kind === "image") block(`_[${w.image}]_`);
      else if (it.injected) block([it.injected.line, it.injected.detail].filter((s) => s?.trim()).join("\n\n"));
      else if (it.text?.trim()) block(it.text.trim() + cutNote((it.full_size ?? 0) - [...it.text].length));
      continue;
    }
    if (it.kind === "notice" || it.role === "system") {
      if (!it.text?.trim()) continue;
      speaker = ""; // a line of its own between the parts
      block(`_${it.text.trim().replace(/\n+/g, " ")}_`);
      continue;
    }
    // the agent's part
    if (it.kind === "text") {
      if (!it.text?.trim()) continue;
      agentHeading(it);
      block(it.text.trim() + cutNote((it.full_size ?? 0) - [...it.text].length));
    } else if (it.kind === "image") {
      agentHeading(it);
      block(`_[${w.image}]_`);
    } else if (!o.work) {
      continue;
    } else if (it.kind === "thinking") {
      if (!it.text?.trim()) continue;
      agentHeading(it);
      block(`<details>\n<summary>${summaryText(w.thinking)}</summary>\n\n${it.text.trim()}\n\n</details>`);
    } else if (it.kind === "tool_use") {
      agentHeading(it);
      const parts = [`<details>\n<summary>${toolSummary(it)}</summary>`, ""];
      if (it.tool_input && Object.keys(it.tool_input).length) {
        const input = clip(JSON.stringify(it.tool_input, null, 2), INPUT_MAX);
        parts.push(`**${w.input}**`, "", fenced(input.text, "json") + cutNote(input.left), "");
      }
      const result = it.tool_use_id ? results.get(it.tool_use_id) : undefined;
      if (result) {
        written.add(result.id);
        parts.push(...resultParts(result, w, cutNote));
      }
      parts.push("</details>");
      block(parts.join("\n"));
    } else if (it.kind === "tool_result" && !written.has(it.id)) {
      // a result whose call is not in the document (the call came before where it starts)
      agentHeading(it);
      block([`<details>\n<summary>${summaryText(it.is_error ? w.error : w.result)}</summary>`, "", ...resultParts(it, w, cutNote), "</details>"].join("\n"));
    }
  }
  return out.join("\n").replace(/\n{3,}/g, "\n\n").trimEnd() + "\n";
}

function resultParts(result: Item, w: ExportWords, cutNote: (left: number) => string): string[] {
  const body = clip(result.text ?? "", RESULT_MAX, result.full_size);
  if (!body.text.trim() && body.left <= 0) return [`**${result.is_error ? w.error : w.result}**`, ""];
  return [`**${result.is_error ? w.error : w.result}**`, "", fenced(body.text) + cutNote(body.left), ""];
}

/** The file's name: the title without what file systems refuse, and the day; "conversation" when it has no title. */
export function exportFileName(title: string, day: Date): string {
  const base = title
    .replace(/[\u0000-\u001f\u007f/\\:*?"<>|]+/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^\.+/, "");
  const name = [...(base || "conversation")].slice(0, 80).join("").trim();
  return `${name} ${day.getFullYear()}-${pad(day.getMonth() + 1)}-${pad(day.getDate())}.md`;
}

/** Reading a conversation stopped: the hub rewrote its history twice while it was being read (a rewind, a
 *  compaction), or it was cancelled. */
export class ExportStopped extends Error {
  constructor(readonly why: "changed" | "cancelled") {
    super(why);
  }
}

export interface ReadOptions {
  /** With its work steps: the pages unfolded (no `fold_work`), so each tool call and thought is an item. */
  work: boolean;
  /** One page of items (`query` without the leading "?"), as GET …/items answers. */
  page: (query: string) => Promise<ItemsPage>;
  /** An item whole (GET …/items/{id}/full), for text the page carries cut. */
  full: (id: string) => Promise<Item>;
  /** How many items are in so far, as they come. */
  progress?: (items: number) => void;
  stopped?: () => boolean;
  /** Where reading stops before the start; the document says where it begins. */
  maxItems?: number;
  maxPages?: number;
  /** At most this many cut texts are read whole. */
  maxFull?: number;
}

/** The whole conversation, oldest first, from its latest page back to its first (`before_seq`), each message's text
 *  whole. History rewritten while reading (the pages' `history` changes) starts over once; `partial` is true when it
 *  stopped at its limit. Throws ExportStopped, or the hub's error. */
export async function readConversation(o: ReadOptions): Promise<{ items: Item[]; partial: boolean }> {
  const base = `turns=10&lazy_images=1${o.work ? "" : "&fold_work=1"}`;
  const maxItems = o.maxItems ?? 20_000;
  const maxPages = o.maxPages ?? 400;
  const check = () => {
    if (o.stopped?.()) throw new ExportStopped("cancelled");
  };
  for (let attempt = 0; ; attempt++) {
    check();
    const first = await o.page(base);
    const history = first.history;
    const bySeq = new Map<string, Item>();
    const add = (items: readonly Item[] | undefined) => items?.forEach((i) => bySeq.set(i.id, i));
    add(first.items);
    let more = Boolean(first.has_more) && (first.items?.length ?? 0) > 0;
    let pages = 1;
    let partial = false;
    let changed = false;
    o.progress?.(bySeq.size);
    while (more) {
      check();
      if (bySeq.size >= maxItems || pages >= maxPages) {
        partial = true;
        break;
      }
      const oldest = Math.min(...[...bySeq.values()].map((i) => i.seq));
      const p = await o.page(`${base}&before_seq=${oldest}`);
      if (p.history && history && p.history !== history) {
        changed = true;
        break;
      }
      const before = bySeq.size;
      add(p.items);
      pages++;
      more = Boolean(p.has_more) && bySeq.size > before; // a page that adds nothing would loop
      o.progress?.(bySeq.size);
    }
    if (changed) {
      if (attempt === 0) continue;
      throw new ExportStopped("changed");
    }
    const items = [...bySeq.values()].sort((a, b) => a.seq - b.seq);
    await wholeTexts(items, o);
    return { items, partial };
  }
}

/** Reads whole each message the pages carry cut (Item.full_size), three at a time, at most `maxFull`; one that fails
 *  stays cut (the document says how much is left out). Tool results stay cut: the document keeps only their start. */
async function wholeTexts(items: Item[], o: ReadOptions): Promise<void> {
  const cut = items
    .map((it, i) => ({ it, i }))
    .filter(({ it }) => it.full_size && (it.kind === "text" || (o.work && it.kind === "thinking")) && [...(it.text ?? "")].length < it.full_size)
    .slice(0, o.maxFull ?? 200);
  const worker = async () => {
    for (let next = cut.shift(); next; next = cut.shift()) {
      if (o.stopped?.()) throw new ExportStopped("cancelled");
      try {
        const whole = await o.full(next.it.id);
        if (typeof whole.text === "string") items[next.i] = { ...next.it, text: whole.text, full_size: undefined };
      } catch {
        // stays cut
      }
    }
  };
  await Promise.all([worker(), worker(), worker()]);
}
