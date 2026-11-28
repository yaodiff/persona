// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({ api: vi.fn(), launch: vi.fn(), upsert: vi.fn(), toast: vi.fn() }));
vi.mock("../../lib/api", async (original) => ({ ...(await original<typeof import("../../lib/api")>()), api: state.api }));
vi.mock("../../lib/new-agent", async (original) => ({ ...(await original<typeof import("../../lib/new-agent")>()), launchAgent: state.launch }));
vi.mock("../../lib/store", async (original) => ({ ...(await original<typeof import("../../lib/store")>()), upsertSession: state.upsert }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: state.toast }));

import { t } from "../../i18n";
import type { Session } from "../../lib/types";
import { CodexReadOnlyLine } from "./CodexReadOnlyLine";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const readOnlyCodex = {
  machine_id: "m1",
  session_id: "codex-1",
  title: "Codex CLI",
  cwd: "/work",
  status: "working",
  agent: "codex",
  source_kind: "codex_history",
  read_only_reason: "Codex is running in Terminal",
  capabilities: { read: true, send: false, takeover: { running: true, can_resume: true } },
} as Session;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  state.api.mockReset(); state.launch.mockReset(); state.upsert.mockReset(); state.toast.mockReset();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

const flush = async () => {
  await act(async () => {
    await import("./TakeoverAction");
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });
};

describe("CodexReadOnlyLine", () => {
  it("renders read-only reason alongside compact takeover control", async () => {
    act(() => root.render(<CodexReadOnlyLine session={readOnlyCodex} visible />));
    await flush();
    const line = host.querySelector("[data-codex-readonly-line]");
    expect(line).toBeTruthy();
    expect(line!.textContent).toContain("Codex is running in Terminal");
    expect([...host.querySelectorAll("button")].some((b) => b.textContent?.trim() === t("chat-takeover.takeover"))).toBe(true);
    expect(host.querySelector(`button[aria-label="${t("chat-chatview.more_actions")}"]`)).toBeTruthy();
  });

  it("keeps TakeoverAction dialog mounted when visible flips false", async () => {
    state.api.mockResolvedValue({
      running: false, can_resume: false, reason: "encrypted_context", token: "tok", cwd: "/work",
      transcript_path: "/tmp/t", continuation_prompt: "Continue",
    });
    act(() => root.render(<CodexReadOnlyLine session={readOnlyCodex} visible />));
    await flush();
    const btn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === t("chat-takeover.takeover")) as HTMLButtonElement;
    await act(async () => btn.click());
    await flush();
    expect(document.body.textContent).toContain(t("chat-takeover.reason_encrypted_context"));

    act(() => root.render(<CodexReadOnlyLine session={{ ...readOnlyCodex, capabilities: undefined } as Session} visible={false} />));
    await flush();
    expect(host.querySelector("[data-codex-readonly-line]")).toBeNull();
    expect(document.body.textContent).toContain(t("chat-takeover.reason_encrypted_context"));
  });

  it("merges Terminal, Resume, and compact takeover controls for stopped Codex sessions with terminal capability", async () => {
    const onTerminal = vi.fn();
    const stoppedCodex = {
      ...readOnlyCodex,
      status: "offline",
      capabilities: { read: true, send: false, terminal: true, takeover: { running: false, can_resume: true } },
    } as Session;
    act(() => root.render(
      <CodexReadOnlyLine session={stoppedCodex} visible stopped={{ mid: "m1", sid: "codex-1", onTerminal }} />,
    ));
    await flush();
    expect(host.querySelector("[data-stopped-agent]")).toBeTruthy();
    expect(host.querySelector("[data-stopped-agent]")?.textContent).toContain(readOnlyCodex.read_only_reason);
    expect(host.querySelector("[data-codex-readonly-line]")).toBeTruthy();
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
    // t() returns an unknown key as is, so compare with real words too: a raw key is not a label
    expect(labels.filter((label) => /^[a-z-]+\.[a-z_]+$/.test(label ?? ""))).toEqual([]);
    expect(labels).toContain(t("lib-terminals.terminal"));
    expect(labels).toContain(t("session-terminalpanel.resume"));
    expect(labels).toContain(t("chat-takeover.takeover"));
    const more = host.querySelector(`button[aria-label="${t("chat-chatview.more_actions")}"]`);
    expect(more).toBeTruthy();
    const actionWrap = more!.parentElement!;
    expect(actionWrap.className).toContain("flex-wrap");
    expect(actionWrap.className).toContain("justify-end");
  });

  it("triggers takeOverRequested from an inactive view layer and keeps the portaled dialog open across view switches", async () => {
    state.api.mockResolvedValue({
      running: true, can_resume: true, token: "tok-term", cwd: "/work",
      transcript_path: "/tmp/t", continuation_prompt: "Continue",
    });
    const onConsumed = vi.fn();
    // Simulate Chat layer hidden/inert while Terminal view is active, and header ⋯ triggering Take over.
    act(() => root.render(
      <div inert aria-hidden>
        <CodexReadOnlyLine session={readOnlyCodex} visible takeOverRequested onTakeOverConsumed={onConsumed} />
      </div>,
    ));
    await flush();
    expect(onConsumed).toHaveBeenCalledTimes(1);
    expect(state.api.mock.calls.map(([p, opts]) => [p, opts?.method ?? "GET"])).toEqual([
      ["/machines/m1/codex/codex-1/takeover", "GET"],
    ]);
    expect(document.body.textContent).toContain(t("chat-takeover.takeover_running_confirm"));

    // Switching back to Chat view (clearing inert) keeps the exact same dialog mounted without re-triggering GET.
    act(() => root.render(
      <div>
        <CodexReadOnlyLine session={readOnlyCodex} visible takeOverRequested={false} onTakeOverConsumed={onConsumed} />
      </div>,
    ));
    await flush();
    expect(state.api).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain(t("chat-takeover.takeover_running_confirm"));
  });
});
