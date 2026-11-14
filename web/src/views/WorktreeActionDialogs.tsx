import { useEffect, useId, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { engineName } from "../lib/engines";
import type { WorkspaceTarget } from "../lib/workspace-target";
import type { WorktreeMutation } from "../lib/worktree-actions-state";
import { Button, Dialog, Loading } from "../ui/atoms";

interface Props { target: WorkspaceTarget; model: WorktreeMutation; onClose: () => void }
export function RenameWorktreeDialog({ target, name, model, onClose }: Props & { name: string }) {
  const [typed, setTyped] = useState(name);
  const state = useSyncExternalStore(model.subscribe, model.snapshot, model.snapshot);
  const field = useRef<HTMLInputElement>(null);
  const label = useId();
  const busy = state.phase === "renaming";
  const submit = () => { if (!busy && typed.trim()) void model.rename(typed); };
  useEffect(() => { field.current?.focus(); field.current?.select(); }, []);
  return <Dialog open title={t("worktree-actions.rename")} onClose={() => { if (!busy) onClose(); }} footer={<>
    <Button disabled={busy} onClick={onClose}>{t("worktree-actions.cancel")}</Button>
    <Button tone="primary" busy={busy} disabled={!typed.trim() || state.phase === "invalid"} onClick={submit}>{t("worktree-actions.save")}</Button>
  </>}>
    <form className="space-y-3 text-sm" onSubmit={(event) => { event.preventDefault(); submit(); }}>
      <p className="break-all text-ink-2">{target.cwd}</p>
      <label htmlFor={label} className="block text-ink">{t("worktree-actions.name")}</label>
      <input ref={field} id={label} value={typed} disabled={busy || state.phase === "invalid"} onChange={(event) => setTyped(event.target.value)}
        className="w-full min-w-0 rounded-control border border-line bg-inset px-3 py-2 text-ink outline-none focus:border-ink-3" />
      <p className="text-ink-2">{t("worktree-actions.rename_hint")}</p>
      {state.error && <p role="alert" className="whitespace-pre-wrap break-words text-red">{state.error}</p>}
    </form>
  </Dialog>;
}

export function DeleteWorktreeDialog({ target, machineName, model, onClose, closeOnly = false }: Props & { machineName?: string; closeOnly?: boolean }) {
  const state = useSyncExternalStore(model.subscribe, model.snapshot, model.snapshot);
  const [discard, setDiscard] = useState(false);
  const [deleteBranch, setDeleteBranch] = useState(false);
  const warning = useId();
  const info = state.preview;
  const busy = ["previewing", "deleting", "checking"].includes(state.phase);
  const invalid = state.phase === "invalid";
  const canRemove = state.phase === "ready" && !state.dispatched && Boolean(info && Boolean(info.close_only) === closeOnly && (info.folder || info.dirty === 0 || discard));
  useEffect(() => {
    const current = model.snapshot();
    if (!current.dispatched && (current.phase === "idle" || current.preview && Boolean(current.preview.close_only) !== closeOnly)) void model.preview(closeOnly);
  }, [model, closeOnly]);
  return <Dialog open title={t(closeOnly ? "worktree-actions.close_title" : info?.folder ? "worktree-actions.unregister" : "worktree-actions.delete_title")}
    onClose={() => { if (!busy) onClose(); }} footer={<>
      <Button disabled={busy} onClick={onClose}>{t(state.dispatched ? "worktree-actions.close" : "worktree-actions.cancel")}</Button>
      {state.dispatched ? <Button tone="primary" busy={busy} disabled={invalid || state.phase !== "uncertain"} onClick={() => void model.reconcile()}>{t("worktree-actions.check_status")}</Button>
        : <Button tone="danger" busy={busy} disabled={!canRemove} onClick={() => void model.remove(discard, deleteBranch)}>{t(closeOnly ? "worktree-actions.close_confirm" : info?.folder ? "worktree-actions.unregister_confirm" : "worktree-actions.delete_confirm")}</Button>}
    </>}>
    <div className="space-y-3 text-sm text-ink">
      <div className="space-y-1 rounded-control border border-line bg-inset p-3">
        <p className="break-words font-medium">{target.label || t("worktree-actions.worktree")}</p>
        <p className="break-words text-ink-2">{machineName || t("worktree-actions.computer")} · {engineName(target.host)}</p>
        <p className="break-all font-mono text-ink-2">{info?.path || target.cwd}</p>
      </div>
      {busy && <Loading label={t(state.phase === "checking" ? "worktree-actions.checking" : state.phase === "deleting" ? "worktree-actions.removing" : "worktree-actions.previewing")} />}
      {info && <>
        <p>{t("worktree-actions.sessions_end")}</p>
        <p>{t(closeOnly ? "worktree-actions.close_keeps_files" : info.folder ? "worktree-actions.folder_kept" : "worktree-actions.files_deleted")}</p>
        {!info.folder && <>
          {info.dirty > 0 && <div className="space-y-2 rounded-control border border-red/30 bg-inset p-3">
            <p id={warning} className="text-red">{t("worktree-actions.dirty", { count: info.dirty })}</p>
            <Choice checked={discard} disabled={busy || state.dispatched || invalid} onChange={setDiscard} describedBy={warning}>{t("worktree-actions.discard")}</Choice>
          </div>}
          {info.branch && <div className="space-y-2">
            <p className="break-all font-mono text-ink-2">{info.branch}</p>
            <Choice checked={deleteBranch} disabled={busy || state.dispatched || invalid} onChange={setDeleteBranch}>{t("worktree-actions.delete_branch")}</Choice>
          </div>}
          <p className="text-ink-2">{t("worktree-actions.keep_branch")}</p>
        </>}
      </>}
      {state.error && <p role="alert" className="whitespace-pre-wrap break-words text-red">{state.error}</p>}
      {!info && !busy && !invalid && <Button onClick={() => void model.preview()}>{t("worktree-actions.retry_preview")}</Button>}
    </div>
  </Dialog>;
}

function Choice({ checked, disabled, onChange, describedBy, children }: {
  checked: boolean; disabled: boolean; onChange: (value: boolean) => void; describedBy?: string; children: React.ReactNode;
}) {
  return <label className="flex cursor-pointer items-start gap-2">
    <input type="checkbox" className="mt-1 shrink-0 accent-accent-ink" checked={checked} disabled={disabled} aria-describedby={describedBy} onChange={(event) => onChange(event.target.checked)} />
    <span className="min-w-0 break-words">{children}</span>
  </label>;
}
