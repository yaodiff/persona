// Lazy-loaded unified Integration Detail view (integrations_v2, #14847 / #17762 / Refs #14802).
// Composes built-in services, standalone channels/connectors, and extensions through the shared
// capability-section IntegrationDetailShell, mounted stably by canonical resource identity
// `(manager, kind, id)` so crossing the 740px split breakpoint preserves configuration state.
import { useMailboxEnabled } from "../../lib/mail";
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { hubServes, moduleFeature } from "../../core/module-host";
import { formatList, t } from "../../i18n";
import { ConnectorExternalSourceSettings } from "../../features/external-sources/ExternalSourceSettingsEntry";
import { enc, errorText, isAbort } from "../../lib/api";
import { canBind, isBound, type ChannelDetail } from "../../lib/channels";
import { connectorState, connectorTone, type ConnectorTone } from "../../lib/connectors";
import { shownPages, signInAsked, takeSignIn } from "../../lib/extension-auth";
import { marketOf } from "../../lib/extension-market";
import { dependencyText, dependentError, type Contribution, type Dependent } from "../../lib/extension-model";
import {
  channelOf, extensionTone, fieldLabel, pointText, runNote, saveFooter, secretState, setContribution,
  setExtensionAutoUpgrade, setExtensionEnabled, setupSecret, stateLine,
  type ExtensionField, type ExtensionSecret, type JsonObject,
} from "../../lib/extensions";
import { otherManager, pinManagerAt, useChosenManager } from "../../lib/manager-choice";
import { useRoster } from "../../lib/managers";
import { useStore } from "../../lib/store";
import { Button, cx, Dialog, Loading, toast } from "../../ui/atoms";
import { ExtensionLogo } from "../../ui/ExtensionLogo";
import { Icon } from "../../ui/icons";
import { countsOf, useSubscriptionSummary } from "../../lib/subscriptions";
import { SubscriptionsEntry } from "../subscriptions/SubscriptionsEntry";
import { Feishu, Mail, MailInboxSwitch } from "./Connectors";
import { AuthGroup } from "./ExtensionAuth";
import { ExtensionInfoAccess } from "./ExtensionInfoAccess";
import { PackMemberRow, UninstallDialog, useInstalledNames } from "./ExtensionMarket";
import {
  FieldRow, hasLauncherField, SandboxGroup, SecretDialog, SecretProviderGroups,
  useFormLaunchers, type FieldProps,
} from "./Extensions";
import {
  enableFooter, Information as ExtensionInformationGroup, lifecycleReason,
  Updates as ExtensionUpdatesGroup,
} from "./ExtensionSettingsPage";
import { Disclosure } from "./ProviderFields";
import { Group, Row, SettingsHint, Switch } from "./SettingsParts";
import { useExtensionBinding, useExtensionConfiguration, type ExtensionBinding } from "./integrations/extensionDrafts";
import { useDetailConnectors } from "./integrations/useDetailConnectors";
import { ChannelBindingBlock, ChannelDeliveryBlock, ChannelStateInline, useChannelController } from "./integrations/ChannelSection";
import { ConnectorConnectionBlock, connectorHasInbox, ConnectorInboxBlock } from "./integrations/ConnectorSection";
import {
  BuiltinMark, ConnectorReadError, IntegrationDetailShell, IntegrationSubscriptions,
  isGitHubIntegration, KnowledgeScopeShortcut, LastChannelEvent, originBadgeLabel,
  ProjectedStatus, SavedRepositoriesShortcut, type IntegrationDetailSlots,
} from "./integrations/DetailShell";
import { FeishuBotConnectionPanel, FeishuBotDeliveryPanel, useFeishuBotController } from "./integrations/FeishuBotSection";
import {
  fieldsHaveError, integrationResourceKey, isIntegrationsV2Active, partitionExtensionFields,
  resolveBuiltinChannelId, resolveBuiltinConnectorId, resolveIntegrationResource,
  type BuiltinIntegrationId, type IntegrationSectionId, type IntegrationTarget,
} from "./integrations/model";
import { editableSection, projectedSections, type ProjectedDetail } from "./integrations/projectedDetail";

export type { BuiltinIntegrationId, IntegrationSectionId, IntegrationTarget };
export { isIntegrationsV2Active, partitionExtensionFields };

export interface IntegrationDetailProps {
  target: IntegrationTarget;
  /** Read-only metadata for this exact resource when the owning assistant supports projection. */
  projection?: ProjectedDetail;
  /** Proposed configuration from a settings link, kept as an unsaved draft. */
  prefill?: JsonObject;
  /** Back action; omit when the top bar (phone) or desktop split-pane list already provides navigation. */
  onBack?: () => void;
  /** Way back from a disabled integration that has no row in the split-pane catalog. */
  onUnavailableBack?: () => void;
  /** Explicit user-initiated switch to this integration's owning assistant (cross-assistant deep links). */
  onSwitchAssistant?: () => void;
  /** Called after an extension is uninstalled so callers can close the detail pane even when `onBack` is omitted. */
  onUninstalled?: () => void;
  /** Open a declarative UI page contributed by an extension. */
  onPage?: (extensionId: string, uiId: string, manager?: string) => void;
  /** Open the marketplace upgrade confirmation flow for an extension. */
  onUpgrade?: (market: string, id: string, manager?: string) => void;
  /** Navigate to a legacy settings URL when linked by a channel override hint. */
  onSettings?: (url: string, manager?: string) => void;
  /** Open a related connector integration (for example larkcli from Feishu Bot). */
  onOpenConnector?: (id: string, manager?: string) => void;
  /** Open scoped source subscriptions inside unified Integrations instead of leaving Settings. */
  onOpenSources?: (state?: string, manager?: string) => void;
  /**
   * Optional gate override; when omitted, follows the active assistant's `integrations_v2` flag.
   * Always fails closed when `integrations_v2` is false or absent.
   */
  integrationsV2?: boolean;
  /** Rendered when `integrations_v2` is false (defaults to null so legacy pages stay unchanged). */
  fallback?: ReactNode;
}

const NARROW_LIST: Intl.ListFormatOptions = { type: "conjunction", style: "narrow" };
const EXT_DOT: Record<ReturnType<typeof extensionTone>, string> = {
  red: "bg-red",
  orange: "bg-orange",
  green: "bg-green",
  idle: "bg-line-strong",
};
const EXT_TILE: Record<ReturnType<typeof extensionTone>, string> = {
  red: "bg-red-tint text-red",
  orange: "bg-orange-tint text-orange",
  green: "bg-accent-tint text-accent-ink",
  idle: "bg-inset text-ink-3 shadow-hairline",
};
const CONN_DOT: Record<ConnectorTone, string> = {
  green: "bg-green",
  orange: "bg-orange",
  red: "bg-red",
  idle: "bg-line-strong",
};

function hasBinding(d: ChannelDetail | null) {
  return !!d && !d.read_only && d.source !== "market" && (canBind(d) || isBound(d));
}

function projectedChannelController(ctl: ReturnType<typeof useChannelController>, projection?: ProjectedDetail) {
  if (!projection || !ctl.detail) return ctl;
  const section = projectedSections(projection, "conversation_and_push", "channel").find(({ ref }) => ref.id === ctl.detail?.id);
  return section && editableSection(section) ? ctl : { ...ctl, detail: { ...ctl.detail, read_only: true } };
}

/** Renders a slice of extension config fields (flat fields + object groups) sharing the unified draft. */
function SectionFieldGroups({
  fields,
  title,
  footer,
  fieldProps,
}: {
  fields: ExtensionField[];
  title?: string;
  footer?: string;
  fieldProps: Omit<FieldProps, "field" | "path">;
}) {
  if (!fields.length) return null;
  const flat = fields.filter((f) => f.type !== "object");
  const groups = fields.filter((f) => f.type === "object");
  return (
    <>
      {flat.length > 0 && (
        <Group title={title} footer={footer}>
          {flat.map((f) => (
            <FieldRow key={f.name} {...fieldProps} field={f} path={[f.name]} />
          ))}
        </Group>
      )}
      {groups.map((g) => (
        <Group key={g.name} title={fieldLabel(g)} footer={g.description}>
          {(g.fields ?? []).map((f) => (
            <FieldRow key={f.name} {...fieldProps} field={f} path={[g.name, f.name]} />
          ))}
        </Group>
      ))}
    </>
  );
}

/**
 * Unified Integration Detail page under `integrations_v2`.
 * When `integrations_v2` is false, renders `fallback` (default null) with zero side effects or API requests.
 */
export function IntegrationDetail(props: IntegrationDetailProps) {
  const picked = useChosenManager();
  const effectiveManager = props.target.manager ?? otherManager(picked ?? undefined);
  const storeEnabled = useStore((s) => isIntegrationsV2Active(s.assistant, effectiveManager, picked));
  const mailEnabled = useMailboxEnabled();
  const larkModule = useStore((s) => hubServes(moduleFeature("lark"), s));
  const enabled = props.integrationsV2 ?? storeEnabled;

  if (!enabled) return <>{props.fallback ?? null}</>;

  const { target, projection } = props;
  const resource = resolveIntegrationResource(target, effectiveManager, projection?.detail.item.ref);
  const resourceKey = integrationResourceKey(resource.manager, resource.kind, resource.id);

  if (target.kind === "builtin") {
    const moduleOff = projection?.detail.item.availability.state === "module_off";
    const mailOff = (projection ? moduleOff : !mailEnabled) && target.id === "mail";
    const feishuOff = (projection ? moduleOff : !larkModule) && (
      target.id === "feishu-bot" || target.id === "feishu" || target.channelId === "feishu" ||
      target.id === "larkcli" || target.connectorId === "larkcli"
    );
    if (mailOff || feishuOff || moduleOff) {
      const back = props.onBack ?? props.onUnavailableBack;
      return (
        <div data-integration-detail={target.id} data-integration-kind="builtin" data-integration-resource={resourceKey}>
          {back && (
            <Button tone="quiet" icon="chevronLeft" onClick={back} className="touch-hit -ml-2 mb-3">
              {t("settings-integrations.back")}
            </Button>
          )}
          <Group title={projection?.detail.item.name ?? t(mailOff ? "settings-connectors.mail" : "settings-feishubot.title")}>
            <SettingsHint>{t(mailOff ? "settings-integrations.mail_module_off" : feishuOff ? "settings-integrations.feishu_module_off" : "settings-integrations.projection_module_off")}</SettingsHint>
          </Group>
        </div>
      );
    }
  }

  if (target.kind === "extension") {
    return (
      <ExtensionCapabilityDetail
        key={resourceKey}
        resourceKey={resourceKey}
        target={target}
        projection={projection}
        manager={effectiveManager}
        prefill={props.prefill}
        onBack={props.onBack}
        onSwitchAssistant={props.onSwitchAssistant}
        onUninstalled={props.onUninstalled}
        onPage={props.onPage}
        onUpgrade={props.onUpgrade}
        onSettings={props.onSettings}
        onOpenSources={props.onOpenSources}
      />
    );
  }

  if (target.id === "feishu-bot" && (!projection || projection.detail.item.ref.kind === "service")) {
    return (
      <FeishuBotCapabilityDetail
        key={resourceKey}
        resourceKey={resourceKey}
        target={target}
        projection={projection}
        manager={effectiveManager}
        onBack={props.onBack}
        onSwitchAssistant={props.onSwitchAssistant}
        onSettings={props.onSettings}
        onOpenConnector={props.onOpenConnector}
        onOpenSources={props.onOpenSources}
      />
    );
  }

  return (
    <BuiltinCapabilityDetail
      key={resourceKey}
      resourceKey={resourceKey}
      target={target}
      projection={projection}
      manager={effectiveManager}
      onBack={props.onBack}
      onSwitchAssistant={props.onSwitchAssistant}
      onSettings={props.onSettings}
      onOpenSources={props.onOpenSources}
    />
  );
}

export default IntegrationDetail;

// ── Feishu Bot built-in capability composition ───────────────────────────────────────────

function FeishuBotCapabilityDetail({
  resourceKey,
  target,
  projection,
  manager,
  onBack,
  onSwitchAssistant,
  onSettings,
  onOpenConnector,
  onOpenSources,
}: {
  resourceKey: string;
  target: Extract<IntegrationTarget, { kind: "builtin" }>;
  projection?: ProjectedDetail;
  manager?: string;
  onBack?: () => void;
  onSwitchAssistant?: () => void;
  onSettings?: (url: string, manager?: string) => void;
  onOpenConnector?: (id: string, manager?: string) => void;
  onOpenSources?: (state?: string, manager?: string) => void;
}) {
  const channelId = projection ? target.channelId ?? null : resolveBuiltinChannelId(target) ?? "feishu";
  const connectorId = projection ? target.connectorId ?? null : "larkcli";
  useLayoutEffect(() => pinManagerAt(["/bots/feishu", ...(connectorId ? [`/connectors/${enc(connectorId)}`] : [])], manager), [connectorId, manager]);

  const channelCtl = projectedChannelController(useChannelController(channelId, manager), projection);
  const { connectors: [larkcli], load: loadLarkcli, loading: connectorLoading, error: connectorError, onSaved } =
    useDetailConnectors(connectorId ? [connectorId] : [], manager, !!projection);

  const serviceConnection = projectedSections(projection, "connection", "service")[0];
  const botEditable = !projection || (!!serviceConnection && editableSection(serviceConnection));
  const channelSection = projectedSections(projection, "conversation_and_push", "channel")[0];
  const canDeliver = !projection || (!!channelSection && editableSection(channelSection));
  const feishuCtl = useFeishuBotController(botEditable || canDeliver, loadLarkcli, channelCtl.load);

  const refreshAll = () => {
    projection?.refresh();
    void loadLarkcli({ fresh: true });
    if (channelId) void channelCtl.load();
    if (botEditable || canDeliver) void feishuCtl.load();
  };

  const title = target.name ?? feishuCtl.status?.bot_name ?? t("settings-feishubot.title");
  const description = target.description ?? t("settings-integrations.feishu_bot_description");
  const showInboxSection = !!larkcli && connectorHasInbox(larkcli) && (!projection ||
    projectedSections(projection, "inbox_and_sync", "connector").some(editableSection));
  const showExternalSources = !!channelId && (!projection ||
    projectedSections(projection, "external_sources", "channel").some(editableSection));

  const status = projection ? (
    <ProjectedStatus projection={projection} channel={channelCtl.detail} />
  ) : channelCtl.detail ? (
    <>
      <ChannelStateInline c={channelCtl.detail} />
      {channelCtl.detail.account?.name && <span className="truncate">{channelCtl.detail.account.name}</span>}
      <LastChannelEvent detail={channelCtl.detail} />
    </>
  ) : undefined;

  const slots: IntegrationDetailSlots = {
    connection: (!projection || serviceConnection || connectorId) ? (
      <>
        {connectorError && (
          <ConnectorReadError error={connectorError} onRetry={() => void loadLarkcli({ fresh: true })} loading={connectorLoading} />
        )}
        {botEditable ? (
          <FeishuBotConnectionPanel
            ctl={feishuCtl}
            larkcli={larkcli ?? null}
            onOpenConnector={(id) => onOpenConnector?.(id, manager)}
          />
        ) : (
          <>
            {serviceConnection && <Group><SettingsHint>{t("settings-integrations.projection_read_only")}</SettingsHint></Group>}
            {larkcli && (
              <ConnectorConnectionBlock
                connector={larkcli}
                reload={() => loadLarkcli({ fresh: true })}
                loading={connectorLoading}
                readOnly={!projectedSections(projection, "connection", "connector").some(editableSection)}
              />
            )}
          </>
        )}
      </>
    ) : undefined,

    channel: channelId ? (
      <>
        <ChannelDeliveryBlock ctl={channelCtl} target={target} targetOnly onSettings={onSettings} />
        {canDeliver && <FeishuBotDeliveryPanel ctl={feishuCtl} />}
      </>
    ) : undefined,

    inbox: (!projection || projectedSections(projection, "inbox_and_sync").length > 0) ? (
      <>
        {showInboxSection && larkcli && (
          <>
            <ConnectorInboxBlock connector={larkcli} reload={loadLarkcli} onSaved={onSaved} />
            <SubscriptionsEntry
              pick={(l) => l.connector === connectorId}
              scope={{ c: larkcli.id }}
              manager={manager}
              onOpen={onOpenSources ? (st) => onOpenSources(st, manager) : undefined}
            />
          </>
        )}
        <KnowledgeScopeShortcut manager={manager} onSettings={onSettings} />
      </>
    ) : undefined,

    // Mounted strictly after Inbox & Sync via IntegrationDetailShell (#17309 host alignment).
    externalSources: showExternalSources && channelId ? (
      <ConnectorExternalSourceSettings ids={[channelId]} manager={manager} />
    ) : undefined,
  };

  return (
    <IntegrationDetailShell
      id={target.id}
      kind="builtin"
      resourceKey={resourceKey}
      manager={manager}
      onBack={onBack}
      onSwitchAssistant={onSwitchAssistant}
      onRefresh={refreshAll}
      refreshDisabled={connectorLoading || channelCtl.loading || feishuCtl.loading || projection?.loading}
      mark={<BuiltinMark id={target.id} channel={channelCtl.detail} />}
      title={title}
      badge={originBadgeLabel(projection?.detail.item.origin, "builtin")}
      status={status}
      headerLoading={(channelCtl.loading || feishuCtl.loading) && !channelCtl.detail && !feishuCtl.status}
      headerBody={description ? <p className="text-meta leading-relaxed text-ink-2">{description}</p> : undefined}
      projection={projection}
      slots={slots}
    />
  );
}

// ── Built-in services, standalone connectors, and standalone channels ────────────────────

function BuiltinCapabilityDetail({
  resourceKey,
  target,
  projection,
  manager,
  onBack,
  onSwitchAssistant,
  onSettings,
  onOpenSources,
}: {
  resourceKey: string;
  target: Extract<IntegrationTarget, { kind: "builtin" }>;
  projection?: ProjectedDetail;
  manager?: string;
  onBack?: () => void;
  onSwitchAssistant?: () => void;
  onSettings?: (url: string, manager?: string) => void;
  onOpenSources?: (state?: string, manager?: string) => void;
}) {
  const service = !projection || projection.detail.item.ref.kind === "service";
  const isMail = service && target.id === "mail";
  const isLarkGateway = service && target.id === "lark";
  const channelId = projection ? target.channelId ?? null : resolveBuiltinChannelId(target);
  const connectorId = projection ? (isMail || isLarkGateway ? null : target.connectorId ?? null) : resolveBuiltinConnectorId(target);
  const serviceConnection = projectedSections(projection, "connection", "service")[0];
  const editService = !projection || (!!serviceConnection && editableSection(serviceConnection));
  const editConnector = !projection || projectedSections(projection, "connection", "connector").some(editableSection);

  useLayoutEffect(() => {
    const prefixes: string[] = [];
    // The item paths are more specific than the Settings category's assistant pins.
    if (isMail) prefixes.push("/mail/accounts", "/mail/oauth", "/connectors/mail");
    if (isLarkGateway) prefixes.push("/lark/status", "/lark/accounts");
    if (connectorId) {
      prefixes.push(`/connectors/${enc(connectorId)}`);
      if (connectorId === "imessage") prefixes.push("/imessage");
    }
    if (!prefixes.length) return undefined;
    return pinManagerAt(prefixes, manager);
  }, [isMail, isLarkGateway, connectorId, manager]);

  const channelCtl = projectedChannelController(useChannelController(channelId, manager), projection);

  const { connectors, loaded: connectorsLoaded, load: loadConnector, loading: connectorLoading, error: connectorReadError, onSaved } =
    useDetailConnectors(connectorId ? [connectorId] : [], manager, !!projection);
  const connector = connectors[0];
  const connectorError = connectorReadError || (connectorsLoaded && !connector ? t("settings-hubconnector.not_on_hub") : "");
  const [reloadKey, setReloadKey] = useState(0);
  const [mailHasAccounts, setMailHasAccounts] = useState(false);

  const refreshAll = () => {
    projection?.refresh();
    if (isMail || isLarkGateway) setReloadKey((key) => key + 1);
    void loadConnector({ fresh: true });
    if (channelId) void channelCtl.load();
  };

  const title =
    target.name ??
    (isMail
      ? t("settings-connectors.mail")
      : isLarkGateway
        ? t("settings-connectors.lark")
        : connector?.name ?? channelCtl.detail?.name ?? (target.id === "github" ? "GitHub" : undefined));

  const description =
    target.description ??
    (isMail
      ? t("settings-integrations.mail_description")
      : isLarkGateway
        ? t("settings-integrations.lark_gateway_description")
        : channelCtl.detail?.description);

  const subscriptionConnectorId = connectorId ?? (isMail ? "mail" : isLarkGateway ? "lark" : null);
  const subscriptions = countsOf(useSubscriptionSummary(manager), (l) => l.connector === subscriptionConnectorId);
  const isGitHub = (!projection || connectorId !== null) && isGitHubIntegration(target.id, connectorId ? [connectorId] : []);
  const showKnowledgeScope =
    isMail ||
    isLarkGateway ||
    connectorId === "larkcli" ||
    (connector?.capabilities?.includes("history") ?? false);
  const showInboxSection = (!projection || projectedSections(projection, "inbox_and_sync").length > 0) &&
    (showKnowledgeScope || (!!connector && connectorHasInbox(connector)) || !!subscriptions);
  const showConnection = (service && (!projection || serviceConnection) && (isMail || isLarkGateway)) ||
    connectorId || isGitHub || hasBinding(channelCtl.detail);
  const sourceChannel = channelCtl.detail?.installed && channelCtl.detail.kind !== "app" ? channelId : null;
  const externalSources = projection
    ? projectedSections(projection, "external_sources").filter(editableSection).map(({ ref }) => ref.id)
    : [connectorId ?? "", sourceChannel ?? ""].filter(Boolean);

  const status = projection ? (
    <ProjectedStatus projection={projection} channel={channelCtl.detail} />
  ) : connector ? (
    <>
      <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", CONN_DOT[connectorTone(connector)])} />
      <span className="truncate">
        {connectorState(connector)}
        {connector.account ? ` · ${connector.account}` : ""}
      </span>
      <LastChannelEvent detail={channelCtl.detail} />
    </>
  ) : channelCtl.detail ? (
    <>
      <ChannelStateInline c={channelCtl.detail} />
      {channelCtl.detail.account?.name && <span className="truncate">{channelCtl.detail.account.name}</span>}
      <LastChannelEvent detail={channelCtl.detail} />
    </>
  ) : undefined;

  const badge = projection
    ? originBadgeLabel(projection.detail.item.origin, "builtin")
    : originBadgeLabel(undefined, "builtin");

  const slots: IntegrationDetailSlots = {
    connection: showConnection ? (
      <>
        {isMail && editService && <Mail hideInboxSwitch manager={manager} reloadKey={reloadKey} onHasAccounts={setMailHasAccounts} />}
        {isLarkGateway && editService && <Feishu reloadKey={reloadKey} manager={manager} />}
        {serviceConnection && !editService && <Group><SettingsHint>{t("settings-integrations.projection_read_only")}</SettingsHint></Group>}
        {connectorId && (
          <>
            {connectorError && (
              <ConnectorReadError error={connectorError} onRetry={() => void loadConnector({ fresh: true })} loading={connectorLoading} />
            )}
            {connector && (
              <ConnectorConnectionBlock
                connector={connector}
                reload={() => loadConnector({ fresh: true })}
                loading={connectorLoading}
                readOnly={!editConnector}
              />
            )}
            {!connector && !connectorError && <Group><div className="px-4 py-3"><Loading /></div></Group>}
          </>
        )}
        {channelId && <ChannelBindingBlock ctl={channelCtl} />}
        {isGitHub && <SavedRepositoriesShortcut manager={manager} onSettings={onSettings} />}
      </>
    ) : undefined,

    channel: channelId ? (
      <ChannelDeliveryBlock ctl={channelCtl} target={target} onSettings={onSettings} />
    ) : undefined,

    inbox: showInboxSection ? (
      <>
        {isMail && editService && mailHasAccounts && <MailInboxSwitch key={reloadKey} manager={manager} grouped />}
        {connector && connectorHasInbox(connector) && (!projection || projectedSections(projection, "inbox_and_sync", "connector").some(editableSection)) && (
          <ConnectorInboxBlock connector={connector} reload={loadConnector} onSaved={onSaved} />
        )}
        {subscriptionConnectorId && subscriptions && (
          <IntegrationSubscriptions
            counts={subscriptions}
            scope={{ c: subscriptionConnectorId }}
            onOpen={onOpenSources ? (st) => onOpenSources(st, manager) : undefined}
          />
        )}
        {showKnowledgeScope && <KnowledgeScopeShortcut manager={manager} onSettings={onSettings} />}
      </>
    ) : undefined,

    externalSources: !isMail && !isLarkGateway && externalSources.length > 0 ? (
      <ConnectorExternalSourceSettings ids={externalSources} manager={manager} />
    ) : undefined,
  };

  return (
    <IntegrationDetailShell
      id={target.id}
      kind="builtin"
      resourceKey={resourceKey}
      manager={manager}
      onBack={onBack}
      onSwitchAssistant={onSwitchAssistant}
      onRefresh={refreshAll}
      refreshDisabled={connectorLoading || channelCtl.loading || projection?.loading}
      mark={<BuiltinMark id={target.id} channel={channelCtl.detail} />}
      title={title}
      badge={badge}
      status={status}
      headerLoading={(connectorLoading || channelCtl.loading) && !connector && !channelCtl.detail}
      headerBody={description ? <p className="text-meta leading-relaxed text-ink-2">{description}</p> : undefined}
      projection={projection}
      slots={slots}
    />
  );
}

// ── Extensions (built-in & installed, including composite connector + channel + config) ──

type ExtensionCapabilityProps = {
  resourceKey: string;
  target: Extract<IntegrationTarget, { kind: "extension" }>;
  projection?: ProjectedDetail;
  manager?: string;
  prefill?: JsonObject;
  onBack?: () => void;
  onSwitchAssistant?: () => void;
  onUninstalled?: () => void;
  onPage?: (extensionId: string, uiId: string, manager?: string) => void;
  onUpgrade?: (market: string, id: string, manager?: string) => void;
  onSettings?: (url: string, manager?: string) => void;
  onOpenSources?: (state?: string, manager?: string) => void;
};

function ExtensionCapabilityDetail(props: ExtensionCapabilityProps) {
  const binding = useExtensionBinding(props.target.id, props.manager);
  return <ExtensionCapabilityResource key={binding.client.key} {...props} binding={binding} />;
}

function ExtensionCapabilityResource({
  resourceKey,
  binding,
  target,
  projection,
  manager,
  prefill,
  onBack: back,
  onSwitchAssistant,
  onUninstalled,
  onPage,
  onUpgrade,
  onSettings,
  onOpenSources,
}: ExtensionCapabilityProps & { binding: ExtensionBinding }) {
  const id = target.id;

  const roster = useRoster();
  const offline = roster?.managers.find((m) => m.id === binding.client.managerId)?.online === false;
  const {
    detail, error, loading, draft, typed, fieldErrors, checkFailed, saving, toggling, saveBarLabel, editable,
    canSave, problems, load, reload, authChanged, connectorRefreshKey, mutate, remove: exit, edit, type, revert, save, saveNow,
  } = useExtensionConfiguration(id, binding, prefill, onUninstalled ?? back);
  const [secret, setSecret] = useState<ExtensionSecret | null>(null);
  const [removing, setRemoving] = useState(false);
  const [dependents, setDependents] = useState<Dependent[] | null>(null);
  const [packOff, setPackOff] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [startSignIn] = useState(() => signInAsked(id));
  useEffect(() => void takeSignIn(id), [id]);

  const projectedChannel = projectedSections(projection, "conversation_and_push", "channel")[0];
  const channelId = projection ? projectedChannel?.ref.id ?? null : target.channelId ?? (detail ? channelOf(detail) : null);
  const channelCtl = projectedChannelController(useChannelController(channelId, manager), projection);

  const projectedDetail = projection?.detail;
  const projectedConnections = useMemo(() => projectedSections(projection, "connection", "connector"), [projection]);
  const connectorIds = useMemo(() => {
    if (projectedDetail) return projectedConnections.map(({ ref }) => ref.id);
    const ids = new Set<string>();
    if (target.connectorId) ids.add(target.connectorId);
    for (const cid of detail?.contributes?.connectors ?? []) {
      if (cid.trim()) ids.add(cid);
    }
    return [...ids];
  }, [projectedConnections, projectedDetail, target.connectorId, detail?.contributes?.connectors]);
  const { connectors, loading: connectorsLoading, error: connectorError, load: loadConnectors, onSaved } = useDetailConnectors(connectorIds, manager, !!projection);
  const lastConnectorRefresh = useRef(connectorRefreshKey);
  useEffect(() => {
    if (lastConnectorRefresh.current === connectorRefreshKey) return;
    lastConnectorRefresh.current = connectorRefreshKey;
    void loadConnectors({ fresh: true });
  }, [connectorRefreshKey, loadConnectors]);

  useLayoutEffect(() => {
    const ids = new Set([...connectorIds, ...connectors.map((c) => c.id)]);
    const prefixes = [...ids].map((cid) => `/connectors/${enc(cid)}`);
    if (ids.has("imessage")) prefixes.push("/imessage");
    return pinManagerAt(prefixes, manager);
  }, [manager, connectorIds, connectors]);

  const allFields = detail?.form?.fields;
  const editableGroups = useMemo(() => new Set(projectedDetail?.sections
    .filter((section) => section.ref.kind === "extension" && section.ref.id === id && editableSection(section))
    .flatMap((section) => section.configuration_groups ?? [])), [projectedDetail, id]);
  const sections = useMemo(() => {
    const parts = partitionExtensionFields(allFields);
    if (projectedDetail) {
      for (const group of Object.keys(parts) as (keyof typeof parts)[]) {
        if (!editableGroups.has(group)) parts[group] = [];
      }
    }
    return parts;
  }, [allFields, projectedDetail, editableGroups]);

  const saveBar = useRef<HTMLDivElement>(null);
  const showSaveBar = !!saveBarLabel;
  useLayoutEffect(() => {
    const field = document.activeElement;
    const bar = saveBar.current;
    if (!bar || !(field instanceof HTMLElement) || !bar.parentElement?.contains(field)) return;
    const bounds = field.getBoundingClientRect();
    const footer = bar.getBoundingClientRect();
    if (bounds.bottom > footer.top && bounds.top < footer.bottom) field.scrollIntoView({ block: "center" });
  }, [showSaveBar]);

  // Auto-open the Advanced disclosure when a validation error targets a field inside it.
  const advancedError = useMemo(
    () => fieldsHaveError(sections.advanced, fieldErrors, problems),
    [sections.advanced, fieldErrors, problems],
  );
  useEffect(() => {
    if (advancedError) setAdvancedOpen(true);
  }, [advancedError]);

  const toggle = async (on: boolean, cascade = false) => {
    try {
      const next = await mutate("management", (client) => setExtensionEnabled(id, on, cascade, client));
      setDependents(null);
      setPackOff(false);
      toast(t(on ? "settings-extensions.enabled_name" : "settings-extensions.disabled_name", { name: next.name }), "ok");
    } catch (error) {
      if (isAbort(error)) return;
      const deps = dependentError(error);
      setPackOff(false);
      if (!on && deps) setDependents(deps);
      else toast(t(on ? "settings-extensions.enable_failed" : "settings-extensions.disable_failed", { error: errorText(error) }), "error");
    }
  };

  const toggleAutoUpgrade = async (on: boolean) => {
    try {
      await mutate("management", (client) => setExtensionAutoUpgrade(id, on, client));
      toast(t(on ? "settings-extensions.auto_upgrade_on" : "settings-extensions.auto_upgrade_off"), "ok");
    } catch (error) {
      if (!isAbort(error)) toast(t(on ? "settings-extensions.auto_upgrade_on_failed" : "settings-extensions.auto_upgrade_off_failed", { error: errorText(error) }), "error");
    }
  };

  const toggleContribution = async (c: Contribution, on: boolean) => {
    try {
      await mutate("management", (client) => setContribution(id, c.point, c.id, on, client));
      toast(t(on ? "settings-extensions.contribution_on" : "settings-extensions.contribution_off"), "ok");
    } catch (error) {
      if (!isAbort(error)) toast(t("settings-extensions.change_failed", { error: errorText(error) }), "error");
    }
  };

  const computers = !!detail?.contributes?.computer;
  const nodes = detail?.node_machines ? { field: detail.node_machines, states: detail.nodes ?? [] } : undefined;
  const launchers = useFormLaunchers(hasLauncherField(detail?.form?.fields), detail?.id);
  const memberNames = useInstalledNames(!!detail?.extension_pack?.length);

  const sharedFieldProps: Omit<FieldProps, "field" | "path"> = {
    draft,
    typed,
    errors: fieldErrors,
    problems,
    onEdit: edit,
    onType: type,
    onSaveNow: saveNow,
    computers,
    nodes,
    launchers,
    secrets: detail?.secrets,
    onSecret: setSecret,
  };

  const p = detail;
  const lifecycle = projectedSections(projection, "lifecycle", "extension");
  const editableLifecycle = editable && (!projection || lifecycle.some(editableSection));
  const editableConfig = editable && (!projection || editableGroups.size > 0);
  const canConfigureSecrets = editable && (!projection || editableGroups.has("general"));
  const configurationUnavailable = projectedDetail?.sections.some((section) => section.kind === "extension_configuration" &&
    section.availability.reason === "read_failed");
  const setupTarget = canConfigureSecrets ? setupSecret(p?.needs_setup, p?.secrets) : undefined;
  const topSecrets = canConfigureSecrets ? (p?.secrets ?? []).filter((s) => !s.list) : [];
  const uiPages = p && (!projection || lifecycle.length > 0) ? shownPages(p.ui, p.auth) : [];
  const showAuth = !!p?.auth && (!projection || editableLifecycle || editable && editableGroups.has("connection"));
  const projectedInbox = projectedSections(projection, "inbox_and_sync");

  const isGitHub = isGitHubIntegration(id, connectorIds, p?.contributes?.repository_providers);
  const showKnowledgeScope = (!projection || projectedInbox.length > 0) &&
    (connectors.some((c) => c.capabilities?.includes("history")) || id === "larkx");

  const hasConnectionSection =
    !!p && (
      showAuth ||
      connectors.length > 0 ||
      !!connectorError ||
      hasBinding(channelCtl.detail) ||
      isGitHub ||
      (editable && sections.connection.length > 0)
    );
  const hasChannelSection = !!p && (!!channelId || (editable && sections.channel.length > 0));
  const inboxConnectors = connectors.filter((connector) => connectorHasInbox(connector) && (!projection ||
    projectedInbox.some((section) => section.ref.kind === "connector" && section.ref.id === connector.id && editableSection(section))));
  const subscriptions = countsOf(useSubscriptionSummary(manager), (l) => l.extension === id);
  const showSubscriptions = (p?.enabled ?? true) && !!subscriptions && (projection
    ? projectedInbox.some(({ ref }) => ref.kind === "connector" || ref.kind === "subscription")
    : !!p?.contributes?.connectors?.length);
  const hasInboxSection =
    !!p && (
      inboxConnectors.length > 0 ||
      showKnowledgeScope ||
      showSubscriptions ||
      (editable && sections.inbox.length > 0)
    );
  const hasAnyConfigElsewhere =
    sections.connection.length > 0 || sections.channel.length > 0 || sections.inbox.length > 0 ||
    !!p?.auth || connectors.length > 0 || !!channelId;
  const noConfiguration =
    !!p && editable && !configurationUnavailable && !allFields?.length && !topSecrets.length && !uiPages.length && !hasAnyConfigElsewhere;

  const externalSources = projection
    ? projectedSections(projection, "external_sources").filter(editableSection).map(({ ref }) => ref.id)
    : [...connectorIds, ...(p?.contributes?.channel ?? []), channelId ?? ""];
  const infoAccess = projectedSections(projection, "information_access", "extension")[0];
  const showInfoAccess = !!p && (!projection || !!infoAccess);
  const showLifecycle = !!p && (!projection || lifecycle.length > 0);
  const name = projection?.detail.item.name ?? (p?.name || target.name);

  const market = p ? (marketOf(p.origin) || p.upgrade?.market || "") : "";
  const allowLifecycle = !projection || editableLifecycle;
  const marketManaged = allowLifecycle && !!p && p.source === "installed" && !p.attached && !p.read_only && !!market;
  const uninstallable = allowLifecycle && !!p && p.source === "installed" && !p.attached && !p.read_only && !!marketOf(p.origin);
  const automatic = allowLifecycle && !!p && p.source === "installed" && !p.attached && !p.read_only && p.auto_upgrade !== undefined;
  const marketRemoved = p?.auto_upgrade_paused === "market_removed";
  const noUpdates = !p
    ? ""
    : offline
      ? t("settings-extensions.manager_offline")
      : marketRemoved
        ? t("settings-extensions.market_removed_check")
        : marketManaged && !automatic
          ? t("settings-extensions.action_unknown")
          : lifecycleReason(p, false);
  const noUninstall = !p ? "" : offline ? t("settings-extensions.manager_offline") : lifecycleReason(p, true);
  const blocked = toggling || saving || offline;

  const refreshAll = () => {
    projection?.refresh();
    load();
    if (channelId) void channelCtl.load();
    void loadConnectors({ fresh: true });
  };

  const status = projection ? (
    <ProjectedStatus projection={projection} channel={channelCtl.detail} />
  ) : (
    <>
      <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", EXT_DOT[extensionTone(p)])} />
      <span className="truncate">
        {p ? stateLine(p) : loading ? t("settings-extensions.loading") : t("settings-extensions.unavailable")}
      </span>
      <LastChannelEvent detail={channelCtl.detail} />
    </>
  );

  const hasHeaderBody = Boolean(p?.description || (p && runNote(p)) || p?.needs_setup?.length || p?.error || error);
  const headerBody = hasHeaderBody ? (
    <>
      {p?.description && <p className="text-meta leading-relaxed text-ink-2">{p.description}</p>}
      {p && runNote(p) && <p className="text-submeta text-ink-3">{runNote(p)}</p>}
      {!!p?.needs_setup?.length && (setupTarget ? (
        <button
          type="button"
          onClick={() => setSecret(setupTarget)}
          className="touch-hit flex items-center gap-1 text-left text-label text-orange hover:underline"
          data-extension-needs-setup
        >
          {t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) })}
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0" style={{ transform: "rotate(-90deg)" }} />
        </button>
      ) : (
        <p className="text-label text-orange" data-extension-needs-setup>
          {t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) })}
        </p>
      ))}
      {p?.error && <p className="text-label break-words text-red select-text">{p.error}</p>}
      {error && <p className="text-label break-words text-red select-text">{t("settings-extensions.detail_error", { error })}</p>}
    </>
  ) : undefined;

  const banner = (
    <>
      {!p && !error && (
        <Group><div className="px-4 py-3"><Loading /></div></Group>
      )}
      {p && fieldErrors[""] && (
        <p role="alert" className="mb-4 rounded-control bg-red-tint px-3 py-2 text-label break-words text-red">
          {t("settings-extensions.not_saved", { error: fieldErrors[""] })}
        </p>
      )}
    </>
  );

  const slots: IntegrationDetailSlots = p ? {
    connection: hasConnectionSection ? (
      <>
        {showAuth && p.auth && (
          <AuthGroup id={p.id} auth={p.auth} enabled={p.enabled ?? true} start={startSignIn} onChange={authChanged} />
        )}
        {connectorError && (
          <ConnectorReadError error={connectorError} onRetry={() => void loadConnectors({ fresh: true })} loading={connectorsLoading} />
        )}
        {connectors.map((c) => (
          <ConnectorConnectionBlock
            key={`${c.manager ?? ""}:${c.id}`}
            connector={c}
            reload={() => loadConnectors({ fresh: true })}
            loading={connectorsLoading}
            authManagedByHost={showAuth}
            readOnly={!!projection && !projectedConnections.some((section) => section.ref.id === c.id && editableSection(section))}
          />
        ))}
        {channelId && <ChannelBindingBlock ctl={channelCtl} />}
        {editable && sections.connection.length > 0 && (
          <SectionFieldGroups
            fields={sections.connection}
            title={t("settings-integrations.connection_fields_title")}
            footer={saveFooter(sections.connection)}
            fieldProps={sharedFieldProps}
          />
        )}
        {isGitHub && <SavedRepositoriesShortcut manager={manager} onSettings={onSettings} />}
      </>
    ) : undefined,

    channel: hasChannelSection ? (
      <>
        {channelId && <ChannelDeliveryBlock ctl={channelCtl} target={target} onSettings={onSettings} />}
        {editable && sections.channel.length > 0 && (
          <SectionFieldGroups
            fields={sections.channel}
            title={t("settings-integrations.channel_fields_title")}
            footer={saveFooter(sections.channel)}
            fieldProps={sharedFieldProps}
          />
        )}
      </>
    ) : undefined,

    inbox: hasInboxSection ? (
      <>
        {inboxConnectors.map((c) => (
          <ConnectorInboxBlock
            key={`${c.manager ?? ""}:${c.id}`}
            connector={c}
            reload={loadConnectors}
            onSaved={onSaved}
          />
        ))}
        {showSubscriptions && (
          <IntegrationSubscriptions
            counts={subscriptions}
            scope={{ ext: p.id }}
            onOpen={onOpenSources ? (st) => onOpenSources(st, manager) : undefined}
          />
        )}
        {editable && sections.inbox.length > 0 && (
          <SectionFieldGroups
            fields={sections.inbox}
            title={t("settings-integrations.inbox_fields_title")}
            footer={saveFooter(sections.inbox)}
            fieldProps={sharedFieldProps}
          />
        )}
        {showKnowledgeScope && <KnowledgeScopeShortcut manager={manager} onSettings={onSettings} />}
      </>
    ) : undefined,

    externalSources: externalSources.length > 0 ? (
      <ConnectorExternalSourceSettings ids={externalSources} manager={manager} />
    ) : undefined,

    extensionSettings: (
      <>
        {!editable && (
          <Group>
            <Row label={t(p.read_only || p.source === "hub.json" ? "settings-extensions.read_only" : "settings-extensions.externally_managed")} wrapHint />
          </Group>
        )}
        {noConfiguration && (
          <Group>
            <Row label={t("settings-extensions.no_configuration")} />
          </Group>
        )}
        {uiPages.length > 0 && onPage && (
          <Group title={t("settings-extensions.pages_title")} footer={t("settings-extensions.pages_footer")}>
            {uiPages.map((u) => (
              <Row
                key={u.id}
                label={
                  <span className="flex items-center gap-2">
                    <Icon name="layers" size={13} className="shrink-0 text-ink-3" />
                    {u.title || u.id}
                  </span>
                }
                onClick={() => onPage(p.id, u.id, manager)}
              />
            ))}
          </Group>
        )}
        {editable && sections.general.length > 0 && (
          <SectionFieldGroups
            fields={sections.general}
            title={t("settings-extensions.fields_title")}
            footer={saveFooter(sections.general)}
            fieldProps={sharedFieldProps}
          />
        )}
        {editable && sections.advanced.length > 0 && (
          <div className="mb-6" data-integration-advanced>
            <Disclosure
              title={t("settings-integrations.advanced_title")}
              badge={t("settings-integrations.advanced_count", { count: sections.advanced.length })}
              open={advancedOpen}
              onToggle={() => setAdvancedOpen((v) => !v)}
            >
              <SectionFieldGroups
                fields={sections.advanced}
                footer={saveFooter(sections.advanced)}
                fieldProps={sharedFieldProps}
              />
            </Disclosure>
          </div>
        )}
        {canConfigureSecrets && topSecrets.length > 0 && (
          <Group title={t("settings-extensions.secrets_title")} footer={t("settings-extensions.secrets_footer")}>
            {topSecrets.map((s) => (
              <Row
                key={s.name}
                label={
                  <span className="flex items-center gap-2">
                    <Icon name="key" size={13} className="shrink-0 text-ink-3" />
                    {s.title}
                  </span>
                }
                hint={s.reference ? s.reference.title || s.reference.reference : s.description}
                onClick={() => setSecret(s)}
              >
                <span className={cx(!s.set && s.required && "text-orange")}>{secretState(s)}</span>
              </Row>
            ))}
          </Group>
        )}
      </>
    ),

    informationAccess: showInfoAccess ? (
      <ExtensionInfoAccess
        client={binding.client}
        extension={id}
        editable={editable && !offline && (!infoAccess || editableSection(infoAccess))}
      />
    ) : undefined,

    lifecycle: showLifecycle ? (
      <>
        <Group footer={offline ? t("settings-extensions.manager_offline") : editableLifecycle ? enableFooter(p, memberNames) : t(p.read_only ? "settings-extensions.read_only" : "settings-extensions.externally_managed")}>
          <Row label={t("settings-extensions.enable")}>
            <Switch
              on={p.enabled ?? true}
              onChange={(on) => (!on && p.pack_members_follow && p.pack_on?.length ? setPackOff(true) : void toggle(on))}
              disabled={blocked || !editableLifecycle}
              label={t("settings-extensions.enable")}
            />
          </Row>
        </Group>

        <ExtensionUpdatesGroup
          d={p}
          manager={manager}
          busy={blocked}
          market={market}
          canCheck={marketManaged && !offline && !marketRemoved}
          automatic={automatic}
          reason={noUpdates}
          onToggle={(on) => void toggleAutoUpgrade(on)}
          onUpgrade={(m, extId) => onUpgrade?.(m, extId, manager)}
          onChecked={reload}
        />

        <ExtensionInformationGroup d={p} />

        <SecretProviderGroups
          extension={id}
          users={p.secret_users}
          rules={p.handoff_rules}
          editable={editableLifecycle}
          mutate={mutate}
        />
        {!!p.depends?.length && (
          <Group title={t("settings-extensions.depends_title")} footer={t("settings-extensions.depends_footer")}>
            {p.depends.map((d) => (
              <Row key={d.id} label={d.name || d.id} hint={d.version}>
                <span className={cx(d.state !== "ok" && "text-orange")}>{dependencyText(d)}</span>
              </Row>
            ))}
          </Group>
        )}
        {!!p.extension_pack?.length && (
          <div data-pack-members>
            <Group title={t("settings-extensions.pack_title")} footer={t("settings-extensions.pack_footer")}>
              {p.extension_pack.map((m) => <PackMemberRow key={m.id} member={m} name={memberNames[m.id]} mono />)}
            </Group>
          </div>
        )}
        {!!p.contributions?.length && (
          <Group title={t("settings-extensions.contributions_title")} footer={t("settings-extensions.contributions_footer")}>
            {p.contributions.map((c) => (
              <Row
                key={`${c.point}/${c.id}`}
                label={c.title || c.id}
                hint={`${pointText(c.point)} · ${c.id}${c.via === "connector" ? t("settings-extensions.via_connector") : c.via === "config" ? t("settings-extensions.via_config") : ""}`}
              >
                <Switch
                  label={c.title || c.id}
                  on={c.enabled}
                  disabled={!editableLifecycle || toggling || offline || !c.can_disable}
                  onChange={(on) => void toggleContribution(c, on)}
                />
              </Row>
            ))}
          </Group>
        )}
        {!p.contributions?.length && p.contributes && Object.keys(p.contributes).length > 0 && (
          <Group title={t("settings-extensions.provides_title")}>
            {Object.keys(p.contributes).sort().map((point) => (
              <Row key={point} label={pointText(point)} mono>
                <span className="truncate">{formatList(p.contributes?.[point] ?? [], NARROW_LIST)}</span>
              </Row>
            ))}
          </Group>
        )}
        {p.sandbox && <SandboxGroup s={p.sandbox} id={p.id} name={p.name} />}
        {!!p.permissions?.length && (
          <Group title={t("settings-extensions.permissions_title")} footer={t("settings-extensions.permissions_footer")}>
            {p.permissions.map((perm) => (
              <div key={perm.key} className="px-4 py-2.5">
                <div className="text-meta text-ink">{perm.title}</div>
                {perm.detail && <div className="mt-0.5 text-submeta leading-snug text-ink-3">{perm.detail}</div>}
              </div>
            ))}
          </Group>
        )}

        <Group title={t("settings-extensions.danger_title")} footer={uninstallable && !offline ? t("settings-extensions.market_installed_footer") : noUninstall}>
          <Row label={t("settings-extensions.uninstall")}>
            <Button tone="danger" icon="trash" className="touch-hit" disabled={blocked || !uninstallable} onClick={() => setRemoving(true)}>
              {t("settings-extensions.uninstall")}
            </Button>
          </Row>
        </Group>
      </>
    ) : undefined,
  } : {};

  const footer = p && showSaveBar ? (
    <div ref={saveBar} className="sticky bottom-0 z-10 flow-root bg-canvas [&>section]:mb-0">
      <Group anchor="integration-save-bar">
        {checkFailed && <SettingsHint alert>{t("settings-extensions.not_saved", { error: checkFailed })}</SettingsHint>}
        <Row label={saveBarLabel}>
          <Button className="min-h-11" tone="quiet" onClick={revert} disabled={saving}>{t("settings-extensions.revert")}</Button>
          <Button className="min-h-11" tone="accent" busy={saving} disabled={!canSave || !editableConfig || offline} onClick={() => void save()}>{t("common.save")}</Button>
        </Row>
      </Group>
    </div>
  ) : undefined;

  const dialogs = (
    <>
      {removing && p && (!projection || uninstallable) && (
        <UninstallDialog
          id={p.id}
          name={p.name}
          version={p.version}
          pack={p.extension_pack}
          client={projection ? binding.client : undefined}
          onClose={() => setRemoving(false)}
          onDone={exit}
        />
      )}
      {packOff && p && !dependents && (
        <Dialog
          open
          title={t("settings-extensions.disable_pack_title", { name: p.name })}
          onClose={() => !toggling && setPackOff(false)}
          footer={
            <>
              <Button tone="quiet" disabled={toggling} onClick={() => setPackOff(false)}>{t("common.cancel")}</Button>
              <Button tone="danger" busy={toggling} onClick={() => void toggle(false)}>{t("settings-extensions.disable_all")}</Button>
            </>
          }
        >
          <p className="text-ink-2">
            {t("settings-extensions.disable_pack_body", {
              names: (p.pack_on ?? []).map((m) => m.name || m.id).join(t("settings-extensionmarket.list_separator")),
            })}
          </p>
        </Dialog>
      )}
      {dependents && p && (
        <Dialog
          open
          title={t("settings-extensions.disable_dependents_title", { name: p.name })}
          onClose={() => !toggling && setDependents(null)}
          footer={
            <>
              <Button tone="quiet" disabled={toggling} onClick={() => setDependents(null)}>{t("common.cancel")}</Button>
              <Button tone="danger" busy={toggling} onClick={() => void toggle(false, true)}>{t("settings-extensions.disable_all")}</Button>
            </>
          }
        >
          <p className="text-ink-2">
            {t("settings-extensions.dependents_body", { names: formatList(dependents.map((d) => d.name), NARROW_LIST) })}
          </p>
        </Dialog>
      )}
      {secret && (
        <SecretDialog
          extension={id}
          secret={secret.list && secret.entry_title ? { ...secret, title: `${secret.entry_title} · ${secret.title}` } : secret}
          onClose={() => setSecret(null)}
          mutate={mutate}
        />
      )}
    </>
  );

  return (
    <IntegrationDetailShell
      id={id}
      kind="extension"
      resourceKey={resourceKey}
      manager={manager}
      onBack={back}
      onSwitchAssistant={onSwitchAssistant}
      onRefresh={refreshAll}
      refreshDisabled={loading || channelCtl.loading || connectorsLoading || projection?.loading}
      mark={
        <ExtensionLogo
          url={p?.icon_url}
          manager={manager ?? ""}
          iconSize={20}
          className={cx("flex size-11 shrink-0 items-center justify-center rounded-card", EXT_TILE[extensionTone(p)])}
        />
      }
      title={name}
      badge={originBadgeLabel(projection?.detail.item.origin, "extension", { builtinExtension: p?.source === "builtin" })}
      status={status}
      headerLoading={loading && !p}
      headerBody={headerBody}
      projection={projection}
      banner={banner}
      slots={slots}
      footer={footer}
      dialogs={dialogs}
    />
  );
}
