import { AT, expect, test, type MockInboxItem } from "./fixtures/mock-hub";
import {
  expectInsideViewport,
  expectNoActiveBackdropFilter,
  expectNoHorizontalOverflow,
  expectSingleLineButtons,
  parseCssAlpha,
} from "./fixtures/assertions";

const SCROLL_INBOX_ITEMS: MockInboxItem[] = [
  ...Array.from({ length: 12 }, (_, i) => ({
    id: `lark-${i + 1}`,
    source: "lark",
    kind: "dm",
    account: "feishu",
    account_name: "Lark",
    title: `Lark thread ${i + 1}`,
    preview: `Follow up item ${i + 1} for scroll sticky header verification.`,
    count: 1,
    updated_at: AT,
    tier: "action" as const,
    version: `v-lark-${i + 1}`,
  })),
  ...Array.from({ length: 12 }, (_, i) => ({
    id: `mail-${i + 1}`,
    source: "mail",
    kind: "mail",
    account: "acc1",
    account_name: "me@example.com",
    title: `Mail summary ${i + 1}`,
    preview: `Status digest ${i + 1} for sticky header contrast check.`,
    count: 1,
    updated_at: AT,
    tier: "action" as const,
    version: `v-mail-${i + 1}`,
  })),
];

test.describe("Inbox sticky group headers & toolbar wrapping", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`renders opaque bg-page/95 sticky group headers with no horizontal overflow (${theme})`, async ({ openApp }) => {
      const page = await openApp({
        theme,
        surfaceStyle: "liquid-glass",
        desktopBridge: true,
        path: "/inbox",
        inboxItems: SCROLL_INBOX_ITEMS,
      });

      const bar = page.locator("[data-inbox-bar]");
      await expect(bar).toBeVisible();

      const stickyHeader = page.locator("[data-group] > .sticky").first();
      await expect(stickyHeader).toBeVisible();

      const classes = await stickyHeader.getAttribute("class");
      expect(classes).toContain("bg-page/95");
      expect(classes).not.toContain("bg-canvas/95");

      const headerStyles = await stickyHeader.evaluate((el) => {
        const cs = getComputedStyle(el);
        return {
          position: cs.position,
          top: cs.top,
          zIndex: Number.parseInt(cs.zIndex, 10) || 0,
          backgroundColor: cs.backgroundColor,
        };
      });
      expect(headerStyles.position).toBe("sticky");
      expect(headerStyles.top).toBe("0px");
      expect(headerStyles.zIndex).toBeGreaterThanOrEqual(10);
      expect(parseCssAlpha(headerStyles.backgroundColor)).toBeGreaterThanOrEqual(0.85);

      const scrollState = await page.evaluate(() => {
        const scroller = document.querySelector<HTMLElement>("[data-inbox-list-scroll]");
        if (!scroller) return null;
        const maxScroll = scroller.scrollHeight - scroller.clientHeight;
        scroller.scrollTop = Math.min(260, Math.max(120, Math.floor(maxScroll * 0.6)));
        const header = document.querySelector<HTMLElement>("[data-group] > .sticky");
        if (!header) return null;
        const scrollerRect = scroller.getBoundingClientRect();
        const headerRect = header.getBoundingClientRect();
        return {
          scrollHeight: scroller.scrollHeight,
          clientHeight: scroller.clientHeight,
          scrollTop: scroller.scrollTop,
          pinnedDelta: Math.abs(headerRect.top - scrollerRect.top),
        };
      });
      expect(scrollState).not.toBeNull();
      expect(scrollState!.scrollHeight).toBeGreaterThan(scrollState!.clientHeight + 100);
      expect(scrollState!.scrollTop).toBeGreaterThan(100);
      expect(scrollState!.pinnedDelta).toBeLessThanOrEqual(4);

      await expectInsideViewport(stickyHeader);
      await expectNoActiveBackdropFilter(page);
      await expectNoHorizontalOverflow(page, '[data-inbox-list-scroll], [aria-label="Inbox list"], [data-app-shell] > main');
      await expectSingleLineButtons(page.locator("[data-app-shell] > main header").first());
    });
  }
});
