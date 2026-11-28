// Safe "Compact and send again" recovery for image-limit turn failures (#13045):
// - Persona assistant conversations trigger `assistant_compact` (POST .../compact), wait for session.compaction to
//   settle, and resend the failed message only if compaction succeeded.
// - Terminal Claude Code sessions send `/compact`, wait for a post-command compaction boundary after the session
//   transitions through working back to idle (with no post-command error), and resend only if compaction succeeded.
// - Original user payload (raw text including contexts/files/attachments + full text + image bytes) is captured before
//   compaction; if original content/image bytes cannot be recovered, automatic resend is withheld rather than silently
//   dropping attachments or degrading to a generic prompt.
// - Sessions that do not support compaction (non-Claude terminals, runtimes, botmux, offline, read-only, or hubs
//   without `assistant_compact`) are not offered a broken action.
import { IMAGE_LIMIT_CODE, isRetryText } from "./api-errors";
import { CONTEXT_RE } from "./compatibility";
import { sessionCan, activityOf } from "./format";
import { isInterjectContext, tagAttrs, withoutViewErrors, type ChatLine, type UserMessage } from "./turns";
import type { Item, Session } from "./types";

export type CompactResendMode = "assistant" | "claude-terminal";

export interface CompactResendSupport {
  supported: boolean;
  mode: CompactResendMode | null;
}

export interface CompactResendOptions {
  assistant?: boolean;
  hubSupportsAssistantCompact?: boolean;
  blocked?: boolean;
  offline?: boolean;
  composerDisabled?: boolean;
  routed?: boolean;
  working?: boolean;
  outgoing?: boolean;
  stopped?: boolean;
}

export interface OutgoingPayload {
  text: string;
  images: { mime: string; data: string }[];
}

export interface FailedTurnCandidate {
  user: UserMessage;
  captured?: OutgoingPayload;
}

export interface FailedTurnOptions {
  /** `client_msg_id` values of internal `/compact` commands sent by this recovery flow. */
  internalCompactIds?: ReadonlySet<string>;
  /** Recently delivered outgoing payloads keyed by `client_msg_id`, preserving exact text + image bytes. */
  captured?: ReadonlyMap<string, OutgoingPayload>;
}

export type ResolvedPayload =
  | { ok: true; payload: OutgoingPayload }
  | { ok: false; reason: "empty" | "images_unavailable" | "text_unavailable" };

export interface ResolvePayloadDeps {
  fetchFullItem?: (mid: string, sid: string, itemId: string) => Promise<Item>;
  fetchImage?: (mid: string, sid: string, item: Item) => Promise<{ mime: string; data: string } | null>;
}

/** Whether a turn's error line (or session turn_error_code) is an image-limit failure (#13045). */
export function isImageLimitError(lineCode: string | undefined, sessionCode: string | undefined): boolean {
  return lineCode === IMAGE_LIMIT_CODE || sessionCode === IMAGE_LIMIT_CODE;
}

/**
 * Determines whether "Compact and send again" can be safely offered for `session`.
 * - Blocked (setup gate), offline/manager-offline, routed to a task, currently working/sending, stopped, or unable to send: never offered.
 * - Persona assistant conversations: requires hub `assistant_compact`.
 * - Agent chats: live terminal Claude Code sessions only (`!source_kind`, Claude agent, not botmux, not offline/needs_you).
 */
export function compactResendSupport(session: Session | undefined, opts: CompactResendOptions): CompactResendSupport {
  if (!session || opts.blocked || opts.offline || opts.routed || opts.working || opts.outgoing || opts.stopped) {
    return { supported: false, mode: null };
  }
  if (!sessionCan(session, "send")) return { supported: false, mode: null };
  const isAssistant = Boolean(opts.assistant || session.assistant);
  if (isAssistant) {
    if (!opts.hubSupportsAssistantCompact) return { supported: false, mode: null };
    return { supported: true, mode: "assistant" };
  }
  if (opts.composerDisabled) return { supported: false, mode: null };
  if (session.source_kind) return { supported: false, mode: null };
  if (session.host === "botmux") return { supported: false, mode: null };
  if (session.agent && session.agent !== "claude") return { supported: false, mode: null };
  const st = activityOf(session);
  if (st === "offline" || st === "needs_you" || st === "working") return { supported: false, mode: null };
  return { supported: true, mode: "claude-terminal" };
}

/**
 * Strips hub-only wrappers (`<persona-view-errors>`, realtime snapshot `<persona-context>`, `skill-instructions`,
 * `message-source`, interject note) while keeping all user-supplied rich content (`<persona-context>` quotes/edits/
 * forwards, `<persona-file>`, `<pasted_content>`, `<attached_images>`, slash commands, and plain text) verbatim.
 */
export function cleanRawUserText(raw: string, stripImagePlaceholders: boolean): string {
  let out = withoutViewErrors(raw).replace(CONTEXT_RE, (whole, attrs: string) => {
    if (!attrs.trim()) return ""; // hub's realtime snapshot, re-prepended on send
    const { kind, title } = tagAttrs(attrs);
    if (kind === "skill-instructions" || kind === "message-source" || isInterjectContext({ kind, title })) return "";
    return whole;
  });
  if (stripImagePlaceholders) out = out.replace(/\[Image #\d+\]\s?/g, "");
  return out.trim();
}

export function userTextItems(user: UserMessage): Item[] {
  return (user.rawInput ?? []).filter((it) => it.kind === "text");
}

export function userImageItems(user: UserMessage): Item[] {
  const rawImgs = (user.rawInput ?? []).filter((it) => it.kind === "image");
  return rawImgs.length ? rawImgs : user.images;
}

function isPlainRetryOnly(user: UserMessage, hasStepsOrAnswer: boolean): boolean {
  if (hasStepsOrAnswer || user.injected) return false;
  if (user.images.length || user.files?.length || user.photos?.length || user.contexts.length) return false;
  const text = user.text.trim();
  return Boolean(text && isRetryText(text));
}

/**
 * Finds the immediately preceding non-injected user message for `tailErrorId`.
 * - Preserves legitimate user slash commands (including `/clear`, `/review`, or a user-typed `/compact`).
 * - Skips only internal `/compact` commands tracked in `opts.internalCompactIds` and folded bare retry-only turns.
 * - Stops at injected messages or non-segment boundaries rather than walking into unrelated earlier history.
 */
export function failedTurnToResend(
  lines: readonly ChatLine[],
  tailErrorId: string,
  opts: FailedTurnOptions = {},
): FailedTurnCandidate | null {
  const idx = lines.findIndex((l) => l.id === tailErrorId);
  const start = idx >= 0 ? idx - 1 : lines.length - 1;
  for (let i = start; i >= 0; i--) {
    const line = lines[i];
    if (line.kind === "apiError") continue;
    if (line.kind !== "segment") return null;
    const user = line.seg.user;
    if (!user || user.injected) return null;
    if (user.client_msg_id && opts.internalCompactIds?.has(user.client_msg_id)) continue;
    if (isPlainRetryOnly(user, Boolean(line.seg.steps.length || line.seg.answer))) continue;
    const captured = user.client_msg_id ? opts.captured?.get(user.client_msg_id) : undefined;
    return { user, ...(captured ? { captured } : {}) };
  }
  return null;
}

/**
 * Static check before offering "Compact and send again": returns false when no failed user message exists or when any
 * attached image item has neither inline bytes nor a lazy image endpoint to recover it from.
 */
export function canResendFailedTurn(candidate: FailedTurnCandidate | null): boolean {
  if (!candidate) return false;
  if (candidate.captured) {
    return Boolean(candidate.captured.text.trim() || candidate.captured.images.length);
  }
  const { user } = candidate;
  const images = userImageItems(user);
  for (const img of images) {
    const hasBytes = Boolean(img.image?.data);
    const hasLazyFetch = Boolean(img.image?.lazy && img.id);
    if (!hasBytes && !hasLazyFetch) return false;
  }
  if (images.length > 0) return true;
  const texts = userTextItems(user);
  if (texts.some((it) => Boolean(it.full_size))) return true;
  if (texts.length > 0) return texts.some((it) => Boolean(cleanRawUserText(it.text ?? "", false)));
  return Boolean(user.text.trim());
}

/** Resolves the original text and attachments when the user tries again. */
export async function resolveFailedTurnPayload(
  candidate: FailedTurnCandidate | null,
  mid: string,
  sid: string,
  deps?: ResolvePayloadDeps,
  signal?: AbortSignal,
): Promise<ResolvedPayload> {
  const { resolvePayload } = await import("./resend-payload");
  return resolvePayload(candidate, mid, sid, deps, signal);
}
