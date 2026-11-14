// The palette's logic (views/Palette.tsx, docs/web/web.md "Keyboard shortcuts"): its state (open, what is typed), the two modes — ⌘K
// jumps to a conversation or a terminal pane, `>` (or ⌘⇧P) lists commands — the commands and their filter, and the hook
// other modules add commands through (`registerContextCommands`). Pure apart from the small store.
import { t } from "../i18n";
import { useSyncExternalStore } from "react";
import { chordParts, SCOPE_LABEL, SHORTCUTS, DIGITS, type Bindings, type Platform } from "./shortcuts";
import type { Route } from "./router";
import type { PaneState, TermSpace } from "./terminals";
import type { AgentSurface } from "./agent-kind";
import type { IconName } from "../ui/icons";
import { byTime } from "./times";

// ── state ────────────────────────────────────────────────

export type PaletteMode = "jump" | "command";

export interface PaletteState {
  open: boolean;
  query: string;
}

/** `>` first (spaces before it allowed) is command mode; the rest is what is searched for. */
export function parseQuery(query: string): { mode: PaletteMode; text: string } {
  const q = query.trimStart();
  return q.startsWith(">") ? { mode: "command", text: q.slice(1).trim() } : { mode: "jump", text: query.trim() };
}

let state: PaletteState = { open: false, query: "" };
const subscribers = new Set<() => void>();

function set(next: PaletteState) {
  if (next.open === state.open && next.query === state.query) return;
  state = next;
  subscribers.forEach((f) => f());
}

export const getPalette = () => state;
export const openPalette = (mode: PaletteMode = "jump") => set({ open: true, query: mode === "command" ? ">" : "" });
export const closePalette = () => set({ open: false, query: "" });
/** Opens on `query` to change it: the Agents list's query chip (#6589). */
export const openPaletteWith = (query: string) => set({ open: true, query });
export const setPaletteQuery = (query: string) => set({ open: state.open, query });

/** The key of a mode: opens that mode; pressed again in it, closes (⌘K ⌘K, ⌘⇧P ⌘⇧P); the other mode's key switches. */
export function togglePalette(mode: PaletteMode) {
  if (state.open && parseQuery(state.query).mode === mode) closePalette();
  else openPalette(mode);
}

export function usePalette(): PaletteState {
  return useSyncExternalStore(
    (f) => {
      subscribers.add(f);
      return () => void subscribers.delete(f);
    },
    getPalette,
    getPalette,
  );
}

// ── commands ─────────────────────────────────────────────

/** What a command needs to know about where the user is. */
export interface PaletteContext {
  route: Route;
  /** The conversation on the page (a session's, or the pane a tab opens on). */
  pane?: { mid: string; sid: string };
}

export interface PaletteCommand {
  /** Unique among commands; a provider's own ids are prefixed by the provider. */
  id: string;
  title: string;
  /** Drawn quieter after the title: what it acts on ("Go back to the previous conversation": that conversation's title). */
  detail?: string;
  /** The group it is listed under: a heading in the list, also searched. */
  section?: string;
  /** The section holds a name (the pane's, the space's): its heading is drawn as written, not in a label's capitals. */
  sectionAsWritten?: boolean;
  icon?: IconName;
  /** Its shortcut, in the chord spelling (shortcuts.ts); drawn as keycaps on its row. */
  chord?: string;
  /** More words that find it. */
  keywords?: string;
  /** A destructive one is drawn in the danger colour. */
  danger?: boolean;
  run: () => void;
}

/**
 * Other modules add commands to the palette's command mode (`>`, ⌘⇧P) here — for example the actions of the node the
 * user is on. A provider is called each time the palette lists commands, with where the user is, and returns that
 * moment's commands (none is fine); one that throws is skipped. Returns the way to take the provider back:
 *
 *   useEffect(() => registerContextCommands((ctx) => ctx.pane ? [{ id: "agent:rename", section: "Agent", title: "Rename…", run: … }] : []), []);
 */
export type ContextCommandProvider = (ctx: PaletteContext) => PaletteCommand[];

const providers = new Set<ContextCommandProvider>();

export function registerContextCommands(provider: ContextCommandProvider): () => void {
  providers.add(provider);
  return () => void providers.delete(provider);
}

/** The commands the providers offer for this context, in the order the providers registered; the first of a repeated id wins. */
export function contextCommands(ctx: PaletteContext): PaletteCommand[] {
  const seen = new Set<string>();
  const out: PaletteCommand[] = [];
  for (const provider of providers) {
    let list: PaletteCommand[] = [];
    try {
      list = provider(ctx) ?? [];
    } catch {
      continue;
    }
    for (const c of list) {
      if (seen.has(c.id)) continue;
      seen.add(c.id);
      out.push(c);
    }
  }
  return out;
}

export interface AppCommandInput {
  bindings: Bindings;
  /** Which commands have a handler here (shortcuts.ts handlerOf). */
  available: (id: string) => boolean;
  run: (id: string, digit?: number) => void;
  /** The machines, in the order ⌘1–9 counts them. */
  machines: { id: string; name: string }[];
  /** Commands that have no shortcut ("New agent…"). */
  extra?: PaletteCommand[];
  platform?: Platform;
}

/** The registered commands with their chords, grouped by where they work; the machine command becomes one per machine. */
export function appCommands(input: AppCommandInput): PaletteCommand[] {
  const out: PaletteCommand[] = [];
  for (const def of SHORTCUTS) {
    if (!input.available(def.id)) continue;
    const chord = input.bindings[def.id] ?? def.chord;
    const section = SCOPE_LABEL[def.scope];
    if (chord.endsWith(`+${DIGITS}`)) {
      input.machines.slice(0, 9).forEach((m, i) => {
        out.push({ id: `app:${def.id}:${i + 1}`, title: t("lib-palette.switch_machine", { name: m.name }), section, icon: def.icon, chord: chord.replace(DIGITS, String(i + 1)), keywords: t("lib-palette.machine_keywords"), run: () => input.run(def.id, i + 1) });
      });
      continue;
    }
    out.push({ id: `app:${def.id}`, title: def.label, section, icon: def.icon, chord, run: () => input.run(def.id) });
  }
  return [...out, ...(input.extra ?? [])];
}

/** The keycaps of a command's chord ([] without one). */
export const commandKeys = (c: Pick<PaletteCommand, "chord">, platform?: Platform): string[] => (c.chord ? chordParts(c.chord, platform) : []);

/** Shared name/metadata matching for commands and immediate chat previews. Query is already lowercased. */
export function matchPaletteWords(title: string, query: string, context: string): number {
  if (!query) return 1;
  const name = title.toLowerCase();
  if (name.startsWith(query)) return 100;
  if (name.includes(query)) return 80;
  const hay = context.toLowerCase();
  return query.split(/\s+/).every((word) => name.includes(word) || hay.includes(word)) ? 40 : 0;
}

/** The commands that match what is typed after `>`, the best first (the order they came in otherwise); all of them when nothing is typed. */
export function filterCommands(commands: readonly PaletteCommand[], text: string): PaletteCommand[] {
  const query = text.trim().toLowerCase().replace(/\s+/g, " ");
  if (!query) return [...commands];
  return commands
    .map((c, i) => ({ c, i, s: matchPaletteWords(c.title, query, [c.section, c.keywords, c.id].join(" ")) }))
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .map((x) => x.c);
}

// ── terminal panes ───────────────────────────────────────

/** One machine's terminal tree (lib/terminals.ts), and the agent sessions it already lists as conversations. */
export interface MachinePanes {
  mid: string;
  machine: string;
  spaces: readonly TermSpace[];
  /** Session ids of the machine: an agent pane that is one of them is found as a conversation, not twice. */
  sessionIds: ReadonlySet<string>;
}

export interface TerminalHit {
  key: string;
  mid: string;
  /** The id the terminal opens by (an agent session's id, or "pane:<pane>"). */
  id: string;
  title: string;
  /** The place and what runs: machine › workspace › tab. */
  trail: string;
  detail: string;
  /** The agent in the pane ("" for a plain shell), and the client it runs in. */
  agent: string;
  surface?: AgentSurface;
  state: PaneState;
  to: Route;
  score: number;
}

/** How many panes a jump lists with nothing typed (a long list of shells would bury the conversations). */
export const IDLE_PANES = 8;

/** The terminal panes ⌘K finds: those whose row or surroundings hold every word typed. Nothing typed: the first few. */
export function terminalHits(machines: readonly MachinePanes[], query: string): TerminalHit[] {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const out: TerminalHit[] = [];
  const seen = new Set<string>();
  for (const m of machines) {
    for (const sp of m.spaces) {
      for (const tab of sp.tabs) {
        for (const p of tab.panes) {
          const key = `p:${m.mid}/${p.id}`;
          if (m.sessionIds.has(p.id) || seen.has(key)) continue;
          let s = 0.5;
          if (words.length) {
            const title = p.title.toLowerCase();
            const joined = words.join(" ");
            s = title.startsWith(joined) ? 90 : title.includes(joined) ? 70 : words.every((w) => `${p.haystack} ${m.machine.toLowerCase()}`.includes(w)) ? 35 : 0;
            if (!s) continue;
          }
          seen.add(key);
          out.push({
            key,
            mid: m.mid,
            id: p.id,
            title: p.title,
            trail: [m.machine, sp.label, tab.label === sp.label ? "" : tab.label].filter(Boolean).join(" › "),
            detail: p.detail,
            agent: p.agent,
            surface: p.surface,
            state: p.state,
            to: { name: "terminal", mid: m.mid, id: p.id },
            score: s,
          });
        }
      }
    }
  }
  out.sort((a, b) => b.score - a.score);
  return words.length ? out : out.slice(0, IDLE_PANES);
}

// ── Inbox and "Go to" (the Mac's quick open, MacQuickOpen) ──────────

/** Mac's match: the title starting with what is typed, then holding it, then every word somewhere; 1 with nothing typed. */
export function textScore(title: string, hay: readonly (string | undefined)[], text: string): number {
  const q = text.trim().toLowerCase();
  if (!q) return 1;
  const ti = title.toLowerCase();
  if (ti.startsWith(q)) return 100;
  if (ti.includes(q)) return 80;
  const all = [...hay, title].filter(Boolean).join(" ").toLowerCase();
  const words = q.split(/\s+/).filter(Boolean);
  return words.length && words.every((w) => all.includes(w)) ? 40 : 0;
}

/** A page ⌘K goes to: the sidebar's entries (NavPage), with the other words it answers to. */
export interface PlaceInput {
  key: string;
  title: string;
  icon: IconName;
  to: Route;
  /** More words that find it, space-separated (inbox mail lark…). */
  words?: string;
}

export interface PlaceHit {
  key: string;
  place: PlaceInput;
  score: number;
}

/** The pages whose name or words match, in the sidebar's order (all of them with nothing typed). */
export function placeHits(places: readonly PlaceInput[], text: string): PlaceHit[] {
  return places.flatMap((p) => {
    const score = textScore(p.title, (p.words ?? "").split(/\s+/), text);
    return score ? [{ key: `p:${p.key}`, place: p, score }] : [];
  });
}

/** What ⌘K reads of an inbox item (lib/inbox.ts InboxItem). */
export interface InboxLike {
  id: string;
  /** Whose inbox it is (several managers may list the same id). */
  manager?: string;
  title: string;
  sender?: string;
  preview: string;
  account_name?: string;
  updated_at?: string;
}

export interface InboxHit<T extends InboxLike = InboxLike> {
  key: string;
  item: T;
  score: number;
}

/** How many inbox items a jump lists (the Mac's otherLimit). */
export const INBOX_LIMIT = 5;

/** The open inbox items whose title, sender, preview or account match: the best first, then the newest. */
export function inboxHits<T extends InboxLike>(items: readonly T[], text: string, limit = INBOX_LIMIT): InboxHit<T>[] {
  return items
    .flatMap((item) => {
      const score = textScore(item.title, [item.sender, item.preview, item.account_name], text);
      return score ? [{ key: item.manager ? `i:${item.manager}:${item.id}` : `i:${item.id}`, item, score }] : [];
    })
    .sort((a, b) => b.score - a.score || byTime(b.item.updated_at, a.item.updated_at))
    .slice(0, limit);
}

/** The groups in their order, empty ones dropped, unless a later group has a better kind of match (typing "Settings" puts "Go to"
 *  above a conversation that merely mentions it): by the best score's tier (score / 20), ties in the order given. */
export function orderGroups<T extends { score: number }>(groups: readonly (readonly T[])[]): T[][] {
  const tier = (g: readonly T[]) => Math.floor(Math.max(...g.map((x) => x.score)) / 20);
  return groups
    .map((g, i) => ({ g, i }))
    .filter((x) => x.g.length > 0)
    .sort((a, b) => tier(b.g) - tier(a.g) || a.i - b.i)
    .map((x) => [...x.g]);
}
