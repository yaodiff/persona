import { useCallback, useMemo, useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { defaultManager } from "../../lib/manager-choice";
import { href, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import { Button, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, Row, RowFailed } from "../../views/settings/SettingsParts";
import { externalBindingSummary, isExternalSourcesUnsupported, readExternalDirections, type ExternalRef } from "./api";
import { ChannelDefaultsDialog, type ChannelChoice } from "./ChannelDefaultsDialog";
import { channelInput, PROVIDERS, refKey } from "./draft";
import { externalRefLabel, providerLabel } from "./ReferenceFields";
import { useExternalRead } from "./useExternalRead";

export interface ExternalSourceSettingsProps {
  manager: string;
  provider?: string;
  channel?: string;
  /** Known account IDs and their visible names, when the integration already lists them. */
  accounts?: { channel: string; name: string }[];
  /** Host-provided wording where the blocks appear without their usual page context (an extension's details). */
  copy?: ExternalSourceSettingsCopy;
}

export interface ExternalSourceSettingsCopy {
  channelDefaultsTitle?: string;
  channelHint?: string;
  flowsTitle?: string;
  flowsHint?: string;
  /** An empty list is not useful where the block is secondary: leave it out until something is connected. */
  hideEmptyFlows?: boolean;
}

function channelChoices(provider: string | undefined, accounts: ExternalSourceSettingsProps["accounts"], refs: ExternalRef[]) {
  const choices = new Map<string, ChannelChoice>();
  function add(ref: ExternalRef | null, label?: string) {
    if (ref && !choices.has(refKey(ref))) {
      choices.set(refKey(ref), { ref, label: label || externalRefLabel(ref) });
    }
  }
  for (const account of accounts ?? []) if (provider) add(channelInput(provider, account.channel), account.name);
  for (const name of provider ? [provider] : PROVIDERS) add(channelInput(name, ""));
  for (const ref of refs) add(channelInput(ref.provider, ref.channel ?? ""));
  return [...choices.values()];
}

export function ExternalSourceSettings({ manager, provider, channel, accounts, copy }: ExternalSourceSettingsProps) {
  const [editing, setEditing] = useState(false);
  const summary = useExternalRead(useCallback((signal: AbortSignal) =>
    externalBindingSummary(manager, provider, channel, signal), [manager, provider, channel]));
  const bindings = summary.data?.bindings ?? [];
  const readDirections = useCallback((signal: AbortSignal) => readExternalDirections(manager, signal), [manager]);
  const directions = useExternalRead(bindings.some((binding) => binding.target_kind === "direction") ? readDirections : null);
  const todos = useStore((state) => state.todos);
  const todoTitles = useMemo(() => new Map(todos
    .filter((row) => (row.manager ?? defaultManager()) === manager)
    .map((row) => [row.id, row.title])), [todos, manager]);
  const directionTitles = useMemo(() => new Map(directions.data?.map((row) => [row.id, row.name || row.title])), [directions.data]);
  if (isExternalSourcesUnsupported(summary.error)) return null;
  const showFlows = !copy?.hideEmptyFlows || summary.loading || summary.error !== undefined || bindings.length > 0;

  return (
    <div data-external-source-settings>
      {!summary.loading && (
        <Group title={copy?.channelDefaultsTitle ?? t("external-sources.channel_defaults")} footer={copy?.channelHint ?? t("external-sources.channel_hint")}>
          {provider
            ? <Row label={providerLabel(provider)} wrapHint>
                <Button tone="quiet" className="min-h-11" icon="pencil" onClick={() => setEditing(true)}>{t("external-sources.edit_defaults")}</Button>
              </Row>
            : <div className="px-4 py-2">
                <Button tone="quiet" className="min-h-11" icon="pencil" onClick={() => setEditing(true)}>{t("external-sources.edit_defaults")}</Button>
              </div>}
        </Group>
      )}
      {showFlows && <Group title={copy?.flowsTitle ?? t("external-sources.flows")} footer={copy?.flowsHint}>
        {summary.loading && <div className="px-4 py-3"><Loading /></div>}
        {summary.error !== undefined && <RowFailed text={errorText(summary.error)} retry={t("todos.retry")} onRetry={summary.retry} />}
        {summary.data && !bindings.length && <Row label={t("external-sources.no_flows")} wrapHint />}
        <ul className="divide-y divide-line">
          {bindings.map((binding) => {
            const { external_ref: ref, target_kind: kind, target_id: id } = binding;
            const label = (kind === "direction" ? directionTitles : todoTitles).get(id) || t(`external-sources.${kind}`);
            const target: Route = kind === "todo" ? { name: "todos", id, manager } : { name: "todos", direction: id, manager };
            return (
              <li key={binding.source === "explicit" ? binding.binding_id : JSON.stringify([refKey(ref), id, binding.repository_role])}>
                <a href={href(target)} onClick={(e) => onLinkClick(e, target)}
                  className="flex min-h-11 items-center gap-3 px-4 py-2.5 hover:bg-hover focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent">
                  <div className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                    <span className="text-meta font-medium text-ink">{label}</span>
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-submeta text-ink-3">
                      <span>{externalRefLabel(ref)}</span>
                      {binding.source === "repository_link" && <Pill>{t("external-sources.repository_link")}</Pill>}
                    </div>
                  </div>
                  <Icon name="chevronRight" size={12} className="shrink-0 text-ink-3" />
                </a>
              </li>
            );
          })}
        </ul>
      </Group>}
      {editing && (
        <ChannelDefaultsDialog manager={manager} provider={provider} channel={channel}
          channels={channelChoices(provider, accounts, bindings.map((binding) => binding.external_ref))}
          onClose={() => setEditing(false)} />
      )}
    </div>
  );
}
