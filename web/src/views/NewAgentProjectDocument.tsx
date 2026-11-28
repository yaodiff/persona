import { createContext, useContext, useId, useLayoutEffect, useRef, useState } from "react";
import { LEGACY_CONTEXT_OR_FILE_RE } from "../lib/compatibility";
import { formatDate, t } from "../i18n";
import {
  listProjectDocumentApprovals, listProjectDocuments, loadApprovedProjectDocument,
  TeamProjectDocumentError, type ApprovedProjectRevision, type ProjectDocumentPage, type TeamProjectDocument,
} from "../lib/teamProjectDocument";
import { Button } from "../ui/atoms";

export type ProjectDocumentSnapshot = Readonly<TeamProjectDocument>;

export function projectDocumentText({ content, ...source }: ProjectDocumentSnapshot): string {
  const ends = new Set(content.match(/<\/persona-project-document(?:-\d+)?>/g));
  let boundary = "persona-project-document";
  for (let suffix = 1; ends.has(`</${boundary}>`); suffix++) boundary = `persona-project-document-${suffix}`;
  // A terminal can consume control bytes, and a legacy hub rewrites context/file tags in first messages.
  const encoded = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/u.test(content)
    || LEGACY_CONTEXT_OR_FILE_RE.test(content);
  const metadata = encoded ? { ...source, content_encoding: "json-string" } : source;
  const body = encoded
    ? JSON.stringify(content).replace(/[<\u007f-\u009f]/gu, char => "\\u" + char.charCodeAt(0).toString(16).padStart(4, "0"))
    : content;
  return `<${boundary}>\n${JSON.stringify(metadata)}\n\n${body}\n</${boundary}>`;
}

export const ProjectDocumentContext = createContext<{
  open: boolean;
  scope: string | null;
  busy: boolean;
  attached: ProjectDocumentSnapshot | null;
  blocked: string | null;
  onAttach: (document: ProjectDocumentSnapshot) => void;
  onRemove: () => void;
} | null>(null);

export function ProjectDocumentSource() {
  const state = useContext(ProjectDocumentContext);
  if (!state?.open) return null;
  if (state.attached) {
    return (
      <div className="mx-2 mt-2 flex min-w-0 flex-col gap-1 rounded-control bg-surface px-3 py-2 shadow-hairline">
        <div className="flex flex-wrap items-center justify-between gap-x-2">
          <div className="min-w-0 text-submeta text-ink-2 [overflow-wrap:anywhere]">
            <div className="font-medium text-ink">{t("newagent.project_document_attached")}</div>
            {t("newagent.project_document_source", state.attached)}
          </div>
          <Button tone="quiet" className="touch-hit" disabled={state.busy} onClick={state.onRemove}>{t("newagent.project_document_remove")}</Button>
        </div>
        {state.blocked && <p role="status" className="text-submeta text-ink-2">{state.blocked}</p>}
      </div>
    );
  }
  return state.scope ? <ProjectDocumentPicker key={state.scope} busy={state.busy} onAttach={state.onAttach} /> : null;
}

function ProjectDocumentPicker({ busy, onAttach }: { busy: boolean; onAttach: (document: ProjectDocumentSnapshot) => void }) {
  const [expanded, setExpanded] = useState(false);
  const panel = useId();
  return (
    <div className="mx-2 mt-2">
      <Button tone="quiet" className="touch-hit" expanded={expanded} aria-controls={panel} onClick={() => setExpanded(!expanded)}>
        {t("newagent.project_document_use")}
      </Button>
      <div id={panel} hidden={!expanded}>
        {expanded && <ProjectDocumentLookup busy={busy} onAttach={onAttach} />}
      </div>
    </div>
  );
}

const loadErrors = {
  access: "newagent.project_document_error_access",
  missing: "newagent.project_document_error_missing",
  invalid: "newagent.project_document_error_invalid",
  unavailable: "newagent.project_document_error_unavailable",
} as const;

function ProjectDocumentLookup({ busy, onAttach }: { busy: boolean; onAttach: (document: ProjectDocumentSnapshot) => void }) {
  const [project, setProject] = useState("");
  const [documentId, setDocumentId] = useState("");
  const [token, setToken] = useState("");
  const [preview, setPreview] = useState<ProjectDocumentSnapshot | null>(null);
  const [catalog, setCatalog] = useState<ProjectDocumentPage | null>(null);
  const [history, setHistory] = useState<{ document: string; page: ProjectDocumentPage } | null>(null);
  const [loading, setLoading] = useState<"catalog" | "history" | "preview" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const active = useRef<AbortController | null>(null);
  const reviewed = useRef<ProjectDocumentSnapshot | null>(null);

  const abort = () => {
    const previous = active.current;
    active.current = null;
    previous?.abort();
  };
  useLayoutEffect(() => abort, []);

  const invalidate = () => {
    abort();
    reviewed.current = null;
    setLoading(null);
    setCatalog(null);
    setHistory(null);
    setPreview(null);
    setError(null);
  };
  const request = async (kind: NonNullable<typeof loading>, run: (controller: AbortController) => Promise<void>) => {
    if (busy || active.current || reviewed.current || !project.trim() || !token.trim()) return;
    const controller = new AbortController();
    active.current = controller;
    setLoading(kind);
    setError(null);
    try {
      await run(controller);
    } catch (failure) {
      if (active.current !== controller) return;
      const code = failure instanceof TeamProjectDocumentError ? failure.code : "unavailable";
      if (code === "access") {
        setCatalog(null);
        setHistory(null);
      }
      setError(kind === "catalog" && code === "missing" ? "newagent.project_document_catalog_unavailable" : loadErrors[code]);
    } finally {
      if (active.current === controller) {
        active.current = null;
        setLoading(null);
      }
    }
  };

  const browse = (document?: string, previous?: ProjectDocumentPage) => request(document ? "history" : "catalog", async (controller) => {
    const page = document
      ? await listProjectDocumentApprovals(project.trim(), document, token.trim(), controller.signal, previous?.next)
      : await listProjectDocuments(project.trim(), token.trim(), controller.signal, previous?.next);
    if (active.current !== controller) return;
    const combined = previous ? { ...page, items: [...previous.items, ...page.items] } : page;
    if (document) setHistory({ document, page: combined });
    else {
      setCatalog(combined);
      setHistory(null);
    }
  });

  const load = (selected?: ApprovedProjectRevision) => request("preview", async (controller) => {
    const projectId = project.trim();
    const memberToken = token.trim();
    const { project: loadedProject, document, revision, content, sha256, author, approved_by } = selected
      ? await loadApprovedProjectDocument(projectId, selected.document, memberToken, controller.signal, selected)
      : await loadApprovedProjectDocument(projectId, documentId.trim(), memberToken, controller.signal);
    if (active.current !== controller) return;
    setToken("");
    setDocumentId(document);
    setCatalog(null);
    setHistory(null);
    reviewed.current = Object.freeze({ project: loadedProject, document, revision, content, sha256, author, approved_by });
    setPreview(reviewed.current);
  });
  const canRead = !busy && !!project.trim() && !!token.trim() && !loading;
  const inputClass = "h-11 min-w-0 w-full rounded-control bg-inset px-3 text-meta text-ink shadow-hairline outline-none focus:shadow-[0_0_0_1px_var(--line-strong)]";
  return (
    <fieldset disabled={busy} aria-label={t("newagent.project_document_use")} className="flex min-w-0 flex-col gap-2 rounded-control bg-surface p-3 shadow-hairline">
      <div className="grid gap-2 sm:grid-cols-2">
        <label className="flex min-w-0 flex-col gap-1 text-submeta text-ink-2">
          {t("newagent.project_document_project")}
          <input className={inputClass} value={project} autoCapitalize="none" autoComplete="off" spellCheck={false} onChange={(event) => { invalidate(); setProject(event.target.value); }} />
        </label>
        <label className="flex min-w-0 flex-col gap-1 text-submeta text-ink-2">
          {t("newagent.project_document_id")}
          <input className={inputClass} value={documentId} autoCapitalize="none" autoComplete="off" spellCheck={false} onChange={(event) => { invalidate(); setDocumentId(event.target.value); }} />
        </label>
      </div>
      <label className="flex flex-col gap-1 text-submeta text-ink-2">
        {t("newagent.project_document_token")}
        <input className={inputClass} type="password" value={token} autoComplete="off" onChange={(event) => { invalidate(); setToken(event.target.value); }} />
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <Button className="touch-hit" busy={loading === "catalog"} disabled={!canRead} onClick={() => void browse()}>
          {t("newagent.project_document_browse")}
        </Button>
        <Button tone="quiet" className="touch-hit" busy={loading === "preview"} disabled={!canRead || !documentId.trim()} onClick={() => void load()}>
          {t("newagent.project_document_load")}
        </Button>
      </div>
      {loading && <p role="status" className="text-submeta text-ink-2">
        {loading === "preview" ? t("newagent.project_document_loading") : t("newagent.project_document_loading_list")}
      </p>}
      {catalog && <ProjectRevisionList
        page={history?.page ?? catalog}
        document={history?.document}
        disabled={!canRead}
        onBack={() => setHistory(null)}
        onMore={() => void browse(history?.document, history?.page ?? catalog)}
        onHistory={(id) => void browse(id)}
        onPreview={(row) => void load(row)}
      />}
      {error && <p role="alert" className="text-submeta text-ink-2">{t(error)}</p>}
      {preview && (
        <div className="flex min-w-0 flex-col gap-2">
          <div role="status" className="text-submeta font-medium text-ink">{t("newagent.project_document_verified")}</div>
          <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-1 text-submeta text-ink-2 [overflow-wrap:anywhere]">
            <dt>{t("newagent.project_document_revision")}</dt><dd>{preview.revision}</dd>
            <dt>{t("newagent.project_document_author")}</dt><dd>{preview.author}</dd>
            <dt>{t("newagent.project_document_approver")}</dt><dd>{preview.approved_by}</dd>
            <dt>{t("newagent.project_document_digest")}</dt><dd className="font-mono">{preview.sha256}</dd>
          </dl>
          <pre tabIndex={0} aria-label={t("newagent.project_document_preview")} className="max-h-48 overflow-auto whitespace-pre-wrap rounded-control bg-inset p-2 font-mono text-submeta text-ink [overflow-wrap:anywhere]">{preview.content}</pre>
          <div><Button className="touch-hit" disabled={busy} onClick={() => {
            if (reviewed.current !== preview) return;
            reviewed.current = null;
            onAttach(preview);
          }}>{t("newagent.project_document_attach")}</Button></div>
        </div>
      )}
    </fieldset>
  );
}

function ProjectRevisionList({ page, document, disabled, onBack, onMore, onHistory, onPreview }: {
  page: ProjectDocumentPage;
  document?: string;
  disabled: boolean;
  onBack: () => void;
  onMore: () => void;
  onHistory: (id: string) => void;
  onPreview: (row: ApprovedProjectRevision) => void;
}) {
  const heading = document ? t("newagent.project_document_history", { document }) : t("newagent.project_document_catalog");
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center justify-between gap-1">
        <h3 className="min-w-0 text-meta font-medium text-ink [overflow-wrap:anywhere]">{heading}</h3>
        {document && <Button tone="quiet" size="sm" className="touch-hit" disabled={disabled} onClick={onBack}>
          {t("newagent.project_document_back")}
        </Button>}
      </div>
      {page.items.length === 0 && <p role="status" className="text-submeta text-ink-2">
        {document ? t("newagent.project_document_no_versions") : t("newagent.project_document_no_documents")}
      </p>}
      <ul aria-label={heading} className="max-h-48 overflow-auto divide-y divide-line">
        {page.items.map(row => (
          <li key={document ? row.revision : row.document} className="flex min-w-0 flex-wrap items-center justify-between gap-x-2 py-1">
            <div className="min-w-0 text-submeta [overflow-wrap:anywhere]">
              <div className="font-medium text-ink">{document
                ? t("newagent.project_document_version", row)
                : <>{row.document} · {t("newagent.project_document_version", row)}</>}</div>
              <div className="text-ink-2">{t("newagent.project_document_approved_details", {
                approver: row.approved_by, date: formatDate(row.approved_at),
              })}</div>
            </div>
            <div className="flex flex-wrap items-center">
              {!document && <Button tone="quiet" size="sm" className="touch-hit" disabled={disabled}
                aria-label={t("newagent.project_document_versions_of", row)} onClick={() => onHistory(row.document)}>
                {t("newagent.project_document_versions")}
              </Button>}
              <Button size="sm" className="touch-hit" disabled={disabled} aria-label={t("newagent.project_document_preview_version", row)} onClick={() => onPreview(row)}>
                {t("newagent.project_document_view")}
              </Button>
            </div>
          </li>
        ))}
      </ul>
      {page.next && <div><Button size="sm" tone="quiet" className="touch-hit" disabled={disabled} onClick={onMore}>
        {document ? t("newagent.project_document_older") : t("newagent.project_document_more")}
      </Button></div>}
    </div>
  );
}
