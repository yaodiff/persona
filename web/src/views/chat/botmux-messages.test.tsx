// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { t } from "../../i18n";
import { setBotmuxMode } from "../../lib/botmux-mode";
import { segments } from "../../lib/turns";
import type { Item, Session } from "../../lib/types";
import { AgentStatusLine } from "./AgentStatusLine";
import { SegmentView } from "./SegmentView";

// A botmux session's exchange as the node sends it (docs/protocol/protocol.md "BotMux messages"): the message with
// botmux's blocks, and the `botmux send` call that answered it in Lark.
const raw = "<botmux_reminder>reply with botmux send</botmux_reminder>\n\n<user_message>\nship it\n</user_message>\n\n<sender type=\"user\" open_id=\"ou_example_alex\" name=\"Alex Chen\" />";
const items: Item[] = [
  { id: "u1#0", seq: 1, role: "user", kind: "text", text: raw, botmux: { kind: "message", text: "ship it", sender: "Alex Chen", sender_type: "user" } },
  { id: "a1#0", seq: 2, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: "t1", tool_input: { command: "botmux send --response-kind final 'shipped v2'" }, botmux: { kind: "reply", text: "shipped v2", final: true } },
  { id: "r1#0", seq: 3, role: "user", kind: "tool_result", tool_use_id: "t1", text: "{\"ok\":true}" },
];

// the botmux pieces load on demand (React.lazy): render, then let them come in
async function render(transcript = items): Promise<string> {
  const host = document.createElement("div");
  const root = createRoot(host);
  await act(async () => root.render(<SegmentView seg={segments(transcript)[0]} live={false} mid="m" sid="s" expandFold={async () => {}} />));
  await act(async () => { await import("./BotmuxMessages"); await new Promise((r) => setTimeout(r, 0)); });
  const html = host.innerHTML;
  act(() => root.unmount());
  return html;
}

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("botmux sessions in the chat", () => {
  it("decoded (the default): the words written in Lark, the sender as a tag, and the reply sent there", async () => {
    const html = await render();
    expect(html).toContain('data-botmux-message="message"');
    expect(html).toContain("ship it");
    expect(html).toContain("Alex Chen");
    expect(html).not.toContain("&lt;user_message&gt;");
    expect(html).toContain("data-botmux-original");
    expect(html).toContain("data-botmux-reply");
    expect(html).toContain("shipped v2");
  });

  it("loads a paged Original text item and retries a failed request", async () => {
    const pagedItem: Item = {
      id: "u1#1", seq: 1, role: "user", kind: "text",
      text: "Original prefix\n…（已截断，点开看全文，共 90000 字节）", full_size: 90000,
    };
    const completeText = "Original prefix\nThe rest of the transcript";
    let failRequest!: (error: Error) => void;
    const request = new Promise<Item>((_resolve, reject) => { failRequest = reject; });
    const fullItem = vi.spyOn(api, "fullItem").mockReturnValueOnce(request)
      .mockResolvedValueOnce({ ...pagedItem, text: completeText, full_size: undefined });
    const host = document.createElement("div");
    const root = createRoot(host);
    const fullTextButton = () => host.querySelector<HTMLButtonElement>("[data-full-text]");

    try {
      await act(async () => { await import("./BotmuxMessages"); });
      await act(async () => root.render(<SegmentView seg={segments([items[0], pagedItem])[0]} live={false} mid="machine" sid="session" expandFold={async () => {}} />));
      expect(fullTextButton()).toBeNull();
      expect(fullItem).not.toHaveBeenCalled();

      await act(async () => host.querySelector<HTMLButtonElement>("[data-botmux-original]")!.click());
      expect(Array.from(host.querySelectorAll("pre"), (pre) => pre.textContent)).toEqual([raw, "Original prefix"]);
      expect(host.querySelectorAll("[data-full-text]")).toHaveLength(1);
      expect(fullTextButton()?.dataset.fullText).toBe("idle");
      expect(fullItem).not.toHaveBeenCalled();

      await act(async () => fullTextButton()!.click());
      expect(fullItem).toHaveBeenNthCalledWith(1, "machine", "session", pagedItem.id);
      expect(fullTextButton()?.dataset.fullText).toBe("loading");
      expect(fullTextButton()?.disabled).toBe(true);

      await act(async () => failRequest(new Error("Network failure")));
      expect(fullTextButton()?.dataset.fullText).toBe("failed");
      expect(fullTextButton()?.disabled).toBe(false);
      expect(fullTextButton()?.textContent).toContain("重试");

      await act(async () => fullTextButton()!.click());
      expect(fullItem).toHaveBeenNthCalledWith(2, "machine", "session", pagedItem.id);
      expect(Array.from(host.querySelectorAll("pre"), (pre) => pre.textContent)).toEqual([raw, completeText]);
      expect(fullTextButton()).toBeNull();
    } finally {
      act(() => root.unmount());
    }
  });

  it("preserves a single newline in a decoded reply", async () => {
    const html = await render([items[0], { ...items[1], botmux: { kind: "reply", text: "line one\nline two" } }, items[2]]);
    const host = document.createElement("div");
    host.innerHTML = html;
    const lineBreak = host.querySelector("[data-botmux-reply] br");
    expect(lineBreak?.previousSibling?.textContent).toBe("line one");
    expect(lineBreak?.nextSibling?.textContent?.trim()).toBe("line two");
  });

  it("as they are: the transcript's words, no tags, no reply block", async () => {
    setBotmuxMode("all");
    const html = await render();
    expect(html).not.toContain("data-botmux-message");
    expect(html).not.toContain("data-botmux-reply");
    expect(html).toContain("user_message");
  });

  it("shows a one-line composer notice when botmux falls back to external-event trigger", async () => {
    const host = document.createElement("div");
    const root = createRoot(host);
    const session = { machine_id: "m", session_id: "s", title: "t", cwd: "/tmp", status: "idle", botmux_external_trigger: true } as Session;
    await act(async () => root.render(<AgentStatusLine session={session} />));
    expect(host.innerHTML).toContain('data-testid="botmux-external-trigger-note"');
    expect(host.textContent).toContain("3.59.23");
    act(() => root.unmount());
  });

  it("says why when no BotMux Owner Union ID is set, not to upgrade", async () => {
    const host = document.createElement("div");
    const root = createRoot(host);
    const session = { machine_id: "m", session_id: "s", title: "t", cwd: "/tmp", status: "idle", botmux_external_trigger: true,
      botmux_external_trigger_reason: "owner_unset" } as Session;
    await act(async () => root.render(<AgentStatusLine session={session} />));
    expect(host.querySelector('[data-testid="botmux-external-trigger-note"]')?.textContent).toBe(t("chat-botmux.owner_unset_notice"));
    expect(host.textContent).not.toContain("3.59.23");
    act(() => root.unmount());
  });

  it("suppresses errored botmux send replies and renders unknown trigger source generically", async () => {
    const erroredItems: Item[] = [
      { id: "u2#0", seq: 1, role: "user", kind: "text", text: "raw", botmux: { kind: "trigger", text: "cron tick", mentions: ["Alice", "Alice"] } },
      { id: "a2#0", seq: 2, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: "t2", tool_input: { command: "botmux send 'fail'" }, botmux: { kind: "reply", text: "fail" } },
      { id: "r2#0", seq: 3, role: "user", kind: "tool_result", tool_use_id: "t2", is_error: true, text: "error" },
    ];
    const host = document.createElement("div");
    const root = createRoot(host);
    await act(async () => root.render(<SegmentView seg={segments(erroredItems)[0]} live={false} mid="m" sid="s" expandFold={async () => {}} />));
    await act(async () => { await import("./BotmuxMessages"); await new Promise((r) => setTimeout(r, 0)); });
    const html = host.innerHTML;
    expect(html).toContain("外部触发");
    expect(html).not.toContain("从 Persona 发送");
    expect(html).not.toContain("data-botmux-reply");
    act(() => root.unmount());
  });
});
