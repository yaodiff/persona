// @vitest-environment jsdom
// ⌘K is the Agents list's search too (#6589): the list's operators narrow it to Agent sessions, plain words never filter
// (#4397), a match on the name comes first, then the project or computer, then what is inside a session (that line as
// the row's second line, the match marked); "Filter Agents list" (⇧↵) puts what was typed on the list in place, and the
// Filter beside the field writes the list's filter choices into the query as its operators.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine, Session } from "../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  // jsdom lays nothing out: the palette keeps its active row in view
  (g.Element as { prototype: { scrollIntoView?: () => void } }).prototype.scrollIntoView = () => {};
});
const { getState, reset } = await import("../lib/store");
const { closePalette, getPalette, openPalette, setPaletteQuery } = await import("../lib/palette");
const { t } = await import("../i18n");
const { getAgentListFilters, resetAgentListFilters } = await import("../lib/agent-list-filters");
const { Palette } = await import("./Palette");
// the palette imports lib/agent-search.ts with the first query typed, and the Filter's menu with its first click: loaded
// here first, so those imports are answered from the module cache however busy the test run is
await import("../lib/agent-search");
await import("./palette/AgentConditionsMenu");

const machines: Machine[] = [
  { id: "mac", name: "Mac Studio", online: true },
  { id: "box", name: "Build Box", online: true },
];
const agent = (sid: string, p: Partial<Session>): Session =>
  ({ machine_id: "mac", session_id: sid, title: sid, cwd: "/work/misc", status: "idle", state: "idle", updated_at: "2026-11-01T09:00:00Z", ...p }) as Session;
const sessions: Session[] = [
  // found by what is inside it only
  agent("inside", { title: "Write the docs", last_text: "The parser crashed on an empty file", updated_at: "2026-11-03T09:00:00Z" }),
  // its project is the word
  agent("project", { title: "Fix login", cwd: "/work/parser", repo_root: "/work/parser", machine_id: "box", updated_at: "2026-11-02T09:00:00Z" }),
  // its name starts with the word
  agent("name", { title: "parser rewrite", cwd: "/work/tools" }),
  // a word the old hidden filters took (#4397)
  agent("pinned-words", { title: "write pinned notes doc", machine_id: "box" }),
];

let host: HTMLDivElement;
let root: Root;
let closed = 0;
beforeEach(() => {
  reset();
  resetAgentListFilters();
  localStorage.clear();
  closed = 0;
  // reset() keeps the hub's features: the paged case's are its own
  Object.assign(getState(), { loaded: true, features: [], machines, sessions: { mac: sessions.filter((s) => s.machine_id === "mac"), box: sessions.filter((s) => s.machine_id === "box") } });
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  closePalette();
  reset();
  resetAgentListFilters();
});

async function search(query: string, listShown = true) {
  openPalette("jump");
  setPaletteQuery(query);
  act(() => root.render(<Palette open onClose={() => closed++} onNewAgent={() => {}} listShown={listShown} />));
  await ticks();
  return document.body;
}
/** lib/agent-search.ts loads with the first query typed, or the Filter's first click. */
async function ticks() {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 10; i++) await act(async () => { await new Promise((ok) => setTimeout(ok, 0)); });
}
/** The Agent rows' titles, top to bottom. */
const titles = () =>
  [...document.body.querySelectorAll<HTMLElement>('[role="option"]')].map((o) => o.querySelector(".truncate")?.textContent ?? "");

describe("⌘K as the Agents list's search", () => {
  it("puts a match on the name first, then the project or computer, then what is inside, that line marked", async () => {
    await search("parser");
    // newest first would be the other way round: the match decides
    expect(titles()).toEqual(["parser rewrite", "Fix login", "Write the docs"]);
    const line = document.body.querySelector("[data-evidence]");
    expect(line?.textContent).toContain("The parser crashed");
    expect(line?.querySelector("mark")?.textContent).toBe("parser");
  });

  it("matches a computer's name as a place, the newest first among equals, no line marked", async () => {
    await search("studio");
    expect(titles()).toEqual(["Write the docs", "parser rewrite"]);
    expect(document.body.querySelector("[data-evidence]")).toBeNull();
    // every word is found in the one session: "box" is one session's computer, "crashed" is inside another
    await search("box crashed");
    expect(titles()).toEqual([]);
    await search("build");
    expect(titles()).toEqual(["Fix login", "write pinned notes doc"]);
  });

  it("narrows with the list's operators to Agent sessions alone", async () => {
    await search("machine:box");
    expect(titles()).toEqual(["Fix login", "write pinned notes doc"]);
    // no page ("Go to"), Inbox or terminal rows for a query about Agents
    expect(document.body.textContent).not.toContain("Go to");
    await search("project:parser");
    expect(titles()).toEqual(["Fix login"]);
    await search("machine:mac parser");
    expect(titles()).toEqual(["parser rewrite", "Write the docs"]);
  });

  it("never turns plain words into filters (#4397)", async () => {
    await search("pinned notes");
    expect(titles()).toEqual(["write pinned notes doc"]);
  });

  it("Filter Agents list puts the query on the list, and opens the Agents page where the sidebar is not shown", async () => {
    await search("machine:box");
    const row = document.body.querySelector<HTMLButtonElement>("[data-testid=palette-filter-list]")!;
    expect(row.textContent).toContain("machine:box");
    await act(async () => row.click());
    expect(getAgentListFilters().query).toBe("machine:box");
    expect(closed).toBe(1);
    expect(location.pathname).toBe("/");

    resetAgentListFilters();
    await search("parser", false);
    const input = document.body.querySelector<HTMLInputElement>('input[role="combobox"]')!;
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
    });
    expect(getAgentListFilters().query).toBe("parser");
    expect(location.pathname).toBe("/agents");
  });

  it("offers no Filter Agents list without an Agent found, nor operators to the hub's paged list, which matches text", async () => {
    await search("nothing-like-this");
    expect(document.body.querySelector("[data-testid=palette-filter-list]")).toBeNull();
    act(() => root.unmount());
    root = createRoot(host);
    // the default By to-do is the paged list's on a hub that lists sessions by to-do (#6662)
    Object.assign(getState(), { features: ["sessions_list", "sessions_todo"], sessionSummaries: sessions });
    await search("parser");
    expect(titles()).toContain("parser rewrite");
    const row = document.body.querySelector<HTMLButtonElement>("[data-testid=palette-filter-list]")!;
    await act(async () => row.click());
    expect(getAgentListFilters().query).toBe("parser");
    await search("machine:box");
    expect(titles()).toEqual(["Fix login", "write pinned notes doc"]);
    expect(document.body.querySelector("[data-testid=palette-filter-list]")).toBeNull();
  });
});

const menuRow = (label: string) =>
  [...document.body.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemradio"], [role="menuitemcheckbox"]')].find((el) => el.textContent?.includes(label));
async function choose(label: string) {
  const row = menuRow(label);
  expect(row, `menu row ${label}`).toBeTruthy();
  await act(async () => row!.click());
}
const filterButton = () => document.body.querySelector<HTMLButtonElement>("[data-testid=palette-agent-filter]")!;

describe("the Filter beside ⌘K's field", () => {
  it("writes a choice into the query as the list's operator, counts it, and takes it out again", async () => {
    await search("");
    expect(filterButton().textContent).toBe("");
    await act(async () => filterButton().click());
    await ticks();
    await choose(t("lib-agent-filter.filter_machine"));
    await choose("Build Box");
    expect(getPalette().query).toBe("machine:box ");
    expect(titles()).toEqual(["Fix login", "write pinned notes doc"]);
    expect(filterButton().textContent).toBe("1");
    // the menu stays open on its page: a second computer is either of them, choosing one again takes it out
    await choose("Mac Studio");
    expect(getPalette().query).toBe("machine:box machine:mac ");
    expect(filterButton().textContent).toBe("2");
    await choose("Build Box");
    expect(getPalette().query).toBe("machine:mac ");
  });

  it("toggles the parser's pin alias off in the Filter menu", async () => {
    await search("is:pin");
    await act(async () => filterButton().click());
    await ticks();
    expect(menuRow(t("lib-agent-filter.filter_pinned"))?.querySelector("svg")?.getAttribute("class")).not.toContain("invisible");
    await choose(t("lib-agent-filter.filter_pinned"));
    expect(getPalette().query.trim()).toBe("");
    expect(titles()).not.toEqual([]);
  });

  it("keeps the words typed, Pinned and the time window as operators, and Reset leaves the words alone", async () => {
    await search("parser");
    await act(async () => filterButton().click());
    await ticks();
    await choose(t("lib-agent-filter.filter_pinned"));
    expect(getPalette().query).toBe("parser is:pinned ");
    await choose(t("palette.cond_since"));
    await choose(t("palette.since_7d"));
    expect(getPalette().query).toBe("parser is:pinned since:7d ");
    // one window at a time
    await choose(t("palette.since_24h"));
    expect(getPalette().query).toBe("parser is:pinned since:24h ");
    await choose(t("sidebar.back"));
    await choose(t("lib-agent-filter.reset_filters"));
    expect(getPalette().query).toBe("parser ");
  });
});
