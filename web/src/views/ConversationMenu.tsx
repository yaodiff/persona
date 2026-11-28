import { lazy, Suspense } from "react";
// A Persona conversation's menu in the sidebar (matching iOS AssistantSidebar):
// - Non-main conversations: pin/unpin as direction leader, Rename…, Archive, Copy session ID, Delete, and End… while live.
// - Main conversation: capability-gated Compact context, Clear context, and New main chat… (matching iOS MainConversationMenu),
//   plus Copy session ID and End… while live; never made a leader, renamed, archived, or deleted.
// - Built-in background supervisor (#5082, lib/background-supervisor.ts): only Copy session ID.
import { api, enc, errorText, sessionPath } from "../lib/api";
import { COMPACT_REQUESTED_EVENT, compactBody, compactPath } from "../lib/compaction";
import { sessionTitle } from "../lib/format";
import { clearMainContext } from "../lib/main-clear";
import { refreshAssistant } from "../lib/store";
import type { Session } from "../lib/types";
import { copyText, Dialog, Loading, toast, type MenuItem, type ToastAction } from "../ui/atoms";
import { t } from "../i18n";

/** What the hub does for the menu; tests swap it. */
export interface ConversationMenuDeps {
  rename: (s: Session, title: string) => Promise<unknown>;
  archive: (s: Session) => Promise<unknown>;
  unarchive: (s: Session) => Promise<unknown>;
  end: (s: Session) => Promise<unknown>;
  refresh: () => Promise<unknown>;
  /** Makes the conversation a direction leader or ends it, on the hub for every device. */
  lead: (s: Session, on: boolean) => Promise<unknown>;
  /** Compacts the conversation's context on the hub. */
  compact?: (s: Session) => Promise<unknown>;
  /** Clears the main conversation's context in place on the hub. */
  clearMain?: (s: Session) => Promise<unknown>;
  copy: (text: string) => unknown;
  notify: (text: string, tone: "ok" | "error", action?: ToastAction) => void;
}

export const conversationMenuDeps: ConversationMenuDeps = {
  rename: (s, title) => api(`${sessionPath(s.machine_id, s.session_id)}/rename`, { method: "POST", json: { title } }),
  archive: (s) => api(`/assistant/conversations/${enc(s.session_id)}/archive`, { method: "POST", json: {} }),
  unarchive: (s) => api(`/assistant/conversations/${enc(s.session_id)}/unarchive`, { method: "POST", json: {} }),
  end: (s) => api(`${sessionPath(s.machine_id, s.session_id)}/close`, { method: "POST" }),
  refresh: () => refreshAssistant(),
  lead: (s, on) => api(`/assistant/directions/${enc(s.session_id)}/${on ? "make" : "end"}`, { method: "POST", json: {} }),
  compact: async (s) => {
    await api(compactPath(s.session_id), { method: "POST", json: compactBody(s.machine_id) });
    if (typeof window !== "undefined") {
      window.dispatchEvent(new CustomEvent(COMPACT_REQUESTED_EVENT, { detail: { mid: s.machine_id, sid: s.session_id } }));
    }
  },
  clearMain: (s) => clearMainContext(s.session_id),
  copy: copyText,
  notify: (...args) => toast(...args),
};

/** A running conversation can be ended (iOS `status.isLive`). */
export const conversationLive = (s: Session) => s.status !== "offline";

const reread = (deps: ConversationMenuDeps) => void deps.refresh().catch(() => {});

async function undoArchive(s: Session, title: string, deps: ConversationMenuDeps) {
  try {
    await deps.unarchive(s);
    deps.notify(t("conversation-menu.unarchived", { title }), "ok");
  } catch (e) {
    deps.notify(t("conversation-menu.undo_failed", { error: errorText(e) }), "error");
  }
  reread(deps);
}

/** 设为常驻 / 取消常驻 for a conversation that is not the main one (`leader`: it is one now). Both ask a second time
 * before the hub call (#6214): the first choice only arms the row, the second runs it, and the detail says what moves —
 * unpinning sends the chat back to 最近 and returns its open tasks to the main chat's supervision, nothing deleted. */
export function leaderItem(s: Session, leader: boolean, deps: ConversationMenuDeps = conversationMenuDeps): MenuItem {
  const title = sessionTitle(s);
  return {
    key: "leader",
    label: leader ? t("conversation-menu.end_leader") : t("conversation-menu.make_leader"),
    icon: "pin",
    accel: "p",
    confirm: leader ? t("conversation-menu.end_leader_confirm") : t("conversation-menu.make_leader_confirm"),
    confirmDetail: leader
      ? t("conversation-menu.end_leader_confirm_detail")
      : t("conversation-menu.make_leader_confirm_detail"),
    // The menu stays open while the hub call runs, shows a refusal in place, and closes only on success.
    inline: true,
    onSelect: async () => {
      try {
        await deps.lead(s, !leader);
        deps.notify(leader ? t("conversation-menu.leader_ended", { title }) : t("conversation-menu.leader_made", { title }), "ok");
      } catch (e) {
        deps.notify(t("conversation-menu.leader_failed", { error: errorText(e) }), "error");
      }
      reread(deps);
    },
  };
}

/** Archive is the same reversible action in a sidebar row and the open conversation's title menu (#6565). */
export function conversationArchiveItem(s: Session, deps: ConversationMenuDeps = conversationMenuDeps): MenuItem {
  const title = sessionTitle(s);
  return {
    key: "archive",
    label: t("conversation-menu.archive"),
    icon: "archive",
    onSelect: async () => {
      try {
        await deps.archive(s);
        // Undo puts it back as it was (an open conversation stays open: its page reads the machine's sessions)
        deps.notify(t("conversation-menu.archived", { title }), "ok", { label: t("conversation-menu.undo"), run: () => void undoArchive(s, title, deps) });
      } catch (e) {
        deps.notify(errorText(e), "error");
      }
      reread(deps);
    },
  };
}

export interface ConversationMenuOptions {
  main: boolean;
  leader?: boolean;
  builtIn?: boolean;
  canDelete?: boolean;
  onDelete?: () => void;
  canCompact?: boolean;
  canClearMain?: boolean;
  canNewMain?: boolean;
  clearingMain?: boolean;
  onNewMain?: () => void;
}

/** Context actions shared by the header and sidebar. The sidebar keeps the menu owner mounted to host the dialog. */
export function assistantContextItems(
  s: Session,
  o: Pick<ConversationMenuOptions, "canCompact" | "canClearMain" | "canNewMain" | "clearingMain" | "onNewMain">,
  keepMenuOpen: boolean,
  deps: ConversationMenuDeps = conversationMenuDeps,
): MenuItem[] {
  const compacting = s.compaction?.state === "running";
  const compactAction = deps.compact ?? conversationMenuDeps.compact!;
  const clearMainAction = deps.clearMain ?? conversationMenuDeps.clearMain!;
  return [
    ...(o.canCompact
      ? [{
          key: "compact",
          label: compacting ? t("assistant-assistantchat.compacting") : t("assistant-assistantchat.compact"),
          icon: "layers" as const,
          disabled: compacting,
          onSelect: async () => {
            try {
              await compactAction(s);
            } catch (e) {
              deps.notify(t("assistant-assistantchat.compact_start_failed", { error: errorText(e) }), "error");
            }
          },
        }]
      : []),
    ...(o.canClearMain
      ? [{
          key: "clear",
          label: t("assistant-assistantchat.clear"),
          icon: "retry" as const,
          disabled: Boolean(o.clearingMain),
          onSelect: () => clearMainAction(s),
        }]
      : []),
    ...(o.canNewMain && o.onNewMain
      ? [{
          key: "newmain",
          label: t("assistant-assistantchat.new_main_ellipsis"),
          icon: "compose" as const,
          keepOpen: keepMenuOpen,
          disabled: Boolean(o.clearingMain),
          onSelect: o.onNewMain,
        }]
      : []),
  ];
}

/** `leader`: the conversation is a direction leader now. `builtIn`: it is the background supervisor, which the hub keeps
 *  listed and running: no leader toggle, Rename, Archive, main actions, or End. */
export function conversationMenuItems(s: Session, o: ConversationMenuOptions, deps: ConversationMenuDeps = conversationMenuDeps): MenuItem[] {
  const copyBase: MenuItem = { key: "copy", label: t("conversation-menu.copy_id"), icon: "copy", accel: "c", onSelect: () => void deps.copy(s.session_id) };
  if (o.builtIn) return [copyBase];
  const own: MenuItem[] = o.main
    ? assistantContextItems(s, o, true, deps)
    : [
        leaderItem(s, !!o.leader, deps),
        {
          key: "rename",
          label: t("conversation-menu.rename"),
          icon: "pencil",
          accel: "r",
          onSelect: () => {},
          rename: {
            value: s.title ?? "",
            placeholder: t("conversation-menu.title_placeholder"),
            commit: async (name) => {
              await deps.rename(s, name).catch((e) => Promise.reject(new Error(t("conversation-menu.rename_failed", { error: errorText(e) }))));
              deps.notify(t("conversation-menu.renamed"), "ok");
              reread(deps);
            },
          },
        },
        conversationArchiveItem(s, deps),
      ];
  const copy: MenuItem = o.main && own.length > 0 ? { ...copyBase, divider: true } : copyBase;
  return [
    ...own,
    copy,
    ...(!o.main && o.canDelete && o.onDelete ? [{ key: "delete", label: t("conversation-menu.delete"), icon: "trash" as const, danger: true, divider: true, keepOpen: true, onSelect: o.onDelete }] : []),
    ...(conversationLive(s)
      ? [
          {
            key: "end",
            label: t("conversation-menu.end"),
            icon: "stop" as const,
            danger: true,
            divider: true,
            closes: true,
            confirm: t("conversation-menu.end_confirm"),
            inline: true,
            onSelect: async () => {
              await deps.end(s).catch((e) => Promise.reject(new Error(t("conversation-menu.end_failed", { error: errorText(e) }))));
              deps.notify(t("conversation-menu.ended"), "ok");
              reread(deps);
            },
          },
        ]
      : []),
  ];
}

const DeleteConversationDialog = lazy(() => import("./DeleteConversationDialog").then((m) => ({ default: m.DeleteConversationDialog })));

/** The destructive confirmation loads on intent, keeping the common first screen small. */
export function ConversationDeleteConfirmation(props: { s: Session; onClose: () => void }) {
  return <Suspense fallback={<Dialog open title={t("conversation-menu.delete")} onClose={props.onClose}><Loading /></Dialog>}>
    <DeleteConversationDialog {...props} />
  </Suspense>;
}
