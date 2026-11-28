// @vitest-environment jsdom
// Controlled panels supply catalog, capabilities, quotas, and mutation callbacks without touching default singletons; default callers still use every fallback.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AssistantChat, AssistantSetup } from "../../lib/assistant-models";
import type { ConversationSwitchRequest, ConversationSwitchState } from "../../lib/conversation-switch";
import type { LauncherQuotaAnswer } from "../../lib/quota";
import type { AssistantLaunchers, ModelList } from "../../lib/session-models";
import type { Session } from "../../lib/types";
import type { ModelPanelProps } from "../../ui/model-panel/ModelPanel";
import type { AssistantModelPanelProps } from "./AssistantModelPanel";

const mocks = vi.hoisted(() => ({
  panelProps: null as ModelPanelProps | null,
  api: vi.fn(),
  toast: vi.fn(),
  useStore: vi.fn(),
  getSession: vi.fn(),
  upsertSession: vi.fn(),
  refreshAssistant: vi.fn(),
  useChosenManager: vi.fn(),
  useModelLists: vi.fn(),
  assistantLaunchers: vi.fn(),
  modelList: vi.fn(),
  readModels: vi.fn(),
  forgetModel: vi.fn(),
  usePanelQuotas: vi.fn(),
  assistantSetup: vi.fn(),
  pickLauncher: vi.fn(),
  pickModel: vi.fn(),
  pickEffort: vi.fn(),
  pickServiceTier: vi.fn(),
  dropConversationPick: vi.fn(),
  conversationSwitchState: vi.fn(),
  prepareConversationSwitch: vi.fn(),
  submitConversationSwitch: vi.fn(),
  setServiceTier: vi.fn(),
}));

vi.mock("../../ui/model-panel/ModelPanel", () => ({
  ModelPanel: (props: ModelPanelProps) => {
    mocks.panelProps = props;
    return <div data-testid="mock-model-panel">{props.header}</div>;
  },
}));
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/atoms")>()),
  toast: (...args: unknown[]) => mocks.toast(...args),
}));
vi.mock("../../lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api")>()),
  api: (...args: unknown[]) => mocks.api(...args),
}));
vi.mock("../../lib/store", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/store")>()),
  useStore: (selector: (s: unknown) => unknown) => mocks.useStore(selector),
  getSession: (...args: unknown[]) => mocks.getSession(...args),
  upsertSession: (...args: unknown[]) => mocks.upsertSession(...args),
  refreshAssistant: (...args: unknown[]) => mocks.refreshAssistant(...args),
}));
vi.mock("../../lib/manager-choice", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/manager-choice")>()),
  useChosenManager: () => mocks.useChosenManager(),
}));
vi.mock("../../lib/session-models", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/session-models")>()),
  useModelLists: () => mocks.useModelLists(),
  assistantLaunchers: (...args: unknown[]) => mocks.assistantLaunchers(...args),
  modelList: (...args: unknown[]) => mocks.modelList(...args),
  readModels: (...args: unknown[]) => mocks.readModels(...args),
  forgetModel: (...args: unknown[]) => mocks.forgetModel(...args),
}));
vi.mock("../../lib/panel-quotas", () => ({
  usePanelQuotas: (...args: unknown[]) => mocks.usePanelQuotas(...args),
}));
vi.mock("../../lib/assistant-models", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/assistant-models")>()),
  assistantSetup: (...args: unknown[]) => mocks.assistantSetup(...args),
}));
vi.mock("../../lib/assistant-chat", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/assistant-chat")>()),
  pickLauncher: (...args: unknown[]) => mocks.pickLauncher(...args),
  pickModel: (...args: unknown[]) => mocks.pickModel(...args),
  pickEffort: (...args: unknown[]) => mocks.pickEffort(...args),
  pickServiceTier: (...args: unknown[]) => mocks.pickServiceTier(...args),
}));
vi.mock("../../lib/assistant-pick", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/assistant-pick")>()),
  dropConversationPick: (...args: unknown[]) => mocks.dropConversationPick(...args),
}));
vi.mock("../../lib/conversation-switch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/conversation-switch")>()),
  conversationSwitchState: (...args: unknown[]) => mocks.conversationSwitchState(...args),
  prepareConversationSwitch: (...args: unknown[]) => mocks.prepareConversationSwitch(...args),
  submitConversationSwitch: (...args: unknown[]) => mocks.submitConversationSwitch(...args),
}));
vi.mock("./SessionDialogs", () => ({
  setServiceTier: (...args: unknown[]) => mocks.setServiceTier(...args),
}));

const { AssistantModelPanel } = await import("./AssistantModelPanel");
const { ApiError } = await import("../../lib/api");
const { t } = await import("../../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const claudeList: ModelList = {
  list: {
    agent: "claude",
    models: [
      { id: "default", name: "Default", default: true, resolved: "claude-opus-5-5", efforts: ["low", "high"] },
      { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: ["low", "high"] },
      { id: "sonnet", name: "Sonnet 5.5", resolved: "claude-sonnet-5-5", efforts: ["low", "high"] },
    ],
  },
};

const codexList: ModelList = {
  list: {
    agent: "codex",
    models: [
      {
        id: "gpt-fast",
        name: "GPT Fast",
        efforts: ["low", "high"],
        service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }, { id: "ultrafast", name: "Ultrafast" }],
      },
    ],
  },
};

const launchersInfo: AssistantLaunchers = {
  launchers: [
    { name: "claude", source: "builtin", agent: "claude" },
    { name: "relay", source: "zshrc", agent: "claude", display_name: "Relay" },
    { name: "hosted-claude", source: "runtime-other", agent: "claude", assistant: true, supervision_policy: false },
    { name: "codex", source: "builtin", agent: "codex", agent_type: "codex" },
  ],
  def: { provider: "claude", model: "opus", effort: "high" },
  unavailable: [],
};

const makeSession = (extra: Partial<Session> = {}): Session => ({
  machine_id: "example-machine",
  session_id: "example-conv",
  title: "Assistant",
  cwd: "/workspace/example",
  status: "idle",
  state: "idle",
  assistant: true,
  agent: "claude",
  launcher: "claude",
  model: "claude-opus-5-5",
  effort: "high",
  ...extra,
});

const makeChat = (extra: Partial<AssistantChat> = {}): AssistantChat => ({
  session: makeSession(),
  isMain: true,
  followsDefault: true,
  kept: true,
  config: "claude",
  ...extra,
});

const lists: Record<string, ModelList> = {
  claude: claudeList,
  relay: claudeList,
  "hosted-claude": claudeList,
  codex: codexList,
};
const controlledListOf = (provider: string): ModelList => lists[provider] ?? {};
const sampleQuotas: Readonly<Record<string, LauncherQuotaAnswer>> = {
  claude: { checked_at: "2026-11-25T10:00:00Z", quota: { gateway: "claude", gateway_name: "Claude", budget: [] } },
};

let host: HTMLDivElement;
let anchor: HTMLButtonElement;
let root: Root;
let onClose: ReturnType<typeof vi.fn<() => void>>;

const mount = async (props: Partial<AssistantModelPanelProps> = {}) => {
  await act(async () => {
    root.render(
      <AssistantModelPanel
        chat={makeChat()}
        mid="example-machine"
        anchor={anchor}
        onClose={onClose}
        {...props}
      />,
    );
  });
};

const flush = async () => {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const dialogButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;

const expectNoDefaultSingletons = () => {
  expect(mocks.api).not.toHaveBeenCalled();
  expect(mocks.toast).not.toHaveBeenCalled();
  expect(mocks.useStore).not.toHaveBeenCalled();
  expect(mocks.getSession).not.toHaveBeenCalled();
  expect(mocks.upsertSession).not.toHaveBeenCalled();
  expect(mocks.refreshAssistant).not.toHaveBeenCalled();
  expect(mocks.useChosenManager).not.toHaveBeenCalled();
  expect(mocks.useModelLists).not.toHaveBeenCalled();
  expect(mocks.assistantLaunchers).not.toHaveBeenCalled();
  expect(mocks.modelList).not.toHaveBeenCalled();
  expect(mocks.readModels).not.toHaveBeenCalled();
  expect(mocks.forgetModel).not.toHaveBeenCalled();
  expect(mocks.usePanelQuotas).not.toHaveBeenCalled();
  expect(mocks.assistantSetup).not.toHaveBeenCalled();
  expect(mocks.pickLauncher).not.toHaveBeenCalled();
  expect(mocks.pickModel).not.toHaveBeenCalled();
  expect(mocks.pickEffort).not.toHaveBeenCalled();
  expect(mocks.pickServiceTier).not.toHaveBeenCalled();
  expect(mocks.dropConversationPick).not.toHaveBeenCalled();
  expect(mocks.conversationSwitchState).not.toHaveBeenCalled();
  expect(mocks.prepareConversationSwitch).not.toHaveBeenCalled();
  expect(mocks.submitConversationSwitch).not.toHaveBeenCalled();
  expect(mocks.setServiceTier).not.toHaveBeenCalled();
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.panelProps = null;
  mocks.useStore.mockImplementation((selector: (s: { features: string[] }) => unknown) =>
    selector({ features: ["assistant_segments", "typed_models", "assistant_start_service_tier"] }),
  );
  mocks.useChosenManager.mockReturnValue(null);
  mocks.useModelLists.mockReturnValue(1);
  mocks.assistantLaunchers.mockReturnValue({ info: launchersInfo });
  mocks.modelList.mockImplementation((_mid: string, provider: string) => controlledListOf(provider));
  mocks.usePanelQuotas.mockReturnValue({});
  mocks.assistantSetup.mockReturnValue({ provider: "claude", model: "opus", effort: "high", source: "default" } satisfies AssistantSetup);
  mocks.conversationSwitchState.mockResolvedValue({ enabled: true, epoch: 4, supervision_required: false } satisfies ConversationSwitchState);
  mocks.api.mockResolvedValue({ ok: true });
  mocks.forgetModel.mockResolvedValue("");
  mocks.setServiceTier.mockResolvedValue(true);
  mocks.dropConversationPick.mockResolvedValue(true);
  mocks.refreshAssistant.mockResolvedValue(undefined);
  host = document.createElement("div");
  anchor = document.createElement("button");
  document.body.append(anchor, host);
  root = createRoot(host);
  onClose = vi.fn();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
});

describe("AssistantModelPanel with controlled props", () => {
  it("renders controlled catalog, setup, quotas, and forwards reload, forget, configure, keepPick, serviceTier, and dropPick without touching defaults", async () => {
    const session = makeSession({
      model_pick: { launcher: "relay", model: "sonnet", effort: "low" },
      service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }],
    });
    const chat = makeChat({ session });
    const setup: AssistantSetup = { provider: "relay", model: "sonnet", effort: "low", source: "pick" };
    const onKeepPick = vi.fn().mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    const onDropPick = vi.fn();
    const onSetServiceTier = vi.fn().mockResolvedValue(true);
    const onReload = vi.fn();
    const onForget = vi.fn();
    const onConfigure = vi.fn();
    const onNotice = vi.fn();

    await mount({
      chat,
      launchersInfo,
      getModelList: controlledListOf,
      setup,
      canSwitch: false,
      typed: true,
      startServiceTier: false,
      supervisionRequired: false,
      quotas: sampleQuotas,
      getLatestSession: () => session,
      onKeepPick,
      onDropPick,
      onSetServiceTier,
      onReload,
      onForget,
      onConfigure,
      onNotice,
    });

    const props = mocks.panelProps!;
    expect(props.scope).toBe("assistant");
    expect(props.quotas).toBe(sampleQuotas);
    expect(props.onConfigure).toBe(onConfigure);
    expect(props.providers.find((p) => p.id === "claude")?.typable).toBe(true);
    expect(host.querySelector("[data-pick-strip]")?.getAttribute("data-pick-strip")).toBe("pick");
    expect(host.textContent).toContain("Relay · Sonnet 5.5");

    // PickStrip reset routes through onDropPick
    const backBtn = host.querySelector<HTMLButtonElement>("[data-pick-strip] button")!;
    act(() => backBtn.click());
    expect(onDropPick).toHaveBeenCalledWith(session);

    // Reload and forget route through controlled callbacks
    props.onReload?.("claude");
    expect(onReload).toHaveBeenCalledWith("claude");
    props.onForget?.("claude", "vendor/custom-1");
    expect(onForget).toHaveBeenCalledWith("claude", "vendor/custom-1");

    // Persisted pick success and rejection
    await expect(props.onPick({ provider: "claude", model: "opus", effort: "high" })).resolves.toBe(true);
    expect(onKeepPick).toHaveBeenNthCalledWith(1, session, { launcher: "claude", model: "opus", effort: "high" });
    await expect(props.onPick({ provider: "claude", model: "sonnet", effort: "low" })).resolves.toBe(false);

    // Live speed change on the current setup
    await expect(props.onApplyCurrent?.({ speed: "fast" })).resolves.toBe(true);
    expect(onSetServiceTier).toHaveBeenCalledWith(session, "priority");

    expectNoDefaultSingletons();
    expect(onNotice).not.toHaveBeenCalled();
  });

  it("handles draft picks, start speed pinning, older-hub effort updates, and draft reset via controlled callbacks", async () => {
    const draftChat = makeChat({ session: undefined, kept: true });
    const draftSetup: AssistantSetup = { provider: "codex", model: "gpt-fast", effort: "low", source: "pick" };
    const onPickDraft = vi.fn();
    const onPickServiceTier = vi.fn();
    const onDropPick = vi.fn();

    await mount({
      chat: draftChat,
      launchersInfo,
      getModelList: controlledListOf,
      setup: draftSetup,
      canSwitch: false,
      typed: false,
      startServiceTier: true,
      supervisionRequired: false,
      quotas: {},
      getLatestSession: () => undefined,
      onPickDraft,
      onPickServiceTier,
      onDropPick,
    });

    await expect(mocks.panelProps!.onPick({ provider: "codex", model: "gpt-fast", effort: "high", speed: "fast" })).resolves.toBe(true);
    expect(onPickDraft).toHaveBeenCalledWith({ launcher: "codex", model: "gpt-fast", effort: "high" });
    expect(onPickServiceTier).toHaveBeenCalledWith("priority");

    onPickDraft.mockClear();
    onPickServiceTier.mockClear();
    await expect(mocks.panelProps!.onApplyCurrent?.({ speed: "ultrafast", effort: "high" })).resolves.toBe(true);
    expect(onPickDraft).toHaveBeenCalledWith({ launcher: "codex", model: "gpt-fast", effort: "high" });
    expect(onPickServiceTier).toHaveBeenCalledWith("ultrafast");

    await expect(mocks.panelProps!.onApplyCurrent?.({ speed: "standard" })).resolves.toBe(true);
    expect(onPickServiceTier).toHaveBeenLastCalledWith(undefined);

    const backBtn = host.querySelector<HTMLButtonElement>("[data-pick-strip] button")!;
    act(() => backBtn.click());
    expect(onDropPick).toHaveBeenCalledWith(undefined);

    // Older hub (`kept: false`) writes draft pick + effort via onSetEffort
    const legacySession = makeSession({ effort: "low" });
    const onSetEffort = vi.fn().mockResolvedValue(true);
    await mount({
      chat: makeChat({ session: legacySession, kept: false }),
      launchersInfo,
      getModelList: controlledListOf,
      setup: { provider: "claude", model: "opus", effort: "low", source: "default" },
      canSwitch: false,
      typed: false,
      startServiceTier: false,
      supervisionRequired: false,
      quotas: {},
      getLatestSession: () => legacySession,
      onPickDraft,
      onSetEffort,
    });

    await expect(mocks.panelProps!.onPick({ provider: "relay", model: "sonnet", effort: "high" })).resolves.toBe(true);
    expect(onPickDraft).toHaveBeenLastCalledWith({ launcher: "relay", model: "sonnet", effort: "high" });
    expect(onSetEffort).toHaveBeenCalledWith(legacySession, "high");

    expectNoDefaultSingletons();
  });

  it("handles controlled conversation switching, custom switch-state loading, uncertain retries, and synchronous refresh throws", async () => {
    const session = makeSession();
    const request: ConversationSwitchRequest = {
      operation_id: "op-stable-1",
      expected_epoch: 7,
      source_machine_id: session.machine_id,
      machine_id: session.machine_id,
      launcher: "codex",
      model: "gpt-fast",
      effort: "high",
    };
    const onLoadSwitchState = vi.fn().mockResolvedValue({ enabled: true, epoch: 7, supervision_required: true });
    const onPrepareSwitch = vi.fn().mockResolvedValue(request);
    const onSubmitSwitch = vi.fn()
      .mockRejectedValueOnce(new Error("network drop"))
      .mockRejectedValueOnce(new ApiError(409, "epoch mismatch", { delivered: "none", error: "epoch mismatch" }))
      .mockResolvedValueOnce(undefined);
    const onRefreshAssistant = vi.fn(() => {
      throw new Error("refresh failed synchronously");
    });
    const onNotice = vi.fn();

    await mount({
      chat: makeChat({ session }),
      launchersInfo,
      getModelList: controlledListOf,
      setup: { provider: "claude", model: "opus", effort: "high", source: "default" },
      canSwitch: true,
      typed: false,
      startServiceTier: false,
      onLoadSwitchState,
      quotas: {},
      getLatestSession: () => session,
      onPrepareSwitch,
      onSubmitSwitch,
      onRefreshAssistant,
      onNotice,
    });
    await flush();

    // Custom switch-state loader ran once and marked non-supervision runtime launcher unavailable
    expect(onLoadSwitchState).toHaveBeenCalledTimes(1);
    expect(onLoadSwitchState).toHaveBeenCalledWith({ machine_id: "example-machine", session_id: "example-conv" });
    expect(mocks.panelProps!.providers.find((p) => p.id === "hosted-claude")?.unavailable).toBe(t("chat-modelpanel.supervision_unavailable"));

    // Re-rendering with a fresh inline onLoadSwitchState wrapper does not refetch for the same session
    await mount({
      chat: makeChat({ session }),
      launchersInfo,
      getModelList: controlledListOf,
      setup: { provider: "claude", model: "opus", effort: "high", source: "default" },
      canSwitch: true,
      typed: false,
      startServiceTier: false,
      onLoadSwitchState: (target) => onLoadSwitchState(target),
      quotas: {},
      getLatestSession: () => session,
      onPrepareSwitch,
      onSubmitSwitch,
      onRefreshAssistant,
      onNotice,
    });
    await flush();
    expect(onLoadSwitchState).toHaveBeenCalledTimes(1);

    // Picking a cross-engine launcher opens the confirmation dialog and holds the panel
    let picked = true;
    await act(async () => {
      picked = await mocks.panelProps!.onPick({ provider: "codex", model: "gpt-fast", effort: "high" });
    });
    expect(picked).toBe(false);
    expect(mocks.panelProps!.held).toBe(true);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();

    // First confirm fails uncertainly: shows Close + unconfirmed alert, keeps prepared request
    await act(async () => {
      dialogButton(t("chat-modelpanel.switch_confirm")).click();
    });
    await flush();
    expect(onPrepareSwitch).toHaveBeenCalledTimes(1);
    expect(onSubmitSwitch).toHaveBeenCalledTimes(1);
    expect(onSubmitSwitch).toHaveBeenLastCalledWith(session, request);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("network drop");
    expect(dialogButton(t("common.close"))).toBeDefined();

    // Second confirm reuses exact request without calling onPrepareSwitch again; delivered:"none" restores Cancel
    await act(async () => {
      dialogButton(t("chat-modelpanel.switch_confirm")).click();
    });
    await flush();
    expect(onPrepareSwitch).toHaveBeenCalledTimes(1);
    expect(onSubmitSwitch).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("epoch mismatch");
    expect(dialogButton(t("common.cancel"))).toBeDefined();

    // Third confirm succeeds: notifies, tolerates sync throw in refresh, and closes
    await act(async () => {
      dialogButton(t("chat-modelpanel.switch_confirm")).click();
    });
    await flush();
    expect(onPrepareSwitch).toHaveBeenCalledTimes(1);
    expect(onSubmitSwitch).toHaveBeenCalledTimes(3);
    expect(onNotice).toHaveBeenCalledWith(t("chat-modelpanel.switch_requested"), undefined);
    expect(onRefreshAssistant).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);

    expectNoDefaultSingletons();
  });

  it("bypasses individual default singletons independently for partial controlled props", async () => {
    const session = makeSession();
    const onLoadSwitchState = vi.fn();

    await mount({
      chat: makeChat({ session }),
      launchersInfo,
      getModelList: controlledListOf,
      setup: { provider: "claude", model: "opus", effort: "high", source: "default" },
      canSwitch: true,
      supervisionRequired: true,
      onLoadSwitchState,
      startServiceTier: false,
      quotas: {},
      getLatestSession: () => session,
    });
    await flush();

    // Explicit supervisionRequired bypasses both conversationSwitchState and onLoadSwitchState
    expect(mocks.conversationSwitchState).not.toHaveBeenCalled();
    expect(onLoadSwitchState).not.toHaveBeenCalled();
    expect(mocks.panelProps!.providers.find((p) => p.id === "hosted-claude")?.unavailable).toBe(t("chat-modelpanel.supervision_unavailable"));

    // Explicit startServiceTier bypasses useChosenManager even while typed falls back to useStore
    expect(mocks.useStore).toHaveBeenCalled();
    expect(mocks.useChosenManager).not.toHaveBeenCalled();
    expect(mocks.useModelLists).not.toHaveBeenCalled();
    expect(mocks.usePanelQuotas).not.toHaveBeenCalled();
  });
});

describe("AssistantModelPanel default fallback compatibility", () => {
  it("uses default store, manager, model lists, quotas, API, rollback, and toast paths when controlled props are omitted", async () => {
    const session = makeSession({
      model_pick: { launcher: "relay", model: "sonnet" },
      service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }],
    });
    mocks.getSession.mockReturnValue(session);
    mocks.assistantSetup.mockReturnValue({ provider: "relay", model: "sonnet", effort: "high", source: "pick" });
    // the panel also reads the four dimensions' catalog (an older hub's answer: no catalog); only the pick is refused
    let refused = false;
    mocks.api.mockImplementation(async (path: string) => {
      if (path === "/assistant/conversations/example-conv/model" && !refused) {
        refused = true;
        throw new Error("hub rejected");
      }
      return { ok: true };
    });
    mocks.forgetModel.mockResolvedValueOnce("cannot forget");

    await mount({ chat: makeChat({ session }) });
    await flush();
    expect(mocks.api).toHaveBeenCalledWith("/execution/catalog", { query: { machine: "example-machine" } });

    expect(mocks.useStore).toHaveBeenCalled();
    expect(mocks.useChosenManager).toHaveBeenCalled();
    expect(mocks.useModelLists).toHaveBeenCalled();
    expect(mocks.assistantLaunchers).toHaveBeenCalledWith("example-machine");
    expect(mocks.modelList).toHaveBeenCalled();
    expect(mocks.usePanelQuotas).toHaveBeenCalled();
    expect(mocks.assistantSetup).toHaveBeenCalled();
    expect(mocks.conversationSwitchState).toHaveBeenCalledWith({ machine_id: "example-machine", session_id: "example-conv" });

    // Default keepPick error rolls back session and toasts
    await expect(mocks.panelProps!.onPick({ provider: "claude", model: "opus", effort: "high" })).resolves.toBe(false);
    expect(mocks.api).toHaveBeenCalledWith("/assistant/conversations/example-conv/model", {
      method: "PUT",
      json: { launcher: "claude", model: "opus", effort: "high", machine_id: "example-machine" },
    });
    expect(mocks.upsertSession).toHaveBeenCalledTimes(2);
    expect(mocks.toast).toHaveBeenCalledWith(t("chat-sessiondialogs.change_failed", { error: "hub rejected" }), "error");

    // Default PickStrip back calls dropConversationPick
    act(() => host.querySelector<HTMLButtonElement>("[data-pick-strip] button")!.click());
    expect(mocks.dropConversationPick).toHaveBeenCalledWith(session);

    // Default reload, forget error toast, and setServiceTier
    mocks.panelProps!.onReload?.("claude");
    expect(mocks.readModels).toHaveBeenCalledWith("example-machine", "claude", { purpose: "assistant", refresh: true, first: false });
    mocks.panelProps!.onForget?.("claude", "vendor/x");
    await flush();
    expect(mocks.forgetModel).toHaveBeenCalledWith("example-machine", "claude", "vendor/x", "assistant");
    expect(mocks.toast).toHaveBeenCalledWith("cannot forget", "error");

    await expect(mocks.panelProps!.onApplyCurrent?.({ speed: "fast" })).resolves.toBe(true);
    expect(mocks.setServiceTier).toHaveBeenCalledWith(session, "priority");
  });

  it("uses default draft pick and explicit switch fallbacks when controlled props are omitted", async () => {
    const draftChat = makeChat({ session: undefined, kept: true });
    mocks.assistantSetup.mockReturnValue({ provider: "codex", model: "gpt-fast", effort: "low", source: "default" });
    await mount({ chat: draftChat });

    await expect(mocks.panelProps!.onPick({ provider: "codex", model: "gpt-fast", effort: "high", speed: "fast" })).resolves.toBe(true);
    expect(mocks.pickLauncher).toHaveBeenCalledWith("new", "codex");
    expect(mocks.pickModel).toHaveBeenCalledWith("new", "gpt-fast");
    expect(mocks.pickEffort).toHaveBeenCalledWith("new", "high");
    expect(mocks.pickServiceTier).toHaveBeenCalledWith("priority");

    // Default explicit switch fallback
    const session = makeSession();
    const request: ConversationSwitchRequest = {
      operation_id: "default-op-1",
      expected_epoch: 4,
      source_machine_id: session.machine_id,
      machine_id: session.machine_id,
      launcher: "codex",
      model: "gpt-fast",
      effort: "high",
    };
    mocks.getSession.mockReturnValue(session);
    mocks.assistantSetup.mockReturnValue({ provider: "claude", model: "opus", effort: "high", source: "default" });
    mocks.prepareConversationSwitch.mockResolvedValueOnce(request);
    mocks.submitConversationSwitch.mockResolvedValueOnce(undefined);

    await mount({ chat: makeChat({ session }) });
    await flush();

    await act(async () => {
      await mocks.panelProps!.onPick({ provider: "codex", model: "gpt-fast", effort: "high" });
    });
    await act(async () => {
      dialogButton(t("chat-modelpanel.switch_confirm")).click();
    });
    await flush();

    expect(mocks.prepareConversationSwitch).toHaveBeenCalledWith(session, { launcher: "codex", model: "gpt-fast", effort: "high" });
    expect(mocks.submitConversationSwitch).toHaveBeenCalledWith(session, request);
    expect(mocks.toast).toHaveBeenCalledWith(t("chat-modelpanel.switch_requested"), undefined);
    expect(mocks.refreshAssistant).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
