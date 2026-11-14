import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

function selectors(html) {
  const styles = [...html.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/g)].map((match) => match[1]).join("\n");
  const css = styles.replace(/\/\*[\s\S]*?\*\//g, "");
  return [...css.matchAll(/([^{}]+)\{/g)].flatMap((match) => match[1].split(",").map((selector) => selector.trim()));
}

for (const page of ["index.html", "en/index.html"]) {
  test(`${page}: demo message styles cannot match a platform download tile`, async () => {
    const html = await readFile(new URL(`./public/${page}`, import.meta.url), "utf8");
    assert.ok(html.includes('add(TASK, \'mine\')'), "the demo still uses the shared class name checked here");
    assert.ok(html.includes('tile.classList.add("mine")'), "the detected platform tile still uses the shared class name checked here");
    assert.ok(!selectors(html).includes(".mine"),
      "a bare .mine rule meant for the demo bubble also applies max-width and align-self to .dl-tile.mine");
  });
}
