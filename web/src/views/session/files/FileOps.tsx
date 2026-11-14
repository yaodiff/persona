// What the Files panel changes on the machine (#4883, aligned with the apps' #4858): upload into a folder (dropped or
// chosen, with progress and cancel; a name already there asks replace or keep both), download, rename, move and move
// to the trash (asked first, then "Moved to trash · Undo"). Both views share it: the drill-in list (FilesPanel) and the
// tree (FilesTree). Each action shows only when the hub says it can (`upload_dir`, `file_edit`).
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { api, credentialOwner, enc, errorText, fileDownloadURL, getToken, isAbort, onCredentialChange, subscribeCredentialOwner } from "../../../lib/api";
import { onManagerChoice, shownManager, useShownManager } from "../../../lib/manager-choice";
import { t } from "../../../i18n";
import {
  copyEntries,
  FILE_COPY_FEATURE,
  FILE_EDIT_FEATURE,
  FILE_ZIP_FEATURE,
  fileOperation,
  isNameTaken,
  moveEntries,
  nameProblem,
  opErrorText,
  parentOf,
  renameEntry,
  restoreEntries,
  trashEntries,
  UPLOAD_DIR_FEATURE,
  uploadToDir,
  within,
  zipEntries,
  type Conflict,
  type FileCredential,
  type UploadCredential,
} from "../../../lib/file-ops";
import { joinPath, type FsListing } from "../../../lib/files";
import { baseName, bytes, shortPath, uuid } from "../../../lib/format";
import { hubHas, useStore } from "../../../lib/store";
import { contextMenuProps } from "../../../ui/context-menu";
import type { MenuAt, MenuItem } from "../../../ui/menu-model";
import { Button, copyText, cx, Dialog, IconButton, Loading, toast, type ToastAction } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { FileGlyph } from "./parts";

/** A row the actions are about. */
export interface OpTarget {
  path: string;
  name: string;
  dir: boolean;
}

type UploadState = "queued" | "sending" | "conflict" | "failed" | "done";
interface Upload {
  id: number;
  file: File;
  dir: string;
  credential: UploadCredential;
  sent: number;
  state: UploadState;
  conflict?: Conflict;
  error?: string;
  ac?: AbortController;
}

interface RemoteOperation {
  id: number;
  operationId: string;
  kind: "copy" | "zip";
  duplicate: boolean;
  name: string;
  targets: OpTarget[];
  dir: string;
  owner: MutationOwner;
  state: "running" | "conflict" | "done";
  conflictName?: string;
  shown: boolean;
  filesDone: number;
  filesTotal: number;
  bytesDone: number;
  bytesTotal: number;
}

interface ActiveRemoteOperation {
  ac: AbortController;
  operationId: string;
  owner: MutationOwner;
  shown: boolean;
}

// The authoritative POST outlives a panel, but not its credential. Keep only the opaque owner and controller outside
// React: navigation can hide every visible row without making a later sign-out unable to retire the request.
const remotePostCredentials = new Map<AbortController, string>();
onCredentialChange((nextOwner) => {
  remotePostCredentials.forEach((owner, ac) => {
    if (owner === nextOwner) return;
    ac.abort();
    remotePostCredentials.delete(ac);
  });
});

/** A question waiting for the user: replace or keep both, for an upload or a move. */
interface NameAsk {
  name: string;
  dir: string;
  /** More that are waiting on the same question ("Apply to the remaining items"). */
  more: number;
  answer: (c: Conflict | null, all: boolean) => void;
}

interface WorkspaceOwner {
  credential: FileCredential;
  manager: string;
  mid: string;
  cwd: string;
  workspace: string;
}

interface MutationOwner extends WorkspaceOwner {
  workspaceOwner: WorkspaceOwner;
  path: string;
}

interface MutationIntent {
  owner: MutationOwner;
  targets: OpTarget[];
  /** Copy to… instead of Move to… (#5830): the same folder picker and name question. */
  copy?: boolean;
}

export interface FileOps {
  /** Identity of this panel's current mutation context, for menus and selections. */
  scope: object;
  canUpload: boolean;
  canEdit: boolean;
  /** The hub zips (`file_zip`): several items or a folder download as one zip. */
  canZip: boolean;
  /** Sends files into `dir` (dropped or chosen). */
  upload: (files: File[], dir: string) => void;
  /** Opens the file chooser for `dir`. */
  choose: (dir: string) => void;
  /** The row's menu: open, download, rename, move, copy path, move to the trash. */
  rowItems: (target: OpTarget, open: () => void) => MenuItem[];
  move: (targets: OpTarget[]) => void;
  /** Copy to… (#5830). */
  copy: (targets: OpTarget[]) => void;
  /** Copies beside themselves as "name 2.ext" (#5830). */
  duplicate: (targets: OpTarget[]) => void;
  /** Zips them beside the first one (#5830). */
  compress: (targets: OpTarget[]) => void;
  /** The hub copies (`file_copy`). */
  canCopy: boolean;
  trash: (targets: OpTarget[]) => void;
  download: (targets: OpTarget[]) => void;
  /** The tray, the dialogs and the hidden chooser: rendered once by the panel. */
  ui: React.ReactNode;
}

/** What a row of either view does with the actions: a checkbox while selecting, a menu on right click and on its ⋯. */
export interface RowOps {
  selecting: boolean;
  isSelected: (path: string) => boolean;
  toggle: (x: OpTarget) => void;
  menu: (x: OpTarget, at: MenuAt, open: () => void) => void;
}

/** Whether a drag carries files from the computer (not a row of the panel, lib/entry-drag.ts). */
export const dragHasFiles = (e: React.DragEvent) => Array.from(e.dataTransfer.types).includes("Files");

export function useFileOps({ mid, cwd, path = cwd, workspace = cwd, onChanged }: { mid: string; cwd: string; path?: string; workspace?: string; onChanged: (dirs: string[]) => void }): FileOps {
  const features = useStore((s) => s.features);
  const canUpload = hubHas(features, UPLOAD_DIR_FEATURE);
  const canEdit = hubHas(features, FILE_EDIT_FEATURE);
  const changed = useRef(onChanged);
  changed.current = onChanged;

  // ── uploads: one at a time, in the order they came ──
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner);
  const manager = useShownManager();
  const [managerRevision, setManagerRevision] = useState(0);
  const managerSerial = useRef(0);
  const lastManager = useRef(manager);
  const workspaceOwner = useMemo<WorkspaceOwner>(() => ({ credential: { owner, token: getToken() }, manager, mid, cwd, workspace }), [owner, manager, managerRevision, mid, cwd, workspace]);
  const mutationOwner = useMemo<MutationOwner>(() => ({ ...workspaceOwner, workspaceOwner, path }), [workspaceOwner, path]);
  const liveWorkspace = useRef<WorkspaceOwner | null>(workspaceOwner);
  const liveOwner = useRef<MutationOwner | null>(mutationOwner);
  useLayoutEffect(() => {
    liveWorkspace.current = workspaceOwner;
    return () => { if (liveWorkspace.current === workspaceOwner) liveWorkspace.current = null; };
  }, [workspaceOwner]);
  useLayoutEffect(() => {
    liveOwner.current = mutationOwner;
    return () => { if (liveOwner.current === mutationOwner) liveOwner.current = null; };
  }, [mutationOwner]);
  useLayoutEffect(() => onManagerChoice(() => {
    const nextManager = shownManager();
    if (lastManager.current === nextManager) return;
    lastManager.current = nextManager;
    liveWorkspace.current = null;
    liveOwner.current = null;
    setManagerRevision(++managerSerial.current);
  }), []);
  const currentWorkspace = useCallback((candidate: WorkspaceOwner) => liveWorkspace.current === candidate
    && candidate.credential.owner === credentialOwner() && Boolean(candidate.credential.token)
    && candidate.credential.token === getToken() && candidate.manager === shownManager(), []);
  const currentOwner = useCallback((candidate: MutationOwner) => liveOwner.current === candidate && currentWorkspace(candidate.workspaceOwner), [currentWorkspace]);
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [remoteOperations, setRemoteOperations] = useState<RemoteOperation[]>([]);
  const activeRemote = useRef(new Map<number, ActiveRemoteOperation>());
  const ownedUploads = uploads.filter((u) => u.credential.owner === owner);
  const ownedRemoteOperations = remoteOperations.filter((op) => op.owner.workspaceOwner === workspaceOwner && op.owner.path === path);
  const active = useRef(new Map<number, AbortController>());
  const nextId = useRef(0);
  const patch = useCallback((id: number, p: Partial<Upload>) => setUploads((us) => us.map((u) => (u.id === id ? { ...u, ...p } : u))), []);
  const patchRemote = useCallback((id: number, p: Partial<RemoteOperation>) => setRemoteOperations((ops) => ops.map((op) => op.id === id ? { ...op, ...p } : op)), []);
  const enqueue = useCallback((files: File[], dir: string, selectedOwner: string) => {
    const token = getToken();
    if (!files.length || !token || selectedOwner !== credentialOwner()) return;
    const credential = { owner: selectedOwner, token };
    const added = files.map((file) => ({ id: ++nextId.current, file, dir, credential, sent: 0, state: "queued" as const }));
    setUploads((us) => selectedOwner === credentialOwner() ? [...us, ...added] : us);
  }, []);
  const upload = useCallback((files: File[], dir: string) => enqueue(files, dir, owner), [enqueue, owner]);
  useEffect(() => {
    setUploads((us) => us.filter((u) => u.credential.owner === owner));
  }, [owner]);
  useEffect(() => {
    activeRemote.current.forEach((operation, id) => {
      if (operation.owner.workspaceOwner === workspaceOwner) return;
      const credential = operation.owner.credential;
      if (credential.owner !== credentialOwner() || credential.token !== getToken()) operation.ac.abort();
      // A different workspace hides the row and drops its handle. The authoritative POST continues unless credentials died.
      activeRemote.current.delete(id);
    });
    setRemoteOperations((ops) => ops.filter((op) => op.owner.workspaceOwner === workspaceOwner));
  }, [workspaceOwner]);
  const sending = ownedUploads.find((u) => u.state === "sending");
  const next = ownedUploads.find((u) => u.state === "queued");
  useEffect(() => {
    if (sending || !next || next.credential.owner !== credentialOwner() || active.current.has(next.id)) return;
    const ac = new AbortController();
    active.current.set(next.id, ac);
    const current = () => !ac.signal.aborted && next.credential.owner === credentialOwner() && active.current.get(next.id) === ac;
    patch(next.id, { state: "sending", ac, sent: 0, error: undefined });
    uploadToDir(mid, next.file, { dir: next.dir, cwd, credential: next.credential, conflict: next.conflict, signal: ac.signal, onProgress: (sent) => { if (current()) patch(next.id, { sent }); } })
      .then(() => {
        if (!current()) return;
        patch(next.id, { state: "done", sent: next.file.size, ac: undefined });
        changed.current([next.dir]);
      })
      .catch((e) => {
        if (next.credential.owner !== credentialOwner() || active.current.get(next.id) !== ac) return;
        if (isAbort(e)) return setUploads((us) => us.filter((u) => u.id !== next.id)); // cancelled: it goes
        if (!current()) return;
        if (isNameTaken(e) && !next.conflict) return patch(next.id, { state: "conflict", ac: undefined });
        patch(next.id, { state: "failed", error: opErrorText(e), ac: undefined });
      })
      .finally(() => { if (active.current.get(next.id) === ac) active.current.delete(next.id); });
  }, [mid, cwd, sending, next, patch]);
  // a finished one leaves the tray a moment later
  useEffect(() => {
    if (!uploads.some((u) => u.state === "done")) return;
    const id = setTimeout(() => setUploads((us) => us.filter((u) => u.state !== "done")), 1600);
    return () => clearTimeout(id);
  }, [uploads]);
  // Uploads stop with the panel. Remote POSTs continue: dropping their handles only stops polling and hides the UI.
  useEffect(() => () => {
    active.current.forEach((ac) => ac.abort());
    active.current.clear();
    activeRemote.current.clear();
  }, []);

  // ── the name question: the first upload waiting on it, else a move's ──
  const [moveAsk, setMoveAsk] = useState<{ intent: MutationIntent; ask: NameAsk } | null>(null);
  const moveQuestion = useRef<NameAsk | null>(null);
  const conflicted = ownedUploads.filter((u) => u.state === "conflict");
  const uploadAsk: NameAsk | null = conflicted.length
    ? {
        name: conflicted[0].file.name,
        dir: conflicted[0].dir,
        more: conflicted.length - 1,
        answer: (c, all) => {
          if (owner !== credentialOwner()) return;
          const ids = new Set(all ? conflicted.map((u) => u.id) : [conflicted[0].id]);
          setUploads((us) => (c ? us.map((u) => (ids.has(u.id) ? { ...u, state: "queued", conflict: c } : u)) : us.filter((u) => !ids.has(u.id))));
        },
      }
    : null;

  // ── the chooser ──
  const input = useRef<HTMLInputElement>(null);
  const choice = useRef<{ dir: string; owner: string } | null>(null);
  const choose = useCallback((dir: string) => {
    if (owner !== credentialOwner() || !getToken()) return;
    choice.current = { dir, owner };
    input.current?.click();
  }, [owner]);

  // ── what was done, at the panel's foot: the page's toasts sit over the chat box beside it ──
  const [notice, setNotice] = useState<(Notice & { owner: WorkspaceOwner }) | null>(null);
  const noticeId = useRef(0);
  const say = useCallback((candidate: WorkspaceOwner, text: string, tone: Notice["tone"], action?: ToastAction) => {
    if (currentWorkspace(candidate)) setNotice({ owner: candidate, id: ++noticeId.current, text, tone, action });
  }, [currentWorkspace]);

  // ── remote copies and zips: the long POST is authoritative; polling only paints its progress ──
  // Requests live at workspace scope. The current path controls only row visibility and polling, so a return restores Cancel.
  const executeRemote = useCallback(async (op: RemoteOperation, conflict?: Conflict) => {
    const previous = activeRemote.current.get(op.id);
    const ac = new AbortController();
    const operationId = uuid();
    activeRemote.current.set(op.id, { ac, operationId, owner: op.owner, shown: previous?.shown ?? op.shown });
    patchRemote(op.id, { operationId, state: "running", bytesDone: 0, bytesTotal: 0, filesDone: 0, filesTotal: 0 });
    const candidate = op.owner;
    remotePostCredentials.set(ac, candidate.credential.owner);
    const auth = { ...candidate.credential, isAuthContextCurrent: () => currentWorkspace(candidate.workspaceOwner) };
    let settled = false;
    let pollTimer: ReturnType<typeof setTimeout> | undefined;
    const current = () => !settled && !ac.signal.aborted && activeRemote.current.get(op.id)?.operationId === operationId;
    const visible = () => currentWorkspace(candidate.workspaceOwner) && liveOwner.current?.path === candidate.path;
    const poll = async () => {
      if (!current()) return;
      if (visible()) {
        try {
          const status = await fileOperation(candidate.mid, operationId, auth, ac.signal);
          if (current() && visible()) {
            const active = activeRemote.current.get(op.id);
            if (active) active.shown = true;
            patchRemote(op.id, {
              shown: true,
              bytesDone: status.bytes_done,
              bytesTotal: status.bytes_total,
              filesDone: status.files_done,
              filesTotal: status.files_total,
            });
          }
        } catch {
          // The POST remains authoritative; an old node or a transient read failure only leaves progress unknown.
        }
      }
      if (current()) pollTimer = setTimeout(() => void poll(), 250);
    };
    const showTimer = setTimeout(() => {
      if (current()) void poll();
    }, 1000);
    try {
      let archiveName = op.name;
      if (op.kind === "copy") {
        await copyEntries(candidate.mid, op.targets.map((x) => x.path), candidate.cwd, op.dir, conflict, auth, { operationId, signal: ac.signal });
      } else {
        const zipped = await zipEntries(candidate.mid, op.targets.map((x) => x.path), candidate.cwd, op.name, op.dir, auth, { operationId, signal: ac.signal });
        archiveName = zipped.name;
      }
      if (!current()) return;
      const shown = activeRemote.current.get(op.id)?.shown ?? false;
      activeRemote.current.delete(op.id);
      if (!currentWorkspace(candidate.workspaceOwner)) return;
      if (shown) {
        patchRemote(op.id, { state: "done" });
        setTimeout(() => setRemoteOperations((ops) => ops.filter((x) => x.id !== op.id)), 1600);
      } else {
        setRemoteOperations((ops) => ops.filter((x) => x.id !== op.id));
      }
      changed.current([op.dir]);
      say(candidate.workspaceOwner, op.kind === "copy"
        ? t(op.duplicate ? "files-ops.duplicated" : "files-ops.copied", { count: op.targets.length, dir: shortPath(op.dir) })
        : t("files-ops.compressed", { name: archiveName }), "ok");
    } catch (e) {
      if (!current()) return;
      if (op.kind === "copy" && isNameTaken(e) && !conflict) {
        const name = typeof (e as { body?: { name?: unknown } }).body?.name === "string" ? String((e as { body: { name: string } }).body.name) : op.name;
        patchRemote(op.id, { state: "conflict", conflictName: name });
        return;
      }
      activeRemote.current.delete(op.id);
      setRemoteOperations((ops) => ops.filter((x) => x.id !== op.id));
      if (isAbort(e) || !currentWorkspace(candidate.workspaceOwner)) return;
      say(candidate.workspaceOwner, op.kind === "copy" ? opErrorText(e) : t("files-ops.zip_failed", { error: opErrorText(e) }), "error");
    } finally {
      settled = true;
      remotePostCredentials.delete(ac);
      clearTimeout(showTimer);
      if (pollTimer) clearTimeout(pollTimer);
    }
  }, [currentWorkspace, patchRemote, say]);

  const startRemote = useCallback((kind: RemoteOperation["kind"], owner: MutationOwner, targets: OpTarget[], dir: string, name: string, conflict?: Conflict, duplicate = false) => {
    const op: RemoteOperation = {
      id: ++nextId.current,
      operationId: "",
      kind,
      duplicate,
      name,
      targets: targets.map((target) => ({ ...target })),
      dir,
      owner,
      state: "running",
      shown: false,
      filesDone: 0,
      filesTotal: 0,
      bytesDone: 0,
      bytesTotal: 0,
    };
    setRemoteOperations((ops) => [...ops, op]);
    void executeRemote(op, conflict);
  }, [executeRemote]);

  const cancelRemote = useCallback((id: number) => {
    activeRemote.current.get(id)?.ac.abort();
    activeRemote.current.delete(id);
    setRemoteOperations((ops) => ops.filter((op) => op.id !== id));
  }, []);

  const retryRemote = useCallback((op: RemoteOperation, conflict: Conflict | null) => {
    if (op.state !== "conflict" || activeRemote.current.get(op.id)?.owner !== op.owner) return;
    if (!conflict) return cancelRemote(op.id);
    void executeRemote(op, conflict);
  }, [cancelRemote, executeRemote]);

  // ── move, trash, download ──
  const [moving, setMoving] = useState<MutationIntent | null>(null);
  const [trashing, setTrashing] = useState<MutationIntent | null>(null);
  const movingRef = useRef<MutationIntent | null>(null);
  const trashingRef = useRef<MutationIntent | null>(null);
  const moveRequest = useRef<MutationIntent | null>(null);
  const moveLocation = useRef<{ intent: MutationIntent; dir: string } | null>(null);
  const trashRequest = useRef<MutationIntent | null>(null);
  const [moveBusy, setMoveBusy] = useState<MutationIntent | null>(null);
  const [moveFailure, setMoveFailure] = useState<{ intent: MutationIntent; text: string } | null>(null);
  const [trashBusy, setTrashBusy] = useState<MutationIntent | null>(null);
  const activeMove = useCallback((intent: MutationIntent) => movingRef.current === intent && currentOwner(intent.owner), [currentOwner]);
  const activeTrash = useCallback((intent: MutationIntent) => trashingRef.current === intent && currentOwner(intent.owner), [currentOwner]);
  const openMove = useCallback((targets: OpTarget[], copy = false) => {
    if (!targets.length || !currentOwner(mutationOwner)) return;
    const intent: MutationIntent = { owner: mutationOwner, targets: targets.map((target) => ({ ...target })), ...(copy ? { copy: true } : {}) };
    movingRef.current = intent;
    moveLocation.current = null;
    moveQuestion.current = null;
    setMoveAsk(null);
    setMoveFailure(null);
    setMoving(intent);
  }, [currentOwner, mutationOwner]);
  const openTrash = useCallback((targets: OpTarget[]) => {
    if (!targets.length || !currentOwner(mutationOwner)) return;
    const intent = { owner: mutationOwner, targets: targets.map((target) => ({ ...target })) };
    trashingRef.current = intent;
    setTrashing(intent);
  }, [currentOwner, mutationOwner]);

  useEffect(() => {
    setMoving((value) => value?.owner === mutationOwner ? value : null);
    setTrashing((value) => value?.owner === mutationOwner ? value : null);
    setMoveAsk((value) => value?.intent.owner === mutationOwner ? value : null);
    setMoveFailure((value) => value?.intent.owner === mutationOwner ? value : null);
    setMoveBusy((value) => value?.owner === mutationOwner ? value : null);
    setTrashBusy((value) => value?.owner === mutationOwner ? value : null);
  }, [mutationOwner]);
  useEffect(() => setNotice((value) => value?.owner === workspaceOwner ? value : null), [workspaceOwner]);

  const doMove = useCallback(
    async (intent: MutationIntent, dir: string, conflict?: Conflict) => {
      if (!activeMove(intent) || moveRequest.current === intent) return;
      moveRequest.current = intent;
      moveLocation.current = { intent, dir };
      setMoveBusy(intent);
      setMoveFailure(null);
      const { targets, owner: candidate } = intent;
      const from = [...new Set(targets.map((x) => parentOf(x.path)))];
      try {
        const auth = { ...candidate.credential, isAuthContextCurrent: () => activeMove(intent) };
        await moveEntries(candidate.mid, targets.map((x) => x.path), candidate.cwd, dir, conflict, auth);
        if (!activeMove(intent)) return;
        movingRef.current = null;
        setMoving(null);
        changed.current([...from, dir]);
        say(candidate.workspaceOwner, t("files-ops.moved", { count: targets.length, dir: shortPath(dir) }), "ok");
      } catch (e) {
        if (!activeMove(intent)) return;
        if (isNameTaken(e) && !conflict) {
          const question: NameAsk = {
            name: typeof (e as { body?: { name?: unknown } }).body?.name === "string" ? String((e as { body: { name: string } }).body.name) : targets[0].name,
            dir,
            more: 0,
            answer: (c) => {
              if (!activeMove(intent) || moveQuestion.current !== question) return;
              moveQuestion.current = null;
              setMoveAsk(null);
              if (c) void doMove(intent, dir, c);
            },
          };
          moveQuestion.current = question;
          setMoveAsk({ intent, ask: question });
          return;
        }
        setMoveFailure({ intent, text: opErrorText(e) });
      } finally {
        if (moveRequest.current === intent) moveRequest.current = null;
        if (activeMove(intent)) setMoveBusy((value) => value === intent ? null : value);
      }
    },
    [activeMove, say],
  );

  const doCopy = useCallback((intent: MutationIntent, dir: string) => {
    if (!intent.copy || !activeMove(intent)) return;
    movingRef.current = null;
    moveQuestion.current = null;
    setMoving(null);
    setMoveAsk(null);
    setMoveBusy(null);
    setMoveFailure(null);
    startRemote("copy", intent.owner, intent.targets, dir, intent.targets.map((x) => x.name).join(", "));
  }, [activeMove, startRemote]);

  const doTrash = useCallback(async (intent: MutationIntent) => {
    if (!activeTrash(intent) || trashRequest.current === intent) return;
    trashRequest.current = intent;
    setTrashBusy(intent);
    const { targets, owner: candidate } = intent;
    try {
      const done = await trashEntries(candidate.mid, targets.map((x) => x.path), candidate.cwd, { ...candidate.credential, isAuthContextCurrent: () => activeTrash(intent) });
      if (!activeTrash(intent)) return;
      trashingRef.current = null;
      setTrashing(null);
      const dirs = [...new Set(targets.map((x) => parentOf(x.path)))];
      changed.current(dirs);
      const ids = done.map((d) => d.trash_id).filter(Boolean);
      let undoStarted = false;
      say(
        candidate.workspaceOwner,
        targets.length === 1 ? t("files-ops.trashed_named", { name: targets[0].name }) : t("files-ops.trashed", { count: targets.length }),
        "ok",
        ids.length
          ? {
              label: t("files-ops.undo"),
              run: () => {
                if (!currentWorkspace(candidate.workspaceOwner) || undoStarted) return;
                undoStarted = true;
                restoreEntries(candidate.mid, ids, { ...candidate.credential, isAuthContextCurrent: () => currentWorkspace(candidate.workspaceOwner) })
                  .then(() => {
                    if (!currentWorkspace(candidate.workspaceOwner)) return;
                    changed.current(dirs);
                    say(candidate.workspaceOwner, t("files-ops.restored", { count: ids.length }), "ok");
                  })
                  .catch((e) => { if (currentWorkspace(candidate.workspaceOwner)) say(candidate.workspaceOwner, t("files-ops.restore_failed", { error: opErrorText(e) }), "error"); });
              },
            }
          : undefined,
      );
    } catch (e) {
      if (activeTrash(intent)) say(candidate.workspaceOwner, t("files-ops.trash_failed", { error: opErrorText(e) }), "error");
    } finally {
      if (trashRequest.current === intent) trashRequest.current = null;
      if (currentOwner(candidate)) setTrashBusy((value) => value === intent ? null : value);
    }
  }, [activeTrash, currentOwner, currentWorkspace, say]);

  const canZip = hubHas(features, FILE_ZIP_FEATURE);
  const download = useCallback(
    (targets: OpTarget[]) => {
      const save = (path: string, name: string) => {
        const a = document.createElement("a");
        a.href = fileDownloadURL(mid, path, cwd);
        a.download = name;
        a.rel = "noopener";
        document.body.append(a);
        a.click();
        a.remove();
      };
      if (targets.length === 1 && !targets[0].dir) return save(targets[0].path, targets[0].name);
      if (!canZip) {
        // an older hub: one link each, folders left out
        for (const x of targets.filter((x) => !x.dir)) save(x.path, x.name);
        return;
      }
      // several items or a folder: one zip the computer makes in a temporary folder, then downloaded (#5828)
      const name = targets.length === 1 ? `${targets[0].name}.zip` : t("files-ops.archive_name");
      toast(t("files-ops.zipping", { count: targets.length }));
      zipEntries(mid, targets.map((x) => x.path), cwd, name)
        .then((z) => save(z.path, name))
        .catch((e) => !isAbort(e) && toast(t("files-ops.zip_failed", { error: opErrorText(e) }), "error"));
    },
    [mid, cwd, canZip],
  );

  const canCopy = hubHas(features, FILE_COPY_FEATURE);
  // Duplicate and Compress (#5830): beside the first one, named by the computer ("name 2.ext", "Archive 2.zip")
  const duplicate = useCallback((targets: OpTarget[]) => {
    if (!targets.length || !currentOwner(mutationOwner)) return;
    const dir = parentOf(targets[0].path);
    startRemote("copy", mutationOwner, targets, dir, targets.map((x) => x.name).join(", "), "keep", true);
  }, [currentOwner, mutationOwner, startRemote]);
  const compress = useCallback((targets: OpTarget[]) => {
    if (!targets.length || !currentOwner(mutationOwner)) return;
    const dir = parentOf(targets[0].path);
    const name = targets.length === 1 ? `${targets[0].name.replace(/\.[^./]+$/, "") || targets[0].name}.zip` : t("files-ops.archive_name");
    startRemote("zip", mutationOwner, targets, dir, name);
  }, [currentOwner, mutationOwner, startRemote]);

  const rowItems = useCallback(
    (x: OpTarget, open: () => void): MenuItem[] => {
      let renaming = false;
      const items: MenuItem[] = [{ key: "open", label: x.dir ? t("files-ops.open_folder") : t("files-ops.open"), icon: x.dir ? "folder" : "file", onSelect: open }];
      if (!x.dir) items.push({ key: "download", label: t("files-ops.download"), icon: "download", onSelect: () => download([x]) });
      else if (canZip) items.push({ key: "download", label: t("files-ops.download_zip"), icon: "download", onSelect: () => download([x]) });
      if (x.dir && canUpload) items.push({ key: "upload", label: t("files-ops.upload_here"), icon: "upload", onSelect: () => choose(x.path) });
      if (canEdit) {
        items.push(
          {
            key: "rename",
            label: t("files-ops.rename"),
            icon: "pencil",
            divider: true,
            onSelect: () => {},
            rename: {
              value: x.name,
              commit: async (name) => {
                if (!currentOwner(mutationOwner) || renaming) throw new DOMException("aborted", "AbortError");
                const problem = nameProblem(name);
                if (problem) throw new Error(problem);
                renaming = true;
                try {
                  await renameEntry(mutationOwner.mid, x.path, mutationOwner.cwd, name.trim(), { ...mutationOwner.credential, isAuthContextCurrent: () => currentOwner(mutationOwner) });
                  if (!currentOwner(mutationOwner)) throw new DOMException("aborted", "AbortError");
                  changed.current([parentOf(x.path)]);
                } catch (e) {
                  if (!currentOwner(mutationOwner)) throw new DOMException("aborted", "AbortError");
                  throw new Error(opErrorText(e));
                } finally {
                  renaming = false;
                }
              },
            },
          },
          { key: "move", label: t("files-ops.move"), icon: "arrowRight", onSelect: () => openMove([x]) },
          ...(canCopy
            ? [
                { key: "copy_to", label: t("files-ops.copy_to"), icon: "copy" as const, onSelect: () => openMove([x], true) },
                { key: "duplicate", label: t("files-ops.duplicate"), icon: "copy" as const, onSelect: () => duplicate([x]) },
              ]
            : []),
          ...(canZip ? [{ key: "compress", label: t("files-ops.compress"), icon: "archive" as const, onSelect: () => compress([x]) }] : []),
        );
      }
      items.push({ key: "copy", label: t("files-ops.copy_path"), icon: "copy", divider: !canEdit, onSelect: () => copyText(x.path) });
      if (canEdit) items.push({ key: "trash", label: t("files-ops.trash"), icon: "trash", danger: true, divider: true, onSelect: () => openTrash([x]) });
      return items;
    },
    [canUpload, canEdit, canZip, canCopy, download, choose, currentOwner, mutationOwner, openMove, openTrash, duplicate, compress],
  );

  const remoteConflict = ownedRemoteOperations.find((op) => op.state === "conflict");
  const remoteAsk: NameAsk | null = remoteConflict ? {
    name: remoteConflict.conflictName ?? remoteConflict.name,
    dir: remoteConflict.dir,
    more: 0,
    answer: (choice) => retryRemote(remoteConflict, choice),
  } : null;
  const ask = uploadAsk ?? remoteAsk ?? (moveAsk?.intent.owner === mutationOwner && currentOwner(moveAsk.intent.owner) ? moveAsk.ask : null);
  const visibleMove = moving?.owner === mutationOwner && currentOwner(moving.owner) ? moving : null;
  const visibleTrash = trashing?.owner === mutationOwner && currentOwner(trashing.owner) ? trashing : null;
  const visibleNotice = notice?.owner === workspaceOwner && currentWorkspace(notice.owner) ? notice : null;

  const ui = (
    <>
      <input
        ref={input}
        type="file"
        multiple
        hidden
        data-file-ops-input
        aria-hidden
        tabIndex={-1}
        onChange={(e) => {
          if (choice.current) enqueue(Array.from(e.target.files ?? []), choice.current.dir, choice.current.owner);
          choice.current = null;
          e.target.value = "";
        }}
      />
      <UploadTray
        uploads={ownedUploads}
        operations={ownedRemoteOperations}
        // a sending one is aborted (and goes when its request does), a waiting or failed one goes at once
        onCancel={(u) => (u.ac ? u.ac.abort() : setUploads((us) => us.filter((x) => x.id !== u.id)))}
        onCancelRemote={cancelRemote}
        onRetry={(u) => { if (u.credential.owner === credentialOwner()) patch(u.id, { state: "queued", error: undefined }); }}
      />
      <PanelNotice notice={visibleNotice} onDone={(id) => setNotice((n) => (n?.id === id ? null : n))} />
      <NameDialog ask={ask} />
      {visibleMove && moveAsk?.intent !== visibleMove && <MoveDialog
        intent={visibleMove}
        initialDir={moveLocation.current?.intent === visibleMove ? moveLocation.current.dir : parentOf(visibleMove.targets[0].path)}
        current={() => activeMove(visibleMove)}
        busy={moveBusy === visibleMove}
        failure={moveFailure?.intent === visibleMove ? moveFailure.text : ""}
        onClose={() => {
          if (!activeMove(visibleMove) || moveRequest.current === visibleMove) return;
          movingRef.current = null;
          moveQuestion.current = null;
          setMoveAsk(null);
          setMoving(null);
        }}
        onMove={(dir) => visibleMove.copy ? doCopy(visibleMove, dir) : void doMove(visibleMove, dir)}
      />}
      {visibleTrash && <TrashDialog targets={visibleTrash.targets} busy={trashBusy === visibleTrash} onClose={() => {
        if (!activeTrash(visibleTrash) || trashRequest.current === visibleTrash) return;
        trashingRef.current = null;
        setTrashing(null);
      }} onTrash={() => void doTrash(visibleTrash)} />}
    </>
  );

  return { scope: mutationOwner, canUpload, canEdit, canZip, canCopy, upload, choose, rowItems, move: openMove, copy: (targets: OpTarget[]) => openMove(targets, true), duplicate, compress, trash: openTrash, download, ui };
}
/** Uploads and remote operations at the panel's foot: progress and cancel, with upload failure retry. */
function UploadTray({ uploads, operations, onCancel, onCancelRemote, onRetry }: {
  uploads: Upload[];
  operations: RemoteOperation[];
  onCancel: (u: Upload) => void;
  onCancelRemote: (id: number) => void;
  onRetry: (u: Upload) => void;
}) {
  const shown = uploads.filter((u) => u.state !== "conflict");
  const remoteShown = operations.filter((op) => op.shown && op.state !== "conflict");
  if (!shown.length && !remoteShown.length) return null;
  const left = shown.filter((u) => u.state === "queued" || u.state === "sending").length;
  const remoteLeft = remoteShown.filter((op) => op.state === "running").length;
  return (
    <section aria-label={shown.length ? t("files-ops.uploads") : t("files-ops.file_operations")} className="shrink-0 border-t border-line bg-inset px-3 py-2">
      <p className="pb-1 text-[11.5px] font-medium text-ink-2" aria-live="polite">
        {left ? t("files-ops.uploading", { count: left }) : remoteLeft ? t("files-ops.operations_running", { count: remoteLeft }) : t("files-ops.uploads_done")}
      </p>
      <ul className="flex max-h-40 flex-col gap-1 overflow-y-auto">
        {shown.map((u) => {
          const pct = u.file.size ? Math.min(100, Math.round((u.sent / u.file.size) * 100)) : u.state === "done" ? 100 : 0;
          return (
            <li key={u.id} className="flex items-center gap-2">
              <FileGlyph name={u.file.name} />
              <span className="min-w-0 flex-1">
                <span className="flex items-baseline gap-2">
                  <span className="truncate text-[12.5px] text-ink" title={joinPath(u.dir, u.file.name)}>
                    {u.file.name}
                  </span>
                  <span className="ml-auto shrink-0 text-[11px] text-ink-3 tabular-nums">
                    {u.state === "done" ? t("files-ops.upload_done") : u.state === "queued" ? t("files-ops.upload_waiting") : u.state === "failed" ? "" : `${pct}%`}
                  </span>
                </span>
                {u.state === "failed" ? (
                  <span className="block truncate text-[11.5px] text-red" title={u.error}>
                    {u.error}
                  </span>
                ) : (
                  <span
                    role="progressbar"
                    aria-label={u.file.name}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={pct}
                    aria-valuetext={`${pct}% · ${bytes(u.sent)} / ${bytes(u.file.size)}`}
                    className="mt-1 block h-1 overflow-hidden rounded-full bg-field"
                  >
                    <span className={cx("block h-full rounded-full transition-[width] duration-200", u.state === "done" ? "bg-green" : "bg-accent")} style={{ width: `${pct}%` }} />
                  </span>
                )}
              </span>
              {u.state === "failed" && <IconButton icon="retry" label={t("files-ops.upload_retry", { name: u.file.name })} onClick={() => onRetry(u)} size={26} iconSize={13} />}
              {u.state !== "done" && <IconButton icon="x" label={t("files-ops.upload_cancel", { name: u.file.name })} onClick={() => onCancel(u)} size={26} iconSize={13} />}
            </li>
          );
        })}
        {remoteShown.map((op) => <RemoteOperationRow key={op.id} operation={op} onCancel={() => onCancelRemote(op.id)} />)}
      </ul>
    </section>
  );
}

function RemoteOperationRow({ operation: op, onCancel }: { operation: RemoteOperation; onCancel: () => void }) {
  const pct = op.state === "done" ? 100 : op.bytesTotal > 0 ? Math.min(100, Math.round((op.bytesDone / op.bytesTotal) * 100)) : op.filesTotal > 0 ? Math.min(100, Math.round((op.filesDone / op.filesTotal) * 100)) : 0;
  const name = op.kind === "zip" ? op.name : op.targets[0]?.name ?? op.name;
  const files = op.filesTotal > 0 ? `${op.filesDone} / ${op.filesTotal}` : "";
  const detail = op.state === "done" ? t("files-ops.operation_done") : op.bytesTotal > 0 ? `${files}${files ? " · " : ""}${bytes(op.bytesDone)} / ${bytes(op.bytesTotal)}` : files || t("files-ops.operation_preparing");
  return (
    <li className="flex items-center gap-2">
      <FileGlyph name={name} />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span className="truncate text-xs text-ink" title={op.dir}>{op.name}</span>
          <span className="ml-auto shrink-0 text-xs text-ink-3 tabular-nums">{op.state === "done" ? t("files-ops.operation_done") : `${pct}%`}</span>
        </span>
        <span
          role="progressbar"
          aria-label={op.targets[0]?.name ?? op.name}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
          aria-valuetext={`${pct}% · ${detail}`}
          className="mt-1 block h-1 overflow-hidden rounded-full bg-field"
        >
          <span className={cx("block h-full rounded-full transition-[width] duration-200", op.state === "done" ? "bg-green" : "bg-accent")} style={{ width: `${pct}%` }} />
        </span>
        <span className="mt-1 block truncate text-xs text-ink-3" title={detail}>{detail}</span>
      </span>
      {op.state !== "done" && <IconButton
        icon="x"
        label={t(op.kind === "copy" ? "files-ops.copy_cancel" : "files-ops.compress_cancel", { name: op.name })}
        onClick={onCancel}
        size={26}
        iconSize={13}
      />}
    </li>
  );
}

interface Notice {
  id: number;
  text: string;
  tone: "ok" | "error";
  action?: ToastAction;
}

/** A line at the panel's foot for what was just done ("Moved to trash · Undo"). It goes after a while, held while the
 *  pointer or the focus is on it (WCAG 2.2.1), or with its ×; its button runs and it goes. */
function PanelNotice({ notice, onDone }: { notice: Notice | null; onDone: (id: number) => void }) {
  const [held, setHeld] = useState(false);
  useEffect(() => {
    if (!notice || held) return;
    const id = setTimeout(() => onDone(notice.id), notice.action ? 8000 : notice.tone === "error" ? 6000 : 3000);
    return () => clearTimeout(id);
  }, [notice, held, onDone]);
  return (
    <div role="status" aria-live="polite" className="shrink-0">
      {notice && (
        <div
          key={notice.id}
          onMouseEnter={() => setHeld(true)}
          onMouseLeave={() => setHeld(false)}
          onFocus={() => setHeld(true)}
          onBlur={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setHeld(false)}
          className="flex items-center gap-2 border-t border-line bg-inset px-3 py-2 text-[12.5px] text-ink"
          style={{ animation: "fade-in 160ms ease-out both" }}
        >
          <Icon name={notice.tone === "error" ? "warning" : "check"} size={13} stroke={2.4} className={cx("shrink-0", notice.tone === "error" ? "text-red" : "text-green")} />
          <span className="min-w-0 flex-1 truncate" title={notice.text}>
            {notice.text}
          </span>
          {notice.action && (
            <Button
              tone="quiet"
              onClick={() => {
                notice.action!.run();
                onDone(notice.id);
              }}
            >
              {notice.action.label}
            </Button>
          )}
          <IconButton icon="x" label={t("files-ops.dismiss")} onClick={() => onDone(notice.id)} size={24} iconSize={12} />
        </div>
      )}
    </div>
  );
}

/** "x already exists": replace it, keep both, or skip; with more waiting, the same for the rest too. */
function NameDialog({ ask }: { ask: NameAsk | null }) {
  const [all, setAll] = useState(false);
  useEffect(() => setAll(false), [ask?.name, ask?.dir]);
  const answer = (c: Conflict | null) => ask?.answer(c, all);
  return (
    <Dialog
      open={Boolean(ask)}
      onClose={() => answer(null)}
      title={t("files-ops.exists_title")}
      footer={
        <>
          <Button tone="quiet" onClick={() => answer(null)}>
            {t("files-ops.skip")}
          </Button>
          <Button onClick={() => answer("keep")}>{t("files-ops.keep_both")}</Button>
          <Button tone="primary" onClick={() => answer("replace")}>
            {t("files-ops.replace")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{ask && t("files-ops.exists_body", { name: ask.name, dir: shortPath(ask.dir) })}</p>
      {ask && ask.more > 0 && (
        <label className="mt-3 flex items-center gap-2 text-[12.5px] text-ink-2">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} className="accent-[var(--accent)]" />
          {t("files-ops.same_for_rest", { count: ask.more })}
        </label>
      )}
    </Dialog>
  );
}

/** "Move to...": a folder picker on the machine, from the folder the first one is in; up, into a folder, then move here. */
function MoveDialog({
  intent,
  initialDir,
  current,
  busy,
  failure,
  onClose,
  onMove,
}: {
  intent: MutationIntent;
  initialDir: string;
  current: () => boolean;
  busy: boolean;
  failure: string;
  onClose: () => void;
  onMove: (dir: string) => void;
}) {
  const { targets, owner } = intent;
  const [at, setAt] = useState(initialDir);
  const [listing, setListing] = useState<{ at: string; dirs?: string[]; error?: string } | null>(null);
  const currentRef = useRef(current);
  currentRef.current = current;
  useEffect(() => {
    if (!at || !currentRef.current()) return;
    const ac = new AbortController();
    let active = true;
    setListing(null);
    api<FsListing>(`/machines/${enc(owner.mid)}/fs?path=${enc(at)}&cwd=${enc(owner.cwd)}`, { signal: ac.signal, token: owner.credential.token, tokenOwner: owner.credential.owner, isAuthContextCurrent: () => active && currentRef.current() })
      .then((l) => { if (active && currentRef.current()) setListing({ at, dirs: l.entries.filter((e) => e.dir && !e.name.startsWith(".")).map((e) => e.name) }); })
      .catch((e) => { if (active && currentRef.current() && !isAbort(e)) setListing({ at, error: errorText(e) }); });
    return () => { active = false; ac.abort(); };
  }, [owner, at]);
  const sources = useMemo(() => new Set(targets.map((x) => parentOf(x.path))), [targets]);
  // not where they all are already, nor into one of the folders moved
  // a copy may go beside them (it is then a duplicate); never into a folder copied
  const blocked = targets.some((x) => x.dir && within(x.path, at)) || (!intent.copy && sources.size === 1 && sources.has(at));
  const title = intent.copy
    ? targets.length === 1 ? t("files-ops.copy_one_title", { name: targets[0].name }) : t("files-ops.copy_title", { count: targets.length })
    : targets.length === 1 ? t("files-ops.move_one_title", { name: targets[0].name }) : t("files-ops.move_title", { count: targets.length });
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={title}
      width={420}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("files-ops.cancel")}
          </Button>
          <Button tone="primary" onClick={() => onMove(at)} busy={busy} disabled={blocked || !listing?.dirs}>
            {intent.copy ? t("files-ops.copy_here") : t("files-ops.move_here")}
          </Button>
        </>
      }
    >
      <div className="flex items-center gap-1 pb-2">
        <IconButton icon="chevronLeft" label={t("files-ops.up")} onClick={() => setAt(parentOf(at))} disabled={at === "/"} iconSize={16} />
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink" title={at} aria-live="polite">
          {shortPath(at) || "/"}
        </span>
      </div>
      <div aria-label={t("files-ops.folders")} role="group" className="h-56 overflow-y-auto rounded-[10px] border border-line">
        {!listing || listing.at !== at ? (
          <div className="flex h-full items-center justify-center">
            <Loading label={t("files-ops.reading")} />
          </div>
        ) : listing.error ? (
          <p className="p-3 text-[12.5px] text-red">{listing.error}</p>
        ) : listing.dirs!.length === 0 ? (
          <p className="p-3 text-[12.5px] text-ink-3">{t("files-ops.no_folders")}</p>
        ) : (
          listing.dirs!.map((name) => {
            const path = joinPath(at, name);
            const self = targets.some((x) => x.dir && within(x.path, path));
            return (
              <button
                key={name}
                type="button"
                disabled={self}
                onClick={() => setAt(path)}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] text-ink transition-colors hover:bg-hover disabled:opacity-40"
              >
                <FileGlyph name={name} dir />
                <span className="min-w-0 flex-1 truncate">{name}</span>
                <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
              </button>
            );
          })
        )}
      </div>
      {failure && (
        <p role="alert" className="pt-2 text-[12px] text-red">
          {failure}
        </p>
      )}
    </Dialog>
  );
}

/** "Move to trash?": what goes, and that it can be put back. */
function TrashDialog({ targets, busy, onClose, onTrash }: { targets: OpTarget[] | null; busy: boolean; onClose: () => void; onTrash: () => void }) {
  const one = targets?.length === 1 ? targets[0] : null;
  return (
    <Dialog
      open={Boolean(targets)}
      onClose={onClose}
      title={one ? t("files-ops.trash_one_title", { name: one.name }) : t("files-ops.trash_title", { count: targets?.length ?? 0 })}
      width={400}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("files-ops.cancel")}
          </Button>
          <Button tone="danger" onClick={onTrash} busy={busy}>
            {t("files-ops.trash")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{one?.dir ? t("files-ops.trash_folder_body") : t("files-ops.trash_body")}</p>
      {targets && targets.length > 1 && (
        <ul className="mt-2 max-h-32 overflow-y-auto text-[12.5px] text-ink">
          {targets.map((x) => (
            <li key={x.path} className="flex items-center gap-2 py-0.5">
              <FileGlyph name={x.name} dir={x.dir} />
              <span className="truncate">{x.name}</span>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

/** The bar a selection brings: how many, and what can be done to them all. */
export function SelectionBar({ ops, selected, onClear }: { ops: FileOps; selected: OpTarget[]; onClear: () => void }) {
  const files = ops.canZip ? selected : selected.filter((x) => !x.dir);
  return (
    <div role="toolbar" aria-label={t("files-ops.selection")} className="flex h-10 shrink-0 items-center gap-1 border-b border-line bg-inset px-2">
      <IconButton icon="x" label={t("files-ops.clear_selection")} onClick={onClear} iconSize={14} />
      <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink" aria-live="polite">
        {t("files-ops.selected", { count: selected.length })}
      </span>
      <IconButton icon="download" label={t("files-ops.download")} onClick={() => ops.download(files)} disabled={!files.length} iconSize={14} />
      {ops.canCopy && <IconButton icon="copy" label={t("files-ops.copy_to")} onClick={() => ops.copy(selected)} disabled={!selected.length} iconSize={14} />}
      {ops.canEdit && <IconButton icon="arrowRight" label={t("files-ops.move")} onClick={() => ops.move(selected)} disabled={!selected.length} iconSize={14} />}
      {ops.canZip && ops.canEdit && <IconButton icon="archive" label={t("files-ops.compress")} onClick={() => ops.compress(selected)} disabled={!selected.length} iconSize={14} />}
      {ops.canEdit && <IconButton icon="trash" label={t("files-ops.trash")} onClick={() => ops.trash(selected)} disabled={!selected.length} iconSize={14} />}
    </div>
  );
}

/** What covers a folder while files are dragged over it: where they will go. */
export function DropHint({ dir }: { dir: string }) {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-1 z-10 flex items-center justify-center rounded-[12px] border-2 border-dashed border-accent bg-accent-tint/50">
      <span className="flex items-center gap-1.5 rounded-full bg-surface px-3 py-1.5 text-[12.5px] font-medium text-ink shadow-btn">
        <Icon name="upload" size={14} />
        {t("files-ops.drop_here", { dir: baseName(dir) || dir })}
      </span>
    </div>
  );
}

/** A row's own handlers: its menu on right click and the menu key, the folder files dropped on it go to, its selection
 *  state for a screen reader. */
export function rowFrameProps(ops: RowOps | undefined, x: OpTarget, open: () => void) {
  if (!ops) return {};
  return {
    ...contextMenuProps((at) => ops.menu(x, at, open)),
    "data-drop-dir": x.dir ? x.path : parentOf(x.path),
    "aria-selected": ops.selecting ? ops.isSelected(x.path) : undefined,
  };
}

/** A click on a row while selecting (or with ⌘ / Ctrl) picks it instead of opening it. */
export function rowClick(ops: RowOps | undefined, x: OpTarget, open: () => void) {
  return (e: React.MouseEvent) => {
    if (ops && (ops.selecting || e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      ops.toggle(x);
    } else open();
  };
}

/** The checkbox at a row's start while selecting. */
export function RowCheck({ ops, x }: { ops: RowOps | undefined; x: OpTarget }) {
  if (!ops?.selecting) return null;
  return (
    <input
      type="checkbox"
      checked={ops.isSelected(x.path)}
      onChange={() => ops.toggle(x)}
      aria-label={t("files-ops.select_row", { name: x.name })}
      className="ml-3 size-3.5 shrink-0 accent-[var(--accent)]"
    />
  );
}

/** The ⋯ at a row's end (hover devices show it on hover, and on focus): the same menu as a right click. */
export function RowMore({ ops, x, open }: { ops: RowOps | undefined; x: OpTarget; open: () => void }) {
  if (!ops) return null;
  return <IconButton icon="more" label={t("files-ops.row_more", { name: x.name })} onClick={(e) => ops.menu(x, { anchor: e.currentTarget }, open)} size={26} iconSize={13} />;
}
