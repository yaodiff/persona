// The browser's side of the voice extension's socket (docs/assistant/voice.md 3: protocol 2). Pure data, as iOS's
// VoiceWire.swift: the frames' types, and what the first screen reads from a conversation's lines and runs. The frames
// themselves are read and written in frames.ts, which only a call loads (#3461).
import { CONTEXT_RE } from "../../lib/compatibility";
import type { Item } from "../../lib/types";

/** The conversation a call is bound to, as `start.target` sends it. */
export interface VoiceTarget {
  machine_id: string;
  session_id: string;
}

/** The bound conversation as `ready.target` describes it: fixed for the whole call. */
export interface VoiceTargetInfo {
  machineID: string;
  sessionID: string;
  kind?: string;
  title?: string;
  /** "Claude Code", "Codex": who the call hands things to. */
  agentLabel?: string;
  machineName?: string;
}

/** One line of words: `start.recent` (the conversation's last turns) and `start.history` (the call's captions so far). */
export interface VoiceLine {
  role: "user" | "assistant";
  text: string;
}

export const RECENT_TURNS = 3;
export const RECENT_LENGTH = 300;
/** At most this many lines of `recent`: an agent's turn can be a long run of short texts between tool calls. */
export const RECENT_LINES = 12;
/** How many of the call's captions a reconnect hands back (`history`). */
export const HISTORY_LINES = 40;

export const DEFAULT_INPUT_RATE = 16000;
export const DEFAULT_OUTPUT_RATE = 24000;

/** Text without the page's context blocks, its runs of blank space made one. */
export function cleanLine(raw: string): string {
  return raw.replace(new RegExp(CONTEXT_RE.source, "g"), " ").split(/\s+/).filter(Boolean).join(" ");
}

/** At most `limit` characters (code points), the last one an ellipsis when cut. */
export function cutLine(text: string, limit: number): string {
  const chars = Array.from(text);
  return chars.length <= limit ? text : chars.slice(0, limit - 1).join("") + "…";
}

/** The conversation's last three turns as `start.recent`: what the user and the agent wrote, nothing else (tool calls,
 *  thinking and notices stay out), context blocks taken out, each line cut to 300 characters. The extension fetches
 *  them itself when this is empty. */
export function recentLines(items: readonly Item[]): VoiceLine[] {
  const lines: VoiceLine[] = [];
  for (const item of items) {
    if (item.kind !== "text" || (item.role !== "user" && item.role !== "assistant")) continue;
    const text = cleanLine(item.text ?? "");
    if (!text) continue;
    lines.push({ role: item.role, text: cutLine(text, RECENT_LENGTH) });
  }
  const starts = lines.flatMap((l, i) => (l.role === "user" ? [i] : []));
  const from = starts.length > RECENT_TURNS ? starts[starts.length - RECENT_TURNS] : 0;
  return lines.slice(from).slice(-RECENT_LINES);
}

/** What a `run` frame says about one instruction the call handed to the conversation (docs/assistant/voice.md 3.3); a
 *  state this page does not know is kept as it came. */
export type VoiceRunState = "sending" | "sent" | "working" | "needs_you" | "blocked" | "held" | "done" | "failed" | "refused" | "unknown" | "folded" | (string & {});

const CLOSED_RUNS = new Set(["done", "failed", "refused", "unknown", "folded"]);

/** Not over yet: a reconnect hands it back (`resume`). `held` answered once and still works in the background; a state
 *  this page does not know is kept open too, so a result that comes later is still read out. */
export function runOpen(state: VoiceRunState): boolean {
  return !CLOSED_RUNS.has(state);
}

/** `started_at` as the extension wrote it (an RFC 3339 string or a number), echoed back unchanged in `resume`. */
export type VoiceStamp = string | number;

/** When it started: a number is Unix seconds, or milliseconds when it is that large. */
export function stampDate(stamp: VoiceStamp | undefined): number | undefined {
  if (stamp === undefined) return undefined;
  if (typeof stamp === "number") return stamp > 100_000_000_000 ? stamp : stamp * 1000;
  const ms = Date.parse(stamp);
  return Number.isFinite(ms) ? ms : undefined;
}

/** One instruction handed to the bound conversation, as the latest `run` frame for its id says. */
export interface VoiceRun {
  id: string;
  task: string;
  state: VoiceRunState;
  msgIDs: string[];
  /** The transcript's last seq before it was sent; moves on after a held answer. */
  baseSeq: number;
  startedAt?: VoiceStamp;
  /** Why it was refused or failed: dialog_open, input_draft, unprompted… */
  code?: string;
  message?: string;
  /** The words left unsent in the agent's input box on the computer (409 input_draft): the whole line, shown. */
  draft?: string;
  /** The hub's 409 `clearable` text: only when the node can clear that line, not always the same as `draft` (an IDE
   *  selection marker stays out of it). Clear and Send sends it back as `clear_draft` exactly (docs/assistant/voice.md 3.2). */
  clearable?: string;
  /** The extension keeps it for a resend (#5904): what the user-speech gate held back offers Send anyway only then (one
   *  longer than its quoted task, or from before it, is not kept). */
  resendable?: boolean;
}

export type VoiceAskState = "asking" | "answered" | "released" | (string & {});

/** Which card the call is asking about by voice (`ask` frame). */
export interface VoiceAsk {
  interactionID: string;
  state: VoiceAskState;
  by?: string;
}

/** The upstream model's connection as the extension reports it (`link` frame). */
export type VoiceLink = "live" | "paused" | "waking" | "reconnecting" | (string & {});

/** Explicit reply boundary emitted by protocol-2 extensions with the `reply` feature (docs/assistant/voice.md 3.3). */
export type VoiceReplyState = "start" | "end";

/** An `error` frame. `message` is the extension's (or the upstream's) own words: for Details, not the line on screen. */
export interface VoiceFailure {
  code?: string;
  message: string;
  /** The extension closes the socket after a fatal one: the call is over, nothing reconnects. */
  fatal: boolean;
}

/** The extension sends anyway what its user-speech gate held back (a resend of a run refused `unprompted`), and its
 *  `start` takes `speech_gate` (#5904, docs/assistant/voice.md 3.1). */
export const SPEECH_GATE_FEATURE = "speech_gate";

/** The `ready` frame: the extension is connected upstream and the rates are known. */
export interface VoiceReady {
  /** 1 when the frame does not say (an extension from before protocol 2). */
  protocol: number;
  features: string[];
  inputRate: number;
  outputRate: number;
  model?: string;
  voice?: string;
  target?: VoiceTargetInfo;
}

export interface VoiceTranscript {
  id: string;
  role: string;
  text: string;
  final: boolean;
  /** The line this one goes before, when it is new (protocol 2): the user's caption came after the voice began to
   *  answer it. Absent: at the end. */
  before?: string;
}

export type VoiceFrame =
  | { type: "ready"; ready: VoiceReady }
  | { type: "clear" }
  | { type: "transcript"; transcript: VoiceTranscript }
  /** A read-only function tool at work. */
  | { type: "tool"; id: string; label: string; done: boolean }
  | { type: "run"; run: VoiceRun }
  | { type: "ask"; ask: VoiceAsk }
  | { type: "link"; state: VoiceLink }
  | { type: "reply"; state: VoiceReplyState }
  | { type: "error"; failure: VoiceFailure }
  | { type: "other"; name: string };

/** The `start` frame: the call's first, and again after every reconnect. */
export interface VoiceStart {
  model?: string;
  voice?: string;
  target: VoiceTarget;
  recent?: VoiceLine[];
  /** The call's captions so far: only when it picks up again. */
  history?: VoiceLine[];
  /** The runs not over yet, as last reported: only when it picks up again. */
  resume?: VoiceRun[];
  /** False: the microphone records without echo cancellation, so a caption of the user's may be the voice's own. */
  echoCancellation?: boolean;
  /** The user-speech gate on (true) or off (false) for the call (#5904, docs/assistant/voice.md 3.2). Saying it also tells
   *  the extension this page has Send anyway; a page from before it says nothing, and an older extension ignores it. */
  speechGate?: boolean;
}

/** Extension ids are a-z, 0-9, `_` and `-` (docs/extensions/extensions.md): anything else must not become a path. */
export function validExtensionID(id: string): boolean {
  return /^[a-z0-9_-]+$/.test(id);
}
