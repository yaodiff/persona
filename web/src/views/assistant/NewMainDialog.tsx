import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, errorText } from "../../lib/api";
import { archivedMainTitle, NEW_MAIN_FEATURE, NEW_MAIN_PATH, newMainBody } from "../../lib/compaction";
import { beginMainChange, forgetMainClearedIf, mainActionScope, markMainCleared, useMainClearing } from "../../lib/main-clear";
import { chosenManager } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { getState, hubHas, refreshAssistant } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, Dialog, toast, type ToastAction } from "../../ui/atoms";
import { Switch } from "./parts";

export interface NewMainResult {
  ok: boolean;
  archived?: string;
  carried?: boolean;
}

export interface NewMainDialogDeps {
  canStart?: () => boolean;
  startNewMain?: (carry: boolean, expect: string) => Promise<NewMainResult>;
  refresh?: () => Promise<unknown>;
  navigateMain?: () => void;
  notify?: (text: string, tone: "ok" | "error" | "info", action?: ToastAction) => void;
  markCleared?: (sid: string) => void;
  forgetCleared?: () => void;
  scopeKey?: string;
}

export interface NewMainDialogProps {
  open: boolean;
  onClose: () => void;
  session: Session;
  scopeKey?: string;
  onNavigate?: () => void;
  deps?: NewMainDialogDeps;
}

/** Archives the main conversation (renamed "Old main chat · until <date>") and leaves none: the next message starts the new one. */
export function NewMainDialog({ open, onClose, session, scopeKey, onNavigate, deps }: NewMainDialogProps) {
  const effectiveScope = JSON.stringify([scopeKey ?? deps?.scopeKey ?? "", session.machine_id, session.session_id, session.manager ?? ""]);
  const [instance, setInstance] = useState<{ scope: string; open: boolean; epoch: number }>(() => ({
    scope: effectiveScope,
    open,
    epoch: 1,
  }));
  const scopeChanged = instance.scope !== effectiveScope;
  const openedFresh = open && !instance.open;
  const effectiveEpoch = scopeChanged || openedFresh ? instance.epoch + 1 : instance.epoch;
  if (scopeChanged || instance.open !== open) {
    setInstance({ scope: effectiveScope, open, epoch: effectiveEpoch });
  }

  const [carryState, setCarryState] = useState<{ epoch: number; carry: boolean }>(() => ({ epoch: effectiveEpoch, carry: true }));
  const [busyEpoch, setBusyEpoch] = useState<number | null>(null);
  const carry = carryState.epoch === effectiveEpoch ? carryState.carry : true;
  const busy = busyEpoch === effectiveEpoch;

  const mountedRef = useRef(true);
  const openRef = useRef(open);
  const activeEpochRef = useRef(effectiveEpoch);
  const busyLockRef = useRef(false);
  const offerDetachedRetry = useRef<(() => void) | null>(null);
  const onCloseRef = useRef(onClose);
  const onNavigateRef = useRef(onNavigate);
  const depsRef = useRef(deps);

  useLayoutEffect(() => {
    openRef.current = open;
    activeEpochRef.current = effectiveEpoch;
    onCloseRef.current = onClose;
    onNavigateRef.current = onNavigate;
    depsRef.current = deps;
  });

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeEpochRef.current = 0;
      offerDetachedRetry.current?.();
      offerDetachedRetry.current = null;
    };
  }, []);

  useEffect(() => {
    busyLockRef.current = false;
  }, [effectiveEpoch]);

  const setCarry = (next: boolean) => setCarryState({ epoch: effectiveEpoch, carry: next });

  type Outcome = { message: string; tone: "ok" | "info" };
  const [readRetry, setReadRetry] = useState<{ epoch: number; outcome: Outcome; error: string } | null>(null);
  const retry = readRetry?.epoch === effectiveEpoch ? readRetry : null;
  const changingMain = useMainClearing(session.session_id);
  const go = async () => {
    const epoch = activeEpochRef.current;
    if (busyLockRef.current || !openRef.current || !mountedRef.current) return;
    const sid = session.session_id;
    const actions = depsRef.current;
    const canStart = actions?.canStart ?? (() => hubHas(getState().features, NEW_MAIN_FEATURE) && getState().assistant?.main === sid);
    if (!retry && !canStart()) { onCloseRef.current(); return; }
    const release = beginMainChange(sid);
    if (!release) return;
    const scope = mainActionScope();
    const manager = chosenManager() ?? "";
    const forget = () => {
      if (!actions?.markCleared) forgetMainClearedIf(sid, manager);
      actions?.forgetCleared?.();
    };
    const currentView = () => scope.current() && scope.samePage() && (mountedRef.current
      ? openRef.current && activeEpochRef.current === epoch
      : Boolean(getState().assistant && getState().assistant?.main !== sid));
    const notify = actions?.notify ?? toast;
    async function readReplacement(outcome: Outcome) {
      if (!currentView()) return;
      try {
        // A stale/coalesced read may lose to a newer read. Only announce success once the replacement is visible.
        const updated = await (actions?.refresh ?? refreshAssistant)();
        const assistant = getState().assistant;
        if ((!actions?.refresh || updated === false) && (!assistant || assistant.main === sid)) {
          throw new Error(t("chat-chatview.refresh_failed"));
        }
      } catch (e) {
        if (!currentView()) return;
        const error = t("personaassistant.load_failed", { error: errorText(e) });
        const offerRetry = () => {
          if (!currentView()) return;
          let used = false;
          notify(error, "error", {
            label: t("personaassistant.retry"),
            sticky: true,
            run: () => {
              if (used) return;
              used = true;
              void readReplacement(outcome);
            },
          });
        };
        if (mountedRef.current) {
          offerDetachedRetry.current = offerRetry;
          setReadRetry({ epoch, outcome, error });
        } else {
          offerRetry();
        }
        return;
      }
      // The refresh itself can unmount the menu. It belongs to this request unless the user left its page or scope.
      if (!scope.current() || !scope.samePage() || (mountedRef.current && activeEpochRef.current !== epoch)) return;
      onCloseRef.current();
      (actions?.navigateMain ?? (() => navigate({ name: "persona" })))();
      onNavigateRef.current?.();
      notify(outcome.message, outcome.tone);
    }
    offerDetachedRetry.current = null;
    busyLockRef.current = true;
    setBusyEpoch(epoch);
    try {
      let outcome = retry?.outcome;
      if (!outcome) {
        (actions?.markCleared ?? markMainCleared)(sid);
        try {
          const r = actions?.startNewMain ? await actions.startNewMain(carry, sid)
            : await api<NewMainResult>(NEW_MAIN_PATH, { method: "POST", json: newMainBody(carry, sid) });
          outcome = { message: r.carried ? t("assistant-assistantchat.new_main_done_carried") : t("assistant-assistantchat.new_main_done"), tone: "ok" };
        } catch (e) {
          forget();
          if (!(e instanceof ApiError && e.code === "main_changed")) {
            if (currentView()) notify(t("assistant-assistantchat.new_main_failed", { error: errorText(e) }), "error");
            return;
          }
          outcome = { message: t("assistant-assistantchat.new_main_elsewhere"), tone: "info" };
        }
      }
      await readReplacement(outcome);
    } finally {
      release();
      if (activeEpochRef.current === epoch) {
        busyLockRef.current = false;
        if (mountedRef.current) setBusyEpoch(null);
      }
    }
  };

  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("assistant-assistantchat.new_main_title")}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={changingMain} onClick={() => void go()}>{retry ? t("personaassistant.retry") : t("assistant-assistantchat.new_main")}</Button>
        </>
      }
    >
      {retry && <p role="alert" className="text-danger">{retry.error}</p>}
      <p className="text-[13px] leading-relaxed text-ink-2">{t("assistant-assistantchat.new_main_body", { title: archivedMainTitle() })}</p>
      <div className="mt-3 flex items-start gap-3 rounded-control bg-inset px-3 py-2.5 shadow-hairline">
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium text-ink">{t("assistant-assistantchat.carry_summary")}</div>
          <div className="mt-0.5 text-[12px] leading-relaxed text-ink-3">{t("assistant-assistantchat.carry_summary_hint")}</div>
        </div>
        <Switch checked={carry} onChange={setCarry} label={t("assistant-assistantchat.carry_summary")} disabled={busy || Boolean(retry)} />
      </div>
    </Dialog>
  );
}
