import { CONTEXT_RE, FILE_RE, PERSONA_SERVERS, canonicalTool } from "./compatibility";
import { splitInlineTrailer } from "./inline-mentions";
// Items → what the chat shows (docs/apple/ios.md "Information architecture"): every user message opens a segment; the segment shows
// the message, one work row folding the tools / thoughts / notices / intermediate replies, and the agent's last
// words before the next message as the answer.

import { t } from "../i18n";
import { errorSignature, isRetryText, parseAPIError, type APIErrorInfo } from "./api-errors";
import { foldCompactions, isCompaction } from "./compaction";
import { engineSwitchOf, type EngineSwitch } from "./engine-switch";
import { injectedOf } from "./injected";
import { markModelChanges, type ModelChange } from "./model-change";
import { sourceKey, sourceOf, userSourceSentinel } from "./message-source";
import { attachedAgentNotices, taskCalls } from "./task-association";
import { addUsage, countUsage, itemUsage, markFirstUsage, type TurnUsage } from "./turn-usage";
import type { Botmux, Injected, Item, MessageSource, TokenUsage } from "./types";

export type Step =
  | { kind: "tool"; id: string; use: Item; result?: Item; images: Item[] }
  | { kind: "thinking"; id: string; item: Item }
  | { kind: "text"; id: string; item: Item }
  | { kind: "notice"; id: string; item: Item }
  | { kind: "image"; id: string; item: Item }
  | { kind: "visualization"; id: string; item: Item }
  | { kind: "fold"; id: string; item: Item };

export interface UserMessage {
  id: string;
  text: string;
  /** The optional inline reference map at the end of this message's wire body. */
  inlineTrailer?: string;
  images: Item[];
  queued?: boolean;
  ts?: string;
  contexts: Context[];
  /** Sent by another Claude session (cross-session messaging): its name. */
  from?: string;
  /** A finished exchange that took several messages: each message's text, shown in one bubble. */
  parts?: string[];
  /** The message sent from here it came from, as the hub tied it (Item.client_msg_id). */
  client_msg_id?: string;
  /** Files it carries as <persona-file> tags (videos sent from the phone…), drawn as cards. */
  files?: AttachedFile[];
  /** Paths of photos sent with it (`<attached_images>`), on the session's machine: thumbnails above the bubble. */
  photos?: string[];
  /** Persona typed it in for the model (a task's notice, a scheduled run…): one line, not a bubble (lib/injected.ts). */
  injected?: Injected;
  source?: MessageSource;
  /** The previous visible message has the same source and no intervening reply or work. */
  continuedSource?: boolean;
  /** Original input blocks, before display parsing or shortening. */
  rawInput?: Item[];
  /** A scheduled task's or follow-up's input while background rounds fold (docs/assistant/assistant.md "Background
   *  rounds"): only its one line is shown, never the prompt (`injected.detail`, `rawInput`). */
  promptHidden?: boolean;
  /** Inside an opened background round: drawn as its source block, not as the raw model input supervision shows. */
  sourceInTrace?: boolean;
  /** What botmux wrapped, as the node read it (Item.botmux): drawn as written in Lark while the chip decodes (lib/botmux.ts). */
  botmux?: Botmux;
  /** An explicitly promoted main-chat round keeps this conversation input behind its source disclosure by default. */
  promotedRelay?: boolean;
}

/** A <persona-file kind="video" path="…" name="…" size="…" duration="…"/> in a message (docs/protocol/protocol.md "Sending video"). */
export interface AttachedFile {
  kind: string; // video | audio | file
  path: string; // on the session's machine
  name: string;
  size?: number;
  duration?: number; // seconds
}

export interface Context {
  kind: string;
  title?: string;
  url?: string;
  text: string;
  /** Its other attributes as written (a forward's `msg` and metadata, a workspace's `machine`); absent when it has none. */
  attrs?: Record<string, string>;
}

export interface Segment {
  id: string;
  user?: UserMessage;
  steps: Step[]; // the work, in order (the answer is not among them)
  answer?: Item;
  start?: string;
  end?: string;
  tools: number;
  thoughts: number;
  replies: number;
  folds: Item[];
  /** Part of a supervision round: its visibility tools stay outside the ordinary work fold. */
  supervision?: boolean;
  /** Its reply came from another model than the previous visible reply (lib/model-change.ts): a divider before it. */
  modelChange?: ModelChange;
  /** Its message is the first the conversation's new engine got (#5054, lib/engine-switch.ts): the switch divider
   *  before it, with the handoff its block carried (never a context chip). */
  engineSwitch?: EngineSwitch;
  /** The exchange ended in a model API error or an interruption after everything in it (turnLines, lineError): its last
   *  words are not a finished reply, and say so (cindy's "This reply did not complete", #5314). */
  cut?: boolean;
  /** A settled user turn without a visible response or an explicit closing notice. */
  emptyResponse?: boolean;
  /** The transcript recorded an assistant reply consisting of whitespace. */
  blankReply?: boolean;
  /** What its items used (#5426, lib/turn-usage.ts): every item counts, those it does not show too (a tool's result, a
   *  Codex turn's duration, a stopped turn's notice); each subagent counts once in the chat, in the exchange that
   *  launched it (chatLines' lineUsage). Absent when none of them carries tokens. */
  usage?: TurnUsage;
  /** First observed streaming time-to-first-token (ms) in this exchange (proto.Item.TTFTMS, #12542). */
  ttftMs?: number;
}

function blockPart(id: string) {
  return Number(id.split("#")[1] ?? 0);
}

function blockOrder(a: Pick<Item, "id" | "seq">, b: Pick<Item, "id" | "seq">) {
  return a.seq - b.seq || (blockPart(a.id) || 0) - (blockPart(b.id) || 0);
}

/** Fold bounds are seq + numeric block, not seq alone: a final answer can share the fold's last line. */
export function insideFold(i: Item, f: Item) {
  if (!f.fold || blockOrder(i, f) < 0 || i.seq > f.fold.through_seq) return false;
  return i.seq < f.fold.through_seq || !f.fold.through_id || blockPart(i.id) <= blockPart(f.fold.through_id);
}

export function sortItems(items: Item[]): Item[] {
  return [...items].sort(blockOrder);
}

// Merge a batch into what we have, by id. A fold stands for a range of work: once any real step of that range
// is here (the fold was expanded, or a later page overlaps it) the fold goes.
export function mergeItems(have: Item[], batch: Item[]): Item[] {
  const byId = new Map(have.map((i) => [i.id, i]));
  for (const it of batch) {
    const old = byId.get(it.id);
    let next = old?.supervisionUser ? { ...it, supervisionUser: true } : it;
    if (old?.fold?.supervision_user && next.fold && old.fold.through_seq === next.fold.through_seq) next = { ...next, fold: { ...next.fold, supervision_user: true } };
    byId.set(it.id, next);
  }
  const all = [...byId.values()];
  const work = all.filter((i) => i.kind === "tool_use" || i.kind === "tool_result" || i.kind === "thinking" ||
    (i.kind === "text" && i.role === "assistant")); // text inside a fold's range is its narration; outside it is the answer
  const promotedFolds = all.filter((i) => i.fold?.supervision_user);
  const kept = all.filter((f) => f.kind !== "fold" || !f.fold || !work.some((i) => insideFold(i, f)));
  // An overlapping page may replace only part of a fold. Its authoritative promotion must survive that replacement,
  // even if the real visibility tool is on the part that has not arrived yet, or a later receipt says silent.
  if (!promotedFolds.length) return sortItems(kept);
  const realWork = new Set(work);
  return sortItems(kept.map((i) => realWork.has(i) && promotedFolds.some((f) => insideFold(i, f)) ? { ...i, supervisionUser: true } : i));
}

/** `have` without what a fold of this fresh page now stands for. Left in, a step kept from before the fold existed
 *  would make mergeItems drop the fold as if it had been expanded, and every other step in it would be lost. */
export function withoutFolded(have: Item[], page: Item[]): Item[] {
  const folds = page.filter((f) => f.fold);
  if (!folds.length) return have;
  return have.filter((i) => !((i.kind === "thinking" || (i.kind === "text" && i.role === "assistant") || i.kind === "fold" || i.tool_use_id) && folds.some((f) => insideFold(i, f))));
}

// Only the canonical persona-context tag is recognized.

const ATTR_RE = /(\w+)="([^"]*)"/g;

const NAMED_ENTITIES: Record<string, string> = { quot: '"', apos: "'", lt: "<", gt: ">", amp: "&" };

/** One pass, so an escaped entity stays itself; numeric ones too (Go's html.EscapeString writes `&#34;` and `&#39;`). */
function unescapeAttr(s: string) {
  return s.replace(/&(quot|apos|lt|gt|amp|#\d+|#[xX][0-9a-fA-F]+);/g, (m, e: string) => {
    if (e[0] !== "#") return NAMED_ENTITIES[e];
    const code = e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : Number(e.slice(1));
    return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : m;
  });
}

/** A tag's `name="value"` attributes, unescaped. */
export function tagAttrs(attrs: string): Record<string, string> {
  const a: Record<string, string> = {};
  for (const m of attrs.matchAll(ATTR_RE)) a[m[1]] = unescapeAttr(m[2]);
  return a;
}



const VIDEO = ["mp4", "m4v", "mov", "webm", "mkv"];
const AUDIO = ["mp3", "m4a", "aac", "wav", "flac", "ogg", "opus"];
const IMAGE = ["png", "jpg", "jpeg", "gif", "webp", "heic", "svg"];
const DOCS = ["pdf", "docx", "doc", "pptx", "ppt", "xlsx", "xls", "rtf", "csv", "ipynb", "html", "zip", "key", "pages", "numbers"];

/** What a file is for its card: video / audio play in place, the rest open in the viewer. */
export function mediaKindOf(path: string): string {
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  return VIDEO.includes(ext) ? "video" : AUDIO.includes(ext) ? "audio" : IMAGE.includes(ext) ? "image" : "file";
}

/**
 * Absolute paths (/… or ~/…) to media and documents an answer names, in order, at most 8: shown as cards under it when
 * they exist on the machine. Code files are left to the path links in the text.
 */
export function replyPaths(text: string): string[] {
  const exts = [...VIDEO, ...AUDIO, ...IMAGE, ...DOCS].join("|");
  // i18n-ignore: CJK punctuation around paths in the agent's reply
  const re = new RegExp(`(?:^|[\\s\`'"(（「:：])((?:~/|/(?!/))[^\\s\`'"()（）<>「」，。；！？]*\\.(?:${exts}))(?=$|[\\s\`'")）」，。；！？.,:;!?])`, "gim");
  const out: string[] = [];
  for (const m of text.matchAll(re)) if (!out.includes(m[1]) && out.length < 8) out.push(m[1]);
  return out;
}

/** 1:05, 12:00:03 */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds)); // down, as players show it (2.5 s is 0:02)
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return h ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

// The node's <persona-view-errors> feedback after the assistant's snapshot (its last rich views failed their check):
// for the model, never the user's words. A hub before #4315 gave it as a user item of its own, or before the message.
const VIEW_ERRORS_RE = /<persona-view-errors\b[^>]*>[\s\S]*?<\/persona-view-errors>\s*/g;

/** What a user line says without the view feedback the node put before it (#4315). */
export function withoutViewErrors(raw: string): string {
  return raw.includes("<persona-view-errors") ? raw.replace(VIEW_ERRORS_RE, "") : raw;
}

/** An older hub's user item holding nothing but that feedback: no message of the user's. */
export function viewFeedbackOnly(it: Item): boolean {
  return it.role === "user" && it.kind === "text" && !!it.text?.includes("<persona-view-errors") && !withoutViewErrors(it.text).trim();
}

/** The hub's mid-turn interject note (server/internal/prompts/assistant/interject.md): instructions for the model
 *  inside `<persona-context kind="context" title="插话">`, never a quote the owner picked, so it never renders as one.
 *  The title is pinned by server/internal/node/assistant_interject_test.go. */
export const INTERJECT_TITLE = "插话"; // i18n-ignore: canonical hub prompt title

/** A persona-context block that is the hub's interject note rather than user-picked context. */
export function isInterjectContext(attrs: { kind?: string; title?: string }): boolean {
  return (attrs.kind ?? "context") === "context" && attrs.title === INTERJECT_TITLE;
}

export function parseUserText(raw: string): { text: string; inlineTrailer?: string; contexts: Context[]; files: AttachedFile[]; photos: string[] } {
  const contexts: Context[] = [];
  const files: AttachedFile[] = [];
  const photos: string[] = [];
  let text = withoutViewErrors(raw).replace(CONTEXT_RE, (_, attrs: string, body: string) => {
    const { kind, title, url, ...rest } = tagAttrs(attrs);
    if (kind === "skill-instructions" || isInterjectContext({ kind, title })) return "";
    const words = body.trim();
    contexts.push({ kind: kind ?? "context", title, url, text: kind === "forward" ? splitInlineTrailer(words).text : words, ...(Object.keys(rest).length ? { attrs: rest } : {}) });
    return "";
  });
  text = text.replace(FILE_RE, (_, attrs: string) => {
    const a = tagAttrs(attrs);
    if (!a.path) return "";
    const num = (v: string | undefined) => (v !== undefined && Number.isFinite(Number(v)) ? Number(v) : undefined);
    files.push({ kind: a.kind || "file", path: a.path, name: a.name || a.path.split("/").pop() || a.path, size: num(a.size), duration: num(a.duration) });
    return "";
  });
  text = text
    .replace(/<pasted_content\b[^>]*>([\s\S]*?)<\/pasted_content>/g, "$1")
    // photos sent with the message (from the phone, the composer, a Codex local_image): the node saved them on the
    // machine and wrote their paths here; they show as thumbnails above the bubble (iOS AttachedImages)
    .replace(/<attached_images>([\s\S]*?)<\/attached_images>/g, (_, body: string) => {
      for (const line of body.split("\n")) {
        const path = line.trim();
        // only absolute / home paths, as iOS UserBubble does: stray text in the block is not a photo
        if ((path.startsWith("/") || path.startsWith("~")) && !photos.includes(path)) photos.push(path);
      }
      return "";
    })
    .replace(/\[Image #\d+\]\s?/g, "")
    .trim();
  return { ...splitInlineTrailer(text), contexts, files, photos };
}

function isUserMessage(i: Item) {
  return i.role === "user" && (i.kind === "text" || (i.kind === "image" && !i.tool_use_id));
}

export function segments(items: Item[], results?: ReadonlyMap<string, Item>): Segment[] {
  const attached = attachedAgentNotices(items);
  const out: Segment[] = [];
  let cur: Segment | null = null;
  let lastUserEntry = "";
  const tools = new Map<string, Extract<Step, { kind: "tool" }>>();

  const open = (id: string): Segment => {
    const s: Segment = { id, steps: [], tools: 0, thoughts: 0, replies: 0, folds: [] };
    out.push(s);
    return s;
  };

  for (const it of items) {
    if (attached.has(it.id)) continue; // what its subagent used: chatLines counts it where it was launched
    const entry = it.id.split("#")[0];
    if (viewFeedbackOnly(it)) continue;
    if (isUserMessage(it)) {
      if (cur?.user?.injected && entry === lastUserEntry && !cur.steps.length && !cur.answer && !cur.user.rawInput!.some((i) => i.id === it.id)) {
        cur.user.rawInput!.push(it);
        if (it.kind === "image") cur.user.images.push(it);
        continue;
      }
      const injected = injectedOf(it);
      if (injected) {
        // The line stands for the input; the complete original remains available in the supervision trace.
        cur = open(`seg:${it.id}`);
        cur.user = { id: it.id, text: injected.line, images: [], ts: it.ts, contexts: [], injected, source: sourceOf(it, injected), rawInput: [it] };
        cur.start = it.ts;
        lastUserEntry = entry;
        continue;
      }
      // blocks of one transcript line (text + its images) are one message
      if (!cur || !cur.user || cur.user.injected || entry !== lastUserEntry || cur.steps.length || cur.answer) {
        cur = open(`seg:${it.id}`);
        cur.user = { id: it.id, text: "", images: [], queued: it.queued, ts: it.ts, contexts: [], from: it.from || undefined, source: sourceOf(it, null), rawInput: [it] };
        cur.start = it.ts;
      } else {
        cur.user.rawInput!.push(it);
      }
      lastUserEntry = entry;
      cur.user!.client_msg_id ??= it.client_msg_id || undefined;
      if (it.kind === "image") cur.user!.images.push(it);
      else {
        if (it.botmux) cur.user!.botmux ??= it.botmux;
        const p = parseUserText(it.text ?? "");
        if (p.text) {
          cur.user!.text = cur.user!.text ? `${cur.user!.text}\n${p.text}` : p.text;
          cur.user!.inlineTrailer = p.inlineTrailer;
        }
        for (const c of p.contexts) {
          if (c.kind === "message-source") continue;
          // a real switch is the divider, not a chip; a block that only looks like one (pasted, naming no engines) stays a chip
          const sw = engineSwitchOf(c);
          if (sw) cur.engineSwitch ??= sw;
          else cur.user!.contexts.push(c);
        }
        if (p.files.length) cur.user!.files = [...(cur.user!.files ?? []), ...p.files];
        if (p.photos.length) cur.user!.photos = [...new Set([...(cur.user!.photos ?? []), ...p.photos])];
      }
      continue;
    }
    if (!cur) {
      cur = open(`seg:${it.id}`);
      cur.start = it.ts;
    }
    if (it.ts) cur.end = it.ts;
    countUsage(cur, it);
    if (cur.ttftMs === undefined && typeof it.ttft_ms === "number" && Number.isFinite(it.ttft_ms) && it.ttft_ms >= 0) {
      cur.ttftMs = it.ttft_ms;
    }
    switch (it.kind) {
      case "tool_use": {
        const st: Extract<Step, { kind: "tool" }> = { kind: "tool", id: it.id, use: it, result: it.tool_use_id ? results?.get(it.tool_use_id) : undefined, images: [] };
        if (it.tool_use_id) tools.set(it.tool_use_id, st);
        cur.steps.push(st);
        cur.tools++;
        break;
      }
      case "tool_result": {
        const st = it.tool_use_id ? tools.get(it.tool_use_id) : undefined;
        if (st && !results?.has(it.tool_use_id!)) st.result = it;
        break; // a result without its call (the call is on an earlier page) has nothing to show
      }
      case "image": {
        const st = it.tool_use_id ? tools.get(it.tool_use_id) : undefined;
        if (st) st.images.push(it);
        else cur.steps.push({ kind: "image", id: it.id, item: it });
        break;
      }
      case "visualization":
        cur.steps.push({ kind: "visualization", id: it.id, item: it });
        break;
      case "thinking":
        // an empty thought (Claude Code mostly writes them without words) still shows as "Thinking Ns"
        cur.steps.push({ kind: "thinking", id: it.id, item: it });
        if (it.text?.trim()) cur.thoughts++;
        break;
      case "text":
        if (it.text?.trim()) cur.steps.push({ kind: "text", id: it.id, item: it });
        else if (it.role === "assistant" && it.text) cur.blankReply = true;
        break;
      case "notice":
        cur.steps.push({ kind: "notice", id: it.id, item: it });
        break;
      case "fold":
        cur.steps.push({ kind: "fold", id: it.id, item: it });
        cur.folds.push(it);
        if (it.fold) {
          cur.tools += it.fold.tools;
          cur.thoughts += it.fold.thoughts;
          if (it.fold.until) cur.end = it.fold.until;
        }
        break;
    }
  }

  // the answer: the last thing the agent said in the segment
  for (const s of out) {
    for (let i = s.steps.length - 1; i >= 0; i--) {
      const st = s.steps[i];
      if (st.kind === "text") {
        s.answer = st.item;
        s.steps.splice(i, 1);
        break;
      }
    }
    s.replies = s.steps.filter((x) => x.kind === "text").length;
  }
  return groupSourceLabels(out);
}

/** Keep consecutive provenance labels quiet without hiding any individual message. */
function groupSourceLabels(segs: Segment[]): Segment[] {
  let previous: string | undefined;
  for (const seg of segs) {
    const source = seg.user?.source;
    if (source && seg.user) {
      const key = sourceKey(source);
      seg.user.continuedSource = previous === key;
      previous = key;
    } else previous = undefined;
    if (seg.steps.length || seg.answer) previous = undefined;
  }
  return segs;
}

/** The agent's words in a pushed batch that the chat did not have yet: they just came in (their words fade in once). */
export function freshWords(have: Item[], pushed: Item[]): string[] {
  const known = new Set(have.map((i) => i.id));
  return pushed.filter((i) => i.role === "assistant" && i.kind === "text" && !known.has(i.id)).map((i) => i.id);
}

/** A segment's steps with its answer back in its place: what lib/work-groups.ts groups. */
export function exchangeSteps(s: Segment): Step[] {
  if (!s.answer) return s.steps;
  const a = s.answer;
  const at = s.steps.findIndex((st) => blockOrder(st.kind === "tool" ? st.use : st.item, a) > 0);
  const step: Step = { kind: "text", id: a.id, item: a };
  return at < 0 ? [...s.steps, step] : [...s.steps.slice(0, at), step, ...s.steps.slice(at)];
}

/** Presentation identity for an exchange without input: a fold and its real nonzero block share the source sequence. */
export function segmentIdentity(s: Segment): string {
  if (s.user) return s.id;
  const first = s.steps[0];
  let seq = first ? (first.kind === "tool" ? first.use.seq : first.item.seq) : undefined;
  if (s.answer && (seq === undefined || s.answer.seq < seq)) seq = s.answer.seq;
  return seq !== undefined && Number.isFinite(seq) ? `exchange-${seq}` : s.id;
}

// The first line worth showing for a tool call: the command, the file, the pattern…
export function toolChip(use: Item): string {
  const inp = (use.tool_input ?? {}) as Record<string, unknown>;
  const pick = (...keys: string[]) => {
    for (const k of keys) {
      const v = inp[k];
      if (typeof v === "string" && v.trim()) return v.trim();
    }
    return "";
  };
  const name = canonicalMcpToolName(use.tool_name) ?? "";
  const tool = name.startsWith("mcp__") && !isPersonaTool(name) ? "" : name;
  const newer = ownEntry(CLAUDE_TOOL_CHIP, tool);
  let v = "";
  if (tool === "Bash" || tool === "exec_command" || tool === "shell" || tool === "PowerShell") v = pick("description", "command", "cmd");
  else if (/^(Read|Edit|Write|MultiEdit|NotebookEdit)$/.test(tool)) v = shortFile(pick("file_path", "notebook_path"));
  else if (tool === "Grep" || tool === "Glob") v = pick("pattern");
  else if (tool === "WebFetch") v = pick("url");
  else if (tool === "WebSearch") v = pick("query");
  else if (tool === "Task" || tool === "Agent") v = pick("description", "prompt");
  else if (tool === "TodoWrite") v = t("lib-turns.update_todos");
  else if (newer) v = newer(inp, pick);
  else v = pickSubject(inp);
  if (!v && Array.isArray(inp.command)) v = (inp.command as unknown[]).join(" ");
  return v.split("\n").map((l) => l.trim()).find(Boolean)?.slice(0, 200) ?? "";
}

/** The fields that say what an unknown tool's call is about, the most telling first (iOS ToolNaming.subjectKeys). */
export const SUBJECT_KEYS = ["description", "title", "subject", "name", "file_path", "filePath", "notebook_path", "path", "url", "uri", "query", "pattern", "command", "prompt"];
const PATH_KEYS = new Set(["file_path", "filePath", "notebook_path", "path"]);

/** An unknown tool's subject: the first of SUBJECT_KEYS it has (a path by its file's name), else its first string by field name. */
export function pickSubject(inp: Record<string, unknown>): string {
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  for (const k of SUBJECT_KEYS) if (str(inp[k])) return PATH_KEYS.has(k) ? baseFileName(str(inp[k])) : str(inp[k]);
  for (const k of Object.keys(inp).sort()) if (str(inp[k])) return str(inp[k]);
  return "";
}

const baseFileName = (p: string) => p.split(/[/\\]/).filter(Boolean).pop() ?? p;

type PickToolField = (...keys: string[]) => string;
/** What Claude Code's newer tools are about (iOS ToolPresentation.subject). */
const CLAUDE_TOOL_CHIP: Record<string, (inp: Record<string, unknown>, pick: PickToolField) => string> = {
  TaskCreate: (_, pick) => pick("subject", "description"),
  TaskUpdate: (inp, pick) => pick("subject", "activeForm") || (inp.taskId != null ? `#${String(inp.taskId)}` : ""),
  TaskGet: (inp) => (inp.taskId != null ? `#${String(inp.taskId)}` : ""),
  TaskList: () => "",
  ListAgents: () => "",
  CronList: () => "",
  ExitWorktree: () => "",
  TaskStop: (_, pick) => pick("task_id", "shell_id"),
  KillShell: (_, pick) => pick("shell_id", "task_id"),
  KillBash: (_, pick) => pick("shell_id", "task_id"),
  TaskOutput: (_, pick) => pick("task_id"),
  BashOutput: (_, pick) => pick("bash_id", "task_id"),
  SendMessage: (_, pick) => [pick("to", "recipient"), pick("summary") || pick("message", "content").split("\n")[0]].filter(Boolean).join(" · "),
  Monitor: (inp, pick) => pick("description", "command") || String((inp.ws as Record<string, unknown> | undefined)?.url ?? ""),
  ScheduleWakeup: (_, pick) => pick("reason"),
  CronCreate: (_, pick) => [pick("cron"), pick("prompt").split("\n")[0]].filter(Boolean).join(" · "),
  CronDelete: (_, pick) => pick("id"),
  RemoteTrigger: (_, pick) => [humanizeTool(pick("action")), pick("trigger_id")].filter(Boolean).join(" · "),
  LSP: (inp, pick) =>
    inp.operation === "workspaceSymbol" && pick("query") ? pick("query") : [baseFileName(pick("filePath")), inp.line != null ? String(inp.line) : ""].filter(Boolean).join(":"),
  EnterWorktree: (_, pick) => pick("name") || baseFileName(pick("path")),
  SendUserFile: (inp, pick) => {
    const list = [inp.files, inp.file_paths, inp.paths].find(Array.isArray) as unknown[] | undefined;
    const paths = (list ?? []).map((f) => (typeof f === "string" ? f : String((f as Record<string, unknown>)?.path ?? (f as Record<string, unknown>)?.file_path ?? ""))).filter(Boolean);
    if (pick("file_path", "path")) paths.push(pick("file_path", "path"));
    return paths.length ? paths.map(baseFileName).join(", ") : pick("caption");
  },
  Artifact: (_, pick) => pick("title") || baseFileName(pick("file_path")) || pick("url"),
  PushNotification: (_, pick) => pick("message"),
  ReportFindings: (inp) => {
    const findings = Array.isArray(inp.findings) ? (inp.findings as Record<string, unknown>[]) : [];
    if (findings.length === 1) return String(findings[0]?.short_summary ?? findings[0]?.summary ?? "");
    return findings.length ? t("lib-turns.findings", { count: findings.length }) : "";
  },
  ToolSearch: (_, pick) => (pick("query").startsWith("select:") ? pick("query").slice(7).split(",").join(", ") : pick("query")),
  WaitForMcpServers: (inp, pick) => (Array.isArray(inp.servers) ? (inp.servers as unknown[]).map(String).join(", ") : pick("server")),
  ListMcpResourcesTool: (_, pick) => pick("server"),
  ReadMcpResourceTool: (_, pick) => pick("uri", "server"),
  Skill: (_, pick) => pick("skill", "command"),
};

/** A table's entry for a key that comes from data (a tool's name, a word of it): its own entries only, so a tool or a
 *  word called "constructor" or "toString" is not mistaken for one of Object's methods. */
export function ownEntry<T>(table: Record<string, T>, key: string): T | undefined {
  return Object.hasOwn(table, key) ? table[key] : undefined;
}

/** Words spelled their own way wherever they show up in a name (iOS ToolNaming.spelled). */
// i18n-ignore-start: product names and acronyms, the same in every language
const SPELLED: Record<string, string> = {
  api: "API", ai: "AI", aws: "AWS", ci: "CI", cli: "CLI", csv: "CSV", devtools: "DevTools", dns: "DNS", gcp: "GCP", github: "GitHub",
  gitlab: "GitLab", graphql: "GraphQL", html: "HTML", http: "HTTP", https: "HTTPS", id: "ID", ids: "IDs", ide: "IDE", ios: "iOS",
  json: "JSON", llm: "LLM", lsp: "LSP", macos: "macOS", mcp: "MCP", oauth: "OAuth", openai: "OpenAI", pdf: "PDF", pr: "PR", prs: "PRs",
  sdk: "SDK", sql: "SQL", ssh: "SSH", ui: "UI", uri: "URI", url: "URL", urls: "URLs", youtube: "YouTube",
};
// i18n-ignore-end
const SPELLED_VALUES = new Set(Object.values(SPELLED));

/** An identifier cut into words at _ - . / : and spaces, and where camel case turns ("getPRComments": get, PR, Comments). */
export function nameWords(id: string): string[] {
  return id
    .split(/[_\-./: ]+/)
    .flatMap((part) => part.split(/(?<=[a-z0-9])(?=[A-Z])|(?<=[A-Z])(?=[A-Z][a-z])/))
    .filter(Boolean);
}

/** An identifier in words, the first capitalized: "create_issue" is "Create issue", "getPRComments" "Get PR comments" (iOS ToolNaming.humanize). */
export function humanizeTool(id: string): string {
  const words = nameWords(id).map((w) => ownEntry(SPELLED, w.toLowerCase()) ?? (w.length > 1 && /^[A-Z0-9]+$/.test(w) ? w : w.toLowerCase()));
  if (!words.length) return "";
  const [first, ...rest] = words;
  return [SPELLED_VALUES.has(first) ? first : first.charAt(0).toUpperCase() + first.slice(1), ...rest].join(" ");
}

/** An MCP server's name as a name: "github" is "GitHub", "chrome-devtools" "Chrome DevTools"; a plugin's server and a
 *  claude.ai connector read as their own name, once (iOS ToolNaming.serverName). */
export function mcpServerName(server: string): string {
  let s = server;
  for (const prefix of ["plugin_", "claude_ai_"]) if (s.startsWith(prefix) && s.length > prefix.length) s = s.slice(prefix.length);
  let words = nameWords(s);
  const half = words.length / 2;
  if (half >= 1 && Number.isInteger(half) && words.slice(0, half).join(" ").toLowerCase() === words.slice(half).join(" ").toLowerCase()) words = words.slice(0, half);
  return words.map((w) => ownEntry(SPELLED, w.toLowerCase()) ?? w.charAt(0).toUpperCase() + w.slice(1)).join(" ");
}

export function shortFile(p: string) {
  const parts = p.split("/");
  return parts.length > 3 ? `…/${parts.slice(-2).join("/")}` : p;
}

export const TOOL_LABEL: Record<string, string> = {
  Bash: t("lib-turns.tool_run"),
  exec_command: t("lib-turns.tool_run"),
  shell: t("lib-turns.tool_run"),
  Read: t("lib-turns.tool_read"),
  Edit: t("lib-turns.tool_edit"),
  MultiEdit: t("lib-turns.tool_edit"),
  Write: t("lib-turns.tool_write"),
  Grep: t("lib-turns.tool_search"),
  Glob: t("lib-turns.tool_find_files"),
  WebFetch: t("lib-turns.tool_open_web_page"),
  WebSearch: t("lib-turns.tool_search_web"),
  Task: t("lib-turns.tool_subagent"),
  Agent: t("lib-turns.tool_subagent"),
  TodoWrite: t("lib-turns.tool_todos"),
  ToolSearch: t("lib-turns.tool_load_tools"),
  Skill: t("lib-turns.tool_skill"),
  AskUserQuestion: t("lib-turns.tool_question"),
  ExitPlanMode: t("lib-turns.tool_plan"),
  apply_patch: t("lib-turns.tool_change"),
};

const AGY_PERSONA_MCP_RE = new RegExp(`^mcp_(?:${[...PERSONA_SERVERS].join("|")})(?:_[0-9a-fA-F]{8,16})?_([a-z][a-z0-9_]*)$`);
const AGY_TOOL_RESULT_HEADER_RE = /^(?:Created At: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\r?\nCompleted At: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})(?:\r?\n|$))+/;

/** Normalize Antigravity's single-underscore Persona MCP names (`mcp_persona_<tool>`, `mcp_persona_<hex>_<tool>`) to `mcp__persona__<tool>`. */
export function canonicalMcpToolName(name?: string): string | undefined {
  if (!name || !name.startsWith("mcp_") || name.startsWith("mcp__")) return name;
  const m = AGY_PERSONA_MCP_RE.exec(name);
  return m ? `mcp__persona__${m[1]}` : name;
}

/** Strip leading Antigravity Created At / Completed At RFC3339 wrapper lines (and an immediate Output: line) from tool results. */
export function stripToolResultTimestampHeaders(text: string): string {
  if (!text.startsWith("Created At: ")) return text;
  const m = AGY_TOOL_RESULT_HEADER_RE.exec(text);
  return m ? text.slice(m[0].length).replace(/^Output:\r?\n/, "") : text;
}

export function toolLabel(rawName?: string): string {
  const name = canonicalMcpToolName(rawName);
  if (!name) return t("lib-turns.tool");
  const label = ownEntry(TOOL_LABEL, name);
  if (label) return label;
  // mcp__<server>__<tool>, and the server may have underscores itself (mcp__plugin_x_y__tool), like the apps
  if (name.startsWith("mcp__")) return name.slice(name.lastIndexOf("__") + 2).replace(/_/g, " ");
  return name;
}

/** The MCP server's name as shown: Persona's own server keeps its old id `persona` (allow lists and hooks match it). */
export function mcpServerLabel(server: string): string {
  const s = server.replace(/^plugin_/, "");
  return PERSONA_SERVERS.has(s) ? "Persona" : s;
}

/** The server part of mcp__<server>__<tool> ("mcp__<server>" alone for a server-wide request), else null. */
export function mcpServer(rawName?: string): string | null {
  const name = canonicalMcpToolName(rawName);
  if (!name?.startsWith("mcp__")) return null;
  const rest = name.slice(5);
  const i = rest.lastIndexOf("__");
  return i > 0 ? rest.slice(0, i) : rest || null;
}

/** A tool's name for a sentence (a permission card): Persona's own MCP tool as "Persona · mail send", another server's
 *  in words ("GitHub · Create issue", #2986), others as written. */
export function toolDisplayName(rawName?: string): string {
  const name = canonicalMcpToolName(rawName);
  const server = mcpServer(name);
  if (server === null) return name || toolLabel();
  const raw = name!.slice(5 + server.length + 2);
  if (PERSONA_SERVERS.has(server.replace(/^plugin_/, ""))) return raw ? `Persona · ${raw.replace(/_/g, " ")}` : "Persona";
  const tool = humanizeTool(raw);
  return tool ? `${mcpServerName(server)} · ${tool}` : mcpServerName(server);
}

/** The tool a card asks about, for its title: the manager's name for its own tools when it gave one (tool_title). */
export function cardToolName(ix: { tool_name?: string; tool_title?: string }): string {
  return ix.tool_title || toolDisplayName(ix.tool_name);
}

/** The MCP server ids that are Persona itself (`persona` today, `persona` if it is ever renamed; plugin_ dropped). */


/** Whether a tool is one of Persona's own MCP tools (mcp__persona__agent_read…). */
export function isPersonaTool(rawName?: string): boolean {
  const name = canonicalMcpToolName(rawName);
  const server = mcpServer(name);
  return server !== null && PERSONA_SERVERS.has(server.replace(/^plugin_/, "")) && name!.length > 5 + server.length + 2;
}

/** Persona's tools with a verb of their own, by their bare name (iOS PersonaToolNames + ToolPresentation.verb). */
const PERSONA_VERB_KEY: Record<string, string> = {
  // Agents on the user's computers (docs/assistant/assistant.md "Current structure", Agents)
  agent_read: "persona_agent_read",
  agent_send: "persona_agent_send",
  send_to_agent: "persona_agent_send",
  agents_list: "persona_agents_list",
  list_agents: "persona_agents_list",
  agent_start: "persona_agent_start",
  task_plan: "persona_agent_start",
  interaction_answer: "persona_interaction_answer",
  ask_persona: "persona_ask_persona",
  // the knowledge base (docs/assistant/knowledge.md)
  kb_get: "persona_kb_search",
  kb_list: "persona_kb_search",
  kb_search: "persona_kb_search",
  search_knowledge: "persona_kb_search",
  kb_update: "persona_kb_update",
  // talking to the user
  ask_user: "persona_ask_user",
  notify_user: "persona_notify_user",
  schedule_followup: "persona_schedule_followup",
  list_followups: "persona_list_followups",
  cancel_followup: "persona_cancel_followup",
  schedule_routine: "persona_schedule_routine",
  delete_routine: "persona_delete_routine",
  inbox_draft: "persona_inbox_draft",
  persona_settings: "persona_settings",
  // live cards (docs/assistant/assistant-views.md)
  view_show: "persona_view_show",
  view_get: "persona_view_get",
  view_update: "persona_view_update",
  // its own conversations (docs/assistant/assistant.md "Managing its own conversations")
  conversations_list: "persona_conversations_list",
  conversation_read: "persona_conversation_read",
  conversation_rename: "persona_conversation_rename",
  conversation_archive: "persona_conversation_archive",
  conversation_unarchive: "persona_conversation_unarchive",
  conversation_new: "persona_conversation_new",
  conversation_send: "persona_conversation_send",
  // a Mac through Cua Driver (docs/extensions/computer-use.md)
  computer_state: "persona_computer_state",
  computer_run: "persona_computer_run",
  browser_run: "persona_browser_run",
  browser_look: "persona_browser_look",
  // commands run on a computer with a password typed on the iPhone (#1137)
  secret_run: "persona_secret_run",
};

/** How one of Persona's own tools reads in a step: a verb ("Read agent chat"), or for the phone and mail families the
 *  family and what it did ("iPhone" + "contacts search"); null for every other tool and the Persona tools without one
 *  (they keep "Persona · agent read"). Display only: the server stays `persona` on the wire. */
export function personaToolVerb(rawName?: string): { verb: string; what?: string } | null {
  const name = canonicalMcpToolName(rawName);
  if (!isPersonaTool(name)) return null;
  const base = name!.slice(name!.lastIndexOf("__") + 2);
  const key = ownEntry(PERSONA_VERB_KEY, canonicalTool(base));
  if (key) return { verb: t(`lib-turns.${key}`) };
  const family = /^(phone|mail)_(.+)$/.exec(base);
  if (family) return { verb: t(family[1] === "phone" ? "lib-turns.persona_phone" : "lib-turns.persona_mail"), what: family[2].replace(/_/g, " ") };
  return null;
}

// ── exchanges and lines (iOS ChatBlock.build + ChatLines) ──────────────────────────
// Items group into exchanges (`Turn`): what you said (messages typed while the agent worked join the exchange they were
// sent during), everything the agent did, and its answer; recaps stand between them. A background task that wakes the
// idle agent starts an exchange of its own (#9140): the turn before it had ended, and its last words stay its answer.
// The lines are then what the chat draws, one per row.

export interface Turn {
  id: string;
  /** Your messages and their pictures. */
  users: Item[];
  /** Everything the agent did (no input of yours), the answer included. */
  process: Item[];
  answer?: Item;
  /** Notices that close the exchange visibly ("Interrupted"). */
  trailing: Item[];
  /** Messages and steps in arrival order (how the exchange is drawn). */
  ordered: Item[];
}

export type Block = { kind: "turn"; turn: Turn } | { kind: "row"; item: Item };

/** One of your messages, or a picture sent with one. */
export function isUserInput(i: Item): boolean {
  return i.role === "user" && (i.kind === "text" || (i.kind === "image" && !i.tool_use_id));
}

function isAnswerText(i: Item): boolean {
  return i.role === "assistant" && i.kind === "text" && Boolean(i.text?.trim());
}

/** What woke the idle agent (docs/protocol/protocol.md `wake`): a background task's notice, or a subagent's report as
 *  its call's result. The turn before it was over, so it starts an exchange as a message does (#9140). */
const isWake = (i: Item) => i.wake === true;

function newTurn(id: string): Turn {
  return { id, users: [], process: [], trailing: [], ordered: [] };
}

/** Only Persona's visibility tools, not similarly named tools on unrelated MCP servers. */
export function supervisionTool(rawName?: string): "notify_user" | "ask_user" | "supervision_finish" | "task_review" | undefined {
  const name = canonicalMcpToolName(rawName);
  const bare = name?.startsWith("mcp__") && isPersonaTool(name) ? name.slice(name.lastIndexOf("__") + 2) : name;
  return bare === "notify_user" || bare === "ask_user" || bare === "supervision_finish" || bare === "task_review" ? bare : undefined;
}

/** Visibility is round-wide and derived again after every page merge. Arguments alone never prove success. */
function supervisionVisibility(ordered: Item[], results: ReadonlyMap<string, Item> | undefined, live: boolean, routineMainRelay = false, attentionReply?: string): { visible: boolean; completedSilent: boolean; promoted: boolean } {
  if (ordered.some((i) => i.supervisionUser || i.fold?.supervision_user || (injectedOf(i)?.state === "supervise" && injectedOf(i)?.level === "user"))) return { visible: true, completedSilent: false, promoted: true };
  let silent = false;
  for (const use of ordered) {
    if (use.kind !== "tool_use") continue;
    const name = supervisionTool(use.tool_name);
    if (!name) continue;
    const result = use.tool_use_id ? results?.get(use.tool_use_id) : undefined;
    // ask_user waits for a person before it has a result; never bury its pending answer card.
    if (!result) {
      if (name === "ask_user" && live) return { visible: true, completedSilent: false, promoted: true };
      continue;
    }
    if (result.is_error) continue;
    if (name === "task_review") {
      const verdict = use.tool_input?.verdict;
      let internalAccepted = false;
      if (verdict === "accepted" && use.tool_input?.user_check !== true) {
        try {
          const { background } = JSON.parse(stripToolResultTimestampHeaders(result.text ?? ""));
          internalAccepted = background === true;
        } catch {
          // Legacy and malformed receipts keep the accepted review visible.
        }
      }
      if ((verdict === "accepted" || verdict === "needs_user") && !internalAccepted) return { visible: true, completedSilent: false, promoted: true };
      continue;
    }
    if (name !== "supervision_finish") return { visible: true, completedSilent: false, promoted: true };
    try {
      const { visibility } = JSON.parse(stripToolResultTimestampHeaders(result.text ?? ""));
      if (visibility === "user") return { visible: true, completedSilent: false, promoted: true };
      if (visibility === "silent") silent = true;
    } catch {
      // A failed or malformed receipt does not decide visibility.
    }
  }
  const visible = !live && hasBackgroundReply(ordered, routineMainRelay, attentionReply, silent);
  return { visible, completedSilent: !live && silent && !visible, promoted: false };
}

export function supervisionVisible(ordered: Item[], results: ReadonlyMap<string, Item>, live: boolean, routineMainRelay = false, attentionReply?: string): boolean {
  return supervisionVisibility(ordered, results, live, routineMainRelay, attentionReply).visible;
}

/** An answer ends the round's work. Older runtimes also emit plain intermediate text before their next call. */
function hasBackgroundReply(items: Item[], routineMainRelay: boolean, attentionReply: string | undefined, silent: boolean): boolean {
  let last: Item | undefined;
  for (const item of items) {
    const relevant = isAnswerText(item) || item.kind === "tool_use" || item.kind === "thinking" || item.kind === "fold" ||
      item.wake || item.model_refusal || isInterruptNotice(item);
    if (relevant && (!last || blockOrder(last, item) < 0)) last = item;
  }
  if (!last || !isAnswerText(last) || last.narration || last.codex_heartbeat_quiet) return false;
  const needsAttention = last.id === attentionReply || parseAPIError(last.text) !== null;
  return (!routineMainRelay || needsAttention) && (!silent || needsAttention);
}

/** Message sources whose rounds fold in the main conversation while `assistant.main_background_folded` is on
 *  (docs/assistant/assistant.md "Background rounds"). */
const BACKGROUND_SOURCES: ReadonlySet<string> = new Set(["conversation", "followup", "patrol", "notice"]);

/** The input's source when it starts a background round. A `followup` is one only as a scheduled follow-up (the hub
 *  titles those): Persona continuing the user's own work after a usage limit reset (`limit_continue`) or waking the chat
 *  for what it said it waits on (`wake`) carries the same source, and folding it would hide the answer to the user. */
export function backgroundSource(it: Item): MessageSource | undefined {
  if (!isUserInput(it) || it.kind !== "text") return undefined;
  const injected = injectedOf(it);
  const source = sourceOf(it, injected);
  if (!source || !BACKGROUND_SOURCES.has(source.kind)) return undefined;
  if (source.kind === "followup" && (injected?.state === "limit_continue" || injected?.state === "wake" || !source.title?.trim())) return undefined;
  return source;
}

/** `background`: background rounds are separate rounds like supervision ones (a queued real message never joins them). */
export function buildBlocks(items: readonly Item[], background = false): Block[] {
  const blocks: Block[] = [];
  let cur: Turn | null = null;
  let lastUserEntry = "";
  let supervisionRound = false;

  const close = () => {
    if (!cur) return;
    // the answer is the exchange's latest words
    cur.answer = [...cur.process].reverse().find(isAnswerText);
    blocks.push({ kind: "turn", turn: cur });
    cur = null;
  };
  const add = (t: Turn, it: Item, process = true) => {
    if (process) t.process.push(it);
    t.ordered.push(it);
  };

  for (const [i, it] of items.entries()) {
    if (viewFeedbackOnly(it)) continue;
    if (isUserInput(it)) {
      const entry = it.id.split("#")[0];
      const prev: Item | undefined = cur ? cur.ordered[cur.ordered.length - 1] : undefined;
      // Supervision is a separate round even when queued. A later real message must never join its fold.
      const sameMessage = prev && isUserInput(prev) && entry === lastUserEntry;
      const supervision = injectedOf(it)?.state === "supervise" || (background && !!backgroundSource(it));
      const supervising = supervisionRound;
      if (cur && (sameMessage || (it.queued && !supervision && !supervising))) {
        cur.users.push(it);
        add(cur, it, false);
      } else {
        close();
        cur = newTurn(it.id);
        cur.users.push(it);
        add(cur, it, false);
      }
      if (!sameMessage) supervisionRound = supervision;
      lastUserEntry = entry;
      continue;
    }
    if (isWake(it)) {
      // A background task woke the idle agent: the turn before it was over, and the last words of that turn stay its
      // answer. The work it woke is an exchange of its own that opens with the notice, where a message would be
      // (#9140: kept in the exchange before it, each woken turn's last words took the answer's place and the real
      // answer folded into the work). After a recap it simply follows the recap.
      close();
      cur = newTurn(it.id);
      add(cur, it);
      continue;
    }
    // a compaction is a divider of its own after the exchange; in the middle of work (more of it follows) it stays a
    // step of that work, so an auto-compaction does not split the exchange
    if (isCompaction(it)) {
      const next = items[i + 1];
      if (cur && next && !isUserInput(next)) {
        add(cur, it);
        continue;
      }
      close();
      blocks.push({ kind: "row", item: it });
      continue;
    }
    if (it.kind === "recap") {
      close();
      blocks.push({ kind: "row", item: it });
      continue;
    }
    if (it.kind === "notice" && it.model_refusal) {
      // #2528: an upstream refusal (and the model Claude Code moved to) is a line of its own after the exchange, never
      // folded into the work row where it would go unseen
      if (!cur) cur = newTurn(it.id);
      cur.trailing.push(it);
      continue;
    }
    if (it.kind === "notice") {
      if (!cur) {
        // e.g. a background command finishing after a recap: part of the (next) work, never its own line
        cur = newTurn(it.id);
        add(cur, it);
      } else if (isInterruptNotice(it)) {
        cur.trailing.push(it);
      } else {
        add(cur, it);
      }
      continue;
    }
    if (!cur) cur = newTurn(it.id);
    add(cur, it);
  }
  close();
  return blocks;
}

/** The transcript's "Interrupted" notice: a line of its own after the exchange it stopped. */
export function isInterruptNotice(it: Item): boolean {
  // The hub may already have localized the canonical transcript marker.
  return it.kind === "notice" && !it.task && (it.text === "已中断" || it.text === "Interrupted" || it.text === "中断しました"); // i18n-ignore: transcript markers
}

export type ChatLine =
  /** A quiet supervision round, including all its segments and notices, never the next real input. `source`: a
   *  background round's (another conversation, a scheduled task, a follow-up); absent for supervision. */
  | { kind: "supervision"; id: string; lines: ChatLine[]; live: boolean; source?: MessageSource;
    /** Main only: a successful silent receipt ended this unpromoted round. Its full trace belongs in Background activity. */
    completedSilent?: boolean; start?: string; until?: string }
  /** A message (or none), one work row for what the agent did after it, the agent's last words. */
  | { kind: "segment"; id: string; seg: Segment; live: boolean }
  /** A notice of its own line: a wake that opens new work, "Interrupted" after an exchange. */
  | { kind: "notice"; id: string; item: Item }
  /** Where the context was compacted: a divider across the chat (lib/compaction.ts). */
  | { kind: "compact"; id: string; item: Item }
  /** One or more "while you were away" recaps in a row. */
  | { kind: "recaps"; id: string; items: Item[] }
  /** A model API error, `count` times in a row (retries between folded in). */
  | { kind: "apiError"; id: string; item: Item; info: APIErrorInfo; count: number };

export interface ChatLinesOptions {
  /** The agent is at work: the last exchange is the live one. */
  live?: boolean;
  /** Fold quiet background rounds in the main and leader conversations. A scheduled task's prompt stays hidden
   *  (docs/assistant/assistant.md "Background rounds"). */
  background?: boolean;
  /** This is the main conversation: completed canonical silent rounds belong in Background activity. */
  main?: boolean;
  /** The main conversation opted in to folding ordinary nonempty relay replies. Explicit promotions stay visible. */
  foldRoutineRelayReplies?: boolean;
  /** The current session needs attention, even if its latest completed reply came from a relay. */
  latestReplyNeedsAttention?: boolean;
  /** The items may predate what the transcript holds: a cached page shown before the hub's newest page landed, or after
   *  that read failed. A turn without an answer there may have one the page has not read, so none is called empty. */
  stale?: boolean;
  /** The latest turn is paused waiting for the user (needs_you or an open interaction card on this session) and has not
   *  finished yet, so it is not marked empty even while not actively working. */
  needsYou?: boolean;
  /** The session's live state is currently unknown (for example, a runtime summary lagging behind the transcript):
   *  the latest turn may still be running or finalizing, so it is not marked empty until a definite settled state lands. */
  statusUnknown?: boolean;
}

const COMPACT_COMMAND_RE = /^\/compact(?:\s|$)/;
const SLASH_COMMAND_RE = /^\/[a-zA-Z][\w:-]*(?:\s|$)/;

/** Local slash commands (/compact with or without args, or another slash command answered only by non-error local
 *  notices) never produce an assistant reply and must not be marked empty or offered for Retry. */
function isLocalCommandTurn(user: UserMessage, steps: readonly Step[]): boolean {
  const text = user.text.trim();
  if (COMPACT_COMMAND_RE.test(text)) return true;
  return !user.images.length && SLASH_COMMAND_RE.test(text) && steps.length > 0 &&
    steps.every((step) => step.kind === "notice" && !step.item.task && !step.item.wake && !parseAPIError(step.item.text));
}

function turnLines(t: Turn, live: boolean, results?: ReadonlyMap<string, Item>, stale = false, waiting = false): ChatLine[] {
  const out: ChatLine[] = [];
  let ordered = t.ordered;
  // work a background task woke the agent for opens with that notice, where a message would be (a subagent's report
  // that woke it is drawn under its call, so it opens with the work)
  const woke = ordered[0] && isWake(ordered[0]) ? ordered[0] : undefined;
  if (woke) {
    if (woke.kind === "notice") out.push({ kind: "notice", id: woke.id, item: woke });
    ordered = ordered.slice(1);
  }
  const segs = segments(ordered, results);
  // woken and at work with nothing of it here yet: its work row
  if (woke && live && !segs.length) segs.push({ id: `seg:${woke.id}`, steps: [], tools: 0, thoughts: 0, replies: 0, folds: [] });
  // what the background task that woke it used is the work it woke
  if (woke && segs[0]) {
    countUsage(segs[0], woke);
    if (segs[0].ttftMs === undefined && typeof woke.ttft_ms === "number" && Number.isFinite(woke.ttft_ms) && woke.ttft_ms >= 0) {
      segs[0].ttftMs = woke.ttft_ms;
    }
  }
  // stopped after everything it did (not an exchange that went on after it): its last words are not a finished reply
  const end = segs.at(-1), at = t.ordered.at(-1)?.seq ?? 0;
  if (end && !live && t.trailing.some((n) => isInterruptNotice(n) && (!end.answer || n.seq > at))) end.cut = true;
  const user = end?.user;
  if (end && user && !live && !waiting && !stale && !end.answer && !t.trailing.length && isOwnMessage(user) && !user.queued && !isLoopTick(user) &&
      !isLocalCommandTurn(user, end.steps) &&
      !end.steps.some((step) => step.kind === "visualization" || (step.kind === "image" && step.item.role === "assistant") ||
        (step.kind === "tool" && !step.result && supervisionTool(step.use.tool_name) === "supervision_finish" && step.use.tool_input?.visibility === "silent")) &&
      !supervisionVisibility(ordered, results, false).completedSilent) {
    end.emptyResponse = true;
  }
  // what a stopped turn's notice carries (a Codex turn's total) is its last exchange's
  if (end) for (const n of t.trailing) countUsage(end, n);
  segs.forEach((seg, k) => {
    const opening = k === 0 && !seg.user && woke;
    // the work a wake opened is known by the wake: its row keeps its key from before its first step to its last
    if (opening) Object.assign(seg, { id: `seg:${woke.id}`, start: woke.ts });
    out.push({ kind: "segment", id: opening ? `exchange-${woke.seq}` : segmentIdentity(seg), seg, live: live && k === segs.length - 1 });
  });
  for (const n of t.trailing) out.push({ kind: "notice", id: n.id, item: n });
  return out;
}

/** The chat as lines (iOS ChatLines.lines): exchanges, the notices that opened or closed them, recaps, API errors as cards. */
export function chatLines(items: Item[], opts: ChatLinesOptions = {}): ChatLine[] {
  const attached = attachedAgentNotices(items);
  const feed = items.filter((i) => !attached.has(i.id));
  const background = opts.background === true;
  const blocks = buildBlocks(foldCompactions(feed), background);
  const source = feed.at(-1);
  // The live exchange is the owner of the latest source item, not the last drawn block.
  const attentionReply = opts.latestReplyNeedsAttention && opts.foldRoutineRelayReplies ? feed.findLast(isAnswerText)?.id : undefined;
  const latestTurn = (opts.live || opts.needsYou || opts.statusUnknown) && source
    ? blocks.find((b) => b.kind === "turn" && (b.turn.ordered.some((i) => i.id === source.id) || b.turn.trailing.some((i) => i.id === source.id)))
    : undefined;
  const latestTurnId = latestTurn && latestTurn.kind === "turn" ? latestTurn.turn.id : undefined;
  const liveId = opts.live ? latestTurnId : undefined;
  const waitingId = (opts.needsYou || opts.statusUnknown) ? latestTurnId : undefined;
  const out: ChatLine[] = [];
  // A result can land after another segment or user input; associate it with its original call, not its position.
  const results = new Map(items.filter((i) => i.kind === "tool_result" && i.tool_use_id).map((i) => [i.tool_use_id!, i]));
  // A recap or compaction can split one round into several blocks. Supervision lasts until the next actual input,
  // not merely until one Turn closes; orphan continuation segments and live prose still belong to that round.
  let round: { lines: ChatLine[]; ordered: Item[]; live: boolean; source?: MessageSource } | undefined;
  const flush = (until?: string) => {
    if (!round) return;
    const visibility = supervisionVisibility(round.ordered, results, round.live, opts.foldRoutineRelayReplies === true && round.source?.kind === "conversation", attentionReply);
    if (visibility.visible) {
      if (opts.main === true && visibility.promoted && round.source?.kind === "conversation") {
        for (const line of round.lines) {
          if (line.kind === "segment" && line.seg.user?.source?.kind === "conversation") line.seg.user.promotedRelay = true;
        }
      }
      out.push(...round.lines);
    } else out.push({
      kind: "supervision", id: round.lines[0].id, lines: round.lines, live: round.live,
      ...(round.source ? { source: round.source } : {}),
      ...(opts.main && visibility.completedSilent ? { completedSilent: true, start: round.ordered.find((i) => i.ts)?.ts, until } : {}),
    });
    round = undefined;
  };
  const append = (lines: ChatLine[], ordered: Item[], live: boolean) => {
    if (!round) { out.push(...lines); return; }
    for (const line of lines) if (line.kind === "segment") {
      line.seg.supervision = true;
      if (round.source && line.seg.user) line.seg.user.sourceInTrace = true;
    }
    round.lines.push(...lines);
    round.ordered.push(...ordered);
    round.live ||= live;
  };
  for (const b of blocks) {
    if (b.kind === "turn" && b.turn.users.length) {
      flush(b.turn.users[0]?.ts);
      const source = background && b.turn.users[0] ? backgroundSource(b.turn.users[0]) : undefined;
      if (b.turn.users.some((i) => injectedOf(i)?.state === "supervise")) round = { lines: [], ordered: [], live: false };
      else if (source) round = { lines: [], ordered: [], live: false, source };
    }
    if (b.kind === "row") {
      append([isCompaction(b.item) ? { kind: "compact", id: b.item.id, item: b.item } : { kind: "recaps", id: b.item.id, items: [b.item] }], [b.item], false);
    } else {
      const live = b.turn.id === liveId;
      const waiting = b.turn.id === waitingId;
      append(turnLines(b.turn, live, results, opts.stale, waiting), round ? [...b.turn.ordered, ...b.turn.trailing] : b.turn.ordered, live);
    }
  }
  // Working with nothing of this run yet (the last thing is a recap): retain the supervision round's visibility.
  if (opts.live && !liveId) append([{ kind: "segment", id: "live", seg: { id: "live", steps: [], tools: 0, thoughts: 0, replies: 0, folds: [] }, live: true }], [], true);
  flush();
  lineUsage(out, items);
  const lines = markModelChanges(foldRecaps(foldAPIErrors(out)));
  if (background) hideScheduledPrompts(lines);
  let previous: string | undefined;
  for (const line of lines) {
    if (line.kind === "supervision") {
      groupSourceLabels(line.lines.filter((child): child is Extract<ChatLine, { kind: "segment" }> => child.kind === "segment").map((child) => child.seg));
    }
    if (line.kind !== "segment" || !line.seg.user?.source) { previous = undefined; continue; }
    const key = sourceKey(line.seg.user.source);
    line.seg.user.continuedSource = previous === key;
    previous = line.seg.steps.length || line.seg.answer ? undefined : key;
  }
  return lines;
}

const KINDS = ["in", "out", "cache_read", "cache_write", "cache_write_1h"] as const;

/** What the exchanges' subagents used, across the chat (lib/turn-usage.ts): each subagent (an entry's `id`, its model and
 *  speed) once, at the largest count of each kind any of its reports gives (a later report includes the work it did
 *  after being resumed), in the earliest exchange that launched an item carrying it (the item's call, its task's, or
 *  the call taskCalls finds); with none of those on the page, where its first report is. Then only the chat's first
 *  exchange that used tokens is its first round. */
function lineUsage(lines: ChatLine[], items: Item[]) {
  const segs = lines.flatMap((l) => (l.kind === "segment" ? [l.seg] : l.kind === "supervision" ? l.lines.flatMap((c) => (c.kind === "segment" ? [c.seg] : [])) : []));
  if (items.some((it) => itemUsage(it, true).length)) {
    // each item's exchange, and each call's: the first exchange that has it
    const at = new Map<string, number>();
    const own = (id: string | undefined, k: number) => id && !at.has(id) && at.set(id, k);
    segs.forEach((s, k) => {
      for (const i of s.user?.rawInput ?? []) own(i.id, k);
      for (const st of s.steps) {
        own(st.kind === "tool" ? st.use.id : st.item.id, k);
        if (st.kind === "tool") {
          own(`call:${st.use.tool_use_id}`, k);
          own(st.result?.id, k);
        }
      }
      own(s.answer?.id, k);
    });
    const calls = taskCalls(items);
    const reports = new Map<string, { u: TokenUsage; k: number; launched?: boolean }>();
    let here = 0; // the exchange the walk is in: an item none draws (a result, a notice on a card) lands in the one before
    for (const it of items) {
      here = at.get(it.id) ?? here;
      const list = itemUsage(it, true);
      if (!list.length) continue;
      const launch = [it.tool_use_id, it.task?.tool_use_id, calls.get(it.id)].map((c) => at.get(`call:${c}`)).find((k) => k !== undefined);
      for (const u of list) {
        const key = `${u.id}\n${u.model ?? ""}\n${u.fast ? 1 : 0}`;
        const r = reports.get(key) ?? reports.set(key, { u: { id: u.id, ...(u.model ? { model: u.model } : {}), ...(u.fast ? { fast: true } : {}) }, k: here }).get(key)!;
        for (const kind of KINDS) if ((u[kind] ?? 0) > (r.u[kind] ?? 0)) r.u[kind] = u[kind];
        if (launch !== undefined && (!r.launched || launch < r.k)) Object.assign(r, { k: launch, launched: true });
      }
    }
    for (const { u, k } of reports.values()) if (segs[k]) addUsage(segs[k], [u]);
  }
  markFirstUsage(segs);
}

/** A scheduled task's or follow-up's input shows its one line, never its prompt, folded or not. */
function hideScheduledPrompts(lines: ChatLine[]) {
  for (const line of lines) {
    if (line.kind === "supervision") hideScheduledPrompts(line.lines);
    const user = line.kind === "segment" ? line.seg.user : undefined;
    if (user && (user.source?.kind === "patrol" || user.source?.kind === "followup")) user.promptHidden = true;
  }
}

/** The API error a line ends in: an answer, or a notice of its own line. `rest`: the line without it (null: nothing). */
function lineError(l: ChatLine): { item: Item; info: APIErrorInfo; rest: ChatLine | null } | null {
  if (l.kind === "notice") {
    const info = parseAPIError(l.item.text);
    return info ? { item: l.item, info, rest: null } : null;
  }
  if (l.kind !== "segment" || !l.seg.answer) return null;
  const info = parseAPIError(l.seg.answer.text);
  if (!info) return null;
  const seg: Segment = { ...l.seg, answer: undefined };
  // the error came after everything else the exchange did: the words before it are cut short (Segment.cut)
  if (!l.live && exchangeSteps(l.seg).at(-1)?.id === l.seg.answer.id) seg.cut = true;
  const empty = !seg.user && !seg.steps.length && !l.live;
  return { item: l.seg.answer, info, rest: empty ? null : { ...l, seg } };
}

/** A line that only asked to try again: "continue" with nothing else. */
function isRetryLine(l: ChatLine): boolean {
  return l.kind === "segment" && !l.live && !l.seg.steps.length && !l.seg.answer && Boolean(l.seg.user) && !l.seg.user!.images.length && isRetryText(l.seg.user!.text);
}

/** Model API errors become cards; the same error again right after, with nothing but a retry between, folds into the
 *  first with a count (iOS ChatLines.foldAPIErrors). */
export function foldAPIErrors(lines: ChatLine[]): ChatLine[] {
  const out: ChatLine[] = [];
  for (const line of lines) {
    const e = lineError(line);
    if (!e) {
      out.push(line);
      continue;
    }
    if (e.rest) out.push(e.rest);
    let last = -1;
    for (let i = out.length - 1; i >= 0; i--) if (out[i].kind === "apiError") { last = i; break; }
    const prev = last >= 0 ? out[last] : undefined;
    if (prev?.kind === "apiError" && errorSignature(prev.info) === errorSignature(e.info) && out.slice(last + 1).every(isRetryLine)) {
      out.splice(last + 1);
      out[last] = { ...prev, info: e.info, count: prev.count + 1 };
      continue;
    }
    out.push({ kind: "apiError", id: `err-${e.item.id}`, item: e.item, info: e.info, count: 1 });
  }
  return out;
}

/** Several recaps in a row (nothing else between) are one line, keeping the first one's id. */
export function foldRecaps(lines: ChatLine[]): ChatLine[] {
  const out: ChatLine[] = [];
  for (const l of lines) {
    const prev = out[out.length - 1];
    if (l.kind === "recaps" && prev?.kind === "recaps") out[out.length - 1] = { ...prev, items: [...prev.items, ...l.items] };
    else out.push(l);
  }
  return out;
}

/** The newest recap shown open, in full, as the chat's last line (#1360): in the flow with the agent's words, so it
 *  scrolls with them and never covers them. Only while it is the last line and `pending` (not closed); the older
 *  recaps folded into the same line stay a one-line chip above it. */
export type OpenRecap = { line: string; earlier: Item[]; newest: Item };

export function openRecap(lines: ChatLine[], pending?: Item): OpenRecap | undefined {
  const last = lines[lines.length - 1];
  if (!pending || last?.kind !== "recaps" || last.items[last.items.length - 1]?.id !== pending.id) return undefined;
  return { line: last.id, earlier: last.items.slice(0, -1), newest: pending };
}

/** Sent by another Persona conversation (conversation_new / conversation_send), not typed by you. */
export function isRelayed(msg: UserMessage): boolean {
  if (msg.client_msg_id || msg.rawInput?.some((item) => userSourceSentinel(item.text ?? ""))) return false;
  return msg.source?.kind === "conversation";
}

/** Typed (or dictated, or a quick command expanded) by you: the pale blue bubble. Persona's own messages, another
 *  session's and another conversation's are not. */
export function isOwnMessage(msg: UserMessage): boolean {
  return !msg.source && !msg.injected && !msg.from && !isRelayed(msg);
}

/** A /loop wakeup (the transcript records each as the "/loop …" prompt again): a quiet line, not a bubble. */
export function isLoopTick(msg: UserMessage): boolean {
  return msg.text.startsWith("/loop") && !msg.parts;
}
