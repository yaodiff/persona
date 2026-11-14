// @vitest-environment jsdom
// "Disconnect this browser"'s question is a chunk of its own (audit item 20). When that chunk won't load (the hub was
// redeployed, or it is offline), the page that asked stays as it was, nothing is revoked, a toast says why, and the next
// Disconnect tries the chunk again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Revoke } from "../lib/disconnect";

const fake = vi.hoisted(() => ({
  revoke: vi.fn<() => Promise<Revoke>>(),
  signOut: vi.fn<(unrevoked?: string) => void>(),
  toast: vi.fn<(text: string, tone?: string) => void>(),
  chunkFails: true,
}));
vi.mock("../lib/disconnect", () => ({ revokeThisBrowser: () => fake.revoke(), signOutHere: (id?: string) => fake.signOut(id) }));
vi.mock("../lib/api", () => ({ handleUnauthorized: () => () => {} }));
vi.mock("../ui/atoms", async (importOriginal) => ({ ...(await importOriginal<typeof import("../ui/atoms")>()), toast: (text: string, tone?: string) => fake.toast(text, tone) }));
// the question's chunk: the browser's own words for a module it could not fetch, until it can again
vi.mock("./DisconnectConfirm", async (importOriginal) => {
  const real = await importOriginal<typeof import("./DisconnectConfirm")>();
  return {
    get DisconnectConfirm() {
      if (fake.chunkFails) throw new TypeError("Failed to fetch dynamically imported module: /src/views/DisconnectConfirm.tsx");
      return real.DisconnectConfirm;
    },
  };
});

import { t } from "../i18n";
import { useDisconnect } from "./DisconnectBrowser";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

function Probe() {
  const d = useDisconnect();
  return (
    <main data-host>
      <button type="button" data-start onClick={d.start}>
        {t("settings.disconnect_browser")}
      </button>
      {d.dialog}
    </main>
  );
}

const settle = async () => {
  for (let i = 0; i < 20; i++) await act(() => new Promise((done) => setTimeout(done, 5)));
};
const confirmButton = () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.textContent?.includes(t("disconnect-confirm.confirm")));

beforeEach(() => {
  fake.revoke.mockReset();
  fake.signOut.mockReset();
  fake.toast.mockReset();
  fake.chunkFails = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Probe />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("useDisconnect when the question's chunk won't load", () => {
  it("keeps the page, revokes nothing, says why, and tries again on the next Disconnect", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    await act(async () => document.querySelector<HTMLButtonElement>("button[data-start]")!.click());
    await settle();
    // the page that asked is still drawn: no blank app
    expect(host.querySelector("[data-host]")).not.toBeNull();
    expect(host.querySelector("button[data-start]")).not.toBeNull();
    expect(confirmButton()).toBeUndefined();
    expect(fake.revoke).not.toHaveBeenCalled();
    expect(fake.signOut).not.toHaveBeenCalled();
    expect(fake.toast).toHaveBeenCalledWith(t("pageboundary.chunk_body"), "error");
    errors.mockRestore();

    // the chunk is back: the next Disconnect asks, and still nothing is revoked until it is confirmed
    fake.chunkFails = false;
    await act(async () => document.querySelector<HTMLButtonElement>("button[data-start]")!.click());
    await settle();
    expect(confirmButton()).toBeDefined();
    expect(fake.revoke).not.toHaveBeenCalled();
  });
});
