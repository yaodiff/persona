// @vitest-environment jsdom
// What Persona sent to a task's agent (#1542): a folded line on the left, not the user's bubble, that opens to the whole
// text with Copy; the user's own word stays a bubble.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type FeedItem } from "../../lib/feed";
import { closePanel } from "../../lib/panel";
import { refresh } from "../../lib/store";
import { SentLine, sentPreview } from "./Sent";
import { FeedLineView } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const HANDOFF = "把后台同步的内存降下来。\n\n完成标准：\n- 峰值内存降一半以上";
const now = Date.now();
const iso = (ms: number) => new Date(now + ms).toISOString();
const TODO = { id: "todo-1", title: "降低内存占用", created_at: iso(-60_000), updated_at: iso(-60_000) };
const FEED: FeedItem[] = [
  { id: "f-card", conversation: "c1", at: iso(-60_000), kind: "task", ref: TODO.id },
  { id: "f-handoff", conversation: "c1", at: iso(-59_990), kind: "routed", ref: TODO.id, body: { text: HANDOFF, from: "assistant" } },
  { id: "f-rework", conversation: "c1", at: iso(-10_000), kind: "routed", ref: TODO.id, body: { text: "（督办：返工）\n补截图", from: "assistant", interrupted: true } },
];

let host: HTMLDivElement;
let root: Root;
const click = (el: Element | null) => act(() => (el as HTMLElement).click());

beforeEach(async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) =>
      Promise.resolve(
        new Response(JSON.stringify(String(url).includes("/todos") ? { todos: [TODO] } : String(url).includes("/feed") ? { items: FEED } : {}), {
          headers: { "Content-Type": "application/json" },
        }),
      ),
    ),
  );
  await refresh();
  closePanel();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  closePanel();
  vi.unstubAllGlobals();
});

describe("Persona's messages to agents (#1542)", () => {
  it("are a folded line, not the user's bubble; the user's own word stays one", () => {
    const say = (body: Record<string, unknown>) =>
      renderToStaticMarkup(<FeedLineView item={{ id: "f3", conversation: "c1", at: iso(0), kind: "routed", ref: "todo_x", body }} />);
    const persona = say({ text: HANDOFF, from: "assistant" });
    expect(persona).toContain('data-testid="sent-line"');
    expect(persona).toContain("发给「");
    expect(persona).toContain("· 把后台同步的内存降下来。");
    expect(persona).not.toContain("峰值内存降一半以上");
    expect(persona).not.toContain("items-end");
    const user = say({ text: "顺便看看日志" });
    expect(user).not.toContain("sent-line");
    expect(user).toContain("items-end");
    expect(user).toContain("对「");
    expect(say({ text: "先停", from: "assistant", interrupted: true })).toContain("打断「");
  });

  it("opens to the whole text with Copy", () => {
    act(() => root.render(<SentLine item={FEED[1]} title="降低内存占用" />));
    const toggle = host.querySelector('[aria-expanded="false"]');
    expect(host.textContent).not.toContain("峰值内存降一半以上");
    click(toggle);
    expect(host.querySelector('[aria-expanded="true"]')).not.toBeNull();
    expect(host.querySelector('[data-testid="sent-detail"]')?.textContent).toContain("峰值内存降一半以上");
    expect(host.textContent).toContain("复制");
  });

  it("draws Persona's Markdown: the folded line without its marks, the opened text rendered (audit item 7)", () => {
    const md = "**先别动缓存层。**\n1. 网络层的重试和超时先补测试，用 `LocalStubServer`；\n2. 覆盖率按 **80%** 算，生成的代码不算；";
    const item: FeedItem = { id: "f-md", conversation: "c1", at: iso(0), kind: "routed", ref: TODO.id, body: { text: md, from: "assistant" } };
    act(() => root.render(<SentLine item={item} title="补齐网络层的单元测试" />));
    const line = host.querySelector('[aria-expanded="false"]')!;
    expect(line.textContent).toBe("· 先别动缓存层。");
    click(line);
    const detail = host.querySelector('[data-testid="sent-detail"]')!;
    expect(detail.querySelector("strong")?.textContent).toBe("先别动缓存层。");
    expect(detail.querySelector("code")?.textContent).toBe("LocalStubServer");
    expect(detail.querySelectorAll("ol > li")).toHaveLength(2);
    expect(detail.textContent).not.toContain("**");
    expect(detail.textContent).not.toContain("`");
  });

  it("keeps the lines Persona wrote apart, as its replies do", () => {
    const open = (text: string) => {
      const item: FeedItem = { id: `f-${text.length}`, conversation: "c1", at: iso(0), kind: "routed", ref: TODO.id, body: { text, from: "assistant" } };
      act(() => root.render(<SentLine key={item.id} item={item} title="降低内存占用" />));
      click(host.querySelector('[aria-expanded="false"]'));
      return host.querySelector('[data-testid="sent-detail"]')!;
    };
    // the drawn lines: a <br> is a line break (the newline Markdown keeps after it is not one on screen)
    const lines = (el: Element) => el.innerHTML.replaceAll("\n", "");
    // a hand-off written a line per point: three lines, not one run-on paragraph
    const steps = open("先看日志，找出超时的请求\n再把重试次数从 3 改成 5\n改完跑一次全量测试");
    const para = steps.querySelector("p")!;
    expect(para.querySelectorAll("br")).toHaveLength(2);
    expect(lines(para)).toBe("先看日志，找出超时的请求<br>再把重试次数从 3 改成 5<br>改完跑一次全量测试");
    // the supervisor's rework note (server/internal/prompts/assistant/supervise_rework.md): its first two lines and the
    // note under "要你做的：" each on a line of their own, the checks a list
    const rework = open(
      "（督办：验收没通过，第 1 轮返工，最多 2 轮）\n逐条验收：\n- ✅ PR 合并到 main\n- ❌ 横屏截图贴进 PR：只有竖屏截图\n\n要你做的：\n补一张横屏的截图贴进 PR，再告诉我。\n把缺的补上。",
    );
    const [head, ask] = rework.querySelectorAll("p");
    expect(lines(head)).toBe("（督办：验收没通过，第 1 轮返工，最多 2 轮）<br>逐条验收：");
    expect([...rework.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["✅ PR 合并到 main", "❌ 横屏截图贴进 PR：只有竖屏截图"]);
    expect(lines(ask)).toBe("要你做的：<br>补一张横屏的截图贴进 PR，再告诉我。<br>把缺的补上。");
  });

  it("previews the first line that has words, a heading or list item without its marks", () => {
    expect(sentPreview("\n\n## 先看 `README`\n正文")).toBe("先看 README");
    expect(sentPreview("```\n```\n- **补** 测试")).toBe("补 测试");
    expect(sentPreview("把后台同步的内存降下来。")).toBe("把后台同步的内存降下来。");
    expect(sentPreview("")).toBe("");
  });
});
