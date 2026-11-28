// @vitest-environment jsdom
// The native extension's real confirmation component must resolve the occurrence it drew, not a same-ID replacement.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AppUIStreamPage, NativePagePermission } from "../../lib/extension-ui-app-stream";
const op = vi.hoisted(() => vi.fn());
vi.mock("../../lib/ops", async (original) => ({ ...(await original<typeof import("../../lib/ops")>()), apiOp: (...args: unknown[]) => op(...args) }));
import { PagePermission } from "./NativeExtensionUI";

let root: Root, host: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  op.mockReset();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); });

describe("native extension host confirmation", () => {
  it("sends the displayed occurrence with the answer", async () => {
    op.mockResolvedValue({ ok: true });
    const ix: NativePagePermission = {
      id: "native-permission", machine_id: "", session_id: "session", manager: "manager",
      kind: "permission", tool_name: "extension.ui", summary: "Confirm synthetic change",
      tool_input: { id: "save", confirm: "Confirm synthetic change" },
      created_at: "2026-01-02T03:04:05.000000001Z",
    };
    const adapter = { ownsHostConfirmation: () => true } as unknown as AppUIStreamPage;
    await act(async () => root.render(<PagePermission adapter={adapter} ix={ix} />));
    const buttons = [...document.body.querySelectorAll<HTMLButtonElement>("button")];
    await act(async () => buttons.at(-1)!.click());
    expect(op).toHaveBeenCalledWith("/interactions/native-permission/resolve", {
      method: "POST", json: { allow: true },
      headers: { "X-Persona-Interaction-Created-At": ix.created_at },
    });
  });
});
