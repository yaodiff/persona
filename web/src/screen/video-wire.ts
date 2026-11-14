// The live screen's video wire in the browser (#3954, #6558; spec/fixtures/screen-video has the rules and the cases this
// file is checked against, the same ones PersonaScreenKit replays): a message is a u32 big-endian length (the type byte
// plus the body), a u8 type and the body. JSON messages keep the wire's own field names; the binary ones (`format`,
// `frame`, `report`) are named the same way. Pure: no DOM, no WebCodecs (./video-bitstream turns `format` into a decoder
// configuration).

/** A message longer than this (or of length 0) is a broken stream, as on the Swift side. */
export const MAX_MESSAGE_BYTES = 4 * 1024 * 1024;

/** Message types. Helper → client: 1–8. Client → helper: 16–19. */
export const KIND = {
  hello: 1,
  format: 2,
  frame: 3,
  heartbeat: 4,
  paused: 5,
  resumed: 6,
  timing: 7,
  encoding: 8,
  start: 16,
  report: 17,
  keyframe: 18,
  update: 19,
} as const;

/** `frame` flags. */
export const FRAME_KEYFRAME = 1;
export const FRAME_REFINEMENT = 2;
/** `report` flags. */
export const REPORT_DECODING_ERROR = 1;

export type Hello = {
  type: "hello";
  version: number;
  codec: string; // "hevc" | "h264"
  display: number;
  width: number;
  height: number;
  rect?: number[];
  features?: string[];
  points?: number[];
  pixels?: number[];
};
export type Format = { type: "format"; parameter_sets: Uint8Array[] };
export type Frame = { type: "frame"; number: number; capture_micros: number; flags: number; sample: Uint8Array };
export type Heartbeat = { type: "heartbeat"; last: number };
export type Paused = { type: "paused"; reason: string };
export type Resumed = { type: "resumed" };
export type Timing = {
  type: "timing";
  capture_ms?: number;
  encode_ms?: number;
  network_ms?: number;
  kbps: number;
  fps: number;
  target_kbps?: number;
  skipped?: number;
  blocked_ms?: number;
  pending?: number;
};
export type Encoding = { type: "encoding"; width: number; height: number; fps: number; scale?: number; min_kbps: number; max_kbps: number };
export type Start = {
  type: "start";
  display?: number;
  max_size: number;
  fps: number;
  max_kbps: number;
  codecs: string[];
  scale?: number;
  min_kbps?: number;
  target_kbps?: number;
};
export type Report = {
  type: "report";
  last_frame: number;
  capture_micros: number;
  arrival_micros: number;
  bytes_since_last: number;
  flags: number;
};
export type Keyframe = { type: "keyframe" };
export type Update = {
  type: "update";
  display?: number;
  max_size?: number;
  fps?: number;
  max_kbps?: number;
  codecs?: string[];
  paused?: boolean;
  scale?: number;
  min_kbps?: number;
  target_kbps?: number;
  path?: { kind: string; via: string; min_rtt_ms?: number };
};
export type Unknown = { type: "unknown"; code: number; body: Uint8Array };

export type HelperMessage = Hello | Format | Frame | Heartbeat | Paused | Resumed | Timing | Encoding;
export type ClientMessage = Start | Report | Keyframe | Update;
export type VideoMessage = HelperMessage | ClientMessage | Unknown;

/** One message off the wire, before its body is decoded. */
export type RawMessage = { type: number; body: Uint8Array };

export class VideoWireError extends Error {}

/** A whole message: length prefix, type, body. */
export function frame(type: number, body: Uint8Array = new Uint8Array()): Uint8Array {
  const out = new Uint8Array(5 + body.length);
  new DataView(out.buffer).setUint32(0, body.length + 1);
  out[4] = type;
  out.set(body, 5);
  return out;
}

/** Buffers stream bytes and yields whole messages as they complete; a length of 0 or over MAX_MESSAGE_BYTES throws. */
export class VideoParser {
  private buf = new Uint8Array(0);

  /** Bytes of a message still incomplete after the last push. */
  get pendingBytes(): number {
    return this.buf.length;
  }

  push(chunk: Uint8Array): RawMessage[] {
    const joined = new Uint8Array(this.buf.length + chunk.length);
    joined.set(this.buf);
    joined.set(chunk, this.buf.length);
    const view = new DataView(joined.buffer);
    const out: RawMessage[] = [];
    let at = 0;
    while (joined.length - at >= 4) {
      const length = view.getUint32(at);
      if (length < 1 || length > MAX_MESSAGE_BYTES) throw new VideoWireError(`bad message length ${length}`);
      if (joined.length - at - 4 < length) break;
      out.push({ type: joined[at + 4], body: joined.slice(at + 5, at + 4 + length) });
      at += 4 + length;
    }
    this.buf = joined.slice(at);
    return out;
  }
}

const NAMES: Record<number, VideoMessage["type"]> = Object.fromEntries(Object.entries(KIND).map(([k, v]) => [v, k as VideoMessage["type"]]));
const JSON_TYPES = new Set(["hello", "heartbeat", "paused", "timing", "encoding", "start", "update"]);

/** A raw message's contents. An unknown type is `unknown`, kept by its length; a broken body throws. */
export function decode(type: number, body: Uint8Array): VideoMessage {
  const name = NAMES[type];
  if (!name) return { type: "unknown", code: type, body };
  if (JSON_TYPES.has(name)) return { ...jsonBody(body), type: name } as VideoMessage;
  const r = new Reader(body);
  switch (name) {
    case "format": {
      const count = r.u8();
      const parameter_sets: Uint8Array[] = [];
      for (let i = 0; i < count; i++) parameter_sets.push(r.take(r.u16()));
      return { type: "format", parameter_sets };
    }
    case "frame":
      return { type: "frame", number: r.u32(), capture_micros: r.u64(), flags: r.u8(), sample: r.rest() };
    case "report":
      return { type: "report", last_frame: r.u32(), capture_micros: r.u64(), arrival_micros: r.u64(), bytes_since_last: r.u32(), flags: r.u8() };
    case "resumed":
      return { type: "resumed" };
    default:
      return { type: "keyframe" };
  }
}

/** The whole message for `m`. JSON bodies leave absent fields out (never null). */
export function encode(m: VideoMessage): Uint8Array {
  if (m.type === "unknown") return frame(m.code, m.body);
  const type = KIND[m.type];
  if (JSON_TYPES.has(m.type)) {
    const { type: _, ...fields } = m;
    return frame(type, new TextEncoder().encode(JSON.stringify(fields)));
  }
  const w = new Writer();
  switch (m.type) {
    case "format":
      w.u8(m.parameter_sets.length);
      for (const set of m.parameter_sets) {
        w.u16(set.length);
        w.bytes(set);
      }
      break;
    case "frame":
      w.u32(m.number);
      w.u64(m.capture_micros);
      w.u8(m.flags);
      w.bytes(m.sample);
      break;
    case "report":
      w.u32(m.last_frame);
      w.u64(m.capture_micros);
      w.u64(m.arrival_micros);
      w.u32(m.bytes_since_last);
      w.u8(m.flags);
      break;
    case "resumed":
      w.bytes(new TextEncoder().encode("{}"));
      break;
  }
  return frame(type, w.done());
}

function jsonBody(body: Uint8Array): Record<string, unknown> {
  let v: unknown;
  try {
    v = JSON.parse(new TextDecoder().decode(body));
  } catch {
    throw new VideoWireError("bad JSON body");
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) throw new VideoWireError("a JSON body is not an object");
  return v as Record<string, unknown>;
}

class Reader {
  private at = 0;
  private view: DataView;
  constructor(private bytes: Uint8Array) {
    this.view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  }
  private need(n: number) {
    if (this.at + n > this.bytes.length) throw new VideoWireError("truncated body");
  }
  u8() {
    this.need(1);
    return this.view.getUint8(this.at++);
  }
  u16() {
    this.need(2);
    const v = this.view.getUint16(this.at);
    this.at += 2;
    return v;
  }
  u32() {
    this.need(4);
    const v = this.view.getUint32(this.at);
    this.at += 4;
    return v;
  }
  /** A u64 as a number: exact below 2^53, which capture times and byte counts stay under. */
  u64() {
    this.need(8);
    const v = Number(this.view.getBigUint64(this.at));
    this.at += 8;
    return v;
  }
  take(n: number) {
    this.need(n);
    const v = this.bytes.slice(this.at, this.at + n);
    this.at += n;
    return v;
  }
  rest() {
    const v = this.bytes.slice(this.at);
    this.at = this.bytes.length;
    return v;
  }
}

class Writer {
  private parts: number[] = [];
  u8(v: number) {
    this.parts.push(v & 0xff);
  }
  u16(v: number) {
    this.parts.push((v >>> 8) & 0xff, v & 0xff);
  }
  u32(v: number) {
    this.parts.push((v >>> 24) & 0xff, (v >>> 16) & 0xff, (v >>> 8) & 0xff, v & 0xff);
  }
  u64(v: number) {
    const b = new Uint8Array(8);
    new DataView(b.buffer).setBigUint64(0, BigInt(v));
    this.parts.push(...b);
  }
  bytes(b: Uint8Array) {
    for (const x of b) this.parts.push(x);
  }
  done() {
    return Uint8Array.from(this.parts);
  }
}
