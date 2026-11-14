// What the video stream and link tests share (#6558): the mock's HEVC fixture as stream bytes, a scripted transport and
// a decoder that answers each frame with a picture.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { decode, encode, VideoParser, type VideoMessage } from "./video-wire";
import type { Decoder, DecoderMaker, Picture, VideoTransport } from "./video-stream";

export const fixture = new Uint8Array(readFileSync(fileURLToPath(new URL("../../../scripts/mock/screen_video/fixture.bin", import.meta.url))));
/** The fixture's first `n` messages, framed. */
export function prefix(n: number): Uint8Array {
  const raws = new VideoParser().push(fixture).slice(0, n);
  return concat(raws.map((r) => encode(decode(r.type, r.body))));
}
export const concat = (parts: Uint8Array[]) => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) (out.set(p, at), (at += p.length));
  return out;
};
export const framed = (m: VideoMessage) => encode(m);

/** A transport the test feeds; what the page sent, decoded. */
export function scripted() {
  const sent: VideoMessage[] = [];
  let closedHere = false;
  const t: VideoTransport & { feed(b: Uint8Array): void; end(): void; sent: VideoMessage[]; closedHere: () => boolean } = {
    send: (b) => {
      for (const r of new VideoParser().push(b)) sent.push(decode(r.type, r.body));
    },
    close: () => void (closedHere = true),
    onBytes: () => {},
    onClosed: () => {},
    feed: (b) => t.onBytes(b),
    end: () => t.onClosed(),
    sent,
    closedHere: () => closedHere,
  };
  return t;
}

/** A decoder that answers each frame with a picture right away (or fails when told to). */
export function fakeDecoders(opts: { failOn?: number } = {}) {
  const made: { configured: VideoDecoderConfig[]; closed: boolean; pictures: Picture[] }[] = [];
  const make: DecoderMaker = (output, error) => {
    const rec = { configured: [] as VideoDecoderConfig[], closed: false, pictures: [] as Picture[] };
    made.push(rec);
    const d: Decoder = {
      configure: (c) => void rec.configured.push(c),
      decode: (chunk) => {
        if (chunk.timestamp === opts.failOn) return error(new Error("bad frame"));
        const p: Picture & { closed: boolean } = { timestamp: chunk.timestamp, closed: false, close() { this.closed = true; } };
        rec.pictures.push(p);
        output(p);
      },
      close: () => void (rec.closed = true),
      decodeQueueSize: 0,
    };
    return d;
  };
  return { make, made };
}

export const tick = () => new Promise((r) => setTimeout(r, 0));
