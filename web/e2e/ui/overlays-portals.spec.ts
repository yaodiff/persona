import { expect, openSplitPanelAgent, PNG_1X1, test } from "./fixtures/mock-hub";
import { expectInsideViewport, expectNoActiveBackdropFilter, expectNoHorizontalOverflow } from "./fixtures/assertions";

test.describe("Overlay & portal containment under Frosted Glass", () => {
  test("portals Palette, New Agent dialog, ImageViewer, and ComposerImageStage cleanly on desktop split view", async ({ openApp }) => {
    const page = await openApp({ theme: "dark", surfaceStyle: "liquid-glass", desktopBridge: true });
    await openSplitPanelAgent(page);

    // 1. Command Palette
    await page.locator("[data-mac-bay-tools] button[aria-label='Search chats']").click();
    const paletteDialog = page.getByRole("dialog", { name: "Search sessions" });
    await expect(paletteDialog).toBeVisible();
    expect(await paletteDialog.evaluate((el) => el.closest("[data-app-shell]") === null)).toBe(true);
    await expectInsideViewport(paletteDialog);
    await expectNoActiveBackdropFilter(page);
    await page.keyboard.press("Escape");
    await expect(paletteDialog).toBeHidden();

    // 2. New Agent Dialog
    await page.getByRole("button", { name: "New agent" }).first().click();
    await page.getByRole("menuitem", { name: "New agent" }).click();
    const modalDialog = page.getByRole("dialog", { name: "New agent" });
    await expect(modalDialog).toBeVisible();
    expect(await modalDialog.evaluate((el) => el.closest("[data-app-shell]") === null)).toBe(true);
    await expectInsideViewport(modalDialog);
    await expectNoActiveBackdropFilter(page);
    await page.keyboard.press("Escape");
    await expect(modalDialog).toBeHidden();

    // 3. Message ImageViewer
    await page.locator("[data-attached-photos] button").first().click();
    const viewerDialog = page.getByRole("dialog", { name: /^Image\b|shot\.png/ });
    await expect(viewerDialog).toBeVisible();
    expect(await viewerDialog.evaluate((el) => el.closest("[data-app-shell]") === null)).toBe(true);
    await expectInsideViewport(viewerDialog);
    await expectNoActiveBackdropFilter(page);
    await page.keyboard.press("Escape");
    await expect(viewerDialog).toBeHidden();

    // 4. ComposerImageStage clears active shell drop-shadow filter & clip-path in rail mode, then restores them.
    const shell = page.locator("[data-app-shell]");
    await page.locator('[data-mac-bay-tools] button[aria-label="Collapse sidebar"]').click();
    await expect(shell).toHaveAttribute("data-mac-bay", "rail");
    await expect
      .poll(async () =>
        page.evaluate(() => {
          const main = document.querySelector<HTMLElement>("[data-app-shell] > main");
          if (!main) return null;
          const cs = getComputedStyle(main);
          return { filter: cs.filter, clipPath: cs.clipPath };
        }),
      )
      .toEqual({
        filter: expect.stringContaining("drop-shadow("),
        clipPath: expect.stringContaining("path("),
      });

    const fileInput = page.locator('[data-app-shell] > main input[type="file"]').first();
    await fileInput.setInputFiles({
      name: "stage-check.png",
      mimeType: "image/png",
      buffer: PNG_1X1,
    });
    const thumbTile = page.getByRole("button", { name: "Edit stage-check.png" });
    await expect(thumbTile).toBeVisible();
    await thumbTile.click();

    const stageArea = page.getByTestId("composer-image-stage-area");
    await expect(stageArea).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("data-composer-stage", "1");
    await expectInsideViewport(stageArea);
    await expectNoActiveBackdropFilter(page);

    const elevatedComposer = page.locator("[data-app-shell] > main [data-composer-image-expanded]").first();
    await expectInsideViewport(elevatedComposer);
    expect(await elevatedComposer.evaluate((el) => getComputedStyle(el).position)).toBe("fixed");

    const shellStyles = await page.evaluate(() => {
      const read = (sel: string) => {
        const el = document.querySelector<HTMLElement>(sel);
        if (!el) return null;
        const cs = getComputedStyle(el);
        return { filter: cs.filter, clipPath: cs.clipPath, backdropFilter: cs.backdropFilter };
      };
      return {
        shell: read("[data-app-shell]"),
        main: read("[data-app-shell] > main"),
        aside: read("aside[data-side-panel]"),
      };
    });
    expect(shellStyles.shell).toEqual({ filter: "none", clipPath: "none", backdropFilter: "none" });
    expect(shellStyles.main).toEqual({ filter: "none", clipPath: "none", backdropFilter: "none" });
    expect(shellStyles.aside).toEqual({ filter: "none", clipPath: "none", backdropFilter: "none" });

    await page.getByTestId("composer-image-close").click();
    await expect(stageArea).toBeHidden();
    await expect(page.locator("html")).not.toHaveAttribute("data-composer-stage", "1");
    await expect
      .poll(async () =>
        page.evaluate(() => {
          const main = document.querySelector<HTMLElement>("[data-app-shell] > main");
          if (!main) return null;
          const cs = getComputedStyle(main);
          return { filter: cs.filter, clipPath: cs.clipPath };
        }),
      )
      .toEqual({
        filter: expect.stringContaining("drop-shadow("),
        clipPath: expect.stringContaining("path("),
      });
  });

  test("portals ExpandedComposer to document.body under compact Web Frosted Glass", async ({ openApp }) => {
    const page = await openApp({
      theme: "dark",
      surfaceStyle: "liquid-glass",
      desktopBridge: false,
      viewport: { width: 390, height: 844 },
    });

    const initialDraft = Array.from({ length: 10 }, (_, i) => `Draft line ${i + 1}`).join("\n");
    const mainInput = page.locator("[data-app-shell] > main textarea").first();
    await mainInput.fill(initialDraft);

    const expandBtn = page.locator('[data-app-shell] > main [data-testid="composer-expand"]').first();
    await expect(expandBtn).toBeVisible();
    await expandBtn.click();

    const expanded = page.getByRole("dialog", { name: "Message" });
    await expect(expanded).toBeVisible();
    await expect(expanded).toHaveAttribute("data-testid", "composer-expanded");
    expect(await expanded.evaluate((el) => ({ parentIsBody: el.parentElement === document.body, position: getComputedStyle(el).position }))).toEqual({
      parentIsBody: true,
      position: "fixed",
    });
    await expectInsideViewport(expanded);
    await expectNoHorizontalOverflow(page);

    const updatedDraft = `${initialDraft}\nEdited in ExpandedComposer`;
    await expanded.locator("textarea").first().fill(updatedDraft);
    await page.getByTestId("composer-collapse").click();
    await expect(expanded).toBeHidden();
    await expect(mainInput).toHaveValue(updatedDraft);
  });
});
