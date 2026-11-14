import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/minutes-overview/cases.json";
import { supportsIcon } from "../ui/icons";
import { boldParts, minutesOverview, TOPIC_GLYPH, TOPIC_ICONS } from "./minutes-overview";

// docs/assistant/meeting-minutes.md "Summary card (#6563)", "Clients": the page shows the summary card only when a
// bullet is left after this cleaning; anything else shows the minutes as before.
const bullet = (text: string, extra: Record<string, unknown> = {}) => ({ text, at: 12, ...extra });
const one = (bullets: unknown[], extra: Record<string, unknown> = {}) => ({ groups: [{ title: "", topics: [{ title: "Export", icon: "progress", bullets }] }], ...extra });

describe("minutesOverview", () => {
  it.each([
    ["missing", undefined],
    ["null", null],
    ["a string", "x"],
    ["a number", 3],
    ["an array", [{ groups: [] }]],
    ["an empty object", {}],
    ["groups not an array", { groups: "x" }],
    ["groups an object", { groups: { title: "A" } }],
    ["no groups", { groups: [] }],
    ["groups that are not objects", { groups: ["x", 1, null, []] }],
    ["a group without an array of topics", { groups: [{ title: "A", topics: "x" }] }],
    ["a topic without an array of bullets", { groups: [{ topics: [{ title: "Export", bullets: { text: "x" } }] }] }],
    ["a topic without a title", { groups: [{ topics: [{ bullets: [bullet("Kept")] }] }] }],
    ["a topic with a blank title", { groups: [{ topics: [{ title: "  ", bullets: [bullet("Kept")] }] }] }],
    ["a topic with a numeric title", { groups: [{ topics: [{ title: 3, bullets: [bullet("Kept")] }] }] }],
    ["bullets without text", one([{ at: 3 }, { text: "", at: 3 }, { text: "   ", at: 3 }, { text: 5, at: 3 }, "words", null])],
  ])("is none for %s", (_, v) => {
    expect(minutesOverview(v)).toBeNull();
  });

  it("keeps a usable overview as it is", () => {
    const o = minutesOverview({
      headline: "Export ships with the cap",
      conclusions: [{ text: "Export ships with the 4 MB cap", strong: ["4 MB"], at: 1504.5 }, { text: "A photo's word", at: 20, photo: 2 }],
      groups: [
        { title: "Project progress", topics: [{ title: "Export", icon: "progress", bullets: [
          { text: "Export drops entries over 4 MB", strong: ["4 MB"], at: 312, who: "Ada" },
          { text: "Export keeps the cap", strong: ["keeps the cap"], at: 1504.5, decision: true },
        ] }] },
        { title: "Tooling", topics: [{ title: "Cache bug", icon: "quality", bullets: [{ text: "It reproduced once", strong: ["once"], at: 0, photo: 2 }] }] },
      ],
    });
    expect(o).toEqual({
      headline: "Export ships with the cap",
      conclusions: [{ text: "Export ships with the 4 MB cap", strong: ["4 MB"], at: 1504.5 }, { text: "A photo's word", strong: [], at: 20, photo: 2 }],
      groups: [
        { title: "Project progress", topics: [{ title: "Export", icon: "progress", bullets: [
          { text: "Export drops entries over 4 MB", strong: ["4 MB"], at: 312, who: "Ada", decision: false },
          { text: "Export keeps the cap", strong: ["keeps the cap"], at: 1504.5, decision: true },
        ] }] },
        { title: "Tooling", topics: [{ title: "Cache bug", icon: "quality", bullets: [{ text: "It reproduced once", strong: ["once"], at: 0, photo: 2, decision: false }] }] },
      ],
    });
  });

  it("reads wrong-typed optional fields as absent and drops invalid entries", () => {
    const o = minutesOverview({
      headline: 3,
      conclusions: { text: "not a list" },
      groups: [{ title: 7, topics: [{ title: "Export", icon: 4, bullets: [
        { text: "Who is a number", who: 3, strong: "Who", at: -1, photo: 0, decision: "true" },
        { text: "Blank who", who: "  ", strong: [1, "", null, "Blank"], at: "12", photo: 2.5, decision: 1 },
        { text: "No at", at: null, photo: "2" },
        { text: "Infinity is no number", at: Number.POSITIVE_INFINITY, photo: -3 },
      ] }] }],
    })!;
    expect(o.headline).toBeUndefined();
    expect(o.conclusions).toEqual([]);
    expect(o.groups[0].title).toBe("");
    expect(o.groups[0].topics[0].icon).toBe("general");
    expect(o.groups[0].topics[0].bullets).toEqual([
      { text: "Who is a number", strong: [], decision: false },
      { text: "Blank who", strong: ["Blank"], decision: false },
      { text: "No at", strong: [], decision: false },
      { text: "Infinity is no number", strong: [], decision: false },
    ]);
  });

  it("drops non-object members, empty topics and empty groups, and keeps the rest in order", () => {
    const o = minutesOverview({
      headline: "  ",
      conclusions: ["x", null, { text: "" }, { text: "Kept", at: 5 }, { at: 3 }],
      groups: [
        "x",
        { title: "Empty", topics: [{ title: "Nothing", bullets: [{ text: " " }] }] },
        { title: "A", topics: [null, { title: "One", icon: "rocket", bullets: ["x", bullet("First")] }, { title: "Gone", bullets: [] }] },
        { topics: [{ title: "Two", icon: "risk", bullets: [bullet("Second", { decision: true, who: "Bo" })] }] },
      ],
    })!;
    expect(o.headline).toBeUndefined();
    expect(o.conclusions).toEqual([{ text: "Kept", strong: [], at: 5 }]);
    expect(o.groups.map((g) => [g.title, g.topics.map((t) => [t.title, t.icon, t.bullets.map((b) => b.text)])])).toEqual([
      ["A", [["One", "general", ["First"]]]],
      ["", [["Two", "risk", ["Second"]]]],
    ]);
    // Only the boolean true marks a decision; a decision keeps a who it was given (the page shows "Decision" there).
    expect(o.groups[1].topics[0].bullets[0]).toMatchObject({ decision: true, who: "Bo" });
  });

  it("knows every icon of the contract, each drawn with a glyph the page has", () => {
    expect([...TOPIC_ICONS]).toEqual(["progress", "schedule", "release", "performance", "metrics", "tooling", "quality", "design", "people", "customer", "budget", "risk", "general"]);
    for (const icon of TOPIC_ICONS) expect(supportsIcon(TOPIC_GLYPH[icon]), `${icon} → ${TOPIC_GLYPH[icon]}`).toBe(true);
    const iconOf = (icon: unknown) => minutesOverview({ groups: [{ topics: [{ title: "T", icon, bullets: [bullet("x")] }] }] })!.groups[0].topics[0].icon;
    for (const icon of TOPIC_ICONS) expect(iconOf(icon)).toBe(icon);
    for (const icon of ["rocket", "Progress", "", null, 3, ["progress"]]) expect(iconOf(icon)).toBe("general");
  });
});

describe("boldParts", () => {
  const bold = (text: string, strong: string[]) => boldParts(text, strong).map((p) => (p.bold ? `[${p.text}]` : p.text)).join("");

  it("bolds each entry at its first occurrence after the previous one", () => {
    expect(bold("Load-test QPS raised to 40K", ["40K"])).toBe("Load-test QPS raised to [40K]");
    expect(bold("From 20K to 40K", ["20K", "40K"])).toBe("From [20K] to [40K]");
    // A figure that appears twice: the second entry is looked for after the first.
    expect(bold("40K now, 40K later", ["40K", "40K"])).toBe("[40K] now, [40K] later");
    expect(bold("The cap stays at 4 MB, then the cap grows", ["4 MB", "cap"])).toBe("The cap stays at [4 MB], then the [cap] grows");
  });

  it("skips an entry that is not found, or found only before the cursor, and bolds nothing for none", () => {
    expect(bold("Export keeps the cap", ["missing", "cap"])).toBe("Export keeps the [cap]");
    expect(bold("cap first, then 4 MB", ["4 MB", "cap"])).toBe("cap first, then [4 MB]");
    expect(bold("No marks", [])).toBe("No marks");
    expect(boldParts("No marks", [])).toEqual([{ text: "No marks", bold: false }]);
  });

  it("compares code units exactly: no case folding, no normalization", () => {
    expect(bold("Done and DONE", ["DONE"])).toBe("Done and [DONE]");
    expect(bold("caf\u00e9", ["cafe\u0301"])).toBe("caf\u00e9"); // the same word, composed and decomposed
    expect(bold("任务已完成，测试 40K", ["完成", "40K"])).toBe("任务已[完成]，测试 [40K]"); // i18n-ignore
    expect(bold("emoji 🚀 launch", ["🚀"])).toBe("emoji [🚀] launch");
  });

  it("covers the whole text, nothing lost or repeated", () => {
    const text = "The release moves a week unless the cache bug is fixed by Wednesday";
    const parts = boldParts(text, ["a week", "Wednesday"]);
    expect(parts.map((p) => p.text).join("")).toBe(text);
    expect(parts.filter((p) => p.bold).map((p) => p.text)).toEqual(["a week", "Wednesday"]);
  });
});

// The cases every client checks (spec/fixtures/minutes-overview/cases.json, with the hub and the iPhone): the layout
// each minutes get, and the summary card's lines with their bold segments, citations, speakers and decisions.
describe("the shared minutes-overview fixture", () => {
  type Line = { text: string; strong: string[]; at?: number; photo?: number };
  const line = (l: Line) => ({ segments: boldParts(l.text, l.strong), at: l.at ?? null, photo: l.photo ?? null });
  const shown = (v: unknown) => {
    const o = minutesOverview(v);
    if (!o) return { layout: "legacy", overview: null };
    return {
      layout: "overview",
      overview: {
        headline: o.headline ?? null,
        conclusions: o.conclusions.map(line),
        groups: o.groups.map((g) => ({ title: g.title, topics: g.topics.map((t) => ({
          title: t.title, icon: t.icon, bullets: t.bullets.map((b) => ({ ...line(b), who: b.who ?? null, decision: b.decision })),
        })) })),
      },
    };
  };

  it("has the cases the contract names", () => {
    expect(fixture.version).toBe(1);
    expect(fixture.cases.map((c) => c.name)).toEqual(expect.arrayContaining([
      "legacy_only", "rich", "one_group", "repeated_figure", "unknown_icon", "strong_not_found", "empty_topic",
      "overview_empty_object", "overview_string", "overview_null", "groups_not_array", "nothing_left", "malformed_members",
    ]));
  });

  it.each(fixture.cases.map((c) => [c.name, c] as const))("%s", (_, c) => {
    const minutes = c.input.minutes as Record<string, unknown>;
    const got = shown(minutes.overview);
    expect(got.layout).toBe(c.expected.layout);
    expect(got.overview).toEqual(c.expected.overview ?? null);
  });
});
