import { describe, expect, it } from "vitest";
import fixture from "../../../../spec/fixtures/minutes-debates/cases.json";
import { decisionAnchor, minutesDebates, overviewDecisionTargets } from "./minutes-debates";
import { minutesOverview } from "./minutes-overview";

// docs/assistant/meeting-minutes.md "Disagreements (#16728)", "Clients": `debates` is cleaned on its own;
// when nothing survives, no section is shown and the minutes look identical to minutes without `debates`.
describe("minutesDebates", () => {
  it.each([
    ["missing", undefined],
    ["null", null],
    ["a string", "none"],
    ["a number", 3],
    ["a boolean", true],
    ["an object", { question: "Not a list" }],
    ["an empty array", []],
    ["non-object members", ["x", 1, null, true, []]],
    ["a blank question", [{ question: "   ", status: "open", positions: [{ stance: "A", at: 1 }, { stance: "B", at: 2 }] }]],
    ["non-array positions", [{ question: "Q?", positions: { stance: "A" } }]],
    ["fewer than two valid positions", [{ question: "Q?", positions: [{ stance: "Only one", at: 1 }, { stance: "  ", at: 2 }, null, "bad"] }]],
  ])("is empty for %s", (_, v) => {
    expect(minutesDebates(v, 2)).toEqual([]);
  });

  it("keeps valid settled and open disagreements and enforces decision bounds", () => {
    const out = minutesDebates(
      [
        {
          question: "Should the export cap count items or recordings?",
          topic: "Export",
          status: "settled",
          at: 312,
          positions: [
            { stance: "Per item", at: 312, who: "Ada" },
            { stance: "Per recording", at: 340.5 },
          ],
          outcome: { text: "Per item for this release", at: 1504.5, decision: 0 },
        },
        {
          question: "Should the weekly digest default to on?",
          status: "open",
          at: 1800,
          positions: [
            { stance: "Default on", at: 1800, who: "Ada" },
            { stance: "Default off", at: 1860, who: "Bo" },
          ],
        },
      ],
      1,
    );
    expect(out).toEqual([
      {
        question: "Should the export cap count items or recordings?",
        topic: "Export",
        status: "settled",
        at: 312,
        positions: [
          { stance: "Per item", at: 312, who: "Ada" },
          { stance: "Per recording", at: 340.5 },
        ],
        outcome: { text: "Per item for this release", at: 1504.5, decision: 0 },
      },
      {
        question: "Should the weekly digest default to on?",
        status: "open",
        at: 1800,
        positions: [
          { stance: "Default on", at: 1800, who: "Ada" },
          { stance: "Default off", at: 1860, who: "Bo" },
        ],
      },
    ]);
  });

  it("decodes unknown or missing status as unknown, ignores outcome when not settled, and strips unknown keys", () => {
    const out = minutesDebates(
      [
        {
          question: "Escalate to security review?",
          status: "escalated",
          future_key: "ignored",
          at: Number.NaN,
          positions: [
            { stance: "Escalate", at: -1, who: "  ", extra: true },
            { stance: "Ship behind flag", at: Number.POSITIVE_INFINITY, who: 99 },
          ],
          outcome: { text: "Ignored because status is unknown", at: 10, decision: 0 },
        },
        {
          question: "Missing status?",
          positions: [
            { stance: "Side A", at: 0 },
            { stance: "Side B", at: 10 },
          ],
        },
      ],
      1,
    );
    expect(out).toEqual([
      {
        question: "Escalate to security review?",
        status: "unknown",
        positions: [{ stance: "Escalate" }, { stance: "Ship behind flag" }],
      },
      {
        question: "Missing status?",
        status: "unknown",
        positions: [
          { stance: "Side A", at: 0 },
          { stance: "Side B", at: 10 },
        ],
      },
    ]);
  });

  it("validates outcome.decision strictly as an in-bounds non-negative integer", () => {
    const make = (decision: unknown) =>
      minutesDebates(
        [
          {
            question: "Pick one?",
            status: "settled",
            positions: [{ stance: "A" }, { stance: "B" }],
            outcome: { text: "Picked A", at: 10, decision },
          },
        ],
        2,
      )[0].outcome;
    expect(make(0)).toEqual({ text: "Picked A", at: 10, decision: 0 });
    expect(make(1.0)).toEqual({ text: "Picked A", at: 10, decision: 1 });
    for (const bad of [-1, 2, 0.5, true, false, "0", null, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(make(bad), String(bad)).toEqual({ text: "Picked A", at: 10 });
    }
    // No decisions at all: even 0 is out of bounds.
    expect(
      minutesDebates(
        [{ question: "Pick one?", status: "settled", positions: [{ stance: "A" }, { stance: "B" }], outcome: { text: "Picked A", decision: 0 } }],
        0,
      )[0].outcome,
    ).toEqual({ text: "Picked A" });
  });

  it("preserves valid speaker, speaker_unsure and also attribution fields and drops malformed ones", () => {
    const out = minutesDebates(
      [
        {
          question: "Ship per item?",
          status: "open",
          positions: [
            {
              stance: "Per item",
              at: 10,
              speaker: "s1",
              speaker_unsure: false,
              also: [
                { speaker: "s3", at: 42 },
                { speaker: "s4", speaker_unsure: true },
                { speaker: "bad_id" },
                null,
              ],
            },
            {
              stance: "Per recording",
              at: 20,
              speaker: "s2",
              speaker_unsure: true,
              who: "Grace",
              also: "not-an-array",
            },
          ],
        },
      ],
      0,
    );
    expect(out[0]?.positions).toEqual([
      {
        stance: "Per item",
        at: 10,
        speaker: "s1",
        also: [
          { speaker: "s3", at: 42 },
          { speaker: "s4", speaker_unsure: true },
        ],
      },
      {
        stance: "Per recording",
        at: 20,
        speaker: "s2",
        speaker_unsure: true,
        who: "Grace",
      },
    ]);
  });
});

describe("overviewDecisionTargets", () => {
  it("maps each decision index to its decision bullet in the overview", () => {
    const o = minutesOverview({
      groups: [
        {
          title: "Progress",
          topics: [
            {
              title: "Export",
              icon: "progress",
              bullets: [
                { text: "Cap exports per item", at: 100 }, // a point with the same text, never matched
                { text: "Cap exports per item", at: 200, decision: true },
              ],
            },
            {
              title: "Schedule",
              icon: "schedule",
              bullets: [
                { text: "Line 1", at: 210 },
                { text: "Line 2", at: 220 },
                { text: "Line 3", at: 230 },
                { text: "Line 4", at: 240 },
                { text: "Move release a week", at: 300, decision: true },
              ],
            },
          ],
        },
      ],
    })!;
    const targets = overviewDecisionTargets(o, [
      { text: "Cap exports per item", at: 200 },
      { text: "Move release a week", at: 300 },
    ]);
    expect(targets.get(0)).toEqual({ anchor: decisionAnchor(0), groupIndex: 0, topicIndex: 0, bulletIndex: 1 });
    expect(targets.get(1)).toEqual({ anchor: decisionAnchor(1), groupIndex: 0, topicIndex: 1, bulletIndex: 4 });
  });
});

// The cases every client checks (spec/fixtures/minutes-debates/cases.json, with the hub, mock hub and iPhone).
describe("the shared minutes-debates fixture", () => {
  const toFixtureShape = (raw: unknown, decisionsCount: number) =>
    minutesDebates(raw, decisionsCount).map((d) => ({
      question: d.question,
      topic: d.topic ?? null,
      status: d.status === "unknown" ? null : d.status,
      at: d.at ?? null,
      positions: d.positions.map((p) => ({
        stance: p.stance,
        at: p.at ?? null,
        who: p.who ?? null,
      })),
      outcome: d.outcome
        ? {
            text: d.outcome.text,
            at: d.outcome.at ?? null,
            decision: d.outcome.decision ?? null,
          }
        : null,
    }));

  it("has the cases the contract names", () => {
    expect(fixture.version).toBe(1);
    expect(fixture.cases.map((c) => c.name)).toEqual(
      expect.arrayContaining([
        "overview_with_debates",
        "legacy_with_debates_en",
        "legacy_with_debates_zh",
        "legacy_with_debates_ja",
        "legacy_no_debates_zh",
        "legacy_no_debates_ja",
        "empty_debates_overview",
        "empty_debates_legacy",
        "cleaned_to_empty_overview",
        "debates_not_array",
        "debates_null",
        "debates_object",
        "unknown_status_and_extra_keys",
        "malformed_members",
        "overview_star_escape",
      ]),
    );
  });

  it.each(fixture.cases.map((c) => [c.name, c] as const))("%s", (_, c) => {
    const minutes = c.input.minutes as Record<string, unknown>;
    const decisionsCount = Array.isArray(minutes.decisions) ? minutes.decisions.length : 0;
    expect(toFixtureShape(minutes.debates, decisionsCount)).toEqual(c.expected.debates);
  });
});
