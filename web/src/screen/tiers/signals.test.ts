// The browser's receiver signals against spec/fixtures/screen-tiers/signals, which PersonaScreenKit's ReceiverSignals
// writes (TierFixtureTests): the same frames, decodes and shows give the same TierSignals at every take.
import { describe, expect, it } from "vitest";
import { expectClose, scenarios } from "./fixtures";
import { ReceiverSignals } from "./signals";

type Step =
  | { frame: { capture: number; arrival: number; firstByte: number | null; bytes: number; refinement: boolean; fps: number } }
  | { decoded: { ms: number; at: number } }
  | { shown: { replaced: boolean; at: number } }
  | { pathChanged: Record<string, never> }
  | { take: { now: number }; expected: Record<string, unknown> };
type Case = { name: string; steps: Step[] };
const cases = scenarios<Case>("signals");

describe("the shared receiver signals", () => {
  it("has the scenarios", () => expect(cases.length).toBeGreaterThanOrEqual(10));
  for (const [file, c] of cases) {
    it(file, () => {
      expect(c.name).toBe(file);
      const r = new ReceiverSignals();
      let takes = 0;
      for (const step of c.steps) {
        if ("frame" in step) {
          const f = step.frame;
          r.frame(f.capture, f.arrival, f.firstByte, f.bytes, f.refinement, f.fps);
        } else if ("decoded" in step) r.decoded(step.decoded.ms, step.decoded.at);
        else if ("shown" in step) r.shown(step.shown.replaced, step.shown.at);
        else if ("pathChanged" in step) r.pathChanged();
        else {
          takes++;
          expectClose(r.take(step.take.now), step.expected, `${file} take ${takes}`);
        }
      }
      expect(takes).toBeGreaterThan(0);
    });
  }
});
