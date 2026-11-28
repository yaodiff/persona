import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DirectionTree } from "./direction-tree";
import type { Todo } from "./todos";

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({ updated: [] }));
vi.mock("./api", async (orig) => ({
  ...(await orig<typeof import("./api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const {
  MAX_REORGANIZE_IDS,
  buildReorganizeRequest,
  canTransferSupervision,
  eligibleParentTargets,
  reorganizeDirectionOptions,
  reorganizeTodos,
  resolveDirectionSections,
} = await import("./todo-reorganize");

const makeTodo = (id: string, title: string, extra: Partial<Todo> = {}): Todo => ({
  id,
  title,
  created_at: "2026-11-24T10:00:00.000Z",
  updated_at: "2026-11-24T11:00:00.000Z",
  ...extra,
});

describe("todo-reorganize helpers (#14758)", () => {
  beforeEach(() => {
    api.mockReset();
    api.mockResolvedValue({ updated: [] });
  });

  describe("canTransferSupervision", () => {
    it("allows transfer only for non-empty active direction targets", () => {
      const active = new Set(["dir-active"]);
      expect(canTransferSupervision({ mode: "keep" }, active)).toBe(false);
      expect(canTransferSupervision({ mode: "clear" }, active)).toBe(false);
      expect(canTransferSupervision({ mode: "set", id: "" }, active)).toBe(false);
      expect(canTransferSupervision({ mode: "set", id: "dir-inactive" }, active)).toBe(false);
      expect(canTransferSupervision({ mode: "set", id: "dir-active" }, undefined)).toBe(false);
      expect(canTransferSupervision({ mode: "set", id: "dir-active" }, active)).toBe(true);
    });
  });

  describe("buildReorganizeRequest", () => {
    it("returns null when ids are empty, exceed limit, or no target change is requested", () => {
      expect(
        buildReorganizeRequest({
          ids: [],
          partOf: { mode: "clear" },
          direction: { mode: "keep" },
        }),
      ).toBeNull();

      expect(
        buildReorganizeRequest({
          ids: ["t1"],
          partOf: { mode: "keep" },
          direction: { mode: "keep" },
        }),
      ).toBeNull();

      expect(
        buildReorganizeRequest({
          ids: ["t1"],
          partOf: { mode: "set", id: "   " },
          direction: { mode: "keep" },
        }),
      ).toBeNull();

      const tooMany = Array.from({ length: MAX_REORGANIZE_IDS + 1 }, (_, i) => `t${i}`);
      expect(
        buildReorganizeRequest({
          ids: tooMany,
          partOf: { mode: "clear" },
          direction: { mode: "keep" },
        }),
      ).toBeNull();
    });

    it("distinguishes keep, clear, and set states with safe defaults", () => {
      expect(
        buildReorganizeRequest({
          ids: [" t1 ", "t2", "t1"],
          partOf: { mode: "set", id: " goal-1 " },
          direction: { mode: "keep" },
        }),
      ).toEqual({
        ids: ["t1", "t2"],
        part_of: "goal-1",
        include_children: false,
        transfer_supervision: false,
      });

      expect(
        buildReorganizeRequest({
          ids: ["t1"],
          partOf: { mode: "clear" },
          direction: { mode: "clear" },
          includeChildren: true,
          transferSupervision: true,
          activeDirectionIds: new Set(["dir-1"]),
        }),
      ).toEqual({
        ids: ["t1"],
        part_of: "",
        direction_id: "",
        include_children: true,
        transfer_supervision: false,
      });
    });

    it("enables transfer_supervision only when targeting an active direction", () => {
      const active = new Set(["dir-active"]);

      expect(
        buildReorganizeRequest({
          ids: ["t1"],
          partOf: { mode: "keep" },
          direction: { mode: "set", id: "dir-active" },
          transferSupervision: true,
          activeDirectionIds: active,
        }),
      ).toEqual({
        ids: ["t1"],
        direction_id: "dir-active",
        include_children: false,
        transfer_supervision: true,
      });

      expect(
        buildReorganizeRequest({
          ids: ["t1"],
          partOf: { mode: "keep" },
          direction: { mode: "set", id: "dir-external" },
          transferSupervision: true,
          activeDirectionIds: active,
        }),
      ).toEqual({
        ids: ["t1"],
        direction_id: "dir-external",
        include_children: false,
        transfer_supervision: false,
      });
    });
  });

  describe("eligibleParentTargets", () => {
    it("excludes selected todos and all their multi-hop descendants while keeping ancestors and siblings", () => {
      const todos: Todo[] = [
        makeTodo("root", "Root Goal"),
        makeTodo("mid", "Mid Task", { part_of: "root" }),
        makeTodo("leaf", "Leaf Task", { part_of: "mid" }),
        makeTodo("grandleaf", "Grandleaf Task", { part_of: "leaf" }),
        makeTodo("sibling", "Sibling Task", { part_of: "root" }),
        makeTodo("other", "Other Goal"),
      ];

      expect(eligibleParentTargets(todos, ["mid"]).map((t) => t.id)).toEqual(["root", "sibling", "other"]);
      expect(eligibleParentTargets(todos, new Set(["mid", "other"])).map((t) => t.id)).toEqual(["root", "sibling"]);
    });

    it("terminates safely when existing data already contains a part_of cycle", () => {
      const cyclic: Todo[] = [
        makeTodo("a", "A", { part_of: "b" }),
        makeTodo("b", "B", { part_of: "a" }),
        makeTodo("c", "C"),
      ];
      expect(eligibleParentTargets(cyclic, ["a"]).map((t) => t.id)).toEqual(["c"]);
    });
  });

  describe("reorganizeDirectionOptions", () => {
    it("combines direction tree leaders and assistant leader IDs, excluding secretary and marking external inactive", () => {
      const tree: DirectionTree = {
        directions: [
          {
            id: "dir-1",
            name: "Core Platform",
            counts: { working: 1, needs_you: 0, stuck: 0, review: 0 },
            running: true,
          },
          {
            id: "dir-ext",
            name: "External Bot",
            external: true,
            counts: { working: 0, needs_you: 0, stuck: 0, review: 0 },
            running: false,
          },
          {
            id: "main",
            name: "",
            secretary: true,
            counts: { working: 0, needs_you: 0, stuck: 0, review: 0 },
            running: false,
          },
        ],
      };

      const opts = reorganizeDirectionOptions(tree, undefined, ["dir-1", "dir-2"]);
      expect(opts).toEqual([
        { id: "dir-1", label: "Core Platform", active: true },
        { id: "dir-ext", label: "External Bot", active: false },
        { id: "dir-2", label: "dir-2", active: true },
      ]);
    });
  });

  describe("reorganizeTodos", () => {
    it("POSTs to /todos/reorganize with json body and headers", async () => {
      const updated = [makeTodo("t1", "Task 1", { part_of: "goal-1" })];
      api.mockResolvedValueOnce({ updated, transferred_ids: ["t1"] });

      const req = {
        ids: ["t1"],
        part_of: "goal-1",
        include_children: false,
        transfer_supervision: false,
      };
      const res = await reorganizeTodos(req);

      expect(api).toHaveBeenCalledWith(
        "/todos/reorganize",
        expect.objectContaining({
          method: "POST",
          json: req,
        }),
      );
      expect(res).toEqual({ updated, transferred_ids: ["t1"] });
    });
  });

  describe("resolveDirectionSections (#15653)", () => {
    const counts = { working: 1, needs_you: 0, stuck: 0, review: 0 };
    const tree: DirectionTree = {
      directions: [
        {
          id: "dir-infra",
          name: "Infrastructure",
          counts,
          running: true,
          tasks: [
            { id: "t1", title: "Infra A", state: "working" },
            { id: "t2", title: "Moved to Product", state: "working" },
          ],
        },
        {
          id: "dir-empty",
          name: "New Initiative",
          counts: { working: 0, needs_you: 0, stuck: 0, review: 0 },
          running: false,
          tasks: [],
        },
        {
          id: "dir-product",
          name: "Product",
          counts,
          running: false,
          tasks: [{ id: "t3", title: "Product Native", state: "working" }],
        },
        {
          id: "main",
          name: "",
          secretary: true,
          counts,
          running: false,
          tasks: [{ id: "t4", title: "Secretary Task", state: "working" }],
        },
      ],
    };

    const resolveName = (d: DirectionTree["directions"][number]) => (d.secretary ? "Secretary" : d.name || d.id);

    it("ignores explicit direction_id and keeps legacy directionTree grouping when todosV2 is false", () => {
      const openTodos = [
        makeTodo("t1", "Infra A"),
        makeTodo("t2", "Moved to Product", { direction_id: "dir-product" }),
        makeTodo("t3", "Product Native"),
        makeTodo("t4", "Secretary Task", { direction_id: "dir-unlisted" }),
      ];

      const sections = resolveDirectionSections({
        tree,
        openTodos,
        todosV2: false,
        resolveDirectionName: resolveName,
      });

      expect(sections.map((s) => [s.d.id, s.todos.map((td) => td.id)])).toEqual([
        ["dir-infra", ["t1", "t2"]],
        ["dir-product", ["t3"]],
        ["main", ["t4"]],
      ]);
    });

    it("prioritizes explicit direction_id over runtime tree membership when todosV2 is true, with fallback and synthetic sections", () => {
      const openTodos = [
        makeTodo("t1", "Infra A"),
        makeTodo("t2", "Moved to Product", { direction_id: "dir-product" }),
        makeTodo("t3", "Product Native", { direction_id: "dir-product" }),
        makeTodo("t4", "Secretary Task"),
        makeTodo("t5", "Into Empty Dir", { direction_id: "dir-empty" }),
        makeTodo("t6", "Into Unlisted Dir", { direction_id: "dir-custom" }),
      ];

      const sections = resolveDirectionSections({
        tree,
        openTodos,
        todosV2: true,
        resolveDirectionName: resolveName,
        resolveFallbackName: (id) => (id === "dir-custom" ? "Custom Direction" : undefined),
      });

      expect(sections.map((s) => [s.d.id, s.title, s.todos.map((td) => td.id)])).toEqual([
        ["dir-infra", "Infrastructure", ["t1"]],
        ["dir-empty", "New Initiative", ["t5"]],
        ["dir-product", "Product", ["t3", "t2"]],
        ["dir-custom", "Custom Direction", ["t6"]],
        ["main", "Secretary", ["t4"]],
      ]);
    });

    it("falls back to direction_id itself as title when no fallback name is known", () => {
      const openTodos = [makeTodo("t-orphan", "Orphan", { direction_id: "dir-raw-id" })];

      const sections = resolveDirectionSections({
        tree,
        openTodos,
        todosV2: true,
        resolveDirectionName: resolveName,
      });

      expect(sections.map((s) => [s.d.id, s.title, s.todos.map((td) => td.id)])).toEqual([
        ["dir-raw-id", "dir-raw-id", ["t-orphan"]],
      ]);
    });

    it("respects effective_direction_id and effective_direction_name when direction_id is empty under todosV2=true, and ignores them under todosV2=false (#17181)", () => {
      const umbrellaTree: DirectionTree = {
        directions: [
          ...tree.directions.slice(0, 3),
          {
            id: "main",
            name: "",
            secretary: true,
            counts,
            running: false,
            tasks: [
              { id: "t4", title: "Uncategorized Task", state: "working" },
              { id: "t-umbrella", title: "Umbrella Goal", state: "working" },
              { id: "t-synth", title: "Inferred Stream Goal", state: "working" },
            ],
          },
        ],
      };
      const openTodos = [
        makeTodo("t1", "Infra A"),
        makeTodo("t2", "Moved Explicit Wins", {
          direction_id: "dir-product",
          effective_direction_id: "dir-infra",
          effective_direction_name: "Infrastructure",
        }),
        makeTodo("t3", "Product Native"),
        makeTodo("t4", "Uncategorized Task"),
        makeTodo("t-umbrella", "Umbrella Goal", {
          effective_direction_id: "dir-product",
          effective_direction_name: "Product",
        }),
        makeTodo("t-synth", "Inferred Stream Goal", {
          effective_direction_id: "dir-inferred",
          effective_direction_name: "Inferred Stream",
        }),
      ];

      const legacySections = resolveDirectionSections({
        tree: umbrellaTree,
        openTodos,
        todosV2: false,
        resolveDirectionName: resolveName,
      });
      expect(legacySections.map((s) => [s.d.id, s.title, s.todos.map((td) => td.id)])).toEqual([
        ["dir-infra", "Infrastructure", ["t1", "t2"]],
        ["dir-product", "Product", ["t3"]],
        ["main", "Secretary", ["t4", "t-umbrella", "t-synth"]],
      ]);

      const v2Sections = resolveDirectionSections({
        tree: umbrellaTree,
        openTodos,
        todosV2: true,
        resolveDirectionName: (d) => (d.secretary ? "Uncategorized" : d.name || d.id),
      });
      expect(v2Sections.map((s) => [s.d.id, s.title, s.todos.map((td) => td.id)])).toEqual([
        ["dir-infra", "Infrastructure", ["t1"]],
        ["dir-product", "Product", ["t3", "t2", "t-umbrella"]],
        ["dir-inferred", "Inferred Stream", ["t-synth"]],
        ["main", "Uncategorized", ["t4"]],
      ]);
    });
  });
});
