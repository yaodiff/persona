import { beforeAll, describe, expect, it } from "vitest";
import { initI18n, t } from "../i18n";
import {
  filterTodosBySharedBlocker,
  findSelectedBlocker,
  isRawTodoId,
  matchesSharedBlocker,
  parseSharedBlockers,
  resolveSelectedBlockerKey,
  safeBlockerUrl,
  type SharedBlocker,
} from "./todo-shared-blockers";

beforeAll(async () => {
  await initI18n("en");
});

describe("safeBlockerUrl", () => {
  it("accepts valid http and https URLs", () => {
    expect(safeBlockerUrl("https://github.com/org/repo/pull/42")).toBe("https://github.com/org/repo/pull/42");
    expect(safeBlockerUrl("  http://git.example.com/team/app/-/issues/7  ")).toBe("http://git.example.com/team/app/-/issues/7");
  });

  it("rejects non-http(s), malformed, or whitespace-containing values", () => {
    expect(safeBlockerUrl(undefined)).toBeUndefined();
    expect(safeBlockerUrl("")).toBeUndefined();
    expect(safeBlockerUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeBlockerUrl("data:text/html,hi")).toBeUndefined();
    expect(safeBlockerUrl("not a url")).toBeUndefined();
    expect(safeBlockerUrl("https://github.com/org/repo/pull/42 extra")).toBeUndefined();
  });
});

describe("parseSharedBlockers", () => {
  it("degrades safely to [] for missing or non-array payloads (older hubs)", () => {
    expect(parseSharedBlockers(undefined)).toEqual([]);
    expect(parseSharedBlockers(null)).toEqual([]);
    expect(parseSharedBlockers({})).toEqual([]);
    expect(parseSharedBlockers("bad")).toEqual([]);
    expect(parseSharedBlockers([])).toEqual([]);
  });

  it("keeps only valid items with count >= 2 and >= 2 unique todo_ids, sorted deterministically", () => {
    const parsed = parseSharedBlockers([
      null,
      { key: "", kind: "pull_request", label: "missing key", todo_ids: ["a", "b"], count: 2 },
      { key: "k1", kind: "pull_request", label: "  ", todo_ids: ["a", "b"], count: 2 },
      { key: "k_single_count", kind: "issue", label: "org/repo#1", todo_ids: ["a", "b"], count: 1 },
      { key: "k_single_id", kind: "issue", label: "org/repo#2", todo_ids: ["a", "a"], count: 2 },
      {
        key: "issue:https://github.com/org/repo/issues/9",
        kind: "issue",
        label: "org/repo#9",
        url: "https://github.com/org/repo/issues/9",
        todo_ids: ["t1", "t2"],
        count: 2,
      },
      {
        key: "pull_request:https://github.com/org/repo/pull/42",
        kind: "pull_request",
        label: "org/repo#42",
        url: "https://github.com/org/repo/pull/42",
        todo_ids: ["t1", "t2", "t3"],
        count: 3,
      },
      // Duplicate key should be ignored
      {
        key: "pull_request:https://github.com/org/repo/pull/42",
        kind: "pull_request",
        label: "duplicate",
        todo_ids: ["t1", "t2"],
        count: 2,
      },
      // Unknown kind degrades to external_target, unsafe URL is stripped
      {
        key: "target:ci release",
        kind: "custom_kind",
        label: "CI release gate",
        url: "javascript:void(0)",
        todo_ids: ["t2", "t3"],
        count: 2,
      },
    ]);

    expect(parsed).toEqual([
      {
        key: "pull_request:https://github.com/org/repo/pull/42",
        kind: "pull_request",
        label: "org/repo#42",
        url: "https://github.com/org/repo/pull/42",
        todo_ids: ["t1", "t2", "t3"],
        count: 3,
      },
      {
        key: "issue:https://github.com/org/repo/issues/9",
        kind: "issue",
        label: "org/repo#9",
        url: "https://github.com/org/repo/issues/9",
        todo_ids: ["t1", "t2"],
        count: 2,
      },
      {
        key: "target:ci release",
        kind: "external_target",
        label: "CI release gate",
        todo_ids: ["t2", "t3"],
        count: 2,
      },
    ]);
  });

  it("scopes todo_ids to visibleTodoIds and drops blockers falling below 2 visible tasks", () => {
    const raw = [
      {
        key: "pr:1",
        kind: "pull_request",
        label: "org/repo#1",
        url: "https://github.com/org/repo/pull/1",
        todo_ids: ["t1", "t2", "other_mgr_t3"],
        count: 3,
      },
      {
        key: "pr:2",
        kind: "pull_request",
        label: "org/repo#2",
        todo_ids: ["t1", "other_mgr_t3"],
        count: 2,
      },
    ];
    const parsed = parseSharedBlockers(raw, new Set(["t1", "t2"]));
    expect(parsed).toEqual([
      {
        key: "pr:1",
        kind: "pull_request",
        label: "org/repo#1",
        url: "https://github.com/org/repo/pull/1",
        todo_ids: ["t1", "t2"],
        count: 2,
      },
    ]);
  });

  it("resolves internal prerequisite todo blockers with server title, todosById fallback, and zero raw todo_... IDs (#17938)", () => {
    expect(isRawTodoId("todo_e8acf635da201b7a")).toBe(true);
    expect(isRawTodoId("org/repo#42")).toBe(false);

    const todosById = new Map([
      ["todo_e8acf635da201b7a", { id: "todo_e8acf635da201b7a", title: "Stabilize Steam extension pipeline" }],
      ["todo_server_named", { id: "todo_server_named", title: "Local title fallback" }],
    ]);

    const parsed = parseSharedBlockers(
      [
        // Legacy hub emits target:todo_... as external_target with raw ID as label
        {
          key: "target:todo_e8acf635da201b7a",
          kind: "external_target",
          label: "todo_e8acf635da201b7a",
          todo_ids: ["t1", "t2"],
          count: 2,
        },
        // Newer hub (#17937) emits kind: "todo" with server-resolved readable label -> prefer server label
        {
          key: "todo:todo_server_named",
          kind: "todo",
          todo_id: "todo_server_named",
          label: "Server resolved prerequisite title",
          todo_ids: ["t1", "t2", "t3"],
          count: 3,
        },
        // Deleted / unloaded prerequisite todo -> never expose raw todo_... ID
        {
          key: "target:todo_missing1234",
          kind: "external_target",
          label: "todo_missing1234",
          todo_ids: ["t2", "t3"],
          count: 2,
        },
      ],
      undefined,
      todosById,
    );

    expect(parsed).toEqual([
      {
        key: "todo:todo_server_named",
        kind: "todo",
        todo_id: "todo_server_named",
        label: "Server resolved prerequisite title",
        todo_ids: ["t1", "t2", "t3"],
        count: 3,
      },
      {
        key: "target:todo_e8acf635da201b7a",
        kind: "todo",
        todo_id: "todo_e8acf635da201b7a",
        label: "Stabilize Steam extension pipeline",
        todo_ids: ["t1", "t2"],
        count: 2,
      },
      {
        key: "target:todo_missing1234",
        kind: "todo",
        todo_id: "todo_missing1234",
        label: t("todos.shared_blockers_unknown_todo"),
        todo_ids: ["t2", "t3"],
        count: 2,
      },
    ]);
    for (const blocker of parsed) {
      expect(blocker.label).not.toMatch(/^todo_/i);
    }
  });
});

describe("shared blocker selection & filtering", () => {
  const blockers: SharedBlocker[] = [
    {
      key: "pr:42",
      kind: "pull_request",
      label: "org/repo#42",
      url: "https://github.com/org/repo/pull/42",
      todo_ids: ["t1", "t2"],
      count: 2,
    },
    {
      key: "target:deploy",
      kind: "external_target",
      label: "staging deploy",
      todo_ids: ["t2", "t3"],
      count: 2,
    },
  ];
  const todos = [{ id: "t1" }, { id: "t2" }, { id: "t3" }, { id: "t4" }];

  it("resolves active key and auto-clears stale key when blocker disappears", () => {
    expect(resolveSelectedBlockerKey(null, blockers)).toBeNull();
    expect(resolveSelectedBlockerKey("pr:42", blockers)).toBe("pr:42");
    expect(resolveSelectedBlockerKey("gone:99", blockers)).toBeNull();
    expect(findSelectedBlocker("pr:42", blockers)?.label).toBe("org/repo#42");
    expect(findSelectedBlocker("gone:99", blockers)).toBeUndefined();
  });

  it("filters todos to the selected blocker's todo_ids and returns all when cleared or stale", () => {
    expect(filterTodosBySharedBlocker(todos, blockers, null).map((t) => t.id)).toEqual(["t1", "t2", "t3", "t4"]);
    expect(filterTodosBySharedBlocker(todos, blockers, "pr:42").map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(filterTodosBySharedBlocker(todos, blockers, "target:deploy").map((t) => t.id)).toEqual(["t2", "t3"]);
    expect(filterTodosBySharedBlocker(todos, blockers, "gone:99").map((t) => t.id)).toEqual(["t1", "t2", "t3", "t4"]);
    expect(matchesSharedBlocker({ id: "t1" }, blockers[0])).toBe(true);
    expect(matchesSharedBlocker({ id: "t4" }, blockers[0])).toBe(false);
    expect(matchesSharedBlocker({ id: "t4" }, undefined)).toBe(true);
  });
});
