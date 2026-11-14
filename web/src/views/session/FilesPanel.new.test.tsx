// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../../lib/use-fs-live", () => ({ useFsLive: () => {} }));
import { setToken } from "../../lib/api";
import { getState } from "../../lib/store";
import { t } from "../../i18n";
import { FilesPanel } from "./FilesPanel";

// New folder, new text file and downloading several items as one zip in the web's Files panel (#5828).
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
window.matchMedia = ((media: string) => ({ matches: media.includes("max-width"), media, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
HTMLElement.prototype.scrollTo ??= () => {};
HTMLElement.prototype.scrollIntoView ??= () => {};
let host: HTMLDivElement, root: Root, sent: { path: string; body: Record<string, unknown> }[], clicked: string[];
const original = { features: getState().features, machines: getState().machines };
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
async function click(label: string) { const found = button(label); expect(found, label).toBeTruthy(); await act(async () => found!.click()); await flush(); }
const item = (label: string) => [...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes(label)) as HTMLElement | undefined;

beforeEach(async () => {
  localStorage.clear(); setToken("synthetic-A"); sent = []; clicked = [];
  Object.assign(getState(), { features: ["file_edit", "upload_dir", "file_mkdir", "file_write", "file_zip", "file_copy", "pane_cwd"], machines: [{ id: "m1", name: "Work computer", online: true, home: "/home/me", os: "linux" }] });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { clicked.push(this.getAttribute("href") ?? ""); });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const u = new URL(String(input), "http://local.invalid");
    if (u.pathname.endsWith("/git/status")) return Response.json({ is_repo: false, files: [], commits: [] });
    if (u.pathname.endsWith("/fs")) return Response.json({ path: "/home/me/app", entries: [{ name: "src", dir: true, size: 0 }, { name: "a.txt", dir: false, size: 1 }], truncated: false });
    if (u.pathname.endsWith("/file")) return Response.json({ path: "/home/me/app/Untitled.txt", name: "Untitled.txt", size: 0, text: "", truncated: false, is_dir: false });
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    sent.push({ path: u.pathname, body });
    if (u.pathname.endsWith("/files/mkdir")) return Response.json({ path: "/home/me/app/" + body.name });
    if (u.pathname.endsWith("/files/write")) return Response.json({ path: body.path, size: 0, revision: "r1" });
    if (u.pathname.endsWith("/files/zip")) return Response.json({ path: (body.dir ?? "/tmp/persona-zips/z-1") + "/" + body.name, name: body.name, size: 10, files: 2 });
    if (u.pathname.endsWith("/panes")) return Response.json({ pane: "w1:p9", terminal: "pane:w1:p9", tab_id: "w1:t9" });
    if (u.pathname.endsWith("/files/copy")) return Response.json({ copied: body.paths.map((p: string) => ({ from: p, to: p + " 2" })) });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<FilesPanel mid="m1" sid="s1" cwd="/home/me/app" onClose={() => {}} />)); await flush();
});
afterEach(() => { act(() => root.unmount()); host.remove(); document.body.innerHTML = ""; Object.assign(getState(), original); setToken(""); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("FilesPanel New and zip (#5828)", () => {
  it("makes a folder in the folder shown", async () => {
    await click(t("files-new.new"));
    await act(async () => item(t("files-new.new_folder"))!.click()); await flush();
    await click(t("files-new.create"));
    expect(sent).toEqual([{ path: "/api/v1/machines/m1/files/mkdir", body: { dir: "/home/me/app", cwd: "/home/me/app", name: t("files-new.untitled_folder") } }]);
  });

  it("makes an empty text file and opens it", async () => {
    await click(t("files-new.new"));
    await act(async () => item(t("files-new.new_text"))!.click()); await flush();
    await click(t("files-new.create"));
    expect(sent[0]).toEqual({ path: "/api/v1/machines/m1/files/write", body: { path: "/home/me/app/" + t("files-new.untitled_text"), cwd: "/home/me/app", text: "", create: true } });
  });

  it("downloads a folder and a file together as one zip", async () => {
    await click(t("session-filespanel.more"));
    await click(t("files-ops.select"));
    for (const name of ["src", "a.txt"]) {
      const box = document.querySelector<HTMLInputElement>(`input[aria-label="${t("files-ops.select_row", { name })}"]`)!;
      await act(async () => box.click()); await flush();
    }
    const bar = document.querySelector('[role="toolbar"]')!;
    await act(async () => bar.querySelector<HTMLButtonElement>(`button[aria-label="${t("files-ops.download")}"]`)!.click()); await flush();
    expect(sent[0].path).toBe("/api/v1/machines/m1/files/zip");
    expect(sent[0].body).toEqual({ paths: ["/home/me/app/src", "/home/me/app/a.txt"], cwd: "/home/me/app", name: t("files-ops.archive_name") });
    expect(clicked.some((h) => h.includes("file/download") && h.includes(encodeURIComponent("/tmp/persona-zips/z-1/")))).toBe(true);
  });

  it("duplicates and compresses from a row's menu, and copies to a folder (#5830)", async () => {
    const menuOf = async (name: string) => {
      const row = host.querySelector<HTMLButtonElement>(`button[title="/home/me/app/${name}"]`)!;
      await act(async () => row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 }))); await flush();
    };
    await menuOf("a.txt");
    await act(async () => item(t("files-ops.duplicate"))!.click()); await flush();
    expect(sent.at(-1)).toEqual({ path: "/api/v1/machines/m1/files/copy", body: { paths: ["/home/me/app/a.txt"], cwd: "/home/me/app", dir: "/home/me/app", conflict: "keep", operation_id: expect.any(String) } });
    await menuOf("src");
    await act(async () => item(t("files-ops.compress"))!.click()); await flush();
    expect(sent.at(-1)).toEqual({ path: "/api/v1/machines/m1/files/zip", body: { paths: ["/home/me/app/src"], cwd: "/home/me/app", name: "src.zip", dir: "/home/me/app", operation_id: expect.any(String) } });
    expect(document.body.textContent).toContain(t("files-ops.compressed", { name: "src.zip" }));
    await menuOf("a.txt");
    await act(async () => item(t("files-ops.copy_to"))!.click()); await flush();
    expect(document.body.textContent).toContain(t("files-ops.copy_one_title", { name: "a.txt" }));
    await click(t("files-ops.copy_here"));
    expect(sent.at(-1)).toEqual({ path: "/api/v1/machines/m1/files/copy", body: { paths: ["/home/me/app/a.txt"], cwd: "/home/me/app", dir: "/home/me/app", operation_id: expect.any(String) } });
  });

  it("opens a terminal in the folder shown (#5832)", async () => {
    await click(t("session-filespanel.more"));
    await act(async () => item(t("files-nav.open_terminal"))!.click()); await flush();
    expect(sent.at(-1)).toEqual({ path: "/api/v1/machines/m1/panes", body: { cwd: "/home/me/app" } });
    expect(location.pathname).toContain("pane");
  });
});
