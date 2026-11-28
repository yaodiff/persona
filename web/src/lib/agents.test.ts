import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { accountGroup, addSection, agentPath, arrangeSessions, assignSection, blockView, dateBucket, emptyPrefs, isDefaultView, mergeMessages, normalizePrefs, pendingOutgoing, providerLabel, providerMarkAgent, removeSection, resetView, setView, setupHints, toggleProvider, togglePin, totalUnread, viewOf, type AgentMessage, type AgentSession, type AgentSourceStatus, type Outgoing } from "./agents";
import { agentLetter, markId } from "./agent-kind";

const s = (id: string, provider: string, rank: number, unread = 0, title = id): AgentSession => ({ id, source: "lark-bridge", sessionKey: id, provider, title, canSend: true, unreadCount: unread, rankTimeMs: rank });
const m = (id: string, pos: number, role = "assistant", text = id, t = pos * 1000): AgentMessage => ({ externalId: id, role, blocks: [{ type: "text", text }], createTimeMs: t, position: pos });

describe("agentPath", () => {
  it("encodes the whole id, '/' and ':' included", () => {
    expect(agentPath("lark-bridge:agent:grok_bot:a1:u-1/")).toBe("/agents/sessions/lark-bridge%3Aagent%3Agrok_bot%3Aa1%3Au-1%2F");
  });
});

describe("mergeMessages", () => {
  it("dedupes by externalId and sorts out-of-order positions", () => {
    const older = [m("x100", 100), m("x102", 102), m("x101", 101)];
    const cur = [m("x102", 102, "assistant", "edited"), m("x103", 103)];
    const r = mergeMessages(older, cur);
    expect(r.map((x) => x.externalId)).toEqual(["x100", "x101", "x102", "x103"]);
    expect(r[2].blocks[0].text).toBe("edited");
  });
});

describe("blockView", () => {
  it("knows text and tool calls and shows anything else raw", () => {
    expect(blockView({ type: "text", text: "hi" })).toEqual({ kind: "text", text: "hi" });
    expect(blockView({ type: "tool-call", name: "Bash" })).toEqual({ kind: "tool", name: "Bash", detail: "" });
    expect(blockView({ type: "tool-call", name: "Bash", input: { cmd: "ls" } }).kind).toBe("tool");
    expect(blockView({ type: "reaction", from: "assistant", emoji: "👍" })).toEqual({ kind: "reaction", emoji: "👍" });
    expect(blockView({ type: "reaction" }).kind).toBe("raw");
    const raw = blockView({ type: "image", url: "u" });
    expect(raw.kind === "raw" && JSON.parse(raw.json)).toEqual({ type: "image", url: "u" });
  });
});

describe("pendingOutgoing", () => {
  const o = (clientId: string, text: string, status: Outgoing["status"], at = 10_000): Outgoing => ({ clientId, text, at, updatedAt: at, status });
  it("drops a sent bubble once the persisted user message shows up, keeps failed / unmatched ones", () => {
    const out = [o("1", "hello", "sent"), o("2", "hello", "sent"), o("3", "x", "failed"), o("4", "later", "sent")];
    const msgs = [m("t1", 1, "user", "hello", 11_000), m("t2", 2, "assistant", "later", 12_000)];
    expect(pendingOutgoing(out, msgs).map((x) => x.clientId)).toEqual(["2", "3", "4"]);
  });
  it("ignores an identical message from long before", () => {
    expect(pendingOutgoing([o("1", "ok", "sent", 1_000_000)], [m("t", 1, "user", "ok", 1000)])).toHaveLength(1);
  });
});

describe("arrangeSessions", () => {
  const now = new Date(2026, 9, 31, 12).getTime();
  const H = 3_600_000;
  const list = [s("a", "muse", now - H, 1, "Alpha"), s("b", "grok_bot", now - 2 * H, 3, "Beta"), s("c", "grok_bot", now - 30 * H, 0, "Charlie"), { ...s("d", "muse", now - 90 * 24 * H, 0, "Delta"), canSend: false }];
  const ids = (g: { sessions: AgentSession[] }[]) => g.map((x) => x.sessions.map((y) => y.id));
  it("defaults to date buckets, recent first", () => {
    const g = arrangeSessions(list, emptyPrefs(), "", now);
    expect(g.map((x) => x.label)).toEqual(["今天", "昨天", "更早"]);
    expect(ids(g)).toEqual([["a", "b"], ["c"], ["d"]]);
  });
  it("puts pins first in pin order whatever the grouping", () => {
    const p = { ...emptyPrefs(), pins: ["c", "a", "gone"], view: { group: "provider" as const } };
    const g = arrangeSessions(list, p, "", now);
    expect(g[0]).toMatchObject({ key: "pinned", label: "已置顶" });
    expect(ids(g)).toEqual([["c", "a"], ["b"], ["d"]]);
  });
  it("sorts by unread or name and can skip grouping", () => {
    expect(ids(arrangeSessions(list, { ...emptyPrefs(), view: { group: "none", sort: "unread" } }, "", now))).toEqual([["b", "a", "c", "d"]]);
    expect(ids(arrangeSessions(list, { ...emptyPrefs(), view: { group: "none", sort: "name" } }, "", now))).toEqual([["a", "b", "c", "d"]]);
    expect(totalUnread(list)).toBe(4);
  });
  it("filters by show mode, providers and query", () => {
    const flat = (v: object, q = "") => ids(arrangeSessions(list, { ...emptyPrefs(), view: { group: "none", ...v } }, q, now)).flat();
    expect(flat({ show: "unread" })).toEqual(["a", "b"]);
    expect(flat({ show: "sendable" })).toEqual(["a", "b", "c"]);
    expect(flat({ providers: ["muse"] })).toEqual(["a", "d"]);
    expect(flat({}, "char")).toEqual(["c"]);
    expect(flat({}, "MUSE")).toEqual(["a", "d"]); // the provider, whatever the case
    expect(flat({}, "grok bot")).toEqual(["b", "c"]); // its label
  });
  it("custom sections come after pins; removing one frees its sessions", () => {
    let p = addSection(emptyPrefs(), "工作", "w");
    p = assignSection(assignSection(p, "c", "w"), "d", "w");
    p = togglePin(p, "d");
    const g = arrangeSessions(list, p, "", now);
    expect(g.map((x) => x.key).slice(0, 2)).toEqual(["pinned", "sec:w"]);
    expect(ids(g).slice(0, 2)).toEqual([["d"], ["c"]]);
    p = removeSection(p, "w");
    expect(p.assign).toEqual({});
  });
  it("reset keeps pins and sections, and view helpers merge", () => {
    let p = setView(togglePin(emptyPrefs(), "a"), { sort: "name" });
    expect(isDefaultView(p)).toBe(false);
    p = toggleProvider(p, "muse");
    expect(viewOf(p).providers).toEqual(["muse"]);
    p = resetView(p);
    expect(isDefaultView(p)).toBe(true);
    expect(p.pins).toEqual(["a"]);
    expect(togglePin(p, "a").pins).toEqual([]);
  });
  it("normalizes whatever the hub or storage returns", () => {
    expect(normalizePrefs(null)).toEqual(emptyPrefs());
    expect(normalizePrefs({ pins: ["x"] } as never).sections).toEqual([]);
  });
});

describe("setupHints", () => {
  it("gives a failing source's own reason, and prefers a setup hint", () => {
    expect(setupHints([{ source: "a", enabled: true, error: "Not configured yet" }])).toEqual(["Not configured yet"]);
    expect(setupHints([{ source: "b", enabled: true, setup: "no_accounts", error: "x" }])).not.toContain("x");
  });

  it("says what to do for each source that is not set up, nothing for a ready one", () => {
    const h = setupHints([
      { source: "a", enabled: false, setup: "disabled" },
      { source: "b", enabled: true, setup: "no_accounts" },
      { source: "c", enabled: true },
      { source: "d", enabled: true, setup: "later" },
    ]);
    expect(h).toHaveLength(2);
    expect(h[0]).toContain("扩展");
    expect(h[1]).toContain("账号");
  });
});

describe("date groups by calendar day (#9912)", () => {
  // A calendar day is 23 or 25 hours on the days the clocks change; the groups count days, not 24-hour steps.
  const savedTZ = process.env.TZ;
  beforeAll(() => {
    process.env.TZ = "America/New_York";
  });
  afterAll(() => {
    if (savedTZ === undefined) delete process.env.TZ;
    else process.env.TZ = savedTZ;
  });
  const at = (month: number, date: number, h = 0, m = 0) => new Date(2026, month, date, h, m).getTime();
  const key = (ms: number, now: number) => dateBucket(ms, now).key;

  it("after the clocks go forward, two days ago is not yesterday", () => {
    const now = at(2, 9, 12); // Mon 2026-03-09; Sun 03-08 had 23 hours
    expect(key(at(2, 9, 0, 10), now)).toBe("today");
    expect(key(at(2, 8, 0, 30), now)).toBe("yesterday");
    expect(key(at(2, 7, 23, 30), now)).toBe("week");
  });

  it("after the clocks go back, the first hour of yesterday is yesterday", () => {
    const now = at(10, 2, 12); // Mon 2026-11-02; Sun 11-01 had 25 hours
    expect(key(at(10, 1, 0, 30), now)).toBe("yesterday");
    expect(key(at(10, 1, 23, 30), now)).toBe("yesterday");
    expect(key(at(9, 31, 23, 30), now)).toBe("week");
  });

  it("the last 7 and 30 days start at a midnight across a change too", () => {
    expect(key(at(2, 7, 0, 30), at(2, 14, 12))).toBe("week");
    expect(key(at(2, 6, 23, 30), at(2, 14, 12))).toBe("month");
    expect(key(at(1, 18, 0, 30), at(2, 20, 12))).toBe("month");
    expect(key(at(1, 17, 23, 30), at(2, 20, 12))).toBe("older");
  });

  it("an ordinary week reads the same", () => {
    const now = at(5, 10, 12); // Wed 2026-06-10
    expect(key(at(5, 9, 0, 0), now)).toBe("yesterday");
    expect(key(at(5, 8, 23, 59), now)).toBe("week");
    expect(key(at(5, 3, 0, 0), now)).toBe("week");
    expect(key(at(5, 2, 23, 59), now)).toBe("month");
    expect(key(at(4, 11, 0, 0), now)).toBe("month");
    expect(key(at(4, 10, 23, 59), now)).toBe("older");
  });
});

describe("account groups (#16248)", () => {
  const sources: AgentSourceStatus[] = [
    { source: "bridge-a", enabled: true, accounts: [{ accountId: "acct-1", provider: "dots", displayName: " Dots · Example User " }, { accountId: "acct-2", provider: "muse2api", displayName: "" }] },
    { source: "bridge-b", enabled: true, accounts: [{ accountId: "acct-3", displayName: "" }] },
    { source: "bridge-c", enabled: false },
  ];
  const rows = [s("x", "groq", 1), s("y", "grok_bot", 2)].map((r, i) => ({ ...r, accountId: ["acct-9", "acct-3"][i] }));

  it("names an account by its displayName, else its provider's label, else its sessions' provider, and last its id", () => {
    expect(accountGroup("acct-1", sources, rows)).toEqual({ name: "Dots · Example User", provider: "dots" });
    expect(accountGroup("acct-2", sources, rows)).toEqual({ name: "Muse", provider: "muse2api" });
    expect(accountGroup("acct-3", sources, rows)).toEqual({ name: "Grok Bot", provider: "grok_bot" }); // listed with neither: its sessions say
    expect(accountGroup("acct-9", sources, rows)).toEqual({ name: "Groq", provider: "groq" }); // not listed at all
    expect(accountGroup("acct-0", sources, rows)).toEqual({ name: "acct-0", provider: "" });
    expect(accountGroup("acct-1", null, [])).toEqual({ name: "acct-1", provider: "" }); // before /agents has answered
  });

  it("reads whatever an extension sends for accounts without throwing", () => {
    const odd = [{ source: "x", enabled: true, accounts: [null, "acct-1", { accountId: "acct-1", displayName: 7 }] }] as unknown as AgentSourceStatus[];
    expect(accountGroup("acct-1", odd, [])).toEqual({ name: "acct-1", provider: "" });
  });

  it("labels the agent-source providers", () => {
    expect(["grok_bot", "muse", "muse2api", "cue", "dots", "claude_duet", "gemini_tasks", "groq"].map(providerLabel))
      .toEqual(["Grok Bot", "Muse", "Muse", "Cue", "Dots", "Claude Duet", "Gemini", "Groq"]);
  });

  it("draws a provider with its product's bundled mark, else its label's letter, never Claude Code's for an unknown one", () => {
    const mark = (p: string) => markId(providerMarkAgent(p));
    expect(["claude_duet", "gemini_tasks"].map(mark)).toEqual(["claude", "gemini"]);
    for (const [p, letter] of [["grok_bot", "G"], ["dots", "D"], ["muse", "M"], ["muse2api", "M"], ["cue", "C"], ["groq", "G"], ["some_new_bot", "S"]]) {
      expect(mark(p)).not.toMatch(/^(claude|codex|opencode|pi|gemini|cursor|trae|grok|agy|dsh|kimi|copilot)$/);
      expect(agentLetter(providerMarkAgent(p))).toBe(letter);
    }
  });
});
