// The build and Vitest inline each catalog as one module (i18n-catalogs.ts); the dev server imports each namespace
// file. These tests pin that the inlined catalog holds exactly the strings the import form builds, so the page and
// the tests see the same keys whichever form loaded them, and that the client catalog's two parts make up the whole.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { catalogFallbacks, catalogFiles, catalogModule, i18nCatalogs } from "./i18n-catalogs";
import { flatten } from "./src/i18n/locales/flatten";
import startupNamespaces from "./src/i18n/startup-namespaces.json";
import { startupMatcher } from "./src/i18n/startup-keys";

type Catalog = { default: Record<string, string>; fallbacks: Record<string, Record<string, string>> };

const metas = import.meta.glob("./src/i18n/locales/*/_meta.json");
const client = import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=client" });
const admin = import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=admin" });
const startup = import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=client-startup" });
const rest = import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=client-rest" });
// the feature modules' namespaces (src/modules, docs/web/web.md "Modules"): their own catalogs, in neither part
const modules = import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=modules" });
// each module's own catalog, by its id (a module added to src/modules/index.ts gets a line here: the test below fails
// until it does)
const moduleCatalogs: Record<string, Record<string, () => Promise<Catalog>>> = {
  health: import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-health" }),
  voice: import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-voice" }),
  mail: import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-mail" }),
  office: import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-office" }),
  recording: import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-recording" }),
  lark: import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-lark" }),
  "screen-share": import.meta.glob<Catalog>("./src/i18n/locales/*/_meta.json", { query: "?catalog=module-screen-share" }),
};
/** flatten() over a language's namespace files of one kind, keyed like the import form's entries. The files are read
 *  from disk, not through an eager import.meta.glob: that made ~900 JSON modules, the slowest import of the suite. */
function expected(lang: string, kind: "client" | "admin"): Record<string, string> {
  const dir = fileURLToPath(new URL(`./src/i18n/locales/${lang}/`, import.meta.url));
  const files = readdirSync(dir).filter((name) =>
    name.endsWith(".json") && !name.startsWith("_") && name.startsWith("admin-") === (kind === "admin"));
  const read = (name: string) => JSON.parse(readFileSync(join(dir, name), "utf8")) as Record<string, string>;
  const resources = Object.fromEntries(files.map((name) => [`./${name}`, read(name)]));
  if (kind === "client") {
    for (const id of ["office", "mail"]) {
      resources[`./${id}.json`] = JSON.parse(readFileSync(join(dir, "../../../modules", id, "locales", lang, `${id}.json`), "utf8"));
    }
  }
  return flatten(resources);
}

const langs = Object.keys(metas).map((path) => path.split("/").at(-2)!);

describe("i18n catalogs under Vitest", () => {
  it("finds every language", () => {
    expect(langs).toEqual(expect.arrayContaining(["en", "zh-Hans"]));
  });

  for (const lang of langs) {
    const key = `./src/i18n/locales/${lang}/_meta.json`;
    it(`${lang}: the inlined client catalog equals flatten() of its namespace files`, async () => {
      const catalog = (await client[key]()).default;
      expect(Object.keys(catalog).length).toBeGreaterThan(0);
      expect(catalog).toEqual(expected(lang, "client"));
    });
    it(`${lang}: the inlined admin catalog equals flatten() of its admin-*.json files`, async () => {
      expect((await admin[key]()).default).toEqual(expected(lang, "admin"));
    });
    it(`${lang}: search menu, jumps, return and load outcomes are cold-ready while dialog text stays deferred`, async () => {
      const [first, second] = await Promise.all([startup[key](), rest[key]()]);
      const primary = ["stopped", "busy", "load_failed", "no_progress", "page_cap", "not_found", "origin_changed", "return", "menu"];
      for (const name of primary) {
        const k = `chat-chatview.search_${name}`;
        expect(first.default[k]).toBeTruthy();
        expect(second.default[k]).toBeUndefined();
      }
      expect(Object.keys(first.default).some(k => k.startsWith("chat-chatsearch."))).toBe(false);
      expect(second.default["chat-chatsearch.title"]).toBe(first.default["chat-chatview.search_menu"]);
      expect(first.default["chat-chatview.search_load_failed"]).toBeTruthy();
      expect(second.default["chat-chatsearch.load_failed"]).toBeUndefined();
      const child = readFileSync(fileURLToPath(new URL("./src/views/chat/ChatSearch.tsx", import.meta.url)), "utf8");
      expect(child).toContain('t("chat-chatview.search_load_failed")');
      expect(child).not.toContain("chat-chatsearch.load_failed");
      const source = readFileSync(fileURLToPath(new URL("./src/views/chat/ChatView.tsx", import.meta.url)), "utf8");
      expect(source).not.toContain("chat-chatsearch.");
      for (const name of primary) expect(source).toContain(`chat-chatview.search_${name}`);
    });
    it(`${lang}: client-startup holds what startup-namespaces.json lists, client-rest the rest but the modules', together the client catalog`, async () => {
      const [whole, first, second, third] = await Promise.all([client[key](), startup[key](), rest[key](), modules[key]()]);
      const listed = startupMatcher(startupNamespaces);
      expect(Object.keys(first.default).every(listed)).toBe(true);
      expect(Object.keys(second.default).some(listed)).toBe(false);
      const ofModule = (k: string) => Object.keys(moduleCatalogs).some((id) => k.startsWith(`${id}.`) || k.startsWith(`${id}-`));
      expect(Object.keys(third.default).length).toBeGreaterThan(0);
      expect(Object.keys(third.default).every(ofModule)).toBe(true);
      expect(Object.keys({ ...first.default, ...second.default }).some(ofModule)).toBe(false);
      expect({ ...first.default, ...second.default, ...third.default }).toEqual(whole.default);
      const fallbacks: Catalog["fallbacks"] = {};
      for (const part of [first, second, third]) for (const [l, entries] of Object.entries(part.fallbacks)) fallbacks[l] = { ...fallbacks[l], ...entries };
      expect(fallbacks).toEqual(whole.fallbacks);
    });

    // The core-only lane (web/core-only.ts, #13932) has an empty module list, while the modules' catalogs stay on disk.
    it.skipIf(process.env.PERSONA_WEB_MODULES === "none")(`${lang}: each module's catalog holds its namespaces only, and together they are all the modules' strings`, async () => {
      const { MODULES } = await import("./src/modules");
      expect(Object.keys(moduleCatalogs).sort()).toEqual(MODULES.map((m) => m.id).sort());
      const all = await modules[key]();
      const together: Catalog["default"] = {};
      for (const [id, catalog] of Object.entries(moduleCatalogs)) {
        const own = (await catalog[key]()).default;
        expect(Object.keys(own).length, id).toBeGreaterThan(0);
        expect(Object.keys(own).every((k) => k.startsWith(`${id}.`) || k.startsWith(`${id}-`)), id).toBe(true);
        Object.assign(together, own);
      }
      expect(together).toEqual(all.default);
    });
  }
});

describe("catalogModule", () => {
  const meta = fileURLToPath(new URL("./src/i18n/locales/en/_meta.json", import.meta.url));

  it("emits one import per namespace file for the dev server", () => {
    const code = catalogModule(meta, "client");
    const files = catalogFiles(meta, "client");
    expect(files.length).toBeGreaterThan(1);
    expect(code.match(/^import ns\d+ from /gm)).toHaveLength(files.length);
    expect(code).toContain(`import { flatten } from `);
    expect(code).toMatch(/^export default flatten\(\{/m);
    expect(code).not.toContain("JSON.parse");
  });

  it("emits a single module without imports when inlined", () => {
    const code = catalogModule(meta, "admin", true);
    expect(code).not.toMatch(/^import /m);
    expect(code).toMatch(/^export default JSON\.parse\(/);
  });

  it("inlines under Vitest and watches every namespace file, the fallback languages' too", () => {
    const watched: string[] = [];
    const load = i18nCatalogs.load as (this: { addWatchFile(file: string): void }, id: string) => string;
    const code = load.call({ addWatchFile: (file) => watched.push(file) }, `\0i18n-catalog:client:${meta}.js`);
    expect(code).toBe(catalogModule(meta, "client", true));
    // en falls back only to zh-Hans: its files feed `fallbacks`
    const zh = fileURLToPath(new URL("./src/i18n/locales/zh-Hans/_meta.json", import.meta.url));
    expect(watched).toEqual([meta, ...catalogFiles(meta, "client"), ...catalogFiles(zh, "client")]);
  });

  it("inlines in the build, with the strings and fallbacks the import form gives, and imports when serving", () => {
    const vitest = process.env.VITEST;
    const plugin = i18nCatalogs as unknown as {
      configResolved(config: { command: string }): void;
      load(this: { addWatchFile(file: string): void }, id: string): string;
    };
    const load = (command: string) => {
      plugin.configResolved({ command });
      return plugin.load.call({ addWatchFile: () => {} }, `\0i18n-catalog:client:${meta}.js`);
    };
    delete process.env.VITEST;
    try {
      const code = load("build");
      // the module's two exports, evaluated as a bundle would
      const run = new Function(code.replace(/^export default /m, "const d = ").replace(/^export const fallbacks = /m, "const f = ") + "\nreturn [d, f];");
      const [catalog, fallbacks] = run() as [Record<string, string>, Record<string, Record<string, string>>];
      expect(catalog).toEqual(expected("en", "client"));
      expect(fallbacks).toEqual(catalogFallbacks(fileURLToPath(new URL("./src/i18n/locales/en", import.meta.url)), "client"));
      expect(load("serve")).toBe(catalogModule(meta, "client"));
    } finally {
      process.env.VITEST = vitest;
      plugin.configResolved({ command: "serve" });
    }
  });

  it("lists in startup-namespaces.json only namespaces and keys the client catalog has, none twice", () => {
    const keys = Object.keys(expected("en", "client"));
    const namespaces = new Set(keys.map((k) => k.split(".")[0]));
    const stale = startupNamespaces.filter((entry) => (entry.includes(".") ? !keys.some((k) => k.startsWith(entry)) : !namespaces.has(entry)));
    expect(stale).toEqual([]);
    // "ns" takes "ns.a", and "ns.a" takes "ns.ab": the narrower entry says nothing
    const covers = (wider: string, entry: string) => wider !== entry && (wider.includes(".") ? entry.startsWith(wider) : entry.startsWith(`${wider}.`));
    expect(startupNamespaces.filter((entry) => startupNamespaces.some((wider) => covers(wider, entry)))).toEqual([]);
    expect(new Set(startupNamespaces).size).toBe(startupNamespaces.length);
  });

  it("gives the dev server each part of the client catalog with the keys the build gives it", async () => {
    for (const [kind, built] of [["client-startup", startup], ["client-rest", rest]] as const) {
      // the import form, its imports read from disk as the dev server would load them
      const body = catalogModule(meta, kind)
        .replace(/^import (ns\d+) from ("[^"]+");$/gm, (_, name, path) => `const ${name} = read(${path});`)
        .replace(/^import startupEntries from ("[^"]+");$/m, (_, path) => `const startupEntries = read(${path});`)
        .replace(/^import \{ (flatten|startupMatcher) \} from .*$/gm, "")
        .replace(/^export default /m, "const d = ")
        .replace(/^export const fallbacks = /m, "const f = ");
      expect(body).not.toMatch(/^(import|export) /m);
      const run = new Function("read", "flatten", "startupMatcher", `${body}\nreturn d;`);
      const catalog = run((path: string) => JSON.parse(readFileSync(path, "utf8")), flatten, startupMatcher) as Record<string, string>;
      expect(catalog).toEqual((await built["./src/i18n/locales/en/_meta.json"]()).default);
    }
  });

  it("splits client and admin namespaces by the admin- prefix", () => {
    const name = (file: string) => file.replace(/^.*\//, "");
    expect(catalogFiles(meta, "admin").every((file) => name(file).startsWith("admin-"))).toBe(true);
    expect(catalogFiles(meta, "client").some((file) => name(file).startsWith("admin-"))).toBe(false);
    expect(catalogFiles(meta, "client").some((file) => name(file).startsWith("_"))).toBe(false);
  });
});
