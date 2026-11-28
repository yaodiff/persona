import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { ContextGroup, contextSummary } from "./ContextGroup";

// The disclosure behavior is under test, not how quickly its real lazy chunk arrives.
vi.mock("./ContextDetails", async (importOriginal) => {
  await new Promise((resolve) => setTimeout(resolve, 1200));
  return await importOriginal<typeof import("./ContextDetails")>();
});

const cs = [
  { kind: "instructions", title: "给 Agent 的说明", text: "## 怎么做" },
  { kind: "context", title: "前面的聊天", text: "10-31 09:00 张三：构建挂了" },
  { kind: "doc", title: "文档 · 手册", url: "https://example.com/d", text: "第一步" },
];

describe("ContextGroup", () => {
  beforeAll(async () => {
    // Load before any component mounts, so chunk completion cannot escape an act scope.
    await import("./ContextDetails");
  }, 60_000);

  it("summarizes one context by its title and several by their count", () => {
    expect(contextSummary(cs)).toBe("上下文 · 3 项");
    expect(contextSummary([cs[1]])).toBe("上下文 · 前面的聊天");
    expect(contextSummary([{ kind: "edit", title: "段落", text: "x" }])).toBe("待修改内容 · 段落");
  });

  it("starts folded: one row, no titles or text", () => {
    const html = renderToStaticMarkup(<ContextGroup contexts={cs} />);
    expect(html).toContain("上下文 · 3 项");
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("前面的聊天");
    expect(html).not.toContain("构建挂了");
  });

  it("loads selected-skill details on disclosure and keeps the existing summary reversible", async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const host = document.body.appendChild(document.createElement("div"));
    const root = createRoot(host);
    try {
      await act(async () => root.render(<ContextGroup contexts={[{ kind: "skill", title: "review", text: "project · .\nInspect boundaries", attrs: { ref: "opaque" } }]} />));
      expect(host.textContent).toContain("review");
      expect(host.textContent).not.toContain("Inspect boundaries");
      await act(async () => host.querySelector("button")!.click());
      await vi.waitFor(async () => {
        await act(async () => {});
        expect(host.textContent).toContain("Inspect boundaries");
      });
      expect(host.querySelector("button")?.getAttribute("aria-expanded")).toBe("true");
      await act(async () => host.querySelector("button")!.click());
      expect(host.querySelector("button")?.getAttribute("aria-expanded")).toBe("false");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
});
// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
