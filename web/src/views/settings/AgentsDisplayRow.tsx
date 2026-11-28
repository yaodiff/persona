import { lazy, Suspense, useEffect } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { refreshAgentsDisplay, retryAgentsDisplay, saveAgentsDisplay, useAgentsDisplay } from "../../lib/agents-display";
import { href } from "../../lib/router";
import { Spinner } from "../../ui/atoms";
import { Row, RowFailed, SettingsHint, Switch } from "./SettingsParts";

// Only while botmux sessions are shown, and only from a hub that has it.
const BotmuxOwnerField = lazy(() => import("./BotmuxOwnerField").then((m) => ({ default: m.BotmuxOwnerField })));

export function AgentsDisplayRow() {
  const { value, saving, error, change } = useAgentsDisplay();
  useEffect(() => { void refreshAgentsDisplay(); }, []);
  return <>
    <Row label={t("settings.show_botmux")} hint={t("settings.show_botmux_hint")} wrapHint anchor="botmux-display">
      {value ? <Switch on={value.show_botmux} disabled={saving} onChange={(on) => void saveAgentsDisplay(on)} label={t("settings.show_botmux")} /> : !error && <Spinner />}
    </Row>
    <SettingsHint>{t("settings.show_botmux_inventory")} <a className="text-accent hover:underline" href={href({ name: "machines" })}>{t("settings.show_botmux_computers")}</a></SettingsHint>
    {value?.show_botmux && value.botmux_owner_union_id !== undefined && <Suspense fallback={null}>
      <BotmuxOwnerField saved={value.botmux_owner_union_id} disabled={saving} />
    </Suspense>}
    {value?.use_herdr_tab_name !== undefined && <Row label={t("settings.use_herdr_tab_name")} hint={t("settings.use_herdr_tab_name_hint")} wrapHint anchor="herdr-tab-session-name">
      <Switch on={value.use_herdr_tab_name} disabled={saving} onChange={(on) => void saveAgentsDisplay({ use_herdr_tab_name: on })} label={t("settings.use_herdr_tab_name")} />
    </Row>}
    {value?.sort_by_activity !== undefined && <Row label={t("settings.sort_by_activity")} hint={t("settings.sort_by_activity_hint")} wrapHint anchor="agents-sort-by-activity">
      <Switch on={value.sort_by_activity} disabled={saving} onChange={(on) => void saveAgentsDisplay({ sort_by_activity: on })} label={t("settings.sort_by_activity")} />
    </Row>}
    {value?.show_filter !== undefined && <Row label={t("settings.show_agents_filter")} hint={t("settings.show_agents_filter_hint")} wrapHint anchor="agents-show-filter">
      <Switch on={value.show_filter} disabled={saving} onChange={(on) => void saveAgentsDisplay({ show_filter: on })} label={t("settings.show_agents_filter")} />
    </Row>}
    {saving && <SettingsHint><span role="status">{change && "use_herdr_tab_name" in change ? t("settings.herdr_tab_name_saving") : change && "botmux_owner_union_id" in change ? t("settings.botmux_owner_saving") : t("settings.botmux_saving")}</span></SettingsHint>}
    {error !== undefined && <RowFailed text={errorText(error)} retry={t("app.retry")} onRetry={() => void retryAgentsDisplay()} />}
  </>;
}
