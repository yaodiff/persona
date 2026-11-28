// @vitest-environment jsdom
// A logical conversation's input that has not gone in (#17431): the bubble says why and how long it still waits, an
// expired one offers Resend and Ignore (each runs once however often it is tapped), and the banner over the composer
// sums it up. Never an idle chat that swallowed the message.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { UndeliveredMessage } from "../../lib/types";

const actions = vi.hoisted(() => ({
  retry: vi.fn<(mid: string, sid: string, m: { id: string }) => Promise<void>>(),
  remove: vi.fn<(mid: string, sid: string, m: { id: string }) => Promise<void>>(),
}));
vi.mock("../../lib/undelivered-actions", () => ({ retryUndelivered: actions.retry, deleteUndelivered: actions.remove }));
vi.mock("./SegmentView", () => ({
  UserBubble: ({ msg, onDelete }: { msg: { text: string }; onDelete?: () => void }) => (
    <div data-bubble={msg.text} data-deletable={onDelete ? "" : undefined} />
  ),
}));
vi.mock("../../ui/atoms", () => ({ toast: vi.fn() }));

const { DeliveryBanner, UndeliveredBubble } = await import("./UndeliveredBubbles");

const now = Date.parse("2026-11-27T12:00:00Z");
const at = new Date(now).toISOString();
const head: UndeliveredMessage = { id: "head", text: "first words", at, state: "waiting", reason: "awaiting_receipt", error: "awaiting line", since: at, deadline: new Date(now + 90_000).toISOString(), behind: 2 };
const behind: UndeliveredMessage = { id: "q1", text: "second words", at, state: "waiting", reason: "behind_unconfirmed", error: "behind line", blocked_by: "head", ahead: 1 };
const expired: UndeliveredMessage = { id: "old", text: "lost words", at, state: "failed", reason: "receipt_expired", error: "expired line" };

describe("logical delivery queue in the chat", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval", "setTimeout", "clearTimeout"] });
    vi.setSystemTime(now);
    actions.retry.mockReset();
    actions.remove.mockReset();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    vi.useRealTimers();
  });

  const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent);
  const button = (label: string, scope: ParentNode = host) => [...scope.querySelectorAll("button")].find((b) => b.textContent === label)!;

  it("counts down while its receipt is awaited, with nothing to delete or retry", () => {
    act(() => root.render(<UndeliveredBubble mid="m" sid="s" m={head} />));
    const line = host.querySelector('[role="status"]')!.textContent!;
    expect(line).toContain("awaiting line");
    expect(line).toContain(t("chat-undeliveredbubbles.time_left", { seconds: 90 }));
    expect(buttons()).toEqual([]);
    expect(host.querySelector("[data-deletable]")).toBeNull();
    act(() => vi.advanceTimersByTime(30_000));
    expect(host.querySelector('[role="status"]')!.textContent).toContain(t("chat-undeliveredbubbles.time_left", { seconds: 60 }));
    act(() => root.render(<UndeliveredBubble mid="m" sid="s" m={behind} />));
    expect(host.querySelector('[data-undelivered-reason="behind_unconfirmed"]')).not.toBeNull();
    expect(buttons()).toEqual([]);
  });

  it("offers Resend and Ignore once it expired, each sent once however often it is tapped", async () => {
    let finish!: () => void;
    actions.retry.mockImplementation(() => new Promise<void>((done) => { finish = done; }));
    actions.remove.mockResolvedValue();
    act(() => root.render(<UndeliveredBubble mid="m" sid="s" m={expired} />));
    expect(host.querySelector('[role="alert"]')!.textContent).toContain("expired line");
    expect(buttons()).toEqual([t("chat-undeliveredbubbles.resend"), t("chat-undeliveredbubbles.ignore")]);
    const resend = button(t("chat-undeliveredbubbles.resend"));
    act(() => { resend.click(); resend.click(); resend.click(); });
    expect(actions.retry).toHaveBeenCalledTimes(1);
    expect(actions.retry.mock.calls[0]![2].id).toBe("old");
    await act(async () => { finish(); await Promise.resolve(); });
    await act(async () => { button(t("chat-undeliveredbubbles.ignore")).click(); await Promise.resolve(); });
    expect(actions.remove).toHaveBeenCalledTimes(1);
  });

  it("sums up the queue over the composer and acts on expired inputs from there", async () => {
    actions.remove.mockResolvedValue();
    act(() => root.render(<DeliveryBanner mid="m" sid="s" list={[expired, head, behind]} />));
    const status = host.querySelector('[data-delivery-status="awaiting"]')!.textContent!;
    expect(status).toContain(t("chat-undeliveredbubbles.banner_awaiting", { seconds: 90 }));
    expect(status).toContain(t("chat-undeliveredbubbles.banner_behind", { count: 2 }));
    const alert = host.querySelector('[role="alert"]')!;
    expect(alert.textContent).toContain(t("chat-undeliveredbubbles.banner_expired", { count: 1 }));
    expect(alert.querySelector('[data-delivery-expired="old"]')!.textContent).toContain("lost words");
    await act(async () => { button(t("chat-undeliveredbubbles.ignore"), alert).click(); await Promise.resolve(); });
    expect(actions.remove).toHaveBeenCalledTimes(1);
    expect(actions.remove.mock.calls[0]![2].id).toBe("old");
    // past the deadline: it says the input leaves the queue now, not a countdown stuck at zero
    act(() => vi.advanceTimersByTime(91_000));
    expect(host.querySelector('[data-delivery-status="awaiting"]')!.textContent).toContain(t("chat-undeliveredbubbles.banner_awaiting_now"));
  });

  it("draws nothing for a chat without logical input on its way", () => {
    act(() => root.render(<DeliveryBanner mid="m" sid="s" list={[{ id: "k", text: "kept", at, state: "failed" }]} />));
    expect(host.innerHTML).toBe("");
    act(() => root.render(<DeliveryBanner mid="m" sid="s" list={[{ ...behind, blocked_by: undefined, reason: "queued" }]} />));
    expect(host.querySelector('[data-delivery-status="queued"]')!.textContent).toBe(t("chat-undeliveredbubbles.banner_queued", { count: 1 }));
  });
});
