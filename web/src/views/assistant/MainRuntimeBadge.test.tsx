// @vitest-environment jsdom
// The main chat's runtime line (docs/assistant/assistant.md "Main conversation execution on the web"): on the Assistant
// screen it says only the main chat's state in plain words, in the product's own name for it ("主对话 · 运行中"), never
// where or with what it runs (Settings keeps that), and nothing at all when nothing is known (an older hub without the
// view, a failed read), never "运行信息未上报" (audit item 80).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ answer: (): Promise<unknown> => Promise.reject(new Error("404")) }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => (path.startsWith("/assistant?view=main-runtime") ? hub.answer() : Promise.resolve({})),
}));

import { getState, reset } from "../../lib/store";
import { MainRuntimeBadge } from "./MainRuntimeBadge";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const line = () => host.querySelector("[data-main-runtime]");
async function draw() {
  await act(async () => root.render(<MainRuntimeBadge />));
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  reset();
  Object.assign(getState(), { assistant: { machine_id: "hub", main: "main-1", conversations: [] } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

describe("the main chat's runtime line", () => {
  it("is not drawn when the hub does not report where the main chat runs", async () => {
    hub.answer = () => Promise.reject(new Error("404"));
    await draw();
    expect(line()).toBeNull();
    expect(host.textContent).toBe("");
  });

  it("is not drawn for a report of another conversation or an unknown state", async () => {
    hub.answer = () => Promise.resolve({ session_id: "main-1", state: "unknown" });
    await draw();
    expect(line()).toBeNull();
  });

  it("names the main chat as the rest of the app does, with its state in plain words and no engineering details", async () => {
    hub.answer = () => Promise.resolve({ session_id: "main-1", state: "running", source: "native", harness: "claude", location: { host: "example-host", container: "example-box" } });
    await draw();
    expect(line()?.getAttribute("aria-label")).toBe("主对话 · 运行中");
    expect(host.textContent).not.toContain("主会话");
    expect(host.textContent).not.toContain("未上报");
    expect(host.textContent).not.toMatch(/Claude Code|example-host|example-box|容器/);
  });
});
