// @vitest-environment node
// The catalogs and the code agree (docs/web/web.md "Internationalization"): the three languages have the same namespaces, keys and
// placeholders; every t("literal") key exists and gets a value for each placeholder its text uses; every key is used; and no interface text is written into the code in Chinese. That last
// one is a ratchet while the strings move into catalogs: a file may only lose hard-coded strings, never gain them
// (hardcoded-baseline.json; a file that reaches zero leaves it). Only a gain fails: a file under its record is noted,
// and lowering the record is tidying anyone can do (docs/operations/testing.md, "Count ratchets"). Comments and tests
// don't count; a line that has to keep CJK text (it matches what someone else writes, like a hub's output) says
// `i18n-ignore` in a comment.
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { basename, join, relative } from "node:path";
import ts from "typescript";
import { catalogFiles } from "../../i18n-catalogs";
import { describe, expect, it } from "vitest";
import { adminCatalog } from "../admin/catalog";
import { FALLBACK_LANG, LANG_NAMES, LANGS, SOURCE_LANG } from "./index";

const clientFiles = import.meta.glob<Record<string, string>>("./locales/*/_meta.json", { query: "?catalog=client", eager: true, import: "default" });
const clients: Record<string, Record<string, string>> = Object.fromEntries(
  Object.entries(clientFiles).map(([p, c]) => [p.split("?")[0].split("/").slice(-2)[0], c]),
);
// The admin catalogs load while the file is collected, through the same loaders as the admin page: loading them inside
// the test counted toward its 5 s timeout, which the full parallel suite went past (#4955).
const admins: Record<string, Record<string, string>> = Object.fromEntries(
  await Promise.all(LANGS.map(async (lang) => [lang, (await adminCatalog[lang]()).default] as const)),
);

const SRC = join(__dirname, "..");
const LOCALES = join(__dirname, "locales");
const BASELINE = join(__dirname, "hardcoded-baseline.json");
const PLURAL_FORMS = ["zero", "one", "two", "few", "many", "other"] as const;
const PLURAL = /_(zero|one|two|few|many|other)$/;
const CJK = /[　-〿぀-ヿ㐀-䶿一-鿿！-｠]/;

type Catalog = Record<string, string>;

function catalogs(): Record<string, Record<string, Catalog>> {
  const out: Record<string, Record<string, Catalog>> = {};
  for (const lang of LANGS) {
    out[lang] = {};
    const meta = join(LOCALES, lang, "_meta.json");
    for (const f of [...catalogFiles(meta, "client"), ...catalogFiles(meta, "admin")]) {
      out[lang][basename(f, ".json")] = JSON.parse(readFileSync(f, "utf8"));
    }
  }
  return out;
}

// Read once, while the file is collected: tests that each read every language's files again inside their own timeout
// went past it under the full parallel suite (#4955).
const CATALOGS = catalogs();

const placeholders = (s: string) => [...new Set([...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]))].sort();

// The runtime considers suffixes only when count may be numeric. A key that
// ends in "_other" can also be the exact name of a string.
function callTexts(cat: Catalog, key: string, vars?: string[]): string[] {
  const names = vars === undefined || vars.includes("count")
    ? [key, ...PLURAL_FORMS.map((form) => `${key}_${form}`)]
    : [key];
  return names.filter((name) => Object.hasOwn(cat, name)).map((name) => cat[name]);
}

describe("literal calls", () => {
  const sample = {
    sign_in_other: "Sign in to {provider}.",
    saved_one: "{count} item",
    saved_other: "{count} items in {group}",
  };

  it("keeps the exact name and placeholders of a singular key ending in _other", () => {
    expect(callTexts(sample, "sign_in_other", ["provider"])).toEqual(["Sign in to {provider}."]);
  });

  it("checks the possible plural forms when count can be provided", () => {
    expect(callTexts(sample, "saved", ["count", "group"])).toEqual(["{count} item", "{count} items in {group}"]);
    expect(callTexts(sample, "saved")).toEqual(["{count} item", "{count} items in {group}"]);
    expect(callTexts(sample, "saved", [])).toEqual([]);
  });
});

/** key without its plural suffix → the placeholders of all its forms */
function shape(cat: Catalog): Map<string, string[]> {
  const m = new Map<string, Set<string>>();
  for (const [k, v] of Object.entries(cat)) {
    const base = k.replace(PLURAL, "");
    const set = m.get(base) ?? new Set<string>();
    for (const p of placeholders(v)) set.add(p);
    m.set(base, set);
  }
  return new Map([...m].map(([k, v]) => [k, [...v].sort()]));
}

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (p === __dirname) continue; // the catalogs themselves
      out.push(...sourceFiles(p));
    } else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith(".d.ts") && !/^mock[\w-]*\.ts$/.test(name)) {
      out.push(p);
    }
  }
  return out.sort();
}

interface Scan {
  hardcoded: { line: number; text: string }[];
  /** `vars`: the names a literal second argument gives, [] for none, undefined when the code can't tell (a spread, a computed name, a variable) */
  keys: { line: number; key: string; vars?: string[] }[];
  /** every string the code spells out in full, a key or not */
  literals: string[];
  /** the start of a key the code completes at run time: `ns.prefix_${x}`, or a literal given to a local wrapper that appends to it */
  prefixes: string[];
}

/** The property names of t()/tRich()'s second argument, or undefined when they aren't all written out in the call. */
function varNames(arg: ts.Expression | undefined): string[] | undefined {
  if (!arg) return [];
  if (!ts.isObjectLiteralExpression(arg)) return undefined;
  const names: string[] = [];
  for (const p of arg.properties) {
    if (ts.isShorthandPropertyAssignment(p)) names.push(p.name.text);
    else if ((ts.isPropertyAssignment(p) || ts.isMethodDeclaration(p)) && (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name) || ts.isNumericLiteral(p.name))) names.push(p.name.text);
    else return undefined;
  }
  return names;
}

/** The strings an expression can be when it is written out: a literal, or either side of `cond ? "a" : "b"`. */
function literalsOf(e: ts.Expression | undefined): string[] {
  if (!e) return [];
  if (ts.isStringLiteral(e) || ts.isNoSubstitutionTemplateLiteral(e)) return [e.text];
  if (ts.isParenthesizedExpression(e)) return literalsOf(e.expression);
  if (ts.isConditionalExpression(e)) return [...literalsOf(e.whenTrue), ...literalsOf(e.whenFalse)];
  return [];
}

/**
 * Local functions that build a key from a parameter and a suffix, like `say = (key) => t(`${key}_${self ? "self" : "named"}`)`:
 * name → which parameter and the text right after it.
 */
function suffixWrappers(sf: ts.SourceFile): Map<string, { index: number; suffix: string }> {
  const out = new Map<string, { index: number; suffix: string }>();
  const check = (name: string, fn: ts.SignatureDeclaration) => {
    fn.parameters.forEach((p, index) => {
      if (!ts.isIdentifier(p.name)) return;
      const param = p.name.text;
      const find = (n: ts.Node): void => {
        if (ts.isTemplateExpression(n) && !n.head.text) {
          const [first] = n.templateSpans;
          if (ts.isIdentifier(first.expression) && first.expression.text === param && first.literal.text) out.set(name, { index, suffix: first.literal.text });
        }
        ts.forEachChild(n, find);
      };
      find(fn);
    });
  };
  const visit = (n: ts.Node) => {
    if (ts.isFunctionDeclaration(n) && n.name) check(n.name.text, n);
    if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && n.initializer && (ts.isArrowFunction(n.initializer) || ts.isFunctionExpression(n.initializer))) check(n.name.text, n.initializer);
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

function scan(file: string): Scan {
  const text = readFileSync(file, "utf8");
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const lines = text.split("\n");
  const ignored = (line: number) => /i18n-ignore/.test(lines[line] ?? "") || /^\s*\/\/.*i18n-ignore/.test(lines[line - 1] ?? "");
  const out: Scan = { hardcoded: [], keys: [], literals: [], prefixes: [] };
  // t / tRich are ours only where the file imports them from src/i18n (some files have a local t)
  const ours = new Set<string>();
  for (const m of text.matchAll(/import\s*\{([^}]*)\}\s*from\s*["'][./]*i18n(?:\/rich)?["']/g)) {
    for (const name of m[1].split(",").map((x) => x.trim())) if (name === "t" || name === "tRich") ours.add(name);
  }
  const lineOf = (n: ts.Node) => sf.getLineAndCharacterOfPosition(n.getStart(sf)).line;
  const wrappers = suffixWrappers(sf);
  const visit = (n: ts.Node) => {
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) out.literals.push(n.text);
    if (ts.isTemplateExpression(n) && n.head.text) out.prefixes.push(n.head.text);
    if (ts.isCallExpression(n) && ts.isIdentifier(n.expression) && wrappers.has(n.expression.text)) {
      const w = wrappers.get(n.expression.text)!;
      for (const lit of literalsOf(n.arguments[w.index])) out.prefixes.push(lit + w.suffix);
    }
    let literal: string | undefined;
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n)) literal = n.text;
    else if (ts.isJsxText(n)) literal = n.text;
    if (literal !== undefined && CJK.test(literal)) {
      const line = lineOf(n);
      if (!ignored(line)) out.hardcoded.push({ line: line + 1, text: literal.trim().slice(0, 40) });
    }
    if (ts.isCallExpression(n) && ts.isIdentifier(n.expression) && ours.has(n.expression.text)) {
      const a = n.arguments[0];
      if (a && (ts.isStringLiteral(a) || ts.isNoSubstitutionTemplateLiteral(a))) out.keys.push({ line: lineOf(n) + 1, key: a.text, vars: varNames(n.arguments[1]) });
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

describe("languages (resource-only additions)", () => {
  it("are the directories under locales, each complete", () => {
    const dirs = readdirSync(LOCALES).filter((d) => statSync(join(LOCALES, d)).isDirectory()).sort();
    expect([...LANGS].sort()).toEqual(dirs);
    expect(LANGS).toContain(SOURCE_LANG);
    expect(LANGS).toContain(FALLBACK_LANG);
    const claimed = new Map<string, string>();
    for (const lang of dirs) {
      const files = readdirSync(join(LOCALES, lang));
      for (const f of ["_meta.json"]) expect(files, `${lang}/${f}`).toContain(f);
      const meta = JSON.parse(readFileSync(join(LOCALES, lang, "_meta.json"), "utf8")) as { name?: unknown; matches?: unknown };
      expect(typeof meta.name === "string" && meta.name.trim() !== "", `${lang}/_meta.json name`).toBe(true);
      expect(Array.isArray(meta.matches) && meta.matches.length > 0, `${lang}/_meta.json matches`).toBe(true);
      expect(LANG_NAMES[lang]).toBe(meta.name);
      for (const m of meta.matches as string[]) {
        expect(m, `${lang}/_meta.json matches`).toMatch(/^[a-z]{2,3}$/);
        expect(claimed.get(m), `${m} is matched by both ${claimed.get(m)} and ${lang}`).toBeUndefined();
        claimed.set(m, lang);
      }
      expect(meta.matches, `${lang}/_meta.json matches its own language subtag`).toContain(lang.split("-")[0].toLowerCase());
    }
  });
});

describe("catalogs", () => {
  const all = CATALOGS;
  const source = all[SOURCE_LANG];

  it("has the same namespaces in every language", () => {
    for (const lang of LANGS) expect(Object.keys(all[lang]), lang).toEqual(Object.keys(source));
  });

  it("has the same keys and placeholders in every language", () => {
    for (const ns of Object.keys(source)) {
      const want = shape(source[ns]);
      for (const lang of LANGS) {
        const got = shape(all[lang][ns] ?? {});
        expect([...got.keys()].sort(), `${lang}/${ns}.json keys`).toEqual([...want.keys()].sort());
        for (const [k, ps] of want) expect(got.get(k), `${lang}/${ns}.json "${k}" placeholders`).toEqual(ps);
      }
    }
  });

  it("keeps workspace template tokens literal in field hints across languages", () => {
    for (const lang of LANGS) {
      const hints = all[lang]["workspace-naming"];
      const tokens = (key: string) => hints[key].match(/\{[\w-]+\}/g)?.sort();
      expect(tokens("help_base_directory")).toEqual(["{home}"]);
      expect(tokens("help_task_directory_template")).toEqual(["{mmdd}", "{task-id}", "{task-slug}", "{yyyy}"]);
      expect(tokens("help_branch_template")).toEqual(["{base-branch}", "{repo}"]);
    }
  });

  it("gives every plural an _other form and no empty text", () => {
    for (const lang of LANGS) {
      for (const [ns, cat] of Object.entries(all[lang])) {
        for (const [k, v] of Object.entries(cat)) {
          expect(v.trim(), `${lang}/${ns}.json "${k}" is empty`).not.toBe("");
          if (PLURAL.test(k)) expect(cat[k.replace(PLURAL, "_other")], `${lang}/${ns}.json "${k}" has no _other`).toBeDefined();
        }
      }
    }
  });

  it("keeps Persona untranslated", () => {
    for (const [ns, cat] of Object.entries(source)) {
      for (const [k, v] of Object.entries(cat)) {
        if (!v.includes("Persona")) continue;
        for (const lang of LANGS) expect(all[lang][ns][k], `${lang}/${ns}.json "${k}"`).toContain("Persona");
      }
    }
  });

  it("explains the purpose of the Chinese Lark display settings title", () => {
    expect(source["settings-larkdisplay"].title).toContain("消息显示");
  });

  // A file in this repository means nothing to someone using the app and is nowhere they can open it (#3597): say what
  // to do or link to the page. The ones still left may only go away.
  it("names no file of the repository's docs", () => {
    const LEFT = new Set(["admin-pages-usage.url_hint_generic", "settings-connectors.lark_footer_unconfigured"]);
    const found: string[] = [];
    for (const [ns, cat] of Object.entries(source)) {
      for (const k of Object.keys(cat)) {
        if (LANGS.some((lang) => /\bdocs\//.test(all[lang][ns]?.[k] ?? ""))) found.push(`${ns}.${k}`);
      }
    }
    expect(found.sort()).toEqual([...LEFT].sort());
  });
});

describe("client and admin catalogs", () => {
  const ADMIN = /^admin-/;

  it("leaves the admin UI's namespaces out of the client's and puts each namespace in exactly one", () => {
    const all = CATALOGS;
    for (const lang of LANGS) {
      const admin = admins[lang];
      const client = clients[lang];
      expect(Object.keys(client).filter((k) => ADMIN.test(k)), `${lang} client`).toEqual([]);
      expect(Object.keys(admin).filter((k) => !ADMIN.test(k)), `${lang} admin`).toEqual([]);
      const files = Object.entries(all[lang]).flatMap(([ns, cat]) => Object.keys(cat).map((k) => `${ns}.${k}`));
      expect([...Object.keys(client), ...Object.keys(admin)].sort(), lang).toEqual(files.sort());
    }
  });
});

describe("code", () => {
  // The independent viewer is a real resource consumer too, but never imports the authenticated translator.
  const files = [...sourceFiles(SRC), ...sourceFiles(join(SRC, "..", "public-share"))];
  const scans = new Map(files.map((f) => [relative(SRC, f), scan(f)]));
  const sourceCatalog = CATALOGS[SOURCE_LANG];
  const texts = ({ key, vars }: Scan["keys"][number]) => {
    const dot = key.indexOf(".");
    const cat = sourceCatalog[key.slice(0, dot)];
    return cat ? callTexts(cat, key.slice(dot + 1), vars) : [];
  };

  it("uses admin-* keys only in the admin UI, whose page is the only one that loads them", () => {
    const outside = [...scans]
      .filter(([f]) => !f.startsWith("admin/"))
      .flatMap(([f, s]) => s.keys.filter((k) => /^admin-/.test(k.key)).map((k) => `${f}:${k.line} ${k.key}`));
    expect(outside).toEqual([]);
  });

  it("only uses keys that exist", () => {
    const missing = [...scans].flatMap(([f, s]) => s.keys.filter((call) => !texts(call).length).map((k) => `${f}:${k.line} ${k.key}`));
    expect(missing).toEqual([]);
  });

  // A key is used when the code spells it out (or its plural base), or builds it from a prefix the code spells out.
  // Anything else is text no one sees that still has to be translated and kept in step.
  it("uses every key", () => {
    const literals = new Set([...scans.values()].flatMap((s) => s.literals));
    const prefixes = [...new Set([...scans.values()].flatMap((s) => s.prefixes))].filter((p) => p.includes("."));
    const unused: string[] = [];
    for (const [ns, cat] of Object.entries(CATALOGS[SOURCE_LANG])) {
      for (const k of Object.keys(cat)) {
        const key = `${ns}.${k}`;
        if (literals.has(key) || literals.has(`${ns}.${k.replace(PLURAL, "")}`) || prefixes.some((p) => key.startsWith(p))) continue;
        unused.push(key);
      }
    }
    expect(unused, "delete these from every language under src/i18n/locales").toEqual([]);
  });

  // interpolate() and tRich() leave a placeholder with no value as a literal `{name}` on the page
  it("passes every placeholder a key's text uses", () => {
    const missing = [...scans].flatMap(([f, s]) =>
      s.keys.flatMap((call) => {
        const { line, key, vars } = call;
        if (!vars) return [];
        const lacks = [...new Set(texts(call).flatMap(placeholders))].filter((p) => !vars.includes(p));
        return lacks.length ? [`${f}:${line} ${key} lacks ${lacks.join(", ")}`] : [];
      }),
    );
    expect(missing).toEqual([]);
  });

  it("writes no more interface text in Chinese than before", () => {
    const counts: Record<string, number> = {};
    for (const [f, s] of scans) if (s.hardcoded.length) counts[f] = s.hardcoded.length;
    const baseline: Record<string, number> = JSON.parse(readFileSync(BASELINE, "utf8"));
    if (process.env.UPDATE_I18N_BASELINE) {
      // Only lower: a file above its record (or new) keeps what the record allows, and still fails below.
      const lowered = Object.fromEntries(
        Object.entries(baseline)
          .map(([f, n]) => [f, Math.min(n, counts[f] ?? 0)] as const)
          .filter(([, n]) => n > 0),
      );
      writeFileSync(BASELINE, JSON.stringify(lowered, null, 2) + "\n");
    }
    const grew = Object.entries(counts)
      .filter(([f, n]) => n > (baseline[f] ?? 0))
      .map(([f, n]) => `${f}: ${n} (baseline ${baseline[f] ?? 0})\n` + scans.get(f)!.hardcoded.map((h) => `  ${f}:${h.line} ${h.text}`).join("\n"));
    expect(grew, "move these into src/i18n/locales (or mark data with i18n-ignore)").toEqual([]);
    const stale = Object.entries(baseline).filter(([f, n]) => (counts[f] ?? 0) < n).map(([f]) => f);
    if (stale.length && !process.env.UPDATE_I18N_BASELINE)
      console.info(
        `fewer hard-coded strings now in ${stale.join(", ")}; lowering the baseline is tidying anyone can do: ` +
          "python3 scripts/ci/lower_baselines.py, or UPDATE_I18N_BASELINE=1 npx vitest run src/i18n/catalog.test.ts",
      );
  });
});
