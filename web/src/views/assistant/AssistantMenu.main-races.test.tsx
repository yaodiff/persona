// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { COMPACT_FEATURE, NEW_MAIN_FEATURE } from "../../lib/compaction";
import { CLEAR_MAIN_FEATURE, clearMainContext, forgetMainCleared } from "../../lib/main-clear";
import { getState, reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import type { MenuItem } from "../../ui/atoms";
import { useAssistantMenu } from "./AssistantChat";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

const makeSession = (session_id: string): Session =>
  ({
    machine_id: "hub",
    session_id,
    title: session_id,
    cwd: "/",
    assistant: true,
    status: "idle",
  }) as Session;

let capturedItems: MenuItem[] = [];
let capturedDialogOpen = false;

function Probe({ session }: { session?: Session }) {
  const { items, dialogs } = useAssistantMenu(session);
  capturedItems = items;
  capturedDialogOpen = Boolean(dialogs && typeof dialogs === "object" && "props" in dialogs && (dialogs as { props?: { open?: boolean } }).props?.open);
  return null;
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  reset();
  forgetMainCleared();
  capturedItems = [];
  capturedDialogOpen = false;
  Object.assign(getState(), {
    features: [COMPACT_FEATURE, CLEAR_MAIN_FEATURE, NEW_MAIN_FEATURE],
    assistant: { machine_id: "hub", main: "main-1", conversations: [] },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  forgetMainCleared();
  reset();
});

describe("Header Assistant menu parity & scope/ABA guards", () => {
  it("disables Clear context while a clear request is in flight and re-enables once settled", async () => {
    const main = makeSession("main-1");
    act(() => root.render(<Probe session={main} />));
    expect(capturedItems.find((i) => i.key === "clear")?.disabled).toBe(false);

    const gate = deferred<void>();
    let pending!: Promise<boolean>;
    act(() => {
      pending = clearMainContext("main-1", {
        post: () => gate.promise,
        refresh: async () => {},
        home: () => {},
        toast: () => {},
      });
    });
    expect(capturedItems.find((i) => i.key === "clear")?.disabled).toBe(true);

    await act(async () => {
      gate.resolve();
      await pending;
    });
    expect(capturedItems.find((i) => i.key === "clear")?.disabled).toBe(false);
  });

  it("resets New Main Chat dialog open state fail-closed across session and ABA switches", () => {
    const main = makeSession("main-1");
    const side = makeSession("side-1");

    act(() => root.render(<Probe session={main} />));
    expect(capturedDialogOpen).toBe(false);

    act(() => {
      capturedItems.find((i) => i.key === "newmain")!.onSelect();
    });
    expect(capturedDialogOpen).toBe(true);

    // Switch to side chat and back to main chat (ABA): dialog must stay closed.
    act(() => root.render(<Probe session={side} />));
    expect(capturedDialogOpen).toBe(false);
    act(() => root.render(<Probe session={main} />));
    expect(capturedDialogOpen).toBe(false);
  });
});
