// The chat's ⋯ menu dialogs: session info, rename (the agent / its space), delete, close, and the header's permission mode.
import { useEffect, useRef, useState } from "react";
import { api, enc, errorText, HubTimeoutError, pastHub, sessionPath } from "../../lib/api";
import { ago, externalLink, resumeCommand, sessionCan, sessionTitle, shortPath } from "../../lib/format";
import { openLink } from "../../lib/links";
import { BYPASS_MODE, effortTitle, modeTitle, renameBody } from "../../lib/session-actions";
import { bypassRestarts } from "../../lib/header-mode";
import { sessionLauncher } from "../../lib/model-switch";
import { href, navigate } from "../../lib/router";
import { dropSession, getSession, refresh, refreshAssistant, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, copyText, cx, Dialog, Loading, Switch, toast } from "../../ui/atoms";
import { removeAgentWorktree, worktreeLoss, worktreeStatus, type WorktreeStatus } from "../../lib/worktree";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { AlwaysPermissions } from "./AlwaysPermissions";
import { DelegatedHistory } from "./Delegated";

interface SessionInfo {
  session_id?: string;
  transcript_path?: string;
  cwd?: string;
  model?: string;
  launcher?: string;
  git_branch?: string;
  version?: string;
  permission_mode?: string;
  effort?: string;
  context_tokens?: number;
  output_tokens?: number;
  compactions?: number;
  turns?: number;
  started_at?: string;
  last_activity?: string;
}

const count = (n?: number) => (n === undefined ? "" : n >= 1000 ? `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}K` : String(n));

// "Session info" (iOS SessionInfoSheet): how it runs, how much it used, where it lives; ids and the resume command copy.
export function SessionInfoDialog({ session, machineName, open, onClose }: { session: Session; machineName: string; open: boolean; onClose: () => void }) {
  const [info, setInfo] = useState<SessionInfo | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let live = true;
    setInfo(null);
    setError("");
    api<SessionInfo>(`${sessionPath(session.machine_id, session.session_id)}/info`)
      .then((r) => live && setInfo(r))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [open, session.machine_id, session.session_id]);

  const groups: { title: string; rows: [string, string | undefined, boolean?][] }[] = info
    ? [
        { title: t("chat-sessiondialogs.group_run"), rows: [[t("chat-sessiondialogs.model"), info.model], [t("chat-sessiondialogs.launcher"), info.launcher || session.launcher], [t("chat-sessiondialogs.permission_mode"), modeTitle(info.permission_mode)], [t("chat-sessiondialogs.effort"), effortTitle(info.effort)], [t("chat-sessiondialogs.version"), info.version, true]] },
        { title: t("chat-sessiondialogs.group_usage"), rows: [[t("chat-sessiondialogs.context"), count(info.context_tokens) && `${count(info.context_tokens)} tokens`], [t("chat-sessiondialogs.output"), count(info.output_tokens) && `${count(info.output_tokens)} tokens`], [t("chat-sessiondialogs.turns"), info.turns !== undefined ? String(info.turns) : ""], [t("chat-sessiondialogs.compaction"), info.compactions ? t("chat-sessiondialogs.compactions", { count: info.compactions }) : ""], [t("chat-sessiondialogs.started"), info.started_at && ago(info.started_at)], [t("chat-sessiondialogs.last_activity"), info.last_activity && ago(info.last_activity)]] },
        { title: t("chat-sessiondialogs.group_location"), rows: [[t("chat-sessiondialogs.machine"), machineName], [t("chat-sessiondialogs.folder"), info.cwd ? shortPath(info.cwd) : undefined, true], [t("chat-sessiondialogs.branch"), info.git_branch, true], [t("chat-sessiondialogs.transcript"), info.transcript_path ? shortPath(info.transcript_path) : undefined, true]] },
      ]
    : [];

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-sessiondialogs.info_title")}
      width={480}
      footer={
        <>
          <Button tone="quiet" icon="copy" onClick={() => copyText(session.session_id)}>
            {t("chat-sessiondialogs.copy_session_id")}
          </Button>
          {externalLink(session) && (
            <Button tone="secondary" icon="external" onClick={() => openLink(externalLink(session)!)}>
              {t("chat-sessiondialogs.open_in_lark")}
            </Button>
          )}
          {!session.assistant && (
            <Button tone="secondary" icon="terminal" onClick={() => copyText(resumeCommand(session))}>
              {t("chat-sessiondialogs.copy_resume_command")}
            </Button>
          )}
        </>
      }
    >
      {!info && !error && (
        <div className="flex justify-center py-6">
          <Loading />
        </div>
      )}
      {error && <p className="py-4 text-[13px] text-red">{error}</p>}
      <div className="flex flex-col gap-3">
        {open && session.assistant && <AlwaysPermissions key={session.session_id} session={session.session_id} />}
        {open && !session.assistant && <DelegatedHistory key={`${session.machine_id}/${session.session_id}`} session={session} />}
        {groups.map((g) => {
          const rows = g.rows.filter(([, v]) => v);
          if (!rows.length) return null;
          return (
            <section key={g.title}>
              <h3 className="mb-1 text-[11.5px] font-medium text-ink-3">{g.title}</h3>
              <div className="divide-y divide-line overflow-hidden rounded-[10px] bg-inset shadow-hairline">
                {rows.map(([k, v, mono]) => (
                  <div key={k} className="flex items-center gap-3 px-3 py-1.5 text-[12.5px]">
                    <span className="w-16 shrink-0 text-ink-3">{k}</span>
                    <span title={v} className={cx("min-w-0 flex-1 truncate text-ink", mono && "font-mono text-[12px]")}>
                      {v}
                    </span>
                  </div>
                ))}
              </div>
            </section>
          );
        })}
      </div>
    </Dialog>
  );
}

// "Rename": the herdr tab and the session's name (Claude's /rename), each only when changed (iOS RenameSessionAlert).
/** Saves a new title from the header's in-place rename (#5421, cindy's double-click on the title, which updates at once and
 *  puts the old title back if the save fails): only the title, the tab keeps its name. False when nothing changed or it
 *  failed (the error toasted). */
export async function renameTitle(session: Session, title: string): Promise<boolean> {
  const body = renameBody(session, "", title);
  if (!body?.title) return false;
  // the header and the list show the new name at once; the hub's push confirms it
  upsertSession({ ...session, title: body.title });
  try {
    await api(`${sessionPath(session.machine_id, session.session_id)}/rename`, { method: "POST", json: { title: body.title } });
    if (session.assistant) void refreshAssistant().catch(() => {});
    return true;
  } catch (e) {
    // back to the old title, unless something renamed it meanwhile
    const now = getSession(session.machine_id, session.session_id);
    if (!now || now.title === body.title) upsertSession({ ...(now ?? session), title: session.title });
    toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}

export function RenameDialog({ session, open, onClose }: { session: Session; open: boolean; onClose: () => void }) {
  const [tab, setTab] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!open) return;
    setTab(session.slot_name ?? "");
    setTitle(session.title ?? "");
    // only when it opens: a push while typing must not overwrite the field
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const hasTab = !session.assistant && Boolean(session.slot_id);
  const body = renameBody(session, tab, title);
  const save = async () => {
    // the form submits on Enter while the field stays editable: a second Enter would rename (type /rename) twice
    if (busy) return;
    if (!body) return onClose();
    setBusy(true);
    try {
      await api(`${sessionPath(session.machine_id, session.session_id)}/rename`, { method: "POST", json: body });
      // the list shows the new name at once; the hub's push confirms it
      upsertSession({ ...session, ...(body.title ? { title: body.title } : {}), ...(body.tab_name ? { slot_name: body.tab_name } : {}) });
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
        {hasTab && <Field label={t("chat-sessiondialogs.tab_name")} value={tab} onChange={setTab} autoFocus />}
        <Field label={t("chat-sessiondialogs.session_name")} value={title} onChange={setTitle} autoFocus={!hasTab} />
        {/* the agent's hint is about herdr's tab and Claude's /rename: a session without a terminal (a runtime's) has neither */}
        {(session.assistant || sessionCan(session, "terminal")) && <p className="text-[12px] leading-relaxed text-ink-3">{session.assistant ? t("chat-sessiondialogs.rename_hint_assistant") : t("chat-sessiondialogs.rename_hint_agent")}</p>}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

// "Rename space": herdr's workspace, or Paseo's project (and the pins / mutes that name it). The hint is herdr's.
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
        {session.space_kind !== "paseo" && <p className="text-[12px] leading-relaxed text-ink-3">{t("chat-sessiondialogs.rename_space_hint")}</p>}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function Field({ label, value, onChange, autoFocus }: { label: string; value: string; onChange: (v: string) => void; autoFocus?: boolean }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[12px] text-ink-3">{label}</span>
      <input
        value={value}
        autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)}
        className="h-9 rounded-control bg-surface px-3 text-[13px] text-ink shadow-btn outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]"
      />
    </label>
  );
}

/** The hub gives a mode call 90 seconds. Five more lets its own refusal arrive first; a lost response still ends Apply. */
const MODE_CONFIRM_MS = pastHub(90_000);

function modeChangeError(e: unknown) {
  toast(e instanceof HubTimeoutError ? t("chat-sessiondialogs.change_unconfirmed") : t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
}

/** POST …/modes with a patch, drawn at once and put back if it is refused (the error toasted). A timeout may mean the
 *  change ran but its response was lost, so it stays drawn for the session push. `latest` is the session as it is now:
 *  a pick applies the model and then the effort, and the second must build on the session the first changed. */
export async function applyModes(latest: () => Session, patch: { permission_mode?: string; effort?: string; model?: string }): Promise<boolean> {
  const base = latest();
  const before = { permission_mode: base.permission_mode, effort: base.effort, model: base.model, bypass_pending: base.bypass_pending };
  // Full access the agent reaches only by a restart (#6476): drawn as waiting for it, in the mode it still runs in;
  // any other mode drops a restart that waits
  const restart = patch.permission_mode === BYPASS_MODE && bypassRestarts(base);
  const drawn: Partial<Session> = restart ? { ...patch, permission_mode: base.permission_mode, bypass_pending: true } : patch.permission_mode ? { ...patch, bypass_pending: undefined } : patch;
  upsertSession({ ...base, ...drawn });
  try {
    await api(`${sessionPath(base.machine_id, base.session_id)}/modes`, { method: "POST", json: patch, timeout: MODE_CONFIRM_MS });
    return true;
  } catch (e) {
    if (!(e instanceof HubTimeoutError)) upsertSession({ ...latest(), ...Object.fromEntries(Object.keys(drawn).map((k) => [k, before[k as keyof typeof before]])) });
    modeChangeError(e);
    return false;
  }
}

/** applyModes for one session: the composer's permission picker and model pill, the header's mode. */
export function useModeSet(session: Session) {
  const latest = useRef(session);
  latest.current = session;
  const set = (patch: { permission_mode?: string; effort?: string; model?: string }): Promise<boolean> => applyModes(() => latest.current, patch);
  /** POST …/modes {launcher, from, model?, effort?} (#5035): another launcher records a switch that the user's next
   *  message carries out, the session's own drops one; `from` is the launcher the pick was made against, so one made on
   *  a session that switched meanwhile is refused (`switch_stale`). The pending switch shows at once (the pill's clock)
   *  and goes back to what it was if the hub says no, with its words. `agent`: the engine a launcher of the other engine
   *  runs (#5054), drawn as the node will record it (`pending_switch.agent`); it is not sent. */
  const switchLauncher = async (step: { launcher: string; from?: string; model?: string; effort?: string }, agent?: string): Promise<boolean> => {
    const base = latest.current;
    const before = base.pending_switch;
    const pending = { launcher: step.launcher, model: step.model, effort: step.effort, ...(agent ? { agent } : {}), at: new Date().toISOString() };
    const next: Session = { ...base, pending_switch: step.launcher === sessionLauncher(base) ? undefined : pending };
    upsertSession(next);
    latest.current = next; // a step after this one builds on it before the push draws it
    try {
      await api(`${sessionPath(base.machine_id, base.session_id)}/modes`, { method: "POST", json: step, timeout: MODE_CONFIRM_MS });
      return true;
    } catch (e) {
      if (!(e instanceof HubTimeoutError)) upsertSession({ ...latest.current, pending_switch: before });
      modeChangeError(e);
      return false;
    }
  };
  return { set, latest, switchLauncher };
}

/** POST …/modes {service_tier}: Codex's speed (the Speed submenu, the model pill). Nothing is drawn ahead of it: the
 *  session's push after the agent took it owns what is shown (DESIGN_SYSTEM.md "Codex speed controls"). */
export async function setServiceTier(s: Session, tier: string): Promise<boolean> {
  try {
    await api(`${sessionPath(s.machine_id, s.session_id)}/modes`, { method: "POST", json: { service_tier: tier }, timeout: MODE_CONFIRM_MS });
    return true;
  } catch (e) {
    modeChangeError(e);
    return false;
  }
}

// "Delete chat" (issue #801): the hub takes it out of every app's list; the transcript stays on the computer and the
// conversation comes back once it is used again (claude --resume).
export function DeleteSessionDialog({ session, machineName, open, onClose }: { session: Session; machineName: string; open: boolean; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const wt = useAgentWorktree(session, open);
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
      <p className="text-[13px] leading-relaxed text-ink-2">
        {t("chat-sessiondialogs.delete_detail", { title: sessionTitle(session), machine: machineName })}
      </p>
      {wt && <WorktreeCleanup st={wt} value={cleanup} onChange={setCleanup} />}
    </Dialog>
  );
}

// ── the worktree a closed or deleted agent worked in (#6115) ──

/** What to do with it: keep it (the default), or remove it, its branch too if asked; `confirmed` that what it loses goes. */
export interface Cleanup {
  remove: boolean;
  deleteBranch: boolean;
  confirmed: boolean;
}

export const KEEP: Cleanup = { remove: false, deleteBranch: false, confirmed: false };

/** The worktree the agent's folder is, when Persona made it; null otherwise (and while the computer is asked). */
export function useAgentWorktree(s: Session, open: boolean): WorktreeStatus | null {
  const [st, setSt] = useState<WorktreeStatus | null>(null);
  useEffect(() => {
    setSt(null);
    if (!open || !s.cwd) return;
    const c = new AbortController();
    // asked of the engine it ran in, which made it (a Paseo agent's is Paseo's)
    void worktreeStatus(s.machine_id, s.cwd, c.signal, s.host).then((x) => {
      if (!c.signal.aborted) setSt(x?.ours && x.path ? x : null);
    });
    return () => c.abort();
  }, [open, s.machine_id, s.cwd, s.host]);
  return st;
}

/** Removing it loses something the user has not confirmed yet: the button waits. */
export function cleanupReady(st: WorktreeStatus | null, c: Cleanup): boolean {
  if (!st || !c.remove) return true;
  const loss = worktreeLoss(st, c.deleteBranch);
  return c.confirmed || (loss.dirty === 0 && loss.unmerged === 0);
}

/** After the agent is closed or deleted: removes its worktree when asked, forced only for what the user confirmed, by
 *  the engine the agent ran in (kind: its host). */
export async function runCleanup(mid: string, st: WorktreeStatus | null, c: Cleanup, kind?: string) {
  if (!st?.path || !c.remove) return;
  const loss = worktreeLoss(st, c.deleteBranch);
  try {
    const r = await removeAgentWorktree(mid, st.path, { deleteBranch: c.deleteBranch, force: loss.dirty > 0 || loss.unmerged > 0, kind });
    toast(t(r.branch_deleted ? "chat-sessiondialogs.wt_removed_branch" : "chat-sessiondialogs.wt_removed"), "ok");
  } catch (e) {
    toast(t("chat-sessiondialogs.wt_remove_failed", { error: errorText(e) }), "error");
  }
}

/** Keep or remove the worktree, and what removing it loses (said with the counts, and to be confirmed). */
export function WorktreeCleanup({ st, value, onChange }: { st: WorktreeStatus; value: Cleanup; onChange: (c: Cleanup) => void }) {
  const loss = worktreeLoss(st, value.deleteBranch);
  const losing = value.remove && (loss.dirty > 0 || loss.unmerged > 0);
  const pick = (remove: boolean) => onChange({ ...value, remove, confirmed: false });
  return (
    <div data-worktree-cleanup className="mt-3 rounded-control bg-inset px-3 py-2.5">
      <div className="flex items-center gap-2 text-[12.5px] font-medium text-ink">
        <Icon name="branch" size={13} stroke={2.2} className="shrink-0 text-ink-2" />
        <span className="min-w-0 flex-1 truncate">{t("chat-sessiondialogs.wt_title")}</span>
      </div>
      <p className="mt-0.5 truncate pl-[21px] font-mono text-[11.5px] text-ink-3" title={st.path}>
        {st.branch ? `${st.branch} · ` : ""}
        {shortPath(st.path ?? "")}
      </p>
      <div role="radiogroup" aria-label={t("chat-sessiondialogs.wt_title")} className="mt-2 flex gap-1.5 pl-[21px]">
        {([false, true] as const).map((remove) => (
          <button
            key={String(remove)}
            type="button"
            role="radio"
            aria-checked={value.remove === remove}
            onClick={() => pick(remove)}
            className={cx("h-7 rounded-full px-3 text-[12px] font-medium whitespace-nowrap transition-colors", value.remove === remove ? "bg-surface text-ink shadow-btn" : "text-ink-2 hover:bg-hover")}
          >
            {t(remove ? "chat-sessiondialogs.wt_remove" : "chat-sessiondialogs.wt_keep")}
          </button>
        ))}
      </div>
      {value.remove && (
        <div className="mt-2 flex flex-col gap-1.5 pl-[21px]">
          {st.branch && (
            <div className="flex items-center gap-2 text-[12px] text-ink-2">
              <span className="min-w-0 flex-1">{t("chat-sessiondialogs.wt_delete_branch", { branch: st.branch })}</span>
              <Switch on={value.deleteBranch} onChange={(on) => onChange({ ...value, deleteBranch: on, confirmed: false })} label={t("chat-sessiondialogs.wt_delete_branch", { branch: st.branch })} />
            </div>
          )}
          {loss.dirty > 0 && <p className="text-[11.5px] leading-snug text-red" data-wt-dirty>{t("chat-sessiondialogs.wt_dirty", { count: loss.dirty })}</p>}
          {loss.unmerged > 0 && <p className="text-[11.5px] leading-snug text-red" data-wt-unmerged>{t("chat-sessiondialogs.wt_unmerged", { count: loss.unmerged, main: st.main_branch || "main" })}</p>}
          {losing && (
            <div className="flex items-center gap-2 text-[12px] font-medium text-ink">
              <span className="min-w-0 flex-1">{t("chat-sessiondialogs.wt_confirm_loss")}</span>
              <Switch on={value.confirmed} onChange={(on) => onChange({ ...value, confirmed: on })} label={t("chat-sessiondialogs.wt_confirm_loss")} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
