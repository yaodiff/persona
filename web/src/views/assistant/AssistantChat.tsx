// What a Persona conversation adds to the chat page (the app's AssistantTopBar): the persona's dot in the header,
// moving with the conversation's state; the header menu's "Details" / "Knowledge" / "Back to main chat" / "Copy info"; the
// conversation's "Compact context", the main conversation's "New main chat", and the strip while it compacts (#1727). Its
// model is the composer's model pill (views/chat/ModelPill.tsx AssistantModelPill, #5033).
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { assistantInfoText, conversationKey, effectiveLauncher, shortModel, usePickedLauncher } from "../../lib/assistant-chat";
import { isBackgroundSupervisor, isBackgroundSupervisorInteraction } from "../../lib/background-supervisor";
import { COMPACT_FEATURE, COMPACT_REQUESTED_EVENT, compactionView, NEW_MAIN_FEATURE, type CompactionView } from "../../lib/compaction";
import { runtimeFallbackText } from "../../lib/runtime-fallback";
import { personaState } from "../../lib/persona";
import { conversationPage } from "../../lib/conversation-page";
import { lazyPage } from "../../lib/lazy-page";
import { CLEAR_MAIN_FEATURE, useMainClearing } from "../../lib/main-clear";
import { shownManager } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { hubHas, useStore } from "../../lib/store";
import type { Item, Session } from "../../lib/types";
import { copyText, IconButton, Spinner, type MenuItem } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { assistantContextItems, conversationArchiveItem } from "../ConversationMenu";
import { lazyDialog } from "../LazyOverlay";
import { PersonaDot, usePersona } from "./Persona";
import { t } from "../../i18n";

const ModelFallbackCard = lazy(() => import("../chat/ModelFallbackCard").then((m) => ({ default: m.ModelFallbackCard })));

// ── the header ──────────────────────────────────────────

/** The persona's dot beside a Persona conversation's title, in the conversation's state; nothing for the page's mark. */
export function AssistantFace({ session, last }: { session?: Session; last?: Pick<Item, "role" | "kind"> }) {
  const persona = usePersona();
  const conn = useStore((s) => s.conn);
  const machineOnline = useStore((s) => s.machines.find((m) => m.id === (session?.machine_id ?? s.assistant?.machine_id))?.online);
  const waiting = useStore((s) => {
    if (!session) return 0;
    const isMainSession = Boolean(s.assistant?.main && s.assistant.main === session.session_id);
    return s.interactions.filter((i) =>
      (i.machine_id === session.machine_id && i.session_id === session.session_id) ||
      (isMainSession && isBackgroundSupervisorInteraction(s.assistant, i, session.manager)),
    ).length;
  });
  if (!persona.info && !persona.mochi) return null;
  const state = personaState({ online: conn === "online", machineOnline, state: session?.state, interactions: waiting, last });
  return <PersonaDot size={30} state={state} fallback={null} className="-my-1" />;
}

/** The header menu's Persona part: "Details" (the profile), "Knowledge", "Back to main chat", an ordinary conversation's
 *  "Archive" (the sidebar row's action), "Compact context", the main conversation's "Clear context" and "New main chat"
 *  (when the hub can), "Copy info"; `dialogs` goes with them. Empty for an agent. */
const NewMainDialog = lazyDialog(() => import("./NewMainDialog").then((m) => m.NewMainDialog));

export function useAssistantMenu(session: Session | undefined): { items: MenuItem[]; dialogs: React.ReactNode } {
  const assistant = useStore((s) => s.assistant);
  const machine = useStore((s) => (session ? s.machines.find((m) => m.id === session.machine_id)?.name ?? "" : ""));
  const canCompact = useStore((s) => hubHas(s.features, COMPACT_FEATURE));
  const canNewMain = useStore((s) => hubHas(s.features, NEW_MAIN_FEATURE));
  const canClear = useStore((s) => hubHas(s.features, CLEAR_MAIN_FEATURE));
  const clearing = useMainClearing(session?.session_id);
  const picked = usePickedLauncher(session ? conversationKey(session.machine_id, session.session_id) : "");
  const isMain = Boolean(session && assistant?.main === session.session_id);
  const mainScope = session ? JSON.stringify([session.machine_id, session.session_id, session.manager ?? "", isMain, canNewMain]) : "";
  const [mainScopeState, setMainScopeState] = useState<{ scope: string; epoch: number }>(() => ({ scope: mainScope, epoch: 1 }));
  const mainEpoch = mainScopeState.scope === mainScope ? mainScopeState.epoch : mainScopeState.epoch + 1;
  if (mainScopeState.scope !== mainScope) {
    setMainScopeState({ scope: mainScope, epoch: mainEpoch });
  }
  const [newMainState, setNewMainState] = useState<{ epoch: number; open: boolean }>({ epoch: mainEpoch, open: false });
  const newMain = newMainState.epoch === mainEpoch && newMainState.open;
  const setNewMain = (open: boolean) => setNewMainState({ epoch: mainEpoch, open });
  if (!session) return { items: [], dialogs: null };
  const launcher = effectiveLauncher({ isMain, picked: session.model_pick?.launcher || picked, session: session.launcher, config: assistant?.launcher });
  const cwd = session.cwd || assistant?.cwd || "~/.persona/assistant";
  const title = !isMain && session.title && session.title !== "Claude Code" ? session.title : undefined;
  const items: MenuItem[] = [
    { key: "profile", label: t("assistant-assistantchat.details"), icon: "info", onSelect: () => navigate({ name: "profile" }) },
    { key: "kb", label: t("assistant-assistantchat.knowledge"), icon: "book", onSelect: () => navigate({ name: "kb" }) },
    ...(assistant?.main && !isMain ? [{ key: "main", label: t("assistant-assistantchat.back_to_main"), icon: "home" as const, onSelect: () => navigate(conversationPage(assistant.main!, assistant.machine_id, shownManager())) }] : []),
    ...(!isMain && !isBackgroundSupervisor(assistant, session.session_id) ? [conversationArchiveItem(session)] : []),
    ...assistantContextItems(session, {
      canCompact, canClearMain: isMain && canClear, canNewMain: isMain && canNewMain,
      clearingMain: clearing, onNewMain: () => setNewMain(true),
    }, false),
    {
      key: "copyinfo",
      label: t("assistant-assistantchat.copy_info"),
      icon: "copy",
      onSelect: () => void copyText(assistantInfoText({ title, isMain, sessionId: session.session_id, machine, machineId: session.machine_id, cwd, launcher, model: shortModel(session.model) || undefined })),
    },
  ];
  const dialogs = isMain && canNewMain ? <NewMainDialog open={newMain} onClose={() => setNewMain(false)} session={session} scopeKey={`${mainEpoch}:${mainScope}`} /> : null;
  return { items, dialogs };
}

// ── Compact context ─────────────────────────────────────

const dismissedKey = (mid: string, sid: string) => `compact-dismissed:${mid}/${sid}`;
/** How long a done compaction's notice stays. */
const DONE_SHOWN_MS = 4000;
/** How long the strip says "Compacting" after asking, before the hub's word on it arrives. */
const REQUEST_GRACE_MS = 15_000;

const MainChatAgentNotice = lazyPage(() => import("./MainChatAgentNotice").then((m) => m.MainChatAgentNotice));

/** Only the main conversation loads the mismatch check; the existing New-main dialog is shared with the header menu. */
function MainChatAgentNoticeHost({ session }: { session: Session }) {
  const isMain = useStore((s) => s.assistant?.main === session.session_id);
  const canNewMain = useStore((s) => hubHas(s.features, NEW_MAIN_FEATURE));
  const [open, setOpen] = useState(false);
  return (
    <>
      {isMain && <Suspense fallback={null}><MainChatAgentNotice session={session} isMain onNewMain={canNewMain ? () => setOpen(true) : undefined} /></Suspense>}
      {isMain && canNewMain && <NewMainDialog open={open} onClose={() => setOpen(false)} session={session} />}
    </>
  );
}

/** Above the composer of an assistant conversation: its runtime fallback (#2455), then the compaction strip. */
export function CompactionStrip({ session }: { session: Session }) {
  return (
    <>
      <MainChatAgentNoticeHost session={session} />
      {session.runtime_fallback && <RuntimeFallbackNote reason={session.runtime_fallback} />}
      {session.model_fallback && <AssistantModelFallback session={session} />}
      <Compaction session={session} />
    </>
  );
}

/** The refusal fallback's card in a Persona conversation (#2528): "Back to the configured model", and in the main
 *  conversation the existing "New main chat" too (the same dialog as the menu's). */
function AssistantModelFallback({ session }: { session: Session }) {
  const isMain = useStore((s) => s.assistant?.main === session.session_id);
  const canNewMain = useStore((s) => hubHas(s.features, NEW_MAIN_FEATURE));
  const [open, setOpen] = useState(false);
  const offer = isMain && canNewMain;
  return (
    <>
      <Suspense fallback={null}><ModelFallbackCard session={session} headless onNewMain={offer ? () => setOpen(true) : undefined} /></Suspense>
      {offer && <NewMainDialog open={open} onClose={() => setOpen(false)} session={session} />}
    </>
  );
}

/** The conversation asked for the Agent SDK and runs on the CLI: why (no Node.js 22+, no bridge, no claude). */
export function RuntimeFallbackNote({ reason }: { reason: string }) {
  const text = runtimeFallbackText(reason);
  return (
    <div role="status" title={text} className="flex min-h-8 items-center gap-2 rounded-[12px] bg-orange-tint px-3 py-1 text-[12px] text-orange">
      <Icon name="warning" size={12} stroke={2.2} className="shrink-0" />
      <span className="min-w-0 flex-1 truncate">{text}</span>
    </div>
  );
}

/** Above the composer while the conversation compacts ("Compacting context…"), briefly once it is done ("Context compacted"), and a
 *  failure until it is closed (lib/compaction.ts compactionView: only what this page saw change, or just now). */
function Compaction({ session }: { session: Session }) {
  const mid = session.machine_id;
  const sid = session.session_id;
  const c = session.compaction;
  const sig = c ? `${c.started_at}|${c.state}|${c.finished_at ?? ""}|${c.error ?? ""}` : "";
  const prev = useRef<NonNullable<typeof c>["state"] | undefined>(undefined);
  const [view, setView] = useState<CompactionView>(null);
  // asked here and the hub has not said anything new yet: already compacting
  const [requested, setRequested] = useState<{ sig: string; at: number } | null>(null);

  useEffect(() => {
    const v = compactionView(c, prev.current, Date.now(), localStorage.getItem(dismissedKey(mid, sid)) ?? undefined);
    prev.current = c?.state;
    setView(v);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mid, sid, sig]);

  useEffect(() => {
    const on = (e: Event) => {
      const d = (e as CustomEvent<{ mid: string; sid: string }>).detail;
      if (d.mid === mid && d.sid === sid) setRequested({ sig, at: Date.now() });
    };
    window.addEventListener(COMPACT_REQUESTED_EVENT, on);
    return () => window.removeEventListener(COMPACT_REQUESTED_EVENT, on);
  }, [mid, sid, sig]);
  useEffect(() => {
    if (!requested) return;
    if (requested.sig !== sig) {
      setRequested(null);
      return;
    }
    const timer = setTimeout(() => setRequested(null), REQUEST_GRACE_MS);
    return () => clearTimeout(timer);
  }, [requested, sig]);
  useEffect(() => {
    if (view?.kind !== "done") return;
    const timer = setTimeout(() => setView(null), DONE_SHOWN_MS);
    return () => clearTimeout(timer);
  }, [view]);

  const shown: CompactionView = requested ? { kind: "running" } : view;
  if (!shown) return null;
  const dismiss = () => {
    if (shown.kind === "failed") localStorage.setItem(dismissedKey(mid, sid), shown.key);
    setView(null);
  };
  return (
    <div role="status" className="flex min-h-9 items-center gap-2 rounded-[12px] bg-surface px-3 py-1.5 text-[12.5px] text-ink-2 shadow-card" style={{ animation: "fade-up 200ms ease-out both" }}>
      {shown.kind === "running" ? (
        <>
          <Spinner size={12} />
          <span className="min-w-0 flex-1 truncate">{t("assistant-assistantchat.compacting")}</span>
        </>
      ) : shown.kind === "done" ? (
        <>
          <Icon name="check" size={13} stroke={2.2} className="shrink-0 text-green" />
          <span className="min-w-0 flex-1 truncate">{t("assistant-assistantchat.compacted")}</span>
        </>
      ) : (
        <>
          <Icon name="info" size={13} className="shrink-0 text-red" />
          <span className="min-w-0 flex-1 break-words">{t("assistant-assistantchat.compact_failed", { error: shown.error })}</span>
          <IconButton icon="x" label={t("assistant-assistantchat.close")} onClick={dismiss} size={24} iconSize={13} />
        </>
      )}
    </div>
  );
}
