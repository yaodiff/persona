// What the chat hands down to its rows without threading props through every step: the background tasks and their
// calls (to badge a call that went to the background and name a notice's call), the subagents (their cards), the
// workflow runs' calls (their cards), the items that came in live (their words fade in), and the key the open / closed
// state is kept under, and where the child sessions' cards go.
import { createContext, useContext } from "react";
import type { ChildPlace } from "../../lib/child-cards";
import type { ExternalOrigin } from "../../lib/conversation-origins";
import { emptyTaskIndex, type TaskIndex } from "../../lib/tasks";
import type { Subagent } from "../../lib/types";
import { NO_WORKFLOW_CALLS, type WorkflowCalls } from "../../lib/workflows";
import type { WorkLineStyle } from "../../lib/work-groups";

export interface ChatContextValue {
  tasks: TaskIndex;
  /** The session's subagents, by the tool_use_id of the call that launched them. */
  subagents: Subagent[];
  /** Prefixes the keys of what the reader opened (expanded.ts): one chat's groups do not open another's. */
  scope: string;
  /** Brings the call with this tool_use_id into view, opening the groups it is folded in (a background task's
   *  "Jump to the step that started it"). */
  jumpToCall?: (call: string) => void;
  /** The Workflow calls whose run the hub lists (#3434): they draw the run's card, and a running one stays out of the
   *  folded work. Only which runs there are, not their progress (each card reads that), so it seldom changes. */
  workflowCalls?: WorkflowCalls;
  /** Conversation identity; both styles share the collapsed work-line default (#2602). */
  lineStyle?: WorkLineStyle;
  /** What runs the chat's subagents, as its agent is called ("Claude Code", "Codex", a Paseo provider's label, #8142);
   *  unset: Claude Code. */
  harness?: string;
  /** The message an extension opened this conversation with and where it came from (#3758, `externalOpening`), with
   *  the extension that opened it (`openerOf`) and the conversation's manager, so its mark is the rows' (#10358). */
  opening?: { id: string; origin: ExternalOrigin; opened_by?: string; manager?: string };
  /** The exchange whose last words carried the closeout the card above the transcript shows (#3612): they are drawn
   *  without the raw block. Unset while no card is shown. */
  closeoutSegment?: string;
  /** The child sessions drawn as cards in this chat (#5120, lib/child-cards.ts), by the exchange each sits in. The same
   *  map while none moved: a child's own updates redraw only its card. */
  children?: ReadonlyMap<string, ChildPlace[]>;
  /** A long chat mounts its finished off-screen exchanges near the viewport instead of all at once. Live exchanges and
   *  the final two never get ChatView's `offscreen` watcher, so their immediate rendering path is unchanged. */
  windowHistory?: boolean;
  /** A deferred body just replaced its estimated placeholder. ChatView compensates it now and advances its observer
   *  baseline; the old height was read before that commit. */
  windowDrawn?: (section: HTMLElement, was: number) => void;
  /** The chat is drawn inside another page's panel (the Team panel's worker, ChatView `embedded`): what would open in
   *  the side panel opens in place instead, since that panel is the one it is drawn in (a workflow card, #5421). */
  embedded?: boolean;
}

export const ChatContext = createContext<ChatContextValue>({ tasks: emptyTaskIndex(), subagents: [], scope: "", workflowCalls: NO_WORKFLOW_CALLS });

/** Counts the live work lines' clocks on screen (+1 shown, -1 gone): while one ticks, the running bar above the
 *  composer leaves the time to it (#3534). Null outside a chat with that bar. */
export const LiveClockContext = createContext<((delta: 1 | -1) => void) | null>(null);

export function useChatContext() {
  return useContext(ChatContext);
}
