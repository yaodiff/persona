// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { t } from "../i18n";
import type { Session } from "../lib/types";
import ArchivedNotice from "./ArchivedNotice";

const archive = vi.hoisted(() => ({ restore: vi.fn(), deps: {} }));
vi.mock("../lib/agent-archives", () => ({ restoreAgent: archive.restore, appArchiveDeps: archive.deps }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("keeps the archived notice and Restore action unchanged when loaded on demand", async () => {
  let finish!: () => void;
  archive.restore.mockReturnValue(new Promise<void>((resolve) => { finish = resolve; }));
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const session = { machine_id: "mac", session_id: "archived-example" } as Session;
  try {
    act(() => root.render(<ArchivedNotice session={session} />));
    expect(host.querySelector("[data-archived-notice]")?.textContent).toContain(t("chat-closed.archived"));
    const button = host.querySelector<HTMLButtonElement>("button")!;
    expect(button.textContent).toContain(t("agents-archives.restore"));
    act(() => button.click());
    expect(archive.restore).toHaveBeenCalledWith(session, archive.deps);
    expect(button.getAttribute("aria-busy")).toBe("true");
    await act(async () => { finish(); });
    expect(button.getAttribute("aria-busy")).not.toBe("true");
    const reason = "Stopped because its to-do was completed.";
    act(() => root.render(<ArchivedNotice session={{ ...session, read_only_reason: reason }} />));
    expect(host.textContent).toContain(reason);
    expect(host.textContent).not.toContain(t("chat-closed.archived"));
    expect(host.querySelector("button")?.textContent).toContain(t("agents-archives.restore"));
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});
