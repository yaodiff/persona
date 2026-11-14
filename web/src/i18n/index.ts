// The web client's i18n (docs/protocol/i18n.md, docs/web/web.md"Internationalization"). The page runs in one language for its whole life: the
// catalog loads before any other module (main.tsx), so t() works anywhere, module scope included, and picking another
// language reloads the page. In the build, the page waits only for the first screen's part of it (startI18n): every
// lazy import waits for the rest (vite.config.ts), so the modules that read those keys still find them loaded.
//
// The languages are the directories under ./locales: each holds its namespace files and _meta.json (its own name, the language subtags it is picked for). Adding a language is adding a
// directory; nothing here lists them (docs/protocol/i18n.md "Adding a language").

/** A language code: a directory under ./locales (zh-Hans, en, ja…). */
export type Lang = string;
export type LangPreference = "system" | Lang;

/** The source language (catalogs are written in it first) and the fallback for anything else. */
export const SOURCE_LANG: Lang = "zh-Hans";
export const FALLBACK_LANG: Lang = "en";

/** A language's _meta.json. */
export interface LangMeta {
  /** Its own name: the picker shows this whatever the interface language is. */
  name: string;
  /** The BCP 47 language subtags (lowercase) that resolve to it: ["zh"] makes zh-TW show zh-Hans. */
  matches: string[];
}

const dirOf = (path: string) => path.split("?")[0].split("/").slice(-2)[0];
const metaFiles = import.meta.glob<LangMeta>("./locales/*/_meta.json", { eager: true, import: "default" });
const META: Record<Lang, LangMeta> = Object.fromEntries(Object.entries(metaFiles).map(([p, m]) => [dirOf(p), m]));

/** Every language, the source first, then the fallback, then the rest by code. */
export const LANGS: readonly Lang[] = Object.keys(META).sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
function rank(l: Lang) {
  return l === SOURCE_LANG ? 0 : l === FALLBACK_LANG ? 1 : 2;
}

/** Each language by its own name: the picker shows these whatever the interface language is. */
export const LANG_NAMES: Record<Lang, string> = Object.fromEntries(LANGS.map((l) => [l, META[l].name]));

/** Language subtag → language, from every _meta.json (startup-preload.ts gets the same table). */
export const LANG_MATCHES: Record<string, Lang> = Object.fromEntries(
  LANGS.flatMap((l) => META[l].matches.map((m) => [m.toLowerCase(), l] as const)),
);

const PREF_KEY = "persona.language";

/** docs/protocol/i18n.md: a tag's language subtag picks the language whose _meta.json lists it (any Chinese, Traditional too,
 *  is zh-Hans); a language the client has no catalog for falls back to en. */
export function resolveLanguage(tag: string | null | undefined): Lang {
  const primary = (tag ?? "").trim().replace(/_/g, "-").split("-")[0].toLowerCase();
  return Object.hasOwn(LANG_MATCHES, primary) ? LANG_MATCHES[primary] : FALLBACK_LANG;
}

/** The system's first preferred language, resolved. Only the first one counts (docs/protocol/i18n.md). */
export function systemLanguage(): Lang {
  if (typeof navigator === "undefined") return FALLBACK_LANG;
  return resolveLanguage(navigator.languages?.[0] ?? navigator.language);
}

export function languagePreference(): LangPreference {
  try {
    const v = localStorage.getItem(PREF_KEY);
    return v && LANGS.includes(v) ? v : "system";
  } catch {
    return "system";
  }
}

export function preferredLanguage(pref: LangPreference = languagePreference()): Lang {
  return pref === "system" ? systemLanguage() : pref;
}

type Catalog = Record<string, string>;

let lang: Lang = SOURCE_LANG;
let catalogs: Record<Lang, Catalog> = {};
/** Counts initI18n calls: the language and catalogs are the last call's, not those of whichever load finishes last. */
let initCalls = 0;

/** A language's catalog module (web/i18n-catalogs.ts): its own entries, and the entries of the fallback languages
 *  (FALLBACK_LANG, SOURCE_LANG) that t() can still reach from it, keyed by language. */
export interface CatalogModule {
  default: Catalog;
  fallbacks: Record<Lang, Catalog>;
}

/** One dynamic import per language, so each language stays an independent cached chunk. */
export type CatalogLoaders = Record<Lang, () => Promise<CatalogModule>>;

/** Language → loader, from a glob's `./locales/<lang>/<file>` keys (admin/catalog.ts uses it for admin.ts too). */
export function byLanguage(glob: Record<string, () => Promise<CatalogModule>>): CatalogLoaders {
  return Object.fromEntries(Object.entries(glob).map(([p, load]) => [dirOf(p), load]));
}

// The web client's catalog, in two parts (web/i18n-catalogs.ts): the keys the first screen reads
// (startup-namespaces.json: namespaces, and keys of others) and the rest. The admin UI's own namespaces (admin-*) are in neither: admin/main.tsx adds them.
const startupLoaders = byLanguage(import.meta.glob<CatalogModule>("./locales/*/_meta.json", { query: "?catalog=client-startup" }));
const restLoaders = byLanguage(import.meta.glob<CatalogModule>("./locales/*/_meta.json", { query: "?catalog=client-rest" }));

/** The rest's load while startI18n's page waits for it (a failed one is dropped, so the next ask tries again). */
let rest: Promise<void> | undefined;
/** Whether the rest is in: false only between startI18n and its rest's arrival. */
let restIn = true;

/**
 * Loads the client catalog of the preferred language (or `forced`, for tests), plus the `extra` catalogs of that
 * language (the admin UI's). Call before importing anything that says t().
 */
export function initI18n(forced?: Lang, ...extra: CatalogLoaders[]): Promise<Lang> {
  return load(forced, [startupLoaders, restLoaders, ...extra], true);
}

/**
 * main.tsx's initI18n in the build: waits only for the first screen's part of the client catalog and loads the rest
 * after it. Every lazy import of the build waits for the rest (i18nRest), so only the first screen's modules can run
 * before it is in, and the build checks that they name no namespace of it (vite.config.ts).
 */
export async function startI18n(forced?: Lang): Promise<Lang> {
  const selected = await load(forced, [startupLoaders], false);
  // A failed load is not kept: the next lazy import asks again.
  void i18nRest()?.catch(() => {});
  return selected;
}

async function load(forced: Lang | undefined, parts: CatalogLoaders[], whole: boolean): Promise<Lang> {
  const requested = forced ?? preferredLanguage();
  const selected = startupLoaders[requested] ? requested : FALLBACK_LANG;
  const call = ++initCalls;
  // t() is synchronous, so its fallbacks must be ready before the page starts. Only the selected language's modules
  // load: each brings the en and zh-Hans entries that t() could fall back to, not those whole catalogs.
  const modules = await Promise.all(parts.map((l) => l[selected]()));
  // A later call took over while this one was loading (#4955): that call's language stands, not this one's.
  if (call !== initCalls) return lang;
  lang = selected;
  catalogs = { [lang]: {} };
  for (const module of modules) merge(module);
  rest = undefined;
  restIn = whole;
  if (typeof document !== "undefined") document.documentElement.lang = lang;
  return lang;
}

function merge(module: CatalogModule) {
  Object.assign(catalogs[lang] ??= {}, module.default);
  for (const [language, entries] of Object.entries(module.fallbacks)) catalogs[language] = Object.assign(catalogs[language] ?? {}, entries);
}

/** Resolves when the whole client catalog is in, or undefined if it already is. The build's lazy imports wait on it. */
export function i18nRest(): Promise<void> | undefined {
  if (restIn) return undefined;
  const call = initCalls;
  const loading = (rest ??= restLoaders[lang]().then(
    (module) => {
      if (call !== initCalls) return;
      merge(module);
      restIn = true;
    },
    (e: unknown) => {
      if (rest === loading) rest = undefined;
      throw e;
    },
  ));
  return loading;
}

// The name vite.config.ts holdLazyImports calls: the chunks it rewrites cannot import this module.
(globalThis as { __personaI18nRest?: typeof i18nRest }).__personaI18nRest = i18nRest;

/** Saves the choice; when it changes the language the page reloads in it (no t() result is cached past a reload). */
export function setLanguagePreference(pref: LangPreference) {
  try {
    if (pref === "system") localStorage.removeItem(PREF_KEY);
    else localStorage.setItem(PREF_KEY, pref);
  } catch {
    // storage refused (private mode): the choice lasts this page only
  }
  if (preferredLanguage(pref) !== lang && typeof location !== "undefined") location.reload();
}

export function currentLanguage(): Lang {
  return lang;
}

// formatLocale()'s last inputs and answer.
let localeKey: string | undefined;
let locale = SOURCE_LANG;

/** The locale for Intl: the system's when it is in the interface language (keeps its region, en-GB dates), else the
 *  interface language itself. */
export function formatLocale(): string {
  const sys = typeof navigator === "undefined" ? "" : (navigator.languages?.[0] ?? navigator.language ?? "");
  // Formatting calls this on every row render: reuse the answer while the system language and `lang` are unchanged.
  const key = `${sys}\u0000${lang}`;
  if (key !== localeKey) {
    localeKey = key;
    locale = resolveFormatLocale(sys);
  }
  return locale;
}

function resolveFormatLocale(sys: string): string {
  const primary = sys.replace(/_/g, "-").split("-")[0].toLowerCase();
  if (sys && LANG_MATCHES[primary] === lang) {
    // a Traditional Chinese system still formats as Simplified: the interface is zh-Hans
    if (lang === SOURCE_LANG && /-(hant|tw|hk|mo)\b/i.test(sys)) return SOURCE_LANG;
    try {
      return Intl.getCanonicalLocales(sys)[0] ?? lang;
    } catch {
      return lang;
    }
  }
  return lang;
}

export type Vars = Record<string, string | number>;

const pluralRules = new Map<Lang, Intl.PluralRules>();

function lookup(language: Lang, key: string, vars?: Vars): string | undefined {
  const catalog = catalogs[language] ?? {};
  if (vars && typeof vars.count === "number") {
    let rules = pluralRules.get(language);
    if (!rules) {
      rules = new Intl.PluralRules(language);
      pluralRules.set(language, rules);
    }
    const text = catalog[`${key}_${rules.select(vars.count)}`] ?? catalog[`${key}_other`];
    if (text !== undefined) return text;
  }
  return catalog[key];
}

/** Fills `{name}` placeholders; a missing value leaves the placeholder visible so the gap is obvious. */
export function interpolate(text: string, vars?: Vars): string {
  if (!vars) return text;
  return text.replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m));
}

/**
 * The text of `key` (`<namespace>.<name>`) in the page's language. A numeric `count` picks `<key>_one` / `<key>_other`
 * by the language's plural rules when those exist. An unknown key comes back as itself (the catalog test catches it).
 */
export function t(key: string, vars?: Vars): string {
  // Each catalog chooses its own plural form before fallback: merging English suffixes into a CJK catalog
  // would shadow that language's unsuffixed entry (count_items vs the English count_items_other).
  const text = lookup(lang, key, vars) ?? lookup(FALLBACK_LANG, key, vars) ?? lookup(SOURCE_LANG, key, vars);
  return text === undefined ? missing(key) : interpolate(text, vars);
}

/** An unknown key's text: the key. Under test, a key asked for before the rest is in throws instead: in the build,
 *  its module would have run ahead of the wait for it (startI18n), or the startup part would lack it. */
function missing(key: string): string {
  if (import.meta.env.MODE === "test" && !restIn) {
    throw new Error(`"${key}" was read before the rest of the catalog was in: add it, or its namespace, to src/i18n/startup-namespaces.json`);
  }
  return key;
}

/** Whether the page's catalog has `key` (plural forms count). For text that is optional per language. */
export function hasKey(key: string): boolean {
  const has = [lang, FALLBACK_LANG, SOURCE_LANG].some((l) => key in (catalogs[l] ?? {}) || `${key}_other` in (catalogs[l] ?? {}));
  if (!has) missing(key);
  return has;
}

// Formatting: always Intl with formatLocale(), never hand-built month / day strings.

// Building an Intl formatter costs tens of microseconds and list rows format on every push, so each (kind, locale,
// options) builds one and keeps it. The options are a few literals per call site; the cap only guards against a caller
// passing ever-new values.
const formatters = new Map<string, unknown>();
const MAX_FORMATTERS = 256;

function formatter<T>(kind: string, opts: object | undefined, make: (locale: string) => T, zone = ""): T {
  const loc = formatLocale();
  const key = `${kind}\u0000${loc}\u0000${zone}\u0000${JSON.stringify(opts ?? {})}`;
  let f = formatters.get(key) as T | undefined;
  if (!f) {
    if (formatters.size >= MAX_FORMATTERS) formatters.clear();
    f = make(loc);
    formatters.set(key, f);
  }
  return f;
}

export function formatNumber(n: number, opts?: Intl.NumberFormatOptions): string {
  return formatter("number", opts, (loc) => new Intl.NumberFormat(loc, opts)).format(n);
}

export function formatDate(d: Date | number | string, opts: Intl.DateTimeFormatOptions = { dateStyle: "medium" }): string {
  const date = d instanceof Date ? d : new Date(d);
  if (Number.isNaN(date.getTime())) return "";
  return formatter("date", opts, (loc) => new Intl.DateTimeFormat(loc, opts), opts.timeZone ? "" : systemZone(date)).format(date);
}

// A DateTimeFormat built without timeZone fixes the system zone at construction, so a tab left open across a zone
// change (travel, a device setting) would keep printing the old wall clock. The zone name itself is only reachable by
// building a formatter, so the key uses the offsets that decide the output instead: this year's January and July
// offsets tell zones (and their DST) apart, and the date's own offset covers the instant being formatted.
function systemZone(date: Date): string {
  const year = new Date().getFullYear();
  return `${new Date(year, 0, 1).getTimezoneOffset()},${new Date(year, 6, 1).getTimezoneOffset()},${date.getTimezoneOffset()}`;
}

export function formatRelative(value: number, unit: Intl.RelativeTimeFormatUnit, opts: Intl.RelativeTimeFormatOptions = { numeric: "auto" }): string {
  return formatter("relative", opts, (loc) => new Intl.RelativeTimeFormat(loc, opts)).format(value, unit);
}

/** A list joined the way the language does it: Chinese with an ideographic comma and "and", "A, B, and C", Japanese with ideographic commas only. */
export function formatList(items: string[], opts: Intl.ListFormatOptions = { type: "conjunction" }): string {
  return formatter("list", opts, (loc) => new Intl.ListFormat(loc, opts)).format(items);
}
