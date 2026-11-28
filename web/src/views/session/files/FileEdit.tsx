// Editing a text file in the Files panel (#5829; iOS FileEditSheet): the editor under Cancel and Save. Save writes only
// over the version opened (files/write with its revision and the hash of what was opened); when the file changed on
// the computer meanwhile the user chooses: Save as copy, Discard mine (open the new version), Overwrite anyway (asked
// again), or Keep editing. Line endings and a byte order mark go back as the file had them.
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { t } from "../../../i18n";
import { ApiError } from "../../../lib/api";
import { opErrorText, writeTextFile } from "../../../lib/file-ops";
import { dirName, fileName, joinPath, type FileContent } from "../../../lib/files";
import { languageOf } from "../../../lib/files-syntax";
import { Button, Dialog, Loading, toast } from "../../../ui/atoms";

const CodeEditor = lazy(() => import("./CodeEditor"));

/** The editor takes text files up to this (the iPhone's too). */
export const MAX_EDIT_BYTES = 1 << 20;

/** Why a file can't be edited here (null: it can). */
export function editProblem(file: FileContent | null, whole: boolean): string | null {
  if (!file || file.is_dir || typeof file.text !== "string") return t("files-editor.not_text");
  if (file.size > MAX_EDIT_BYTES) return t("files-editor.too_large");
  if (!whole || file.truncated) return t("files-editor.partial");
  return null;
}

/** The text as the editor holds it (LF, no byte order mark) and how to put the file's back. */
export function editText(text: string): { body: string; crlf: boolean; bom: boolean } {
  const bom = text.startsWith("﻿");
  const t2 = bom ? text.slice(1) : text;
  const crlfs = t2.split("\r\n").length - 1;
  const lfs = t2.split("\n").length - 1;
  const crlf = crlfs > 0 && crlfs === lfs; // only when every break is one: a mixed file is left as it is
  return { body: crlf ? t2.replaceAll("\r\n", "\n") : t2, crlf, bom };
}

export function fileText(edited: string, how: { crlf: boolean; bom: boolean }): string {
  return (how.bom ? "﻿" : "") + (how.crlf ? edited.replaceAll("\n", "\r\n") : edited);
}

/** The hex SHA-256 of a text's UTF-8; "" where the page cannot hash (an insecure origin has no crypto.subtle). */
export async function sha256Hex(text: string): Promise<string> {
  if (!globalThis.crypto?.subtle) return "";
  const sum = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(sum)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** "name 2.ext", "name 3.ext"… for i. */
export function copyName(name: string, i: number): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? `${name.slice(0, dot)} ${i}${name.slice(dot)}` : `${name} ${i}`;
}

export function FileEdit({ mid, cwd, file, text, onDone }: {
  mid: string;
  cwd?: string;
  file: FileContent;
  /** The whole file's text as opened. */
  text: string;
  /** Edit is over: saved (the viewer reads the file again) or not. */
  onDone: (changed: boolean) => void;
}) {
  const how = useRef(editText(text)).current;
  const read = useRef<() => string>(() => how.body);
  const revision = useRef(file.revision);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [asking, setAsking] = useState<null | "discard" | "changed" | "overwrite">(null);
  const lang = languageOf(file.name, how.body.slice(0, 200).split("\n")[0]);

  const save = async (force = false) => {
    setSaving(true);
    try {
      const out = await writeTextFile(mid, file.path, cwd ?? "", fileText(read.current(), how), { revision: revision.current, sha256: await sha256Hex(text), ...(force ? { force: true } : {}) });
      revision.current = out.revision;
      toast(t("files-editor.saved"), "ok");
      onDone(true);
    } catch (e) {
      if (e instanceof ApiError && e.code === "file_changed") setAsking("changed");
      else toast(t("files-editor.save_failed", { error: opErrorText(e) }), "error");
    } finally {
      setSaving(false);
    }
  };

  const saveCopy = async () => {
    setAsking(null);
    setSaving(true);
    try {
      for (let i = 2; i < 100; i++) {
        const name = copyName(file.name, i);
        try {
          await writeTextFile(mid, joinPath(dirName(file.path), name), cwd ?? "", fileText(read.current(), how), { create: true });
          toast(t("files-editor.saved_as", { name }), "ok");
          return onDone(true);
        } catch (e) {
          if (!(e instanceof ApiError && e.code === "exists")) throw e;
        }
      }
    } catch (e) {
      toast(t("files-editor.save_failed", { error: opErrorText(e) }), "error");
    } finally {
      setSaving(false);
    }
  };

  const rootRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = rootRef.current;
    if (!el || (!dirty && !saving)) return;
    const onPanelEscape = (ev: Event) => {
      ev.preventDefault();
      if (asking || saving) return;
      if (dirty) setAsking("discard");
    };
    el.addEventListener("persona:escape-unsaved", onPanelEscape);
    return () => el.removeEventListener("persona:escape-unsaved", onPanelEscape);
  }, [dirty, saving, asking]);

  return (
    <div
      ref={rootRef}
      className="flex min-h-0 flex-1 flex-col"
      data-testid="file-edit"
      data-unsaved-edit={dirty || saving ? "" : undefined}
      onKeyDown={(e) => {
        if (e.key !== "Escape" || e.defaultPrevented || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229 || asking) return;
        if ([...document.querySelectorAll("[role=menu],[role=dialog],[role=alertdialog]")].some((el) => !el.closest("[data-closing],[inert]"))) return;
        e.preventDefault();
        e.stopPropagation();
        if (saving) return;
        if (dirty) setAsking("discard");
        else onDone(false);
      }}
    >
      <div className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold text-ink">{fileName(file.path)}</p>
          <p className="truncate text-[11.5px] text-ink-3">{dirty ? t("files-editor.edited") : t("files-editor.editing")}</p>
        </div>
        <Button onClick={() => (dirty ? setAsking("discard") : onDone(false))}>{t("files-editor.cancel")}</Button>
        <Button tone="primary" icon="check" busy={saving} disabled={!dirty || saving} onClick={() => void save()} title="⌘S">
          {t("files-editor.save")}
        </Button>
      </div>
      <Suspense fallback={<div className="flex h-40 items-center justify-center"><Loading label={t("files-editor.loading")} /></div>}>
        <CodeEditor
          text={how.body}
          lang={lang}
          onChange={() => setDirty(true)}
          onSave={() => dirty && !saving && void save()}
          bind={(r) => (read.current = r)}
        />
      </Suspense>
      <Dialog
        open={asking === "discard"}
        onClose={() => setAsking(null)}
        title={t("files-editor.discard_title")}
        footer={
          <>
            <Button onClick={() => setAsking(null)}>{t("files-editor.keep_editing")}</Button>
            <Button tone="danger" onClick={() => onDone(false)}>{t("files-editor.discard")}</Button>
          </>
        }
      >
        <p className="text-xs text-ink-2">{t("files-editor.discard_detail")}</p>
      </Dialog>
      <Dialog
        open={asking === "changed"}
        onClose={() => setAsking(null)}
        title={t("files-editor.changed_title", { name: file.name })}
        width={480}
        footer={
          <>
            <Button onClick={() => setAsking(null)}>{t("files-editor.keep_editing")}</Button>
            <Button tone="danger" onClick={() => setAsking("overwrite")}>{t("files-editor.overwrite_anyway")}</Button>
            <Button onClick={() => onDone(true)}>{t("files-editor.discard_mine")}</Button>
            <Button tone="primary" onClick={() => void saveCopy()}>{t("files-editor.save_copy")}</Button>
          </>
        }
      >
        <p className="text-xs text-ink-2">{t("files-editor.changed_detail")}</p>
      </Dialog>
      <Dialog
        open={asking === "overwrite"}
        onClose={() => setAsking(null)}
        title={t("files-editor.overwrite_title")}
        footer={
          <>
            <Button onClick={() => setAsking(null)}>{t("files-editor.cancel")}</Button>
            <Button tone="danger" onClick={() => { setAsking(null); void save(true); }}>{t("files-editor.overwrite")}</Button>
          </>
        }
      >
        <p className="text-xs text-ink-2">{t("files-editor.overwrite_detail")}</p>
      </Dialog>
    </div>
  );
}
