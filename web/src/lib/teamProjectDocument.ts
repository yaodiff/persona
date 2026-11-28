// The project-document service has its own member credentials. Keep this read independent of hub authentication.
export type TeamProjectDocument = {
  project: string;
  document: string;
  revision: number;
  content: string;
  sha256: string;
  author: string;
  approved_by: string;
};

export type ApprovedProjectRevision = Omit<TeamProjectDocument, "content"> & { approved_at: string };
export type ProjectDocumentPage = { items: ApprovedProjectRevision[]; next?: string };

type ErrorCode = "access" | "missing" | "invalid" | "unavailable";

export class TeamProjectDocumentError extends Error {
  constructor(readonly code: ErrorCode) {
    super(code);
    this.name = "TeamProjectDocumentError";
  }
}

function validId(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9_-]{1,100}$/.test(value);
}

const maxContentBytes = 256 * 1024;
// JSON can encode each content byte as six bytes (e.g. a control character).
const maxReplyBytes = 6 * maxContentBytes + 4096;

async function readReply(response: Response, byteLimit: number): Promise<unknown> {
  if (!response.body) throw new TeamProjectDocumentError("invalid");
  if (Number(response.headers.get("Content-Length")) > byteLimit) {
    void response.body.cancel().catch(() => {});
    throw new TeamProjectDocumentError("invalid");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let size = 0;
  let text = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > byteLimit) {
        void reader.cancel().catch(() => {});
        throw new TeamProjectDocumentError("invalid");
      }
      text += decoder.decode(value, { stream: true });
    }
    return JSON.parse(text + decoder.decode());
  } catch {
    throw new TeamProjectDocumentError("invalid");
  } finally {
    reader.releaseLock();
  }
}

async function requestProjectData(
  project: string, path: string, token: string, signal: AbortSignal, byteLimit: number,
): Promise<unknown> {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  const { origin, protocol, hostname } = location;
  const loopback = hostname === "localhost" || hostname.endsWith(".localhost") || hostname === "[::1]" ||
    /^127(?:\.\d{1,3}){3}$/.test(hostname);
  if (!(protocol === "https:" || (protocol === "http:" && loopback)) || !globalThis.crypto?.subtle) {
    throw new TeamProjectDocumentError("unavailable");
  }
  if (!validId(project)) throw new TeamProjectDocumentError("invalid");
  if (!/^[A-Za-z0-9_-]+$/.test(token)) throw new TeamProjectDocumentError("access");

  try {
    const response = await fetch(new URL(`/v1/projects/${project}/documents${path}`, origin), {
      method: "GET", headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      credentials: "omit", mode: "same-origin", cache: "no-store", redirect: "error", referrerPolicy: "no-referrer", signal,
    });
    if (response.status === 401 || response.status === 403) throw new TeamProjectDocumentError("access");
    if (response.status === 404) throw new TeamProjectDocumentError("missing");
    if (response.status !== 200) throw new TeamProjectDocumentError("unavailable");
    const row = await readReply(response, byteLimit);
    signal.throwIfAborted();
    return row;
  } catch (err) {
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    if (err instanceof TeamProjectDocumentError) throw err;
    throw new TeamProjectDocumentError("unavailable");
  }
}

function validRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function hasRevisionMetadata(value: unknown, project: string): value is Omit<TeamProjectDocument, "content"> {
  if (!value || typeof value !== "object") return false;
  const row = value as Partial<TeamProjectDocument>;
  return row.project === project && validId(row.document) && validRevision(row.revision) &&
    validId(row.author) && validId(row.approved_by) && typeof row.sha256 === "string" && /^[a-f0-9]{64}$/.test(row.sha256);
}

function isApprovedSummary(value: unknown, project: string): value is ApprovedProjectRevision {
  return hasRevisionMetadata(value, project) && !("content" in value) &&
    "approved_at" in value && typeof value.approved_at === "string" && Number.isFinite(Date.parse(value.approved_at));
}

async function loadProjectPage(
  project: string, document: string | undefined, token: string, signal: AbortSignal, cursor?: string,
): Promise<ProjectDocumentPage> {
  let path = "";
  if (document === undefined) {
    if (cursor !== undefined && !validId(cursor)) throw new TeamProjectDocumentError("invalid");
    if (cursor !== undefined) path = `?after=${cursor}`;
  } else {
    if (!validId(document) || (cursor !== undefined && (!validRevision(Number(cursor)) || String(Number(cursor)) !== cursor))) {
      throw new TeamProjectDocumentError("invalid");
    }
    path = `/${document}/approvals` + (cursor === undefined ? "" : `?before=${cursor}`);
  }
  const page = await requestProjectData(project, path, token, signal, 128 * 1024);
  if (!page || typeof page !== "object" || !("items" in page) || !Array.isArray(page.items) || page.items.length > 100) {
    throw new TeamProjectDocumentError("invalid");
  }
  const items: ApprovedProjectRevision[] = [];
  let previous = cursor;
  for (const row of page.items) {
    if (!isApprovedSummary(row, project)) throw new TeamProjectDocumentError("invalid");
    if (document === undefined) {
      if (previous !== undefined && row.document <= previous) throw new TeamProjectDocumentError("invalid");
      previous = row.document;
    } else {
      if (row.document !== document || (previous !== undefined && row.revision >= Number(previous))) {
        throw new TeamProjectDocumentError("invalid");
      }
      previous = String(row.revision);
    }
    items.push(row);
  }
  if ("next" in page) {
    if (!items.length || typeof page.next !== "string" || page.next !== previous) throw new TeamProjectDocumentError("invalid");
    return { items, next: page.next };
  }
  return { items };
}

export function listProjectDocuments(project: string, token: string, signal: AbortSignal, after?: string) {
  return loadProjectPage(project, undefined, token, signal, after);
}

export function listProjectDocumentApprovals(project: string, document: string, token: string, signal: AbortSignal, before?: string) {
  return loadProjectPage(project, document, token, signal, before);
}

export async function loadApprovedProjectDocument(
  project: string, document: string, token: string, signal: AbortSignal, selected?: ApprovedProjectRevision,
): Promise<TeamProjectDocument> {
  if (!validId(document) || (selected && (!isApprovedSummary(selected, project) || selected.document !== document))) {
    throw new TeamProjectDocumentError("invalid");
  }
  const version = selected ? `revisions/${selected.revision}` : "approved";
  const doc = await requestProjectData(project, `/${document}/${version}`, token, signal, maxReplyBytes);
  if (!hasRevisionMetadata(doc, project) || doc.document !== document || !("content" in doc) ||
    typeof doc.content !== "string" || !doc.content.length || doc.content.length > maxContentBytes ||
    /[\u0000\uD800-\uDFFF]/u.test(doc.content)) throw new TeamProjectDocumentError("invalid");
  if (selected && (selected.revision !== doc.revision || selected.sha256 !== doc.sha256 ||
    selected.approved_by !== doc.approved_by || selected.author !== doc.author)) {
    throw new TeamProjectDocumentError("invalid");
  }
  const bytes = new TextEncoder().encode(doc.content);
  if (bytes.length > maxContentBytes) throw new TeamProjectDocumentError("invalid");
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  const digest = Array.from(hash, byte => byte.toString(16).padStart(2, "0")).join("");
  if (digest !== doc.sha256) throw new TeamProjectDocumentError("invalid");
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  return { project, document, revision: doc.revision, content: doc.content, sha256: digest, author: doc.author, approved_by: doc.approved_by };
}
