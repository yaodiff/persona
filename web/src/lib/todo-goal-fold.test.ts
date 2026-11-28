import { describe, expect, it } from "vitest";
import { foldGoalTasks, isChildBlocker, isChildDone, nestSectionHierarchy } from "./todo-goal-fold";
import { TRIAGE_SECTIONS, type Triage, type TriageGroup, type TriageResult, type TriageSection } from "./todo-triage";
import type { Todo } from "./todos";

const NOW = Date.parse("2026-11-24T12:00:00.000Z");

const td = (id: string, patch: Partial<Todo> = {}): Todo => ({
  id,
  title: `Todo ${id}`,
  created_at: "2026-11-24T08:00:00.000Z",
  updated_at: "2026-11-24T11:00:00.000Z",
  ...patch,
});

function buildTriage(entries: Array<{ todo: Todo; section: TriageSection; members?: Todo[] }>): TriageResult {
  const sections = Object.fromEntries(TRIAGE_SECTIONS.map((s) => [s, [] as TriageGroup[]])) as Record<TriageSection, TriageGroup[]>;
  const todoCounts = Object.fromEntries(TRIAGE_SECTIONS.map((s) => [s, 0])) as Record<TriageSection, number>;
  for (const { todo, section, members } of entries) {
    const groupMembers = members ?? [todo];
    const triage = new Map<string, Triage>(
      groupMembers.map((m) => [m.id, { section, rule: section === "waiting" ? "owner_decides" : "rest", last: NOW }]),
    );
    sections[section].push({
      key: todo.id,
      section,
      lead: groupMembers[0],
      members: groupMembers,
      triage,
      last: NOW,
    });
    todoCounts[section] += groupMembers.length;
  }
  return { sections, todoCounts, open: 0, shelved: todoCounts.shelved, finished: todoCounts.done, cancelled: todoCounts.cancelled };
}

describe("foldGoalTasks (#14661)", () => {
  it("folds non-urgent part_of children under their top-level parent goal and computes done/total progress", () => {
    const parent = td("goal-1", { title: "Ship v2" });
    const childDone = td("c-done", { part_of: "goal-1", done_at: "2026-11-24T10:00:00.000Z" });
    const childWorking = td("c-work", { part_of: "goal-1" });
    const childOther = td("c-other", { part_of: "goal-1" });
    const standalone = td("solo");

    const triaged = buildTriage([
      { todo: parent, section: "working" },
      { todo: childWorking, section: "working" },
      { todo: childOther, section: "other" },
      { todo: standalone, section: "other" },
      { todo: childDone, section: "done" },
    ]);

    const res = foldGoalTasks(triaged);
    expect(res.triaged.sections.working.map((g) => g.lead.id)).toEqual(["goal-1"]);
    expect(res.triaged.sections.other.map((g) => g.lead.id)).toEqual(["solo"]);
    expect(res.triaged.sections.done).toHaveLength(0);
    expect(res.triaged.todoCounts.working).toBe(1);
    expect(res.triaged.todoCounts.other).toBe(1);
    expect(res.triaged.todoCounts.done).toBe(0);

    const summary = res.goals.get("goal-1");
    expect(summary).toBeDefined();
    expect(summary!.done).toBe(1);
    expect(summary!.total).toBe(3);
    expect(summary!.blockedCount).toBe(0);
    expect(summary!.children.map((c) => c.todo.id)).toEqual(["c-work", "c-other", "c-done"]);
    expect(res.surfacedParent.size).toBe(0);
  });

  it("surfaces needs_you children in waiting with parent goal hint while still counting them in progress", () => {
    const parent = td("goal-1", { title: "Ship v2" });
    const blocker = td("c-block", { part_of: "goal-1", view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0 } });
    const worker = td("c-work", { part_of: "goal-1" });
    const done = td("c-done", { part_of: "goal-1", done_at: "2026-11-24T10:00:00.000Z" });

    const triaged = buildTriage([
      { todo: blocker, section: "waiting" },
      { todo: parent, section: "working" },
      { todo: worker, section: "working" },
      { todo: done, section: "done" },
    ]);

    const res = foldGoalTasks(triaged);
    expect(res.triaged.sections.waiting.map((g) => g.lead.id)).toEqual(["c-block"]);
    expect(res.triaged.sections.working.map((g) => g.lead.id)).toEqual(["goal-1"]);
    expect(res.triaged.sections.done).toHaveLength(0);

    expect(res.surfacedParent.get("c-block")?.id).toBe("goal-1");
    const summary = res.goals.get("goal-1")!;
    expect(summary.done).toBe(1);
    expect(summary.total).toBe(3);
    expect(summary.blockedCount).toBe(1);
    expect(summary.blockers.map((b) => b.todo.id)).toEqual(["c-block"]);
    expect(summary.children[0].todo.id).toBe("c-block");
  });

  it("promotes open children with an active owner ask into waiting even if placed outside waiting", () => {
    const parent = td("goal-1", { title: "Release" });
    const askChild = td("c-ask", {
      part_of: "goal-1",
      ask: { verdict: "Approve rollout?", asked_at: "2026-11-24T11:30:00.000Z", outcome: "decide" },
    });
    const closedStaleAsk = td("c-closed-ask", {
      part_of: "goal-1",
      done_at: "2026-11-24T11:00:00.000Z",
      ask: { verdict: "Old ask", asked_at: "2026-11-24T10:00:00.000Z", outcome: "done" },
    });

    const triaged = buildTriage([
      { todo: parent, section: "other" },
      { todo: askChild, section: "other" },
      { todo: closedStaleAsk, section: "done" },
    ]);

    const res = foldGoalTasks(triaged);
    expect(res.triaged.sections.waiting.map((g) => g.lead.id)).toEqual(["c-ask"]);
    expect(res.triaged.sections.other.map((g) => g.lead.id)).toEqual(["goal-1"]);
    expect(res.triaged.sections.done).toHaveLength(0);
    expect(res.surfacedParent.get("c-ask")?.id).toBe("goal-1");
    expect(res.surfacedParent.has("c-closed-ask")).toBe(false);
    expect(res.goals.get("goal-1")?.blockedCount).toBe(1);
    expect(res.goals.get("goal-1")?.done).toBe(1);
  });

  it("treats failed-start waiting child as open and blocked", () => {
    const failed = td("c-fail", { part_of: "goal-1", done_at: "2026-11-24T11:00:00.000Z", start_error: "offline" });
    expect(isChildDone(failed, "waiting")).toBe(false);
    expect(isChildBlocker(failed, "waiting")).toBe(true);
  });

  it("keeps orphan children top-level when parent is absent from current filtered set", () => {
    const orphan = td("c-orphan", { part_of: "missing-parent" });
    const triaged = buildTriage([{ todo: orphan, section: "working" }]);

    const res = foldGoalTasks(triaged);
    expect(res.triaged.sections.working.map((g) => g.lead.id)).toEqual(["c-orphan"]);
    expect(res.goals.size).toBe(0);
    expect(res.surfacedParent.size).toBe(0);
  });

  it("flattens multi-level descendants under the top-level ancestor goal and ignores part_of cycles safely", () => {
    const root = td("root");
    const mid = td("mid", { part_of: "root" });
    const leaf = td("leaf", { part_of: "mid", done_at: "2026-11-24T10:00:00.000Z" });
    const cycA = td("cyc-a", { part_of: "cyc-b" });
    const cycB = td("cyc-b", { part_of: "cyc-a" });
    const selfRef = td("self", { part_of: "self" });

    const triaged = buildTriage([
      { todo: root, section: "working" },
      { todo: mid, section: "working" },
      { todo: leaf, section: "done" },
      { todo: cycA, section: "other" },
      { todo: cycB, section: "other" },
      { todo: selfRef, section: "other" },
    ]);

    const res = foldGoalTasks(triaged);
    expect(res.triaged.sections.working.map((g) => g.lead.id)).toEqual(["root"]);
    expect(res.triaged.sections.done).toHaveLength(0);
    expect(res.triaged.sections.other.map((g) => g.lead.id)).toEqual(["cyc-a", "cyc-b", "self"]);
    expect(res.goals.get("root")?.done).toBe(1);
    expect(res.goals.get("root")?.total).toBe(2);
    expect(res.goals.has("mid")).toBe(false);
  });

  it("strips folded children out of merged related groups without dropping the parent lead", () => {
    const parent = td("goal-1");
    const child = td("c-1", { part_of: "goal-1" });
    const triaged = buildTriage([{ todo: parent, section: "working", members: [parent, child] }]);

    const res = foldGoalTasks(triaged);
    expect(res.triaged.sections.working).toHaveLength(1);
    expect(res.triaged.sections.working[0].members.map((m) => m.id)).toEqual(["goal-1"]);
    expect(res.goals.get("goal-1")?.total).toBe(1);
  });
});

describe("nestSectionHierarchy (#16939)", () => {
  it("nests same-section children under their parent regardless of order and computes done/blocked counts", () => {
    const parent = td("goal-1", { title: "Parent Goal" });
    const blocker = td("c-block", {
      part_of: "goal-1",
      view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0 },
    });
    const childDone = td("c-done", { part_of: "goal-1", done_at: "2026-11-24T10:00:00.000Z" });
    const standalone = td("solo");

    // Child appears before parent in section order
    const entries = nestSectionHierarchy(
      [blocker, parent, childDone, standalone],
      new Map([parent, blocker, childDone, standalone].map((x) => [x.id, x])),
      new Map<string, TriageSection>([
        ["c-block", "waiting"],
        ["goal-1", "working"],
        ["c-done", "done"],
        ["solo", "other"],
      ]),
    );

    expect(entries.map((e) => e.todo.id)).toEqual(["goal-1", "solo"]);
    expect(entries[0].total).toBe(2);
    expect(entries[0].done).toBe(1);
    expect(entries[0].blockedCount).toBe(1);
    expect(entries[0].children.map((c) => c.todo.id)).toEqual(["c-block", "c-done"]);
    expect(entries[1].children).toHaveLength(0);
  });

  it("attaches parentGoalTitle for cross-section children whose parent lives outside the section and keeps cycles safe", () => {
    const outsideParent = td("outside-goal", { title: "External Umbrella" });
    const crossChild = td("cross-child", { part_of: "outside-goal" });
    const root = td("root", { title: "Root" });
    const mid = td("mid", { title: "Middle", part_of: "root" });
    const leaf = td("leaf", { title: "Leaf", part_of: "mid" });
    const cycA = td("cyc-a", { part_of: "cyc-b" });
    const cycB = td("cyc-b", { part_of: "cyc-a" });

    const allById = new Map([outsideParent, crossChild, root, mid, leaf, cycA, cycB].map((x) => [x.id, x]));
    const entries = nestSectionHierarchy([crossChild, root, mid, leaf, cycA, cycB], allById);

    expect(entries.map((e) => e.todo.id)).toEqual(["cross-child", "root", "cyc-a", "cyc-b"]);
    expect(entries[0].parentGoalTitle).toBe("External Umbrella");
    expect(entries[1].children.map((c) => c.todo.id)).toEqual(["mid", "leaf"]);
    expect(entries[1].children[0].parentGoalTitle).toBeUndefined();
    expect(entries[1].children[1].parentGoalTitle).toBe("Middle");
  });
});
