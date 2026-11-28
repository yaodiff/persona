import type { Page } from "@playwright/test";
import { expect, openSplitPanelAgent, test } from "./fixtures/mock-hub";
import {
  expectInsideViewport,
  expectNoActiveBackdropFilter,
  expectNoHorizontalOverflow,
  expectSingleLineButtons,
} from "./fixtures/assertions";

async function readShellGlassState(page: Page) {
  return page.evaluate(() => {
    const read = (sel: string) => {
      const el = document.querySelector<HTMLElement>(sel);
      return el ? getComputedStyle(el).backdropFilter : null;
    };
    return {
      surfaceStyle: document.documentElement.dataset.surfaceStyle ?? null,
      desktopGlass: document.documentElement.dataset.desktopGlass ?? null,
      shell: read("[data-app-shell]"),
      main: read("[data-app-shell] > main"),
      aside: read("aside[data-side-panel]"),
    };
  });
}

test.describe("Shell split pane & Frosted Glass invariants", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`desktop Frosted Glass split view stays aligned with no backdrop ghosting (${theme})`, async ({ openApp }) => {
      const page = await openApp({ theme, surfaceStyle: "liquid-glass", desktopBridge: true });
      await openSplitPanelAgent(page);

      const attrs = await page.evaluate(() => ({
        surfaceStyle: document.documentElement.dataset.surfaceStyle ?? null,
        desktopGlass: document.documentElement.dataset.desktopGlass ?? null,
      }));
      expect(attrs).toEqual({ surfaceStyle: "liquid-glass", desktopGlass: "1" });

      await expectNoActiveBackdropFilter(page);
      await expectNoHorizontalOverflow(page);

      const sidebar = page.locator("[data-sidebar-root]").first();
      const main = page.locator("[data-app-shell] > main").first();
      const aside = page.locator("aside[data-side-panel]").first();

      await expectInsideViewport(sidebar);
      await expectInsideViewport(main);
      await expectInsideViewport(aside);

      await expect
        .poll(async () => {
          const [mainBox, asideBox] = await Promise.all([main.boundingBox(), aside.boundingBox()]);
          if (!mainBox || !asideBox) return false;
          return mainBox.width > 300 && asideBox.width > 280 && mainBox.x + mainBox.width <= asideBox.x + 2;
        })
        .toBe(true);

      await expectSingleLineButtons(main.locator("header").first());
      await expectSingleLineButtons(aside.locator("header").first());

      await expect(sidebar).toMatchAriaSnapshot(`
        - complementary:
          - link "Main chat":
            - /url: /
          - button "Pages" [expanded]
          - link /Inbox/
          - button "Agents" [expanded]
          - radiogroup "Group agents by"
      `);
    });
  }

  test("web-only Frosted Glass preserves CSS blur when desktop bridge is absent", async ({ openApp }) => {
    const page = await openApp({ theme: "light", surfaceStyle: "liquid-glass", desktopBridge: false });
    await openSplitPanelAgent(page);

    const glass = await readShellGlassState(page);
    expect(glass.surfaceStyle).toBe("liquid-glass");
    expect(glass.desktopGlass).toBeNull();
    expect(glass.shell).toContain("blur(");
    expect(glass.main).toContain("blur(");
    expect(glass.aside).toContain("blur(");
    await expectNoHorizontalOverflow(page);
  });

  test("default opaque appearance keeps glass attributes and blur disabled", async ({ openApp }) => {
    const page = await openApp({ theme: "dark", surfaceStyle: "default", desktopBridge: true });
    await openSplitPanelAgent(page);

    const state = await readShellGlassState(page);
    expect(state.surfaceStyle).toBeNull();
    expect(state.desktopGlass).toBeNull();
    expect(state.shell).toBe("none");
    expect(state.main).toBe("none");
    expect(state.aside).toBe("none");
    await expectNoHorizontalOverflow(page);
  });
});
