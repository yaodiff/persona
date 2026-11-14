// The browser's tier ladder against spec/fixtures/screen-tiers/ladder, which PersonaScreenKit's TierLadder writes
// (TierFixtureTests): the same input, the same tiers and bands, every scenario.
import { describe, expect, it } from "vitest";
import { expectClose, scenarios } from "./fixtures";
import { makeLadder, type LadderInput, type Tier } from "./ladder";

type Case = { name: string; input: LadderInput; expected: Tier[] };
const cases = scenarios<Case>("ladder");

describe("the shared tier ladders", () => {
  it("has the scenarios", () => expect(cases.length).toBeGreaterThanOrEqual(10));
  for (const [file, c] of cases) {
    it(file, () => {
      expect(c.name).toBe(file);
      expectClose(makeLadder(c.input), c.expected);
    });
  }
});
