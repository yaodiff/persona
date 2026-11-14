// Which codecs the live screen offers (#6558): only what this browser says it decodes, HEVC only in hardware, and why
// there is none: not a secure address, or no codec.
import { describe, expect, it } from "vitest";
import { probeCodecs } from "./video-probe";

const decoders = (yes: (c: VideoDecoderConfig) => boolean) => {
  const asked: VideoDecoderConfig[] = [];
  return { asked, env: { secure: true, decoders: { isConfigSupported: async (c: VideoDecoderConfig) => (asked.push(c), { supported: yes(c) }) } } };
};

describe("probeCodecs", () => {
  it("offers HEVC first, then H.264, at the size asked, HEVC only with a hardware decoder", async () => {
    const d = decoders(() => true);
    expect(await probeCodecs(2560, 1600, d.env)).toEqual({ codecs: ["hevc", "h264"] });
    expect(d.asked.map((c) => [c.codec.slice(0, 4), c.hardwareAcceleration, c.codedWidth, c.optimizeForLatency])).toEqual([
      ["hvc1", "prefer-hardware", 2560, true],
      ["avc1", "no-preference", 2560, true],
    ]);
  });

  it("offers H.264 alone when HEVC doesn't decode here, and says why when nothing does", async () => {
    expect(await probeCodecs(1920, 1080, decoders((c) => c.codec.startsWith("avc1")).env)).toEqual({ codecs: ["h264"] });
    expect(await probeCodecs(1920, 1080, decoders(() => false).env)).toEqual({ codecs: [], why: "unsupported" });
    const throws = { secure: true, decoders: { isConfigSupported: async () => Promise.reject(new TypeError("bad config")) } };
    expect(await probeCodecs(1920, 1080, throws)).toEqual({ codecs: [], why: "unsupported" });
  });

  it("offers nothing on a page that isn't a secure address (no WebCodecs), or a browser without a VideoDecoder", async () => {
    expect(await probeCodecs(1920, 1080, { secure: false, decoders: decoders(() => true).env.decoders })).toEqual({ codecs: [], why: "insecure" });
    expect(await probeCodecs(1920, 1080, { secure: true })).toEqual({ codecs: [], why: "unsupported" });
  });
});
