import { describe, expect, it } from "vitest";
import { lookupKey, lookupUnavailable, normalizeEntry, normalizeState, textLookupApi, withWordSaved } from "./client";
import type { api } from "../lib/api";

describe("text lookup responses", () => {
  it("treats a missing or unavailable provider as inert", () => {
    expect(normalizeState(null)).toEqual({ available: false, underline_saved: false, words: [] });
    expect(normalizeState({ available: false, words: ["hello"] }).available).toBe(false);
  });

  it("keeps only real words and the underline switch", () => {
    const state = normalizeState({ available: true, name: "Example", underline_saved: true, words: ["Hello", "a", "你好", 3, "well-known"] });
    expect(state.words).toEqual(["Hello", "well-known"]);
    expect(state.underline_saved).toBe(true);
    expect(state.name).toBe("Example");
  });

  it("saves and removes through the word routes", async () => {
    const calls: { path: string; method?: string; json?: unknown }[] = [];
    const request = (async (path: string, init?: { method?: string; json?: unknown }) => {
      calls.push({ path, method: init?.method, json: init?.json });
      if (path.endsWith("/lookup")) return { found: true, word: "hello", phonetic: "/həˈloʊ/", senses: [{ pos: "interjection", text: "a greeting" }], saved: false };
      return { saved: path.endsWith("/words") };
    }) as unknown as typeof api;
    const client = textLookupApi(request);
    const entry = await client.lookup("hello", "Say hello there.");
    expect(entry.senses[0]?.text).toBe("a greeting");
    await client.save("hello", "Say hello there.");
    await client.remove("hello");
    expect(calls.map((call) => [call.method ?? "GET", call.path])).toEqual([
      ["POST", "/text-lookup/lookup"],
      ["POST", "/text-lookup/words"],
      ["DELETE", "/text-lookup/words/hello"],
    ]);
    expect(calls[1]?.json).toMatchObject({ word: "hello", source: "web" });
    expect(normalizeEntry({ found: false, senses: [{ text: "" }, { text: "kept" }] }, "hello").senses).toEqual([{ text: "kept" }]);
  });

  it("adds and drops a saved word without duplicating it", () => {
    const state = { available: true, underline_saved: true, words: ["hello"] };
    expect(withWordSaved(state, "Hello", true).words).toEqual(["Hello"]);
    expect(withWordSaved(state, "hello", false).words).toEqual([]);
  });

  it("normalizes lookup keys by lemma when present and falls back to word", () => {
    expect(lookupKey({ word: "Deployed", lemma: "Deploy" })).toBe("deploy");
    expect(lookupKey({ word: "Don\u2019t", lemma: "  " })).toBe("don't");
    expect(lookupKey({ word: "Hello" })).toBe("hello");
  });

  it("recognizes the provider going away", () => {
    expect(lookupUnavailable({ code: "text_lookup_unavailable" })).toBe(true);
    expect(lookupUnavailable({ code: "internal" })).toBe(false);
    expect(lookupUnavailable(new Error("no"))).toBe(false);
  });
});
