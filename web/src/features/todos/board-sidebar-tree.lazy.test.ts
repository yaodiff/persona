/// <reference types="node" />
// Keep the Lab's By-to-do implementation separate from both the cold sidebar and the To-dos page.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("board-sidebar-tree", () => {
  it("keeps the Todos page's board consumers off the first screen", () => {
    expect(read("../../views/Sidebar.tsx")).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["'](\.\.\/)+features\/todos\/board-consumer["']/m);
    expect(read("./board-sidebar-tree.ts")).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/board-consumer["']/m);
  });

  it("keeps the v2 board adapter and goal folding behind the sidebar's dynamic import", () => {
    const sidebar = read("../../views/Sidebar.tsx");
    expect(/^import\s+(?!type\b)[^;]*from\s+["'](\.\.\/)+features\/todos\/board-sidebar-tree["']/m.test(sidebar)).toBe(false);
    expect(sidebar).toMatch(/import\(["']\.\.\/features\/todos\/board-sidebar-tree["']\)/);
    expect(read("../../lib/todo-tree.ts")).not.toContain("foldTreeGoals");
  });

  it("keeps opening To-dos from loading the sidebar chunk", () => {
    expect(read("./board-consumer.ts")).not.toContain("./board-sidebar-tree");
  });
});
