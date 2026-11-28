// @vitest-environment jsdom
// The AI twin's draft cut at four lines opens the rest in place (#4742), so the owner reads it before "Send".
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import { DraftCard } from "./InboxParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
let tall = 0;
const desc = { scroll: Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight"), client: Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight") };

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", { configurable: true, get: () => tall });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 80 });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  if (desc.scroll) Object.defineProperty(HTMLElement.prototype, "scrollHeight", desc.scroll);
  if (desc.client) Object.defineProperty(HTMLElement.prototype, "clientHeight", desc.client);
});

const item = (draft: string): InboxItem =>
  ({ id: "mail:1", source: "mail", kind: "mail", account: "a", account_name: "Mail", title: "周会", preview: "来吗", count: 1, agent: { state: "drafted", draft } }) as InboxItem;

registerTestMailbox();

describe("DraftCard expand (#4742)", () => {
  it("offers 展开全文 when four lines cut the draft, and opens it in place", () => {
    tall = 200;
    const onEdit = vi.fn();
    const onSend = vi.fn();
    act(() => root.render(<DraftCard it={item("第一行\n第二行\n第三行\n第四行\n第五行：周五不行")} onEdit={onEdit} onSend={onSend} />));
    const text = host.querySelector("[data-draft-text]")!;
    expect(text.className).toContain("line-clamp-4");
    const button = [...host.querySelectorAll("button")].find((b) => b.textContent === "展开全文")!;
    expect(button.getAttribute("aria-expanded")).toBe("false");
    act(() => button.click());
    expect(text.className).not.toContain("line-clamp-4");
    expect(button.textContent).toBe("收起");
    expect(button.getAttribute("aria-expanded")).toBe("true");
    act(() => button.click());
    expect(text.className).toContain("line-clamp-4");
    // toggling only folds the text: it neither sends nor opens the editor
    expect(onSend).not.toHaveBeenCalled();
    expect(onEdit).not.toHaveBeenCalled();
  });

  it("has no expander when the draft fits", () => {
    tall = 80;
    act(() => root.render(<DraftCard it={item("好的")} onEdit={() => {}} onSend={() => {}} />));
    expect([...host.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["修改", "发送"]);
  });
});
