// @vitest-environment jsdom
// A connector's page while its program starts: only the newest read of its status lands. An older answer arriving
// late, or one for a page already left, never puts the page back to "starting" or shows another connector's status.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HubConnector } from "../../lib/connectors";

type Read = { resolve: (list: HubConnector[]) => void };
const reads = vi.hoisted(() => [] as Read[]);
vi.mock("../../lib/connectors", async (original) => ({
  ...(await original<typeof import("../../lib/connectors")>()),
  listConnectors: () => new Promise<HubConnector[]>((resolve) => reads.push({ resolve })),
}));
import { ConnectorDetail, ConnectorList } from "./HubConnector";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Switched on, its program not started yet (no capabilities), then running and signed in.
const starting = (id: string): HubConnector => ({ id, name: `示例 ${id}`, status: "not_signed_in", enabled: true, capabilities: [] });
const running = (id: string): HubConnector => ({ id, name: `示例 ${id}`, status: "ok", account: `账号 ${id}`, enabled: true, capabilities: ["read"] });

let host: HTMLDivElement;
let root: Root;
const text = () => host.textContent ?? "";
const draw = (id: string) => act(() => root.render(<ConnectorDetail id={id} onBack={() => {}} />));
const answer = async (n: number, list: HubConnector[]) => act(async () => reads[n].resolve(list));
const tick = async (ms = 1000) => act(async () => void vi.advanceTimersByTime(ms));

beforeEach(() => {
  vi.useFakeTimers();
  reads.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("a connector's page while it starts", () => {
  it("keeps lark-cli out of More connectors while reporting it to the Lark group", async () => {
    const onLarkCli = vi.fn();
    act(() => root.render(<ConnectorList onOpen={() => {}} onLarkCli={onLarkCli} />));
    await answer(0, [
      { id: "larkcli", name: "Lark via lark-cli", status: "ok", account: "me" },
      running("github"),
    ]);
    expect(text()).toContain("示例 github");
    expect(text()).not.toContain("Lark via lark-cli");
    expect(onLarkCli).toHaveBeenCalledWith(expect.objectContaining({ id: "larkcli" }));
  });

  it("keeps the newest status when an older read answers last, and stops polling", async () => {
    draw("x");
    await answer(0, [starting("x")]);
    expect(text()).toContain("启动完成后即可登录");

    await tick(); // the poll's first read, still on its way
    expect(reads).toHaveLength(2);
    await tick(3000); // ticks skipped while it hasn't answered
    expect(reads).toHaveLength(2);

    const recheck = host.querySelector<HTMLButtonElement>('button[aria-label="重新检查"]')!;
    await act(async () => recheck.click());
    expect(reads).toHaveLength(3);
    await answer(2, [running("x")]); // the newer read: signed in
    await answer(1, [starting("x")]); // the older one, late
    expect(text()).toContain("已连接");
    expect(text()).toContain("账号 x");
    expect(text()).not.toContain("启动完成后即可登录");

    await tick(20_000);
    expect(reads).toHaveLength(3); // no longer starting: no more polling
  });

  it("drops a read for the connector opened before", async () => {
    draw("a");
    expect(reads).toHaveLength(1);
    draw("b"); // another connector while a's read is on its way
    expect(reads).toHaveLength(2);
    await answer(1, [running("b")]);
    await answer(0, [starting("a")]); // a's list has no b: applied to b it would say "not on hub"
    expect(text()).toContain("账号 b");
    expect(text()).toContain("已连接");
    expect(text()).not.toContain("hub 上没有这个连接器");
  });

  it("applies nothing once the page is gone", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    draw("x");
    act(() => root.unmount());
    root = createRoot(host);
    await answer(0, [running("x")]);
    expect(text()).toBe("");
    expect(errors).not.toHaveBeenCalled();
    errors.mockRestore();
  });
});
