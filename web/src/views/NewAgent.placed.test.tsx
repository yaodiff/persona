// @vitest-environment jsdom
// "New agent" opened on a place (a project's + in By project, a space's or a board card's New agent, #13523): the
// computer and the folder are filled in, so with a fine pointer the caret is in the first message, ready for it; on a
// touch screen the sheet keeps the keyboard down, and an opening on no place leaves the focus where the sheet puts it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../lib/types";
import type { NewAgentPreset } from "../lib/new-agent";
import { t } from "../i18n";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[], fine: true }));
// one assistant: its computers are the store's (lib/current-assistant.ts reads the roster, which this store stand-in has none of)
vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: { machines: unknown[] }) => T) => T };
  return { useScopedMachines: () => store.useStore((s) => s.machines) };
});
vi.mock("../lib/store", () => ({ getState: () => state, useStore: (selector: (value: { machines: Machine[] }) => unknown) => selector(state), hubHas: () => false }));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));

import { NewAgent } from "./NewAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
const firstMessage = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${t("newagent.first_message")}"]`)!;
const folderField = () => [...host.querySelectorAll<HTMLInputElement>("input")].find((i) => i.placeholder === t("newagent.folder_placeholder"))!;
async function open(preset: NewAgentPreset | null) {
  await act(async () => root.render(<NewAgent open preset={preset} onClose={() => {}} />));
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
}

beforeEach(() => {
  vi.useFakeTimers();
  state.machines = [{ id: "laptop", name: "Laptop", online: true }, { id: "server", name: "Server", online: true }];
  state.fine = true;
  localStorage.clear();
  // the media list is kept once read (lib/viewport.ts): its answer follows `state.fine`
  vi.stubGlobal("matchMedia", (query: string) => ({ get matches() { return query === "(any-pointer: fine)" && state.fine; }, media: query, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://hub.invalid");
    if (url.pathname.endsWith("/dirs")) return json({ path: url.searchParams.get("path") || "/home/me", parent: "/", entries: [] });
    return json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("New agent opened on a place (#13523)", () => {
  it("fills in the computer and the folder and puts the caret in the first message", async () => {
    await open({ machineId: "server", cwd: "/srv/site" });
    expect(folderField().value).toBe("/srv/site");
    // the computer picked is the one drawn chosen
    const chip = (name: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(name))!;
    expect(chip("Server").className).toMatch(/(^|\s)bg-ink(\s|$)/);
    expect(chip("Laptop").className).not.toMatch(/(^|\s)bg-ink(\s|$)/);
    expect(document.activeElement).toBe(firstMessage());
  });

  it("leaves the keyboard down on a touch screen", async () => {
    state.fine = false;
    await open({ machineId: "server", cwd: "/srv/site" });
    expect(folderField().value).toBe("/srv/site");
    expect(document.activeElement).not.toBe(firstMessage());
  });

  it("leaves the caret alone when it opens on no place", async () => {
    await open(null);
    expect(document.activeElement).not.toBe(firstMessage());
    await open({ prompt: "Summarize the notes" });
    expect(document.activeElement).not.toBe(firstMessage());
  });
});
