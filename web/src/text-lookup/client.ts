// The text-lookup App API (#10977). Nothing here runs unless the hub's hello lists `text_lookup`.
import { api, enc, type ApiError } from "../lib/api";
import type { TextLookupLookupResult, TextLookupSense } from "../lib/extension-wire";
import { isLookupWord } from "./word";

export const TEXT_LOOKUP_FEATURE = "text_lookup";
export const TEXT_LOOKUP_CHANGED = "text_lookup_changed";
/** Same window event lib/extension-prompts.ts dispatches when an extension is installed, removed or reconfigured. */
export const EXTENSIONS_CHANGED = "persona:extensions-changed";
/** How often a tab that comes back may ask again. */
export const FOCUS_REFRESH_MS = 15_000;

/** `GET /api/v1/text-lookup` (server/internal/extmgr/text_lookup.go `textLookupState`; not a generated wire type). */
export interface LookupState {
  available: boolean;
  extension?: string;
  contribution?: string;
  name?: string;
  underline_saved: boolean;
  words: string[];
}

/** One meaning, as the generated wire type has it. */
export type LookupSense = TextLookupSense;

/** `POST /api/v1/text-lookup/lookup`: the extension's generated result, with `senses` always present. */
export type LookupEntry = TextLookupLookupResult & { senses: LookupSense[] };

type Request = typeof api;

export function normalizeState(raw: unknown): LookupState {
  const o = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  const words = Array.isArray(o.words) ? o.words.filter((w): w is string => typeof w === "string" && isLookupWord(w)) : [];
  return {
    available: o.available === true,
    extension: typeof o.extension === "string" ? o.extension : undefined,
    contribution: typeof o.contribution === "string" ? o.contribution : undefined,
    name: typeof o.name === "string" ? o.name : undefined,
    underline_saved: o.underline_saved === true,
    words,
  };
}

export function normalizeEntry(raw: unknown, fallbackWord: string): LookupEntry {
  const o = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  const senses = Array.isArray(o.senses)
    ? o.senses.flatMap((item) => {
        if (!item || typeof item !== "object") return [];
        const sense = item as Record<string, unknown>;
        const text = typeof sense.text === "string" ? sense.text : "";
        if (!text) return [];
        return [{ pos: typeof sense.pos === "string" ? sense.pos : undefined, text }];
      })
    : [];
  return {
    found: o.found === true,
    word: typeof o.word === "string" && o.word ? o.word : fallbackWord,
    lemma: typeof o.lemma === "string" ? o.lemma : undefined,
    phonetic: typeof o.phonetic === "string" ? o.phonetic : undefined,
    senses,
    saved: o.saved === true,
  };
}

export function textLookupApi(request: Request = api) {
  return {
    state: () => request<unknown>("/text-lookup").then(normalizeState),
    lookup: (word: string, context?: string) =>
      request<unknown>("/text-lookup/lookup", { method: "POST", json: { word, ...(context ? { context } : {}) } }).then((raw) => normalizeEntry(raw, word)),
    save: (word: string, context?: string) =>
      request<{ saved?: boolean }>("/text-lookup/words", { method: "POST", json: { word, ...(context ? { context } : {}), source: "web" } }),
    remove: (word: string) => request<{ saved?: boolean }>(`/text-lookup/words/${enc(word)}`, { method: "DELETE" }),
  };
}

export type LookupApi = ReturnType<typeof textLookupApi>;

/** The dictionary is gone: the hub's coded refusal, not a generic failure. */
export function lookupUnavailable(error: unknown): boolean {
  return typeof error === "object" && error !== null && (error as ApiError).code === "text_lookup_unavailable";
}

/** The form a looked-up word is saved, removed and underlined by: the lemma when the extension gave one, as iOS's
 * `TextLookupEntry.key` (so "deployed" is saved as "deploy", and both clients keep one list). */
export function lookupKey(entry: Pick<LookupEntry, "word" | "lemma">): string {
  const lemma = entry.lemma?.trim();
  return (lemma || entry.word).trim().replace(/\u2019/g, "'").toLowerCase();
}

export function withWordSaved(state: LookupState, word: string, saved: boolean): LookupState {
  const key = lookupKey({ word });
  const words = state.words.filter((item) => lookupKey({ word: item }) !== key);
  return { ...state, words: saved ? [...words, word] : words };
}

/** The latest state the root listener read. The popover and the underline share it. */
let snapshot: LookupState | null = null;
const listeners = new Set<() => void>();

export function lookupSnapshot(): LookupState | null {
  return snapshot;
}

export function replaceLookupSnapshot(next: LookupState | null): void {
  snapshot = next;
  listeners.forEach((l) => l());
}

export function subscribeLookup(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export interface LookupBridge {
  setWordSaved(word: string, saved: boolean): void;
  refresh(): void;
}

let bridge: LookupBridge | null = null;

export function bindLookupBridge(next: LookupBridge | null): void {
  bridge = next;
}

export function lookupSetSaved(word: string, saved: boolean): void {
  bridge?.setWordSaved(word, saved);
}

export function lookupRefresh(): void {
  bridge?.refresh();
}
