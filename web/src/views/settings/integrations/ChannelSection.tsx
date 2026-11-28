// Channel state and unified sections (Account & Connection binding + Chat & Delivery routing) for IntegrationDetail (#14847).
import { useEffect } from "react";
import { t } from "../../../i18n";
import { settingsTarget } from "../../../lib/channels";
import { Loading } from "../../../ui/atoms";
import { ChannelBindingBlock, ChannelDeliveryControls, ChannelTargetControls, ChannelStateInline } from "../Channels";
import { ConfigurationFormView } from "../configuration/ConfigurationForm";
import { Group, Row, SettingsHint } from "../SettingsParts";
import type { ChannelController } from "../useChannelController";
import type { IntegrationTarget } from "./model";

export { ChannelBindingBlock, ChannelStateInline, type ChannelController };
export { useChannelController } from "../useChannelController";

function ChannelRoutingForm({ routing }: { routing: NonNullable<ChannelController["routing"]> }) {
  useEffect(routing.start, [routing]);
  return <ConfigurationFormView ctl={routing.form} onReload={() => void routing.reload()} />;
}

/** Chat & Delivery controls: enable, target, thread_mode & thread_idle_hours, features, capabilities, test message. */
export function ChannelDeliveryBlock({ ctl, target, targetOnly, onSettings }: {
  ctl: ChannelController;
  target: IntegrationTarget;
  targetOnly?: boolean;
  onSettings?: (url: string) => void;
}) {
  const d = ctl.detail;
  const loadError = ctl.error && (
    <Group>
      <SettingsHint alert>{t("settings-integrations.channel_unavailable", { error: ctl.error })}</SettingsHint>
    </Group>
  );
  if (!d) {
    if (loadError) return loadError;
    if (ctl.loading) {
      return (
        <Group>
          <div className="px-4 py-3"><Loading /></div>
        </Group>
      );
    }
    return null;
  }

  const link = d.settings_url && settingsTarget(d.settings_url);
  const selfLink = link && !link.anchor && link.sub === target.id && link.tab === (target.kind === "extension" ? "extensions" : "connectors");
  const openSettings = selfLink ? undefined : onSettings;
  const settingsUrl = d.settings_url;

  const showRouting = d.source === "plugin" && (d.kind === "im" || d.kind === "mention") && d.installed && !d.read_only;
  const controls = targetOnly ? <ChannelTargetControls ctl={ctl} onSettings={openSettings} /> : (
    <ChannelDeliveryControls
      ctl={ctl}
      onSettings={openSettings}
      afterTarget={
        showRouting && ctl.routing ? (
          <fieldset className="mb-6 min-w-0" data-channel-routing disabled={!!ctl.busy && ctl.busy !== "routing"}>
            <ChannelRoutingForm key={ctl.routing.form.source.key} routing={ctl.routing} />
          </fieldset>
        ) : undefined
      }
    />
  );
  return (
    <>
      {loadError}
      {controls}
      {d.read_only && settingsUrl && openSettings && (
        <Group footer={t("settings-channels.settings_elsewhere")}>
          <Row label={t("settings-channels.more_settings")} onClick={() => openSettings(settingsUrl)} />
        </Group>
      )}
    </>
  );
}
