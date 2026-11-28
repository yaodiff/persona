// What a card's open action may open (CheckAssistantOpen) and the strict <persona-action> tag the hub ends a card's
// reply with, apart from assistant-blocks.ts's block parsing and card checks: the first screen's chat reads only these.
import { APP_SCHEMES } from "./compatibility";

export const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

// ── open targets (CheckAssistantOpen) ──

export type OpenTarget =
  | { kind: "web"; url: string }
  | { kind: "session"; mid: string; sid: string }
  | { kind: "task"; id: string }
  | { kind: "kb"; path: string[] }
  | { kind: "file"; mid: string; path: string };

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

/** What an open action opens, or undefined when it is not one the apps open (never handed to the system). */
export function openTarget(target: string): OpenTarget | undefined {
  if (CONTROL.test(target) || target.includes("\\")) return undefined;
  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(target)?.[1]?.toLowerCase();
  if (scheme === "https") {
    let u: URL;
    try {
      u = new URL(target);
    } catch {
      return undefined;
    }
    return u.username || u.password || !u.hostname ? undefined : { kind: "web", url: u.href };
  }
  if (!scheme || !APP_SCHEMES.has(scheme)) return undefined;
  const m = /^[a-z]+:\/\/([^/?#]*)(\/[^?#]*)?$/i.exec(target);
  if (!m || /[@:]/.test(m[1])) return undefined;
  const host = m[1].toLowerCase();
  const parts = (m[2] ?? "").replace(/^\//, "").split("/");
  const decoded: string[] = [];
  for (const p of parts) {
    let v: string;
    try {
      v = decodeURIComponent(p);
    } catch {
      return undefined;
    }
    if (!v || CONTROL.test(v) || v.includes("\\")) return undefined;
    if (host !== "file" && v.includes("/")) return undefined;
    decoded.push(v);
  }
  switch (host) {
    case "session":
      return decoded.length === 2 ? { kind: "session", mid: decoded[0], sid: decoded[1] } : undefined;
    case "task":
      return decoded.length === 1 ? { kind: "task", id: decoded[0] } : undefined;
    case "kb":
      return decoded.some((p) => p === "." || p === "..") ? undefined : { kind: "kb", path: decoded };
    case "file":
      return decoded.length === 2 && !decoded[0].includes("/") && decoded[1].startsWith("/") ? { kind: "file", mid: decoded[0], path: decoded[1] } : undefined;
  }
  return undefined;
}

// ── the tag a card's reply ends with ──

export interface CardReply {
  /** What the message says before the tag (the reply and the form's lines). */
  text: string;
  view: string;
  action: string;
  values: Record<string, string>;
}

const unescapeXML = (s: string) =>
  s.replace(/&(lt|gt|amp|quot|#34|#39|apos);/g, (_, e: string) => ({ lt: "<", gt: ">", amp: "&", quot: '"', "#34": '"', "#39": "'", apos: "'" })[e] ?? "");

// Exactly what the hub writes (viewReplyMessage, and an HTML card's answer): a last line, nothing after it, attributes
// XML-escaped, a JSON object whose keys are values (string → string) and, for an HTML card, html: true.
const TAG = /\n<persona-action view="([^"<>]*)" action="([^"<>]*)">(\{[^<>]*\})<\/persona-action>$/;

/** A message sent from a card: its text without the tag and what was answered; undefined for any other message. */
export function cardReply(message: string): CardReply | undefined {
  const m = TAG.exec(message);
  if (!m) return undefined;
  let payload: unknown;
  try {
    payload = JSON.parse(m[3]);
  } catch {
    return undefined;
  }
  // values, string → string; an HTML card's answer adds html: true and sends no values (view_actions.go, #6281)
  if (!isObject(payload) || Object.keys(payload).some((k) => k !== "values" && k !== "html") || ("html" in payload && payload.html !== true)) return undefined;
  const values = payload.values ?? {};
  if (!isObject(values) || Object.values(values).some((v) => typeof v !== "string")) return undefined;
  const view = unescapeXML(m[1]);
  const action = unescapeXML(m[2]);
  if (!view || !action) return undefined;
  return { text: message.slice(0, m.index), view, action, values: values as Record<string, string> };
}
