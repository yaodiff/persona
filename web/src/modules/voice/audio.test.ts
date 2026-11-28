// BrowserVoiceAudio against fake Web Audio and getUserMedia: what reaches the capture worklet and the call (the real audio
// path is walked in a browser by web/e2e/voice.mjs).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserVoiceAudio, KEEP_AWAKE_LEVEL, REPLY_CUSHION_S, REPLY_HOLD_TIMEOUT_MS } from "./audio";
import type { VoiceAudioEvent } from "./conversation";
import type { CaptureFrame } from "./capture.worklet";

/** A node of the fake graph: what it is and where its output goes. */
class FakeNode {
  outs: FakeNode[] = [];
  constructor(readonly kind: string) {}
  connect<T>(x: T): T {
    this.outs.push(x as unknown as FakeNode);
    return x;
  }
  disconnect() {
    this.outs = [];
  }
}
class FakeGain extends FakeNode {
  gain = { value: 1 };
  constructor() {
    super("gain");
  }
}
class FakeConstant extends FakeNode {
  offset = { value: 1 };
  started = false;
  stopped = false;
  constructor() {
    super("constant");
  }
  start() {
    this.started = true;
  }
  stop() {
    this.stopped = true;
  }
}
class FakeBuffer {
  readonly duration: number;
  data: Float32Array;
  constructor(
    readonly numberOfChannels: number,
    readonly length: number,
    readonly sampleRate: number,
  ) {
    this.duration = length / sampleRate;
    this.data = new Float32Array(length);
  }
  copyToChannel(src: Float32Array) {
    this.data = new Float32Array(src);
  }
}
class FakeBufferSource extends FakeNode {
  buffer: FakeBuffer | null = null;
  startedAt: number | null = null;
  stopped = false;
  onended: (() => void) | null = null;
  constructor() {
    super("bufferSource");
  }
  start(when = 0) {
    this.startedAt = when;
  }
  stop() {
    this.stopped = true;
  }
}

/** Every path from `from` to `to`, as the nodes along it. */
function paths(from: FakeNode, to: FakeNode, seen: FakeNode[] = []): FakeNode[][] {
  if (from === to) return [[...seen, from]];
  return from.outs.flatMap((next) => (seen.includes(next) ? [] : paths(next, to, [...seen, from])));
}

class FakePort {
  sent: unknown[] = [];
  onmessage: ((e: { data: CaptureFrame }) => void) | null = null;
  closed = false;
  postMessage(m: unknown) {
    this.sent.push(m);
  }
  close() {
    this.closed = true;
  }
}

const nodes: FakeWorkletNode[] = [];
class FakeWorkletNode extends FakeNode {
  port = new FakePort();
  constructor(
    readonly ctx: unknown,
    readonly name: string,
    readonly options: { processorOptions: { rate: number; muted: boolean } },
  ) {
    super("worklet");
    nodes.push(this);
  }
}

const contexts: FakeContext[] = [];
class FakeContext {
  state = "running";
  closed = false;
  sampleRate: number;
  currentTime = 0;
  destination = new FakeNode("destination");
  made: FakeNode[] = [];
  sources: FakeBufferSource[] = [];
  onstatechange: (() => void) | null = null;
  audioWorklet = { addModule: async () => {} };
  constructor(o?: { sampleRate?: number }) {
    this.sampleRate = o?.sampleRate ?? 48000;
    contexts.push(this);
  }
  resume() {
    return Promise.resolve();
  }
  close() {
    this.closed = true;
    return Promise.resolve();
  }
  private make<T extends FakeNode>(node: T): T {
    this.made.push(node);
    return node;
  }
  createMediaStreamSource() {
    return this.make(new FakeNode("microphone"));
  }
  createGain() {
    return this.make(new FakeGain());
  }
  createConstantSource() {
    return this.make(new FakeConstant());
  }
  createBuffer(channels: number, length: number, sampleRate: number) {
    return new FakeBuffer(channels, length, sampleRate);
  }
  createBufferSource() {
    const s = this.make(new FakeBufferSource());
    this.sources.push(s);
    return s;
  }
}

function fakeStream() {
  const track = { stopped: false, onended: null as null | (() => void), stop() { this.stopped = true; }, getSettings: () => ({ echoCancellation: true }) };
  return { track, stream: { getTracks: () => [track], getAudioTracks: () => [track] } };
}

let answer: (s: unknown) => void;
beforeEach(() => {
  nodes.length = 0;
  contexts.length = 0;
  vi.stubGlobal("AudioContext", FakeContext);
  vi.stubGlobal("AudioWorkletNode", FakeWorkletNode);
  vi.stubGlobal("navigator", { mediaDevices: { getUserMedia: () => new Promise((resolve) => (answer = resolve)) } });
});
/** Every audio a test made, stopped after it (a call's pace check runs on an interval). */
const audios: BrowserVoiceAudio[] = [];
const owned = (audio: BrowserVoiceAudio) => (audios.push(audio), audio);
afterEach(() => {
  audios.splice(0).forEach((a) => a.stop());
  vi.unstubAllGlobals();
});

const opts = (frames: [Int16Array, number][] = []) => ({ inputRate: 16000, muted: false, onFrame: (f: Int16Array, r: number) => frames.push([f, r]), onEvent: () => {} });

describe("BrowserVoiceAudio", () => {
  it("a mute tapped while the microphone's permission is asked reaches the worklet made after it", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts());
    audio.setMuted(true);
    answer(fakeStream().stream);
    await started;
    expect(nodes).toHaveLength(1);
    expect(nodes[0].options.processorOptions).toEqual({ rate: 16000, muted: true });
  });

  it("passes each frame on at the rate the worklet made it, not the one asked for since", async () => {
    const frames: [Int16Array, number][] = [];
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts(frames));
    answer(fakeStream().stream);
    await started;
    audio.setInputRate(24000);
    expect(nodes[0].port.sent).toContainEqual({ type: "rate", rate: 24000 });
    // posted before the worklet heard of the new rate, then after
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0.2 } });
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(480).buffer, rate: 24000, level: 0.2 } });
    expect(frames.map(([f, r]) => [f.length, r])).toEqual([[320, 16000], [480, 24000]]);
  });

  it("reports no input level while muted", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts());
    answer(fakeStream().stream);
    await started;
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0.5 } });
    expect(audio.levels().input).toBe(0.5);
    audio.setMuted(true);
    expect(nodes[0].port.sent).toContainEqual({ type: "mute", on: true });
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0.5 } });
    expect(audio.levels().input).toBe(0);
  });

  it("keeps the capture context's output above zero and inaudible, and never plays the microphone back", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const [capture, player] = contexts;
    // from the click on, before the microphone's permission is answered
    const awake = capture.made.filter((n): n is FakeConstant => n instanceof FakeConstant);
    expect(awake).toHaveLength(1);
    expect(awake[0].started).toBe(true);
    expect(awake[0].outs).toEqual([capture.destination]);
    // not zero (Chrome's silent-sink suspender looks for exact zeros), under half a step of a 24-bit converter
    expect(awake[0].offset.value).toBe(KEEP_AWAKE_LEVEL);
    expect(KEEP_AWAKE_LEVEL).toBeGreaterThan(0);
    expect(KEEP_AWAKE_LEVEL).toBeLessThan(2 ** -24);

    const started = audio.start(opts());
    answer(fakeStream().stream);
    await started;
    const microphone = capture.made.find((n) => n.kind === "microphone")!;
    expect(microphone.outs).toEqual([nodes[0]]);
    // the only way from the microphone to the speaker is the worklet's unwritten output, through a gain of 0
    const toSpeaker = paths(microphone, capture.destination);
    expect(toSpeaker.map((p) => p.map((n) => n.kind))).toEqual([["microphone", "worklet", "gain", "destination"]]);
    expect((toSpeaker[0][2] as FakeGain).gain.value).toBe(0);
    // the player is not touched
    expect(player.made).toEqual([]);

    audio.stop();
    expect(awake[0].stopped).toBe(true);
    expect(awake[0].outs).toEqual([]);
  });

  it("logs at debug level when the microphone's frames fall behind the wall clock", async () => {
    vi.useFakeTimers();
    const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
    try {
      const audio = owned(new BrowserVoiceAudio(24000));
      const started = audio.start(opts());
      answer(fakeStream().stream);
      await started;
      const capture = contexts[0];
      const frame = () => nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0 } });
      // five seconds at 50 frames a second: quiet
      frame();
      for (let i = 0; i < 250; i++) {
        vi.advanceTimersByTime(20);
        capture.currentTime += 0.02;
        frame();
      }
      expect(debug).not.toHaveBeenCalled();
      // the context's clock at a quarter of real time: a frame every 80 ms
      for (let i = 0; i < 63; i++) {
        vi.advanceTimersByTime(80);
        capture.currentTime += 0.02;
        frame();
      }
      expect(debug).toHaveBeenCalledTimes(1);
      expect(String(debug.mock.calls[0][0])).toMatch(/^\[voice\] microphone frames fell behind: \d+ in 5\.0 s, 250 expected; the capture context's clock moved 1\.\d s/);
      audio.stop();
      vi.advanceTimersByTime(20_000);
      expect(debug).toHaveBeenCalledTimes(1);
    } finally {
      debug.mockRestore();
      vi.useRealTimers();
    }
  });

  it("releases the microphone and closes both contexts on stop, even when the microphone answers after it", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts());
    audio.stop();
    const { track, stream } = fakeStream();
    answer(stream);
    await started;
    expect(track.stopped).toBe(true);
    expect(nodes).toHaveLength(0);
    expect(contexts.map((c) => c.closed)).toEqual([true, true]);

    const again = owned(new BrowserVoiceAudio(24000));
    const live = fakeStream();
    const going = again.start(opts());
    answer(live.stream);
    await going;
    again.stop();
    expect(live.track.stopped).toBe(true);
    expect(nodes[0].port.closed).toBe(true);
    expect(contexts.slice(2).map((c) => c.closed)).toEqual([true, true]);
  });
});

/** PCM16 little-endian audio of `ms` milliseconds at `rate`, filled with `sample` so tests can verify ordering. */
function pcmChunk(ms: number, rate = 24000, sample = 1000): ArrayBuffer {
  const count = Math.round((rate * ms) / 1000);
  const view = new DataView(new ArrayBuffer(count * 2));
  for (let i = 0; i < count; i++) view.setInt16(i * 2, sample, true);
  return view.buffer;
}

describe("BrowserVoiceAudio reply playback", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  async function startedAudio(events: VoiceAudioEvent[] = []) {
    const audio = owned(new BrowserVoiceAudio(24000));
    const player = contexts[1];
    const started = audio.start({ inputRate: 16000, muted: false, onFrame: () => {}, onEvent: (e) => events.push(e) });
    answer(fakeStream().stream);
    await started;
    return { audio, player, events };
  }

  it("schedules chunks immediately when no reply frames are sent (behaviour unchanged)", async () => {
    const { audio, player, events } = await startedAudio();
    audio.play(pcmChunk(20, 24000, 1000), 24000);
    expect(player.sources).toHaveLength(1);
    expect(player.sources[0].startedAt).toBeCloseTo(0.05, 6);
    expect(events).toEqual([{ kind: "playing", on: true }]);

    audio.play(pcmChunk(20, 24000, 2000), 24000);
    expect(player.sources).toHaveLength(2);
    expect(player.sources[1].startedAt).toBeCloseTo(0.07, 6);

    // After a gap and drain grace, playback turns off and the next unframed chunk still plays immediately.
    player.currentTime = 0.5;
    vi.advanceTimersByTime(500);
    expect(events).toEqual([{ kind: "playing", on: true }, { kind: "playing", on: false }]);

    audio.play(pcmChunk(20, 24000, 3000), 24000);
    expect(player.sources).toHaveLength(3);
    expect(player.sources[2].startedAt).toBeCloseTo(0.55, 6);
    expect(events).toEqual([{ kind: "playing", on: true }, { kind: "playing", on: false }, { kind: "playing", on: true }]);
  });

  it("buffers a small cushion before starting playback within an open reply, then appends seamlessly", async () => {
    const { audio, player, events } = await startedAudio();
    expect(REPLY_CUSHION_S).toBeGreaterThanOrEqual(0.06);
    expect(REPLY_CUSHION_S).toBeLessThanOrEqual(0.12);

    audio.reply(true);
    for (let i = 1; i <= 4; i++) {
      audio.play(pcmChunk(20, 24000, i * 1000), 24000);
      expect(player.sources).toHaveLength(0);
      expect(events).toEqual([]);
    }

    // Reaching the 100 ms cushion flushes all five chunks in order, back-to-back.
    audio.play(pcmChunk(20, 24000, 5000), 24000);
    expect(player.sources).toHaveLength(5);
    expect(player.sources.map((s) => Number(s.startedAt!.toFixed(3)))).toEqual([0.05, 0.07, 0.09, 0.11, 0.13]);
    expect(player.sources.map((s) => Math.round(s.buffer!.data[0] * 32768))).toEqual([1000, 2000, 3000, 4000, 5000]);
    expect(events).toEqual([{ kind: "playing", on: true }]);

    // While audio is still playing, subsequent chunks append immediately without re-buffering.
    player.currentTime = 0.06;
    audio.play(pcmChunk(20, 24000, 6000), 24000);
    expect(player.sources).toHaveLength(6);
    expect(player.sources[5].startedAt).toBeCloseTo(0.15, 6);

    // A single chunk already at least as long as the cushion plays at once.
    audio.reply(false);
    player.currentTime = 1.0;
    audio.reply(true);
    audio.play(pcmChunk(120, 24000, 7000), 24000);
    expect(player.sources).toHaveLength(7);
    expect(player.sources[6].startedAt).toBeCloseTo(1.05, 6);
  });

  it("plays held audio when the hold timeout expires even if the cushion has not filled", async () => {
    const { audio, player, events } = await startedAudio();
    audio.reply(true);
    audio.play(pcmChunk(20, 24000, 1000), 24000);

    // A second small chunk arrives later; the deadline stays anchored to the first held chunk.
    vi.advanceTimersByTime(100);
    player.currentTime = 0.1;
    audio.play(pcmChunk(20, 24000, 2000), 24000);
    expect(player.sources).toHaveLength(0);

    vi.advanceTimersByTime(REPLY_HOLD_TIMEOUT_MS - 101);
    player.currentTime = (REPLY_HOLD_TIMEOUT_MS - 1) / 1000;
    expect(player.sources).toHaveLength(0);

    player.currentTime = REPLY_HOLD_TIMEOUT_MS / 1000;
    vi.advanceTimersByTime(1);
    expect(player.sources).toHaveLength(2);
    expect(player.sources[0].startedAt).toBeCloseTo(REPLY_HOLD_TIMEOUT_MS / 1000 + 0.05, 6);
    expect(player.sources[1].startedAt).toBeCloseTo(REPLY_HOLD_TIMEOUT_MS / 1000 + 0.07, 6);
    expect(events).toEqual([{ kind: "playing", on: true }]);
  });

  it("re-buffers across a gap inside an open reply without clicks or pile-ups", async () => {
    const { audio, player } = await startedAudio();
    audio.reply(true);
    // Sentence 1: 100 ms plays at 0.05..0.15.
    for (let i = 0; i < 5; i++) audio.play(pcmChunk(20, 24000, 1000), 24000);
    expect(player.sources).toHaveLength(5);

    // 500 ms silence gap while the next sentence is synthesized; player is well past 0.15.
    player.currentTime = 0.65;
    for (let i = 1; i <= 4; i++) {
      audio.play(pcmChunk(20, 24000, 2000 + i), 24000);
      expect(player.sources).toHaveLength(5);
    }

    // Cushion fills for sentence 2: schedules freshly ahead of currentTime with no overlap or past timestamps.
    player.currentTime = 0.69;
    audio.play(pcmChunk(20, 24000, 2005), 24000);
    expect(player.sources).toHaveLength(10);
    const s2 = player.sources.slice(5);
    expect(s2.map((s) => Number(s.startedAt!.toFixed(3)))).toEqual([0.74, 0.76, 0.78, 0.8, 0.82]);
    for (let i = 1; i < s2.length; i++) {
      expect(s2[i].startedAt!).toBeCloseTo(s2[i - 1].startedAt! + s2[i - 1].buffer!.duration, 6);
    }
  });

  it("flushes queued audio immediately on reply end and returns to unframed playback afterwards", async () => {
    const { audio, player, events } = await startedAudio();
    audio.reply(true);
    audio.play(pcmChunk(20, 24000, 1000), 24000);
    audio.play(pcmChunk(20, 24000, 2000), 24000);
    expect(player.sources).toHaveLength(0);

    audio.reply(false);
    expect(player.sources).toHaveLength(2);
    expect(player.sources.map((s) => Number(s.startedAt!.toFixed(3)))).toEqual([0.05, 0.07]);
    expect(events).toEqual([{ kind: "playing", on: true }]);

    // Hold timer was cancelled, and a redundant reply(false) is a no-op.
    vi.advanceTimersByTime(REPLY_HOLD_TIMEOUT_MS + 100);
    audio.reply(false);
    expect(player.sources).toHaveLength(2);

    // After reply end, an unframed chunk plays immediately.
    player.currentTime = 1.0;
    audio.play(pcmChunk(20, 24000, 3000), 24000);
    expect(player.sources).toHaveLength(3);
    expect(player.sources[2].startedAt).toBeCloseTo(1.05, 6);
  });

  it("drops held and playing audio immediately on clear during a reply", async () => {
    const { audio, player, events } = await startedAudio();
    // Case 1: clear while holding before first play, followed by reply(false) as the server sends.
    audio.reply(true);
    audio.play(pcmChunk(20, 24000, 1000), 24000);
    audio.play(pcmChunk(20, 24000, 2000), 24000);
    audio.clear();
    audio.reply(false);
    vi.advanceTimersByTime(REPLY_HOLD_TIMEOUT_MS + 100);
    expect(player.sources).toHaveLength(0);
    expect(events).toEqual([]);

    // Case 2: clear while playing sentence 1 and holding sentence 2 after a gap.
    audio.reply(true);
    for (let i = 0; i < 5; i++) audio.play(pcmChunk(20, 24000, 3000), 24000);
    expect(player.sources).toHaveLength(5);
    player.currentTime = 0.5;
    audio.play(pcmChunk(20, 24000, 4000), 24000);
    audio.clear();
    expect(player.sources.every((s) => s.stopped)).toBe(true);
    expect(events).toEqual([{ kind: "playing", on: true }, { kind: "playing", on: false }]);
    vi.advanceTimersByTime(REPLY_HOLD_TIMEOUT_MS + 100);
    expect(player.sources).toHaveLength(5);

    // Case 3: a fresh reply after clear gathers a full cushion (dropped chunks do not carry over).
    audio.reply(true);
    for (let i = 0; i < 4; i++) audio.play(pcmChunk(20, 24000, 5000), 24000);
    expect(player.sources).toHaveLength(5);
    audio.play(pcmChunk(20, 24000, 5000), 24000);
    expect(player.sources).toHaveLength(10);
  });
});
