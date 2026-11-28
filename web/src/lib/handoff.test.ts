// @vitest-environment jsdom
// Leaving a conversation for an Agent it handed work to: the side panel by default (#5424), or, set to Full page, the
// Agent's page, the way back, and the chat's place (#5096).
import { afterEach, describe, expect, it, vi } from "vitest";
import { agentOpenMode, setAgentOpenMode } from "./agent-open";
import { chatPlaceIn, chatPlaceOf, dropChatPlace, fromHere, handoffFrom, leaveChat, openAgent, openedFromHere, openFromPage, openLinkPage, openTask, panelIn, pickMainChat, sidebarPageOf, taskRoute } from "./handoff";
import { openLink } from "./links";
import { closePanel, getPanel, openPanel } from "./panel";
import { navigate } from "./router";
import { getState, patchAssistant, refreshAssistant, reset, type State } from "./store";
import { NARROW } from "./viewport";
import type { Todo } from "./todos";

afterEach(() => {
  setAgentOpenMode("panel");
  closePanel();
  reset();
  history.replaceState(null, "", "/");
  document.body.innerHTML = "";
});

const rect = (top: number, bottom: number) => ({ top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON() {} }) as DOMRect;

/** A chat scroller showing lines `a` (above the view), `b` (cut by the view's top) and `c`. */
function chat(scroll: { top: number; height: number; client: number }) {
  const sc = document.createElement("div");
  sc.setAttribute("data-chat-scroll", "");
  Object.defineProperties(sc, { scrollTop: { value: scroll.top, writable: true }, scrollHeight: { value: scroll.height }, clientHeight: { value: scroll.client } });
  sc.getBoundingClientRect = () => rect(100, 100 + scroll.client);
  for (const [id, top] of [["a", -200], ["b", 60], ["c", 300]] as const) {
    const line = document.createElement("div");
    line.dataset.line = id;
    const row = document.createElement("div");
    row.getBoundingClientRect = () => rect(top, top + 120);
    line.append(row);
    sc.append(line);
  }
  document.body.append(sc);
  return sc;
}

describe("where a chat was", () => {
  it("is its end, or the first line still in view and how far under the top it starts", () => {
    expect(chatPlaceOf(chat({ top: 900, height: 1500, client: 600 }))).toEqual({ end: true, offset: 0 });
    document.body.innerHTML = "";
    expect(chatPlaceOf(chat({ top: 300, height: 1500, client: 600 }))).toEqual({ end: false, line: "b", offset: -40 });
  });

  it("reads only what a hand-off wrote, and is dropped once used", () => {
    history.replaceState({ chatPlace: { end: false, line: "b", offset: -40 }, other: 1 }, "");
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
    dropChatPlace();
    expect(chatPlaceIn()).toBeUndefined();
    expect(history.state).toEqual({ other: 1 });
    expect(chatPlaceIn({ chatPlace: { line: "x" } })).toBeUndefined();
    expect(handoffFrom({ handoff: { path: "https://example.com/" } })).toBeUndefined();
    expect(handoffFrom({ handoff: { path: "/persona/c1" } })).toEqual({ path: "/persona/c1" });
  });
});

describe("leaving a conversation", () => {
  it("keeps the chat's place on its entry and says where the Agent's page came from", () => {
    history.replaceState(null, "", "/persona/c1?machine=m1");
    chat({ top: 300, height: 1500, client: 600 });
    const before = history.length;
    leaveChat({ name: "session", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/s/m1/s1");
    expect(history.length).toBe(before + 1);
    expect(handoffFrom()).toEqual({ path: "/persona/c1?machine=m1" });
    history.back();
  });

  it("keeps the place for Back when it leaves from an overlay's entry, which the new page takes over", () => {
    history.replaceState({ personaOverlay: "o1" }, "", "/persona/c1");
    chat({ top: 300, height: 1500, client: 600 });
    leaveChat({ name: "session", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/s/m1/s1");
    expect(chatPlaceIn()).toBeUndefined();
    // Back lands on the conversation's own entry, under the overlay's
    history.replaceState(null, "", "/persona/c1");
    dispatchEvent(new PopStateEvent("popstate"));
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
    dropChatPlace();
    expect(chatPlaceIn()).toBeUndefined();
  });

  it("opens a link's page with no way-back strip, and from any other page names that page (#12055)", () => {
    history.replaceState(null, "", "/persona");
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, false);
    expect(handoffFrom()).toBeUndefined();
    history.replaceState(null, "", "/workspace-board");
    leaveChat({ name: "session", mid: "m1", sid: "s2" });
    expect(location.pathname).toBe("/s/m1/s2");
    expect(handoffFrom()).toEqual({ path: "/workspace-board" });
    history.replaceState(null, "", "/workspace-board");
    leaveChat({ name: "session", mid: "m1", sid: "s3" }, false);
    expect(handoffFrom()).toBeUndefined();
  });
});

describe("an Agent opened from inside a page (#12055)", () => {
  const back = () => new Promise<void>((done) => {
    addEventListener("popstate", () => setTimeout(done, 0), { once: true });
    history.back();
  });

  it("is a new entry naming the page, and Back returns to it", async () => {
    history.replaceState(null, "", "/todos?id=t1");
    const before = history.length;
    openFromPage({ name: "session", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/s/m1/s1");
    expect(history.length).toBe(before + 1);
    expect(handoffFrom()).toEqual({ path: "/todos?id=t1" });
    expect(sidebarPageOf(handoffFrom())).toBe("/todos?id=t1");
    await back();
    expect(location.pathname + location.search).toBe("/todos?id=t1");
    expect(handoffFrom()).toBeUndefined();
  });

  it("from a Persona conversation, is the hand-off it always was: the chat's place kept, the conversation named", () => {
    history.replaceState(null, "", "/persona/c1");
    chat({ top: 300, height: 1500, client: 600 });
    openFromPage({ name: "session", mid: "m1", sid: "s1" });
    expect(handoffFrom()).toEqual({ path: "/persona/c1" });
    history.back();
  });

  it("from an Agent's page opened from another page, keeps that first page for the sidebar", () => {
    history.replaceState(null, "", "/kb/projects/p1");
    openFromPage({ name: "session", mid: "m1", sid: "s1" });
    expect(fromHere()).toEqual({ path: "/s/m1/s1", page: "/kb/projects/p1" });
    openFromPage({ name: "session", mid: "m1", sid: "s2" });
    expect(handoffFrom()).toEqual({ path: "/s/m1/s1", page: "/kb/projects/p1" });
    expect(sidebarPageOf(handoffFrom())).toBe("/kb/projects/p1");
    // an Agent's page picked in the sidebar is the first page itself
    history.replaceState(null, "", "/s/m1/root");
    expect(fromHere()).toEqual({ path: "/s/m1/root" });
    // a page that is not an Agent's starts a chain of its own, whatever its entry carries
    history.replaceState({ handoff: { path: "/persona" } }, "", "/todos?id=t1");
    expect(openedFromHere()).toEqual({ handoff: { path: "/todos?id=t1" } });
  });

  it("reads only addresses in the app", () => {
    expect(handoffFrom({ handoff: { path: "/s/m1/s1", page: "https://example.com/" } })).toEqual({ path: "/s/m1/s1" });
    expect(handoffFrom({ handoff: { path: "/s/m1/s1", page: "/todos" } })).toEqual({ path: "/s/m1/s1", page: "/todos" });
    expect(handoffFrom({ handoff: { page: "/todos" } })).toBeUndefined();
    expect(sidebarPageOf(undefined)).toBeUndefined();
  });
});

describe("leaving a conversation from its side panel (Open Agent page)", () => {
  const back = () => new Promise<void>((done) => {
    addEventListener("popstate", () => setTimeout(done, 0), { once: true });
    history.back();
  });

  it("opens the task's panel again and puts the main chat back where it was, on Back (the strip's return too)", async () => {
    history.replaceState(null, "", "/persona/c1");
    chat({ top: 300, height: 1500, client: 600 });
    // the panel's own chat scroller comes after the main one's and is not the conversation's place
    const aside = document.createElement("aside");
    const inPanel = document.createElement("div");
    inPanel.setAttribute("data-chat-scroll", "");
    aside.append(inPanel);
    document.body.prepend(aside);
    openPanel({ kind: "task", todo: "t1" });
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, getPanel()!);
    closePanel();
    expect(location.pathname).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/persona/c1" });
    expect(panelIn()).toBeUndefined();
    await back();
    expect(location.pathname).toBe("/persona/c1");
    expect(getPanel()).toEqual({ kind: "task", todo: "t1" });
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
  });

  it("keeps an Agent's panel too, and nothing for other panels or a plain hand-off", async () => {
    history.replaceState(null, "", "/persona");
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "agent", mid: "m1", sid: "s1" });
    await back();
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s1" });
    closePanel();
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "files", mid: "m1", sid: "s1", cwd: "/example" });
    await back();
    expect(getPanel()).toBeNull();
    // a hand-off with no panel clears what an earlier one kept on this entry
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "task", todo: "t1" });
    await back();
    closePanel();
    chat({ top: 900, height: 1500, client: 600 });
    leaveChat({ name: "session", mid: "m1", sid: "s1" });
    await back();
    expect(getPanel()).toBeNull();
    expect(panelIn({ sidePanel: { kind: "link", url: "https://example.com/" } })).toBeUndefined();
  });

  it("keeps the panel for Back when it leaves from an overlay's entry", async () => {
    history.replaceState({ personaOverlay: "o1" }, "", "/persona/c1");
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "task", todo: "t1" });
    expect(panelIn()).toBeUndefined();
    history.replaceState(null, "", "/persona/c1");
    dispatchEvent(new PopStateEvent("popstate"));
    expect(getPanel()).toEqual({ kind: "task", todo: "t1" });
  });
});

describe("the main conversation's page, set to Full page (#13164)", () => {
  const back = () => new Promise<void>((done) => {
    addEventListener("popstate", () => setTimeout(done, 0), { once: true });
    history.back();
  });
  const at = () => location.pathname + location.search;
  // the main conversation of the assistant the page shows (made up)
  const assistant = async (main: string, manager = "") => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(JSON.stringify({ machine_id: "m1", main, manager, conversations: [] }), { headers: { "Content-Type": "application/json" } }))));
    await refreshAssistant();
  };
  afterEach(() => {
    // Main Chat picked in front: nothing kept for the next test
    pickMainChat(true);
    vi.unstubAllGlobals();
    localStorage.clear();
  });

  it("opens again over the conversation when Main Chat is picked from another place, and Back returns to it", async () => {
    setAgentOpenMode("page");
    history.replaceState(null, "", "/");
    chat({ top: 300, height: 1500, client: 600 });
    openAgent("m1", "s1");
    expect(at()).toBe("/s/m1/s1");
    navigate({ name: "inbox" });
    const before = history.length;
    expect(pickMainChat(false)).toBe(true);
    // the conversation's entry, then the Agent's naming it
    expect(history.length).toBe(before + 2);
    expect(at()).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/" });
    // kept while another place is in front: picked again from there, it opens again
    navigate({ name: "todos" });
    expect(pickMainChat(false)).toBe(true);
    expect(at()).toBe("/s/m1/s1");
    await back();
    expect(at()).toBe("/");
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
    // Back to the conversation drops it: Main Chat from another place is the bare conversation again
    navigate({ name: "inbox" });
    expect(pickMainChat(false)).toBe(false);
  });

  it("is dropped when Main Chat is picked while the conversation or its page is in front", () => {
    setAgentOpenMode("page");
    history.replaceState(null, "", "/");
    openTask("t1");
    expect(at()).toBe("/todos?id=t1");
    // its page in front: the row goes to the bare conversation (a pop to its root)
    expect(pickMainChat(true)).toBe(false);
    navigate({ name: "inbox" });
    expect(pickMainChat(false)).toBe(false);
  });

  it("is the page opened last, a link's from the conversation's cards too, which names the conversation", () => {
    setAgentOpenMode("page");
    history.replaceState(null, "", "/");
    openAgent("m1", "s1");
    navigate({ name: "persona" });
    openTask("t1");
    navigate({ name: "persona" });
    openLinkPage({ name: "session", mid: "m1", sid: "s2" });
    expect(handoffFrom()).toEqual({ path: "/" });
    navigate({ name: "inbox" });
    expect(pickMainChat(false)).toBe(true);
    expect(at()).toBe("/s/m1/s2");
    // from any other page a link's page is a root: it names nowhere, and the conversation keeps its page
    navigate({ name: "inbox" });
    openLinkPage({ name: "session", mid: "m1", sid: "s3" });
    expect(handoffFrom()).toBeUndefined();
    expect(pickMainChat(false)).toBe(true);
    expect(at()).toBe("/s/m1/s2");
  });

  it("belongs to the main conversation only, of the assistant shown", async () => {
    setAgentOpenMode("page");
    await assistant("c-main", "mgr-a");
    history.replaceState(null, "", "/persona/c-main");
    openAgent("m1", "s1");
    // another conversation's hand-off is that conversation's, not the main one's page
    navigate({ name: "persona", sid: "c-side" });
    openAgent("m1", "s2");
    expect(pickMainChat(false)).toBe(true);
    expect(at()).toBe("/s/m1/s1");
    // another assistant's main conversation never opens it
    navigate({ name: "inbox" });
    patchAssistant({ manager: "mgr-b", main: "c-other" });
    expect(pickMainChat(false)).toBe(false);
  });

  it("is not kept in the side panel mode, nor for a page naming nowhere, nor opened once set back to the panel", () => {
    history.replaceState(null, "", "/");
    // the side panel mode on a narrow window: a card's link opens a page, which names the conversation
    let narrow = true;
    vi.stubGlobal("matchMedia", (q: string) => ({ get matches() { return q === NARROW && narrow; }, addEventListener() {}, removeEventListener() {} }));
    openLink(`${location.origin}/s/m1/s1`);
    narrow = false;
    expect(at()).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/" });
    navigate({ name: "inbox" });
    setAgentOpenMode("page");
    expect(pickMainChat(false)).toBe(false);
    // a page opened naming nowhere (a Brief row's other conversation) is not the conversation's page
    history.replaceState(null, "", "/");
    leaveChat({ name: "persona", sid: "c2" }, false);
    navigate({ name: "inbox" });
    expect(pickMainChat(false)).toBe(false);
    history.replaceState(null, "", "/");
    openAgent("m1", "s1");
    navigate({ name: "inbox" });
    setAgentOpenMode("panel");
    expect(pickMainChat(false)).toBe(false);
  });
});

describe("where a task opens", () => {
  const todo = (agents: Todo["agents"]): Todo => ({ id: "t1", title: "Task", created_at: "", updated_at: "", agents });
  const at = "2026-11-10T08:00:00Z";
  const with_ = (t: Todo): State => ({ ...getState(), todos: [t] });

  it("is its most pressing Agent's page, else the to-do's page", () => {
    expect(taskRoute("t1", with_(todo([{ machine_id: "m1", session_id: "s1", linked_at: at }])))).toEqual({ name: "session", mid: "m1", sid: "s1" });
    expect(taskRoute("t1", with_(todo([{ machine_id: "m1", session_id: "s1", linked_at: at, released_at: at }])))).toEqual({ name: "todos", id: "t1" });
    expect(taskRoute("t1", with_(todo([])))).toEqual({ name: "todos", id: "t1" });
    expect(taskRoute("t1", with_(todo([{ machine_id: "m1", session_id: "s1", linked_at: at, start_error: "launcher missing" }])))).toEqual({ name: "todos", id: "t1" });
    expect(taskRoute("unknown")).toEqual({ name: "todos", id: "unknown" });
  });

  it("opens an owner verification on the task page and returns there from the raw worker", async () => {
    const verifying = { ...todo([{ machine_id: "m1", session_id: "s1", linked_at: at }]), supervision: { supervisor: "owner", state: "user_check" as const } };
    const report = taskRoute("t1", with_(verifying));
    expect(report).toEqual({ name: "todos", id: "t1" });
    leaveChat(report);
    openFromPage({ name: "session", mid: "m1", sid: "s1" });
    expect(handoffFrom()?.path).toBe("/todos?id=t1");
    const returned = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
    history.back();
    await returned;
    expect(location.pathname + location.search).toBe("/todos?id=t1");
  });

  it("opens there, set to Full page", () => {
    setAgentOpenMode("page");
    openTask("unknown");
    expect(location.pathname + location.search).toBe("/todos?id=unknown");
    expect(getPanel()).toBeNull();
  });
});

describe("where it opens (#5424)", () => {
  it("is the side panel by default: the task's or the Agent's, the page staying where it is", () => {
    history.replaceState(null, "", "/persona");
    expect(agentOpenMode()).toBe("panel");
    openTask("t1");
    expect(getPanel()).toEqual({ kind: "task", todo: "t1" });
    openAgent("m1", "s1");
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/persona");
    expect(handoffFrom()).toBeUndefined();
  });

  it("is the Agent's page with the way back, set to Full page, and the panel again once set back", () => {
    history.replaceState(null, "", "/persona");
    setAgentOpenMode("page");
    expect(localStorage.getItem("agent.open")).toBe("page");
    openAgent("m1", "s1");
    expect(getPanel()).toBeNull();
    expect(location.pathname).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/persona" });
    setAgentOpenMode("panel");
    // the default is not written: a browser left on it follows the default
    expect(localStorage.getItem("agent.open")).toBeNull();
    openAgent("m1", "s2");
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s2" });
    expect(location.pathname).toBe("/s/m1/s1");
  });
});
