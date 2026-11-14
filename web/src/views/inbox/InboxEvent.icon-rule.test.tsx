// @vitest-environment jsdom
// The state icon rule of DECISIONS D3 "状态句前的图标" on both surfaces: the row's mark (EventMark) and the detail's state
// sentence (HandlingLine). The expectations below are written from the decision text, not from the implementation:
// waiting -> a still clock; queued / looking / agent_working -> the spinner; confirm_*, decide, follow_up_ask, check_due,
// failed, needs_you -> the app's existing Needs-you mark (ui/StatusMark, state "needs_you"); done_* -> a check; fyi and
// folded -> no leading icon.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { EventMark } from "./InboxEvent";

const TEXT = (JSON.parse(readFileSync(join(__dirname, "../../../../spec/fixtures/inbox/now.json"), "utf8")) as {
  cases: { input: { item: InboxItem; follow_by_lang?: Record<string, Partial<NonNullable<InboxItem["follow"]>>> }; expected: { now: string } }[];
}).cases;

/** D3's rule, one line per key group. */
const RULE: Record<string, string> = {
  waiting: "clock",
  queued: "spinner",
  looking: "spinner",
  agent_working: "spinner",
  confirm_reply: "needs-you",
  confirm_agent: "needs-you",
  decide: "needs-you",
  follow_up_ask: "needs-you",
  check_due: "needs-you",
  failed: "needs-you",
  needs_you: "needs-you",
  fyi: "none",
  folded: "none",
  done_replied: "check",
  done_manager: "check",
  done_user: "check",
  done_read: "check",
  done_dismissed: "check",
  done_muted: "check",
  done_superseded: "check",
};

/** One case per key, worded in the item's own fields (the text itself is covered by lib/inbox-now.test.ts). */
const oneCasePerKey = () => {
  const seen = new Map<string, InboxItem>();
  for (const c of TEXT) {
    const input = { ...c.input.item };
    const translated = c.input.follow_by_lang?.["zh-Hans"];
    if (translated) input.follow = { ...input.follow, ...translated };
    if (!seen.has(c.expected.now)) seen.set(c.expected.now, input);
  }
  return seen;
};

const mark = (it: InboxItem) => renderToStaticMarkup(<EventMark it={it} />);

describe("列表行的状态图标按 D3", () => {
  it("knows all 20 keys and no others", () => {
    expect([...oneCasePerKey().keys()].sort()).toEqual(Object.keys(RULE).sort());
  });

  for (const [now, icon] of Object.entries(RULE)) {
    it(`${now}: ${icon}`, () => {
      const item = oneCasePerKey().get(now)!;
      const html = mark(item);
      expect(html).toContain('data-event-mark="now"');
      expect(html).toContain(`data-event-icon="${icon}"`);
      if (icon === "needs-you") {
        // the one Needs-you mark the web already has, not a new one
        expect(html).toContain('data-state="needs_you"');
        expect(html).toContain('data-shape="alert"');
        expect(html).not.toContain("data-state-pill");
      }
      if (icon === "none") {
        expect(html).not.toContain("<svg");
        expect(html).not.toContain("status-mark");
      }
      if (icon === "check") expect(html).toContain("<svg");
      if (icon === "clock") expect(html).toContain("<svg");
      if (icon === "spinner") expect(html).toContain("border-t-ink-2");
    });
  }
});
