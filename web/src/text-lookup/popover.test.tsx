// @vitest-environment jsdom
// The word card (#10977): what it asks the hub, Save / Saved, a failed save, Escape and focus return.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => ({ list: [] as { path: string; method?: string; json?: unknown }[], failSave: false, word: "deploy" }));
vi.mock("../lib/api", async (load) => {
  const real = await load<typeof import("../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, init: { method?: string; json?: unknown } = {}) => {
      calls.list.push({ path, method: init.method, json: init.json });
      if (path === "/text-lookup/lookup") {
        return { found: true, word: calls.word, lemma: "deploy", phonetic: "/dɪˈplɔɪ/", senses: [{ pos: "v.", text: "to put into use" }], saved: false };
      }
      if (calls.failSave && path.startsWith("/text-lookup/words")) throw new real.ApiError(502, "no answer", { code: "extension_unavailable" });
      return { saved: init.method !== "DELETE" };
    }),
  };
});

import { replaceLookupSnapshot, lookupSnapshot, bindLookupBridge } from "./client";
import LookupPopover from "./popover";

let root: Root;
let host: HTMLDivElement;
let opener: HTMLButtonElement;
const hit = { word: "deploy", context: "We deploy on Friday.", rect: { left: 10, right: 60, top: 10, bottom: 30 } };

beforeEach(() => {
  calls.list = [];
  calls.failSave = false;
  calls.word = "deploy";
  replaceLookupSnapshot({ available: true, name: "Relingo", underline_saved: true, words: [] });
  bindLookupBridge({
    setWordSaved(word, saved) {
      const s = lookupSnapshot()!;
      replaceLookupSnapshot({ ...s, words: saved ? [...s.words, word] : s.words.filter((w) => w !== word) });
    },
    refresh() {},
  });
  opener = document.createElement("button");
  document.body.append(opener);
  opener.focus();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.replaceChildren();
  bindLookupBridge(null);
  replaceLookupSnapshot(null);
});

async function open(onClose = vi.fn()) {
  await act(async () => root.render(<LookupPopover hit={hit} onClose={onClose} />));
  await act(async () => {});
  return { card: document.querySelector<HTMLElement>("[data-testid=text-lookup]")!, onClose };
}

const saveButton = (card: HTMLElement) => card.querySelector<HTMLButtonElement>("button[aria-pressed]")!;

describe("the word card", () => {
  it("asks for the word with its sentence and shows it as a labelled, focused dialog", async () => {
    const { card } = await open();
    expect(calls.list[0]).toEqual({ path: "/text-lookup/lookup", method: "POST", json: { word: "deploy", context: "We deploy on Friday." } });
    expect(card.getAttribute("role")).toBe("dialog");
    expect(document.getElementById(card.getAttribute("aria-labelledby")!)?.textContent).toBe("deploy");
    expect(card.textContent).toContain("/dɪˈplɔɪ/");
    expect(card.textContent).toContain("to put into use");
    expect(card.textContent).toContain("Relingo");
    expect(card.hasAttribute("data-no-lookup")).toBe(true);
    expect(document.activeElement).toBe(card);
  });

  it("saves, then removes, the word and keeps the shared list in step", async () => {
    const { card } = await open();
    expect(saveButton(card).getAttribute("aria-pressed")).toBe("false");
    await act(async () => saveButton(card).click());
    expect(calls.list.at(-1)).toEqual({ path: "/text-lookup/words", method: "POST", json: { word: "deploy", context: "We deploy on Friday.", source: "web" } });
    expect(saveButton(card).getAttribute("aria-pressed")).toBe("true");
    expect(lookupSnapshot()?.words).toEqual(["deploy"]);
    await act(async () => saveButton(card).click());
    expect(calls.list.at(-1)).toEqual({ path: "/text-lookup/words/deploy", method: "DELETE", json: undefined });
    expect(saveButton(card).getAttribute("aria-pressed")).toBe("false");
    expect(lookupSnapshot()?.words).toEqual([]);
  });

  it("saves and removes an inflected word by its lemma, as iOS does", async () => {
    calls.word = "deployed";
    const { card } = await open();
    await act(async () => saveButton(card).click());
    expect(calls.list.at(-1)).toEqual({ path: "/text-lookup/words", method: "POST", json: { word: "deploy", context: "We deploy on Friday.", source: "web" } });
    expect(lookupSnapshot()?.words).toEqual(["deploy"]);
    await act(async () => saveButton(card).click());
    expect(calls.list.at(-1)).toEqual({ path: "/text-lookup/words/deploy", method: "DELETE", json: undefined });
    expect(lookupSnapshot()?.words).toEqual([]);
  });

  it("shows an inflected word as saved when its lemma is in the list", async () => {
    calls.word = "deployed";
    replaceLookupSnapshot({ available: true, name: "Relingo", underline_saved: true, words: ["Deploy"] });
    const { card } = await open();
    expect(saveButton(card).getAttribute("aria-pressed")).toBe("true");
  });

  it("puts the button back and says so when a save fails", async () => {
    calls.failSave = true;
    const { card } = await open();
    await act(async () => saveButton(card).click());
    expect(saveButton(card).getAttribute("aria-pressed")).toBe("false");
    expect(card.querySelector("[role=alert]")?.textContent).toBeTruthy();
    expect(lookupSnapshot()?.words).toEqual([]);
  });

  it("closes on Escape and gives focus back to where it was", async () => {
    const onClose = vi.fn();
    await open(onClose);
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(onClose).toHaveBeenCalled();
    act(() => root.render(<></>));
    expect(document.activeElement).toBe(opener);
  });

  it("closes when the dictionary goes away", async () => {
    const { onClose } = await open();
    act(() => replaceLookupSnapshot({ available: false, underline_saved: false, words: [] }));
    expect(onClose).toHaveBeenCalled();
  });
});
