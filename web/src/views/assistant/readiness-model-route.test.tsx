// @vitest-environment jsdom
// A model the main chat can't run (model_route, #10365): the readiness card and the blocked send button open the chat's
// own model panel, as its model error's Fix it does, never the new-provider wizard; Settings › Models where the chat has
// no panel. Once another model is picked, "Try it" leads, since only a try of it lets the chat send again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { onModelPicker } from "../../lib/model-switch";
import { resetReadiness, sendLabel, stepActions, type Readiness, type ReadinessStep } from "../../lib/readiness";
import { getState, reset, upsertSession } from "../../lib/store";
import type { AssistantInfo, Session } from "../../lib/types";
import { assistantPickerKey } from "../chat/ModelPill";
import { ReadinessCard, useReadinessGate, type ReadinessGate } from "./Readiness";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
beforeAll(() => loadPages(), 60_000);

const main = { machine_id: "mac", session_id: "m1", assistant: true, state: "idle", turn_error_code: "model_route" } as Session;
const readiness: Readiness = {
  version: 1, mode: "setup", ready: false, next: "model", machine: { id: "mac", name: "Mac" }, launcher: "claude", dismissed: false,
  checked_at: "2026-11-18T09:00:00Z",
  steps: [
    { id: "computer", state: "ok" }, { id: "claude", state: "ok" },
    { id: "model", state: "problem", code: "model_route", title: "This model isn't available", action: { id: "connect_model" }, alt_actions: [{ id: "test_assistant" }] },
    { id: "first_message", state: "ok" },
  ],
};

let gate: ReadinessGate | null = null;
function Holder() {
  gate = useReadinessGate();
  return <ReadinessCard gate={gate} />;
}

let host: HTMLDivElement;
let root: Root;
let asked: string[] = [];
let opened = 0;
let unpick: () => void;
beforeEach(() => {
  asked = [];
  opened = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    asked.push(String(url));
    return new Response(JSON.stringify(readiness), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  Object.assign(getState(), {
    features: ["assistant_readiness", "assistant_model_pick"],
    assistant: { machine_id: "mac", main: "m1", conversations: [] } as unknown as AssistantInfo,
    sessions: { mac: [main] },
  });
  resetReadiness(readiness);
  unpick = onModelPicker(assistantPickerKey("mac", "m1"), () => opened++);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Holder />));
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  unpick();
  resetReadiness();
  reset();
  vi.unstubAllGlobals();
});

const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent);
const click = (label: string) => act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === label)!.click());

describe("a model the main chat can't run", () => {
  it("opens the chat's own model panel from the card and the send button, not the wizard", () => {
    expect(buttons()).toEqual(["更换模型", "试一试"]);
    click("更换模型");
    expect(opened).toBe(1);
    expect(gate!.blocked?.label).toBe("更换模型");
    act(() => gate!.blocked!.onClick!());
    expect(opened).toBe(2);
    expect(location.pathname + location.search).toBe("/");
  });

  it("opens Settings › Models where the chat has no panel", () => {
    unpick();
    click("更换模型");
    expect(location.pathname + location.search).toBe("/settings?tab=models");
  });

  it("leads with Try it once another model is picked, until that try ends", async () => {
    act(() => upsertSession({ ...main, model_pick: { launcher: "claude", model: "model-b" } }));
    expect(buttons()).toEqual(["试一试", "更换模型"]);
    expect(gate!.blocked?.label).toBe("试一试");
    await act(async () => gate!.blocked!.onClick!());
    expect(opened).toBe(0);
    expect(asked.some((u) => u.includes("probe=1"))).toBe(true);
    // the try of model-b still met model_route: another model is what's next
    expect(buttons()).toEqual(["更换模型", "试一试"]);
  });
});

describe("stepActions", () => {
  it("swaps the wizard for the chat's panel on model_route alone", () => {
    const route = readiness.steps[2];
    expect(stepActions(route)).toEqual(["pick_model", ["test_assistant"]]);
    expect(stepActions(route, true)).toEqual(["test_assistant", ["pick_model"]]);
    const login: ReadinessStep = { id: "model", state: "problem", code: "not_logged_in", action: { id: "connect_model" }, alt_actions: [{ id: "login_claude" }] };
    expect(stepActions(login, true)).toEqual(["connect_model", ["login_claude"]]);
    expect(sendLabel(login, "connect_model")).toBe("先连接模型");
  });
});
