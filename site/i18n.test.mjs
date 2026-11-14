// node --test site/i18n.test.mjs
import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const PAIRS = [
  ["index.html", "en/index.html"],
  ["extensions/index.html", "en/extensions/index.html"],
  ["releases/index.html", "en/releases/index.html"],
  ["privacy/index.html", "en/privacy/index.html"],
  ["terms/index.html", "en/terms/index.html"],
  ["404.html", "en/404.html"],
];
const INSTALL = "curl -fsSL https://github.com/nwverse/persona/releases/latest/download/install.sh | sh -s -- --hub";

async function page(path) {
  return readFile(new URL(`./public/${path}`, import.meta.url), "utf8");
}

function outline(html) {
  return [...html.matchAll(/<(section|h1|h2|h3|details|footer|nav)\b([^>]*)>/g)].map(([, tag, attrs]) => {
    const id = /\bid="([^"]+)"/.exec(attrs)?.[1] || "";
    const label = /\baria-labelledby="([^"]+)"/.exec(attrs)?.[1] || "";
    return `${tag}#${id}[${label}]`;
  });
}

function links(html) {
  return [...html.matchAll(/<a\b([^>]*)>/g)].flatMap(([, attrs]) => {
    if (/\bclass="[^"]*\blang\b/.test(attrs)) return [];
    const href = /\bhref="([^"]+)"/.exec(attrs)?.[1];
    if (!href) return [];
    const url = new URL(href, "https://personaside.com");
    if (url.origin !== "https://personaside.com") return [href];
    url.pathname = url.pathname.replace(/^\/en(?=\/|$)/, "") || "/";
    return [url.pathname + url.search + url.hash];
  });
}

function publicPath(file) {
  if (file === "index.html") return "/";
  if (file.endsWith("/index.html")) return "/" + file.slice(0, -"index.html".length);
  return "/" + file;
}

test("every site page has an English sibling with the same structure and link targets", async () => {
  for (const [zhPath, enPath] of PAIRS) {
    const [zh, en] = await Promise.all([page(zhPath), page(enPath)]);
    assert.match(zh, /<html lang="zh-CN">/, zhPath);
    assert.match(en, /<html lang="en">/, enPath);
    assert.deepEqual(outline(en), outline(zh), `${enPath} has the same sections and headings as ${zhPath}`);
    assert.deepEqual(links(en), links(zh), `${enPath} has the same links as ${zhPath}, with only the /en prefix changed`);
    assert.match(zh, /<link rel="alternate" hreflang="en" /, zhPath);
    assert.match(en, /<link rel="alternate" hreflang="zh-CN" /, enPath);
    const zhURL = "https://personaside.com" + publicPath(zhPath);
    const enURL = "https://personaside.com" + publicPath(enPath);
    for (const [html, self, other, selfLanguage, otherLanguage] of [
      [zh, zhURL, enURL, "zh-CN", "en"], [en, enURL, zhURL, "en", "zh-CN"],
    ]) {
      assert.ok(html.includes(`<link rel="alternate" hreflang="${selfLanguage}" href="${self}">`), self);
      assert.ok(html.includes(`<link rel="alternate" hreflang="${otherLanguage}" href="${other}">`), other);
      assert.ok(html.includes(`<link rel="alternate" hreflang="x-default" href="${zhURL}">`), zhURL);
    }
    assert.match(zh, /<a\b[^>]*class="[^"]*\blang\b[^>]*href="[^"]*[?&]lang=en"[^>]*hreflang="en"/, zhPath);
    assert.match(en, /<a\b[^>]*class="[^"]*\blang\b[^>]*href="[^"]*[?&]lang=zh"[^>]*hreflang="zh-CN"/, enPath);
  }
});

test("English pages have no untranslated Chinese outside the deliberate Chinese switch", async () => {
  for (const [, enPath] of PAIRS) {
    const html = await page(enPath);
    const withoutSwitch = html.replace(/<a\b(?=[^>]*\bclass="[^"]*\blang\b)[^>]*>[\s\S]*?<\/a>/g, "");
    const leftover = /[㐀-鿿]/.exec(withoutSwitch);
    assert.equal(leftover, null, leftover ? `${enPath}: ${withoutSwitch.slice(Math.max(0, leftover.index - 50), leftover.index + 80)}` : enPath);
  }
});

test("English rich pages and social previews use the English README recording", async () => {
  for (const [zhPath, enPath] of PAIRS.slice(0, 3)) {
    assert.match(await page(zhPath), /\/img\/recording\.webp/, zhPath);
    const en = await page(enPath);
    assert.match(en, /\/img\/recording-en\.webp/, enPath);
    assert.doesNotMatch(en, /\/img\/recording\.webp/, enPath);
  }
});

test("privacy update dates are equal in both languages", async () => {
  const [zh, en] = await Promise.all([page("privacy/index.html"), page("en/privacy/index.html")]);
  const zhDate = /更新于 (\d{4}) 年 (\d{1,2}) 月 (\d{1,2}) 日/.exec(zh);
  const enDate = /Updated ([A-Z][a-z]+) (\d{1,2}), (\d{4})/.exec(en);
  assert.ok(zhDate, "Chinese privacy update date");
  assert.ok(enDate, "English privacy update date");
  const months = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
  const fromEnglish = `${enDate[3]}-${String(months.indexOf(enDate[1]) + 1).padStart(2, "0")}-${enDate[2].padStart(2, "0")}`;
  const fromChinese = `${zhDate[1]}-${zhDate[2].padStart(2, "0")}-${zhDate[3].padStart(2, "0")}`;
  assert.equal(fromEnglish, fromChinese);
  assert.equal(fromChinese, "2026-11-13", "the language-cookie disclosure's update date");
});

test("both home pages carry the same install command", async () => {
  for (const path of PAIRS.slice(0, 1)) {
    for (const file of path) {
      const html = await page(file);
      const command = /<span id="cmd-text">([^<]+)<\/span>/.exec(html)?.[1];
      assert.equal(command, INSTALL, file);
    }
  }
});
