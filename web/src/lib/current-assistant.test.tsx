// @vitest-environment jsdom
// The current assistant (docs/assistant/managers.md "Clients"): the pick is kept in this browser and survives a reload, a
// manager that left the roster goes back to the default, the pages list only the current one's things, and each
// assistant's count is its agents waiting plus its inbox items that need the user.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { scoped, scopeKey, scopeOf, useScope, type Scope } from "./current-assistant";
import { managerBadges, waitingAgents } from "./manager-attention";
import { CHOICE_KEY, chooseManager, chosenManager, currentHeaders, DISPLAY_IDENTITY_KEY, managerHeaderFor, PICKED_NAME_KEY, pickedName, resetManagerChoice, shownIdentity, shownManager } from "./manager-choice";
import { avatarColor, avatarInitial, resetRoster, setRoster, switcherLine, type ManagerList } from "./managers";
import { reopensOnSwitch, routeAfterSwitch } from "./persona-nav";
import type { Interaction, Session } from "./types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  resetRoster();
  resetManagerChoice();
});

const ROSTER: ManagerList = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, builtin: true, host: "server", inbox_needs_you: 4 },
    { id: "m-1", name: "工作", online: true, host: "MacBook", inbox_needs_you: 2 },
    { id: "m-2", name: "家里", online: false, last_seen: new Date(Date.now() - 3 * 3600_000).toISOString() },
    { id: "m-3", name: "测试", online: false, installing: new Date(Date.now() + 8 * 60_000).toISOString() },
  ],
};
const ONE: ManagerList = { default: "assistant", managers: [ROSTER.managers[0]] };

const session = (id: string, manager: string | undefined, patch: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: id, state: "needs_you", manager, ...patch }) as Session;
const card = (id: string, manager: string | undefined, sid: string): Interaction => ({ id, manager, machine_id: "mac", session_id: sid, kind: "question" }) as Interaction;

describe("the pick", () => {
  it("is kept in this browser, and only when it is another assistant than the default", () => {
    setRoster(ROSTER);
    chooseManager("m-1");
    expect(localStorage.getItem(CHOICE_KEY)).toBe("m-1");
    expect(managerHeaderFor("/assistant")).toBe("m-1");
    // the default is the absence of a pick: nothing kept, no header, as on a hub with one manager
    chooseManager("assistant");
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
    expect(managerHeaderFor("/assistant")).toBeUndefined();
  });

  it("remembers the default assistant for display without creating a pick or a routing header", () => {
    expect(DISPLAY_IDENTITY_KEY.slice("persona.shownAssistant".length)).toBe(CHOICE_KEY.slice("persona.currentManager".length));
    setRoster(ONE);
    expect(shownIdentity()).toEqual({ id: "assistant", name: "助手", isDefault: true, line: switcherLine(ONE.managers[0]), tone: "green" });
    expect(JSON.parse(localStorage.getItem(DISPLAY_IDENTITY_KEY)!)).toEqual({ id: "assistant", name: "助手", isDefault: true, line: switcherLine(ONE.managers[0]), tone: "green" });
    expect(chosenManager()).toBeNull();
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
    expect(currentHeaders()).toBeUndefined();
    expect(managerHeaderFor("/assistant/tasks")).toBeUndefined();

    resetRoster(); // loading again keeps the display identity, and still sends the default's requests unchanged
    expect(shownIdentity()).toEqual({ id: "assistant", name: "助手", isDefault: true, line: switcherLine(ONE.managers[0]), tone: "green" });
    expect(chosenManager()).toBeNull();
    expect(currentHeaders()).toBeUndefined();
    expect(managerHeaderFor("/assistant/tasks")).toBeUndefined();

    setRoster(ONE);
    resetManagerChoice(); // test cleanup clears the display even though a fresh roster is still in place
    expect(shownIdentity()).toBeNull();
    expect(localStorage.getItem(DISPLAY_IDENTITY_KEY)).toBeNull();
  });

  it("is there again after a reload, before the roster is read", async () => {
    localStorage.setItem(CHOICE_KEY, "m-1");
    vi.resetModules();
    const fresh = await import("./manager-choice");
    expect(fresh.chosenManager()).toBe("m-1");
    expect(fresh.managerHeaderFor("/assistant")).toBe("m-1");
    fresh.resetManagerChoice();
  });

  it("goes back to the default when its manager left the roster, or is not installed", () => {
    setRoster(ROSTER);
    chooseManager("m-1");
    setRoster({ ...ROSTER, managers: ROSTER.managers.filter((m) => m.id !== "m-1") });
    expect(chosenManager()).toBeNull();
    expect(shownManager()).toBe("assistant");
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
    chooseManager("m-3"); // still installing: nothing to show
    setRoster(ROSTER);
    expect(chosenManager()).toBeNull();
  });

  it("becomes no pick when its manager is made the default", () => {
    setRoster(ROSTER);
    chooseManager("m-1");
    setRoster({ ...ROSTER, default: "m-1" });
    expect(chosenManager()).toBeNull();
    expect(shownManager()).toBe("m-1");
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
  });

  it("keeps its name under a key per hub like the pick's, only for another assistant than the default, gone with the pick", () => {
    // the same hub suffix as the pick's key (an origin serving several hubs under prefixes keeps each one's apart)
    expect(PICKED_NAME_KEY.slice("persona.pickedAssistantName".length)).toBe(CHOICE_KEY.slice("persona.currentManager".length));
    setRoster(ROSTER);
    chooseManager("assistant"); // the default: nothing kept at all
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
    expect(localStorage.getItem(PICKED_NAME_KEY)).toBeNull();
    chooseManager("m-1");
    expect(localStorage.getItem(PICKED_NAME_KEY)).toBe("工作");
    // the pick left the roster: back to the default, the pick and its name both forgotten
    setRoster({ ...ROSTER, managers: ROSTER.managers.filter((m) => m.id !== "m-1") });
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
    expect(localStorage.getItem(PICKED_NAME_KEY)).toBeNull();
    // made the default: no pick, no name
    setRoster(ROSTER);
    chooseManager("m-1");
    setRoster({ ...ROSTER, default: "m-1" });
    expect(localStorage.getItem(CHOICE_KEY)).toBeNull();
    expect(localStorage.getItem(PICKED_NAME_KEY)).toBeNull();
  });

  it("keeps its name as the roster gives it, for the sidebar's head before the roster is read again", async () => {
    setRoster(ROSTER);
    chooseManager("m-1");
    expect(localStorage.getItem(PICKED_NAME_KEY)).toBe("工作");
    setRoster({ ...ROSTER, managers: ROSTER.managers.map((m) => (m.id === "m-1" ? { ...m, name: "公司" } : m)) });
    expect(pickedName()).toBe("公司");
    // a reload: the name is there before the roster
    vi.resetModules();
    const fresh = await import("./manager-choice");
    expect(fresh.pickedName()).toBe("公司");
    fresh.resetManagerChoice();
    // the default picked: no pick, no name
    chooseManager(null);
    expect(localStorage.getItem(PICKED_NAME_KEY)).toBeNull();
    expect(pickedName()).toBeNull();
  });

  it("names the current assistant on its knowledge base and on what it makes, never the health space", () => {
    setRoster(ROSTER);
    expect(currentHeaders()).toBeUndefined();
    chooseManager("m-1");
    for (const path of ["/kb/projects", "/kb-search?q=x", "/recordings/r1/minutes", "/assistant/tasks"]) expect(managerHeaderFor(path)).toBe("m-1");
    expect(currentHeaders()).toEqual({ "X-Persona-Manager": "m-1" });
    // the merged lists stay whole (the pages filter them), and the health space stays the default's
    for (const path of ["/inbox?sources=all", "/todos", "/machines", "/assistant?space=health", "/kbx"]) expect(managerHeaderFor(path)).toBeUndefined();
  });
});

describe("what the pages list", () => {
  it("is everything with one assistant", () => {
    const scope = scopeOf(ONE, "assistant");
    const items = [{ manager: "assistant" }, { manager: undefined }, { manager: "gone" }];
    expect(scope.several).toBe(false);
    expect(scoped(items, scope)).toBe(items);
  });

  it("is the current assistant's with several: no stamp or an unknown one is the default's", () => {
    const items = [{ id: 1, manager: "assistant" }, { id: 2, manager: undefined }, { id: 3, manager: "m-1" }, { id: 4, manager: "gone" }];
    expect(scoped(items, scopeOf(ROSTER, "assistant")).map((x) => x.id)).toEqual([1, 2, 4]);
    expect(scoped(items, scopeOf(ROSTER, "m-1")).map((x) => x.id)).toEqual([3]);
    expect(scopeOf(ROSTER, "m-1").manager?.name).toBe("工作");
  });
});

describe("what the pages list before the roster is read", () => {
  it("is the kept pick's only, not every assistant's until the roster comes", () => {
    const items = [{ id: 1, manager: "assistant" }, { id: 2, manager: undefined }, { id: 3, manager: "m-1" }];
    const kept = scopeOf(null, "m-1", "m-1");
    expect(kept.several).toBe(true);
    expect(scoped(items, kept).map((x) => x.id)).toEqual([3]);
    // no pick: one assistant as far as anyone knows, everything listed
    expect(scopeOf(null, "assistant", null).several).toBe(false);
  });
});

describe("the scope's identity", () => {
  it("stays across a roster read again with only a count changed, and moves with what the pages filter by", () => {
    const key = scopeKey(ROSTER, "m-1");
    const counted = { ...ROSTER, managers: ROSTER.managers.map((m) => ({ ...m, inbox_needs_you: (m.inbox_needs_you ?? 0) + 1 })) };
    expect(scopeKey(counted, "m-1")).toBe(key);
    expect(scopeKey(ROSTER, "assistant")).not.toBe(key);
    expect(scopeKey({ ...ROSTER, default: "m-1" }, "m-1")).not.toBe(key);
    expect(scopeKey({ ...ROSTER, managers: ROSTER.managers.filter((m) => m.id !== "m-2") }, "m-1")).not.toBe(key);
    expect(scopeKey({ ...ROSTER, managers: ROSTER.managers.map((m) => (m.id === "m-1" ? { ...m, online: false } : m)) }, "m-1")).not.toBe(key);
  });

  it("is the same object for every managers push that changes no more than a count", async () => {
    setRoster(ROSTER);
    chooseManager("m-1");
    const seen: Scope[] = [];
    function Probe() {
      seen.push(useScope());
      return null;
    }
    const host = document.createElement("div");
    const root = createRoot(host);
    await act(async () => root.render(<Probe />));
    for (let n = 1; n <= 3; n++) await act(async () => setRoster({ ...ROSTER, managers: ROSTER.managers.map((m) => ({ ...m, inbox_needs_you: n })) }));
    expect(seen.length).toBeGreaterThan(1);
    expect(new Set(seen).size).toBe(1);
    await act(async () => chooseManager(null));
    expect(seen.at(-1)).not.toBe(seen[0]);
    expect(seen.at(-1)!.id).toBe("assistant");
    await act(async () => root.unmount());
  });
});

describe("what waits at each assistant", () => {
  const sessions = [
    session("a", "assistant"),
    session("b", "m-1"),
    session("c", "m-1", { state: "working" }),
    session("quiet", "m-1", { quiet: true }), // a quiet task's agent: the main conversation tells about it
    session("main", "m-1", { assistant: true }),
    session("d", undefined), // no stamp: the default's
  ];

  it("counts an agent once, by a card or by its state, under its manager", () => {
    const cards = [card("x1", "m-1", "b"), card("x2", "m-1", "c"), card("x3", "m-1", "c"), card("x4", "assistant", "")];
    // m-1: b (needs you, and a card: once) and c (two cards: once); the default: a, d and a card without a session
    expect(waitingAgents(ROSTER, sessions, cards)).toEqual({ assistant: 3, "m-1": 2 });
  });

  it("leaves out a quiet task's cards and muted agents, and counts each card on no session", () => {
    const mine = [
      session("ok", "m-1", { state: "working" }),
      session("q", "m-1", { state: "working", quiet: true }),
      session("muted", "m-1"),
      session("spaced", "m-1", { state: "working", space_name: "noisy" }),
      session("allowed", "m-1", { state: "working", space_name: "noisy" }),
    ];
    const cards = [
      card("c1", "m-1", "ok"),
      card("c2", "m-1", "ok"), // the same session: once
      card("c3", "m-1", "q"), // a quiet task's: none
      card("c4", "m-1", "muted"), // muted session (and in Needs you): none
      card("c5", "m-1", "spaced"), // its space muted: none
      card("c6", "m-1", "allowed"), // its space muted but itself let through: counts
      card("c7", "m-1", ""), // on no session: each counts
      card("c8", "m-1", ""),
      card("c9", "m-1", "unknown"), // a session not listed yet, not muted: counts
    ];
    const mutes = { sessions: ["mac/muted"], spaces: ["mac/noisy"], allowed: ["mac/allowed"] };
    // ok, allowed, c7, c8, unknown
    expect(waitingAgents(ROSTER, mine, cards, mutes)["m-1"]).toBe(5);
    // no mutes: muted (needs you and a card) and spaced count too
    expect(waitingAgents(ROSTER, mine, cards)["m-1"]).toBe(7);
  });

  it("adds the roster's inbox count, none where the hub gives none", () => {
    expect(managerBadges(ROSTER, sessions, [])).toEqual({ assistant: 2 + 4, "m-1": 1 + 2, "m-2": 0 });
    const old = { ...ROSTER, managers: ROSTER.managers.map(({ inbox_needs_you: _n, ...m }) => m) };
    expect(managerBadges(old, sessions, [])).toEqual({ assistant: 2, "m-1": 1, "m-2": 0 });
  });
});

describe("an assistant in the switcher", () => {
  it("has the AI purple when it is the default, else the hue its id's bytes add up to, of five", () => {
    expect(avatarColor("anything", true)).toBe("var(--ai)");
    // "a" is 97, 97 % 5 = 2: orange; "b" 98 % 5 = 3: pink; "c" 4: indigo; "d" 0: blue; "e" 1: green
    expect(["a", "b", "c", "d", "e"].map((id) => avatarColor(id, false))).toEqual(["var(--hue-orange)", "var(--hue-pink)", "var(--hue-indigo)", "var(--hue-blue)", "var(--hue-green)"]);
    // UTF-8 bytes, not UTF-16 units: "é" is 0xC3 0xA9 = 364, 364 % 5 = 4
    expect(avatarColor("é", false)).toBe("var(--hue-indigo)");
  });

  it("shows its name's first character, whole and upper case", () => {
    expect(avatarInitial("工作")).toBe("工");
    expect(avatarInitial(" work")).toBe("W");
    expect(avatarInitial("👩‍💻 dev")).toBe("👩‍💻");
    expect(avatarInitial("")).toBe("");
  });

  it("says where an online one runs, when an offline one last synced, and how long an install code lasts", () => {
    expect(switcherLine(ROSTER.managers[1])).toBe("在线 · MacBook");
    expect(switcherLine(ROSTER.managers[2])).toContain("离线");
    expect(switcherLine(ROSTER.managers[3])).toBe("安装中 · 安装码 8 分钟后过期");
  });
});

describe("the page after a switch", () => {
  it("leaves one thing of the assistant left behind for its list; a list stays", () => {
    expect(routeAfterSwitch({ name: "persona" })).toBeNull();
    expect(routeAfterSwitch({ name: "persona", sid: "c1" })).toEqual({ name: "persona" });
    expect(routeAfterSwitch({ name: "session", mid: "mac", sid: "s1" })).toEqual({ name: "persona" });
    expect(routeAfterSwitch({ name: "machine", mid: "mac" })).toEqual({ name: "machines" });
    expect(routeAfterSwitch({ name: "inbox", id: "i1", manager: "m-1" })).toEqual({ name: "inbox" });
    expect(routeAfterSwitch({ name: "inbox" })).toBeNull();
    expect(routeAfterSwitch({ name: "kb", kind: "people", id: "p1" })).toEqual({ name: "kb", kind: "people" });
    expect(routeAfterSwitch({ name: "todos", id: "t1", view: "calendar", mode: "week" })).toEqual({ name: "todos", view: "calendar", mode: "week" });
    for (const name of ["settings", "machines", "routines", "home", "agent-list"] as const) expect(routeAfterSwitch({ name })).toBeNull();
    // a feature module's page (the health module's) stays too
    expect(routeAfterSwitch({ name: "module", module: "health", base: "health", path: ["h1"] })).toBeNull();
  });

  it("opens again where a page that stays reads the assistant's things itself", () => {
    for (const route of [{ name: "history" }, { name: "profile" }, { name: "routines" }, { name: "kb", kind: "docs" }] as const) expect(reopensOnSwitch(route)).toBe(true);
    // these follow the pick by themselves, or are no assistant's
    for (const name of ["persona", "home", "inbox", "machines", "todos", "agent-list", "settings", "messages", "terminal"] as const) expect(reopensOnSwitch({ name } as never)).toBe(false);
    expect(reopensOnSwitch({ name: "module", module: "health", base: "health", path: [] })).toBe(false);
  });
});
