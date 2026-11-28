// Which agent a row, a header or a launcher is: the `agent` string the node reports (herdr's own name for the program in
// the pane, a runtime's id, "" for Claude Code) turned into one of the kinds that have a mark (ui/AgentMark.tsx), or
// left as it is for any other. Nothing here decides what the mark looks like.
import { t } from "../i18n";
import type { Session } from "./types";

const NAMED_AGENTS = ["claude", "codex", "opencode", "pi", "gemini", "cursor", "trae"] as const;
/** The agents whose own mark is drawn (ui/AgentMark.tsx). */
export const MARKED_AGENTS = [...NAMED_AGENTS, "grok", "agy", "dsh", "kimi", "copilot"] as const;
export type MarkedAgent = (typeof MARKED_AGENTS)[number];

/** Other names for the same program (the CLI's binary, the product's own name), by the lowercased `agent` string with
 *  its spaces, dots, dashes and underscores left out ("trae-cli", "TRAE CLI" → traecli). */
const ALIASES: Record<string, string> = {
  claudecode: "claude",
  codexcli: "codex",
  geminicli: "gemini",
  cursoragent: "cursor",
  piweb: "pi",
  traecli: "trae",
};

// Alternate product names for the artwork. Provider IDs still key filters and launchers.
const MARK_ALIASES: Record<string, MarkedAgent> = {
  traex: "trae",
  grokbuild: "grok",
  grokchat: "grok",
  grokcli: "grok",
  antigravity: "agy",
  antigravitycli: "agy",
  googleantigravity: "agy",
  deepseekharness: "dsh",
  kimicli: "kimi",
  copilotcli: "copilot",
  githubcopilot: "copilot",
  githubcopilotcli: "copilot",
};

const agentKey = (id: string) => id.replace(/[\s._-]/g, "");

/** The agent a reported `agent` string names: lowercase, aliases folded in, "" (Claude Code reports nothing) is Claude. */
export function agentId(raw: string | undefined | null): string {
  const id = (raw ?? "").trim().toLowerCase();
  return ALIASES[agentKey(id)] ?? (id || "claude");
}

/** The built-in type and the CLI's public command name share a display name. */
export function isTraeCli(raw: string | undefined | null): boolean {
  return /^trae(?:x|cli)$/.test(agentKey((raw ?? "").toLowerCase()));
}

export function normalizeTraeCliName<T extends string | undefined>(name: T): T | string {
  return isTraeCli(name) ? t("agent-mark.trae_cli") : name;
}

export function isMarked(id: string): id is MarkedAgent {
  return (MARKED_AGENTS as readonly string[]).includes(id);
}

/** The agent whose mark is drawn: the reported one, else the one its name says when the app has that mark (a runtime's
 *  provider named "Cursor" or "TRAE CLI", #8142). */
export function markId(raw: string | undefined | null, label?: string): string {
  const id = brandedId(agentId(raw));
  if (isMarked(id)) return id;
  const product = label?.split("·", 1)[0]?.trim();
  const named = product ? brandedId(agentId(product)) : "";
  return isMarked(named) ? named : id;
}

function brandedId(id: string): string {
  const key = agentKey(id);
  return Object.hasOwn(MARK_ALIASES, key) ? MARK_ALIASES[key] : id;
}

/** The id Paseo reports as the agent of each provider the app has no id for (Grok, Kimi, Cursor…: the bridge's
 *  appAgent, integrations/paseo/lib/map.js). It names the engine, never an agent (DESIGN_SYSTEM.md 5.6). */
const ENGINE_AGENT = "paseo";

/** The agent a row or a launcher runs as the app tells agents apart: its reported `agent`, except the engine's own id
 *  (above), where its provider is the agent (`provider`: the row's launcher, the launcher's name), so each provider has
 *  its own mark, name and filter (#8142). */
export function providerAgent(agent: string | undefined | null, provider: string | undefined): string {
  return provider && agent?.trim().toLowerCase() === ENGINE_AGENT ? provider : (agent ?? "");
}

/** What the agent is called: the product's name for the ones the app knows, else the runtime's own label, else the string as reported. */
export function agentName(raw: string | undefined | null, label?: string): string {
  if (isTraeCli(raw)) return t("agent-mark.trae_cli");
  const id = agentId(raw);
  if ((NAMED_AGENTS as readonly string[]).includes(id) || id === "omp") return t(`agent-mark.${id}`); // spec/fixtures/agent-names
  return label?.trim() || (raw ?? "").trim() || t("agent-mark.unknown");
}

/** The agent of a session, the same way `agentLabel` (lib/mentions.ts) tells: a Codex thread read off the app-server or history is Codex whatever it reports;
 *  one of Paseo's providers is that provider (providerAgent). */
export function sessionAgent(s: Pick<Session, "agent" | "agent_label" | "source_kind" | "agent_surface" | "launcher">): { agent: string; label?: string; surface?: AgentSurface } {
  const codex = s.source_kind === "codex_app_server" || s.source_kind === "codex_history";
  return { agent: codex ? "codex" : providerAgent(s.agent, s.launcher), label: s.agent_label, surface: agentSurface(s.agent_surface) };
}

/** What a session's agent is called (agentName): "Claude Code", "Codex", a provider's label ("Grok"). */
export function sessionAgentName(s: Parameters<typeof sessionAgent>[0]): string {
  const { agent, label } = sessionAgent(s);
  return agentName(agent, label);
}

/** The client an agent runs in (the node's `agent_surface`, #6390): its desktop app, or its command line in a terminal. */
export type AgentSurface = "desktop" | "cli";

/** A reported surface the app knows, else none: an older node, an SDK, a runtime say nothing. */
export function agentSurface(raw: string | undefined | null): AgentSurface | undefined {
  return raw === "desktop" || raw === "cli" ? raw : undefined;
}

/** The letter drawn for an agent with no mark of its own: the first letter or digit of its name, upper case. */
export function agentLetter(name: string): string {
  const first = Array.from(name.trim()).find((c) => /[\p{L}\p{N}]/u.test(c));
  return first ? first.toUpperCase() : "?";
}
