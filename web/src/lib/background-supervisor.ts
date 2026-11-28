// The background supervisor (#4927, #5082, docs/assistant/unified-entry.md "Background supervisor"): the hub's built-in
// conversation that follows up the tasks no leader supervises. The hub names it in GET /api/v1/assistant
// (`background_supervisor`, its session id once it was made); it is listed with the scheduled tasks' conversations,
// never as a direction or among the user's recent chats, and it is not renamed, archived, ended or made a direction.
// The hub makes it on first use, and replaces one that is gone, while the page is open: the page learns its id by
// reading the assistant again, as for any conversation the held answer does not list (lib/conversation-origins.ts
// unlistedConversations). Pure functions, tested without a DOM.
import { t } from "../i18n";
import type { FeedItem } from "./feed";
import { sameManager } from "./manager-choice";
import { supervisorHeld } from "./supervisor-held";
import type { AssistantInfo, Interaction, Session } from "./types";

/** `by` on a review's chat-activity entry the background supervisor made (#5084, docs/protocol/protocol.md, FeedItem "review"). */
export const REVIEW_BY_BACKGROUND = "background";

/** The purpose its conversation runs under (docs/assistant/model-config.md); it follows Main chat until it has its own. */
export const SUPERVISE_PURPOSE = "assistant.supervise";

/** Conversation `id` is the background supervisor. Never the main conversation; false on a hub before it, or before it was made. */
export function isBackgroundSupervisor(assistant: Pick<AssistantInfo, "background_supervisor" | "main"> | null | undefined, id: string | undefined): boolean {
  return !!id && !!assistant?.background_supervisor && assistant.background_supervisor === id && id !== assistant.main;
}

/** Whether `ix` is a user-facing card raised by the hidden background supervisor conversation (or its folded children)
 *  rather than an agent permission still held by a supervisor for internal review (`supervisorHeld`). */
export function isBackgroundSupervisorInteraction(
  assistant: Pick<AssistantInfo, "background_supervisor" | "main"> | null | undefined,
  ix: Pick<Interaction, "session_id" | "folded_under" | "folded_side" | "kind" | "delegating" | "manager">,
  manager?: string,
): boolean {
  const bg = assistant?.background_supervisor;
  if (!bg || bg === assistant?.main || supervisorHeld(ix)) return false;
  if (manager && !sameManager(ix.manager, manager)) return false;
  return ix.session_id === bg || ix.folded_under === bg || ix.folded_side === bg;
}

const mainCopies = new WeakMap<Interaction, { mainSid: string; mainMid?: string; copy: Interaction }>();

/** Wraps a background supervisor's own card so Main chat shows it cleanly above the composer with localized
 *  attribution; child cards already folded under another parent keep their own child attribution unchanged. */
export function asBackgroundSupervisorCard(ix: Interaction, mainSid: string, mainMid?: string): Interaction {
  if (ix.folded_under) return ix;
  const cached = mainCopies.get(ix);
  if (cached && cached.mainSid === mainSid && cached.mainMid === mainMid) return cached.copy;
  const copy: Interaction = {
    ...ix,
    folded_under: mainSid,
    folded_under_machine_id: mainMid && ix.machine_id !== mainMid ? mainMid : undefined,
    session_title: backgroundSupervisorTitle({ title: ix.session_title ?? "" }),
  };
  mainCopies.set(ix, { mainSid, mainMid, copy });
  return copy;
}

/** What it is called: the hub's title (it names it in the owner's language), else this page's own name for it. */
export function backgroundSupervisorTitle(s?: Pick<Session, "title">): string {
  const title = s?.title?.trim();
  return !title || title === "Claude Code" || title === "assistant" ? t("assistant-conversations.background_supervisor") : title;
}

/** The review entry is one the background supervisor made: the main chat took no turn for it, so the chat shows its
 *  line. Any other `by` (a value this page doesn't know) is not: the entry stays undrawn, as before. */
export function reviewedInBackground(item: Pick<FeedItem, "kind" | "body">): boolean {
  return item.kind === "review" && item.body?.by === REVIEW_BY_BACKGROUND;
}
