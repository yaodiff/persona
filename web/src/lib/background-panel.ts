// An Agent's Background tasks side panel (#5421, after cindy's right-sidebar Background tasks tab, `openBackgroundTasksTab`;
// docs/web/web.md "Background tasks"): what keeps the Agent going besides its turn, its /loop, background commands and
// subagents, and its workflow runs. Every way in goes through here: the header's button, the loop mark before the title, a
// workflow card's "Open in the background tasks panel" and `/workflows` in the composer.
import { useSyncExternalStore } from "react";
import { getPanel, openPanel, type Panel } from "./panel";
import type { Session } from "./types";
import { otherBackground, recentWorkflows } from "./workflows";
import { byTime } from "./times";

export type BackgroundPanel = Extract<Panel, { kind: "background" }>;

/** The open panel when it is this session's Background tasks panel. */
export function backgroundPanelOf(p: Panel | null, mid: string, sid: string): BackgroundPanel | undefined {
  return p?.kind === "background" && p.mid === mid && p.sid === sid ? p : undefined;
}

/** Opens the session's panel, at a workflow run's detail when `run` names one (cindy's focusTaskId). Asked again for the
 *  same run, it is a new object, so the panel goes to that run again. */
export function openBackgroundPanel(mid: string, sid: string, run?: string) {
  openPanel({ kind: "background", mid, sid, ...(run ? { run } : {}) });
}

/** The header's button: closes the session's panel when it is open, else opens it at its list. */
export function toggleBackgroundPanel(mid: string, sid: string) {
  if (backgroundPanelOf(getPanel(), mid, sid)) openPanel(null);
  else openPanel({ kind: "background", mid, sid }, { push: false });
}

/** Whether the session has anything the panel lists now: a /loop, a background task, or a workflow run that runs or ended
 *  within the day the node keeps it (lib/workflows.ts recentWorkflows). The header's button shows only then. */
export function hasBackgroundWork(s: Session | undefined, now: number): boolean {
  if (!s) return false;
  return Boolean(s.loop) || otherBackground(s).length > 0 || (s.background_ended?.length ?? 0) > 0 || recentWorkflows(s, now).length > 0;
}

/** The composer's `/workflows` (cindy's): opens the panel instead of going to the Agent. */
export const WORKFLOWS_COMMAND = "/workflows";

/** A message that is `/workflows`: the word alone, no pictures. */
export function isWorkflowsCommand(text: string, images: number): boolean {
  return images === 0 && text.trim().toLowerCase() === WORKFLOWS_COMMAND;
}

/** The run `/workflows` opens at (cindy's findLatestWorkflowTask): the latest running one, else the latest that started,
 *  among those the panel lists (recentWorkflows: a run that ended over a day ago is not one of them). */
export function latestWorkflowRun(s: Session | undefined, now = Date.now()): string | undefined {
  const runs = recentWorkflows(s, now);
  const pick = (list: typeof runs) => list.slice().sort((a, b) => byTime(b.started_at, a.started_at))[0];
  return (pick(runs.filter((w) => w.running)) ?? pick(runs))?.task_id;
}

/** What only the chat page can do for its panel: stop the /loop (a message the page sends, in its outbox) and bring a
 *  workflow's launching call into view in the chat. The page provides them while it shows the session (ChatView). */
export interface BackgroundChat {
  stopLoop: () => void;
  jumpToCall?: (call: string) => void;
  /** Whether the parent chat currently holds a launching tool call or placed child card for `call`. */
  hasCall?: (call: string) => boolean;
}

const chats = new Map<string, BackgroundChat[]>();
const chatKey = (mid: string, sid: string) => `${mid}/${sid}`;
const watchers = new Set<() => void>();
const changed = () => watchers.forEach((w) => w());

/** The chat page showing the session provides its actions; the function returned takes them back. The same session
 *  drawn twice (a page and an embedded copy) has two: the one that came last answers. */
export function provideBackgroundChat(mid: string, sid: string, chat: BackgroundChat): () => void {
  const k = chatKey(mid, sid);
  chats.set(k, [...(chats.get(k) ?? []), chat]);
  changed();
  return () => {
    const left = (chats.get(k) ?? []).filter((c) => c !== chat);
    if (left.length) chats.set(k, left);
    else chats.delete(k);
    changed();
  };
}

/** The actions of the chat page showing the session, if one does. */
export function backgroundChat(mid: string, sid: string): BackgroundChat | undefined {
  const list = chats.get(chatKey(mid, sid));
  return list?.[list.length - 1];
}

/** The actions of the chat page showing the session, kept current: the panel redraws when a page comes or goes, so Stop
 *  and "Show in chat" are offered only while there is a page to do them (none: undefined). */
export function useBackgroundChat(mid: string, sid: string): BackgroundChat | undefined {
  const read = () => backgroundChat(mid, sid);
  return useSyncExternalStore(
    (fn) => {
      watchers.add(fn);
      return () => void watchers.delete(fn);
    },
    read,
    read,
  );
}
