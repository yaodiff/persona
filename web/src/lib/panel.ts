// The page's right-hand panel (docs/web/web.md "Side panel"): a session's files / Git, a link opened in the page, or an
// Agent or task opened from a conversation (lib/agent-open.ts). A session's terminal is the page's own view now
// (lib/session-view.ts), not a panel.
// One panel at a time: opening from outside replaces it and clears the back stack; drilling in from inside the active
// side panel pushes the previous panel onto a bounded back stack. Session panels close when the page moves to another
// conversation; a link, a task and an Agent stay until closed (they are not tied to the conversation they came from).
import { createContext, useContext, useSyncExternalStore } from "react";
import type { DiffScope } from "./comments";
import { followSubagentLink } from "./subagent-link";

/** Maximum number of previous panels kept in the side-panel back stack. */
export const MAX_PANEL_HISTORY = 8;

const PANEL_SEL = "[data-side-panel]";
const TRIGGER_EVENTS = ["click", "keydown", "submit"] as const;

export interface OpenPanelOptions {
  /** Push the current panel onto the back stack (`true`), clear it (`false`), or auto-detect from the trigger (default). */
  push?: boolean;
  /** Replace the current panel in place without pushing or clearing the existing back stack. */
  replace?: boolean;
}

/** Provides `popPanel` to headers rendered inside `<SidePanel />` when the back stack is non-empty. */
export const SidePanelBackContext = createContext<(() => void) | undefined>(undefined);

export function useSidePanelBack(): (() => void) | undefined {
  return useContext(SidePanelBackContext);
}

/** Forces machine-file links and cards inside a read-only archive view to open read-only file panels. */
export const ReadOnlyFileContext = createContext(false);

export function useReadOnlyFile(): boolean {
  return useContext(ReadOnlyFileContext);
}

/** A diff opened straight at a line (a comment's card, lib/comments.ts): which diff, which file, which line. */
export interface GitSpot {
  /** The repository-relative path git reports. */
  path: string;
  diff: DiffScope;
  line: number;
  /** The last line of the range, when it is one. */
  to?: number;
  /** The line number is the old file's. */
  old?: boolean;
}

/** Where a review opens (Panel "review" `at`). */
export interface ReviewSpot {
  path: string;
  line?: number;
  /** The line number is the old file's. */
  old?: boolean;
  /** A changed-files card's file: when the review does not list it (or cannot be read), the file itself opens in the
   *  side panel instead of a review with nothing to show for it. */
  file?: boolean;
}

export type Panel =
  /** `path`: the folder to open at; `file`: a file open in it (a panel brought back, lib/panel-memory.ts); `highlight`: a name
   *  in the folder to mark (the viewer's Show in Files, #5827). */
  | { kind: "files"; mid: string; sid: string; cwd: string; path?: string; file?: string; highlight?: string }
  | { kind: "git"; mid: string; sid: string; cwd: string; at?: GitSpot }
  /** Reviewing the session's changes (#151): `scope` to open at, `at` a comment's file and line to go to, or a changed
   *  file to scroll to (`path` from the repository's root, or a full path inside it; no `line`). */
  | { kind: "review"; mid: string; sid: string; cwd: string; scope?: "uncommitted" | "branch" | "commit" | "turn"; base?: string; target?: string; live?: boolean; at?: ReviewSpot }
  /** The transcript's own edits, by exchange or session: `turn` scopes the exchange and `at` selects a file / call. */
  | { kind: "changes"; mid: string; sid: string; turn?: string; at?: { path: string; call?: string } }
  /** `sid`: the session whose message box takes the comments made in it (else the one last used). `readOnly`: suppress
   *  edit and Show-in-Files write entries (for read-only archive views such as disabled Health data). */
  | { kind: "file"; mid: string; path: string; cwd?: string; line?: number; to?: number; sid?: string; readOnly?: boolean; navigationId?: string }
  /** `manager`: the chat's it was opened from, which reads it (none: the default's). */
  | { kind: "link"; url: string; title?: string; manager?: string }
  /** A task of a conversation, opened from it while Agents open in the side panel (lib/agent-open.ts, #5424): its
   *  Agent's chat (docs/assistant/unified-entry.md "Task details"). */
  | { kind: "task"; todo: string }
  | { kind: "conversation-tasks"; mid: string; sid: string; manager: string }
  /** An Agent opened from a conversation while Agents open in the side panel (#5424), one no task stands for (#1113)
   *  included: its chat. */
  | { kind: "agent"; mid: string; sid: string }
  /** A lead's Team (#5180, lib/team.ts): its workers' strip and the conversation of `worker` (else the one lib/team.ts
   *  shownWorker picks). `sid` is the lead's: the panel stays while the page shows the lead. */
  | { kind: "team"; mid: string; sid: string; worker?: string; at?: string }
  /** A session's side sessions (#7944, lib/side-panel.ts): the Team panel's strip of them and the conversation of `side`
   *  (else the one lib/side-panel.ts shownSide picks), landed at `at` (a search hit's message). `sid` is the parent's: the
   *  panel stays while the page shows the parent. */
  | { kind: "sides"; mid: string; sid: string; side?: string; at?: string }
  /** An Agent's background work (#5421, cindy's Background tasks tab; lib/background-panel.ts): its /loop, background tasks
   *  and workflow runs. `run`: a workflow run's task_id; `task`: a background command's id; `from`: Session overview when
   *  that view opened the detail. */
  | { kind: "background"; mid: string; sid: string; run?: string; task?: string; from?: "overview" }
  /** A subagent's full conversation opened from Session overview, an expanded TaskCard, or a deep link (#14323):
   *  renders embedded ChatView in the right panel (full-screen overlay on narrow viewports) while keeping the parent chat
   *  in place. `mid`/`sid` name the subagent (including `<parent>@<agent>` or cross-machine child); `parentMid`/`parentSid`
   *  name the parent session; `call` is the parent transcript's launching call ID when known. */
  | { kind: "subagent"; mid: string; sid: string; parentMid?: string; parentSid?: string; call?: string; from?: "overview" | "card" | "link" }
  /** The session's dev server in a headless browser on its computer (docs/web/preview.md); `port` / `path`: the one to
   *  show (a localhost link of a reply), else the one shown last or the first running one. */
  | { kind: "preview"; mid: string; sid: string; cwd: string; port?: number; path?: string };

/** Files and Git state belongs to a directory even when that workspace has no Agent/session ID. */
export function directoryPanelKey(panel: { mid: string; sid: string; cwd: string }): string {
  return JSON.stringify([panel.mid, panel.sid, panel.cwd]);
}

let current: Panel | null = null;
let history: Panel[] = [];
let opener: Element | null = null;
const listeners = new Set<() => void>();

let tracking = false;
let trigger: EventTarget | null = null;

function inSidePanel(n: EventTarget | Node | null | undefined): boolean {
  if (!(n instanceof Node)) return false;
  return Boolean((n instanceof Element ? n : n.parentElement)?.closest(PANEL_SEL));
}

function onCapture(e: Event) {
  const t = e.target;
  trigger = t;
  queueMicrotask(() => {
    if (trigger === t) trigger = null;
  });
}

function onBubble(e: Event) {
  if (trigger === e.target) trigger = null;
}

function setTracking(on: boolean) {
  if (typeof window === "undefined" || on === tracking) return;
  tracking = on;
  for (const ev of TRIGGER_EVENTS) {
    if (on) {
      window.addEventListener(ev, onCapture, true);
      window.addEventListener(ev, onBubble);
    } else {
      window.removeEventListener(ev, onCapture, true);
      window.removeEventListener(ev, onBubble);
    }
  }
  if (!on) trigger = null;
}

function panelTargetKey(p: Panel): string {
  switch (p.kind) {
    case "files":
    case "git":
    case "review":
    case "preview":
      return `${p.kind}:${directoryPanelKey(p)}`;
    case "changes":
    case "agent":
    case "team":
    case "sides":
    case "background":
    case "subagent":
      return `${p.kind}:${p.mid}/${p.sid}`;
    case "file":
      return `file:${p.mid}:${p.path}${p.readOnly ? ":ro" : ""}`;
    case "link":
      return `link:${p.manager ?? ""}:${p.url}`;
    case "task":
      return `task:${p.todo}`;
    case "conversation-tasks":
      return `conversation-tasks:${p.manager}:${p.mid}/${p.sid}`;
  }
}

const sub = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};
const emit = () => listeners.forEach((l) => l());

export function openPanel(p: Panel | null, options?: OpenPanelOptions) {
  // a subagent's conversation is not opened (#5030): its parent's chat, at the subagent's card
  if (p?.kind === "agent") p = { ...p, sid: followSubagentLink(p.mid, p.sid) };
  // what had the focus when the panel opened (its header button): closing gives the focus back to it (#5155). Called
  // from the click, before the panel renders, so it is never something inside the panel; a panel replacing another keeps
  // the first one's, except an overview detail, which returns to the overview row that opened it.
  if (p && (!current || ((p.kind === "background" || p.kind === "subagent") && p.from === "overview")))
    opener = typeof document === "undefined" ? null : document.activeElement;
  if (!p) {
    history = [];
    current = null;
  } else if (options?.replace) {
    current = p;
  } else {
    // Auto-push only during an active user interaction event inside the side panel; programmatic and outside opens
    // always clear history regardless of whether the requested panel shares the current target key.
    const shouldPush = options?.push ?? (Boolean(current) && trigger !== null && inSidePanel(trigger));
    if (!shouldPush || !current) {
      history = [];
    } else {
      const curKey = panelTargetKey(current);
      if (curKey !== panelTargetKey(p)) {
        const top = history[history.length - 1];
        if (!top || panelTargetKey(top) !== curKey) {
          history = [...history, current].slice(-MAX_PANEL_HISTORY);
        }
      }
    }
    current = p;
  }
  setTracking(current !== null);
  emit();
}

/** Clears the side-panel back stack without closing the currently open panel. */
export function clearPanelHistory() {
  if (!history.length) return;
  history = [];
  emit();
}

/** Pops the top panel from the back stack into `current`; if the stack is empty, closes the panel. */
export function popPanel(): boolean {
  if (!history.length) {
    if (current) closePanel();
    return false;
  }
  current = history[history.length - 1];
  history = history.slice(0, -1);
  setTracking(true);
  emit();
  return true;
}

export function closePanel() {
  openPanel(null);
}

export function getPanel() {
  return current;
}

export function canGoBack(): boolean {
  return history.length > 0;
}

export function getPanelHistory(): readonly Panel[] {
  return history;
}

/** What had the focus when the open panel was opened (see openPanel). */
export function panelOpener(): Element | null {
  return opener;
}

/** Toggle a session panel from a header button: the same kind for the same session closes it; another replaces it. */
export function togglePanel(p: Panel) {
  const c = current;
  const sameKind = c && c.kind === p.kind && "mid" in c && "mid" in p && c.mid === p.mid && ("sid" in c && "sid" in p ? c.sid === p.sid : true);
  openPanel(sameKind ? null : p, { push: false });
}

export function usePanel(): Panel | null {
  return useSyncExternalStore(sub, () => current, () => current);
}

export function usePanelCanGoBack(): boolean {
  return useSyncExternalStore(sub, canGoBack, canGoBack);
}
