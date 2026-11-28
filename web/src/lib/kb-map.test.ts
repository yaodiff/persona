import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import { type KBDetail, type KBEntry } from "./kb";
import { type TopicPoint } from "./kb-points";
import { buildProjectMap, buildTopicMap, layout, parseSummary, personaLine, pointsUnder, walk, type MapNode } from "./kb-map";

beforeAll(async () => {
  await initI18n("en");
});

const entry = (kind: string, id: string, extra: Partial<KBEntry> = {}): KBEntry => ({ kind, id, name: `Example ${id}`, relevance: 0, ...extra });
const point = (id: string, path: string[] = [], kind = "fact"): TopicPoint => ({ id, path, kind, text: `Point ${id}`, sources: [{ kind: "projects", id: "alpha" }] });
const topicDetail = (id: string, points: TopicPoint[]): KBDetail => ({ kind: "topics", id, path: "", meta: { name: `Example ${id}` }, points, links: [entry("projects", "alpha")] });
const titles = (n: MapNode) => n.kids.map((k) => k.title);

describe("parseSummary", () => {
  it("splits sections, nests list items and drops the title and emphasis", () => {
    const md = ["# Release 1.3", "", "## Goal", "Ship the widget in three sizes.", "", "## State", "- **11-08**: code merged", "  - review done", "- 11-09: canary not upgraded", "", "## Next", "1. upgrade the canary", "2. compare five tasks"].join("\n");
    expect(parseSummary(md)).toEqual([
      { title: "Goal", items: [{ text: "Ship the widget in three sizes.", kids: [] }] },
      { title: "State", items: [{ text: "11-08: code merged", kids: [{ text: "review done", kids: [] }] }, { text: "11-09: canary not upgraded", kids: [] }] },
      { title: "Next", items: [{ text: "upgrade the canary", kids: [] }, { text: "compare five tasks", kids: [] }] },
    ]);
  });

  it("puts text before the first section under the lead, and gives nothing for an empty summary", () => {
    expect(parseSummary("Loose line.", "Summary")).toEqual([{ title: "Summary", items: [{ text: "Loose line.", kids: [] }] }]);
    expect(parseSummary("")).toEqual([]);
    expect(parseSummary("# Only a title\n")).toEqual([]);
  });
});

describe("personaLine", () => {
  it("keeps only the main chat's lines and drops a repeated prefix", () => {
    expect(personaLine("- 2026-11-10 15:57 · Persona · 2026-11-10 15:56 · Persona · review passed")).toEqual({ when: "2026-11-10 15:57", text: "review passed" });
    expect(personaLine("- 2026-11-10 09:23 · Agent · title · laptop · done")).toBeNull();
  });
});

describe("buildTopicMap", () => {
  it("makes a branch per topic, biggest first, with subtopics and points from each point's path", () => {
    const small = entry("topics", "small", { extra: "1" }), big = entry("topics", "big", { extra: "3" });
    const root = buildTopicMap([
      { entry: small, detail: topicDetail("small", [point("s1")]) },
      { entry: big, detail: topicDetail("big", [point("b1", ["Storage"]), point("b2", ["Storage", "Index"]), point("b3")]) },
    ]);
    expect(root.view).toBe("topics");
    expect(titles(root)).toEqual(["Example big", "Example small"]);
    const b = root.kids[0];
    expect(b).toMatchObject({ kind: "branch", meta: "3", isTopic: true, open: { kind: "topics", id: "big" }, collapsed: false });
    // a subtopic, then the loose point (one is not enough for an "Other" node)
    expect(b.kids.map((k) => [k.kind, k.title])).toEqual([["topic", "Storage"], ["point", "Point b3"]]);
    const storage = b.kids[0];
    expect(storage).toMatchObject({ collapsed: true, meta: "2" });
    expect(storage.kids.map((k) => [k.kind, k.title])).toEqual([["topic", "Index"], ["point", "Point b1"]]);
    expect(pointsUnder(root)).toHaveLength(4);
    // sources resolve through the topic's links
    expect(pointsUnder(b)[0].links?.map((l) => l.id)).toEqual(["alpha"]);
  });

  it("keeps more than three loose points beside subtopics under one closed Other node", () => {
    const pts = [point("a", ["Sub"]), ...["1", "2", "3", "4"].map((id) => point(id))];
    const root = buildTopicMap([{ entry: entry("topics", "t"), detail: topicDetail("t", pts) }]);
    const kids = root.kids[0].kids;
    expect(kids.map((k) => k.title)).toEqual(["Sub", "Other"]);
    expect(kids[1]).toMatchObject({ kind: "topic", collapsed: true, meta: "4" });
  });

  it("opens only the six biggest topics, balances the sides, and counts from the list when a detail is missing", () => {
    const topics = Array.from({ length: 8 }, (_, i) => ({ entry: entry("topics", `t${i}`, { extra: String(10 - i) }) }));
    const root = buildTopicMap(topics);
    expect(root.kids.map((k) => k.collapsed)).toEqual([false, false, false, false, false, false, true, true]);
    expect(root.kids.map((k) => k.meta)).toEqual(["10", "9", "8", "7", "6", "5", "4", "3"]);
    expect(new Set(root.kids.map((k) => k.side))).toEqual(new Set(["R", "L"]));
  });

  it("on a large base opens the biggest topics only while what they show fits the budget, skipping one that does not", () => {
    // a topic with n subtopics of one point each opens onto n rows of 40 px; the budget is 3,200 px
    const topic = (id: string, n: number) => ({ entry: entry("topics", id), detail: topicDetail(id, Array.from({ length: n }, (_, i) => point(`${id}-${i}`, [`Part ${i}`]))) });
    const root = buildTopicMap([topic("a", 100), topic("b", 50), topic("c", 40), topic("d", 20), topic("e", 5), topic("f", 4), topic("g", 3), topic("h", 2), topic("i", 1)]);
    // a: 4,000 px, too big; b: 2,000; c: 1,600, more than the 1,200 left; d: 800; e, f: 200 and 160; g: 120 is over the 40 left
    expect(root.kids.map((k) => [k.title, !k.collapsed])).toEqual([
      ["Example a", false], ["Example b", true], ["Example c", false], ["Example d", true], ["Example e", true], ["Example f", true],
      ["Example g", false], ["Example h", false], ["Example i", true],
    ]);
    // loose points count as two-line rows (56 px): 58 of them (3,248 px) no longer fit
    const loose = (id: string, n: number) => ({ entry: entry("topics", id), detail: topicDetail(id, Array.from({ length: n }, (_, i) => point(`${id}-${i}`))) });
    expect(buildTopicMap([loose("x", 58)]).kids[0].collapsed).toBe(true);
    expect(buildTopicMap([loose("x", 57)]).kids[0].collapsed).toBe(false);
  });
});

describe("buildProjectMap", () => {
  const summary = ["# Example alpha", "## 目标", "Ship it.", "## 现状", "- merged", "## 下一步", "- canary"].join("\n"); // i18n-ignore: the curated summary's section names
  const projectDetail = (id: string, meta: Record<string, unknown>, extra: Partial<KBDetail> = {}): KBDetail => ({ kind: "projects", id, path: "", meta, ...extra });

  it("groups active projects by importance on the right and done ones on the left, leaving merged ones out", () => {
    const list = [
      entry("projects", "alpha", { importance: "P1", extra: "short · active" }),
      entry("projects", "beta", { extra: "short · active" }),
      entry("projects", "gamma", { extra: "short · done" }),
      entry("projects", "delta", { extra: "short · merged" }),
    ];
    const root = buildProjectMap(list, new Map());
    expect(root.view).toBe("projects");
    expect(root.kids.map((k) => [k.title, k.side, k.meta])).toEqual([["Active", "R", "2"], ["Done", "L", "1"]]);
    expect(titles(root.kids[0])).toEqual(["P1", "Not rated"]);
    const names: string[] = [];
    walk(root, (n) => { if (n.kind === "project") names.push(n.title); });
    expect(names).not.toContain("Example delta");
  });

  it("puts the curated summary's sections under a project, its goal first and open, and the main chat's lines by day", () => {
    const d = projectDetail("alpha", { goal: "One-line goal", status: "active" }, {
      summary,
      log: ["- 2026-11-09 10:00 · Persona · started", "- 2026-11-10 09:00 · Agent · build · laptop · done", "- 2026-11-10 11:00 · Persona · reviewed"],
      links: [entry("chats", "c1"), entry("people", "p1")],
    });
    const root = buildProjectMap([entry("projects", "alpha", { extra: "short · active" })], new Map([["alpha", d]]));
    let p: MapNode | undefined;
    walk(root, (n) => { if (n.kind === "project") p = n; });
    expect(p!.noGoal).toBe(false);
    // the summary's goal replaces the one-line goal; its sections carry the "Summary" mark
    expect(p!.kids.map((k) => k.title)).toEqual(["目标", "现状", "下一步", "Latest", "Main chat notes", "Related chats", "Related people"]); // i18n-ignore
    expect(p!.kids[0]).toMatchObject({ goal: true, fromSummary: true, collapsed: false, icon: "target" });
    expect(p!.kids[2]).toMatchObject({ collapsed: true, icon: "arrowRight" });
    expect(p!.lines?.map((l) => l.text)).toEqual(["reviewed", "started"]);
    expect(p!.kids[4].kids.map((k) => k.title)).toEqual(["11-10", "11-09"]);
    expect(p!.kids[5].kids[0].open).toEqual({ kind: "chats", id: "c1" });
  });

  it("falls back to the main chat's goal, and flags a project without one", () => {
    const root = buildProjectMap([entry("projects", "a"), entry("projects", "b")], new Map([["a", projectDetail("a", { goal: "Ship it" })], ["b", projectDetail("b", {})]]));
    const ps: MapNode[] = [];
    walk(root, (n) => { if (n.kind === "project") ps.push(n); });
    expect(ps[0].kids[0]).toMatchObject({ title: "Goal", goal: true });
    expect(ps.map((x) => x.noGoal)).toEqual([false, true]);
  });

  it("opens the importance groups in order, at most two, and stops at the first whose projects do not fit the budget", () => {
    // a project card is a 70 px row; the budget is 3,200 px
    const groups = (sizes: [string, number][]) => {
      const list = sizes.flatMap(([imp, n]) => Array.from({ length: n }, (_, i) => entry("projects", `${imp}-${i}`, { importance: imp, extra: "short · active" })));
      return buildProjectMap(list, new Map()).kids[0].kids.map((g) => [g.title, !g.collapsed]);
    };
    // a small base as before: the first two open
    expect(groups([["P0", 2], ["P1", 3], ["P2", 1]])).toEqual([["P0", true], ["P1", true], ["P2", false]]);
    // P0 (2,100 px) fits, P1 (1,400 px) is over the 1,100 left: it and every group after it stay closed, P3 included
    expect(groups([["P0", 30], ["P1", 20], ["P3", 1]])).toEqual([["P0", true], ["P1", false], ["P3", false]]);
    // P0 alone over the budget: nothing opens, rather than a less important group
    expect(groups([["P0", 46], ["P1", 1]])).toEqual([["P0", false], ["P1", false]]);
  });
});

describe("layout", () => {
  it("places the root between both sides and every visible node once", () => {
    const root = buildTopicMap([{ entry: entry("topics", "a", { extra: "2" }) }, { entry: entry("topics", "b", { extra: "1" }) }]);
    const L = layout(root, () => ({ w: 100, h: 20, lines: [] }));
    expect(L.placed).toHaveLength(3);
    const [r, a, b] = L.placed;
    expect(r.node).toBe(root);
    expect(a.x > r.x).not.toBe(b.x > r.x); // one on each side
    expect(L.width).toBeGreaterThan(0);
  });
});
