import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { appArchiveDeps, archiveAgent, closeBusyReason, closeOffer, type CloseBusyReason, type CloseOffer } from "../../lib/agent-archives";
import { api, errorText, sessionPath } from "../../lib/api";
import { stripBase } from "../../lib/base";
import { sessionMenuActions, sessionTitle, tabLabel } from "../../lib/format";
import { navigate, parse, showsSession } from "../../lib/router";
import { getSession, getState, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, cx, Dialog, toast } from "../../ui/atoms";
import { cleanupReady, KEEP, runCleanup, useAgentWorktree, WorktreeCleanup, type Cleanup } from "./SessionDialogs";

// "Close…" (#7125): the one way the web closes an Agent. Its row menu, the chat's ⋯ and "Close pane" on its pane all
// ask here (lib/close-agent.ts; views/CloseAgentHost.tsx loads this chunk once the browser is idle), reading the agent's latest row as it changes:
// - not busy, it archives and closes (lib/agent-archives.ts, `archive?close=1`): the chat stays, saying it is archived;
// - busy, it says what closing interrupts, and "Close anyway" closes it through `POST …/close` (the hub refuses to
//   archive a busy agent), then leaves its chat as "Close agent" did (#3405); so does an agent the hub cannot archive and close.
// The conversation stays every way. `onClosed`: what to do once a plain close is done; without it, the page leaves the
// agent's chat only when that chat is open.
export function CloseAgentDialog({ open, onClose, onClosed, session, machineName }: { open: boolean; onClose: () => void; onClosed?: () => void; session: Session; machineName: string }) {
  const [pending, setPending] = useState(false);
  // the hub said it is busy though this page doesn't know why yet (a 409 to the archive): it closes as a busy one does
  const [refused, setRefused] = useState(false);
  const live = useStore(() => getSession(session.machine_id, session.session_id));
  const features = useStore((st) => st.features);
  const wt = useAgentWorktree(session, open);
  const [cleanup, setCleanup] = useState<Cleanup>(KEEP);
  useEffect(() => {
    if (open) setCleanup(KEEP); // each time: keeping is the default
    if (open) setRefused(false);
  }, [open]);
  const shown = closing(live ?? session, session, features, refused);
  const go = async () => {
    // the agent went away while the dialog was open: nothing left to close, so don't leave a button that does nothing
    const current = getSession(session.machine_id, session.session_id);
    const now = current && closing(current, session, getState().features, refused);
    if (!current || !now || now.offer === "no" || now.offer === "busy") {
      onClose();
      return;
    }
    setPending(true);
    try {
      if (now.offer === "archive") {
        const r = await archiveAgent(current, true, appArchiveDeps);
        if (!r.ok) {
          if (r.busy) setRefused(true); // the toast said why; the dialog now offers to close it anyway
          return;
        }
        onClose();
      } else {
        await api(`${sessionPath(session.machine_id, session.session_id)}/close`, { method: "POST" });
        toast(t("chat-chatview.closed"), "ok");
        onClose();
        if (onClosed) onClosed();
        else if (showsSession(parse(stripBase(location.pathname), location.search), session.machine_id, session.session_id)) navigate({ name: "home" });
      }
      await runCleanup(session.machine_id, wt, cleanup, session.host);
    } catch (e) {
      toast(t("chat-chatview.close_failed", { error: errorText(e) }), "error");
    } finally {
      setPending(false);
    }
  };
  const row = live ?? session;
  const detail = shown.offer === "archive"
    ? t("chat-sessiondialogs.archive_close_detail", { title: sessionTitle(row), machine: machineName })
    : t(row.source_kind === "runtime" ? "chat-chatview.close_runtime_detail" : "chat-chatview.close_agent_detail", { machine: machineName, title: sessionTitle(row), where: [row.space_name, tabLabel(row)].filter(Boolean).join(" › ") || sessionTitle(row) });
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-chatview.close_agent_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {shown.offer !== "busy" && (
            <Button tone="danger" busy={pending} disabled={!cleanupReady(wt, cleanup)} onClick={go}>
              {shown.why ? t("close-agent.close_anyway") : shown.offer === "archive" ? t("chat-sessiondialogs.archive_close") : t("chat-chatview.close_agent")}
            </Button>
          )}
        </>
      }
    >
      {shown.why && <p className="text-meta leading-relaxed font-medium text-red" data-close-busy={shown.why}>{closeBusyText(shown.why)}</p>}
      <p className={cx("text-meta leading-relaxed text-ink-2", shown.why && "mt-2")}>{shown.offer === "busy" ? t("agentmenus.close_busy") : detail}</p>
      {wt && shown.offer !== "busy" && <WorktreeCleanup st={wt} value={cleanup} onChange={setCleanup} />}
    </Dialog>
  );
}

/** What closing a busy agent interrupts, in the dialog's words. */
function closeBusyText(why: CloseBusyReason): string {
  if (why === "waiting") return t("close-agent.busy_waiting");
  if (why === "loop") return t("close-agent.busy_loop");
  if (why === "server") return t("close-agent.busy_server");
  return t("close-agent.busy_working");
}

/** What "Close…" does with row now (lib/agent-archives.ts closeOffer) and, busy, what closing interrupts. `asked` is the
 *  row the dialog was opened for: an archived chat's (the menus' snapshot says so) is only closed. `refused`: the hub
 *  refused to archive it as busy, so it closes as a busy one does. */
function closing(now: Session, asked: Session, features: readonly string[], refused: boolean): { offer: CloseOffer; why?: CloseBusyReason } {
  const row = asked.archived ? { ...now, archived: true } : now;
  const why = closeBusyReason(row) ?? (refused ? "working" : undefined);
  let offer = closeOffer(row, features);
  if (offer === "archive" && why) offer = sessionMenuActions(row).includes("close") ? "close" : "busy";
  return { offer, why: offer === "close" ? why : undefined };
}
