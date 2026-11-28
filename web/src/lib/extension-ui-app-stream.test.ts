// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { AppUIStreamPage, canTryUIStream, type AppStreamChannel } from "./extension-ui-app-stream";
import type { UIStreamRequest, UIStreamSnapshot } from "./extension-wire";
const pages: AppUIStreamPage[] = [];
afterEach(() => { pages.splice(0).forEach((p) => p.dispose()); });
const tick = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
function fixture(onEffect?: (page: AppUIStreamPage) => void) {
  let frames: (raw: string) => void = () => {}, connection: (connected: boolean) => void = () => {};
  let connected = true;
  const sent: UIStreamRequest[] = [], effects: unknown[] = [], failures: unknown[] = [];
  const channel: AppStreamChannel = { connected: () => connected, send: (r) => sent.push(r), frames: (fn) => { frames = fn; return () => {}; }, connection: (fn) => { connection = fn; return () => {}; } };
  const page = new AppUIStreamPage({ identity: { owner: "principal", hubGroup: "group", extension: "example", contribution: "home", navigation: "page" }, context: { platform: "web", ui_version: 7, stream: { protocol: 1, components: ["button"] } } }, "manager-a", channel, (r, event) => { effects.push({ r, event, version: page.session.getState().snapshot?.version }); onEffect?.(page); }, (e, fallback) => failures.push({ e: e.message, fallback }));
  pages.push(page);
  const snapshot: UIStreamSnapshot = { protocol: 1, session: "s", epoch: "e", version: 1, root_key: "root", view: { sections: [{ key: "section", items: [{ type: "button", key: "button", title: "Save", action: { id: "save" } }] }] } };
  const frame = (body: object) => frames(JSON.stringify({ type: "extension_ui_stream", manager: "manager-a", ...body }));
  const open = async () => { page.session.open(); frame({ kind: "snapshot", request_id: sent[0].request_id, snapshot }); await tick(); };
  const delta = (version = 2) => frame({ kind: "delta", delta: { protocol: 1, session: "s", epoch: "e", base_version: 1, version, operations: [] } });
  const receipt = (request: UIStreamRequest, result: object = { toast: "Saved" }) => {
    if (request.request !== "event") throw Error("event expected");
    frame({ kind: "receipt", request_id: request.request_id, receipt: { session: "s", epoch: "e", event_id: request.interaction.event_id, version: 2, status: "accepted", result } });
  };
  return { page, sent, effects, failures, snapshot, frame, open, delta, receipt, reconnect: (value: boolean) => { connected = value; connection(value); } };
}
describe("actual app UI stream adapter contract", () => {
  it("reuses the complete original open when the first snapshot is lost, without extra allocation requests", async () => {
    const f = fixture(); f.page.session.open();
    const original = f.sent[0];
    f.reconnect(false); await tick(); f.reconnect(true);
    expect(f.sent.filter((r) => r.request === "open")).toEqual([original, original]);
    f.frame({ kind: "snapshot", request_id: original.request_id, snapshot: f.snapshot }); await tick();
    expect(f.page.session.getState().phase).toBe("ready");
  });
  it.each(["resume", "snapshot"])("reopens an explicitly expired %s from canonical state, never retries callbacks", async (kind) => {
    const f = fixture(); f.snapshot.view.state = "canonical-navigation"; await f.open();
    const firstOpen = f.sent[0];
    if (kind === "resume") { f.reconnect(false); await tick(); f.reconnect(true); }
    else f.page.session.refresh();
    const request = f.sent.at(-1)!;
    expect(request.request).toBe(kind);
    f.frame({ kind: "error", request_id: request.request_id, error: { code: "expired", message: "Session expired" } });
    await tick();
    const reopened = f.sent.at(-1)!;
    expect(reopened).toMatchObject({ request: "open", state: "canonical-navigation" });
    expect(reopened.request_id).not.toBe(firstOpen.request_id);
    // Late frames for the expired session have neither current identity nor open correlation.
    f.frame({ kind: "snapshot", request_id: request.request_id, snapshot: f.snapshot });
    expect(f.page.session.getState().snapshot).toBeNull();
    f.frame({ kind: "snapshot", request_id: reopened.request_id, snapshot: { ...f.snapshot, session: "replacement", epoch: "new-epoch" } }); await tick();
    expect(f.page.session.getState()).toMatchObject({ phase: "ready", pending: 0, snapshot: { session: "replacement" } });
    expect(f.sent.filter((r) => r.request === "event")).toHaveLength(0);
    expect(f.failures).toEqual([]);
  });
  it.each(["inactive", "manager_unavailable", "rejected", "forbidden"])("does not reopen a transient or unclassified %s lifecycle error", async (code) => {
    const f = fixture(); await f.open(); f.reconnect(false); await tick(); f.reconnect(true);
    f.frame({ kind: "error", request_id: f.sent.at(-1)!.request_id, error: { code, message: "Session expired" } }); await tick();
    expect(f.sent.filter((r) => r.request === "open")).toHaveLength(1);
    expect(f.page.session.getState().phase).toBe("recovering");
    f.page.session.refresh();
    expect(f.sent.at(-1)).toMatchObject({ request: "snapshot", session: "s" });
  });
  it.each(["ambiguous", "rejected"] as const)("returns false settlement for a %s native control without retry/recovery", async (status) => {
    const f = fixture(); await f.open();
    const event = f.page.session.eventDisplayed(f.page.session.getState().snapshot!, "button", "save", "press", {});
    const request = f.sent.at(-1)!;
    if (request.request !== "event") throw Error("event expected");
    if (status === "ambiguous") f.delta();
    f.frame({ kind: "receipt", request_id: request.request_id, receipt: { session: "s", epoch: "e", event_id: request.interaction.event_id, version: status === "ambiguous" ? 2 : 1, status } });
    await expect(event).resolves.toBeNull(); await tick();
    expect(f.page.session.getState()).toMatchObject({ phase: "ready", pending: 0 });
    expect(f.sent.filter((r) => r.request === "event")).toHaveLength(1);
    expect(f.sent.filter((r) => r.request === "snapshot")).toHaveLength(0);
    expect(f.effects).toEqual([]);
    await expect(f.page.session.retryEvent(request.interaction.event_id)).rejects.toThrow("No retryable event");
  });
  it("evicts bounded receipt history while presenting callback 257 and rejecting delayed unmatched replays", async () => {
    const f = fixture(); await f.open();
    let first: UIStreamRequest | undefined;
    for (let version = 1; version <= 258; version++) {
      const event = f.page.session.eventDisplayed(f.page.session.getState().snapshot!, "button", "save", "press", {});
      const request = f.sent.at(-1)!; first ??= request;
      if (request.request !== "event") throw Error("event expected");
      f.frame({ kind: "delta", delta: { protocol: 1, session: "s", epoch: "e", base_version: version, version: version + 1, operations: [] } });
      f.frame({ kind: "receipt", request_id: request.request_id, receipt: { session: "s", epoch: "e", event_id: request.interaction.event_id, version: version + 1, status: "accepted", result: { toast: `Saved ${version}` } } });
      expect(await event).not.toBeNull(); await tick();
    }
    expect(f.page.session.getState()).toMatchObject({ phase: "ready", pending: 0 });
    expect(f.effects).toHaveLength(258);
    expect(f.effects[256]).toMatchObject({ r: { toast: "Saved 257" }, version: 258 });
    expect(f.failures).toEqual([]);
    if (first?.request !== "event") throw Error("event expected");
    expect(await f.page.readResult(first.interaction)).toBeNull();
    f.receipt(first, { toast: "Saved 1" });
    expect(f.effects).toHaveLength(258);
  });
  it("requires the explicit backend hello feature", () => {
    expect(canTryUIStream([])).toBe(false);
    expect(canTryUIStream(["extension_ui_stream"])).toBe(false);
    expect(canTryUIStream(["extension_ui_stream_v1"])).toBe(true);
  });
  it("rejects a same-version consumed receipt and a larger empty delta before effects", async () => {
    const f = fixture(); await f.open();
    const event = f.page.session.event("button", "save", "press", null).catch(() => {});
    const request = f.sent.at(-1)!;
    if (request.request !== "event") throw Error("event expected");
    f.frame({ kind: "receipt", request_id: request.request_id, receipt: { session: "s", epoch: "e", event_id: request.interaction.event_id, version: 1, status: "accepted", result: { toast: "Wrong" } } });
    expect(f.effects).toEqual([]);
    f.delta(3);
    expect(f.page.session.getState().snapshot?.version).toBe(1);
    expect(f.page.session.getState().phase).toBe("recovering");
    f.page.dispose(); await event;
  });
  it("settles an ambiguous fence without effects or a fresh callback", async () => {
    const f = fixture(); await f.open();
    const event = f.page.session.event("button", "save", "press", null);
    const request = f.sent.at(-1)!;
    if (request.request !== "event") throw Error("event expected");
    f.delta();
    f.frame({ kind: "receipt", request_id: request.request_id, receipt: { session: "s", epoch: "e", event_id: request.interaction.event_id, version: 2, status: "ambiguous" } });
    await event; await tick();
    expect(f.effects).toEqual([]);
    expect(f.sent.filter((r) => r.request === "event")).toHaveLength(1);
    expect(f.failures).toMatchObject([{ e: "ambiguous", fallback: false }]);
  });
  it("preserves native lines, push and HTTPS projections only after the atomic fence", async () => {
    const f = fixture(); await f.open();
    const event = f.page.session.eventDisplayed(f.page.session.getState().snapshot!, "button", "save", "press", { path: "demo.ts", start: "7" });
    const request = f.sent.at(-1)!;
    f.receipt(request, { toast: "Saved", push: { ui: "detail", state: "saved-native-state" }, open_url: "https://example.com/", lines: { path: "demo.ts", start: 7, lines: ["synthetic line"] } });
    expect(f.effects).toEqual([]);
    f.delta();
    const sent = await event;
    expect(sent).not.toBeNull();
    expect(await f.page.readResult(sent!)).toEqual({ lines: { path: "demo.ts", start: 7, lines: ["synthetic line"] } });
    expect(f.effects).toMatchObject([{ version: 2, event: { value: { path: "demo.ts", start: "7" } }, r: { push: { state: "saved-native-state" }, open_url: "https://example.com/" } }]);
  });
  it("presents a page's own quick prompt as its one in-app receipt URL (#16697)", async () => {
    const f = fixture(); await f.open();
    const event = f.page.session.eventDisplayed(f.page.session.getState().snapshot!, "button", "save", "press", {});
    const request = f.sent.at(-1)!;
    f.receipt(request, { open_url: "persona://compose?prompt=today&text=What%20happened%20today%3F" });
    f.delta();
    expect(await event).not.toBeNull(); await tick();
    expect(f.failures).toEqual([]);
    expect(f.effects).toMatchObject([{ r: { open_url: "persona://compose?prompt=today&text=What%20happened%20today%3F" } }]);
  });
  it("settles the native caller before a receipt effect synchronously disposes its navigation page", async () => {
    const f = fixture((page) => page.dispose()); await f.open();
    const event = f.page.session.event("button", "save", "press", {});
    const request = f.sent.at(-1)!;
    f.delta(); f.receipt(request, { pop: true });
    await expect(event).resolves.toBeUndefined(); await tick();
    expect(f.effects).toHaveLength(1);
    expect(f.page.session.getState().phase).toBe("closed");
    expect(f.failures).toEqual([]);
  });
  it("recognizes only its actual manager's exact pending saved-action permission, never generic Agent approval", async () => {
    const f = fixture();
    const button = f.snapshot.view.sections[0].items[0];
    if (button.type !== "button") throw Error("button expected");
    button.action.confirm = "Confirm synthetic change";
    await f.open();
    const event = f.page.session.event("button", "save", "press", {}).catch(() => {});
    const ix = { manager: "manager-a", id: "permission", machine_id: "", session_id: "s", kind: "permission" as const, tool_name: "extension.ui", summary: "Confirm synthetic change", tool_input: { id: "save", confirm: "Confirm synthetic change" } };
    expect(f.page.ownsHostConfirmation(ix)).toBe(true);
    for (const changed of [{ manager: "manager-b" }, { session_id: "foreign" }, { machine_id: "agent-computer" }, { tool_name: "Bash" }, { tool_input: { id: "save", confirm: "Different", authority: true } }]) expect(f.page.ownsHostConfirmation({ ...ix, ...changed })).toBe(false);
    f.reconnect(false);
    expect(f.page.ownsHostConfirmation(ix)).toBe(false);
    f.page.dispose(); await event;
  });
  it("waits for the event receipt even when a large update is published as a snapshot", async () => {
    const f = fixture(); await f.open();
    let resolved = false;
    const event = f.page.session.event("button", "save", "press", null).then(() => { resolved = true; });
    const request = f.sent.at(-1)!;
    f.frame({ kind: "snapshot", request_id: request.request_id, snapshot: { ...f.snapshot, version: 2 } });
    await tick();
    expect(f.page.session.getState().snapshot?.version).toBe(2);
    expect(resolved).toBe(false); expect(f.effects).toEqual([]);
    f.receipt(request, { toast: "Saved", pop: true });
    await event; await tick();
    expect(f.effects).toMatchObject([{ r: { toast: "Saved", pop: true }, version: 2 }]);
  });
  it("disposes the owned source session on a non-cached document navigation", async () => {
    const f = fixture(); await f.open();
    window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: false }));
    expect(f.sent.at(-1)).toMatchObject({ request: "close", session: "s", epoch: "e" });
    expect(f.page.session.getState().phase).toBe("closed");
  });
  it("opens explicit empty state and waits for the receipt fence before presentation and result resolution", async () => {
    const f = fixture(); await f.open();
    expect(f.sent[0]).toMatchObject({ request: "open", state: "" });
    const event = f.page.session.event("button", "save", "press", null);
    const request = f.sent.at(-1)!;
    f.receipt(request); expect(f.effects).toEqual([]); expect(f.page.session.getState().pending).toBe(1);
    f.delta(); await event; await tick();
    expect(f.effects).toMatchObject([{ r: { toast: "Saved" }, version: 2 }]);
    expect(f.page.session.getState().snapshot?.view).toEqual(f.snapshot.view);
  });
  it("does not apply another manager's correlated snapshot or another event's receipt", async () => {
    const f = fixture(); f.page.session.open();
    f.frame({ kind: "snapshot", manager: "manager-b", request_id: f.sent[0].request_id, snapshot: f.snapshot });
    expect(f.page.session.getState().snapshot).toBeNull();
    f.frame({ kind: "snapshot", request_id: f.sent[0].request_id, snapshot: f.snapshot }); await tick();
    const event = f.page.session.event("button", "save", "press", null).catch(() => {});
    f.frame({ kind: "receipt", request_id: f.sent.at(-1)!.request_id, receipt: { session: "s", epoch: "e", event_id: "other", version: 1, status: "accepted", result: { toast: "Wrong" } } });
    expect(f.effects).toEqual([]); f.page.dispose(); await event;
  });
  it("reconnects snapshot-first, never resends an uncertain callback", async () => {
    const f = fixture(); await f.open();
    const event = f.page.session.event("button", "save", "press", null).catch(() => {});
    f.reconnect(false); await event; await tick(); f.reconnect(true);
    expect(f.sent.at(-1)).toMatchObject({ request: "resume", session: "s", version: 1 });
    f.delta(); expect(f.page.session.getState().snapshot?.version).toBe(1);
    f.frame({ kind: "snapshot", request_id: f.sent.at(-1)!.request_id, snapshot: { ...f.snapshot, version: 2 } }); await tick();
    expect(f.page.session.getState().phase).toBe("ready");
    expect(f.sent.filter((r) => r.request === "event")).toHaveLength(1);
  });
  it("replays the frozen event after an advanced snapshot without borrowing the new callback", async () => {
    const f = fixture(); await f.open();
    const event = f.page.session.event("button", "save", "press", { saved: "native" }).catch(() => {});
    const original = f.sent.at(-1)!;
    if (original.request !== "event") throw Error("event expected");
    f.reconnect(false); await event; await tick(); f.reconnect(true);
    const next = structuredClone(f.snapshot); next.version = 2;
    next.view.sections[0].items = [{ type: "button", key: "button", title: "New", action: { id: "new-action" } }];
    f.frame({ kind: "snapshot", request_id: f.sent.at(-1)!.request_id, snapshot: next }); await tick();
    const replayed = f.page.session.retryEvent(original.interaction.event_id);
    const replay = f.sent.at(-1)!;
    expect(replay).toMatchObject({ interaction: original.interaction });
    expect(replay.request_id).not.toBe(original.request_id);
    f.receipt(replay); await replayed; await tick();
    f.receipt(replay); expect(f.effects).toHaveLength(1);
    expect(f.effects).toMatchObject([{ event: { action: "save", value: { saved: "native" } }, version: 2 }]);
  });
  it("rejects result-derived views and credentials before any effects", async () => {
    for (const result of [
      { toast: "Wrong", view: {} },
      { open_url: "https://user:pass@example.com" },
      { open_url: "https:/admin" },
      { open_url: "https://@evil.example/a" },
      { open_url: "https://good.example\\@evil.example/a" },
      { open_url: "https://evil.example/a\tb" },
      // the one in-app URL allowed is a well-formed persona://compose; no other persona:// action, and malformed compose, pass
      { open_url: "persona://settings/extensions/other" },
      { open_url: "persona://ask?text=today" },
      { open_url: "persona://compose?prompt=today" },
      { open_url: "persona://compose?prompt=today&text=today&x=1" },
      { push: { state: "é".repeat(9000) } },
    ]) {
      const f = fixture(); await f.open();
      const event = f.page.session.event("button", "save", "press", null).catch(() => {});
      const request = f.sent.at(-1)!; f.delta(); f.receipt(request, result);
      expect(f.effects).toEqual([]); expect(f.failures.length).toBeGreaterThan(0);
      f.page.dispose(); await event;
    }
  });
});
