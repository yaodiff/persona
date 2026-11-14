// @vitest-environment jsdom
// The personal agent list (views/agents/AgentsView.tsx) with one source failing while another lists agents: the failing
// source's hint shows as a notice above the agents, in the paged and the legacy list (#3098).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSession } from "../../lib/agents";

const state = vi.hoisted(() => ({
  messages: (() => Promise.resolve({ messages: [], hasMore: false })) as (id: string) => Promise<unknown>,
  features: [] as string[],
  list: { sessions: [] as unknown[], loaded: true, error: "", hints: [] as string[], sources: null },
}));
vi.mock("../../lib/agents", async (original) => ({
  ...(await original<typeof import("../../lib/agents")>()),
  useAgentList: () => state.list,
  fetchHiddenSessions: async () => [],
  fetchMessages: (id: string) => state.messages(id),
  markRead: async () => {},
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  useStore: <T,>(select: (s: { features: string[] }) => T): T => select({ features: state.features }),
}));
// the paged list fetches its own rows; the notice is what is under test
vi.mock("./PagedSessionList", () => ({ PagedSessionList: () => null }));
import { AgentsView } from "./AgentsView";
import { ApiError } from "../../lib/api";
import { t } from "../../i18n";

vi.hoisted(() => {
  (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
});

const agent = { id: "lark:a", title: "Cue", unreadCount: 0, rankTimeMs: 1 } as AgentSession;
const notice = () => document.querySelector("[role=status]")?.textContent ?? "";

let root: Root;
let el: HTMLDivElement;
beforeEach(() => {
  el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
});
afterEach(() => {
  act(() => root.unmount());
  el.remove();
  history.replaceState(null, "", "/");
});

describe.each([["paged", ["sessions_list"]], ["legacy", []]])("personal agents, %s list", (_, features) => {
  it("names a failing source above the agents the others list", async () => {
    state.features = features;
    state.list = { ...state.list, sessions: [agent], hints: ["plugin not running"] };
    await act(async () => root.render(<AgentsView onToggleSidebar={() => {}} />));
    expect(notice()).toContain(t("agents-agentsview.sources_unavailable"));
    expect(notice()).toContain("plugin not running");
  });

  it("shows no notice when every source lists", async () => {
    state.features = features;
    state.list = { ...state.list, sessions: [agent], hints: [] };
    await act(async () => root.render(<AgentsView onToggleSidebar={() => {}} />));
    expect(notice()).toBe("");
  });
});

describe("a link to a personal agent's conversation the hub doesn't have (web usability audit item 39)", () => {
  it("says so plainly with the way back to the list, and no box left loading", async () => {
    state.features = ["sessions_list"];
    state.list = { ...state.list, sessions: [], hints: [] };
    state.messages = () => Promise.reject(new ApiError(404, "not found", { error: "not found" }));
    await act(async () => root.render(<AgentsView id="does-not-exist" onToggleSidebar={() => {}} />));
    // the header names the list it belongs to; what is missing is said once, in the empty state
    expect(document.querySelector("h1")?.textContent).toBe(t("agents-agentsview.title"));
    expect(document.querySelector("[role=status]")?.textContent).toContain(t("not-found.agent_title"));
    expect(document.body.textContent?.split(t("not-found.agent_title")).length).toBe(2);
    expect(document.body.textContent).toContain(t("not-found.agent_detail"));
    expect(document.body.textContent).not.toContain("404");
    expect(document.querySelector("textarea")).toBeNull();
    history.replaceState(null, "", "/a/does-not-exist");
    const back = [...document.querySelectorAll("button")].find((b) => b.textContent === t("not-found.agent_back"));
    await act(async () => back!.click());
    expect(location.pathname).toBe("/a");
  });

  it("keeps the conversation and its error for another failure", async () => {
    state.list = { ...state.list, sessions: [], hints: [] };
    state.messages = () => Promise.reject(new ApiError(500, "storage unavailable", {}));
    await act(async () => root.render(<AgentsView id="lark:a" onToggleSidebar={() => {}} />));
    expect(document.body.textContent).not.toContain(t("not-found.agent_title"));
    expect(document.querySelector("textarea")).not.toBeNull();
  });
});
