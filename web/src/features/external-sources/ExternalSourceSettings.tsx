import { useCallback, useId, useMemo, useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { defaultManager } from "../../lib/manager-choice";
import { href, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import { Button, cx, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, Row, RowFailed } from "../../views/settings/SettingsParts";
import {
  externalBindingSummary,
  isExternalSourcesUnsupported,
  normalizeExternalBindingSummary,
  readExternalDirections,
  type ExternalRef,
} from "./api";
import { ChannelDefaultsDialog, type ChannelChoice } from "./ChannelDefaultsDialog";
import { channelInput, PROVIDERS, refKey } from "./draft";
import { externalRefLabel, providerLabel } from "./ReferenceFields";
import { useExternalRead } from "./useExternalRead";

const INITIAL_VISIBLE = 5;
const EXPAND_STEP = 10;

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
  const listId = useId();
  const scopeKey = JSON.stringify([manager, provider ?? "", channel ?? ""]);
  const [editingScope, setEditingScope] = useState<string | null>(null);
  const [viewState, setViewState] = useState({
    scope: scopeKey,
    includeDone: false,
    visibleCount: INITIAL_VISIBLE,
    toggled: false,
  });
  const editing = editingScope === scopeKey;
  const includeDone = viewState.scope === scopeKey ? viewState.includeDone : false;
  const visibleCount = viewState.scope === scopeKey ? viewState.visibleCount : INITIAL_VISIBLE;
  const toggled = viewState.scope === scopeKey ? viewState.toggled : false;

  const summary = useExternalRead(useCallback((signal: AbortSignal) =>
    externalBindingSummary(manager, provider, channel, signal, includeDone), [manager, provider, channel, includeDone]));
  const { bindings, omittedDoneCount, totalCount, hasServerCounts } = useMemo(
    () => normalizeExternalBindingSummary(summary.data),
    [summary.data],
  );
  const readDirections = useCallback((signal: AbortSignal) => readExternalDirections(manager, signal), [manager]);
  const directions = useExternalRead(bindings.some((binding) => binding.target_kind === "direction") ? readDirections : null);
  const todos = useStore((state) => state.todos);
  const todoTitles = useMemo(() => new Map(todos
    .filter((row) => (row.manager ?? defaultManager()) === manager)
    .map((row) => [row.id, row.title])), [todos, manager]);
  const directionTitles = useMemo(() => new Map(directions.data?.map((row) => [row.id, row.name || row.title])), [directions.data]);
  if (isExternalSourcesUnsupported(summary.error)) return null;

  const showChannelDefaults = !summary.loading || toggled;
  const showFlows = !copy?.hideEmptyFlows || summary.loading || summary.error !== undefined || bindings.length > 0 || omittedDoneCount > 0 || includeDone;
  const visibleBindings = bindings.slice(0, visibleCount);
  const remainingCount = Math.max(0, bindings.length - visibleBindings.length);
  const nextExpandCount = Math.min(remainingCount, EXPAND_STEP);
  const canShowFewer = visibleBindings.length > INITIAL_VISIBLE;
  const showCompletedToggle = Boolean(summary.data) && (includeDone || omittedDoneCount > 0);
  const showControlsBar = Boolean(summary.data) && bindings.length > 0 && (remainingCount > 0 || canShowFewer || showCompletedToggle);
  const headerCount = summary.data && totalCount > 0
    ? (!includeDone && hasServerCounts
        ? t("external-sources.flows_count_active", { count: totalCount })
        : t("external-sources.flows_count_total", { count: totalCount }))
    : undefined;

  const handleShowMore = () => setViewState({
    scope: scopeKey,
    includeDone,
    visibleCount: visibleCount + EXPAND_STEP,
    toggled,
  });
  const handleShowFewer = () => setViewState({
    scope: scopeKey,
    includeDone,
    visibleCount: INITIAL_VISIBLE,
    toggled,
  });
  const handleToggleCompleted = () => setViewState(includeDone
    ? { scope: scopeKey, includeDone: false, visibleCount: INITIAL_VISIBLE, toggled: true }
    : {
        scope: scopeKey,
        includeDone: true,
        visibleCount: Math.max(visibleCount, Math.min(bindings.length, EXPAND_STEP) + INITIAL_VISIBLE),
        toggled: true,
      });

  return (
    <div data-external-source-settings>
      {showChannelDefaults && (
        <Group title={copy?.channelDefaultsTitle ?? t("external-sources.channel_defaults")} footer={copy?.channelHint ?? t("external-sources.channel_hint")}>
          {provider
            ? <Row label={providerLabel(provider)} wrapHint>
                <Button tone="quiet" className="min-h-11" icon="pencil" onClick={() => setEditingScope(scopeKey)}>{t("external-sources.edit_defaults")}</Button>
              </Row>
            : <div className="px-4 py-2">
                <Button tone="quiet" className="min-h-11" icon="pencil" onClick={() => setEditingScope(scopeKey)}>{t("external-sources.edit_defaults")}</Button>
              </div>}
        </Group>
      )}
      {showFlows && (
        <Group
          title={copy?.flowsTitle ?? t("external-sources.flows")}
          right={headerCount && <span className="shrink-0 text-label text-ink-3 tabular-nums" data-connected-work-count>{headerCount}</span>}
          footer={copy?.flowsHint}
        >
          {summary.loading && <div className="px-4 py-3"><Loading /></div>}
          {summary.error !== undefined && <RowFailed text={errorText(summary.error)} retry={t("todos.retry")} onRetry={summary.retry} />}
          {summary.data && !bindings.length && (!includeDone && omittedDoneCount > 0 ? (
            <div className="flex flex-wrap items-center justify-between gap-2 px-4 py-1.5" data-connected-work-empty-active>
              <span className="text-meta text-ink-2">{t("external-sources.no_active_flows")}</span>
              <Button tone="quiet" className="min-h-11" aria-pressed={false} onClick={handleToggleCompleted}>
                {t("external-sources.show_completed", { count: omittedDoneCount })}
              </Button>
            </div>
          ) : includeDone ? (
            <div className="flex flex-wrap items-center justify-between gap-2 px-4 py-1.5">
              <span className="text-meta text-ink-2">{t("external-sources.no_flows")}</span>
              <Button tone="quiet" className="min-h-11" aria-pressed onClick={handleToggleCompleted}>
                {t("external-sources.hide_completed")}
              </Button>
            </div>
          ) : (
            <Row label={t("external-sources.no_flows")} wrapHint />
          ))}
          {visibleBindings.length > 0 && (
            <ul id={listId} className="divide-y divide-line">
              {visibleBindings.map((binding) => {
                const { external_ref: ref, target_kind: kind, target_id: id } = binding;
                const label = binding.target_title?.trim()
                  || (kind === "direction" ? directionTitles : todoTitles).get(id)?.trim()
                  || t(`external-sources.${kind}`);
                const done = Boolean(binding.target_done);
                const target: Route = kind === "todo" ? { name: "todos", id, manager } : { name: "todos", direction: id, manager };
                const key = binding.source === "explicit"
                  ? `explicit:${binding.binding_id}`
                  : `repo:${refKey(ref)}:${kind}:${id}`;
                return (
                  <li key={key} data-completed={done || undefined}>
                    <a
                      href={href(target)}
                      onClick={(e) => onLinkClick(e, target)}
                      className="flex min-h-11 items-center gap-3 px-4 py-2.5 hover:bg-hover focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
                    >
                      <div className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                        <span className={cx("text-meta font-medium", done ? "text-ink-2" : "text-ink")}>{label}</span>
                        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-submeta text-ink-3">
                          <span>{externalRefLabel(ref)}</span>
                          {done && <Pill>{t("external-sources.completed")}</Pill>}
                          {binding.source === "repository_link" && <Pill>{t("external-sources.repository_link")}</Pill>}
                        </div>
                      </div>
                      <Icon name="chevronRight" size={12} className="shrink-0 text-ink-3" />
                    </a>
                  </li>
                );
              })}
            </ul>
          )}
          {showControlsBar && (
            <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-1" data-connected-work-controls>
              {(remainingCount > 0 || canShowFewer) && (
                <div className="flex flex-wrap items-center gap-1">
                  {remainingCount > 0 && (
                    <Button tone="quiet" className="min-h-11" expanded={false} aria-controls={listId} onClick={handleShowMore}>
                      {t("external-sources.show_more", { count: nextExpandCount })}
                    </Button>
                  )}
                  {canShowFewer && (
                    <Button tone="quiet" className="min-h-11" expanded aria-controls={listId} onClick={handleShowFewer}>
                      {t("external-sources.show_fewer")}
                    </Button>
                  )}
                </div>
              )}
              {showCompletedToggle && (
                <Button tone="quiet" className="min-h-11" aria-pressed={includeDone} onClick={handleToggleCompleted}>
                  {includeDone
                    ? t("external-sources.hide_completed")
                    : t("external-sources.show_completed", { count: omittedDoneCount })}
                </Button>
              )}
            </div>
          )}
        </Group>
      )}
      {editing && (
        <ChannelDefaultsDialog manager={manager} provider={provider} channel={channel}
          channels={channelChoices(provider, accounts, bindings.map((binding) => binding.external_ref))}
          onClose={() => setEditingScope(null)} />
      )}
    </div>
  );
}
