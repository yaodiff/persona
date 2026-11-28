// Optional disk backing for image drafts. Only the composer image store imports this module, on demand.
import { base64File, releaseAttachments } from "./attachments";
import { imageDraftMarker, imageDraftMarkers, imageDraftOwner, imageDraftPrefix } from "./composer-images-index";
import type { Attachment } from "./composer-images";
import type { DraftImage } from "./drafts";
import type { EditState } from "./image-edit";

interface StoredImage extends DraftImage {
  id: string;
  original?: Blob;
  edited?: EditState & { source: Blob };
}

interface ImageDraft {
  owner: string;
  draftKey: string;
  updatedAt: number;
  bytes: number;
  images: StoredImage[];
}

const STORE = "drafts";
const BY_AGE = "byAge";
const MAX_BYTES = 50 * 1024 * 1024;
const MAX_DRAFTS = 50;
let connection: Promise<IDBDatabase> | undefined;

function database(): Promise<IDBDatabase> {
  return connection ??= new Promise<IDBDatabase>((resolve, reject) => {
    let blocked = false;
    const request = indexedDB.open(imageDraftPrefix + "db", 1);
    request.onupgradeneeded = () => {
      const store = request.result.createObjectStore(STORE, { keyPath: ["owner", "draftKey"] });
      store.createIndex(BY_AGE, ["updatedAt", "bytes"]);
    };
    request.onblocked = () => { blocked = true; reject(new Error("Image draft database is blocked")); };
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      if (blocked) { db.close(); return; }
      const close = () => { db.close(); connection = undefined; };
      db.onversionchange = close;
      db.onclose = close;
      resolve(db);
    };
  }).catch((error) => {
    connection = undefined;
    throw error;
  });
}

function transact(db: IDBDatabase, mode: IDBTransactionMode, work: (store: IDBObjectStore) => void): Promise<void> {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, mode);
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error);
    try { work(tx.objectStore(STORE)); }
    catch (error) { tx.abort(); reject(error); }
  });
}

function forget(key: string, owner: string) {
  try {
    const marker = imageDraftMarker(key);
    if (localStorage.getItem(marker) === owner) localStorage.removeItem(marker);
  } catch {
    // The database deletion can still go through.
  }
}

async function sourceBlob(source: string): Promise<Blob> {
  const response = await fetch(source);
  if (!response.ok) throw new Error("Image draft source is unavailable");
  return response.blob();
}

async function serialize({ id, name, mime, data, original, edited }: Attachment): Promise<StoredImage> {
  if (!edited) return { id, name, mime, data, original };
  const { source, edits, history } = edited;
  const bytes = original ?? await sourceBlob(source);
  return { id, name, mime, data, edited: { source: bytes, edits, history } };
}

function approximateBytes({ id, name, mime, data, original, edited }: StoredImage): number {
  const metadata = edited ? JSON.stringify({ edits: edited.edits, history: edited.history }).length : 0;
  return 2 * (id.length + name.length + mime.length + data.length + metadata) + (edited?.source.size ?? original?.size ?? 0);
}

function restore(images: StoredImage[]): Attachment[] {
  const result: Attachment[] = [];
  try {
    for (const { edited, original, ...image } of images) {
      const preview = original ?? base64File(image.name, image.mime, image.data);
      const attachment: Attachment = { ...image, url: URL.createObjectURL(preview), original: edited?.source ?? original };
      result.push(attachment);
      if (edited) attachment.edited = { ...edited, source: URL.createObjectURL(edited.source) };
    }
    return result;
  } catch (error) {
    releaseAttachments(result);
    throw error;
  }
}

export async function readImageDraft(key: string, current: () => boolean): Promise<Attachment[]> {
  const owner = imageDraftOwner();
  if (!owner) return [];
  const indexed = () => localStorage.getItem(imageDraftMarker(key)) === owner;
  if (!indexed()) return [];
  const db = await database();
  if (!current()) return [];
  let draft: ImageDraft | undefined;
  await transact(db, "readonly", (store) => {
    const request = store.get([owner, key]);
    request.onsuccess = () => { draft = request.result; };
  });
  if (!current() || !indexed()) return [];
  if (!draft) { forget(key, owner); return []; }
  return restore(draft.images);
}

async function removeDraft(owner: string, key: string, current: () => boolean) {
  forget(key, owner);
  const db = await database();
  if (current()) await transact(db, "readwrite", (store) => { store.delete([owner, key]); });
}

// The index carries sizes so bounding the store never copies other drafts' image payloads into memory.
async function writeBounded(db: IDBDatabase, draft: ImageDraft) {
  const evicted: [owner: string, key: string][] = [];
  await transact(db, "readwrite", (store) => {
    const older: { key: [string, string]; bytes: number }[] = [];
    let bytes = draft.bytes;
    const request = store.index(BY_AGE).openKeyCursor();
    request.onsuccess = () => {
      const cursor = request.result;
      if (cursor) {
        const key = cursor.primaryKey as [string, string];
        if (key[0] !== draft.owner || key[1] !== draft.draftKey) {
          const size = (cursor.key as [number, number])[1];
          older.push({ key, bytes: size });
          bytes += size;
        }
        cursor.continue();
        return;
      }
      try {
        let i = 0;
        while (bytes > MAX_BYTES || older.length - i >= MAX_DRAFTS) {
          const victim = older[i++];
          store.delete(victim.key);
          evicted.push(victim.key);
          bytes -= victim.bytes;
        }
        store.put(draft);
      } catch {
        store.transaction.abort();
      }
    };
  });
  for (const [owner, key] of evicted) forget(key, owner);
}

// A transaction may be in flight before it can publish a presence marker. Account cleanup also fences those writes.
const writtenOwners = new Set<string>();

export async function writeImageDraft(key: string, attachments: Attachment[], current: () => boolean) {
  const owner = imageDraftOwner();
  if (!owner) return;
  if (!attachments.length) return removeDraft(owner, key, current);
  try {
    const images = await Promise.all(attachments.map(serialize));
    if (!current()) return;
    const bytes = images.reduce((sum, image) => sum + approximateBytes(image), 0);
    if (bytes > MAX_BYTES) return removeDraft(owner, key, current);
    const db = await database();
    if (!current()) return;
    writtenOwners.add(owner);
    await writeBounded(db, { owner, draftKey: key, updatedAt: Date.now(), bytes, images });
    if (current()) localStorage.setItem(imageDraftMarker(key), owner);
  } catch {
    // A quota error, blocked database, or an unavailable source leaves the live copy intact.
    // Forget an older disk snapshot so it cannot reappear after the failed update.
    if (current()) await removeDraft(owner, key, current);
  }
}

export async function removeOtherImageOwners(keep: string | null) {
  const retired = new Set(writtenOwners);
  for (const marker of imageDraftMarkers()) {
    try {
      const owner = localStorage.getItem(marker);
      if (owner !== null && owner !== keep) {
        retired.add(owner);
        localStorage.removeItem(marker);
      }
    } catch {
      // A blocked presence index is never used to hydrate.
    }
  }
  if (keep !== null) retired.delete(keep);
  if (!retired.size) return;
  const db = await database();
  await transact(db, "readwrite", (store) => {
    const request = store.getAllKeys();
    request.onsuccess = () => {
      for (const key of request.result as [string, string][]) {
        if (retired.has(key[0])) store.delete(key);
      }
    };
  });
  for (const owner of retired) writtenOwners.delete(owner);
}
