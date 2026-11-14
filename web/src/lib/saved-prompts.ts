// The user's saved prompts (#7329, docs/web/web.md "Saved prompts"): the "/" prompts they keep on the hub, the current
// assistant's (GET /api/v1/assistant/saved-prompts, docs/protocol/protocol.md "REST (App → hub)"). One list for the page,
// read again when the hub comes back, when another device changes it (assistant_saved_prompts) and when another
// assistant becomes the current one; Settings › Saved Prompts and every composer's "/" list read it. A chunk of its own:
// lib/saved-prompt-rows.ts loads it the first time a composer has the focus, the settings page imports it.
import { useEffect, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, ApiError, credentialOwner, errorText, getToken, subscribeCredentialOwner } from "./api";
import type { ComposerCommand } from "./extension-prompts";
import { chosenManager, currentHeaders, defaultKnown, onDefaultManager, onManagerChoice, shownManager } from "./manager-choice";
import { setSavedPromptRows } from "./saved-prompt-rows";
import { onHubEvent } from "./store";

/** The hub's limits (server/internal/assistant/saved_prompts.go). */
export const SAVED_PROMPT_NAME_MAX = 32;
export const SAVED_PROMPT_TEXT_MAX = 8000;
export const SAVED_PROMPTS_MAX = 100;

const PATH = "/assistant/saved-prompts";

export interface SavedPrompt {
  id: string;
  name: string;
  text: string;
  created_at?: string;
  updated_at?: string;
}

/** The list as the page has it: `unsupported` is an assistant from before saved prompts (its hub answers 404); `error`
 *  the last read that failed (a list read before stays). */
export interface SavedPromptsView {
  manager: string;
  prompts: readonly SavedPrompt[];
  status: "loading" | "ready" | "failed" | "unsupported";
  error: string;
}

let view: SavedPromptsView = { manager: "", prompts: [], status: "loading", error: "" };
const listeners = new Set<() => void>();
let stop: (() => void)[] | null = null;
let reads = 0;
let managerGeneration = 0;
let lastManager = shownManager();
let lastDefaultKnown = defaultKnown();
let viewOwner = "";
// Switching away and back still retires the earlier assistant's work.
onManagerChoice(() => {
  const manager = shownManager();
  const known = defaultKnown();
  // Initial discovery identifies the existing implicit default; a later replacement retires it.
  if (manager !== lastManager && (lastDefaultKnown || !known)) {
    managerGeneration++;
  }
  lastManager = manager;
  lastDefaultKnown = known;
});
onDefaultManager(() => { lastDefaultKnown = defaultKnown(); });

function requestOwner() {
  const credential = credentialOwner();
  const manager = shownManager();
  const unresolvedDefault = !defaultKnown() && chosenManager() === null;
  const generation = managerGeneration;
  const key = JSON.stringify([credential, manager, generation]);
  const valid = () => credential === credentialOwner() && generation === managerGeneration;
  const current = () => valid() && manager === shownManager();
  // An unidentified default's response cannot be applied optimistically, but its completion can refresh
  // the canonical list if no actual assistant or credential switch has intervened.
  const recoverable = () => unresolvedDefault && valid() && chosenManager() === null;
  return {
    manager, key, current, recoverable, identified: !unresolvedDefault,
    options: { token: getToken(), tokenOwner: credential, headers: currentHeaders(), isAuthContextCurrent: current },
  };
}

function show(next: SavedPromptsView) {
  view = next;
  setSavedPromptRows(next.status === "ready" ? savedPromptCommands(next.prompts) : []);
  listeners.forEach((l) => l());
}

const isPrompt = (p: unknown): p is SavedPrompt =>
  !!p && typeof p === "object" && typeof (p as SavedPrompt).id === "string" && typeof (p as SavedPrompt).name === "string" && typeof (p as SavedPrompt).text === "string";

/** Reads the current assistant's list; the latest read wins. */
export async function reloadSavedPrompts(): Promise<void> {
  const n = ++reads;
  const owner = requestOwner();
  const { manager } = owner;
  if (viewOwner !== owner.key) {
    viewOwner = owner.key;
    show({ manager, prompts: [], status: "loading", error: "" });
  }
  try {
    const r = await api<{ prompts?: unknown }>(PATH, owner.options);
    if (n !== reads || !owner.current()) return;
    show({ manager, prompts: Array.isArray(r.prompts) ? r.prompts.filter(isPrompt) : [], status: "ready", error: "" });
  } catch (e) {
    if (n !== reads || !owner.current()) return;
    if (e instanceof ApiError && e.status === 404) return show({ manager, prompts: [], status: "unsupported", error: "" });
    show({ ...view, manager, status: view.status === "ready" ? "ready" : "failed", error: errorText(e) });
  }
}

/** Starts following the list (once a page): read now, and again on each of the events above. */
export function startSavedPrompts() {
  if (stop) return;
  credentialOwner();
  const reload = () => void reloadSavedPrompts();
  stop = [
    onHubEvent("hello", reload),
    onHubEvent("assistant_saved_prompts", reload), // only the current assistant's (lib/store.ts PAGE_EVENTS)
    onManagerChoice(reload),
    onDefaultManager((was) => { if (!was.settled && was.id === shownManager()) reload(); }),
    subscribeCredentialOwner(reload),
  ];
  reload();
}

const subscribe = (l: () => void) => (listeners.add(l), () => void listeners.delete(l));
const snapshot = () => view;

/** The list, following it from the first use on. */
export function useSavedPrompts(): SavedPromptsView {
  useEffect(startSavedPrompts, []);
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

/** The prompts as rows of the "/" list: the word is the name, picking one fills the composer with its text (never sends
 *  it, as an extension's quick prompt does), and the row's source says it is one of yours. */
export function savedPromptCommands(prompts: readonly SavedPrompt[]): ComposerCommand[] {
  if (!prompts.length) return [];
  const source = t("chat-savedprompts.source");
  return prompts.map((p) => ({ key: `saved:${p.id}`, word: `/${p.name}`, desc: "", text: p.text, source }));
}

/** A name as the hub keeps it: trimmed, without a "/" typed before it. */
export function savedPromptName(name: string): string {
  return name.trim().replace(/^\//, "");
}

// letters of any script, digits, combining marks and - _ . (the hub's savedPromptNameRune)
const NAME = /^[\p{L}\p{Nd}\p{Mn}_.-]+$/u;

/** Why the hub would refuse this name, before asking it: "" when it would not. `id`: the prompt being edited, whose own
 *  name is no clash. */
export function savedPromptNameProblem(raw: string, prompts: readonly SavedPrompt[], id: string | null): string {
  const name = savedPromptName(raw);
  if (!name || [...name].length > SAVED_PROMPT_NAME_MAX || !NAME.test(name)) return t("chat-savedprompts.name_rule", { max: SAVED_PROMPT_NAME_MAX });
  if (prompts.some((p) => p.id !== id && p.name.toLowerCase() === name.toLowerCase())) return t("chat-savedprompts.name_taken", { name });
  return "";
}

/** Why the hub would refuse this text: "" when it would not. */
export function savedPromptTextProblem(text: string): string {
  const body = text.trim();
  if (!body || [...body].length > SAVED_PROMPT_TEXT_MAX) return t("chat-savedprompts.text_rule", { max: SAVED_PROMPT_TEXT_MAX });
  return "";
}

/** A name to start a prompt saved from a message with: its first words, joined by "-", fit for a name. */
export function suggestedSavedPromptName(text: string, prompts: readonly SavedPrompt[]): string {
  const words = text.trim().split(/\s+/).map((w) => [...w].filter((c) => NAME.test(c)).join("")).filter(Boolean);
  let base = "";
  for (const w of words) {
    const next = base ? `${base}-${w}` : w;
    if ([...next].length > 20) {
      if (!base) base = [...w].slice(0, 12).join(""); // one long word (a sentence with no spaces): its start
      break;
    }
    base = next;
    if ([...base].length >= 12) break;
  }
  base = base.toLowerCase() || "prompt";
  const taken = new Set(prompts.map((p) => p.name.toLowerCase()));
  if (!taken.has(base)) return base;
  for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`;
}

function keep(owner: string, change: (prompts: readonly SavedPrompt[]) => readonly SavedPrompt[]) {
  if (view.status === "ready" && viewOwner === owner) show({ ...view, prompts: change(view.prompts), error: "" });
}

/** Adds a prompt (`id` null) or changes one; the list shows it at once and is read again. */
export async function saveSavedPrompt(id: string | null, input: { name: string; text: string }): Promise<SavedPrompt> {
  const owner = requestOwner();
  const body = { name: savedPromptName(input.name), text: input.text.trim() };
  const r = await api<{ prompt: SavedPrompt }>(id ? `${PATH}/${encodeURIComponent(id)}` : PATH, { ...owner.options, method: id ? "PUT" : "POST", json: body });
  if (owner.current() && owner.identified) {
    keep(owner.key, (prompts) => (id ? prompts.map((p) => (p.id === id ? r.prompt : p)) : [...prompts, r.prompt]));
  }
  if (owner.current() || owner.recoverable()) void reloadSavedPrompts();
  return r.prompt;
}

/** Deletes a prompt; one already gone is fine. */
export async function deleteSavedPrompt(id: string): Promise<void> {
  const owner = requestOwner();
  await api(`${PATH}/${encodeURIComponent(id)}`, { ...owner.options, method: "DELETE" });
  if (owner.current() && owner.identified) {
    keep(owner.key, (prompts) => prompts.filter((p) => p.id !== id));
  }
  if (owner.current() || owner.recoverable()) void reloadSavedPrompts();
}

/** Moves a prompt one place up (-1) or down (1) in the "/" list. The hub refuses an order made from a list that changed
 *  since (another device added or deleted one): the list is read again and the error goes up. */
export async function moveSavedPrompt(id: string, by: -1 | 1): Promise<void> {
  const ids = view.prompts.map((p) => p.id);
  const i = ids.indexOf(id);
  const j = i + by;
  if (i < 0 || j < 0 || j >= ids.length) return;
  [ids[i], ids[j]] = [ids[j], ids[i]];
  const owner = requestOwner();
  try {
    const r = await api<{ prompts?: unknown }>(`${PATH}/order`, { ...owner.options, method: "PUT", json: { ids } });
    if (owner.current() && owner.identified && Array.isArray(r.prompts)) keep(owner.key, () => (r.prompts as unknown[]).filter(isPrompt));
    else if (owner.recoverable()) void reloadSavedPrompts();
  } catch (e) {
    if (owner.current() || owner.recoverable()) void reloadSavedPrompts();
    throw e;
  }
}

/** Tests: a fresh page. */
export function resetSavedPrompts() {
  view = { manager: "", prompts: [], status: "loading", error: "" };
  stop?.forEach((off) => off());
  stop = null;
  reads++;
  managerGeneration++;
  viewOwner = "";
  setSavedPromptRows([]);
  listeners.forEach((l) => l());
}
