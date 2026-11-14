// @vitest-environment jsdom
// ⌘K finds a Persona chat by what was said in it (#6283): the first query typed loads lib/assistant-search.ts, and the
// manager's results merge into the local matches; a chat found by a message shows that sentence with the match marked
// as its second line, and opens at it. Without the hub's feature nothing is asked.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  // jsdom lays nothing out: the palette keeps its active row in view
  (g.Element as { prototype: { scrollIntoView?: () => void } }).prototype.scrollIntoView = () => {};
});
const fake = vi.hoisted(() => ({ answer: null as unknown, asked: [] as string[], features: ["assistant_search"] }));
const conv = (sid: string, p: Partial<Session> = {}): Session => ({
  machine_id: "server", session_id: sid, title: `对话 ${sid}`, cwd: "/home/me/.persona/assistant", status: "idle", assistant: true, updated_at: "2026-11-01T09:00:00Z", ...p,
} as Session);
vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  // "ctx": a chat started from picked context, titled by its <persona-context> tag (docs/protocol/protocol.md "Tags in messages")
  const assistant = { machine_id: "server", main: "main", conversations: [conv("main"), conv("trip"), conv("keep"), conv("ctx", { title: '<persona-context kind="context" title="Lark · Release &quot;Fri&quot;" url="">' })] };
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), assistant, features: fake.features } as unknown as ReturnType<typeof real.getState>) };
});
vi.mock("../lib/assistant-search", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../lib/assistant-search")>()),
  searchAssistantChats: async (q: string) => {
    fake.asked.push(q);
    return fake.answer;
  },
}));
const { closePalette, openPalette, setPaletteQuery } = await import("../lib/palette");
const { Palette } = await import("./Palette");
await import("../lib/assistant-search");
await import("../lib/agent-search");

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  fake.asked = [];
  fake.features = ["assistant_search"];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  closePalette();
  vi.useRealTimers();
});
async function search(query: string) {
  openPalette("jump");
  setPaletteQuery(query);
  act(() => root.render(<Palette open onClose={() => {}} onNewAgent={() => {}} />));
  await act(async () => { await vi.dynamicImportSettled(); });
  await act(async () => { vi.advanceTimersByTime(210); });
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
  if (fake.features.includes("assistant_search")) expect(fake.asked).toContain(query);
  return document.body;
}

describe("⌘K with the manager's chat search", () => {
  it("lists a chat found by a message with that sentence, the match marked", async () => {
    fake.answer = {
      results: [{ session_id: "trip", machine_id: "server", title: "对话 trip", hits: [{ item_id: "i1#0", seq: 1, role: "user", snippet: "我想去青石巷 BlueLane 看看", ranges: [[7, 15]] }] }],
      partial: false,
    };
    const body = await search("BlueLane");
    expect(fake.asked).toEqual(["BlueLane"]);
    const said = body.querySelector("[data-said]");
    expect(said?.textContent).toBe("我想去青石巷 BlueLane 看看");
    expect(said?.querySelector("mark")?.textContent).toBe("BlueLane");
    expect(body.textContent).toContain("对话 trip");
    expect(body.textContent).not.toContain("对话 keep");
  });

  it("an archived chat the page does not list comes from the result itself", async () => {
    fake.answer = { results: [{ session_id: "old", machine_id: "server", title: "Old trip", archived: true, hits: [{ item_id: "i3#0", seq: 3, role: "assistant", snippet: "旧的 BlueLane", ranges: [[3, 11]] }] }], partial: false };
    expect((await search("BlueLane")).textContent).toContain("Old trip");
  });

  it("calls a chat titled by its <persona-context> tag by the tag's title, as the sidebar does, never by the raw tag", async () => {
    fake.answer = {
      results: [{ session_id: "old-ctx", machine_id: "server", title: '<persona-context kind="context" title="Mail · Invoice &amp; receipt" url="">', archived: true, hits: [{ item_id: "i4#0", seq: 4, role: "user", snippet: "Release the invoice", ranges: [[0, 7]] }] }],
      partial: false,
    };
    const body = await search("Release");
    expect(body.textContent).toContain('Lark · Release "Fri"');
    expect(body.textContent).toContain("Mail · Invoice & receipt");
    expect(body.textContent).not.toContain("persona-context");
  });

  it("does not ask without the hub's feature", async () => {
    fake.features = [];
    fake.answer = { results: [{ session_id: "trip", machine_id: "server", title: "对话 trip", hits: [] }], partial: false };
    const body = await search("BlueLane");
    expect(fake.asked).toEqual([]);
    expect(body.querySelector("[data-said]")).toBeNull();
  });
});
