// UI version 9 as the web reads it (docs/extensions/extensions.md "UI version 9", #9337): a tile's gauge, from the
// schema's examples; what it cannot use is left out, and the tile is then drawn as one without a gauge.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { readView, UI_VERSION, type UIView } from "./extension-ui";

const example = (name: string) => JSON.parse(readFileSync(new URL(`../../../schema/extension/examples/View.v9-${name}.json`, import.meta.url), "utf8"));
const tiles = (v: UIView) => v.sections.flatMap((s) => s.items).flatMap((it) => it.tiles ?? []);
const gaugeOf = (raw: unknown) => tiles(readView({ sections: [{ items: [{ type: "grid", tiles: [{ title: "t", gauge: raw }] }] }] }))[0].gauge;

describe("UI 9: reading a tile's gauge", () => {
  it("is the version the web sends", () => {
    expect(UI_VERSION).toBe(13);
  });

  it("keeps every gauge of the examples, and no gauge on the other tiles", () => {
    const quota = tiles(readView(example("quota")));
    expect(quota.map((t) => t.gauge)).toEqual([
      undefined, undefined, undefined, undefined,
      { ratio: 0.94, unit: "USD", color: "green" },
      { ratio: 0.76, unit: "USD", color: "green" },
      { ratio: 0.12, unit: "USD", color: "amber" },
    ]);
    // the rest of a gauge's tile reads as any tile's
    expect(quota[6]).toMatchObject({ title: "Fable this week", value: "0.62", badge: { text: "Low", color: "amber" }, action: { id: "open_limit", values: { limit: "fable_week" } } });
    expect(tiles(readView(example("gauge-percent"))).map((t) => t.gauge)).toEqual([{ ratio: 0 }, { ratio: 1 }]);
  });

  it("keeps a gauge with a finite ratio as sent (the drawing holds it to 0–1), and leaves out any other", () => {
    expect(gaugeOf({ ratio: 1.5, unit: "GB", color: "teal" })).toEqual({ ratio: 1.5, unit: "GB", color: "teal" });
    for (const raw of [{ ratio: "0.5" }, { unit: "USD" }, { ratio: Number.NaN }, { ratio: Number.POSITIVE_INFINITY }, 0.5, null, "ring", [0.5]])
      expect(gaugeOf(raw), JSON.stringify(raw)).toBeUndefined();
  });
});
