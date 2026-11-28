import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { navigate } from "../../lib/router";
import { dismissHubNotice, hubNoticeKey, hubNoticeText, setHubUpdateSettings, startHubUpdate, type HubNotice, type HubUpdateStatus } from "../../lib/hub-update";
import { Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export default function HubNoticeView({ notice, onNavigate, onStatus, onDismiss }: {
  notice: HubNotice;
  onNavigate?: () => void;
  onStatus: (status: HubUpdateStatus) => void;
  onDismiss: (dismissed: string[]) => void;
}) {
  async function update(request: Promise<HubUpdateStatus>) {
    try {
      onStatus(await request);
    } catch (error) {
      toast(errorText(error), "error");
    }
  }
  function open() {
    navigate("/settings?tab=hub");
    onNavigate?.();
  }
  const warn = notice.kind === "rolled_back";
  const key = hubNoticeKey(notice);
  const action = "shrink-0 whitespace-nowrap rounded-control px-2 py-0.5 text-label [@media(pointer:coarse)]:min-h-11 font-medium text-accent-ink hover:bg-hover";
  return (
    <div data-testid="hub-notice" data-kind={notice.kind} className="relative z-10 ms-(--sidebar-indent) flex flex-wrap items-center gap-x-1 rounded-control border-s border-line ps-2 pe-1 py-1 hover:bg-hover [@media(pointer:coarse)]:min-h-11">
      <button type="button" onClick={open} title={t("lib-hub-update.notice_open")} className="flex min-w-0 grow basis-24 items-start gap-2 rounded-control text-left [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:items-center" aria-live="polite">
        <span className={`mt-[3px] flex size-3.5 shrink-0 items-center justify-center ${warn ? "text-orange" : "text-accent-ink"}`}>
          {notice.kind === "updating" ? <Spinner size={11} /> : <Icon name={warn ? "warning" : "retry"} size={13} stroke={2.2} />}
        </span>
        <span className="min-w-0 flex-1 text-meta leading-snug wrap-anywhere text-ink-2">{hubNoticeText(notice)}</span>
      </button>
      {notice.kind === "auto_on" && (
        <>
          <button type="button" data-testid="hub-notice-auto-off" onClick={() => void update(setHubUpdateSettings({ auto: false }))} className={action}>{t("lib-hub-update.notice_turn_off")}</button>
          <button type="button" data-testid="hub-notice-seen" onClick={() => void update(setHubUpdateSettings({ auto_notice_seen: true }))} className={action}>{t("lib-hub-update.notice_got_it")}</button>
        </>
      )}
      {notice.kind === "available" && (
        <button type="button" data-testid="hub-notice-update" onClick={() => void update(startHubUpdate(false))} className={action}>{t("lib-hub-update.notice_update")}</button>
      )}
      {key && (
        <button type="button" data-testid="hub-notice-dismiss" aria-label={t("lib-hub-update.notice_dismiss")} title={t("lib-hub-update.notice_dismiss")} onClick={() => onDismiss(dismissHubNotice(key))} className="shrink-0 rounded-control p-1 [@media(pointer:coarse)]:size-11 inline-grid place-items-center text-ink-3 hover:bg-hover hover:text-ink-2">
          <Icon name="x" size={12} stroke={2} />
        </button>
      )}
    </div>
  );
}
