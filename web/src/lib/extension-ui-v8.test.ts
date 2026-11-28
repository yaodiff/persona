// UI version 8 as the web reads it (docs/extensions/extensions.md "UI version 8", #9158): the columns layout and a
// section's span, icon, icon color, link and columns, from the schema's examples; what it cannot use is left out.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { readView, UI_VERSION } from "./extension-ui";

const example = (name: string) => JSON.parse(readFileSync(new URL(`../../../schema/extension/examples/View.v8-${name}.json`, import.meta.url), "utf8"));

describe("UI 8: reading a view", () => {
  it("is the version the web sends", () => {
    expect(UI_VERSION).toBe(13);
  });

  it("keeps the columns layout and every section field of the examples", () => {
    const v = readView(example("today"));
    expect(v.layout).toBe("columns");
    expect(v.sections[0]).toMatchObject({
      title: "Needs me", span: "full", icon: "bell", icon_color: "orange", columns: 2,
      link: { title: "All", action: { id: "open_needs", push: { state: "needs", title: "Needs me" } } },
    });
    expect(v.sections[1]).toMatchObject({ icon: "calendar", icon_color: "red", link: { title: "Calendar", action: { id: "open_calendar" } } });
    expect(v.sections[1]).not.toHaveProperty("span");
    expect(v.sections[1]).not.toHaveProperty("columns");
    const header = readView(example("section-header"));
    expect(header.layout).toBeUndefined();
    expect(header.sections[0]).toMatchObject({ icon: "chart", link: { title: "History", action: { id: "history", values: { range: "month" } } } });
  });

  it("leaves out what it cannot use", () => {
    const v = readView({ layout: "masonry", sections: [
      { items: [], link: { title: "All" }, columns: 1 },
      { items: [], link: { title: "", action: { id: "a" } }, columns: 9, span: 3, icon: "" },
      { items: [], link: { action: { id: "a" } }, columns: 2.5 },
    ] });
    expect(v.layout).toBeUndefined();
    expect(v.sections).toEqual([{ items: [] }, { items: [], columns: 3 }, { items: [], columns: 2 }]);
  });
});
