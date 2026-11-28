import { api, enc } from "./api";
import { uuid } from "./format";
import { t } from "../i18n";

export const MAX_IMAGE = 8 * 1024 * 1024;
export const MAX_FILE = 200 * 1024 * 1024;

export interface LocalAttachment {
  id: string;
  name: string;
  mime: string;
  file: File;
  url?: string;
}

/** `file` as an attachment, refused over MAX_IMAGE for a picture (`imageLimit`: New Agent scales them down, lib/image-scale.ts)
 *  or MAX_FILE for anything else. */
export function pickAttachment(file: File, imageLimit = MAX_IMAGE): LocalAttachment {
  const image = file.type.startsWith("image/");
  if (file.size > (image ? imageLimit : MAX_FILE)) {
    throw new Error(t(image ? "lib-attachments.too_big_image" : "lib-attachments.too_big_file", { name: file.name }));
  }
  return { id: uuid(), name: file.name || "image.png", mime: file.type || "application/octet-stream", file, url: image ? URL.createObjectURL(file) : undefined };
}

/** A picture held as base64 (a forwarded or a draft one, lib/drafts.ts DraftImage) as a file, to send as a file. */
export function base64File(name: string, mime: string, data: string): File {
  const bin = atob(data);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new File([bytes], name, { type: mime });
}

export async function imagePayload(file: Blob): Promise<{ mime: string; data: string }> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return { mime: file.type || "image/png", data: btoa(bin) };
}

/**
 * Sends a file to machine `mid` (POST /machines/{mid}/files, docs/protocol/protocol.md "Sending files"): the chat's attachments, a new
 * agent's files and the terminal's pasted ones. Resolves to the file's absolute path on that machine.
 */
export async function sendFile(mid: string, file: File, opts: { name?: string; dest?: "terminal" | "health"; signal?: AbortSignal } = {}): Promise<string> {
  const query = `name=${enc(opts.name ?? file.name)}${opts.dest ? `&dest=${opts.dest}` : ""}`;
  const r = await api<{ path: string }>(`/machines/${enc(mid)}/files?${query}`, {
    method: "POST",
    signal: opts.signal,
    body: file,
    headers: { "Content-Type": "application/octet-stream" },
  });
  return r.path;
}

export function quotePath(path: string) {
  return /[\s'"\\$`]/.test(path) ? `'${path.replace(/'/g, `'\\''`)}'` : path;
}

/** Frees the pictures' object URLs, an edited one's source too (lib/image-edit.ts EditedImage): the composer made
 *  every `blob:` source it holds (a picture in the chat edited from the viewer keeps its own URL, never a blob). */
export function releaseAttachments(attachments: { url?: string; edited?: { source: string } }[], keep?: ReadonlySet<string>) {
  for (const { url, edited } of attachments) {
    if (url && !keep?.has(url)) URL.revokeObjectURL(url);
    const source = edited?.source;
    if (source?.startsWith("blob:") && source !== url && !keep?.has(source)) URL.revokeObjectURL(source);
  }
}
