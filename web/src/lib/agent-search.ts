// ⌘K's search over Agent sessions (#6589): the Agents list's own field went, and Search chats finds what it found. Loaded
// with the first query typed (views/Palette.tsx), as lib/assistant-search.ts is: off the first screen and the idle tier
// (src/startup-preload.ts); the list itself only reads the query it is given.
import { baseName, sessionTitle } from "./format";
import { machineWithHub } from "./mesh";
import {
  buildEvidenceSnippet,
  compileSearchAsCode,
  DEFAULT_DIMENSION_FILTERS,
  NO_FACETS,
  parseSearchQuery,
  scopeHaystacks,
  sessionProjectName,
  type EvalContext,
  type EvidenceSnippet,
} from "./agent-chatlist-filter";
import type { Session } from "./types";

const words = (query: string) => query.trim().split(/\s+/).filter(Boolean);

/** How many operators a query has (`machine:`, `project:`, `is:pinned`, `since:`…): with one it speaks of Agent sessions. */
export function conditionCount(query: string): number {
  return words(query).length - parseSearchQuery(query).terms.length;
}
export const hasSearchOperators = (query: string): boolean => conditionCount(query) > 0;

// ⌘K's Filter menu (#6589) writes its choices into the query as these operators, so what is chosen stays text to read
// and change, and "Filter Agents list" carries it to the list. A value with a space has no operator spelling.
export { buildAgentFacets } from "./agent-chatlist-filter";
export const conditionValue = (value: string): boolean => value !== "" && !/\s/.test(value);

// The parser's operator aliases (agent-chatlist-filter.ts), shared by the menu's reads, toggles and drops.
const CONDITION_KEYS: Record<string, string[]> = {
  machine: ["machine", "host"],
  project: ["project", "repo", "cwd", "path"],
  harness: ["harness", "agent", "launcher"],
  state: ["state", "status"],
  since: ["since", "after", "updated"],
};
const conditionKeys = (key: string) => CONDITION_KEYS[key] ?? [key];

/** The values the query holds for an operator, as typed (`machine:box` → "box" for "machine"). */
export function conditionValues(query: string, key: string): string[] {
  const prefixes = conditionKeys(key).map((k) => `${k}:`);
  return words(query).filter((w) => prefixes.some((p) => w.toLowerCase().startsWith(p) && w.length > p.length)).map((w) => {
    const value = w.slice(w.indexOf(":") + 1);
    return key === "is" && value.toLowerCase() === "pin" ? "pinned" : value;
  });
}

/** The query with `key:value` added, or taken out where it was; `only`: the one value its operator has (`since:`). */
export function toggleCondition(query: string, key: string, value: string, only = false): string {
  const tokens = conditionKeys(key).map((k) => `${k}:${value}`.toLowerCase());
  if (key === "is" && value === "pinned") tokens.push("is:pin");
  const had = words(query).some((w) => tokens.includes(w.toLowerCase()));
  const kept = words(query).filter((w) => !tokens.includes(w.toLowerCase()) &&
    !(only && conditionKeys(key).some((k) => w.toLowerCase().startsWith(`${k}:`))));
  return [...kept, ...(had ? [] : [`${key}:${value}`])].join(" ");
}

/** The query without an operator's values, or without every operator (the words stay). */
export function dropConditions(query: string, key?: string): string {
  if (!key) return parseSearchQuery(query).terms.join(" ");
  return words(query).filter((w) => !conditionKeys(key).some((k) => w.toLowerCase().startsWith(`${k}:`))).join(" ");
}

/** How ⌘K ranks an Agent session (#6589). */
export interface AgentSearchHit {
  /** 0: no match. 100 the name starts with what was typed, 80 contains it, 60 every word is in the name, the project or
   *  the computer (with the space, tab and folder), 40 every word is somewhere the list's Full text reads (the last line,
   *  what was asked, the branch, the model, an error), 30 operators alone, 1 nothing typed. */
  score: number;
  /** Found by what is inside it (40): the line that matched, the match apart. */
  snippet?: EvidenceSnippet;
}

/** ⌘K's match for an Agent session: the list's operators narrow (plain words never filter, #4397), then what was typed
 *  ranks the name first, then the project and computer, then the rest (`AgentSearchHit.score`). */
export function agentSearchHit(s: Session, query: string, ctx: EvalContext): AgentSearchHit {
  const parsed = parseSearchQuery(query);
  const tokens = words(query);
  const operators = tokens.filter((w) => !parsed.terms.includes(w)).join(" ");
  if (operators) {
    const plan = compileSearchAsCode(operators, { ...DEFAULT_DIMENSION_FILTERS, query: operators }, NO_FACETS);
    if (!plan.predicates.every((p) => p(s, ctx))) return { score: 0 };
  }
  const terms = parsed.terms.map((w) => w.toLowerCase());
  if (terms.length === 0) return { score: operators ? 30 : 1 };
  const text = parsed.text.toLowerCase();
  const title = sessionTitle(s).toLowerCase();
  if (title.startsWith(text)) return { score: 100 };
  if (title.includes(text)) return { score: 80 };
  const place = [title, sessionProjectName(s), machineWithHub(s.machine_id, ctx.machines), s.machine_id, s.space_name, s.slot_name, baseName(s.cwd)]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  if (terms.every((w) => place.includes(w))) return { score: 60 };
  const inside = scopeHaystacks(s, "all", ctx).map((h) => h.text).join("\n").toLowerCase();
  if (!terms.every((w) => place.includes(w) || inside.includes(w))) return { score: 0 };
  return { score: 40, snippet: buildEvidenceSnippet(s, parsed.text, "all", ctx) ?? undefined };
}
