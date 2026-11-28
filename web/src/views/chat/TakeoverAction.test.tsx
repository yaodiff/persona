// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({ api: vi.fn(), launch: vi.fn(), upsert: vi.fn(), toast: vi.fn() }));
vi.mock("../../lib/api", async (original) => ({ ...(await original<typeof import("../../lib/api")>()), api: state.api }));
vi.mock("../../lib/new-agent", async (original) => ({ ...(await original<typeof import("../../lib/new-agent")>()), launchAgent: state.launch }));
vi.mock("../../lib/store", async (original) => ({ ...(await original<typeof import("../../lib/store")>()), upsertSession: state.upsert }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: state.toast }));

import { ApiError } from "../../lib/api";
import type { Session } from "../../lib/types";
import { t } from "../../i18n";
import TakeoverAction, { takeoverMenuLabels } from "./TakeoverAction";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const baseSession = {
  machine_id: "machine", session_id: "thread", title: "Codex history", cwd: "/work", status: "offline",
  capabilities: { takeover: { running: true, can_resume: true } }, todo: { id: "todo-original", title: "Original task" },
} as Session;

const plan = (patch: Record<string, unknown> = {}) => ({
  running: true, can_resume: true, token: "token-1", cwd: "/work", transcript_path: "/private/transcript",
  continuation_prompt: "opaque encrypted continuation", ...patch,
});

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  state.api.mockReset(); state.launch.mockReset(); state.upsert.mockReset(); state.toast.mockReset();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve()); };
const button = (scope: ParentNode, label: string) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement;
const render = (session = baseSession) => act(() => root.render(<TakeoverAction session={session} />));

describe("TakeoverAction interactions", () => {
  it("running session click performs GET only, and cancel never posts", async () => {
    state.api.mockResolvedValue(plan());
    render();
    await act(async () => button(host, t("chat-takeover.takeover")).click());
    await flush();
    expect(state.api.mock.calls.map(([path, options]) => [path, options?.method ?? "GET"])).toEqual([["/machines/machine/codex/thread/takeover", "GET"]]);
    await act(async () => button(document.body, t("chat-chatview.not_now")).click());
    expect(state.api.mock.calls).toHaveLength(1);
  });

  it.each([false, true])("posts exactly once after running confirmation (app_owned=%s)", async (app_owned) => {
    state.api.mockImplementation(async (_path: string, options?: { method?: string }) => options?.method === "POST" ? { session: baseSession } : plan({ app_owned }));
    render();
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(state.api).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain(t("chat-takeover.takeover_running_confirm"));
    expect(document.body.textContent).not.toContain(t("chat-takeover.takeover_app_confirm"));
    await act(async () => button(document.body, t("chat-takeover.takeover_confirm")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST"]);
    expect(state.api.mock.calls[1][1]).toMatchObject({ json: { token: "token-1", confirm_stop: true } });
  });

  it("an idle app-owned session shows app confirmation and cancel never posts", async () => {
    state.api.mockResolvedValue(plan({ running: false, app_owned: true }));
    render();
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET"]);
    expect(document.body.textContent).toContain(t("chat-takeover.takeover_app_confirm"));
    expect(document.body.textContent).not.toContain(t("chat-takeover.takeover_running_confirm"));
    await act(async () => button(document.body, t("chat-chatview.not_now")).click()); await flush();
    expect(state.api).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(state.launch).not.toHaveBeenCalled();
  });

  it("an idle app-owned session posts confirm_stop true only after confirmation", async () => {
    state.api.mockImplementation(async (_path: string, options?: { method?: string }) => options?.method === "POST" ? { session: baseSession } : plan({ running: false, app_owned: true }));
    render();
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(state.api).toHaveBeenCalledTimes(1);
    await act(async () => button(document.body, t("chat-takeover.takeover_confirm")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST"]);
    expect(state.api.mock.calls[1][1]).toMatchObject({ json: { token: "token-1", confirm_stop: true } });
    expect(state.upsert).toHaveBeenCalledTimes(1);
    expect(state.toast).toHaveBeenCalledWith(t("chat-takeover.takeover_done"), "ok");
  });

  it("an exited session takes over with GET followed by an unconfirmed POST", async () => {
    state.api.mockImplementation(async (_path: string, options?: { method?: string }) => options?.method === "POST" ? { session: baseSession } : plan({ running: false }));
    render({ ...baseSession, capabilities: { takeover: { running: false, can_resume: true } } } as Session);
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST"]);
    expect(state.api.mock.calls[1][1]).toMatchObject({ json: { token: "token-1", confirm_stop: false } });
  });

  it("continues in a new session with the opaque prompt and original Todo", async () => {
    state.api.mockResolvedValue(plan());
    render();
    await act(async () => button(host, t("chat-takeover.takeover_new")).click()); await flush();
    expect(state.launch).toHaveBeenCalledWith({ machineId: "machine", cwd: "/work", launcher: "codex", agentType: "codex", inlinePrompt: true, text: "opaque encrypted continuation", attachments: [], todoId: "todo-original" });
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET"]);
  });

  it("can_resume false shows a translated reason and only the continue action", async () => {
    state.api.mockResolvedValue(plan({ can_resume: false, reason: "encrypted_context" }));
    render({ ...baseSession, capabilities: { takeover: { running: false, can_resume: false } } } as Session);
    expect(button(host, t("chat-takeover.takeover"))).toBeFalsy();
    await act(async () => button(host, t("chat-takeover.takeover_new")).click()); await flush();
    expect(document.body.textContent).toContain(t("chat-takeover.reason_encrypted_context"));
    expect(document.body.textContent).not.toContain("encrypted_context");
    expect([...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].filter((b) => b.getAttribute("aria-label") !== t("ui-atoms.close"))).toHaveLength(1);
    expect(button(document.body, t("chat-takeover.takeover_confirm"))).toBeFalsy();
  });

  it.each(["unavailable", "app_running"])("localizes %s reason codes instead of displaying the enum", async (reason) => {
    state.api.mockResolvedValue(plan({ can_resume: false, reason }));
    render({ ...baseSession, capabilities: { takeover: { running: false, can_resume: false } } } as Session);
    await act(async () => button(host, t("chat-takeover.takeover_new")).click()); await flush();
    expect(document.body.textContent).toContain(t(`chat-takeover.reason_${reason}`));
    expect(document.body.textContent).not.toContain(reason);
    expect(button(document.body, t("chat-takeover.takeover_confirm"))).toBeFalsy();
    expect(state.api).toHaveBeenCalledTimes(1);
  });

  it("continue-only POST refusal switches to sealed continuation without retrying the token", async () => {
    state.api.mockImplementation(async (_path: string, options?: { method?: string; json?: { token?: string } }) => {
      if (options?.method === "POST") throw new ApiError(409, "continue only", { code: "codex_takeover_continue_only" });
      return plan({ running: false });
    });
    render();
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(document.body.textContent).toContain(t("chat-takeover.reason_encrypted_context"));
    expect(document.body.textContent).not.toContain("encrypted_context");
    expect([...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].filter((b) => b.getAttribute("aria-label") !== t("ui-atoms.close"))).toHaveLength(1);
    await act(async () => button(document.querySelector('[role="dialog"]')!, t("chat-takeover.takeover_new")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST"]);
    expect(state.launch).toHaveBeenCalledTimes(1);
  });

  it("a failed POST retry fetches a fresh token before posting again", async () => {
    let gets = 0;
    state.api.mockImplementation(async (_path: string, options?: { method?: string; json?: { token?: string } }) => {
      if (options?.method === "POST") {
        if (options?.json?.token === "token-1") throw new ApiError(503, "temporary", { code: "temporary" });
        return { session: baseSession };
      }
      gets++;
      return plan({ running: false, token: `token-${gets}` });
    });
    render({ ...baseSession, capabilities: { takeover: { running: false, can_resume: true } } } as Session);
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(state.api.mock.calls[0][1]).toBeUndefined();
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST", "GET", "POST"]);
    expect(state.api.mock.calls[1][1]).toMatchObject({ json: { token: "token-1" } });
    expect(state.api.mock.calls[3][1]).toMatchObject({ json: { token: "token-2" } });
  });

  it("keeps a pending continue-only dialog when SSE removes takeover capability", async () => {
    let rejectPost!: (error: Error) => void;
    state.api.mockImplementation(async (_path: string, options?: { method?: string }) => {
      if (options?.method === "POST") return new Promise((_resolve, reject) => { rejectPost = reject; });
      return plan();
    });
    render();
    await act(async () => button(host, t("chat-takeover.takeover")).click()); await flush();
    await act(async () => button(document.body, t("chat-takeover.takeover_confirm")).click()); await flush();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST"]);

    render({ ...baseSession, source_kind: "codex_app_server", capabilities: undefined } as Session);
    await act(async () => rejectPost(new ApiError(409, "continue only", { code: "codex_takeover_continue_only" })));
    await flush();

    expect(document.body.textContent).toContain(t("chat-takeover.reason_encrypted_context"));
    expect(document.body.textContent).not.toContain("encrypted_context");
    expect([...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].filter((b) => b.getAttribute("aria-label") !== t("ui-atoms.close"))).toHaveLength(1);
    expect(button(host, t("chat-takeover.takeover"))).toBeFalsy();
    expect(state.api.mock.calls.map(([, options]) => options?.method ?? "GET")).toEqual(["GET", "POST"]);
  });

  it("renders no takeover controls for an ordinary live Codex session without a capability", () => {
    render({ ...baseSession, source_kind: "codex_app_server", capabilities: undefined } as Session);
    expect(host.querySelectorAll("button")).toHaveLength(0);
  });

  it("compact variant renders primary Take over plus overflow menu for Continue in a new session", async () => {
    state.api.mockResolvedValue(plan());
    act(() => root.render(<TakeoverAction session={baseSession} variant="compact" />));
    expect(button(host, t("chat-takeover.takeover"))).toBeTruthy();
    expect(button(host, t("chat-takeover.takeover_new"))).toBeFalsy();
    const more = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`);
    expect(more).toBeTruthy();
    await act(async () => more!.click());
    const menuItem = [...document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(t("chat-takeover.takeover_new")));
    expect(menuItem).toBeTruthy();
    await act(async () => menuItem!.click());
    await flush();
    expect(state.launch).toHaveBeenCalledTimes(1);
  });

  it("compact variant with can_resume false renders single short button without overflow menu", async () => {
    state.api.mockResolvedValue(plan({ can_resume: false, reason: "unavailable" }));
    const continueOnly = { ...baseSession, capabilities: { takeover: { running: false, can_resume: false } } } as Session;
    act(() => root.render(<TakeoverAction session={continueOnly} variant="compact" />));
    expect(button(host, t("chat-takeover.takeover"))).toBeFalsy();
    expect(button(host, t("chat-takeover.takeover_new_short"))).toBeTruthy();
    expect(host.querySelector(`button[aria-label="${t("chat-chatview.more_actions")}"]`)).toBeNull();
    await act(async () => button(host, t("chat-takeover.takeover_new_short")).click());
    await flush();
    expect(document.body.textContent).toContain(t("chat-takeover.reason_unavailable"));
  });

  it("continueRequested is one-shot consumed and never replays after cancel and remount", async () => {
    const pendingChanges: boolean[] = [];
    let requested = true;
    const consume = vi.fn(() => { requested = false; });
    state.api.mockResolvedValue(plan({ can_resume: false, reason: "encrypted_context" }));
    act(() => root.render(
      <TakeoverAction session={baseSession} variant="compact" continueRequested={requested} onContinueConsumed={consume} onPendingChange={(p) => pendingChanges.push(p)} />,
    ));
    await flush();
    expect(consume).toHaveBeenCalledTimes(1);
    expect(requested).toBe(false);
    expect(state.api).toHaveBeenCalledTimes(1);
    expect(pendingChanges.at(-1)).toBe(true);
    expect(document.body.textContent).toContain(t("chat-takeover.reason_encrypted_context"));

    // Simulate offline unmount followed by reconnect remount with the consumed prop: no duplicate GET or launch.
    act(() => root.unmount());
    root = createRoot(host);
    act(() => root.render(
      <TakeoverAction session={baseSession} variant="compact" continueRequested={requested} onContinueConsumed={consume} onPendingChange={(p) => pendingChanges.push(p)} />,
    ));
    await flush();
    expect(state.api).toHaveBeenCalledTimes(1);
    expect(state.launch).not.toHaveBeenCalled();
  });

  it("takeOverRequested is one-shot consumed and takeoverMenuLabels exposes both menu strings", async () => {
    expect(takeoverMenuLabels()).toEqual({
      takeOver: t("chat-takeover.takeover"),
      continueNew: t("chat-takeover.takeover_new"),
    });
    let requested = true;
    const consume = vi.fn(() => { requested = false; });
    state.api.mockResolvedValue(plan({ running: true, can_resume: true }));
    act(() => root.render(
      <TakeoverAction session={baseSession} variant="compact" takeOverRequested={requested} onTakeOverConsumed={consume} />,
    ));
    await flush();
    expect(consume).toHaveBeenCalledTimes(1);
    expect(requested).toBe(false);
    expect(state.api.mock.calls.map(([, opts]) => opts?.method ?? "GET")).toEqual(["GET"]);
    expect(document.body.textContent).toContain(t("chat-takeover.takeover_running_confirm"));
  });
});
