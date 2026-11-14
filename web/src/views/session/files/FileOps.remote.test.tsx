// @vitest-environment jsdom
// Remote copy / zip progress through the mounted Files hook and a controllable transport; no real file is touched.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { handleUnauthorized, setToken } from "../../../lib/api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../../../lib/manager-choice";
import { getState } from "../../../lib/store";
import { t } from "../../../i18n";
import { shortPath } from "../../../lib/format";
import { useFileOps, type FileOps, type OpTarget } from "./FileOps";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;

type Call = { path: string; body: Record<string, unknown>; signal?: AbortSignal | null; answer: (body: unknown, status?: number) => void };
let host: HTMLDivElement, root: Root, ops: FileOps, calls: Call[];
let pendingRemote = false;
let operationAvailable = true;
let remoteBytes = { done: 32, total: 100 };
const changed = vi.fn();
const target: OpTarget = { path: "/work/from/sample.txt", name: "sample.txt", dir: false };
const originalFeatures = getState().features;
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };
const byRoute = (route: string) => calls.filter((call) => call.path.endsWith(`/files/${route}`));
const operationReads = () => calls.filter((call) => call.path.includes("/files/operations/"));
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
async function click(label: string) {
  const found = button(label);
  expect(found, label).toBeTruthy();
  await act(async () => found!.click());
  await flush();
}
async function answer(call: Call, body: unknown, status = 200) { await act(async () => call.answer(body, status)); await flush(); }
function Harness({ cwd = "/work", path = "/work/from" }: { cwd?: string; path?: string }) {
  ops = useFileOps({ mid: "machine-a", cwd, path, onChanged: changed });
  return <div>{ops.ui}</div>;
}

beforeEach(async () => {
  calls = [];
  pendingRemote = false;
  operationAvailable = true;
  remoteBytes = { done: 32, total: 100 };
  changed.mockReset();
  resetManagerChoice(); setRosterDefault("manager-a"); chooseManager("manager-a");
  setToken("jd_synthetic-a");
  handleUnauthorized(() => {});
  Object.assign(getState(), { features: ["file_edit", "file_copy", "file_zip"] });
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://local.invalid");
    if (url.pathname.endsWith("/fs")) return Promise.resolve(Response.json({ entries: [] }));
    if (url.pathname === "/api/v1/machines") return Promise.resolve(Response.json({ machines: [] }));
    if (url.pathname.includes("/files/operations/")) {
      calls.push({ path: url.pathname, body: {}, signal: init?.signal, answer: () => {} });
      if (!operationAvailable) return Promise.resolve(Response.json({ error: "not found" }, { status: 404 }));
      return Promise.resolve(Response.json({ id: "synthetic-operation", kind: "copy", state: "running", files_done: 1, files_total: 2, bytes_done: remoteBytes.done, bytes_total: remoteBytes.total }));
    }
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    const result = url.pathname.endsWith("/files/zip")
      ? { path: "/work/from/sample.zip", name: "sample.zip", size: 10, files: 1 }
      : { copied: [{ from: target.path, to: "/work/from/sample 2.txt" }] };
    if (!pendingRemote) {
      calls.push({ path: url.pathname, body, signal: init?.signal, answer: () => {} });
      return Promise.resolve(Response.json(result));
    }
    return new Promise<Response>((resolve, reject) => {
      const call: Call = { path: url.pathname, body, signal: init?.signal, answer: (value, status = 200) => resolve(Response.json(value, { status })) };
      calls.push(call);
      init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
    });
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<Harness />)); await flush();
});

afterEach(() => {
  act(() => root.unmount()); host.remove(); document.body.innerHTML = "";
  Object.assign(getState(), { features: originalFeatures });
  handleUnauthorized(() => {}); setToken(""); resetManagerChoice(); vi.unstubAllGlobals(); vi.useRealTimers();
});

describe("remote file operations", () => {
  it("adds operation ids without polling quick Duplicate and Compress", async () => {
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => ops.compress([target])); await flush();
    const copyID = byRoute("copy")[0].body.operation_id;
    const zipID = byRoute("zip")[0].body.operation_id;
    expect(copyID).toEqual(expect.any(String));
    expect(zipID).toEqual(expect.any(String));
    expect(copyID).not.toBe(zipID);
    expect(operationReads()).toHaveLength(0);
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    expect(changed).toHaveBeenCalledTimes(2);
  });

  it("shows polled progress after one second and Cancel aborts the original POST", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    expect(operationReads()).toHaveLength(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    const progress = host.querySelector<HTMLElement>('[role="progressbar"][aria-label="sample.txt"]');
    expect(progress?.getAttribute("aria-valuenow")).toBe("32");
    expect(progress?.getAttribute("aria-valuetext")).toContain("32");
    expect(host.textContent).toContain("1 / 2 · 32 B / 100 B");
    expect(operationReads().length).toBeGreaterThan(0);
    await click(t("files-ops.copy_cancel", { name: target.name }));
    expect(byRoute("copy")[0].signal?.aborted).toBe(true);
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    await answer(byRoute("copy")[0], { copied: [{ from: target.path, to: "/work/from/late.txt" }] });
    expect(changed).not.toHaveBeenCalled();
  });

  it("does not offer a false Cancel when operation lookup is unsupported", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    operationAvailable = false;
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    expect(operationReads().length).toBeGreaterThan(0);
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    await answer(byRoute("copy")[0], { copied: [{ from: target.path, to: "/work/from/sample 2.txt" }] });
    expect(changed).toHaveBeenCalledExactlyOnceWith(["/work/from"]);
  });

  it("uses file progress when an operation has no source bytes", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    remoteBytes = { done: 0, total: 0 };
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    const progress = host.querySelector<HTMLElement>('[role="progressbar"][aria-label="sample.txt"]');
    expect(progress?.getAttribute("aria-valuenow")).toBe("50");
    expect(progress?.getAttribute("aria-valuetext")).toContain("1 / 2");
    expect(progress?.getAttribute("aria-valuetext")).not.toContain(t("files-ops.operation_preparing"));
    await click(t("files-ops.copy_cancel", { name: target.name }));
  });

  it("does not abort the authoritative POST when the panel unmounts", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    expect(host.querySelector('[role="progressbar"]')).not.toBeNull();
    await act(async () => root.render(<div />)); await flush();
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    expect(byRoute("copy")[0].signal?.aborted).toBe(false);
    await answer(byRoute("copy")[0], { copied: [{ from: target.path, to: "/work/from/sample 2.txt" }] });
    expect(changed).not.toHaveBeenCalled();
  });

  it("retires an unmounted authoritative POST when credentials change", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    const request = byRoute("copy")[0];
    await act(async () => root.render(<div />)); await flush();
    expect(request.signal?.aborted).toBe(false);
    await act(async () => setToken("jd_synthetic-b")); await flush();
    expect(request.signal?.aborted).toBe(true);
  });

  it("does not abort the authoritative POST when the workspace changes", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    await act(async () => root.render(<Harness cwd="/elsewhere" />)); await flush();
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    expect(byRoute("copy")[0].signal?.aborted).toBe(false);
    await answer(byRoute("copy")[0], { copied: [{ from: target.path, to: "/work/from/sample 2.txt" }] });
    expect(changed).not.toHaveBeenCalled();
  });

  it("restores a running operation and Cancel after leaving and returning", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    expect(host.querySelector('[role="progressbar"]')).not.toBeNull();
    const before = operationReads().length;
    await act(async () => root.render(<Harness path="/work/elsewhere" />)); await flush();
    expect(host.querySelector('[role="progressbar"]')).toBeNull();
    expect(byRoute("copy")[0].signal?.aborted).toBe(false);
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); }); await flush();
    expect(operationReads()).toHaveLength(before);
    await act(async () => root.render(<Harness path="/work/from" />)); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(300); }); await flush();
    expect(operationReads().length).toBeGreaterThan(before);
    expect(host.querySelector('[role="progressbar"][aria-label="sample.txt"]')).not.toBeNull();
    await click(t("files-ops.copy_cancel", { name: target.name }));
    expect(byRoute("copy")[0].signal?.aborted).toBe(true);
  });

  it("keeps workspace completion feedback when the operation finishes away and the user returns", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    await act(async () => root.render(<Harness path="/work/elsewhere" />)); await flush();
    await answer(byRoute("copy")[0], { copied: [{ from: target.path, to: "/work/from/sample 2.txt" }] });
    const completed = t("files-ops.duplicated", { count: 1, dir: shortPath("/work/from") });
    expect(changed).toHaveBeenCalledExactlyOnceWith(["/work/from"]);
    expect(host.textContent).toContain(completed);
    await act(async () => root.render(<Harness path="/work/from" />)); await flush();
    expect(host.textContent).toContain(completed);
  });

  it("keeps workspace failure feedback when the operation fails away and the user returns", async () => {
    vi.useFakeTimers();
    pendingRemote = true;
    await act(async () => ops.duplicate([target])); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); }); await flush();
    await act(async () => root.render(<Harness path="/work/elsewhere" />)); await flush();
    await answer(byRoute("copy")[0], { error: "synthetic copy failed" }, 500);
    expect(changed).not.toHaveBeenCalled();
    expect(host.textContent).toContain("synthetic copy failed");
    await act(async () => root.render(<Harness path="/work/from" />)); await flush();
    expect(host.textContent).toContain("synthetic copy failed");
  });

  it("closes Copy To on start and retries a conflict with a fresh operation", async () => {
    pendingRemote = true;
    await act(async () => ops.copy([target])); await flush();
    await click(t("files-ops.up"));
    await click(t("files-ops.copy_here"));
    expect(button(t("files-ops.copy_here"))).toBeUndefined();
    const first = byRoute("copy")[0];
    const firstID = first.body.operation_id;
    expect(firstID).toEqual(expect.any(String));
    await answer(first, { error: "taken", code: "exists", name: target.name }, 409);
    await click(t("files-ops.keep_both"));
    const second = byRoute("copy")[1];
    expect(second.body.conflict).toBe("keep");
    expect(second.body.operation_id).toEqual(expect.any(String));
    expect(second.body.operation_id).not.toBe(firstID);
    await answer(second, { copied: [{ from: target.path, to: "/work/sample.txt" }] });
    expect(changed).toHaveBeenCalledExactlyOnceWith(["/work"]);
  });
});
