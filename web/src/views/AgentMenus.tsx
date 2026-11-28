// The Agents list's menus (iOS AgentRowMenu / SpaceMenuItems): a row's ⋯ or right click, a space's ⋯ on its header,
// and the confirmation of "Mark all as seen". What they do is in lib/agent-actions.ts.
import { flipMute, flipPin, handToPersona } from "../lib/agent-actions";
import { errorText } from "../lib/api";
import { resumeCommand, sessionCanRename, sessionMenuActions } from "../lib/format";
import { appArchiveDeps, archiveAgent, archiveOffers, closeOffer, offerSessionDelete } from "../lib/agent-archives";
import { closeAgentAction } from "../lib/close-agent";
import { machineWithHub } from "../lib/mesh";
import { isPinned } from "../lib/pins";
import { isMuted } from "../lib/session-actions";
import { getState, refresh, refreshTodos, useStore } from "../lib/store";
import { unlinkAgentFromTodo } from "../lib/todos";
import type { Session } from "../lib/types";
import { nodeActions, sessionNode, type NodeActionDeps } from "../lib/node-actions";
import { copyText, Menu, toast, type MenuItem } from "../ui/atoms";
import type { MenuAt } from "../ui/menu-model";
import type { NodeMenuProps } from "./NodeMenu";
import { preloadSessionDialogs } from "./chat/SessionDialogs";
import { t } from "../i18n";
import { lazy, Suspense, useEffect, type ComponentType } from "react";
import type { SpaceMenuProps } from "./SpaceMenu";
import { lazyDialog } from "./LazyOverlay";

export type NoticeTone = "ok" | "error" | "info";
export interface ArchiveCloseActions {
  onArchive?: (s: Session) => void | Promise<void>;
  onCloseAgent?: (s: Session) => void;
  onNotice?: (message: string, tone: NoticeTone) => void;
}
export interface AgentRowMenuActions extends ArchiveCloseActions {
  onAttach?: (s: Session) => void | Promise<void>;
  onTogglePin?: (s: Session) => void | Promise<void>;
  onToggleMute?: (s: Session) => void | Promise<void>;
  onHandToPersona?: (s: Session, machineName?: string) => void | Promise<void>;
  onUnlinkTodo?: (s: Session, todoId: string) => void | Promise<void>;
  onCopy?: (text: string) => void | Promise<void>;
}
export interface AgentRowMenuItemsOptions extends AgentRowMenuActions {
  pinned: boolean; muted: boolean; onRename: (s: Session) => void; onDelete: (s: Session) => void;
  onLinkTodo?: (s: Session) => void; features?: readonly string[]; machineName?: string;
}
export interface AgentRowMenuProps extends AgentRowMenuActions {
  s: Session; anchor: HTMLElement; at?: MenuAt; onClose: () => void; onRename: (s: Session) => void; onDelete: (s: Session) => void;
  onLinkTodo?: (s: Session) => void; pinned?: boolean; muted?: boolean; features?: readonly string[]; online?: boolean;
  machineName?: string; nodeDeps?: NodeActionDeps; nodeItems?: MenuItem[]; preloadDialogs?: boolean;
}
export interface MarkSeenDialogProps {
  list: Session[] | null; release?: Session[]; onClose: () => void; onDone?: () => void;
  onMarkSeen?: (sessions: Session[]) => void | Promise<void>; onNotice?: (message: string, tone: NoticeTone) => void;
}

const isPromiseLike = (v: unknown): v is PromiseLike<unknown> => Boolean(v) && typeof (v as PromiseLike<unknown>).then === "function";
const LazyMarkSeenDialog = lazyDialog((): Promise<ComponentType<MarkSeenDialogProps & { open: boolean }>> => import("./MarkSeenDialog").then((m) => m.MarkSeenDialogBody));

function runControlled(fn: () => void | Promise<void>, onNotice?: (message: string, tone: NoticeTone) => void) {
  try {
    const res = fn();
    if (isPromiseLike(res)) void Promise.resolve(res).catch((e) => { onNotice?.(errorText(e), "error"); });
  } catch (e) { onNotice?.(errorText(e), "error"); }
}

/** What a row's ⋯ (and its right click) offers. `onLinkTodo`: the row can hang on a todo ("Link to to-do…"; "Move to another to-do…" and
 *  "Unlink" when it does). Everything a row offers is here, so a touch screen (no right click; iOS Safari sends no contextmenu on a
 *  long press) reaches it from the ⋯ (#1996). */
export function agentRowMenuItems(s: Session, o: AgentRowMenuItemsOptions): MenuItem[] {
  const { onRename, onDelete, onLinkTodo } = o;
  // "Live link" shows when the chat's ⋯ shows it (sessionMenuActions), and does the same (#3405); so do Archive and Close…
  const allowed = sessionMenuActions(s);
  const features = o.features ?? getState().features;
  const copy = (text: string) => (o.onCopy ? runControlled(() => o.onCopy!(text), o.onNotice) : void copyText(text));
  return [
    ...(allowed.includes("attach") ? [{ key: "attach", label: t("chat-chatview.attach"), icon: "link" as const, onSelect: () => (o.onAttach ? runControlled(() => o.onAttach!(s), o.onNotice) : void import("../lib/attach-live").then((m) => m.attachLive(s))) }] : []),
    { key: "pin", label: o.pinned ? t("agentmenus.unpin") : t("agentmenus.pin"), icon: "pin", accel: "p", onSelect: () => (o.onTogglePin ? runControlled(() => o.onTogglePin!(s), o.onNotice) : void flipPin(s)) },
    // as the chat's ⋯: by capabilities.rename where the node says so (a runtime's session, #6294)
    ...(sessionCanRename(s) ? [{ key: "rename", label: t("agentmenus.rename"), icon: "pencil" as const, accel: "r", onSelect: () => onRename(s) }] : []),
    { key: "mute", label: o.muted ? t("agentmenus.unmute") : t("agentmenus.mute"), icon: o.muted ? "bell" : "bellOff", onSelect: () => (o.onToggleMute ? runControlled(() => o.onToggleMute!(s), o.onNotice) : void flipMute(s)) },
    { key: "persona", label: t("agentmenus.hand_to_persona"), icon: "spark", onSelect: () => {
      const machine = o.machineName !== undefined ? o.machineName : (o.onHandToPersona ? undefined : machineWithHub(s.machine_id, getState().machines));
      if (o.onHandToPersona) runControlled(() => o.onHandToPersona!(s, machine), o.onNotice); else void handToPersona(s, machine);
    } },
    // on a todo, the same dialog moves it to another (the hub releases the old link), so it says so (#2348)
    ...(onLinkTodo ? [{ key: "link-todo", label: s.todo ? t("agentmenus.move_to_other_todo") : t("agentmenus.link_to_todo"), icon: "todo" as const, divider: true, onSelect: () => onLinkTodo(s) }] : []),
    ...(onLinkTodo && s.todo ? [{ key: "unlink-todo", label: t("agentmenus.unlink"), icon: "x" as const, onSelect: () => (o.onUnlinkTodo ? runControlled(() => o.onUnlinkTodo!(s, s.todo!.id), o.onNotice) : void unlinkTodo(s, s.todo!.id, o.onNotice)) }] : []),
    ...(s.cwd ? [{ key: "cwd", label: t("agentmenus.copy_folder"), icon: "folder" as const, accel: "c", onSelect: () => copy(s.cwd) }] : []),
    ...(allowed.includes("resume") ? [{ key: "resume", label: t("agentmenus.copy_resume"), icon: "terminal" as const, onSelect: () => copy(resumeCommand(s)) }] : []),
    ...archiveCloseItems(s, features, o),
    ...(offerSessionDelete(s, features) ? [{ key: "delete", label: t("agentmenus.delete_chat"), icon: "trash" as const, danger: true, onSelect: () => onDelete(s) }] : []),
  ];
}

/** "Archive" and "Close…" after a hairline (#7125). Archive (lib/agent-archives.ts) only hides the agent, so a busy one
 *  shows it disabled, saying what to wait for. Close… (lib/close-agent.ts) asks, then archives and closes an agent that
 *  is not busy, and says what closing interrupts before it closes a busy one. The chat's ⋯ offers the same. */
export function archiveCloseItems(s: Session, features: readonly string[], actions?: ArchiveCloseActions): MenuItem[] {
  const archive = archiveOffers(s, features).archive;
  const items: MenuItem[] = [
    ...(archive !== "no"
      ? [{ key: "archive", label: t("agentmenus.archive"), icon: "archive" as const, disabled: archive === "busy", detail: archive === "busy" ? t("agentmenus.archive_busy") : undefined,
          onSelect: () => { if (archive !== "busy") actions?.onArchive ? runControlled(() => actions.onArchive!(s), actions.onNotice) : void archiveAgent(s, false, appArchiveDeps); } }]
      : []),
    ...closeItems(s, features, actions?.onCloseAgent),
  ];
  return items.map((it, i) => (i === 0 ? { ...it, divider: true } : it));
}

/** "Close…" alone: an archived chat's ⋯ has it under Restore while its agent is still open on the computer. */
export function closeItems(s: Session, features: readonly string[], onCloseAgent?: (s: Session) => void): MenuItem[] {
  if (onCloseAgent) {
    const offer = closeOffer(s, features);
    if (offer === "no") return [];
    return [{ key: "close", label: t("agentmenus.close"), icon: "x" as const, danger: true,
      ...(offer === "busy" ? { disabled: true, detail: t("agentmenus.close_busy"), onSelect: () => {} } : { onSelect: () => onCloseAgent(s) }) }];
  }
  const close = closeAgentAction(s, features);
  return close ? [{ key: "close", label: t("agentmenus.close"), icon: "x" as const, danger: true, ...close }] : [];
}

async function unlinkTodo(s: Session, todo: string, onNotice?: (message: string, tone: NoticeTone) => void) {
  const notify = onNotice ?? toast;
  try {
    await unlinkAgentFromTodo(todo, s.machine_id, s.session_id);
    await refreshTodos();
    await refresh();
    notify(t("agentmenus.unlinked"), "ok");
  } catch (e) {
    notify(t("agentmenus.unlink_failed", { error: errorText(e) }), "error");
  }
}

type PinnedProps = AgentRowMenuProps & { pinned: boolean };
type MutedProps = PinnedProps & { muted: boolean };
type FeaturesProps = MutedProps & { features: readonly string[] };
type ResolvedProps = FeaturesProps & { online: boolean };

/** The row's menu, then what any pane offers (split or close it, git, copy its ids): src/lib/node-actions.ts. The path
 *  and the rename are the row's own above. `at`: where a right click opened it (the pointer); without it, beside `anchor`. */
export function AgentRowMenu(props: AgentRowMenuProps) {
  return props.pinned === undefined ? <RowWithPinned {...props} /> : <RowMuted {...props} pinned={props.pinned} />;
}
function RowWithPinned(props: AgentRowMenuProps) { return <RowMuted {...props} pinned={useStore((st) => isPinned(props.s, st.pins))} />; }
function RowMuted(props: PinnedProps) { return props.muted === undefined ? <RowWithMuted {...props} /> : <RowFeatures {...props} muted={props.muted} />; }
function RowWithMuted(props: PinnedProps) { return <RowFeatures {...props} muted={useStore((st) => isMuted(props.s, st.mutes))} />; }
function RowFeatures(props: MutedProps) { return props.features === undefined ? <RowWithFeatures {...props} /> : <RowOnline {...props} features={props.features} />; }
function RowWithFeatures(props: MutedProps) { return <RowOnline {...props} features={useStore((st) => st.features)} />; }
function RowOnline(props: FeaturesProps) { return props.online === undefined ? <RowWithOnline {...props} /> : <AgentRowMenuView {...props} online={props.online} />; }
function RowWithOnline(props: FeaturesProps) { return <AgentRowMenuView {...props} online={useStore((st) => st.machines.find((m) => m.id === props.s.machine_id)?.online !== false)} />; }

function AgentRowMenuView({ s, anchor, at, onClose, pinned, muted, features, online, nodeDeps, nodeItems, preloadDialogs, ...o }: ResolvedProps) {
  const shouldPreload = preloadDialogs ?? (nodeItems === undefined && nodeDeps === undefined);
  // its Rename… and Delete… open dialogs from a chunk of their own: on its way while the pointer moves to them (#10382)
  useEffect(() => { if (shouldPreload) preloadSessionDialogs(); }, [shouldPreload]);
  const before = agentRowMenuItems(s, { pinned, muted, features, ...o });
  const place = at ?? { anchor };
  const menuKey = `${s.machine_id}/${s.session_id}`;
  if (nodeItems !== undefined || nodeDeps !== undefined) {
    const own = nodeItems ?? nodeActions(sessionNode(s, online), nodeDeps!, { omit: ["node-copy-path", "node-rename"] });
    const items = before.length && own.length ? [...before, { ...own[0], divider: true }, ...own.slice(1)] : [...before, ...own];
    return <Menu key={menuKey} anchor={place.anchor ?? null} point={place.point} onClose={onClose} align="right" width={224} headerAsWritten items={items} />;
  }
  return <NodeMenu key={menuKey} node={sessionNode(s, online)} at={place} onClose={onClose} before={before} omit={["node-copy-path", "node-rename"]} />;
}

// The menu of a computer, folder or Agent opens on a click: its code comes in a chunk of its own on the first one, as
// SpaceMenu's below does, so it is not on every first screen (Sidebar, AgentRowMenu).
const LazyNodeMenu = lazy(() => import("./NodeMenu").then((module) => ({ default: module.NodeMenu })));
export function NodeMenu(props: NodeMenuProps) {
  return <Suspense fallback={null}><LazyNodeMenu {...props} /></Suspense>;
}

const LazySpaceMenu = lazy(() => import("./SpaceMenu").then((module) => ({ default: module.SpaceMenu })));
export function SpaceMenu({ preloadDialogs = true, ...props }: SpaceMenuProps & { preloadDialogs?: boolean }) {
  useEffect(() => { if (preloadDialogs) preloadSessionDialogs(); }, [preloadDialogs]); // its Rename… opens a dialog from a chunk of its own (#10382)
  return <Suspense fallback={null}><LazySpaceMenu {...props} /></Suspense>;
}

/** "Mark all as seen", asked first: it changes what the other devices show too. `release`: every row in the scope
 *  whose held-seen place also leaves when confirmed (defaults to `list`). Nothing until its first open, then the dialog
 *  from its own chunk (views/MarkSeenDialog.tsx), kept mounted for its exit. */
export function MarkSeenDialog(props: MarkSeenDialogProps) {
  return <LazyMarkSeenDialog {...props} open={Boolean(props.list)} />;
}
