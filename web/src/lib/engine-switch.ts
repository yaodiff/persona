// A conversation that moved between Claude Code and Codex (#5054). The first message the new engine got carries the
// handoff in a `<persona-context kind="engine-switch" …>` block (the chat draws the switch divider from it, never a
// context chip), and the row running the conversation now names the sessions that ran it before (Session.engine_history),
// whose transcripts the chat reads in order so the conversation reads as one (docs/protocol/protocol.md "Switching a
// running Agent to another engine").
import type { EngineLeg, Item, Session } from "./types";

/** The `kind` of the block the handoff is carried in (the node's handoffBlock). */
export const ENGINE_SWITCH_KIND = "engine-switch";

/** One switch as its block says it: the engines ("claude" / "codex"), the model the new engine runs (when known),
 *  whether the conversation went back to an earlier session of that engine (`resumed="1"`), and the handoff text. */
export interface EngineSwitch {
  from: string;
  to: string;
  model?: string;
  resumed: boolean;
  text: string;
}

/** The engines a conversation moves between. */
const ENGINES = ["claude", "codex", "agy"];

/** The switch a parsed `<persona-context>` block stands for (lib/turns.ts Context, its attributes already unescaped):
 *  only an engine-switch block from one known engine to the other. Any other block, a pasted one naming something else
 *  included, is undefined (it stays an ordinary context). */
export function engineSwitchOf(c: { kind: string; text: string; attrs?: Record<string, string> }): EngineSwitch | undefined {
  if (c.kind !== ENGINE_SWITCH_KIND) return undefined;
  const a = c.attrs ?? {};
  const from = a.from?.trim() ?? "";
  const to = a.to?.trim() ?? "";
  if (from === to || !ENGINES.includes(from) || !ENGINES.includes(to)) return undefined;
  return { from, to, model: a.model?.trim() || undefined, resumed: a.resumed === "1", text: c.text };
}

/** A time in ms, undefined when `s` is none (an item without a time, an odd `until`). */
export const atMs = (s: string | undefined): number | undefined => {
  const v = Date.parse(s ?? "");
  return Number.isFinite(v) ? v : undefined;
};

/** A key that changes only when the stretches do (the row's array is new with every push): "" when it never switched. */
export function stretchesKey(history: readonly EngineLeg[] | undefined): string {
  return (history ?? []).filter((l) => l.session_id).map((l) => `${l.session_id}@${l.until}`).join("|");
}

/** The live stretch's items when the live session ran an earlier stretch too (A → B → A): those written after the last
 *  switch, an item without a time going with the one before it (the first ones with the first timed one), as
 *  lib/engine-stretches.ts splitStretches places them. Undefined when the live stretch is all of its transcript. */
export function liveStretch(items: readonly Item[], history: readonly EngineLeg[] | undefined, sid: string): Item[] | undefined {
  const legs = (history ?? []).filter((l) => l.session_id);
  if (!legs.some((l) => l.session_id === sid)) return undefined;
  const after = atMs(legs[legs.length - 1].until);
  let at = items.map((i) => atMs(i.ts)).find((v) => v !== undefined);
  return items.filter((i) => {
    at = atMs(i.ts) ?? at;
    return at !== undefined && (after === undefined || at > after);
  });
}

/** The listed row a session's conversation moved on to (#5054): a session named in another listed row's
 *  engine_history and not listed itself opens that row's chat (`rows`: one machine's). */
export function engineSwitchHome(rows: readonly Session[], sid: string): Session | undefined {
  if (!sid || rows.some((r) => r.session_id === sid)) return undefined;
  return rows.find((r) => r.engine_history?.some((l) => l.session_id === sid));
}
