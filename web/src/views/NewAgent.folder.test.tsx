// @vitest-environment jsdom
// "New agent"'s folder picker (#14595, #9452): Browse-first with clickable breadcrumbs and immediate subfolders, a
// secondary Browse | Recent toggle when recent folders exist (with worktree paths de-prioritized), typed-folder
// following with prefix filtering, and picking/clearing folders.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../lib/types";
import { t } from "../i18n";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[] }));
// one assistant: its computers are the store's (lib/current-assistant.ts reads the roster, which this store stand-in has none of)
vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: { machines: unknown[] }) => T) => T };
  return { useScopedMachines: () => store.useStore((s) => s.machines) };
});
vi.mock("../lib/store", () => ({ getState: () => state, useStore: (selector: (value: { machines: Machine[] }) => unknown) => selector(state), hubHas: () => false }));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));

import { NewAgent } from "./NewAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const entry = (dir: string, name: string) => ({ name, path: `${dir}/${name}` });
// the computer's folders, as the node lists them (it cleans a trailing slash away, and every answer carries the recent
// folders); "" is home
let recentList = [
  { path: "/srv/app_worktrees/feat-1", name: "feat-1" },
  { path: "/srv/work/recent-repo", name: "recent-repo" },
];
const FOLDERS: Record<string, unknown> = {
  "": { path: "/home/me", parent: "/home", entries: [entry("/home/me", "notes"), entry("/home/me", "code")] },
  "/home/me": { path: "/home/me", parent: "/home", entries: [entry("/home/me", "notes"), entry("/home/me", "code")] },
  "/home/me/code": { path: "/home/me/code", parent: "/home/me", entries: [entry("/home/me/code", "my-app")] },
  "/home/me/code/my-app": { path: "/home/me/code/my-app", parent: "/home/me/code", entries: [entry("/home/me/code/my-app", "web")] },
  "/srv/work/recent-repo": { path: "/srv/work/recent-repo", parent: "/srv/work", entries: [entry("/srv/work/recent-repo", "docs")] },
  "/srv/work": { path: "/srv/work", parent: "/srv", entries: [entry("/srv/work", "Alpha"), entry("/srv/work", "alpine"), entry("/srv/work", "beta")] },
  "/srv/work/alpine": { path: "/srv/work/alpine", parent: "/srv/work", entries: [] },
  "/srv/work/beta": { path: "/srv/work/beta", parent: "/srv/work", entries: [entry("/srv/work/beta", "src")] },
  "/srv": { path: "/srv", parent: "/", entries: [entry("/srv", "work")] },
  "/": { path: "/", parent: "/", entries: [entry("", "srv")] },
};
let asked: string[];
let host: HTMLDivElement;
let root: Root;
const tick = (ms = 0) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const field = () => [...host.querySelectorAll<HTMLInputElement>("input")].find((i) => i.placeholder === t("newagent.folder_placeholder"))!;
async function typeFolder(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field(), value);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const folderRows = () => [...host.querySelectorAll<HTMLButtonElement>("button[data-dir-row]")];
const rows = () => folderRows().map((b) => b.querySelector("span")?.textContent ?? "");
const breadcrumbButtons = () => [...host.querySelectorAll<HTMLButtonElement>("button[data-breadcrumb-path]")];
const breadcrumbLabels = () => breadcrumbButtons().map((b) => b.textContent?.trim() ?? "");
const toggle = () => host.querySelector<HTMLElement>('[data-testid="folder-view-toggle"]');
const tab = (label: string) => [...(toggle()?.querySelectorAll<HTMLButtonElement>('[role="tab"]') ?? [])].find((b) => b.textContent?.trim() === label);
const upButton = () => host.querySelector<HTMLButtonElement>(`[aria-label="${t("newagent.parent_folder")}"]`);
const clearButton = () => host.querySelector<HTMLButtonElement>(`[aria-label="${t("newagent.clear_folder")}"]`);
let opening = 0;
// the sheet opened again, reading the folder filled in afresh
async function reopen() {
  await act(async () => root.render(<NewAgent open onClose={() => {}} key={++opening} />));
  await tick();
}

beforeEach(async () => {
  vi.useFakeTimers();
  state.machines = [{ id: "computer", name: "Computer", online: true }];
  localStorage.clear();
  asked = [];
  recentList = [
    { path: "/srv/app_worktrees/feat-1", name: "feat-1" },
    { path: "/srv/work/recent-repo", name: "recent-repo" },
  ];
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://hub.invalid");
    if (!url.pathname.endsWith("/dirs")) return json({});
    const path = url.searchParams.get("path") ?? "";
    asked.push(path);
    const listed = FOLDERS[path.length > 1 ? path.replace(/\/+$/, "") : path];
    return listed ? json({ ...listed, recent: recentList }) : json({ error: `open ${path}: no such file or directory`, code: "internal" }, 502);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<NewAgent open onClose={() => {}} />));
  await tick();
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("NewAgent breadcrumb path picker and Browse/Recent views (#14595)", () => {
  it("opens in Browse view with clickable breadcrumbs and immediate subfolders, keeping Recent secondary", async () => {
    expect(breadcrumbLabels()).toEqual(["~"]);
    expect(rows()).toEqual(["notes", "code"]);
    expect(tab(t("newagent.browse"))?.getAttribute("aria-selected")).toBe("true");
    expect(tab(t("newagent.recent"))?.getAttribute("aria-selected")).toBe("false");

    // Switching to Recent shows primary folders ahead of worktree paths.
    await act(async () => tab(t("newagent.recent"))!.click());
    expect(tab(t("newagent.recent"))?.getAttribute("aria-selected")).toBe("true");
    expect(rows()).toEqual(["recent-repo", "feat-1"]);

    // Clicking a recent row selects it and switches back to Browse at that folder.
    await act(async () => folderRows().find((b) => b.title === "/srv/work/recent-repo")!.click());
    await tick();
    expect(field().value).toBe("/srv/work/recent-repo");
    expect(tab(t("newagent.browse"))?.getAttribute("aria-selected")).toBe("true");
    expect(breadcrumbLabels()).toEqual(["/", "srv", "work", "recent-repo"]);
    expect(rows()).toEqual(["docs"]);
  });

  it("hides the Browse | Recent toggle when there are no recent folders", async () => {
    recentList = [];
    await reopen();
    expect(toggle()).toBeNull();
    expect(rows()).toEqual(["notes", "code"]);
  });

  it("navigates directly to ancestor folders via breadcrumb segments and updates cwd", async () => {
    localStorage.setItem("new.cwd.computer", "/home/me/code/my-app");
    await reopen();
    expect(breadcrumbLabels()).toEqual(["~", "code", "my-app"]);
    expect(rows()).toEqual(["web"]);

    // Jump straight to ~/code
    await act(async () => breadcrumbButtons().find((b) => b.dataset.breadcrumbPath === "/home/me/code")!.click());
    await tick();
    expect(field().value).toBe("/home/me/code");
    expect(breadcrumbLabels()).toEqual(["~", "code"]);
    expect(rows()).toEqual(["my-app"]);

    // Jump straight to ~
    await act(async () => breadcrumbButtons().find((b) => b.dataset.breadcrumbPath === "/home/me")!.click());
    await tick();
    expect(field().value).toBe("/home/me");
    expect(breadcrumbLabels()).toEqual(["~"]);
    expect(rows()).toEqual(["notes", "code"]);
  });

  it("handles non-home root breadcrumbs cleanly", async () => {
    localStorage.setItem("new.cwd.computer", "/srv/work/beta");
    await reopen();
    expect(breadcrumbLabels()).toEqual(["/", "srv", "work", "beta"]);
    expect(rows()).toEqual(["src"]);

    await act(async () => breadcrumbButtons().find((b) => b.dataset.breadcrumbPath === "/srv")!.click());
    await tick();
    expect(field().value).toBe("/srv");
    expect(breadcrumbLabels()).toEqual(["/", "srv"]);
    expect(rows()).toEqual(["work"]);

    await act(async () => breadcrumbButtons().find((b) => b.dataset.breadcrumbPath === "/")!.click());
    await tick();
    expect(field().value).toBe("/");
    expect(breadcrumbLabels()).toEqual(["/"]);
    expect(rows()).toEqual(["srv"]);
  });

  it("switches back to Browse when typing in the path input and clears cleanly with the clear button", async () => {
    await act(async () => tab(t("newagent.recent"))!.click());
    expect(tab(t("newagent.recent"))?.getAttribute("aria-selected")).toBe("true");

    await typeFolder("/srv/work/");
    expect(tab(t("newagent.browse"))?.getAttribute("aria-selected")).toBe("true");
    await tick(100);
    expect(asked).not.toContain("/srv/work/"); // not on every keystroke
    await tick(200);
    expect(asked.at(-1)).toBe("/srv/work/");
    expect(rows()).toEqual(["Alpha", "alpine", "beta"]);
    expect(upButton()).not.toBeNull();

    // Clear button resets cwd and returns to home immediately.
    await act(async () => clearButton()!.click());
    await tick();
    expect(field().value).toBe("");
    expect(breadcrumbLabels()).toEqual(["~"]);
    expect(rows()).toEqual(["notes", "code"]);
  });
});

describe("NewAgent folder list follows the folder field (#9452)", () => {
  it("lists the folders starting with a partial name beside it, ignoring case", async () => {
    await typeFolder("/srv/work/al");
    await tick(300);
    expect(asked.slice(-2)).toEqual(["/srv/work/al", "/srv/work"]);
    expect(rows()).toEqual(["Alpha", "alpine"]);
    expect(field().value).toBe("/srv/work/al"); // the list never rewrites what was typed

    // picking a filtered row enters that folder and updates cwd in lockstep
    await act(async () => folderRows().find((b) => b.title === "/srv/work/alpine")!.click());
    await tick(300);
    expect(field().value).toBe("/srv/work/alpine");
    expect(breadcrumbLabels()).toEqual(["/", "srv", "work", "alpine"]);
    expect(rows()).toEqual([]);
  });

  it("keeps cwd, breadcrumbs, and subfolders in lockstep when clicking into subfolders and going up", async () => {
    await typeFolder("/srv/work");
    await tick(300);
    await act(async () => folderRows().find((b) => b.title === "/srv/work/beta")!.click());
    await tick();
    expect(field().value).toBe("/srv/work/beta");
    expect(breadcrumbLabels()).toEqual(["/", "srv", "work", "beta"]);
    expect(rows()).toEqual(["src"]);
    await act(async () => upButton()!.click());
    await tick();
    expect(field().value).toBe("/srv/work");
    expect(breadcrumbLabels()).toEqual(["/", "srv", "work"]);
    expect(rows()).toEqual(["Alpha", "alpine", "beta"]);
    await act(async () => upButton()!.click());
    await tick();
    expect(field().value).toBe("/srv");
    expect(breadcrumbLabels()).toEqual(["/", "srv"]);
    expect(rows()).toEqual(["work"]);

    await typeFolder("/srv/work/b");
    await tick(300);
    expect(rows()).toEqual(["beta"]);
  });

  it("leaves a relative path alone: it names no folder the computer would agree on", async () => {
    await typeFolder("work/al");
    await tick(300);
    expect(asked.every((p) => p === "")).toBe(true);
    expect(rows()).toEqual(["notes", "code"]);
  });

  it("starts where browsing starts when the folder filled in is gone", async () => {
    localStorage.setItem("new.cwd.computer", "/srv/work/gone");
    await reopen();
    expect(asked.slice(-2)).toEqual(["/srv/work/gone", ""]);
    expect(breadcrumbLabels()).toEqual(["~"]);
    expect(rows()).toEqual(["notes", "code"]);
  });
});
