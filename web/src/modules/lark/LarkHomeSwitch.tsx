// Settings › Connectors › Lark › Show messages on home (iOS FeishuConnectorView's switch): off by default, kept by this browser.
// While it is on, a second switch says whether the page lists the personal AI assistants too (mix.ts, on by default).
import { t } from "../../i18n";
import { setLarkHome, useLarkHome } from "./home";
import { setMessagesMix, useMessagesMix } from "./mix";
import { Switch } from "../../views/settings/SettingsParts";

export function LarkHomeSwitch() {
  const on = useLarkHome();
  const mix = useMessagesMix();
  return (
    <>
      <div className="flex items-center gap-3 px-4 py-2.5">
        <div className="min-w-0 flex-1">
          <div className="text-meta text-ink">{t("lark-homeswitch.label")}</div>
          <div className="mt-0.5 text-submeta leading-snug text-ink-3">{t("lark-homeswitch.hint")}</div>
        </div>
        <Switch on={on} onChange={setLarkHome} label={t("lark-homeswitch.label")} />
      </div>
      {on && (
        <div className="flex items-center gap-3 border-t border-line px-4 py-2.5">
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("lark-homeswitch.mix_label")}</div>
            <div className="mt-0.5 text-submeta leading-snug text-ink-3">{t("lark-homeswitch.mix_hint")}</div>
          </div>
          <Switch on={mix} onChange={setMessagesMix} label={t("lark-homeswitch.mix_label")} />
        </div>
      )}
    </>
  );
}
