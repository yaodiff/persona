// Turning `format` into a WebCodecs decoder setup (#6558), on the mock's two fixtures (scripts/mock/screen_video:
// HEVC and H.264, encoded by VideoToolbox as Persona Screen encodes). That a browser really decodes with these is
// web/e2e/screen-video-decode.mjs's job.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { decoderSetup } from "./video-bitstream";
import { decode, VideoParser, type Format, type Hello } from "./video-wire";

const hex = (b: Uint8Array) => [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
function fixture(name: string): { hello: Hello; format: Format } {
  const raws = new VideoParser().push(readFileSync(fileURLToPath(new URL(`../../../scripts/mock/screen_video/${name}`, import.meta.url))));
  return { hello: decode(raws[0].type, raws[0].body) as Hello, format: decode(raws[1].type, raws[1].body) as Format };
}

describe("decoderSetup", () => {
  it("reads HEVC's VPS, SPS and PPS into hvc1 and an HEVCDecoderConfigurationRecord", () => {
    const { hello, format } = fixture("fixture.bin");
    expect(hello.codec).toBe("hevc");
    const s = decoderSetup("hevc", format.parameter_sets)!;
    expect(s.codec).toBe("hvc1.1.6.L93.B0"); // Main, Main-compatible, Main tier level 3.1, progressive and frame-only
    expect([s.width, s.height]).toEqual([hello.width, hello.height]);
    const d = s.description;
    expect(hex(d.slice(0, 13))).toBe("01" + "01" + "60000000" + "b00000000000" + "5d"); // version, Main, compatibility, constraints, level 93
    expect(d[16] & 3).toBe(1); // 4:2:0
    expect([d[17] & 7, d[18] & 7]).toEqual([0, 0]); // 8-bit
    expect(d[21] & 3).toBe(3); // 4-byte NAL lengths, as every frame carries them
    expect(d[22]).toBe(3); // VPS, SPS, PPS
    expect(d[23]).toBe(0x80 | 32);
  });

  it("reads H.264's SPS and PPS into avc1 and an AVCDecoderConfigurationRecord with the High extension", () => {
    const { hello, format } = fixture("fixture-h264.bin");
    expect(hello.codec).toBe("h264");
    const s = decoderSetup("h264", format.parameter_sets)!;
    expect(s.codec).toBe("avc1.64001F"); // High, level 3.1
    const d = s.description;
    expect(hex(d.slice(0, 6))).toBe("0164001fffe1"); // version, profile, compatibility, level, 4-byte lengths, one SPS
    const sps = format.parameter_sets[0], pps = format.parameter_sets[1];
    expect(hex(d.slice(6, 8 + sps.length))).toBe(hex(Uint8Array.of(0, sps.length, ...sps)));
    expect(hex(d.slice(8 + sps.length, 11 + sps.length + pps.length))).toBe(hex(Uint8Array.of(1, 0, pps.length, ...pps)));
    expect(hex(d.slice(-4))).toBe("fdf8f800"); // 4:2:0, 8-bit, no SPS extension
  });

  it("is null for parameter sets that aren't the codec's, or too short to read", () => {
    const { format } = fixture("fixture.bin");
    expect(decoderSetup("h264", format.parameter_sets)).toBeNull(); // HEVC's sets
    expect(decoderSetup("hevc", format.parameter_sets.slice(0, 2))).toBeNull(); // no PPS
    expect(decoderSetup("hevc", [Uint8Array.of(0x40, 1), Uint8Array.of(0x42, 1), Uint8Array.of(0x44, 1)])).toBeNull();
    expect(decoderSetup("av1", format.parameter_sets)).toBeNull();
  });
});
