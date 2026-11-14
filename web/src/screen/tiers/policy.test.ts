// The browser's tier policy against spec/fixtures/screen-tiers/policy, which PersonaScreenKit's TierPolicy writes
// (TierFixtureTests): the same start, then the same change (or none) at every step of every timeline.
import { describe, expect, it } from "vitest";
import { expectClose, scenarios } from "./fixtures";
import type { Bias } from "./ladder";
import { TierPolicy, type Change, type HelperTiming, type Link, type PolicyInit } from "./policy";
import type { TierSignals } from "./signals";

type ChangeCase = { index: number; scale: number; fps: number; width: number; height: number; minKbps: number; maxKbps: number; targetKbps: number; reason: string } | null;
type Step =
  | { helper: HelperTiming }
  | { tick: { signals: TierSignals; now: number }; expected: ChangeCase }
  | { path: { to: Link; now: number }; expected: ChangeCase }
  | { setBias: { bias: Bias; now: number }; expected: ChangeCase }
  | { setBudget: { capKbps: number; maxFps: number; maxScale: number | null; now: number }; expected: ChangeCase };
type Case = { name: string; init: PolicyInit; start: ChangeCase; steps: Step[] };
const cases = scenarios<Case>("policy");

const asCase = (c: Change | null): ChangeCase =>
  c && { index: c.index, scale: c.tier.scale, fps: c.tier.fps, width: c.tier.width, height: c.tier.height, minKbps: c.tier.minKbps, maxKbps: c.tier.maxKbps, targetKbps: c.targetKbps, reason: c.reason };

describe("the shared tier policy timelines", () => {
  it("has the scenarios", () => expect(cases.length).toBeGreaterThanOrEqual(20));
  for (const [file, c] of cases) {
    it(file, () => {
      expect(c.name).toBe(file);
      const p = new TierPolicy(c.init);
      expectClose(asCase(p.last), c.start, `${file} start`);
      c.steps.forEach((step, i) => {
        const at = `${file} step ${i}`;
        if ("helper" in step) p.helper(step.helper);
        else if ("tick" in step) expectClose(asCase(p.tick(step.tick.signals, step.tick.now)), step.expected, at);
        else if ("path" in step) expectClose(asCase(p.path(step.path.to, step.path.now)), step.expected, at);
        else if ("setBias" in step) expectClose(asCase(p.setBias(step.setBias.bias, step.setBias.now)), step.expected, at);
        else expectClose(asCase(p.setBudget(step.setBudget.capKbps, step.setBudget.maxFps, step.setBudget.maxScale, step.setBudget.now)), step.expected, at);
      });
    });
  }
});
