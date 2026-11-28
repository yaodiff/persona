// Engines' names as people see them: proper nouns, the same in every language (the hub's proto.EngineNames). No translated
// words here, so the sidebar can name a row's engine on the first screen; lib/engines.ts adds what each engine is.
import { paneHost } from "./agent-groups";
import type { Machine, Session } from "./types";

export const ENGINE_NAMES: Readonly<Record<string, string>> = {
  herdr: "herdr",
  "herdr-core": "Herdr App",
  "herdr-both": "herdr + Herdr App",
  orca: "Orca",
  tern: "Tern",
  rex: "Rex",
  paseo: "Paseo",
  cindy: "Cindy",
  t3code: "T3 Code",
  botmux: "botmux",
};

/** An engine's name: the node's words first, then ours, then its id (a future engine). Host badges and the words that
 *  name a session's engine use it, never the raw id (#17053). */
export function engineName(id: string, m?: Pick<Machine, "engines">): string {
  return m?.engines?.find((e) => e.id === id)?.name || ENGINE_NAMES[id] || id;
}

/** The engine a session's terminal is in, by name, for the words that say where its tab or workspace is renamed or that
 *  it isn't running there (#17053): its pane's host (`pane` overrides the session's, a pane address's own), else its
 *  space's kind when that is an engine (an Orca chat has no pane), else herdr. */
export function sessionEngineName(s: Pick<Session, "pane_id" | "space_kind"> | undefined, m?: Pick<Machine, "engines">, pane?: string): string {
  const p = pane ?? s?.pane_id;
  const kind = p ? paneHost(p) : s?.space_kind;
  return engineName(kind && ENGINE_NAMES[kind] ? kind : "herdr", m);
}
