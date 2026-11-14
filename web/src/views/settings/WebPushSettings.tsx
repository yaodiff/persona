// Settings › Notifications › When Persona is closed (#7459, docs/web/web.md "Web Push"): this browser's Web Push, off
// until turned on here. What it needs is said in the row when the browser can't (https, the Home Screen app on iPhone
// and iPad, not the desktop app), its state when it can (through which push service, the last delivery or failure),
// and a test that shows a notification at once.
import { useEffect, useState } from "react";
import { formatDate, t } from "../../i18n";
import { errorText } from "../../lib/api";
import { loadWebPush, sendTestPush, turnOffWebPush, turnOnWebPush, useWebPush, WebPushError, webPushSupport, type WebPushState, type WebPushSupport } from "../../lib/web-push";
import { Button, toast } from "../../ui/atoms";
import { Group, Pill, Row, Switch } from "./SettingsParts";

const SUPPORT_KEY: Record<Exclude<WebPushSupport, "ok">, string> = {
  "desktop-app": "settings-notifications.webpush_desktop_app",
  dev: "settings-notifications.webpush_dev",
  subpath: "settings-notifications.webpush_subpath",
  insecure: "settings-notifications.webpush_insecure",
  "home-screen": "settings-notifications.webpush_home_screen",
  unsupported: "settings-notifications.webpush_unsupported",
};

type Permission = NotificationPermission | "unsupported";
const currentPermission = (): Permission => (typeof Notification === "undefined" ? "unsupported" : Notification.permission);

/** The row's second line: why it can't, or how it stands. */
export function webPushHint(support: WebPushSupport, s: WebPushState, permission: Permission): string {
  if (support !== "ok") return t(SUPPORT_KEY[support]);
  if (s.hubMissing) return t("settings-notifications.webpush_old_hub");
  if (!s.loaded) return t("settings-notifications.webpush_loading");
  if (permission === "denied") return t("settings-notifications.webpush_denied");
  const st = s.status;
  if (!st?.subscribed) return t("settings-notifications.webpush_off");
  if (st.failed_at) return t("settings-notifications.webpush_failed", { error: st.error ?? "" });
  if (st.current === false) return t("settings-notifications.webpush_renewing");
  const service = st.service || st.host || "";
  if (st.last_ok_at) return t("settings-notifications.webpush_on_last", { service, time: formatDate(st.last_ok_at, { dateStyle: "medium", timeStyle: "short" }) });
  return t("settings-notifications.webpush_on", { service });
}

export function WebPushSettings() {
  const [support] = useState(webPushSupport);
  const s = useWebPush();
  const [permission, setPermission] = useState(currentPermission);
  useEffect(() => {
    if (support === "ok") void loadWebPush();
  }, [support]);

  const on = !!s.status?.subscribed;
  const usable = support === "ok" && s.loaded && !s.hubMissing;

  const toggle = async (next: boolean) => {
    try {
      if (next) await turnOnWebPush();
      else await turnOffWebPush();
    } catch (e) {
      if (e instanceof WebPushError) toast(t(e.reason === "denied" ? "settings-notifications.webpush_denied" : "settings-notifications.webpush_no_worker"), "error");
      else toast(t("settings-notifications.webpush_toggle_failed", { error: errorText(e) }), "error");
    } finally {
      setPermission(currentPermission());
    }
  };

  const test = async () => {
    try {
      await sendTestPush();
      toast(t("settings-notifications.webpush_test_sent"));
    } catch (e) {
      toast(t("settings-notifications.webpush_test_failed", { error: errorText(e) }), "error");
    }
  };

  return (
    <Group title={t("settings-notifications.webpush_title")} anchor="web-push" footer={t("settings-notifications.webpush_footer")}>
      <Row icon="send" label={t("settings-notifications.webpush")} hint={webPushHint(support, s, permission)} wrapHint>
        {on && s.status?.failed_at && <Pill tone="orange">{t("settings-notifications.webpush_failed_pill")}</Pill>}
        {usable && !on && permission === "denied" && <Pill tone="orange">{t("settings-notifications.denied_pill")}</Pill>}
        <Switch
          on={on}
          onChange={(next) => void toggle(next)}
          disabled={!usable || s.working || (!on && permission === "denied")}
          label={t("settings-notifications.webpush")}
        />
      </Row>
      {on && usable && (
        <Row label={t("settings-notifications.webpush_test_label")} hint={t("settings-notifications.webpush_test_hint")} wrapHint>
          <Button tone="secondary" onClick={() => void test()} disabled={s.working} busy={s.working}>
            {t("settings-notifications.webpush_test")}
          </Button>
        </Row>
      )}
    </Group>
  );
}
