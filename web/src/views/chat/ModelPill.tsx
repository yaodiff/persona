// The composer's model pill (#5032, after Cindy's model selector, which the user asked this part of the chat to match;
// docs/web/web.md "Model pill and model panel", docs/design/DESIGN_SYSTEM.md 3.1): at the right of the composer's bottom
// row, before voice / Stop / Send, `[engine][model][effort][speed][⌄]`. It opens the unified model panel above
// it (ui/model-panel/ModelPanel.tsx), and so does `/model` Enter in the composer. The composer card is a container: under
// 600 px the pill takes the width its words need (the name at most 108 px, a bracketed qualifier left out but for a
// context size, `pillShortName`) and gives way
// only when the row is short. Under 520 px its text and marks are a step smaller. Under 420 px the compact pill keeps the
// engine, speed mark and chevron. Explicit Agent launcher labels stay visible (#10248). The Agent's mark comes first
// and only once (#5497).
// An agent that can switch neither its model nor its effort gets the same
// words as a label, except Codex: its panel explains why. A Persona chat (and the new-chat page) has `AssistantModelPill`
// (#5033): the same pill over what the assistant's computer can run Persona chats on, its panel saying whether the chat
// follows the default.
import { memo, useCallback, useEffect, useRef, useState, type ReactNode, type Ref } from "react";
import { t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import { onModelPicker, requestModelPicker, sessionLauncher } from "../../lib/model-switch";
import { effortVariantBase, withoutEffortQualifier } from "../../lib/launcher-models";
import { effortTitle } from "../../lib/session-actions";
import { dropPick, launcherOffered, pickServiceTier, usePickedEffort, usePickedLauncher, usePickedModel, usePickedServiceTier } from "../../lib/assistant-chat";
import { isBackgroundSupervisor } from "../../lib/background-supervisor";
import { launcherTitle } from "../../lib/provider-names";
import { ASSISTANT_MODEL_PICK_FEATURE, ASSISTANT_START_SERVICE_TIER_FEATURE, assistantModelHost, assistantPickKey, assistantPillView, assistantSetup, canonicalLauncher, findAssistantLauncher, type AssistantChat } from "../../lib/assistant-models";
import {
  agentCatalogScope, assistantCatalogScope, assistantLaunchers, machineLauncherList, modelList, pendingView, pillView, readAgentLauncherNames, readAssistantLaunchers, readForPanel, readModels, readMainDefault, mainDefault, readSuperviseDefault, sessionModelHost, useRevisionReread, sessionSwitches, superviseDefault, useModelLists, currentModelRow, type PillSetup,
} from "../../lib/session-models";
import { turnErrorFix, type TurnFix } from "../../lib/readiness";
import { launchModelForSession } from "../../lib/new-agent";
import { speedId, startSpeedTier } from "../../lib/session-speed";
import { getSession, hubHas, useStore } from "../../lib/store";
import { CODEX_ULTRA, ultracodeOn } from "../../lib/ultracode";
import type { AssistantInfo, Session } from "../../lib/types";
import { cx, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ProviderMark } from "../../ui/model-panel/ProviderMark";
import { SpeedMark } from "../../ui/model-panel/SpeedMark";
import { lazyOverlay } from "../LazyOverlay";
import { UltraMark } from "./Ultracode";

// the panel (and what it is built from) loads on the pill's first tap, not with every chat's composer
const SessionModelPanel = lazyOverlay(() => import("./SessionModelPanel").then((m) => m.SessionModelPanel));

function pillDisplayName({ model, effort, engine }: PillSetup): string | undefined {
  if (!model || !effort) return model;
  // Before the catalog arrives, Antigravity can report only the variant ID and its effort.
  return (engine === "agy" && effortVariantBase(model, effort)) || withoutEffortQualifier(model);
}

// the composer card's widths (Composer.tsx `@container/composer`): narrow < 600, dense < 520, ultra < 420
const ULTRA_HIDDEN = "@max-[420px]/composer:hidden";

function pillSpeedLabel(speed?: string): string {
  if (speed === "fast") return t("chat-sessiondialogs.speed_fast");
  if (speed === "ultrafast") return t("ui-modelpanel.speed_ultrafast");
  return "";
}

/** The name where the pill is narrow: a trailing qualifier in brackets left out ("Default (recommended)" → "Default",
 *  "GPT-5 (beta)" → "GPT-5"), except a context size, which names another model and stays as a short mark, as the chat's
 *  model names have it ("Opus 5.5 (1M context)" and "Opus 5.5 [1m]" → "Opus 5.5 1M"). The tooltip and the accessible
 *  name keep the whole name. */
export function pillShortName(name: string): string {
  const m = /^(.*?)\s*[(（[]([^()（）[\]]*)[)）\]]\s*$/u.exec(name);
  const base = m?.[1].trim();
  if (!m || !base) return name;
  const size = /^(\d+(?:\.\d+)?)\s*([km])(?:\s*(?:context|ctx|tokens?|上下文|コンテキスト))?$/iu.exec(m[2].trim());
  return size ? `${base} ${size[1]}${size[2].toUpperCase()}` : base;
}

export function ModelPill({
  current,
  pending,
  loading,
  failed,
  disabled,
  open,
  onOpen,
  workflow,
  buttonRef,
}: {
  current: PillSetup;
  /** A switch accepted for the next message (#5035): a clock first, the launcher it moves to named (the engine, when
   *  that changes, #5054: Cindy's "clock · agent · model") and drawn, the tooltip both setups. */
  pending?: PillSetup | null;
  /** The model list is read for the first time. */
  loading?: boolean;
  /** Reading the model list failed. */
  failed?: boolean;
  /** A switch is on its way: dimmed, takes no clicks. */
  disabled?: boolean;
  open?: boolean;
  /** Opens (or closes) the panel at the pill; absent: the pill is a label. */
  onOpen?: (anchor: HTMLElement) => void;
  /** The workflow mark after the effort (ultracode on, a Codex thread on its "ultra" effort; #3428). */
  workflow?: { label?: string; title?: string };
  buttonRef?: Ref<HTMLButtonElement>;
}) {
  const shown = pending ?? current;
  const launcher = shown.launcherName;
  const speedLabel = pillSpeedLabel(shown.speed);
  const unknown = !shown.model;
  const name = pillDisplayName(shown) ?? (loading ? t("chat-modelpill.loading") : failed ? t("chat-modelpill.failed") : t("chat-modelpill.default_model"));
  const describe = (x: PillSetup) => {
    const engine = agentName(x.engine, x.engineLabel);
    const provider = x.launcherName || x.provider;
    return [engine, pillDisplayName(x) ?? name, provider && provider !== engine ? provider : "", x.effort && effortTitle(x.effort), pillSpeedLabel(x.speed)].filter(Boolean).join(" · ");
  };
  const title = pending
    ? t("chat-modelpill.current_and_next", { current: describe(current), next: describe(pending) })
    : [describe(current), failed && !unknown ? t("chat-modelpill.failed") : ""].filter(Boolean).join(" · ");
  const short = pillShortName(name);
  const effort = effortTitle(shown.effort);
  // An explicit Agent launcher matches the panel even across engines; other pills keep the existing pending label.
  const target = launcher || (pending ? (pending.engine !== current.engine ? agentName(pending.engine, pending.engineLabel) : pending.provider) : "");
  const label = t(
    pending
      ? (effort ? "chat-modelpill.aria_pending_effort" : "chat-modelpill.aria_pending")
      : (effort ? "chat-modelpill.aria_effort" : "chat-modelpill.aria"),
    { agent: target, model: !pending && launcher ? `${launcher} · ${name}` : name, effort },
  );
  const accessibleLabel = speedLabel ? `${label} · ${speedLabel}` : label;

  const body: ReactNode = (
    <>
      {pending && <Icon name="clock" stroke={2} className="size-[12px] shrink-0 text-ink-3 @max-[520px]/composer:size-[11px]" />}
      {unknown && loading && <Spinner size={13} />}
      {unknown && failed && !loading && <Icon name="warning" stroke={2} className="size-[13px] shrink-0 text-red @max-[520px]/composer:size-[12px]" />}
      <span aria-hidden data-pill-engine={shown.engine} className="mr-1 flex shrink-0 items-center @max-[520px]/composer:scale-[0.933]">
        <ProviderMark engine={shown.engine} label={shown.engineLabel} className={failed && !unknown ? "text-red" : "text-ink-2"} />
      </span>
      {(launcher || pending) && (
        <>
          <span data-pill-target title={target} className={cx("max-w-[96px] truncate text-ink-3", launcher ? "min-w-[3ch] shrink" : "shrink-0 @max-[600px]/composer:hidden")}>{target}</span>
          <span aria-hidden className={cx("shrink-0 text-ink-3", !launcher && "@max-[600px]/composer:hidden")}>·</span>
        </>
      )}
      {/* the name gives way first (a far larger shrink than the effort's): narrow, the effort truncates only once the
          name has no room left */}
      <span title={name} className={cx("shrink-[1000] truncate text-ink @max-[600px]/composer:max-w-[108px]", launcher ? "min-w-[3ch]" : "min-w-0", !launcher && ULTRA_HIDDEN)}>
        {short === name ? name : (
          <>
            <span className="@max-[600px]/composer:hidden">{name}</span>
            <span data-pill-short className="hidden @max-[600px]/composer:inline">{short}</span>
          </>
        )}
      </span>
      {shown.effort && (
        <span title={effort} data-pill-effort className={cx("ml-1 shrink-0 whitespace-nowrap text-ink @max-[600px]/composer:min-w-0 @max-[600px]/composer:shrink @max-[600px]/composer:truncate", ULTRA_HIDDEN)}>
          {effort}
        </span>
      )}
      {speedLabel && (
        <span data-pill-speed={shown.speed} aria-hidden className="ml-0.5 flex shrink-0 text-ink-2">
          <SpeedMark speed={shown.speed} className="size-[13px] @max-[520px]/composer:size-[12px]" />
        </span>
      )}
      {workflow && !pending && <UltraMark size={12} label={workflow.label} title={workflow.title} className={cx("ml-0.5", ULTRA_HIDDEN)} />}
      {!unknown && loading && <span className="ml-0.5 flex shrink-0"><Spinner size={12} /></span>}
      {!unknown && failed && !loading && <Icon name="warning" stroke={2} className="ml-0.5 size-[12px] shrink-0 text-red @max-[520px]/composer:size-[11px]" />}
      {onOpen && <Icon name="chevron" stroke={2} className="size-[14px] shrink-0 text-ink-2 @max-[520px]/composer:size-[13px]" />}
    </>
  );
  const shell = cx(
    // Cindy's bare toolbar pill: no frame at rest, the capsule shows on hover and while its panel is open
    "flex h-[30px] max-w-[min(320px,100%)] min-w-[72px] shrink items-center rounded-full border border-transparent px-2.5 text-[13px] leading-normal whitespace-nowrap transition-colors duration-150",
    // narrow, it still takes the width its words need (a fixed width cut "Opus 5.5" to "Opus …" beside free room) and
    // shrinks, the name first, only when the row is short of room
    "@max-[520px]/composer:text-[12px] @max-[420px]/composer:min-w-[64px]",
    // Under 420 px the fixed width fits the engine and chevron. A launcher or speed mark needs its own width.
    (pending || launcher || speedLabel) ? "@max-[420px]/composer:w-auto" : "@max-[420px]/composer:w-[64px]",
  );
  // the clip is inside, so the touch target (touch-hit) can reach past the pill's 30 px
  const inner = "flex min-w-0 flex-1 items-center gap-1 overflow-hidden";
  if (!onOpen) {
    return (
      <span data-model-pill="label" title={title} className={shell}>
        <span className="sr-only">{accessibleLabel}</span>
        <span aria-hidden className={inner}>{body}</span>
      </span>
    );
  }
  return (
    <button
      ref={buttonRef}
      type="button"
      data-model-pill={pending ? "pending" : failed ? "failed" : unknown && loading ? "loading" : "ready"}
      aria-haspopup="listbox"
      aria-expanded={Boolean(open) && !disabled}
      aria-label={accessibleLabel}
      title={title}
      disabled={disabled}
      // the caret stays in the message box (Tab still focuses the pill)
      onMouseDown={(e) => e.preventDefault()}
      onClick={(e) => onOpen(e.currentTarget)}
      className={cx(
        shell,
        "touch-hit hover:border-line-strong hover:bg-hover focus-visible:rounded-full! aria-expanded:border-line-strong aria-expanded:bg-hover",
        disabled && "pointer-events-none opacity-50",
      )}
    >
      <span className={inner}>{body}</span>
    </button>
  );
}

/** An Agent's pill: what the session runs, and its panel. Loads the session's own model list once, for the name its list
 *  gives the model and to say when that list can't be read. It opens where the model or effort can change, and for Codex
 *  to explain unsupported effort controls (including a terminal Agent that may switch engines, #5054). */
export const AgentModelPill = memo(function AgentModelPill({ session }: { session: Session }) {
  useModelLists();
  const scope = agentCatalogScope();
  const mid = session.machine_id;
  const own = sessionLauncher(session);
  const ownHost = sessionModelHost(session);
  const launchModel = useStore((s) => launchModelForSession(s.launches, session));
  const switches = sessionSwitches(session);
  const view = pillView(session, modelList(mid, own, undefined, ownHost), launchModel);
  const opens = switches.model || switches.effort || view.engine === "codex";
  useEffect(() => {
    if (opens) void readModels(mid, own, { first: true, host: ownHost });
  }, [opens, mid, own, ownHost, scope]);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const button = useRef<HTMLButtonElement>(null);
  const key = `${mid}/${session.session_id}`;
  // `/model` Enter in the composer opens it where the agent switches models (the composer sends the text otherwise)
  useEffect(() => (switches.model ? onModelPicker(key, () => setAnchor(button.current)) : undefined), [switches.model, key]);
  useEffect(() => {
    if (!opens) setAnchor(null);
  }, [opens]);
  const close = useCallback(() => setAnchor(null), []);
  useEffect(close, [close, scope]);
  const shown = Boolean(anchor) && opens;
  // every list again behind the one shown, once per opening (while the panel's code loads, the first time)
  useEffect(() => {
    if (shown) readForPanel(mid, own, ownHost);
  }, [shown, mid, own, ownHost, scope]);

  // a switch to another launcher that the next message carries out (#5035): its list names the model picked
  const target = session.pending_switch?.launcher;
  useEffect(() => {
    if (target && target !== own) void readModels(mid, target);
  }, [target, own, mid, scope]);
  const namesLauncher = Boolean((opens || view.model) && (session.launcher || target));
  useEffect(() => {
    if (namesLauncher) void readAgentLauncherNames(mid);
  }, [namesLauncher, mid, scope]);

  const launchers = machineLauncherList(mid);
  const waiting = pendingView(session, (name) => modelList(mid, name));
  const pending = waiting ? { ...waiting, launcherName: launcherTitle(waiting.provider, launchers) } : null;
  if (!opens && !view.model) return null;
  const workflow = ultracodeOn(session) ? {} : view.engine === "codex" && session.effort === CODEX_ULTRA ? { label: t("ultracode.codex_ultra_title"), title: t("ultracode.codex_ultra_title") } : undefined;
  return (
    <>
      <ModelPill
        current={{ ...view, launcherName: session.launcher ? launcherTitle(session.launcher, launchers) : undefined }}
        pending={pending}
        loading={view.loading}
        failed={view.failed}
        open={Boolean(anchor)}
        onOpen={opens ? (el) => setAnchor((a) => (a ? null : el)) : undefined}
        workflow={workflow}
        buttonRef={button}
      />
      {shown && anchor && <SessionModelPanel session={session} displayModel={launchModel} anchor={anchor} onClose={close} />}
    </>
  );
});

const AssistantModelPanel = lazyOverlay(() => import("./AssistantModelPanel").then((m) => m.AssistantModelPanel));

/** The key a Persona chat's pill opens its panel on (requestModelPicker): apart from an Agent's, which `/model` Enter
 *  opens, so the text still goes to a Persona chat as it is. */
export const assistantPickerKey = (mid: string, sid: string) => `assistant:${mid}/${sid}`;

/** AssistantChat.followsDefault of chat `session` (the pill's rule below). */
function followsDefaultOf(assistant: AssistantInfo | null, session: Session): boolean {
  return assistant?.main === session.session_id || isBackgroundSupervisor(assistant, session.session_id) ||
    Boolean(assistant?.leaders?.includes(session.session_id)) || session.origin === "task";
}

/** A Persona chat's card for its last turn's model error (turnErrorFix of the session's turn_error_code): a model the chat
 *  can't run opens the chat's own model panel, where another one is picked; while the hub keeps a pick for the chat the
 *  card also offers the panel's way back (follow the default again, or undo the pick), whose code (lib/assistant-pick.ts)
 *  loads on the tap, not with the first screen (startup-preload's budget). `on`: a Persona chat. */
export function useAssistantTurnFix(session: Session | undefined, on: boolean): TurnFix | null {
  const assistant = useStore((s) => s.assistant);
  const kept = useStore((s) => hubHas(s.features, ASSISTANT_MODEL_PICK_FEATURE));
  const fix = on ? turnErrorFix(session?.turn_error_code, session?.launcher) : null;
  if (!fix || !session || fix.action !== "pick_model") return fix;
  const { machine_id: mid, session_id: sid } = session;
  fix.onPickModel = () => requestModelPicker(assistantPickerKey(mid, sid));
  if (kept) {
    // the four dimensions' last working setup (#17368); the hub says when it keeps none
    fix.restore = {
      label: t("chat-modelpill.restore_last_good"),
      run: () => void import("../../lib/execution-restore").then((m) => m.restoreLastGood(getSession(mid, sid) ?? session)),
    };
  }
  if (kept && session.model_pick) {
    fix.back = {
      label: t(followsDefaultOf(assistant, session) ? "chat-modelpill.follow_default" : "chat-modelpill.undo_pick"),
      run: () => void import("../../lib/assistant-pick").then((m) => m.dropConversationPick(getSession(mid, sid) ?? session)),
    };
  }
  return fix;
}

/** A Persona chat's pill (#5033): the launcher, model and effort its next message runs on (its own pick, the default, or
 *  what it started with: lib/assistant-models.ts), opening the unified panel (scope "assistant"). `session` absent: the
 *  new-conversation page, whose pick goes with the start. Every Persona chat has one (#9248) and switches whatever its
 *  state: the hub applies a pick from the next message. */
export const AssistantModelPill = memo(function AssistantModelPill({ session, mainStart = false }: { session?: Session; mainStart?: boolean }) {
  const version = useModelLists();
  const scope = assistantCatalogScope();
  const assistant = useStore((s) => s.assistant);
  const kept = useStore((s) => hubHas(s.features, ASSISTANT_MODEL_PICK_FEATURE));
  const startServiceTier = useStore((s) => hubHas(s.features, ASSISTANT_START_SERVICE_TIER_FEATURE));
  const mid = session?.machine_id ?? assistant?.machine_id ?? "";
  const isMain = mainStart || Boolean(session && assistant?.main === session.session_id);
  // Without a pick, what the next message runs on is a default: Main chat's for the main conversation, a leader and a
  // scheduled task's own conversation (origin "task"); the supervisor's own model purpose for the background supervisor.
  const supervisor = Boolean(session && isBackgroundSupervisor(assistant, session.session_id));
  const followsDefault = Boolean(session && followsDefaultOf(assistant, session));
  const chat: AssistantChat = { session, isMain, followsDefault, kept, config: assistant?.launcher, ...(isMain ? { def: mainDefault(mid) } : supervisor ? { def: superviseDefault(mid) } : {}) };
  const key = assistantPickKey(chat);
  // this page's picks (an older hub, the new-conversation page) draw it again
  const picked = usePickedLauncher(key);
  usePickedModel(key);
  usePickedEffort(key);
  const tier = usePickedServiceTier();
  const { info } = assistantLaunchers(mid);
  // the computer's default and what can run a Persona chat: once for the pill, again behind it on each opening
  useEffect(() => {
    if (!mid) return;
    void readAssistantLaunchers(mid);
    if (isMain) void readMainDefault(mid, { again: true });
    if (supervisor) void readSuperviseDefault(mid);
  }, [mid, scope, supervisor, isMain]);
  // A long-open page must still show the effective Main chat model after a hub-side change (#6542): a push that moves
  // this conversation's revision re-reads the picker, so the pill can't keep an old model the hub no longer resolves.
  // Once per turn: the revision moves with every write while the turn streams (useRevisionReread).
  useRevisionReread(session, Boolean(mid), () => {
    if (!mid) return;
    void readAssistantLaunchers(mid, { again: true });
    if (isMain) void readMainDefault(mid, { again: true });
    if (supervisor) void readSuperviseDefault(mid, { again: true });
  });
  // a pick of this page's that the list no longer offers (gone, or it can't run the assistant) goes back to the default,
  // its model and effort with it: on the new-conversation page they would otherwise go with the start
  useEffect(() => {
    if (!picked || (session && kept) || launcherOffered(info?.launchers ?? null, picked)) return;
    dropPick(key);
    toast(t("chat-modelpill.pick_dropped", { launcher: picked }), "info");
  }, [key, picked, info, session, kept]);
  const setup = assistantSetup(chat, info);
  const catalogLauncher = findAssistantLauncher(info, setup.provider)?.name ?? canonicalLauncher(setup.provider);
  const modelHost = assistantModelHost(session, catalogLauncher);
  // A hub or native catalog that can't honor the speed disarms the draft even while the lazy panel is closed. A manager
  // change disarms it in the store, before this pill or its destination catalog answers.
  useEffect(() => {
    if (session || !tier) return;
    if (!startServiceTier) {
      pickServiceTier(undefined);
      return;
    }
    const list = modelList(mid, catalogLauncher, "assistant", modelHost).list;
    if (!list) return;
    if (!startSpeedTier(currentModelRow(list.models, setup.model), speedId(tier))) pickServiceTier(undefined);
  }, [key, mid, session, catalogLauncher, modelHost, setup.model, tier, startServiceTier, version]);
  useEffect(() => {
    if (mid) void readModels(mid, catalogLauncher, { purpose: "assistant", first: true, host: modelHost });
  }, [mid, scope, catalogLauncher, modelHost]);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const button = useRef<HTMLButtonElement>(null);
  const close = useCallback(() => setAnchor(null), []);
  useEffect(close, [close, scope]);
  // a model error's "Fix it" opens it (useAssistantTurnFix): the chat's own pick, not a new provider
  const sid = session?.session_id;
  useEffect(() => (sid && mid ? onModelPicker(assistantPickerKey(mid, sid), () => setAnchor(button.current)) : undefined), [mid, sid]);
  const shown = Boolean(anchor) && Boolean(mid);
  // every list again behind the one shown, once per opening
  useEffect(() => {
    if (!shown) return;
    void readModels(mid, catalogLauncher, { purpose: "assistant", again: true, first: true, host: modelHost });
    if (isMain) void readMainDefault(mid, { again: true });
    if (supervisor) void readSuperviseDefault(mid, { again: true });
    void readAssistantLaunchers(mid, { again: true }).then(() => {
      if (scope !== assistantCatalogScope()) return;
      for (const l of assistantLaunchers(mid).info?.launchers ?? []) if (l.name !== catalogLauncher) void readModels(mid, l.name, { purpose: "assistant", again: true, host: assistantModelHost(session, l.name) });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per opening
  }, [shown, mid, scope]);
  if (!mid) return null;
  const view = assistantPillView(chat, setup, info, modelList(mid, catalogLauncher, "assistant", modelHost));
  const workflow = view.engine === "codex" && view.effort === CODEX_ULTRA ? { label: t("ultracode.codex_ultra_title"), title: t("ultracode.codex_ultra_title") } : undefined;
  return (
    <>
      <ModelPill current={view} loading={view.loading} failed={view.failed} open={Boolean(anchor)} onOpen={(el) => setAnchor((a) => (a ? null : el))} workflow={workflow} buttonRef={button} />
      {shown && anchor && <AssistantModelPanel chat={chat} mid={mid} anchor={anchor} onClose={close} />}
    </>
  );
});
