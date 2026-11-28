import { useEffect, useRef, useState } from "react";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { continuationLaunch, takeoverBody, type CodexTakeoverPlan } from "../../lib/codex-takeover";
import { launchAgent } from "../../lib/new-agent";
import { t } from "../../i18n";
import type { Session } from "../../lib/types";
import { Button, Dialog, IconButton, Menu, toast, useMenu } from "../../ui/atoms";
import { upsertSession } from "../../lib/store";

/** Localized labels for the top-bar overflow menu entries, loaded lazily with this chunk to stay off the first screen. */
export function takeoverMenuLabels(): { takeOver: string; continueNew: string } {
  return {
    takeOver: t("chat-takeover.takeover"),
    continueNew: t("chat-takeover.takeover_new"),
  };
}

export function takeoverContinueLabel(): string {
  return t("chat-takeover.takeover_new");
}

export default function TakeoverAction({
  session,
  variant = "default",
  takeOverRequested = false,
  onTakeOverConsumed,
  continueRequested = false,
  onContinueConsumed,
  onPendingChange,
}: {
  session: Session;
  variant?: "default" | "compact";
  /** One-shot request from the top-bar overflow menu (e.g. Terminal view); consumed via [onTakeOverConsumed]. */
  takeOverRequested?: boolean;
  onTakeOverConsumed?: () => void;
  /** One-shot request from the top-bar overflow menu; consumed immediately via [onContinueConsumed] so reconnects never replay it. */
  continueRequested?: boolean;
  onContinueConsumed?: () => void;
  /** True while a request or confirmation dialog is active, so hosts keep this mounted across SSE updates. */
  onPendingChange?: (pending: boolean) => void;
}) {
  const [plan, setPlan] = useState<CodexTakeoverPlan | null>(null);
  const [loading, setLoading] = useState(false);
  const [confirmStop, setConfirmStop] = useState(false);
  const [busy, setBusy] = useState(false);
  const menu = useMenu();
  const path = `/machines/${enc(session.machine_id)}/codex/${enc(session.session_id)}/takeover`;
  const takeoverAvailable = Boolean(session.capabilities?.takeover);
  const canResume = Boolean(session.capabilities?.takeover?.can_resume);

  useEffect(() => {
    onPendingChange?.(Boolean(plan || loading || busy));
  }, [plan, loading, busy, onPendingChange]);

  const commit = async (p: CodexTakeoverPlan, confirm: boolean) => {
    const body = takeoverBody(p, confirm);
    if (!body) return;
    setBusy(true);
    try {
      const result = await api<{ session: Session }>(path, { method: "POST", json: body });
      upsertSession({ ...result.session, machine_id: session.machine_id });
      setPlan(null); setConfirmStop(false);
      toast(t("chat-takeover.takeover_done"), "ok");
    } catch (e) {
      if (e instanceof ApiError && e.code === "codex_takeover_continue_only") {
        setPlan({ ...p, can_resume: false, reason: "encrypted_context" });
        setConfirmStop(false);
      } else {
        // A failed response may follow token consumption. A retry must start with a fresh GET.
        setPlan(null);
        setConfirmStop(false);
        toast(errorText(e), "error");
      }
    } finally { setBusy(false); }
  };

  const prepare = async () => {
    if (loading || busy) return;
    setLoading(true);
    try {
      const result = await api<CodexTakeoverPlan>(path);
      setPlan(result);
      if (!result.can_resume) return;
      if (result.running || result.app_owned) { setConfirmStop(true); return; }
      await commit(result, false);
    } catch (e) { toast(errorText(e), "error"); }
    finally { setLoading(false); }
  };

  const continueNew = async () => {
    if (loading || busy) return;
    setLoading(true);
    try {
      const result = await api<CodexTakeoverPlan>(path);
      if (!result.can_resume) { setPlan(result); return; }
      launchAgent(continuationLaunch(result, session.machine_id, session.todo?.id));
    } catch (e) { toast(errorText(e), "error"); }
    finally { setLoading(false); }
  };

  const prepareRef = useRef(prepare);
  prepareRef.current = prepare;
  const takeOverConsumedRef = useRef(onTakeOverConsumed);
  takeOverConsumedRef.current = onTakeOverConsumed;
  useEffect(() => {
    if (!takeOverRequested) return;
    takeOverConsumedRef.current?.();
    void prepareRef.current();
  }, [takeOverRequested]);

  const continueRef = useRef(continueNew);
  continueRef.current = continueNew;
  const consumedRef = useRef(onContinueConsumed);
  consumedRef.current = onContinueConsumed;
  useEffect(() => {
    if (!continueRequested) return;
    consumedRef.current?.();
    void continueRef.current();
  }, [continueRequested]);

  const continueElsewhere = () => {
    if (!plan) return;
    launchAgent(continuationLaunch(plan, session.machine_id, session.todo?.id));
    setPlan(null); setConfirmStop(false);
  };

  if (!takeoverAvailable && !plan) return null;

  const controls = variant === "compact" ? (
    takeoverAvailable ? (
      canResume ? (
        <>
          <Button size="sm" tone="secondary" busy={loading} disabled={loading} onClick={() => void prepare()}>{t("chat-takeover.takeover")}</Button>
          <IconButton icon="more" size={28} label={t("chat-chatview.more_actions")} disabled={loading} active={Boolean(menu.anchor)} onClick={menu.open} />
          {menu.anchor && (
            <Menu
              anchor={menu.anchor}
              onClose={menu.close}
              align="right"
              side="top"
              items={[{ key: "continue", label: t("chat-takeover.takeover_new"), icon: "compose", onSelect: () => void continueNew() }]}
            />
          )}
        </>
      ) : (
        <Button size="sm" tone="secondary" busy={loading} disabled={loading} onClick={() => void continueNew()}>{t("chat-takeover.takeover_new_short")}</Button>
      )
    ) : null
  ) : (
    <>
      {canResume && <Button size="sm" busy={loading} disabled={loading} onClick={() => void prepare()}>{t("chat-takeover.takeover")}</Button>}
      {takeoverAvailable && <Button size="sm" disabled={loading} onClick={() => void continueNew()}>{t("chat-takeover.takeover_new")}</Button>}
    </>
  );

  return <>
    {controls}
    <Dialog open={Boolean(plan && (confirmStop || !plan.can_resume))} onClose={() => { if (!busy) { setPlan(null); setConfirmStop(false); } }} title={t("chat-takeover.takeover_title")}
      footer={plan && !plan.can_resume
        ? <Button tone="secondary" onClick={continueElsewhere}>{t("chat-takeover.takeover_new")}</Button>
        : <><Button tone="quiet" disabled={busy} onClick={() => { setPlan(null); setConfirmStop(false); }}>{t("chat-chatview.not_now")}</Button>
          {plan?.can_resume && <Button tone={confirmStop ? "danger" : "accent"} busy={busy} onClick={() => plan && void commit(plan, confirmStop)}>{t("chat-takeover.takeover_confirm")}</Button>}
          {plan && <Button tone="secondary" disabled={busy} onClick={continueElsewhere}>{t("chat-takeover.takeover_new")}</Button>}</>}>
      {confirmStop ? <p>{plan?.app_owned && !plan?.running
        ? t("chat-takeover.takeover_app_confirm")
        : t("chat-takeover.takeover_running_confirm")}</p> : <p>{takeoverReason(plan?.reason)}</p>}
    </Dialog>
  </>;
}

function takeoverReason(reason: string | undefined): string {
  switch (reason) {
    case "app_running": return t("chat-takeover.reason_app_running");
    case "encrypted_context": return t("chat-takeover.reason_encrypted_context");
    case "unavailable": return t("chat-takeover.reason_unavailable");
    default: return t("chat-takeover.reason_unavailable");
  }
}
