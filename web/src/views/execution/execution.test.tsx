// @vitest-environment jsdom
// The four dimensions in the UI: a Persona chat's panel opens on the model and the effort only, the engine and channel
// folded into "Advanced / route" and narrowing one another; the three levels of a combination's check (allowed says
// nothing, a notice says the hub's words and marks an experimental bridge, blocked disables confirming and offers the
// hub's alternatives); and the last working setup brought back from the panel.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { anyBlocked, recommendedPicks, type CombinationVerdict, type ExecutionCatalog, type SpecPick } from "../../lib/execution";
import { effortTitle } from "../../lib/session-actions";
import type { Session } from "../../lib/types";
import { ExecutionCheck } from "./ExecutionCheck";
import { SimpleModelSheet } from "./SimpleModelSheet";
import { VerdictNote } from "./VerdictNote";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const allowed = (spec: CombinationVerdict["spec"], launcher: string): CombinationVerdict => ({ spec, level: "allowed", tier: "native", recommended: true, launcher });

const catalog: ExecutionCatalog = {
  runtimes: [
    { runtime: "claude", protocols: ["anthropic_messages"], efforts: ["low", "high"] },
    { runtime: "codex", protocols: ["openai_responses"], efforts: ["low", "high", "xhigh"] },
  ],
  channels: [
    { name: "native:claude", kind: "native", runtimes: ["claude"], models: [{ canonical: "opus", name: "Opus", efforts: ["low", "high"] }] },
    { name: "gw", display_name: "Team gateway", kind: "managed", runtimes: ["claude", "codex"], models: [{ canonical: "m1", name: "Model One", efforts: ["low", "high"] }] },
    { name: "solo", kind: "managed", runtimes: ["claude"], models: [{ canonical: "m9" }] },
  ],
  machines: [{ machine: "mac", name: "Mac", online: true, known: true, runtimes: ["claude", "codex"], channels: ["native:claude", "gw", "solo"] }],
  aliases: [
    { launcher: "claude", spec: { runtime: "claude", channel: "native:claude" } },
    { launcher: "gw-claude", spec: { runtime: "claude", channel: "gw" } },
    { launcher: "gw-codex", spec: { runtime: "codex", channel: "gw" } },
  ],
  combinations: [
    allowed({ runtime: "claude", channel: "native:claude", model: "opus", effort: "high" }, "claude"),
    allowed({ runtime: "claude", channel: "native:claude", model: "opus", effort: "low" }, "claude"),
    allowed({ runtime: "codex", channel: "gw", model: "m1", effort: "high" }, "gw-codex"),
    { spec: { runtime: "claude", channel: "gw", model: "m1", effort: "high" }, level: "notice", tier: "bridged_experimental", recommended: false, launcher: "gw-claude" },
  ],
};

type Call = { url: string; method: string; body?: Record<string, unknown> };
let calls: Call[];
let validate: (body: Record<string, unknown>) => CombinationVerdict;
let lastGood: unknown;
let restore: () => Response;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  await initI18n("en");
  calls = [];
  validate = (body) => ({ spec: body.spec as CombinationVerdict["spec"], level: "allowed", recommended: true });
  lastGood = null;
  restore = () => json({ error: "none" }, 404);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const call: Call = { url, method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    if (url.includes("/execution/validate")) return json(validate(call.body ?? {}));
    if (url.includes("/execution/catalog")) return json(catalog);
    if (url.endsWith("/model/last-good")) return json({ last_good: lastGood });
    if (url.endsWith("/model/restore")) return restore();
    return json({ ok: true });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
/** Past the check's short wait (useVerdict), then its answer. */
const settle = async () => {
  await act(async () => { await new Promise((r) => setTimeout(r, 260)); });
  await flush();
};
const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const testId = <T extends Element = HTMLElement>(id: string) => $<T>(`[data-testid="${id}"]`);
const options = (id: string) => [...(testId<HTMLSelectElement>(id)?.options ?? [])].map((o) => o.textContent);
async function choose(id: string, value: string) {
  await act(async () => {
    const el = testId<HTMLSelectElement>(id)!;
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}
const click = async (el: HTMLElement | null) => {
  await act(async () => el!.click());
  await flush();
};
const session = { machine_id: "mac", session_id: "c1", assistant: true } as Session;

describe("a Persona chat's model panel", () => {
  async function sheet(props: { kept?: Session; onApply?: (pick: SpecPick) => Promise<boolean>; onRestored?: (pick: SpecPick) => void; onNotice?: () => void; onMore?: () => void } = {}) {
    const onApply = props.onApply ?? vi.fn(async () => true);
    const onClose = vi.fn();
    await act(async () => root.render(
      <SimpleModelSheet
        catalog={catalog}
        picks={recommendedPicks(catalog)}
        machine="mac"
        current={{ launcher: "claude", model: "opus", effort: "high" }}
        kept={props.kept}
        onApply={onApply}
        onRestored={props.onRestored}
        onNotice={props.onNotice ?? vi.fn()}
        onMore={props.onMore ?? vi.fn()}
        onClose={onClose}
      />,
    ));
    await flush();
    return { onApply, onClose };
  }

  it("shows only the model and the effort, the recommended ones once each, and applies the matched route", async () => {
    const { onApply, onClose } = await sheet();
    expect(options("sheet-model")).toEqual(["Opus", "Model One"]);
    expect(options("sheet-effort")).toEqual([effortTitle("high"), effortTitle("low")]);
    // the engine and the channel are folded away; an experimental bridge is not among the choices
    expect(testId("route-runtime")).toBeNull();
    expect(testId("route-channel")).toBeNull();
    expect(document.body.textContent).not.toContain(t("execution.bridged_experimental"));
    // nothing changed yet: nothing to apply
    expect(testId<HTMLButtonElement>("model-sheet-apply")!.disabled).toBe(true);
    await choose("sheet-model", "m1");
    expect(options("sheet-effort")).toEqual([effortTitle("high")]);
    await click(testId("model-sheet-apply"));
    expect(onApply).toHaveBeenCalledWith({ launcher: "gw-codex", model: "m1", effort: "high", runtime: "codex", channel: "gw" });
    expect(onClose).toHaveBeenCalled();
    // the recommended combination's verdict is the catalog's: nothing to ask the hub
    expect(calls.filter((c) => c.url.includes("/execution/validate"))).toEqual([]);
  });

  it("opens all models and providers on request", async () => {
    const onMore = vi.fn();
    await sheet({ onMore });
    await click(testId("model-sheet-more"));
    expect(onMore).toHaveBeenCalled();
  });

  it("folds out the engine and channel, which narrow one another, and checks the route", async () => {
    await sheet();
    await click(testId("sheet-advanced"));
    expect(testId("sheet-model")).toBeNull();
    expect(options("route-runtime")).toEqual([t("execution.automatic"), "Claude Code", "Codex"]);
    expect(options("route-channel")).toEqual([t("execution.automatic"), t("execution.channel_native", { runtime: "Claude Code" }), "Team gateway", "solo"]);
    await choose("route-runtime", "codex");
    // the native sign-in does not run Codex, so it is cleared and left out
    expect(testId<HTMLSelectElement>("route-channel")!.value).toBe("");
    expect(options("route-channel")).toEqual([t("execution.automatic"), "Team gateway"]);
    // the gateway does not offer Opus: the model goes back to automatic, the channel's models offered
    await choose("route-channel", "gw");
    expect(testId<HTMLSelectElement>("route-model")!.value).toBe("");
    expect(options("route-model")).toEqual([t("execution.automatic"), "Model One"]);
    expect(options("route-effort")).toEqual([t("execution.automatic"), effortTitle("low"), effortTitle("high"), effortTitle("xhigh")]);
    await settle();
    expect(calls.filter((c) => c.url.includes("/execution/validate")).at(-1)?.body).toEqual({ spec: { runtime: "codex", channel: "gw", effort: "high" }, machine: "mac" });
  });

  it("disables Use on a blocked route and takes an alternative in one tap", async () => {
    validate = (body) => {
      const spec = body.spec as CombinationVerdict["spec"];
      return spec.channel === "solo"
        ? { spec, level: "blocked", recommended: false, reasons: [{ code: "no_common_protocol", dimension: "channel", text: "Codex can't speak this channel's protocol." }], alternatives: [{ runtime: "claude", channel: "solo", model: "m9" }] }
        : { spec, level: "allowed", recommended: true, launcher: "gw-claude" };
    };
    await sheet();
    await click(testId("sheet-advanced"));
    await choose("route-channel", "solo");
    await settle();
    const alert = $('[data-verdict="blocked"]');
    expect(alert?.getAttribute("role")).toBe("alert");
    expect(alert?.textContent).toContain("Codex can't speak this channel's protocol.");
    expect(testId<HTMLButtonElement>("model-sheet-apply")!.disabled).toBe(true);
    await click(testId("verdict-alternative-0"));
    expect(testId<HTMLSelectElement>("route-runtime")!.value).toBe("claude");
    expect(testId<HTMLSelectElement>("route-model")!.value).toBe("m9");
  });

  it("brings back the last working setup of a kept chat", async () => {
    lastGood = { spec: { runtime: "claude", channel: "native:claude", model: "opus", effort: "low" }, launcher: "claude", machine: "mac", at: "2026-11-27T00:00:00Z" };
    restore = () => json({ ok: true, model_pick: { launcher: "claude", model: "opus", effort: "low" }, last_good: lastGood });
    const onRestored = vi.fn();
    const onNotice = vi.fn();
    const { onClose } = await sheet({ kept: session, onRestored, onNotice });
    expect(testId("sheet-last-good")?.textContent).toContain(t("execution.last_good", { setup: `Opus · ${effortTitle("low")}` }));
    await click(testId("sheet-restore"));
    expect(calls.filter((c) => c.method === "POST" && c.url.endsWith("/assistant/conversations/c1/model/restore"))).toHaveLength(1);
    expect(onRestored).toHaveBeenCalledWith({ launcher: "claude", model: "opus", effort: "low" });
    expect(onNotice).toHaveBeenCalledWith(t("execution.restored"), "ok");
    expect(onClose).toHaveBeenCalled();
  });

  it("offers no restore for a new conversation", async () => {
    await sheet();
    expect(calls.filter((c) => c.url.endsWith("/model/last-good"))).toEqual([]);
    expect(testId("sheet-last-good")).toBeNull();
  });
});

describe("a combination's three levels", () => {
  it("says nothing when allowed, the hub's words with the bridge's mark on a notice", async () => {
    await act(async () => root.render(<VerdictNote verdict={{ spec: {}, level: "allowed", recommended: true }} />));
    expect(host.textContent).toBe("");
    await act(async () => root.render(
      <VerdictNote verdict={{ spec: {}, level: "notice", tier: "bridged_experimental", recommended: false, reasons: [{ code: "bridged_experimental", text: "Runs through an experimental bridge." }, { code: "effort_clamped", text: "Runs at high." }] }} />,
    ));
    expect($('[data-verdict="notice"]')?.getAttribute("role")).toBe("status");
    expect($("[data-bridged-experimental]")?.textContent).toBe(t("execution.bridged_experimental"));
    expect(host.textContent).toContain("Runs through an experimental bridge.");
    expect(host.textContent).toContain("Runs at high.");
  });

  it("blocks a form's confirm and hands back an alternative as a launcher pick", async () => {
    validate = (body) => {
      const spec = body.spec as CombinationVerdict["spec"];
      return spec.runtime === "codex"
        ? { spec, level: "blocked", recommended: false, reasons: [{ code: "model_not_offered", text: "The channel doesn't offer m9." }], alternatives: [{ runtime: "claude", channel: "gw", model: "m1", effort: "high" }] }
        : { spec, level: "allowed", recommended: true };
    };
    const onBlocked = vi.fn();
    const onPick = vi.fn();
    await act(async () => root.render(
      <ExecutionCheck machine="mac" launcher="gw-codex" model="m9" effort="high" catalog={catalog} scope="form-a" part="cand-1" onBlocked={onBlocked} onPick={onPick} />,
    ));
    await settle();
    expect(calls.filter((c) => c.url.includes("/execution/validate"))[0]?.body).toEqual({ spec: { runtime: "codex", channel: "gw", model: "m9", effort: "high" }, machine: "mac" });
    expect(onBlocked).toHaveBeenLastCalledWith(true);
    expect(anyBlocked("form-a")).toBe(true);
    expect(host.textContent).toContain("The channel doesn't offer m9.");
    await click(testId("verdict-alternative-0"));
    expect(onPick).toHaveBeenCalledWith({ launcher: "gw-claude", runtime: "claude", channel: "gw", model: "m1", effort: "high" });
    // unmounted, the form is no longer held
    act(() => root.render(<></>));
    expect(anyBlocked("form-a")).toBe(false);
  });

  it("picks another route through the engine and channel fields", async () => {
    validate = (body) => ({ spec: body.spec as CombinationVerdict["spec"], level: "allowed", recommended: true, launcher: "gw-claude" });
    const onPick = vi.fn();
    await act(async () => root.render(<ExecutionCheck machine="mac" launcher="claude" model="m1" effort="high" catalog={catalog} onPick={onPick} />));
    await settle();
    expect($('[data-verdict]')).toBeNull();
    await click(testId("execution-route-toggle"));
    expect(testId("route-model")).toBeNull(); // the form's own pickers choose the model and effort
    await choose("route-channel", "gw");
    await settle();
    await click(testId("execution-route-use"));
    expect(onPick).toHaveBeenCalledWith({ launcher: "gw-claude", runtime: "claude", channel: "gw", model: "m1", effort: "high" });
  });

  it("checks nothing for a launcher the hub doesn't know as an alias", async () => {
    await act(async () => root.render(<ExecutionCheck machine="mac" launcher="old-wrapper" model="x" catalog={catalog} onPick={vi.fn()} />));
    await settle();
    expect(calls.filter((c) => c.url.includes("/execution/validate"))).toEqual([]);
    expect(testId("execution-route-toggle")).not.toBeNull();
  });
});
