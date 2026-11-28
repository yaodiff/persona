// @vitest-environment jsdom
// Making an avatar from an image (#2404): the crop's geometry (kept inside the image, zoomed about the pointer), the
// export's size and encoding, the request bodies and how the hub's refusals reach the user.
import { afterEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => [] as { path: string; init?: { method?: string } }[]);
vi.mock("./api", async (orig) => {
  const real = await orig<typeof import("./api")>();
  return {
    ...real,
    getToken: () => "tok",
    api: vi.fn((path: string, init?: { method?: string }) => {
      calls.push({ path, init });
      return Promise.resolve({ current: { id: "persona", effective: "persona", updated_at: "2026-11-09T00:00:00Z" } });
    }),
  };
});

import { t } from "../i18n";
import { ApiError, errorText, HubUnreachableError, isTransient } from "./api";
import {
  acceptsType, base64Bytes, bigEnough, canSend, centeredCrop, clampCrop, cropSide, deleteUploadedLook, encodingFor, EXPORT_MAX, exportCrop,
  exportRect, MIN_SIDE, maxZoom, NAME_MAX, panCrop, PNG_LIMIT, refusalText, SUBTITLE_MAX, uploadBody, uploadLook, xhrPost, ZOOM_MAX, zoomCrop,
  type ProgressPost,
} from "./look-upload";

const wide = { w: 3000, h: 2000 };

afterEach(() => {
  calls.length = 0;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the crop", () => {
  it("starts as the largest centered square", () => {
    const c = centeredCrop(wide);
    expect(c).toEqual({ x: 1500, y: 1000, zoom: 1 });
    expect(cropSide(wide, c.zoom)).toBe(2000);
  });

  it("stays inside the image", () => {
    expect(clampCrop(wide, { x: 0, y: 0, zoom: 1 })).toEqual({ x: 1000, y: 1000, zoom: 1 });
    expect(clampCrop(wide, { x: 9999, y: -5, zoom: 2 })).toEqual({ x: 2500, y: 500, zoom: 2 });
  });

  it("zooms between 1 and the most that keeps the square at least MIN_SIDE pixels", () => {
    expect(maxZoom(wide)).toBe(ZOOM_MAX);
    expect(maxZoom({ w: 256, h: 200 })).toBeCloseTo(200 / MIN_SIDE);
    expect(maxZoom({ w: 64, h: 64 })).toBe(1);
    expect(clampCrop(wide, { x: 1500, y: 1000, zoom: 0.2 }).zoom).toBe(1);
    expect(clampCrop({ w: 256, h: 200 }, { x: 128, y: 100, zoom: 50 }).zoom).toBeCloseTo(200 / MIN_SIDE);
    expect(cropSide({ w: 256, h: 200 }, maxZoom({ w: 256, h: 200 }))).toBeCloseTo(MIN_SIDE);
  });

  it("follows a drag: the image moves with the pointer", () => {
    const c = { x: 1500, y: 1000, zoom: 2 }; // a 1000 px square in a 250 px box: 4 image px per CSS px
    expect(panCrop(wide, c, 25, -10, 250)).toEqual({ x: 1400, y: 1040, zoom: 2 });
    // dragged past the edge, it stops there
    expect(panCrop(wide, c, -5000, 0, 250).x).toBe(2500);
  });

  it("zooms about the pointer, keeping the image point under it in place", () => {
    const c = { x: 1500, y: 1000, zoom: 1 };
    const view = 200;
    const px = 150, py = 50;
    const under = (k: typeof c) => {
      const side = cropSide(wide, k.zoom);
      return { x: k.x - side / 2 + (px * side) / view, y: k.y - side / 2 + (py * side) / view };
    };
    const z = zoomCrop(wide, c, 2, view, px, py);
    expect(z.zoom).toBe(2);
    expect(under(z).x).toBeCloseTo(under(c).x);
    expect(under(z).y).toBeCloseTo(under(c).y);
    // about the center by default: the center stays
    expect(zoomCrop(wide, { x: 1200, y: 900, zoom: 1.5 }, 3, view)).toEqual({ x: 1200, y: 900, zoom: 3 });
  });

  it("refuses images under MIN_SIDE on a side", () => {
    expect(bigEnough({ w: 64, h: 640 })).toBe(true);
    expect(bigEnough({ w: 63, h: 640 })).toBe(false);
  });

  it("takes the formats the hub reads", () => {
    for (const ok of ["image/png", "image/jpeg", "image/webp", "image/gif", "IMAGE/PNG", ""]) expect(acceptsType(ok)).toBe(true);
    for (const no of ["image/svg+xml", "image/heic", "text/plain", "application/pdf"]) expect(acceptsType(no)).toBe(false);
  });
});

describe("the export", () => {
  it("is the crop at its own size, never past EXPORT_MAX", () => {
    expect(exportRect(wide, centeredCrop(wide))).toEqual({ sx: 500, sy: 0, side: 2000, out: EXPORT_MAX });
    expect(exportRect({ w: 500, h: 300 }, centeredCrop({ w: 500, h: 300 })).out).toBe(300);
    const small = { w: 256, h: 200 };
    expect(exportRect(small, clampCrop(small, { x: 128, y: 100, zoom: 99 })).out).toBe(MIN_SIDE);
  });

  it("counts a base64 text's bytes", () => {
    expect(base64Bytes(btoa("hello"))).toBe(5);
    expect(base64Bytes(btoa("hell"))).toBe(4);
    expect(base64Bytes("data:image/png;base64," + btoa("hello!"))).toBe(6);
  });

  it("is a PNG, or a JPEG once the PNG would pass the limit", () => {
    expect(encodingFor(PNG_LIMIT)).toBe("png");
    expect(encodingFor(PNG_LIMIT + 1)).toBe("jpeg");
  });

  function fakeCanvas(pngBytes: number) {
    const drawn: unknown[][] = [];
    const g = { drawImage: (...a: unknown[]) => drawn.push(a), fillRect: vi.fn(), imageSmoothingEnabled: false, imageSmoothingQuality: "low", globalCompositeOperation: "", fillStyle: "" };
    const canvas = {
      width: 0,
      height: 0,
      getContext: () => g,
      toDataURL: (type: string) => (type === "image/png" ? "data:image/png;base64," + "A".repeat(Math.ceil((pngBytes * 4) / 3)) : "data:image/jpeg;base64,SlBFRw=="),
    };
    const doc = { createElement: () => canvas } as unknown as Document;
    return { doc, canvas, g, drawn };
  }

  it("draws the crop's square into a canvas of the export's size", () => {
    const f = fakeCanvas(1000);
    const out = exportCrop({} as CanvasImageSource, wide, { x: 1500, y: 1000, zoom: 2 }, f.doc);
    expect(out.mime).toBe("image/png");
    expect(out.side).toBe(1000);
    expect([f.canvas.width, f.canvas.height]).toEqual([1000, 1000]);
    expect(f.drawn[0].slice(1)).toEqual([1000, 500, 1000, 1000, 0, 0, 1000, 1000]);
    expect(out.image.startsWith("data:")).toBe(false);
  });

  it("sends a JPEG on white when the PNG is too big", () => {
    const f = fakeCanvas(PNG_LIMIT + 10);
    const out = exportCrop({} as CanvasImageSource, wide, centeredCrop(wide), f.doc);
    expect(out).toEqual({ image: "SlBFRw==", mime: "image/jpeg", side: EXPORT_MAX });
    expect(f.g.fillRect).toHaveBeenCalledWith(0, 0, EXPORT_MAX, EXPORT_MAX);
    expect(f.g.globalCompositeOperation).toBe("destination-over");
  });
});

describe("the requests", () => {
  it("trims the name, leaves an empty description out and chooses the new look", () => {
    expect(uploadBody("  Doudou ", "  ", "QUJD")).toEqual({ name: "Doudou", image: "QUJD", choose: true });
    expect(uploadBody("Doudou", " round ", "QUJD")).toEqual({ name: "Doudou", subtitle: "round", image: "QUJD", choose: true });
  });

  it("needs a name of 1 to 40 characters and a description of at most 80, counted as the hub counts", () => {
    expect(canSend("", "")).toBe(false);
    expect(canSend("   ", "")).toBe(false);
    expect(canSend("x".repeat(NAME_MAX), "y".repeat(SUBTITLE_MAX))).toBe(true);
    expect(canSend("x".repeat(NAME_MAX + 1), "")).toBe(false);
    expect(canSend("x", "y".repeat(SUBTITLE_MAX + 1))).toBe(false);
    // 40 emoji are 80 UTF-16 units but 40 characters
    expect(canSend("😀".repeat(NAME_MAX), "")).toBe(true);
  });

  it("posts the body to /looks/uploads and passes progress through", async () => {
    const seen: { path: string; json: unknown }[] = [];
    const post: ProgressPost = async <T,>(path: string, json: unknown, opts: { onProgress?: (a: number, b: number) => void }) => {
      seen.push({ path, json });
      opts.onProgress?.(5, 10);
      return { look: { id: "u-1", source: "hub", name: "Doudou", app_icon: "persona", uploaded: true }, current: { id: "u-1", effective: "u-1" } } as T;
    };
    const progress: number[] = [];
    const r = await uploadLook(uploadBody("Doudou", "", "QUJD"), { onProgress: (a, b) => progress.push(a / b) }, post);
    expect(seen).toEqual([{ path: "/looks/uploads", json: { name: "Doudou", image: "QUJD", choose: true } }]);
    expect(progress).toEqual([0.5]);
    expect(r.look.id).toBe("u-1");
    expect(r.current).toEqual({ id: "u-1", effective: "u-1" });
  });

  it("deletes with DELETE /looks/uploads/{id} and returns the choice after it", async () => {
    const cur = await deleteUploadedLook("u-ab/cd");
    expect(calls).toEqual([{ path: "/looks/uploads/u-ab%2Fcd", init: { method: "DELETE" } }]);
    expect(cur).toEqual({ id: "persona", effective: "persona", updated_at: "2026-11-09T00:00:00Z" });
  });

  it("shows the hub's refusal as it is, anything else as the usual error text", () => {
    expect(refusalText(new ApiError(409, "x", { error: "最多只能有 50 个上传的形象", code: "too_many" }))).toBe("最多只能有 50 个上传的形象");
    expect(refusalText(new ApiError(404, "这个形象已经不在 hub 上了", { error: "这个形象已经不在 hub 上了", code: "not_found" }))).toBe("这个形象已经不在 hub 上了");
    expect(refusalText(new ApiError(502, "HTTP 502", { error: "" }, false))).toContain("HTTP 502");
  });
});

describe("the upload request (XHR, for its progress)", () => {
  class FakeXHR {
    static last: FakeXHR;
    headers: Record<string, string> = {};
    method = "";
    url = "";
    sent: unknown = null;
    status = 0;
    responseText = "";
    upload: { onprogress: ((e: { loaded: number; total: number; lengthComputable: boolean }) => void) | null } = { onprogress: null };
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    onabort: (() => void) | null = null;
    aborted = false;
    constructor() {
      FakeXHR.last = this;
    }
    open(method: string, url: string) {
      this.method = method;
      this.url = url;
    }
    setRequestHeader(k: string, v: string) {
      this.headers[k] = v;
    }
    getResponseHeader() {
      return null;
    }
    send(body: unknown) {
      this.sent = body;
    }
    abort() {
      this.aborted = true;
      this.onabort?.();
    }
  }

  it("sends the JSON with the token and the language, reports progress and resolves with the answer", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const progress: [number, number][] = [];
    const p = xhrPost<{ look: { id: string } }>("/looks/uploads", { name: "Doudou" }, { onProgress: (a, b) => progress.push([a, b]) });
    const x = FakeXHR.last;
    expect(x.method).toBe("POST");
    expect(x.url).toMatch(/\/api\/v1\/looks\/uploads$/);
    expect(x.headers.Authorization).toBe("Bearer tok");
    expect(x.headers["Content-Type"]).toBe("application/json");
    expect(x.headers["Accept-Language"]).toBe("zh-Hans");
    expect(JSON.parse(x.sent as string)).toEqual({ name: "Doudou" });
    x.upload.onprogress!({ loaded: 3, total: 6, lengthComputable: true });
    x.status = 200;
    x.responseText = JSON.stringify({ look: { id: "u-1" } });
    x.onload!();
    await expect(p).resolves.toEqual({ look: { id: "u-1" } });
    expect(progress).toEqual([[3, 6]]);
  });

  it("rejects with the hub's code and words", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const p = xhrPost("/looks/uploads", {}, {});
    const x = FakeXHR.last;
    x.status = 413;
    x.responseText = JSON.stringify({ error: "Too big", code: "too_large" });
    x.onload!();
    const e = await p.catch((err: unknown) => err);
    expect(e).toBeInstanceOf(ApiError);
    expect((e as ApiError).code).toBe("too_large");
    expect(refusalText(e)).toBe("Too big");
  });

  it("says a dropped connection is \"Can't reach the hub\", and may be retried", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const p = xhrPost("/looks/uploads", {}, {});
    FakeXHR.last.onerror!();
    const e = await p.catch((err: unknown) => err);
    expect(e).toBeInstanceOf(HubUnreachableError);
    expect(e).toBeInstanceOf(TypeError);
    expect(errorText(e)).toBe(t("lib-api.cant_reach_hub"));
    expect(refusalText(e)).toBe(t("lib-api.cant_reach_hub"));
    expect(isTransient(e)).toBe(true);
  });

  it("stops when aborted", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const ctl = new AbortController();
    const p = xhrPost("/looks/uploads", {}, { signal: ctl.signal });
    ctl.abort();
    await expect(p).rejects.toMatchObject({ name: "AbortError" });
    expect(FakeXHR.last.aborted).toBe(true);
  });
});
