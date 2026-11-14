// @vitest-environment jsdom
// The health page's memory card (docs/assistant/health.md) after the hub comes back: a read that failed while it was
// away is read again on the next hello, as the space's conversations and reports are, and its error goes.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ hello: new Set<() => void>(), memoryReads: 0, memoryDown: true }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path.startsWith("/assistant/memory?space=health")) {
      hub.memoryReads++;
      return hub.memoryDown ? Promise.reject(new TypeError("Failed to fetch")) : Promise.resolve({ files: [] });
    }
    if (path.startsWith("/assistant?space=health")) return Promise.resolve({ machine_id: "mac", conversations: [] });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: () => void) => {
    if (type !== "hello") return () => {};
    hub.hello.add(l);
    return () => void hub.hello.delete(l);
  },
}));
vi.mock("../chat/ChatView", () => ({ ChatView: () => null }));

import { Health } from "./Health";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  hub.hello.clear();
  hub.memoryReads = 0;
  hub.memoryDown = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the health memory card", () => {
  it("reads again on the next hello after a read failed while the hub was away", async () => {
    act(() => root.render(<Health fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(hub.memoryReads).toBe(1);
    expect(host.textContent).toContain("没读到");

    hub.memoryDown = false;
    await act(async () => hub.hello.forEach((l) => l()));
    await flush();
    expect(hub.memoryReads).toBe(2);
    expect(host.textContent).not.toContain("没读到");
    expect(host.textContent).toContain(t("health.memory_empty"));
  });
});
