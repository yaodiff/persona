import { t } from "../../i18n";
import { Group, Row } from "./SettingsParts";

/** The shell's own local options, kept outside the hub page's process and credentials (#6770). */
export function DesktopSettings() {
  const open = window.personaDesktop?.openLocalSettings;
  if (typeof open !== "function") return null;
  return (
    <Group title={t("settings-desktop.title")} anchor="desktop-options">
      <Row label={t("settings-desktop.open")} hint={t("settings-desktop.hint")} onClick={() => open.call(window.personaDesktop)} wrapHint />
    </Group>
  );
}
