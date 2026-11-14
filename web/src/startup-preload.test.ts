// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { brotliCompressSync, brotliDecompressSync, constants, gzipSync } from "node:zlib";
import { LANG_MATCHES, LANGS, preferredLanguage } from "./i18n";
import {
  brotliCopies, BROTLI_MIN_SIZE, EXPORT_NAME_PLACEHOLDER, FIRST_SCREEN_LOW_ROOM, firstScreenBytes, firstScreenOverBudget, firstScreenRoom, gzipBytes,
  HASH_PLACEHOLDER, idleImports, localeDirs, nameHashes, placeholderExportNames, placeholderHashes, preloadScript, startupGaps,
} from "./startup-preload";

const langs = localeDirs(join(__dirname, "i18n/locales"));

const files = {
  shared: ["assets/react.js"],
  app: ["assets/App.js", "assets/App.css"],
  screen: ["assets/ScreenView.js"],
  catalogs: Object.fromEntries(Object.keys(langs).map((lang) => [lang, `assets/${lang}.js`])),
  matches: Object.fromEntries(Object.entries(langs).flatMap(([lang, ms]) => ms.map((m) => [m.toLowerCase(), lang]))),
};

// Runs the inline script on a <head> holding only the page's <base> (index.html always has one, set by the hub) and
// returns the hrefs it added, in order.
function run(base = "/"): string[] {
  document.head.innerHTML = `<base href="${base}" />`;
  new Function(preloadScript(files))();
  return [...document.head.querySelectorAll("link")].map((l) => l.getAttribute("href")!.replace(/^\.\//, ""));
}

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
  document.head.innerHTML = "";
});

describe("startup preload script", () => {
  it("has a catalog for every client language", () => {
    expect(Object.keys(langs).sort()).toEqual([...LANGS].sort());
    expect(files.matches).toEqual(LANG_MATCHES);
  });

  it.each([
    [null, ["zh-TW"]],
    [null, ["ja_JP"]],
    [null, ["fr-FR", "zh-CN"]],
    [null, []],
    ["ja", ["en-US"]],
    ["en", ["zh-CN"]],
    ["klingon", ["ja"]],
  ])("picks the catalog preferredLanguage picks (stored %s, system %j)", (stored, languages) => {
    if (stored) localStorage.setItem("persona.language", stored);
    vi.spyOn(navigator, "languages", "get").mockReturnValue(languages);
    vi.spyOn(navigator, "language", "get").mockReturnValue(languages[0] ?? "");
    expect(run()[0]).toBe(files.catalogs[preferredLanguage()]);
  });

  it("preloads App on client pages and only the screen view on the live screen", () => {
    expect(run()).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.app]);
    window.history.pushState(null, "", "/screen/mac");
    try {
      expect(run()).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.screen]);
      window.history.pushState(null, "", "/persona/screen/mac");
      expect(run("/persona/")).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.screen]);
      window.history.pushState(null, "", "/persona/");
      expect(run("/persona/")).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.app]);
    } finally {
      window.history.pushState(null, "", "/");
    }
  });
});

describe("brotli copies", () => {
  it("writes name.br beside each file of 1 KB or more, decoding to it, the same bytes every time", async () => {
    const dir = mkdtempSync(join(tmpdir(), "startup-br-"));
    mkdirSync(join(dir, "assets"));
    const big = "export const greeting = \"hello\";\n".repeat(100);
    writeFileSync(join(dir, "assets/App-1.js"), big);
    writeFileSync(join(dir, "assets/small-2.js"), "x".repeat(BROTLI_MIN_SIZE - 1));
    const st = await brotliCopies(dir, ["assets/App-1.js", "assets/small-2.js"]);
    const br = readFileSync(join(dir, "assets/App-1.js.br"));
    expect(st).toEqual({ files: 1, before: big.length, after: br.length });
    expect(brotliDecompressSync(br).toString()).toBe(big);
    expect(existsSync(join(dir, "assets/small-2.js.br"))).toBe(false);
    // what the hub sends for them, as the first-screen budget counts it when they name no other file. Local
    // bindings are normalized for this measurement, so the count may be smaller than the original file's brotli copy.
    const [[, counted]] = await firstScreenBytes(dir, [["en", ["assets/App-1.js", "assets/small-2.js"]]], []);
    expect(counted).toBeLessThanOrEqual(br.length + BROTLI_MIN_SIZE - 1);
    expect(counted).toBeGreaterThan(BROTLI_MIN_SIZE - 1);
    await brotliCopies(dir, ["assets/App-1.js"]);
    expect(readFileSync(join(dir, "assets/App-1.js.br")).equals(br)).toBe(true);
    rmSync(dir, { recursive: true });
  });
});

// A small build as Vite writes one: main imports App, App imports api and preloads and imports a lazy chunk, each file
// named [name]-[hash].js with a hash of its contents (Rollup's is of the contents too, so a change to the lazy chunk
// renames it, then App, then main). Returns the directory, the first screen's files and every file's name.
function writeBuild(dir: string, lazy: string, extra = "") {
  const hash = (code: string) => createHash("sha256").update(code).digest("base64url").slice(0, 8);
  const write = (name: string, code: string) => {
    const file = `assets/${name}-${hash(code)}.js`;
    writeFileSync(join(dir, file), code);
    return file;
  };
  rmSync(join(dir, "assets"), { recursive: true, force: true });
  mkdirSync(join(dir, "assets"));
  const ref = (file: string) => file.slice("assets/".length);
  const lazyFile = write("Lazy", lazy);
  const api = write("api", `export function get(path) { return fetch("/api/v1/" + path).then((r) => r.json()); }\n`.repeat(40));
  const body = Array.from({ length: 60 }, (_, i) => `function view${i}(props) { return h("div", { className: "row-${i}" }, props.title, get("item/${i}")); }\n`).join("");
  const app = write("App", [
    `import { get } from "./${ref(api)}";`,
    `const __vite__mapDeps = (i, m = __vite__mapDeps, d = (m.f || (m.f = ["assets/${ref(lazyFile)}"]))) => i.map((i) => d[i]);`,
    body,
    `export const openLazy = () => __vitePreload(() => import("./${ref(lazyFile)}"), __vite__mapDeps([0]));`,
    extra,
  ].join("\n"));
  const main = write("main", `import "./${ref(app)}";\nimport "./${ref(api)}";\n${"// the page's start\n".repeat(80)}`);
  return { first: [main, app, api], names: [main, app, api, lazyFile, "sw.js", "index.html"] };
}

// The brotli copy the build writes (brotliCopies).
const brotliSize = (data: Buffer) =>
  brotliCompressSync(data, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: data.length } }).length;

// A minified build as Rollup writes one: App defines 120 functions and exports those other chunks import, each under
// an alias given in App's own order (a, b, …, z, A, …, Z, then two letters), its export list sorted by alias; main (the
// first screen) imports 70 of them, and a lazy chunk imports `lazyUses` (indexes into App's functions). Returns the
// directory, the first screen's files and every file's name.
function writeMinifiedBuild(dir: string, lazyUses: number[], extra = "") {
  const hash = (code: string) => createHash("sha256").update(code).digest("base64url").slice(0, 8);
  const write = (name: string, code: string) => {
    const file = `assets/${name}-${hash(code)}.js`;
    writeFileSync(join(dir, file), code);
    return file;
  };
  rmSync(join(dir, "assets"), { recursive: true, force: true });
  mkdirSync(join(dir, "assets"));
  const ref = (file: string) => file.slice("assets/".length);
  const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
  const alias = (i: number) => (i < letters.length ? letters[i] : letters[Math.floor(i / letters.length) - 1] + letters[i % letters.length]);
  const local = (i: number) => `F${i}x`;
  const mainUses = Array.from({ length: 70 }, (_, i) => 10 + i);
  const exported = [...new Set([...mainUses, ...lazyUses])].sort((a, b) => a - b);
  const aliasOf = new Map(exported.map((f, i) => [f, alias(i)]));
  const body = Array.from({ length: 120 }, (_, i) => `function ${local(i)}(e){return h("div",{className:"row-${i}"},e.title,${local((i + 7) % 120)})}`).join(";");
  const list = exported.map((f) => [aliasOf.get(f)!, `${local(f)} as ${aliasOf.get(f)}`]).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([, s]) => s);
  const app = write("App", `${body};${extra}export{${list.join(",")}};`);
  const imports = (uses: number[], prefix: string) => uses.map((f, i) => `${aliasOf.get(f)} as ${prefix}${i}`).join(",");
  const lazyFile = write("Lazy", `import{${imports(lazyUses, "l")}}from"./${ref(app)}";export const page=()=>[${lazyUses.map((_, i) => `l${i}`).join(",")}];`);
  const main = write("main", `import{${imports(mainUses, "m")}}from"./${ref(app)}";render([${mainUses.map((_, i) => `m${i}`).join(",")}]);${"/* the page's start */".repeat(60)}`);
  return { first: [main, app], names: [main, app, lazyFile, "index.html"] };
}

// 1 KB of code that does not compress, as brotli sends it: 1,024 bytes of a hash chain, written as base64.
function incompressibleKB() {
  let seed = Buffer.from("first screen");
  return Buffer.concat(Array.from({ length: 32 }, () => (seed = createHash("sha256").update(seed).digest()))).toString("base64");
}

describe("first-screen budget", () => {
  it("finds the hash in each name Vite gives a built file", () => {
    expect(nameHashes([
      "assets/api-kRgYO_n8.js",
      "assets/abnfDiagram-S7VZAUFO-C2PG9UkD.js",
      "assets/_meta.json-BBjh60da.js",
      "assets/App-CYYgV9zH.css",
      "admin/assets/admin-a-bcdefg.js",
      "assets/KaTeX_Main-Regular-B22Nviop.woff2",
      "sw.js",
      "index.html",
      "admin/index.html",
    ])).toEqual(new Set(["kRgYO_n8", "C2PG9UkD", "BBjh60da", "CYYgV9zH", "a-bcdefg", "B22Nviop"]));
  });

  it("counts the build's hashes in a file's code as one fixed string, and leaves the rest alone", () => {
    const hashes = nameHashes(["assets/api-kRgYO_n8.js", "assets/App-CYYgV9zH.css", "assets/abnfDiagram-S7VZAUFO-C2PG9UkD.js", "admin/assets/admin-a-bcdefg.js"]);
    const code = 'import{g}from"./api-kRgYO_n8.js";m.f=["assets/App-CYYgV9zH.css","assets/abnfDiagram-S7VZAUFO-C2PG9UkD.js","admin/assets/admin-a-bcdefg.js"];n=total-firstRow.length;"Lazy-AbCd1234.js"';
    const out = placeholderHashes(code, hashes);
    expect(out).toBe(code.replace("kRgYO_n8", HASH_PLACEHOLDER).replace("CYYgV9zH", HASH_PLACEHOLDER).replace("C2PG9UkD", HASH_PLACEHOLDER).replace("a-bcdefg", HASH_PLACEHOLDER));
    expect(out).toHaveLength(code.length);
  });

  it("counts the names a file imports from another of the build's files as one fixed string, and its export lists as empty ones", () => {
    const hashes = nameHashes(["assets/App-CYYgV9zH.js", "assets/api-kRgYO_n8.js"]);
    const code = [
      'import{eu as a,j,$1 as $$}from"./App-CYYgV9zH.js";',
      'import { get } from "./api-kRgYO_n8.js";',
      'import{x as y}from"./vendor.js";', // not a file of this build
      'import{a as b c}from"./App-CYYgV9zH.js";', // not a list of names
      'import"./api-kRgYO_n8.js";f(a,j,$$,get,y);',
      'export{q as r}from"./api-kRgYO_n8.js";',
      "export{Yme as eu,Xme as ev,j};",
    ].join("");
    const _ = EXPORT_NAME_PLACEHOLDER;
    expect(placeholderExportNames(code, hashes)).toBe([
      `import{${_} as a,${_} as j,${_} as $$}from"./App-CYYgV9zH.js";`,
      `import { ${_} as get } from "./api-kRgYO_n8.js";`,
      'import{x as y}from"./vendor.js";',
      'import{a as b c}from"./App-CYYgV9zH.js";',
      'import"./api-kRgYO_n8.js";f(a,j,$$,get,y);',
      'export{}from"./api-kRgYO_n8.js";',
      "export{};",
    ].join(""));
  });

  it("keeps the same count when the minifier renames a chunk's local bindings", async () => {
    const { placeholderLocalNames } = await import("./startup-preload");
    const before = 'const firstName="x";function doThing(longParameter){const result=longParameter+firstName;return result}doThing("y");';
    const after = 'const z9="x";function q(a){const b=a+z9;return b}q("y");';
    expect(placeholderLocalNames(before)).toBe(placeholderLocalNames(after));
  });

  it("leaves strings, property names, method names, labels and globals alone", async () => {
    const { placeholderLocalNames } = await import("./startup-preload");
    const code = 'const value={longKey:"firstName",method(){return this.longKey}};outer:for(const item of [value]){if(item)break outer;console.log(Math.max(item.longKey,1)}';
    const out = placeholderLocalNames(code);
    expect(out).toContain('"firstName"');
    expect(out).toContain('longKey:');
    expect(out).toContain('method(){');
    expect(out).toContain('this.longKey');
    expect(out).toContain('outer:');
    expect(out).toContain('break outer');
    expect(out).toContain('console.log');
    expect(out).toContain('Math.max');
    expect(out).not.toContain('value');
  });

  it("does not move the first-screen count when local names are renamed", async () => {
    const dir = mkdtempSync(join(tmpdir(), "first-screen-locals-"));
    mkdirSync(join(dir, "assets"));
    const file = "assets/App-abcdefgh.js";
    const write = (code: string) => {
      writeFileSync(join(dir, file), code);
      return firstScreenBytes(dir, [["ja", [file]]], [file]);
    };
    const before = await write('const firstName="x";function doThing(longParameter){const result=longParameter+firstName;return result}doThing("y");');
    const after = await write('const z9="x";function q(a){const b=a+z9;return b}q("y");');
    expect(after).toEqual(before);
    rmSync(dir, { recursive: true });
  });

  it("does not move when a lazy chunk makes a first-screen chunk export one more name, though what is sent does", async () => {
    const dir = mkdtempSync(join(tmpdir(), "first-screen-"));
    const measure = async (b: ReturnType<typeof writeMinifiedBuild>) => {
      const sent = b.first.reduce((sum, file) => sum + brotliSize(readFileSync(join(dir, file))), 0);
      const code = b.first.map((file) => readFileSync(join(dir, file), "latin1"));
      const [[, counted]] = await firstScreenBytes(dir, [["ja", b.first]], b.names);
      return { sent, code, counted };
    };
    const before = await measure(writeMinifiedBuild(dir, [100]));
    // the lazy chunk now also imports App's 4th function, which main does not: App exports it first, under "a", and
    // every name main imports moves one alias on (its first, F10x, from "a" to "b"), as #5310's did
    const after = await measure(writeMinifiedBuild(dir, [100, 3]));
    expect(before.code[0]).toContain("import{a as m0,b as m1,");
    expect(after.code[0]).toContain("import{b as m0,c as m1,");
    expect(before.code[1]).toContain("F10x as a,");
    expect(after.code[1]).toContain("F3x as a,");
    expect(after.code[1]).toContain("F10x as b,");
    expect(after.sent).not.toBe(before.sent);
    expect(after.counted).toBe(before.counted);
    rmSync(dir, { recursive: true });
  });

  it("still counts 1 KB more of first-screen code when a lazy chunk renames its exports too", async () => {
    const dir = mkdtempSync(join(tmpdir(), "first-screen-"));
    const measure = (b: ReturnType<typeof writeMinifiedBuild>) => firstScreenBytes(dir, [["en", b.first], ["ja", b.first]], b.names);
    const budget = (await measure(writeMinifiedBuild(dir, [100])))[1][1];
    const after = await measure(writeMinifiedBuild(dir, [100, 3], `const blob="${incompressibleKB()}";`));
    const grown = after[1][1] - budget;
    expect(grown).toBeGreaterThan(1000);
    expect(grown).toBeLessThan(1100);
    expect(firstScreenOverBudget(after, budget)).toContain("the en, ja first screen is over its budget of " + budget + " B");
    rmSync(dir, { recursive: true });
  });

  it("does not move when only a lazy chunk changes, though what is sent does", async () => {
    const dir = mkdtempSync(join(tmpdir(), "first-screen-"));
    const sent = new Set<number>();
    const measured = new Set<number>();
    for (let i = 0; i < 12; i++) {
      const { first, names } = writeBuild(dir, `export const label = "lazy page, build ${i}";\n`);
      sent.add(first.reduce((sum, file) => sum + brotliSize(readFileSync(join(dir, file))), 0));
      const [[, bytes]] = await firstScreenBytes(dir, [["ja", first]], names);
      measured.add(bytes);
    }
    expect(sent.size).toBeGreaterThan(1); // the hashes alone move what is sent
    expect(measured.size).toBe(1);
    rmSync(dir, { recursive: true });
  });

  it("counts real growth of the first screen and fails the budget on it", async () => {
    const dir = mkdtempSync(join(tmpdir(), "first-screen-"));
    const lazy = 'export const label = "lazy page";\n';
    const before = writeBuild(dir, lazy);
    const measure = (b: ReturnType<typeof writeBuild>) => firstScreenBytes(dir, [["en", b.first], ["ja", b.first]], b.names);
    const budget = (await measure(before))[1][1];
    expect(firstScreenOverBudget(await measure(before), budget)).toBeUndefined();
    const after = writeBuild(dir, lazy, `export const blob = "${incompressibleKB()}";`);
    const grown = (await measure(after))[1][1] - budget;
    expect(grown).toBeGreaterThan(1000);
    expect(grown).toBeLessThan(1100);
    const problem = firstScreenOverBudget(await measure(after), budget);
    expect(problem).toContain("the en, ja first screen is over its budget of " + budget + " B");
    expect(problem).toContain(`ja ${budget + grown} B`);
    rmSync(dir, { recursive: true });
  });
});

describe("idle tier", () => {
  it("finds the modules lazyPage loads at idle, not those it loads on intent or when shown", () => {
    const code = [
      'const Palette = lazyPage(() => import("./views/Palette").then((m) => m.Palette), { idle: true });',
      'const Settings = lazyPage(() => import("./views/Settings").then((m) => m.Settings), { routes: ["settings"] });',
      "const Chart = lazyPage(() => import('./Chart').then((m) => m.Chart), {\n  idle: true,\n});",
      'const Diff = lazyPage(() => import("./DiffView").then((m) => m.DiffView));',
      'const idle = { idle: true };',
      'const Inbox = lazyPage(() => import("./views/Inbox").then((m) => m.Inbox), { idle: true, routes: ["inbox"] });',
      'const Kb = lazyPage(() => import("./views/Kb").then((m) => m.Kb), { routes: ["kb"], idle: true });',
      'const Todos = lazyPage(() => import("./views/Todos").then((m) => m.Todos), { idle: false, routes: ["todos"] });',
      "export function lazyPage(load, opts = {}) {}",
    ].join("\n");
    expect(idleImports(code)).toEqual(["./views/Palette", "./Chart", "./views/Inbox", "./views/Kb"]);
  });

  it.each([
    ['lazyPage(() => codeViews().then((m) => m.Header), { idle: true })', "its loader is not"],
    ['lazyPage(() => import("./Header"), opts)', "object literal"],
  ])("refuses an idle page it cannot count: %s", (code, why) => {
    expect(() => idleImports(code)).toThrow(why);
  });

  it("counts each file gzipped at the best level, or as it is under 1 KB", async () => {
    const dir = mkdtempSync(join(tmpdir(), "idle-gz-"));
    mkdirSync(join(dir, "assets"));
    const big = "export const greeting = \"hello\";\n".repeat(100);
    writeFileSync(join(dir, "assets/Palette-1.js"), big);
    writeFileSync(join(dir, "assets/small-2.js"), "x".repeat(BROTLI_MIN_SIZE - 1));
    expect(await gzipBytes(dir, ["assets/Palette-1.js", "assets/small-2.js"])).toBe(gzipSync(big, { level: 9 }).length + BROTLI_MIN_SIZE - 1);
    rmSync(dir, { recursive: true });
  });
});

describe("startup catalog check", () => {
  const keys = ["common.ok", "common.cancel", "page.title", "page.title_one", "page.title_other", "page.step_a", "page.step_b", "menu.open", "menu.close", "settings.avatar", "unread.x"];

  it("names each key the first screen's code reads that the startup list leaves to the rest", () => {
    const code = ['t("common.ok");t("page.title",{count:n});x(`page.step_${n}`);y("settings.json");z("other.key")', "w('menu.open')"];
    expect(startupGaps(code, keys, ["common"])).toEqual(["menu.open", "page.step_", "page.title"]);
    expect(startupGaps(code, keys, ["common", "menu.open", "page.step_", "page.title"])).toEqual([]);
    expect(startupGaps(code, keys, ["common", "menu", "page"])).toEqual([]);
  });

  it("asks for the whole namespace when the code names none of the key, and only for it", () => {
    expect(startupGaps(['t("page."+k)'], keys, ["page.title"])).toEqual(["page"]);
    expect(startupGaps(['t("page."+k);t("page.title");t("page.step_a")'], keys, [])).toEqual(["page"]);
  });

  it("takes a listed key's plural forms and the suffixes a wrapper adds", () => {
    expect(startupGaps(['t("page.title",{count:2});say("page.step")'], keys, ["page.title", "page.step"])).toEqual([]);
    expect(startupGaps(['say("page.step")'], keys, ["page.step_a"])).toEqual(["page.step"]);
  });

  it("passes a string that only looks like a key", () => {
    expect(startupGaps(['y("settings.json");z("unread.")'], keys, ["unread"])).toEqual([]);
  });
});

describe("first-screen room", () => {
  it("says each language's count and the room left in the fullest", () => {
    expect(firstScreenRoom([["en", 1000], ["ja", 1500], ["zh-Hans", 1200]], 4000)).toEqual({
      line: "startup-preload: first screen en 1,000 B, ja 1,500 B, zh-Hans 1,200 B; 2,500 B left under FIRST_SCREEN_BUDGET (ja)",
      low: false,
    });
  });

  it("warns under FIRST_SCREEN_LOW_ROOM", () => {
    const room = firstScreenRoom([["en", 3000], ["ja", 3100]], 3100 + FIRST_SCREEN_LOW_ROOM - 1);
    expect(room.low).toBe(true);
    expect(room.line).toContain(`${(FIRST_SCREEN_LOW_ROOM - 1).toLocaleString("en-US")} B left under FIRST_SCREEN_BUDGET (ja), under FIRST_SCREEN_LOW_ROOM`);
  });
});
