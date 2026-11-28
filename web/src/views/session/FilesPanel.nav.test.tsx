// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../../lib/use-fs-live", () => ({ useFsLive: () => {} }));
import { setToken } from "../../lib/api";
import { getState } from "../../lib/store";
import { t } from "../../i18n";
import { FilesPanel } from "./FilesPanel";

// The file page's navigation, find, Info and links on the web (#5827), against a made-up computer whose home is /home/me.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
window.matchMedia = ((media: string) => ({ matches: media.includes("max-width"), media, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
HTMLElement.prototype.scrollTo ??= () => {};
let host: HTMLDivElement, root: Root, asked: string[];
const original = { features: getState().features, machines: getState().machines };
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
async function click(label: string) { const found = button(label); expect(found, label).toBeTruthy(); await act(async () => found!.click()); await flush(); }
const current = () => document.querySelector('[aria-current="location"]')?.getAttribute("title");

const LISTINGS: Record<string, unknown[]> = {
  "/home/me/app": [
    { name: "src", dir: true, size: 0 },
    { name: "shared", dir: true, size: 0, symlink: true, link_target: "../shared" },
    { name: "old.json", dir: false, size: 0, symlink: true, link_target: "../legacy/old.json", link_broken: true },
    { name: "notes.txt", dir: false, size: 5 },
  ],
  "/home/me/app/src": [{ name: "main.ts", dir: false, size: 9 }],
  "/home/me": [{ name: "app", dir: true, size: 0 }],
  "/home": [{ name: "me", dir: true, size: 0 }],
};

beforeEach(async () => {
  localStorage.clear(); setToken("synthetic-A"); asked = [];
  Object.assign(getState(), { features: ["file_edit", "file_info"], machines: [{ id: "m1", name: "Work computer", online: true, home: "/home/me", os: "linux" }] });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const u = new URL(String(input), "http://local.invalid");
    asked.push(u.pathname + u.search);
    if (u.pathname.endsWith("/git/status")) return Response.json({ is_repo: false, files: [], commits: [] });
    if (u.pathname.endsWith("/fs")) {
      const p = u.searchParams.get("path")!;
      const path = p === "~" ? "/home/me" : p;
      return Response.json({ path, entries: LISTINGS[path] ?? [], truncated: false });
    }
    if (u.pathname.endsWith("/fs/find")) return Response.json({ cwd: u.searchParams.get("cwd"), files: ["src/main.ts", "notes.txt", "src/config/app.ts"], truncated: false });
    if (u.pathname.endsWith("/stat")) {
      const p = u.searchParams.get("path")!;
      if (p.endsWith("old.json")) return Response.json({ stats: [{ path: p, kind: "link", symlink: true, link_target: "../legacy/old.json", link_broken: true, mode: 511, mtime: "2026-11-10T10:00:00Z" }] });
      return Response.json({ stats: [{ path: p, kind: "file", size: 5, mode: 0o644, owner: "me", group: "staff", mtime: "2026-11-10T10:00:00Z", birthtime: "2026-11-01T10:00:00Z" }] });
    }
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<FilesPanel mid="m1" sid="s1" cwd="/home/me/app" onClose={() => {}} />)); await flush();
});
afterEach(() => { act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; Object.assign(getState(), original); setToken(""); vi.unstubAllGlobals(); });

describe("FilesPanel navigation (#5827)", () => {
  it("goes in, back, forward, up above the session folder and home", async () => {
    expect(current()).toBe("/home/me/app");
    expect(document.body.textContent).toContain(t("files-nav.session"));
    const src = host.querySelector<HTMLButtonElement>('button[title="/home/me/app/src"]')!;
    await act(async () => src.click()); await flush();
    expect(current()).toBe("/home/me/app/src");
    await click(t("files-nav.back"));
    expect(current()).toBe("/home/me/app");
    await click(t("files-nav.forward"));
    expect(current()).toBe("/home/me/app/src");
    await click(t("session-filespanel.up"));
    await click(t("session-filespanel.up"));
    await click(t("session-filespanel.up"));
    expect(current()).toBe("/home");
    expect(document.body.textContent).toContain(t("files-nav.read_only")); // outside the home
    expect(button(t("files-ops.upload"))).toBeFalsy();
    await click(t("files-nav.forward")); // nothing ahead after going up: disabled
    expect(current()).toBe("/home");
  });

  it("opens the home from the house in the path bar", async () => {
    await click(t("files-nav.home"));
    expect(current()).toBe("/home/me");
    expect(asked.some((a) => a.includes("/fs?path=%2Fhome%2Fme&"))).toBe(true);
  });

  it("marks links, and a broken one opens Info instead", async () => {
    expect(document.body.textContent).toContain(t("files-nav.link_to", { target: "../shared" }));
    expect(document.body.textContent).toContain(t("files-nav.link_broken", { target: "../legacy/old.json" }));
    const broken = host.querySelector<HTMLButtonElement>('button[title="/home/me/app/old.json"]')!;
    await act(async () => broken.click()); await flush();
    const dialog = document.querySelector('[data-testid="file-info"]')!;
    expect(dialog.textContent).toContain(t("files-info.link_missing"));
    expect(dialog.textContent).toContain("../legacy/old.json");
    expect(asked.some((a) => a.includes("/stat?") && a.includes("detail=1"))).toBe(true);
  });

  it("shows details and permissions in words", async () => {
    await click(t("session-filespanel.more"));
    await click(t("files-info.info"));
    const dialog = document.querySelector('[data-testid="file-info"]')!;
    expect(dialog.textContent).toContain("rw-r--r-- · 644");
    expect(dialog.textContent).toContain(t("files-info.owner_group", { owner: "me", group: "staff" }));
    expect(dialog.textContent).toContain(t("files-info.words_short", { owner: t("files-info.access_6"), others: t("files-info.access_4") }));
  });

  it("finds names under the folder shown, folders included", async () => {
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t("files-nav.find_in", { name: "app" })}"]`)!;
    expect(input).toBeTruthy();
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, "config");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();
    const found = document.querySelector('[data-testid="files-find"]')!;
    const titles = [...found.querySelectorAll("button")].map((b) => b.getAttribute("title"));
    expect(titles).toEqual(["src/config"]);
    expect(asked.some((a) => a.includes("/fs/find?cwd=%2Fhome%2Fme%2Fapp"))).toBe(true);
    await act(async () => (found.querySelector("button") as HTMLButtonElement).click()); await flush();
    expect(current()).toBe("/home/me/app/src/config");
  });

  it("exits multi-select on Escape from a row checkbox without propagating to an enclosing SidePanel, and ignores IME Escape", async () => {
    await click(t("session-filespanel.more"));
    await click(t("files-ops.select"));
    const checkbox = host.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(checkbox).toBeTruthy();

    const outerKeyDown = vi.fn();
    window.addEventListener("keydown", outerKeyDown);
    try {
      // Safari IME Escape (keyCode 229) must not exit multi-select.
      checkbox.focus();
      const imeEsc = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
      Object.defineProperty(imeEsc, "keyCode", { value: 229 });
      await act(async () => { checkbox.dispatchEvent(imeEsc); });
      expect(host.querySelector('input[type="checkbox"]')).toBeTruthy();

      // Real Escape on the checkbox exits multi-select and stops propagation so enclosing SidePanel stays open.
      outerKeyDown.mockClear();
      const esc = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
      await act(async () => { checkbox.dispatchEvent(esc); });
      expect(esc.defaultPrevented).toBe(true);
      expect(host.querySelector('input[type="checkbox"]')).toBeNull();
      expect(outerKeyDown).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", outerKeyDown);
    }
  });
});
