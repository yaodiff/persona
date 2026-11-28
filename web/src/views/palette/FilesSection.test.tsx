import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createFileSearch } from "../../lib/file-find";
import { FilesHeading, FilesNotice, FileResultRow, type FileRow } from "./FilesSection";

// the modules read the colour scheme and this browser's settings when they load
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const row = (path: string, query: string): FileRow => ({ kind: "file", key: `f:${path}`, hit: createFileSearch([path]).search(query)[0], ctx: { mid: "m1", cwd: "/work/app" } });
const draw = (r: FileRow) => renderToStaticMarkup(<FileResultRow row={r} className="row" onMouseMove={() => {}} onClick={() => {}} />);

describe("a file in the palette", () => {
  it("shows the name first with the matched letters marked, and the folders beneath", () => {
    const html = draw(row("web/src/views/Palette.tsx", "pal"));
    expect(html).toContain('<b class="font-semibold text-accent-ink">Pal</b><span>ette.tsx</span>');
    expect(html.indexOf("Pal")).toBeLessThan(html.indexOf("web/src/views"));
    expect(html).toContain("web/src/views");
  });
  it("marks the letters of the folders a term matched too", () => {
    const html = draw(row("web/src/views/Palette.tsx", "views pal"));
    expect(html).toContain('<b class="font-semibold text-accent-ink">views</b>');
  });
  it("has no folder line for a file at the top", () => {
    const html = draw(row("README.md", "read"));
    expect(html).toContain('<b class="font-semibold text-accent-ink">READ</b><span>ME.md</span>');
    expect(html.match(/block truncate/g)).toHaveLength(1);
  });
  it("is a button the palette's rows can be navigated over", () => {
    expect(draw(row("a.ts", "a"))).toMatch(/^<button type="button" class="row">/);
  });
});

describe("the files section's notices", () => {
  it("says the files are being indexed, once something is typed", () => {
    expect(renderToStaticMarkup(<FilesNotice status="loading" text="pal" count={0} />)).toContain("正在建立索引…");
    expect(renderToStaticMarkup(<FilesNotice status="loading" text="" count={0} />)).toBe("");
  });
  it("says so when the directory cannot be read, unless there is something to list", () => {
    expect(renderToStaticMarkup(<FilesNotice status="error" text="pal" count={0} />)).toContain("无法读取该文件夹中的文件");
    expect(renderToStaticMarkup(<FilesNotice status="error" text="pal" count={3} />)).toBe("");
  });
  it("is silent when the files are ready or there is nowhere to search", () => {
    expect(renderToStaticMarkup(<FilesNotice status="ready" text="pal" count={0} />)).toBe("");
    expect(renderToStaticMarkup(<FilesNotice status="off" text="pal" count={0} />)).toBe("");
  });
  it("has a heading", () => {
    expect(renderToStaticMarkup(<FilesHeading />)).toContain(">文件<");
  });
});
