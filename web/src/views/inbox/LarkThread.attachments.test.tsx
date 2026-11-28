// @vitest-environment jsdom
// Pictures and files in the Feishu chat's composer (#2902, docs/extensions/lark-attachments.md): offered only when the hub's
// extension sends them, sent after the text through the hub's own routes, and kept in the box when they quote a message
// the extension cannot route.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ features: [] as string[] }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  const state = () => ({ ...m.getState(), features: hub.features });
  return { ...m, getState: state, useStore: <T,>(select: (s: ReturnType<typeof state>) => T) => select(state()) };
});
const toasts = vi.hoisted(() => [] as string[]);
vi.mock("../../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../../ui/atoms")>()),
  toast: (text: string) => void toasts.push(text),
}));

import { LarkThread } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const calls: { method: string; path: string; body: unknown }[] = [];

beforeEach(() => {
  calls.length = 0;
  toasts.length = 0;
  // the box keeps a chat's draft (lib/lark-chat.ts): one test's text is not the next one's
  localStorage.clear();
  URL.createObjectURL = vi.fn(() => "blob:test/1");
  URL.revokeObjectURL = vi.fn();
  vi.stubGlobal("IntersectionObserver", class { observe() {} disconnect() {} });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    const path = url.replace(/^.*\/api\/v1/, "");
    const method = init.method ?? "GET";
    calls.push({ method, path, body: init.body });
    if (method === "GET") return Response.json({ messages: [{ id: "om_1", position: "1", text: "你好", sender_name: "示例甲", sender_id: "u1" }] });
    if (path.includes("/files")) return Response.json({ message_id: "om_file" });
    return Response.json({ message_id: "om_text" });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

const open = async () => {
  await act(async () => root.render(<LarkThread chat={{ feed: "oc_1", title: "示例群" }} />));
  await settle();
};

const pick = async (f: File) => {
  const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
  Object.defineProperty(input, "files", { value: [f], configurable: true });
  await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); });
  await settle();
};

const type = async (text: string) => {
  const box = host.querySelector("textarea")!;
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const send = async () => {
  const button = [...host.querySelectorAll("button")].find((b) => b.textContent === "发送")!;
  await act(async () => button.click());
  await settle();
};

const posts = () => calls.filter((c) => c.method === "POST" && !c.path.includes("/read"));

describe("飞书聊天发附件", () => {
  it("hub 的扩展不发附件时没有附件按钮", async () => {
    hub.features = ["lark"];
    await open();
    expect(host.querySelector('[aria-label="添加图片或文件"]')).toBeNull();
    expect(host.querySelector('input[type="file"]')).toBeNull();
  });

  it("先发文字，再把文件原样传上去，输入框清空", async () => {
    hub.features = ["lark", "lark_attachments"];
    await open();
    expect(host.querySelector('[aria-label="添加图片或文件"]')).not.toBeNull();
    const f = new File(["报告内容"], "报告.pdf", { type: "application/pdf" });
    await pick(f);
    expect(host.textContent).toContain("报告.pdf");
    await type("看下这个");
    await send();
    const sent = posts();
    expect(sent.map((c) => c.path.split("?")[0])).toEqual(["/lark/conversations/oc_1/messages", "/lark/conversations/oc_1/files"]);
    expect(JSON.parse(String(sent[0].body))).toMatchObject({ text: "看下这个" });
    const q = new URL(sent[1].path, "http://hub").searchParams;
    expect(q.get("name")).toBe("报告.pdf");
    expect(q.get("mime")).toBe("application/pdf");
    expect(q.get("client_id")).toBeTruthy();
    expect(sent[1].body).toBe(f);
    expect(host.querySelector("textarea")!.value).toBe("");
    expect(host.querySelector('[aria-label="移除 报告.pdf"]')).toBeNull();
  });

  it("引用消息时扩展不支持带附件：什么都不发，内容留在输入框", async () => {
    hub.features = ["lark", "lark_attachments"];
    await open();
    await act(async () => host.querySelector<HTMLButtonElement>('button[title="回复"]')!.click());
    await pick(new File(["x"], "表格.csv", { type: "text/csv" }));
    await type("附件在这");
    await send();
    expect(posts()).toEqual([]);
    expect(toasts).toContain("请先更新 hub 上的飞书扩展，即可在引用消息时发送图片和文件。");
    expect(host.querySelector("textarea")!.value).toBe("附件在这");
    expect(host.textContent).toContain("表格.csv");
  });

  it("扩展支持时附件带上引用", async () => {
    hub.features = ["lark", "lark_attachments", "lark_attachment_reply"];
    await open();
    await act(async () => host.querySelector<HTMLButtonElement>('button[title="回复"]')!.click());
    await pick(new File(["x"], "表格.csv", { type: "text/csv" }));
    await send();
    const sent = posts();
    expect(sent).toHaveLength(1);
    const q = new URL(sent[0].path, "http://hub").searchParams;
    expect(q.get("reply_to")).toBe("om_1");
    expect(q.has("thread_root")).toBe(false);
  });

  it("粘贴文件加进输入框", async () => {
    hub.features = ["lark", "lark_attachments"];
    await open();
    const box = host.querySelector("textarea")!;
    const paste = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    Object.defineProperty(paste, "clipboardData", { value: { files: [new File(["x"], "粘贴.txt", { type: "text/plain" })] } });
    await act(async () => { box.dispatchEvent(paste); });
    await settle();
    expect(host.textContent).toContain("粘贴.txt");
  });
});
