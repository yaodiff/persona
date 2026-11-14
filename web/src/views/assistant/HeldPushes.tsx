import { useCallback, useEffect, useRef, useState } from "react";
import { api, onCredentialChange } from "../../lib/api";
import { heldPushDestination, heldPushGroups, lnModeEnabled, type HeldPushDestination, type HeldPushGroups, type HeldPushItem } from "../../lib/push-intercepts";
import { getState } from "../../lib/store";
import { formatDate, t } from "../../i18n";
import { Button, cx, Dialog, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { openAgent, openTask } from "../../lib/handoff";
import { openLink } from "../../lib/links";

export type { HeldPushDestination } from "../../lib/push-intercepts";

/** A destination is opened only when the hub supplies a typed, unambiguous one. */
export function openHeldPushDestination(destination: HeldPushDestination, manager?: string) {
  if (destination.kind === "todo") openTask(destination.id);
  else if (destination.kind === "session") openAgent(destination.machineId, destination.sessionId);
  else openLink(destination.url, undefined, manager);
}

function reasonLabel(reason: string): string {
  if (reason === "ln_dedup_12h") return t("push-intercepts.held_reason_dedup");
  if (reason === "ln_p2_silent") return t("push-intercepts.held_reason_p2");
  return t("push-intercepts.held_reason_p1");
}

function HeldPushRow({ item, destination, onOpen }: { item: HeldPushItem; destination?: HeldPushDestination; onOpen?: () => void }) {
  const summaries = item.summaries.length ? item.summaries : [""];
  const content = (
    <>
      <div className="min-w-0 flex-1">
        <div className="break-words text-[13px] font-medium text-ink">{summaries[0] || t("push-intercepts.held_untitled")}</div>
        {summaries.slice(1).map((summary, index) => <div key={`${item.id}-summary-${index}`} className="mt-1 break-words text-[12px] text-ink-2">{summary}</div>)}
        <div className="mt-1 text-[11px] text-ink-3">{reasonLabel(item.reason)}{item.manager?.trim() ? ` · ${item.manager}` : ""}</div>
      </div>
      <time dateTime={item.at} className="shrink-0 text-[11px] tabular-nums text-ink-3">{formatDate(item.date, { timeStyle: "short" })}</time>
      {destination && <Icon name="chevronRight" size={13} className="shrink-0 text-ink-3" />}
    </>
  );
  const className = "flex w-full min-w-0 items-start gap-3 rounded-control bg-field px-3 py-2.5 text-left";
  if (destination && onOpen) {
    return <button type="button" data-held-push={item.id} onClick={onOpen} className={cx(className, "hover:bg-hover")}>{content}</button>;
  }
  return <article data-held-push={item.id} className={className}>{content}</article>;
}

function HeldPushList({ groups, destinations }: { groups: HeldPushGroups; destinations?: (item: HeldPushItem) => HeldPushDestination | undefined }) {
  const [showP2, setShowP2] = useState(false);
  const row = (item: HeldPushItem) => {
    const destination = destinations ? destinations(item) : heldPushDestination(item, new Set(getState().todos.map((todo) => todo.id)));
    return <HeldPushRow key={item.id} item={item} destination={destination} onOpen={destination ? () => openHeldPushDestination(destination, item.manager) : undefined} />;
  };
  return (
    <div className="flex flex-col gap-4" data-held-push-list>
      <section aria-label={t("push-intercepts.held_p1")}>
        <h3 className="mb-1.5 px-1 text-[12px] font-medium text-ink-3">{t("push-intercepts.held_p1_count", { count: groups.p1.length })}</h3>
        <div className="flex flex-col gap-1.5">{groups.p1.map(row)}</div>
      </section>
      {groups.p2.length > 0 && (
        <section aria-label={t("push-intercepts.held_p2")}>
          <button type="button" aria-expanded={showP2} onClick={() => setShowP2((value) => !value)} className="mb-1.5 flex min-h-8 w-full items-center gap-1.5 px-1 text-left text-[12px] font-medium text-ink-3 hover:text-ink">
            <Icon name={showP2 ? "chevron" : "chevronRight"} size={12} className="shrink-0" />
            <span>{t("push-intercepts.held_p2_count", { count: groups.p2.length })}</span>
          </button>
          {showP2 && <div className="flex flex-col gap-1.5">{groups.p2.map(row)}</div>}
        </section>
      )}
      {groups.p1.length + groups.p2.length === 0 && <p className="px-1 py-3 text-[13px] text-ink-2">{t("push-intercepts.held_empty")}</p>}
    </div>
  );
}

export function useHeldPushes() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [groups, setGroups] = useState<HeldPushGroups>({ p1: [], p2: [] });
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const mounted = useRef(true);

  const refresh = useCallback(async () => {
    abort.current?.abort();
    const controller = new AbortController();
    abort.current = controller;
    setLoading(true);
    try {
      const mode = await api<unknown>("/push/ln-mode", { signal: controller.signal });
      if (controller.signal.aborted || !mounted.current) return;
      const on = lnModeEnabled(mode);
      setEnabled(on);
      if (!on) {
        setGroups({ p1: [], p2: [] });
        setFailed(false);
        return;
      }
      const records = await api<unknown>("/push/intercepts", { signal: controller.signal });
      if (controller.signal.aborted || !mounted.current) return;
      setGroups(heldPushGroups(records));
      setFailed(false);
    } catch (error) {
      if (controller.signal.aborted || !mounted.current) return;
      setFailed(true);
      // A missing route is an older hub: it has no ln-mode entry to show.
      if (typeof error === "object" && error !== null && "status" in error && (error.status === 404 || error.status === 405)) setEnabled(false);
    } finally {
      if (!controller.signal.aborted && mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refresh();
    const stop = onCredentialChange(() => void refresh());
    const onVisible = () => {
      if (document.visibilityState === "visible") void refresh();
    };
    document.addEventListener("visibilitychange", onVisible);
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, 60_000);
    return () => {
      mounted.current = false;
      stop();
      document.removeEventListener("visibilitychange", onVisible);
      window.clearInterval(timer);
      abort.current?.abort();
    };
  }, [refresh]);

  return { enabled, groups, loading, failed, refresh };
}

export function HeldPushesEntry({ className, destinations }: { className?: string; destinations?: (item: HeldPushItem) => HeldPushDestination | undefined }) {
  const { enabled, groups, loading, failed, refresh } = useHeldPushes();
  const [open, setOpen] = useState(false);
  if (enabled !== true) return null;
  const count = groups.p1.length + groups.p2.length;
  return (
    <>
      <button
        type="button"
        data-held-pushes-entry
        aria-expanded={open}
        aria-label={t("push-intercepts.held_entry_label", { count })}
        title={t("push-intercepts.held_entry_hint")}
        onClick={() => {
          setOpen(true);
          void refresh();
        }}
        className={className}
      >
        <Icon name="bell" size={13} className="shrink-0" />
        <span>{t("push-intercepts.held_entry")}</span>
        {count > 0 && <span className="tabular-nums text-ink-2">{count}</span>}
      </button>
      <Dialog open={open} onClose={() => setOpen(false)} title={t("push-intercepts.held_title")} width={560}>
        <div className="mb-3 flex items-center justify-between gap-3">
          <p className="text-[12px] text-ink-3">{t("push-intercepts.held_explanation")}</p>
          <Button icon="retry" busy={loading} onClick={() => void refresh()}>{t("assistant-tasks.retry")}</Button>
        </div>
        {failed && <div role="alert" className="mb-3 rounded-control bg-danger-wash px-3 py-2 text-[12px] text-ink">{t("push-intercepts.held_load_failed")}</div>}
        {loading && count === 0 && !failed ? <Loading label={t("push-intercepts.held_loading")} /> : <HeldPushList groups={groups} destinations={destinations} />}
      </Dialog>
    </>
  );
}
