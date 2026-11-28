import { describe, expect, it } from "vitest";
import fixture from "../../../../spec/fixtures/speakers-names/cases.json";
import { initI18n } from "../../i18n";
import { recording } from "./module";
import {
  applyOptimisticSpeakerEdit,
  applySpeakerEdit,
  canonicalSpeakerId,
  decodeSpeakers,
  formatPositionDisplays,
  formatSpeakerStats,
  groupLinesBySpeaker,
  hasVisibleSpeakers,
  resolvePositionDisplays,
  speakerAvatarGlyph,
  speakerCandidateNames,
  speakerLabel,
  speakerPositions,
  speakerSampleLines,
  waitingSpeakerMinutes,
  type DetailSpeakers,
  type PositionAttributionInput,
  type RosterPerson,
  type SpeakerEditOp,
  type SpeakerStatsMap,
} from "./speakers";
import { minutesDebates } from "./minutes-debates";
import { recordingView } from "./recording-view";

// Every case in the shared contract fixture spec/fixtures/speakers-names/cases.json (verified with the hub).
describe("the shared speakers-names fixture", () => {
  const displayCases = fixture.cases.filter((c) => c.kind === "display");
  const editCases = fixture.cases.filter((c) => c.kind === "edit");

  it("loads version 1 with both display and edit cases", () => {
    expect(fixture.version).toBe(1);
    expect(displayCases.length).toBeGreaterThanOrEqual(8);
    expect(editCases.length).toBeGreaterThanOrEqual(10);
  });

  it.each(displayCases.map((c) => [c.name, c] as const))("display: %s", (_, c) => {
    const input = c.input as unknown as {
      people: RosterPerson[];
      position: PositionAttributionInput;
    };
    const expected = c.expected as unknown as { displays: unknown[] };
    expect(resolvePositionDisplays(input.people, input.position, "ok")).toEqual(expected.displays);
  });

  it.each(editCases.map((c) => [c.name, c] as const))("edit: %s", (_, c) => {
    const input = c.input as unknown as {
      next: number;
      people: RosterPerson[];
      stats: SpeakerStatsMap;
      ops: SpeakerEditOp[];
    };
    const expected = c.expected as unknown as {
      people?: RosterPerson[];
      edits?: Record<string, number>;
      undo?: SpeakerEditOp[];
      verify_undo_roundtrip?: boolean;
      error?: string;
      error_code?: string;
    };

    const initial = { next: input.next, people: input.people };
    const result = applySpeakerEdit(initial, input.stats, input.ops);

    if (expected.error) {
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe(expected.error);
        expect(result.error_code).toBe(expected.error_code);
      }
      return;
    }

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.roster.people).toEqual(expected.people);
    expect(result.roster.edits).toEqual(expected.edits);
    expect(result.undo).toEqual(expected.undo);

    if (expected.verify_undo_roundtrip) {
      const undone = applySpeakerEdit(result.roster, input.stats, result.undo);
      expect(undone.ok).toBe(true);
      if (undone.ok) {
        expect(undone.roster.people).toEqual(input.people);
      }
    }
  });
});

describe("decodeSpeakers and defensive helpers", () => {
  it.each([
    ["undefined", undefined],
    ["null", null],
    ["string", "ready"],
    ["array", []],
    ["missing state", { rev: 1 }],
    ["unknown state", { rev: 1, state: "running" }],
  ])("returns null for malformed or unknown block (%s)", (_, raw) => {
    expect(decodeSpeakers(raw)).toBeNull();
  });

  it("decodes ready speakers defensively, filtering bad people/spans/covered and unknown reasons", () => {
    const decoded = decodeSpeakers({
      rev: 5,
      state: "ready",
      quality: "ok",
      source: "hub",
      people: [
        { id: "s1", name: "  Ada  ", seconds: 120.5, lines: 12, samples: [10, -1, 45.2] },
        { id: "s1", name: "Duplicate" },
        { id: "bad", name: "Ignored" },
        { id: "s2", merged_into: "s1", minor: true },
        { id: "s3", merged_into: "s3", minor: true, seconds: 8, lines: 1 },
      ],
      candidates: ["Ada", "  ", "Grace", "Ada"],
      spans: [[0, 10, "s1"], [10, 5, "s1"], [12, 20, "bad"]],
      covered: [[0, 60], [50, 40]],
      trimmed: false,
    });

    expect(decoded).toEqual({
      rev: 5,
      state: "ready",
      reason: "unknown",
      quality: "ok",
      source: "hub",
      people: [
        { id: "s1", name: "Ada", seconds: 120.5, lines: 12, samples: [10, 45.2] },
        { id: "s2", merged_into: "s1" },
        { id: "s3", minor: true, seconds: 8, lines: 1 },
      ],
      candidates: ["Ada", "Grace"],
      spans: [[0, 10, "s1"]],
      covered: [[0, 60]],
      trimmed: false,
    });
    expect(hasVisibleSpeakers(decoded)).toBe(true);
  });

  it("treats cleared, unknown failed reasons and unknown quality as hidden", () => {
    expect(hasVisibleSpeakers(decodeSpeakers({ state: "cleared" }))).toBe(false);
    expect(hasVisibleSpeakers(decodeSpeakers({ state: "failed", reason: "future_reason" }))).toBe(false);
    expect(hasVisibleSpeakers(decodeSpeakers({ state: "failed", reason: "too_long" }))).toBe(true);
    expect(hasVisibleSpeakers(decodeSpeakers({ state: "ready", quality: "future_quality" }))).toBe(false);
  });

  it("computes waiting minutes and avatar glyphs cleanly", () => {
    expect(waitingSpeakerMinutes(undefined, 600)).toBeNull();
    expect(waitingSpeakerMinutes(125, undefined)).toEqual({ done: 2 });
    expect(waitingSpeakerMinutes(125, 600)).toEqual({ done: 2, total: 10 });
    expect(waitingSpeakerMinutes(700, 600)).toEqual({ done: 10, total: 10 });
    expect(speakerAvatarGlyph({ id: "s1", name: "Ada" })).toBe("A");
    expect(speakerAvatarGlyph({ id: "s3" })).toBe("3");
    expect(speakerLabel({ id: "s2", name: "Grace" }, (n) => `Speaker ${n}`)).toBe("Grace");
    expect(speakerLabel({ id: "s2" }, (n) => `Speaker ${n}`)).toBe("Speaker 2");
    expect(canonicalSpeakerId([{ id: "s1", merged_into: "s2" }, { id: "s2", merged_into: "s1" }], "s1")).toBe("s1");
  });
});

describe("transcript grouping, low quality, optimistic edits and dialog helpers", () => {
  const ready: DetailSpeakers = {
    rev: 2,
    state: "ready",
    reason: "unknown",
    quality: "ok",
    people: [
      { id: "s1", name: "Ada", seconds: 300, lines: 10, samples: [10, 40] },
      { id: "s2", seconds: 90, lines: 4, samples: [20] },
      { id: "s3", merged_into: "s1" },
      { id: "s4", minor: true, seconds: 8, lines: 1 },
    ],
    candidates: ["Ada", "Grace", "Bo"],
    spans: [],
    covered: [],
    trimmed: false,
  };

  it("groups consecutive lines by canonical speaker and certainty while leaving bookmarks plain", () => {
    const rows = [
      { id: "l1", speaker: "s1", speakerUnsure: false },
      { id: "l2", speaker: "s3", speakerUnsure: false },
      { id: "l3", speaker: "s1", speakerUnsure: true },
      { id: "b1", bookmark: true },
      { id: "l4", speaker: "s4", speakerUnsure: false },
      { id: "l5" },
    ];
    const groups = groupLinesBySpeaker(rows, ready);
    expect(groups.map((g) => (g.kind === "speaker" ? [g.speakerId, g.unsure, g.rows.length] : ["plain", g.rows.length]))).toEqual([
      ["s1", false, 2],
      ["s1", true, 1],
      ["plain", 1],
      ["s4", true, 1],
      ["plain", 1],
    ]);

    const lowGroups = groupLinesBySpeaker(rows.slice(0, 2), { ...ready, quality: "low" });
    expect(lowGroups).toHaveLength(1);
    expect(lowGroups[0]?.kind === "speaker" && lowGroups[0].unsure).toBe(true);
  });

  it("suppresses speaker attribution on low quality while keeping transcript who fallback", () => {
    expect(
      resolvePositionDisplays(ready.people, { speaker: "s1", speaker_unsure: false, who: "Grace" }, "low"),
    ).toEqual([{ kind: "who", value: "Grace" }]);
    expect(
      resolvePositionDisplays(ready.people, { speaker: "s1", speaker_unsure: false }, "low"),
    ).toEqual([]);
  });

  it("applies optimistic edits (name, auto-merge, clear) and builds candidates, samples and positions", async () => {
    await initI18n("en", recording.locales!);

    const merged = applyOptimisticSpeakerEdit(ready, [{ op: "name", id: "s2", name: "Ada" }]);
    expect(merged).not.toBeNull();
    expect(merged!.speakers.people.find((p) => p.id === "s2")?.merged_into).toBe("s1");
    expect(merged!.speakers.people.find((p) => p.id === "s1")?.seconds).toBe(390);
    expect(merged!.speakers.people.find((p) => p.id === "s1")?.lines).toBe(14);

    const cleared = applyOptimisticSpeakerEdit(ready, [{ op: "clear" }]);
    expect(cleared?.speakers.state).toBe("cleared");
    expect(cleared?.speakers.people).toEqual([]);

    expect(speakerCandidateNames(ready.people[1]!, ready)).toEqual(["Ada", "Grace", "Bo"]);
    expect(speakerCandidateNames(ready.people[0]!, ready)).toEqual(["Grace", "Bo"]);

    const rec = recordingView("recordings", { started_at: "2026-11-10T14:00:00+08:00" }, [
      { start: 10, end: 18, text: "First line", speaker: "s1" },
      { start: 20, end: 28, text: "Second line", speaker: "s2" },
      { start: 40, end: 48, text: "Third line", speaker: "s1" },
    ])!;
    expect(speakerSampleLines(ready.people[0]!, rec.lines).map((s) => s.text)).toEqual(["First line", "Third line"]);

    const debates = minutesDebates(
      [
        {
          question: "Ship cap?",
          status: "open",
          positions: [
            { stance: "Per item", speaker: "s3", at: 10 },
            { stance: "Per recording", speaker: "s2", also: [{ speaker: "s1" }], at: 20 },
          ],
        },
      ],
      0,
    );
    expect(speakerPositions(debates, ready.people, "s1", "ok").map((p) => p.stance)).toEqual([
      "Per item",
      "Per recording",
    ]);
    expect(speakerPositions(debates, ready.people, "s1", "low")).toEqual([]);

    expect(formatPositionDisplays([{ kind: "name", value: "Ada" }, { kind: "numbered", number: 2 }])).toBe(
      "Ada, Speaker 2",
    );
    expect(formatSpeakerStats(720.5, 34)).toBe("spoke 12 min · 34 lines");
    expect(formatSpeakerStats(12, 1)).toBe("spoke 12 s · 1 line");

    await initI18n("zh-Hans", recording.locales!);
    expect(formatPositionDisplays([{ kind: "name", value: "Ada" }, { kind: "numbered", number: 2 }])).toBe(
      "Ada、发言人 2",
    );
    await initI18n("ja", recording.locales!);
    expect(formatPositionDisplays([{ kind: "name", value: "Ada" }, { kind: "numbered", number: 2 }])).toBe(
      "Ada、話者 2",
    );
  });
});
