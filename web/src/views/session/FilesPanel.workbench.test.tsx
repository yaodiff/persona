// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../../lib/use-fs-live", () => ({ useFsLive: () => {} }));
import { setToken } from "../../lib/api";
import { getState } from "../../lib/store";
import { t } from "../../i18n";
import { FilesPanel } from "./FilesPanel";
import { FileViewerPanel } from "./FileViewer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
HTMLElement.prototype.scrollTo ??= () => {};
HTMLElement.prototype.scrollIntoView ??= () => {};

let narrowViewport = false;
window.matchMedia = ((media: string) => ({
  get matches() {
    return narrowViewport && media.includes("max-width");
  },
  media,
  addEventListener() {},
  removeEventListener() {},
})) as unknown as typeof window.matchMedia;

const LISTINGS: Record<string, unknown[]> = {
  "/work/app": [
    { name: "src", dir: true, size: 0 },
    { name: "README.md", dir: false, size: 12 },
  ],
  "/work/app/src": [
    { name: "views", dir: true, size: 0 },
    { name: "index.ts", dir: false, size: 24 },
  ],
  "/work/app/src/views": [
    { name: "Alpha.tsx", dir: false, size: 42 },
    { name: "Beta.tsx", dir: false, size: 38 },
  ],
};

const FILES: Record<string, { name: string; text: string; mime?: string }> = {
  "/work/app/src/views/Alpha.tsx": { name: "Alpha.tsx", text: "export const a = 1;\nexport const target = 2;\nexport const b = 3;\n" },
  "/work/app/src/views/Beta.tsx": { name: "Beta.tsx", text: "export const beta = true;\n" },
  "/work/app/src/index.ts": { name: "index.ts", text: "export * from './views/Alpha';\n" },
  "/work/app/README.md": { name: "README.md", text: "# App\n", mime: "text/markdown" },
};

let host: HTMLDivElement;
let root: Root;
const original = { features: getState().features, machines: getState().machines };

const flush = async () => {
  for (let i = 0; i < 16; i++) await act(async () => { await Promise.resolve(); });
};

const button = (label: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);

beforeEach(() => {
  localStorage.clear();
  setToken("synthetic-A");
  narrowViewport = false;
  Object.assign(getState(), { features: ["file_edit", "file_write"], machines: [{ id: "m1", name: "Work computer", online: true, home: "/work", os: "linux" }] });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const u = new URL(String(input), "http://local.invalid");
    if (u.pathname.endsWith("/git/status")) return Response.json({ is_repo: false, files: [], commits: [] });
    if (u.pathname.endsWith("/fs")) {
      const path = u.searchParams.get("path")!;
      return Response.json({ path, entries: LISTINGS[path] ?? [], truncated: false });
    }
    if (u.pathname.endsWith("/file")) {
      const path = u.searchParams.get("path")!;
      const f = FILES[path];
      if (!f) return new Response("not found", { status: 404 });
      return Response.json({ path, name: f.name, size: f.text.length, mime: f.mime ?? "text/plain", text: f.text, next: 0, revision: "r1", truncated: false, is_dir: false });
    }
    return Response.json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  Object.assign(getState(), original);
  setToken("");
  vi.unstubAllGlobals();
});

describe("File Workbench (#14903)", () => {
  it("opens chat file links in the split workbench with auto-reveal, clean active row, line jump, in-place switching, and tree toggle", async () => {
    await act(async () =>
      root.render(<FileViewerPanel mid="m1" sid="s1" path="/work/app/src/views/Alpha.tsx" cwd="/work/app" line={2} to={2} onClose={() => {}} />),
    );
    await flush();

    // Split workbench with collapsible directory tree open by default on wide viewports.
    expect(host.querySelector('[data-testid="file-workbench"]')).toBeTruthy();
    const tree = host.querySelector('[data-testid="file-workbench-tree"]');
    expect(tree).toBeTruthy();

    // Ancestor folders auto-expanded down to Alpha.tsx and active row highlighted with flat bg-hover-2 fill.
    const activeRow = host.querySelector<HTMLElement>('[role="treeitem"][aria-selected="true"]');
    expect(activeRow).toBeTruthy();
    expect(activeRow?.textContent).toContain("Alpha.tsx");
    expect(activeRow?.className).toContain("bg-hover-2");
    expect(activeRow?.className).toContain("text-ink");
    expect(activeRow?.className).toContain("font-medium");
    expect(activeRow?.className).toContain("rounded-md");

    // Line jump preserved in FileView.
    const markedLine = host.querySelector<HTMLElement>('[data-line="2"]');
    expect(markedLine?.className).toContain("bg-accent-tint");
    expect(host.textContent).toContain("export const target = 2;");

    // Clicking a sibling file in the left tree updates FileView in place while keeping the tree open.
    const betaButton = host.querySelector<HTMLButtonElement>('button[title="/work/app/src/views/Beta.tsx"]')!;
    expect(betaButton).toBeTruthy();
    await act(async () => betaButton.click());
    await flush();

    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeTruthy();
    expect(host.textContent).toContain("export const beta = true;");
    const nextActive = host.querySelector<HTMLElement>('[role="treeitem"][aria-selected="true"]');
    expect(nextActive?.textContent).toContain("Beta.tsx");

    // Collapsing and re-expanding the tree via the top-left toggle button.
    const hideBtn = button(t("files-tree.hideTree"));
    expect(hideBtn).toBeTruthy();
    await act(async () => hideBtn!.click());
    await flush();
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeNull();

    const showBtn = button(t("files-tree.showTree"));
    expect(showBtn).toBeTruthy();
    await act(async () => showBtn!.click());
    await flush();
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeTruthy();
  });

  it("opens files from FilesPanel in the same split workbench with active highlight and in-place switching", async () => {
    await act(async () => root.render(<FilesPanel mid="m1" sid="s1" cwd="/work/app" onClose={() => {}} />));
    await flush();

    // Expand src folder and open index.ts.
    const srcFolder = host.querySelector<HTMLButtonElement>('button[title="/work/app/src"]')!;
    await act(async () => srcFolder.click());
    await flush();

    const indexFile = host.querySelector<HTMLButtonElement>('button[title="/work/app/src/index.ts"]')!;
    await act(async () => indexFile.click());
    await flush();

    expect(host.querySelector('[data-testid="file-workbench"]')).toBeTruthy();
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeTruthy();
    expect(host.textContent).toContain("export * from './views/Alpha';");
    const activeRow = host.querySelector<HTMLElement>('[role="treeitem"][aria-selected="true"]');
    expect(activeRow?.textContent).toContain("index.ts");

    // Switch to README.md in place from the left tree.
    const readme = host.querySelector<HTMLButtonElement>('button[title="/work/app/README.md"]')!;
    await act(async () => readme.click());
    await flush();
    expect(host.querySelector<HTMLElement>('[role="treeitem"][aria-selected="true"]')?.textContent).toContain("README.md");
  });

  it("defaults the directory tree to collapsed on narrow viewports and toggles cleanly", async () => {
    narrowViewport = true;
    await act(async () =>
      root.render(<FileViewerPanel mid="m1" sid="s1" path="/work/app/src/views/Alpha.tsx" cwd="/work/app" onClose={() => {}} />),
    );
    await flush();

    expect(host.querySelector('[data-testid="file-workbench"]')).toBeTruthy();
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeNull();
    expect(host.textContent).toContain("export const target = 2;");

    // Open tree on demand, pick Beta.tsx, and verify it switches and collapses back on narrow screens.
    const showBtn = button(t("files-tree.showTree"));
    expect(showBtn).toBeTruthy();
    await act(async () => showBtn!.click());
    await flush();
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeTruthy();

    const betaButton = host.querySelector<HTMLButtonElement>('button[title="/work/app/src/views/Beta.tsx"]')!;
    await act(async () => betaButton.click());
    await flush();
    expect(host.textContent).toContain("export const beta = true;");
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeNull();
  });

  it("forwards readOnly through FileViewerPanel and disables editing and Show in Files", async () => {
    await act(async () =>
      root.render(<FileViewerPanel mid="m1" sid="s1" path="/work/app/src/views/Alpha.tsx" cwd="/work/app" readOnly onClose={() => {}} />),
    );
    await flush();
    expect(host.textContent).toContain("export const target = 2;");
    expect(button(t("files-editor.edit"))).toBeUndefined();
    expect(button(t("files-nav.show_in_files"))).toBeUndefined();
    expect(host.querySelector('[data-testid="file-workbench-tree"]')).toBeNull();
  });
});
