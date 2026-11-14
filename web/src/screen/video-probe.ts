// Which video codecs this browser can decode for the live screen (#6558, docs/extensions/computer-use.md "Live screen
// video (#3954)"), asked once a page: never guessed from the user agent. WebCodecs exists only in a secure context
// (https, or localhost), so a hub opened over plain http on the LAN has no decoder at all. HEVC counts only with a
// hardware decoder (there is no reasonable software one, and on a phone software decoding would cost the battery);
// H.264 counts either way. The answer is `start.codecs`, HEVC first; the helper picks the first it can encode.

export type VideoCodec = "hevc" | "h264";
/** Why this page plays no video although the computer offers it: not a secure address, or no codec decodes here. */
export type NoVideo = "insecure" | "unsupported";
export type Probe = { codecs: VideoCodec[]; why?: NoVideo };

type Decoders = { isConfigSupported(config: VideoDecoderConfig): Promise<{ supported?: boolean }> };
export type ProbeEnv = { secure: boolean; decoders?: Decoders };

const defaultEnv = (): ProbeEnv => ({
  secure: typeof isSecureContext === "boolean" ? isSecureContext : false,
  decoders: typeof VideoDecoder === "undefined" ? undefined : VideoDecoder,
});

/** What each codec is asked as: Main / High at level 5.1, which covers what the helper sends (up to 4K). */
const ASK: Record<VideoCodec, Omit<VideoDecoderConfig, "codedWidth" | "codedHeight">> = {
  hevc: { codec: "hvc1.1.6.L153.B0", hardwareAcceleration: "prefer-hardware", optimizeForLatency: true },
  h264: { codec: "avc1.640033", hardwareAcceleration: "no-preference", optimizeForLatency: true },
};

/** The codecs this browser decodes at `width` × `height`, HEVC first, or why there are none. */
export async function probeCodecs(width: number, height: number, env: ProbeEnv = defaultEnv()): Promise<Probe> {
  if (!env.secure || !env.decoders) return { codecs: [], why: env.secure ? "unsupported" : "insecure" };
  const codecs: VideoCodec[] = [];
  for (const c of ["hevc", "h264"] as const) {
    try {
      const { supported } = await env.decoders.isConfigSupported({ ...ASK[c], codedWidth: width, codedHeight: height });
      if (supported) codecs.push(c);
    } catch {
      // a config this browser rejects outright: that codec isn't offered
    }
  }
  return codecs.length ? { codecs } : { codecs, why: "unsupported" };
}

/** The decoder configuration options for a codec the stream turned out to use, as the probe asked for it. */
export const decodeOptions = (codec: string): Pick<VideoDecoderConfig, "hardwareAcceleration" | "optimizeForLatency"> =>
  codec === "hevc" ? { hardwareAcceleration: "prefer-hardware", optimizeForLatency: true } : { hardwareAcceleration: "no-preference", optimizeForLatency: true };
