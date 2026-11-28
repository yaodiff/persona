import { t } from "../../i18n";
import type { Machine } from "../../lib/types";
import { Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

const phaseLabels = {
  waiting_idle: t("chat-assistantupdate.waiting_idle"),
  transferring: t("chat-assistantupdate.transferring"),
  draining: t("chat-assistantupdate.draining"),
  restarting: t("chat-assistantupdate.restarting"),
  confirming: t("chat-assistantupdate.confirming"),
} satisfies Record<NonNullable<Machine["deploy_phase"]>, string>;

export default function AssistantUpdateNotice({ phase }: { phase: keyof typeof phaseLabels | "updating" | "pending_restart" }) {
  const waitingIdleRestart = phase === "pending_restart" || phase === "draining";
  const label = phase === "pending_restart" ? t("chat-assistantupdate.pending_restart")
    : phase === "updating" ? t("chat-assistantupdate.updating")
    : phaseLabels[phase] ?? t("chat-assistantupdate.updating");
  return (
    <div role="status" data-assistant-update={phase} className="shrink-0 border-b border-line bg-inset px-4 py-2 text-meta text-ink-2">
      <div className="flex items-center gap-2">
        {waitingIdleRestart ? <Icon name="clock" className="size-3.5 shrink-0" /> : <Spinner />}
        <div className="flex flex-wrap gap-x-2">
          <span className="font-medium text-ink">{label}</span>
          <span>{t(waitingIdleRestart ? "chat-assistantupdate.restart_when_idle" : "chat-assistantupdate.reconnect")}</span>
        </div>
      </div>
    </div>
  );
}
