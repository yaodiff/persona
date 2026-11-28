// Compacting the assistant's context and starting a new main conversation (#1727), and how a compaction reads in the
// chat: the transcript's notices of one compaction fold into one divider.
import { currentLanguage, formatDate, formatNumber, t } from "../i18n";
import { enc } from "./api";
import type { Compaction, Item } from "./types";

// ── the actions ─────────────────────────────────────────

export const COMPACT_FEATURE = "assistant_compact";
export const NEW_MAIN_FEATURE = "assistant_main_new";
export const COMPACT_REQUESTED_EVENT = "persona:compact-requested";

/** POST …/compact: where it goes. */
export function compactPath(sid: string): string {
  return `/assistant/conversations/${enc(sid)}/compact`;
}

/** POST …/compact's body: the conversation's machine when known (the hub finds it otherwise). */
export function compactBody(machineId?: string): { machine_id?: string } {
  return machineId ? { machine_id: machineId } : {};
}

export const NEW_MAIN_PATH = "/assistant/main/new";

/** POST /assistant/main/new's body: carry the old conversation's summary or nothing; `expect` the main conversation
 *  this device has, so a main another device just replaced is not archived by mistake (409 main_changed). */
export function newMainBody(carry: boolean, expect: string): { carry: "summary" | "none"; expect: string } {
  return { carry: carry ? "summary" : "none", expect };
}

/** The title the hub gives the archived main conversation: "old main chat · until Nov 4" (in Chinese). */
export function archivedMainTitle(now: Date = new Date()): string {
  return t("lib-compaction.archived_title", { date: formatDate(now, { month: "short", day: "numeric" }) });
}

// ── the conversation's compaction state ─────────────────

/** A done compaction shows briefly when seen finishing, or when it finished this recently. */
export const DONE_RECENT_MS = 60_000;
/** A failed one stays until dismissed when it failed this recently (or was seen failing). */
export const FAILED_RECENT_MS = 10 * 60_000;

export type CompactionView =
  | { kind: "running" }
  | { kind: "done"; key: string }
  | { kind: "failed"; key: string; error: string }
  | null;

/** What the chat shows for a conversation's compaction: running while it runs; done / failed only for a change
 *  this page saw (`prev`, the state it saw before) or one that just finished, never an old one on every open.
 *  `dismissed`: the key of a failure the user closed. */
export function compactionView(c: Compaction | undefined, prev: Compaction["state"] | undefined, now: number, dismissed?: string): CompactionView {
  if (!c) return null;
  if (c.state === "running") return { kind: "running" };
  const key = `${c.started_at}|${c.state}`;
  const finished = Date.parse(c.finished_at ?? "");
  const age = Number.isFinite(finished) ? now - finished : Infinity;
  const seen = prev === "running";
  if (c.state === "done") return seen || age <= DONE_RECENT_MS ? { kind: "done", key } : null;
  if (c.state === "failed") {
    if (dismissed === key) return null;
    return seen || age <= FAILED_RECENT_MS ? { kind: "failed", key, error: c.error?.trim() || t("lib-compaction.unknown_error") } : null;
  }
  return null;
}

// ── the divider in the chat ─────────────────────────────

/** The boundary Claude Code writes (the hub's text for it, Codex's too). */
// i18n-ignore: matches the hub's notice text in the transcript
export const COMPACTED_TEXT = "上下文已压缩";
/** The summary it carries on with. */
// i18n-ignore: matches the hub's notice text in the transcript
export const CONTINUED_TEXT = "已从压缩的上下文继续";
/** A /compact sent to it (the hub's text for the command). */
// i18n-ignore: matches the hub's text for the /compact command
export const COMMAND_TEXT = "压缩上下文";

function isNotice(it: Item, text: string): boolean {
  return it.kind === "notice" && !it.task && !it.wake && (it.text ?? "").trim() === text;
}

/** Claude Code's note after compacting ("Compacted while idle…", "Compacted. …"): joins the divider before it. */
function isCompactedNote(it: Item): boolean {
  return it.kind === "notice" && !it.task && !it.wake && (it.text ?? "").startsWith("Compacted");
}

const isIdleNote = (it: Item) => (it.text ?? "").startsWith("Compacted while idle");

function isBoundary(it: Item): boolean {
  return it.kind === "notice" && (Boolean(it.compact) || isNotice(it, COMPACTED_TEXT));
}

/** A notice that stands for a compaction (after `foldCompactions`): one divider. */
export function isCompaction(it: Item): boolean {
  return it.kind === "notice" && Boolean(it.compact);
}

/**
 * One compaction is one item: the /compact that asked for it (before the boundary, or after it as claude -p writes it), the boundary, the summary after it and Claude Code's idle
 * note fold into the boundary (its `compact`, the texts in `notes`). A summary whose boundary is on an older page
 * stands in for it; a /compact with no boundary after it (still running, or it failed) stays as it is.
 */
export function foldCompactions(items: Item[]): Item[] {
  if (!items.some((it) => isBoundary(it) || isNotice(it, CONTINUED_TEXT))) return items;
  const out: Item[] = [];
  let open = -1; // the divider in `out` the notes after it still join
  let command: Item | undefined; // a /compact right before
  const flushCommand = () => {
    if (command) out.push(command);
    command = undefined;
  };
  for (const it of items) {
    if (isNotice(it, COMMAND_TEXT)) {
      // claude -p writes a /compact's command after its boundary and summary (#1865): it joins that divider.
      const d = open >= 0 ? out[open] : undefined;
      if (d?.compact) {
        out[open] = { ...d, compact: { ...d.compact, notes: [...(d.compact.notes ?? []), it.text ?? COMMAND_TEXT] } };
        continue;
      }
      flushCommand();
      command = it;
      open = -1;
      continue;
    }
    if (isBoundary(it)) {
      const notes = [...(command ? [command.text ?? COMMAND_TEXT] : []), it.text ?? COMPACTED_TEXT];
      command = undefined;
      out.push({ ...it, compact: { ...it.compact, notes } });
      open = out.length - 1;
      continue;
    }
    if (isNotice(it, CONTINUED_TEXT) || isCompactedNote(it)) {
      const d = open >= 0 ? out[open] : undefined;
      if (d?.compact) {
        out[open] = { ...d, compact: { ...d.compact, notes: [...(d.compact.notes ?? []), it.text ?? ""], ...(isIdleNote(it) ? { trigger: "idle" } : {}) } };
        continue;
      }
      if (isNotice(it, CONTINUED_TEXT)) {
        // its boundary is not here (an older page)
        const notes = [...(command ? [command.text ?? COMMAND_TEXT] : []), it.text ?? CONTINUED_TEXT];
        command = undefined;
        out.push({ ...it, compact: { notes } });
        open = out.length - 1;
        continue;
      }
    }
    flushCommand();
    open = -1; // anything else ends it
    out.push(it);
  }
  flushCommand();
  return out;
}

const TRIGGERS: Record<string, string> = { manual: t("lib-compaction.trigger_manual"), auto: t("lib-compaction.trigger_auto"), idle: t("lib-compaction.trigger_idle") };

/** 18.1 with the Chinese ten-thousand unit, 8,500 */
export function formatTokens(n: number): string {
  // Chinese keeps its ten-thousand unit with a space (Intl's compact zh output has none); other languages use Intl's compact form
  if (currentLanguage() !== "zh-Hans") return formatNumber(n, n >= 10_000 ? { notation: "compact", maximumFractionDigits: 1 } : {});
  if (n >= 10_000) return t("lib-compaction.tokens_wan", { n: (n / 10_000).toFixed(1).replace(/\.0$/, "") });
  return n.toLocaleString("en-US");
}

/** The divider's words: "the above was compacted · manual · 181K tokens before" (the parts that are known). */
export function compactionLabel(c: Item["compact"]): string {
  const parts = [t("lib-compaction.compacted_above")];
  const trigger = c?.trigger ? TRIGGERS[c.trigger] : undefined;
  if (trigger) parts.push(trigger);
  if (c?.pre_tokens && c.pre_tokens > 0) parts.push(t("lib-compaction.tokens_before", { tokens: formatTokens(c.pre_tokens) }));
  return parts.join(" · ");
}
