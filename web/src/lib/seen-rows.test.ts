/// <reference types="node" />
// @vitest-environment jsdom
// Rows read from Unread or Just finished stay in place, drawn as Seen, until refreshed (#12585):
// replays the shared cross-platform contract in spec/fixtures/sort/next/held_set.json and spec/fixtures/sort/held.json,
// and tests the visit state machine and Unread filter `kept` updates.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyFilter, arrangeSpaces, extendKept, filterCounts, removeKept, type AgentFilter } from "./agent-filter";
import { buildAgentGroups, buildSpaces, isHeldSeen, isQuiet, type AgentMutes, type HeldOrderOptions, type HeldPlace } from "./agent-groups";
import { api } from "./api";
import { activityOf, viewOf } from "./format";
import { agentKey, type AgentPins } from "./pins";
import { getSeenVisit, getShownThisVisitForTest, heldOrderFor, noteShownUnseen, releaseAllSeen, releaseSeen, resetSeenVisit, seenRowsMode, seenRowsNoticed, setSeenRowsMode, setSeenRowsNoticed } from "./seen-rows";
import { connect, getSession, markSeen, markSeenLocally, reset, viewSession } from "./store";
import { byTime } from "./times";
import type { Machine, Session } from "./types";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));

interface SortFixture {
  name: string;
  input: {
    sessions: Session[];
    machines: { id: string; name: string }[];
    pins: AgentPins;
    mutes: AgentMutes;
    settings: {
      grouping: "state" | "space" | "machine";
      quick: "all" | "needsYou" | "unread" | "working" | "backgroundWaiting";
      query: string;
      machine_filter: string;
      kept: string[];
      folded: string[];
      expanded_idle: string[];
      expanded_quiet: string[];
      compact: boolean;
      held?: string;
      held_seen?: Record<string, HeldPlace>;
    };
  };
  expected: {
    counts: Record<string, number>;
    groups: {
      id: string;
      title: string;
      rows: string[];
      visible_rows: string[];
      folded: boolean;
      parent?: string;
      state_counts?: Record<string, number>;
      held_seen?: string[];
    }[];
    unread_shown: string[];
    quiet_title?: string | null;
  };
}

const FIXTURE_DIR = resolve(__dirname, "../../../spec/fixtures/sort");
// These shared fixtures predate P5. Retained membership, folding and counts stay the
// same. On Web the old completion sections are Done and Working precedes Done.
function loadFixture(rel: string): SortFixture[] {
  const { cases } = JSON.parse(readFileSync(resolve(FIXTURE_DIR, rel), "utf8")) as { cases: SortFixture[] };
  for (const fixture of cases) {
    const groups = fixture.expected.groups;
    if (fixture.input.settings.grouping === "state") {
      for (const group of groups) {
        if (group.id === "state/unread" || group.id === "state/just_finished") group.id = "state/done";
      }
      const order = ["state/needs", "state/working", "state/done", "state/idle"];
      groups.sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id));
    }
    // A held, seen row keeps its section. Its rank now comes from its current
    // view, so it follows live work and unseen replies within that section.
    const expectedOrder: Record<string, string[]> = {
      held_set_rows_keep_unread_section_by_status: ["still-unread", "read-one", "read-two"],
      held_set_unread_filter_by_status: ["b", "a"],
      held_set_machine_section_shows_read_rows: ["work", "idle", "read"],
      held_quiet_space_stays_active: ["seen", "held"],
      held_set_quiet_space_stays_active: ["idle-peer", "read"],
    };
    const rows = expectedOrder[fixture.name]?.map((sid) => `machine-a/${sid}`);
    if (rows) {
      const group = groups.find((g) => g.rows.includes(rows[0]));
      if (group) {
        group.rows = rows;
        group.visible_rows = rows.filter((key) => group.visible_rows.includes(key));
      }
    }
  }
  return cases;
}

const QUICK_MAP: Record<SortFixture["input"]["settings"]["quick"], AgentFilter> = {
  all: "all", needsYou: "needs", unread: "unread", working: "working", backgroundWaiting: "bg_waiting",
};
const GROUP_ID_MAP: Record<string, string> = {
  "state:needs": "state/needs",
  "state:done": "state/done",
  "state:reply": "state/awaiting",
  "state:working": "state/working",
  "state:bg_waiting": "state/background",
  "state:just_finished": "state/just_finished",
  "state:waiting": "state/waiting",
  "state:muted": "state/muted",
  "state:idle": "state/idle",
};

/** Replays one spec/fixtures/sort agents_list case through Web's real projection functions. */
function replayCase(c: SortFixture) {
  const { settings, pins, mutes } = c.input;
  const machines: Machine[] = c.input.machines.map((m) => ({ id: m.id, name: m.name, online: true }));
  const held: HeldOrderOptions = {
    ...(settings.held ? { held: settings.held } : {}),
    ...(settings.held_seen ? { heldSeen: settings.held_seen } : {}),
  };
  const live = c.input.sessions.filter((s) => !s.assistant && activityOf(s) !== "offline");
  const rawCounts = filterCounts(live, mutes);
  const counts = {
    needsYou: rawCounts.needs,
    unread: rawCounts.unread,
    working: rawCounts.working,
    backgroundWaiting: rawCounts.bg_waiting,
  };
  const filter = QUICK_MAP[settings.quick];
  const filtered = applyFilter(live, filter, mutes, new Set(settings.kept));
  const unread_shown = filtered.filter((s) => activityOf(s) === "unread").map(agentKey);

  const sortDate = (s: Session) => (activityOf(s) === "working" ? s.state_since || s.updated_at : s.updated_at) ?? "";
  const ordered = (list: Session[], byRank = filter === "all") =>
    [...list].sort((a, b) =>
      (byRank ? viewOf(a).rank - viewOf(b).rank : 0)
      || byTime(sortDate(b), sortDate(a))
      || a.session_id.localeCompare(b.session_id)
      || agentKey(a).localeCompare(agentKey(b)),
    );
  const stays = (s: Session) => !isQuiet(s, mutes, held) || pins.sessions.includes(agentKey(s));

  const groups: {
    id: string;
    rows: string[];
    visible_rows: string[];
    folded: boolean;
    parent?: string;
    state_counts?: Record<string, number>;
    held_seen: string[];
  }[] = [];

  if (settings.grouping === "state") {
    const built = buildAgentGroups(filtered, machines, "state", mutes, undefined, held);
    for (const g of built) {
      const id = GROUP_ID_MAP[g.key] ?? g.key;
      const folded = g.key === "state:idle" ? !settings.folded.includes("!state/idle") : settings.folded.includes(id);
      const rows = g.sessions.map(agentKey);
      groups.push({
        id,
        rows,
        visible_rows: folded ? [] : rows,
        folded,
        held_seen: (g.heldSeen ?? []).map(agentKey),
      });
    }
  } else if (settings.grouping === "space") {
    const { spaces } = buildSpaces(filtered, machines);
    const { active, idle } = arrangeSpaces(spaces, {
      pins,
      mutes,
      filtered: filter !== "all",
      busy: (sp) => sp.sessions.some((s) => !isQuiet(s, mutes, held)),
    });
    const stateCountsOf = (sessions: Session[]) => {
      const sc = { needs_you: 0, working: 0, unread: 0, idle: 0, unknown: 0 };
      for (const s of sessions) {
        const st = activityOf(s);
        if (st in sc) sc[st as keyof typeof sc]++;
      }
      return sc;
    };
    for (const sp of active) {
      const rows = ordered(sp.sessions);
      const folded = settings.folded.includes(sp.key);
      const visible = folded ? [] : settings.expanded_idle.includes(sp.key) || filter !== "all" ? rows : rows.filter(stays);
      groups.push({
        id: sp.key,
        rows: rows.map(agentKey),
        visible_rows: visible.map(agentKey),
        folded,
        state_counts: stateCountsOf(rows),
        held_seen: rows.filter((s) => isHeldSeen(s, mutes, held)).map(agentKey),
      });
    }
    for (const sp of idle) {
      const rows = sp.sessions;
      const expanded = settings.expanded_quiet.includes(sp.key);
      groups.push({
        id: sp.key,
        rows: rows.map(agentKey),
        visible_rows: expanded ? rows.map(agentKey) : [],
        folded: !expanded,
        parent: "quiet",
        state_counts: stateCountsOf(rows),
        held_seen: rows.filter((s) => isHeldSeen(s, mutes, held)).map(agentKey),
      });
    }
  } else if (settings.grouping === "machine") {
    for (const m of machines) {
      const mine = ordered(filtered.filter((s) => s.machine_id === m.id));
      if (!mine.length) continue;
      const id = `machine/${m.id}`;
      const folded = settings.folded.includes(id);
      const visible = folded ? [] : settings.expanded_idle.includes(`machine-${m.id}`) || filter !== "all" ? mine : mine.filter(stays);
      groups.push({
        id,
        rows: mine.map(agentKey),
        visible_rows: visible.map(agentKey),
        folded,
        held_seen: mine.filter((s) => isHeldSeen(s, mutes, held)).map(agentKey),
      });
    }
  }

  return { counts, groups, unread_shown };
}

describe("shared contract fixtures (spec/fixtures/sort/next/held_set.json)", () => {
  const cases = loadFixture("next/held_set.json");

  it("loads all six held-set cases", () => {
    expect(cases.length).toBeGreaterThanOrEqual(6);
  });

  it.each(cases)("$name matches iOS held rows, groups and counts", (fixture) => {
    const actual = replayCase(fixture);
    expect(actual.counts).toEqual(fixture.expected.counts);
    expect(actual.unread_shown).toEqual(fixture.expected.unread_shown);
    expect(actual.groups.map((g) => g.id)).toEqual(fixture.expected.groups.map((g) => g.id));
    for (let i = 0; i < fixture.expected.groups.length; i++) {
      const got = actual.groups[i]!;
      const want = fixture.expected.groups[i]!;
      expect(got.rows, `${fixture.name} ${got.id} rows`).toEqual(want.rows);
      expect(got.visible_rows, `${fixture.name} ${got.id} visible_rows`).toEqual(want.visible_rows);
      expect(got.folded, `${fixture.name} ${got.id} folded`).toBe(want.folded);
      if (want.parent) expect(got.parent, `${fixture.name} ${got.id} parent`).toBe(want.parent);
      if (want.state_counts) expect(got.state_counts, `${fixture.name} ${got.id} state_counts`).toEqual(want.state_counts);
      expect(got.held_seen, `${fixture.name} ${got.id} held_seen`).toEqual(want.held_seen ?? []);
    }
  });
});

describe("shared single-held fixtures (spec/fixtures/sort/held.json)", () => {
  const cases = loadFixture("held.json");

  it.each(cases)("$name matches iOS held row behavior", (fixture) => {
    const actual = replayCase(fixture);
    expect(actual.counts).toEqual(fixture.expected.counts);
    expect(actual.unread_shown).toEqual(fixture.expected.unread_shown);
    expect(actual.groups.map((g) => g.id)).toEqual(fixture.expected.groups.map((g) => g.id));
    for (let i = 0; i < fixture.expected.groups.length; i++) {
      const got = actual.groups[i]!;
      const want = fixture.expected.groups[i]!;
      expect(got.rows).toEqual(want.rows);
      expect(got.visible_rows).toEqual(want.visible_rows);
      expect(got.folded).toBe(want.folded);
      if (want.state_counts) expect(got.state_counts).toEqual(want.state_counts);
    }
  });
});

describe("Unread filter kept helpers", () => {
  it("takes in newly matching keys while chosen and removes cleared keys without mutating", () => {
    const base = new Set(["m/a"]);
    expect(extendKept(base, new Set(["m/a"]))).toBe(base);
    const extended = extendKept(base, new Set(["m/a", "m/b"]));
    expect([...extended]).toEqual(["m/a", "m/b"]);
    expect([...base]).toEqual(["m/a"]);
    expect(removeKept(extended, ["m/missing"])).toBe(extended);
    const trimmed = removeKept(extended, ["m/a"]);
    expect([...trimmed]).toEqual(["m/b"]);
  });
});

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.last = this; }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
}

const session = (id: string, state: Session["state"], patch: Partial<Session> = {}): Session => ({
  machine_id: "m",
  session_id: id,
  title: id,
  cwd: "/tmp/p",
  status: state === "unread" ? "done" : state === "working" ? "working" : "idle",
  state,
  updated_at: "2026-01-01T00:01:00Z",
  ...patch,
});

describe("visit state machine and store integration", () => {
  const opened: Array<() => void> = [];
  const open = (mid: string, sid: string) => {
    const release = viewSession(mid, sid);
    let done = false;
    const close = () => { if (!done) { done = true; release(); } };
    opened.push(close);
    return close;
  };

  const online = (rows: Session[]) => {
    connect();
    FakeSocket.last.open();
    FakeSocket.last.push({ type: "hello", features: ["sessions_delta"], event_stream: "e", event_seq: 1 });
    FakeSocket.last.push({ type: "sessions", machine_id: "m", sessions: rows });
    return FakeSocket.last;
  };

  beforeEach(() => {
    localStorage.clear();
    resetSeenVisit();
    vi.stubGlobal("WebSocket", FakeSocket);
    vi.stubGlobal("location", { protocol: "https:", host: "hub.example.com" });
    reset();
    vi.mocked(api).mockReset().mockResolvedValue({});
  });

  afterEach(() => {
    opened.splice(0).forEach((close) => close());
    reset();
    resetSeenVisit();
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("holds rows opened, marked, or seen elsewhere until Clear seen or refresh", () => {
    const sock = online([
      session("open", "unread"),
      session("menu", "unread"),
      session("done", "idle", { work: { phase: "done", just_finished: true } }),
      session("away", "unread"),
    ]);
    noteShownUnseen({ "m/open": "unread", "m/away": "unread" });
    const close = open("m", "open");
    close();
    expect(getSeenVisit().open).toEqual({});
    markSeenLocally("m", "menu");
    markSeenLocally("m", "done");
    expect(getSeenVisit().seen).toEqual({ "m/open": "unread", "m/menu": "unread", "m/done": "just_finished" });
    expect([...getShownThisVisitForTest().entries()]).toEqual([["m/away", "unread"]]);

    // Seen on another device: hub pushes idle, list holds it in Unread.
    sock.push({
      type: "sessions",
      machine_id: "m",
      sessions: [session("open", "idle"), session("menu", "idle"), session("done", "idle"), session("away", "idle")],
    });
    expect(getSeenVisit().seen).toEqual({
      "m/open": "unread",
      "m/menu": "unread",
      "m/done": "just_finished",
      "m/away": "unread",
    });
    expect(getShownThisVisitForTest().size).toBe(0);

    releaseSeen(["m/menu"]);
    expect(getSeenVisit().seen["m/menu"]).toBeUndefined();
    expect(Object.keys(getSeenVisit().seen)).toHaveLength(3);

    releaseAllSeen();
    expect(getSeenVisit().seen).toEqual({});
  });

  it("releases a currently open held row immediately on Clear seen and keeps it released on updates", () => {
    const sock = online([session("open", "unread")]);
    noteShownUnseen({ "m/open": "unread" });
    const close = open("m", "open");
    expect(getSeenVisit().seen["m/open"]).toBe("unread");
    expect(getSeenVisit().open["m/open"]).toBe("unread");
    expect(getSeenVisit().snapshots["m/open"]).toBeDefined();

    releaseSeen(["m/open"]);
    expect(getSeenVisit().seen["m/open"]).toBeUndefined();
    expect(getSeenVisit().open["m/open"]).toBeUndefined();
    expect(getSeenVisit().snapshots["m/open"]).toBeUndefined();
    expect(heldOrderFor("stay", getSeenVisit()).heldSeen?.["m/open"]).toBeUndefined();

    // Subsequent store updates while the chat remains open do not re-hold the cleared row.
    sock.push({ type: "sessions_delta", machine_id: "m", upsert: [session("open", "idle")], remove: [] });
    expect(getSeenVisit().open["m/open"]).toBeUndefined();
    expect(getSeenVisit().seen["m/open"]).toBeUndefined();
    expect(heldOrderFor("stay", getSeenVisit()).heldSeen?.["m/open"]).toBeUndefined();
    close();
  });

  it("does not hold a chat that finished while already open once it closes", () => {
    const sock = online([session("chat", "working")]);
    const close = open("m", "chat");
    sock.push({ type: "sessions_delta", machine_id: "m", upsert: [session("chat", "unread")], remove: [] });
    expect(getSeenVisit().open).toEqual({ "m/chat": "unread" });
    expect(getSeenVisit().seen).toEqual({});
    close();
    expect(getSeenVisit().open).toEqual({});
    expect(getSeenVisit().seen).toEqual({});
  });

  it("holds a noted row seen meanwhile and updates place when a held row finishes again elsewhere", () => {
    const late = session("late", "idle");
    const again = session("again", "unread");
    online([late, again]);
    noteShownUnseen({ "m/late": "unread" }, [late, again]);
    expect(getSeenVisit().seen).toEqual({ "m/late": "unread" });
    markSeenLocally("m", "again");
    expect(getSeenVisit().seen["m/again"]).toBe("unread");

    const againDone = session("again", "idle", { work: { phase: "done", just_finished: true } });
    FakeSocket.last.push({ type: "sessions", machine_id: "m", sessions: [late, againDone] });
    noteShownUnseen({ "m/again": "just_finished" }, [late, againDone]);
    expect(getSeenVisit().seen["m/again"]).toBe("just_finished");
  });

  it("Mark all as seen lets both newly marked and already-held rows go at once", () => {
    const rows = [session("a", "unread"), session("b", "unread"), session("c", "unread")];
    online(rows);
    noteShownUnseen({ "m/a": "unread", "m/b": "unread", "m/c": "unread" });
    markSeenLocally("m", "a");
    expect(getSeenVisit().seen).toEqual({ "m/a": "unread" });
    markSeen([getSession("m", "a")!, getSession("m", "b")!]);
    expect(getSeenVisit().seen).toEqual({});
    expect([...getShownThisVisitForTest().entries()]).toEqual([["m/c", "unread"]]);
    expect(activityOf(getSession("m", "b")!)).toBe("idle");
  });

  it("leave mode holds only the open row and releases it when the chat closes", () => {
    setSeenRowsMode("leave");
    expect(seenRowsMode()).toBe("leave");
    expect(seenRowsNoticed()).toBe(true);
    online([session("open", "unread"), session("other", "unread")]);
    noteShownUnseen({ "m/open": "unread", "m/other": "unread" });
    const close = open("m", "open");
    expect(heldOrderFor("leave", getSeenVisit())).toEqual({ heldSeen: { "m/open": "unread" } });
    expect(getSeenVisit().seen).toEqual({});
    close();
    expect(heldOrderFor("leave", getSeenVisit())).toEqual({});
    // Switching back to stay keeps noticed true.
    setSeenRowsMode("stay");
    expect(seenRowsMode()).toBe("stay");
    expect(seenRowsNoticed()).toBe(true);
    setSeenRowsNoticed(false);
    expect(seenRowsNoticed()).toBe(false);
  });

  it("refresh starts the next visit with rows still unseen and reset clears everything", () => {
    online([session("read", "unread"), session("next", "unread")]);
    noteShownUnseen({ "m/read": "unread", "m/next": "unread" });
    markSeenLocally("m", "read");
    releaseAllSeen({ "m/next": "unread" });
    expect(getSeenVisit().seen).toEqual({});
    expect([...getShownThisVisitForTest().entries()]).toEqual([["m/next", "unread"]]);
    const close = open("m", "next");
    close();
    expect(getSeenVisit().seen).toEqual({ "m/next": "unread" });
    reset();
    expect(getSeenVisit().seen).toEqual({});
    expect(getShownThisVisitForTest().size).toBe(0);
  });
});
