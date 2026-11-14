// The video channel's switching in the browser (#6558), as the iPhone's ScreenVideoLink: video once RFB is signed in and
// a picture decodes; back to VNC with a plain reason when it stops; tried again after 30 s doubling to 5 min, reset after
// a minute of video; a held page (in the background) stops it with nothing said.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Budget } from "./data-use";
import { pausedFallback, ScreenVideoLink, type Fallback } from "./video-link";
import { fakeDecoders, framed, prefix, scripted } from "./video-test-helpers";
import { decode, VideoParser, type Hello, type VideoMessage } from "./video-wire";

let now = 0;
beforeEach(() => {
  vi.useFakeTimers();
  now = 0;
});
afterEach(() => vi.useRealTimers());

function link(tiers: { key: string; budget: Budget } | null = null) {
  const transports: ReturnType<typeof scripted>[] = [];
  const pairs: string[] = [];
  const l = new ScreenVideoLink({
    open: (r) => {
      pairs.push(r.pair);
      const t = scripted();
      transports.push(t);
      return t;
    },
    codecs: ["hevc", "h264"],
    maxSize: 2880,
    maxKbps: 12_000,
    fps: 60,
    makeDecoder: fakeDecoders().make,
    tiers: tiers && { key: tiers.key, bias: "auto", budget: tiers.budget },
    now: () => now,
  });
  const shows: boolean[] = [], said: (Fallback | null)[] = [];
  l.onShowing = (on) => shows.push(on);
  l.onFallback = (f) => said.push(f);
  return { l, transports, pairs, shows, said };
}
const flush = async () => {
  await vi.advanceTimersByTimeAsync(0);
};

describe("ScreenVideoLink", () => {
  it("opens once RFB is signed in and video is wanted, with the page's start; the first picture makes video the picture", async () => {
    const { l, transports, pairs, shows } = link();
    l.want({ display: 2, rect: [960, 0, 960, 600] });
    expect(transports).toHaveLength(0); // no RFB yet
    l.rfb({ pair: "0123" });
    expect(pairs).toEqual(["0123"]);
    expect(transports[0].sent[0]).toEqual({ type: "start", display: 2, max_size: 2880, fps: 60, max_kbps: 12_000, codecs: ["hevc", "h264"] });
    transports[0].feed(prefix(3));
    await flush();
    expect(shows).toEqual([true]);
    expect([l.showing, l.codec, l.size, l.rect]).toEqual([true, "HEVC", { width: 960, height: 600 }, [960, 0, 960, 600]]);
  });

  it("falls back saying why when the stream ends, retries after 30 s doubling to 5 min, and resets after a minute of video", async () => {
    const { l, transports, shows, said } = link();
    l.want({ display: null, rect: null });
    l.rfb({ pair: "p" });
    transports[0].feed(prefix(3));
    await flush();
    transports[0].end();
    expect(shows).toEqual([true, false]);
    expect(said).toEqual(["lost"]);
    expect(l.retryAt).toBe(30_000);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(transports).toHaveLength(2);
    transports[1].end(); // fails again before any picture: the line stays, the wait doubles
    expect(said).toEqual(["lost"]);
    expect(l.retryAt! - now).toBe(60_000);
    for (const wait of [60_000, 120_000, 240_000]) {
      await vi.advanceTimersByTimeAsync(wait);
      transports.at(-1)!.end();
    }
    expect(l.retryAt! - now).toBe(300_000); // at most 5 min
    await vi.advanceTimersByTimeAsync(300_000);
    transports.at(-1)!.feed(prefix(3));
    await flush();
    expect(said.at(-1)).toBe(null); // video back: the line goes
    now += 61_000; // a minute of video
    transports.at(-1)!.end();
    expect(l.retryAt! - now).toBe(30_000); // the waits start over
  });

  it("says the Mac is locked or hasn't allowed video from paused, and stops without a word while the page is held", async () => {
    expect([pausedFallback("locked"), pausedFallback("login_window"), pausedFallback("permission"), pausedFallback("busy")]).toEqual(["locked", "locked", "notAllowed", "lost"]);
    const { l, transports, said } = link();
    l.want({ display: null, rect: null });
    l.rfb({ pair: "p" });
    transports[0].feed(prefix(3));
    await flush();
    transports[0].feed(framed({ type: "paused", reason: "locked" }));
    expect([l.showing, said.at(-1)]).toEqual([false, "locked"]);
    const before = said.length;
    l.hold("background", true);
    expect(transports[0].closedHere()).toBe(true);
    expect(said.length).toBe(before); // nothing new said for a page in the background: the line stays as it was
    l.hold("background", false);
    expect(transports).toHaveLength(2); // back in front: at once
  });

  it("stops when video isn't wanted any more, and a new RFB connection tries at once", async () => {
    const { l, transports, said } = link();
    l.want({ display: null, rect: null });
    l.rfb({ pair: "a" });
    transports[0].end();
    expect(l.retryAt).toBe(30_000);
    l.rfb({ pair: "b" }); // a new connection: no wait
    expect(transports).toHaveLength(2);
    l.want(null);
    expect([transports[1].closedHere(), said.at(-1), l.retryAt]).toEqual([true, null, null]);
  });

  it("reads once a second: pictures, megabits that arrived, the helper's segments, the page's decode, and their total", async () => {
    const { l, transports } = link();
    const readings: unknown[] = [];
    l.onReading = (r) => readings.push(r);
    l.want({ display: null, rect: null });
    l.rfb({ pair: "p" });
    transports[0].feed(framed({ type: "timing", capture_ms: 0, encode_ms: 5, network_ms: 7.5, kbps: 900, fps: 30 }));
    transports[0].feed(prefix(3));
    await flush();
    now = 1000;
    transports[0].feed(prefix(4).slice(prefix(3).length)); // one more frame a second later
    await flush();
    expect(readings).toHaveLength(1);
    // the second runs from the first picture: two pictures in it; the total is capture + encode + network + decode
    expect(readings[0]).toMatchObject({ fps: 2, captureMs: 0, encodeMs: 5, networkMs: 7.5, decodeMs: 0, totalMs: 12.5 });
  });

  it("sends a new cap to the open stream as an update, nothing for the same cap, and starts the next stream with it", () => {
    const { l, transports } = link();
    l.setBudget({ maxKbps: 4_000, fps: 60 }); // nothing open: kept for the start
    l.want({ display: null, rect: null });
    l.rfb({ pair: "a" });
    expect(transports[0].sent[0]).toMatchObject({ type: "start", max_kbps: 4_000, fps: 60 });
    l.setBudget({ maxKbps: 1_500, fps: 60 });
    l.setBudget({ maxKbps: 1_500, fps: 60 });
    expect(transports[0].sent.slice(1)).toEqual([{ type: "update", fps: 60, max_kbps: 1_500 }]);
    l.rfb({ pair: "b" });
    expect(transports[1].sent[0]).toMatchObject({ type: "start", max_kbps: 1_500 });
  });

  it("with tiers on both sides: the start tier, the data cap and Quality go out as tier updates, and the tier is where this computer's next stream starts", async () => {
    const { l, transports } = link({ key: "tiers-mac", budget: { maxKbps: 1_500, fps: 60, maxScale: 1 } });
    l.setBudget({ maxKbps: 4_000, fps: 60 }, { maxKbps: 1_500, fps: 60, maxScale: 1 }); // mobile data, on Data Saver
    l.want({ display: null, rect: null });
    l.rfb({ pair: "a" });
    expect(transports[0].sent[0]).toMatchObject({ type: "start", max_kbps: 4_000 }); // tiers aren't known before hello
    transports[0].feed(tieredHello());
    const updates = () => transports[0].sent.filter((m): m is Extract<VideoMessage, { type: "update" }> => m.type === "update");
    expect(updates()[0].scale, "Data Saver: one pixel per point").toBe(1);
    expect(updates()[0].max_kbps).toBeLessThanOrEqual(1_500);
    expect([l.tier?.reason, l.tierAt]).toEqual(["start", 0]);
    now = 5_000;
    l.setBudget({ maxKbps: 4_000, fps: 60 }, { maxKbps: 4_000, fps: 60 }); // Clearer
    expect([l.tier?.reason, l.tierAt, updates().length]).toEqual(["network_changed", 5_000, 2]);
    expect(updates().every((u) => u.scale !== undefined), "the tiers set the cap: no plain update").toBe(true);
    l.setQuality("smoother");
    l.setQuality("smoother");
    expect([l.tier?.reason, updates().length]).toEqual(["choice", 3]);
    const settled = l.tier!.index;
    transports[0].feed(framed({ type: "encoding", width: 1280, height: 800, fps: 60, scale: 1, min_kbps: 900, max_kbps: 4_000 }));
    expect(l.size).toEqual({ width: 1280, height: 800 });
    l.rfb({ pair: "b" });
    transports[1].feed(tieredHello());
    expect([l.tier?.reason, l.tier?.index]).toEqual(["start", settled]);
  });

  it("with a helper without tiers the cap goes as before and Quality changes nothing", () => {
    const { l, transports } = link({ key: "plain-mac", budget: { maxKbps: 12_000, fps: 60 } });
    l.want({ display: null, rect: null });
    l.rfb({ pair: "a" });
    transports[0].feed(prefix(1)); // a hello without features
    l.setQuality("sharper");
    l.setBudget({ maxKbps: 4_000, fps: 60 }, { maxKbps: 1_500, fps: 60, maxScale: 1 });
    expect([l.tier, transports[0].sent.slice(1)]).toEqual([null, [{ type: "update", fps: 60, max_kbps: 4_000 }]]);
  });
});

/** The fixture's hello from a helper with tiers on a 960×600-point display, then its format. */
function tieredHello(): Uint8Array {
  const raws = new VideoParser().push(prefix(2));
  const h = decode(raws[0].type, raws[0].body) as Hello;
  const hello = framed({ ...h, features: ["tiers"], points: [960, 600], pixels: [1920, 1200] });
  const format = framed(decode(raws[1].type, raws[1].body));
  return new Uint8Array([...hello, ...format]);
}
