// @vitest-environment jsdom
// The pages put away in Settings › General › Sidebar pages wait under More (docs/web/web.md "The sidebar's pages"): at the end of Pages, folded,
// opening by itself while one of them is open; the rail and the settings list follow the same choices.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { setSidebarCollapsed } from "../lib/layout";
import { getSidebarPrefs, setPageShown } from "../lib/sidebar-pages";
import { reset } from "../lib/store";
import { SidebarPagesGroup } from "./settings/SidebarPages";
import { NavGrid, Sidebar, SidebarRail } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const pageLinks = () => [...host.querySelectorAll<HTMLAnchorElement>("a")].map((a) => a.textContent?.trim() ?? "");
const moreButton = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("更多"));

beforeEach(() => {
  localStorage.clear();
  setSidebarCollapsed(false);
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("更多 in the sidebar", () => {
  it("is not there while every page is in the list", async () => {
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(moreButton()).toBeUndefined();
  });

  it("holds the pages put away, folded, and opens to them", async () => {
    setPageShown("kb", false);
    setPageShown("health", false);
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    const more = moreButton()!;
    expect(more.textContent).toContain("2");
    expect(more.getAttribute("aria-expanded")).toBe("false");
    // the folded More holds its pages without drawing them (a folded group mounts no rows); opened, they are there
    const kb = () => [...host.querySelectorAll("a")].find((a) => a.textContent?.includes("知识库"));
    expect(kb()).toBeUndefined();
    act(() => more.click());
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(kb()!.closest("[inert]")).toBeNull();
    // Settings stays in the list
    expect(pageLinks()).toContain("设置");
  });

  it("opens by itself while one of its pages is open", async () => {
    setPageShown("kb", false);
    act(() => root.render(<Sidebar route={{ name: "kb" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(moreButton()!.getAttribute("aria-expanded")).toBe("true");
  });

  it("is a ⋯ on the rail that lists them", async () => {
    setPageShown("kb", false);
    act(() => root.render(<SidebarRail route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    const labels = [...host.querySelectorAll("nav a")].map((a) => a.getAttribute("aria-label"));
    expect(labels).not.toContain("知识库");
    const more = host.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!;
    expect(more.getAttribute("aria-label")).toContain("1");
    act(() => more.click());
    await flush();
    expect(document.body.textContent).toContain("知识库");
  });

  it("is a ⋯ in the phone drawer's grid whose pick closes the drawer", async () => {
    const onNavigate = vi.fn();
    const page = (k: string, label: string) => ({ k, to: { name: k } as never, icon: "gear" as const, label });
    act(() => root.render(<NavGrid pages={[page("home", "总览")]} more={[page("kb", "知识库")]} activeKey="home" go={() => () => {}} onNavigate={onNavigate} />));
    act(() => host.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!.click());
    await flush();
    const row = [...document.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemcheckbox"]')].find((el) => el.textContent?.includes("知识库"))!;
    act(() => row.click());
    await flush();
    expect(location.pathname).toBe("/kb");
    expect(onNavigate).toHaveBeenCalledTimes(1);
  });
});

describe("设置 › 通用 › 侧栏", () => {
  it("switches a page between the list and 更多, and 设置 cannot be switched off", async () => {
    act(() => root.render(<SidebarPagesGroup />));
    await flush();
    const sw = (name: string) => host.querySelector<HTMLButtonElement>(`button[role="switch"][aria-label="「${name}」放在侧栏上"]`)!;
    expect(sw("知识库").getAttribute("aria-checked")).toBe("true");
    act(() => sw("知识库").click());
    expect(getSidebarPrefs().hidden).toEqual(["kb"]);
    expect(sw("知识库").getAttribute("aria-checked")).toBe("false");
    expect(host.textContent).toContain("在「更多」里");
    expect(sw("设置").disabled).toBe(true);
    // a built-in page's line says where it is, not its badge's explanation
    const inbox = host.querySelector('li[data-page="inbox"]')!;
    expect(inbox.textContent).not.toContain("·");
  });

  it("moves a page with its arrows, and 恢复默认 forgets every choice", async () => {
    act(() => root.render(<SidebarPagesGroup />));
    await flush();
    const order = () => [...host.querySelectorAll("li[data-page]")].map((li) => li.getAttribute("data-page"));
    const first = order()[0]!;
    const second = order()[1]!;
    const down = host.querySelector<HTMLButtonElement>(`li[data-page="${first}"] button[aria-label$="下移"]`)!;
    act(() => down.click());
    expect(order().slice(0, 2)).toEqual([second, first]);
    const reset = [...host.querySelectorAll("button")].find((b) => b.textContent === "恢复默认")!;
    act(() => reset.click());
    expect(order().slice(0, 2)).toEqual([first, second]);
    expect(getSidebarPrefs()).toEqual({ order: [], shown: [], hidden: [] });
  });
});
