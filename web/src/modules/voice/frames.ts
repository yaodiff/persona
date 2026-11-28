// The voice extension's socket frames, read and written (docs/assistant/voice.md 3: protocol 2; their types are in
// wire.ts). As iOS's VoiceWire.swift: an unknown frame comes back as `other`, a missing or odd field falls back to its
// default, so a newer extension never breaks an older page. Only a call uses them, so they load with its code
// (call-stack.ts, #3461), not with the first screen.
import {
  DEFAULT_INPUT_RATE, DEFAULT_OUTPUT_RATE,
  type VoiceFrame, type VoiceStart, type VoiceTargetInfo,
} from "./wire";

const str = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);
const nonEmpty = (v: unknown): string | undefined => {
  const s = str(v);
  return s ? s : undefined;
};
/** A whole number; anything else (a fraction, a string, too large) takes the default. */
const whole = (v: unknown): number | undefined => (typeof v === "number" && Number.isSafeInteger(v) ? v : undefined);
/** A rate must be a positive number below a million. */
const rate = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) && v > 0 && v < 1_000_000 ? Math.floor(v) : undefined);

function target(v: unknown): VoiceTargetInfo | undefined {
  if (!v || typeof v !== "object") return undefined;
  const t = v as Record<string, unknown>;
  const machineID = str(t.machine_id), sessionID = str(t.session_id);
  if (machineID === undefined || sessionID === undefined) return undefined;
  return { machineID, sessionID, kind: str(t.kind), title: str(t.title), agentLabel: str(t.agent_label), machineName: str(t.machine_name) };
}

/** One text frame from the extension; null when it is not a JSON object with a string "type". */
export function parseFrame(text: string): VoiceFrame | null {
  let o: Record<string, unknown>;
  try {
    const v: unknown = JSON.parse(text);
    if (!v || typeof v !== "object" || Array.isArray(v)) return null;
    o = v as Record<string, unknown>;
  } catch {
    return null;
  }
  const type = str(o.type);
  if (type === undefined) return null;
  switch (type) {
    case "ready": {
      const protocol = whole(o.protocol);
      return {
        type,
        ready: {
          protocol: protocol !== undefined && protocol > 0 ? protocol : 1,
          features: Array.isArray(o.features) ? o.features.filter((f): f is string => typeof f === "string") : [],
          inputRate: rate(o.input_rate) ?? DEFAULT_INPUT_RATE,
          outputRate: rate(o.output_rate) ?? DEFAULT_OUTPUT_RATE,
          model: str(o.model),
          voice: str(o.voice),
          target: target(o.target),
        },
      };
    }
    case "clear":
      return { type };
    case "transcript":
      return { type, transcript: { id: str(o.id) ?? "", role: str(o.role) ?? "", text: str(o.text) ?? "", final: o.final === true, before: nonEmpty(o.before) } };
    case "tool":
      return { type, id: str(o.id) ?? "", label: str(o.label) ?? "", done: o.done === true };
    case "run": {
      const id = nonEmpty(o.id);
      if (!id) return { type: "other", name: type };
      const started = o.started_at;
      return {
        type,
        run: {
          id,
          task: str(o.task) ?? "",
          state: str(o.state) ?? "",
          msgIDs: Array.isArray(o.msg_ids) ? o.msg_ids.filter((m): m is string => typeof m === "string") : [],
          baseSeq: whole(o.base_seq) ?? 0,
          startedAt: (typeof started === "string" && started) || (typeof started === "number" && Number.isFinite(started)) ? started : undefined,
          code: str(o.code),
          message: str(o.message),
          draft: str(o.draft),
          clearable: nonEmpty(o.clearable),
          resendable: o.resendable === true ? true : undefined,
        },
      };
    }
    case "ask": {
      const id = nonEmpty(o.interaction_id);
      if (!id) return { type: "other", name: type };
      return { type, ask: { interactionID: id, state: str(o.state) ?? "", by: str(o.by) } };
    }
    case "link":
      return { type, state: str(o.state) ?? "" };
    case "reply": {
      const state = str(o.state);
      return state === "start" || state === "end" ? { type, state } : { type: "other", name: type };
    }
    case "error":
      return { type, failure: { code: nonEmpty(o.code), message: str(o.message) ?? "", fatal: o.fatal === true } };
    default:
      return { type: "other", name: type };
  }
}

/** Text frames the page sends. Keys in a fixed order, absent fields left out. */
export function startFrame(s: VoiceStart): string {
  const frame: Record<string, unknown> = { type: "start", protocol: 2 };
  if (s.model) frame.model = s.model;
  if (s.voice) frame.voice = s.voice;
  frame.target = { machine_id: s.target.machine_id, session_id: s.target.session_id };
  if (s.recent?.length) frame.recent = s.recent;
  if (s.history?.length) frame.history = s.history;
  if (s.resume?.length) {
    frame.resume = {
      runs: s.resume.map((r) => ({
        id: r.id, msg_ids: r.msgIDs, task: r.task, base_seq: r.baseSeq,
        // returned unchanged: a string stays a string, a number a number
        ...(r.startedAt !== undefined ? { started_at: r.startedAt } : {}),
        state: r.state,
      })),
    };
  }
  if (s.echoCancellation !== undefined) frame.echo_cancellation = s.echoCancellation;
  if (s.speechGate !== undefined) frame.speech_gate = s.speechGate;
  return JSON.stringify(frame);
}

/** Stops what the voice is saying (never the agent). */
export const INTERRUPT_FRAME = JSON.stringify({ type: "interrupt" });

/** Says a sentence by typing it: the same as speaking it. */
export function textFrame(text: string): string {
  return JSON.stringify({ type: "text", text });
}

/** Sends again a run a 409 refused, under the same id. `clearDraft`: the line in the agent's input box the node is to
 *  clear first (`run.clearable`, checked against what is there). */
export function resendFrame(id: string, clearDraft?: string): string {
  return JSON.stringify(clearDraft ? { type: "resend", id, clear_draft: clearDraft } : { type: "resend", id });
}

/** The call's socket: the extension's `/ws` under the hub's extension routes, on this page's own origin and sub-path,
 *  with the browser's token in the query (a browser cannot set a header on a WebSocket; the hub takes `?token=` and
 *  strips it before forwarding, as it does for /api/v1/ws). */
export function voiceSocketURL(loc: { protocol: string; host: string }, base: string, root: string, extensionID: string, token: string): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${base}/api/v1${root}/${encodeURIComponent(extensionID)}/ws?token=${encodeURIComponent(token)}`;
}
