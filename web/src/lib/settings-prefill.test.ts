import { describe, expect, it } from "vitest";
import { decodeSettingsPrefill, SETTINGS_PREFILL_MOST, settingsPrefill } from "./settings-prefill";

describe("an extension settings prefill", () => {
  it("takes one bounded JSON object from the set query", () => {
    expect(settingsPrefill("?tab=extensions&extension=ats&set=%7B%22machines%22%3A%5B%22studio%22%5D%7D"))
      .toEqual({ machines: ["studio"] });
    expect(settingsPrefill("?tab=extensions&extension=ats")).toBeUndefined();
    expect(settingsPrefill("?set=%5B%5D")).toBeUndefined();
    expect(settingsPrefill("?set=null")).toBeUndefined();
    expect(settingsPrefill("?set=%7B&set=%7B%7D")).toBeUndefined();
    expect(decodeSettingsPrefill("x".repeat(SETTINGS_PREFILL_MOST + 1))).toBeUndefined();
  });
});
