// The "Assistant cards" page's preview (#2023): every example reads, a press says what it would send, an html card is its text
// only, and the page has no way to reach a conversation.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { example, EXAMPLE_ORDER, previewAction, readExample, submitSummary } from "./assistant-examples";
import { fieldValues } from "../lib/extension-ui";
import { Preview } from "./pages/Assistant";

// the admin router and the design system read the browser when they load
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.addEventListener ??= () => {};
  g.removeEventListener ??= () => {};
  g.matchMedia ??= () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

describe("the example cards", () => {
  it("all read, with their text fallback", () => {
    for (const id of [...EXAMPLE_ORDER, "progress-updated"]) {
      const ex = example(id);
      expect(ex, id).not.toBeNull();
      expect(ex!.fallback, id).not.toBe("");
      if (id === "html") expect(ex!.view).toBeNull();
      else expect(ex!.view?.sections.length, id).toBeGreaterThan(0);
    }
  });

  it("an html component anywhere, a named page too, makes the card its fallback", () => {
    const raw = { fallback: "文字", sections: [{ items: [{ type: "text", text: "a" }] }], pages: { more: { sections: [{ items: [{ type: "html", html: "<p>x</p>" }] }] } } };
    expect(readExample("x", raw)).toMatchObject({ html: true, view: null, fallback: "文字" });
  });

  it("finds an action's reply, on a row or a button", () => {
    const decision = example("decision")!;
    expect(previewAction(decision.raw, "choose-b")).toEqual({ sends: true, text: "选 B：队列去重" });
    expect(previewAction(example("form")!.raw, "submit")).toEqual({ sends: true, text: "按这些信息安排任务" });
    expect(previewAction(decision.raw, "nope")).toBeNull();
  });

  it("describes navigation without turning opaque ids into reply text", () => {
    const compare = example("compare")!;
    expect(previewAction(compare.raw, "details")).toEqual({ sends: false, text: "在真实对话中，这里会打开详情页「详细依据」。" });
    expect(previewAction(compare.raw, "back")).toEqual({ sends: false, text: "在真实对话中，这里会返回上一页。" });
    expect(previewAction(compare.raw, "unknown")).toBeNull();
    expect(previewAction(example("decision")!.raw, "choose-b")).toEqual({ sends: true, text: "选 B：队列去重" });
    expect(previewAction({ sections: [{ items: [{ action: { id: "open", open: "https://example.com" } }] }] }, "open"))
      .toEqual({ sends: false, text: "在真实对话中，这里会在 App 内打开「https://example.com」。" });
  });

  it("reads a submitted form as people would", () => {
    const form = example("form")!;
    const values = { ...fieldValues(form.view), task: "整理读书笔记", priority: "high" };
    expect(submitSummary(form.view!, values)).toEqual(["任务名称：整理读书笔记", "优先级：高", "完成后通知：是"]);
    expect(submitSummary(form.view!, fieldValues(form.view))[0]).toBe("任务名称：（空）");
  });
});

describe("the preview", () => {
  it("draws each example's card, and an html one as its text without a frame or a script", () => {
    for (const id of [...EXAMPLE_ORDER, "progress-updated"]) {
      const html = renderToStaticMarkup(<Preview name={id} />);
      expect(html, id).toContain(`data-preview="${id}"`);
      expect(html, id).not.toMatch(/<iframe|<script|srcdoc/i);
    }
    const html = renderToStaticMarkup(<Preview name="html" />);
    expect(html).toContain(example("html")!.fallback);
    expect(html).not.toContain("persona.reply");
    expect(renderToStaticMarkup(<Preview name="decision" />)).toContain("B 队列去重");
  });

  it("has no way to reach a conversation: the page calls no hub API at all", () => {
    const dir = fileURLToPath(new URL(".", import.meta.url));
    for (const file of ["pages/Assistant.tsx", "assistant-examples.ts"]) {
      const src = readFileSync(dir + file, "utf8");
      expect(src, file).not.toMatch(/\bfetch\(|XMLHttpRequest|WebSocket|EventSource|sendBeacon|adminApi|\/api\/v1\/assistant|openLink|window\.open/);
    }
    expect(readFileSync(dir + "pages/Assistant.tsx", "utf8")).not.toMatch(/from "\.\.\/api"/);
  });
});
