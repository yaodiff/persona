// A launcher that is a managed channel's alias (ManagedLauncher.channel, docs/assistant/execution-spec.md): its key,
// Base URL, headers, proxy and scope are the channel's, so Providers shows it read-only and sends the user to Settings ›
// Models › Channels to change it. The hub refuses editing or deleting it on its own (400, err_alias_read_only); that
// answer gets the same way there.
import { t } from "../../i18n";
import { ApiError, errorText } from "../../lib/api";
import { Button, Pill, toast } from "../../ui/atoms";
import { openExecutionChannel } from "./ExecutionChannelsCard";

/** Opens the channel's editor on the Channels card. */
export const editInChannels = (channel: string) => openExecutionChannel(channel);

/** The row's mark: whose alias it is. */
export function ChannelAliasPill({ channel }: { channel: string }) {
  return (
    <span data-channel-alias={channel} title={t("execution.alias_readonly_title", { channel })}>
      <Pill size="sm">{t("execution.alias_readonly", { channel })}</Pill>
    </span>
  );
}

/** The details' note in place of Edit and Delete. */
export function ChannelAliasNote({ channel, onLeave }: { channel: string; onLeave?: () => void }) {
  return (
    <div role="note" data-channel-alias-note={channel} className="flex flex-wrap items-center gap-2 rounded-control bg-inset px-3 py-2 text-label text-ink-2">
      <span className="min-w-0 flex-1 break-words">{t("execution.alias_readonly_detail", { channel })}</span>
      <Button size="sm" tone="secondary" testId="alias-edit-in-channels" onClick={() => (onLeave?.(), editInChannels(channel))}>
        {t("execution.alias_edit_in_channels")}
      </Button>
    </div>
  );
}

/** A launcher write the hub refused: a channel alias's refusal (400) comes with the way to the Channels card. */
export function launcherWriteFailed(e: unknown, channel?: string) {
  if (e instanceof ApiError && e.status === 400 && channel) {
    toast(errorText(e), "error", { label: t("execution.alias_edit_in_channels"), run: () => editInChannels(channel) });
    return;
  }
  toast(errorText(e), "error");
}
