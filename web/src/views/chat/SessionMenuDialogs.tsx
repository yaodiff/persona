// The Rename, Rename space and Delete dialogs a session's menus open (the chat's ⋯ menu, an Agent row's, a space's): a
// chunk of their own (SessionDialogs.tsx loads them with lazyDialog), so the first screen does not carry them (#10382).
// The menu that offers one starts the chunk when it opens (preloadSessionDialogs), so the click draws the dialog at once.
import { useEffect, useState } from "react";
import { isUntouchedStart } from "../../lib/agent-archives";
import { api, enc, errorText, sessionPath } from "../../lib/api";
import { herdrTabName, sessionCan, sessionTitle } from "../../lib/format";
import { sessionEngineName } from "../../lib/engine-names";
import { useHerdrTabName } from "../../lib/agents-display";
import { renameBody } from "../../lib/session-actions";
import { href, navigate } from "../../lib/router";
import { dropSession, getSession, refresh, refreshAssistant, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, Dialog, toast } from "../../ui/atoms";
import { t } from "../../i18n";
import { cleanupReady, KEEP, runCleanup, useAgentWorktree, WorktreeCleanup, type Cleanup } from "./SessionWorktreeCleanup";

// "Rename": the terminal tab and the session's name (Claude's /rename), each only when changed (iOS RenameSessionAlert).
export function RenameDialog({ session, open, onClose }: { session: Session; open: boolean; onClose: () => void }) {
  const [tab, setTab] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const preferTab = useHerdrTabName();
  useEffect(() => {
    if (!open) return;
    setTab(session.slot_name ?? "");
    setTitle(session.title ?? "");
    // only when it opens: a push while typing must not overwrite the field
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const hasTab = !session.assistant && Boolean(session.slot_id);
  const unified = preferTab && Boolean(herdrTabName(session));
  // the tab is in the session's own engine: Orca's, Tern's, herdr's (#17053)
  const engine = sessionEngineName(session);
  const body = renameBody(session, tab, title, unified);
  const save = async () => {
    // the form submits on Enter while the field stays editable: a second Enter would rename (type /rename) twice
    if (busy) return;
    if (!body) return onClose();
    setBusy(true);
    try {
      await api(`${sessionPath(session.machine_id, session.session_id)}/rename`, { method: "POST", json: body });
      // the list shows the new name at once; the hub's push confirms it
      const latest = getSession(session.machine_id, session.session_id) ?? session;
      upsertSession({ ...latest, ...(body.title ? { title: body.title } : {}), ...(body.tab_name ? { slot_name: body.tab_name } : {}) });
      if (session.assistant) void refreshAssistant().catch(() => {});
      toast(t("chat-sessiondialogs.renamed"), "ok");
      onClose();
    } catch (e) {
      toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.rename")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!body} onClick={() => void save()}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        {unified ? <Field label={t("chat-sessiondialogs.name")} value={tab} onChange={setTab} autoFocus /> : <>
          {hasTab && <Field label={t("chat-sessiondialogs.tab_name", { engine })} value={tab} onChange={setTab} autoFocus />}
          <Field label={t("chat-sessiondialogs.session_name")} value={title} onChange={setTitle} autoFocus={!hasTab} />
        </>}
        {/* the agent's hint is about herdr's tab and Claude's /rename: a session without a terminal (a runtime's) has neither */}
        {(session.assistant || sessionCan(session, "terminal")) && <p className="text-label leading-relaxed text-ink-3">{unified ? t("chat-sessiondialogs.rename_hint_unified", { engine }) : session.assistant ? t("chat-sessiondialogs.rename_hint_assistant") : t("chat-sessiondialogs.rename_hint_agent", { engine })}</p>}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

// "Rename space": the terminal engine's workspace (herdr's, an Orca worktree), or Paseo's project (and the pins / mutes
// that name it). The hint names the engine (#17053).
export function RenameSpaceDialog({ session, open, onClose }: { session: Session; open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) setName(session.space_name ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const next = name.trim();
  const save = async () => {
    if (busy) return; // a second Enter while the first rename is out
    if (!session.space_id || !next || next === session.space_name) return onClose();
    setBusy(true);
    try {
      await api(`/machines/${enc(session.machine_id)}/spaces/${enc(session.space_id)}/rename`, { method: "POST", json: { name: next } });
      void refresh().catch(() => {});
      toast(t("chat-sessiondialogs.space_renamed"), "ok");
      onClose();
    } catch (e) {
      toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.rename_space")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!next || next === session.space_name} onClick={() => void save()}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <Field label={t("chat-sessiondialogs.space_name")} value={name} onChange={setName} autoFocus />
        {session.space_kind !== "paseo" && <p className="text-label leading-relaxed text-ink-3">{t("chat-sessiondialogs.rename_space_hint", { engine: sessionEngineName(session) })}</p>}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function Field({ label, value, onChange, autoFocus }: { label: string; value: string; onChange: (v: string) => void; autoFocus?: boolean }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-label text-ink-3">{label}</span>
      <input
        value={value}
        autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)}
        className="h-9 rounded-control bg-surface px-3 text-meta text-ink shadow-btn outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]"
      />
    </label>
  );
}

// "Delete chat" (issue #801): the hub takes it out of every app's list; the transcript stays on the computer and the
// conversation comes back once it is used again (claude --resume).
export function DeleteSessionDialog({ session, machineName, open, onClose }: { session: Session; machineName: string; open: boolean; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const wt = useAgentWorktree(session, open && !isUntouchedStart(session));
  const [cleanup, setCleanup] = useState<Cleanup>(KEEP);
  useEffect(() => {
    if (open) setCleanup(KEEP); // each time: keeping is the default
  }, [open]);
  const go = async () => {
    setBusy(true);
    try {
      await api(sessionPath(session.machine_id, session.session_id), { method: "DELETE" });
      dropSession(session.machine_id, session.session_id);
      if (location.pathname === href({ name: "session", mid: session.machine_id, sid: session.session_id })) navigate({ name: "home" }, true);
      toast(t("chat-sessiondialogs.deleted"), "ok");
      onClose();
      await runCleanup(session.machine_id, wt, cleanup, session.host);
    } catch (e) {
      toast(t("chat-sessiondialogs.delete_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.delete_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} disabled={!cleanupReady(wt, cleanup)} onClick={go}>
            {t("chat-sessiondialogs.delete")}
          </Button>
        </>
      }
    >
      <p className="text-meta leading-relaxed text-ink-2">
        {t("chat-sessiondialogs.delete_detail", { title: sessionTitle(session), machine: machineName })}
      </p>
      {wt && <WorktreeCleanup st={wt} value={cleanup} onChange={setCleanup} />}
    </Dialog>
  );
}
