// Making an avatar from an image (#2404, docs/apple/persona.md "Uploading an image"): the user picks an image, crops it
// square (with the circle the avatar shows), names it, and the hub makes a package of it (POST /looks/uploads), which
// it then chooses. Uploaded looks can be deleted again (DELETE /looks/uploads/{id}). The crop's geometry, the export's
// size and encoding and the requests are here; the dialog is views/assistant/LookUpload.tsx.
import { ApiError, CLIENT_PLATFORM, CLIENT_VERSION, HubUnreachableError, api, enc, errorText, getToken } from "./api";
import { BASE } from "./base";
import { CLIENT_HEADERS, compatibleEnvelope } from "./compatibility";
import type { LookCurrent, LookView } from "./persona";
import { currentLanguage } from "../i18n";

/** hello / health `features`: the hub makes avatars from uploaded images. Without it the picker offers no upload. */
export const LOOK_UPLOADS_FEATURE = "look_uploads";
/** hello / health `features`: per-state pictures, animated uploads, theme color and PATCH editing (#15449). */
export const LOOK_UPLOADS_STATES_FEATURE = "look_uploads_states";

/** What the file picker takes: the formats the hub reads (the first frame of a GIF). */
export const UPLOAD_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"] as const;
/** The hub's bounds (server/internal/hub/look_uploads.go): name and description length, the least side it takes. */
export const NAME_MAX = 40;
export const SUBTITLE_MAX = 80;
export const MIN_SIDE = 64;
/** The crop is sent at most this big: the largest size the hub keeps is 512. */
export const EXPORT_MAX = 1024;
/** A PNG past this goes as a JPEG instead (the hub takes 8 MB; base64 and the rest of the body on top). */
export const PNG_LIMIT = 6 * 1024 * 1024;
export const JPEG_QUALITY = 0.92;
/** How far the slider zooms in at most, before the crop would get under MIN_SIDE. */
export const ZOOM_MAX = 8;

/** An image's size in pixels, upright. */
export interface Size {
  w: number;
  h: number;
}

/** The square crop: its center in image pixels and the zoom (1: the square spans the image's short side). */
export interface Crop {
  x: number;
  y: number;
  zoom: number;
}

const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), hi);

/** Whether the picked file is one of the formats the hub reads (by its type; an empty type is left to the decoder). */
export function acceptsType(type: string): boolean {
  return !type || (UPLOAD_TYPES as readonly string[]).includes(type.toLowerCase());
}

/** Whether an image is big enough to make an avatar of. */
export function bigEnough(img: Size): boolean {
  return Math.min(img.w, img.h) >= MIN_SIDE;
}

/** The crop's side in image pixels at `zoom`. */
export function cropSide(img: Size, zoom: number): number {
  return Math.min(img.w, img.h) / zoom;
}

/** The most the crop can zoom in: ZOOM_MAX, or less where the square would get smaller than MIN_SIDE pixels. */
export function maxZoom(img: Size): number {
  return Math.max(1, Math.min(ZOOM_MAX, Math.min(img.w, img.h) / MIN_SIDE));
}

/** The largest centered square: where a crop starts. */
export function centeredCrop(img: Size): Crop {
  return { x: img.w / 2, y: img.h / 2, zoom: 1 };
}

/** The crop kept inside the image, its zoom between 1 and maxZoom. */
export function clampCrop(img: Size, c: Crop): Crop {
  const zoom = clamp(c.zoom, 1, maxZoom(img));
  const half = cropSide(img, zoom) / 2;
  return { zoom, x: clamp(c.x, half, img.w - half), y: clamp(c.y, half, img.h - half) };
}

/** The crop after the image is dragged (dx, dy) CSS pixels in a square viewport `view` pixels wide. */
export function panCrop(img: Size, c: Crop, dx: number, dy: number, view: number): Crop {
  const k = view / cropSide(img, c.zoom); // CSS pixels per image pixel
  return clampCrop(img, { ...c, x: c.x - dx / k, y: c.y - dy / k });
}

/** The crop at `zoom`, keeping the image point under (px, py) of the viewport (default: its center) where it is. */
export function zoomCrop(img: Size, c: Crop, zoom: number, view: number, px = view / 2, py = view / 2): Crop {
  const before = cropSide(img, c.zoom);
  const z = clamp(zoom, 1, maxZoom(img));
  const after = cropSide(img, z);
  // the image point under (px, py), and where the square's corner must go so it stays there
  const ux = c.x - before / 2 + (px * before) / view;
  const uy = c.y - before / 2 + (py * before) / view;
  return clampCrop(img, { zoom: z, x: ux - (px * after) / view + after / 2, y: uy - (py * after) / view + after / 2 });
}

/** What the export (and the crop box on screen) draws: the square in image pixels (sx, sy, side) and the side of the image it makes (never larger
 *  than the crop, never over `maxSide`). */
export function exportRect(img: Size, c: Crop, maxSide = EXPORT_MAX): { sx: number; sy: number; side: number; out: number } {
  const side = cropSide(img, c.zoom);
  return { sx: c.x - side / 2, sy: c.y - side / 2, side, out: Math.max(1, Math.min(maxSide, Math.round(side))) };
}

/** A picked image, decoded upright (its EXIF orientation applied; a GIF's first frame), and how to let it go. */
export interface PickedImage {
  source: CanvasImageSource;
  size: Size;
  release: () => void;
}

/** Decodes a picked file. Rejects when the browser can't read it as an image. */
export async function readImage(file: Blob): Promise<PickedImage> {
  if (typeof createImageBitmap === "function") {
    try {
      const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
      return { source: bitmap, size: { w: bitmap.width, h: bitmap.height }, release: () => bitmap.close() };
    } catch {
      // an older browser without the option, or a format only <img> reads: tried below
    }
  }
  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    img.src = url;
    await img.decode();
    return { source: img, size: { w: img.naturalWidth, h: img.naturalHeight }, release: () => URL.revokeObjectURL(url) };
  } catch (e) {
    URL.revokeObjectURL(url);
    throw e;
  }
}

/** The bytes a base64 text (or a data: URL) stands for. */
export function base64Bytes(data: string): number {
  const b64 = data.startsWith("data:") ? data.slice(data.indexOf(",") + 1) : data;
  const pad = b64.endsWith("==") ? 2 : b64.endsWith("=") ? 1 : 0;
  return Math.floor((b64.length * 3) / 4) - pad;
}

/** A PNG keeps edges and transparency; one past PNG_LIMIT goes as a JPEG (on white: JPEG has no transparency). */
export function encodingFor(pngBytes: number): "png" | "jpeg" {
  return pngBytes > PNG_LIMIT ? "jpeg" : "png";
}

/** The base64 of a data: URL. */
function payload(dataURL: string): string {
  return dataURL.slice(dataURL.indexOf(",") + 1);
}

/** The crop drawn into a square canvas at exportRect's size: base64 of a PNG, or of a JPEG when the PNG is too big. */
export function exportCrop(source: CanvasImageSource, img: Size, c: Crop, doc: Document = document, maxSide = EXPORT_MAX): { image: string; mime: string; side: number } {
  const r = exportRect(img, clampCrop(img, c), maxSide);
  const canvas = doc.createElement("canvas");
  canvas.width = r.out;
  canvas.height = r.out;
  const g = canvas.getContext("2d");
  if (!g) throw new Error("canvas");
  g.imageSmoothingEnabled = true;
  g.imageSmoothingQuality = "high";
  g.drawImage(source, r.sx, r.sy, r.side, r.side, 0, 0, r.out, r.out);
  const png = canvas.toDataURL("image/png");
  if (encodingFor(base64Bytes(png)) === "png") return { image: payload(png), mime: "image/png", side: r.out };
  // what was transparent would turn black: white goes under it (image data, not a page color)
  g.globalCompositeOperation = "destination-over";
  g.fillStyle = "#ffffff";
  g.fillRect(0, 0, r.out, r.out);
  return { image: payload(canvas.toDataURL("image/jpeg", JPEG_QUALITY)), mime: "image/jpeg", side: r.out };
}

/** POST /looks/uploads's body: the name and description trimmed, an empty description left out, and the new look chosen. */
export interface UploadBody {
  name: string;
  subtitle?: string;
  image: string;
  choose: true;
}

export function uploadBody(name: string, subtitle: string, image: string): UploadBody {
  const sub = subtitle.trim();
  return { name: name.trim(), ...(sub ? { subtitle: sub } : {}), image, choose: true };
}

/** How long a text is the way the hub counts it (characters, not UTF-16 units). */
export function textLength(s: string): number {
  return [...s].length;
}

/** Whether the form can be sent: a name of 1 to NAME_MAX characters and a description of at most SUBTITLE_MAX. */
export function canSend(name: string, subtitle: string): boolean {
  const n = textLength(name.trim());
  return n > 0 && n <= NAME_MAX && textLength(subtitle.trim()) <= SUBTITLE_MAX;
}

/** What to tell the user about a failed request: the hub's own words (already in their language) as they are. */
export function refusalText(e: unknown): string {
  if (e instanceof ApiError && e.isJson && typeof e.body.error === "string" && e.body.error.trim()) return e.body.error;
  return errorText(e);
}

/** POST /looks/uploads's answer: the new look as GET /looks lists it, and the choice (asked for with choose). */
export interface UploadAnswer {
  look: LookView;
  current?: LookCurrent;
}

/** Sends a JSON body and reports the bytes sent (fetch cannot). Rejects with an ApiError carrying the hub's code and
 *  words, a HubUnreachableError (a TypeError) when the hub can't be reached, or an AbortError. */
export type ProgressPost = <T>(path: string, json: unknown, opts: { onProgress?: (sent: number, total: number) => void; signal?: AbortSignal; method?: "POST" | "PATCH" }) => Promise<T>;

export const xhrPost: ProgressPost = <T,>(path: string, json: unknown, opts: { onProgress?: (sent: number, total: number) => void; signal?: AbortSignal; method?: "POST" | "PATCH" }) =>
  new Promise<T>((resolve, reject) => {
    const body = JSON.stringify(json);
    const xhr = new XMLHttpRequest();
    let settled = false;
    const finish = (done: () => void) => {
      if (settled) return;
      settled = true;
      opts.signal?.removeEventListener("abort", cancel);
      done();
    };
    const cancel = () => {
      finish(() => reject(new DOMException("aborted", "AbortError")));
      xhr.abort();
    };
    if (opts.signal?.aborted) return reject(new DOMException("aborted", "AbortError"));
    xhr.open(opts.method ?? "POST", `${BASE}/api/v1${path}`);
    xhr.setRequestHeader("Authorization", `Bearer ${getToken()}`);
    // the hub words its refusals in the interface language (docs/protocol/i18n.md "Protocol")
    xhr.setRequestHeader("Accept-Language", currentLanguage());
    CLIENT_HEADERS.forEach((name) => xhr.setRequestHeader(name, `${CLIENT_PLATFORM}/${CLIENT_VERSION}`));
    xhr.setRequestHeader("Content-Type", "application/json");
    xhr.upload.onprogress = (e) => {
      if (!settled) opts.onProgress?.(e.loaded, e.lengthComputable ? e.total : body.length);
    };
    xhr.onload = () => {
      let data: Record<string, unknown> = {};
      let isJson = false;
      try {
        const parsed = xhr.responseText ? JSON.parse(xhr.responseText) : {};
        isJson = typeof parsed === "object" && parsed !== null && !Array.isArray(parsed);
        if (isJson) data = parsed;
      } catch {
        // a proxy's HTML page says nothing a person can use: the status stands in for it
        data = { error: /^\s*</.test(xhr.responseText) ? "" : xhr.responseText };
      }
      if (xhr.status >= 200 && xhr.status < 300) return finish(() => resolve(compatibleEnvelope(data) as T));
      const msg = typeof data.error === "string" && data.error ? data.error : `HTTP ${xhr.status}`;
      finish(() => reject(new ApiError(xhr.status, msg, data, isJson, xhr.getResponseHeader("X-Request-Id") ?? undefined)));
    };
    // the connection failed: "Can't reach the hub", not a bug in the page (lib/network-failure.ts)
    xhr.onerror = () => finish(() => reject(new HubUnreachableError("network")));
    xhr.onabort = () => finish(() => reject(new DOMException("aborted", "AbortError")));
    opts.signal?.addEventListener("abort", cancel, { once: true });
    xhr.send(body);
  });

/** The choice as the hub answered it, with its built-in ids the way this page names them. */
function currentOf(cur: LookCurrent | undefined): LookCurrent | undefined {
  return cur && typeof cur === "object" && typeof cur.effective === "string" ? compatibleEnvelope(cur) : undefined;
}

/** Makes a look of the cropped image and chooses it. */
export async function uploadLook(
  body: UploadBody,
  opts: { onProgress?: (sent: number, total: number) => void; signal?: AbortSignal } = {},
  post: ProgressPost = xhrPost,
): Promise<UploadAnswer> {
  const r = await post<UploadAnswer>("/looks/uploads", body, opts);
  return { look: r.look, current: currentOf(r.current) };
}

/** Deletes an uploaded look; the answer is the choice after it (the default when it was the chosen one). */
export async function deleteUploadedLook(id: string): Promise<LookCurrent | undefined> {
  const r = await api<{ current?: LookCurrent }>(`/looks/uploads/${enc(id)}`, { method: "DELETE" });
  return currentOf(r.current);
}
