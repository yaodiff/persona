// @vitest-environment jsdom
// The node commands' rename and close questions (lib/node-ask.ts): the sidebar's NodePrompt stays on the first screen,
// its dialogs come from a chunk of their own (#17142) and still answer each question.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { askConfirm, askText, getAsk } from "../lib/node-ask";
import { NodePrompt } from "./NodePrompt";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};
const dialogButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent?.trim() === label);

beforeEach(async () => {
  await initI18n("en");
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root.render(<NodePrompt />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("NodePrompt", () => {
  it("draws nothing until a command asks", () => {
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("asks for a new name and answers with it", async () => {
    let answer: Promise<string | null> | undefined;
    act(() => { answer = askText("Rename pane", "Name", "old"); });
    await flush();
    const input = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
    expect(input.value).toBe("old");
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => {
      setValue.call(input, "new name");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => dialogButton(t("common.save"))!.click());
    await expect(answer).resolves.toBe("new name");
    expect(getAsk()).toBeNull();
  });

  it("asks for a yes and answers true or false", async () => {
    let yes: Promise<boolean> | undefined;
    act(() => { yes = askConfirm("Close pane?", "It stops the Agent.", "Close"); });
    await flush();
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("It stops the Agent.");
    act(() => dialogButton("Close")!.click());
    await expect(yes).resolves.toBe(true);

    let no: Promise<boolean> | undefined;
    act(() => { no = askConfirm("Close pane?", "It stops the Agent.", "Close"); });
    await flush();
    act(() => dialogButton(t("common.cancel"))!.click());
    await expect(no).resolves.toBe(false);
  });

  it("keeps the dialogs out of the first screen's module", () => {
    const source = readFileSync(resolve(__dirname, "NodePrompt.tsx"), "utf8");
    expect(source).not.toMatch(/from ["']\.\.\/ui\/atoms["']/);
    expect(source).toMatch(/import\(["']\.\/NodeAskDialog["']\)/);
  });
});
