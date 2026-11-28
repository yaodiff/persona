import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import { walk, type MapNode } from "./kb-map";
import { below, buildTreeMap, canMove, depthOf, milestoneFlag, moved, treeOf, type TreeEntry } from "./kb-tree";

beforeAll(async () => {
  await initI18n("en");
});

const p = (id: string, extra: Partial<TreeEntry> = {}): TreeEntry => ({ kind: "projects", id, name: `Example ${id}`, relevance: 0, extra: "short · active", ...extra });
// garden › seeds › soil; docs on its own; old is merged, gone is not a project.
const list = [
  p("garden", { tags: ["home", "q4"] }), p("seeds", { parent: "garden", tags: ["home"], milestones: [{ id: "m1", title: "Beta", target: "2026-12-15", state: "open" }] }),
  p("soil", { parent: "seeds" }), p("docs"), p("old", { extra: "short · merged" }), p("gone", { extra: "short · ignored" }), p("orphan", { parent: "gone" }),
];

describe("treeOf", () => {
  it("keeps live projects and parents among them; a cycle or hidden parent leaves the top", () => {
    const { live, parents } = treeOf([...list, p("x", { parent: "y" }), p("y", { parent: "x" })]);
    expect(live.map((e) => e.id)).not.toContain("old");
    expect(parents.get("seeds")).toBe("garden");
    expect(parents.has("orphan")).toBe(false);
    expect(parents.has("x") && parents.has("y")).toBe(false);
  });
});

describe("canMove", () => {
  const { parents } = treeOf(list);
  it("refuses itself, a project below it and where it already is", () => {
    expect(canMove(parents, "garden", "garden").ok).toBe(false);
    expect(canMove(parents, "garden", "soil")).toMatchObject({ ok: false, why: "It’s under this project" });
    expect(canMove(parents, "seeds", "garden")).toMatchObject({ ok: false, why: "Already here" });
    expect(canMove(parents, "docs", null)).toMatchObject({ ok: false });
  });
  it("allows any depth but says when it goes past three levels", () => {
    expect(canMove(parents, "docs", "seeds")).toEqual({ ok: true });
    expect(canMove(parents, "docs", "soil")).toMatchObject({ ok: true, warn: "4 levels: deeper than the suggested 3" });
    // moving garden (3 levels tall) under docs makes 4
    expect(canMove(parents, "garden", "docs").warn).toContain("4 levels");
    expect(canMove(parents, "soil", null)).toEqual({ ok: true });
  });
  it("moves locally and counts depth", () => {
    const next = moved(parents, "docs", "soil");
    expect(depthOf(next, "docs")).toBe(4);
    expect([...below(next, "garden")].sort()).toEqual(["docs", "seeds", "soil"]);
    expect(moved(next, "docs", null).has("docs")).toBe(false);
  });
});

describe("milestoneFlag", () => {
  it("shows the next open date, else how they ended", () => {
    expect(milestoneFlag([{ id: "a", title: "A", target: "2026-12-20", state: "open" }, { id: "b", title: "B", target: "2026-12-01", state: "open" }])).toEqual({ text: "12-01", state: "open" });
    expect(milestoneFlag([{ id: "a", title: "A", state: "accepted" }])).toEqual({ text: "Accepted", state: "accepted" });
    expect(milestoneFlag([{ id: "a", title: "A", state: "accepted" }, { id: "b", title: "B", state: "not_accepted" }])?.state).toBe("not_accepted");
    expect(milestoneFlag([])).toBeNull();
  });
});

describe("buildTreeMap", () => {
  const { parents } = treeOf(list);
  const ids = (n: MapNode) => { const out: string[] = []; walk(n, (x) => out.push(x.id)); return out; };
  it("by hierarchy: top projects as branches, sub-projects under them, flags and counts", () => {
    const root = buildTreeMap(list, parents, "tree", new Map());
    expect(root.kids.map((k) => k.project?.id).sort()).toEqual(["docs", "garden", "orphan"]);
    const garden = root.kids.find((k) => k.project?.id === "garden")!;
    expect(garden.kids[0].project?.id).toBe("seeds");
    expect(garden.kids[0].flag).toEqual({ text: "12-15", state: "open" });
    expect(garden.kids[0].kids[0].project?.id).toBe("soil");
    expect(garden.meta).toBe("1 sub-project");
    expect(root.meta).toBe("5 projects");
    expect(ids(root)).not.toContain("p:old");
  });
  it("by tag: a project under each of its tags, untagged last, with where it sits", () => {
    const root = buildTreeMap(list, parents, "tag", new Map());
    expect(root.kids.map((k) => k.title)).toEqual(["#home", "#q4", "No tag"]);
    const home = root.kids[0];
    expect(home.kids.map((k) => k.id)).toEqual(["p:garden@home", "p:seeds@home"]);
    expect(home.kids[1].where).toBe("Example garden");
    expect(home.kids[0].meta).toBe("Also under 1 more tag");
  });
  it("keeps what the user closed", () => {
    const root = buildTreeMap(list, parents, "tree", new Map([["p:garden", true]]));
    expect(root.kids.find((k) => k.project?.id === "garden")!.collapsed).toBe(true);
  });
});
