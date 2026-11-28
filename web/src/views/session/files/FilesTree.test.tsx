// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../../../lib/use-fs-live", () => ({ useFsLive: () => {} }));
import { setToken } from "../../../lib/api";
import { FilesTree } from "./FilesTree";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  localStorage.clear();
  setToken("synthetic-A");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const u = new URL(String(input), "http://local.invalid");
    const p = u.searchParams.get("path");
    if (p === "/work/demo") {
      return Response.json({
        path: "/work/demo",
        entries: [
          { name: "src", dir: true, size: 0 },
          { name: "README.md", dir: false, size: 12 },
        ],
        truncated: false,
      });
    }
    if (p === "/work/demo/src") {
      return Response.json({
        path: "/work/demo/src",
        entries: [{ name: "main.ts", dir: false, size: 24 }],
        truncated: false,
      });
    }
    return Response.json({ path: p ?? "", entries: [], truncated: false });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setToken("");
  vi.unstubAllGlobals();
});

describe("FilesTree", () => {
  it("uses unified inset rounded rows, 12px depth indent, neutral folder icons, and bg-hover-2 selection", async () => {
    await act(async () => {
      root.render(
        <FilesTree
          mid="m1"
          sid="s1"
          cwd="/work/demo"
          root="/work/demo"
          reveal={["/work/demo/src"]}
          showHidden={false}
          git={null}
          reloads={0}
          onOpenFile={() => {}}
          onShowHidden={() => {}}
          rowOps={{
            selecting: true,
            isSelected: (p) => p === "/work/demo/src/main.ts",
            toggle: () => {},
            menu: () => {},
          }}
        />,
      );
    });
    await flush();

    const tree = host.querySelector<HTMLElement>('[role="tree"]')!;
    expect(tree.className.split(" ")).toEqual(expect.arrayContaining(["px-1.5", "py-1"]));

    const items = [...host.querySelectorAll<HTMLElement>('[role="treeitem"]')];
    expect(items).toHaveLength(3);

    const [folderRow, nestedFileRow, rootFileRow] = items;
    expect(folderRow.className.split(" ")).toEqual(expect.arrayContaining(["h-7", "rounded-md", "hover:bg-hover"]));
    expect(folderRow.className.split(" ")).not.toContain("bg-hover-2");
    const folderButton = folderRow.querySelector<HTMLButtonElement>("button")!;
    expect(folderButton.style.paddingLeft).toBe("8px");
    expect(folderButton.querySelectorAll("svg").length).toBe(2);
    for (const svg of folderButton.querySelectorAll("svg")) {
      expect(svg.getAttribute("class")).toContain("text-ink-3");
      expect(svg.getAttribute("class")).not.toContain("text-accent");
    }

    expect(nestedFileRow.className.split(" ")).toEqual(expect.arrayContaining(["h-7", "rounded-md", "bg-hover-2", "font-medium", "text-ink"]));
    expect(nestedFileRow.querySelector<HTMLButtonElement>("button")!.style.paddingLeft).toBe("20px");
    expect(nestedFileRow.querySelector<HTMLButtonElement>("button")!.className.split(" ")).toContain("text-tree-row");

    expect(rootFileRow.className.split(" ")).toEqual(expect.arrayContaining(["h-7", "rounded-md", "hover:bg-hover"]));
    expect(rootFileRow.querySelector<HTMLButtonElement>("button")!.style.paddingLeft).toBe("8px");
  });
});
