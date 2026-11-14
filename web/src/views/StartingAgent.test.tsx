// @vitest-environment jsdom
// The page of an agent being started names its launcher by the provider's display name, the startup ID in the line's
// tooltip (#5029); an rc alias reads as before. The field to change it on a failed start keeps the startup ID.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { setToken } from "../lib/api";
import { getState } from "../lib/store";
import type { AgentLaunch } from "../lib/new-agent";

vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (s: { machines: { id: string; name: string }[] }) => unknown) => select({ machines: [{ id: "mac", name: "Mac" }] }),
}));

import { forgetProviderNames } from "../lib/provider-names";
import { StartingAgent } from "./StartingAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const launch = (o: Partial<AgentLaunch>): AgentLaunch => ({
  id: "l1", machineId: "mac", cwd: "/code/app", launcher: "", text: "", attachments: [], phase: "failed", error: "busy", uploaded: {},
  startOpId: "o1", startPromptOpId: "o2", messageId: "m1", ...o,
});
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const line = () => host.querySelector<HTMLElement>("header p")!;

beforeEach(() => {
  forgetProviderNames();
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ providers: [{ id: "launcher.relay-example-com", name: "我的中转 站", kind: "launcher" }] }), { status: 200 })));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  getState().launches = {};
});

describe("starting an agent", () => {
  it("names a provider by its display name, its ID in the tooltip; the field keeps the ID", async () => {
    act(() => root.render(<StartingAgent launch={launch({ launcher: "relay-example-com", model: "glm-5" })} />));
    await flush();
    expect(line().textContent).toBe("Mac · /code/app · 我的中转 站 · glm-5");
    expect(line().title).toBe("Mac · /code/app · 我的中转 站 (relay-example-com) · glm-5");
    expect(host.querySelector("input")!.value).toBe("relay-example-com");
  });

  it("names an rc alias by its ID, with no tooltip", async () => {
    act(() => root.render(<StartingAgent launch={launch({ launcher: "cc_glm" })} />));
    await flush();
    expect(line().textContent).toBe("Mac · /code/app · cc_glm");
    expect(line().hasAttribute("title")).toBe(false);
  });
});

const typeRetry = (input: HTMLInputElement | HTMLTextAreaElement, value: string) => act(() => {
  const prototype = input.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
});
async function retryFixture(overrides: Partial<AgentLaunch> = {}, lostReply = false) {
  setToken("synthetic-retry-owner");
  const item = launch({ launcher: "work", text: "Original task", agentType: "traex", inlinePrompt: true, host: "herdr", ...overrides });
  getState().launches[item.id] = item;
  let attempts = 0;
  const fetcher = vi.fn(async (url: string, _init: RequestInit = {}) => {
    if (url.endsWith("/sessions")) {
      if (lostReply && attempts++ === 0) throw new TypeError("Synthetic lost reply");
      return new Response(JSON.stringify({ session: { machine_id: "mac", session_id: "synthetic-started", agent: item.agentType, status: "idle" } }), { status: 200 });
    }
    return new Response(JSON.stringify({ providers: [] }), { status: 200 });
  });
  vi.stubGlobal("fetch", fetcher);
  act(() => root.render(<StartingAgent launch={item} />));
  await flush();
  return { item, fetcher, starts: () => fetcher.mock.calls.filter(([url]) => url.endsWith("/sessions")) };
}
const retry = async () => {
  const button = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("startingagent.retry"))!;
  act(() => button.click());
  await flush();
};

it.each(["launcher", "empty text"])("retires captured Traex startup when editing %s (#1842)", async (edit) => {
  const fixture = await retryFixture();
  if (edit === "launcher") typeRetry(host.querySelector("input")!, "claude");
  else typeRetry(host.querySelector("textarea")!, " ");
  await retry();
  expect(fixture.starts()).toHaveLength(1);
  const body = JSON.parse(fixture.starts()[0][1]!.body as string);
  expect(body).toMatchObject({ async: true, host: "herdr" });
  expect(body).not.toHaveProperty("agent_type");
  expect(body).not.toHaveProperty("prompt");
  expect(getState().launches[fixture.item.id].agentType).toBeUndefined();
});

it("retries the same valid Traex snapshot inline without an ordinary first message", async () => {
  const fixture = await retryFixture();
  await retry();
  expect(fixture.starts()).toHaveLength(1);
  const body = JSON.parse(fixture.starts()[0][1]!.body as string);
  expect(body).toMatchObject({ launcher: "work", agent_type: "traex", host: "herdr", prompt: "Original task" });
  expect(body).not.toHaveProperty("async");
  expect(fixture.fetcher.mock.calls.filter(([url]) => url.endsWith("/messages"))).toHaveLength(0);
});

it("preserves Codex's optional empty inline retry", async () => {
  const fixture = await retryFixture({ agentType: "codex", text: "" });
  await retry();
  const body = JSON.parse(fixture.starts()[0][1]!.body as string);
  expect(body).toMatchObject({ agent_type: "codex" });
  expect(body).not.toHaveProperty("async");
  expect(body).not.toHaveProperty("prompt");
});

it("keeps a repeat-only Traex target locked", async () => {
  const fixture = await retryFixture({ repeatOnly: true });
  expect(host.querySelector("input")).toBeNull();
  expect(host.querySelector("textarea")).toBeNull();
  await retry();
  const body = JSON.parse(fixture.starts()[0][1]!.body as string);
  expect(body).toMatchObject({ launcher: "work", agent_type: "traex", prompt: "Original task" });
});

it("keeps an uncertain target without dispatch when confirmation is declined", async () => {
  vi.stubGlobal("confirm", vi.fn(() => false));
  const fixture = await retryFixture({ uncertain: true });
  expect(host.querySelector("input")).toBeNull();
  await retry();
  expect(fixture.starts()).toHaveLength(0);
  expect(getState().launches[fixture.item.id].agentType).toBe("traex");
});

it("replays an inline Traex attempt with the same operation and payload after a gap", async () => {
  vi.useFakeTimers();
  const fixture = await retryFixture({ startPromptOpId: "00000000-1111-4000-8000-000000000002" }, true);
  await retry();
  await act(async () => vi.advanceTimersByTimeAsync(500));
  const starts = fixture.starts();
  expect(starts).toHaveLength(2);
  expect(starts[0][1]!.body).toBe(starts[1][1]!.body);
  expect(new Headers(starts[0][1]!.headers).get("X-Persona-Op-Id")).toBe("00000000-1111-4000-8000-000000000002");
  expect(new Headers(starts[1][1]!.headers).get("X-Persona-Op-Id")).toBe("00000000-1111-4000-8000-000000000002");
  expect(fixture.fetcher.mock.calls.filter(([url]) => url.endsWith("/messages"))).toHaveLength(0);
});
