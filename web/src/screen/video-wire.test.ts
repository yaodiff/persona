// The browser's video wire against the shared cases (spec/fixtures/screen-video, written from PersonaScreenKit's own
// messages): every case decodes to what it says, a byte-exact one encodes back to the same bytes, and a JSON one survives
// a round trip. Plus what the cases can't show: a stream cut anywhere, and the broken ones.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { decode, encode, frame, MAX_MESSAGE_BYTES, VideoParser, VideoWireError, type VideoMessage } from "./video-wire";

type Case = { name: string; input: { frame_hex: string }; expected: Record<string, unknown> & { type: string }; exact: boolean };
const doc = JSON.parse(readFileSync(fileURLToPath(new URL("../../../spec/fixtures/screen-video/messages.json", import.meta.url)), "utf8")) as {
  version: number;
  cases: Case[];
};

const hex = (b: Uint8Array) => [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
const bytes = (h: string) => Uint8Array.from(h.match(/../g) ?? [], (x) => parseInt(x, 16));

/** A message as the cases write it: hex for bytes, the wire's names for the rest. */
function asCase(m: VideoMessage): Record<string, unknown> {
  switch (m.type) {
    case "format":
      return { type: "format", parameter_sets_hex: m.parameter_sets.map(hex) };
    case "frame":
      return { type: "frame", number: m.number, capture_micros: m.capture_micros, flags: m.flags, sample_hex: hex(m.sample) };
    case "unknown":
      return { type: "unknown", code: m.code, body_hex: hex(m.body) };
    default:
      return { ...m };
  }
}

/** The message a case describes. */
function fromCase(e: Case["expected"]): VideoMessage {
  switch (e.type) {
    case "format":
      return { type: "format", parameter_sets: (e.parameter_sets_hex as string[]).map(bytes) };
    case "frame":
      return { type: "frame", number: e.number as number, capture_micros: e.capture_micros as number, flags: e.flags as number, sample: bytes(e.sample_hex as string) };
    case "unknown":
      return { type: "unknown", code: e.code as number, body: bytes(e.body_hex as string) };
    default:
      return { ...e } as VideoMessage;
  }
}

describe("the shared video wire cases", () => {
  it("has the cases", () => {
    expect(doc.version).toBe(1);
    expect(doc.cases.length).toBeGreaterThan(15);
  });

  for (const c of doc.cases) {
    it(c.name, () => {
      const raws = new VideoParser().push(bytes(c.input.frame_hex));
      expect(raws).toHaveLength(1);
      expect(asCase(decode(raws[0].type, raws[0].body))).toEqual(c.expected);
      const encoded = encode(fromCase(c.expected));
      if (c.exact) expect(hex(encoded)).toBe(c.input.frame_hex);
      else {
        const [back] = new VideoParser().push(encoded);
        expect(back.type).toBe(raws[0].type);
        expect(asCase(decode(back.type, back.body))).toEqual(c.expected);
      }
    });
  }
});

describe("the video parser", () => {
  const all = Uint8Array.from(doc.cases.flatMap((c) => [...bytes(c.input.frame_hex)]));

  it("finds every message in a stream cut anywhere, a byte at a time included", () => {
    const p = new VideoParser();
    const got = [...all].flatMap((b) => p.push(Uint8Array.of(b)));
    expect(got).toHaveLength(doc.cases.length);
    const q = new VideoParser();
    const halves = [...q.push(all.slice(0, 1000)), ...q.push(all.slice(1000))];
    expect(halves.map((r) => r.type)).toEqual(got.map((r) => r.type));
  });

  it("throws on a length of 0 or over the limit", () => {
    expect(() => new VideoParser().push(Uint8Array.of(0, 0, 0, 0, 1))).toThrow(VideoWireError);
    const over = new Uint8Array(5);
    new DataView(over.buffer).setUint32(0, MAX_MESSAGE_BYTES + 1);
    expect(() => new VideoParser().push(over)).toThrow(VideoWireError);
  });

  it("throws on a truncated binary body or a JSON body that isn't an object", () => {
    expect(() => decode(3, Uint8Array.of(0, 0, 0, 1))).toThrow(VideoWireError);
    expect(() => decode(1, new TextEncoder().encode("[1]"))).toThrow(VideoWireError);
    expect(() => decode(1, new TextEncoder().encode("{"))).toThrow(VideoWireError);
  });

  it("leaves absent fields out of a JSON body, never null", () => {
    const [raw] = new VideoParser().push(encode({ type: "start", max_size: 1920, fps: 60, max_kbps: 4000, codecs: ["h264"], scale: undefined }));
    expect(new TextDecoder().decode(raw.body)).toBe('{"max_size":1920,"fps":60,"max_kbps":4000,"codecs":["h264"]}');
  });

  it("frames an empty body as a type alone", () => {
    expect(hex(frame(18))).toBe("0000000112");
  });
});
