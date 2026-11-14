// What a browser's VideoDecoder needs from the live screen's `format` message (#6558): the codec string and the
// decoder configuration record (`description`) for the parameter sets Persona Screen sends: H.264's SPS and PPS
// (`avc1.PPCCLL` and an AVCDecoderConfigurationRecord) or HEVC's VPS, SPS and PPS (`hvc1.…` and an
// HEVCDecoderConfigurationRecord), both as ISO/IEC 14496-15 lays them out, with 4-byte NAL lengths, which is how every
// `frame` carries its sample. Pure; checked against the mock's fixtures and, in a real browser, by
// web/e2e/screen-video-decode.mjs.

export type DecoderSetup = {
  /** WebCodecs' codec string, e.g. "avc1.640028" or "hvc1.1.6.L93.B0". */
  codec: string;
  description: Uint8Array;
  /** The picture's size as the SPS gives it, after its conformance window (HEVC only; H.264's is left to `hello`). */
  width?: number;
  height?: number;
};

/** The setup for `codec` ("hevc" | "h264") from `format`'s parameter sets; null when they aren't what that codec needs. */
export function decoderSetup(codec: string, parameterSets: Uint8Array[]): DecoderSetup | null {
  try {
    if (codec === "h264") return avc(parameterSets);
    if (codec === "hevc") return hevc(parameterSets);
  } catch {
    // a parameter set too short to read: no setup, and the page stays on VNC
  }
  return null;
}

const hex2 = (v: number) => v.toString(16).toUpperCase().padStart(2, "0");

function avc(sets: Uint8Array[]): DecoderSetup | null {
  const sps = sets.filter((s) => (s[0] & 0x1f) === 7);
  const pps = sets.filter((s) => (s[0] & 0x1f) === 8);
  if (!sps.length || !pps.length || sps[0].length < 4) return null;
  const [profile, compat, level] = [sps[0][1], sps[0][2], sps[0][3]];
  const out = new Out();
  out.u8(1, profile, compat, level, 0xff, 0xe0 | sps.length);
  for (const s of sps) out.nal(s);
  out.u8(pps.length);
  for (const p of pps) out.nal(p);
  if ([100, 110, 122, 144].includes(profile)) {
    // the High profiles' extension: chroma format and bit depths, from the SPS
    const r = new Bits(rbsp(sps[0], 1));
    r.skip(24); // profile, constraint flags, level
    r.ue(); // seq_parameter_set_id
    const chroma = r.ue();
    if (chroma === 3) r.skip(1);
    out.u8(0xfc | chroma, 0xf8 | r.ue(), 0xf8 | r.ue(), 0);
  }
  return { codec: `avc1.${hex2(profile)}${hex2(compat)}${hex2(level)}`, description: out.done() };
}

function hevc(sets: Uint8Array[]): DecoderSetup | null {
  const kind = (s: Uint8Array) => (s[0] >> 1) & 0x3f;
  const vps = sets.filter((s) => kind(s) === 32);
  const sps = sets.filter((s) => kind(s) === 33);
  const pps = sets.filter((s) => kind(s) === 34);
  if (!vps.length || !sps.length || !pps.length) return null;
  const r = new Bits(rbsp(sps[0], 2));
  r.skip(4); // sps_video_parameter_set_id
  const subLayers = r.u(3); // sps_max_sub_layers_minus1
  const nested = r.u(1);
  // profile_tier_level: the general part is copied into the record as it is
  const space = r.u(2), tier = r.u(1), profile = r.u(5);
  const compat = r.u(32) >>> 0;
  const constraints = Array.from({ length: 6 }, () => r.u(8));
  const level = r.u(8);
  const present: [number, number][] = [];
  for (let i = 0; i < subLayers; i++) present.push([r.u(1), r.u(1)]);
  if (subLayers > 0) for (let i = subLayers; i < 8; i++) r.skip(2);
  for (const [p, l] of present) r.skip((p ? 88 : 0) + (l ? 8 : 0));
  r.ue(); // sps_seq_parameter_set_id
  const chroma = r.ue();
  if (chroma === 3) r.skip(1);
  let width = r.ue(), height = r.ue();
  if (r.u(1)) {
    const [left, right, top, bottom] = [r.ue(), r.ue(), r.ue(), r.ue()];
    const sx = chroma === 1 || chroma === 2 ? 2 : 1, sy = chroma === 1 ? 2 : 1;
    width -= sx * (left + right);
    height -= sy * (top + bottom);
  }
  const depthLuma = r.ue(), depthChroma = r.ue();

  const out = new Out();
  out.u8(1, (space << 6) | (tier << 5) | profile);
  out.u32(compat);
  out.u8(...constraints, level);
  out.u16(0xf000); // min_spatial_segmentation_idc 0
  out.u8(0xfc, 0xfc | chroma, 0xf8 | depthLuma, 0xf8 | depthChroma);
  out.u16(0); // avgFrameRate
  out.u8(((subLayers + 1) << 3) | (nested << 2) | 3); // 4-byte NAL lengths
  const arrays = [vps, sps, pps];
  out.u8(arrays.length);
  for (const nals of arrays) {
    out.u8(0x80 | kind(nals[0])); // array_completeness: every set of this kind is here
    out.u16(nals.length);
    for (const n of nals) out.nal(n);
  }

  // hvc1.<space><profile>.<compatibility, bits reversed>.<tier><level>[.<constraint bytes, trailing zeros left out>]
  let reversed = 0;
  for (let i = 0; i < 32; i++) if (compat & (1 << i)) reversed |= 1 << (31 - i);
  const bytes = [...constraints];
  while (bytes.length && bytes[bytes.length - 1] === 0) bytes.pop();
  const codec = [`hvc1.${["", "A", "B", "C"][space]}${profile}`, (reversed >>> 0).toString(16).toUpperCase(), `${tier ? "H" : "L"}${level}`, ...bytes.map(hex2)].join(".");
  return { codec, description: out.done(), width, height };
}

/** A NAL unit's payload without its header and with emulation-prevention bytes (00 00 03) taken out. */
function rbsp(nal: Uint8Array, header: number): Uint8Array {
  const out: number[] = [];
  let zeros = 0;
  for (let i = header; i < nal.length; i++) {
    const b = nal[i];
    if (zeros >= 2 && b === 3) {
      zeros = 0;
      continue;
    }
    zeros = b === 0 ? zeros + 1 : 0;
    out.push(b);
  }
  return Uint8Array.from(out);
}

class Bits {
  private at = 0;
  constructor(private bytes: Uint8Array) {}
  u(n: number): number {
    let v = 0;
    for (let i = 0; i < n; i++) {
      const byte = this.bytes[this.at >> 3];
      if (byte === undefined) throw new Error("past the end");
      v = v * 2 + ((byte >> (7 - (this.at & 7))) & 1);
      this.at++;
    }
    return v;
  }
  skip(n: number) {
    if (this.at + n > this.bytes.length * 8) throw new Error("past the end");
    this.at += n;
  }
  /** Exp-Golomb, unsigned. */
  ue(): number {
    let zeros = 0;
    while (this.u(1) === 0) if (++zeros > 31) throw new Error("bad exp-Golomb");
    return 2 ** zeros - 1 + (zeros ? this.u(zeros) : 0);
  }
}

class Out {
  private parts: number[] = [];
  u8(...vs: number[]) {
    for (const v of vs) this.parts.push(v & 0xff);
  }
  u16(v: number) {
    this.u8(v >>> 8, v);
  }
  u32(v: number) {
    this.u8(v >>> 24, v >>> 16, v >>> 8, v);
  }
  nal(n: Uint8Array) {
    this.u16(n.length);
    for (const b of n) this.parts.push(b);
  }
  done() {
    return Uint8Array.from(this.parts);
  }
}
