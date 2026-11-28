// @vitest-environment jsdom
// "Start new Agents as Orca chats" under a computer's Engine (#17650): shown only where the hub sends the switch (Orca in the
// engine set), flipped at once and settled on the hub's answer, rolled back with a message when the save fails.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { Machine } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const mocks = vi.hoisted(() => ({ api: vi.fn(), applyMachine: vi.fn(), toast: vi.fn() }));

vi.mock("../../lib/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/api")>();
  return { ...real, api: (...args: unknown[]) => mocks.api(...args) };
});
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return { ...real, applyMachine: (...args: unknown[]) => mocks.applyMachine(...args) };
});
vi.mock("../../ui/atoms", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../ui/atoms")>();
  return { ...real, toast: (...args: unknown[]) => mocks.toast(...args) };
});

import { EngineSection } from "./EngineSection";

function machine(hosts: string[], orca?: string): Machine {
  return {
    id: "box", name: "box", online: true, engine: hosts[0] ?? "herdr", engine_hosts: hosts, engine_default: hosts[0] ?? "",
    engines: [{ id: "herdr", name: "herdr", available: true }, { id: "orca", name: "Orca", available: true }],
    ...(orca === undefined ? {} : { orca_chat_starts: orca }),
  } as Machine;
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (m: Machine) => {
  await act(async () => root.render(<EngineSection machine={m} heading={<h2>Engine</h2>} />));
  await flush();
};
const toggle = () => host.querySelector<HTMLButtonElement>('[data-testid="orca-chat-starts"] button[role="switch"]');

beforeEach(() => {
  mocks.api.mockReset();
  mocks.applyMachine.mockReset();
  mocks.toast.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("OrcaChatStarts", () => {
  it("shows only where the hub sends the switch", async () => {
    await render(machine(["herdr"]));
    expect(host.querySelector('[data-testid="orca-chat-starts"]')).toBeNull();
    await render(machine(["orca"], "later-state"));
    expect(host.querySelector('[data-testid="orca-chat-starts"]')).toBeNull();
    await render(machine(["herdr", "orca"], "off"));
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
    expect(toggle()?.getAttribute("aria-label")).toBe(t("machines-orcachatstarts.title"));
    expect(host.textContent).toContain(t("machines-orcachatstarts.detail"));
  });

  it("flips at once, saves, and follows the hub's answer", async () => {
    let resolve!: (v: { on: boolean }) => void;
    mocks.api.mockReturnValueOnce(new Promise((r) => { resolve = r; }));
    await render(machine(["orca"], "off"));
    await act(async () => toggle()!.click());
    expect(toggle()?.getAttribute("aria-checked")).toBe("true"); // before the hub answers
    expect(mocks.api).toHaveBeenCalledWith("/machines/box/orca-chat-starts", { method: "PUT", json: { on: true } });
    await act(async () => resolve({ on: true }));
    await flush();
    expect(toggle()?.getAttribute("aria-checked")).toBe("true");
    expect(mocks.applyMachine).toHaveBeenCalledWith({ id: "box", orca_chat_starts: "on" });
    // the machine's push says so too; later another device turns it off, and that push moves the switch
    await render(machine(["orca"], "on"));
    expect(toggle()?.getAttribute("aria-checked")).toBe("true");
    await render(machine(["orca"], "off"));
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
  });

  it("goes back and says why when the save fails", async () => {
    mocks.api.mockRejectedValueOnce(new Error("offline"));
    await render(machine(["orca"], "off"));
    await act(async () => toggle()!.click());
    await flush();
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
    expect(mocks.toast).toHaveBeenCalledWith(t("machines-orcachatstarts.save_failed", { error: "offline" }), "error");
    expect(mocks.applyMachine).not.toHaveBeenCalled();
  });
});
