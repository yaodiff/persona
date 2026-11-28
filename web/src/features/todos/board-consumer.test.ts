import { describe, expect, it } from "vitest";
import {
  boardSectionMap,
  boardSectionToTriageSection,
  consumeBoardDirectionGroups,
  consumeBoardStatusGroups,
} from "./board-consumer";
import { consumeBoardSidebarTree } from "./board-sidebar-tree";
import type { Todo, TodoBoardProjection } from "../../lib/todos";
import type { Machine, Session } from "../../lib/types";

const NOW = Date.parse("2026-11-25T12:00:00.000Z");
const iso = (offsetHours: number) => new Date(NOW - offsetHours * 3_600_000).toISOString();

const makeTodo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({
  id,
  title,
  created_at: iso(10),
  updated_at: iso(2),
  ...patch,
});

const makeSession = (sid: string, state: Session["state"], patch: Partial<Session> = {}): Session => ({
  machine_id: "m1",
  session_id: sid,
  title: `Agent ${sid}`,
  cwd: "/code/persona",
  status: state === "working" ? "working" : "idle",
  state,
  updated_at: iso(1),
  ...patch,
});

const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];

describe("board-consumer", () => {
  it("maps server status group keys to web triage sections", () => {
    expect(boardSectionToTriageSection("needs_you")).toBe("waiting");
    expect(boardSectionToTriageSection("can_wait")).toBe("can_wait");
    expect(boardSectionToTriageSection("in_progress")).toBe("working");
    expect(boardSectionToTriageSection("pending_review")).toBe("just_done");
    expect(boardSectionToTriageSection("waiting")).toBe("parked");
    expect(boardSectionToTriageSection("todo")).toBe("todo");
    expect(boardSectionToTriageSection("someday")).toBe("shelved");
    expect(boardSectionToTriageSection("done")).toBe("done");
    expect(boardSectionToTriageSection("cancelled")).toBe("cancelled");
    expect(boardSectionToTriageSection("custom_future")).toBe("other");
  });

  it("keeps current and older closed outcomes separate, including a folded cancellation", () => {
    const cancelled = { done_at: iso(1), cancelled_at: iso(1) };
    const todos = [
      makeTodo("todo", "Ordinary to-do"),
      makeTodo("later", "Deferred decision"),
      makeTodo("done", "Finished"),
      makeTodo("cancelled", "Cancelled", cancelled),
      makeTodo("folded-cancelled", "Folded cancelled", { ...cancelled, part_of: "todo" }),
    ];
    const olderClosed = [
      makeTodo("old-done", "Older done", { done_at: iso(24) }),
      makeTodo("old-cancelled", "Older cancelled", { done_at: iso(24), done_by: "cancelled" }),
      todos[3],
    ];
    const board: TodoBoardProjection = {
      status_groups: [
        { key: "can_wait", total_count: 1, visible_count: 1, entries: [{ todo_id: "later" }] },
        { key: "todo", total_count: 1, visible_count: 1, entries: [{ todo_id: "todo", folded_child_ids: ["folded-cancelled"] }] },
        { key: "done", total_count: 2, visible_count: 2, entries: [{ todo_id: "done" }, { todo_id: "old-done" }] },
        { key: "cancelled", total_count: 3, visible_count: 2, entries: [{ todo_id: "cancelled" }, { todo_id: "old-cancelled" }] },
      ],
      direction_groups: [],
      sidebar_tree: [],
      counts: { needs_you: 0, can_wait: 1, in_progress: 0, pending_review: 0, waiting: 0, todo: 1, someday: 0, done: 2, cancelled: 2, open_total: 2 },
    };
    const result = consumeBoardStatusGroups(todos, board, undefined, olderClosed);
    const ids = (section: ReturnType<typeof boardSectionToTriageSection>) => result.goalFold.triaged.sections[section].map(({ key }) => key);
    expect(result.sectionOrder.slice(0, 4)).toEqual(["can_wait", "todo", "done", "cancelled"]);
    expect(ids("done")).toEqual(["done", "old-done"]);
    expect(ids("cancelled")).toEqual(["cancelled", "old-cancelled"]);
    expect(ids("waiting")).toEqual([]);
    expect(result.sectionCounts.cancelled).toBe(2);
    expect(result.goalFold.goals.get("todo")?.children[0].section).toBe("cancelled");
  });

  it("strictly preserves server status_groups order, entry order, folding, surfacing, and counts without re-triaging", () => {
    // Deliberately give item_b an older timestamp and no client needs_you signals;
    // the server board puts item_b BEFORE item_a in needs_you, proving the client does not re-sort or re-bucket.
    const todos: Todo[] = [
      makeTodo("item_a", "Newer item A", { created_at: iso(1), updated_at: iso(1) }),
      makeTodo("item_b", "Older item B", { created_at: iso(48), updated_at: iso(48) }),
      makeTodo("goal_1", "Ship v2"),
      makeTodo("child_ok", "Subtask normal", { part_of: "goal_1" }),
      makeTodo("child_done", "Subtask done", { part_of: "goal_1", done_at: iso(1) }),
      makeTodo("child_blocked", "Subtask needs decision", { part_of: "goal_1" }),
      makeTodo("review_1", "Delivered PR"),
    ];

    const board: TodoBoardProjection = {
      status_groups: [
        {
          key: "needs_you",
          total_count: 3,
          visible_count: 3,
          entries: [
            { todo_id: "item_b" },
            { todo_id: "child_blocked" },
            { todo_id: "item_a" },
          ],
        },
        {
          key: "in_progress",
          total_count: 2,
          visible_count: 1,
          entries: [
            {
              todo_id: "goal_1",
              folded_child_ids: ["child_ok", "child_done"],
              surfaced_child_ids: ["child_blocked"],
              folded_count: 2,
            },
          ],
        },
        {
          key: "pending_review",
          total_count: 1,
          visible_count: 1,
          entries: [{ todo_id: "review_1" }],
        },
      ],
      direction_groups: [],
      sidebar_tree: [],
      counts: {
        needs_you: 7, // Deliberately distinct from entries.length to verify header count reads board.counts directly
        in_progress: 2,
        pending_review: 1,
        waiting: 0,
        someday: 0,
        done: 1,
        open_total: 10,
      },
    };

    const res = consumeBoardStatusGroups(todos, board);
    expect(res.sectionOrder.slice(0, 3)).toEqual(["waiting", "working", "just_done"]);
    // Exact server entry order preserved (item_b before child_blocked before item_a)
    expect(res.goalFold.triaged.sections.waiting.map((g) => g.lead.id)).toEqual([
      "item_b",
      "child_blocked",
      "item_a",
    ]);
    expect(res.goalFold.triaged.sections.working.map((g) => g.lead.id)).toEqual(["goal_1"]);
    expect(res.goalFold.triaged.sections.just_done.map((g) => g.lead.id)).toEqual(["review_1"]);

    // Folded/surfaced children attached to parent goal directly from board entry
    const goal = res.goalFold.goals.get("goal_1")!;
    expect(goal).toBeDefined();
    expect(goal.total).toBe(3);
    expect(goal.done).toBe(1);
    expect(goal.blockedCount).toBe(1);
    expect(goal.children.map((c) => [c.todo.id, c.blocker, c.done])).toEqual([
      ["child_blocked", true, false],
      ["child_ok", false, false],
      ["child_done", false, true],
    ]);
    expect(res.goalFold.surfacedParent.get("child_blocked")?.id).toBe("goal_1");

    // Server counts preserved
    expect(res.sectionCounts.waiting).toBe(3);
    expect(res.sectionCounts.working).toBe(1);
    expect(res.goalFold.triaged.open).toBe(10);

    // boardSectionMap maps both top-level entries and folded children
    const secMap = boardSectionMap(board, new Map(todos.map((td) => [td.id, td])));
    expect(secMap.get("item_b")).toBe("waiting");
    expect(secMap.get("child_blocked")).toBe("waiting");
    expect(secMap.get("child_ok")).toBe("working");
    expect(secMap.get("child_done")).toBe("done");
  });

  it("keeps the complete Done count when a full board names rows outside the bounded list", () => {
    const recent = makeTodo("recent", "Recent done", { done_at: iso(1) });
    const board: TodoBoardProjection = {
      status_groups: [{
        key: "done",
        total_count: 55,
        visible_count: 55,
        entries: [{ todo_id: "recent" }, ...Array.from({ length: 54 }, (_, i) => ({ todo_id: `older-${i}` }))],
      }],
      direction_groups: [],
      sidebar_tree: [],
      counts: { needs_you: 0, in_progress: 0, pending_review: 0, waiting: 0, someday: 0, done: 55, open_total: 0 },
    };

    const res = consumeBoardStatusGroups([recent], board);
    expect(res.sectionCounts.done, "the server's complete count, not the bounded row count").toBe(55);
    expect(res.goalFold.triaged.sections.done).toHaveLength(1);
  });

  it("applies local UI filters on top of board.status_groups and surfaces matching folded children", () => {
    const todos: Todo[] = [
      makeTodo("goal_1", "Parent goal"),
      makeTodo("child_match", "Specific keyword child", { part_of: "goal_1" }),
      makeTodo("other_1", "Unrelated task"),
    ];
    const board: TodoBoardProjection = {
      status_groups: [
        {
          key: "in_progress",
          total_count: 3,
          visible_count: 2,
          entries: [
            { todo_id: "goal_1", folded_child_ids: ["child_match"], folded_count: 1 },
            { todo_id: "other_1" },
          ],
        },
      ],
      direction_groups: [],
      sidebar_tree: [],
      counts: { needs_you: 0, in_progress: 3, pending_review: 0, waiting: 0, someday: 0, done: 0, open_total: 3 },
    };

    const filtered = consumeBoardStatusGroups(todos, board, (td) => td.title.includes("keyword"));
    expect(filtered.goalFold.triaged.sections.working.map((g) => g.lead.id)).toEqual(["child_match"]);
    expect(filtered.sectionCounts.working).toBe(1);
  });

  it("strictly preserves server direction_groups order, labels, counts, and member order", () => {
    const todos: Todo[] = [
      makeTodo("t1", "Alpha"),
      makeTodo("t2", "Beta"),
      makeTodo("t3", "Loose"),
      makeTodo("t4", "Finished", { done_at: iso(1) }),
    ];
    const board: TodoBoardProjection = {
      status_groups: [
        { key: "needs_you", total_count: 1, visible_count: 1, entries: [{ todo_id: "t2" }] },
        { key: "in_progress", total_count: 2, visible_count: 2, entries: [{ todo_id: "t1" }, { todo_id: "t3" }] },
        { key: "done", total_count: 1, visible_count: 1, entries: [{ todo_id: "t4" }] },
      ],
      direction_groups: [
        {
          direction_id: "dir-b",
          label: "Growth",
          counts: { needs_you: 1, in_progress: 1, pending_review: 0, waiting: 0, done: 1, total: 3 },
          todo_ids: ["t2", "t1", "t4"],
        },
        {
          direction_id: "",
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
          todo_ids: ["t3"],
        },
      ],
      sidebar_tree: [],
      counts: { needs_you: 1, in_progress: 2, pending_review: 0, waiting: 0, someday: 0, done: 1, open_total: 3 },
    };

    const sections = consumeBoardDirectionGroups({
      todos,
      board,
      ungroupedLabel: "Secretary",
    });

    expect(sections.map((s) => ({ id: s.d.id, title: s.title, secretary: s.d.secretary, ids: s.todos.map((td) => td.id), counts: s.d.counts }))).toEqual([
      {
        id: "dir-b",
        title: "Growth",
        secretary: undefined,
        ids: ["t2", "t1"], // done item t4 excluded by default (shown in Completed fold)
        counts: { working: 1, needs_you: 1, stuck: 0, review: 0 },
      },
      {
        id: "__ungrouped__",
        title: "Secretary",
        secretary: true,
        ids: ["t3"],
        counts: { working: 1, needs_you: 0, stuck: 0, review: 0 },
      },
    ]);
  });

  it("renders sidebar_tree in exact server order, aggregates folded children, surfaces blockers, and keeps unassigned loose", () => {
    const todos: Todo[] = [
      makeTodo("goal_alpha", "Alpha Goal", {
        agents: [{ machine_id: "m1", session_id: "s_alpha", linked_at: iso(2) }],
      }),
      makeTodo("child_quiet", "Quiet Subtask", {
        part_of: "goal_alpha",
        agents: [{ machine_id: "m1", session_id: "s_child", linked_at: iso(2) }],
      }),
      makeTodo("child_done", "Done Subtask", {
        part_of: "goal_alpha",
        done_at: iso(1),
      }),
      makeTodo("child_urgent", "Urgent Blocker", {
        part_of: "goal_alpha",
        agents: [{ machine_id: "m1", session_id: "s_urgent", linked_at: iso(1) }],
      }),
      makeTodo("solo_z", "Solo Z", {
        agents: [{ machine_id: "m1", session_id: "s_solo", linked_at: iso(3) }],
      }),
    ];

    const sessions: Session[] = [
      makeSession("s_alpha", "working"),
      makeSession("s_child", "working"),
      makeSession("s_urgent", "needs_you"),
      makeSession("s_solo", "working"),
      makeSession("s_loose", "idle"),
    ];

    // Deliberately put solo_z BEFORE goal_alpha in sidebar_tree to verify server order is respected verbatim.
    const board: TodoBoardProjection = {
      status_groups: [],
      direction_groups: [],
      sidebar_tree: [
        {
          todo_id: "child_urgent",
          section: "needs_you",
          item_type: "task",
          folded_count: 0,
          needs_you_count: 1,
          active_count: 0,
        },
        {
          todo_id: "solo_z",
          section: "in_progress",
          item_type: "task",
          folded_count: 0,
          needs_you_count: 0,
          active_count: 1,
        },
        {
          todo_id: "goal_alpha",
          section: "in_progress",
          item_type: "task",
          child_ids: ["child_quiet", "child_done"],
          surfaced_child_ids: ["child_urgent"],
          folded_count: 2,
          needs_you_count: 0,
          active_count: 2,
        },
      ],
      counts: { needs_you: 1, in_progress: 3, pending_review: 0, waiting: 0, someday: 0, done: 1, open_total: 4 },
    };

    const tree = consumeBoardSidebarTree(sessions, machines, todos, board, { spaces: [], sessions: [] }, NOW, { keepEmpty: true });

    // Exact server sidebar_tree order preserved!
    expect(tree.todos.map((n) => n.todoId)).toEqual(["child_urgent", "solo_z", "goal_alpha"]);
    expect(tree.todos[0]!.state).toBe("needs_you");
    expect(tree.todos[0]!.folded).toBe(false);
    expect(tree.todos[0]!.sessions.map((s) => s.session_id)).toEqual(["s_urgent"]);

    // goal_alpha aggregates its own agent + folded child_quiet's agent (excluding surfaced child_urgent)
    const goalNode = tree.todos[2]!;
    expect(goalNode.sessions.map((s) => s.session_id).sort()).toEqual(["s_alpha", "s_child"]);
    expect(goalNode.goalProgress).toEqual({ done: 1, total: 3 });

    // Unassigned session lands in loose group
    expect(tree.loose).toHaveLength(1);
    expect(tree.loose[0]!.sessions.map((s) => s.session_id)).toEqual(["s_loose"]);
  });
});
