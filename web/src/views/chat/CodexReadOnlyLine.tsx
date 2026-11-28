// A read-only Codex session's footer line (matching StoppedAgentLine): replaces the disabled composer with the
// read-only reason (or stopped Terminal/Resume controls when the exited Codex thread also exposes `terminal`) and
// the lazy-loaded compact takeover / continuation control, reserving height to prevent layout shift.
import { Suspense, lazy } from "react";
import { t } from "../../i18n";
import { resumeInTerminal, useResume } from "../../lib/agent-resume";
import { sourceSubtitle } from "../../lib/format";
import type { Session } from "../../lib/types";
import { Button } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

const TakeoverAction = lazy(() => import("./TakeoverAction"));

export interface StoppedCodexTerminal {
  mid: string;
  sid: string;
  onTerminal: () => void;
}

function StoppedTerminalControls({ mid, sid, onTerminal }: StoppedCodexTerminal) {
  const resume = useResume(mid, sid);
  return (
    <>
      <Button tone="quiet" icon="terminal" onClick={onTerminal}>{t("lib-terminals.terminal")}</Button>
      <Button
        tone="secondary"
        icon="play"
        busy={resume.phase === "resuming"}
        onClick={() => void resumeInTerminal(mid, sid, onTerminal)}
      >
        {t("session-terminalpanel.resume")}
      </Button>
    </>
  );
}

function StoppedResumeError({ mid, sid }: Pick<StoppedCodexTerminal, "mid" | "sid">) {
  const resume = useResume(mid, sid);
  if (resume.phase !== "failed") return null;
  return <p role="alert" className="w-full break-words text-red">{t("session-terminalpanel.resume_failed", { error: resume.error })}</p>;
}

export function CodexReadOnlyLine({
  session,
  visible,
  stopped,
  takeOverRequested = false,
  onTakeOverConsumed,
  continueRequested = false,
  onContinueConsumed,
  onPendingChange,
}: {
  session: Session;
  visible: boolean;
  /** Present when an exited Codex session has both `terminal` (`StoppedAgentLine`) and `takeover` capabilities. */
  stopped?: StoppedCodexTerminal;
  takeOverRequested?: boolean;
  onTakeOverConsumed?: () => void;
  continueRequested?: boolean;
  onContinueConsumed?: () => void;
  onPendingChange?: (pending: boolean) => void;
}) {
  const reason = visible ? (stopped ? session.read_only_reason || t("chat-chatview.stopped_line") : sourceSubtitle(session) || t("chat-chatview.view_only")) : "";
  // Keep the same DOM wrapper and child slot when `visible` flips false so an active TakeoverAction dialog
  // (portaled to document.body) never unmounts across SSE updates that remove capabilities.takeover.
  return (
    <div
      role={visible ? "status" : undefined}
      data-codex-readonly-line={visible ? "" : undefined}
      data-stopped-agent={visible && stopped ? "" : undefined}
      hidden={!visible}
      className={visible ? "flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-card bg-inset px-3 py-2 text-xs text-ink-2" : undefined}
    >
      {visible && stopped ? <Icon name="stop" size={13} className="shrink-0 text-ink-3" /> : null}
      {visible ? <span className="min-w-0 flex-1 basis-48 break-words">{reason}</span> : null}
      <span className="flex flex-wrap items-center justify-end gap-1.5">
        {visible && stopped ? <StoppedTerminalControls {...stopped} /> : null}
        <Suspense fallback={<span aria-hidden className="inline-block h-7 shrink-0" />}>
          <TakeoverAction
            key={`${session.machine_id}:${session.session_id}`}
            session={session}
            variant="compact"
            takeOverRequested={takeOverRequested}
            onTakeOverConsumed={onTakeOverConsumed}
            continueRequested={continueRequested}
            onContinueConsumed={onContinueConsumed}
            onPendingChange={onPendingChange}
          />
        </Suspense>
      </span>
      {visible && stopped ? <StoppedResumeError mid={stopped.mid} sid={stopped.sid} /> : null}
    </div>
  );
}
