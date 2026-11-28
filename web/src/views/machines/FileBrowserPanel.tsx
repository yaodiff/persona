// Lazy-loaded machine file browser (Phase 6A read-only + Phase 6B human-facing write/upload).
// Single-level directory listing, quick root shortcuts, bounded text preview (<= 2 MiB),
// lightweight UTF-8 editor with mtime+SHA-256 CAS & pre-save unified diff confirmation,
// sensitive-file warnings, create/rename/move/safe-delete, log tail, streaming download & upload.
// Strictly human-facing Web UI only — zero AI MCP tools, kept inside the lazy chunk.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { currentLanguage, formatDate, t } from "../../i18n";
import { api, ApiError, enc, errorText, getToken, isAbort } from "../../lib/api";
import { BASE } from "../../lib/base";
import { writableHere } from "../../lib/files-nav";
import { openPanel } from "../../lib/panel";
import { hubHas, useStore } from "../../lib/store";
import { FileInfoDialog } from "../session/files/FileInfo";
import { FILE_CHMOD_FEATURE, PermissionsDialog } from "../session/files/Permissions";
import { dragHasFiles, SelectionBar, useFileOps, type OpTarget } from "../session/files/FileOps";
import { FilesPanel } from "../session/FilesPanel";
import { navigate } from "../../lib/router";
import type { Machine } from "../../lib/types";
import { MachineTool, type MachineToolProps } from "./MachinePicker";
import { Button, cx, IconButton, Loading, Menu } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import {
  classifySensitivePathClient,
  computeLineDiff,
  detectLineEnding,
  normalizeToLF,
  restoreLineEnding,
  type LineEnding,
} from "./fileDiff";

export interface MachineFileRoot {
  id: string;
  label: string;
  path: string;
  kind: "home" | "workspace" | "config" | string;
  exists: boolean;
}

export interface MachineFileRootsResponse {
  home: string;
  os: string;
  roots: MachineFileRoot[];
}

export interface MachineFileEntry {
  name: string;
  path: string;
  kind: "dir" | "file" | "symlink" | "other" | string;
  size: number;
  mode: string;
  perm: number;
  mtime?: string;
  symlink?: boolean;
  link_target?: string;
  link_broken?: boolean;
  target_kind?: string;
  sensitive?: boolean;
  sensitive_reason?: string;
  binary?: boolean;
}

export interface MachineFileListResponse {
  path: string;
  canonical_path: string;
  parent?: string;
  entries: MachineFileEntry[];
  total: number;
  truncated?: boolean;
}

export interface MachineFilePreviewResponse {
  path: string;
  canonical_path: string;
  name: string;
  size: number;
  mode: string;
  perm: number;
  mtime?: string;
  mime: string;
  revision: string;
  sha256?: string;
  editable?: boolean;
  edit_max_bytes?: number;
  symlink?: boolean;
  link_target?: string;
  sensitive?: boolean;
  sensitive_reason?: string;
  binary?: boolean;
  text?: string;
  bytes_read: number;
  truncated?: boolean;
  max_bytes: number;
}

export interface MachineFileTailResponse {
  path: string;
  canonical_path: string;
  name: string;
  size: number;
  mtime?: string;
  mime: string;
  revision: string;
  sensitive?: boolean;
  sensitive_reason?: string;
  binary?: boolean;
  text?: string;
  lines: number;
  requested_lines: number;
  bytes_read: number;
  offset: number;
  truncated?: boolean;
}

export interface MachineFileWriteResponse {
  path: string;
  canonical_path: string;
  name: string;
  size: number;
  mode: string;
  perm: number;
  mtime: string;
  revision: string;
  sha256: string;
  sensitive?: boolean;
  sensitive_reason?: string;
}

export interface MachineFileCreateResponse {
  entry: MachineFileEntry;
  canonical_path: string;
  revision?: string;
  sha256?: string;
}

export interface MachineFileRenameResponse {
  old_path: string;
  path: string;
  canonical_path: string;
  name: string;
  entry: MachineFileEntry;
}

export interface MachineFileDeleteResponse {
  path: string;
  canonical_path: string;
  kind: string;
  deleted: boolean;
}

export interface MachineFileUploadEndResponse {
  entry: MachineFileEntry;
  canonical_path: string;
  size: number;
  sha256: string;
}

type ViewMode = "preview" | "tail";
type CreateKind = "file" | "dir";

const TAIL_LINE_OPTIONS = [100, 200, 500, 1000] as const;
const LARGE_LOG_THRESHOLD = 256 * 1024;
const DEFAULT_EDIT_MAX_BYTES = 1024 * 1024; // 1 MiB
const MAX_UPLOAD_BYTES = 100 * 1024 * 1024; // 100 MiB

/** Streaming single-file download URL on the authenticated Hub HTTP route. */
export function machineFileDownloadURL(mid: string, path: string): string {
  return `${BASE}/api/v1/machines/${enc(mid)}/files/download?path=${enc(path)}&token=${enc(getToken())}`;
}

/** Streaming upload URL on the authenticated Hub HTTP route. */
export function machineFileUploadURL(
  mid: string,
  opts: { dir: string; name: string; overwrite?: boolean; confirmSensitive?: boolean },
): string {
  const params = [`dir=${enc(opts.dir)}`, `name=${enc(opts.name)}`];
  if (opts.overwrite) params.push("overwrite=1");
  if (opts.confirmSensitive) params.push("confirm_sensitive=1");
  return `${BASE}/api/v1/machines/${enc(mid)}/files/upload?${params.join("&")}`;
}

function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 ? value.toFixed(1) : value.toFixed(2)} ${units[unit]}`;
}

function formatMtime(iso?: string): string {
  if (!iso) return "";
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return "";
  return formatDate(new Date(ms), { dateStyle: "short", timeStyle: "short" });
}

function utf8ByteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

function rootKindLabel(kind: string): string {
  switch (kind) {
    case "home":
      return t("machines-files.root_home");
    case "workspace":
      return t("machines-files.root_workspace");
    default:
      return t("machines-files.root_config");
  }
}

function sensitiveReasonLabel(reason?: string): string {
  switch (reason) {
    case "env":
      return t("machines-files.sensitive_env");
    case "private_key":
      return t("machines-files.sensitive_private_key");
    case "hub_config":
      return t("machines-files.sensitive_hub_config");
    case "credentials":
      return t("machines-files.sensitive_credentials");
    default:
      return t("machines-files.sensitive_protected");
  }
}

function entryKindLabel(entry: MachineFileEntry): string {
  if (entry.symlink || entry.kind === "symlink") return t("machines-files.kind_symlink");
  if (entry.kind === "dir") return t("machines-files.kind_dir");
  return t("machines-files.kind_file");
}

function isNavigableDir(entry: MachineFileEntry): boolean {
  if (entry.kind === "dir") return true;
  return Boolean(entry.symlink && !entry.link_broken && entry.target_kind === "dir");
}

function shouldDefaultToTail(entry: MachineFileEntry): boolean {
  const lower = entry.name.toLowerCase();
  if (entry.size >= LARGE_LOG_THRESHOLD && !entry.binary) return true;
  return (lower.endsWith(".log") || lower.endsWith(".out") || lower.endsWith(".jsonl")) && entry.size > 32 * 1024;
}

function joinDisplayPath(dirPath: string, name: string): string {
  const cleanDir = dirPath.replace(/[\\/]+$/, "");
  const cleanName = name.trim();
  if (!cleanName) return cleanDir || dirPath;
  return `${cleanDir}/${cleanName}`;
}

function classifyTargetPath(dir: MachineFileListResponse | null, name: string): { sensitive: boolean; reason: string } {
  const trimmed = name.trim();
  const candidates = [
    dir?.path ? joinDisplayPath(dir.path, trimmed) : trimmed,
    dir?.canonical_path ? joinDisplayPath(dir.canonical_path, trimmed) : "",
  ];
  for (const p of candidates) {
    if (!p) continue;
    const res = classifySensitivePathClient(p);
    if (res.sensitive) return res;
  }
  return { sensitive: false, reason: "" };
}

type DirectoryTarget = { path?: string; routed?: boolean };

export function FileBrowserPanel({ path, routed, ...props }: MachineToolProps & DirectoryTarget) {
  return (
    <MachineTool {...props}>
      {(machine) => <FileBrowser machine={machine} path={path} routed={routed} />}
    </MachineTool>
  );
}

function FileBrowser({ machine, path, routed }: { machine?: Machine } & DirectoryTarget) {
  const [roots, setRoots] = useState<MachineFileRoot[]>([]);
  const [dir, setDir] = useState<MachineFileListResponse | null>(null);
  const [legacy, setLegacy] = useState(false);
  const [pathInput, setPathInput] = useState(path || "~");
  const [filter, setFilter] = useState("");
  const [loadingDir, setLoadingDir] = useState(true);
  const [dirError, setDirError] = useState("");
  const [dirNotice, setDirNotice] = useState("");

  const [selectedEntry, setSelectedEntry] = useState<MachineFileEntry | null>(null);
  const [mode, setMode] = useState<ViewMode>("preview");
  const [tailLines, setTailLines] = useState<number>(200);
  const [wrap, setWrap] = useState(false);
  const [preview, setPreview] = useState<MachineFilePreviewResponse | null>(null);
  const [tail, setTail] = useState<MachineFileTailResponse | null>(null);
  const [loadingFile, setLoadingFile] = useState(false);
  const [fileError, setFileError] = useState("");

  // Lightweight editor + diff confirm + CAS state
  const [editing, setEditing] = useState(false);
  const [draftText, setDraftText] = useState("");
  const [draftEol, setDraftEol] = useState<LineEnding>("lf");
  const [reviewingDiff, setReviewingDiff] = useState(false);
  const [sensitiveSaveAck, setSensitiveSaveAck] = useState(false);
  const [saving, setSaving] = useState(false);
  const [savedNotice, setSavedNotice] = useState("");
  const [casConflict, setCasConflict] = useState(false);

  // Create file / folder state
  const [createKind, setCreateKind] = useState<CreateKind | null>(null);
  const [createName, setCreateName] = useState("");
  const [createText, setCreateText] = useState("");
  const [createSensitiveAck, setCreateSensitiveAck] = useState(false);
  const [createServerSensitive, setCreateServerSensitive] = useState<{ reason: string } | null>(null);
  const [creating, setCreating] = useState(false);

  // Rename / move state
  const [renameTarget, setRenameTarget] = useState<MachineFileEntry | null>(null);
  const [renameDest, setRenameDest] = useState("");
  const [renameSensitiveAck, setRenameSensitiveAck] = useState(false);
  const [renaming, setRenaming] = useState(false);

  // Delete confirmation state
  const [deleteTarget, setDeleteTarget] = useState<MachineFileEntry | null>(null);
  const [deleteSensitiveAck, setDeleteSensitiveAck] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // Streaming upload state
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const uploadXhrRef = useRef<XMLHttpRequest | null>(null);
  const [uploadProgress, setUploadProgress] = useState<{ name: string; loaded: number; total: number } | null>(null);
  const [pendingUpload, setPendingUpload] = useState<{
    mid: string;
    dir: string;
    file: File;
    kind: "sensitive" | "exists";
    reason?: string;
    confirmedSensitive?: boolean;
    overwrite?: boolean;
  } | null>(null);
  const [uploadSensitiveAck, setUploadSensitiveAck] = useState(false);

  const currentDirRef = useRef<{ mid: string; path: string }>({ mid: "", path: "" });
  const dirAbort = useRef<AbortController | null>(null);
  const fileAbort = useRef<AbortController | null>(null);

  const resetEditor = useCallback(() => {
    setEditing(false);
    setDraftEol("lf");
    setReviewingDiff(false);
    setSensitiveSaveAck(false);
    setSaving(false);
    setSavedNotice("");
    setCasConflict(false);
  }, []);

  const loadDirectory = useCallback(async (mid: string, targetPath: string) => {
    if (!mid) return;
    const prevDir = currentDirRef.current;
    if (prevDir.mid !== mid || prevDir.path !== targetPath) {
      setPendingUpload(null);
      setUploadSensitiveAck(false);
    }
    currentDirRef.current = { mid, path: targetPath };
    dirAbort.current?.abort();
    const ctrl = new AbortController();
    dirAbort.current = ctrl;
    setLoadingDir(true);
    setDirError("");
    try {
      const qs = targetPath ? `?path=${enc(targetPath)}` : "";
      const res = await api<MachineFileListResponse>(`/machines/${enc(mid)}/files/list${qs}`, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      currentDirRef.current = { mid, path: res.path };
      setDir(res);
      setPathInput(res.path);
      setFilter("");
      setCreateServerSensitive(null);
      setCreateSensitiveAck(false);
      setPendingUpload((cur) => (cur && (cur.mid !== mid || cur.dir !== res.path) ? null : cur));
    } catch (err) {
      if (ctrl.signal.aborted || isAbort(err)) return;
      if (err instanceof ApiError && (err.code === "node_outdated" || (err.status === 404 && !err.code))) setLegacy(true);
      currentDirRef.current = prevDir;
      setDirError(t("machines-files.load_error", { error: errorText(err) }));
    } finally {
      if (!ctrl.signal.aborted) setLoadingDir(false);
    }
  }, []);

  const folder = dir?.path || path || "~";
  const reload = () => { if (machine?.online) void loadDirectory(machine.id, folder); };
  const ops = useFileOps({ mid: machine?.id ?? "", cwd: folder, onChanged: reload });
  const features = useStore(s => s.features);
  const writable = writableHere(folder, machine?.home);
  const [selected, select] = useState<OpTarget[] | null>(null);
  const [menu, setMenu] = useState<{ entry: MachineFileEntry; anchor: HTMLElement } | null>(null);
  const [info, setInfo] = useState<MachineFileEntry | null>(null);
  const [permissions, setPermissions] = useState<string | null>(null);
  const canChmod = writable && hubHas(features, FILE_CHMOD_FEATURE) && machine?.os?.toLowerCase() !== "windows";
  const target = (entry: MachineFileEntry): OpTarget => ({ path: entry.path, name: entry.name, dir: isNavigableDir(entry) });

  const loadFile = useCallback(async (mid: string, targetPath: string, viewMode: ViewMode, lines: number) => {
    if (!mid || !targetPath) return;
    fileAbort.current?.abort();
    const ctrl = new AbortController();
    fileAbort.current = ctrl;
    setLoadingFile(true);
    setFileError("");
    resetEditor();
    try {
      if (viewMode === "tail") {
        const res = await api<MachineFileTailResponse>(
          `/machines/${enc(mid)}/files/tail?path=${enc(targetPath)}&lines=${lines}`,
          { signal: ctrl.signal },
        );
        if (ctrl.signal.aborted) return;
        setTail(res);
        setPreview(null);
      } else {
        const res = await api<MachineFilePreviewResponse>(
          `/machines/${enc(mid)}/files/preview?path=${enc(targetPath)}`,
          { signal: ctrl.signal },
        );
        if (ctrl.signal.aborted) return;
        setPreview(res);
        setTail(null);
      }
    } catch (err) {
      if (isAbort(err)) return;
      setFileError(t("machines-files.load_error", { error: errorText(err) }));
    } finally {
      if (!ctrl.signal.aborted) setLoadingFile(false);
    }
  }, [resetEditor]);

  // List the requested folder immediately. Optional root shortcuts load independently.
  useEffect(() => {
    setLegacy(false);
    select(null);
    setInfo(null);
    setPermissions(null);
    setMenu(null);
    setRoots([]);
    setDir(null);
    setDirError("");
    setDirNotice("");
    setSelectedEntry(null);
    setPreview(null);
    setTail(null);
    setFileError("");
    setCreateKind(null);
    setRenameTarget(null);
    setDeleteTarget(null);
    setPendingUpload(null);
    setUploadSensitiveAck(false);
    setUploadProgress(null);
    currentDirRef.current = { mid: machine?.id ?? "", path: "" };
    resetEditor();
    if (!machine?.online) return;

    const ctrl = new AbortController();
    void loadDirectory(machine.id, path || "~");
    api<MachineFileRootsResponse>(`/machines/${enc(machine.id)}/files/roots`, { signal: ctrl.signal })
      .then((res) => {
        if (ctrl.signal.aborted) return;
        setRoots(res.roots ?? []);
      })
      .catch(() => {}); // Root shortcuts are optional. Directory failures appear in the listing.

    return () => {
      ctrl.abort();
      dirAbort.current?.abort();
      fileAbort.current?.abort();
      uploadXhrRef.current?.abort();
    };
  }, [machine?.id, machine?.online, loadDirectory, resetEditor, path]);

  const browse = useCallback((mid: string, next: string) => {
    if (routed && next !== path) navigate({ name: "machine", mid, subview: "files", path: next });
    else void loadDirectory(mid, next);
  }, [routed, path, loadDirectory]);

  const openEntry = useCallback((entry: MachineFileEntry) => {
    if (!machine?.online) return;
    if (isNavigableDir(entry)) {
      setSelectedEntry(null);
      setPreview(null);
      setTail(null);
      setFileError("");
      setPendingUpload(null);
      setUploadSensitiveAck(false);
      resetEditor();
      browse(machine.id, entry.path);
      return;
    }
    const nextMode: ViewMode = shouldDefaultToTail(entry) ? "tail" : "preview";
    setSelectedEntry(entry);
    setMode(nextMode);
    void loadFile(machine.id, entry.path, nextMode, tailLines);
  }, [machine?.id, machine?.online, browse, loadFile, tailLines, resetEditor]);

  const switchMode = useCallback((nextMode: ViewMode) => {
    setMode(nextMode);
    if (machine?.online && selectedEntry) {
      void loadFile(machine.id, selectedEntry.path, nextMode, tailLines);
    }
  }, [machine?.id, machine?.online, selectedEntry, loadFile, tailLines]);

  const changeTailLines = useCallback((nextLines: number) => {
    setTailLines(nextLines);
    if (machine?.online && selectedEntry && mode === "tail") {
      void loadFile(machine.id, selectedEntry.path, "tail", nextLines);
    }
  }, [machine?.id, machine?.online, selectedEntry, mode, loadFile]);

  const previewEol = useMemo(
    () => (preview && !preview.binary ? detectLineEnding(preview.text ?? "") : "lf"),
    [preview],
  );
  const previewMixedEol = Boolean(preview && !preview.binary && previewEol === "mixed");

  const startEdit = useCallback(() => {
    if (!preview || !preview.editable || previewMixedEol) return;
    const raw = preview.text ?? "";
    setDraftEol(previewEol);
    setDraftText(normalizeToLF(raw));
    setEditing(true);
    setReviewingDiff(false);
    setSensitiveSaveAck(false);
    setSavedNotice("");
    setFileError("");
    setCasConflict(false);
  }, [preview, previewEol, previewMixedEol]);

  const saveText = useMemo(
    () => (editing ? restoreLineEnding(draftText, draftEol) : ""),
    [editing, draftText, draftEol],
  );

  const confirmSave = useCallback(async () => {
    if (!machine?.online || !preview || !preview.mtime || !preview.sha256) return;
    setSaving(true);
    setFileError("");
    setCasConflict(false);
    try {
      const res = await api<MachineFileWriteResponse>(`/machines/${enc(machine.id)}/files/content`, {
        method: "PUT",
        json: {
          path: preview.path,
          text: saveText,
          expected_mtime: preview.mtime,
          expected_sha256: preview.sha256,
          expected_revision: preview.revision,
          confirm_sensitive: preview.sensitive ? sensitiveSaveAck : false,
        },
      });
      setPreview({
        ...preview,
        text: saveText,
        size: res.size,
        bytes_read: res.size,
        mode: res.mode,
        perm: res.perm,
        mtime: res.mtime,
        revision: res.revision,
        sha256: res.sha256,
        sensitive: res.sensitive ?? preview.sensitive,
        sensitive_reason: res.sensitive_reason ?? preview.sensitive_reason,
      });
      setEditing(false);
      setDraftEol("lf");
      setReviewingDiff(false);
      setSensitiveSaveAck(false);
      setSavedNotice(t("machines-files.saved_notice"));
      if (dir?.path) void loadDirectory(machine.id, dir.path);
    } catch (err) {
      if (err instanceof ApiError && err.code === "file_changed") {
        setCasConflict(true);
        setReviewingDiff(false);
      } else {
        setFileError(t("machines-files.action_error", { error: errorText(err) }));
      }
    } finally {
      setSaving(false);
    }
  }, [machine?.id, machine?.online, preview, saveText, sensitiveSaveAck, dir?.path, loadDirectory]);

  const submitCreate = useCallback(async () => {
    if (!machine?.online || !dir?.path || !createKind || !createName.trim()) return;
    const sens = createServerSensitive ? { sensitive: true, reason: createServerSensitive.reason } : classifyTargetPath(dir, createName);
    setCreating(true);
    setDirError("");
    setDirNotice("");
    try {
      const res = await api<MachineFileCreateResponse>(`/machines/${enc(machine.id)}/files/create`, {
        method: "POST",
        json: {
          dir: dir.path,
          name: createName.trim(),
          kind: createKind,
          text: createKind === "file" ? createText : undefined,
          confirm_sensitive: sens.sensitive ? createSensitiveAck : false,
        },
      });
      setCreateKind(null);
      setCreateName("");
      setCreateText("");
      setCreateSensitiveAck(false);
      setCreateServerSensitive(null);
      await loadDirectory(machine.id, dir.path);
      if (createKind === "file" && res.entry) {
        setSelectedEntry(res.entry);
        setMode("preview");
        void loadFile(machine.id, res.entry.path, "preview", tailLines);
      }
    } catch (err) {
      if (err instanceof ApiError && err.code === "machine_file_sensitive_confirm") {
        const reason = typeof err.body.sensitive_reason === "string" ? err.body.sensitive_reason : "";
        setCreateServerSensitive({ reason });
        setCreateSensitiveAck(false);
        setDirError("");
      } else {
        setDirError(t("machines-files.action_error", { error: errorText(err) }));
      }
    } finally {
      setCreating(false);
    }
  }, [machine?.id, machine?.online, dir, createKind, createName, createText, createSensitiveAck, createServerSensitive, loadDirectory, loadFile, tailLines]);

  const submitRename = useCallback(async () => {
    if (!machine?.online || !renameTarget || !renameDest.trim()) return;
    const destSens = classifySensitivePathClient(renameDest);
    const needsSensitive = Boolean(renameTarget.sensitive || destSens.sensitive);
    setRenaming(true);
    setDirError("");
    setDirNotice("");
    try {
      const res = await api<MachineFileRenameResponse>(`/machines/${enc(machine.id)}/files/entry`, {
        method: "PATCH",
        json: {
          path: renameTarget.path,
          dest_path: renameDest.trim(),
          confirm_sensitive: needsSensitive ? renameSensitiveAck : false,
        },
      });
      const oldPath = renameTarget.path;
      const newPath = res.entry.path || res.path;
      const oldTrimmed = oldPath.replace(/\/+$/, "");
      const oldPrefix = `${oldTrimmed}/`;
      const renamedDir = renameTarget.kind === "dir" || res.entry.kind === "dir" || dir?.path === oldPath;
      if (renamedDir) {
        if (selectedEntry && (selectedEntry.path === oldPath || selectedEntry.path.startsWith(oldPrefix))) {
          setSelectedEntry(null);
          setPreview(null);
          setTail(null);
          resetEditor();
        }
      } else if (
        selectedEntry?.path === oldPath ||
        preview?.path === oldPath ||
        tail?.path === oldPath
      ) {
        setSelectedEntry(res.entry);
        setPreview(null);
        setTail(null);
        void loadFile(machine.id, newPath, mode, tailLines);
      }
      setRenameTarget(null);
      setRenameDest("");
      setRenameSensitiveAck(false);
      if (dir?.path) {
        const curTrimmed = dir.path.replace(/\/+$/, "");
        if (renamedDir && newPath && (curTrimmed === oldTrimmed || dir.path.startsWith(oldPrefix))) {
          const nextDir = curTrimmed === oldTrimmed
            ? newPath
            : `${newPath.replace(/\/+$/, "")}${dir.path.slice(oldTrimmed.length)}`;
          browse(machine.id, nextDir);
        } else {
          void loadDirectory(machine.id, dir.path);
        }
      }
    } catch (err) {
      setDirError(t("machines-files.action_error", { error: errorText(err) }));
    } finally {
      setRenaming(false);
    }
  }, [machine?.id, machine?.online, renameTarget, renameDest, renameSensitiveAck, selectedEntry, preview?.path, tail?.path, mode, tailLines, dir?.path, browse, loadDirectory, loadFile, resetEditor]);

  const submitDelete = useCallback(async () => {
    if (!machine?.online || !deleteTarget) return;
    setDeleting(true);
    setDirError("");
    setDirNotice("");
    try {
      await api<MachineFileDeleteResponse>(`/machines/${enc(machine.id)}/files/entry`, {
        method: "DELETE",
        json: {
          path: deleteTarget.path,
          confirm_sensitive: deleteTarget.sensitive ? deleteSensitiveAck : false,
        },
      });
      if (selectedEntry?.path === deleteTarget.path) {
        setSelectedEntry(null);
        setPreview(null);
        setTail(null);
        resetEditor();
      }
      setDeleteTarget(null);
      setDeleteSensitiveAck(false);
      if (dir?.path) void loadDirectory(machine.id, dir.path);
    } catch (err) {
      setDirError(t("machines-files.action_error", { error: errorText(err) }));
    } finally {
      setDeleting(false);
    }
  }, [machine?.id, machine?.online, deleteTarget, deleteSensitiveAck, selectedEntry?.path, dir?.path, loadDirectory, resetEditor]);

  const startStreamingUpload = useCallback(
    (file: File, opts: { dir?: string; overwrite?: boolean; confirmSensitive?: boolean } = {}) => {
      const targetMid = machine?.id ?? "";
      const targetDir = opts.dir ?? dir?.path ?? "";
      if (!machine?.online || !targetMid || !targetDir) return;
      if (file.size > MAX_UPLOAD_BYTES) {
        setDirError(t("machines-files.upload_too_large", { max: formatSize(MAX_UPLOAD_BYTES) }));
        return;
      }
      uploadXhrRef.current?.abort();
      const xhr = new XMLHttpRequest();
      uploadXhrRef.current = xhr;
      setDirError("");
      setDirNotice("");
      setPendingUpload(null);
      setUploadSensitiveAck(false);
      setUploadProgress({ name: file.name, loaded: 0, total: file.size });

      const url = machineFileUploadURL(targetMid, {
        dir: targetDir,
        name: file.name,
        overwrite: opts.overwrite,
        confirmSensitive: opts.confirmSensitive,
      });
      xhr.open("POST", url);
      xhr.setRequestHeader("Authorization", `Bearer ${getToken()}`);
      xhr.setRequestHeader("Accept-Language", currentLanguage());
      xhr.setRequestHeader("Content-Type", "application/octet-stream");

      xhr.upload.onprogress = (ev) => {
        const total = ev.lengthComputable ? ev.total : file.size;
        setUploadProgress({ name: file.name, loaded: ev.loaded, total });
      };

      xhr.onabort = () => {
        if (uploadXhrRef.current === xhr) uploadXhrRef.current = null;
        setUploadProgress(null);
        setDirNotice(t("machines-files.upload_cancelled"));
      };

      xhr.onerror = () => {
        if (uploadXhrRef.current === xhr) uploadXhrRef.current = null;
        setUploadProgress(null);
        setDirError(t("machines-files.action_error", { error: t("lib-api.cant_reach_hub") }));
      };

      xhr.onload = () => {
        if (uploadXhrRef.current === xhr) uploadXhrRef.current = null;
        setUploadProgress(null);
        let body: Record<string, unknown> = {};
        try {
          body = xhr.responseText ? JSON.parse(xhr.responseText) : {};
        } catch {
          body = {};
        }
        const stillInTargetDir =
          currentDirRef.current.mid === targetMid && currentDirRef.current.path === targetDir;
        if (xhr.status >= 200 && xhr.status < 300) {
          const done = body as unknown as MachineFileUploadEndResponse;
          setDirNotice(t("machines-files.upload_complete", { name: file.name, size: formatSize(done.size ?? file.size) }));
          if (stillInTargetDir) {
            void loadDirectory(targetMid, targetDir);
          }
          return;
        }
        const code = typeof body.code === "string" ? body.code : "";
        if (xhr.status === 409 && code === "exists") {
          if (stillInTargetDir) {
            setPendingUpload({ mid: targetMid, dir: targetDir, file, kind: "exists", confirmedSensitive: opts.confirmSensitive });
          } else {
            const msg = typeof body.error === "string" && body.error ? body.error : `HTTP ${xhr.status}`;
            setDirError(t("machines-files.action_error", { error: msg }));
          }
          return;
        }
        if (xhr.status === 412 && code === "machine_file_sensitive_confirm") {
          if (stillInTargetDir) {
            const reason = typeof body.sensitive_reason === "string" ? body.sensitive_reason : "";
            setPendingUpload({
              mid: targetMid,
              dir: targetDir,
              file,
              kind: "sensitive",
              reason,
              overwrite: Boolean(opts.overwrite),
            });
          } else {
            const msg = typeof body.error === "string" && body.error ? body.error : `HTTP ${xhr.status}`;
            setDirError(t("machines-files.action_error", { error: msg }));
          }
          return;
        }
        if (xhr.status === 413 || code === "too_large") {
          setDirError(t("machines-files.upload_too_large", { max: formatSize(MAX_UPLOAD_BYTES) }));
          return;
        }
        const msg = typeof body.error === "string" && body.error ? body.error : `HTTP ${xhr.status}`;
        setDirError(t("machines-files.action_error", { error: msg }));
      };

      xhr.send(file);
    },
    [machine?.id, machine?.online, dir?.path, loadDirectory],
  );

  const handleUploadPick = useCallback((file: File | undefined) => {
    if (!file || !machine?.online || !dir?.path) return;
    if (file.size > MAX_UPLOAD_BYTES) {
      setDirError(t("machines-files.upload_too_large", { max: formatSize(MAX_UPLOAD_BYTES) }));
      return;
    }
    const sens = classifyTargetPath(dir, file.name);
    if (sens.sensitive) {
      setUploadSensitiveAck(false);
      setPendingUpload({ mid: machine.id, dir: dir.path, file, kind: "sensitive", reason: sens.reason });
      return;
    }
    startStreamingUpload(file, { dir: dir.path });
  }, [machine?.id, machine?.online, dir, startStreamingUpload]);

  const cancelUpload = useCallback(() => {
    uploadXhrRef.current?.abort();
  }, []);

  const visibleEntries = useMemo(() => {
    const list = dir?.entries ?? [];
    const q = filter.trim().toLowerCase();
    if (!q) return list;
    return list.filter((e) => e.name.toLowerCase().includes(q));
  }, [dir?.entries, filter]);

  const editMaxBytes = preview?.edit_max_bytes ?? DEFAULT_EDIT_MAX_BYTES;
  const draftBytes = useMemo(() => (editing ? utf8ByteLength(saveText) : 0), [editing, saveText]);
  const draftOversized = editing && draftBytes > editMaxBytes;
  const diffResult = useMemo(
    () => (reviewingDiff && preview ? computeLineDiff(preview.text ?? "", saveText) : null),
    [reviewingDiff, preview, saveText],
  );

  const createSensitive = useMemo(() => {
    if (createServerSensitive) return { sensitive: true, reason: createServerSensitive.reason };
    return classifyTargetPath(dir, createName);
  }, [createServerSensitive, dir, createName]);
  const renameSensitiveInfo = useMemo(() => {
    if (!renameTarget) return { sensitive: false, reason: "" };
    if (renameTarget.sensitive) return { sensitive: true, reason: renameTarget.sensitive_reason ?? "" };
    return classifySensitivePathClient(renameDest);
  }, [renameTarget, renameDest]);

  if (!machine) {
    return (
      <section className="rounded-card bg-surface p-6 text-center text-sm text-ink-3 shadow-card" data-testid="machine-file-browser">
        {t("machines-files.no_machines")}
      </section>
    );
  }

  if (legacy && machine.online) return <FilesPanel mid={machine.id} sid="" cwd={path || machine.home || "~"}
    onBrowse={routed ? next => browse(machine.id, next) : undefined}
    onClose={() => navigate({ name: "machine", mid: machine.id })} />;

  const activeFile = mode === "tail" ? tail : preview;
  const activeSensitive = Boolean(activeFile?.sensitive ?? selectedEntry?.sensitive);
  const activeSensitiveReason = activeFile?.sensitive_reason ?? selectedEntry?.sensitive_reason;
  const activeBinary = Boolean(activeFile?.binary);
  const downloadPath = activeFile?.path ?? selectedEntry?.path ?? "";
  const downloadName = activeFile?.name ?? selectedEntry?.name ?? "";
  const canEdit = Boolean(
    mode === "preview" &&
    preview &&
    preview.editable &&
    !preview.binary &&
    !preview.truncated &&
    !previewMixedEol &&
    preview.mtime &&
    preview.sha256,
  );
  const uploadPct = uploadProgress && uploadProgress.total > 0
    ? Math.min(100, Math.round((uploadProgress.loaded / uploadProgress.total) * 100))
    : 0;

  return (
    <section className="flex min-w-0 flex-col gap-3" data-testid="machine-file-browser"
      onDragOver={e => { if (machine.online && writable && ops.canUpload && dragHasFiles(e)) e.preventDefault(); }}
      onDrop={e => {
        if (!machine.online || !writable || !dir || !ops.canUpload || !dragHasFiles(e)) return;
        e.preventDefault();
        ops.upload(Array.from(e.dataTransfer.files), dir.path);
      }}>
      {machine.online && <>
        {ops.ui}
        {menu && <Menu anchor={menu.anchor} align="right" onClose={() => setMenu(null)} items={[
          ...ops.rowItems(target(menu.entry), () => openEntry(menu.entry))
            .filter(item => writable ? !["open", "rename", "move", "upload"].includes(item.key) : ["copy", "download"].includes(item.key)),
          { key: "info", icon: "info", label: t("files-info.info"), onSelect: () => setInfo(menu.entry) },
          ...(!isNavigableDir(menu.entry) ? [{ key: "preview", icon: "read" as const, label: t("files-ops.open"),
            onSelect: () => openPanel({ kind: "file", mid: machine.id, cwd: folder, path: menu.entry.path }) }] : []),
          ...(canChmod ? [{ key: "permissions", icon: "shield" as const, label: t("files-perm.menu"), onSelect: () => setPermissions(menu.entry.path) }] : []),
        ]} />}
        {info && <FileInfoDialog mid={machine.id} cwd={folder} path={info.path}
          entry={{ name: info.name, dir: isNavigableDir(info), size: info.size }} onClose={() => setInfo(null)} />}
        {permissions && <PermissionsDialog mid={machine.id} cwd={folder} path={permissions} onClose={() => setPermissions(null)} onChanged={reload} />}
      </>}
      {/* Directory shortcuts and path navigation */}
      <div className="rounded-card bg-surface p-4 shadow-card">
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-2">
            <Icon name="folder" size={16} className="text-ink-2" />
            <h2 className="text-sm font-semibold text-ink">{t("machines-files.title")}</h2>
          </div>
          <div className="flex-1" />
        </div>

        {!machine?.online ? (
          <p className="mt-3 rounded-control bg-field px-3 py-2 text-xs text-ink-2">
            {t("machines-files.machine_offline")}
          </p>
        ) : (
          <>
            {roots.length > 0 && (
              <div className="mt-3 flex flex-wrap items-center gap-1.5" aria-label={t("machines-files.roots_label")}>
                <span className="mr-1 text-xs text-ink-3">{t("machines-files.roots_label")}</span>
                {roots.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    onClick={() => browse(machine.id, r.path)}
                    title={r.path}
                    className={cx(
                      "inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs transition-colors",
                      dir?.path === r.path
                        ? "bg-ink text-surface font-medium"
                        : "bg-inset text-ink-2 shadow-hairline hover:bg-hover hover:text-ink",
                    )}
                  >
                    <Icon name={r.kind === "home" ? "home" : r.kind === "workspace" ? "layers" : "gear"} size={12} />
                    <span>{r.label}</span>
                    <span className="opacity-70">{rootKindLabel(r.kind)}</span>
                  </button>
                ))}
              </div>
            )}

            <form
              className="mt-3 flex flex-wrap items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (pathInput.trim()) browse(machine.id, pathInput.trim());
              }}
            >
              <Button
                tone="secondary"
                icon="up"
                disabled={!dir?.parent || loadingDir}
                onClick={() => dir?.parent && browse(machine.id, dir.parent)}
              >
                {t("machines-files.up")}
              </Button>
              <input
                type="text"
                value={pathInput}
                onChange={(e) => setPathInput(e.target.value)}
                placeholder={t("machines-files.path_placeholder")}
                aria-label={t("machines-files.path_label")}
                className="h-8 min-w-[200px] flex-1 rounded-control bg-inset px-2.5 font-mono text-xs text-ink shadow-hairline outline-none placeholder:text-ink-3"
              />
              <Button type="submit" tone="primary" disabled={loadingDir || !pathInput.trim()}>
                {t("machines-files.go")}
              </Button>
              <IconButton
                icon="retry"
                label={t("machines-files.refresh")}
                disabled={loadingDir}
                onClick={() => void loadDirectory(machine.id, dir?.path || pathInput || "~")}
              />
            </form>

            {/* Directory write actions toolbar */}
            {dir && (
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <Button
                  tone="secondary"
                  icon="file"
                  onClick={() => {
                    setCreateKind("file");
                    setCreateName("");
                    setCreateText("");
                    setCreateSensitiveAck(false);
                    setCreateServerSensitive(null);
                    setRenameTarget(null);
                    setDeleteTarget(null);
                  }}
                >
                  {t("machines-files.new_file")}
                </Button>
                <Button
                  tone="secondary"
                  icon="folder"
                  onClick={() => {
                    setCreateKind("dir");
                    setCreateName("");
                    setCreateText("");
                    setCreateSensitiveAck(false);
                    setCreateServerSensitive(null);
                    setRenameTarget(null);
                    setDeleteTarget(null);
                  }}
                >
                  {t("machines-files.new_folder")}
                </Button>
                <Button
                  tone="secondary"
                  icon="upload"
                  disabled={Boolean(uploadProgress)}
                  onClick={() => fileInputRef.current?.click()}
                >
                  {t("machines-files.upload")}
                </Button>
                <input
                  ref={fileInputRef}
                  type="file"
                  className="hidden"
                  data-testid="machine-file-upload-input"
                  onChange={(e) => {
                    const picked = e.target.files?.[0];
                    e.target.value = "";
                    handleUploadPick(picked);
                  }}
                />
              </div>
            )}

            {/* Inline Create File / Folder form */}
            {createKind && dir && (
              <div className="mt-3 flex flex-col gap-2 rounded-control bg-field p-3 text-xs" data-testid="create-entry-panel">
                <div className="font-semibold text-ink">
                  {createKind === "file" ? t("machines-files.create_file_title") : t("machines-files.create_folder_title")}
                </div>
                <input
                  type="text"
                  value={createName}
                  onChange={(e) => {
                    setCreateName(e.target.value);
                    setCreateSensitiveAck(false);
                    setCreateServerSensitive(null);
                  }}
                  placeholder={t("machines-files.create_name_placeholder")}
                  aria-label={t("machines-files.create_name_placeholder")}
                  data-testid="create-name-input"
                  className="h-8 rounded-control bg-inset px-2.5 font-mono text-xs text-ink shadow-hairline outline-none placeholder:text-ink-3"
                />
                {createKind === "file" && (
                  <textarea
                    value={createText}
                    onChange={(e) => setCreateText(e.target.value)}
                    placeholder={t("machines-files.create_text_placeholder")}
                    data-testid="create-text-input"
                    className="rounded-control bg-inset p-3 font-mono text-xs text-ink shadow-hairline outline-none placeholder:text-ink-3"
                    style={{ minHeight: "5rem" }}
                  />
                )}
                {createSensitive.sensitive && (
                  <label className="flex items-start gap-2 rounded-control bg-orange-tint px-3 py-2 text-xs text-ink">
                    <input
                      type="checkbox"
                      checked={createSensitiveAck}
                      onChange={(e) => setCreateSensitiveAck(e.target.checked)}
                      data-testid="create-sensitive-checkbox"
                    />
                    <span>{t("machines-files.sensitive_confirm_checkbox", { reason: sensitiveReasonLabel(createSensitive.reason) })}</span>
                  </label>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    tone="primary"
                    disabled={creating || !createName.trim() || (createSensitive.sensitive && !createSensitiveAck)}
                    onClick={() => void submitCreate()}
                  >
                    {t("machines-files.create_submit")}
                  </Button>
                  <Button tone="secondary" disabled={creating} onClick={() => { setCreateKind(null); setCreateServerSensitive(null); }}>
                    {t("machines-files.cancel")}
                  </Button>
                </div>
              </div>
            )}

            {/* Streaming upload progress bar */}
            {uploadProgress && (
              <div className="mt-3 flex flex-col gap-2 rounded-control bg-field p-3 text-xs" data-testid="upload-progress-panel">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-mono text-ink">
                    {t("machines-files.upload_uploading", {
                      name: uploadProgress.name,
                      loaded: formatSize(uploadProgress.loaded),
                      total: formatSize(uploadProgress.total),
                      percent: uploadPct,
                    })}
                  </span>
                  <Button tone="secondary" onClick={cancelUpload}>
                    {t("machines-files.upload_cancel")}
                  </Button>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-inset">
                  <div className="h-2 bg-ink" style={{ width: `${uploadPct}%`, transition: "width 120ms linear" }} />
                </div>
              </div>
            )}

            {/* Pending upload confirmation (sensitive filename or overwrite conflict) */}
            {pendingUpload && (
              <div className="mt-3 flex flex-col gap-2 rounded-control bg-orange-tint p-3 text-xs text-ink" data-testid="upload-confirm-panel">
                {pendingUpload.kind === "sensitive" ? (
                  <>
                    <div className="flex items-center gap-2 font-medium">
                      <Icon name="shield" size={14} className="shrink-0 text-orange" />
                      <span>
                        {t("machines-files.upload_sensitive_prompt", {
                          name: pendingUpload.file.name,
                          reason: sensitiveReasonLabel(pendingUpload.reason),
                        })}
                      </span>
                    </div>
                    <label className="flex items-start gap-2">
                      <input
                        type="checkbox"
                        checked={uploadSensitiveAck}
                        onChange={(e) => setUploadSensitiveAck(e.target.checked)}
                        data-testid="upload-sensitive-checkbox"
                      />
                      <span>{t("machines-files.sensitive_confirm_checkbox", { reason: sensitiveReasonLabel(pendingUpload.reason) })}</span>
                    </label>
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        tone="primary"
                        disabled={!uploadSensitiveAck}
                        onClick={() =>
                          startStreamingUpload(pendingUpload.file, {
                            dir: pendingUpload.dir,
                            overwrite: pendingUpload.overwrite,
                            confirmSensitive: true,
                          })}
                      >
                        {t("machines-files.upload_confirm_start")}
                      </Button>
                      <Button tone="secondary" onClick={() => setPendingUpload(null)}>
                        {t("machines-files.cancel")}
                      </Button>
                    </div>
                  </>
                ) : (
                  <>
                    <div className="font-medium">
                      {t("machines-files.upload_exists_prompt", { name: pendingUpload.file.name })}
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        tone="primary"
                        onClick={() =>
                          startStreamingUpload(pendingUpload.file, {
                            dir: pendingUpload.dir,
                            overwrite: true,
                            confirmSensitive: pendingUpload.confirmedSensitive,
                          })}
                      >
                        {t("machines-files.upload_overwrite")}
                      </Button>
                      <Button tone="secondary" onClick={() => setPendingUpload(null)}>
                        {t("machines-files.cancel")}
                      </Button>
                    </div>
                  </>
                )}
              </div>
            )}

            {/* Inline Rename / Move panel */}
            {renameTarget && (
              <div className="mt-3 flex flex-col gap-2 rounded-control bg-field p-3 text-xs" data-testid="rename-entry-panel">
                <div className="font-semibold text-ink">
                  {t("machines-files.rename_title", { name: renameTarget.name })}
                </div>
                <p className="text-ink-3">{t("machines-files.rename_hint")}</p>
                <input
                  type="text"
                  value={renameDest}
                  onChange={(e) => {
                    setRenameDest(e.target.value);
                    setRenameSensitiveAck(false);
                  }}
                  placeholder={t("machines-files.rename_dest_placeholder")}
                  aria-label={t("machines-files.rename_dest_placeholder")}
                  data-testid="rename-dest-input"
                  className="h-8 rounded-control bg-inset px-2.5 font-mono text-xs text-ink shadow-hairline outline-none placeholder:text-ink-3"
                />
                {renameSensitiveInfo.sensitive && (
                  <label className="flex items-start gap-2 rounded-control bg-orange-tint px-3 py-2 text-xs text-ink">
                    <input
                      type="checkbox"
                      checked={renameSensitiveAck}
                      onChange={(e) => setRenameSensitiveAck(e.target.checked)}
                      data-testid="rename-sensitive-checkbox"
                    />
                    <span>{t("machines-files.sensitive_confirm_checkbox", { reason: sensitiveReasonLabel(renameSensitiveInfo.reason) })}</span>
                  </label>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    tone="primary"
                    disabled={renaming || !renameDest.trim() || (renameSensitiveInfo.sensitive && !renameSensitiveAck)}
                    onClick={() => void submitRename()}
                  >
                    {t("machines-files.rename_submit")}
                  </Button>
                  <Button tone="secondary" disabled={renaming} onClick={() => setRenameTarget(null)}>
                    {t("machines-files.cancel")}
                  </Button>
                </div>
              </div>
            )}

            {/* Explicit Delete confirmation panel */}
            {deleteTarget && (
              <div className="mt-3 flex flex-col gap-2 rounded-control bg-red-tint p-3 text-xs text-ink" data-testid="delete-entry-panel">
                <div className="flex items-center gap-2 font-semibold text-red">
                  <Icon name="warning" size={14} className="shrink-0" />
                  <span>{t("machines-files.delete_title", { name: deleteTarget.name })}</span>
                </div>
                <p className="font-mono text-ink-2">{t("machines-files.delete_body", { path: deleteTarget.path })}</p>
                {deleteTarget.kind === "dir" && <p className="text-ink-2">{t("machines-files.delete_dir_note")}</p>}
                {deleteTarget.symlink && <p className="text-ink-2">{t("machines-files.delete_symlink_note")}</p>}
                {deleteTarget.sensitive && (
                  <>
                    <div className="flex items-center gap-2 rounded-control bg-orange-tint px-3 py-2 font-medium text-ink" data-testid="delete-sensitive-warning">
                      <Icon name="shield" size={14} className="shrink-0 text-orange" />
                      <span>{t("machines-files.delete_sensitive_warning", { reason: sensitiveReasonLabel(deleteTarget.sensitive_reason) })}</span>
                    </div>
                    <label className="flex items-start gap-2">
                      <input
                        type="checkbox"
                        checked={deleteSensitiveAck}
                        onChange={(e) => setDeleteSensitiveAck(e.target.checked)}
                        data-testid="delete-sensitive-checkbox"
                      />
                      <span>{t("machines-files.sensitive_confirm_checkbox", { reason: sensitiveReasonLabel(deleteTarget.sensitive_reason) })}</span>
                    </label>
                  </>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    tone="primary"
                    disabled={deleting || (Boolean(deleteTarget.sensitive) && !deleteSensitiveAck)}
                    onClick={() => void submitDelete()}
                  >
                    {t("machines-files.delete_confirm")}
                  </Button>
                  <Button tone="secondary" disabled={deleting} onClick={() => setDeleteTarget(null)}>
                    {t("machines-files.cancel")}
                  </Button>
                </div>
              </div>
            )}

            {dirNotice && (
              <p className="mt-3 rounded-control bg-green-tint px-3 py-2 text-xs text-green" data-testid="dir-notice">
                {dirNotice}
              </p>
            )}
          </>
        )}
      </div>

      {machine?.online && (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {/* Single-level directory listing */}
          <div className="flex flex-col overflow-hidden rounded-card bg-surface shadow-card">
            <div className="flex items-center gap-2 border-b border-line px-3 py-2">
              <Icon name="search" size={13} className="shrink-0 text-ink-3" />
              <input
                type="search"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder={t("machines-files.filter_placeholder")}
                aria-label={t("machines-files.filter_label")}
                className="min-w-0 flex-1 bg-transparent text-xs text-ink outline-none placeholder:text-ink-3"
              />
              {dir && (
                <span className="shrink-0 text-xs text-ink-3 tabular-nums">
                  {t("machines-files.entries_count", { count: visibleEntries.length })}
                </span>
              )}
            </div>

            {dir && writable && routed && <div className="border-b border-line">
              {selected ? <SelectionBar ops={ops} selected={selected} onClear={() => select(null)} /> :
                <Button tone="quiet" onClick={() => select([])}>{t("files-ops.select")}</Button>}
            </div>}
            {dir?.truncated && (
              <p className="border-b border-line bg-orange-tint px-3 py-1.5 text-xs text-ink">
                {t("machines-files.entries_truncated", { shown: dir.entries.length, total: dir.total })}
              </p>
            )}

            {dirError && (
              <p role="alert" className="m-3 rounded-control bg-red-tint px-3 py-2 text-xs text-red" data-testid="dir-error">
                {dirError}
              </p>
            )}

            {loadingDir && !dir ? (
              <div className="flex h-48 items-center justify-center">
                <Loading />
              </div>
            ) : !dir ? null : visibleEntries.length === 0 ? (
              <p className="px-4 py-8 text-center text-xs text-ink-3">
                {filter.trim() ? t("machines-files.no_filter_matches", { query: filter.trim() }) : t("machines-files.empty_dir")}
              </p>
            ) : (
              <div className="divide-y divide-line overflow-y-auto" style={{ maxHeight: "34rem" }} role="table" aria-label={t("machines-files.title")}>
                <div className="hidden grid-cols-[minmax(0,1fr)_auto_auto_auto] gap-2 border-b border-line bg-field/60 px-3 py-1.5 text-xs font-medium text-ink-3 sm:grid" role="row">
                  <span role="columnheader">{t("machines-files.col_name")}</span>
                  <span role="columnheader">{t("machines-files.col_size")}</span>
                  <span role="columnheader">{t("machines-files.col_mode")}</span>
                  <span role="columnheader">{t("machines-files.col_mtime")}</span>
                </div>
                {visibleEntries.map((entry) => {
                  const previewing = selectedEntry?.path === entry.path;
                  const dirLike = isNavigableDir(entry);
                  return (
                    <div
                      key={entry.path}
                      className={cx(
                        "flex items-center gap-1 pr-2 transition-colors hover:bg-hover",
                        previewing && "bg-accent-tint/60",
                      )}
                    >
                      {selected && <input type="checkbox" aria-label={entry.name} checked={selected.some(x => x.path === entry.path)}
                        onChange={e => select(e.target.checked ? [...selected, target(entry)] : selected.filter(x => x.path !== entry.path))} />}
                      <button
                        type="button"
                        onClick={() => openEntry(entry)}
                        className="flex min-w-0 flex-1 items-center gap-2 px-3 py-2 text-left text-xs"
                        data-testid={`file-entry-${entry.name}`}
                      >
                        <Icon
                          name={entry.symlink ? "link" : dirLike ? "folder" : "file"}
                          size={14}
                          className={cx("shrink-0", dirLike ? "text-accent-ink" : "text-ink-3")}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="flex flex-wrap items-center gap-1.5">
                            <span className="truncate font-medium text-ink" title={`${entryKindLabel(entry)}: ${entry.name}`}>
                              {entry.name}
                            </span>
                            {entry.symlink && (
                              <span
                                className={cx(
                                  "inline-flex items-center rounded px-1 py-0.5 font-mono text-xs",
                                  entry.link_broken ? "bg-red-tint text-red" : "bg-field text-ink-3",
                                )}
                                title={entry.link_target ? t("machines-files.symlink_target", { target: entry.link_target }) : undefined}
                              >
                                {entry.link_broken ? t("machines-files.symlink_broken") : `→ ${entry.link_target ?? ""}`}
                              </span>
                            )}
                            {entry.sensitive && (
                              <span
                                className="inline-flex items-center gap-1 rounded bg-orange-tint px-1.5 py-0.5 text-xs font-medium text-orange"
                                title={sensitiveReasonLabel(entry.sensitive_reason)}
                              >
                                <Icon name="shield" size={11} />
                                {t("machines-files.badge_sensitive")}
                              </span>
                            )}
                            {entry.binary && (
                              <span className="inline-flex items-center rounded bg-field px-1.5 py-0.5 text-xs text-ink-3">
                                {t("machines-files.badge_binary")}
                              </span>
                            )}
                          </span>
                          <span className="mt-0.5 flex flex-wrap items-center gap-2 font-mono text-xs text-ink-3">
                            <span>{dirLike ? "—" : formatSize(entry.size)}</span>
                            <span>{entry.mode}</span>
                            {entry.mtime && <span>{formatMtime(entry.mtime)}</span>}
                          </span>
                        </span>
                      </button>
                      {routed && <IconButton icon="more" size={44} label={t("files-ops.row_more", { name: entry.name })}
                        onClick={e => setMenu({ entry, anchor: e.currentTarget })} />}
                      <IconButton
                        icon="pencil"
                        label={`${t("machines-files.rename_move")}: ${entry.name}`}
                        onClick={() => {
                          setRenameTarget(entry);
                          setRenameDest(entry.path);
                          setRenameSensitiveAck(false);
                          setCreateKind(null);
                          setDeleteTarget(null);
                        }}
                      />
                      <IconButton
                        icon="trash"
                        label={`${t("machines-files.delete")}: ${entry.name}`}
                        onClick={() => {
                          setDeleteTarget(entry);
                          setDeleteSensitiveAck(false);
                          setCreateKind(null);
                          setRenameTarget(null);
                        }}
                      />
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {/* Bounded preview, lightweight UTF-8 editor + diff confirm, log tail & streaming download pane */}
          <div className="flex flex-col overflow-hidden rounded-card bg-surface shadow-card">
            {!selectedEntry ? (
              <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center text-xs text-ink-3" style={{ minHeight: "16rem" }}>
                <Icon name="read" size={20} className="text-ink-3" />
                <p>{t("machines-files.select_file")}</p>
              </div>
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2.5">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <h3 className="truncate font-mono text-sm font-semibold text-ink">{selectedEntry.name}</h3>
                      <span className="font-mono text-xs text-ink-3">{formatSize(activeFile?.size ?? selectedEntry.size)}</span>
                      {preview?.mode && <span className="font-mono text-xs text-ink-3">{preview.mode}</span>}
                    </div>
                    <p className="truncate font-mono text-xs text-ink-3" title={activeFile?.canonical_path || selectedEntry.path}>
                      {activeFile?.canonical_path || selectedEntry.path}
                    </p>
                  </div>

                  <div className="flex flex-wrap items-center gap-1.5">
                    <div className="flex items-center rounded-control bg-inset p-0.5 shadow-hairline" role="tablist">
                      <button
                        type="button"
                        role="tab"
                        aria-selected={mode === "preview"}
                        onClick={() => switchMode("preview")}
                        className={cx(
                          "rounded px-2 py-1 text-xs transition-colors",
                          mode === "preview" ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink",
                        )}
                      >
                        {t("machines-files.mode_preview")}
                      </button>
                      <button
                        type="button"
                        role="tab"
                        aria-selected={mode === "tail"}
                        onClick={() => switchMode("tail")}
                        className={cx(
                          "rounded px-2 py-1 text-xs transition-colors",
                          mode === "tail" ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink",
                        )}
                      >
                        {t("machines-files.mode_tail")}
                      </button>
                    </div>

                    {mode === "preview" && !editing && preview && (
                      <Button
                        tone="secondary"
                        icon="pencil"
                        disabled={!canEdit || loadingFile}
                        title={
                          canEdit
                            ? undefined
                            : previewMixedEol
                              ? t("machines-files.edit_mixed_eol_hint")
                              : t("machines-files.edit_disabled_hint", { max: formatSize(editMaxBytes) })
                        }
                        onClick={startEdit}
                      >
                        {t("machines-files.edit")}
                      </Button>
                    )}

                    {mode === "tail" && (
                      <>
                        <select
                          aria-label={t("machines-files.tail_lines_label")}
                          value={tailLines}
                          onChange={(e) => changeTailLines(Number(e.target.value))}
                          className="h-7 rounded-control bg-inset px-2 text-xs text-ink shadow-hairline outline-none"
                        >
                          {TAIL_LINE_OPTIONS.map((n) => (
                            <option key={n} value={n}>
                              {t("machines-files.tail_lines_option", { count: n })}
                            </option>
                          ))}
                        </select>
                        <Button
                          tone="secondary"
                          icon="retry"
                          disabled={loadingFile}
                          onClick={() => void loadFile(machine.id, selectedEntry.path, "tail", tailLines)}
                        >
                          {t("machines-files.tail_refresh")}
                        </Button>
                      </>
                    )}

                    {!activeBinary && !editing && (
                      <Button tone="quiet" onClick={() => setWrap((w) => !w)}>
                        {wrap ? t("machines-files.wrap_on") : t("machines-files.wrap_off")}
                      </Button>
                    )}

                    {downloadPath && (
                      <a
                        href={machineFileDownloadURL(machine.id, downloadPath)}
                        download={downloadName}
                        className="inline-flex h-8 items-center gap-1.5 rounded-control bg-inset px-2.5 text-xs font-medium text-ink shadow-hairline transition-colors hover:bg-hover"
                        data-testid="machine-file-download"
                      >
                        <Icon name="download" size={13} />
                        {t("machines-files.download")}
                      </a>
                    )}
                  </div>
                </div>

                {activeSensitive && (
                  <div className="flex items-center gap-2 border-b border-line bg-orange-tint px-4 py-2 text-xs text-ink" data-testid="sensitive-banner">
                    <Icon name="shield" size={14} className="shrink-0 text-orange" />
                    <span>{t("machines-files.sensitive_banner", { reason: sensitiveReasonLabel(activeSensitiveReason) })}</span>
                  </div>
                )}

                {savedNotice && (
                  <div className="border-b border-line bg-green-tint px-4 py-1.5 text-xs text-green" data-testid="saved-notice">
                    {savedNotice}
                  </div>
                )}

                {casConflict && (
                  <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line bg-red-tint px-4 py-2 text-xs text-ink" role="alert" data-testid="cas-conflict-banner">
                    <div>
                      <div className="font-semibold text-red">{t("machines-files.cas_conflict_title")}</div>
                      <p className="text-ink-2">{t("machines-files.cas_conflict_body")}</p>
                    </div>
                    <Button
                      tone="secondary"
                      icon="retry"
                      onClick={() => void loadFile(machine.id, selectedEntry.path, "preview", tailLines)}
                    >
                      {t("machines-files.reload_latest")}
                    </Button>
                  </div>
                )}

                {mode === "preview" && preview?.editable && !preview.binary && !preview.truncated && previewMixedEol && (
                  <div className="border-b border-line bg-field px-4 py-2 text-xs text-ink-2" data-testid="mixed-eol-notice">
                    {t("machines-files.edit_mixed_eol_hint")}
                  </div>
                )}

                {mode === "preview" && preview?.truncated && !preview.binary && (
                  <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line bg-field px-4 py-2 text-xs text-ink-2">
                    <span>
                      {t("machines-files.preview_truncated", {
                        max: formatSize(preview.max_bytes),
                        shown: formatSize(preview.bytes_read),
                        total: formatSize(preview.size),
                      })}
                    </span>
                    <Button tone="secondary" onClick={() => switchMode("tail")}>
                      {t("machines-files.switch_to_tail")}
                    </Button>
                  </div>
                )}

                {mode === "tail" && tail && !tail.binary && (
                  <div className="border-b border-line bg-field px-4 py-1.5 font-mono text-xs text-ink-3">
                    {t("machines-files.tail_meta", {
                      lines: tail.lines,
                      bytes: formatSize(tail.bytes_read),
                      offset: tail.offset,
                    })}
                  </div>
                )}

                {fileError && (
                  <p role="alert" className="m-4 rounded-control bg-red-tint px-3 py-2 text-xs text-red">
                    {fileError}
                  </p>
                )}

                {loadingFile ? (
                  <div className="flex h-56 items-center justify-center">
                    <Loading />
                  </div>
                ) : activeBinary ? (
                  <div className="flex flex-col items-center justify-center gap-3 p-8 text-center" data-testid="binary-refusal">
                    <Icon name="warning" size={22} className="text-orange" />
                    <h4 className="text-sm font-semibold text-ink">{t("machines-files.binary_title")}</h4>
                    <p className="max-w-md text-xs text-ink-2">
                      {t("machines-files.binary_body", {
                        mime: activeFile?.mime || "application/octet-stream",
                        size: formatSize(activeFile?.size ?? selectedEntry.size),
                      })}
                    </p>
                    {downloadPath && (
                      <a
                        href={machineFileDownloadURL(machine.id, downloadPath)}
                        download={downloadName}
                        className="inline-flex h-8 items-center gap-1.5 rounded-control bg-ink px-3 text-xs font-medium text-surface transition-opacity hover:opacity-90"
                      >
                        <Icon name="download" size={13} />
                        {t("machines-files.download")}
                      </a>
                    )}
                  </div>
                ) : editing && reviewingDiff && diffResult ? (
                  <div className="flex flex-col gap-3 p-4 text-xs" data-testid="machine-file-diff">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-semibold text-ink">{t("machines-files.diff_title")}</span>
                      <span className="font-mono text-ink-3">
                        {t("machines-files.diff_summary", { added: diffResult.added, removed: diffResult.removed })}
                      </span>
                    </div>

                    {!diffResult.changed ? (
                      <p className="rounded-control bg-field px-3 py-2 text-ink-3">{t("machines-files.diff_no_changes")}</p>
                    ) : (
                      <div className="overflow-auto rounded-control bg-inset font-mono text-xs" style={{ maxHeight: "22rem" }}>
                        {diffResult.hunks.map((hunk, hi) => (
                          <div key={hi}>
                            <div className="bg-field px-3 py-1 text-ink-3">
                              {`@@ -${hunk.oldStart},${hunk.oldCount} +${hunk.newStart},${hunk.newCount} @@`}
                            </div>
                            {hunk.lines.map((line, li) => (
                              <div
                                key={li}
                                className={cx(
                                  "whitespace-pre px-3 py-0.5",
                                  line.type === "add" && "bg-green-tint text-green",
                                  line.type === "del" && "bg-red-tint text-red",
                                  line.type === "context" && "text-ink-2",
                                )}
                              >
                                {`${line.type === "add" ? "+" : line.type === "del" ? "-" : " "}${line.text}`}
                              </div>
                            ))}
                          </div>
                        ))}
                      </div>
                    )}

                    {preview?.sensitive && (
                      <div className="flex flex-col gap-2 rounded-control bg-orange-tint p-3 text-xs text-ink" data-testid="sensitive-write-warning">
                        <div className="flex items-center gap-2 font-semibold">
                          <Icon name="shield" size={14} className="shrink-0 text-orange" />
                          <span>{t("machines-files.sensitive_write_warning", { reason: sensitiveReasonLabel(preview.sensitive_reason) })}</span>
                        </div>
                        <label className="flex items-start gap-2">
                          <input
                            type="checkbox"
                            checked={sensitiveSaveAck}
                            onChange={(e) => setSensitiveSaveAck(e.target.checked)}
                            data-testid="sensitive-confirm-checkbox"
                          />
                          <span>{t("machines-files.sensitive_confirm_checkbox", { reason: sensitiveReasonLabel(preview.sensitive_reason) })}</span>
                        </label>
                      </div>
                    )}

                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        tone="primary"
                        disabled={saving || !diffResult.changed || (Boolean(preview?.sensitive) && !sensitiveSaveAck)}
                        onClick={() => void confirmSave()}
                      >
                        {saving ? t("machines-files.saving") : t("machines-files.confirm_save")}
                      </Button>
                      <Button tone="secondary" disabled={saving} onClick={() => setReviewingDiff(false)}>
                        {t("machines-files.back_to_edit")}
                      </Button>
                    </div>
                  </div>
                ) : editing ? (
                  <div className="flex flex-1 flex-col gap-2 p-4">
                    <textarea
                      value={draftText}
                      onChange={(e) => setDraftText(e.target.value)}
                      aria-label={t("machines-files.edit_label")}
                      data-testid="machine-file-editor"
                      className="flex-1 rounded-control bg-inset p-3 font-mono text-xs leading-relaxed text-ink shadow-hairline outline-none"
                      style={{ minHeight: "22rem", maxHeight: "34rem" }}
                    />
                    {draftOversized && (
                      <p role="alert" className="rounded-control bg-red-tint px-3 py-1.5 text-xs text-red">
                        {t("machines-files.edit_oversized", { max: formatSize(editMaxBytes) })}
                      </p>
                    )}
                    <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
                      <span className="font-mono text-ink-3 tabular-nums">
                        {t("machines-files.edit_bytes", { bytes: formatSize(draftBytes), max: formatSize(editMaxBytes) })}
                      </span>
                      <div className="flex flex-wrap items-center gap-2">
                        <Button
                          tone="primary"
                          disabled={draftOversized || saveText === (preview?.text ?? "")}
                          onClick={() => {
                            setSensitiveSaveAck(false);
                            setReviewingDiff(true);
                          }}
                        >
                          {t("machines-files.review_diff")}
                        </Button>
                        <Button tone="secondary" onClick={resetEditor}>
                          {t("machines-files.cancel")}
                        </Button>
                      </div>
                    </div>
                  </div>
                ) : activeFile && !(activeFile.text ?? "") ? (
                  <p className="p-6 text-center text-xs text-ink-3">{t("machines-files.empty_file")}</p>
                ) : activeFile ? (
                  <pre
                    className={cx(
                      "flex-1 overflow-auto bg-inset p-4 font-mono text-xs leading-relaxed text-ink select-text",
                      wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre",
                    )}
                    style={{ maxHeight: "34rem" }}
                    data-testid="machine-file-content"
                  >
                    {activeFile.text}
                  </pre>
                ) : null}
              </>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
