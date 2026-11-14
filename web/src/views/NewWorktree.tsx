import { useEffect, useId, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { t } from "../i18n";
import { ApiError, credentialOwner, errorText, subscribeCredentialOwner } from "../lib/api";
import { useKnownDefault } from "../lib/manager-choice";
import { openNewAgent } from "../lib/new-agent";
import { useStore } from "../lib/store";
import { useWorkspaceCatalog, workspaceTarget } from "../lib/workspace-catalog";
import type { WorkspaceTarget } from "../lib/workspace-target";
import { createdWorkspace, creationSession, repositoryDraft, samePlacement, sourceKey, unsupportedWorktreeParent, type WorktreeCreationSession } from "../lib/worktree-creation";
import { openCreated, prepareWorktree, saveWorktreePreset, suggestedPath, typeBranch, typePath, worktreeAdmission,
  worktreeProblem, worktreeSources, WORKTREE_COPY, type WorktreePrepare, type WorktreeRequest, type WorktreeSource, type WorktreeTarget } from "../lib/worktree";
import { Button, cx, Dialog, Spinner } from "../ui/atoms";

export function NewWorktree({ request, onClose }: { request: WorktreeRequest; onClose: () => void }) {
  const [loaded, setLoaded] = useState<{ request: WorktreeRequest; session: WorktreeCreationSession }>();
  const session = loaded?.request === request ? loaded.session : undefined;
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    creationSession(request).then((value) => { if (active) setLoaded({ request, session: value }); }, (e) => { if (active) setError(errorText(e)); });
    return () => { active = false; };
  }, [request]);
  return session ? <Composer session={session} request={request} onClose={onClose} /> :
    <Dialog open title={t("worktree.dialog_title")} onClose={onClose} footer={<Button onClick={onClose}>{t("common.cancel")}</Button>}>
      {error ? <p role="alert" className="text-sm text-red">{error}</p> : <p role="status" className="flex gap-2 text-sm text-ink-3"><Spinner />{WORKTREE_COPY.reading}</p>}
    </Dialog>;
}

type Read = { key: string; info?: WorktreePrepare; error?: string; basicAvailable?: boolean };
function Composer({ session, request, onClose }: { session: WorktreeCreationSession; request: WorktreeRequest; onClose: () => void }) {
  const { draft, intent, storageError } = useSyncExternalStore(session.subscribe, session.getSnapshot, session.getSnapshot);
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  useKnownDefault();
  const machines = useStore((s) => s.machines);
  const catalog = useWorkspaceCatalog(true);
  const source = draft.source;
  const key = sourceKey(source);
  const [read, setRead] = useState<Read>({ key });
  const [readVersion, setReadVersion] = useState(0);
  const [error, setError] = useState("");
  const [providerQueries, setProviderQueries] = useState<Record<string, string>>({});
  const providerQuery = providerQueries[key] ?? "";
  const setProviderQuery = (value: string) => {
    searchGeneration.current++; setSearching(false);
    setProviderQueries((old) => ({ ...old, [key]: value }));
    setProviderRead({ key, items: [] });
  };
  const [providerRead, setProviderRead] = useState<{ key: string; items: WorktreeSource[]; message?: string }>({ key, items: [] });
  const [searching, setSearching] = useState(false);
  const [presetName, setPresetName] = useState("");
  const [directories, setDirectories] = useState("");
  const [savingPreset, setSavingPreset] = useState(false);
  const alive = useRef(true);
  const sourceGeneration = useRef(0);
  const searchGeneration = useRef(0);
  const notified = useRef("");
  const launched = useRef(false);
  const validOwner = owner === source.owner && worktreeAdmission(source);
  const locked = Boolean(intent && intent.phase !== "refused");
  const sending = intent?.phase === "sending";
  const info = read.key === key ? read.info : undefined;
  const repo = repositoryDraft(draft);
  const composer = source.host === "orca" && !repo.basic ? info?.composer : undefined;
  const has = (cap: string) => Boolean(composer?.capabilities.includes(cap));
  const actual = intent && createdWorkspace(intent);
  const machine = machines.find((m) => m.id === source.mid);
  const offline = machine?.online === false;
  // Paseo's workspaces have no terminal to open (docs/integrations/paseo.md): an Agent in it is what opening it means.
  const agentOnly = source.host === "paseo";
  const startAgent = () => {
    if (actual && !launched.current && worktreeAdmission(source)) { launched.current = true; openNewAgent({ workspace: actual }); onClose(); }
  };

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; sourceGeneration.current++; searchGeneration.current++; };
  }, []);
  useEffect(() => {
    const generation = ++sourceGeneration.current;
    const controller = new AbortController();
    if (!validOwner) return () => controller.abort();
    prepareWorktree(source, { composer: !repo.basic, signal: controller.signal }).then((value) => {
      if (!controller.signal.aborted && generation === sourceGeneration.current && worktreeAdmission(source)) setRead({ key, info: value });
    }, (e) => {
      if (!controller.signal.aborted && generation === sourceGeneration.current && worktreeAdmission(source)) setRead({ key, error: worktreeProblem(e, WORKTREE_COPY.readFailed).text,
        basicAvailable: !repo.basic && e instanceof ApiError && e.status !== 401 && e.status !== 403
          && (worktreeProblem(e, WORKTREE_COPY.readFailed).needsUpdate || e.message.includes(t("worktree-composer.update_composer"))) });
    });
    return () => controller.abort();
  }, [source, key, readVersion, validOwner, repo.basic]);
  useEffect(() => {
    if (!intent?.created || !actual || !validOwner || notified.current === intent.id) return;
    notified.current = intent.id;
    request.changed();
    request.onCreated?.(intent.created, actual);
  }, [intent, actual, validOwner, request]);

  const catalogRows = catalog.owner === owner && catalog.manager === source.manager ? catalog.workspaces : [];
  const gitRows = catalogRows.filter((w) => ["orca", "herdr", "paseo"].includes(w.source) && !isFolder(w.workspace_kind) && w.available !== false && w.online !== false);
  const choices: { row: import("../lib/workspace-catalog").HostWorkspace; target: WorkspaceTarget }[] = gitRows.map((w) => ({ row: w, target: workspaceTarget(w, catalog.owner, catalog.manager) }));
  if (!choices.some((c) => sourceKey(toSource(c.target)) === key)) choices.unshift({ row: { id: "", machine_id: source.mid, source: source.host || "", source_id: source.workspaceId,
    title: source.label || info?.repoName || source.cwd || "", cwd: source.cwd || "", project_name: composer?.project || info?.repoName },
    target: { machineId: source.mid, host: source.host || "", workspaceId: source.workspaceId, cwd: source.cwd || "", label: source.label, owner: source.owner, manager: source.manager } });
  const sourceRow = choices.find((c) => sourceKey(toSource(c.target)) === key)?.row;
  const projectOf = (row: typeof sourceRow) => row?.project_id || row?.project_path || row?.project_name || row?.cwd || "";
  const project = projectOf(sourceRow);
  const availableMachines = machines.filter((m) => choices.some((c) => c.target.machineId === m.id));
  const hosts = [...new Set(choices.filter((c) => c.target.machineId === source.mid).map((c) => c.target.host))];
  const sameHost = choices.filter((c) => c.target.machineId === source.mid && c.target.host === (source.host || ""));
  const projects = [...new Map(sameHost.map((c) => [projectOf(c.row), c.row])).entries()];
  const parents: WorkspaceTarget[] = catalogRows.filter((w) => w.machine_id === source.mid && w.source === source.host
    && (!isFolder(w.workspace_kind) || has("folder_parent"))).map((w) => workspaceTarget(w, catalog.owner, catalog.manager));
  if (draft.parent && !parents.some((p) => p.workspaceId === draft.parent?.workspaceId && samePlacement(source, p)) && samePlacement(source, draft.parent)) parents.unshift(draft.parent);
  const parentMismatch = draft.parent && !samePlacement(source, draft.parent);
  const folderParent = draft.parent && catalogRows.some((w) => w.machine_id === draft.parent!.machineId && w.source === draft.parent!.host && w.source_id === draft.parent!.workspaceId && isFolder(w.workspace_kind));
  const unsupportedParent = folderParent && info && !has("folder_parent");

  function selectSource(next: WorkspaceTarget) {
    sourceGeneration.current++; searchGeneration.current++;
    setSearching(false); setSavingPreset(false); setError("");
    session.update({ source: toSource(next) });
  }
  const submit = () => {
    if (!validOwner || sending || offline || unsupportedParent || parentMismatch || intent?.phase === "created") return;
    setError("");
    try { void session.submit(info).catch((e) => { if (alive.current) setError(errorText(e)); }); }
    catch (e) { setError(errorText(e)); }
  };
  async function search() {
    if (!has("source.github") || !validOwner || locked) return;
    const generation = ++searchGeneration.current;
    setSearching(true);
    try {
      const result = await worktreeSources(source, providerQuery);
      if (alive.current && generation === searchGeneration.current && worktreeAdmission(source)) setProviderRead({ key, items: result.items,
        message: [result.warning, ...Object.values(result.errors ?? {}).map((item) => typeof item === "string" ? item : item.message)].filter(Boolean).join(" · ") });
    } catch (e) {
      if (alive.current && generation === searchGeneration.current) setProviderRead({ key, items: [], message: errorText(e) });
    } finally { if (alive.current && generation === searchGeneration.current) setSearching(false); }
  }
  async function savePreset() {
    if (!has("sparse_presets") || savingPreset || !presetName.trim() || !directories.trim() || locked) return;
    const generation = sourceGeneration.current;
    setSavingPreset(true); setError("");
    try {
      const result = await saveWorktreePreset(source, presetName.trim(), directories);
      if (!alive.current || generation !== sourceGeneration.current || !worktreeAdmission(source)) return;
      // Patch the same option snapshot, preserving every edited field and the selected ref.
      setRead((old) => old.key === key && old.info?.composer ? { ...old, info: { ...old.info, composer: { ...old.info.composer,
        presets: [...old.info.composer.presets.filter((p) => p.id !== result.preset.id), result.preset] } } } : old);
      session.updateRepository({ preset: result.preset.id });
    } catch (e) { if (alive.current && generation === sourceGeneration.current) setError(errorText(e)); }
    finally { if (alive.current && generation === sourceGeneration.current) setSavingPreset(false); }
  }

  return <Dialog open onClose={onClose} title={intent?.created ? t("worktree.created") : t("worktree.dialog_title")}
    footer={<>
      <Button tone="quiet" onClick={onClose}>{t(intent?.created ? "worktree-composer.done" : "common.cancel")}</Button>
      {intent?.created ? agentOnly ? <Button tone="primary" disabled={!validOwner || launched.current} onClick={startAgent}>{t("worktree-composer.start_agent")}</Button> :
        <Button tone="primary" disabled={!validOwner} onClick={() => { openCreated(source.mid, intent.created!); onClose(); }}>{t("worktree-composer.open")}</Button> :
        <Button tone="primary" busy={sending} disabled={!validOwner || offline || (!info && !intent) || Boolean(read.error && !intent) || Boolean(parentMismatch || unsupportedParent)} onClick={submit}>
          {t(locked ? "worktree-composer.check_result" : "worktree-composer.create")}</Button>}
    </>}>
    {!validOwner ? <Notice error>{t("worktree.owner_changed")}</Notice> : intent?.created && actual ? <div className="flex flex-col gap-3 text-sm">
      <p className="font-medium text-ink">{actual.label}</p>
      <p className="break-all font-mono text-ink-2">{intent.created.path}</p>
      <p className="break-all text-ink-2">{intent.created.branch}</p>
      <p className="text-ink-3">{t("worktree-composer.created_help")}</p>
      {!agentOnly && <Button disabled={launched.current} onClick={startAgent}>{t("worktree-composer.start_agent")}</Button>}
      <Button tone="quiet" onClick={() => { notified.current = ""; session.fresh(); }}>{t("worktree-composer.create_another")}</Button>
    </div> : <form className="flex flex-col gap-3 text-sm text-ink-2" onSubmit={(e) => { e.preventDefault(); submit(); }}>
      <fieldset disabled={locked || !validOwner} className="flex min-w-0 flex-col gap-3">
        <div className="grid grid-cols-2 gap-3">
          <Select label={t("worktree-composer.machine")} value={source.mid} onChange={(v) => { const choice = choices.find((c) => c.target.machineId === v); if (choice) selectSource(choice.target); }}>
            {availableMachines.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
            {!availableMachines.length && <option value={source.mid}>{source.label || source.cwd || t("worktree-composer.current_machine")}</option>}
          </Select>
          <Select label={t("worktree-composer.engine")} value={source.host || ""} onChange={(v) => { const choice = choices.find((c) => c.target.machineId === source.mid && c.target.host === v); if (choice) selectSource(choice.target); }}>
            {hosts.map((h) => <option key={h} value={h}>{h || t("worktree-composer.default_engine")}</option>)}
          </Select>
        </div>
        <Select label={t("worktree-composer.project")} value={project} onChange={(v) => { const choice = sameHost.find((c) => projectOf(c.row) === v); if (choice) selectSource(choice.target); }}>
          {projects.map(([id, row]) => <option key={id} value={id}>{row.project_name || row.title || row.cwd}</option>)}
        </Select>
        <Select label={t("worktree-composer.git_source")} value={key} onChange={(v) => { const choice = sameHost.find((c) => sourceKey(toSource(c.target)) === v); if (choice) selectSource(choice.target); }}>
          {sameHost.filter((c) => projectOf(c.row) === project).map((c) => <option key={sourceKey(toSource(c.target))} value={sourceKey(toSource(c.target))}>{c.row.title || c.row.branch || c.row.cwd}</option>)}
        </Select>
        {source.cwd && <p className="break-all text-xs text-ink-3">{source.cwd}</p>}
        {!info ? <Notice error={Boolean(read.key === key && read.error)}>{read.key === key && read.error || WORKTREE_COPY.reading}</Notice> : <>
          {composer ? <>
            <Select label={t("worktree-composer.base")} value={repo.baseRef || composer.base_ref} onChange={(v) => session.updateRepository({ baseRef: v })}>
              {[...new Set([composer.base_ref, ...composer.refs, ...(repo.baseRef ? [repo.baseRef] : [])])].map((ref) => <option key={ref} value={ref}>{ref}</option>)}
            </Select>
            <Input label={t("worktree-composer.name")} value={draft.name} onChange={(name) => session.update({ name })} placeholder={t("worktree-composer.generated")} hint={t("worktree-composer.location_help")} />
            {has("source.github") && <details><summary className="cursor-pointer py-1 text-accent">{t("worktree-composer.github")}</summary>
              <div className="mt-2 flex flex-col gap-2">
                <Input label={t("worktree-composer.search_source")} value={providerQuery} onChange={setProviderQuery} onEnter={() => void search()} />
                <Button busy={searching} onClick={() => void search()}>{t("worktree-composer.search")}</Button>
                {providerRead.key === key && providerRead.message && <Notice error>{providerRead.message}</Notice>}
                {providerRead.key === key && providerRead.items.map((item) => <Button key={`${item.type}:${item.number}`} onClick={() => { searchGeneration.current++; setSearching(false); session.updateRepository({ provider: item }); }}>
                  <span className="truncate">{`#${item.number} · ${item.title}`}</span>
                </Button>)}
              </div>
            </details>}
            {repo.provider && <div className="flex min-w-0 items-center gap-2"><span className="min-w-0 flex-1 truncate">{`#${repo.provider.number} · ${repo.provider.title}`}</span><Button tone="quiet" onClick={() => session.updateRepository({ provider: undefined })}>{t("worktree-composer.clear")}</Button></div>}
            <details><summary className="cursor-pointer py-1 text-accent">{t("worktree-composer.advanced")}</summary><div className="mt-2 flex flex-col gap-3">
              {has("branch_override") && <Input label={t("worktree-composer.branch")} value={draft.branch} onChange={(branch) => session.update({ branch })} hint={t("worktree-composer.branch_help")} />}
              {!has("branch_override") && draft.branch && <div className="flex flex-col gap-2"><Notice error>{t("worktree-composer.unsupported")}</Notice><Button tone="quiet" onClick={() => session.update({ branch: "" })}>{t("worktree-composer.clear_branch")}</Button></div>}
              {parentField()}
              <Input label={t("worktree-composer.note")} multiline value={draft.note} onChange={(note) => session.update({ note })} />
              {(has("sparse_presets") || repo.preset) && <>
                <Select label={t("worktree-composer.sparse")} value={repo.preset} onChange={(preset) => session.updateRepository({ preset })}>
                  <option value="">{t("worktree-composer.full_checkout")}</option>
                  {composer.presets.map((p) => <option key={p.id} value={p.id}>{p.name || p.directories.join(", ")}</option>)}
                </Select>
                {has("sparse_presets") && <details><summary className="cursor-pointer text-accent">{t("worktree-composer.new_preset")}</summary><div className="mt-2 flex flex-col gap-2">
                  <Input label={t("worktree-composer.preset_name")} value={presetName} onChange={setPresetName} />
                  <Input label={t("worktree-composer.directories")} multiline value={directories} onChange={setDirectories} hint={t("worktree-composer.directories_help")} />
                  <Button busy={savingPreset} disabled={!presetName.trim() || !directories.trim()} onClick={() => void savePreset()}>{t("worktree-composer.save_preset")}</Button>
                </div></details>}
              </>}
            </div></details>
            <p className="text-xs text-ink-3">{t("worktree-composer.agent_help")}</p>
          </> : <>
            {/* only Orca has the advanced options an update would bring */}
            {source.host === "orca" && <Notice>{t("worktree-composer.legacy")}</Notice>}
            <p className="truncate text-xs text-ink-3" title={info.root}>{info.branch ? t("worktree.repo_on_branch", { repo: info.repoName, branch: info.branch }) : t("worktree.repo_detached", { repo: info.repoName })}</p>
            {repo.basic && <Notice>{t("worktree-composer.basic_help")}</Notice>}
            {(draft.name || draft.note || repo.baseRef || repo.preset || repo.provider || unsupportedWorktreeParent(draft, info)) && <Button tone="quiet" onClick={() => {
              session.update({ name: "", note: "", ...(!info.choose_parent ? { parent: undefined } : {}) }); session.updateRepository({ baseRef: "", preset: "", provider: undefined });
            }}>{t("worktree-composer.clear_advanced")}</Button>}
            {info.automatic_name && <Select label={t("worktree-composer.naming")} value={draft.automaticName ? "auto" : "manual"} onChange={(v) => session.update({ automaticName: v === "auto" })}>
              <option value="manual">{t("worktree-composer.manual")}</option><option value="auto">{t("worktree-composer.generated")}</option>
            </Select>}
            {!draft.automaticName && <>
              <Input label={t("worktree.branch_label")} placeholder={t("worktree.branch_placeholder")} value={draft.branch} onChange={(v) => session.update(typeBranch(draft, info.path, v))} />
              <Input label={t("worktree.path_label")} value={draft.pathEdited ? draft.path : suggestedPath(info.path, draft.branch)} onChange={(v) => session.update(typePath(draft, info.path, v))} hint={t("worktree.path_hint")} />
            </>}
            {info.choose_parent && parentField()}
            <p className="text-xs text-ink-3">{t("worktree.note")}</p>
          </>}
        </>}
      </fieldset>
      {unsupportedParent && <Notice error>{t("worktree-composer.unsupported")}</Notice>}
      {offline && <Notice error>{t("worktree-composer.offline")}</Notice>}
      {parentMismatch && <Notice error>{t("worktree-composer.parent_mismatch")}</Notice>}
      {read.key === key && read.basicAvailable && !locked && <Button onClick={() => {
        setRead({ key }); setError(""); session.updateRepository({ basic: true });
      }}>{t("worktree-composer.use_basic")}</Button>}
      {read.key === key && read.error && !locked && <Button onClick={() => { setRead({ key }); setReadVersion((v) => v + 1); }}>{t("worktree-composer.retry")}</Button>}
      {locked && <Notice>{t(intent?.phase === "pending" || sending ? "worktree-composer.pending" : "worktree.unknown")}</Notice>}
      {(error || storageError || intent?.error) && <Notice error>{error || storageError || intent?.error}</Notice>}
      <button type="submit" hidden />
    </form>}
  </Dialog>;

  function parentField() {
    return <Select label={t("worktree-composer.parent")} value={draft.parent === null ? "none" : draft.parent ? `parent:${draft.parent.workspaceId}` : "source"}
      hint={t("worktree-composer.parent_help")} onChange={(v) => session.update({ parent: v === "none" ? null : v === "source" ? undefined : parents.find((p) => `parent:${p.workspaceId}` === v) })}>
      <option value="source">{t("worktree-composer.source_parent")}</option><option value="none">{t("worktree-composer.no_parent")}</option>
      {parentMismatch && <option value={`parent:${draft.parent!.workspaceId}`}>{draft.parent!.label || draft.parent!.cwd}</option>}
      {parents.map((p) => <option key={p.workspaceId} value={`parent:${p.workspaceId}`}>{p.label || p.cwd}</option>)}
    </Select>;
  }
}

const isFolder = (kind?: string) => kind === "folder" || kind === "folder-workspace";
const toSource = (target: WorkspaceTarget): WorktreeTarget => ({ mid: target.machineId, workspaceId: target.workspaceId, host: target.host,
  label: target.label, cwd: target.cwd, owner: target.owner, manager: target.manager });
const control = "min-h-9 w-full min-w-0 rounded-control bg-field px-3 py-2 text-sm text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]";
function Notice({ error, children }: { error?: boolean; children: ReactNode }) { return <p role={error ? "alert" : "status"} className={cx("break-words text-sm", error ? "text-red" : "text-ink-3")}>{children}</p>; }
function Select({ label, value, onChange, children, hint }: { label: string; value: string; onChange: (value: string) => void; children: ReactNode; hint?: string }) {
  const id = useId();
  return <div className="min-w-0"><label className="mb-1 block text-xs font-medium text-ink-2" htmlFor={id}>{label}</label>
    <select id={id} className={control} value={value} aria-describedby={hint ? `${id}-hint` : undefined} onChange={(e) => onChange(e.target.value)}>{children}</select>
    {hint && <p id={`${id}-hint`} className="mt-1 text-xs text-ink-3">{hint}</p>}
  </div>;
}
function Input({ label, value, onChange, placeholder, hint, multiline, onEnter }: { label: string; value: string; onChange: (value: string) => void; placeholder?: string; hint?: string; multiline?: boolean; onEnter?: () => void }) {
  const id = useId();
  const props = { id, className: control, value, placeholder, "aria-describedby": hint ? `${id}-hint` : undefined,
    autoComplete: "off", spellCheck: false, onKeyDown: (e: React.KeyboardEvent) => { if (e.key === "Enter" && onEnter) { e.preventDefault(); onEnter(); } } };
  return <div className="min-w-0"><label className="mb-1 block text-xs font-medium text-ink-2" htmlFor={id}>{label}</label>
    {multiline ? <textarea {...props} rows={3} onChange={(e) => onChange(e.target.value)} /> : <input {...props} autoCapitalize="off" onChange={(e) => onChange(e.target.value)} />}
    {hint && <p id={`${id}-hint`} className="mt-1 text-xs text-ink-3">{hint}</p>}
  </div>;
}
