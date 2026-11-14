import { CLIENT_HEADERS } from "./compatibility";
// Changing a machine's files from the Files panel (#4883, the hub / node side is #4858): upload into a folder, rename,
// move, move to the trash and put back. Only home is writable there, and not ~/.persona, ~/.persona or ~/Library (the
// node checks, `read_only`); a delete goes to the system's trash and can be undone, there is no permanent one.
import { ApiError, api, CLIENT_PLATFORM, CLIENT_VERSION, credentialOwner, enc, errorText, getToken, subscribeCredentialOwner } from "./api";
import { BASE } from "./base";
import { currentLanguage, t } from "../i18n";

/** hello `features`: uploads take `dest=dir` (#4858 part 1). */
export const UPLOAD_DIR_FEATURE = "upload_dir";
/** hello `features`: rename / move / trash / restore (#4858 part 2). */
export const FILE_EDIT_FEATURE = "file_edit";
/** New folders, text files and zips (#5828; docs/protocol/protocol.md "Changing files"). */
export const FILE_MKDIR_FEATURE = "file_mkdir";
export const FILE_WRITE_FEATURE = "file_write";
export const FILE_ZIP_FEATURE = "file_zip";
/** Copy and duplicate (#5830). */
export const FILE_COPY_FEATURE = "file_copy";

/** What an upload with a name already there does: `replace` it, or `keep` both ("name 2.extension", as Finder does). */
export type Conflict = "replace" | "keep";

export interface Uploaded {
  path: string;
  name: string;
  size: number;
}

/** In-memory authorization captured when an action is admitted, never rendered or persisted. */
export interface FileCredential {
  owner: string;
  token: string;
  isAuthContextCurrent?: () => boolean;
}
export type UploadCredential = FileCredential;

function mutationAuth(credential?: FileCredential): { token?: string; tokenOwner?: string; isAuthContextCurrent?: () => boolean } {
  if (!credential) return {};
  if (!credential.token || credential.owner !== credentialOwner() || credential.token !== getToken() || credential.isAuthContextCurrent?.() === false) {
    throw new DOMException("aborted", "AbortError");
  }
  return { token: credential.token, tokenOwner: credential.owner, isAuthContextCurrent: credential.isAuthContextCurrent };
}

/**
 * Sends `file` into folder `dir` on machine `mid` (`POST /machines/{mid}/files?dest=dir`), reporting the bytes sent. A
 * name already there is refused before any byte goes (409 `exists`) unless `conflict` says what to do. Aborting
 * `signal` or retiring the credential drops the request where possible; an already dispatched server write may
 * still finish. Rejects with an ApiError (the hub's code and words) or an AbortError.
 */
export function uploadToDir(
  mid: string,
  file: File,
  opts: { dir: string; cwd?: string; conflict?: Conflict; credential?: UploadCredential; signal?: AbortSignal; onProgress?: (sent: number, total: number) => void },
): Promise<Uploaded> {
  const credential = opts.credential ?? { owner: credentialOwner(), token: getToken() };
  const q = `name=${enc(file.name)}&dest=dir&dir=${enc(opts.dir)}${opts.cwd ? `&cwd=${enc(opts.cwd)}` : ""}${opts.conflict ? `&conflict=${opts.conflict}` : ""}`;
  return new Promise((resolve, reject) => {
    // XHR, not fetch: only it reports an upload's progress
    const xhr = new XMLHttpRequest();
    let settled = false;
    let off = () => {};
    const finish = (done: () => void) => {
      if (settled) return;
      settled = true;
      off();
      opts.signal?.removeEventListener("abort", cancel);
      done();
    };
    const cancel = () => {
      if (settled) return;
      finish(() => reject(new DOMException("aborted", "AbortError")));
      xhr.abort(); // settle first, including transports that ignore abort or report it synchronously
    };
    const current = () => {
      if (settled) return false;
      if (opts.signal?.aborted || credential.owner !== credentialOwner() || credential.token !== getToken()) {
        cancel();
        return false;
      }
      return !settled;
    };
    off = subscribeCredentialOwner(() => { current(); });
    if (!current()) return;
    xhr.open("POST", `${BASE}/api/v1/machines/${enc(mid)}/files?${q}`);
    xhr.setRequestHeader("Authorization", `Bearer ${credential.token}`);
    xhr.setRequestHeader("Accept-Language", currentLanguage());
    CLIENT_HEADERS.forEach(name => xhr.setRequestHeader(name, `${CLIENT_PLATFORM}/${CLIENT_VERSION}`));
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.onprogress = (e) => { if (current()) opts.onProgress?.(e.loaded, e.lengthComputable ? e.total : file.size); };
    xhr.onload = () => {
      if (!current()) return;
      let body: Record<string, unknown> = {};
      let isJson = false;
      try {
        const parsed = xhr.responseText ? JSON.parse(xhr.responseText) : {};
        isJson = typeof parsed === "object" && parsed !== null && !Array.isArray(parsed);
        if (isJson) body = parsed;
      } catch {
        body = { error: xhr.responseText };
      }
      if (xhr.status >= 200 && xhr.status < 300) return finish(() => resolve(body as unknown as Uploaded));
      const msg = typeof body.error === "string" && body.error ? body.error : `HTTP ${xhr.status}`;
      finish(() => reject(new ApiError(xhr.status, msg, body, isJson, xhr.getResponseHeader("X-Request-Id") ?? undefined)));
    };
    xhr.onerror = () => { if (current()) finish(() => reject(new TypeError("network"))); };
    xhr.onabort = () => finish(() => reject(new DOMException("aborted", "AbortError")));
    opts.signal?.addEventListener("abort", cancel, { once: true });
    try {
      if (current()) xhr.send(file);
    } catch (e) {
      finish(() => reject(e));
    }
  });
}

/** Renames `path` to `name` in the same folder; resolves to its new path. */
export async function renameEntry(mid: string, path: string, cwd: string, name: string, credential?: FileCredential): Promise<string> {
  const r = await api<{ path: string }>(`/machines/${enc(mid)}/files/rename`, { method: "POST", json: { path, cwd, name }, ...mutationAuth(credential) });
  return r.path;
}

/** Moves `paths` into folder `dir`. */
export async function moveEntries(mid: string, paths: string[], cwd: string, dir: string, conflict?: Conflict, credential?: FileCredential): Promise<{ from: string; to: string }[]> {
  const r = await api<{ moved?: { from: string; to: string }[] }>(`/machines/${enc(mid)}/files/move`, {
    method: "POST",
    json: { paths, cwd, dir, ...(conflict ? { conflict } : {}) },
    ...mutationAuth(credential),
  });
  return r.moved ?? [];
}

/** A long copy / zip: its original request can be aborted and its operation polled for progress. */
export interface RemoteRequestOptions {
  operationId?: string;
  signal?: AbortSignal;
}

/** Copies `paths` into folder `dir` (#5830); a copy into their own folder with `keep` duplicates them. */
export async function copyEntries(mid: string, paths: string[], cwd: string, dir: string, conflict?: Conflict, credential?: FileCredential, options: RemoteRequestOptions = {}): Promise<{ from: string; to: string }[]> {
  const r = await api<{ copied?: { from: string; to: string }[] }>(`/machines/${enc(mid)}/files/copy`, {
    method: "POST",
    json: { paths, cwd, dir, ...(conflict ? { conflict } : {}), ...(options.operationId ? { operation_id: options.operationId } : {}) },
    timeout: 610_000,
    signal: options.signal,
    ...mutationAuth(credential),
  });
  return r.copied ?? [];
}

/** Moves `paths` to the machine's trash; resolves to what puts each back (restoreEntries). */
export async function trashEntries(mid: string, paths: string[], cwd: string, credential?: FileCredential): Promise<{ path: string; trash_id: string }[]> {
  const r = await api<{ trashed?: { path: string; trash_id: string }[] }>(`/machines/${enc(mid)}/files/trash`, { method: "POST", json: { paths, cwd }, ...mutationAuth(credential) });
  return r.trashed ?? [];
}

/** Puts what trashEntries moved back where it was (Undo). */
export async function restoreEntries(mid: string, trashIds: string[], credential?: FileCredential): Promise<string[]> {
  const r = await api<{ restored?: { path: string }[] }>(`/machines/${enc(mid)}/files/restore`, { method: "POST", json: { trash_ids: trashIds }, ...mutationAuth(credential) });
  return (r.restored ?? []).map((x) => x.path);
}

/** Makes folder `name` in `dir` (#5828); resolves to its path. */
export async function makeFolder(mid: string, dir: string, cwd: string, name: string, credential?: FileCredential): Promise<string> {
  const r = await api<{ path: string }>(`/machines/${enc(mid)}/files/mkdir`, { method: "POST", json: { dir, cwd, name }, ...mutationAuth(credential) });
  return r.path;
}

/** What files/write answers: the file as it is now, for the next save's check. */
export interface WrittenFile {
  path: string;
  size: number;
  revision: string;
  mtime?: string;
}

/** Writes a text file (#5828, #5829): `create` makes a new one (409 `exists` when taken); otherwise it replaces the file
 *  while it is still at `revision` (or hashes to `sha256`), else 409 `file_changed`; `force`: Overwrite Anyway. */
export async function writeTextFile(
  mid: string,
  path: string,
  cwd: string,
  text: string,
  opts: { create?: boolean; revision?: string; sha256?: string; force?: boolean } = {},
  credential?: FileCredential,
): Promise<WrittenFile> {
  return api<WrittenFile>(`/machines/${enc(mid)}/files/write`, { method: "POST", json: { path, cwd, text, ...opts }, ...mutationAuth(credential) });
}

/** Zips `paths` into `dir` as `name`, or with no `dir` into a temporary file on the computer for a download (#5828). */
export async function zipEntries(mid: string, paths: string[], cwd: string, name: string, dir?: string, credential?: FileCredential, options: RemoteRequestOptions = {}): Promise<{ path: string; name: string; size: number; files: number }> {
  return api(`/machines/${enc(mid)}/files/zip`, {
    method: "POST",
    json: { paths, cwd, name, ...(dir ? { dir } : {}), ...(options.operationId ? { operation_id: options.operationId } : {}) },
    timeout: 610_000,
    signal: options.signal,
    ...mutationAuth(credential),
  });
}

export interface FileOperation {
  id: string;
  kind: string;
  state: "running" | "done" | "failed" | "cancelled";
  files_done: number;
  files_total: number;
  bytes_done: number;
  bytes_total: number;
}

/** Progress for a copy / zip's original POST. A terminal state here never substitutes for that POST's response. */
export function fileOperation(mid: string, id: string, credential?: FileCredential, signal?: AbortSignal): Promise<FileOperation> {
  return api(`/machines/${enc(mid)}/files/operations/${enc(id)}`, { signal, ...mutationAuth(credential) });
}

/** Why a name can't be a file's (`null`: it can). The node checks again; this is only to say it before asking. */
export function nameProblem(name: string): string | null {
  const n = name.trim();
  if (!n) return t("lib-file-ops.name_empty");
  if (n === "." || n === "..") return t("lib-file-ops.name_dots");
  if (/[/\\\0]/.test(n)) return t("lib-file-ops.name_slash");
  return null;
}

/** The folder `path` is in ("/" for a top-level one). */
export function parentOf(path: string): string {
  const p = path.replace(/\/+$/, "");
  const i = p.lastIndexOf("/");
  return i <= 0 ? "/" : p.slice(0, i);
}

/** Whether `inside` is `dir` or under it (a folder can't be moved into itself). */
export function within(dir: string, inside: string): boolean {
  const d = dir.replace(/\/+$/, "");
  return inside === d || inside.startsWith(d + "/");
}

/** What the user is told about a failed change: the hub's codes in their words, anything else as the hub said it. */
export function opErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "read_only":
        return t("lib-file-ops.read_only");
      case "exists":
        return t("lib-file-ops.exists", { name: typeof e.body.name === "string" ? e.body.name : "" });
      case "node_outdated":
        return t("lib-file-ops.node_outdated");
      case "too_large":
        return t("lib-file-ops.too_large");
      case "not_found":
        return t("lib-file-ops.not_found");
      case "node_unreachable":
      case "timeout":
        return t("lib-file-ops.unreachable");
      case "internal":
        return e.message; // the node's own reason (a folder that is not there, not a folder): #4858 says show it
    }
    if (e.status === 404 && !e.code) return t("lib-file-ops.hub_outdated"); // a hub without the endpoint
  }
  return errorText(e);
}

/** A 409 `exists`: the name is taken there, and the user chooses replace or keep both. */
export const isNameTaken = (e: unknown): boolean => e instanceof ApiError && e.status === 409 && e.code === "exists";
