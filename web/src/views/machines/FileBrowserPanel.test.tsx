// @vitest-environment jsdom
import { t } from "../../i18n";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = {
    getItem: (k: string) => kept.get(k) ?? null,
    setItem: (k: string, v: string) => void kept.set(k, v),
    removeItem: (k: string) => void kept.delete(k),
  };
});

import { setToken } from "../../lib/api";
import { FileBrowserPanel, machineFileDownloadURL } from "./FileBrowserPanel";
import {
  classifySensitivePathClient,
  computeLineDiff,
  detectLineEnding,
  normalizeToLF,
  restoreLineEnding,
} from "./fileDiff";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;
let calls: Array<{ url: string; method: string; body?: string }>;
let tailCalls = 0;
let writeMode: "ok" | "conflict" | "plain-409" = "ok";
let listDirPath = "/home/dev";
let createRequire412Once = false;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

function setNativeValue(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  setter?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function stubHub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = String(input);
  const method = (init?.method ?? "GET").toUpperCase();
  const body = typeof init?.body === "string" ? init.body : undefined;
  calls.push({ url, method, body });

  if (url.includes("/files/roots")) {
    return Promise.resolve(json({
      home: "/home/dev",
      os: "linux",
      roots: [
        { id: "home", label: "$HOME", path: "/home/dev", kind: "home", exists: true },
        { id: "ws:1", label: "persona", path: "/home/dev/persona", kind: "workspace", exists: true },
        { id: "cfg:1", label: ".config", path: "/home/dev/.config", kind: "config", exists: true },
      ],
    }));
  }
  if (url.includes("/files/list")) {
    const reqPath = new URL(url, "http://localhost").searchParams.get("path") ?? "";
    const curPath = reqPath.endsWith("/logs") || reqPath.endsWith("/logs-archived") ? reqPath : listDirPath;
    return Promise.resolve(json({
      path: curPath,
      canonical_path: curPath,
      parent: "/home",
      total: 6,
      entries: [
        { name: "logs", path: `${curPath}/logs`, kind: "dir", size: 4096, mode: "drwxr-xr-x", perm: 0o755, mtime: "2026-11-20T00:00:00Z" },
        { name: ".env", path: `${curPath}/.env`, kind: "file", size: 12, mode: "-rw-------", perm: 0o600, mtime: "2026-11-20T00:00:00Z", sensitive: true, sensitive_reason: "env" },
        { name: "win.ini", path: `${curPath}/win.ini`, kind: "file", size: 22, mode: "-rw-r--r--", perm: 0o644, mtime: "2026-11-20T00:00:00Z" },
        { name: "mixed.txt", path: `${curPath}/mixed.txt`, kind: "file", size: 18, mode: "-rw-r--r--", perm: 0o644, mtime: "2026-11-20T00:00:00Z" },
        { name: "app.log", path: `${curPath}/app.log`, kind: "file", size: 600000, mode: "-rw-r--r--", perm: 0o644, mtime: "2026-11-20T00:00:00Z" },
        { name: "image.bin", path: `${curPath}/image.bin`, kind: "file", size: 2048, mode: "-rw-r--r--", perm: 0o644, mtime: "2026-11-20T00:00:00Z", binary: true },
      ],
    }));
  }
  if (url.includes("/files/preview") && url.includes("mixed.txt")) {
    return Promise.resolve(json({
      path: `${listDirPath}/mixed.txt`,
      canonical_path: `${listDirPath}/mixed.txt`,
      name: "mixed.txt",
      size: 18,
      mode: "-rw-r--r--",
      perm: 0o644,
      mtime: "2026-11-20T00:00:00Z",
      mime: "text/plain; charset=utf-8",
      revision: "r-mixed",
      sha256: "sha-mixed-1",
      editable: true,
      edit_max_bytes: 1048576,
      text: "alpha\r\nbeta\ngamma\n",
      bytes_read: 18,
      max_bytes: 262144,
    }));
  }
  if (url.includes("/files/preview") && url.includes("win.ini")) {
    return Promise.resolve(json({
      path: `${listDirPath}/win.ini`,
      canonical_path: `${listDirPath}/win.ini`,
      name: "win.ini",
      size: 22,
      mode: "-rw-r--r--",
      perm: 0o644,
      mtime: "2026-11-20T00:00:00Z",
      mime: "text/plain; charset=utf-8",
      revision: "r-win",
      sha256: "sha-win-1",
      editable: true,
      edit_max_bytes: 1048576,
      text: "alpha\r\nbeta\r\ngamma\r\n",
      bytes_read: 22,
      max_bytes: 262144,
    }));
  }
  if (url.includes("/files/preview") && url.includes(".env")) {
    return Promise.resolve(json({
      path: "/home/dev/.env",
      canonical_path: "/home/dev/.env",
      name: ".env",
      size: 12,
      mode: "-rw-------",
      perm: 0o600,
      mtime: "2026-11-20T00:00:00Z",
      mime: "text/plain; charset=utf-8",
      revision: "r-env",
      sha256: "sha-env-1",
      editable: true,
      edit_max_bytes: 1048576,
      sensitive: true,
      sensitive_reason: "env",
      text: "DEMO_FLAG=1\n",
      bytes_read: 12,
      max_bytes: 262144,
    }));
  }
  if (url.includes("/files/preview") && url.includes("image.bin")) {
    return Promise.resolve(json({
      path: "/home/dev/image.bin",
      canonical_path: "/home/dev/image.bin",
      name: "image.bin",
      size: 2048,
      mode: "-rw-r--r--",
      perm: 0o644,
      mtime: "2026-11-20T00:00:00Z",
      mime: "application/octet-stream",
      revision: "r-bin",
      binary: true,
      bytes_read: 2048,
      max_bytes: 262144,
    }));
  }
  if (url.includes("/files/tail")) {
    tailCalls++;
    const reqPath = new URL(url, "http://localhost").searchParams.get("path") ?? "/home/dev/app.log";
    const name = reqPath.split("/").pop() || "app.log";
    return Promise.resolve(json({
      path: reqPath,
      canonical_path: reqPath,
      name,
      size: 600000,
      mime: "text/plain; charset=utf-8",
      revision: `r-log-${tailCalls}`,
      text: `tail-batch-${tailCalls}\nline-end\n`,
      lines: 2,
      requested_lines: 200,
      bytes_read: 24,
      offset: 599976,
      truncated: true,
    }));
  }
  if (url.includes("/files/content") && method === "PUT") {
    if (writeMode === "conflict") {
      return Promise.resolve(json({ code: "file_changed", error: "file was modified externally" }, 409));
    }
    if (writeMode === "plain-409") {
      return Promise.resolve(json({ code: "exists", error: "target already exists" }, 409));
    }
    return Promise.resolve(json({
      path: "/home/dev/.env",
      canonical_path: "/home/dev/.env",
      name: ".env",
      size: 12,
      mode: "-rw-------",
      perm: 0o600,
      mtime: "2026-11-20T00:05:00Z",
      revision: "r-env-2",
      sha256: "sha-env-2",
      sensitive: true,
      sensitive_reason: "env",
    }));
  }
  if (url.includes("/files/create") && method === "POST") {
    const parsed = JSON.parse(body ?? "{}") as { dir?: string; name?: string; kind?: string; confirm_sensitive?: boolean };
    if (createRequire412Once && !parsed.confirm_sensitive) {
      createRequire412Once = false;
      return Promise.resolve(json({
        code: "machine_file_sensitive_confirm",
        error: "explicit confirmation is required to modify or delete a sensitive file",
        sensitive_reason: "protected",
      }, 412));
    }
    const base = parsed.dir ?? listDirPath;
    return Promise.resolve(json({
      entry: { name: parsed.name, path: `${base}/${parsed.name}`, kind: parsed.kind, size: 0, mode: "drwxr-xr-x", perm: 0o755 },
      canonical_path: `${base}/${parsed.name}`,
    }));
  }
  if (url.includes("/files/entry") && method === "PATCH") {
    const parsed = JSON.parse(body ?? "{}") as { path?: string; dest_path?: string };
    const dest = parsed.dest_path ?? "";
    const name = dest.split("/").pop() || "renamed";
    const isDir = (parsed.path ?? "").endsWith("/logs");
    return Promise.resolve(json({
      old_path: parsed.path,
      path: dest,
      canonical_path: dest,
      name,
      entry: { name, path: dest, kind: isDir ? "dir" : "file", size: isDir ? 4096 : 600000, mode: isDir ? "drwxr-xr-x" : "-rw-r--r--", perm: isDir ? 0o755 : 0o644 },
    }));
  }
  if (url.includes("/files/entry") && method === "DELETE") {
    return Promise.resolve(json({ path: "/home/dev/.env", canonical_path: "/home/dev/.env", kind: "file", deleted: true }));
  }
  return Promise.resolve(json({ error: "not found" }, 404));
}

class FakeXHR {
  static last: FakeXHR | null = null;
  method = "";
  url = "";
  headers: Record<string, string> = {};
  status = 0;
  responseText = "";
  aborted = false;
  upload: { onprogress: ((e: ProgressEvent) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onabort: (() => void) | null = null;
  onerror: (() => void) | null = null;

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
    FakeXHR.last = this;
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v;
  }
  send(_body: unknown) {}
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
}

beforeEach(() => {
  calls = [];
  tailCalls = 0;
  writeMode = "ok";
  listDirPath = "/home/dev";
  createRequire412Once = false;
  FakeXHR.last = null;
  setToken("tok-test");
  vi.stubGlobal("fetch", vi.fn(stubHub));
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("fileDiff helpers", () => {
  it("computes unified line diff, preserves CRLF/CR line endings, and classifies sensitive paths", () => {
    const diff = computeLineDiff("a\nb\nc\n", "a\nb-edited\nc\nd\n");
    expect(diff.changed).toBe(true);
    expect(diff.added).toBe(2);
    expect(diff.removed).toBe(1);
    expect(diff.hunks.length).toBe(1);

    // CRLF line ending detection, normalization, restoration, and minimal diff
    const crlfOriginal = "alpha\r\nbeta\r\ngamma\r\n";
    expect(detectLineEnding(crlfOriginal)).toBe("crlf");
    expect(detectLineEnding("a\rb\r")).toBe("cr");
    expect(detectLineEnding("a\nb\n")).toBe("lf");
    expect(detectLineEnding("alpha\r\nbeta\ngamma\n")).toBe("mixed");
    expect(detectLineEnding("a\rb\nc\n")).toBe("mixed");
    expect(detectLineEnding("a\r\nb\rc\r")).toBe("mixed");
    const lfDraft = normalizeToLF(crlfOriginal).replace("beta", "beta-2");
    const crlfRestored = restoreLineEnding(lfDraft, "crlf");
    expect(crlfRestored).toBe("alpha\r\nbeta-2\r\ngamma\r\n");
    const crlfDiff = computeLineDiff(crlfOriginal, crlfRestored);
    expect(crlfDiff.changed).toBe(true);
    expect(crlfDiff.added).toBe(1);
    expect(crlfDiff.removed).toBe(1);
    expect(crlfDiff.hunks[0]?.lines.map((l) => `${l.type}:${l.text}`)).toEqual([
      "context:alpha",
      "del:beta",
      "add:beta-2",
      "context:gamma",
    ]);

    // Line-ending-only changes are shown explicitly instead of hidden as identical lines.
    const eolOnlyDiff = computeLineDiff("a\r\nb\n", "a\nb\n");
    expect(eolOnlyDiff.changed).toBe(true);
    expect(eolOnlyDiff.hunks[0]?.lines.map((l) => `${l.type}:${l.text}`)).toEqual([
      "del:a\\r",
      "add:a",
      "context:b",
    ]);

    expect(classifySensitivePathClient(".env")).toEqual({ sensitive: true, reason: "env" });
    expect(classifySensitivePathClient(".env.example").sensitive).toBe(false);
    expect(classifySensitivePathClient("hub.json")).toEqual({ sensitive: true, reason: "hub_config" });
    expect(classifySensitivePathClient("id_rsa")).toEqual({ sensitive: true, reason: "private_key" });
    expect(classifySensitivePathClient("/home/dev/.aws/config")).toEqual({ sensitive: true, reason: "credentials" });
    expect(classifySensitivePathClient("/home/dev/.ssh/config")).toEqual({ sensitive: true, reason: "private_key" });
  });
});

describe("FileBrowserPanel", () => {
  const onlineMachines: Machine[] = [{ id: "m1", name: "DevBox", online: true }];

  it("loads roots and single-level listing, labels sensitive/binary files, previews, tails, and builds download links", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    expect(host.textContent).toContain("$HOME");
    expect(host.textContent).toContain("persona");
    expect(host.textContent).toContain(".config");

    const envRow = host.querySelector<HTMLButtonElement>('[data-testid="file-entry-.env"]');
    expect(envRow).not.toBeNull();
    await act(async () => { envRow!.click(); });
    await flush();
    expect(host.querySelector('[data-testid="sensitive-banner"]')).not.toBeNull();
    expect(host.querySelector('[data-testid="machine-file-content"]')?.textContent).toContain("DEMO_FLAG=1");
    const dl = host.querySelector<HTMLAnchorElement>('[data-testid="machine-file-download"]');
    expect(dl?.getAttribute("href")).toBe(machineFileDownloadURL("m1", "/home/dev/.env"));

    const binRow = host.querySelector<HTMLButtonElement>('[data-testid="file-entry-image.bin"]');
    await act(async () => { binRow!.click(); });
    await flush();
    expect(host.querySelector('[data-testid="binary-refusal"]')).not.toBeNull();
    expect(host.querySelector('[data-testid="machine-file-content"]')).toBeNull();

    const logRow = host.querySelector<HTMLButtonElement>('[data-testid="file-entry-app.log"]');
    await act(async () => { logRow!.click(); });
    await flush();
    expect(host.querySelector('[data-testid="machine-file-content"]')?.textContent).toContain("tail-batch-1");
    const refreshBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("刷新末尾") || b.textContent?.includes("Refresh tail"));
    expect(refreshBtn).toBeTruthy();
    await act(async () => { refreshBtn!.click(); });
    await flush();
    expect(host.querySelector('[data-testid="machine-file-content"]')?.textContent).toContain("tail-batch-2");
  });

  const findButton = (pattern: RegExp, scope: ParentNode = host): HTMLButtonElement => {
    const btn = [...scope.querySelectorAll("button")].find((b) => pattern.test(b.textContent ?? ""));
    expect(btn, `button matching ${pattern}`).toBeTruthy();
    return btn as HTMLButtonElement;
  };

  it("edits text with pre-save diff confirmation, sensitive warning gate, CAS tokens, and conflict handling", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    // Open .env and enter edit mode.
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-.env"]')!.click();
    });
    await flush();
    await act(async () => {
      findButton(/^(编辑|Edit)$/).click();
    });
    const editor = host.querySelector<HTMLTextAreaElement>('[data-testid="machine-file-editor"]');
    expect(editor).not.toBeNull();
    await act(async () => {
      setNativeValue(editor!, "DEMO_FLAG=2\n");
    });

    // Review diff before saving.
    await act(async () => {
      findButton(/查看差异|Review diff/).click();
    });
    const diffEl = host.querySelector('[data-testid="machine-file-diff"]')!;
    expect(diffEl.textContent).toContain("-DEMO_FLAG=1");
    expect(diffEl.textContent).toContain("+DEMO_FLAG=2");
    expect(host.querySelector('[data-testid="sensitive-write-warning"]')).not.toBeNull();

    // Save button stays disabled until sensitive checkbox is checked.
    const saveBtn = findButton(/确认保存|Confirm & Save/, diffEl);
    expect(saveBtn.disabled).toBe(true);
    await act(async () => {
      host.querySelector<HTMLInputElement>('[data-testid="sensitive-confirm-checkbox"]')!.click();
    });
    expect(saveBtn.disabled).toBe(false);

    // First attempt simulates 409 CAS conflict: draft stays preserved and conflict banner renders.
    writeMode = "conflict";
    await act(async () => { saveBtn.click(); });
    await flush();
    expect(host.querySelector('[data-testid="cas-conflict-banner"]')).not.toBeNull();
    expect(host.querySelector<HTMLTextAreaElement>('[data-testid="machine-file-editor"]')?.value).toBe("DEMO_FLAG=2\n");

    // Retry with clean CAS save succeeds with expected_mtime, expected_sha256, and confirm_sensitive.
    writeMode = "ok";
    await act(async () => {
      findButton(/查看差异|Review diff/).click();
    });
    await act(async () => {
      host.querySelector<HTMLInputElement>('[data-testid="sensitive-confirm-checkbox"]')!.click();
    });
    await act(async () => {
      findButton(/确认保存|Confirm & Save/, host.querySelector('[data-testid="machine-file-diff"]')!).click();
    });
    await flush();
    expect(host.querySelector('[data-testid="saved-notice"]')).not.toBeNull();
    const putCall = calls.filter((c) => c.method === "PUT" && c.url.includes("/files/content")).pop();
    expect(putCall).toBeTruthy();
    const sent = JSON.parse(putCall!.body ?? "{}") as Record<string, unknown>;
    expect(sent.expected_mtime).toBe("2026-11-20T00:00:00Z");
    expect(sent.expected_sha256).toBe("sha-env-1");
    expect(sent.confirm_sensitive).toBe(true);
  });

  it("supports create folder, sensitive delete confirmation, and streaming upload progress + abort", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    // Create folder
    await act(async () => { findButton(/新建文件夹|New folder/).click(); });
    const nameInput = host.querySelector<HTMLInputElement>('[data-testid="create-name-input"]')!;
    await act(async () => { setNativeValue(nameInput, "notes-dir"); });
    const createSubmit = [...host.querySelectorAll('[data-testid="create-entry-panel"] button')][0] as HTMLButtonElement;
    await act(async () => { createSubmit.click(); });
    await flush();
    expect(calls.some((c) => c.method === "POST" && c.url.includes("/files/create"))).toBe(true);

    // Sensitive delete requires explicit confirmation checkbox.
    const deleteEnvBtn = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label")?.includes(".env") && b.getAttribute("aria-label")?.match(/delete|删除/i));
    expect(deleteEnvBtn).toBeTruthy();
    await act(async () => { deleteEnvBtn!.click(); });
    const deletePanel = host.querySelector('[data-testid="delete-entry-panel"]')!;
    expect(host.querySelector('[data-testid="delete-sensitive-warning"]')).not.toBeNull();
    const delConfirm = findButton(/永久删除|Delete permanently/, deletePanel);
    expect(delConfirm.disabled).toBe(true);
    await act(async () => {
      host.querySelector<HTMLInputElement>('[data-testid="delete-sensitive-checkbox"]')!.click();
    });
    expect(delConfirm.disabled).toBe(false);
    await act(async () => { delConfirm.click(); });
    await flush();
    expect(calls.some((c) => c.method === "DELETE" && c.url.includes("/files/entry"))).toBe(true);

    // Streaming upload shows live progress and supports abort cleanup.
    const uploadInput = host.querySelector<HTMLInputElement>('[data-testid="machine-file-upload-input"]')!;
    const file = new File([new Uint8Array(1000)], "report.txt", { type: "text/plain" });
    await act(async () => {
      Object.defineProperty(uploadInput, "files", { configurable: true, value: [file] });
      uploadInput.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(FakeXHR.last?.method).toBe("POST");
    expect(FakeXHR.last?.url).toContain("/files/upload?dir=");
    expect(FakeXHR.last?.headers.Authorization).toBe("Bearer tok-test");

    await act(async () => {
      FakeXHR.last?.upload.onprogress?.({ lengthComputable: true, loaded: 500, total: 1000 } as ProgressEvent);
    });
    const uploadPanel = host.querySelector('[data-testid="upload-progress-panel"]')!;
    expect(uploadPanel.textContent).toContain("50%");

    await act(async () => {
      findButton(/取消上传|Cancel upload/, uploadPanel).click();
    });
    expect(FakeXHR.last?.aborted).toBe(true);
    expect(host.querySelector('[data-testid="upload-progress-panel"]')).toBeNull();
    expect(host.querySelector('[data-testid="dir-notice"]')).not.toBeNull();
  });

  it("classifies full joined path for sensitive directories on create and falls back on 412 sensitive confirm", async () => {
    // 1. Inside a sensitive directory (~/.aws), even a plain name like "config" triggers sensitive confirmation.
    listDirPath = "/home/dev/.aws";
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    await act(async () => { findButton(/新建文件|New file/).click(); });
    const nameInput = host.querySelector<HTMLInputElement>('[data-testid="create-name-input"]')!;
    await act(async () => { setNativeValue(nameInput, "config"); });

    const createPanel = host.querySelector('[data-testid="create-entry-panel"]')!;
    const createBtn = findButton(/^(创建|Create)$/, createPanel);
    const sensBox = host.querySelector<HTMLInputElement>('[data-testid="create-sensitive-checkbox"]');
    expect(sensBox).not.toBeNull();
    expect(createBtn.disabled).toBe(true);

    await act(async () => { sensBox!.click(); });
    expect(createBtn.disabled).toBe(false);
    await act(async () => { createBtn.click(); });
    await flush();

    const firstCreate = calls.filter((c) => c.method === "POST" && c.url.includes("/files/create")).pop();
    expect(firstCreate).toBeTruthy();
    expect(JSON.parse(firstCreate!.body ?? "{}")).toMatchObject({
      dir: "/home/dev/.aws",
      name: "config",
      kind: "file",
      confirm_sensitive: true,
    });

    // 2. Non-sensitive directory where server responds 412 machine_file_sensitive_confirm surfaces checkbox & retries cleanly.
    listDirPath = "/home/dev/custom";
    createRequire412Once = true;
    await act(async () => {
      const refreshBtn = host.querySelector<HTMLButtonElement>('button[aria-label*="刷新"], button[aria-label*="Refresh"]');
      refreshBtn!.click();
    });
    await flush();

    await act(async () => { findButton(/新建文件|New file/).click(); });
    const nameInput2 = host.querySelector<HTMLInputElement>('[data-testid="create-name-input"]')!;
    await act(async () => { setNativeValue(nameInput2, "vault.txt"); });
    const createPanel2 = host.querySelector('[data-testid="create-entry-panel"]')!;
    expect(host.querySelector('[data-testid="create-sensitive-checkbox"]')).toBeNull();

    // First submit triggers 412 -> surfaces confirmation checkbox without leaving a hard error alert.
    await act(async () => { findButton(/^(创建|Create)$/, createPanel2).click(); });
    await flush();
    expect(host.querySelector('[data-testid="dir-error"]')).toBeNull();
    const fallbackBox = host.querySelector<HTMLInputElement>('[data-testid="create-sensitive-checkbox"]');
    expect(fallbackBox).not.toBeNull();
    const retryBtn = findButton(/^(创建|Create)$/, host.querySelector('[data-testid="create-entry-panel"]')!);
    expect(retryBtn.disabled).toBe(true);

    await act(async () => { fallbackBox!.click(); });
    expect(retryBtn.disabled).toBe(false);
    await act(async () => { retryBtn.click(); });
    await flush();

    const retryCreate = calls.filter((c) => c.method === "POST" && c.url.includes("/files/create")).pop();
    expect(JSON.parse(retryCreate!.body ?? "{}")).toMatchObject({
      dir: "/home/dev/custom",
      name: "vault.txt",
      kind: "file",
      confirm_sensitive: true,
    });
  });

  it("preserves CRLF line endings across textarea edit, minimal diff preview, and PUT save", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-win.ini"]')!.click();
    });
    await flush();

    await act(async () => { findButton(/^(编辑|Edit)$/).click(); });
    const editor = host.querySelector<HTMLTextAreaElement>('[data-testid="machine-file-editor"]')!;
    // Textarea receives LF-normalized text, and Review diff stays disabled while unchanged.
    expect(editor.value).toBe("alpha\nbeta\ngamma\n");
    expect(findButton(/查看差异|Review diff/).disabled).toBe(true);

    // Editing only line 2 produces a +1 / -1 diff (not rewriting all lines) and saves CRLF back to server.
    await act(async () => { setNativeValue(editor, "alpha\nbeta-2\ngamma\n"); });
    await act(async () => { findButton(/查看差异|Review diff/).click(); });

    const diffEl = host.querySelector('[data-testid="machine-file-diff"]')!;
    expect(diffEl.textContent).toContain("+1 / -1");
    expect(diffEl.textContent).toContain("-beta");
    expect(diffEl.textContent).toContain("+beta-2");
    expect(diffEl.textContent).not.toContain("-alpha");
    expect(diffEl.textContent).not.toContain("-gamma");

    await act(async () => { findButton(/确认保存|Confirm & Save/, diffEl).click(); });
    await flush();

    const putCall = calls.filter((c) => c.method === "PUT" && c.url.includes("/files/content")).pop();
    expect(putCall).toBeTruthy();
    expect(JSON.parse(putCall!.body ?? "{}")).toMatchObject({
      path: "/home/dev/win.ini",
      text: "alpha\r\nbeta-2\r\ngamma\r\n",
      expected_mtime: "2026-11-20T00:00:00Z",
      expected_sha256: "sha-win-1",
    });
  });

  it("disables inline editing and displays a notice for files with mixed line endings", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-mixed.txt"]')!.click();
    });
    await flush();

    const editBtn = findButton(/^(编辑|Edit)$/);
    expect(editBtn.disabled).toBe(true);
    expect(host.querySelector('[data-testid="mixed-eol-notice"]')).not.toBeNull();
  });

  it("does not hijack directory navigation on upload completion and clears stale pendingUpload on navigation", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    // 1. Start upload in /home/dev, navigate into /home/dev/logs before upload completes, then finish XHR.
    const uploadInput = host.querySelector<HTMLInputElement>('[data-testid="machine-file-upload-input"]')!;
    const file1 = new File([new Uint8Array(100)], "big.bin", { type: "application/octet-stream" });
    await act(async () => {
      Object.defineProperty(uploadInput, "files", { configurable: true, value: [file1] });
      uploadInput.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const firstXhr = FakeXHR.last!;
    expect(firstXhr.url).toContain("dir=%2Fhome%2Fdev");

    // Navigate into /home/dev/logs while upload is still in flight.
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-logs"]')!.click();
    });
    await flush();
    const listCallsBeforeDone = calls.filter((c) => c.url.includes("/files/list")).length;

    // Upload completes in the background: must NOT reload /home/dev or snap the user back from /home/dev/logs.
    await act(async () => {
      firstXhr.status = 200;
      firstXhr.responseText = JSON.stringify({ size: 100 });
      firstXhr.onload?.();
    });
    await flush();
    expect(calls.filter((c) => c.url.includes("/files/list")).length).toBe(listCallsBeforeDone);
    expect(host.querySelector<HTMLInputElement>('input[aria-label]')?.value).toBe("/home/dev/logs");

    // 2. Trigger 409 exists prompt in /home/dev/logs, confirm overwrite stays pinned to /home/dev/logs.
    const file2 = new File([new Uint8Array(64)], "dup.txt", { type: "text/plain" });
    await act(async () => {
      Object.defineProperty(uploadInput, "files", { configurable: true, value: [file2] });
      uploadInput.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const conflictXhr = FakeXHR.last!;
    await act(async () => {
      conflictXhr.status = 409;
      conflictXhr.responseText = JSON.stringify({ code: "exists", error: "already exists" });
      conflictXhr.onload?.();
    });
    const confirmPanel = host.querySelector('[data-testid="upload-confirm-panel"]');
    expect(confirmPanel).not.toBeNull();

    // Clicking Overwrite while still in /home/dev/logs targets /home/dev/logs with overwrite=1.
    await act(async () => {
      findButton(/覆盖上传|Overwrite/, confirmPanel!).click();
    });
    expect(FakeXHR.last?.url).toContain("dir=%2Fhome%2Fdev%2Flogs");
    expect(FakeXHR.last?.url).toContain("overwrite=1");

    // Trigger another 409 prompt, then navigate to another directory: pendingUpload must clear immediately.
    await act(async () => {
      FakeXHR.last!.status = 409;
      FakeXHR.last!.responseText = JSON.stringify({ code: "exists", error: "already exists" });
      FakeXHR.last!.onload?.();
    });
    expect(host.querySelector('[data-testid="upload-confirm-panel"]')).not.toBeNull();

    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-logs"]')!.click();
    });
    await flush();
    expect(host.querySelector('[data-testid="upload-confirm-panel"]')).toBeNull();
  });

  it("surfaces non-CAS HTTP 409 errors normally, refreshes current dir on child dir rename, reloads tail on file rename, and preserves overwrite across 412", async () => {
    act(() => root.render(<FileBrowserPanel machines={onlineMachines} />));
    await flush();

    // 1. Non-CAS HTTP 409 (code !== "file_changed") displays server error instead of CAS conflict banner.
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-win.ini"]')!.click();
    });
    await flush();
    await act(async () => { findButton(/^(编辑|Edit)$/).click(); });
    const editor = host.querySelector<HTMLTextAreaElement>('[data-testid="machine-file-editor"]')!;
    await act(async () => { setNativeValue(editor, "alpha\nbeta-plain409\ngamma\n"); });
    await act(async () => { findButton(/查看差异|Review diff/).click(); });
    writeMode = "plain-409";
    await act(async () => {
      findButton(/确认保存|Confirm & Save/, host.querySelector('[data-testid="machine-file-diff"]')!).click();
    });
    await flush();
    expect(host.querySelector('[data-testid="cas-conflict-banner"]')).toBeNull();
    expect(host.textContent).toContain("target already exists");
    writeMode = "ok";
    await act(async () => {
      findButton(/确认保存|Confirm & Save/, host.querySelector('[data-testid="machine-file-diff"]')!).click();
    });
    await flush();
    expect(host.querySelector('[data-testid="cas-conflict-banner"]')).toBeNull();
    expect(host.querySelector('[data-testid="saved-notice"]')).not.toBeNull();

    // 2. Renaming a file currently open in tail mode reloads tail from the new path.
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="file-entry-app.log"]')!.click();
    });
    await flush();
    expect(host.querySelector('[data-testid="machine-file-content"]')?.textContent).toContain("tail-batch-1");
    const renameLogBtn = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label")?.includes("app.log") && b.getAttribute("aria-label")?.match(/rename|重命名/i));
    expect(renameLogBtn).toBeTruthy();
    await act(async () => { renameLogBtn!.click(); });
    const renameInput = host.querySelector<HTMLInputElement>('[data-testid="rename-dest-input"]')!;
    await act(async () => { setNativeValue(renameInput, "/home/dev/app-rotated.log"); });
    await act(async () => {
      findButton(/重命名|Rename/, host.querySelector('[data-testid="rename-entry-panel"]')!).click();
    });
    await flush();
    expect(calls.some((c) => c.url.includes("/files/tail?path=%2Fhome%2Fdev%2Fapp-rotated.log"))).toBe(true);
    expect(host.querySelector('[data-testid="machine-file-content"]')?.textContent).toContain("tail-batch-2");

    // 3. Renaming a child directory refreshes the current listing (/home/dev) instead of jumping into the moved directory.
    const listCountBeforeDirRename = calls.filter((c) => c.url.includes("/files/list")).length;
    const renameDirBtn = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label")?.includes("logs") && b.getAttribute("aria-label")?.match(/rename|重命名/i));
    expect(renameDirBtn).toBeTruthy();
    await act(async () => { renameDirBtn!.click(); });
    const renameDirInput = host.querySelector<HTMLInputElement>('[data-testid="rename-dest-input"]')!;
    await act(async () => { setNativeValue(renameDirInput, "/home/dev/logs-archived"); });
    await act(async () => {
      findButton(/重命名|Rename/, host.querySelector('[data-testid="rename-entry-panel"]')!).click();
    });
    await flush();
    const listCallsAfterDirRename = calls.filter((c) => c.url.includes("/files/list")).slice(listCountBeforeDirRename);
    expect(listCallsAfterDirRename.some((c) => c.url.includes("/files/list?path=%2Fhome%2Fdev"))).toBe(true);
    expect(listCallsAfterDirRename.some((c) => c.url.includes("logs-archived"))).toBe(false);
    expect(host.querySelector<HTMLInputElement>('input[aria-label]')?.value).toBe("/home/dev");

    // 4. Upload 409 exists -> Overwrite (overwrite=1) -> 412 sensitive confirm -> final retry keeps BOTH overwrite=1 and confirm_sensitive=1.
    const uploadInput = host.querySelector<HTMLInputElement>('[data-testid="machine-file-upload-input"]')!;
    const file = new File([new Uint8Array(64)], "vault.txt", { type: "text/plain" });
    await act(async () => {
      Object.defineProperty(uploadInput, "files", { configurable: true, value: [file] });
      uploadInput.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      FakeXHR.last!.status = 409;
      FakeXHR.last!.responseText = JSON.stringify({ code: "exists", error: "already exists" });
      FakeXHR.last!.onload?.();
    });
    await act(async () => {
      findButton(/覆盖上传|Overwrite/, host.querySelector('[data-testid="upload-confirm-panel"]')!).click();
    });
    expect(FakeXHR.last?.url).toContain("overwrite=1");

    await act(async () => {
      FakeXHR.last!.status = 412;
      FakeXHR.last!.responseText = JSON.stringify({ code: "machine_file_sensitive_confirm", sensitive_reason: "protected" });
      FakeXHR.last!.onload?.();
    });
    const sensCheckbox = host.querySelector<HTMLInputElement>('[data-testid="upload-sensitive-checkbox"]')!;
    expect(sensCheckbox).not.toBeNull();
    await act(async () => { sensCheckbox.click(); });
    await act(async () => {
      findButton(/确认上传|Start upload/, host.querySelector('[data-testid="upload-confirm-panel"]')!).click();
    });
    expect(FakeXHR.last?.url).toContain("overwrite=1");
    expect(FakeXHR.last?.url).toContain("confirm_sensitive=1");
  });

  it("keeps a refused workspace directory as the retry target without showing an empty folder", async () => {
    const get = vi.fn(async (input: RequestInfo | URL) => String(input).includes("/files/list")
      ? json({ code: "forbidden", error: "Access denied" }, 403)
      : json({ roots: [] }));
    vi.stubGlobal("fetch", get);
    await act(async () => root.render(<FileBrowserPanel machine={{ id: "b", name: "Computer B", online: true }} path="/projects/quiet" routed />));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Access denied");
    expect(host.querySelector('[data-files-panel]')).toBeNull();
    expect(host.textContent).not.toContain(t("machines-files.empty_dir"));
    const refresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("machines-files.refresh")}"]`)!;
    await act(async () => refresh.click());
    expect(get.mock.calls.filter(([url]) => String(url).endsWith("/machines/b/files/list?path=%2Fprojects%2Fquiet"))).toHaveLength(2);
  });

  it("shows offline notice without issuing file requests when selected machine is offline", async () => {
    act(() => root.render(<FileBrowserPanel machines={[{ id: "m2", name: "OfflineBox", online: false }]} />));
    await flush();
    expect(calls).toEqual([]);
    expect(host.querySelector('[data-testid="machine-file-browser"]')?.textContent).toBeTruthy();
  });
});
