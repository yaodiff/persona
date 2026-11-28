// @vitest-environment jsdom
// Downloads use an independent fetch after the displayed image loads. Exercise the real viewer and toast UI.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi, type MockInstance } from "vitest";
import { t } from "../../i18n";
import { Toasts } from "../../ui/atoms";
import { ChatImageViewer, ImageViewer } from "./ImageViewer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const source = "/api/v1/machines/computer/file/download?path=%2Fsynthetic%2Fsample.png&inline=1&token=synthetic-token";
const png = Uint8Array.from(atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jB0cAAAAASUVORK5CYII="), (c) => c.charCodeAt(0));
const createURL = vi.fn<(blob: Blob | MediaSource) => string>(() => "blob:synthetic-download");
const revokeURL = vi.fn();
let root: Root;
let host: HTMLDivElement;
let saves: { name: string; href: string }[];
let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>;
let makeElement: MockInstance<typeof document.createElement>;
let clock = Date.UTC(2026, 10, 8, 14, 35);
const nativeFetch = globalThis.fetch;
const originalCreateURL = URL.createObjectURL;
const originalRevokeURL = URL.revokeObjectURL;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const downloadButton = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("chat-imageviewer.download"))!;
const anchorsMade = () => makeElement.mock.calls.filter(([tag]) => tag === "a").length;
// Read the original bytes from either native fetch or jsdom Blobs.
const blobBytes = async (blob: Blob): Promise<Uint8Array> => {
  if (typeof blob.arrayBuffer === "function") return new Uint8Array(await blob.arrayBuffer());
  return new Promise<Uint8Array>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
    reader.onerror = () => reject(reader.error);
    reader.readAsArrayBuffer(blob);
  });
};

beforeEach(() => {
  // Keep FileReader's native task scheduling; only UI timeout cleanup and the toast clock are simulated.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  clock += 10_000;
  vi.setSystemTime(clock);
  vi.stubGlobal("matchMedia", (media: string) => ({
    matches: media.includes("prefers-reduced-motion"), media, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  createURL.mockClear(); revokeURL.mockClear();
  URL.createObjectURL = createURL;
  URL.revokeObjectURL = revokeURL;
  saves = [];
  fetchMock = vi.fn<typeof fetch>();
  vi.stubGlobal("fetch", fetchMock);
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    saves.push({ name: this.download, href: this.getAttribute("href") ?? "" });
  });
  makeElement = vi.spyOn(document, "createElement");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  // Let genuine toast timers remove their state while this test's Toasts is still mounted.
  await act(async () => vi.advanceTimersByTime(6_000));
  await act(async () => root.unmount());
  host.remove();
  URL.createObjectURL = originalCreateURL;
  URL.revokeObjectURL = originalRevokeURL;
  vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.clearAllTimers(); vi.useRealTimers();
});

async function openLoaded(name?: string, src = source) {
  await act(async () => root.render(<><Toasts /><ImageViewer images={[{ id: "synthetic-image", src, name, mime: "image/png" }]} start={0} onClose={() => {}} /></>));
  const image = document.querySelector<HTMLImageElement>('[role="dialog"] img')!;
  expect(image?.getAttribute("src")).toBe(src);
  Object.defineProperties(image, { naturalWidth: { configurable: true, value: 1 }, naturalHeight: { configurable: true, value: 1 } });
  await act(async () => image.dispatchEvent(new Event("load")));
  expect(downloadButton().disabled).toBe(false);
  expect(document.body.textContent).not.toContain(t("chat-imageviewer.unreadable"));
  expect(fetchMock).not.toHaveBeenCalled();
}

async function clickDownload() {
  await act(async () => downloadButton().click());
  await flush();
}

it.each([
  { status: 403, mime: "application/json", body: '{"error":"Synthetic image source unavailable"}' },
  { status: 503, mime: "text/html", body: "<p>Synthetic image source unavailable</p>" },
])("does not save an HTTP $status error body after a loaded image, and allows a successful retry", async ({ status, mime, body }) => {
  await openLoaded("sample photo.png");
  const baselineAnchors = anchorsMade();
  const response = new Response(body, { status, headers: { "Content-Type": mime } });
  const read = vi.spyOn(response, "blob");
  fetchMock.mockResolvedValueOnce(response);
  await clickDownload();
  expect(fetchMock).toHaveBeenCalledWith(source);
  expect(read, "an unsuccessful response must be rejected before reading its body").not.toHaveBeenCalled();
  expect(createURL, "an HTTP refusal must not become a downloadable Blob URL").not.toHaveBeenCalled();
  expect(anchorsMade(), "an HTTP refusal must not create a save link").toBe(baselineAnchors);
  expect(saves, "no error body may be saved as a PNG").toEqual([]);
  expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(t("chat-imageviewer.download_failed"));
  expect(downloadButton().disabled).toBe(false);

  fetchMock.mockResolvedValueOnce(new Response(png, { status: 200, headers: { "Content-Type": "image/png" } }));
  await clickDownload();
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(createURL).toHaveBeenCalledTimes(1);
  expect(saves).toEqual([{ name: "sample photo.png", href: "blob:synthetic-download" }]);
  const saved = createURL.mock.calls[0][0] as Blob;
  expect(saved.type).toBe("image/png");
  expect(await blobBytes(saved)).toEqual(png);
  expect(revokeURL).not.toHaveBeenCalled();
  await act(async () => vi.advanceTimersByTime(1_000));
  expect(revokeURL).toHaveBeenCalledExactlyOnceWith("blob:synthetic-download");
});

it.each([
  { mime: "image/png", name: undefined, expected: "image-1.png" },
  { mime: "image/jpeg", name: undefined, expected: "image-1.jpg" },
  { mime: "image/webp", name: "sample.webp", expected: "sample.webp" },
  { mime: "image/gif", name: undefined, expected: "image-1.gif" },
  { mime: "", name: undefined, expected: "image-1.png" },
])("keeps a successful $mime download's existing name and bytes", async ({ mime, name, expected }) => {
  await openLoaded(name);
  const content = new Uint8Array([1, 2, 3, 4]);
  fetchMock.mockResolvedValueOnce(new Response(content, { status: 200, headers: { "Content-Type": mime } }));
  await clickDownload();
  expect(saves).toEqual([{ name: expected, href: "blob:synthetic-download" }]);
  const saved = createURL.mock.calls[0][0] as Blob;
  expect(saved.type).toBe(mime);
  expect(await blobBytes(saved)).toEqual(content);
});

it("allows retry after a network failure without saving an error body", async () => {
  await openLoaded("sample.png");
  fetchMock.mockRejectedValueOnce(new TypeError("Synthetic network failure"));
  await clickDownload();
  expect(createURL).not.toHaveBeenCalled();
  expect(saves).toEqual([]);
  expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(t("chat-imageviewer.download_failed"));
  expect(downloadButton().disabled).toBe(false);

  fetchMock.mockResolvedValueOnce(new Response(png, { headers: { "Content-Type": "image/png" } }));
  await clickDownload();
  expect(saves).toEqual([{ name: "sample.png", href: "blob:synthetic-download" }]);
  expect(await blobBytes(createURL.mock.calls[0][0] as Blob)).toEqual(png);
});

it("does not save a successful response whose body cannot be read", async () => {
  await openLoaded("sample.png");
  const body = new ReadableStream({ start(controller) { controller.error(new Error("Synthetic interrupted body")); } });
  fetchMock.mockResolvedValueOnce(new Response(body, { headers: { "Content-Type": "image/png" } }));
  await clickDownload();
  expect(createURL).not.toHaveBeenCalled();
  expect(saves).toEqual([]);
  expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain(t("chat-imageviewer.download_failed"));
  expect(downloadButton().disabled).toBe(false);
});

it("keeps an in-flight download bound to the image that was requested when navigating", async () => {
  await act(async () => root.render(<><Toasts /><ImageViewer images={[
    { id: "first", src: source, name: "first.png" },
    { id: "second", src: "/synthetic/second.png", name: "second.png" },
  ]} start={0} onClose={() => {}} /></>));
  let finish!: (response: Response) => void;
  fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => { finish = resolve; }));
  await clickDownload();
  const next = document.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-imageviewer.next")}"]`)!;
  await act(async () => next.click());
  expect(document.querySelector('[role="dialog"] img')?.getAttribute("src")).toBe("/synthetic/second.png");
  await act(async () => finish(new Response(png, { headers: { "Content-Type": "image/png" } })));
  await flush();
  expect(fetchMock).toHaveBeenCalledExactlyOnceWith(source);
  expect(saves).toEqual([{ name: "first.png", href: "blob:synthetic-download" }]);
  expect(await blobBytes(createURL.mock.calls[0][0] as Blob)).toEqual(png);
});

it("downloads an embedded data image with its original bytes", async () => {
  const src = `data:image/png;base64,${btoa(String.fromCharCode(...png))}`;
  await openLoaded("embedded.png", src);
  fetchMock.mockImplementationOnce(nativeFetch);
  await clickDownload();
  expect(fetchMock).toHaveBeenCalledExactlyOnceWith(src);
  expect(saves).toEqual([{ name: "embedded.png", href: "blob:synthetic-download" }]);
  const saved = createURL.mock.calls[0][0] as Blob;
  expect(saved.type).toBe("image/png");
  expect(await blobBytes(saved)).toEqual(png);
});

const openTabButton = () => document.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-imageviewer.open_in_new_tab")}"]`)!;

it.each([
  "data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+PC9zdmc+",
  "DATA:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
  "javascript:alert(1)",
  "https:/admin",
  "https://user:pw@evil.example/pic.png",
  "/api/../admin",
  "/api/v1/lark/media/../../admin",
  "/api/v1/lark/media/%252e%252e/admin",
  "/api/v1/lark/media/..%2fadmin",
  "/api/v1/lark/media/foo%5cbar",
  "/api/v1/lark/media/..#x",
  "blob:https://evil.example/00000000-0000-0000-0000-000000000000",
])("refuses to open unsafe image URL %s in a new tab", async (badSrc) => {
  const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
  await act(async () => root.render(<><Toasts /><ImageViewer images={[{ id: "bad", src: badSrc }]} start={0} onClose={() => {}} /></>));
  await act(async () => openTabButton().click());
  await flush();
  expect(fetchMock).not.toHaveBeenCalled();
  expect(createURL).not.toHaveBeenCalled();
  expect(openSpy).not.toHaveBeenCalled();
});

it("opens safe raster data:, hub /api/, and https:// images in a new tab with noopener,noreferrer", async () => {
  const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
  const dataSrc = `data:image/png;base64,${btoa(String.fromCharCode(...png))}`;
  await openLoaded("embedded.png", dataSrc);
  fetchMock.mockImplementationOnce(nativeFetch);
  await act(async () => openTabButton().click());
  await flush();
  expect(createURL).toHaveBeenCalledTimes(1);
  expect((createURL.mock.calls[0][0] as Blob).type).toBe("image/png");
  expect(openSpy).toHaveBeenCalledExactlyOnceWith("blob:synthetic-download", "_blank", "noopener,noreferrer");
  await act(async () => vi.advanceTimersByTime(60_000));
  expect(revokeURL).toHaveBeenCalledExactlyOnceWith("blob:synthetic-download");

  openSpy.mockClear();
  await act(async () => root.render(<><Toasts /><ImageViewer images={[{ id: "hub", src: source }]} start={0} onClose={() => {}} /></>));
  await act(async () => openTabButton().click());
  await flush();
  expect(openSpy).toHaveBeenCalledExactlyOnceWith(source, "_blank", "noopener,noreferrer");

  openSpy.mockClear();
  await act(async () => root.render(<><Toasts /><ImageViewer images={[{ id: "web", src: "https://example.com/pic.png" }]} start={0} onClose={() => {}} /></>));
  await act(async () => openTabButton().click());
  await flush();
  expect(openSpy).toHaveBeenCalledExactlyOnceWith("https://example.com/pic.png", "_blank", "noopener,noreferrer");
});

it("refuses a same-origin blob: URL whose fetched MIME type is SVG", async () => {
  const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
  const sameOriginBlob = `blob:${location.origin}/svg-payload`;
  fetchMock.mockResolvedValueOnce(new Response("<svg/>", { status: 200, headers: { "Content-Type": "image/svg+xml" } }));
  await act(async () => root.render(<><Toasts /><ImageViewer images={[{ id: "svg-blob", src: sameOriginBlob }]} start={0} onClose={() => {}} /></>));
  await act(async () => openTabButton().click());
  await flush();
  expect(createURL).not.toHaveBeenCalled();
  expect(openSpy).not.toHaveBeenCalled();
});

it("opens parameterized raster data: and same-origin blob: URLs with normalized parameterless MIME", async () => {
  const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
  const paramData = `data:image/png;charset=utf-8;base64,${btoa(String.fromCharCode(...png))}`;
  await openLoaded("param.png", paramData);
  fetchMock.mockResolvedValueOnce(new Response(png, { status: 200, headers: { "Content-Type": "image/png; charset=utf-8" } }));
  await act(async () => openTabButton().click());
  await flush();
  expect(createURL).toHaveBeenCalledTimes(1);
  expect((createURL.mock.calls[0][0] as Blob).type).toBe("image/png");
  expect(openSpy).toHaveBeenCalledExactlyOnceWith("blob:synthetic-download", "_blank", "noopener,noreferrer");

  createURL.mockClear();
  openSpy.mockClear();
  const sameOriginBlob = `blob:${location.origin}/param-png`;
  fetchMock.mockResolvedValueOnce(new Response(png, { status: 200, headers: { "Content-Type": "image/jpeg; charset=binary" } }));
  await act(async () => root.render(<><Toasts /><ImageViewer images={[{ id: "blob-param", src: sameOriginBlob }]} start={0} onClose={() => {}} /></>));
  await act(async () => openTabButton().click());
  await flush();
  expect(createURL).toHaveBeenCalledTimes(1);
  expect((createURL.mock.calls[0][0] as Blob).type).toBe("image/jpeg");
  expect(openSpy).toHaveBeenCalledExactlyOnceWith("blob:synthetic-download", "_blank", "noopener,noreferrer");
});

it("falls back from malformed item.image.mime to hub imageURL in ChatImageViewer", async () => {
  const b64 = btoa(String.fromCharCode(...png));
  await act(async () =>
    root.render(
      <ChatImageViewer
        mid="m1"
        sid="s1"
        onClose={() => {}}
        item={{ id: "img-bad-mime", kind: "image", image: { mime: "text/html,payload", data: b64 } } as never}
      />,
    ),
  );
  expect(document.querySelector('[role="dialog"] img')?.getAttribute("src")).toMatch(/^\/api\/v1\/sessions\/m1\/s1\/items\/img-bad-mime\/image/);

  await act(async () =>
    root.render(
      <ChatImageViewer
        key="good"
        mid="m1"
        sid="s1"
        onClose={() => {}}
        item={{ id: "img-good-mime", kind: "image", image: { mime: "image/png; charset=utf-8", data: b64 } } as never}
      />,
    ),
  );
  expect(document.querySelector('[role="dialog"] img')?.getAttribute("src")).toBe(`data:image/png;base64,${b64}`);
});
