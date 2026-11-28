// Unified capability-composed Integration Detail shell (integrations_v2, #17762 / Refs #14802).
// Shared framing (top navigation bar, owning assistant context, header card, projection issues)
// and canonical capability mounting slots consumed by built-in services, extensions, standalone
// channels/connectors, and standalone push/subscription sources.
import { useEffect, useState, type ReactNode } from "react";
import { t } from "../../../i18n";
import { api } from "../../../lib/api";
import { settingsTarget, type ChannelDetail } from "../../../lib/channels";
import { connectorBrand, type ConnectorTone } from "../../../lib/connectors";
import { ago } from "../../../lib/format";
import type { IntegrationOrigin, IntegrationSectionKind } from "../../../lib/integrations-projection-wire";
import { ownerHeaders, sameManager, useShownManager } from "../../../lib/manager-choice";
import { managerOf, useRoster } from "../../../lib/managers";
import { navigate } from "../../../lib/router";
import { subscriptionsHref, type SubscriptionCounts, type SubscriptionsScope } from "../../../lib/subscriptions";
import { Button, cx, IconButton, Spinner } from "../../../ui/atoms";
import { BrandMark } from "../../../ui/BrandMark";
import { ChannelMark } from "../../../ui/ChannelMark";
import { Icon } from "../../../ui/icons";
import { countsText } from "../../subscriptions/SubscriptionsEntry";
import { settingsHref } from "../href";
import { OwnerMark } from "../OwnerMark";
import { Group, Pill, Row, SettingsHint } from "../SettingsParts";
import type { IntegrationSectionId } from "./model";
import { projectionStatus } from "./projectedCatalog";
import { ProjectionDetailIssues, type ProjectedDetail } from "./projectedDetail";

const CONN_DOT: Record<ConnectorTone, string> = {
  green: "bg-green",
  orange: "bg-orange",
  red: "bg-red",
  idle: "bg-line-strong",
};

export interface IntegrationDetailSlots {
  connection?: ReactNode;
  channel?: ReactNode;
  inbox?: ReactNode;
  externalSources?: ReactNode;
  extensionSettings?: ReactNode;
  informationAccess?: ReactNode;
  lifecycle?: ReactNode;
}

export interface IntegrationDetailShellProps {
  id: string;
  kind: "builtin" | "extension" | "source";
  resourceKey: string;
  manager?: string;
  onBack?: () => void;
  backLabel?: string;
  onSwitchAssistant?: () => void;
  onRefresh?: () => void;
  refreshDisabled?: boolean;
  mark?: ReactNode;
  title?: string;
  badge?: string;
  status?: ReactNode;
  headerLoading?: boolean;
  headerBody?: ReactNode;
  hideHeaderCard?: boolean;
  projection?: ProjectedDetail;
  banner?: ReactNode;
  slots: IntegrationDetailSlots;
  footer?: ReactNode;
  dialogs?: ReactNode;
}

/** Derives a consistent origin pill label from server projection origin or legacy item metadata. */
export function originBadgeLabel(
  origin: IntegrationOrigin | undefined,
  kind: "builtin" | "extension" | "source",
  options?: { builtinExtension?: boolean; subscription?: boolean },
): string | undefined {
  if (origin) {
    switch (origin) {
      case "builtin_service":
      case "builtin_extension":
        return t("settings-integrations.builtin_badge");
      case "installed_extension":
      case "configured_extension":
        return t("settings-integrations.extension_badge");
      case "standalone_channel":
        return t("settings-integrations.cap_channels");
      case "standalone_connector":
        return t("settings-integrations.cap_data");
      case "standalone_push_source":
        return t("settings-integrations.push_source_badge");
      case "standalone_subscription":
        return t("subscriptions.row");
    }
  }
  if (kind === "extension") {
    return t(options?.builtinExtension ? "settings-integrations.builtin_badge" : "settings-integrations.extension_badge");
  }
  if (kind === "source") {
    return t(options?.subscription ? "subscriptions.row" : "settings-integrations.push_source_badge");
  }
  return t("settings-integrations.builtin_badge");
}

function SectionBlock({
  id,
  slot,
  title,
  children,
}: {
  id: IntegrationSectionId;
  slot: IntegrationSectionKind;
  title: string;
  children: ReactNode;
}) {
  return (
    <section data-integration-section={id} data-integration-slot={slot} aria-label={title} className="mb-6">
      <h2 className="mb-2 px-1 text-label font-semibold text-ink-3">{title}</h2>
      {children}
    </section>
  );
}

/**
 * Capability-composed detail container shell.
 * Enforces one consistent header, owning-assistant bar, and canonical capability slot order:
 *   1. connection
 *   2. conversation_and_push (channel)
 *   3. inbox_and_sync (inbox)
 *   4. external_sources (ConnectorExternalSourceSettings — always after Inbox & Sync)
 *   5. extension_configuration (extension-settings)
 *   6. information_access (ExtensionInfoAccess)
 *   7. lifecycle (management)
 */
export function IntegrationDetailShell({
  id,
  kind,
  resourceKey,
  manager,
  onBack,
  backLabel,
  onSwitchAssistant,
  onRefresh,
  refreshDisabled = false,
  mark,
  title,
  badge,
  status,
  headerLoading = false,
  headerBody,
  hideHeaderCard = false,
  projection,
  banner,
  slots,
  footer,
  dialogs,
}: IntegrationDetailShellProps) {
  const roster = useRoster();
  const shown = useShownManager();
  const crossAssistant = Boolean(manager && !sameManager(manager, shown));
  const ownerName = manager ? managerOf(roster, manager)?.name ?? manager : "";

  return (
    <div
      data-integration-detail={id}
      data-integration-kind={kind}
      data-integration-resource={resourceKey}
    >
      <div data-integration-detail-bar className="mb-3 flex flex-wrap items-center gap-2">
        {onBack && (
          <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="touch-hit -ml-2">
            {backLabel ?? t("settings-integrations.back")}
          </Button>
        )}
        <OwnerMark manager={manager} />
        {crossAssistant && onSwitchAssistant && ownerName && (
          <Button
            tone="secondary"
            size="sm"
            className="touch-hit"
            testId="integration-detail-switch-assistant"
            onClick={onSwitchAssistant}
          >
            {t("settings-integrations.switch_assistant_integrations", { name: ownerName })}
          </Button>
        )}
        <span className="flex-1" />
        {onRefresh && (
          <IconButton
            icon="retry"
            label={t("settings-integrations.refresh")}
            onClick={onRefresh}
            disabled={refreshDisabled}
          />
        )}
      </div>

      {!hideHeaderCard && (
        <section className="mb-6 overflow-hidden rounded-card bg-surface shadow-card">
          <div className="flex items-center gap-3 px-4 py-3.5">
            {mark}
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-2">
                {title && <h1 className="truncate text-card-title font-semibold text-ink">{title}</h1>}
                {badge && <Pill>{badge}</Pill>}
              </div>
              {status && (
                <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-label text-ink-3">
                  {status}
                </div>
              )}
            </div>
            {headerLoading && <Spinner />}
          </div>
          {headerBody && (
            <div className="flex flex-col gap-1.5 border-t border-line px-4 py-3">
              {headerBody}
            </div>
          )}
        </section>
      )}

      <ProjectionDetailIssues projection={projection} />
      {banner}

      {slots.connection && (
        <SectionBlock id="connection" slot="connection" title={t("settings-integrations.section_connection")}>
          {slots.connection}
        </SectionBlock>
      )}

      {slots.channel && (
        <SectionBlock id="channel" slot="conversation_and_push" title={t("settings-integrations.section_channel")}>
          {slots.channel}
        </SectionBlock>
      )}

      {slots.inbox && (
        <SectionBlock id="inbox" slot="inbox_and_sync" title={t("settings-integrations.section_inbox")}>
          {slots.inbox}
        </SectionBlock>
      )}

      {slots.externalSources && (
        <div data-integration-slot="external_sources">
          {slots.externalSources}
        </div>
      )}

      {slots.extensionSettings && (
        <SectionBlock
          id="extension-settings"
          slot="extension_configuration"
          title={t("settings-integrations.section_extension_settings")}
        >
          {slots.extensionSettings}
        </SectionBlock>
      )}

      {slots.informationAccess && (
        <div data-integration-slot="information_access">
          {slots.informationAccess}
        </div>
      )}

      {slots.lifecycle && (
        <SectionBlock id="management" slot="lifecycle" title={t("settings-integrations.section_management")}>
          {slots.lifecycle}
        </SectionBlock>
      )}

      {footer}
      {dialogs}
    </div>
  );
}

export function LastChannelEvent({ detail }: { detail: ChannelDetail | null }) {
  return detail?.last_event_at
    ? <span className="shrink-0">{t("settings-channels.last_event", { when: ago(detail.last_event_at) })}</span>
    : null;
}

export function ProjectedStatus({ projection, channel }: { projection: ProjectedDetail; channel?: ChannelDetail | null }) {
  const status = projectionStatus(projection.detail.item.availability);
  return (
    <>
      <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", CONN_DOT[status.tone])} />
      <span className="truncate">{status.text}</span>
      <LastChannelEvent detail={channel ?? null} />
    </>
  );
}

export function ConnectorReadError({ error, onRetry, loading = false }: { error: string; onRetry?: () => void; loading?: boolean }) {
  return (
    <Group>
      <SettingsHint alert>{t("settings-integrations.connector_unavailable", { error })}</SettingsHint>
      {onRetry && (
        <div className="px-4 py-2.5">
          <Button tone="secondary" icon="retry" className="touch-hit" onClick={onRetry} busy={loading}>
            {t("settings-hubconnector.recheck")}
          </Button>
        </div>
      )}
    </Group>
  );
}

export function BuiltinMark({ id, channel }: { id: string; channel?: ChannelDetail | null }) {
  if (channel) return <ChannelMark c={channel} size={44} />;
  const brand = connectorBrand(id === "feishu-bot" ? "lark" : id);
  if (brand) {
    return (
      <span className="flex size-11 shrink-0 items-center justify-center rounded-card bg-surface shadow-hairline">
        <BrandMark brand={brand} size={44} fit="tile" />
      </span>
    );
  }
  const icon = id === "mail" ? "mail" : id === "imessage" || id === "lark" ? "chat" : id === "feishu-bot" ? "send" : "link";
  return (
    <span className="flex size-11 shrink-0 items-center justify-center rounded-card bg-accent-tint text-accent-ink">
      <Icon name={icon} size={20} />
    </span>
  );
}

// Use the same summary for the row and its section so a subscription-only section needs one read.
export function IntegrationSubscriptions({ counts, scope, onOpen }: {
  counts: SubscriptionCounts;
  scope: NonNullable<SubscriptionsScope>;
  onOpen?: (state: string) => void;
}) {
  return (
    <Group>
      <Row
        icon="loop"
        label={t("subscriptions.row")}
        hint={countsText(counts)}
        onClick={() => (onOpen ? onOpen(JSON.stringify(scope)) : navigate(subscriptionsHref(scope)))}
      />
    </Group>
  );
}

export function openSettingsTarget(
  url: string,
  manager: string | undefined,
  onSettings?: (url: string, manager?: string) => void,
) {
  if (onSettings) {
    if (manager) onSettings(url, manager);
    else onSettings(url);
    return;
  }
  const parsed = settingsTarget(url);
  if (!parsed) return;
  navigate(settingsHref(parsed.tab, parsed.sub, false, manager) + (parsed.anchor ? `#${parsed.anchor}` : ""));
}

export function isGitHubIntegration(id: string, connectorIds: readonly string[] = [], repoProviders?: readonly string[]): boolean {
  return id === "github" || connectorIds.includes("github") || (repoProviders?.includes("github") ?? false);
}

export function SavedRepositoriesShortcut({ manager, onSettings }: { manager?: string; onSettings?: (url: string, manager?: string) => void }) {
  const [count, setCount] = useState<number | null>(null);
  useEffect(() => {
    let alive = true;
    api<{ repositories?: unknown[] }>("/assistant/repositories", { headers: ownerHeaders(manager) })
      .then((res) => {
        if (alive && Array.isArray(res?.repositories)) setCount(res.repositories.length);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [manager]);

  return (
    <Group anchor="integration-saved-repositories">
      <Row
        anchor="saved-repositories-shortcut"
        icon="git"
        label={t("settings-integrations.saved_repositories_title")}
        hint={t("settings-integrations.saved_repositories_hint")}
        wrapHint
        onClick={() => openSettingsTarget("settings/persona#repositories", manager, onSettings)}
      >
        {count !== null ? (
          <span className="tabular-nums">
            {count === 0
              ? t("settings-integrations.saved_repositories_empty")
              : t("settings-integrations.saved_repositories_count", { count })}
          </span>
        ) : undefined}
      </Row>
    </Group>
  );
}

export function KnowledgeScopeShortcut({ manager, onSettings }: { manager?: string; onSettings?: (url: string, manager?: string) => void }) {
  return (
    <Group anchor="integration-knowledge-scope">
      <Row
        anchor="knowledge-scope-shortcut"
        icon="filter"
        label={t("settings-integrations.knowledge_scope_title")}
        hint={t("settings-integrations.knowledge_scope_hint")}
        wrapHint
        onClick={() => openSettingsTarget("settings/knowledge#knowledge-items", manager, onSettings)}
      />
    </Group>
  );
}
