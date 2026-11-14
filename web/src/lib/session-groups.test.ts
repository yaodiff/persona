import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { LIST_PAIRS_MAX, listPair, type ListCounts } from "./session-list";
import { catalogGroups, countGroups, groupMachines, holdsPlace, validCatalog, type SessionCatalog } from "./session-groups";

const catalog = (patch: Partial<SessionCatalog> = {}): SessionCatalog => ({
  revision: "r1", total: 12,
  projects: [
    { id: "git:git.example.com/team/app", name: "app", origin: "git.example.com/team/app", total: 6,
      places: [{ machine_id: "mac", path: "/Users/me/code/app", total: 4 }, { machine_id: "pc", path: "C:\\code\\App", total: 2 }] },
    { id: "dir:notes", name: "notes", total: 3, places: [{ machine_id: "mac", path: "/Users/me/notes", total: 2 }, { machine_id: "pc", path: "D:\\Notes", total: 1 }] },
    { id: "dir:empty", name: "empty", total: 0, places: [{ machine_id: "mac", path: "/Users/me/empty", total: 0 }] },
    { id: "", name: "", total: 3, places: [{ machine_id: "nas", path: "", total: 2 }, { machine_id: "mac", path: "", total: 1 }] },
  ],
  spaces: [
    { machine_id: "mac", id: "w1", name: "Release", total: 5 },
    { machine_id: "pc", id: "w1", name: "Release", total: 2 },
    { machine_id: "pc", id: "ws-9", name: "", total: 1 },
    { machine_id: "nas", id: "", name: "", total: 3 },
    { machine_id: "mac", id: "", name: "", total: 1 },
  ],
  ...patch,
});
const counts = (patch: Partial<ListCounts> = {}): ListCounts => ({ total: 9, machine: {}, project: {}, group: {}, state: {}, revision: "c1", ...patch });

describe("groups from the hub's catalog", () => {
  it("makes a repository one project with a place for each directory, and lists its sessions by those places", () => {
    const [app, notes, rest, ...more] = catalogGroups(catalog(), "project");
    expect(more).toEqual([]); // a project nothing in the scope is in is not a group
    expect(app).toMatchObject({ key: "git:git.example.com/team/app", name: "app", count: 6, needs: 0, origin: "git.example.com/team/app" });
    expect(app.places).toEqual([{ machine: "mac", path: "/Users/me/code/app", count: 4 }, { machine: "pc", path: "C:\\code\\App", count: 2 }]);
    expect(app.filters).toEqual({ place: [listPair("mac", "/Users/me/code/app"), listPair("pc", "C:\\code\\App")] });
    expect(notes).toMatchObject({ key: "dir:notes", name: "notes", count: 3 });
    expect(notes.origin).toBeUndefined();
    // the sessions in no directory: one last group, named, listed by each computer's empty path
    expect(rest).toMatchObject({ name: t("paged-agents.no_folder"), count: 3, rest: true, places: [] });
    expect(rest.filters).toEqual({ place: [listPair("nas", ""), listPair("mac", "")] });
    expect(new Set([app.key, notes.key, rest.key]).size).toBe(3);
  });

  it("keeps a space its computer's, and puts every computer's sessions in no space in one last group", () => {
    const groups = catalogGroups(catalog(), "group");
    expect(groups.map((g) => [g.name, g.count, g.places.map((p) => p.machine)])).toEqual([
      ["Release", 5, ["mac"]], ["Release", 2, ["pc"]], ["ws-9", 1, ["pc"]], [t("paged-agents.no_space"), 4, []],
    ]);
    expect(groups[0].filters).toEqual({ space: [listPair("mac", "w1")] });
    expect(groups[1].filters).toEqual({ space: [listPair("pc", "w1")] });
    expect(groups[0].key).not.toBe(groups[1].key);
    expect(groups[3]).toMatchObject({ rest: true, filters: { space: [listPair("nas", ""), listPair("mac", "")] } });
    expect(groupMachines(groups)).toBe(2);
  });

  it("counts on each group its sessions that need the user, as the hub's catalog says them", () => {
    const base = catalog();
    const waiting = catalog({
      projects: base.projects.map((p) => p.id === "" ? { ...p, needs_you: 1 } : p.id.startsWith("git:") ? { ...p, needs_you: 2 } : p),
      spaces: base.spaces.map((s) => s.machine_id === "pc" && s.id === "w1" ? { ...s, needs_you: 1 } : s.id === "" ? { ...s, needs_you: s.machine_id === "nas" ? 2 : 1 } : s),
    });
    expect(catalogGroups(waiting, "project").map((g) => g.needs)).toEqual([2, 0, 1]);
    expect(catalogGroups(waiting, "group").map((g) => g.needs)).toEqual([0, 1, 0, 3]); // every computer's sessions in no space together
    expect(catalogGroups(base, "group").map((g) => g.needs)).toEqual([0, 0, 0, 0]);
  });

  it("asks for no more places than the hub takes", () => {
    const places = Array.from({ length: LIST_PAIRS_MAX + 6 }, (_, i) => ({ machine_id: "mac", path: `/tmp/run-${i}/src`, total: 1 }));
    const [src] = catalogGroups(catalog({ projects: [{ id: "dir:src", name: "src", total: places.length, places }] }), "project");
    expect(src.filters.place).toHaveLength(LIST_PAIRS_MAX);
    expect(src.members).toHaveLength(places.length);
    expect(src.count).toBe(places.length);
  });

  it("tells whether a session is one of a group's by where it is", () => {
    const [app, , rest] = catalogGroups(catalog(), "project");
    expect(holdsPlace(app, "project", { machine: "pc", project: "C:\\code\\App", group: "w1" })).toBe(true);
    expect(holdsPlace(app, "project", { machine: "nas", project: "/Users/me/code/app", group: "" })).toBe(false); // the same path on another computer
    expect(holdsPlace(rest, "project", { machine: "nas", project: "", group: "" })).toBe(true);
    const [macRelease, pcRelease] = catalogGroups(catalog(), "group");
    expect(holdsPlace(macRelease, "group", { machine: "mac", project: "/x", group: "w1" })).toBe(true);
    expect(holdsPlace(pcRelease, "group", { machine: "mac", project: "/x", group: "w1" })).toBe(false);
  });

  it("takes only a catalog for one: an older hub answers the key with a page", () => {
    expect(validCatalog(catalog())).toBe(true);
    expect(validCatalog({ revision: "r", total: 0, projects: [], spaces: [] })).toBe(true);
    for (const other of [null, { sessions: [], remove: [], has_more: false, revision: "r", total: 0 }, { ...catalog(), projects: null },
      { ...catalog(), total: -1 }, { ...catalog(), projects: [{ id: "dir:x", name: "x", total: 1, places: [] }] },
      { ...catalog(), projects: [{ id: "dir:x", name: "x", total: 1, places: [{ machine_id: "", path: "/x", total: 1 }] }] },
      { ...catalog(), spaces: [{ machine_id: "mac", id: 1, name: "x", total: 1 }] },
      { ...catalog(), spaces: [{ machine_id: "mac", id: "w1", name: "x", total: 1, needs_you: "1" }] }]) {
      expect(validCatalog(other)).toBe(false);
    }
  });
});

describe("groups from a counts read", () => {
  it("lists computers the busiest first, each by its own filter", () => {
    const groups = countGroups(counts({ machine: { mac: 4, pc: 7, nas: 4, "": 2, gone: 0 } }), "machine", { needs: counts({ machine: { pc: 2 } }) });
    expect(groups.map((g) => [g.key, g.count, g.needs])).toEqual([["pc", 7, 2], ["mac", 4, 0], ["nas", 4, 0]]);
    expect(groups[0].filters).toEqual({ machine_id: "pc" });
    expect(groups[0].members).toBeUndefined();
    expect(holdsPlace(groups[0], "machine", { machine: "pc", project: "/x", group: "" })).toBe(true);
  });

  it("names a project by its folder and keeps its path, a Windows path by its own separator", () => {
    const groups = countGroups(counts({ project: { "/work/alpha": 2, "C:\\work\\Beta": 5, solo: 1 } }), "project");
    expect(groups.map((g) => [g.name, g.places.map((p) => p.path), g.filters])).toEqual([
      ["Beta", ["C:\\work\\Beta"], { project: "C:\\work\\Beta" }], ["alpha", ["/work/alpha"], { project: "/work/alpha" }], ["solo", [], { project: "solo" }],
    ]);
  });

  it("narrows a space id to the computer it is read on", () => {
    expect(countGroups(counts({ group: { w1: 3 } }), "group", { machine: "mac" })[0].filters).toEqual({ group: "w1", machine_id: "mac" });
    expect(countGroups(counts({ group: { w1: 3 } }), "group")[0].filters).toEqual({ group: "w1" });
    expect(countGroups(null, "group")).toEqual([]);
  });
});

describe("the sidebar's lists by to-do and by state (#6662)", () => {
  const todos = { order: ["todo_b", "todo_a"], title: (id: string) => ({ todo_a: "Ship 1.3", todo_b: "Review" } as Record<string, string>)[id] };

  it("has a section per open to-do in the To-dos page's order, those needing the user first, then Not on a to-do", () => {
    const by = counts({ todo: { todo_a: 2, todo_b: 1, todo_c: 4, "": 5, todo_gone: 0 } });
    const plain = countGroups(by, "todo", { todos });
    expect(plain.map((g) => [g.name, g.count, g.filters, Boolean(g.rest)])).toEqual([
      ["Review", 1, { todo: "todo_b" }, false],
      ["Ship 1.3", 2, { todo: "todo_a" }, false],
      // one the page does not know (another assistant's): after the known ones, by the word for a to-do
      [t("lib-agent-groups.seg_todo"), 4, { todo: "todo_c" }, false],
      [t("lib-todos.unassigned"), 5, { todo: "none" }, true],
    ]);
    const needing = countGroups(by, "todo", { todos, needs: counts({ todo: { todo_a: 1 } }) });
    expect(needing.map((g) => [g.filters.todo, g.needs])).toEqual([["todo_a", 1], ["todo_b", 0], ["todo_c", 0], ["none", 0]]);
    // an older hub counts no to-dos
    expect(countGroups(counts({}), "todo", { todos })).toEqual([]);
  });

  it("has a section per state with sessions, in the quick filters' order", () => {
    const groups = countGroups(counts({ state: { idle: 3, needs_you: 1, working: 2, offline: 0 } }), "state");
    expect(groups.map((g) => [g.key, g.count, g.filters, g.needs])).toEqual([["needs_you", 1, { state: "needs_you" }, 0], ["working", 2, { state: "working" }, 0], ["idle", 3, { state: "idle" }, 0]]);
  });

  it("finds the open session's section by its to-do and its state", () => {
    const [review, none] = countGroups(counts({ todo: { todo_b: 1, "": 1 } }), "todo", { todos });
    const place = { machine: "mac", project: "/code", group: "w1", todo: "todo_b", state: "working" };
    expect(holdsPlace(review, "todo", place)).toBe(true);
    expect(holdsPlace(none, "todo", place)).toBe(false);
    expect(holdsPlace(none, "todo", { ...place, todo: "" })).toBe(true);
    const [working] = countGroups(counts({ state: { working: 1 } }), "state");
    expect(holdsPlace(working, "state", place)).toBe(true);
  });
});
