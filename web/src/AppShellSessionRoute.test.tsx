// @vitest-environment jsdom
// An agent's page (`/s/…`, `/t/…`) takes only what it opens from the machine's session list: a push for another session
// on that machine leaves the shell alone, so its sidebar and the open chat are not drawn again. The shell's link
// handling stays as it was: a pane's `/s/…` goes to its tab, an id prefix to the whole id, several matches to a list.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// every render of the shell draws its sidebar and the page's chat: these stand-ins count them and read nothing from the store
const drawn = vi.hoisted(() => ({ sidebar: 0, chat: 0, chatSid: "", at: undefined as string | undefined }));
vi.mock("./views/Sidebar", () => ({
  Sidebar: () => {
    drawn.sidebar++;
    return null;
  },
  SidebarRail: () => null,
}));
vi.mock("./views/chat/ChatView", () => ({
  ChatView: ({ sid, landAt }: { sid: string; landAt?: string }) => {
    drawn.chat++;
    drawn.chatSid = sid;
    drawn.at = landAt;
    return null;
  },
}));
vi.mock("./views/Palette", () => ({ Palette: () => null }));
vi.mock("./views/NewAgent", () => ({ NewAgent: () => null }));
vi.mock("./views/NewWorktreeHost", () => ({ NewWorktreeHost: () => null }));
vi.mock("./views/SidePanel", () => ({ SidePanel: () => null }));
vi.mock("./views/ShortcutHelp", () => ({ ShortcutHelp: () => null }));
vi.mock("./lib/store", async (load) => ({ ...(await load<typeof import("./lib/store")>()), connect: () => {}, disconnect: () => {} }));
import { getState, refresh, reset, upsertSession } from "./lib/store";
import { doneSubagentPanelAsk, useSubagentPanelAsk, type SubagentPanelAsk } from "./lib/subagent-link";
import { doneTeamAsk, useTeamAsk } from "./lib/team";
import { doneSideAsk, useSideAsk } from "./lib/side-panel";
import { setSideFold } from "./lib/side-fold";
import { setToken } from "./lib/api";
import { inboxReturnTo } from "./lib/inbox-nav";
import type { Machine, Session } from "./lib/types";
import { App } from "./App";
import { t } from "./i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const PREFIX_ID = "abcdef12-3456-4789-8abc-def012345678";
const TWIN_A = "fedcba98-0000-4000-8000-000000000001";
const TWIN_B = "fedcba98-0000-4000-8000-000000000002";
const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }];
const row = (session_id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id, title: `Task ${session_id}`, cwd: "/code", status: "working", state: "working", ...extra });
const sessions: Session[] = [
  row("s1"),
  row("s2"),
  // two panes of one tab: the second needs you, so a tab link without a pane would open it
  row("p1", { space_id: "sp", slot_id: "sl" }),
  row("p2", { space_id: "sp", slot_id: "sl", state: "needs_you", status: "needs_you" }),
  row(PREFIX_ID),
  row(TWIN_A, { activity_at: "2026-11-01T10:00:00Z" }),
  row(TWIN_B, { activity_at: "2026-11-02T10:00:00Z" }),
  // a child session folded under s1 (#5120, #14323): opens parent + subagent panel unless opened standalone
  row("kid1", { folded_under: "s1", is_subagent: true, parent_session_id: "s1" }),
  row("remote-kid", { machine_id: "box", folded_under: "s1", folded_under_machine_id: "mac", is_subagent: true, parent_machine_id: "mac", parent_session_id: "s1" }),
  row("asst-parent", { assistant: true }),
  row("asst-kid", { machine_id: "box", folded_under: "asst-parent", folded_under_machine_id: "mac", is_subagent: true, parent_machine_id: "mac", parent_session_id: "asst-parent" }),
  row("side1", { folded_under: "s1", folded_as: "side" }),
  row("side-kid", { folded_under: "s1", folded_side: "side1", is_subagent: true, parent_session_id: "side1" }),
  // a herdr tab's lead and its worker (#5180): the worker is in the lead's Team panel, never a page of its own
  row("lead", { space_id: "ts", slot_id: "tt", pane_index: 0, children: { total: 1, workers: 1, needs_you: 1 } }),
  row("mate", { space_id: "ts", slot_id: "tt", pane_index: 1, folded_under: "lead", folded_as: "worker", state: "needs_you", status: "needs_you" }),
  // a conversation that moved from Claude Code to Codex (#5054): its Claude session is not listed, the Codex one runs it
  row("conv", { agent: "codex", engine_history: [{ session_id: "conv-claude", agent: "claude", until: "2026-11-12T09:00:00Z" }] }),
];
const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions }, "/api/v1/machines": { machines }, "/api/v1/assistant": { machine_id: "mac", conversations: [] } };

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const open = async (path: string, state: Record<string, unknown> | null = null) => {
  history.replaceState(state, "", path);
  await act(() => refresh());
  act(() => root.render(<App />));
  await flush();
};
const pushTitles = async (sid: string, times: number) => {
  for (let i = 0; i < times; i++) {
    const current = getState().sessions.mac.find((s) => s.session_id === sid)!;
    act(() => upsertSession({ ...current, title: `Task ${sid} (${i})` }));
    await flush();
  }
};

beforeEach(() => {
  localStorage.clear();
  setToken("tok");
  drawn.sidebar = drawn.chat = 0;
  drawn.chatSid = "";
  drawn.at = undefined;
  setSideFold(false);
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("another session's push on the machine", () => {
  it("leaves a session page's shell and chat alone", async () => {
    await open("/s/mac/s1");
    expect(drawn.chatSid).toBe("s1");
    const sidebar = drawn.sidebar, chat = drawn.chat;
    expect(sidebar).toBeGreaterThan(0);

    await pushTitles("s2", 10);
    expect({ sidebar: drawn.sidebar - sidebar, chat: drawn.chat - chat }).toEqual({ sidebar: 0, chat: 0 });
  });

  it("leaves a tab page's shell and chat alone", async () => {
    await open("/t/mac/sp/sl/p1");
    expect(drawn.chatSid).toBe("p1");
    const sidebar = drawn.sidebar, chat = drawn.chat;

    await pushTitles("s2", 10);
    expect({ sidebar: drawn.sidebar - sidebar, chat: drawn.chat - chat }).toEqual({ sidebar: 0, chat: 0 });
  });
});

describe("the shell's session links", () => {
  it("replaces a summary-only archived Inbox conversation link with its Persona page", async () => {
    const summary = row("inbox-chat", { assistant: true, archived: true, origin: "inbox", origin_ref: "larkcli:chat:100" });
    getState().sessionSummaries = [summary];
    await open("/s/mac/inbox-chat", { personaInboxFrom: "/inbox/larkcli%3Achat%3A100" });
    expect(getState().sessions.mac.some((r) => r.session_id === summary.session_id)).toBe(false);
    expect(location.pathname + location.search).toBe("/persona/inbox-chat");
    expect(drawn.chatSid).toBe(summary.session_id);
    expect(inboxReturnTo(history.state)).toEqual({ id: summary.origin_ref });
  });

  it("replaces a pane's /s link with its tab's /t route", async () => {
    await open("/s/mac/p1");
    expect(location.pathname).toBe("/t/mac/sp/sl/p1");
    expect(drawn.chatSid).toBe("p1");
  });

  it("opens an 8-character prefix under the whole id", async () => {
    await open("/s/mac/abcdef12");
    expect(location.pathname).toBe(`/s/mac/${PREFIX_ID}`);
    expect(drawn.chatSid).toBe(PREFIX_ID);
  });

  it("lists the sessions an ambiguous prefix matches, newest first, and keeps the list up to date", async () => {
    await open("/s/mac/fedcba98");
    expect(location.pathname).toBe("/s/mac/fedcba98");
    expect(drawn.chat).toBe(0);
    const links = () => [...host.querySelectorAll("li a")].map((a) => a.getAttribute("href"));
    expect(links()).toEqual([`/s/mac/${TWIN_B}`, `/s/mac/${TWIN_A}`]);

    await pushTitles(TWIN_A, 1);
    expect(host.textContent).toContain(`Task ${TWIN_A} (0)`);
  });

  it("opens embedded <parent>@<agent> and child session deep links as parent chat + subagent panel", async () => {
    let asked: SubagentPanelAsk | undefined;
    function Probe() {
      asked = useSubagentPanelAsk("mac", "s1");
      return null;
    }
    const probe = createRoot(document.createElement("div"));
    act(() => probe.render(<Probe />));
    await open("/s/mac/kid1");
    expect(location.pathname).toBe("/s/mac/s1");
    expect(drawn.chatSid).toBe("s1");
    expect(asked).toEqual({ mid: "mac", sid: "kid1", from: "link" });
    act(() => doneSubagentPanelAsk("mac", "s1"));

    await open("/s/mac/s1@agent42");
    expect(location.pathname).toBe("/s/mac/s1");
    expect(drawn.chatSid).toBe("s1");
    expect(asked).toEqual({ mid: "mac", sid: "s1@agent42", from: "link" });
    act(() => doneSubagentPanelAsk("mac", "s1"));

    await open("/s/box/remote-kid");
    expect(location.pathname).toBe("/s/mac/s1");
    expect(drawn.chatSid).toBe("s1");
    expect(asked).toEqual({ mid: "box", sid: "remote-kid", from: "link" });
    act(() => doneSubagentPanelAsk("mac", "s1"));
    act(() => probe.unmount());

    // Deep link to a subagent under an Assistant conversation redirects to /persona/<parent> + queues subagent panel
    let asstAsked: SubagentPanelAsk | undefined;
    function AsstProbe() {
      asstAsked = useSubagentPanelAsk("mac", "asst-parent");
      return null;
    }
    const asstProbe = createRoot(document.createElement("div"));
    act(() => asstProbe.render(<AsstProbe />));
    await open("/s/box/asst-kid");
    expect(location.pathname).toBe("/persona/asst-parent");
    expect(asstAsked).toEqual({ mid: "box", sid: "asst-kid", from: "link" });
    act(() => doneSubagentPanelAsk("mac", "asst-parent"));
    act(() => asstProbe.unmount());
  });

  it("keeps standalone=1 subagent pages open without redirecting back to the parent", async () => {
    await open("/s/mac/s1@agent42?standalone=1");
    expect(location.pathname).toBe("/s/mac/s1@agent42");
    expect(location.search).toBe("?standalone=1");
    expect(drawn.chatSid).toBe("s1@agent42");

    await open("/s/mac/kid1?standalone=1");
    expect(location.pathname).toBe("/s/mac/kid1");
    expect(drawn.chatSid).toBe("kid1");

    await open("/s/box/remote-kid?standalone=1");
    expect(location.pathname).toBe("/s/box/remote-kid");
    expect(drawn.chatSid).toBe("remote-kid");
  });

  it.each(["asst-parent", "mate"])("keeps a standalone terminal link on the exact session %s", async (sid) => {
    const path = `/s/mac/${sid}?view=term&standalone=1`;
    await open(path);
    expect(location.pathname + location.search).toBe(path);
    expect(drawn.chatSid).toBe(sid);
  });

  it("an unlisted child opens as itself until its row says whose it is", async () => {
    await open("/s/mac/kid2");
    expect(drawn.chatSid).toBe("kid2");
    act(() => upsertSession(row("kid2", { folded_under: "s2", parent_session_id: "s2" })));
    await flush();
    expect(location.pathname).toBe("/s/mac/s2");
    expect(drawn.chatSid).toBe("s2");
    act(() => doneSubagentPanelAsk("mac", "s2"));
  });

  it("opens a worker's /s and /t links as its lead's page, which is asked to show the worker in its Team panel", async () => {
    let asked: ReturnType<typeof useTeamAsk>;
    function Probe() {
      asked = useTeamAsk("mac", "lead");
      return null;
    }
    const probe = createRoot(document.createElement("div"));
    act(() => probe.render(<Probe />));
    await open("/s/mac/mate");
    expect(location.pathname).toBe("/t/mac/ts/tt/lead");
    expect(drawn.chatSid).toBe("lead");
    expect(asked).toEqual({ worker: "mate" });
    act(() => doneTeamAsk("mac", "lead"));
    expect(asked).toBeUndefined();
    await open("/t/mac/ts/tt/mate");
    expect(location.pathname).toBe("/t/mac/ts/tt/lead");
    expect(drawn.chatSid).toBe("lead");
    expect(asked).toEqual({ worker: "mate" });
    act(() => doneTeamAsk("mac", "lead"));
    await open("/s/mac/mate?at=worker-item");
    expect(asked).toEqual({ worker: "mate", at: "worker-item" });
    act(() => doneTeamAsk("mac", "lead"));
    await open("/t/mac/ts/tt/mate?at=other-worker-item");
    expect(asked).toEqual({ worker: "mate", at: "other-worker-item" });
    act(() => doneTeamAsk("mac", "lead"));
    // a tab link without a pane, whose most urgent pane is the waiting worker: the lead, not a link to the worker (its
    // panel opens or not as the lead's page decides)
    await open("/t/mac/ts/tt");
    expect(location.pathname).toBe("/t/mac/ts/tt/lead");
    expect(drawn.chatSid).toBe("lead");
    expect(asked).toBeUndefined();
    act(() => probe.unmount());
  });

  it("delivers a side's message target to its own panel", async () => {
    setSideFold(true);
    let asked: ReturnType<typeof useSideAsk>;
    function Probe() {
      asked = useSideAsk("mac", "s1");
      return null;
    }
    const probe = createRoot(document.createElement("div"));
    act(() => probe.render(<Probe />));
    await open("/s/mac/side1?at=side-item");
    expect(drawn.chatSid).toBe("s1");
    expect(drawn.at).toBeUndefined();
    expect(asked).toEqual({ side: "side1", at: "side-item" });
    act(() => doneSideAsk("mac", "s1", asked!));
    act(() => probe.unmount());
  });

  it.each(["kid1", "side-kid", "conv-claude", "s1@child-agent"])("keeps %s's exact transcript for message links", async (sid) => {
    setSideFold(true);
    const at = `star:${"a".repeat(32)}:line12#0`;
    await open(`/s/mac/${encodeURIComponent(sid)}?at=${encodeURIComponent(at)}`);
    expect(drawn.chatSid).toBe(sid);
    expect(drawn.at).toBe(at);
    expect(location.pathname).toBe(`/s/mac/${encodeURIComponent(sid)}`);
  });

  it("opens an earlier engine's session of a conversation as the conversation's chat", async () => {
    await open("/s/mac/conv-claude");
    expect(location.pathname).toBe("/s/mac/conv");
    expect(drawn.chatSid).toBe("conv");
  });

  it("opens the pane a /t route names, not the tab's most urgent one", async () => {
    await open("/t/mac/sp/sl/p1");
    expect(location.pathname).toBe("/t/mac/sp/sl/p1");
    expect(drawn.chatSid).toBe("p1");
  });

  it("settles a /t route without a pane on the tab's most urgent one", async () => {
    await open("/t/mac/sp/sl");
    expect(drawn.chatSid).toBe("p2");
    expect(location.pathname).toBe("/t/mac/sp/sl/p2");
  });
});

describe("the browser tab's title on an Agent's page (web usability audit item 35)", () => {
  it("names the open Agent as its header does, and follows a rename, so several tabs tell apart", async () => {
    await open("/s/mac/s1");
    expect(document.title).toMatch(/^(\(\d+\) )?Task s1 · Persona$/);
    await open("/t/mac/sp/sl/p1");
    expect(document.title).toMatch(/^(\(\d+\) )?Task p1 · Persona$/);
    act(() => upsertSession({ ...getState().sessions.mac.find((s) => s.session_id === "p1")!, title: "排查 /orders 接口变慢" }));
    await flush();
    expect(document.title).toMatch(/^(\(\d+\) )?排查 \/orders 接口变慢 · Persona$/);
  });

  it("takes the folder's name for an untitled one, and says Agent only while its row is not known", async () => {
    await open("/s/mac/bare");
    expect(document.title).toMatch(/^(\(\d+\) )?Agent · Persona$/);
    act(() => upsertSession(row("bare", { title: "", cwd: "/code/weather-app" })));
    await flush();
    expect(document.title).toMatch(/^(\(\d+\) )?weather-app · Persona$/);
  });
});

describe("an address that is no page (web usability audit item 42)", () => {
  it("says the page isn't there, with the way to Overview, and keeps the address", async () => {
    await open("/foo-bar-unknown");
    for (let i = 0; i < 20 && !host.textContent?.includes(t("not-found.page_title")); i++) await act(() => new Promise((r) => setTimeout(r, 10)));
    expect(host.textContent).toContain(t("not-found.page_title"));
    expect(location.pathname).toBe("/foo-bar-unknown");
    expect(document.title).toMatch(new RegExp(`^(\\(\\d+\\) )?${t("app.title_not_found")} · Persona$`));
    const back = [...host.querySelectorAll("button")].find((b) => b.textContent === t("not-found.page_back"));
    await act(async () => back!.click());
    expect(location.pathname).toBe("/overview");
  });
});

describe("the reconnect pill (web usability audit item 87)", () => {
  it("is a strip at the top of the page's column that pushes the page down, never a layer over its header or fields", async () => {
    await open("/overview");
    act(() => Object.assign(getState(), { loaded: true, conn: "offline" }));
    act(() => root.render(<App />));
    await flush();
    const main = host.querySelector("main")!;
    const pill = main.querySelector("[data-connection-pill]");
    expect(pill).not.toBeNull();
    // the strip comes first in <main>'s column, and the page's box after it
    const strip = main.firstElementChild!;
    expect(strip.contains(pill)).toBe(true);
    expect(strip.nextElementSibling?.contains(pill)).toBe(false);
    expect(main.className).toMatch(/\bflex-col\b/);
    // nothing between the pill and <main> takes it out of the flow
    for (let el = pill!.parentElement; el && el !== main; el = el.parentElement) expect(el.className).not.toMatch(/\b(absolute|fixed)\b/);
    // once still, the strip doesn't clip the pill's details, which open over the page
    await act(() => new Promise((r) => setTimeout(r, 500)));
    expect(strip.querySelector(".overflow-hidden")).toBeNull();
  });
});
