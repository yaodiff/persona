// The model panel of a Persona chat (#5033, docs/web/web.md "Model pill and model panel"), loaded on its pill's first tap
// (ModelPill.tsx AssistantModelPill): the shared panel (ui/model-panel, scope "assistant") fed with the assistant
// computer's providers (lib/assistant-panel.ts), a strip on top saying whether the chat follows the default or has its
// own pick, with the way back. On a hub that keeps picks a pick is PUT for the conversation (it holds from the next
// message, on every device), drawn at once and put back with the hub's words if it says no; following the default again
// is a DELETE. An older hub: the pick lasts for this page and goes with each message, as before. The new-conversation
// page: the pick goes with the conversation's start. On a hub that declares the four dimensions (GET /execution/catalog
// with recommended combinations for the computer) the panel opens on the model and the effort only
// (views/execution/SimpleModelSheet.tsx); "All models and providers" opens the launcher panel below.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, errorText, sessionPath } from "../../lib/api";
import { assistantModelHost, assistantPickKey, assistantSetup, ASSISTANT_START_SERVICE_TIER_FEATURE, canonicalLauncher, findAssistantLauncher, type AssistantChat, type AssistantSetup } from "../../lib/assistant-models";
import { assistantPanel, assistantPickOf } from "../../lib/assistant-panel";
import { pickEffort, pickLauncher, pickModel, pickServiceTier } from "../../lib/assistant-chat";
import { conversationModelPath, dropConversationPick } from "../../lib/assistant-pick";
import { useChosenManager } from "../../lib/manager-choice";
import type { PanelConfig, PanelOverride } from "../../lib/model-panel";
import { usePanelQuotas } from "../../lib/panel-quotas";
import { TYPED_MODELS_FEATURE } from "../../lib/model-id";
import { providerTitle } from "../../lib/provider-names";
import type { LauncherQuotaAnswer } from "../../lib/quota";
import { speedOptions, startSpeedTier } from "../../lib/session-speed";
import { speedStep } from "../../lib/session-panel";
import { assistantLaunchers, forgetModel, modelList, readModels, useModelLists, type AssistantLaunchers, type ModelList } from "../../lib/session-models";
import { getSession, hubHas, refreshAssistant, upsertSession, useStore } from "../../lib/store";
import type { ModelPick, Session } from "../../lib/types";
import { Button, Dialog, toast } from "../../ui/atoms";
import { ModelPanel, type ModelPanelProps } from "../../ui/model-panel/ModelPanel";
import { ASSISTANT_SEGMENTS_FEATURE, assistantSwitchNeeded, conversationSwitchState, prepareConversationSwitch, submitConversationSwitch, type ConversationSwitchRequest, type ConversationSwitchState } from "../../lib/conversation-switch";
import { setServiceTier } from "./SessionDialogs";
import { recommendedPicks, type ExecutionCatalog, type SpecPick } from "../../lib/execution";
import { useExecutionCatalog } from "../execution/hooks";
import { SimpleModelSheet } from "../execution/SimpleModelSheet";

export type AssistantModelNoticeTone = "info" | "ok" | "error";
export type AssistantModelNotice = (text: string, tone?: AssistantModelNoticeTone) => void;

/** PUT …/model {launcher, model?, effort?, machine_id}: kept for the conversation. Drawn at once; put back, with the hub's
 *  words (out of the computer's scope, gone, another engine than the chat runs, can't run assistant chats…), if it says
 *  no. */
async function keepPick(s: Session, pick: ModelPick, notify: AssistantModelNotice): Promise<boolean> {
  const before = s.model_pick;
  upsertSession({ ...s, model_pick: pick });
  try {
    await api(conversationModelPath(s), { method: "PUT", json: { ...pick, machine_id: s.machine_id } });
    return true;
  } catch (e) {
    upsertSession({ ...(getSession(s.machine_id, s.session_id) ?? s), model_pick: before });
    notify(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}

/** POST …/modes {effort} on an older hub (what the effort chip did): drawn at once, put back if it fails. */
async function setEffort(s: Session, effort: string, notify: AssistantModelNotice): Promise<boolean> {
  const before = s.effort;
  upsertSession({ ...s, effort });
  try {
    await api(`${sessionPath(s.machine_id, s.session_id)}/modes`, { method: "POST", json: { effort } });
    return true;
  } catch (e) {
    upsertSession({ ...(getSession(s.machine_id, s.session_id) ?? s), effort: before });
    notify(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}

export interface PickStripProps {
  chat: AssistantChat;
  setup: AssistantSetup;
  named: string;
  onBack?: () => void;
}

/** The strip on top: where the setup comes from and, for a pick, the way back. */
export function PickStrip({ chat, setup, named, onBack }: PickStripProps) {
  const text = !chat.session
    ? t("chat-modelpill.header_new", { setup: named })
    : setup.source === "pick"
      ? t(chat.kept ? "chat-modelpill.header_picked" : "chat-modelpill.header_picked_page", { setup: named })
      : setup.source === "default"
        ? t("chat-modelpill.header_default", { setup: named })
        : t("chat-modelpill.header_own", { setup: named });
  // the main chat and the new one go back to the default; another chat's pick is undone (it then keeps what it runs)
  const back = chat.session && !(chat.followsDefault ?? chat.isMain) ? t("chat-modelpill.undo_pick") : t("chat-modelpill.follow_default");
  return (
    <div data-pick-strip={setup.source} className="flex min-w-0 items-baseline gap-2 border-b border-line px-3.5 py-2 text-[12px] leading-[1.45] text-ink-2">
      <span className="min-w-0 flex-1 break-words">{text}</span>
      {setup.source === "pick" && onBack && (
        <button type="button" onClick={onBack} className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline">
          {back}
        </button>
      )}
    </div>
  );
}

export interface AssistantModelPanelProps {
  chat: AssistantChat;
  mid: string;
  anchor: HTMLElement;
  onClose: () => void;
  /** Controlled assistant launcher catalog (`null` when unloaded) skips `assistantLaunchers` lookup. */
  launchersInfo?: AssistantLaunchers | null;
  /** Controlled provider model-list reader skips `modelList` cache reads. */
  getModelList?: (provider: string) => ModelList;
  /** Controlled current setup skips `assistantSetup` (and its page-local pick reads). */
  setup?: AssistantSetup;
  /** Controlled hub capabilities skip `useStore` and `useChosenManager`. */
  canSwitch?: boolean;
  typed?: boolean;
  startServiceTier?: boolean;
  /** Controlled supervision requirement skips `conversationSwitchState` fetch. */
  supervisionRequired?: boolean;
  /** Custom switch-state loader replacing `conversationSwitchState` when `supervisionRequired` is omitted. */
  onLoadSwitchState?: (session: Pick<Session, "machine_id" | "session_id">) => Promise<ConversationSwitchState>;
  /** Controlled provider quotas skip `usePanelQuotas`. */
  quotas?: Readonly<Record<string, LauncherQuotaAnswer>>;
  /** Controlled latest-session reader skips `getSession` store lookup. */
  getLatestSession?: () => Session | undefined;
  /** Controlled persisted-pick writer replacing `PUT .../model` + `upsertSession`. */
  onKeepPick?: (session: Session, pick: ModelPick) => boolean | Promise<boolean>;
  /** Controlled pick reset replacing `dropConversationPick` and page-local pick reset. */
  onDropPick?: (session?: Session) => boolean | void | Promise<boolean | void>;
  /** Controlled page-local/draft pick writer replacing `pickLauncher` / `pickModel` / `pickEffort`. */
  onPickDraft?: (pick: ModelPick) => void;
  /** Controlled new-conversation service-tier writer replacing `pickServiceTier`. */
  onPickServiceTier?: (tier: string | undefined) => void;
  /** Controlled older-hub effort writer replacing `POST .../modes { effort }` + `upsertSession`. */
  onSetEffort?: (session: Session, effort: string) => boolean | Promise<boolean>;
  /** Controlled service-tier writer replacing `setServiceTier`. */
  onSetServiceTier?: (session: Session, tier: string) => boolean | Promise<boolean>;
  /** Controlled provider reload handler replacing `readModels`. */
  onReload?: (provider: string) => void;
  /** Controlled typed-model forget handler replacing `forgetModel`. */
  onForget?: (provider: string, model: string) => void;
  /** Controlled explicit conversation switch hooks replacing `prepareConversationSwitch`, `submitConversationSwitch`, and `refreshAssistant`. */
  onPrepareSwitch?: (session: Session, pick: ModelPick) => Promise<ConversationSwitchRequest>;
  onSubmitSwitch?: (session: Session, request: ConversationSwitchRequest) => Promise<void>;
  onRefreshAssistant?: () => void | Promise<unknown>;
  /** Controlled notification handler replacing global `toast`. */
  onNotice?: AssistantModelNotice;
  /** Controlled "Configure models" handler forwarded to `ModelPanel`. */
  onConfigure?: () => void;
  /** Controlled execution catalog (`null`: none, the launcher panel only) skips GET /execution/catalog. */
  execution?: ExecutionCatalog | null;
}

interface ResolvedCapabilitiesProps extends Omit<AssistantModelPanelProps, "canSwitch" | "typed" | "startServiceTier"> {
  canSwitch: boolean;
  typed: boolean;
  startServiceTier: boolean;
}

interface ResolvedCatalogProps extends ResolvedCapabilitiesProps {
  info: AssistantLaunchers | undefined;
  listOf: (provider: string) => ModelList;
  catalogVersion: number;
}

export function AssistantModelPanel(props: AssistantModelPanelProps) {
  if (props.canSwitch !== undefined && props.typed !== undefined && props.startServiceTier !== undefined) {
    return (
      <AssistantModelCatalog
        {...props}
        canSwitch={props.canSwitch}
        typed={props.typed}
        startServiceTier={props.startServiceTier}
      />
    );
  }
  return <DefaultCapabilitiesPanel {...props} />;
}

function DefaultCapabilitiesPanel(props: AssistantModelPanelProps) {
  const storeCanSwitch = useStore((s) => hubHas(s.features, ASSISTANT_SEGMENTS_FEATURE));
  const storeTyped = useStore((s) => hubHas(s.features, TYPED_MODELS_FEATURE));
  const storeStartServiceTier = useStore((s) => hubHas(s.features, ASSISTANT_START_SERVICE_TIER_FEATURE));
  const canSwitch = props.canSwitch ?? storeCanSwitch;
  const typed = props.typed ?? storeTyped;
  if (props.startServiceTier !== undefined) {
    return <AssistantModelCatalog {...props} canSwitch={canSwitch} typed={typed} startServiceTier={props.startServiceTier} />;
  }
  return <DefaultStartServiceTierPanel {...props} canSwitch={canSwitch} typed={typed} hubStartServiceTier={storeStartServiceTier} />;
}

function DefaultStartServiceTierPanel({
  hubStartServiceTier,
  ...props
}: Omit<ResolvedCapabilitiesProps, "startServiceTier"> & { hubStartServiceTier: boolean }) {
  const chosen = useChosenManager();
  return <AssistantModelCatalog {...props} startServiceTier={hubStartServiceTier && !chosen} />;
}

function AssistantModelCatalog(props: ResolvedCapabilitiesProps) {
  if (props.launchersInfo !== undefined && props.getModelList !== undefined) {
    return <AssistantModelPanelView {...props} info={props.launchersInfo ?? undefined} listOf={props.getModelList} catalogVersion={0} />;
  }
  return <DefaultModelCatalogPanel {...props} />;
}

function DefaultModelCatalogPanel(props: ResolvedCapabilitiesProps) {
  const catalogVersion = useModelLists();
  const session = props.chat.session;
  const defaultListOf = useCallback((name: string) => {
    const host = assistantModelHost(session, name);
    return host ? modelList(props.mid, name, "assistant", host) : modelList(props.mid, name, "assistant");
  }, [props.mid, session]);
  const info = props.launchersInfo !== undefined ? (props.launchersInfo ?? undefined) : assistantLaunchers(props.mid).info;
  const listOf = props.getModelList ?? defaultListOf;
  return <AssistantModelPanelView {...props} info={info} listOf={listOf} catalogVersion={catalogVersion} />;
}

function DefaultQuotasModelPanel({
  mid,
  launchers,
  ...panelProps
}: ModelPanelProps & { mid: string; launchers?: AssistantLaunchers["launchers"] }) {
  const quotas = usePanelQuotas(mid, panelProps.providers, launchers);
  return <ModelPanel {...panelProps} quotas={quotas} />;
}

function AssistantModelPanelView({
  chat,
  mid,
  anchor,
  onClose,
  info,
  listOf,
  catalogVersion,
  setup: controlledSetup,
  canSwitch,
  typed,
  startServiceTier,
  supervisionRequired: controlledSupervision,
  onLoadSwitchState,
  quotas: controlledQuotas,
  getLatestSession,
  onKeepPick,
  onDropPick,
  onPickDraft,
  onPickServiceTier,
  onSetEffort,
  onSetServiceTier,
  onReload: controlledReload,
  onForget: controlledForget,
  onPrepareSwitch,
  onSubmitSwitch,
  onRefreshAssistant,
  onNotice,
  onConfigure,
  execution,
  launchersInfo,
}: ResolvedCatalogProps) {
  // the four dimensions' catalog: read with the panel unless the caller controls the launcher catalog (or gives one)
  const exec = useExecutionCatalog(mid, launchersInfo === undefined, execution);
  const [full, setFull] = useState(false);
  const [switchPick, setSwitchPick] = useState<ModelPick | null>(null);
  const [switchError, setSwitchError] = useState("");
  const [switching, setSwitching] = useState(false);
  const [switchUncertain, setSwitchUncertain] = useState(false);
  const switchFlight = useRef(false);
  const switchRequest = useRef<ConversationSwitchRequest | null>(null);
  const setup = controlledSetup ?? assistantSetup(chat, info);
  const engine = chat.session?.agent || "claude";
  const [loadedSupervision, setLoadedSupervision] = useState(false);
  const roleMachine = chat.session?.machine_id;
  const roleSession = chat.session?.session_id;
  const loadSwitchStateRef = useRef(onLoadSwitchState);
  loadSwitchStateRef.current = onLoadSwitchState;
  useEffect(() => {
    if (controlledSupervision !== undefined) return;
    if (!canSwitch || !roleMachine || !roleSession) {
      setLoadedSupervision(false);
      return;
    }
    let current = true;
    setLoadedSupervision(false);
    void (loadSwitchStateRef.current ?? conversationSwitchState)({ machine_id: roleMachine, session_id: roleSession }).then((snapshot) => {
      if (current) setLoadedSupervision(Boolean(snapshot.supervision_required));
    }).catch(() => {});
    return () => { current = false; };
  }, [controlledSupervision, canSwitch, roleMachine, roleSession]);
  const supervisionRequired = controlledSupervision ?? loadedSupervision;
  // a chat that runs keeps its engine; one whose agent isn't known is refused nothing on a guess, as on the hub
  const running = Boolean(chat.session?.agent);
  const tiers = chat.session?.service_tiers;
  const switchingMode = Boolean(canSwitch && chat.kept && chat.session);
  const startingMode = !chat.session && startServiceTier;
  const { providers, state } = useMemo(
    () => assistantPanel(setup, info, listOf, { typed, engine, running, switching: switchingMode, supervision: supervisionRequired, starting: startingMode, speeds: speedOptions({ service_tiers: tiers }) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `catalogVersion` stands for what was read from the default cache
    [setup.provider, setup.model, setup.effort, setup.speed, info, listOf, typed, engine, running, tiers, switchingMode, startingMode, supervisionRequired, catalogVersion],
  );
  const latest = () => (getLatestSession ? getLatestSession() : chat.session ? (getSession(chat.session.machine_id, chat.session.session_id) ?? chat.session) : undefined);
  const key = assistantPickKey(chat);
  const notify: AssistantModelNotice = (text, tone) => {
    if (onNotice) onNotice(text, tone);
    else toast(text, tone);
  };

  // the page's own pick (an older hub, the new-conversation page): launcher, then its model, then the effort
  const keepHere = (pick: ModelPick) => {
    if (onPickDraft) {
      onPickDraft(pick);
      return;
    }
    pickLauncher(key, pick.launcher);
    pickModel(key, pick.model);
    if (!chat.session) pickEffort(key, pick.effort);
  };
  const setDraftServiceTier = onPickServiceTier ?? pickServiceTier;
  const apply = async (pick: ModelPick): Promise<boolean> => {
    const s = latest();
    const provider = providers.find((p) => p.id === pick.launcher);
    const target = findAssistantLauncher(info, pick.launcher);
    if (s && chat.kept && canSwitch && provider && target && assistantSwitchNeeded(s, target, provider.engine)) {
      setSwitchPick(pick);
      switchRequest.current = null;
      setSwitchError("");
      setSwitchUncertain(false);
      return false; // Confirmation, not a model pick, owns the explicit operation.
    }
    if (s && chat.kept) return onKeepPick ? Boolean(await onKeepPick(s, pick)) : keepPick(s, pick, notify);
    keepHere(pick);
    // an older hub keeps the effort on the session (what the effort chip did)
    if (s && pick.effort && pick.effort !== s.effort) return onSetEffort ? Boolean(await onSetEffort(s, pick.effort)) : setEffort(s, pick.effort, notify);
    return true;
  };
  const onPick = async (config: PanelConfig) => {
    const list = listOf(config.provider).list;
    // On a new draft, state.current is the displayed setup rather than a saved pick: picking it still pins every option.
    const pick = assistantPickOf(config, list, chat.session ? state.current : null);
    if (pick) {
      if (!(await apply(pick))) return false;
    } else if (!chat.session || config.provider !== state.current?.provider || config.model !== state.current.model) {
      return false;
    }
    if (!chat.session) {
      const row = list?.models.find((m) => m.id === config.model);
      setDraftServiceTier(startServiceTier ? startSpeedTier(row, config.speed) : undefined);
    }
    return true;
  };
  const onApplyCurrent = async (patch: PanelOverride) => {
    const s = latest();
    const now = state.current;
    if (!now || (patch.effort === undefined && patch.speed === undefined)) return false;
    let effortApplied = false;
    if (patch.speed !== undefined) {
      if (!s) {
        const list = listOf(now.provider).list;
        const row = list?.models.find((m) => m.id === now.model);
        const tier = startServiceTier ? startSpeedTier(row, patch.speed) : undefined;
        if (patch.speed !== "standard" && !tier) return false;
        if (tier) {
          // Pin every displayed start option: a cleared main must not revive its carried launcher, model or effort.
          const pick = assistantPickOf({ ...now, effort: patch.effort ?? now.effort }, list, null);
          if (!pick) return false;
          keepHere(pick);
          effortApplied = patch.effort !== undefined;
        }
        setDraftServiceTier(tier);
      } else {
        const speed = speedStep(s, patch.speed, true);
        if (!(speed && "service_tier" in speed)) return false;
        const ok = onSetServiceTier ? Boolean(await onSetServiceTier(s, speed.service_tier)) : await setServiceTier(s, speed.service_tier);
        if (!ok) return false;
      }
    }
    if (patch.effort === undefined || effortApplied) return true;
    const pick = assistantPickOf({ ...now, effort: patch.effort }, listOf(now.provider).list, null);
    return pick ? apply(pick) : false;
  };
  const back = () => {
    const s = latest();
    if (onDropPick) {
      try {
        void Promise.resolve(onDropPick(s)).catch(() => {});
      } catch {
        // Controlled callback owns its own failure handling.
      }
      return;
    }
    if (s && chat.kept) void dropConversationPick(s);
    else {
      pickLauncher(key, undefined);
      pickEffort(key, undefined);
      setDraftServiceTier(undefined);
    }
  };
  const onReload = controlledReload ?? ((provider: string) => {
    const host = assistantModelHost(chat.session, provider);
    void readModels(mid, provider, {
      purpose: "assistant",
      refresh: true,
      first: canonicalLauncher(provider) === canonicalLauncher(setup.provider),
      ...(host ? { host } : {}),
    });
  });
  const onForget = controlledForget ?? ((provider: string, model: string) => {
    const host = assistantModelHost(chat.session, provider);
    void (host ? forgetModel(mid, provider, model, "assistant", host) : forgetModel(mid, provider, model, "assistant"))
      .then((error) => error && notify(error, "error"));
  });
  const launcher = findAssistantLauncher(info, setup.provider);
  const row = providers.find((p) => p.id === (launcher?.name ?? setup.provider))?.models?.find((m) => m.id === state.current?.model);
  const named = [launcher ? providerTitle(launcher) : canonicalLauncher(setup.provider), row?.name].filter(Boolean).join(" · ");

  const confirmSwitch = async () => {
    const session = latest();
    if (!session || !switchPick || switchFlight.current) return;
    switchFlight.current = true;
    setSwitching(true);
    setSwitchError("");
    let submitted = false;
    try {
      switchRequest.current ??= await (onPrepareSwitch ?? prepareConversationSwitch)(session, switchPick);
      submitted = true;
      await (onSubmitSwitch ?? submitConversationSwitch)(session, switchRequest.current);
      setSwitchPick(null);
      switchRequest.current = null;
      notify(t("chat-modelpanel.switch_requested"));
      try {
        void Promise.resolve((onRefreshAssistant ?? refreshAssistant)()).catch(() => {});
      } catch {
        // Switch already succeeded; refresh failure does not reopen confirmation.
      }
      onClose();
    } catch (error) {
      const uncertain = submitted && !(error instanceof ApiError && error.body.delivered === "none");
      setSwitchUncertain(uncertain);
      setSwitchError(uncertain ? t("chat-modelpanel.switch_unconfirmed", { error: errorText(error) }) : errorText(error));
    }
    finally { switchFlight.current = false; setSwitching(false); }
  };
  const cancelSwitch = () => { if (!switchFlight.current) { setSwitchPick(null); switchRequest.current = null; } };

  const panelProps: ModelPanelProps = {
    anchor,
    held: Boolean(switchPick),
    onClose,
    providers,
    state,
    scope: "assistant",
    onPick,
    onApplyCurrent,
    onReload,
    onForget,
    onConfigure,
    header: <PickStrip chat={chat} setup={setup} named={named} onBack={back} />,
  };

  const picks = full ? [] : recommendedPicks(exec.catalog);
  const simple = exec.catalog && picks.length > 0 && !switchPick ? exec.catalog : null;
  const restored = (pick: SpecPick) => {
    const s = latest();
    if (s) upsertSession({ ...s, model_pick: pick });
  };

  return ( <>
    {simple ? (
      <SimpleModelSheet
        catalog={simple}
        picks={picks}
        machine={mid}
        current={{ launcher: launcher?.name ?? setup.provider, model: state.current?.model ?? setup.model, effort: state.current?.effort ?? setup.effort }}
        kept={chat.kept && chat.session ? chat.session : undefined}
        header={<PickStrip chat={chat} setup={setup} named={named} onBack={() => { back(); onClose(); }} />}
        onApply={apply}
        onRestored={restored}
        onNotice={notify}
        onMore={() => setFull(true)}
        onClose={onClose}
      />
    ) : controlledQuotas !== undefined ? (
      <ModelPanel {...panelProps} quotas={controlledQuotas} />
    ) : (
      <DefaultQuotasModelPanel {...panelProps} mid={mid} launchers={info?.launchers} />
    )}
    <Dialog open={Boolean(switchPick)} title={t("chat-modelpanel.switch_title")} onClose={cancelSwitch}
      footer={<><Button disabled={switching} onClick={cancelSwitch}>{t(switchUncertain ? "common.close" : "common.cancel")}</Button><Button disabled={switching} busy={switching} onClick={() => void confirmSwitch()}>{t("chat-modelpanel.switch_confirm")}</Button></>}>
      <p>{t("chat-modelpanel.switch_explanation", { name: providers.find((p) => p.id === switchPick?.launcher)?.name ?? switchPick?.launcher ?? "" })}</p>
      {switchError && <p role="alert" className="text-red">{switchError}</p>}
    </Dialog>
  </> );
}
