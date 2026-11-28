// Repositories and their Issue/PR links: the to-do detail shows them as rows (the Agent keeps this store), and editing
// opens a dialog. The store is metadata only: nothing here creates folders or moves a task's stage.
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRepositoryRequestContext as useRequestContext } from "../lib/use-repository-request-context";
import { t } from "../i18n";
import { ApiError, errorText } from "../lib/api";
import { safeWebURL } from "../lib/format";
import {
  cleanRepositoryDraft, formatRequestedRef, isExtensionProvider, isReferenceRepositoryLink, loadRepositoryCatalog, loadTaskRepositories,
  partitionTaskRepositories, repositoryDraft, repositoryLinkKindLabel, repositoryLinkView, repositoryProviderName,
  saveTaskRepositories, validRepositoryAlias, validRepositoryDraft, validRequestedRef, repositoryDuplicateLink,
  type CreateRepositoryBindings, type RepositoryCatalog, type RepositoryRequestContext, type RepositoryLinkInput, type RequestedRef,
  type RepositoryLinkLabel, type RepositoryLinkView, type TaskRepository, type TaskRepositoryInput, type TaskRepositoryLink,
  type TaskRepositoryRole, type TaskRepositorySet,
} from "../lib/task-repositories";
import { normalizedPR } from "../lib/todo-triage";
import type { PRFacts, TodoRepoGate } from "../lib/todos";
import { repoGatePRLabel, repoGateReasonLabel, repoGateTone, type RepoGateTone } from "../features/todos/repo-gate";
import type { RepositoryIdentity } from "../lib/repository-identity";
import { Group, Pill, Row, RowFailed, settingsInputClass } from "../views/settings/SettingsParts";
import { Button, Collapse, Dialog, IconButton, Loading, Menu, toast, useMenu } from "./atoms";
import { Icon } from "./icons";
import { RepositoryMark } from "./RepositoryMark";
import { SearchField } from "./SearchField";

const hostOf = (identity: RepositoryIdentity) => { try { return new URL(identity.instance_url).host; } catch { return identity.instance_url; } };
const linkTag = (identity: RepositoryIdentity, view: RepositoryLinkView) => `${identity.provider === "gitlab" && view.kind === "pull_request" ? "!" : "#"}${view.number}`;
/** The address a person recognises as the project, without the scheme every row shares. */
const repositoryAddress = (identity: RepositoryIdentity) => identity.canonical_url.replace(/^https?:\/\//, "");
/**
 * An extension's source as a row names it (the name saved with the repository, never its raw id), and whether it is
 * unavailable; nothing for GitHub and GitLab, whose own marks say it.
 */
function extensionSource(identity: RepositoryIdentity, saved?: string, state?: TaskRepository["provider_state"]) {
  if (!isExtensionProvider(identity.provider)) return undefined;
  return { name: repositoryProviderName(identity.provider, undefined, saved) ?? t("task-repositories.another_source"), unavailable: state === "off" || state === "missing" };
}
/** Why an extension row can't take new links, beneath it in the note style of the catalog's states. */
const SourceNote = ({ name }: { name: string }) => <p role="status" data-repository-note className="py-2 pr-4 pl-10 text-submeta leading-snug text-ink-3">
  {t("task-repositories.provider_unavailable", { provider: name })}
</p>;

/**
 * A saved repository on one line: its provider mark at the same small scale as the stroke icons, its name when the
 * catalog has one, and the project's address, all inline instead of the old stacked name/host pair. The address is
 * the link, so it is a comfortable target on a phone without a second row; an address longer than the line cuts with
 * the full value in its tooltip. An extension's source is named after the address, so a narrow line cuts its name
 * before the repository's path.
 */
function RepositoryRow({ identity, name, source, role, children }: { identity: RepositoryIdentity; name?: string; source?: string; role?: TaskRepositoryRole; children?: React.ReactNode }) {
  const href = safeWebURL(identity.canonical_url);
  const label = <>
    {name && <span className="min-w-0 max-w-1/2 shrink-0 truncate text-meta text-ink">{name}</span>}
    <span className="min-w-0 flex-1 truncate text-submeta text-ink-3">{repositoryAddress(identity)}{source && <span data-repository-source> · {source}</span>}</span>
  </>;
  return <div className="flex items-center gap-2.5 px-4 py-2" data-repository-row data-repo-role={role}>
    <RepositoryMark provider={identity.provider} />
    {href ? (
      <a href={href} target="_blank" rel="noreferrer noopener" title={source ? `${href} · ${source}` : href}
        className="flex touch-hit min-w-0 flex-1 items-center gap-2">
        {label}
      </a>
    ) : (
      <span className="flex min-w-0 flex-1 items-center gap-2">{label}</span>
    )}
    {children}
  </div>;
}

/**
 * One saved issue or pull request: the provider's kind mark, its full title (the link), and its number. An extension's
 * source may name the kinds its own way (`labels`, shown as given); a pasted address its source has not read yet is
 * the address itself, said to be checked on save.
 */
function LinkRow({ identity, view, labels, pr, referenceBadge, onRemove, disabled }: { identity: RepositoryIdentity; view: RepositoryLinkView; labels?: readonly RepositoryLinkLabel[]; pr?: PRFacts; referenceBadge?: boolean; onRemove?: () => void; disabled?: boolean }) {
  const href = safeWebURL(view.url);
  if (view.pending) {
    return <div className="flex items-start gap-2 py-2 pr-4 pl-10" data-repository-link data-link-pending>
      <span data-link-icon="link" className="mt-0.5 shrink-0 text-ink-3"><Icon name="link" size={14} /></span>
      <div className="min-w-0 flex-1">
        {href ? (
          <a href={href} target="_blank" rel="noreferrer noopener" className="flex touch-hit items-start gap-2 text-meta leading-snug text-ink-3 hover:underline">
            <span className="min-w-0 break-all">{view.url}</span>
            <Icon name="external" size={11} className="mt-1 ml-auto shrink-0 text-ink-3" />
          </a>
        ) : (
          <span className="flex items-start gap-2 text-meta leading-snug text-ink-3">
            <span className="min-w-0 break-all">{view.url}</span>
          </span>
        )}
        <p className="text-submeta leading-snug text-ink-3">{t("task-repositories.link_pending")}</p>
      </div>
      {onRemove && <IconButton icon="x" size={24} iconSize={12} className="-mt-0.5" label={t("task-repositories.remove_link", { link: view.url })} onClick={onRemove} disabled={disabled} />}
    </div>;
  }
  const kind = (isExtensionProvider(identity.provider) ? repositoryLinkKindLabel(view.kind, labels) : undefined)
    ?? t(view.kind === "issue" ? "task-repositories.issue" : "task-repositories.pull_request");
  const icon = view.kind === "issue" ? "target" : identity.provider === "gitlab" ? "merge" : "pullRequest";
  const tag = linkTag(identity, view);
  const title = view.title || pr?.title?.trim();
  const state = pr?.state === "open" && pr.draft ? "draft" : pr?.state;
  const tones = { open: "green", draft: undefined, merged: "accent", closed: "red" } as const;
  return <div className="flex items-start gap-2 py-2 pr-4 pl-10" data-repository-link>
    <span data-link-icon={icon} className="mt-0.5 shrink-0 text-ink-3"><Icon name={icon} size={14} /></span>
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
      {href ? (
        <a href={href} target="_blank" rel="noreferrer noopener" aria-label={`${kind} ${tag} · ${identity.path}${title ? ` · ${title}` : ""}`}
          className="flex touch-hit min-w-0 flex-auto items-start gap-2 text-meta leading-snug text-ink-3 hover:underline">
          <span className="min-w-0 break-words">{title || kind}</span>
          <span className="shrink-0 font-medium tabular-nums text-accent-ink">{tag}</span>
          <Icon name="external" size={11} className="mt-1 ml-auto shrink-0 text-ink-3" />
        </a>
      ) : (
        <span className="flex min-w-0 flex-auto items-start gap-2 text-meta leading-snug text-ink-3">
          <span className="min-w-0 break-words">{title || kind}</span>
          <span className="shrink-0 font-medium tabular-nums text-accent-ink">{tag}</span>
        </span>
      )}
      {referenceBadge && <span data-link-role="reference"><Pill>{t("task-repositories.link_role_reference")}</Pill></span>}
      {state && <Pill tone={tones[state]}>{t(`session-gitpr.state_${state}`)}</Pill>}
      {pr?.state === "open" && pr.conflicts && <Pill tone="orange">{t("task-repositories.pr_conflict")}</Pill>}
    </div>
    {onRemove && <IconButton icon="x" size={24} iconSize={12} className="-mt-0.5" label={t("task-repositories.remove_link", { link: tag })} onClick={onRemove} disabled={disabled} />}
  </div>;
}

/**
 * Adds one link by its URL: Enter adds, Escape goes back, a URL of another repository says so. `saved`: the row's saved
 * links, which an extension's kind + number draft names; `unavailable`: its source can't read a new one now.
 */
function AddLink({ identity, links, saved, pending, unavailable, onAdd }: {
  identity: RepositoryIdentity; links: TaskRepositoryInput["links"]; saved?: readonly TaskRepositoryLink[]; pending?: boolean; unavailable?: boolean; onAdd: (link: RepositoryLinkInput) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [input, setInput] = useState("");
  const [invalid, setInvalid] = useState(false);
  const add = () => {
    const view = repositoryLinkView(identity, { url: input.trim() }, saved);
    if (!view) { setInvalid(true); return; }
    if (!(links ?? []).some((link) => repositoryLinkView(identity, link, saved)?.url === view.url)) onAdd({ url: view.url });
    setInput(""); setInvalid(false); setAdding(false);
  };
  if (!adding || unavailable) return <Button icon="plus" tone="quiet" disabled={pending || unavailable || (links?.length ?? 0) >= 50} onClick={() => setAdding(true)}>{t("task-repositories.add_link")}</Button>;
  return <div className="space-y-2">
    <label className="block text-submeta text-ink-3">{t("task-repositories.link_url")}
      <input className={`${settingsInputClass} mt-1 min-h-11`} type="url" autoFocus inputMode="url" autoComplete="off" spellCheck={false}
        value={input} placeholder={t("task-repositories.link_placeholder")} aria-invalid={invalid} disabled={pending}
        onChange={(e) => { setInput(e.target.value); setInvalid(false); }}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing) return;
          if (e.key === "Enter") { e.preventDefault(); add(); }
          else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setAdding(false); setInvalid(false); }
        }} />
    </label>
    {invalid && <p role="alert" className="text-submeta text-red">{t("task-repositories.link_invalid")}</p>}
    <div className="flex flex-wrap justify-end gap-2">
      <Button tone="quiet" onClick={() => { setAdding(false); setInvalid(false); }}>{t("common.cancel")}</Button>
      <Button disabled={pending || !input.trim()} onClick={add}>{t("task-repositories.add_link")}</Button>
    </div>
  </div>;
}

/** One repository being edited: its row, its links, and the source options most people never open. */
function RepositoryDraftRow({ row, entry, saved, pending, duplicateAlias, onChange, onRemove }: {
  row: TaskRepositoryInput; entry?: RepositoryCatalog["repositories"][number]; saved?: TaskRepository; pending?: boolean;
  duplicateAlias?: boolean; onChange: (patch: Partial<TaskRepositoryInput>) => void; onRemove: () => void;
}) {
  const menu = useMenu();
  const identity = saved?.source_snapshot.identity ?? entry;
  const ref = row.requested_ref ?? { kind: "default" };
  const links = row.links ?? [];
  const aliasValid = validRepositoryAlias(row.directory_alias ?? "");
  const refValid = validRequestedRef(ref);
  if (!identity) return null;
  const name = entry && entry.canonical_url === identity.canonical_url ? entry.name || identity.path : identity.path;
  const source = extensionSource(identity, saved?.source_snapshot.provider_name ?? entry?.provider_name, saved?.provider_state);
  return <Group>
    <RepositoryRow identity={identity} name={name === identity.path ? undefined : name} source={source?.name}>
      {source?.unavailable && <Pill>{t("task-repositories.unavailable")}</Pill>}
      {!!links.length && <Pill>{links.length}</Pill>}
      <IconButton icon="more" label={t("task-repositories.options", { name })} onClick={menu.open} disabled={pending} />
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right"
        items={[{ key: "remove", label: t("task-repositories.remove"), icon: "x", danger: true, onSelect: onRemove }]} />}
    </RepositoryRow>
    <div role="group" aria-label={t("task-repositories.links")} className="divide-y divide-line">
      {links.map((link, index) => {
        // An extension's kind + number is drawn from the saved link it names: the draft has no address to rebuild.
        const view = repositoryLinkView(identity, link, saved?.links);
        return view ? <LinkRow key={`${link.kind ?? ""}:${link.number ?? ""}:${"url" in link ? link.url : ""}:${index}`} identity={identity} view={view} labels={saved?.link_kinds} disabled={pending}
          onRemove={() => onChange({ links: links.filter((_, i) => i !== index) })} /> : null;
      })}
    </div>
    <div className="py-2 pr-4 pl-10"><AddLink identity={identity} links={links} saved={saved?.links} pending={pending} unavailable={source?.unavailable} onAdd={(link) => onChange({ links: [...links, link] })} /></div>
    {source?.unavailable && <SourceNote name={source.name} />}
    {!!saved?.catalog_state && saved.catalog_state !== "current" && <p role="status" data-repository-note className="py-2 pr-4 pl-10 text-submeta leading-snug text-ink-3">
      {t(saved.catalog_state === "removed" ? "task-repositories.catalog_removed" : saved.catalog_state === "identity_changed" ? "task-repositories.identity_changed" : "task-repositories.unlisted")}
    </p>}
    <details className="pr-4 pl-10 text-submeta text-ink-3">
      <summary className="flex min-h-11 cursor-pointer items-center gap-2">
        <Icon name="branch" size={13} />
        <span className="min-w-0 flex-1 truncate">{t("task-repositories.source_options")}</span>
        <span className="truncate" style={{ maxWidth: "50%" }}>{ref.kind === "default" ? t("task-repositories.default") : ref.value}</span>
        <Icon name="chevron" size={11} />
      </summary>
      <div className="space-y-3 pt-1 pb-4">
        <label className="block">{t("task-repositories.alias")}
          <input className={`${settingsInputClass} mt-1 min-h-11`} maxLength={64} value={row.directory_alias ?? ""} aria-invalid={!aliasValid} disabled={pending}
            onChange={(e) => onChange({ directory_alias: e.target.value })} />
        </label>
        {!aliasValid && <p role="alert" className="text-red">{t("task-repositories.alias_invalid")}</p>}
        {aliasValid && duplicateAlias && <p role="alert" className="text-red">{t("task-repositories.alias_duplicate")}</p>}
        <label className="block">{t("task-repositories.source")}
          <select className={`${settingsInputClass} mt-1 min-h-11`} value={ref.kind} disabled={pending}
            onChange={(e) => onChange({ requested_ref: e.target.value === "default" ? { kind: "default" } : { kind: "branch", value: "" } })}>
            <option value="default">{t("task-repositories.default")}</option>
            <option value="branch">{t("task-repositories.branch")}</option>
            {ref.kind !== "default" && ref.kind !== "branch" && <option value={ref.kind}>{t(`task-repositories.${ref.kind}`)}</option>}
          </select>
        </label>
        {ref.kind !== "default" && <label className="block">{t("task-repositories.ref_value")}
          <input className={`${settingsInputClass} mt-1 min-h-11`} value={ref.value} aria-invalid={!refValid} disabled={pending}
            onChange={(e) => onChange({ requested_ref: { kind: ref.kind, value: e.target.value } as RequestedRef })} />
        </label>}
        {ref.kind !== "default" && !refValid && <p role="alert" className="text-red">{t("task-repositories.ref_invalid")}</p>}
      </div>
    </details>
  </Group>;
}

/** Picks a saved repository by name, path or host: typing narrows the list, ↑ ↓ move, Enter takes the first match. */
function RepositoryPicker({ catalog, selected, disabled, onPick }: { catalog: RepositoryCatalog; selected: TaskRepositoryInput[]; disabled?: boolean; onPick: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const box = useRef<HTMLDivElement | null>(null);
  const available = catalog.repositories.filter((r) => !selected.some((s) => s.repo_id === r.id));
  const sourceOf = (r: RepositoryCatalog["repositories"][number]) => extensionSource(r, r.provider_name)?.name;
  const rows = available.filter((r) => `${r.name ?? ""} ${r.path} ${hostOf(r)}${isExtensionProvider(r.provider) ? ` ${sourceOf(r)}` : ""}`.toLowerCase().includes(query.trim().toLowerCase()));
  // Closing hands the focus back to the button that opened this, not to the page.
  const close = () => { setOpen(false); setQuery(""); box.current?.querySelector("button")?.focus(); };
  const pick = (id: string) => { onPick(id); setOpen(false); setQuery(""); };
  return <div ref={box}>
    <Button icon="plus" tone="quiet" expanded={open} disabled={disabled || selected.length >= 20} onClick={() => open ? close() : setOpen(true)}>{t("task-repositories.add")}</Button>
    <Collapse open={open}>
      <div className="mt-2 space-y-2 rounded-control bg-inset p-2 shadow-hairline" data-repository-picker>
        <SearchField value={query} onChange={setQuery} placeholder={t("task-repositories.search")} label={t("task-repositories.search")} tone="inset" autoFocus
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return;
            if (e.key === "Enter" && rows[0]) { e.preventDefault(); pick(rows[0].id); }
            else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
            else if (e.key === "ArrowDown") { e.preventDefault(); e.currentTarget.closest("[data-task-repository-editor]")?.querySelector<HTMLElement>('[role="option"]')?.focus(); }
          }} />
        <div role="listbox" aria-label={t("task-repositories.choose")} className="max-h-60 overflow-y-auto rounded-control bg-surface" onKeyDown={(e) => {
          const items = [...e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="option"]')];
          if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); return; }
          if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
          e.preventDefault();
          const index = items.indexOf(document.activeElement as HTMLButtonElement);
          const next = e.key === "Home" ? 0 : e.key === "End" ? items.length - 1
            : index + (e.key === "ArrowDown" ? 1 : items.length - 1);
          items[items.length ? ((next % items.length) + items.length) % items.length : 0]?.focus();
        }}>
          {rows.map((row) => <button key={row.id} type="button" role="option" aria-selected={false} disabled={disabled} onClick={() => pick(row.id)}
            className="flex min-h-11 w-full items-center gap-3 rounded-control px-3 py-2 text-left transition-colors hover:bg-hover focus-visible:bg-hover">
            <RepositoryMark provider={row.provider} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-meta font-medium text-ink">{row.name || row.path}</span>
              <span className="block truncate text-submeta text-ink-3">{row.path} · {hostOf(row)}{isExtensionProvider(row.provider) && ` · ${sourceOf(row)}`}</span>
            </span>
            <Icon name="plus" size={13} className="shrink-0 text-ink-3" />
          </button>)}
          {!rows.length && <p className="px-3 py-4 text-submeta text-ink-3">{t(available.length ? "task-repositories.no_match" : "task-repositories.empty")}</p>}
        </div>
      </div>
    </Collapse>
  </div>;
}

export type TaskRepositoryBindingsEditorProps = { catalog: RepositoryCatalog; value: TaskRepositoryInput[]; snapshots?: TaskRepository[]; pending?: boolean; onChange: (value: TaskRepositoryInput[]) => void };
/** Structured controlled editor, shared by to-do creation and the detail's link dialog. */
export function TaskRepositoryBindingsEditor({ catalog, value, snapshots = [], pending, onChange }: TaskRepositoryBindingsEditorProps) {
  const latest = useRef({ value, pending });
  latest.current = { value, pending };
  const aliasCounts = new Map<string, number>();
  for (const row of value) {
    const key = row.directory_alias?.toLowerCase();
    if (key) aliasCounts.set(key, (aliasCounts.get(key) ?? 0) + 1);
  }
  const remove = (index: number) => {
    const removed = value[index];
    onChange(value.filter((_, i) => i !== index));
    toast(t("task-repositories.removed"), "info", { label: t("task-repositories.undo"), run: () => {
      if (!latest.current.pending && !latest.current.value.some((r) => r.repo_id === removed.repo_id)) {
        const rows = [...latest.current.value];
        rows.splice(Math.min(index, rows.length), 0, removed);
        onChange(rows);
      }
    } });
  };
  return <div data-task-repository-editor>
    {value.map((row, index) => <Fragment key={row.repo_id}>
      <RepositoryDraftRow row={row} entry={catalog.repositories.find((r) => r.id === row.repo_id)}
        saved={snapshots.find((r) => r.repo_id === row.repo_id)} pending={pending}
        duplicateAlias={!!row.directory_alias && (aliasCounts.get(row.directory_alias.toLowerCase()) ?? 0) > 1}
        onChange={(patch) => onChange(value.map((r, i) => i === index ? { ...r, ...patch } : r))}
        onRemove={() => remove(index)} />
    </Fragment>)}
    <RepositoryPicker catalog={catalog} selected={value} disabled={pending}
      onPick={(id) => onChange([...value, { repo_id: id, requested_ref: { kind: "default" }, links: [] }])} />
  </div>;
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError && ["task_repositories_revision_conflict", "task_repositories_catalog_conflict"].includes(error.code ?? "")) return t("task-repositories.conflict");
  if (error instanceof ApiError && error.code?.startsWith("task_repositories_") && error.code !== "task_repositories_store_unavailable") return errorText(error);
  return t("task-repositories.failed");
}
/** A hub that does not serve the association store at all (the route is missing, not the to-do). */
const isUnsupported = (error: unknown) => error instanceof ApiError && error.status === 404 && error.code !== "task_repositories_not_found";

/** A to-do's own PR, as the page already parses it (the editor does not read the stage bar, a first-screen chunk). */
type LegacyPR = { url: string; text: string };
type TodoRepositoryBindingsProps = { manager: string; owner: string; todoId: string; legacyPRs?: LegacyPR[]; prStatus?: PRFacts; otherPRs?: PRFacts[]; version?: unknown; todosV2?: boolean; repoGate?: TodoRepoGate; hideRepoGateBanner?: boolean; onRepositoriesCount?: (count: number) => void };

function hasNewPRLinkTitle(previous: Map<string, PRFacts>, current: Map<string, PRFacts>, saved?: TaskRepositorySet) {
  if (previous === current) return false;
  const changed = (url: string) => {
    const title = current.get(url)?.title?.trim();
    return !!title && title !== previous.get(url)?.title?.trim();
  };
  // While a read is in flight we don't yet know which links its answer will contain.
  if (!saved) return [...current.keys()].some(changed);
  return saved.repositories.some(({ links }) => links.some((link) => link.kind === "pull_request" && changed(normalizedPR(link.canonical_url))));
}

export function TodoRepositoryBindings(props: TodoRepositoryBindingsProps) {
  return <TodoRepositoryBindingsSession key={`${props.owner}:${props.manager}:${props.todoId}`} {...props} />;
}
function TodoRepositoryBindingsSession({ manager, owner, todoId, legacyPRs = [], prStatus, otherPRs, version, todosV2, repoGate, hideRepoGateBanner, onRepositoriesCount }: TodoRepositoryBindingsProps) {
  const prByURL = useMemo(() => {
    const facts = new Map<string, PRFacts>();
    if (prStatus) facts.set(normalizedPR(prStatus.url), prStatus);
    for (const pr of otherPRs ?? []) {
      const url = normalizedPR(pr.url);
      if (!facts.has(url)) facts.set(url, pr); // Earlier observations arrive newest first.
    }
    return facts;
  }, [prStatus, otherPRs]);
  const context = useRequestContext(manager, owner);
  const [catalog, setCatalog] = useState<RepositoryCatalog>();
  const [saved, setSaved] = useState<TaskRepositorySet>();
  const [draft, setDraft] = useState<TaskRepositoryInput[]>([]);
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [unsupported, setUnsupported] = useState(false);
  const identities = useMemo(() => {
    const map = new Map<string, RepositoryIdentity>();
    for (const entry of catalog?.repositories ?? []) map.set(entry.id, entry);
    for (const row of saved?.repositories ?? []) map.set(row.repo_id, row.source_snapshot.identity);
    return map;
  }, [catalog, saved]);
  const lock = useRef(false);
  const loads = useRef(0);
  const editingNow = useRef(editing);
  editingNow.current = editing;
  const versionNow = useRef(0);
  // A version change while the dialog is open or opening must not start a competing read: its answer could replace the
  // user's draft or the revision the dialog edits from. It is remembered and read after the dialog closes instead.
  const editorPending = useRef(false);
  const pendingRefresh = useRef(false);
  // An operation clears busy only if it is still the newest one: an obsolete read must not end another operation's
  // spinner, and a newer read must not be cleared by an older finally.
  const busyOwner = useRef(0);
  const beginBusy = useCallback(() => { const token = ++busyOwner.current; setBusy(true); return token; }, []);
  const endBusy = useCallback((token: number) => { if (token === busyOwner.current) setBusy(false); }, []);
  const inFlight = useRef<{ id: number; version: number; promise: Promise<TaskRepositorySet | undefined> } | undefined>(undefined);
  // A read belongs to the version it began from. Reusing one that began before a version change would answer with the
  // state before the notification; the invalidated read returns undefined, so a fresh one is needed instead.
  const startRead = useCallback((forVersion: number) => {
    const id = ++loads.current;
    const promise = loadTaskRepositories(context, todoId).then(
      (result) => id === loads.current && context.isCurrent() ? result : undefined,
    );
    const entry = { id, version: forVersion, promise };
    inFlight.current = entry;
    return entry;
  }, [context, todoId]);
  const applyLoaded = useCallback((result: TaskRepositorySet) => {
    if (!context.isCurrent()) return;
    if (editingNow.current || editorPending.current) { pendingRefresh.current = true; return; }
    setSaved(result); setDraft(repositoryDraft(result.repositories)); setUnsupported(false);
  }, [context]);
  // Viewing and editing share the same read generation so a late result cannot replace a newer repository revision.
  const [attempt, setAttempt] = useState(0);
  const previousNotice = useRef<{ version: unknown; attempt: number; prByURL: Map<string, PRFacts> }>(undefined);
  const refresh = useCallback(async () => {
    if (!context.isCurrent()) return;
    const busy = beginBusy();
    setError("");
    let entry = inFlight.current;
    if (!entry || entry.version !== versionNow.current || entry.id !== loads.current) {
      entry = startRead(versionNow.current);
    }
    try {
      const result = await entry.promise;
      if (result) applyLoaded(result);
    } catch (failure) {
      // A rejected read is fenced like its answer: an invalidated background read must not surface its 404/503 over
      // the current store, and it must not schedule a refresh of its own.
      if (!context.isCurrent() || entry.id !== loads.current || entry.version !== versionNow.current) return;
      if (editingNow.current || editorPending.current) { pendingRefresh.current = true; return; }
      if (isUnsupported(failure)) setUnsupported(true); else setError(errorMessage(failure));
    } finally {
      if (inFlight.current === entry) inFlight.current = undefined;
      endBusy(busy);
    }
  }, [context, beginBusy, endBusy, applyLoaded, startRead]);
  useEffect(() => {
    if (!context.isCurrent()) return;
    const previous = previousNotice.current;
    previousNotice.current = { version, attempt, prByURL };
    // A manager title write advances the repository revision independently of the to-do's updated_at.
    if (previous && Object.is(previous.version, version) && previous.attempt === attempt
      && !hasNewPRLinkTitle(previous.prByURL, prByURL, inFlight.current ? undefined : saved)) return;
    versionNow.current++;
    if (editingNow.current || editorPending.current) { pendingRefresh.current = true; return; }
    void refresh();
  }, [context, version, attempt, prByURL, saved, refresh]);
  // A completed close (save or cancel) fulfils the refresh that was held back while editing.
  useEffect(() => {
    if (editing || editorPending.current || !pendingRefresh.current || !context.isCurrent()) return;
    pendingRefresh.current = false;
    void refresh();
  }, [editing, context, refresh]);
  // Editing always starts from the store's newest value and catalog: the Agent may have added a link since the load.
  const openEditor = async () => {
    if (lock.current || !context.isCurrent()) return;
    lock.current = true;
    editorPending.current = true;
    const busy = beginBusy();
    setError("");
    const versionAtStart = versionNow.current;
    // An explicit editor load must not adopt a background read: that read may predate this version.
    inFlight.current = undefined;
    const load = startRead(versionAtStart);
    let opened = false;
    try {
      const [nextCatalog, latest] = await Promise.all([loadRepositoryCatalog(context), load.promise]);
      if (!context.isCurrent() || !latest) return;
      // A version change while this load was pending is newer than what it answered: keep it for the close refresh.
      pendingRefresh.current = versionNow.current !== versionAtStart;
      setCatalog(nextCatalog); setSaved(latest); setDraft(repositoryDraft(latest.repositories)); setEditing(true); setUnsupported(false);
      opened = true;
    } catch (failure) {
      if (context.isCurrent()) { if (isUnsupported(failure)) setUnsupported(true); else setError(errorMessage(failure)); }
    } finally {
      if (inFlight.current === load) inFlight.current = undefined;
      editorPending.current = false;
      lock.current = false;
      endBusy(busy);
      if (!opened && pendingRefresh.current && context.isCurrent()) { pendingRefresh.current = false; void refresh(); }
    }
  };
  const reloadForEdit = async () => {
    if (lock.current || !context.isCurrent()) return;
    lock.current = true;
    editorPending.current = true;
    const busy = beginBusy();
    setError("");
    const versionAtStart = versionNow.current;
    inFlight.current = undefined;
    const load = startRead(versionAtStart);
    try {
      const [nextCatalog, latest] = await Promise.all([loadRepositoryCatalog(context), load.promise]);
      if (!context.isCurrent() || !latest) return;
      pendingRefresh.current = versionNow.current !== versionAtStart;
      setCatalog(nextCatalog); setSaved(latest); setDraft(repositoryDraft(latest.repositories));
    } catch (failure) { if (context.isCurrent()) setError(errorMessage(failure)); }
    finally {
      if (inFlight.current === load) inFlight.current = undefined;
      editorPending.current = false;
      lock.current = false;
      endBusy(busy);
    }
  };
  const save = async () => {
    if (lock.current || !saved || !catalog || !context.isCurrent()) return;
    const clean = cleanRepositoryDraft(draft);
    if (!validRepositoryDraft(clean, identities, saved.repositories)) return;
    lock.current = true;
    const busy = beginBusy();
    setError("");
    try {
      const result = await saveTaskRepositories(context, todoId, saved.revision, catalog.catalog_revision, clean);
      if (!context.isCurrent()) return;
      setSaved(result); setDraft(repositoryDraft(result.repositories)); setEditing(false);
      toast(t("task-repositories.saved"), "ok");
    } catch (failure) { if (context.isCurrent()) setError(errorMessage(failure)); }
    finally { lock.current = false; endBusy(busy); }
  };
  const repositories = saved?.repositories ?? [];
  useEffect(() => {
    onRepositoriesCount?.(repositories.length);
  }, [onRepositoriesCount, repositories.length]);
  const matrix = useMemo(() => (todosV2 === true && repositories.length ? partitionTaskRepositories(repositories) : undefined), [todosV2, repositories]);
  const cleanDraft = cleanRepositoryDraft(draft);
  const duplicateLinks = repositoryDuplicateLink(cleanDraft, identities, saved?.repositories);
  const dirty = !!saved && JSON.stringify(cleanDraft) !== JSON.stringify(repositoryDraft(saved.repositories));
  if (unsupported && !legacyPRs.length) return null;
  const renderMatrixGroup = (role: TaskRepositoryRole, rows: readonly TaskRepository[]) => {
    if (!rows.length) return null;
    const isTarget = role === "target";
    return <Fragment key={role}>
      <div data-repo-matrix-group={role} className="flex flex-wrap items-center justify-between gap-2 bg-inset px-4 py-1.5 text-submeta text-ink-3">
        <span className="font-medium text-ink-2">{t(isTarget ? "task-repositories.role_target" : "task-repositories.role_reference")} ({rows.length})</span>
        <span>{t(isTarget ? "task-repositories.role_target_hint" : "task-repositories.role_reference_hint")}</span>
      </div>
      {rows.map((repository) => {
        const source = extensionSource(repository.source_snapshot.identity, repository.source_snapshot.provider_name, repository.provider_state);
        const refLabel = formatRequestedRef(repository.requested_ref);
        return <Fragment key={repository.repo_id}>
          <RepositoryRow identity={repository.source_snapshot.identity} source={source?.name} role={role}>
            <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
              <span data-repo-role-badge={role}>
                <Pill tone={isTarget ? "accent" : undefined}>
                  {t(isTarget ? "task-repositories.role_target" : "task-repositories.role_reference")}
                </Pill>
              </span>
              {refLabel && <span data-repo-ref={repository.requested_ref.value} title={repository.requested_ref.value}>
                <Pill>
                  <span className="inline-flex items-center gap-1">
                    <Icon name="branch" size={11} />
                    <span className="max-w-28 truncate">{refLabel}</span>
                  </span>
                </Pill>
              </span>}
              {source?.unavailable && <Pill>{t("task-repositories.unavailable")}</Pill>}
              {!!repository.links.length && <Pill>{repository.links.length}</Pill>}
            </div>
          </RepositoryRow>
          {repository.links.map((link) => {
            const view = repositoryLinkView(repository.source_snapshot.identity, link);
            return view ? <LinkRow key={`${link.kind}:${link.number}`} identity={repository.source_snapshot.identity} view={view} labels={repository.link_kinds}
              pr={link.kind === "pull_request" ? prByURL.get(normalizedPR(link.canonical_url)) : undefined}
              referenceBadge={isTarget && isReferenceRepositoryLink(link)} /> : null;
          })}
          {source?.unavailable && <SourceNote name={source.name} />}
          {!!repository.catalog_state && repository.catalog_state !== "current" && <p role="status" data-repository-note className="py-2 pr-4 pl-10 text-submeta leading-snug text-ink-3">
            {t(repository.catalog_state === "removed" ? "task-repositories.catalog_removed" : repository.catalog_state === "identity_changed" ? "task-repositories.identity_changed" : "task-repositories.unlisted")}
          </p>}
        </Fragment>;
      })}
    </Fragment>;
  };
  const renderRepoGateBanner = () => {
    if (todosV2 !== true || !repoGate || hideRepoGateBanner) return null;
    const tone = repoGateTone(repoGate.state);
    const toneText: Record<RepoGateTone, string> = {
      green: "text-green",
      red: "text-red",
      orange: "text-orange",
      muted: "text-ink-3",
    };
    const toneBg: Record<RepoGateTone, string> = {
      green: "bg-inset",
      red: "bg-danger-wash",
      orange: "bg-orange-tint",
      muted: "bg-inset",
    };
    const headline =
      repoGate.state === "reference_only"
        ? t("todos.repo_gate_detail_reference_only", { count: repoGate.reference_count })
        : repoGate.state === "ready"
          ? t("todos.repo_gate_detail_ready", { ready: repoGate.ready_count, total: repoGate.target_count })
          : repoGate.blocking_repo?.trim()
            ? t("todos.repo_gate_detail_blocked", {
                ready: repoGate.ready_count,
                total: repoGate.target_count,
                repo: repoGate.blocking_repo.trim(),
              })
            : t("todos.repo_gate_detail_pending", { ready: repoGate.ready_count, total: repoGate.target_count });
    const reasonText = repoGateReasonLabel(repoGate);
    const blockingPR = safeWebURL(repoGate.blocking_pr);
    return (
      <div
        data-testid="todo-detail-repo-gate"
        data-gate-state={repoGate.state}
        className={`flex flex-wrap items-center justify-between gap-2 px-4 py-2 text-submeta ${toneBg[tone]}`}
      >
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
          <span className={`inline-flex items-center gap-1.5 font-medium ${toneText[tone]}`}>
            <Icon name={tone === "green" ? "check" : "alertCircle"} size={13} className="shrink-0" />
            <span className="tabular-nums">{headline}</span>
          </span>
          {repoGate.state !== "reference_only" && repoGate.state !== "ready" && reasonText && (
            <span data-testid="todo-detail-repo-gate-reason" className="text-ink-2">
              · {reasonText}
            </span>
          )}
          {repoGate.state !== "reference_only" && repoGate.reference_count > 0 && (
            <span data-testid="todo-detail-repo-gate-refs" className="text-ink-3 tabular-nums">
              · {t("todos.repo_gate_detail_refs_note", { count: repoGate.reference_count })}
            </span>
          )}
        </div>
        {blockingPR && (
          <a
            href={blockingPR}
            target="_blank"
            rel="noreferrer noopener"
            data-testid="todo-detail-repo-gate-pr"
            className="touch-hit inline-flex shrink-0 items-center gap-1 font-medium whitespace-nowrap text-accent-ink hover:underline"
          >
            <span>{repoGatePRLabel(blockingPR)}</span>
            <Icon name="external" size={11} />
          </a>
        )}
      </div>
    );
  };
  return <section className="mt-6" data-todo-repositories>
    <Group title={t("task-repositories.section")} right={!unsupported &&
      // It stays enabled while the editor opens: a disabled button that had the focus drops it to the page, and the
      // dialog would then have nothing to hand it back to. Opening is guarded against a second press by its lock.
      <Button icon={repositories.length ? "pencil" : "plus"} tone="quiet" size="sm" onClick={() => void openEditor()}>
        {t(repositories.length ? "task-repositories.manage" : "task-repositories.add")}
      </Button>}>
      {unsupported
        ? legacyPRs.map((p) => {
            const href = safeWebURL(p.url);
            return <Row key={p.url} icon="pullRequest" hint={t("task-repositories.legacy_note")}
              label={href ? <a href={href} target="_blank" rel="noreferrer noopener" className="text-accent-ink hover:underline">{p.text}</a> : p.text} />;
          })
        : <>
          {renderRepoGateBanner()}
          {matrix
            ? <div data-repo-matrix className="divide-y divide-line">
                {renderMatrixGroup("target", matrix.target)}
                {renderMatrixGroup("reference", matrix.reference)}
              </div>
            : repositories.map((repository) => {
                const source = extensionSource(repository.source_snapshot.identity, repository.source_snapshot.provider_name, repository.provider_state);
                return <Fragment key={repository.repo_id}>
                  <RepositoryRow identity={repository.source_snapshot.identity} source={source?.name}>
                    {source?.unavailable && <Pill>{t("task-repositories.unavailable")}</Pill>}
                    {!!repository.links.length && <Pill>{repository.links.length}</Pill>}
                  </RepositoryRow>
                  {repository.links.map((link) => {
                    const view = repositoryLinkView(repository.source_snapshot.identity, link);
                    return view ? <LinkRow key={`${link.kind}:${link.number}`} identity={repository.source_snapshot.identity} view={view} labels={repository.link_kinds}
                      pr={link.kind === "pull_request" ? prByURL.get(normalizedPR(link.canonical_url)) : undefined} /> : null;
                  })}
                  {source?.unavailable && <SourceNote name={source.name} />}
                  {!!repository.catalog_state && repository.catalog_state !== "current" && <p role="status" data-repository-note className="py-2 pr-4 pl-10 text-submeta leading-snug text-ink-3">
                    {t(repository.catalog_state === "removed" ? "task-repositories.catalog_removed" : repository.catalog_state === "identity_changed" ? "task-repositories.identity_changed" : "task-repositories.unlisted")}
                  </p>}
                </Fragment>;
              })}
          {!saved && !error && <div className="px-4 py-3"><Loading /></div>}
          {/* A successful answer is the store's whole word, even empty: the manager imports the legacy history once and
              honours an explicit removal, so `pr_url` is a fallback for a missing route (above) and nowhere else. */}
          {saved && !repositories.length && <Row label={t("task-repositories.no_links")} hint={t("task-repositories.agent_hint")} wrapHint />}
          {error && <RowFailed text={error} retry={t("task-repositories.retry")} onRetry={() => { setError(""); setAttempt((n) => n + 1); }} />}
        </>}
    </Group>
    <Dialog open={editing} onClose={() => { if (!busy) setEditing(false); }} title={t("task-repositories.manage")} width={480}
      footer={<>
        <Button disabled={busy} onClick={() => setEditing(false)}>{t("common.cancel")}</Button>
        <Button tone="primary" busy={busy} disabled={!catalog || !dirty || !validRepositoryDraft(cleanDraft, identities, saved?.repositories)} onClick={() => void save()}>{t("common.save")}</Button>
      </>}>
      <p className="mb-4 text-submeta leading-relaxed text-ink-3">{t("task-repositories.hint")}</p>
      {catalog && <TaskRepositoryBindingsEditor catalog={catalog} value={draft} snapshots={saved?.repositories} pending={busy} onChange={setDraft} />}
      {!validRepositoryDraft(cleanDraft, identities, saved?.repositories) && <p role="alert" className="mt-3 text-submeta text-red">{t(duplicateLinks ? "task-repositories.duplicate_link" : "task-repositories.save_invalid")}</p>}
      {busy && !catalog && <Loading />}
      {error && <div className="mt-3 space-y-2">
        <p role="alert" className="text-submeta text-red">{error}</p>
        <Button tone="quiet" disabled={busy} onClick={() => void reloadForEdit()}>{t("task-repositories.reload")}</Button>
      </div>}
    </Dialog>
  </section>;
}

/** Creation remains one POST: the parent sends this optional controlled draft. */
export function NewTodoRepositoryBindings({ context, value, onChange, pending, onCatalog }: {
  context: RepositoryRequestContext; value?: CreateRepositoryBindings; onChange: (value: CreateRepositoryBindings | undefined) => void; pending: boolean;
  onCatalog?: (catalog: RepositoryCatalog) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [catalog, setCatalog] = useState<RepositoryCatalog>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = async () => {
    if (busy || !context.isCurrent()) return;
    setBusy(true); setError("");
    try {
      const result = await loadRepositoryCatalog(context);
      if (context.isCurrent()) { setCatalog(result); onCatalog?.(result); }
    } catch { if (context.isCurrent()) setError(t("task-repositories.failed")); }
    finally { if (context.isCurrent()) setBusy(false); }
  };
  const rows = value?.repositories ?? [];
  return <div className="space-y-3">
    <Button icon="folder" tone="quiet" expanded={expanded} disabled={pending}
      onClick={() => { setExpanded(!expanded); if (!expanded && !catalog) void load(); }}>
      {t("task-repositories.title")}
      {!!rows.length && <Pill>{rows.length}</Pill>}
    </Button>
    {expanded && <>
      {busy && <Loading />}
      {catalog && <TaskRepositoryBindingsEditor catalog={catalog} value={rows} pending={pending}
        onChange={(next) => onChange(next.length ? { catalog_revision: catalog.catalog_revision, repositories: next } : undefined)} />}
      {error && <><p role="alert" className="text-submeta text-red">{error}</p><Button tone="quiet" onClick={() => void load()}>{t("task-repositories.retry")}</Button></>}
    </>}
  </div>;
}

export { useRequestContext as useRepositoryRequestContext };
