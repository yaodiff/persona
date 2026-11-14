// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { EventMark } from "./InboxEvent";

const item = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "event-1", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "A matter",
  preview: "Latest line", count: 1, tier: "action", ...patch,
});
const mark = (it: InboxItem) => renderToStaticMarkup(<EventMark it={it} />);

type NowCase = {
  input: { item: InboxItem; follow_by_lang?: Record<string, Partial<NonNullable<InboxItem["follow"]>>> };
  expected: { now: string; text: Record<string, string> };
};
const shared = JSON.parse(readFileSync(join(__dirname, "../../../../spec/fixtures/inbox/now.json"), "utf8")) as { cases: NowCase[] };
const representatives = new Map<string, NowCase>();
for (const c of shared.cases) {
  // The text tests in lib/inbox-now cover clock formatting; one clock-free example per key is enough for this row.
  if (!representatives.has(c.expected.now) && !c.expected.text["zh-Hans"].includes("{time}")) representatives.set(c.expected.now, c);
}

const NEEDS_YOU = new Set(["confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "failed", "needs_you"]);
const iconFor = (now: string) => now === "waiting" ? "clock" : now === "queued" || now === "looking" || now === "agent_working" ? "spinner" : NEEDS_YOU.has(now) ? "needs-you" : now.startsWith("done_") ? "check" : "none";

describe("EventMark and the manager's now key", () => {
  it("covers every shared state key", () => {
    expect(representatives.size).toBe(20);
  });

  for (const [now, c] of representatives) {
    it(`draws ${now} with its shared words and ${iconFor(now)} mark`, () => {
      const follow = c.input.follow_by_lang?.["zh-Hans"];
      const html = mark({ ...c.input.item, follow: { ...c.input.item.follow, ...(follow ?? {}) } });
      const icon = iconFor(now);
      expect(html).toContain(`data-event-mark="now"`);
      expect(html).toContain(`data-event-icon="${icon}"`);
      expect(html).toContain(c.expected.text["zh-Hans"]);
      if (icon === "needs-you") {
        expect(html).toContain('data-shape="alert"');
        expect(html).toContain('data-state="needs_you"');
      } else if (icon === "none") {
        expect(html).not.toContain("<svg");
        expect(html).not.toContain("status-mark");
      } else if (icon === "spinner") {
        expect(html).toContain("border-t-ink-2");
      } else {
        expect(html).toContain("<svg");
      }
    });
  }

  it("keeps every legacy mark byte-identical without now, including for an unknown key", () => {
    const legacy = [
      item({ suggestion: { version: 1, kind: "reply", text: "周五可以。", handback: false } }),
      item({ urgent: true, handling: { owner: "manager", state: "looking" } }),
      item({ handling: { owner: "manager", state: "failed", error: "处理超时了" } }),
      item({ handling: { owner: "manager", state: "waiting", since: "2026-01-01T09:00:00Z" } }),
      item({ handling: { owner: "manager", state: "done" } }),
      item({ done_at: "2026-01-01T10:00:00Z", done_by: "manager" }),
      item({ done_at: "2026-01-01T10:00:00Z", done_by: "user" }),
      item(),
    ];
    for (const old of legacy) {
      expect(mark({ ...old, now: "a_future_state" })).toBe(mark(old));
      expect(mark({ ...old, now: "tomorrow" })).toBe(mark(old));
    }
    expect(mark(legacy[0])).toContain("建议回复");
    expect(mark(legacy[0])).toContain("周五可以。");
    expect(mark(legacy[1])).toContain("AI 正在准备建议");
    expect(mark(legacy[2])).toContain("AI 没处理完");
    expect(mark(legacy[3])).toContain("等对方回复");
    expect(mark(legacy[5])).toContain("助手处理了");
  });

  it("uses the composed waiting words when the key is known", () => {
    const html = mark(item({
      now: "waiting",
      handling: { owner: "manager", state: "waiting", since: "2026-01-01T09:00:00Z" },
      follow: { who: "张三确认日期", next: "问周五行不行" },
    }));
    expect(html).toContain("等张三确认日期：问周五行不行");
    expect(html).not.toContain("等对方回复");
    expect(html).toContain('data-event-icon="clock"');
  });

  it("shows a suggestion's confirmation state, not its title and first line", () => {
    const html = mark(item({
      now: "confirm_reply",
      handling: { owner: "manager", state: "done", since: "2026-01-01T09:00:00Z" },
      suggestion: { version: 1, kind: "reply", text: "周五可以。", handback: false },
    }));
    expect(html).not.toContain("建议回复");
    expect(html).not.toContain("周五可以。");
    expect(html).toContain("等你确认回复");
    expect(html).toContain('data-event-icon="needs-you"');
  });

  it("uses the known failure reason and the shared words without handling", () => {
    const failed = mark(item({ now: "failed", handling: { owner: "manager", state: "failed", error: "处理超时了" } }));
    expect(failed).toContain("AI 没处理完：处理超时了");
    expect(failed).toContain("text-red");
    expect(failed).toContain("--mark-color:var(--red)");
    expect(mark(item({ now: "check_due", follow: { next: "问周五行不行" } }))).toContain("到了再看的时间：问周五行不行");
  });
});
