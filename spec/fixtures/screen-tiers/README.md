# screen-tiers

The live screen's quality tiers (#6396, `docs/extensions/computer-use.md` "Live screen video (#3954)"): what the
iPhone's `PersonaScreenKit` (`TierLadder`, `ReceiverSignals`, `TierPolicy`) and the browser's port (`web/src/screen/tiers/`, #6558)
must both give. One scenario per file: its input and the expected output, never internal state. The format was agreed
between the two lanes that keep the two sides.

## `ladder/`

`TierLadder.make`: `input` is `points` and `pixels` (`[width, height]`), `maxSize` (the long-edge cap; 0: none),
`bias` (`auto` / `sharper` / `smoother`), `capKbps` (0: no cap), `maxFps` and `codec` (`hevc` / `h264`). `expected` is
the whole ladder, best first: `scale`, `fps`, `width`, `height`, `minKbps`, `maxKbps`.

## `signals/`

`ReceiverSignals`: `steps` in order, each one of
- `frame`: `capture`, `arrival` and `firstByte` (µs; `firstByte` null when the frame came in one read), `bytes`,
  `refinement`, `fps`;
- `decoded`: `ms`, `at` (µs); `shown`: `replaced`, `at` (µs); `pathChanged`: `{}`;
- `take`: `now` (µs), with `expected`, every `TierSignals` field (`frames`, `queueP50Ms`, `queueP95Ms`,
  `queueRising`, `queueFalling`, `deliveredKbps`, `burstKbps`, `lateShare`, `decodeP95Ms`, `replacedShare`), null
  where it isn't measured.

## `policy/`

`TierPolicy`: `init` (`display` with `points`, `pixels` and `maxSize`; `bias`, `capKbps`, `maxFps`, `maxScale` (null: no
limit; Data Saver: 1), `link` (`kind`, `via`, `minRttMs`, `srttMs`; null before a route is measured), `remembered`
(null), `now`), then `start`, the Change `init` begins on, then `steps` in order, each one of
- `helper`: the helper's `timing` as the policy reads it (`targetKbps`, `kbps`, `skipped`), with no answer;
- `tick`: `signals` (every `TierSignals` field, as in `signals/`) and `now`;
- `path`: `to` (a link as above) and `now`;
- `setBias`: `bias` and `now`; `setBudget`: `capKbps`, `maxFps`, `maxScale` and `now`;

each with `expected`, the Change it returned (`index`, `scale`, `fps`, `width`, `height`, `minKbps`, `maxKbps`,
`targetKbps`, `reason`) or null. Times are seconds, as the API takes them.

## Numbers

Units are the API's: µs in `signals`, seconds in `policy`, kbit/s and pixels elsewhere. Sizes as `CapturePlan.size`
makes them (integer division, both sides even); bitrates rounded half up, as Swift's `rounded()` does for these
positive values (the same as `Math.round`). Floats are compared within 1e-6.

## Making them again

`apple/Packages/PersonaScreenKit/Tests/PersonaScreenKitTests/TierFixtureTests.swift` holds the scenarios:
`PERSONA_WRITE_TIER_FIXTURES=1 swift test --package-path apple/Packages/PersonaScreenKit --filter TierFixtureTests`
writes every file. Without the variable that test fails when a file is not what the Swift side writes now (or when a
file no scenario writes is left), so a change in behaviour shows up in both languages' tests until both agree.
