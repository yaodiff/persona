// Notice copy loads with the notice view, after the full locale catalog is ready.
import { t } from "../i18n";
import type { HubNotice } from "./hub-update";

export function hubNoticeText(notice: HubNotice): string {
  if (notice.kind === "rolled_back") return notice.why;
  return t(`lib-hub-update.notice_${notice.kind}`, notice);
}
