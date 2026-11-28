// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileContent } from "../../lib/files";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

// A Windows computer answers its paths with backslashes (filepath.Clean on the node). The viewer reads them by either
// separator: a Markdown file's pictures and links are found in its own folder, and Show in Files opens that folder
// with the file marked (#9968).
const README = "C:\\Users\\me\\docs\\README.md";
const LOGO = "C:\\Users\\me\\docs\\img\\logo.png";
const SETUP = "C:\\Users\\me\\setup.md";
const text = (path: string, name: string, body: string): FileContent => ({ path, name, size: body.length, mime: "text/markdown", text: body, next: 0, revision: "r1", truncated: false, is_dir: false });
const files: Record<string, FileContent> = {
  [README]: text(README, "README.md", "![logo](img/logo.png)\n\n[Setup](../setup.md)\n"),
  [LOGO]: { path: LOGO, name: "logo.png", size: 9, mime: "image/png", data: "cG5nLWRhdGE=", truncated: false, is_dir: false },
  [SETUP]: text(SETUP, "setup.md", "# Setup\n"),
};

const asked: string[] = [];
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: async (route: string) => {
    const path = new URL(route, "https://hub.invalid").searchParams.get("path") ?? "";
    asked.push(path);
    if (files[path]) return files[path];
    throw new Error(`no such file: ${path}`);
  },
}));
const opened = vi.hoisted(() => [] as unknown[]);
vi.mock("../../lib/panel", async (original) => ({
  ...(await original<typeof import("../../lib/panel")>()),
  openPanel: (panel: unknown) => opened.push(panel),
}));

const { FileViewerPanel } = await import("./FileViewer");
const { t } = await import("../../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = async () => {
  for (let i = 0; i < 16; i++) await act(async () => { await Promise.resolve(); await new Promise((resolve) => setTimeout(resolve, 0)); });
};

let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  asked.length = 0;
  opened.length = 0;
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<FileViewerPanel mid="win" sid="s1" path={README} cwd={"C:\\Users\\me"} onClose={() => {}} />));
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the file viewer on a Windows computer", () => {
  it("reads a Markdown file's picture from the file's own folder", () => {
    const img = host.querySelector<HTMLImageElement>('[data-testid="file-markdown"] img');
    expect(img?.dataset.machine).toBe(LOGO);
    expect(asked).toContain(LOGO);
    expect(img?.getAttribute("src")).toBe("data:image/png;base64,cG5nLWRhdGE=");
  });

  it("opens a Markdown file's relative link from the file's own folder", async () => {
    const link = [...host.querySelectorAll<HTMLAnchorElement>('[data-testid="file-markdown"] a')].find((a) => a.textContent === "Setup")!;
    await act(async () => link.click());
    await flush();
    expect(asked.at(-1)).toBe(SETUP);
    expect(host.textContent).toContain("setup.md");
  });

  it("returns to the contextual folder when opened from Machines", async () => {
    history.replaceState(null, "", "/machines/win/files");
    try {
      const show = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("files-nav.show_in_files")}"]`)!;
      await act(async () => show.click());
      expect(location.pathname).toBe("/machines/win/files");
      expect(new URLSearchParams(location.search).get("path")).toBe(String.raw`C:\Users\me\docs`);
      expect(opened).toEqual([]);
    } finally {
      history.replaceState(null, "", "/");
    }
  });

  it("shows the file in its folder, marked", async () => {
    const show = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("files-nav.show_in_files")}"]`)!;
    await act(async () => show.click());
    expect(opened).toEqual([{ kind: "files", mid: "win", sid: "s1", cwd: "C:\\Users\\me", path: "C:\\Users\\me\\docs", highlight: "README.md" }]);
  });
});
