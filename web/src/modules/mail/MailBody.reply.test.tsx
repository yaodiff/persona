// @vitest-environment jsdom
// A reply being written by hand survives the mail under it changing (#4563): a new mail of the thread moves the item
// to that mail, and the body reads it again, which took the dialog down with what was typed.
import "./test-register";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import type { MailMessage } from "./mail";

const answers = new Map<string, (v: unknown) => void>();
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn((path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "acc", email: "me@example.com" }] });
    if (path.includes("/flags")) return Promise.resolve({});
    return new Promise((resolve) => answers.set(path.replace(/\?.*$/, ""), resolve));
  }),
}));
vi.mock("../../lib/translate", async (orig) => ({ ...(await orig<typeof import("../../lib/translate")>()), useTranslatePrefs: () => ({ enabled: false, target: "zh-Hans" }) }));

import { FullMailBody as MailBody } from "./FullMailBody";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// each test its own thread: the dialog keeps drafts in the module, by item
let thread = 0;
const item = (uids: number[]): InboxItem =>
  ({ id: `mail:acc:t${thread}:1`, source: "mail", kind: "mail", account: "acc", title: "周会", count: uids.length, tier: "action", version: `v${uids.length}`, read: true,
    mail: { account_id: "acc", mailbox: "INBOX", uids, from: { email: "a@example.com" } } }) as InboxItem;
const mail = (uid: number): MailMessage => ({ uid, subject: "周会", from: { email: "a@example.com" }, text: `第 ${uid} 封`, message_id: `<m${uid}@example.com>` }) as MailMessage;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  thread++;
  answers.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const answer = (uid: number) => act(async () => answers.get(`/mail/messages/acc/${uid}`)!({ message: mail(uid) }));
const body = () => document.querySelector<HTMLTextAreaElement>('textarea[aria-label="正文"]');
const typeInto = (el: HTMLTextAreaElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const clickText = (text: string) =>
  act(() => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!.click());

describe("a reply written by hand", () => {
  it("stays when a new mail of the thread arrives while it is open", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    typeInto(body()!, "好的，周四见");
    act(() => root.render(<MailBody item={item([1, 2])} />)); // the next inbox push: the thread has mail 2 now
    await answer(2);
    expect(body()?.value).toBe("好的，周四见");
  });

  it("is kept for the next 回复 when the body goes away while it is open", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    typeInto(body()!, "先记下");
    act(() => root.render(<div />)); // another item opened, or the page left
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    expect(body()?.value).toBe("先记下");
  });

  it("is dropped by 取消, also when the body goes away after", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    typeInto(body()!, "算了");
    clickText("取消");
    act(() => root.render(<div />));
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    expect(body()?.value).toContain("> 第 1 封");
    expect(body()?.value).not.toContain("算了");
  });

  it("is not kept once sent, also when the body went away while it was on its way", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    typeInto(body()!, "收到");
    clickText("发送");
    act(() => root.render(<div />));
    await act(async () => answers.get("/mail/send")!({}));
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    expect(body()?.value).not.toContain("收到");
  });

  it("keeps the one being written over the item's own, for another item opened in the same body", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    typeInto(body()!, "给第一个线程");
    const other = { ...item([5]), id: "mail:acc:other:5" } as InboxItem;
    act(() => root.render(<MailBody item={other} />));
    expect(body()).toBeNull(); // no reply to the first thread over the other mail
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    expect(body()?.value).toBe("给第一个线程");
  });

  it("is kept when its send fails after the body went away", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    typeInto(body()!, "再试一次");
    clickText("发送");
    act(() => root.render(<div />));
    await act(async () => answers.get("/mail/send")!(Promise.reject(new Error("offline"))));
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    expect(body()?.value).toBe("再试一次");
  });

  it("stays open, the same dialog, while the new mail is read", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1);
    clickText("回复");
    const el = body()!;
    act(() => root.render(<MailBody item={item([1, 2])} />));
    expect(body()).toBe(el); // not taken down under the loading state
  });
});
