import { expect, test } from "./fixtures/mock-hub";
import {
  expectInsideViewport,
  expectNoHorizontalOverflow,
  expectSingleLineButtons,
  parseCssAlpha,
} from "./fixtures/assertions";

test.describe("Mobile narrow viewport (390x844)", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`keeps header, composer, and sidebar drawer within 390x844 viewport (${theme})`, async ({ openApp }) => {
      const page = await openApp({
        theme,
        surfaceStyle: "default",
        desktopBridge: false,
        viewport: { width: 390, height: 844 },
      });

      await expectNoHorizontalOverflow(page);

      const main = page.locator("[data-app-shell] > main").first();
      await expect(main).toHaveJSProperty("inert", false);

      const header = main.locator("header").first();
      await expectSingleLineButtons(header);

      const textarea = main.locator("textarea").first();
      await expectInsideViewport(textarea);

      const sidebarToggle = header.getByRole("button", { name: /^Sidebar/ }).first();
      await expect(sidebarToggle).toBeVisible();
      await sidebarToggle.click();

      const drawer = page.locator('[data-sidebar-column][role="dialog"][aria-modal="true"]');
      await expect(drawer).toBeVisible();
      await expectInsideViewport(drawer);
      await expect(main).toHaveJSProperty("inert", true);

      const drawerBox = await drawer.boundingBox();
      const viewport = page.viewportSize()!;
      expect(drawerBox).not.toBeNull();
      const clickX = Math.min(viewport.width - 10, drawerBox!.x + drawerBox!.width + 20);
      const clickY = drawerBox!.y + 120;
      await page.mouse.click(clickX, clickY);

      await expect(page.locator('[data-sidebar-column][aria-modal="true"]')).toHaveCount(0);
      await expect(main).toHaveJSProperty("inert", false);
      await expectNoHorizontalOverflow(page);
    });
  }

  test("keeps mobile drawer background opaque under Frosted Glass", async ({ openApp }) => {
    const page = await openApp({
      theme: "dark",
      surfaceStyle: "liquid-glass",
      desktopBridge: false,
      viewport: { width: 390, height: 844 },
    });

    const main = page.locator("[data-app-shell] > main").first();
    const sidebarToggle = main.locator("header").first().getByRole("button", { name: /^Sidebar/ }).first();
    await sidebarToggle.click();

    const drawer = page.locator('[data-sidebar-column][role="dialog"][aria-modal="true"]');
    await expect(drawer).toBeVisible();
    await expectInsideViewport(drawer);
    await expect(main).toHaveJSProperty("inert", true);

    const drawerBg = await drawer.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(parseCssAlpha(drawerBg)).toBeGreaterThanOrEqual(0.85);

    const drawerBox = await drawer.boundingBox();
    const viewport = page.viewportSize()!;
    expect(drawerBox).not.toBeNull();
    await page.mouse.click(Math.min(viewport.width - 10, drawerBox!.x + drawerBox!.width + 20), drawerBox!.y + 120);

    await expect(page.locator('[data-sidebar-column][aria-modal="true"]')).toHaveCount(0);
    await expect(main).toHaveJSProperty("inert", false);
    await expectNoHorizontalOverflow(page);
  });
});
