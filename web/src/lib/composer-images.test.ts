// @vitest-environment jsdom
import { Blob as NativeBlob, File as NativeFile } from "node:buffer";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Attachment } from "./composer-images";
import { hasImageDraft, imageDraftPrefix } from "./composer-images-index";
import { noEdits, type EditState } from "./image-edit";

const key = "hub/chat";
const other = "hub/other";
const edits = { ...noEdits(), turns: 1 };
const state: EditState = { edits, history: { past: [noEdits()], future: [{ ...edits, turns: 2 }] } };
let images: typeof import("./composer-images");
let api: typeof import("./api");
let deletions: typeof import("./conversation-deleted");
let factory: IDBFactory;
let urls: Map<string, Blob>;
let serial: number;
const revoke = vi.fn((url: string) => urls.delete(url));

async function reload() {
  vi.resetModules();
  images = await import("./composer-images");
  api = await import("./api");
  deletions = await import("./conversation-deleted");
}

beforeEach(async () => {
  localStorage.clear();
  factory = new IDBFactory();
  urls = new Map();
  serial = 0;
  revoke.mockClear();
  vi.stubGlobal("indexedDB", factory);
  // Node's blobs support structured clone, like browser blobs. jsdom's do not.
  vi.stubGlobal("Blob", NativeBlob);
  vi.stubGlobal("File", NativeFile);
  URL.createObjectURL = (blob) => {
    const url = `blob:draft/${++serial}`;
    if (blob instanceof Blob) urls.set(url, blob);
    return url;
  };
  URL.revokeObjectURL = revoke;
  await reload();
  api.setToken("draft-owner");
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function picture(name = "draft.png", data = "pixels", edited?: Attachment["edited"], original?: Blob) {
  return images.fromDraftImage({ name, mime: "image/png", data: btoa(data) }, undefined, edited, original);
}

async function saved(draftKey = key) {
  await vi.waitFor(() => expect(hasImageDraft(draftKey)).toBe(true));
}

async function records(): Promise<{ draftKey: string; images: { data: string; edited?: { source: Blob } }[] }[]> {
  return new Promise((resolve, reject) => {
    const open = factory.open(imageDraftPrefix + "db");
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const db = open.result;
      const request = db.transaction("drafts").objectStore("drafts").getAll();
      request.onerror = () => { db.close(); reject(request.error); };
      request.onsuccess = () => { db.close(); resolve(request.result); };
    };
  });
}

function pause() {
  let resume!: () => void;
  const ready = new Promise<void>((resolve) => { resume = resolve; });
  return { ready, resume };
}

describe("composer image draft persistence", () => {
  it("does no IndexedDB work when opening a chat with no saved images", async () => {
    const open = vi.spyOn(factory, "open");
    await images.hydrateComposerImages(key);
    expect(images.getComposerImages(key)).toEqual([]);
    expect(open).not.toHaveBeenCalled();
  });

  it("round-trips the preview, original bytes, edits, and undo and redo history after reload", async () => {
    const source = picture("before.png", "unedited pixels");
    const fetchBlob = vi.fn(async (url: string) => new Response(urls.get(url)));
    vi.stubGlobal("fetch", fetchBlob);
    const edited = picture("cropped.png", "cropped pixels", { source: source.url, ...state });
    const original = new Blob(["full-size pixels"], { type: "image/png" });
    const picked = picture("picked.png", "resized pixels", undefined, original);
    images.setComposerImages(key, [edited, picked]);
    await saved();
    expect(fetchBlob).toHaveBeenCalledExactlyOnceWith(source.url);
    const [stored] = await records();
    expect(JSON.stringify(stored)).not.toContain("blob:");
    expect(JSON.stringify(stored)).not.toContain("draft-owner");
    expect(await stored.images[0].edited?.source.text()).toBe("unedited pixels");

    await reload();
    expect(images.getComposerImages(key)).toEqual([]);
    await images.hydrateComposerImages(other);
    await images.hydrateComposerImages(key);
    expect(images.getComposerImages(other)).toEqual([]);
    const [restored, restoredPick] = images.getComposerImages(key);
    expect(restored).toMatchObject({ id: edited.id, name: edited.name, data: edited.data, edited: state });
    expect(restored.url).not.toBe(edited.url);
    expect(restored.edited?.source).not.toBe(source.url);
    expect(await urls.get(restored.url)?.text()).toBe("cropped pixels");
    expect(await urls.get(restored.edited!.source)?.text()).toBe("unedited pixels");
    expect(await urls.get(restoredPick.url)?.text()).toBe("full-size pixels");
    expect(restoredPick.data).toBe(picked.data);
  });

  it.each(["remove", "send"] as const)("deletes the saved record after %s and releases the preview", async (action) => {
    const draft = picture();
    images.setComposerImages(key, [draft]);
    await saved();
    if (action === "remove") images.setComposerImages(key, []);
    else images.takeComposerImages(key, [draft])(false);

    expect(revoke).toHaveBeenCalledExactlyOnceWith(draft.url);
    expect(images.getComposerImages(key)).toEqual([]);
    await vi.waitFor(async () => expect(await records()).toEqual([]));
    expect(hasImageDraft(key)).toBe(false);
    await reload();
    await images.hydrateComposerImages(key);
    expect(images.getComposerImages(key)).toEqual([]);
  });

  it("deletes a conversation's saved images even if its composer has never mounted since reload", async () => {
    images.setComposerImages(key, [picture()]);
    images.setComposerImages(other, [picture()]);
    await Promise.all([saved(), saved(other)]);
    await reload();
    deletions.rememberConversationDeleted("hub", "chat");
    await vi.waitFor(async () => expect((await records()).map((draft) => draft.draftKey)).toEqual([other]));
    expect(hasImageDraft(key)).toBe(false);
    expect(hasImageDraft(other)).toBe(true);
  });

  it("retires stored images before the replacement account hydrates a draft with the same key", async () => {
    images.setComposerImages(key, [picture("previous.png")]);
    await saved();
    await reload();
    api.setToken("replacement");
    await images.hydrateComposerImages(key);
    expect(images.getComposerImages(key)).toEqual([]);
    await vi.waitFor(async () => expect(await records()).toEqual([]));
    expect(hasImageDraft(key)).toBe(false);
    api.setToken("draft-owner");
    await images.hydrateComposerImages(key);
    expect(images.getComposerImages(key)).toEqual([]);
  });

  it("clears live and in-flight images on a new sign-in with the same credential", async () => {
    const inFlight = picture();
    const visible = picture("visible.png");
    images.setComposerImages(key, [inFlight]);
    await saved();
    const settle = images.takeComposerImages(key, [inFlight]);
    images.setComposerImages(other, [visible]);
    await saved(other);
    api.setPairedToken("draft-owner");
    settle(true);
    expect(images.getComposerImages(key)).toEqual([]);
    expect(images.getComposerImages(other)).toEqual([]);
    expect(revoke.mock.calls.flat()).toEqual(expect.arrayContaining([inFlight.url, visible.url]));
    await vi.waitFor(async () => expect(await records()).toEqual([]));
  });

  it.each(["add", "clear", "send", "delete", "credential", "scope"] as const)(
    "discards a hydration result when %s supersedes it, including URLs already allocated",
    async (action) => {
      images.setComposerImages(key, [picture()]);
      await saved();
      await reload();
      const disk = await import("./composer-images-idb");
      const read = disk.readImageDraft;
      const allocated = pause(), finish = pause();
      let hydrated: Attachment[] = [];
      vi.spyOn(disk, "readImageDraft").mockImplementationOnce(async (...args) => {
        hydrated = await read(...args);
        allocated.resume();
        await finish.ready;
        return hydrated;
      });
      const loading = images.hydrateComposerImages(key);
      await allocated.ready;
      const fresh = action === "add" ? [picture("new.png")] : [];
      switch (action) {
        case "add": case "clear": images.setComposerImages(key, fresh); break;
        case "send": images.takeComposerImages(key, [])(false); break;
        case "delete": deletions.rememberConversationDeleted("hub", "chat"); break;
        case "credential": api.setToken("replacement"); break;
        case "scope": deletions.clearConversationDeletions(); break;
      }
      finish.resume();
      await loading;
      expect(hydrated).toHaveLength(1);
      expect(revoke).toHaveBeenCalledWith(hydrated[0].url);
      expect(images.getComposerImages(key)).toEqual(fresh);
      if (action === "add") {
        await vi.waitFor(async () => expect((await records())[0].images[0].data).toBe(fresh[0].data));
      } else if (action !== "scope") {
        await vi.waitFor(async () => expect(await records()).toEqual([]));
      }
    },
  );

  it("evicts the oldest saved draft when image bytes exceed the budget and keeps the live copy", async () => {
    const blob = new Blob([new Uint8Array(26 * 1024 * 1024)], { type: "image/png" });
    const first = picture("old.png", "old", undefined, blob);
    images.setComposerImages(key, [first]);
    await saved();
    images.setComposerImages(other, [picture("new.png", "new", undefined, blob)]);
    await saved(other);
    expect((await records()).map((draft) => draft.draftKey)).toEqual([other]);
    expect(hasImageDraft(key)).toBe(false);
    expect(images.getComposerImages(key)).toEqual([first]);
    expect(revoke).not.toHaveBeenCalled();
  });

  it("keeps a newer write when an earlier edit is still fetching its original", async () => {
    const disk = await import("./composer-images-idb");
    const write = disk.writeImageDraft;
    const fetching = pause(), finishFetch = pause(), written = pause();
    vi.spyOn(disk, "writeImageDraft").mockImplementationOnce(async (...args) => {
      await write(...args);
      written.resume();
    });
    vi.stubGlobal("fetch", vi.fn(async () => {
      fetching.resume();
      await finishFetch.ready;
      return new Response(new Blob(["original"]));
    }));
    images.setComposerImages(key, [picture("edited.png", "old", { source: picture().url, ...state })]);
    await fetching.ready;
    const next = picture("new.png", "new");
    images.setComposerImages(key, [next]);
    await saved();
    finishFetch.resume();
    await written.ready;
    expect((await records())[0].images[0].data).toBe(next.data);
    expect(images.getComposerImages(key)).toEqual([next]);
  });

  it("keeps the latest images in memory when a write exceeds quota", async () => {
    images.setComposerImages(key, [picture("old.png")]);
    await saved();
    const put = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementationOnce(() => {
      throw new DOMException("Quota exceeded", "QuotaExceededError");
    });
    const next = picture("new.png");
    images.setComposerImages(key, [next]);
    await vi.waitFor(async () => expect(await records()).toEqual([]));
    expect(put).toHaveBeenCalledOnce();
    expect(hasImageDraft(key)).toBe(false);
    expect(images.getComposerImages(key)).toEqual([next]);
  });

  it("keeps an image in memory even when IndexedDB access is unavailable", async () => {
    const open = vi.spyOn(factory, "open").mockImplementation(() => {
      throw new DOMException("Storage unavailable", "SecurityError");
    });
    const draft = picture();
    images.setComposerImages(key, [draft]);
    await vi.waitFor(() => expect(open).toHaveBeenCalled());
    expect(images.getComposerImages(key)).toEqual([draft]);
    expect(hasImageDraft(key)).toBe(false);
    vi.stubGlobal("indexedDB", undefined);
    const next = picture("new.png");
    images.setComposerImages(key, [next]);
    await vi.dynamicImportSettled();
    expect(images.getComposerImages(key)).toEqual([next]);
  });
});
