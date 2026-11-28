// ⌘K's "Recently opened" and "Go back to the previous conversation" (#2191): the conversations opened here come first,
// newest first, without the one on the page; the first ("Previous") is picked already, so ⌘K ⏎ goes back to it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { closePalette, openPalette, setPaletteQuery } from "../lib/palette";
import { noteChatShown, resetRecentChatsForTests } from "../lib/recent-chats";
import { getState } from "../lib/store";
import type { Session } from "../lib/types";
import { Palette } from "./Palette";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  Object.defineProperty(globalThis, "navigator", { value: { platform: "Linux x86_64" }, configurable: true, writable: true });
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k), clear: () => kept.clear() };
});

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const at = (minutes: number) => new Date(Date.UTC(2026, 10, 10, 9, 0) - minutes * 60_000).toISOString();
const agent = (sid: string, title: string, minutes: number) =>
  ({ machine_id: "m1", session_id: sid, title, cwd: `/work/${sid}`, state: "idle", updated_at: at(minutes) }) as unknown as Session;

function render(query: string) {
  vi.stubGlobal("document", { body: {} });
  openPalette(query.startsWith(">") ? "command" : "jump");
  setPaletteQuery(query);
  return renderToStaticMarkup(<Palette open onClose={() => {}} onNewAgent={() => {}} />);
}

/** The rows' titles in the order the list draws them, with the group headings among them. */
const outline = (html: string) => [...html.matchAll(/uppercase">([^<]+)<\/div>|font-medium text-ink">([^<]+)<\/span>/g)].map((m) => m[1] ? `# ${m[1]}` : m[2]);

beforeEach(() => {
  localStorage.clear();
  resetRecentChatsForTests();
  // by activity: c is the newest, then b, then a, then d
  Object.assign(getState(), {
    sessions: { m1: [agent("a", "Release notes", 30), agent("b", "Home screen widget", 10), agent("c", "Slow orders endpoint", 1), agent("d", "Blog to Astro", 60)] },
    assistant: { machine_id: "m1", main: "p1", conversations: [{ machine_id: "m1", session_id: "p1", title: "Daily list", assistant: true, updated_at: at(5) }] },
  });
});
afterEach(() => {
  closePalette();
  vi.unstubAllGlobals();
});

describe("⌘K: recently opened (#2191)", () => {
  it("lists the conversations opened here first, newest opened first, without the one on the page; the rest follow under their heading", () => {
    noteChatShown("s:m1/a");
    noteChatShown("j:p1");
    noteChatShown("s:m1/b"); // on the page now
    const html = render("");
    // (the pages, "Go to", follow the conversations as before)
    expect(outline(html).slice(0, outline(html).indexOf("# 前往"))).toEqual(["# 最近打开", "主对话", "Release notes", "# 会话", "Slow orders endpoint", "Home screen widget", "Blog to Astro"]);
    // the first is the previous one, picked already: ⌘K then Return goes back to it
    expect(html).toMatch(/aria-selected="true"[^>]*>(?:(?!aria-selected).)*主对话<\/span>(?:(?!aria-selected).)*上一个/s);
  });

  it("skips one that is gone, and has no group when nothing else was opened", () => {
    noteChatShown("s:m1/gone");
    noteChatShown("s:m1/d");
    expect(outline(render(""))).not.toContain("# 最近打开");
    noteChatShown("s:m1/a");
    expect(outline(render("")).slice(0, 3)).toEqual(["# 最近打开", "Blog to Astro", "# 会话"]);
  });

  it("is only for the empty search: typing searches everything as before", () => {
    noteChatShown("s:m1/a");
    noteChatShown("s:m1/b");
    const html = render("release");
    expect(outline(html)).toEqual(["Release notes"]);
    expect(html).not.toContain("上一个");
  });

  it("offers Go back to the previous conversation among the commands, naming it; none with nothing to go back to", () => {
    noteChatShown("s:m1/b");
    expect(render(">")).not.toContain("返回上一个对话");
    noteChatShown("s:m1/a");
    noteChatShown("s:m1/b");
    const html = render(">返回");
    expect(html).toContain("返回上一个对话");
    expect(html).toMatch(/返回上一个对话<span[^>]*>Release notes<\/span>/);
  });
});
