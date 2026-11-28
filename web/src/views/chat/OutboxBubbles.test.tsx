// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { initI18n, t } from "../../i18n";
import type { OutboxEntry } from "../../lib/outbox";
import { dedupeQueuedOutbox, OutboxBubbles } from "./OutboxBubbles";

function entry(over: Partial<OutboxEntry> & { id: string; text: string }): OutboxEntry {
  return {
    images: [],
    at: 1_700_000_000_000,
    state: "sent",
    ...over,
  };
}

describe("dedupeQueuedOutbox", () => {
  it("removes non-failed outbox entries represented in Session.queued (exact and truncated agy previews) 1-to-1", () => {
    const entries: OutboxEntry[] = [
      entry({ id: "e1", text: "Also run the linter at the end" }),
      entry({ id: "e2", text: "Also run the linter at the end" }),
      entry({ id: "e3", text: "Check the screenshot", images: [{ mime: "image/png", data: "AAAA" }] }),
      entry({ id: "e4", text: "Failed message", state: "failed", error: "refused" }),
    ];
    const kept = dedupeQueuedOutbox(entries, [
      "Also run the linter...",
      "Check the screenshot",
      "Failed message",
    ]);
    expect(kept.map((e) => e.id)).toEqual(["e2", "e4"]);
  });

  it("keeps image-only outbox entries unless a queued item also carries matching photos", () => {
    const photoOnly = entry({ id: "img1", text: "", images: [{ mime: "image/png", data: "AAAA" }] });
    expect(dedupeQueuedOutbox([photoOnly], ["Some unrelated text"]).map((e) => e.id)).toEqual(["img1"]);
    expect(dedupeQueuedOutbox([photoOnly], ["<attached_images>\n/tmp/a.png\n</attached_images>"])).toEqual([]);
  });
});

describe("OutboxBubbles", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    document.body.innerHTML = "";
  });

  it("hides queued outbox entries while keeping non-queued and failed bubbles visible", () => {
    const entries: OutboxEntry[] = [
      entry({ id: "q1", text: "Run the test suite after this" }),
      entry({ id: "other", text: "Separate message" }),
      entry({ id: "f1", text: "Run the test suite after this", state: "failed", error: "busy" }),
    ];
    act(() => root.render(<OutboxBubbles mid="m" sid="s" entries={entries} queued={["Run the test suite..."]} />));
    const rendered = [...host.querySelectorAll<HTMLElement>("[data-outbox]")].map((el) => el.dataset.outbox);
    expect(rendered).toEqual(["other", "f1"]);
  });

  it.each([
    { lang: "en", label: "Open terminal", reason: "A dialog is open on the computer: “Choose a workspace”. Deal with it, then send" },
    { lang: "zh-Hans", label: "打开终端", reason: "电脑上开着对话框「Choose a workspace」，先处理它再发" },
    { lang: "ja", label: "ターミナルを開く", reason: "コンピューターでダイアログ「Choose a workspace」が開いています。先に対応してから送信してください" },
  ] as const)("shows the named refusal and action in $lang", async ({ lang, label, reason }) => {
    await initI18n(lang);
    const failed = entry({ id: "f1", text: "Go on", state: "failed", code: "dialog_open", dialog: "Choose a workspace", dialogMachine: "box", dialogSession: "agent" });
    act(() => root.render(<OutboxBubbles mid="m" sid="s" entries={[failed]} />));
    const alert = host.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain(reason);
    expect([...alert!.querySelectorAll("button")].map((button) => button.textContent)).toEqual([
      label, t("chat-outboxbubbles.retry"), t("chat-outboxbubbles.delete"),
    ]);
  });

  it.each([
    { name: "missing target", patch: {} },
    { name: "partial target", patch: { dialogMachine: "box" } },
    { name: "uncertain delivery", patch: { dialogMachine: "box", dialogSession: "agent", inDoubt: true } },
  ])("omits Open terminal for $name", async ({ patch }) => {
    await initI18n("en");
    const failed = entry({ id: "f1", text: "Go on", state: "failed", code: "dialog_open", ...patch });
    act(() => root.render(<OutboxBubbles mid="m" sid="s" entries={[failed]} />));
    expect(host.textContent).not.toContain("Open terminal");
  });
});
