// @vitest-environment jsdom
// The knowledge badges say what they are in their own text: phones have no hover, so nothing may live only in a title.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { KBEntry } from "../../lib/kb";
import { Badges, importanceLabel } from "./KBParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const project = (o: Partial<KBEntry> = {}): KBEntry => ({ kind: "projects", id: "p", name: "p", relevance: 0, ...o });
const pills = () => [...host.querySelectorAll("span")].map((s) => s.textContent);

describe("Badges", () => {
  it("spells out the importance, the project's kind and the relevance", () => {
    act(() => root.render(<Badges entry={project({ importance: "P0", relevance: 92, extra: "short · active" })} />));
    expect(pills()).toEqual(["P0 最重要", "短期", "与我相关 92 分"]);
  });

  it("names every level, and an unknown one with its raw value", () => {
    expect(["P0", "P1", "P2", "P3", "P9"].map(importanceLabel)).toEqual(["P0 最重要", "P1 重要", "P2 一般", "P3 次要", "重要程度 P9"]);
  });
});
