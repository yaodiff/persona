// @vitest-environment jsdom
// Export Conversation… (#7375): the dialog reads the whole conversation and says how far it is, then saves it as a
// .md file named after the chat or copies it; Include work steps reads it again unfolded; a failed read says why and
// reads again on Try Again; a Persona conversation's replies go under the assistant's name.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { Item, Session } from "../../lib/types";

const hub = vi.hoisted(() => ({
  queries: [] as string[],
  fail: 0,
  hold: null as Promise<void> | null,
}));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      const [route, query = ""] = path.split("?");
      if (!route.includes("/items")) throw new real.ApiError(404, "not found", {}); // the looks and the rest of the page
      hub.queries.push(query);
      if (hub.hold) await hub.hold;
      if (hub.fail > 0) {
        hub.fail--;
        throw new real.ApiError(503, "the computer is offline", {});
      }
      if (route.endsWith("/full")) throw new real.ApiError(404, "not found", {});
      const before = new URLSearchParams(query).get("before_seq");
      const items: Item[] = before
        ? [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "Why does login loop?", ts: "2026-11-14T10:00:00Z" }]
        : [
            { id: "u2#0", seq: 2, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: "t1", tool_input: { command: "npm test" }, ts: "2026-11-14T10:01:00Z" },
            { id: "u3#0", seq: 3, role: "assistant", kind: "text", text: "The guard runs twice.", ts: "2026-11-14T10:02:00Z" },
          ];
      return { items, has_more: !before };
    }),
  };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn(), copyText: vi.fn(async () => true) }));
import { copyText, toast } from "../../ui/atoms";
import { ExportDialog, type ExportTarget } from "./ExportDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session = { machine_id: "macbook", session_id: "s1", agent: "claude", cwd: "/repo/app", title: "Login loop" } as Session;
const target: ExportTarget = { session, title: "Login loop", machineName: "MacBook", assistant: false };
let host: HTMLDivElement;
let root: Root;
const onClose = vi.fn();
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((ok) => setTimeout(ok, 0)); });
};
const open = async (t2: ExportTarget = target) => {
  act(() => root.render(<ExportDialog open target={t2} onClose={onClose} />));
  await flush();
};
const byTestId = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`)!;
const status = () => byTestId("export-status").textContent;

let blobs: Blob[] = [];
beforeEach(() => {
  hub.queries.length = 0;
  hub.fail = 0;
  hub.hold = null;
  blobs = [];
  onClose.mockClear();
  vi.mocked(toast).mockClear();
  vi.mocked(copyText).mockClear();
  URL.createObjectURL = vi.fn((b: Blob) => (blobs.push(b), "blob:export"));
  URL.revokeObjectURL = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("the export dialog", () => {
  it("says it is reading, then how much it holds, and saves a .md named after the chat", async () => {
    let release = () => {};
    hub.hold = new Promise((ok) => (release = ok));
    act(() => root.render(<ExportDialog open target={target} onClose={onClose} />));
    await act(async () => {});
    expect(status()).toContain(t("chat-export.reading", { n: 0 }));
    expect((byTestId("export-download") as HTMLButtonElement).disabled).toBe(true);
    hub.hold = null;
    release();
    await flush();
    expect(hub.queries).toEqual(["turns=10&lazy_images=1&fold_work=1", "turns=10&lazy_images=1&fold_work=1&before_seq=2"]);
    expect(status()?.startsWith(t("chat-export.ready", { n: 2, size: "\u0000" }).split("\u0000")[0])).toBe(true);

    let clicked: HTMLAnchorElement | null = null;
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked = this;
    });
    act(() => byTestId("export-download").click());
    click.mockRestore();
    expect(clicked!.download).toMatch(/^Login loop \d{4}-\d{2}-\d{2}\.md$/);
    expect(blobs[0].type).toBe("text/markdown;charset=utf-8");
    const md = await new Promise<string>((ok) => {
      const r = new FileReader(); // jsdom's Blob has no text()
      r.onload = () => ok(String(r.result));
      r.readAsText(blobs[0]);
    });
    expect(md).toContain("# Login loop");
    expect(md).toContain(`- ${t("chat-export.computer")}: MacBook`);
    expect(md).toContain(`- ${t("chat-export.folder")}: /repo/app`);
    expect(md).toContain("Why does login loop?");
    expect(md).toContain("The guard runs twice.");
    expect(md).not.toContain("npm test"); // work steps are off
    expect(toast).toHaveBeenCalledWith(t("chat-export.downloaded", { name: clicked!.download }), "ok");
    expect(onClose).toHaveBeenCalled();
  });

  it("reads it again unfolded with work steps, and copies what it wrote", async () => {
    await open();
    hub.queries.length = 0;
    act(() => byTestId("export-work").click());
    await flush();
    expect(hub.queries[0]).toBe("turns=10&lazy_images=1");
    act(() => byTestId("export-copy").click());
    await flush();
    const md = vi.mocked(copyText).mock.calls[0][0];
    expect(md).toContain("<summary>Bash: npm test</summary>");
    expect(vi.mocked(copyText).mock.calls[0][1]).toEqual({ done: t("chat-export.copied") });
    expect(onClose).toHaveBeenCalled();
  });

  it("says why a read failed and reads again on Try Again", async () => {
    hub.fail = 1;
    await open();
    expect(document.querySelector('[data-testid="export-status"] [role="alert"]')?.textContent).toContain("the computer is offline");
    expect((byTestId("export-copy") as HTMLButtonElement).disabled).toBe(true);
    const retry = [...document.querySelectorAll("button")].find((b) => b.textContent === t("chat-export.retry"))!;
    act(() => retry.click());
    await flush();
    expect(status()).not.toContain("offline");
    expect((byTestId("export-copy") as HTMLButtonElement).disabled).toBe(false);
  });

  it("writes a Persona conversation's replies under the assistant's name", async () => {
    await open({ ...target, assistant: true, session: { ...session, agent: "claude" } });
    act(() => byTestId("export-copy").click());
    await flush();
    expect(vi.mocked(copyText).mock.calls[0][0]).toContain("## Persona · ");
  });
});
