// One live-screen video stream in the browser (#6558): the page's side of `…/screen/video/connect?pair=…`, as the
// iPhone's ScreenVideoStream is. `start` goes out; `hello`, `format` and frames come in and are decoded with WebCodecs as
// they come (the helper never reorders), only the newest picture of a burst is handed on, and a `report` goes back for
// every frame (the helper's bandwidth adaptation). It ends after 3 s with no frame or heartbeat (not while capture is
// paused: the helper sends no heartbeat then), 15 s before the first message, on a picture that doesn't decode, or when
// the stream closes. The transport and the decoder are passed in, so it tests without a socket or WebCodecs.
import { BASE } from "../lib/base";
import { decoderSetup } from "./video-bitstream";
import { decodeOptions } from "./video-probe";
import type { Budget } from "./data-use";
import type { Change } from "./tiers/policy";
import type { Bias } from "./tiers/ladder";
import { updateOf, type ScreenVideoTiers } from "./video-tiers";
import { encode, FRAME_REFINEMENT, VideoParser, VideoWireError, decode, type Encoding, type Hello, type Start, type Timing, type Update } from "./video-wire";

/** A duplex of bytes, one stream's worth; the relay WebSocket now, a direct route later (#6398 step 6). */
export type VideoTransport = {
  send(bytes: Uint8Array): void;
  close(): void;
  /** Bytes as they come; `closed` once, when it is over (never after `close()`). */
  onBytes: (bytes: Uint8Array) => void;
  onClosed: () => void;
};

/** What the stream needs of a decoder: WebCodecs' VideoDecoder, or a test's. */
export type Decoder = {
  configure(config: VideoDecoderConfig): void;
  decode(chunk: { type: "key" | "delta"; timestamp: number; data: Uint8Array }): void;
  close(): void;
  readonly decodeQueueSize: number;
};
export type DecoderMaker = (output: (picture: Picture) => void, error: (e: unknown) => void) => Decoder;

/** A decoded picture: what WebCodecs gives (a VideoFrame), closed by whoever holds it last. */
export type Picture = { timestamp: number; close(): void; displayWidth?: number; displayHeight?: number };

export type Ending = "closed" | "silent" | "undecodable";
export type StreamEvent =
  | { type: "hello"; hello: Hello }
  | { type: "picture"; picture: Picture; decodeMs: number }
  | { type: "paused"; reason: string }
  | { type: "resumed" }
  | { type: "timing"; timing: Timing }
  | { type: "encoding"; encoding: Encoding }
  | { type: "tier"; change: Change }
  | { type: "ended"; why: Ending };

export const SILENCE_MS = 3000;
export const OPENING_MS = 15_000;

/** WebCodecs' decoder, with each output handed on as a picture (its `decode` takes an EncodedVideoChunk). */
export const webCodecsDecoder: DecoderMaker = (output, error) => {
  const d = new VideoDecoder({ output: (f) => output(f), error: (e) => error(e) });
  return {
    configure: (c) => d.configure(c),
    decode: (c) => d.decode(new EncodedVideoChunk(c)),
    close: () => d.close(),
    get decodeQueueSize() {
      return d.decodeQueueSize;
    },
  };
};

export class ScreenVideoStream {
  /** Every byte that came so far (the overlay's bitrate is what arrived). */
  received = 0;
  /** Bytes since the last `report`, as the iPhone counts them (a burst's first frame takes the whole read). */
  private sinceReport = 0;
  private parser = new VideoParser();
  private decoder: Decoder | null = null;
  private codec = "";
  private configured = false;
  private heardAt: number;
  private heardAny = false;
  private quiet = false;
  private over = false;
  private watch: ReturnType<typeof setInterval>;
  /** When each frame went into the decoder, by its number (the chunk's timestamp). */
  private began = new Map<number, number>();
  private newest: { picture: Picture; decodeMs: number } | null = null;
  private flushQueued = false;
  /** When the message still incomplete after the last read began arriving (a large frame over several reads), µs. */
  private partialSince: number | null = null;
  /** The quality tiers (#6558), when the page drives them; they start once the helper's hello lists them too. */
  readonly tiers: ScreenVideoTiers | null;

  constructor(
    private transport: VideoTransport,
    start: Start,
    private onEvent: (e: StreamEvent) => void,
    private opts: { makeDecoder?: DecoderMaker; silenceMs?: number; openingMs?: number; now?: () => number; nowMicros?: () => number; tiers?: ScreenVideoTiers | null } = {},
  ) {
    this.tiers = opts.tiers ?? null;
    this.heardAt = this.now();
    transport.onBytes = (b) => this.bytes(b);
    transport.onClosed = () => this.finish("closed");
    transport.send(encode(start));
    this.watch = setInterval(() => {
      const limit = this.heardAny ? (this.opts.silenceMs ?? SILENCE_MS) : (this.opts.openingMs ?? OPENING_MS);
      if (!this.quiet && this.now() - this.heardAt > limit) return this.finish("silent");
      if (!this.quiet && this.tiers) this.apply(this.tiers.tick(this.nowMicros())); // the tier's 250 ms decision
    }, 250);
  }

  private now() {
    return this.opts.now?.() ?? performance.now();
  }

  /** The page's clock in µs for `report`: the helper compares only differences, so the clocks needn't agree. */
  private nowMicros() {
    return this.opts.nowMicros?.() ?? Math.round((performance.timeOrigin + performance.now()) * 1000);
  }

  /** Ends it from this side; no `ended` event follows. */
  close(): void {
    if (this.over) return;
    this.over = true;
    this.stop();
    this.transport.close();
  }

  /** `update` to the helper: pause, a new cap or frame rate. */
  send(update: Omit<Update, "type">): void {
    if (!this.over) this.transport.send(encode({ type: "update", ...update }));
  }

  /** A tier the policy chose: to the helper, then to the link. */
  private apply(change: Change | null) {
    if (!change || this.over) return;
    this.send(updateOf(change));
    this.onEvent({ type: "tier", change });
  }

  /** ⋯ › Quality or the data cap changed: the policy looks again. */
  tierBias(b: Bias): void {
    if (this.tiers) this.apply(this.tiers.setBias(b, this.nowMicros()));
  }
  tierBudget(b: Budget): void {
    if (this.tiers) this.apply(this.tiers.setBudget(b, this.nowMicros()));
  }

  /** A decoded picture reached the screen, or a newer one replaced it before it did (the page's draw loop). */
  shown(replaced: boolean): void {
    this.tiers?.shown(replaced, this.nowMicros());
  }

  private stop() {
    clearInterval(this.watch);
    this.newest?.picture.close();
    this.newest = null;
    try {
      this.decoder?.close();
    } catch {
      // already closed by an error
    }
    this.decoder = null;
  }

  private finish(why: Ending) {
    if (this.over) return;
    this.over = true;
    this.stop();
    this.transport.close();
    this.onEvent({ type: "ended", why });
  }

  private heard(paused = false) {
    this.heardAt = this.now();
    this.heardAny = true;
    this.quiet = paused;
  }

  private bytes(chunk: Uint8Array) {
    if (this.over) return;
    this.received += chunk.length;
    this.sinceReport += chunk.length;
    const arrived = this.nowMicros();
    const waiting = this.parser.pendingBytes > 0 ? this.partialSince : null;
    let raws;
    try {
      raws = this.parser.push(chunk);
    } catch {
      return this.finish("undecodable");
    }
    this.partialSince = this.parser.pendingBytes > 0 ? (raws.length ? arrived : (this.partialSince ?? arrived)) : null;
    let firstOfRead = true;
    for (const raw of raws) {
      const first = firstOfRead;
      firstOfRead = false;
      let m;
      try {
        m = decode(raw.type, raw.body);
      } catch (e) {
        if (e instanceof VideoWireError) return this.finish("undecodable");
        throw e;
      }
      switch (m.type) {
        case "hello":
          this.heard();
          this.codec = m.codec;
          this.newDecoder();
          this.onEvent({ type: "hello", hello: m });
          if (this.tiers) this.apply(this.tiers.hello(m, this.nowMicros() / 1_000_000));
          break;
        case "format": {
          const setup = decoderSetup(this.codec, m.parameter_sets);
          if (!setup || !this.decoder) return this.finish("undecodable");
          try {
            this.decoder.configure({ codec: setup.codec, description: setup.description, ...decodeOptions(this.codec) });
            this.configured = true;
          } catch {
            return this.finish("undecodable");
          }
          break;
        }
        case "frame": {
          this.heard();
          this.transport.send(encode({ type: "report", last_frame: m.number, capture_micros: m.capture_micros, arrival_micros: this.nowMicros(), bytes_since_last: this.sinceReport, flags: 0 }));
          this.sinceReport = 0;
          // Only the read's first message can have begun in an earlier read.
          this.tiers?.frame(m.capture_micros, arrived, first ? waiting : null, raw.body.length + 5, (m.flags & FRAME_REFINEMENT) !== 0);
          if (!this.decoder || !this.configured) return this.finish("undecodable");
          this.began.set(m.number, this.now());
          try {
            this.decoder.decode({ type: m.flags & 1 ? "key" : "delta", timestamp: m.number, data: m.sample });
          } catch {
            return this.finish("undecodable");
          }
          break;
        }
        case "heartbeat":
          this.heard();
          break;
        case "paused":
          this.heard(true);
          this.newest?.picture.close();
          this.newest = null;
          this.onEvent({ type: "paused", reason: m.reason });
          break;
        case "resumed":
          this.heard();
          this.onEvent({ type: "resumed" });
          break;
        case "timing":
          this.tiers?.helper(m);
          this.onEvent({ type: "timing", timing: m });
          break;
        case "encoding":
          this.onEvent({ type: "encoding", encoding: m });
          break;
        default:
          break; // client → helper types, or ones newer than this page: skipped
      }
    }
  }

  private newDecoder() {
    try {
      this.decoder?.close();
    } catch {
      // closed already
    }
    this.configured = false;
    this.began.clear();
    const make = this.opts.makeDecoder ?? webCodecsDecoder;
    const decoder: Decoder = make(
      (picture) => this.decoded(decoder, picture),
      () => this.finish("undecodable"),
    );
    this.decoder = decoder;
  }

  /** A picture came out: the newest of a burst is handed on once the decoder has nothing more queued. */
  private decoded(from: Decoder, picture: Picture) {
    if (this.over || from !== this.decoder) return picture.close();
    const began = this.began.get(picture.timestamp);
    this.began.delete(picture.timestamp);
    this.newest?.picture.close();
    this.newest = { picture, decodeMs: began === undefined ? 0 : this.now() - began };
    if (began !== undefined) this.tiers?.decoded(this.newest.decodeMs, this.nowMicros());
    if (this.flushQueued) return;
    this.flushQueued = true;
    queueMicrotask(() => {
      this.flushQueued = false;
      const n = this.newest;
      if (!n || this.over) return;
      if (this.decoder && this.decoder.decodeQueueSize > 0) return; // more on the way: the newest of them shows
      this.newest = null;
      this.onEvent({ type: "picture", picture: n.picture, decodeMs: n.decodeMs });
    });
  }
}

/** The WebSocket of the Mac's live-screen video channel (#3954): bound to the RFB connection that carried `pair` and
 *  signed in, so no password travels anywhere new. */
export function videoSocket(location: { protocol: string; host: string }, mid: string, token: string, pair: string): string {
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${location.host}${BASE}/api/v1/machines/${encodeURIComponent(mid)}/screen/video/connect?token=${encodeURIComponent(token)}&pair=${encodeURIComponent(pair)}`;
}

/** The relay WebSocket as a video transport. */
export function relayTransport(url: string): VideoTransport {
  const ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";
  const queue: Uint8Array[] = [];
  let closed = false;
  const t: VideoTransport = {
    send: (b) => (ws.readyState === WebSocket.OPEN ? ws.send(b) : queue.push(b)),
    close: () => {
      closed = true;
      ws.close();
    },
    onBytes: () => {},
    onClosed: () => {},
  };
  ws.onopen = () => {
    for (const b of queue.splice(0)) ws.send(b);
  };
  ws.onmessage = (e) => {
    if (e.data instanceof ArrayBuffer) t.onBytes(new Uint8Array(e.data));
  };
  ws.onclose = () => {
    if (!closed) t.onClosed();
  };
  return t;
}
