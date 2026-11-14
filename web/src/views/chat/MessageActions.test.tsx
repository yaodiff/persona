import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ChatActionsContext, liveValue, type ChatActions } from "./chat-actions";
import { t } from "../../i18n";
import { MessageActions, moreActions, rewindActions, savePromptActions } from "./MessageActions";

const base: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Persona", title: "Persona", images: () => [] };

function render(actions: ChatActions, itemId?: string, bar = true) {
  return renderToStaticMarkup(
    <ChatActionsContext.Provider value={actions}>
      <MessageActions text="回复" itemId={itemId} bar={bar}>
        <p>回复</p>
      </MessageActions>
    </ChatActionsContext.Provider>,
  );
}

// cindy's bar under a turn's last reply (#5314; its buttons and order: ReplyBar.test.tsx). It loads after the first
// screen: until then its row is kept, so the reply does not move when it comes.
describe("the reply bar", () => {
  it("keeps its row while it loads", () => {
    const html = render({ ...base, fork: () => {} }, "u1#0");
    expect(html).toContain('data-reply-bar="loading"');
    expect(html).toMatch(/data-reply-bar="loading" class="mt-1\.5 hidden h-6 \[@media\(hover:hover\)\]:block"/);
  });
  it("is not there without `bar` (narration, words still streaming): the menus only", () => {
    const html = render({ ...base, fork: () => {} }, "u1#0", false);
    expect(html).not.toContain("data-reply-bar");
    expect(html).not.toContain("aria-label");
    expect(html).toContain("<p>回复</p>");
  });
});

describe("moreActions", () => {
  it("branches off at the reply it is on", () => {
    const got: string[] = [];
    const [fork] = moreActions("u1#0", (id) => got.push(id));
    expect(fork?.label).toBe("开启一个新会话");
    expect(moreActions("u1#0", () => {}, true)[0]?.label).toBe("开启一个新对话");
    fork?.onSelect();
    expect(got).toEqual(["u1#0"]);
  });
  it("offers nothing for a line the node cannot find", () => {
    expect(moreActions("line12#0", () => {})).toEqual([]);
    expect(moreActions("u1", () => {})).toEqual([]);
    expect(moreActions(undefined, () => {})).toEqual([]);
  });
});

// "Save as Prompt…" (#7329): on your messages with words.
describe("savePromptActions", () => {
  it("is there on a message with words", () => {
    expect(savePromptActions("  ")).toEqual([]);
    const [save] = savePromptActions("Review this diff.");
    expect(save).toMatchObject({ key: "save-prompt", label: t("chat-messageactions.save_as_prompt"), icon: "book" });
  });
});

// "Rewind to here" (#2436): on your messages, "Conversation only" / "Code and conversation", the second asking again.
describe("rewindActions", () => {
  const run = async () => {};
  it("has the two choices, the second a danger that asks again", () => {
    const [rewind] = rewindActions("u1#0", "继续", { block: "", run });
    expect(rewind.label).toBe("回退到这里");
    expect(rewind.disabled).toBe(false);
    const [conv, code] = rewind.children as { label: string; danger?: boolean; confirm?: string; inline?: boolean }[];
    expect([conv.label, code.label]).toEqual(["只回退对话", "回退代码和对话"]);
    expect(conv.danger).toBeUndefined();
    expect(conv.confirm).toBeUndefined();
    expect(code.danger).toBe(true);
    expect(code.confirm).toBe("确认回退代码");
    expect(conv.inline && code.inline).toBe(true);
  });
  it("runs the choice it is given, with the message", async () => {
    const got: unknown[] = [];
    const [rewind] = rewindActions("u1#0", "继续", { block: "", run: async (...a) => void got.push(a) });
    const [conv, code] = rewind.children as { onSelect: () => Promise<unknown> }[];
    await conv.onSelect();
    await code.onSelect();
    expect(got).toEqual([["u1#0", "继续", "conversation"], ["u1#0", "继续", "code_and_conversation"]]);
  });
  it("is off with the reason while the session cannot", () => {
    const [rewind] = rewindActions("u1#0", "继续", { block: "工作中", run });
    expect(rewind.disabled).toBe(true);
    expect(rewind.hint).toBe("工作中");
  });
  it("is not there without a message, words or a session that rewinds", () => {
    expect(rewindActions(undefined, "继续", { block: "", run })).toEqual([]);
    expect(rewindActions("u1#0", "  ", { block: "", run })).toEqual([]);
    expect(rewindActions("u1#0", "继续", undefined)).toEqual([]);
  });
});

describe("MessageActions on your own message", () => {
  // read-only: the bar beside your bubble has nothing else for its ⋯ (Forward and Use as context go there when it can send)
  const mine = (actions: ChatActions, messageId?: string) =>
    renderToStaticMarkup(
      <ChatActionsContext.Provider value={{ ...actions, canCompose: false }}>
        <MessageActions text="继续" isUser side messageId={messageId}>
          <p>继续</p>
        </MessageActions>
      </ChatActionsContext.Provider>,
    );
  it("has ⋯ for 回退到这里 when the chat can rewind, even while it is off", () => {
    expect(mine({ ...base, rewind: { block: liveValue(""), run: async () => {} } }, "u1#0")).toContain('aria-label="更多"');
    expect(mine({ ...base, rewind: { block: liveValue("工作中"), run: async () => {} } }, "u1#0")).toContain('aria-label="更多"');
  });
  it("has none for a chat that cannot, or a message without an id", () => {
    expect(mine(base, "u1#0")).not.toContain('aria-label="更多"');
    expect(mine({ ...base, rewind: { block: liveValue(""), run: async () => {} } })).not.toContain('aria-label="更多"');
  });
});


it("offers only conversation rewind when native file restoration is unavailable", () => {
  const [action] = rewindActions("example#0", "Example", { block: "", code: false, run: async () => {} });
  expect((action.children as { key: string }[]).map((item) => item.key)).toEqual(["rewind-conversation"]);
});
