import { describe, expect, it } from "vitest";
import { planStretches, splitStretches, stretchComplete, stretchWindows, type StretchSource } from "./engine-stretches";
import { engineSwitchHome, engineSwitchOf, liveStretch, stretchesKey } from "./engine-switch";
import { parseUserText } from "./turns";
import type { EngineLeg, Item, Session } from "./types";

/** The block as the node writes it (handoffBlock): Go's html.EscapeString on the attributes, the text as it is. */
const block = (attrs: string, text: string, words = "Carry on with the tests.") =>
  `<persona-context kind="engine-switch" title="Handoff" ${attrs}>\n${text}\n</persona-context>\n\n${words}`;

const switchOf = (raw: string) => {
  const p = parseUserText(raw);
  return { sw: p.contexts.map(engineSwitchOf).find(Boolean), text: p.text };
};

describe("engineSwitchOf", () => {
  it("reads the engines, the model, resumed and the handoff off the block", () => {
    const { sw, text } = switchOf(block('from="claude" to="codex" model="gpt-5.5" resumed="1"', "## Conversation so far\nThe user asked for a login page."));
    expect(sw).toEqual({ from: "claude", to: "codex", model: "gpt-5.5", resumed: true, text: "## Conversation so far\nThe user asked for a login page." });
    expect(text).toBe("Carry on with the tests.");
  });

  it("leaves the model out when the block names none, and is not resumed without resumed=\"1\"", () => {
    const { sw } = switchOf(block('from="codex" to="claude"', "summary"));
    expect(sw).toEqual({ from: "codex", to: "claude", model: undefined, resumed: false, text: "summary" });
    expect(switchOf(block('from="codex" to="claude" resumed="0"', "summary")).sw?.resumed).toBe(false);
  });

  it("unescapes the attributes as Go escapes them, numeric entities too, in one pass", () => {
    // html.EscapeString: & < > " ' as &amp; &lt; &gt; &#34; &#39;
    const { sw } = switchOf(block('from="claude" to="codex" model="vendor/&#34;fast&#34; &amp; &#39;cheap&#39; &lt;1&gt;"', "x"));
    expect(sw?.model).toBe(`vendor/"fast" & 'cheap' <1>`);
    // an escaped entity stays itself: &amp;quot; is the text &quot;
    expect(switchOf(block('from="claude" to="codex" model="a&amp;quot;b"', "x")).sw?.model).toBe("a&quot;b");
    expect(switchOf(block('from="claude" to="codex" model="&#x41;&quot;"', "x")).sw?.model).toBe('A"');
  });

  it("is nothing unless it moves from one known engine to the other: a pasted look-alike stays a context", () => {
    expect(switchOf(block('from="claude" to="claude"', "x")).sw).toBeUndefined();
    expect(switchOf(block('from="claude" to="gemini"', "x")).sw).toBeUndefined();
    expect(switchOf(block('to="codex"', "x")).sw).toBeUndefined();
    expect(switchOf(block('from="Claude" to="codex"', "x")).sw).toBeUndefined();
    expect(switchOf(block('from=" codex " to="claude"', "x")).sw).toMatchObject({ from: "codex", to: "claude" });
    expect(switchOf(block('from="claude" to="agy" model="gemini-4-neon-high"', "x")).sw).toEqual({
      from: "claude",
      to: "agy",
      model: "gemini-4-neon-high",
      resumed: false,
      text: "x",
    });
    expect(switchOf(block('from="agy" to="claude" resumed="1"', "x")).sw).toEqual({
      from: "agy",
      to: "claude",
      model: undefined,
      resumed: true,
      text: "x",
    });
    expect(switchOf(block('from="agy" to="agy"', "x")).sw).toBeUndefined();
  });

  it("is nothing for any other block", () => {
    expect(engineSwitchOf({ kind: "context", text: "x" })).toBeUndefined();
    expect(parseUserText('<persona-context kind="forward" title="t">x</persona-context>hi').contexts.map(engineSwitchOf)).toEqual([undefined]);
  });
});

const T = (m: number) => new Date(Date.UTC(2026, 10, 12, 9, m)).toISOString();
let seq = 0;
const it_ = (sid: string, m: number, extra: Partial<Item> = {}): Item => ({ id: `${sid}-${++seq}#0`, seq, role: "user", kind: "text", text: `${sid} at ${m}`, ts: T(m), ...extra });
const legs = (...l: [string, string, number][]): EngineLeg[] => l.map(([session_id, agent, m]) => ({ session_id, agent, until: T(m) }));
const src = (items: Item[], hasMore = false, ready = true): StretchSource => ({ items, hasMore, ready });

describe("stretchesKey", () => {
  it("is empty for a history that names no session", () => {
    expect(stretchesKey(undefined)).toBe("");
    expect(stretchesKey([{ session_id: "", agent: "claude", until: T(1) }])).toBe("");
  });
});

describe("stretchWindows", () => {
  it("is nothing for a conversation that never switched", () => {
    expect(stretchWindows(undefined, "a")).toEqual([]);
    expect(stretchWindows([], "a")).toEqual([]);
  });

  it("gives each leg (until[i-1], until[i]] and the live session what comes after the last", () => {
    const w = stretchWindows(legs(["a", "claude", 10], ["b", "codex", 20]), "a");
    expect(w.map((x) => [x.sid, x.after, x.until, x.live])).toEqual([
      ["a", undefined, Date.parse(T(10)), false],
      ["b", Date.parse(T(10)), Date.parse(T(20)), false],
      ["a", Date.parse(T(20)), undefined, true],
    ]);
  });

  it("keys the history by its sessions and times only", () => {
    expect(stretchesKey(legs(["a", "claude", 10]))).toBe(stretchesKey(legs(["a", "claude", 10])));
    expect(stretchesKey(legs(["a", "claude", 10]))).not.toBe(stretchesKey(legs(["a", "claude", 11])));
  });
});

describe("splitStretches", () => {
  it("keeps every item of a session that ran one stretch in it, whatever its time", () => {
    const w = stretchWindows(legs(["a", "claude", 10]), "b");
    const items = [it_("b", 9), it_("b", 30)]; // the clocks may disagree a little: nothing is lost
    expect(splitStretches(items, w, "b").get(1)).toEqual(items);
    expect([...splitStretches(items, w, "a").keys()]).toEqual([0]);
  });

  it("splits a session that ran several stretches by their times (A → B → A)", () => {
    const w = stretchWindows(legs(["a", "claude", 10], ["b", "codex", 20]), "a");
    const early = [it_("a", 1), it_("a", 10)];
    const untimed = it_("a", 0, { ts: undefined, kind: "fold" });
    const late = [it_("a", 21), untimed, it_("a", 40)];
    const got = splitStretches([...early, ...late], w, "a");
    expect(got.get(0)).toEqual(early); // up to and with until[0]
    expect(got.get(2)).toEqual(late); // after until[1]; the item without a time goes with the one before it
    expect(got.has(1)).toBe(false); // B's stretch is not A's
  });
});

describe("liveStretch", () => {
  it("is nothing when the live session ran only the live stretch, or the conversation never switched", () => {
    expect(liveStretch([it_("b", 12)], legs(["a", "claude", 10]), "b")).toBeUndefined();
    expect(liveStretch([it_("b", 12)], [], "b")).toBeUndefined();
    expect(liveStretch([it_("b", 12)], undefined, "b")).toBeUndefined();
  });

  it("is what splitStretches puts in the live stretch (A → B → A), untimed items and odd times alike", () => {
    const history = legs(["a", "claude", 10], ["b", "codex", 20]);
    const w = stretchWindows(history, "a");
    const cases: Item[][] = [
      [it_("a", 1), it_("a", 10), it_("a", 21), it_("a", 0, { ts: undefined }), it_("a", 40)],
      [it_("a", 0, { ts: undefined }), it_("a", 25), it_("a", 26)], // the first ones go with the first timed one
      [it_("a", 0, { ts: undefined }), it_("a", 0, { ts: undefined })], // no time at all: the first stretch's
      [it_("a", 5), it_("a", 15), it_("a", 20), it_("a", 21)], // a clock between the switches: not the live one's
    ];
    for (const items of cases) expect(liveStretch(items, history, "a")).toEqual(splitStretches(items, w, "a").get(2));
  });
});

describe("planStretches", () => {
  const ab = stretchWindows(legs(["a", "claude", 10]), "b");

  it("shows nothing earlier and loads the live session's pages while it has older ones (A → B)", () => {
    const plan = planStretches(ab, (s) => (s === "b" ? src([it_("b", 12)], true) : undefined));
    expect(plan).toEqual({ shown: [], next: 1 });
  });

  it("waits for the live session's first page", () => {
    expect(planStretches(ab, (s) => (s === "b" ? src([], false, false) : undefined))).toEqual({ shown: [], next: 1 });
  });

  it("goes on to the earlier stretch only once the live one is whole, then follows its pages", () => {
    const live = src([it_("b", 12)]);
    expect(planStretches(ab, (s) => (s === "b" ? live : undefined))).toEqual({ shown: [0], next: 0 });
    expect(planStretches(ab, (s) => (s === "b" ? live : src([it_("a", 5)], true)))).toEqual({ shown: [0], next: 0 });
    expect(planStretches(ab, (s) => (s === "b" ? live : src([it_("a", 5)])))).toEqual({ shown: [0] });
  });

  it("A → B → A: the live session's early stretch waits until B's is whole, and loads from the live transcript then", () => {
    const aba = stretchWindows(legs(["a", "claude", 10], ["b", "codex", 20]), "a");
    // the live transcript reaches back into A's first stretch: its own stretch is whole, B is next
    const a = src([it_("a", 8), it_("a", 25)], true);
    expect(planStretches(aba, (s) => (s === "a" ? a : undefined))).toEqual({ shown: [1], next: 1 });
    // still within the live stretch: it loads first
    expect(planStretches(aba, (s) => (s === "a" ? src([it_("a", 25)], true) : undefined))).toEqual({ shown: [], next: 2 });
    // B whole: A's first stretch shows what is loaded and is next (A has older pages)
    const b = src([it_("b", 12), it_("b", 19)]);
    expect(planStretches(aba, (s) => (s === "a" ? a : b))).toEqual({ shown: [0, 1], next: 0 });
    expect(planStretches(aba, (s) => (s === "a" ? src([...a.items]) : b))).toEqual({ shown: [0, 1] });
  });

  it("calls a stretch whole when its session has nothing older, or (an earlier stretch of it after) reaches past its start", () => {
    const aba = stretchWindows(legs(["a", "claude", 10], ["b", "codex", 20]), "a");
    expect(stretchComplete(aba[2], aba, src([it_("a", 21)], true))).toBe(false);
    expect(stretchComplete(aba[2], aba, src([it_("a", 20), it_("a", 21)], true))).toBe(true);
    expect(stretchComplete(aba[0], aba, src([it_("a", 1)], true))).toBe(false); // nothing before the first: only no older page ends it
    expect(stretchComplete(aba[0], aba, undefined)).toBe(false);
  });
});

describe("engineSwitchHome", () => {
  const row = (session_id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "m", session_id, title: session_id, cwd: "/w", status: "idle", state: "idle", ...extra });

  it("sends an earlier session to the row its conversation moved on to", () => {
    const rows = [row("b", { engine_history: legs(["a", "claude", 10]) }), row("x")];
    expect(engineSwitchHome(rows, "a")?.session_id).toBe("b");
    expect(engineSwitchHome(rows, "x")).toBeUndefined();
    expect(engineSwitchHome(rows, "nope")).toBeUndefined();
  });

  it("leaves a listed session and the live one in its own history (A → B → A) where they are", () => {
    const rows = [row("a", { engine_history: legs(["a", "claude", 10], ["b", "codex", 20]) })];
    expect(engineSwitchHome(rows, "a")).toBeUndefined();
    expect(engineSwitchHome(rows, "b")?.session_id).toBe("a");
    expect(engineSwitchHome([...rows, row("b")], "b")).toBeUndefined(); // listed on its own (an older node): it is its own
  });
});
