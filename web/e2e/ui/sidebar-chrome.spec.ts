import { expect, test } from "./fixtures/mock-hub";
import { expectInsideViewport, expectNoHorizontalOverflow } from "./fixtures/assertions";

test.describe("Sidebar collapse, resize handle & macOS chrome drag regions", () => {
  test("preserves windowed drag/no-drag regions, collapses to rail, and resizes cleanly", async ({ openApp }) => {
    const page = await openApp({ theme: "light", surfaceStyle: "liquid-glass", desktopBridge: true });
    const shell = page.locator("[data-app-shell]");
    const main = page.locator("[data-app-shell] > main");
    const sidebarCol = page.locator("[data-sidebar-column]");
    const header = main.locator("header.h-14").first();
    const title = header.locator("[data-chat-title]").first();
    const separator = page.getByRole("separator", { name: "Drag to resize the sidebar" });

    await expect(shell).toHaveAttribute("data-mac-chrome", "windowed");
    await expect(shell).toHaveAttribute("data-mac-bay", "expanded");
    await expect(separator).toBeVisible();

    const regions = await page.evaluate(() => {
      const hdr = document.querySelector<HTMLElement>("[data-app-shell] > main header.h-14");
      const titleEl = hdr?.querySelector<HTMLElement>("[data-chat-title]");
      const sep = document.querySelector<HTMLElement>('[data-app-shell] [role="separator"]');
      return {
        headerDrag: hdr ? getComputedStyle(hdr).getPropertyValue("-webkit-app-region") : null,
        titleDrag: titleEl ? getComputedStyle(titleEl).getPropertyValue("-webkit-app-region") : null,
        sepWebkitDrag: sep ? getComputedStyle(sep).getPropertyValue("-webkit-app-region") : null,
        sepDrag: sep ? getComputedStyle(sep).getPropertyValue("app-region") : null,
      };
    });
    expect(regions.headerDrag).toBe("drag");
    expect(regions.titleDrag).toBe("no-drag");
    expect(regions.sepWebkitDrag).toBe("no-drag");
    expect(regions.sepDrag).toBe("no-drag");
    await expect(title).toBeVisible();

    const expandedMainBox = await main.boundingBox();
    expect(expandedMainBox).not.toBeNull();

    // Collapse expanded sidebar -> rail mode
    await page.locator('[data-mac-bay-tools] button[aria-label="Collapse sidebar"]').click();
    await expect(shell).toHaveAttribute("data-mac-bay", "rail");
    await expect(shell).not.toHaveAttribute("data-sidebar-animating", "1");

    await expect(page.locator("[data-mac-contour]")).toBeVisible();
    const railPaddingLeft = await header.evaluate((el) => Number.parseFloat(getComputedStyle(el).paddingLeft));
    expect(railPaddingLeft).toBeGreaterThanOrEqual(140);

    const railMainBox = await main.boundingBox();
    expect(railMainBox).not.toBeNull();
    expect(railMainBox!.width).toBeGreaterThan(expandedMainBox!.width + 100);
    await expectNoHorizontalOverflow(page);
    await expectInsideViewport(main);

    // Expand rail -> expanded mode
    await page.locator('[data-mac-bay-tools] button[aria-label="Expand sidebar"]').click();
    await expect(shell).toHaveAttribute("data-mac-bay", "expanded");
    await expect(shell).not.toHaveAttribute("data-sidebar-animating", "1");

    const restoredMainBox = await main.boundingBox();
    expect(restoredMainBox).not.toBeNull();
    expect(Math.abs(restoredMainBox!.width - expandedMainBox!.width)).toBeLessThanOrEqual(2);

    // Drag sidebar separator horizontally to widen sidebar column
    const beforeSidebarBox = await sidebarCol.boundingBox();
    const sepBox = await separator.boundingBox();
    expect(beforeSidebarBox).not.toBeNull();
    expect(sepBox).not.toBeNull();

    const startX = sepBox!.x + sepBox!.width / 2;
    const startY = sepBox!.y + sepBox!.height / 2;
    await page.mouse.move(startX, startY);
    await page.mouse.down();
    await page.mouse.move(startX + 60, startY, { steps: 6 });
    await page.mouse.up();

    await expect.poll(async () => (await sidebarCol.boundingBox())?.width ?? 0).toBeGreaterThan(beforeSidebarBox!.width + 40);
    await expectNoHorizontalOverflow(page);
  });

  test("settles sidebar collapse/expand animation cleanly when reduced motion is off", async ({ openApp }) => {
    const page = await openApp({
      theme: "light",
      surfaceStyle: "liquid-glass",
      desktopBridge: true,
      reducedMotion: "no-preference",
    });
    const shell = page.locator("[data-app-shell]");
    const main = page.locator("[data-app-shell] > main");
    const expandedWidth = (await main.boundingBox())!.width;

    await page.locator('[data-mac-bay-tools] button[aria-label="Collapse sidebar"]').click();
    await expect(shell).toHaveAttribute("data-mac-bay", "rail");
    await expect(shell).not.toHaveAttribute("data-sidebar-animating", "1");

    await page.locator('[data-mac-bay-tools] button[aria-label="Expand sidebar"]').click();
    await expect(shell).toHaveAttribute("data-mac-bay", "expanded");
    await expect(shell).not.toHaveAttribute("data-sidebar-animating", "1");

    const restoredWidth = (await main.boundingBox())!.width;
    expect(Math.abs(restoredWidth - expandedWidth)).toBeLessThanOrEqual(2);
    await expectNoHorizontalOverflow(page);
  });
});
