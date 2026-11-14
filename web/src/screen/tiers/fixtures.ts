// Reading spec/fixtures/screen-tiers in the tests (#6558): every file of one kind, and the 1e-6 comparison the format
// agrees on.
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect } from "vitest";

const root = fileURLToPath(new URL("../../../../spec/fixtures/screen-tiers/", import.meta.url));

/** Every scenario of a kind (`ladder`, `signals`, `policy`), by file name. */
export function scenarios<T>(kind: string): [string, T][] {
  return readdirSync(root + kind)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((f) => [f.replace(/\.json$/, ""), JSON.parse(readFileSync(root + kind + "/" + f, "utf8")) as T]);
}

/** `got` equals `want` with numbers within 1e-6, nulls and booleans exactly. */
export function expectClose(got: unknown, want: unknown, path = "$"): void {
  if (typeof want === "number" && typeof got === "number") {
    expect(Math.abs(got - want), `${path}: ${got} vs ${want}`).toBeLessThanOrEqual(1e-6);
  } else if (Array.isArray(want)) {
    expect(Array.isArray(got), path).toBe(true);
    expect((got as unknown[]).length, `${path}.length`).toBe(want.length);
    want.forEach((w, i) => expectClose((got as unknown[])[i], w, `${path}[${i}]`));
  } else if (want && typeof want === "object") {
    expect(got && typeof got === "object", path).toBe(true);
    expect(Object.keys(got as object).sort(), `${path} keys`).toEqual(Object.keys(want).sort());
    for (const [k, w] of Object.entries(want)) expectClose((got as Record<string, unknown>)[k], w, `${path}.${k}`);
  } else {
    expect(got, path).toBe(want);
  }
}
