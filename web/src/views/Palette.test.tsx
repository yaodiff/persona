import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { closePalette, openPalette, registerContextCommands, setPaletteQuery } from "../lib/palette";
import { registerShortcut } from "../lib/shortcuts";
import { Palette } from "./Palette";
import { placeWords } from "./palette/PlaceWords";
import { initI18n } from "../i18n";
import { getState } from "../lib/store";
import type { Session } from "../lib/types";
import { placeHits } from "../lib/palette";
import { ShortcutHelp } from "./ShortcutHelp";
import { ShortcutSettings } from "./settings/ShortcutSettings";

// the modules read the colour scheme and this browser's settings when they load
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // The platform is read once and kept (lib/shortcuts.ts): set it before anything renders, or on a Mac (Node has a
  // navigator there, platform MacIntel) the first render keeps ⌘ for the whole file.
  Object.defineProperty(globalThis, "navigator", { value: { platform: "Linux x86_64" }, configurable: true, writable: true });
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const cleanups: (() => void)[] = [];
afterEach(() => {
  while (cleanups.length) cleanups.pop()!();
  closePalette();
  vi.unstubAllGlobals();
});

function render(query: string) {
  vi.stubGlobal("document", { body: {} }); // the portal's target (createPortal is mocked)
  openPalette("jump");
  setPaletteQuery(query);
  return renderToStaticMarkup(<Palette open onClose={() => {}} onNewAgent={() => {}} />);
}

describe("the palette's command mode", () => {
  it("is the same search as ever without a `>`", () => {
    const html = render("");
    expect(html).toContain('aria-label="搜索会话"');
    expect(html).toContain("搜索会话、收件箱、终端、目录、空间、机器、页面…");
    expect(html).not.toContain("命令面板");
  });
  it("hints at `>` in the search's footer", () => {
    expect(render("")).toMatch(/&gt;<\/kbd> 命令/);
  });
  it("lists the registered commands with their chords as keycaps, under the heading of where they work", () => {
    cleanups.push(registerShortcut("right-panel", () => {}), registerShortcut("settings", () => {}), registerShortcut("collapse-tree", () => {}));
    vi.stubGlobal("navigator", { platform: "Linux x86_64" });
    const html = render(">");
    expect(html).toContain('aria-label="命令面板"');
    expect(html).toContain("输入命令…");
    expect(html).toContain("显示 / 隐藏右侧面板");
    expect(html).toMatch(/打开设置<\/span>.*?<kbd[^>]*>Ctrl<\/kbd><kbd[^>]*>,<\/kbd>/);
    expect(html).toMatch(/折叠侧边栏的全部分组<\/span>.*?>Ctrl<\/kbd>.*?>Alt<\/kbd>.*?>Shift<\/kbd>.*?>↑<\/kbd>/);
    for (const heading of ["全局", "会话页", "侧边栏"]) expect(html).toContain(`uppercase">${heading}</div>`);
    // a command nothing handles here is not listed
    expect(html).not.toContain("终端 ⇄ 对话");
  });
  it("filters by what follows the `>`", () => {
    cleanups.push(registerShortcut("right-panel", () => {}), registerShortcut("settings", () => {}));
    const html = render(">设置");
    expect(html).toContain("打开设置");
    expect(html).not.toContain("显示 / 隐藏右侧面板");
  });
  it("lists what other modules registered, first, under their own heading", () => {
    cleanups.push(registerContextCommands(() => [{ id: "node:rename", section: "节点", title: "重命名…", run: () => {} }]));
    const html = render(">");
    expect(html).toContain("重命名…");
    expect(html.indexOf("重命名…")).toBeLessThan(html.indexOf("新建 Agent"));
    expect(html).toContain('uppercase">节点</div>');
  });
  it("draws a heading that holds a name as written: the capitals of a label would rewrite it", () => {
    // the node palette's "Space · weather" read "SPACE · WEATHER", a title's "Astro" "ASTRO"
    cleanups.push(registerContextCommands(() => [{ id: "node:close", section: "空间 · weather", sectionAsWritten: true, title: "关闭", run: () => {} }]));
    const html = render(">");
    expect(html).toContain('text-ink-3">空间 · weather</div>');
    expect(html).not.toContain('uppercase">空间 · weather</div>');
  });
  it("says so when no command matches, and how to get back to the search", () => {
    const html = render(">zzzz");
    expect(html).toContain("没有匹配的命令");
    expect(html).toContain("删掉「&gt;」");
  });
});

describe("the shortcut help", () => {
  it("is closed until it is opened (the dialog draws nothing)", () => {
    expect(renderToStaticMarkup(<ShortcutHelp />)).toBe("");
  });
});

describe("设置 › 快捷键", () => {
  it("lists every command with its chord, and offers 全部恢复默认", () => {
    const html = renderToStaticMarkup(<ShortcutSettings />);
    expect(html).toContain("命令面板");
    expect(html).toContain("全部恢复默认");
    expect(html).toContain("跳转到会话或终端窗格");
    expect(html).toContain("切到第 1–9 台电脑");
    // nothing is rebound: no per-row "Reset to default"
    expect(html).not.toContain("的默认快捷键");
  });
});

describe("the palette's 前往 (the Mac's quick open)", () => {
  it("goes to a page by its name or another word for it", () => {
    const html = render("设置");
    expect(html).toContain("前往");
    expect(html).toContain("设置");
    // Alias matching loads on the first typed query; mounted coverage lives in Palette.runtime-search.test.tsx.
    const places = ["machines", "todos"].map((key) => ({ key, title: key, words: placeWords(key), icon: "list" as const, to: { name: "home" } as const }));
    expect(placeHits(places, "cpu").map((hit) => hit.key)).toEqual(["p:machines"]);
    expect(placeHits(places, "todo").map((hit) => hit.key)).toEqual(["p:todos"]);
  });
});

describe("a page's words", () => {
  it("answer to the Chinese names in an English UI too (the Mac's quick open)", async () => {
    await initI18n("en");
    try {
      const places = ["inbox", "todos", "kb", "machines", "settings", "persona"].map((key) => ({ key, title: key, icon: "list" as const, to: { name: "home" } as const, words: placeWords(key) }));
      expect(placeHits(places, "飞书").map((h) => h.key)).toEqual(["p:inbox"]);
      expect(placeHits(places, "待办").map((h) => h.key)).toEqual(["p:todos"]);
      expect(placeHits(places, "知识库").map((h) => h.key)).toEqual(["p:kb"]);
      expect(placeHits(places, "内存").map((h) => h.key)).toEqual(["p:machines"]);
    } finally {
      await initI18n("zh-Hans");
    }
  });
});

// A Persona chat's line under its title said "Persona · ~/.persona/assistant", the same folder for every chat and an
// engineering path besides: it says what was said last, as the chats list does.
describe("a Persona chat in the search", () => {
  it("says what was said last, without the assistant's folder", () => {
    const kept = getState().assistant;
    Object.assign(getState(), {
      assistant: {
        machine_id: "m1",
        main: "p0",
        conversations: [
          { machine_id: "m1", session_id: "p1", title: "今天要处理的事", assistant: true, cwd: "/Users/me/.persona/assistant", last_text: "**发布说明**我来整理，先列出改动。", updated_at: "2026-11-10T09:00:00Z" },
          { machine_id: "m1", session_id: "p2", title: "群里的发布计划", assistant: true, cwd: "/Users/me/.persona/assistant", updated_at: "2026-11-10T08:00:00Z" },
        ] as unknown as Session[],
      },
    });
    try {
      const html = render("");
      expect(html).not.toContain(".persona/assistant");
      expect(html).toContain("发布说明我来整理，先列出改动。");
      // nothing said yet: just whose chat it is
      expect(html).toMatch(/群里的发布计划<\/span>(?:(?!<\/button>).)*>Persona<\/span>/s);
    } finally {
      Object.assign(getState(), { assistant: kept });
    }
  });
});
