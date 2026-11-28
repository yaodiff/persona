// "Mark all as seen", asked first (views/AgentMenus.tsx MarkSeenDialog): a chunk of its own (#17142), loaded on the
// first open, since the sidebar and Home mount it on every first screen.
import { useEffect, useRef, useState } from "react";
import { errorText } from "../lib/api";
import { markSeen } from "../lib/store";
import { markAllSeenLabel } from "../lib/state-labels";
import type { Session } from "../lib/types";
import { Button, Dialog } from "../ui/atoms";
import { t } from "../i18n";
import type { MarkSeenDialogProps } from "./AgentMenus";

const isPromiseLike = (v: unknown): v is PromiseLike<unknown> => Boolean(v) && typeof (v as PromiseLike<unknown>).then === "function";
const sessionScopeKey = (s: Session) => `${s.machine_id}/${s.session_id}`;

/** "Mark all as seen", asked first: it changes what the other devices show too. `release`: every row in the scope
 *  whose held-seen place also leaves when confirmed (defaults to `list`). */
export function MarkSeenDialogBody({ list, release, onClose, onDone, onMarkSeen, onNotice }: MarkSeenDialogProps) {
  const n = list?.length ?? 0;
  const scopeKey = list ? `${list.map(sessionScopeKey).join(",")}::${(release ?? list).map(sessionScopeKey).join(",")}` : "";
  const [prevScope, setPrevScope] = useState(scopeKey);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const opRef = useRef(0);
  const aliveRef = useRef(true);
  const inFlightRef = useRef(false);
  const callbacksRef = useRef({ onClose, onDone, onNotice });
  callbacksRef.current = { onClose, onDone, onNotice };
  if (prevScope !== scopeKey) {
    setPrevScope(scopeKey);
    if (pending) setPending(false);
    if (error) setError("");
  }
  useEffect(() => { aliveRef.current = true; return () => { aliveRef.current = false; inFlightRef.current = false; opRef.current++; }; }, []);
  useEffect(() => () => { inFlightRef.current = false; opRef.current++; }, [scopeKey]);

  const confirm = () => {
    if (!list || pending || inFlightRef.current) return;
    const target = release ?? list;
    if (!onMarkSeen) { markSeen(target); onDone?.(); onClose(); return; }
    setError("");
    const token = ++opRef.current;
    inFlightRef.current = true;
    try {
      const result = onMarkSeen(target);
      if (isPromiseLike(result)) {
        setPending(true);
        void Promise.resolve(result).then(
          () => {
            if (!aliveRef.current || token !== opRef.current) return;
            inFlightRef.current = false;
            setPending(false);
            callbacksRef.current.onDone?.();
            callbacksRef.current.onClose();
          },
          (e) => {
            if (!aliveRef.current || token !== opRef.current) return;
            inFlightRef.current = false;
            const msg = errorText(e);
            setPending(false);
            setError(msg);
            callbacksRef.current.onNotice?.(msg, "error");
          },
        );
        return;
      }
      inFlightRef.current = false;
      onDone?.();
      onClose();
    } catch (e) {
      inFlightRef.current = false;
      const msg = errorText(e);
      setError(msg);
      onNotice?.(msg, "error");
    }
  };

  return (
    <Dialog
      open={Boolean(list)}
      onClose={onClose}
      title={markAllSeenLabel()}
      width={380}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon="check" busy={pending} disabled={pending} onClick={confirm}>
            {markAllSeenLabel(n)}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{t("agentmenus.mark_all_seen_body", { count: n })}</p>
      {error && <p role="alert" className="mt-2 text-meta text-red">{error}</p>}
    </Dialog>
  );
}
