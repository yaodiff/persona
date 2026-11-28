// @vitest-environment jsdom
// The Files panel's changes (#4883, the interface of #4858): the requests each one sends, an upload's progress, abort
// and the hub's codes, what a name may be, and the words a failure is told in.
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { ApiError, errorText, HubUnreachableError, isTransient } from "./api";
import { isNameTaken, moveEntries, nameProblem, opErrorText, parentOf, renameEntry, restoreEntries, trashEntries, uploadToDir, within } from "./file-ops";

/** A stand-in for XMLHttpRequest: what was sent, and the test answers it. */
class FakeXHR {
  static last: FakeXHR;
  method = "";
  url = "";
  headers: Record<string, string> = {};
  body: unknown;
  status = 0;
  responseText = "";
  upload: { onprogress?: (e: { loaded: number; total: number; lengthComputable: boolean }) => void } = {};
  onload?: () => void;
  onerror?: () => void;
  onabort?: () => void;
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
    this.body = body;
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
  answer(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

const file = (name: string, size = 10) => new File([new Uint8Array(size)], name);

afterEach(() => vi.unstubAllGlobals());

describe("uploadToDir", () => {
  it("sends the file into the folder, reporting progress", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const seen: number[] = [];
    const p = uploadToDir("m 1", file("a b.txt"), { dir: "~/Downloads", cwd: "/home/me/x", onProgress: (sent) => seen.push(sent) });
    const x = FakeXHR.last;
    expect(x.method).toBe("POST");
    expect(x.url).toBe("/api/v1/machines/m%201/files?name=a%20b.txt&dest=dir&dir=~%2FDownloads&cwd=%2Fhome%2Fme%2Fx");
    expect(x.headers.Authorization).toMatch(/^Bearer /);
    x.upload.onprogress?.({ loaded: 4, total: 10, lengthComputable: true });
    x.answer(200, { path: "~/Downloads/a b.txt", name: "a b.txt", size: 10 });
    await expect(p).resolves.toEqual({ path: "~/Downloads/a b.txt", name: "a b.txt", size: 10 });
    expect(seen).toEqual([4]);
  });

  it("says what to do with a name already there only when asked", () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    void uploadToDir("m", file("a"), { dir: "/d", conflict: "keep" });
    expect(FakeXHR.last.url).toContain("&conflict=keep");
    void uploadToDir("m", file("a"), { dir: "/d" });
    expect(FakeXHR.last.url).not.toContain("conflict");
  });

  it("rejects with the hub's code: a name taken is a question, not a failure", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const p = uploadToDir("m", file("a.txt"), { dir: "/d" });
    FakeXHR.last.answer(409, { error: "exists", code: "exists", name: "a.txt" });
    const e = await p.catch((x: unknown) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect(isNameTaken(e)).toBe(true);
    expect(opErrorText(e)).toContain("a.txt");
  });

  it("says a dropped connection is \"Can't reach the hub\", and may be retried", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const p = uploadToDir("m", file("a.txt"), { dir: "/d" });
    FakeXHR.last.onerror?.();
    const e = await p.catch((x: unknown) => x);
    expect(e).toBeInstanceOf(HubUnreachableError);
    expect(e).toBeInstanceOf(TypeError); // code that counts a TypeError as a gap keeps working
    expect(errorText(e)).toBe(t("lib-api.cant_reach_hub"));
    expect(opErrorText(e)).toBe(t("lib-api.cant_reach_hub"));
    expect(isTransient(e)).toBe(true);
  });

  it("aborts with the caller's signal, as an AbortError", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    const ac = new AbortController();
    const p = uploadToDir("m", file("a"), { dir: "/d", signal: ac.signal });
    ac.abort();
    expect(FakeXHR.last.aborted).toBe(true);
    await expect(p).rejects.toMatchObject({ name: "AbortError" });
  });
});

describe("rename, move, trash, restore", () => {
  const answering = (body: unknown) => {
    const f = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status: 200 }));
    vi.stubGlobal("fetch", f);
    return f;
  };
  const sent = (f: ReturnType<typeof answering>) => ({ url: f.mock.calls[0][0], body: JSON.parse(String(f.mock.calls[0][1]?.body)) });

  it("posts each to its endpoint with #4858's fields", async () => {
    let f = answering({ path: "/d/b" });
    await expect(renameEntry("m", "/d/a", "/d", "b")).resolves.toBe("/d/b");
    expect(sent(f)).toEqual({ url: "/api/v1/machines/m/files/rename", body: { path: "/d/a", cwd: "/d", name: "b" } });

    f = answering({ moved: [{ from: "/d/a", to: "/e/a" }] });
    await expect(moveEntries("m", ["/d/a"], "/d", "/e")).resolves.toEqual([{ from: "/d/a", to: "/e/a" }]);
    expect(sent(f).body).toEqual({ paths: ["/d/a"], cwd: "/d", dir: "/e" });

    f = answering({ trashed: [{ path: "/d/a", trash_id: "t1" }] });
    await expect(trashEntries("m", ["/d/a"], "/d")).resolves.toEqual([{ path: "/d/a", trash_id: "t1" }]);
    expect(sent(f)).toEqual({ url: "/api/v1/machines/m/files/trash", body: { paths: ["/d/a"], cwd: "/d" } });

    f = answering({ restored: [{ path: "/d/a" }] });
    await expect(restoreEntries("m", ["t1"])).resolves.toEqual(["/d/a"]);
    expect(sent(f)).toEqual({ url: "/api/v1/machines/m/files/restore", body: { trash_ids: ["t1"] } });
  });
});

describe("names and paths", () => {
  it("refuses what can't be a name", () => {
    expect(nameProblem("ok.txt")).toBeNull();
    for (const bad of ["", "  ", ".", "..", "a/b", "a\\b"]) expect(nameProblem(bad)).not.toBeNull();
  });

  it("finds the parent and what is inside", () => {
    expect(parentOf("/home/me/a.txt")).toBe("/home/me");
    expect(parentOf("/a")).toBe("/");
    expect(parentOf("/home/me/dir/")).toBe("/home/me");
    expect(within("/home/me/dir", "/home/me/dir/sub")).toBe(true);
    expect(within("/home/me/dir", "/home/me/dir")).toBe(true);
    expect(within("/home/me/dir", "/home/me/dir2")).toBe(false);
  });
});

describe("opErrorText", () => {
  it("says the hub's codes in the user's words", () => {
    const e = (status: number, code?: string) => new ApiError(status, "raw", code ? { code } : {});
    expect(opErrorText(e(409, "read_only"))).not.toBe("raw");
    expect(opErrorText(e(409, "node_outdated"))).not.toBe("raw");
    expect(opErrorText(e(404))).not.toBe("raw"); // a hub without the endpoint
    expect(opErrorText(e(502, "internal"))).toBe("raw"); // the node's own words
  });
});
