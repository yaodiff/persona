// Language catalogs are resources, not hand-maintained TypeScript entry points. Vite bundles one client catalog
// and one admin catalog per language from the namespace JSON beside _meta.json; adding a directory needs no code.
//
// The client catalog comes in two parts: client-startup holds what the first screen reads
// (src/i18n/startup-namespaces.json: whole namespaces, and single keys of others, src/i18n/startup-keys.ts; the build
// checks it: vite.config.ts), client-rest everything else, loaded after the first screen. `client` is the whole of
// it, for tests.
//
// A feature module's namespaces (src/modules/<id>, docs/web/web.md "Modules": `<id>.json` and `<id>-*.json`) are in
// neither part: they are its own catalog, `module-<id>`, loaded with its code and never while it is off. `modules` is all
// of them together, for tests. `client` still has every namespace, so the checks over it see the modules' keys too.
//
// The page loads only its own language's catalog (src/i18n/index.ts initI18n). t() still falls back to en, then
// zh-Hans, for a key the language lacks (docs/protocol/i18n.md), so each catalog module also exports `fallbacks`: the entries
// of those two languages that t() could still reach from this one, computed here at build time. With complete
// catalogs (src/i18n/catalog.test.ts) that is almost nothing, instead of two more full catalogs on the first load.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import type { Plugin } from "vite";
import { flatten } from "./src/i18n/locales/flatten";
import { startupMatcher } from "./src/i18n/startup-keys";

type Kind = "client" | "client-startup" | "client-rest" | "admin" | "modules" | `module-${string}`;
const KINDS: readonly string[] = ["client", "client-startup", "client-rest", "admin", "modules"] satisfies Kind[];
const isKind = (kind: string): kind is Kind => KINDS.includes(kind) || /^module-[a-z][a-z0-9-]*$/.test(kind);
type Catalog = Record<string, string>;

/** The languages t() falls back to, in order: FALLBACK_LANG and SOURCE_LANG of src/i18n/index.ts (a test checks). */
export const FALLBACK_LANGS = ["en", "zh-Hans"] as const;

const PLURAL = /_(zero|one|two|few|many|other)$/;

/** src/i18n/startup-namespaces.json, from a language's _meta.json: what client-startup holds. */
export function startupNamespacesFile(metaPath: string): string {
  return join(dirname(metaPath), "..", "..", "startup-namespaces.json");
}

/** startup-namespaces.json's entries (a namespace, or the start of a key in one: src/i18n/startup-keys.ts). */
export function startupEntries(metaPath: string): string[] {
  return JSON.parse(readFileSync(startupNamespacesFile(metaPath), "utf8")) as string[];
}

const split = (kind: Kind) => kind === "client-startup" || kind === "client-rest";

/** The feature modules (src/modules/<id>/module.ts), from a language's _meta.json. */
export function moduleIds(metaPath: string): string[] {
  const dir = join(dirname(metaPath), "..", "..", "..", "modules");
  try {
    return readdirSync(dir, { withFileTypes: true })
      .filter((e) => e.isDirectory() && readdirSync(join(dir, e.name)).includes("module.ts"))
      .map((e) => e.name)
      .sort();
  } catch {
    return [];
  }
}

/** The module a namespace belongs to (`<id>` or `<id>-…`), if any. */
export function namespaceModule(ns: string, modules: readonly string[]): string | undefined {
  return modules.find((id) => ns === id || ns.startsWith(`${id}-`));
}

/**
 * The namespace JSON files of one catalog, sorted, as absolute paths. A namespace the startup list takes some keys of
 * has its file in both parts of the client catalog; readCatalog and the dev server's module keep each part's keys.
 */
export function catalogFiles(metaPath: string, kind: Kind): string[] {
  const dir = dirname(metaPath);
  const entries = split(kind) ? startupEntries(metaPath) : [];
  const whole = new Set(entries.filter((e) => !e.includes(".")));
  const named = new Set(entries.map((e) => e.split(".")[0]));
  const modules = moduleIds(metaPath);
  const files = readdirSync(dir).map((name) => join(dir, name));
  // A module can keep its namespace files alongside its implementation. Only catalogs
  // that include that module read its directory, including the aggregate test catalogs.
  for (const id of modules) {
    if (kind !== "client" && kind !== "modules" && kind !== `module-${id}`) continue;
    const local = join(dir, "../../../modules", id, "locales", basename(dir));
    if (existsSync(local)) {
      for (const name of readdirSync(local)) {
        if (namespaceModule(basename(name, ".json"), modules) === id) files.push(join(local, name));
      }
    }
  }
  return files.filter((file) => {
    const name = basename(file);
    if (!name.endsWith(".json") || name.startsWith("_") || name.startsWith("admin-") !== (kind === "admin")) return false;
    const ns = name.slice(0, -".json".length);
    const owner = namespaceModule(ns, modules);
    if (kind === "modules") return owner !== undefined;
    if (kind.startsWith("module-")) return owner === kind.slice("module-".length);
    if (owner && split(kind)) return false;
    return kind === "client-startup" ? named.has(ns) : kind === "client-rest" ? !whole.has(ns) : true;
  }).sort();
}

function readCatalog(dir: string, kind: Kind): Catalog {
  const meta = join(dir, "_meta.json");
  const catalog = flatten(Object.fromEntries(catalogFiles(meta, kind).map((file) =>
    [`./${basename(file)}`, JSON.parse(readFileSync(file, "utf8")) as Catalog],
  )));
  if (!split(kind)) return catalog;
  const startup = startupMatcher(startupEntries(meta));
  return Object.fromEntries(Object.entries(catalog).filter(([key]) => startup(key) === (kind === "client-startup")));
}

/** Every key of the client catalog (`namespace.name`) in the language of `metaPath`: what the build checks against. */
export function clientKeys(metaPath: string): string[] {
  return Object.keys(readCatalog(dirname(metaPath), "client"));
}

/**
 * The entries of `fallback` that t() can still read when the page's catalog is `own`: everything but the keys `own`
 * answers itself. A key `own` has answers every lookup of it; a plural form (`b_one`) is never read when `own` has the
 * base `b` or `b_other`, because a counted lookup of `b` then stops in `own`.
 */
export function fallbackEntries(own: Catalog, fallback: Catalog): Catalog {
  const out: Catalog = {};
  for (const [key, text] of Object.entries(fallback)) {
    if (Object.hasOwn(own, key)) continue;
    const base = key.replace(PLURAL, "");
    if (base !== key && (Object.hasOwn(own, base) || Object.hasOwn(own, `${base}_other`))) continue;
    out[key] = text;
  }
  return out;
}

/** The other languages whose files a catalog's `fallbacks` reads: FALLBACK_LANGS without the catalog's own. */
function fallbackLangs(dir: string): string[] {
  return FALLBACK_LANGS.filter((l) => l !== basename(dir));
}

/** The fallback languages' entries t() may need for the language in `dir`, keyed by language. A key and its plural
 *  forms land in the same part (a startup entry takes every key that begins with it), so each part of the client
 *  catalog gets just its share of the whole's. */
export function catalogFallbacks(dir: string, kind: Kind): Record<string, Catalog> {
  const own = readCatalog(dir, kind);
  return Object.fromEntries(fallbackLangs(dir).map((l) => [l, fallbackEntries(own, readCatalog(join(dirname(dir), l), kind))]));
}

// The import form imports every namespace JSON, so the dev server treats each as its own module and an edit to one
// reloads only it. The inline form (the build and tests) reads and flattens them here into one module holding one
// JSON.parse of a string: in the bundle that is about 26 KB smaller gzipped per language than ~300 object literals
// (each key's namespace prefix repeated, and per-module wrapping), and engines parse JSON faster than the same object
// as code; under Vitest every test file loads the catalogs, and ~300 JSON modules per language through the module
// runner were most of the suite's setup time.
export function catalogModule(metaPath: string, kind: Kind, inline = false): string {
  const dir = dirname(metaPath);
  const fallbacks = `export const fallbacks = ${JSON.stringify(catalogFallbacks(dir, kind))};`;
  if (inline) {
    return [`export default JSON.parse(${JSON.stringify(JSON.stringify(readCatalog(dir, kind)))});`, fallbacks].join("\n");
  }
  const path = (file: string) => JSON.stringify(file.replace(/\\/g, "/"));
  const namespaces = catalogFiles(metaPath, kind);
  const imports = namespaces.map((file, i) => `import ns${i} from ${path(file)};`);
  const entries = namespaces.map((name, i) => `${JSON.stringify(`./${basename(name)}`)}: ns${i}`);
  const whole = `flatten({${entries.join(",")}})`;
  // A part of the client catalog keeps its own keys of the namespaces the startup list splits.
  const part = split(kind)
    ? [
      `import { startupMatcher } from ${path(join(dir, "../../startup-keys.ts"))};`,
      `import startupEntries from ${path(startupNamespacesFile(metaPath))};`,
      `const startup = startupMatcher(startupEntries);`,
      `export default Object.fromEntries(Object.entries(${whole}).filter(([key]) => startup(key) === ${kind === "client-startup"}));`,
    ]
    : [`export default ${whole};`];
  return [
    ...imports,
    `import { flatten } from ${path(join(dir, "../flatten.ts"))};`,
    ...part,
    fallbacks,
  ].join("\n");
}

/** Whether Vite is building (not serving): set by configResolved. */
let building = false;

export const i18nCatalogs: Plugin = {
  name: "i18n-resource-catalogs",
  enforce: "pre",
  configResolved(config) {
    building = config.command === "build";
  },
  resolveId(source, importer) {
    const [path, query] = source.split("?");
    if (!path.endsWith("/_meta.json")) return;
    const kind = new URLSearchParams(query).get("catalog");
    const absolute = path.startsWith(".") && importer ? join(dirname(importer.split("?")[0]), path) : path;
    if (kind && isKind(kind)) return `\0i18n-catalog:${kind}:${absolute}.js`;
  },
  load(id) {
    const match = /^\0i18n-catalog:([a-z0-9-]+):(.+)\.js$/.exec(id);
    if (!match) return;
    const [, kind, path] = match;
    this.addWatchFile(path);
    if (kind.startsWith("client-")) this.addWatchFile(startupNamespacesFile(path));
    const inline = building || Boolean(process.env.VITEST);
    // Inlined JSON is no longer an import, so watch each file for Vitest's (and vite build --watch's) watch mode to
    // rerun on an edit.
    if (inline) for (const file of catalogFiles(path, kind as Kind)) this.addWatchFile(file);
    // `fallbacks` reads the fallback languages' files: editing one rebuilds this module too. (Vite treats a watched
    // path as an import of the module, so only files: a directory would fail import analysis.)
    const dir = dirname(path);
    for (const l of fallbackLangs(dir)) for (const file of catalogFiles(join(dirname(dir), l, "_meta.json"), kind as Kind)) this.addWatchFile(file);
    return catalogModule(path, kind as Kind, inline);
  },
};
