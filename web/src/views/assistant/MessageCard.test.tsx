import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { FeedItem } from "../../lib/feed";
import { draftTasks } from "./Tasks";
import MessageDraft from "./MessageDraft";

// The card "Reply drafted from the task's progress" (#1519): drawn from what the hub records on it, so a reload keeps it sent.
describe("a message card", () => {
  const card = (body: Record<string, unknown> = {}): FeedItem => ({
    id: "m1", conversation: "c1", at: new Date().toISOString(), kind: "message", ref: "example:1",
    body: { source: "github", sender: "reviewer", chat: "example-org/example-repo#12", text: "clearBadges 删记录时不广播？", draft: "确实是 bug，谢谢指出", ...body },
  });

  it("offers the draft to send while it is not sent", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card()} />);
    expect(html).toContain("根据任务进度起草的回复");
    expect(html).toContain("<textarea");
    expect(html).toContain("发送");
  });

  it("names the draft's editor by its visible heading (axe: label)", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card()} />);
    const by = html.match(/<textarea[^>]*aria-labelledby="([^"]+)"/)?.[1];
    expect(by).toBeTruthy();
    const at = html.indexOf(`<span id="${by}"`);
    expect(at).toBeGreaterThanOrEqual(0);
    expect(html.slice(at, html.indexOf("</span>", at))).toContain("根据任务进度起草的回复");
  });

  it("draws a token focus ring on the draft's editor, since the global ring covers only buttons and links", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card()} />);
    const cls = html.match(/<textarea[^>]*class="([^"]+)"/)?.[1] ?? "";
    expect(cls).toContain("focus-visible:ring-2");
    expect(cls).toContain("focus-visible:ring-accent");
  });

  it("shows what went out, and no 发送, once the hub says it was sent", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card({ sent_at: "2026-11-04T10:23:00Z", sent: "确实是 bug，谢谢指出（改过）" })} />);
    expect(html).toContain("已以你的名义回复");
    expect(html).toContain("确实是 bug，谢谢指出（改过）");
    expect(html).not.toContain("<textarea");
    expect(html).not.toContain("发送");
    expect(html).not.toContain("稍后");
  });

  it("keeps a recorded send without showing the draft as the sent text", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card({ sent_at: "2026-11-04T10:23:00Z" })} />);
    expect(html).toContain("已以你的名义回复");
    expect(html).not.toContain("确实是 bug，谢谢指出");
    expect(html).not.toContain("发送");
  });

  it("offers no 发送 or editor once the thing was handled elsewhere, and says nothing went out (#4797)", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card({ filed_at: "2026-11-05T09:00:00Z", draft: "确实是 bug，【预计几点前】修好" })} />);
    expect(html).toContain("该事项此前已处理，未发送任何消息");
    expect(html).not.toContain("已以你的名义回复");
    expect(html).not.toContain("<textarea");
    expect(html).not.toContain("<button");
    expect(html).not.toContain("草稿中仍有未填写的");
  });

  it("draws a handled card plainly, not as a draft waiting on you (#5095)", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card({ filed_at: "2026-11-05T09:00:00Z" })} />);
    expect(html).not.toContain("根据任务进度起草的回复");
    expect(html).not.toContain("bg-accent-tint");
    expect(html).toContain("已处理");
    expect(html).toContain("确实是 bug，谢谢指出");
  });

  it("holds a draft with a blank to fill in", () => {
    const html = renderToStaticMarkup(<MessageDraft tasks={draftTasks} item={card({ draft: "确实是 bug，【预计今晚 / 几点前】修好" })} />);
    expect(html).toContain("草稿中仍有未填写的「【预计今晚 / 几点前】」");
    expect(html).toMatch(/<button[^>]*disabled[^>]*>(?:(?!<\/button>).)*发送/);
  });
});
