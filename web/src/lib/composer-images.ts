import { useEffect, useSyncExternalStore } from "react";
import { credentialOwner, onCredentialChange, subscribeCredentialOwner } from "./api";
import { base64File, releaseAttachments } from "./attachments";
import { hasImageDraft, imageDraftMarkers, imageDraftOwner } from "./composer-images-index";
import { conversationDeletionScope, conversationKeyDeleted, onConversationDeleted } from "./conversation-deleted";
import type { DraftImage } from "./drafts";
import { uuid } from "./format";
import type { EditedImage } from "./image-edit";

export interface Attachment extends DraftImage {
  id: string;
  url: string;
  edited?: EditedImage;
  /** A picked file can have a full-size original and a smaller send payload. */
  original?: Blob;
}

export function fromDraftImage(image: DraftImage, id = uuid(), edited?: EditedImage, original?: Blob): Attachment {
  const preview = (!edited && original) || base64File(image.name, image.mime, image.data);
  return { ...image, id, url: URL.createObjectURL(preview), edited, original };
}

const EMPTY: Attachment[] = [];
interface ImageDraft {
  scope: string;
  generation: number;
  images: Attachment[];
}
const drafts = new Map<string, ImageDraft>();
const listeners = new Set<() => void>();
const sending = new Set<{ key: string; images: Attachment[] }>();
const scope = () => `${credentialOwner()}/${conversationDeletionScope()}`;
const emit = () => listeners.forEach((listener) => listener());
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

export function getComposerImages(key: string): Attachment[] {
  const draft = drafts.get(key);
  return draft && draft.scope === scope() && !conversationKeyDeleted(key) ? draft.images : EMPTY;
}

let disk: Promise<typeof import("./composer-images-idb")> | undefined;
const loadDisk = () => disk ??= import("./composer-images-idb").catch((error) => {
  disk = undefined;
  throw error;
});

let generation = 0;

function putDraft(key: string, images: Attachment[]): ImageDraft {
  const draft = { scope: scope(), generation: ++generation, images };
  drafts.set(key, draft);
  return draft;
}

// A later mutation supersedes both reads and writes, even if the draft is empty again by the time the read returns.
async function withDisk(key: string, draft: ImageDraft, work: (store: Awaited<ReturnType<typeof loadDisk>>, current: () => boolean) => Promise<void>) {
  const current = () => drafts.get(key)?.generation === draft.generation && scope() === draft.scope;
  try {
    const store = await loadDisk();
    if (current()) await work(store, current);
  } catch {
    // Storage is optional. The live draft is still usable.
  } finally {
    if (!draft.images.length && drafts.get(key) === draft) drafts.delete(key);
  }
}

export function hydrateComposerImages(key: string) {
  if (drafts.has(key) || conversationKeyDeleted(key) || !hasImageDraft(key)) return;
  const draft = putDraft(key, EMPTY);
  return withDisk(key, draft, async (store, current) => {
    const images = await store.readImageDraft(key, current);
    if (!images.length) return;
    if (!current()) { releaseAttachments(images); return; }
    draft.images = images;
    emit();
  });
}

export function useComposerImages(key: string | null): Attachment[] {
  const images = useSyncExternalStore(subscribe, () => key === null ? EMPTY : getComposerImages(key), () => EMPTY);
  useEffect(() => { if (key !== null) void hydrateComposerImages(key); }, [key]);
  return images;
}

function put(key: string, images: Attachment[]) {
  if (!images.length && !drafts.has(key) && !hasImageDraft(key)) return;
  const draft = putDraft(key, images);
  void withDisk(key, draft, (store, current) => store.writeImageDraft(key, images, current));
  emit();
}

export function setComposerImages(key: string, images: Attachment[]) {
  const previous = drafts.get(key)?.images ?? EMPTY;
  if (conversationKeyDeleted(key)) {
    releaseAttachments(images.filter((image) => !previous.includes(image)));
    images = EMPTY;
  }
  releaseAttachments(previous, new Set(images.flatMap(({ url, edited }) => edited ? [url, edited.source] : [url])));
  put(key, images);
}

export function moveComposerImages(from: string, to: string) {
  const images = getComposerImages(from);
  if (!images.length || from === to || conversationKeyDeleted(to)) return;
  put(from, EMPTY);
  put(to, [...images, ...getComposerImages(to)]);
}

/** Remove the sent snapshot optimistically. Keep its URLs alive until delivery settles or its owner is cleared. */
export function takeComposerImages(key: string, sent: Attachment[]): (restore: boolean) => void {
  const owner = scope();
  const live = getComposerImages(key);
  const send = { key, images: live.filter((image) => sent.includes(image)) };
  if (send.images.length) sending.add(send);
  put(key, live.filter((image) => !sent.includes(image)));
  return (restore) => {
    if (!sending.delete(send)) return;
    if (restore && scope() === owner && !conversationKeyDeleted(key)) put(key, [...send.images, ...getComposerImages(key)]);
    else releaseAttachments(send.images);
  };
}

onConversationDeleted((mid, sid) => {
  const key = `${mid}/${sid}`;
  setComposerImages(key, EMPTY);
  for (const send of sending) {
    if (send.key !== key) continue;
    sending.delete(send);
    releaseAttachments(send.images);
  }
});

function clearOwner() {
  const changed = drafts.size > 0;
  for (const draft of drafts.values()) releaseAttachments(draft.images);
  for (const send of sending) releaseAttachments(send.images);
  drafts.clear();
  sending.clear();
  if (disk || imageDraftMarkers().length) {
    const owner = imageDraftOwner();
    void loadDisk().then((store) => store.removeOtherImageOwners(owner)).catch(() => {});
  }
  if (changed) emit();
}

if (typeof window === "undefined") onCredentialChange(clearOwner);
else subscribeCredentialOwner(clearOwner);
