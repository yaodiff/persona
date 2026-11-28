// @vitest-environment jsdom
// A Persona conversation's mark in the sidebar, under Pinned (常驻) and Recent (最近): the working arc while it works
// (#5862), and the unread mark while it holds a reply nobody has opened, which opening the chat takes away at once and
// which the folded headings count (#13425).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

import { t } from "../i18n";
import { resetDirectionNames } from "../lib/direction-names";
import { refresh, reset, upsertSession, viewSession } from "../lib/store";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// the page is in front, so an opened chat is seen at once (lib/store.ts viewSession)
Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });

const conversation: Session = {
  machine_id: "hub", session_id: "review", title: "/agent · Review release notes", cwd: "/code",
  status: "idle", state: "idle", assistant: true, origin: "user", updated_at: "2026-05-01T10:00:00Z",
};
const main: Session = { ...conversation, session_id: "main", title: "Persona" };
let listed: Session;
/** More of the user's chats the hub lists: Recent's, as they are not leaders. */
let others: Session[];
let leaders: string[];
/** The leaders the direction tree says something runs under (#6626); undefined: the hub has no tree. */
let running: string[] | undefined;
/** The chats a test opened (viewSession), closed after it: the registry of chats on screen lives as long as the module. */
const views: Array<() => void> = [];
let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const row = () => host.querySelector<HTMLAnchorElement>('a[href="/persona/review"]')!;
const mark = () => row().querySelector('[data-state="working"]');

beforeEach(() => {
  reset();
  localStorage.clear();
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-05-01T10:02:00Z"));
  listed = { ...conversation };
  others = [];
  leaders = ["review"];
  running = undefined;
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://example.com").pathname;
    const chats = [main, listed, ...others];
    const data = path === "/api/v1/assistant" ? { machine_id: "hub", online: true, main: "main", leaders, conversations: chats }
      : path === "/api/v1/sessions" ? { sessions: chats }
      : path === "/api/v1/machines" ? { machines: [{ id: "hub", name: "Hub", online: true }] }
      : path === "/api/v1/assistant/directions/tree" && running ? { directions: [
        ...leaders.map((id) => ({ id, name: "", counts: { working: 0, needs_you: 0, stuck: 0, review: 0 }, running: running!.includes(id), tasks: [] })),
        { id: "main", name: "", secretary: true, counts: { working: 0, needs_you: 0, stuck: 0, review: 0 }, tasks: [] },
      ] }
      : {};
    return Promise.resolve(new Response(JSON.stringify(data), { headers: { "Content-Type": "application/json" } }));
  }));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  views.splice(0).forEach((release) => release());
  act(() => root.unmount());
  host.remove();
  reset();
  resetDirectionNames();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function show(active = false) {
  await act(() => refresh());
  act(() => root.render(<Sidebar route={active ? { name: "persona", sid: "review" } : { name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
}

describe("a working assistant conversation's sidebar mark", () => {
  it.each([
    { name: "a recent conversation", pinned: false, active: false, legacy: false },
    { name: "an open pinned /agent conversation", pinned: true, active: true, legacy: false },
    { name: "a conversation from a legacy hub", pinned: false, active: true, legacy: true },
  ])("updates $name without losing its timestamp or idle icon", async ({ pinned, active, legacy }) => {
    if (pinned) {
      listed.origin = "lark_agent";
      localStorage.setItem("assistant.pinned", '["review"]');
    }
    if (legacy) delete listed.state;
    await show(active);
    const idleIcon = row().firstElementChild!.innerHTML;
    const text = row().textContent;
    const classes = row().className;
    expect(mark()).toBeNull();

    const working: Session = { ...listed, status: "working", ...(legacy ? {} : { state: "working" }) };
    act(() => upsertSession(working));
    await flush();
    expect(mark()).not.toBeNull();
    expect(mark()!.getAttribute("data-shape")).toBe("arc");
    expect(mark()!.getAttribute("style")).toContain("var(--status-working)");
    expect(mark()!.getAttribute("aria-label")).toBe("工作中");
    expect(row().textContent).toBe(text);
    expect(row().className).toBe(classes);
    expect(row().querySelector("[data-agent-mark]")).toBeNull();
    expect(row().querySelector("[data-row-line]")).toBeNull();

    act(() => upsertSession({ ...listed }));
    await flush();
    expect(mark()).toBeNull();
    expect(row().firstElementChild!.innerHTML).toBe(idleIcon);
    expect(row().textContent).toBe(text);
  });

  it("shows work already in progress on the first load", async () => {
    listed = { ...listed, status: "working", state: "working" };
    await show();
    expect(mark()).not.toBeNull();
  });

  it("does not mistake a stale legacy status for the authoritative idle state", async () => {
    listed = { ...listed, status: "working", state: "idle" };
    await show();
    expect(mark()).toBeNull();
  });
});

// "review" is the pinned lead (常驻); "trip" and "notes" are under Recent (最近).
describe("an unread Persona chat's sidebar mark (#13425)", () => {
  const chat = (session_id: string, title: string): Session => ({ ...conversation, session_id, title });
  const unread = { state: "unread", status: "done" } as const;
  const justFinished = { state: "idle", status: "idle", work: { phase: "done", just_finished: true } } as const;
  const usageLimit = { ...unread, turn_error: "You've reached your usage limit.", turn_error_kind: "usage_limit" } as const;
  const rowOf = (sid: string) => host.querySelector<HTMLAnchorElement>(`a[href="/persona/${sid}"]`)!;
  /** The 14 px slot at the row's start: the chat icon at rest, else the conversation's mark. */
  const slotOf = (sid: string) => rowOf(sid).firstElementChild as HTMLElement;
  const markOf = (sid: string) => slotOf(sid).querySelector<HTMLElement>("[data-state]");
  const titleOf = (sid: string) => rowOf(sid).querySelector<HTMLElement>("span.text-meta")!;
  const emphasized = (el: HTMLElement) => el.classList.contains("font-medium") && el.classList.contains("text-ink");
  const current = (sid: string) => [listed, ...others].find((c) => c.session_id === sid)!;
  const update = async (sid: string, patch: Partial<Session>) => {
    act(() => upsertSession({ ...current(sid), ...patch }));
    await flush();
  };
  /** The chat opens on this page (its view mounts): returns its closing. */
  const open = (sid: string) => {
    let close = () => {};
    act(() => { close = viewSession("hub", sid); });
    views.push(close);
    return close;
  };
  /** Folded away: not drawn, or still drawn inert while its group closes. */
  const hidden = (sid: string) => !rowOf(sid) || !!rowOf(sid).closest("[inert]");
  /** The Persona chats section's own heading, where its folded tally sits beside the New chat icon. */
  const personaHeading = () => [...host.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) => b.textContent === t("sidebar.persona_chats"))!.parentElement!;
  /** Recent's subheading (SubHeading), its tally before the help. */
  const recentHeading = () => [...host.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) => b.textContent === t("sidebar.recent_chats"))?.closest('[role="heading"]')?.parentElement;
  const tallyIn = (el: Element | null | undefined) => el?.querySelector<HTMLElement>('[data-testid="state-tally"]') ?? null;
  /** The number a heading's tally shows beside its unread mark; null with no tally. */
  const tallied = (el: Element | null | undefined) => tallyIn(el)?.querySelector(':scope > [data-state="unread"]')?.textContent ?? null;

  beforeEach(() => {
    others = [chat("trip", "Weekend trip"), chat("notes", "Reading notes")];
  });

  it.each([
    { name: "pinned", sid: "review", other: "trip" },
    { name: "recent", sid: "trip", other: "notes" },
  ])("marks a $name chat in its chat icon's place, with the unread title weight, and moves nothing", async ({ sid, other }) => {
    await show();
    const link = rowOf(sid);
    const slot = slotOf(sid);
    const title = titleOf(sid);
    const { className, textContent } = link;
    const slotClass = slot.className;
    const icon = slot.innerHTML;
    expect(markOf(sid)).toBeNull();
    expect(emphasized(title)).toBe(false);

    await update(sid, unread);
    const glow = markOf(sid)!;
    expect(glow.getAttribute("data-state")).toBe("unread");
    expect(glow.getAttribute("data-shape")).toBe("glow");
    expect(glow.getAttribute("aria-label")).toBe("完成未看");
    expect(emphasized(titleOf(sid))).toBe(true);
    // the same row, slot and title, with the same words: its height, the title's place and the time at its end stay
    expect(rowOf(sid)).toBe(link);
    expect(slotOf(sid)).toBe(slot);
    expect(slot.className).toBe(slotClass);
    expect(titleOf(sid)).toBe(title);
    expect(link.className).toBe(className);
    expect(link.textContent).toBe(textContent);
    // a read chat beside it keeps its chat icon and plain title
    expect(markOf(other)).toBeNull();
    expect(emphasized(titleOf(other))).toBe(false);

    await update(sid, { state: "idle", status: "idle" });
    expect(markOf(sid)).toBeNull();
    expect(slot.innerHTML).toBe(icon);
    expect(emphasized(titleOf(sid))).toBe(false);
  });

  it("marks a chat finished before anybody looked with Just finished's plain dot, its title as it was", async () => {
    others = [{ ...chat("trip", "Weekend trip"), ...justFinished }, chat("notes", "Reading notes")];
    await show();
    const dot = markOf("trip")!;
    expect(dot.getAttribute("data-state")).toBe("just_finished");
    expect(dot.getAttribute("data-shape")).toBe("dot");
    expect(dot.getAttribute("aria-label")).toBe("刚做完待看");
    expect(emphasized(titleOf("trip"))).toBe(false);
  });

  it("does not draw a turn a usage limit stopped as unread: it goes on by itself", async () => {
    others = [{ ...chat("trip", "Weekend trip"), ...usageLimit }, chat("notes", "Reading notes")];
    await show();
    expect(markOf("trip")).toBeNull();
    expect(slotOf("trip").innerHTML).toBe(slotOf("notes").innerHTML);
    expect(emphasized(titleOf("trip"))).toBe(false);
  });

  it("keeps the chat icon and pending count of a chat that needs you, and the arc of one at work", async () => {
    others = [
      { ...chat("trip", "Weekend trip"), state: "needs_you", status: "blocked", pending_interactions: 1 },
      { ...chat("notes", "Reading notes"), state: "working", status: "working" },
    ];
    await show();
    expect(markOf("trip")).toBeNull();
    expect(rowOf("trip").querySelector(".text-attention")?.textContent).toBe("1");
    expect(markOf("notes")!.getAttribute("data-state")).toBe("working");
    expect(markOf("notes")!.getAttribute("data-shape")).toBe("arc");
    // a reply after the work: the arc gives way to the unread mark
    await update("notes", unread);
    expect(markOf("notes")!.getAttribute("data-state")).toBe("unread");
  });

  it.each([
    { name: "idle", patch: {}, state: "working" },
    { name: "unread", patch: unread, state: "unread" },
    { name: "just finished", patch: justFinished, state: "working" },
    { name: "stopped on a usage limit", patch: usageLimit, state: "working" },
  ])("on a pinned lead with Agents running under it, its own $name state draws $state", async ({ patch, state }) => {
    running = ["review"];
    listed = { ...listed, ...patch };
    await show();
    // as iOS's RowStateMark: its own Unread wins over the arc for what runs under it; Just finished, ranked after
    // Working, and a turn a usage limit stopped (drawn seen) give way to it
    expect(markOf("review")!.getAttribute("data-state")).toBe(state);
    expect(markOf("review")!.getAttribute("data-shape")).toBe(state === "unread" ? "glow" : "arc");
  });

  it("clears the mark the moment the chat is opened, keeps it cleared while a list still says Unread, and brings it back with a later reply", async () => {
    others = [{ ...chat("trip", "Weekend trip"), ...unread }, chat("notes", "Reading notes")];
    await show();
    expect(markOf("trip")!.getAttribute("data-state")).toBe("unread");

    // opening the chat marks it seen here at once and tells the hub (no socket here: over HTTP)
    const close = open("trip");
    expect(markOf("trip")).toBeNull();
    expect(emphasized(titleOf("trip"))).toBe(false);
    expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url).endsWith("/sessions/hub/trip/seen") && init?.method === "POST")).toBe(true);

    // lists read before the hub recorded it still say Unread: the seen outbox keeps the row seen (lib/seen-outbox.ts)
    await update("trip", {});
    expect(markOf("trip")).toBeNull();
    await act(() => refresh());
    await flush();
    expect(markOf("trip")).toBeNull();
    expect(emphasized(titleOf("trip"))).toBe(false);

    // closed, a later reply is a new turn nobody has opened (one that comes while the chat is on screen is seen there)
    close();
    others = [{ ...current("trip"), updated_at: "2026-05-01T10:05:00Z" }, chat("notes", "Reading notes")];
    await update("trip", {});
    expect(markOf("trip")!.getAttribute("data-state")).toBe("unread");
    expect(emphasized(titleOf("trip"))).toBe(true);
  });

  it("counts, folded, the unread chats the Persona chats heading hides, Pinned and Recent together, as chats", async () => {
    localStorage.setItem("fold:persona", "1");
    listed = { ...listed, ...unread };
    others = [
      { ...chat("trip", "Weekend trip"), ...unread },
      { ...chat("notes", "Reading notes"), ...justFinished },
      { ...chat("limit", "Usage limit"), ...usageLimit },
      chat("read", "Read chat"),
    ];
    await show();
    expect(hidden("review") && hidden("trip")).toBe(true);
    // Just finished is not counted, as in the Agents list's folded tally, nor a turn a usage limit stopped
    const tally = tallyIn(personaHeading())!;
    expect(tallied(personaHeading())).toBe("2");
    // the unread mark beside the number, the words for a screen reader and the tooltip naming chats, not agents
    expect(tally.querySelector(':scope > [data-state="unread"] [data-shape="glow"]')).not.toBeNull();
    expect(tally.querySelector(".sr-only")!.textContent).toBe("已收起的对话：2 个完成未看");
    expect(tally.getAttribute("title")).toBe("已收起的对话：2 个完成未看");

    // opening one takes it off the count at once
    open("trip");
    expect(tallied(personaHeading())).toBe("1");
    open("review");
    expect(tallyIn(personaHeading())).toBeNull();
  });

  it("counts, folded, the unread chats Recent hides on its own heading, and nothing on an open heading", async () => {
    listed = { ...listed, ...unread };
    others = [{ ...chat("trip", "Weekend trip"), ...unread }, { ...chat("notes", "Reading notes"), ...unread }];
    await show();
    // open: the rows say it, the headings count nothing
    expect(tallyIn(personaHeading())).toBeNull();
    expect(tallyIn(recentHeading())).toBeNull();
    expect(markOf("trip")!.getAttribute("data-state")).toBe("unread");

    // Recent folded: its heading counts its own two, not the pinned one (still drawn above it)
    act(() => recentHeading()!.querySelector("button")!.click());
    await flush();
    expect(hidden("trip") && hidden("notes")).toBe(true);
    expect(tallied(recentHeading())).toBe("2");
    expect(tallyIn(personaHeading())).toBeNull();
    expect(markOf("review")!.getAttribute("data-state")).toBe("unread");

    // the count follows the chats it hides: one read elsewhere and one opened here leave it, a new reply joins it
    await update("notes", { state: "idle", status: "idle" });
    expect(tallied(recentHeading())).toBe("1");
    open("trip");
    expect(tallyIn(recentHeading())).toBeNull();
    await update("notes", unread);
    expect(tallied(recentHeading())).toBe("1");

    // open again: the row says it, the heading counts nothing
    act(() => recentHeading()!.querySelector("button")!.click());
    await flush();
    expect(tallyIn(recentHeading())).toBeNull();
    expect(markOf("notes")!.getAttribute("data-state")).toBe("unread");
  });
});
