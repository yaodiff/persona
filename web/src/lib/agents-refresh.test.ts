// The "Personal agents" list is pulled again on every {"type":"agents"} event: one that arrives while a pull is on the way
// may announce a change that pull was answered before, so it must still lead to another pull.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SOURCES_FRESH_MS, agentsChanged, refreshAgentPrefs, refreshAgentSessions, resetAgents, togglePin, updateAgentPrefs, useAgentList, useAgentPrefs, type AgentPrefs, type AgentSession } from "./agents";

vi.mock("react", async (orig) => ({
  ...(await orig<typeof import("react")>()),
  useSyncExternalStore: (_sub: unknown, get: () => unknown) => get(),
}));

const session = (id: string, unread: number): AgentSession => ({ id, source: "lark-bridge", sessionKey: id, provider: "p", title: id, canSend: true, unreadCount: unread, rankTimeMs: 1 });

type Answer = (sessions: AgentSession[]) => void;
let answers: Answer[] = [];
type PrefsAnswer = (pins: string[]) => void;
let prefsGets: PrefsAnswer[] = [];
let prefsPuts: PrefsAnswer[] = [];
let statusReads = 0;

beforeEach(() => {
  answers = [];
  statusReads = 0;
  prefsGets = [];
  prefsPuts = [];
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      if (String(url).endsWith("/agents")) {
        statusReads++;
        return Promise.resolve(new Response(JSON.stringify({ sources: [] })));
      }
      if (String(url).endsWith("/agents/prefs")) {
        const out = (init?.method ?? "GET").toUpperCase() === "PUT" ? prefsPuts : prefsGets;
        return new Promise<Response>((done) => out.push((pins) => done(new Response(JSON.stringify({ pins, view: {}, sections: [], assign: {} } satisfies AgentPrefs)))));
      }
      return new Promise<Response>((done) => answers.push((sessions) => done(new Response(JSON.stringify({ sessions })))));
    }),
  );
});

afterEach(() => {
  resetAgents();
  vi.unstubAllGlobals();
});

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("refreshAgentSessions", () => {
  it("pulls again for an agents event that came while a pull was on the way", async () => {
    const first = refreshAgentSessions();
    await flush();
    agentsChanged([]); // a new message: the pull already out may have been answered before it
    answers[0]([session("a", 0)]);
    await first;
    await flush();
    expect(answers.length).toBe(2);
    answers[1]?.([session("a", 1)]);
    await flush();
    await flush();
    expect(useAgentList().sessions.map((s) => s.unreadCount)).toEqual([1]);
  });

  it("pulls once more however many events came meanwhile", async () => {
    const first = refreshAgentSessions();
    await flush();
    for (let i = 0; i < 5; i++) agentsChanged([]);
    answers[0]([session("a", 0)]);
    await first;
    await flush();
    expect(answers.length).toBe(2);
    answers[1]([session("a", 3)]);
    await flush();
    await flush();
    expect(answers.length).toBe(2);
    expect(useAgentList().sessions.map((s) => s.unreadCount)).toEqual([3]);
  });
});

// GET /api/v1/agents asks every source's extension in turn; a session's new message does not change it (#7681).
describe("the sources' status", () => {
  const settle = async (n: number) => {
    await flush();
    answers[n]?.([session("a", n)]);
    await flush();
    await flush();
  };

  it("is read with the first pull and again for an event that says the list changed", async () => {
    void refreshAgentSessions();
    await settle(0);
    expect(statusReads).toBe(1);
    agentsChanged([]);
    await settle(1);
    expect(answers.length).toBe(2);
    expect(statusReads).toBe(2);
  });

  it("is not read again for events naming sessions while it is fresh", async () => {
    void refreshAgentSessions();
    await settle(0);
    for (let i = 1; i <= 3; i++) {
      agentsChanged(["lark-bridge:a"]);
      await settle(i);
    }
    expect(answers.length).toBe(4);
    expect(statusReads).toBe(1);
    expect(useAgentList().sessions.map((s) => s.unreadCount)).toEqual([3]);
  });

  it("is read again with an event naming sessions once it is a minute old", async () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1_000_000);
    void refreshAgentSessions();
    await settle(0);
    now.mockReturnValue(1_000_000 + SOURCES_FRESH_MS - 1);
    agentsChanged(["lark-bridge:a"]);
    await settle(1);
    expect(statusReads).toBe(1);
    now.mockReturnValue(1_000_000 + SOURCES_FRESH_MS);
    agentsChanged(["lark-bridge:a"]);
    await settle(2);
    expect(statusReads).toBe(2);
    now.mockRestore();
  });

  it("is read by the follow-up pull when one of the events it stands for said the list changed", async () => {
    void refreshAgentSessions();
    await flush();
    agentsChanged(["lark-bridge:a"]);
    agentsChanged([]);
    agentsChanged(["lark-bridge:a"]);
    await settle(0);
    await settle(1);
    expect(answers.length).toBe(2);
    expect(statusReads).toBe(2);
  });

  it("is read by the next login's first pull", async () => {
    void refreshAgentSessions();
    await settle(0);
    resetAgents();
    agentsChanged(["lark-bridge:a"]);
    await settle(1);
    expect(statusReads).toBe(2);
  });
});

describe("resetAgents", () => {
  it("drops a pull that was still on its way, and the pull queued behind it", async () => {
    const first = refreshAgentSessions();
    await flush();
    agentsChanged([]); // queues a follow-up pull
    resetAgents(); // another login
    answers[0]([session("old", 2)]);
    await first;
    await flush();
    await flush();
    expect(answers.length).toBe(1);
    expect(useAgentList().sessions).toEqual([]);
    expect(useAgentList().loaded).toBe(false);
  });

  it("lets the next login pull right away", async () => {
    void refreshAgentSessions();
    await flush();
    resetAgents();
    const next = refreshAgentSessions();
    await flush();
    expect(answers.length).toBe(2);
    answers[1]([session("new", 0)]);
    answers[0]([session("old", 2)]);
    await next;
    await flush();
    expect(useAgentList().sessions.map((s) => s.id)).toEqual(["new"]);
    expect(useAgentList().loaded).toBe(true);
  });

  it("an old login's follow-up settling late does not clear the new login's follow-up", async () => {
    void refreshAgentSessions();
    await flush();
    agentsChanged([]); // the old login's follow-up
    resetAgents();
    const next = refreshAgentSessions();
    await flush();
    agentsChanged([]); // the new login's follow-up
    answers[0]([session("old", 2)]); // the old pull settles now
    await flush();
    await flush();
    agentsChanged([]); // must reuse the new login's follow-up, not queue a second one
    answers[1]([session("new", 0)]);
    await next;
    await flush();
    expect(answers.length).toBe(3);
    answers[2]([session("new", 1)]);
    await flush();
    await flush();
    expect(answers.length).toBe(3);
    expect(useAgentList().sessions.map((s) => s.unreadCount)).toEqual([1]);
  });
});

// The hub broadcasts agents.prefs after every PUT and the page answers with a GET: a GET answer must not undo a
// newer local change or a newer GET answer.
describe("refreshAgentPrefs", () => {
  const pins = () => useAgentPrefs().pins;

  it("keeps a local change made after the GET was sent", async () => {
    const get = refreshAgentPrefs();
    await flush();
    updateAgentPrefs((p) => togglePin({ ...p, pins: [] }, "b"));
    await flush();
    prefsPuts[0](["b"]);
    await flush();
    prefsGets[0]([]); // answered before the PUT reached the hub
    await get;
    await flush();
    expect(pins()).toEqual(["b"]);
    expect(prefsGets.length).toBe(1);
  });

  it("drops a GET sent while a PUT is out, and pulls again once it is done", async () => {
    updateAgentPrefs((p) => togglePin({ ...p, pins: [] }, "a"));
    await flush();
    const get = refreshAgentPrefs(); // the broadcast came before the PUT's own answer
    await flush();
    prefsGets[0]([]);
    await get;
    await flush();
    expect(pins()).toEqual(["a"]);
    prefsPuts[0](["a"]);
    await flush();
    await flush();
    expect(prefsGets.length).toBe(2);
    prefsGets[1](["a", "c"]); // another device pinned c meanwhile
    await flush();
    await flush();
    expect(pins()).toEqual(["a", "c"]);
  });

  it("drops an older GET answered after a newer one", async () => {
    const first = refreshAgentPrefs();
    const second = refreshAgentPrefs();
    await flush();
    prefsGets[1](["a", "b"]);
    await second;
    prefsGets[0](["a"]);
    await first;
    await flush();
    expect(pins()).toEqual(["a", "b"]);
  });
});
