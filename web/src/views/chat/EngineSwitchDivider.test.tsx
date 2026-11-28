// @vitest-environment jsdom
// The switch divider (#5054, after cindy's agent switch card): "Switched from Claude Code to Codex · <model> · <badge>"
// between two hairlines, above the first message the new engine got; the chip opens the handoff when there is one.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { chatLines } from "../../lib/turns";
import type { Item } from "../../lib/types";

const fullItem = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../lib/api")>()), fullItem }));

const { EngineSwitchDivider } = await import("./EngineSwitchDivider");
const { SegmentView } = await import("./SegmentView");
const { forgetExpanded } = await import("./expanded");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const HANDOFF = "## Conversation so far\nThe user asked for a login page.";
const block = (attrs: string, text = HANDOFF) => `<persona-context kind="engine-switch" title="Handoff" ${attrs}>\n${text}\n</persona-context>\n\n`;
const user = (seq: number, text: string, extra: Partial<Item> = {}): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text, ...extra });
const said = (seq: number, text: string): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text, model: "gpt-5.5" });
const sw = (extra = {}) => ({ from: "claude", to: "codex", model: "gpt-5.5", resumed: false, text: HANDOFF, ...extra });

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  forgetExpanded();
  fullItem.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});
/** Until `ok` (the panel's module loads with the divider; a loaded machine takes a few turns), at most about a second. */
const until = async (ok: () => boolean) => {
  for (let i = 0; i < 200 && !ok(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 5)); });
};

describe("EngineSwitchDivider", () => {
  it("names both engines, the model and how the new engine went on (tests run in zh-Hans)", () => {
    const html = renderToStaticMarkup(<EngineSwitchDivider sw={sw()} id="seg:u1#0" mid="m" sid="s" />);
    expect(html).toContain('role="separator"');
    expect(html).toContain('aria-label="已从 Claude Code 切换到 Codex"');
    expect(html).toContain("已从 Claude Code 切换到 Codex");
    expect(html).toMatch(/title="gpt-5\.5"[^>]*>GPT-5\.5</); // as the model lists name it, the id on hover
    expect(html).toContain("已通过摘要继续");
    expect(html).not.toContain("已恢复原会话");
    expect(renderToStaticMarkup(<EngineSwitchDivider sw={sw({ resumed: true, from: "codex", to: "claude" })} id="x" mid="m" sid="s" />)).toContain("已恢复原会话");
  });

  it("leaves the model out when the block names none", () => {
    const html = renderToStaticMarkup(<EngineSwitchDivider sw={sw({ model: undefined })} id="x" mid="m" sid="s" />);
    expect(html).not.toContain("data-engine-model");
    expect(html).toContain("data-engine-badge");
  });

  it("opens and closes the handoff from its chip, remembered per divider", async () => {
    act(() => root.render(<EngineSwitchDivider sw={sw()} id="seg:u1#0" mid="m" sid="s" />));
    const chip = host.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(chip.getAttribute("aria-expanded")).toBe("false");
    expect(chip.title).toBe("查看交接内容");
    expect(host.querySelector("[data-engine-handoff]")).toBeNull(); // folded: no panel drawn
    act(() => chip.click());
    await until(() => host.querySelector("[data-engine-handoff]") !== null);
    expect(chip.getAttribute("aria-expanded")).toBe("true");
    const panel = host.querySelector("[data-engine-handoff]")!;
    expect(panel.id).toBe(chip.getAttribute("aria-controls"));
    expect(panel.textContent).toContain("交接内容——切换时发送给新引擎的上下文摘要");
    expect(panel.querySelector("pre")?.textContent).toBe(HANDOFF);
    // drawn again (scrolled away and back, a push): still open
    act(() => root.unmount());
    root = createRoot(host);
    act(() => root.render(<EngineSwitchDivider sw={sw()} id="seg:u1#0" mid="m" sid="s" />));
    expect(host.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-engine-handoff]")).not.toBeNull(); // the panel's module is in: drawn at once
    act(() => host.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    expect(host.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded")).toBe("false");
  });

  it("is no control when there is no handoff to show", () => {
    const html = renderToStaticMarkup(<EngineSwitchDivider sw={sw({ text: "" })} id="x" mid="m" sid="s" />);
    expect(html).not.toContain("<button");
    expect(html).not.toContain("data-engine-handoff");
    expect(html).toContain('aria-label="已从 Claude Code 切换到 Codex"');
  });

  it("fetches the rest of a handoff the hub cut for the page", async () => {
    const whole = `${HANDOFF}\nand the rest of it.`;
    fullItem.mockResolvedValue({ id: "u1#0", seq: 1, role: "user", kind: "text", text: `${block('from="claude" to="codex"', whole)}Now add the tests.` });
    const cut = user(1, `${block('from="claude" to="codex"', `${HANDOFF}\n…（已截断，共 9000 字节）`)}Now add the tests.`, { full_size: 9000 }); // i18n-ignore: the hub's cut note
    act(() => root.render(<EngineSwitchDivider sw={sw({ text: `${HANDOFF}\n…（已截断，共 9000 字节）` })} id="x" item={cut} mid="m" sid="s" />)); // i18n-ignore: the hub's cut note
    act(() => host.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    await until(() => host.querySelector("[data-engine-handoff]") !== null);
    expect(host.querySelector("[data-engine-handoff] pre")?.textContent).toBe(HANDOFF); // without the hub's note
    act(() => host.querySelector<HTMLButtonElement>("[data-full-text]")!.click());
    await until(() => host.querySelector("[data-full-text]") === null);
    expect(fullItem).toHaveBeenCalledWith("m", "s", "u1#0");
    expect(host.querySelector("[data-engine-handoff] pre")?.textContent).toBe(whole);
    expect(host.querySelector("[data-full-text]")).toBeNull();
  });

  it("stands above the message the new engine got, which keeps only the user's words, and no model change line goes with it", () => {
    const lines = chatLines([user(1, "Build a login page."), { ...said(2, "Done."), model: "claude-opus-5-5" }, user(3, `${block('from="claude" to="codex" model="gpt-5.5"')}Now add the tests.`), said(4, "Added.")]);
    const html = lines.map((l) => (l.kind === "segment" ? renderToStaticMarkup(<SegmentView seg={l.seg} live={false} mid="m" sid="s" expandFold={async () => {}} />) : "")).join("");
    expect(html.match(/data-engine-switch=/g)).toHaveLength(1);
    expect(html).not.toContain("data-model-change");
    const at = html.indexOf("data-engine-switch=");
    expect(html.indexOf("Done.")).toBeLessThan(at);
    expect(html.indexOf("Now add the tests.")).toBeGreaterThan(at);
    expect(html).not.toContain("persona-context");
    expect(html).not.toContain("The user asked for a login page."); // folded: the handoff is not drawn until opened
    expect(html).not.toContain("Handoff"); // no context chip titled with the block's title
  });
});
