import { useId, useRef, useState } from "react";
import "./WorkspaceNaming.css";
import { t } from "../i18n";
import { api, ApiError } from "../lib/api";
import { useStore } from "../lib/store";
import { loadTaskRepositories, repositoryRequestOptions, type RepositoryLayout, type TaskRepository } from "../lib/task-repositories";
import { useRepositoryRequestContext } from "../lib/use-repository-request-context";
import { loadWorkspaceRules, previewWorkspaceNaming, RULE_FIELDS, rulesPatch, sameRules, saveWorkspaceRules, WORKSPACE_NAMING_VERSION,
  type NamingDiagnostic, type WorkspaceNamingPreview as Preview, type WorkspaceNamingSettings, type WorkspaceRules } from "../lib/workspace-naming";
import { Group, settingsInputClass } from "../views/settings/SettingsParts";
import { Icon } from "./icons";
import { Button, Loading } from "./atoms";

function diagnosticCode(diagnostic: NamingDiagnostic, sources: Preview["branch_sources"]) {
  if (diagnostic.code === "field_unavailable" && diagnostic.field === "branch_template" &&
      sources?.some((source) => source.repo_id === diagnostic.repo_id && source.kind === "repository_default")) {
    return "default_branch_unavailable";
  }
  return diagnostic.code;
}

/** An empty field removes its override; placeholders describe the current fallback without materializing it. */
export function WorkspaceRulesEditor({ value, inherited, defaultSource = "system", disabled, diagnostics = [], branchSources, onChange }: {
  value: WorkspaceRules; inherited: WorkspaceRules; defaultSource?: "system" | "assistant";
  disabled?: boolean; diagnostics?: NamingDiagnostic[]; branchSources?: Preview["branch_sources"]; onChange: (value: WorkspaceRules) => void;
}) {
  const id = useId();
  return <div className="space-y-3">
    {RULE_FIELDS.map((field) => {
      const error = diagnostics.find((d) => d.field === field && d.severity === "error");
      const fallback = inherited[field];
      const placeholder = fallback ? t(`workspace-naming.default_${defaultSource}`, { value: fallback }) : "";
      return <div key={field} className="workspace-naming-field">
        <label htmlFor={`${id}-${field}`} className="text-label font-medium text-ink">{t(`workspace-naming.${field}`)}</label>
        <div className="min-w-0 space-y-1">
          <div className="workspace-naming-input">
            <input id={`${id}-${field}`} className={`${settingsInputClass} min-w-0 font-mono pointer-coarse:min-h-11`} spellCheck={false} disabled={disabled}
              value={value[field] ?? ""} placeholder={placeholder} onChange={(e) => {
                const next = { ...value };
                if (e.target.value === "") delete next[field]; else next[field] = e.target.value;
                onChange(next);
              }} aria-invalid={!!error} aria-describedby={`${id}-${field}-help ${id}-${field}-default${error ? ` ${id}-${field}-error` : ""}`} />
            <span id={`${id}-${field}-default`}>{placeholder}</span>
          </div>
          <p id={`${id}-${field}-help`} className="break-words text-label leading-relaxed text-ink-3">{t(`workspace-naming.help_${field}`)}</p>
          {error && <p id={`${id}-${field}-error`} role="alert" className="break-words text-label text-red">{t(`workspace-naming.${diagnosticCode(error, branchSources)}`, { field: t(`workspace-naming.${field}`) })}</p>}
        </div>
      </div>;
    })}
  </div>;
}

type BranchSource = NonNullable<Preview["branch_sources"]>[number];

function sourceLabel(source?: BranchSource) {
  switch (source?.kind) {
    case "repository_default": return t("workspace-naming.from_default");
    case "explicit_branch": return t("workspace-naming.from_branch", { branch: source.branch ?? "" });
    case "non_branch_ref": return t("workspace-naming.from_ref");
  }
}

/** Render the node's plan and branch inputs, including diagnostics when it could not resolve a plan. */
export function WorkspaceNamingPreview({ result, loading, error, repositories = [] }: {
  result?: Preview; loading?: boolean; error?: string; repositories?: TaskRepository[];
}) {
  const plan = result?.plan;
  const repoName = (id: string) => repositories.find((r) => r.repo_id === id)?.source_snapshot.identity.path
    ?? plan?.source.repository_snapshot.repositories.find((r) => r.repo_id === id)?.source_snapshot.identity.path ?? id;
  return <div aria-live="polite" className="min-w-0 space-y-2 text-label text-ink-2">
    {loading && <Loading label={t("workspace-naming.previewing")} />}
    {error && <p role="alert" className="text-red">{error}</p>}
    {result && <>
      <p className="font-medium text-ink">{t(result.sample ? "workspace-naming.sample" : "workspace-naming.preview_title")}</p>
      {result.diagnostics.map((d, index) => <p key={index} className={d.severity === "error" ? "text-red" : "text-ink-2"}>
        {d.repo_id && <span className="font-medium">{repoName(d.repo_id)} · </span>}
        {t(`workspace-naming.${diagnosticCode(d, result.branch_sources)}`, { field: RULE_FIELDS.includes(d.field as typeof RULE_FIELDS[number]) ? t(`workspace-naming.${d.field}`) : t("workspace-naming.title") })}
      </p>)}
      {plan && <div className="min-w-0 space-y-2 rounded-control bg-inset px-3 py-2.5">
        <p className="text-meta text-ink-3">{t("workspace-naming.result_folder")}</p>
        <p data-naming-root className="break-all font-mono text-label text-ink">{plan.root_path}</p>
        {plan.repositories?.length ? <ul className="space-y-2 border-t border-line pt-2">
          {plan.repositories.map((repo) => {
            const source = sourceLabel(result.branch_sources?.find((row) => row.repo_id === repo.repo_id));
            return <li key={repo.repo_id} data-naming-repo className="min-w-0 space-y-1">
              <p className="flex flex-wrap items-baseline gap-x-2">
                <span data-naming-folder className="break-all font-medium text-ink">{repo.relative_path === "." ? repoName(repo.repo_id) : repo.relative_path}</span>
                <span className="text-meta text-ink-3">{t("workspace-naming.result_branch")}</span>
                <span data-naming-branch className="break-all font-mono text-label">{repo.task_branch}</span>
              </p>
              {source && <p className="break-words text-meta text-ink-3">{source}</p>}
            </li>;
          })}
        </ul> : <p className="text-meta text-ink-3">{t("workspace-naming.folder_only")}</p>}
      </div>}
    </>}
  </div>;
}

export function WorkspaceNamingSettingsGroup({ version, manager, owner }: { version: string; manager: string; owner: string }) {
  if (version !== WORKSPACE_NAMING_VERSION) return null;
  return <Group><WorkspaceNaming manager={manager} owner={owner} /></Group>;
}

/** Secondary advanced area. It changes no creation mode and never starts an Agent. */
export function WorkspaceNaming({ manager, owner, todoId }: { manager: string; owner: string; todoId?: string }) {
  return <WorkspaceNamingSession key={`${owner}:${manager}:${todoId ?? "settings"}`} manager={manager} owner={owner} todoId={todoId} />;
}
function WorkspaceNamingSession({ manager, owner, todoId }: { manager: string; owner: string; todoId?: string }) {
  const context = useRepositoryRequestContext(manager, owner);
  const machines = useStore((s) => s.machines);
  const id = useId();
  const [optionsOpen, setOptionsOpen] = useState(false);
  const [open, setOpen] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [supported, setSupported] = useState(false);
  const [draft, setDraft] = useState<WorkspaceRules>({});
  const [saved, setSaved] = useState<WorkspaceRules>({});
  const [inherited, setInherited] = useState<WorkspaceRules>({});
  const [revision, setRevision] = useState(0);
  const [repositories, setRepositories] = useState<TaskRepository[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [machine, setMachine] = useState("");
  const [layout, setLayout] = useState<RepositoryLayout>("container");
  const [busy, setBusy] = useState(false);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Preview>();
  const sequence = useRef(0);
  const saving = useRef(false);
  const failure = (error: unknown, fallback: string) => {
    if (error instanceof ApiError && error.code?.startsWith("workspace_naming_")) {
      setResult({ status: "invalid", sample: !todoId, diagnostics: [{ code: error.code.slice("workspace_naming_".length), field: typeof error.body.field === "string" ? error.body.field : "workspace_rules", severity: "error" }] });
      setError(error.status === 400 ? t("workspace-naming.check_fields") : fallback);
      return;
    }
    setError(fallback);
  };
  const invalidate = () => { sequence.current++; setResult(undefined); setPreviewBusy(false); setError(""); };
  const refresh = async (keepDraft: boolean) => {
    const info = await api<WorkspaceNamingSettings>("/assistant", repositoryRequestOptions(context));
    if (!context.isCurrent()) return;
    if (info.manager !== manager || info.workspace_naming_version !== WORKSPACE_NAMING_VERSION) { setLoaded(true); setSupported(false); return; }
    let rules = info.workspace_rules ?? {};
    if (todoId) {
      const [record, set] = await Promise.all([loadWorkspaceRules(context, todoId), loadTaskRepositories(context, todoId)]);
      if (!context.isCurrent()) return;
      rules = record.workspace_rules; setRevision(record.revision); setRepositories(set.repositories);
      const available = set.repositories.map((r) => r.repo_id);
      // A removed repository must not survive invisibly in the next preview request.
      setSelected((current) => keepDraft ? current.filter((id) => available.includes(id)) : available);
    }
    invalidate();
    if (!keepDraft) setDraft(rules);
    // The freshly fetched rules are the dirty-comparison baseline even when the draft is kept, so a
    // clear the server did not persist stays visible as unsaved and can be retried.
    setSaved(rules);
    setInherited(todoId ? info.effective_workspace_rules ?? {} : info.system_defaults ?? {});
    if (!keepDraft) {
      setMachine(info.machine_id ?? "");
      setOptionsOpen(!info.machine_id);
    }
    setSupported(true); setLoaded(true);
  };
  const load = async (keepDraft = false) => {
    if (!context.isCurrent() || saving.current) return;
    saving.current = true; setBusy(true); setError("");
    try { await refresh(keepDraft); }
    catch { if (context.isCurrent()) setError(t("workspace-naming.failed")); }
    finally { if (context.isCurrent()) { saving.current = false; setBusy(false); } }
  };
  const save = async () => {
    if (!context.isCurrent() || saving.current) return;
    saving.current = true; setBusy(true); setError("");
    try {
      const response = await saveWorkspaceRules(context, todoId, revision, draft);
      if (!context.isCurrent()) return;
      setSaved({ ...draft }); if (todoId) setRevision(response.revision);
      if (todoId) {
        // Keep the confirmed write if a follow-up read fails; recovery retries
        // reads, while a fresh record supplies the next save's baseline and CAS.
        try { await refresh(true); }
        catch { if (context.isCurrent()) setError(t("workspace-naming.failed")); }
      }
    } catch (error) { if (context.isCurrent()) failure(error, t("workspace-naming.save_failed")); }
    finally { if (context.isCurrent()) { saving.current = false; setBusy(false); } }
  };
  const preview = async () => {
    const generation = ++sequence.current; setPreviewBusy(true); setError(""); setResult(undefined);
    try {
      const response = await previewWorkspaceNaming(context, todoId, { machine_id: machine, layout, selected_repo_ids: selected, workspace_rules: rulesPatch(draft) });
      if (context.isCurrent() && generation === sequence.current) setResult(response);
    } catch (error) { if (context.isCurrent() && generation === sequence.current) failure(error, t("workspace-naming.preview_failed")); }
    finally { if (context.isCurrent() && generation === sequence.current) setPreviewBusy(false); }
  };
  const dirty = !sameRules(draft, saved);
  const hasOverrides = RULE_FIELDS.some((field) => draft[field] !== undefined);
  return <div data-workspace-naming className="min-w-0">
    <Button tone="quiet" expanded={open} aria-controls={open ? id : undefined}
      className="workspace-naming-toggle"
      onClick={() => { setOpen(!open); if (!open && !loaded) void load(); }}>
      <span className="flex-1 text-left">{t("workspace-naming.title")}</span>
      <Icon name={open ? "chevronDown" : "chevronRight"} size={14} className="text-ink-3" />
    </Button>
    {open && <div id={id} className="min-w-0 space-y-4 border-t border-line px-4 py-3">
      <p className="text-label text-ink-2">{t("workspace-naming.scope")}</p>
      {busy && !loaded && <Loading />}
      {loaded && !supported && <p className="text-label text-ink-2">{t("workspace-naming.unsupported")}</p>}
      {loaded && supported && <>
        <WorkspaceRulesEditor value={draft} inherited={inherited} defaultSource={todoId ? "assistant" : "system"} disabled={busy}
          diagnostics={result?.diagnostics} branchSources={result?.branch_sources} onChange={(value) => { invalidate(); setDraft(value); }} />
        <div className="min-w-0 space-y-2 border-t border-line pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <Button tone="secondary" busy={previewBusy} disabled={!machine || busy} onClick={() => void preview()}>{t("workspace-naming.preview")}</Button>
            <Button tone="quiet" expanded={optionsOpen} aria-controls={`${id}-options`} onClick={() => setOptionsOpen(!optionsOpen)}>
              {t("workspace-naming.preview_options")}<Icon name={optionsOpen ? "chevronDown" : "chevronRight"} size={12} />
            </Button>
          </div>
          <div hidden={!optionsOpen} id={`${id}-options`} className="min-w-0 space-y-2">
            {todoId && <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
              {repositories.map((repo) => <label key={repo.repo_id} className="flex items-center gap-2 text-label text-ink-2 pointer-coarse:min-h-11">
                <input type="checkbox" checked={selected.includes(repo.repo_id)} onChange={(e) => { invalidate(); setSelected(e.target.checked ? [...selected, repo.repo_id] : selected.filter((id) => id !== repo.repo_id)); }} />{repo.source_snapshot.identity.path}
              </label>)}
            </div>}
            <div className="workspace-naming-options flex min-w-0 flex-col gap-2 sm:flex-row">
              <label className="block min-w-0 text-label text-ink-2 sm:flex-1">{t("workspace-naming.computer")}
                <select aria-label={t("workspace-naming.computer")} className={`${settingsInputClass} mt-1 min-w-0 pointer-coarse:min-h-11`} value={machine} onChange={(e) => { invalidate(); setMachine(e.target.value); }}>
                  <option value="">{t("workspace-naming.choose_computer")}</option>{machines.map((m) => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
                </select>
              </label>
              <label className="block min-w-0 text-label text-ink-2 sm:flex-1">{t("workspace-naming.layout")}
                <select aria-label={t("workspace-naming.layout")} className={`${settingsInputClass} mt-1 min-w-0 pointer-coarse:min-h-11`} value={layout} onChange={(e) => { invalidate(); setLayout(e.target.value as RepositoryLayout); }}>
                  <option value="container">{t("workspace-naming.container")}</option><option value="root">{t("workspace-naming.root")}</option>
                </select>
              </label>
            </div>
          </div>
          <WorkspaceNamingPreview result={result} repositories={repositories} loading={previewBusy} />
        </div>
        {(dirty || hasOverrides) && <footer className="workspace-naming-actions">
          {hasOverrides && <Button tone="quiet" disabled={busy} onClick={() => { invalidate(); setDraft({}); }}>{t("workspace-naming.reset")}</Button>}
          {dirty && <><span className="text-meta text-ink-3">{t("workspace-naming.unsaved")}</span>
            <Button tone="primary" busy={busy} onClick={() => void save()}>{t("common.save")}</Button></>}
        </footer>}
      </>}
      {error && <><p role="alert" className="text-label text-red">{error}</p>{loaded && supported && <Button tone="quiet" disabled={busy} onClick={() => { invalidate(); void load(true); }}>{t("workspace-naming.reload")}</Button>}</>}
      {!loaded && !busy && <Button tone="quiet" onClick={() => void load()}>{t("task-repositories.retry")}</Button>}
    </div>}
  </div>;
}
