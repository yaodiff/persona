// The Agents list's menus (iOS AgentRowMenu / SpaceMenuItems): a row's ⋯ or right click, a space's ⋯ on its header,
// and the confirmation of "Mark all as seen". What they do is in lib/agent-actions.ts.
import { attachLive, flipMute, flipPin, handToPersona } from "../lib/agent-actions";
import { errorText } from "../lib/api";
import { resumeCommand, sessionCanRename, sessionDeletable, sessionMenuActions } from "../lib/format";
import { appArchiveDeps, archiveAgent, archiveOffers, deleteInsteadOfArchive } from "../lib/agent-archives";
import { closeAgentAction } from "../lib/close-agent";
import { machineWithHub } from "../lib/mesh";
import { isPinned } from "../lib/pins";
import { isMuted } from "../lib/session-actions";
import { getState, markSeen, refresh, refreshTodos, useStore } from "../lib/store";
import { unlinkAgentFromTodo } from "../lib/todos";
import type { Session } from "../lib/types";
import { sessionNode } from "../lib/node-actions";
import { Button, copyText, Dialog, toast, type MenuItem } from "../ui/atoms";
import type { MenuAt } from "../ui/menu-model";
import { NodeMenu } from "./NodeMenu";
import { t } from "../i18n";
import { markAllSeenLabel } from "../lib/state-labels";
import { lazy, Suspense } from "react";
import type { SpaceMenuProps } from "./SpaceMenu";

/** What a row's ⋯ (and its right click) offers. `onLinkTodo`: the row can hang on a todo ("Link to to-do…"; "Move to another to-do…" and
 *  "Unlink" when it does). Everything a row offers is here, so a touch screen (no right click; iOS Safari sends no contextmenu on a
 *  long press) reaches it from the ⋯ (#1996). */
export function agentRowMenuItems(s: Session, o: { pinned: boolean; muted: boolean; onRename: (s: Session) => void; onDelete: (s: Session) => void; onLinkTodo?: (s: Session) => void; features?: readonly string[] }): MenuItem[] {
  const { onRename, onDelete, onLinkTodo } = o;
  // "Live link" shows when the chat's ⋯ shows it (sessionMenuActions), and does the same (#3405); so do Archive and Close…
  const allowed = sessionMenuActions(s);
  return [
    ...(allowed.includes("attach") ? [{ key: "attach", label: t("chat-chatview.attach"), icon: "link" as const, onSelect: () => void attachLive(s) }] : []),
    { key: "pin", label: o.pinned ? t("agentmenus.unpin") : t("agentmenus.pin"), icon: "pin", accel: "p", onSelect: () => void flipPin(s) },
    // as the chat's ⋯: by capabilities.rename where the node says so (a runtime's session, #6294)
    ...(sessionCanRename(s) ? [{ key: "rename", label: t("agentmenus.rename"), icon: "pencil" as const, accel: "r", onSelect: () => onRename(s) }] : []),
    { key: "mute", label: o.muted ? t("agentmenus.unmute") : t("agentmenus.mute"), icon: o.muted ? "bell" : "bellOff", onSelect: () => void flipMute(s) },
    { key: "persona", label: t("agentmenus.hand_to_persona"), icon: "spark", onSelect: () => void handToPersona(s, machineWithHub(s.machine_id, getState().machines)) },
    // on a todo, the same dialog moves it to another (the hub releases the old link), so it says so (#2348)
    ...(onLinkTodo ? [{ key: "link-todo", label: s.todo ? t("agentmenus.move_to_other_todo") : t("agentmenus.link_to_todo"), icon: "todo" as const, divider: true, onSelect: () => onLinkTodo(s) }] : []),
    ...(onLinkTodo && s.todo ? [{ key: "unlink-todo", label: t("agentmenus.unlink"), icon: "x" as const, onSelect: () => void unlinkTodo(s, s.todo!.id) }] : []),
    ...(s.cwd ? [{ key: "cwd", label: t("agentmenus.copy_folder"), icon: "folder" as const, accel: "c", onSelect: () => copyText(s.cwd) }] : []),
    ...(allowed.includes("resume") ? [{ key: "resume", label: t("agentmenus.copy_resume"), icon: "terminal" as const, onSelect: () => copyText(resumeCommand(s)) }] : []),
    ...archiveCloseItems(s, o.features ?? getState().features),
    ...(deleteInsteadOfArchive(o.features ?? getState().features) && sessionDeletable(s) ? [{ key: "delete", label: t("agentmenus.delete_chat"), icon: "trash" as const, danger: true, onSelect: () => onDelete(s) }] : []),
  ];
}

/** "Archive" and "Close…" after a hairline (#7125). Archive (lib/agent-archives.ts) only hides the agent, so a busy one
 *  shows it disabled, saying what to wait for. Close… (lib/close-agent.ts) asks, then archives and closes an agent that
 *  is not busy, and says what closing interrupts before it closes a busy one. The chat's ⋯ offers the same. */
export function archiveCloseItems(s: Session, features: readonly string[]): MenuItem[] {
  const archive = archiveOffers(s, features).archive;
  const items: MenuItem[] = [
    ...(archive !== "no"
      ? [{ key: "archive", label: t("agentmenus.archive"), icon: "archive" as const, disabled: archive === "busy", detail: archive === "busy" ? t("agentmenus.archive_busy") : undefined, onSelect: () => void archiveAgent(s, false, appArchiveDeps) }]
      : []),
    ...closeItems(s, features),
  ];
  return items.map((it, i) => (i === 0 ? { ...it, divider: true } : it));
}

/** "Close…" alone: an archived chat's ⋯ has it under Restore while its agent is still open on the computer. */
export function closeItems(s: Session, features: readonly string[]): MenuItem[] {
  const close = closeAgentAction(s, features);
  return close ? [{ key: "close", label: t("agentmenus.close"), icon: "x" as const, danger: true, ...close }] : [];
}

async function unlinkTodo(s: Session, todo: string) {
  try {
    await unlinkAgentFromTodo(todo, s.machine_id, s.session_id);
    await refreshTodos();
    await refresh();
    toast(t("agentmenus.unlinked"), "ok");
  } catch (e) {
    toast(t("agentmenus.unlink_failed", { error: errorText(e) }), "error");
  }
}

/** The row's menu, then what any pane offers (split or close it, git, copy its ids): src/lib/node-actions.ts. The path
 *  and the rename are the row's own above. `at`: where a right click opened it (the pointer); without it, beside `anchor`. */
export function AgentRowMenu({ s, anchor, at, onClose, ...o }: { s: Session; anchor: HTMLElement; at?: MenuAt; onClose: () => void; onRename: (s: Session) => void; onDelete: (s: Session) => void; onLinkTodo?: (s: Session) => void }) {
  const pinned = useStore((st) => isPinned(s, st.pins));
  const muted = useStore((st) => isMuted(s, st.mutes));
  const features = useStore((st) => st.features);
  const online = useStore((st) => st.machines.find((m) => m.id === s.machine_id)?.online !== false);
  return <NodeMenu node={sessionNode(s, online)} at={at ?? { anchor }} onClose={onClose} before={agentRowMenuItems(s, { pinned, muted, features, ...o })} omit={["node-copy-path", "node-rename"]} />;
}

const LazySpaceMenu = lazy(() => import("./SpaceMenu").then((module) => ({ default: module.SpaceMenu })));
export function SpaceMenu(props: SpaceMenuProps) {
  return <Suspense fallback={null}><LazySpaceMenu {...props} /></Suspense>;
}

/** "Mark all as seen", asked first: it changes what the other devices show too. */
export function MarkSeenDialog({ list, onClose, onDone }: { list: Session[] | null; onClose: () => void; onDone?: () => void }) {
  const n = list?.length ?? 0;
  return (
    <Dialog
      open={Boolean(list)}
      onClose={onClose}
      title={markAllSeenLabel()}
      width={380}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            tone="primary"
            icon="check"
            onClick={() => {
              if (list) markSeen(list);
              onDone?.();
              onClose();
            }}
          >
            {markAllSeenLabel(n)}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{t("agentmenus.mark_all_seen_body", { count: n })}</p>
    </Dialog>
  );
}
