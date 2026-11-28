// The shell's closed lists name no feature module (#13434, #13430 section 6; docs/web/web.md "Modules"): a module's page,
// sidebar entry and Settings tab come from the module host, so a module's id, route base or sidebar key appearing in the
// Route union, the sidebar's built-in pages or the Settings tabs means it was wired by hand again. Fails on a new one.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { BUILTIN_PAGE_COLORS } from "../lib/sidebar-icons";
import { NAV_PAGES } from "../lib/persona-nav";
import { TABS } from "../views/settings/nav";
import { MODULES } from ".";

const src = new URL("../", import.meta.url);
const read = (path: string) => readFileSync(new URL(path, src), "utf8");

/** Every name a module goes by in the shell: its id, its route bases, its sidebar keys, its Settings tab keys. */
const names = [...new Set(MODULES.flatMap((m) => [m.id, ...(m.routes ?? []).map((r) => r.base), ...(m.navItems ?? []).map((n) => n.key), ...(m.settingsTabs ?? []).map((s) => s.key)]))];

/** The Route union's variant names (lib/router.ts). */
function routeNames(): string[] {
  const source = read("lib/router.ts");
  const union = source.slice(source.indexOf("export type Route ="), source.indexOf("/** A conversation page's"));
  return [...union.matchAll(/\{ name: "([^"]+)"/g)].map((m) => m[1]!);
}

describe("the shell's closed lists", () => {
  it("knows the modules", () => {
    expect(names.length).toBeGreaterThan(0);
    // every directory under src/modules is a listed module (and the list names no other)
    const dirs = readdirSync(new URL("./", import.meta.url), { withFileTypes: true }).filter((e) => e.isDirectory()).map((e) => e.name).sort();
    expect(MODULES.map((m) => m.id).sort()).toEqual(dirs);
  });

  it("has no module in the Route union", () => {
    const route = routeNames();
    expect(route).toContain("module");
    expect(route.filter((n) => names.includes(n))).toEqual([]);
  });

  it("has no module among the sidebar's built-in pages or their colors", () => {
    expect(NAV_PAGES.filter((k) => names.includes(k))).toEqual([]);
    expect(Object.keys(BUILTIN_PAGE_COLORS).filter((k) => names.includes(k))).toEqual([]);
    // SidebarParts.tsx's own entries: `<key>: { k: "<key>", …`
    const sidebar = read("views/SidebarParts.tsx");
    expect([...sidebar.matchAll(/\{ k: "([^"]+)"/g)].map((m) => m[1]!).filter((k) => names.includes(k))).toEqual([]);
  });

  it("has no module among the Settings tabs", () => {
    expect(TABS.filter((k) => names.includes(k))).toEqual([]);
  });

  it("loads no module's code from App: only the module list", () => {
    const app = read("App.tsx");
    expect(app).not.toMatch(/import\(\s*["']\.\/modules\//);
    expect(app).not.toMatch(/from\s+["']\.\/modules\//);
  });
});

describe("each module's namespaces", () => {
  it("are its own (`<id>` or `<id>-…`) and exist in every language", () => {
    const langs = readdirSync(new URL("i18n/locales/", src), { withFileTypes: true }).filter((e) => e.isDirectory()).map((e) => e.name);
    for (const m of MODULES) {
      for (const ns of m.i18nNamespaces ?? []) {
        expect(ns === m.id || ns.startsWith(`${m.id}-`), `${m.id}: ${ns}`).toBe(true);
        for (const lang of langs) {
          const files = [`i18n/locales/${lang}/${ns}.json`, `modules/${m.id}/locales/${lang}/${ns}.json`];
          expect(files.filter((path) => existsSync(new URL(path, src))), `${lang}/${ns}.json`).toHaveLength(1);
        }
      }
      // and none of them is on the first screen: they load with the module
      const startup = JSON.parse(read("i18n/startup-namespaces.json")) as string[];
      expect(startup.filter((e) => (m.i18nNamespaces ?? []).includes(e.split(".")[0]!))).toEqual([]);
    }
  });
});
