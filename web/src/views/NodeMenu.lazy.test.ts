/// <reference types="node" />
// A computer's, folder's or Agent's menu opens on a click: NodeMenu comes in a chunk of its own on the first one, so it does
// not ride on every first screen through the sidebar (Sidebar.tsx) or an agent row's menu (AgentMenus.tsx).
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const sidebar = read("./Sidebar.tsx");
const agentMenus = read("./AgentMenus.tsx");
/** A value import of ./NodeMenu (`import type` only brings types, which the build drops). */
const STATIC_NODE_MENU = /^import(?!\s+type\b)[^;]*from\s+["']\.\/NodeMenu["']/m;

describe("NodeMenu off the first screen", () => {
  it("is imported lazily by the first screen's two callers", () => {
    expect(sidebar).not.toMatch(STATIC_NODE_MENU);
    expect(agentMenus).not.toMatch(STATIC_NODE_MENU);
    // the sidebar takes the lazy one from AgentMenus, which loads the chunk on the first render
    expect(sidebar).toMatch(/^import \{[^}]*\bNodeMenu\b[^}]*\} from "\.\/AgentMenus";$/m);
    expect(agentMenus).toMatch(/lazy\(\(\) => import\("\.\/NodeMenu"\)\.then\(\(module\) => \(\{ default: module\.NodeMenu \}\)\)\)/);
  });

  it("tells a static import from a type-only one", () => {
    expect('import { NodeMenu } from "./NodeMenu";').toMatch(STATIC_NODE_MENU);
    expect('import { NodeMenu, type NodeMenuProps } from "./NodeMenu";').toMatch(STATIC_NODE_MENU);
    expect('import type { NodeMenuProps } from "./NodeMenu";').not.toMatch(STATIC_NODE_MENU);
  });

  it("resolves the dynamic import to the existing component", async () => {
    const m = await import("./NodeMenu");
    expect(typeof m.NodeMenu).toBe("function");
  }, 60_000);
});
