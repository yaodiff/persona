import { describe, expect, it } from "vitest";
import { createEffectiveDirectionResolver, resolveEffectiveDirection } from "./todo-effective-direction";
import type { Todo, TodoBoardProjection } from "./todos";

const AT = "2026-11-27T06:00:00.000Z";
const makeTodo = (id: string, patch: Partial<Todo> = {}): Todo => ({
  id,
  title: `Todo ${id}`,
  created_at: AT,
  updated_at: AT,
  ...patch,
});

const emptyBoardCounts = {
  needs_you: 0,
  in_progress: 0,
  pending_review: 0,
  waiting: 0,
  someday: 0,
  done: 0,
  open_total: 0,
};

describe("todo-effective-direction (#16939)", () => {
  it("1. prefers explicit todo.direction_id (inherited: false) with label precedence directionLabel -> effective_direction_name -> board -> id", () => {
    const board: TodoBoardProjection = {
      status_groups: [],
      direction_groups: [
        {
          direction_id: "dir-platform",
          label: "Board Platform",
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
          todo_ids: ["t1"],
        },
      ],
      sidebar_tree: [],
      counts: emptyBoardCounts,
    };

    const td = makeTodo("t1", {
      direction_id: "dir-platform",
      effective_direction_id: "dir-platform",
      effective_direction_name: "Server Platform",
    });

    expect(
      resolveEffectiveDirection(td, {
        board,
        directionLabel: (id) => (id === "dir-platform" ? "Tree Platform" : undefined),
      }),
    ).toEqual({ id: "dir-platform", label: "Tree Platform", inherited: false });

    expect(resolveEffectiveDirection(td, { board })).toEqual({
      id: "dir-platform",
      label: "Server Platform",
      inherited: false,
    });

    const tdNoServerName = makeTodo("t1", { direction_id: "dir-platform" });
    expect(resolveEffectiveDirection(tdNoServerName, { board })).toEqual({
      id: "dir-platform",
      label: "Board Platform",
      inherited: false,
    });

    expect(resolveEffectiveDirection(tdNoServerName)).toEqual({
      id: "dir-platform",
      label: "dir-platform",
      inherited: false,
    });

    // Ignores stale effective_direction_name when effective_direction_id points to a different direction
    const tdStaleServer = makeTodo("t1", {
      direction_id: "dir-new",
      effective_direction_id: "dir-old",
      effective_direction_name: "Old Name",
    });
    expect(resolveEffectiveDirection(tdStaleServer)).toEqual({
      id: "dir-new",
      label: "dir-new",
      inherited: false,
    });
  });

  it("2. falls back to server effective_direction_id (inherited: true) when raw direction_id is absent", () => {
    const td = makeTodo("t-server", {
      effective_direction_id: "dir-growth",
      effective_direction_name: "Growth Stream",
    });

    expect(
      resolveEffectiveDirection(td, {
        directionLabel: (id) => (id === "dir-growth" ? "Client Growth" : undefined),
      }),
    ).toEqual({ id: "dir-growth", label: "Growth Stream", inherited: true });

    const tdIdOnly = makeTodo("t-server-id", { effective_direction_id: "dir-growth" });
    expect(
      resolveEffectiveDirection(tdIdOnly, {
        directionLabel: (id) => (id === "dir-growth" ? "Client Growth" : undefined),
      }),
    ).toEqual({ id: "dir-growth", label: "Client Growth", inherited: true });
  });

  it("3. falls back to board.direction_groups when raw direction_id and effective_direction_id are absent, skipping empty ungrouped bucket", () => {
    const board: TodoBoardProjection = {
      status_groups: [],
      direction_groups: [
        {
          direction_id: "dir-infra",
          label: "Infrastructure",
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
          todo_ids: ["t-in-board"],
        },
        {
          direction_id: "",
          label: "Secretary",
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
          todo_ids: ["t-ungrouped"],
        },
      ],
      sidebar_tree: [],
      counts: emptyBoardCounts,
    };

    const resolve = createEffectiveDirectionResolver({ board });
    expect(resolve(makeTodo("t-in-board"))).toEqual({
      id: "dir-infra",
      label: "Infrastructure",
      inherited: true,
    });
    expect(resolve(makeTodo("t-ungrouped"))).toBeUndefined();
  });

  it("4. falls back on older hubs to supervision.supervisor, then origin (only for knownDirectionIds), then cycle-safe part_of parent chain", () => {
    const knownDirectionIds = new Set(["dir-platform", "dir-growth"]);
    const directionLabel = (id: string) =>
      id === "dir-platform" ? "Platform" : id === "dir-growth" ? "Growth" : undefined;

    const supTodo = makeTodo("t-sup", {
      supervision: { supervisor: "dir-platform" },
      origin: "dir-growth",
    });
    const originTodo = makeTodo("t-origin", {
      supervision: { supervisor: "main" },
      origin: "dir-growth",
    });
    const mainOnlyTodo = makeTodo("t-main", {
      supervision: { supervisor: "main", state: "needs_user" },
      origin: "conv-main",
    });

    const root = makeTodo("goal-root", { direction_id: "dir-platform" });
    const mid = makeTodo("goal-mid", { part_of: "goal-root" });
    const leaf = makeTodo("goal-leaf", { part_of: "goal-mid" });

    const cycA = makeTodo("cyc-a", { part_of: "cyc-b" });
    const cycB = makeTodo("cyc-b", { part_of: "cyc-a" });
    const selfLoop = makeTodo("self-loop", { part_of: "self-loop" });

    const todosById = new Map(
      [supTodo, originTodo, mainOnlyTodo, root, mid, leaf, cycA, cycB, selfLoop].map((td) => [td.id, td]),
    );
    const resolve = createEffectiveDirectionResolver({ todosById, knownDirectionIds, directionLabel });

    // Supervisor takes precedence over origin when both are known directions
    expect(resolve(supTodo)).toEqual({ id: "dir-platform", label: "Platform", inherited: true });
    // Unknown supervisor ("main") falls through to known origin
    expect(resolve(originTodo)).toEqual({ id: "dir-growth", label: "Growth", inherited: true });
    // Main/secretary conversations are not directions
    expect(resolve(mainOnlyTodo)).toBeUndefined();
    // Multi-hop part_of inheritance resolves to inherited: true
    expect(resolve(root)).toEqual({ id: "dir-platform", label: "Platform", inherited: false });
    expect(resolve(mid)).toEqual({ id: "dir-platform", label: "Platform", inherited: true });
    expect(resolve(leaf)).toEqual({ id: "dir-platform", label: "Platform", inherited: true });
    // Cycles & self-loops terminate safely with undefined
    expect(resolve(cycA)).toBeUndefined();
    expect(resolve(cycB)).toBeUndefined();
    expect(resolve(selfLoop)).toBeUndefined();
  });

  it("inherits parent direction from board.direction_groups when child is placed in ungrouped bucket on pre-Sub-PR-1 hubs", () => {
    const parent = makeTodo("p1");
    const child = makeTodo("c1", { part_of: "p1" });
    const board: TodoBoardProjection = {
      status_groups: [],
      direction_groups: [
        {
          direction_id: "dir-platform",
          label: "Platform",
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
          todo_ids: ["p1"],
        },
        {
          direction_id: "",
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
          todo_ids: ["c1"],
        },
      ],
      sidebar_tree: [],
      counts: emptyBoardCounts,
    };
    const todosById = new Map([parent, child].map((td) => [td.id, td]));
    const resolve = createEffectiveDirectionResolver({ todosById, board });
    expect(resolve(child)).toEqual({ id: "dir-platform", label: "Platform", inherited: true });
  });
});
