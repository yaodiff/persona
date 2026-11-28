// @vitest-environment jsdom
// The recording module (#13407, web half; docs/web/web.md "Modules"): a recording's parts of its knowledge page come
// through the `kb.detail` slot. On (the default), a recording's page draws its facts and transcript as before, and the
// module's chunk loads only when a recording opens, never for another entry. Off, existing recordings stay listed,
// searchable and readable through Core's read-only detail; the module's chunk, strings, write controls and live views
// stay absent.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../core/module-switch";
import { connectModuleHub, moduleKBKindAt, registerModule, resetModulesForTest, type ModuleDescriptor } from "../../core/module-host";
import { loadKBDetailParts, resetKBDetailPartsForTest } from "../../core/ModuleKBDetail";
import { setToken } from "../../lib/api";
import { setRosterDefault } from "../../lib/manager-choice";
import { parse } from "../../lib/router";
import { getState, reset, subscribe } from "../../lib/store";
import { lists, searches, searchKey } from "../../views/knowledge/cache";
import { EntryDetail } from "../../views/knowledge/KBDetail";
import { ListPane } from "../../views/knowledge/KBList";
import { recording } from "./module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const dayID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";
const pausedID = "cccccccc-cccc-cccc-cccc-cccccccccccc";
const unknownPausedID = "dddddddd-dddd-dddd-dddd-dddddddddddd";
const detail = (kind: string, entry: string) => {
  const day = kind === "recordings" && entry === dayID;
  const paused = kind === "recordings" && entry === pausedID;
  const unknownPaused = kind === "recordings" && entry === unknownPausedID;
  return {
    detail: {
      kind, id: entry, path: `${kind}/${entry}`,
      meta: kind === "recordings" ? { title: day ? "Tuesday" : paused ? "Kept recording, minutes pending" : "Design review", started_at: "2026-11-10T06:00:00Z", complete: true, kind: day ? "day" : "meeting", ...(day ? { day: "2026-11-10" } : {}) } : {},
      paused: paused ? "module_off" : unknownPaused ? "future_reason" : undefined,
      segments: kind === "recordings" ? [{ start: 0, end: 4, text: day ? "Morning transcript." : paused ? "Its minutes were never written." : "Keep the export cap." }] : undefined,
      minutes: kind === "recordings" && !day && !paused && !unknownPaused ? {
        version: 1, revision: 1, generated_at: "2026-11-10T07:00:00Z", title: "Design review", when: "",
        markdown: "## Saved minutes\n\nShip the smaller bundle.",
      } : undefined,
      recap: day ? {
        version: 1, revision: 1, generated_at: "2026-11-10T21:00:00Z", day: "2026-11-10", covers: { from: 0, to: 4 }, final: true,
        summary: "A quiet morning.", markdown: "## Day recap\n\nA quiet morning.",
      } : undefined,
    },
  };
};
const chunk = vi.fn();
const catalog = vi.fn();
function counted(over: Partial<ModuleDescriptor> = {}): ModuleDescriptor {
  const locales = Object.fromEntries(Object.entries(recording.locales!).map(([lang, load]) => [lang, () => (catalog(lang), load())]));
  return { ...recording, locales, load: () => (chunk(), recording.load()), ...over };
}

let host: HTMLDivElement;
let root: Root;
let urls: string[];
let photosMissing = false;
const flush = async () => {
  for (let i = 0; i < 12; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const text = () => host.textContent ?? "";
const open = async (kind: string, entry: string) => {
  act(() => root.render(<EntryDetail kind={kind} id={entry} reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
  await flush();
};

beforeEach(() => {
  connectModuleHub(getState, subscribe);
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
  resetKBDetailPartsForTest();
  lists.clear();
  searches.clear();
  chunk.mockClear();
  catalog.mockClear();
  setToken("token");
  setRosterDefault("assistant");
  urls = [];
  photosMissing = false;
  URL.createObjectURL = vi.fn(() => "blob:stored-photo");
  URL.revokeObjectURL = vi.fn();
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    urls.push(url.replace(/^.*\/api\/v1/, ""));
    if (url.endsWith(`/recordings/${id}/photos`)) {
      if (photosMissing) return Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404, headers: { "Content-Type": "application/json" } }));
      return Promise.resolve(new Response(JSON.stringify({ photos: [{ n: 1, id: "photo-one", at: 2, mime: "image/png", width: 20, height: 10, caption: "A stored whiteboard caption.", cited: false, forwarded: false, added_at: "2026-11-10T06:00:02Z" }] }), { headers: { "Content-Type": "application/json" } }));
    }
    if (url.endsWith(`/recordings/${id}/photos/1`)) {
      return Promise.resolve(new Response(new Blob(["stored image"], { type: "image/png" }), { headers: { "Content-Type": "image/png" } }));
    }
    const m = /\/kb\/([a-z]+)\/([^/?]+)$/.exec(url);
    const body = m ? detail(m[1]!, decodeURIComponent(m[2]!)) : { items: [], tags: [] };
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetModulesForTest();
  resetKBDetailPartsForTest();
  localStorage.clear();
  forgetModuleSwitches();
  vi.unstubAllGlobals();
});

describe("the recording module", () => {
  it("is on by default: a recording's page draws its facts and transcript, its chunk loaded when a recording opens", async () => {
    registerModule(counted());
    expect(moduleKBKindAt("recordings")).toMatchObject({ id: "recording" });
    expect(parse(`/kb/recordings/${id}`)).toEqual({ name: "kb", kind: "recordings", id });
    // another entry's page needs none of it
    await open("projects", "persona");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
    await open("recordings", id);
    // its code takes a moment to come in the first time
    await vi.waitFor(async () => {
      await flush();
      expect(text()).toContain("转写");
    }, { timeout: 10_000 });
    expect(chunk).toHaveBeenCalledTimes(1);
    // its strings came before its code
    expect(catalog).toHaveBeenCalledTimes(1);
    expect(text()).toContain("Keep the export cap.");
    expect(text()).toContain("设备");
    expect(text()).toContain("改名");
    expect(text()).toContain("重新生成");
  });

  it("reads the recordings into the Meetings list while it is on", async () => {
    registerModule(counted());
    await act(async () => root.render(<ListPane kind="meetings" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />));
    await flush();
    expect(urls).toEqual(expect.arrayContaining(["/kb/meetings", "/kb/recordings"]));
  });

  it("switched off, keeps existing recordings in Meetings and search without loading its chunk or strings", async () => {
    setModuleSwitchedOff("recording", true);
    registerModule(counted());
    expect(moduleKBKindAt("recordings")).toBe("absent");
    expect(parse(`/kb/recordings/${id}`)).toEqual({ name: "kb", kind: "recordings", id });
    await act(async () => root.render(<ListPane kind="meetings" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />));
    await flush();
    expect(urls).toEqual(expect.arrayContaining(["/kb/meetings", "/kb/recordings"]));

    searches.set(searchKey("saved"), [{ entry: { kind: "recordings", id, name: "Design review", relevance: 0 }, count: 1 }]);
    const input = host.querySelector<HTMLInputElement>("input")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "saved");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(text()).toContain("Design review");
    expect(await loadKBDetailParts("recordings")).toBeNull();
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("switched off, renders stored minutes, transcript and photos read-only", async () => {
    setModuleSwitchedOff("recording", true);
    registerModule(counted());
    await open("recordings", id);
    expect(text()).toContain("Ship the smaller bundle.");
    expect(text()).toContain("Keep the export cap.");
    expect(text()).toContain("A stored whiteboard caption.");
    const image = host.querySelector<HTMLImageElement>('img[alt="A stored whiteboard caption."]');
    expect(image?.src).toBe("blob:stored-photo");
    const buttons = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(buttons).not.toContain("改名");
    expect(buttons).not.toContain("重新生成");
    expect(buttons).not.toContain("交给 Agent");
    expect(buttons).not.toContain("上传");
    expect(host.querySelector("[role=status]")?.textContent ?? "").not.toContain("找不到");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("switched off, hides only the photos when their list is unavailable", async () => {
    setModuleSwitchedOff("recording", true);
    registerModule(counted());
    photosMissing = true;
    await open("recordings", id);
    expect(text()).toContain("Ship the smaller bundle.");
    expect(text()).toContain("Keep the export cap.");
    expect(text()).not.toContain("A stored whiteboard caption.");
    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector("[role=status]")?.textContent ?? "").not.toContain("找不到");
  });

  it("switched off, renders a stored day recap and its transcript without write controls", async () => {
    setModuleSwitchedOff("recording", true);
    registerModule(counted());
    await open("recordings", dayID);
    expect(text()).toContain("Day recap");
    expect(text()).toContain("A quiet morning.");
    expect(text()).toContain("Morning transcript.");
    const buttons = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(buttons).not.toContain("立即生成");
    expect(buttons).not.toContain("重新生成");
    expect(buttons).not.toContain("更新");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("switched off, shows the calm paused strip for paused='module_off' and ignores unknown paused values", async () => {
    setModuleSwitchedOff("recording", true);
    registerModule(counted());
    await open("recordings", pausedID);
    const strip = host.querySelector('[role="note"][data-recording-paused="module_off"]');
    expect(strip).not.toBeNull();
    expect(text()).toContain("Its minutes were never written.");
    const buttons = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(buttons).not.toContain("改名");
    expect(buttons).not.toContain("重新生成");
    expect(buttons).not.toContain("重试");
    expect(buttons).not.toContain("Retry");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();

    await open("recordings", unknownPausedID);
    expect(host.querySelector("[data-recording-paused]")).toBeNull();
    expect(text()).toContain("Keep the export cap.");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("is off on a hub that publishes module flags without module.recording, and on with it", () => {
    registerModule(counted());
    Object.assign(getState(), { features: ["modules"], loaded: true });
    expect(moduleKBKindAt("recordings")).toBe("absent");
    expect(parse(`/kb/recordings/${id}`)).toEqual({ name: "kb", kind: "recordings", id });
    Object.assign(getState(), { features: ["modules", "module.recording"], loaded: true });
    expect(moduleKBKindAt("recordings")).toMatchObject({ id: "recording" });
    // a hub that publishes no module flags (an older one) keeps it on
    Object.assign(getState(), { features: ["managers"], loaded: true });
    expect(moduleKBKindAt("recordings")).toMatchObject({ id: "recording" });
  });

  it("refuses a second module that claims the same knowledge kind", () => {
    registerModule(recording);
    expect(() => registerModule({ ...recording, id: "recording-two" })).toThrow(/knowledge kind "recordings"/);
  });
});
